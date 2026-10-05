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

var firmwareUpdateLog = logf.Log.WithName("firmwareupdate-resource")

// SetupFirmwareUpdateWebhookWithManager registers the webhook for FirmwareUpdate in the manager.
func SetupFirmwareUpdateWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &systemv1alpha1.FirmwareUpdate{}).
		WithValidator(&FirmwareUpdateValidator{Client: mgr.GetAPIReader()}).
		Complete()
}

// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// +kubebuilder:webhook:path=/validate-system-metal-ironcore-dev-v1alpha1-firmwareupdate,mutating=false,failurePolicy=fail,sideEffects=None,groups=system.metal.ironcore.dev,resources=firmwareupdates,verbs=create;update;delete,versions=v1alpha1,name=vfirmwareupdate-v1alpha1.kb.io,admissionReviewVersions=v1

// FirmwareUpdateValidator struct is responsible for validating the FirmwareUpdate resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type FirmwareUpdateValidator struct {
	Client client.Reader
}

// ValidateCreate implements admission.Validator so a webhook will be registered for the type FirmwareUpdate.
func (v *FirmwareUpdateValidator) ValidateCreate(ctx context.Context, obj *systemv1alpha1.FirmwareUpdate) (admission.Warnings, error) {
	firmwareUpdateLog.Info("Validation for FirmwareUpdate upon creation", "name", obj.GetName())
	fwUpdateList := &systemv1alpha1.FirmwareUpdateList{}
	if err := v.Client.List(ctx, fwUpdateList); err != nil {
		return nil, fmt.Errorf("failed to list FirmwareUpdates: %w", err)
	}
	return checkForDuplicateFirmwareUpdateRefToServer(fwUpdateList, obj)
}

// ValidateUpdate implements admission.Validator so a webhook will be registered for the type FirmwareUpdate.
func (v *FirmwareUpdateValidator) ValidateUpdate(ctx context.Context, oldObj, newObj *systemv1alpha1.FirmwareUpdate) (admission.Warnings, error) {
	firmwareUpdateLog.Info("Validation for FirmwareUpdate upon update", "name", newObj.GetName())

	if !webhookutils.ShouldAllowForceUpdateInProgress(newObj) && oldObj.Status.ServerMaintenanceRef != nil {
		active, err := utils.IsAnyServerMaintenanceActive(ctx, v.Client, []metalv1alpha1.ObjectReference{*oldObj.Status.ServerMaintenanceRef})
		if err != nil {
			return nil, fmt.Errorf("failed to check maintenance state: %w", err)
		}
		if active {
			msg := fmt.Errorf("FirmwareUpdate %s is under active maintenance, unable to update", oldObj.Name)
			return nil, apierrors.NewInvalid(
				schema.GroupKind{Group: newObj.GroupVersionKind().Group, Kind: newObj.Kind},
				newObj.GetName(), field.ErrorList{field.Forbidden(field.NewPath("spec"), msg.Error())})
		}
	}

	fwUpdateList := &systemv1alpha1.FirmwareUpdateList{}
	if err := v.Client.List(ctx, fwUpdateList); err != nil {
		return nil, fmt.Errorf("failed to list FirmwareUpdates: %w", err)
	}
	return checkForDuplicateFirmwareUpdateRefToServer(fwUpdateList, newObj)
}

// ValidateDelete implements admission.Validator so a webhook will be registered for the type FirmwareUpdate.
func (v *FirmwareUpdateValidator) ValidateDelete(ctx context.Context, obj *systemv1alpha1.FirmwareUpdate) (admission.Warnings, error) {
	firmwareUpdateLog.Info("Validation for FirmwareUpdate upon deletion", "name", obj.GetName())

	if !webhookutils.ShouldAllowForceDeleteInProgress(obj) && obj.Status.ServerMaintenanceRef != nil {
		active, err := utils.IsAnyServerMaintenanceActive(ctx, v.Client, []metalv1alpha1.ObjectReference{*obj.Status.ServerMaintenanceRef})
		if err != nil {
			return nil, fmt.Errorf("failed to check maintenance state: %w", err)
		}
		if active {
			return nil, apierrors.NewBadRequest("FirmwareUpdate is under active maintenance, unable to delete")
		}
	}
	return nil, nil
}

// checkForDuplicateFirmwareUpdateRefToServer rejects a FirmwareUpdate whose ServerRef duplicates
// another FirmwareUpdate's ServerRef.
func checkForDuplicateFirmwareUpdateRefToServer(fwUpdateList *systemv1alpha1.FirmwareUpdateList, fwUpdate *systemv1alpha1.FirmwareUpdate) (admission.Warnings, error) {
	if fwUpdate.Spec.ServerRef == nil {
		return nil, nil
	}
	for _, other := range fwUpdateList.Items {
		if fwUpdate.Name == other.Name {
			continue
		}
		if other.Spec.ServerRef == nil {
			continue
		}
		if fwUpdate.Spec.ServerRef.Name != other.Spec.ServerRef.Name {
			continue
		}
		err := fmt.Errorf("server (%s) referred in %s is duplicate of server (%s) referred in %s",
			fwUpdate.Spec.ServerRef.Name, fwUpdate.Name, other.Spec.ServerRef.Name, other.Name)
		return nil, apierrors.NewInvalid(
			schema.GroupKind{Group: fwUpdate.GroupVersionKind().Group, Kind: fwUpdate.Kind},
			fwUpdate.GetName(), field.ErrorList{field.Duplicate(field.NewPath("spec").Child("serverRef"), err)})
	}
	return nil, nil
}
