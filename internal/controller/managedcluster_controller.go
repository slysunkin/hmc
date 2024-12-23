// Copyright 2024
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	hcv2 "github.com/fluxcd/helm-controller/api/v2"
	fluxmeta "github.com/fluxcd/pkg/apis/meta"
	fluxconditions "github.com/fluxcd/pkg/runtime/conditions"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	hmc "github.com/Mirantis/hmc/api/v1alpha1"
	"github.com/Mirantis/hmc/internal/credspropagation"
	"github.com/Mirantis/hmc/internal/helm"
	"github.com/Mirantis/hmc/internal/sveltos"
	"github.com/Mirantis/hmc/internal/telemetry"
	"github.com/Mirantis/hmc/internal/utils/status"
)

const (
	DefaultRequeueInterval = 10 * time.Second
)

// ManagedClusterReconciler reconciles a ManagedCluster object
type ManagedClusterReconciler struct {
	client.Client
	Config          *rest.Config
	DynamicClient   *dynamic.DynamicClient
	SystemNamespace string
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ManagedClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	l := ctrl.LoggerFrom(ctx)
	l.Info("Reconciling ManagedCluster")

	managedCluster := &hmc.ManagedCluster{}
	if err := r.Get(ctx, req.NamespacedName, managedCluster); err != nil {
		if apierrors.IsNotFound(err) {
			l.Info("ManagedCluster not found, ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}

		l.Error(err, "Failed to get ManagedCluster")
		return ctrl.Result{}, err
	}

	if !managedCluster.DeletionTimestamp.IsZero() {
		l.Info("Deleting ManagedCluster")
		return r.Delete(ctx, managedCluster)
	}

	if managedCluster.Status.ObservedGeneration == 0 {
		mgmt := &hmc.Management{}
		mgmtRef := client.ObjectKey{Name: hmc.ManagementName}
		if err := r.Get(ctx, mgmtRef, mgmt); err != nil {
			l.Error(err, "Failed to get Management object")
			return ctrl.Result{}, err
		}
		if err := telemetry.TrackManagedClusterCreate(
			string(mgmt.UID), string(managedCluster.UID), managedCluster.Spec.Template, managedCluster.Spec.DryRun); err != nil {
			l.Error(err, "Failed to track ManagedCluster creation")
		}
	}

	return r.Update(ctx, managedCluster)
}

func (r *ManagedClusterReconciler) setStatusFromClusterStatus(
	ctx context.Context, managedCluster *hmc.ManagedCluster,
) (bool, error) {
	l := ctrl.LoggerFrom(ctx)

	resourceConditions, err := status.GetResourceConditions(ctx, managedCluster.Namespace, r.DynamicClient, schema.GroupVersionResource{
		Group:    "cluster.x-k8s.io",
		Version:  "v1beta1",
		Resource: "clusters",
	}, labels.SelectorFromSet(map[string]string{hmc.FluxHelmChartNameKey: managedCluster.Name}).String())
	if err != nil {
		notFoundErr := status.ResourceNotFoundError{}
		if errors.As(err, &notFoundErr) {
			l.Info(err.Error())
			return true, nil
		}
		return false, fmt.Errorf("failed to get conditions: %w", err)
	}

	allConditionsComplete := true
	for _, metaCondition := range resourceConditions.Conditions {
		if metaCondition.Status != "True" {
			allConditionsComplete = false
		}

		if metaCondition.Reason == "" && metaCondition.Status == "True" {
			metaCondition.Reason = "Succeeded"
		}
		apimeta.SetStatusCondition(managedCluster.GetConditions(), metaCondition)
	}

	return !allConditionsComplete, nil
}

func (r *ManagedClusterReconciler) Update(ctx context.Context, managedCluster *hmc.ManagedCluster) (result ctrl.Result, err error) {
	l := ctrl.LoggerFrom(ctx)

	if controllerutil.AddFinalizer(managedCluster, hmc.ManagedClusterFinalizer) {
		if err := r.Client.Update(ctx, managedCluster); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update managedCluster %s/%s: %w", managedCluster.Namespace, managedCluster.Name, err)
		}
		return ctrl.Result{}, nil
	}

	if len(managedCluster.Status.Conditions) == 0 {
		managedCluster.InitConditions()
	}

	template := &hmc.ClusterTemplate{}

	defer func() {
		err = errors.Join(err, r.updateStatus(ctx, managedCluster, template))
	}()

	templateRef := client.ObjectKey{Name: managedCluster.Spec.Template, Namespace: managedCluster.Namespace}
	if err := r.Get(ctx, templateRef, template); err != nil {
		l.Error(err, "Failed to get Template")
		errMsg := fmt.Sprintf("failed to get provided template: %s", err)
		if apierrors.IsNotFound(err) {
			errMsg = "provided template is not found"
		}
		apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
			Type:    hmc.TemplateReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  hmc.FailedReason,
			Message: errMsg,
		})
		return ctrl.Result{}, err
	}

	if !template.Status.Valid {
		errMsg := "provided template is not marked as valid"
		apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
			Type:    hmc.TemplateReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  hmc.FailedReason,
			Message: errMsg,
		})
		return ctrl.Result{}, errors.New(errMsg)
	}
	// template is ok, propagate data from it
	managedCluster.Status.KubernetesVersion = template.Status.KubernetesVersion

	apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
		Type:    hmc.TemplateReadyCondition,
		Status:  metav1.ConditionTrue,
		Reason:  hmc.SucceededReason,
		Message: "Template is valid",
	})

	source, err := r.getSource(ctx, template.Status.ChartRef)
	if err != nil {
		apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
			Type:    hmc.HelmChartReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  hmc.FailedReason,
			Message: fmt.Sprintf("failed to get helm chart source: %s", err),
		})
		return ctrl.Result{}, err
	}
	l.Info("Downloading Helm chart")
	hcChart, err := helm.DownloadChartFromArtifact(ctx, source.GetArtifact())
	if err != nil {
		apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
			Type:    hmc.HelmChartReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  hmc.FailedReason,
			Message: fmt.Sprintf("failed to download helm chart: %s", err),
		})
		return ctrl.Result{}, err
	}

	l.Info("Initializing Helm client")
	getter := helm.NewMemoryRESTClientGetter(r.Config, r.RESTMapper())
	actionConfig := new(action.Configuration)
	err = actionConfig.Init(getter, managedCluster.Namespace, "secret", l.Info)
	if err != nil {
		return ctrl.Result{}, err
	}

	l.Info("Validating Helm chart with provided values")
	if err := validateReleaseWithValues(ctx, actionConfig, managedCluster, hcChart); err != nil {
		apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
			Type:    hmc.HelmChartReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  hmc.FailedReason,
			Message: fmt.Sprintf("failed to validate template with provided configuration: %s", err),
		})
		return ctrl.Result{}, err
	}

	apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
		Type:    hmc.HelmChartReadyCondition,
		Status:  metav1.ConditionTrue,
		Reason:  hmc.SucceededReason,
		Message: "Helm chart is valid",
	})

	cred := &hmc.Credential{}
	err = r.Client.Get(ctx, client.ObjectKey{
		Name:      managedCluster.Spec.Credential,
		Namespace: managedCluster.Namespace,
	}, cred)
	if err != nil {
		apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
			Type:    hmc.CredentialReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  hmc.FailedReason,
			Message: fmt.Sprintf("Failed to get Credential: %s", err),
		})
		return ctrl.Result{}, err
	}

	if cred.Status.State != hmc.CredentialReady {
		apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
			Type:    hmc.CredentialReadyCondition,
			Status:  metav1.ConditionFalse,
			Reason:  hmc.FailedReason,
			Message: "Credential is not in Ready state",
		})
	}

	apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
		Type:    hmc.CredentialReadyCondition,
		Status:  metav1.ConditionTrue,
		Reason:  hmc.SucceededReason,
		Message: "Credential is Ready",
	})

	if !managedCluster.Spec.DryRun {
		helmValues, err := setIdentityHelmValues(managedCluster.Spec.Config, cred.Spec.IdentityRef)
		if err != nil {
			return ctrl.Result{},
				fmt.Errorf("error setting identity values: %s", err)
		}
		hr, _, err := helm.ReconcileHelmRelease(ctx, r.Client, managedCluster.Name, managedCluster.Namespace, helm.ReconcileHelmReleaseOpts{
			Values: helmValues,
			OwnerReference: &metav1.OwnerReference{
				APIVersion: hmc.GroupVersion.String(),
				Kind:       hmc.ManagedClusterKind,
				Name:       managedCluster.Name,
				UID:        managedCluster.UID,
			},
			ChartRef: template.Status.ChartRef,
		})
		if err != nil {
			apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
				Type:    hmc.HelmReleaseReadyCondition,
				Status:  metav1.ConditionFalse,
				Reason:  hmc.FailedReason,
				Message: err.Error(),
			})
			return ctrl.Result{}, err
		}

		hrReadyCondition := fluxconditions.Get(hr, fluxmeta.ReadyCondition)
		if hrReadyCondition != nil {
			apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
				Type:    hmc.HelmReleaseReadyCondition,
				Status:  hrReadyCondition.Status,
				Reason:  hrReadyCondition.Reason,
				Message: hrReadyCondition.Message,
			})
		}

		requeue, err := r.setStatusFromClusterStatus(ctx, managedCluster)
		if err != nil {
			if requeue {
				return ctrl.Result{RequeueAfter: DefaultRequeueInterval}, err
			}

			return ctrl.Result{}, err
		}

		if requeue {
			return ctrl.Result{RequeueAfter: DefaultRequeueInterval}, nil
		}

		if !fluxconditions.IsReady(hr) {
			return ctrl.Result{RequeueAfter: DefaultRequeueInterval}, nil
		}

		if err := r.reconcileCredentialPropagation(ctx, managedCluster); err != nil {
			l.Error(err, "failed to reconcile credentials propagation")
			return ctrl.Result{}, err
		}

		return r.updateServices(ctx, managedCluster)
	}

	return ctrl.Result{}, nil
}

