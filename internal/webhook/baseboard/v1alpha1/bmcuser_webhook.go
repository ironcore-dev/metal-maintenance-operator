// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	baseboardv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/baseboard/v1alpha1"
)

var bmcuserlog = logf.Log.WithName("bmcuser-resource")

// SetupBMCUserWebhookWithManager registers the webhook for BMCUser in the manager.
func SetupBMCUserWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &baseboardv1alpha1.BMCUser{}).
		WithValidator(&BMCUserValidator{}).
		Complete()
}

// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// +kubebuilder:webhook:path=/validate-baseboard-metal-ironcore-dev-v1alpha1-bmcuser,mutating=false,failurePolicy=fail,sideEffects=None,groups=baseboard.metal.ironcore.dev,resources=bmcusers,verbs=create;update,versions=v1alpha1,name=vbmcuser-v1alpha1.kb.io,admissionReviewVersions=v1

// BMCUserValidator validates BMCUser resources on create and update.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
// +kubebuilder:object:generate=false
type BMCUserValidator struct{}

// ValidateCreate implements admission.Validator so a webhook will be registered for the type BMCUser.
func (v *BMCUserValidator) ValidateCreate(_ context.Context, obj *baseboardv1alpha1.BMCUser) (admission.Warnings, error) {
	bmcuserlog.Info("Validation for BMCUser upon creation", "name", obj.GetName())
	return validateBMCUserTTLFields(obj, nil)
}

// ValidateUpdate implements admission.Validator so a webhook will be registered for the type BMCUser.
func (v *BMCUserValidator) ValidateUpdate(_ context.Context, oldObj, newObj *baseboardv1alpha1.BMCUser) (admission.Warnings, error) {
	bmcuserlog.Info("Validation for BMCUser upon update", "name", newObj.GetName())
	return validateBMCUserTTLFields(newObj, oldObj)
}

// ValidateDelete implements admission.Validator so a webhook will be registered for the type BMCUser.
func (v *BMCUserValidator) ValidateDelete(_ context.Context, _ *baseboardv1alpha1.BMCUser) (admission.Warnings, error) {
	return nil, nil
}

// validateBMCUserTTLFields enforces TTL field constraints:
//   - TTL and ExpiresAt are forbidden on OperatorAdmin users.
//   - ExpiresAt must be in the future at creation time.
//   - At update time, ExpiresAt must be in the future unless it was already in the
//     past (clearing or extending an expired window is allowed).
func validateBMCUserTTLFields(obj, old *baseboardv1alpha1.BMCUser) (admission.Warnings, error) {
	var errs field.ErrorList
	specPath := field.NewPath("spec")

	if obj.Spec.Type == baseboardv1alpha1.BMCUserTypeOperatorAdmin {
		if obj.Spec.TTL != nil {
			errs = append(errs, field.Forbidden(specPath.Child("ttl"),
				"TTL is not allowed for OperatorAdmin users"))
		}
		if obj.Spec.ExpiresAt != nil {
			errs = append(errs, field.Forbidden(specPath.Child("expiresAt"),
				"ExpiresAt is not allowed for OperatorAdmin users"))
		}
		if len(errs) > 0 {
			return nil, apierrors.NewInvalid(
				schema.GroupKind{Group: obj.GroupVersionKind().Group, Kind: obj.Kind},
				obj.GetName(), errs)
		}
		return nil, nil
	}

	if obj.Spec.ExpiresAt != nil {
		isCreate := old == nil
		oldExpiredAlready := old != nil && old.Spec.ExpiresAt != nil && !old.Spec.ExpiresAt.After(time.Now())

		if obj.Spec.ExpiresAt.Before(&metav1.Time{Time: timeNow()}) && (isCreate || !oldExpiredAlready) {
			errs = append(errs, field.Invalid(specPath.Child("expiresAt"), obj.Spec.ExpiresAt,
				fmt.Sprintf("ExpiresAt must be in the future (got %s)", obj.Spec.ExpiresAt)))
		}
	}

	if len(errs) > 0 {
		return nil, apierrors.NewInvalid(
			schema.GroupKind{Group: obj.GroupVersionKind().Group, Kind: obj.Kind},
			obj.GetName(), errs)
	}
	return nil, nil
}

// timeNow is a variable so tests can override the current time.
var timeNow = func() time.Time { return time.Now() }
