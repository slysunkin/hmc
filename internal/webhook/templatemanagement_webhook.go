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

package webhook

import (
	"context"
	"errors"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/Mirantis/hmc/api/v1alpha1"
)

var errTemplateManagementDeletionForbidden = errors.New("TemplateManagement deletion is forbidden")

type TemplateManagementValidator struct {
	client.Client
	SystemNamespace string
}

func (v *TemplateManagementValidator) SetupWebhookWithManager(mgr ctrl.Manager) error {
	v.Client = mgr.GetClient()
	return ctrl.NewWebhookManagedBy(mgr).
		For(&v1alpha1.TemplateManagement{}).
		WithValidator(v).
		WithDefaulter(v).
		Complete()
}

var (
	_ webhook.CustomValidator = &TemplateManagementValidator{}
	_ webhook.CustomDefaulter = &TemplateManagementValidator{}
)

// ValidateCreate implements webhook.Validator so a webhook will be registered for the type.
func (v *TemplateManagementValidator) ValidateCreate(ctx context.Context, _ runtime.Object) (admission.Warnings, error) {
	itemsList := &metav1.PartialObjectMetadataList{}
	itemsList.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind(v1alpha1.TemplateManagementKind))

	if err := v.List(ctx, itemsList, client.Limit(1)); err != nil {
		return nil, err
	}

	if len(itemsList.Items) > 0 {
		return nil, errors.New("TemplateManagement object already exists")
	}

	return nil, nil
}

// ValidateUpdate implements webhook.Validator so a webhook will be registered for the type.
func (*TemplateManagementValidator) ValidateUpdate(_ context.Context, _, _ runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

// ValidateDelete implements webhook.Validator so a webhook will be registered for the type.
func (v *TemplateManagementValidator) ValidateDelete(ctx context.Context, _ runtime.Object) (admission.Warnings, error) {
	partialList := &metav1.PartialObjectMetadataList{}
	partialList.SetGroupVersionKind(v1alpha1.GroupVersion.WithKind(v1alpha1.ManagementKind))

	if err := v.List(ctx, partialList, client.Limit(1)); err != nil {
		return nil, fmt.Errorf("failed to list Management objects: %w", err)
	}

	if len(partialList.Items) > 0 {
		mgmt := partialList.Items[0]
		if mgmt.DeletionTimestamp == nil {
			return nil, errTemplateManagementDeletionForbidden
		}
	}

	return nil, nil
}

// Default implements webhook.Defaulter so a webhook will be registered for the type.
func (*TemplateManagementValidator) Default(context.Context, runtime.Object) error {
	return nil
}