// updateServices reconciles services provided in ManagedCluster.Spec.Services.
// TODO(https://github.com/Mirantis/hmc/issues/361): Set status to ManagedCluster object at appropriate places.
func (r *ManagedClusterReconciler) updateServices(ctx context.Context, mc *hmc.ManagedCluster) (ctrl.Result, error) {
	opts, err := helmChartOpts(ctx, r.Client, mc.Namespace, mc.Spec.Services)
	if err != nil {
		return ctrl.Result{}, err
	}

	if _, err := sveltos.ReconcileProfile(ctx, r.Client, mc.Namespace, mc.Name,
		sveltos.ReconcileProfileOpts{
			OwnerReference: &metav1.OwnerReference{
				APIVersion: hmc.GroupVersion.String(),
				Kind:       hmc.ManagedClusterKind,
				Name:       mc.Name,
				UID:        mc.UID,
			},
			LabelSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{
					hmc.FluxHelmChartNamespaceKey: mc.Namespace,
					hmc.FluxHelmChartNameKey:      mc.Name,
				},
			},
			HelmChartOpts:  opts,
			Priority:       mc.Spec.ServicesPriority,
			StopOnConflict: mc.Spec.StopOnConflict,
		}); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to reconcile Profile: %w", err)
	}

	// We don't technically need to requeue here, but doing so because golint fails with:
	// `(*ManagedClusterReconciler).updateServices` - result `res` is always `nil` (unparam)
	//
	// This will be automatically resolved once setting status is implemented (https://github.com/Mirantis/hmc/issues/361),
	// as it is likely that some execution path in the function will have to return with a requeue to fetch latest status.
	return ctrl.Result{RequeueAfter: DefaultRequeueInterval}, nil
}

