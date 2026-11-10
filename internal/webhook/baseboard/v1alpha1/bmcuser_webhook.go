// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	baseboardv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/baseboard/v1alpha1"
)

var bmcuserlog = logf.Log.WithName("bmcuser-resource")

// SetupBMCUserWebhookWithManager registers the webhook for BMCUser in the manager.
func SetupBMCUserWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &baseboardv1alpha1.BMCUser{}).
		WithValidator(&BMCUserValidator{Client: mgr.GetAPIReader()}).
		Complete()
}

// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// +kubebuilder:webhook:path=/validate-baseboard-metal-ironcore-dev-v1alpha1-bmcuser,mutating=false,failurePolicy=fail,sideEffects=None,groups=baseboard.metal.ironcore.dev,resources=bmcusers,verbs=create;update,versions=v1alpha1,name=vbmcuser-v1alpha1.kb.io,admissionReviewVersions=v1

// BMCUserValidator validates BMCUser resources.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type BMCUserValidator struct {
	Client client.Reader
}

// ValidateCreate implements admission.Validator.
func (v *BMCUserValidator) ValidateCreate(ctx context.Context, obj *baseboardv1alpha1.BMCUser) (admission.Warnings, error) {
	bmcuserlog.Info("Validation for BMCUser upon creation", "name", obj.GetName())
	return v.validateOperatorAdminUniqueness(ctx, obj)
}

// ValidateUpdate implements admission.Validator.
func (v *BMCUserValidator) ValidateUpdate(ctx context.Context, oldObj, newObj *baseboardv1alpha1.BMCUser) (admission.Warnings, error) {
	bmcuserlog.Info("Validation for BMCUser upon update", "name", newObj.GetName())
	if uniquenessFieldsUnchanged(oldObj, newObj) {
		return nil, nil
	}
	return v.validateOperatorAdminUniqueness(ctx, newObj)
}

// ValidateDelete implements admission.Validator.
func (v *BMCUserValidator) ValidateDelete(_ context.Context, _ *baseboardv1alpha1.BMCUser) (admission.Warnings, error) {
	return nil, nil
}

// validateOperatorAdminUniqueness rejects a BMCUser with Type=OperatorAdmin when another
// BMCUser with Type=OperatorAdmin that references the same BMC is already owned by a
// different controller (a different BMCUserSet, or a standalone BMCUser with no owner).
//
// Two OperatorAdmin users sharing the same BMC are permitted when:
//   - They both belong to the same controlling BMCUserSet (the DualAccount -a/-b pair), or
//   - The existing OperatorAdmin is terminating — it is being deleted and the incoming user
//     is a replacement from a new BMCUserSet taking over credential management.
//
// Any other combination would result in competing writes to BMC.Spec.BMCSecretRef.
func (v *BMCUserValidator) validateOperatorAdminUniqueness(ctx context.Context, incoming *baseboardv1alpha1.BMCUser) (admission.Warnings, error) {
	if incoming.Spec.Type != baseboardv1alpha1.BMCUserTypeOperatorAdmin {
		return nil, nil
	}
	if incoming.Spec.BMCRef == nil {
		return nil, nil
	}

	incomingOwner := ownerSetName(incoming)

	userList := &baseboardv1alpha1.BMCUserList{}
	if err := v.Client.List(ctx, userList); err != nil {
		return nil, fmt.Errorf("failed to list BMCUsers: %w", err)
	}

	for _, existing := range userList.Items {
		if existing.Name == incoming.Name {
			continue
		}
		if existing.Spec.Type != baseboardv1alpha1.BMCUserTypeOperatorAdmin {
			continue
		}
		if existing.Spec.BMCRef == nil || existing.Spec.BMCRef.Name != incoming.Spec.BMCRef.Name {
			continue
		}
		// A terminating OperatorAdmin is no longer active; allow a replacement from
		// a different BMCUserSet so that credential promotion is not blocked while
		// the old user's finalizers are being removed.
		if !existing.DeletionTimestamp.IsZero() {
			continue
		}
		existingOwner := ownerSetName(&existing)
		if incomingOwner == "" || existingOwner == "" || incomingOwner != existingOwner {
			err := fmt.Errorf(
				"BMCUser %q is already an OperatorAdmin for BMC %q (owned by BMCUserSet %q); "+
					"only users from the same BMCUserSet may share OperatorAdmin access to a single BMC",
				existing.Name, incoming.Spec.BMCRef.Name, existingOwner,
			)
			return nil, apierrors.NewInvalid(
				schema.GroupKind{Group: incoming.GroupVersionKind().Group, Kind: incoming.Kind},
				incoming.GetName(),
				field.ErrorList{field.Forbidden(field.NewPath("spec").Child("type"), err.Error())},
			)
		}
	}
	return nil, nil
}

// uniquenessFieldsUnchanged returns true when the fields that matter for
// OperatorAdmin uniqueness — Type, BMCRef.Name, and the controlling owner —
// are identical between old and new. Updates that touch only finalizers or
// other metadata fields bypass the conflict check.
func uniquenessFieldsUnchanged(old, new *baseboardv1alpha1.BMCUser) bool {
	if old.Spec.Type != new.Spec.Type {
		return false
	}
	oldBMC := ""
	if old.Spec.BMCRef != nil {
		oldBMC = old.Spec.BMCRef.Name
	}
	newBMC := ""
	if new.Spec.BMCRef != nil {
		newBMC = new.Spec.BMCRef.Name
	}
	if oldBMC != newBMC {
		return false
	}
	return ownerSetName(old) == ownerSetName(new)
}

// ownerSetName returns the name of the controlling BMCUserSet for a BMCUser, or an
// empty string if the user has no controller or is controlled by something else.
func ownerSetName(user *baseboardv1alpha1.BMCUser) string {
	ref := metav1.GetControllerOf(user)
	if ref == nil || ref.Kind != "BMCUserSet" {
		return ""
	}
	return ref.Name
}
