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
	"time"

	helmcontrollerv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	sveltosv1beta1 "github.com/projectsveltos/addon-controller/api/v1beta1"
	"helm.sh/helm/v3/pkg/chart"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	hmc "github.com/Mirantis/hmc/api/v1alpha1"
	"github.com/Mirantis/hmc/internal/utils"
)

var _ = Describe("MultiClusterService Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			testNamespace           = utils.DefaultSystemNamespace
			serviceTemplateName     = "test-service-0-1-0"
			helmRepoName            = "test-helmrepo"
			helmChartName           = "test-helmchart"
			helmChartReleaseName    = "test-helmchart-release"
			helmChartVersion        = "0.1.0"
			helmChartURL            = "http://source-controller.hmc-system.svc.cluster.local./helmchart/hmc-system/test-chart/0.1.0.tar.gz"
			multiClusterServiceName = "test-multiclusterservice"
		)

		fakeDownloadHelmChartFunc := func(context.Context, *sourcev1.Artifact) (*chart.Chart, error) {
			return &chart.Chart{
				Metadata: &chart.Metadata{
					APIVersion: "v2",
					Version:    helmChartVersion,
					Name:       helmChartName,
				},
			}, nil
		}

		ctx := context.Background()

		namespace := &corev1.Namespace{}
		helmChart := &sourcev1.HelmChart{}
		helmRepo := &sourcev1.HelmRepository{}
		serviceTemplate := &hmc.ServiceTemplate{}
		multiClusterService := &hmc.MultiClusterService{}
		clusterProfile := &sveltosv1beta1.ClusterProfile{}

		helmRepositoryRef := types.NamespacedName{Namespace: testNamespace, Name: helmRepoName}
		helmChartRef := types.NamespacedName{Namespace: testNamespace, Name: helmChartName}
		serviceTemplateRef := types.NamespacedName{Namespace: testNamespace, Name: serviceTemplateName}
		multiClusterServiceRef := types.NamespacedName{Name: multiClusterServiceName}
		clusterProfileRef := types.NamespacedName{Name: multiClusterServiceName}

		BeforeEach(func() {
			By("creating Namespace")
			err := k8sClient.Get(ctx, types.NamespacedName{Name: testNamespace}, namespace)
			if err != nil && apierrors.IsNotFound(err) {
				namespace = &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: testNamespace,
					},
				}
				Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
			}

			By("creating HelmRepository")
			err = k8sClient.Get(ctx, types.NamespacedName{Name: helmRepoName, Namespace: testNamespace}, helmRepo)
			if err != nil && apierrors.IsNotFound(err) {
				helmRepo = &sourcev1.HelmRepository{
					ObjectMeta: metav1.ObjectMeta{
						Name:      helmRepoName,
						Namespace: testNamespace,
					},
					Spec: sourcev1.HelmRepositorySpec{
						URL: "oci://test/helmrepo",
					},
				}
				Expect(k8sClient.Create(ctx, helmRepo)).To(Succeed())
			}

			By("creating HelmChart")
			err = k8sClient.Get(ctx, types.NamespacedName{Name: helmChartName, Namespace: testNamespace}, helmChart)
			if err != nil && apierrors.IsNotFound(err) {
				helmChart = &sourcev1.HelmChart{
					ObjectMeta: metav1.ObjectMeta{
						Name:      helmChartName,
						Namespace: testNamespace,
					},
					Spec: sourcev1.HelmChartSpec{
						SourceRef: sourcev1.LocalHelmChartSourceReference{
							Kind: sourcev1.HelmRepositoryKind,
							Name: helmRepoName,
						},
					},
				}
				Expect(k8sClient.Create(ctx, helmChart)).To(Succeed())
			}

			By("updating HelmChart status with artifact URL")
			helmChart.Status.URL = helmChartURL
			helmChart.Status.Artifact = &sourcev1.Artifact{
				URL:            helmChartURL,
				LastUpdateTime: metav1.Now(),
			}
			Expect(k8sClient.Status().Update(ctx, helmChart)).Should(Succeed())

			By("creating ServiceTemplate")
			err = k8sClient.Get(ctx, serviceTemplateRef, serviceTemplate)
			if err != nil && apierrors.IsNotFound(err) {
				serviceTemplate = &hmc.ServiceTemplate{
					ObjectMeta: metav1.ObjectMeta{
						Name:      serviceTemplateName,
						Namespace: testNamespace,
						Labels: map[string]string{
							hmc.HMCManagedLabelKey: "true",
						},
					},
					Spec: hmc.ServiceTemplateSpec{
						Helm: hmc.HelmSpec{
							ChartVersion: helmChartVersion,
							ChartRef: &helmcontrollerv2.CrossNamespaceSourceReference{
								Kind:      "HelmChart",
								Name:      helmChartName,
								Namespace: testNamespace,
							},
						},
					},
				}
			}
			Expect(k8sClient.Create(ctx, serviceTemplate)).To(Succeed())

			By("creating MultiClusterService")
			err = k8sClient.Get(ctx, multiClusterServiceRef, multiClusterService)
			if err != nil && apierrors.IsNotFound(err) {
				multiClusterService = &hmc.MultiClusterService{
					ObjectMeta: metav1.ObjectMeta{
						Name: multiClusterServiceName,
						Finalizers: []string{
							// Reconcile attempts to add this finalizer and returns immediately
							// if successful. So adding this finalizer here manually in order
							// to avoid having to call reconcile multiple times for this test.
							hmc.MultiClusterServiceFinalizer,
						},
					},
					Spec: hmc.MultiClusterServiceSpec{
						Services: []hmc.ServiceSpec{
							{
								Template: serviceTemplateName,
								Name:     helmChartReleaseName,
							},
						},
					},
				}
				Expect(k8sClient.Create(ctx, multiClusterService)).To(Succeed())
			}
		})

		AfterEach(func() {
			By("cleaning up")
			multiClusterServiceResource := &hmc.MultiClusterService{}
			Expect(k8sClient.Get(ctx, multiClusterServiceRef, multiClusterServiceResource)).NotTo(HaveOccurred())

			reconciler := &MultiClusterServiceReconciler{Client: k8sClient}
			Expect(k8sClient.Delete(ctx, multiClusterService)).To(Succeed())
			// Running reconcile to remove the finalizer and delete the MultiClusterService
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: multiClusterServiceRef})
			Expect(err).NotTo(HaveOccurred())
			Eventually(k8sClient.Get, 1*time.Minute, 5*time.Second).WithArguments(ctx, multiClusterServiceRef, multiClusterService).Should(HaveOccurred())

			Expect(k8sClient.Get(ctx, clusterProfileRef, &sveltosv1beta1.ClusterProfile{})).To(HaveOccurred())

			serviceTemplateResource := &hmc.ServiceTemplate{}
			Expect(k8sClient.Get(ctx, serviceTemplateRef, serviceTemplateResource)).NotTo(HaveOccurred())
			Expect(k8sClient.Delete(ctx, serviceTemplateResource)).To(Succeed())

			helmChartResource := &sourcev1.HelmChart{}
			Expect(k8sClient.Get(ctx, helmChartRef, helmChartResource)).NotTo(HaveOccurred())
			Expect(k8sClient.Delete(ctx, helmChartResource)).To(Succeed())

			helmRepositoryResource := &sourcev1.HelmRepository{}
			Expect(k8sClient.Get(ctx, helmRepositoryRef, helmRepositoryResource)).NotTo(HaveOccurred())
			Expect(k8sClient.Delete(ctx, helmRepositoryResource)).To(Succeed())
		})

		It("should successfully reconcile the resource", func() {
			By("reconciling ServiceTemplate used by MultiClusterService")
			templateReconciler := TemplateReconciler{
				Client:                k8sClient,
				downloadHelmChartFunc: fakeDownloadHelmChartFunc,
			}
			serviceTemplateReconciler := &ServiceTemplateReconciler{TemplateReconciler: templateReconciler}
			_, err := serviceTemplateReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: serviceTemplateRef})
			Expect(err).NotTo(HaveOccurred())

			By("reconciling MultiClusterService")
			multiClusterServiceReconciler := &MultiClusterServiceReconciler{Client: k8sClient}

			_, err = multiClusterServiceReconciler.Reconcile(ctx, reconcile.Request{NamespacedName: multiClusterServiceRef})
			Expect(err).NotTo(HaveOccurred())

			Eventually(k8sClient.Get, 1*time.Minute, 5*time.Second).WithArguments(ctx, clusterProfileRef, clusterProfile).ShouldNot(HaveOccurred())
		})
	})
})