func validateReleaseWithValues(ctx context.Context, actionConfig *action.Configuration, managedCluster *hmc.ManagedCluster, hcChart *chart.Chart) error {
	install := action.NewInstall(actionConfig)
	install.DryRun = true
	install.ReleaseName = managedCluster.Name
	install.Namespace = managedCluster.Namespace
	install.ClientOnly = true

	vals, err := managedCluster.HelmValues()
	if err != nil {
		return err
	}
	_, err = install.RunWithContext(ctx, hcChart, vals)
	if err != nil {
		return err
	}
	return nil
}

func (r *ManagedClusterReconciler) updateStatus(ctx context.Context, managedCluster *hmc.ManagedCluster, template *hmc.ClusterTemplate) error {
	managedCluster.Status.ObservedGeneration = managedCluster.Generation
	warnings := ""
	errs := ""
	for _, condition := range managedCluster.Status.Conditions {
		if condition.Type == hmc.ReadyCondition {
			continue
		}
		if condition.Status == metav1.ConditionUnknown {
			warnings += condition.Message + ". "
		}
		if condition.Status == metav1.ConditionFalse {
			errs += condition.Message + ". "
		}
	}
	condition := metav1.Condition{
		Type:    hmc.ReadyCondition,
		Status:  metav1.ConditionTrue,
		Reason:  hmc.SucceededReason,
		Message: "ManagedCluster is ready",
	}
	if warnings != "" {
		condition.Status = metav1.ConditionUnknown
		condition.Reason = hmc.ProgressingReason
		condition.Message = warnings
	}
	if errs != "" {
		condition.Status = metav1.ConditionFalse
		condition.Reason = hmc.FailedReason
		condition.Message = errs
	}
	apimeta.SetStatusCondition(managedCluster.GetConditions(), condition)

	err := r.setAvailableUpgrades(ctx, managedCluster, template)
	if err != nil {
		return errors.New("failed to set available upgrades")
	}
	if err := r.Status().Update(ctx, managedCluster); err != nil {
		return fmt.Errorf("failed to update status for managedCluster %s/%s: %w", managedCluster.Namespace, managedCluster.Name, err)
	}
	return nil
}

