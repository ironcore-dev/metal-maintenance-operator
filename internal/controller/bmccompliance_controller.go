// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/maintenance-operator/internal/utils"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	maintenancev1alpha1 "github.com/ironcore-dev/maintenance-operator/api/v1alpha1"
)

// BMCComplianceReconciler reconciles a BMCCompliance object
type BMCComplianceReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// TODO: clarify if a finalizer is needed for Compliance
// const BMCComplianceFinalizer = "firmware.ironcore.dev/bmccompliance"

// +kubebuilder:rbac:groups=maintenance.ironcore.dev,resources=bmccompliances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=maintenance.ironcore.dev,resources=bmccompliances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=maintenance.ironcore.dev,resources=bmccompliances/finalizers,verbs=update

func (r *BMCComplianceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	bmcCompliance := &maintenancev1alpha1.BMCCompliance{}
	if err := r.Get(ctx, req.NamespacedName, bmcCompliance); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.V(1).Info("Reconciling bmcCompliance", "name", req.Name, "namespace", req.Namespace)

	return r.reconcileExists(ctx, log, bmcCompliance)
}

func (r *BMCComplianceReconciler) reconcileExists(
	ctx context.Context,
	log logr.Logger,
	bmcCompliance *maintenancev1alpha1.BMCCompliance,
) (ctrl.Result, error) {
	//TODO: remove log as it was only to test the fetched data
	log.V(1).Info("bmcCompliance details", "spec", bmcCompliance.Spec, "status", bmcCompliance.Status)
	// validate if compliance crd is not deleted define what should we do if it is deleted?
	//TODO: Implement logic if crd is deleted
	if bmcCompliance.DeletionTimestamp != nil {
		log.V(1).Info("bmcCompliance is being deleted, skipping reconciliation", "name", bmcCompliance.Name)
		return ctrl.Result{}, nil
	}
	return r.reconcile(ctx, log, bmcCompliance)
}

func (r *BMCComplianceReconciler) reconcile(
	ctx context.Context,
	log logr.Logger,
	bmcCompliance *maintenancev1alpha1.BMCCompliance,
) (ctrl.Result, error) {
	//skip reconciliation if BMCRef is not set
	if bmcCompliance.Spec.BMCRef == nil {
		log.Error(nil, "BMCRef is not set in Compliance Check CRD", "name", bmcCompliance.Name)
		return ctrl.Result{}, nil
	}
	// fetch referred BMC object
	bmc, err := r.getReferredBMC(ctx, log, bmcCompliance.Spec.BMCRef)
	if err != nil {
		log.V(1).Info("referred bmc object could not be fetched")
		return ctrl.Result{}, err
	}
	//TODO: remove log as it was only to test the fetched data
	log.V(1).Info("Referred BMC object", "name", bmc.Name, "status", bmc.Status)
	complianceState, err := r.determineComplianceState(log, bmc, bmcCompliance)
	if err != nil {
		log.Error(err, "Failed to determine compliance state", "bmcName", bmc.Name, "bmcComplianceName", bmcCompliance.Name)
		return ctrl.Result{}, err
	}
	log.V(1).Info("Determined compliance state", "state", complianceState)

	bmcCompliance.Status.State = complianceState
	if err := r.Status().Update(ctx, bmcCompliance); err != nil {
		log.Error(err, "Failed to update bmcCompliance status", "name", bmcCompliance.Name)
		return ctrl.Result{}, err
	}
	log.V(1).Info("Updated bmcCompliance status")
	return ctrl.Result{}, nil
}

func (r *BMCComplianceReconciler) getReferredBMC(
	ctx context.Context,
	log logr.Logger,
	bmcRef *corev1.LocalObjectReference,
) (*metalv1alpha1.BMC, error) {
	key := client.ObjectKey{Name: bmcRef.Name}
	bmc := &metalv1alpha1.BMC{}
	if err := r.Get(ctx, key, bmc); err != nil {
		log.V(1).Error(err, "Failed to get BMC by reference")
		return bmc, err
	}
	return bmc, nil
}

