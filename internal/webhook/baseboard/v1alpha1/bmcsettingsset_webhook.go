// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	baseboardv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/baseboard/v1alpha1"
)

var bmcsettingssetlog = logf.Log.WithName("bmcsettingsset-resource")

// SetupBMCSettingsSetWebhookWithManager registers the webhook for BMCSettingsSet in the manager.
func SetupBMCSettingsSetWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &baseboardv1alpha1.BMCSettingsSet{}).
		WithValidator(&BMCSettingsSetValidator{Client: mgr.GetAPIReader()}).
		Complete()
}

// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// +kubebuilder:webhook:path=/validate-baseboard-metal-ironcore-dev-v1alpha1-bmcsettingsset,mutating=false,failurePolicy=fail,sideEffects=None,groups=baseboard.metal.ironcore.dev,resources=bmcsettingssets,verbs=create;update,versions=v1alpha1,name=vbmcsettingsset-v1alpha1.kb.io,admissionReviewVersions=v1

// BMCSettingsSetValidator struct is responsible for validating the BMCSettingsSet resource
// when it is created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type BMCSettingsSetValidator struct {
	Client client.Reader
}

// ValidateCreate implements admission.Validator so a webhook will be registered for the type BMCSettingsSet.
func (v *BMCSettingsSetValidator) ValidateCreate(_ context.Context, obj *baseboardv1alpha1.BMCSettingsSet) (admission.Warnings, error) {
	bmcsettingssetlog.Info("Validation for BMCSettingsSet upon creation", "name", obj.GetName())
	fp := field.NewPath("spec", "bmcSettingsTemplate", "variables")
	if errs := validateVariableFieldPaths(obj.Spec.BMCSettingsTemplate.Variables, fp); len(errs) > 0 {
		return nil, apierrors.NewInvalid(
			schema.GroupKind{Group: obj.GroupVersionKind().Group, Kind: obj.Kind},
			obj.GetName(), errs)
	}
	return nil, nil
}

// ValidateUpdate implements admission.Validator so a webhook will be registered for the type BMCSettingsSet.
func (v *BMCSettingsSetValidator) ValidateUpdate(_ context.Context, _, newObj *baseboardv1alpha1.BMCSettingsSet) (admission.Warnings, error) {
	bmcsettingssetlog.Info("Validation for BMCSettingsSet upon update", "name", newObj.GetName())
	fp := field.NewPath("spec", "bmcSettingsTemplate", "variables")
	if errs := validateVariableFieldPaths(newObj.Spec.BMCSettingsTemplate.Variables, fp); len(errs) > 0 {
		return nil, apierrors.NewInvalid(
			schema.GroupKind{Group: newObj.GroupVersionKind().Group, Kind: newObj.Kind},
			newObj.GetName(), errs)
	}
	return nil, nil
}

// ValidateDelete implements admission.Validator so a webhook will be registered for the type BMCSettingsSet.
func (v *BMCSettingsSetValidator) ValidateDelete(_ context.Context, _ *baseboardv1alpha1.BMCSettingsSet) (admission.Warnings, error) {
	return nil, nil
}