func (r *ManagedClusterReconciler) getSource(ctx context.Context, ref *hcv2.CrossNamespaceSourceReference) (sourcev1.Source, error) {
	if ref == nil {
		return nil, errors.New("helm chart source is not provided")
	}
	chartRef := client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}
	hc := sourcev1.HelmChart{}
	if err := r.Client.Get(ctx, chartRef, &hc); err != nil {
		return nil, err
	}
	return &hc, nil
}

func (r *ManagedClusterReconciler) Delete(ctx context.Context, managedCluster *hmc.ManagedCluster) (ctrl.Result, error) {
	l := ctrl.LoggerFrom(ctx)

	hr := &hcv2.HelmRelease{}
	err := r.Get(ctx, client.ObjectKey{
		Name:      managedCluster.Name,
		Namespace: managedCluster.Namespace,
	}, hr)
	if err != nil {
		if apierrors.IsNotFound(err) {
			l.Info("Removing Finalizer", "finalizer", hmc.ManagedClusterFinalizer)
			if controllerutil.RemoveFinalizer(managedCluster, hmc.ManagedClusterFinalizer) {
				if err := r.Client.Update(ctx, managedCluster); err != nil {
					return ctrl.Result{}, fmt.Errorf("failed to update managedCluster %s/%s: %w", managedCluster.Namespace, managedCluster.Name, err)
				}
			}
			l.Info("ManagedCluster deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	err = helm.DeleteHelmRelease(ctx, r.Client, managedCluster.Name, managedCluster.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Without explicitly deleting the Profile object, we run into a race condition
	// which prevents Sveltos objects from being removed from the management cluster.
	// It is detailed in https://github.com/projectsveltos/addon-controller/issues/732.
	// We may try to remove the explicit call to Delete once a fix for it has been merged.
	// TODO(https://github.com/Mirantis/hmc/issues/526).
	err = sveltos.DeleteProfile(ctx, r.Client, managedCluster.Namespace, managedCluster.Name)
	if err != nil {
		return ctrl.Result{}, err
	}

	err = r.releaseCluster(ctx, managedCluster.Namespace, managedCluster.Name, managedCluster.Spec.Template)
	if err != nil {
		return ctrl.Result{}, err
	}

	l.Info("HelmRelease still exists, retrying")
	return ctrl.Result{RequeueAfter: DefaultRequeueInterval}, nil
}

func (r *ManagedClusterReconciler) releaseCluster(ctx context.Context, namespace, name, templateName string) error {
	providers, err := r.getInfraProvidersNames(ctx, namespace, templateName)
	if err != nil {
		return err
	}

	var (
		gvkAWSCluster = schema.GroupVersionKind{
			Group:   "infrastructure.cluster.x-k8s.io",
			Version: "v1beta2",
			Kind:    "AWSCluster",
		}

		gvkAzureCluster = schema.GroupVersionKind{
			Group:   "infrastructure.cluster.x-k8s.io",
			Version: "v1beta1",
			Kind:    "AzureCluster",
		}

		gvkMachine = schema.GroupVersionKind{
			Group:   "cluster.x-k8s.io",
			Version: "v1beta1",
			Kind:    "Machine",
		}
	)

	providerGVKs := map[string]schema.GroupVersionKind{
		"aws":   gvkAWSCluster,
		"azure": gvkAzureCluster,
	}

	// Associate the provider with it's GVK
	for _, provider := range providers {
		gvk, ok := providerGVKs[provider]
		if !ok {
			continue
		}

		cluster, err := r.getCluster(ctx, namespace, name, gvk)
		if err != nil {
			if provider == "aws" && apierrors.IsNotFound(err) {
				return nil
			}

			return err
		}

		found, err := r.objectsAvailable(ctx, namespace, cluster.Name, gvkMachine)
		if err != nil {
			return err
		}

		if !found {
			return r.removeClusterFinalizer(ctx, cluster)
		}
	}

	return nil
}

func (r *ManagedClusterReconciler) getInfraProvidersNames(ctx context.Context, templateNamespace, templateName string) ([]string, error) {
	template := &hmc.ClusterTemplate{}
	templateRef := client.ObjectKey{Name: templateName, Namespace: templateNamespace}
	if err := r.Get(ctx, templateRef, template); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "Failed to get ClusterTemplate", "template namespace", templateNamespace, "template name", templateName)
		return nil, err
	}

	const infraPrefix = "infrastructure-"
	var (
		ips     = make([]string, 0, len(template.Status.Providers))
		lprefix = len(infraPrefix)
	)
	for _, v := range template.Status.Providers {
		if idx := strings.Index(v, infraPrefix); idx > -1 {
			ips = append(ips, v[idx+lprefix:])
		}
	}

	return ips[:len(ips):len(ips)], nil
}

func (r *ManagedClusterReconciler) getCluster(ctx context.Context, namespace, name string, gvk schema.GroupVersionKind) (*metav1.PartialObjectMetadata, error) {
	opts := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(map[string]string{hmc.FluxHelmChartNameKey: name}),
		Namespace:     namespace,
	}
	itemsList := &metav1.PartialObjectMetadataList{}
	itemsList.SetGroupVersionKind(gvk)
	if err := r.Client.List(ctx, itemsList, opts); err != nil {
		return nil, err
	}
	if len(itemsList.Items) == 0 {
		return nil, fmt.Errorf("%s with name %s was not found", gvk.Kind, name)
	}

	return &itemsList.Items[0], nil
}