func (r *BMCComplianceReconciler) determineComplianceState(
	log logr.Logger,
	bmc *metalv1alpha1.BMC,
	bmcCompliance *maintenancev1alpha1.BMCCompliance,
) (maintenancev1alpha1.BMCComplianceState, error) {
	firmwareVersion := bmc.Status.FirmwareVersion
	if firmwareVersion == "" {
		log.Error(nil, "Firmware version not found in BMC status", "bmcRef", bmcCompliance.Spec.BMCRef.Name)
		return maintenancev1alpha1.BMCComplianceStateUnknown, nil
	}

	// check for outOfSupport version, this has always the highest priority as you will not get support for this version
	if bmcCompliance.Spec.OutOfSupportVersion != "" {
		log.V(1).Info("Checking if firmware version is out of support")
		comparisonResult, err := utils.CompareFirmwareVersions(firmwareVersion, bmcCompliance.Spec.OutOfSupportVersion)
		log.V(1).Info("Firmware version result comparisonResult", "comparisonResult", comparisonResult)
		if err != nil {
			log.Error(err, "Failed to compare firmware versions with out of support version spec")
			return maintenancev1alpha1.BMCComplianceStateUnknown, err
		}
		if comparisonResult == utils.VersionLower || comparisonResult == utils.VersionEqual {
			log.V(1).Info("Firmware version is out of support")
			log.V(1).Info("Firmware version is out of support", "comparisonResult", comparisonResult)
			return maintenancev1alpha1.BMCComplianceStateOutOfSupport, nil
		}
	}

	//check for crtitical versions this has the second highest priority
	if len(bmcCompliance.Spec.CriticalVersions) > 0 {
		for _, criticalVersion := range bmcCompliance.Spec.CriticalVersions {
			if firmwareVersion == criticalVersion {
				log.V(1).Info("Firmware version is critical", "version", firmwareVersion)
				return maintenancev1alpha1.BMCComplianceStateCritical, nil
			}
		}
	}

	// check with policy
	switch bmcCompliance.Spec.CompliancePolicy {
	case maintenancev1alpha1.BMCCompliancePolicyStrict:
		if firmwareVersion == bmcCompliance.Spec.TargetVersion {
			log.V(1).Info("Firmware version is compliant", "version", firmwareVersion)
			return maintenancev1alpha1.BMCComplianceStateCompliant, nil
		} else {
			log.V(1).Info("Firmware version is non-compliant", "version", firmwareVersion)
			return maintenancev1alpha1.BMCComplianceStateNonCompliant, nil
		}
	case maintenancev1alpha1.BMCCompliancePolicyRange:
		if bmcCompliance.Spec.VersionRange != nil && len(bmcCompliance.Spec.VersionRange.Min) > 0 && len(bmcCompliance.Spec.VersionRange.Max) > 0 {
			minVersion := bmcCompliance.Spec.VersionRange.Min
			maxVersion := bmcCompliance.Spec.VersionRange.Max

			comparisonMinResult, err := utils.CompareFirmwareVersions(firmwareVersion, minVersion)
			if err != nil {
				log.Error(err, "Failed to compare firmware versions with minimum version spec")
				return maintenancev1alpha1.BMCComplianceStateUnknown, err
			}
			comparisonMaxResult, err := utils.CompareFirmwareVersions(firmwareVersion, maxVersion)
			if err != nil {
				log.Error(err, "Failed to compare firmware versions with maximum version spec")
				return maintenancev1alpha1.BMCComplianceStateUnknown, err
			}
			if (comparisonMinResult == utils.VersionHigher || comparisonMinResult == utils.VersionEqual) &&
				(comparisonMaxResult == utils.VersionLower || comparisonMaxResult == utils.VersionEqual) {
				log.V(1).Info("Firmware version is within the range", "version", firmwareVersion, "minVersion", minVersion, "maxVersion", maxVersion)
				return maintenancev1alpha1.BMCComplianceStateCompliant, nil
			}

			log.V(1).Info("Firmware version is outside the range", "version", firmwareVersion, "minVersion", minVersion, "maxVersion", maxVersion)
			return maintenancev1alpha1.BMCComplianceStateNonCompliant, nil

		}

	case maintenancev1alpha1.BMCCompliancePolicyMinimum:
		if bmcCompliance.Spec.VersionRange != nil && len(bmcCompliance.Spec.VersionRange.Min) > 0 {
			minVersion := bmcCompliance.Spec.VersionRange.Min
			comparisonResult, err := utils.CompareFirmwareVersions(firmwareVersion, minVersion)
			if err != nil {
				log.Error(err, "Failed to compare firmware versions with minimum version spec")
				return maintenancev1alpha1.BMCComplianceStateUnknown, err
			}
			if comparisonResult == utils.VersionHigher || comparisonResult == utils.VersionEqual {
				log.V(1).Info("Firmware version is compliant with minimum version", "version", firmwareVersion, "minVersion", minVersion)
				return maintenancev1alpha1.BMCComplianceStateCompliant, nil
			}
			log.V(1).Info("Firmware version is non-compliant with minimum version", "version", firmwareVersion, "minVersion", minVersion)
			return maintenancev1alpha1.BMCComplianceStateNonCompliant, nil
		}

	default:
		log.V(1).Info("Compliance policy is not strict, treating as unknown", "version", firmwareVersion)
		return maintenancev1alpha1.BMCComplianceStateUnknown, nil
	}
	return maintenancev1alpha1.BMCComplianceStateUnknown, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *BMCComplianceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&maintenancev1alpha1.BMCCompliance{}).
		Named("bmccompliance").
		Complete(r)
}
