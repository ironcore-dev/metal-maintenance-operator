// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	systemv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/system/v1alpha1"
	utils "github.com/ironcore-dev/metal-maintenance-operator/internal/utils"
	webhookutils "github.com/ironcore-dev/metal-maintenance-operator/internal/webhook"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

var versionLog = logf.Log.WithName("biosversion-resource")

// SetupBIOSVersionWebhookWithManager registers the webhook for BIOSVersion in the manager.
func SetupBIOSVersionWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &systemv1alpha1.BIOSVersion{}).
		WithValidator(&BIOSVersionValidator{Client: mgr.GetAPIReader()}).
		Complete()
}

// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// +kubebuilder:webhook:path=/validate-system-metal-ironcore-dev-v1alpha1-biosversion,mutating=false,failurePolicy=fail,sideEffects=None,groups=system.metal.ironcore.dev,resources=biosversions,verbs=create;update;delete,versions=v1alpha1,name=vbiosversion-v1alpha1.kb.io,admissionReviewVersions=v1

// BIOSVersionValidator struct is responsible for validating the BIOSVersion resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type BIOSVersionValidator struct {
	Client client.Reader
}

// ValidateCreate implements admission.Validator so a webhook will be registered for the type BIOSVersion.
func (v *BIOSVersionValidator) ValidateCreate(ctx context.Context, obj *systemv1alpha1.BIOSVersion) (admission.Warnings, error) {
	versionLog.Info("Validation for BIOSVersion upon creation", "name", obj.GetName())
	versions := &systemv1alpha1.BIOSVersionList{}
	if err := v.Client.List(ctx, versions); err != nil {
		return nil, fmt.Errorf("failed to list BIOSVersion: %w", err)
	}
	return checkForDuplicateBIOSVersionRefToServer(versions, obj)
}

// ValidateUpdate implements admission.Validator so a webhook will be registered for the type BIOSVersion.
func (v *BIOSVersionValidator) ValidateUpdate(ctx context.Context, oldObj, newObj *systemv1alpha1.BIOSVersion) (admission.Warnings, error) {
	versionLog.Info("Validation for BIOSVersion upon update", "name", newObj.GetName())

	if !webhookutils.ShouldAllowForceUpdateInProgress(newObj) && oldObj.Spec.ServerMaintenanceRef != nil {
		active, err := utils.IsAnyServerMaintenanceActive(ctx, v.Client, []metalv1alpha1.ObjectReference{*oldObj.Spec.ServerMaintenanceRef})
		if err != nil {
			return nil, fmt.Errorf("failed to check maintenance state: %w", err)
		}
		if active {
			msg := fmt.Errorf("BIOSVersion %s is under active maintenance, unable to update", oldObj.Name)
			return nil, apierrors.NewInvalid(
				schema.GroupKind{Group: newObj.GroupVersionKind().Group, Kind: newObj.Kind},
				newObj.GetName(), field.ErrorList{field.Forbidden(field.NewPath("spec"), msg.Error())})
		}
	}

	versions := &systemv1alpha1.BIOSVersionList{}
	if err := v.Client.List(ctx, versions); err != nil {
		return nil, fmt.Errorf("failed to list BIOSVersion: %w", err)
	}
	return checkForDuplicateBIOSVersionRefToServer(versions, newObj)
}

// ValidateDelete implements admission.Validator so a webhook will be registered for the type BIOSVersion.
func (v *BIOSVersionValidator) ValidateDelete(ctx context.Context, obj *systemv1alpha1.BIOSVersion) (admission.Warnings, error) {
	versionLog.Info("Validation for BIOSVersion upon deletion", "name", obj.GetName())

	if !webhookutils.ShouldAllowForceDeleteInProgress(obj) && obj.Spec.ServerMaintenanceRef != nil {
		active, err := utils.IsAnyServerMaintenanceActive(ctx, v.Client, []metalv1alpha1.ObjectReference{*obj.Spec.ServerMaintenanceRef})
		if err != nil {
			return nil, fmt.Errorf("failed to check maintenance state: %w", err)
		}
		if active {
			return nil, apierrors.NewBadRequest("BIOSVersion is under active maintenance, unable to delete")
		}
	}
	return nil, nil
}

func checkForDuplicateBIOSVersionRefToServer(versions *systemv1alpha1.BIOSVersionList, version *systemv1alpha1.BIOSVersion) (admission.Warnings, error) {
	if version.Spec.ServerRef == nil {
		return nil, nil
	}
	for _, bv := range versions.Items {
		if version.Name == bv.Name {
			continue
		}
		if bv.Spec.ServerRef == nil {
			continue
		}
		if version.Spec.ServerRef.Name == bv.Spec.ServerRef.Name {
			err := fmt.Errorf("server (%s) referred in %s is duplicate of server (%s) referred in %s",
				version.Spec.ServerRef.Name, version.Name, bv.Spec.ServerRef.Name, bv.Name)
			return nil, apierrors.NewInvalid(
				schema.GroupKind{Group: version.GroupVersionKind().Group, Kind: version.Kind},
				version.GetName(), field.ErrorList{field.Duplicate(field.NewPath("spec").Child("serverRef").Child("name"), err)})
		}
	}
	return nil, nil
}