func (r *ManagedClusterReconciler) removeClusterFinalizer(ctx context.Context, cluster *metav1.PartialObjectMetadata) error {
	originalCluster := *cluster
	if controllerutil.RemoveFinalizer(cluster, hmc.BlockingFinalizer) {
		ctrl.LoggerFrom(ctx).Info("Allow to stop cluster", "finalizer", hmc.BlockingFinalizer)
		if err := r.Client.Patch(ctx, cluster, client.MergeFrom(&originalCluster)); err != nil {
			return fmt.Errorf("failed to patch cluster %s/%s: %w", cluster.Namespace, cluster.Name, err)
		}
	}

	return nil
}

func (r *ManagedClusterReconciler) objectsAvailable(ctx context.Context, namespace, clusterName string, gvk schema.GroupVersionKind) (bool, error) {
	opts := &client.ListOptions{
		LabelSelector: labels.SelectorFromSet(map[string]string{hmc.ClusterNameLabelKey: clusterName}),
		Namespace:     namespace,
		Limit:         1,
	}
	itemsList := &metav1.PartialObjectMetadataList{}
	itemsList.SetGroupVersionKind(gvk)
	if err := r.Client.List(ctx, itemsList, opts); err != nil {
		return false, err
	}
	return len(itemsList.Items) != 0, nil
}

func (r *ManagedClusterReconciler) reconcileCredentialPropagation(ctx context.Context, managedCluster *hmc.ManagedCluster) error {
	l := ctrl.LoggerFrom(ctx)
	l.Info("Reconciling CCM credentials propagation")

	providers, err := r.getInfraProvidersNames(ctx, managedCluster.Namespace, managedCluster.Spec.Template)
	if err != nil {
		return fmt.Errorf("failed to get cluster providers for cluster %s/%s: %w", managedCluster.Namespace, managedCluster.Name, err)
	}

	kubeconfSecret := &corev1.Secret{}
	if err := r.Client.Get(ctx, client.ObjectKey{
		Name:      managedCluster.Name + "-kubeconfig",
		Namespace: managedCluster.Namespace,
	}, kubeconfSecret); err != nil {
		return fmt.Errorf("failed to get kubeconfig secret for cluster %s/%s: %w", managedCluster.Namespace, managedCluster.Name, err)
	}

	propnCfg := &credspropagation.PropagationCfg{
		Client:          r.Client,
		ManagedCluster:  managedCluster,
		KubeconfSecret:  kubeconfSecret,
		SystemNamespace: r.SystemNamespace,
	}

	for _, provider := range providers {
		switch provider {
		case "aws":
			l.Info("Skipping creds propagation for AWS")
		case "azure":
			l.Info("Azure creds propagation start")
			if err := credspropagation.PropagateAzureSecrets(ctx, propnCfg); err != nil {
				errMsg := fmt.Sprintf("failed to create Azure CCM credentials: %s", err)
				apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
					Type:    hmc.CredentialsPropagatedCondition,
					Status:  metav1.ConditionFalse,
					Reason:  hmc.FailedReason,
					Message: errMsg,
				})

				return errors.New(errMsg)
			}

			apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
				Type:    hmc.CredentialsPropagatedCondition,
				Status:  metav1.ConditionTrue,
				Reason:  hmc.SucceededReason,
				Message: "Azure CCM credentials created",
			})
		case "vsphere":
			l.Info("vSphere creds propagation start")
			if err := credspropagation.PropagateVSphereSecrets(ctx, propnCfg); err != nil {
				errMsg := fmt.Sprintf("failed to create vSphere CCM credentials: %s", err)
				apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
					Type:    hmc.CredentialsPropagatedCondition,
					Status:  metav1.ConditionFalse,
					Reason:  hmc.FailedReason,
					Message: errMsg,
				})
				return errors.New(errMsg)
			}

			apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
				Type:    hmc.CredentialsPropagatedCondition,
				Status:  metav1.ConditionTrue,
				Reason:  hmc.SucceededReason,
				Message: "vSphere CCM credentials created",
			})
		default:
			apimeta.SetStatusCondition(managedCluster.GetConditions(), metav1.Condition{
				Type:    hmc.CredentialsPropagatedCondition,
				Status:  metav1.ConditionFalse,
				Reason:  hmc.FailedReason,
				Message: "unsupported infrastructure provider " + provider,
			})
		}
	}

	l.Info("CCM credentials reconcile finished")

	return nil
}

func setIdentityHelmValues(values *apiextensionsv1.JSON, idRef *corev1.ObjectReference) (*apiextensionsv1.JSON, error) {
	var valuesJSON map[string]any
	err := json.Unmarshal(values.Raw, &valuesJSON)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling values: %s", err)
	}

	valuesJSON["clusterIdentity"] = idRef
	valuesRaw, err := json.Marshal(valuesJSON)
	if err != nil {
		return nil, fmt.Errorf("error marshalling values: %s", err)
	}

	return &apiextensionsv1.JSON{
		Raw: valuesRaw,
	}, nil
}

func (r *ManagedClusterReconciler) setAvailableUpgrades(ctx context.Context, managedCluster *hmc.ManagedCluster, template *hmc.ClusterTemplate) error {
	if template == nil {
		return nil
	}
	chains := &hmc.ClusterTemplateChainList{}
	err := r.List(ctx, chains,
		client.InNamespace(template.Namespace),
		client.MatchingFields{hmc.SupportedTemplateKey: template.GetName()},
	)
	if err != nil {
		return err
	}

	availableUpgradesMap := make(map[string]hmc.AvailableUpgrade)
	for _, chain := range chains.Items {
		for _, supportedTemplate := range chain.Spec.SupportedTemplates {
			if supportedTemplate.Name == template.Name {
				for _, availableUpgrade := range supportedTemplate.AvailableUpgrades {
					availableUpgradesMap[availableUpgrade.Name] = availableUpgrade
				}
			}
		}
	}
	availableUpgrades := make([]string, 0, len(availableUpgradesMap))
	for _, availableUpgrade := range availableUpgradesMap {
		availableUpgrades = append(availableUpgrades, availableUpgrade.Name)
	}

	managedCluster.Status.AvailableUpgrades = availableUpgrades
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ManagedClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&hmc.ManagedCluster{}).
		Watches(&hcv2.HelmRelease{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []ctrl.Request {
				managedCluster := hmc.ManagedCluster{}
				managedClusterRef := client.ObjectKey{
					Namespace: o.GetNamespace(),
					Name:      o.GetName(),
				}
				err := r.Client.Get(ctx, managedClusterRef, &managedCluster)
				if err != nil {
					return []ctrl.Request{}
				}
				return []reconcile.Request{
					{
						NamespacedName: managedClusterRef,
					},
				}
			}),
		).
		Watches(&hmc.ClusterTemplateChain{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []ctrl.Request {
				chain, ok := o.(*hmc.ClusterTemplateChain)
				if !ok {
					return nil
				}

				var req []ctrl.Request
				for _, template := range getTemplateNamesManagedByChain(chain) {
					managedClusters := &hmc.ManagedClusterList{}
					err := r.Client.List(ctx, managedClusters,
						client.InNamespace(chain.Namespace),
						client.MatchingFields{hmc.TemplateKey: template})
					if err != nil {
						return []ctrl.Request{}
					}
					for _, cluster := range managedClusters.Items {
						req = append(req, ctrl.Request{
							NamespacedName: client.ObjectKey{
								Namespace: cluster.Namespace,
								Name:      cluster.Name,
							},
						})
					}
				}
				return req
			}),
			builder.WithPredicates(predicate.Funcs{
				UpdateFunc:  func(event.UpdateEvent) bool { return false },
				GenericFunc: func(event.GenericEvent) bool { return false },
			}),
		).
		Complete(r)
}
