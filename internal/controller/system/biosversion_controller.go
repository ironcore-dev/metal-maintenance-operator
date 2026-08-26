// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package system

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ironcore-dev/controller-utils/clientutils"
	"github.com/ironcore-dev/controller-utils/conditionutils"
	"github.com/ironcore-dev/metal-maintenance-operator/api"
	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/maintenance/v1alpha1"
	systemv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/system/v1alpha1"
	constants "github.com/ironcore-dev/metal-maintenance-operator/internal/constants"
	utils "github.com/ironcore-dev/metal-maintenance-operator/internal/utils"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/ironcore-dev/metal-operator/bmc"
	"github.com/ironcore-dev/metal-operator/pkg/bmcutils"
	"github.com/stmcginnis/gofish"
	"github.com/stmcginnis/gofish/schemas"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
)

// BIOSVersionReconciler reconciles a BIOSVersion object
const (
	BIOSVersionFinalizer = "system.metal.ironcore.dev/biosversion"

	ConditionUpgradeRebootIssued        = "VersionUpgradeRebootIssued"
	ConditionUpgradeRebootObservedOff   = "VersionUpgradeRebootObservedOff"
	ConditionUpgradePowerOn             = "VersionUpgradePowerOn"
	ConditionUpgradeRebootTimedOut      = "VersionUpgradeRebootTimedOut"
	ConditionUpgradeServerPowerOnIssued = "VersionUpgradeServerPowerOnIssued"

	ReasonRebootIssued        = "RebootRequestIssuedToBMC"
	ReasonRebootObservedOff   = "RebootObservedServerLeftPowerOnState"
	ReasonRebootPowerOn       = "RebootPowerOn"
	ReasonRebootTimedOut      = "RebootTimedOutWaitingForPowerState"
	ReasonServerPowerOnIssued = "ServerPowerOnIssuedToBMC"
)

type BIOSVersionReconciler struct {
	client.Client
	ManagerNamespace            string
	DefaultProtocol             metalv1alpha1.ProtocolScheme
	SkipCertValidation          bool
	Scheme                      *runtime.Scheme
	BMCOptions                  bmc.Options
	ResyncInterval              time.Duration
	RebootTimeoutExpiry         time.Duration
	Conditions                  *conditionutils.Accessor
	DefaultFailedAutoRetryCount int32
}

// +kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=biosversions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=biosversions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=biosversions/finalizers,verbs=update
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=servermaintenances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=servermaintenances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="batch",resources=jobs,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *BIOSVersionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	biosVersion := &systemv1alpha1.BIOSVersion{}
	if err := r.Get(ctx, req.NamespacedName, biosVersion); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.V(1).Info("Reconciling BIOSVersion")

	return r.reconcileExists(ctx, biosVersion)
}

func (r *BIOSVersionReconciler) reconcileExists(ctx context.Context, biosVersion *systemv1alpha1.BIOSVersion) (ctrl.Result, error) {
	ok, err := r.shouldDelete(ctx, biosVersion)
	if err != nil {
		return ctrl.Result{}, err
	}
	if ok {
		return r.delete(ctx, biosVersion)
	}
	return r.reconcile(ctx, biosVersion)
}

func (r *BIOSVersionReconciler) shouldDelete(ctx context.Context, biosVersion *systemv1alpha1.BIOSVersion) (bool, error) {
	isProgressing := func() (bool, error) {
		if biosVersion.Status.State != systemv1alpha1.BIOSVersionStateInProgress {
			return false, nil
		}
		if biosVersion.Spec.ServerRef != nil {
			if _, err := utils.GetServerByName(ctx, r.Client, biosVersion.Spec.ServerRef.Name); apierrors.IsNotFound(err) {
				return false, nil
			}
		}
		if biosVersion.Spec.ServerMaintenanceRef == nil {
			return false, nil
		}
		return utils.IsAnyServerMaintenanceActive(ctx, r.Client, []metalv1alpha1.ObjectReference{*biosVersion.Spec.ServerMaintenanceRef})
	}
	return utils.ShouldProceedWithDeletion(ctx, biosVersion, BIOSVersionFinalizer, isProgressing)
}

func (r *BIOSVersionReconciler) delete(ctx context.Context, biosVersion *systemv1alpha1.BIOSVersion) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("Deleting BIOSVersion")
	defer log.V(1).Info("Deleted BIOSVersion")

	if !controllerutil.ContainsFinalizer(biosVersion, BIOSVersionFinalizer) {
		return ctrl.Result{}, nil
	}

	log.V(1).Info("Ensuring that the finalizer is removed")
	if modified, err := clientutils.PatchEnsureNoFinalizer(ctx, r.Client, biosVersion, BIOSVersionFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *BIOSVersionReconciler) cleanupServerMaintenanceReferences(ctx context.Context, biosVersion *systemv1alpha1.BIOSVersion) error {
	log := ctrl.LoggerFrom(ctx)
	if biosVersion.Spec.ServerMaintenanceRef == nil {
		return nil
	}

	serverMaintenance, err := r.getServerMaintenanceForRef(ctx, biosVersion.Spec.ServerMaintenanceRef)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to get referred ServerMaintenance: %w", err)
	}

	if serverMaintenance.DeletionTimestamp.IsZero() {
		if metav1.IsControlledBy(serverMaintenance, biosVersion) {
			log.V(1).Info("Deleting ServerMaintenance", "ServerMaintenance", client.ObjectKeyFromObject(serverMaintenance))
			if err := r.Delete(ctx, serverMaintenance); err != nil {
				return err
			}
		} else {
			log.V(1).Info("ServerMaintenance is controlled by somebody else", "ServerMaintenance", client.ObjectKeyFromObject(serverMaintenance))
		}
	}

	// Remove the reference if the object is gone.
	if apierrors.IsNotFound(err) || err == nil {
		log.V(1).Info("Cleaning up ServerMaintenance ref in BIOSVersion as the object is gone")
		if err := r.patchServerMaintenanceRef(ctx, biosVersion, nil); err != nil {
			return fmt.Errorf("failed to clean up serverMaintenance ref in BIOSVersionReconciler status: %w", err)
		}
	}
	return nil
}

func (r *BIOSVersionReconciler) reconcile(ctx context.Context, biosVersion *systemv1alpha1.BIOSVersion) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	if utils.ShouldIgnoreReconciliation(biosVersion) {
		log.V(1).Info("Skipped BIOSVersion reconciliation")
		return ctrl.Result{}, nil
	}

	if modified, err := clientutils.PatchEnsureFinalizer(ctx, r.Client, biosVersion, BIOSVersionFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}

	requeue, err := r.transitionState(ctx, biosVersion)
	if err != nil {
		return ctrl.Result{}, err
	}
	if requeue {
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}

	log.V(1).Info("Reconciled BIOSVersion")
	return ctrl.Result{}, nil
}

func (r *BIOSVersionReconciler) transitionState(ctx context.Context, biosVersion *systemv1alpha1.BIOSVersion) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if biosVersion.Spec.ServerRef == nil {
		return false, fmt.Errorf("BIOSVersion does not have a ServerRef")
	}

	server, err := utils.GetServerByName(ctx, r.Client, biosVersion.Spec.ServerRef.Name)
	if err != nil {
		return false, fmt.Errorf("failed to fetch server: %w", err)
	}

	bmcClient, err := bmcutils.GetBMCClientForServer(ctx, r.Client, server, r.DefaultProtocol, r.SkipCertValidation, r.BMCOptions)
	if err != nil {
		if errors.As(err, &bmcutils.BMCUnAvailableError{}) {
			log.V(1).Info("BMC is not available, skipping", "BMC", server.Spec.BMCRef.Name, "Server", server.Name, "error", err)
			return true, nil
		}
		return false, fmt.Errorf("failed to get BMC client for server %s: %w", server.Name, err)
	}
	defer bmcClient.Logout()

	switch biosVersion.Status.State {
	case "", systemv1alpha1.BIOSVersionStatePending:
		// remove the retry annotation if it's present as we are retrying now
		if utils.ShouldRetryReconciliation(biosVersion) {
			biosVersionBase := biosVersion.DeepCopy()
			annotations := biosVersion.GetAnnotations()
			delete(annotations, constants.OperationAnnotation)
			biosVersion.SetAnnotations(annotations)
			if err := r.Patch(ctx, biosVersion, client.MergeFrom(biosVersionBase)); err != nil {
				return true, fmt.Errorf("failed to patch BIOSVersion for retrying: %w", err)
			}
			log.V(1).Info("Removed retry annotation from BIOSVersion for retrying", "BIOSVersion", biosVersion.Annotations)
			return false, nil
		}
		return false, r.cleanup(ctx, bmcClient, biosVersion, server)
	case systemv1alpha1.BIOSVersionStateInProgress:
		if ok, err := r.handleServerMaintenance(ctx, bmcClient, biosVersion, server); err != nil || !ok {
			return false, err
		}

		return r.processInProgressState(ctx, bmcClient, biosVersion, server)
	case systemv1alpha1.BIOSVersionStateCompleted:
		return false, r.cleanup(ctx, bmcClient, biosVersion, server)
	case systemv1alpha1.BIOSVersionStateFailed:
		return r.processFailedState(ctx, biosVersion, server)
	}

	log.V(1).Info("Unknown State found", "State", biosVersion.Status.State)
	return false, nil
}

func (r *BIOSVersionReconciler) handleServerMaintenance(ctx context.Context, bmcClient bmc.BMC, biosVersion *systemv1alpha1.BIOSVersion, server *metalv1alpha1.Server) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if biosVersion.Spec.ServerMaintenanceRef == nil {
		if requeue, err := r.requestServerMaintenance(ctx, biosVersion, server); err != nil || requeue {
			return false, err
		}
	}

	condition, err := utils.GetCondition(r.Conditions, biosVersion.Status.Conditions, constants.ConditionServerMaintenanceWaiting)
	if err != nil {
		return false, err
	}

	// The ServerMaintenance controller is solely responsible for requesting and confirming
	// that the Server is actually Parked before moving to InMaintenance, and for keeping
	// both in sync afterwards, so we only need to trust its reported state here.
	maintenance, err := utils.GetServerMaintenanceForObjectReference(ctx, r.Client, biosVersion.Spec.ServerMaintenanceRef)
	if err != nil {
		return false, fmt.Errorf("failed to get referenced ServerMaintenance: %w", err)
	}
	if maintenance.Status.State != maintenancev1alpha1.ServerMaintenanceStateInMaintenance {
		log.V(1).Info("Server not yet in maintenance", "Server", server.Name, "ServerMaintenanceState", maintenance.Status.State)
		if condition.Status != metav1.ConditionTrue {
			if err := r.Conditions.Update(
				condition,
				conditionutils.UpdateStatus(corev1.ConditionTrue),
				conditionutils.UpdateReason(constants.ReasonMaintenanceWaiting),
				conditionutils.UpdateMessage(fmt.Sprintf("Waiting for approval of %v", biosVersion.Spec.ServerMaintenanceRef.Name)),
			); err != nil {
				return false, fmt.Errorf("failed to update creating ServerMaintenance condition: %w", err)
			}
			if err := r.updateStatus(ctx, biosVersion, biosVersion.Status.State, biosVersion.Status.UpgradeTask, condition); err != nil {
				return false, fmt.Errorf("failed to patch BIOSVersion ServerMaintenance waiting conditions: %w", err)
			}
		}
		return false, nil
	}

	if condition.Reason != constants.ReasonMaintenanceApproved {
		if err := r.Conditions.Update(
			condition,
			conditionutils.UpdateStatus(corev1.ConditionFalse),
			conditionutils.UpdateReason(constants.ReasonMaintenanceApproved),
			conditionutils.UpdateMessage("Server is now in Maintenance mode"),
		); err != nil {
			return false, fmt.Errorf("failed to update creating ServerMaintenance condition: %w", err)
		}
		if err := r.updateStatus(ctx, biosVersion, biosVersion.Status.State, biosVersion.Status.UpgradeTask, condition); err != nil {
			return false, fmt.Errorf("failed to patch BIOSVersion ServerMaintenance waiting conditions: %w", err)
		}
		return false, nil
	}

	if ok, err := r.handleBMCReset(ctx, bmcClient, biosVersion, server); !ok || err != nil {
		return false, err
	}
	return true, nil
}

func (r *BIOSVersionReconciler) processInProgressState(ctx context.Context, bmcClient bmc.BMC, biosVersion *systemv1alpha1.BIOSVersion, server *metalv1alpha1.Server) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	issuedCondition, err := utils.GetCondition(r.Conditions, biosVersion.Status.Conditions, constants.ConditionVersionUpgradeIssued)
	if err != nil {
		return false, err
	}

	if issuedCondition.Status != metav1.ConditionTrue {
		log.V(1).Info("Processing BIOS version upgrade")
		if server.Status.PowerState != metalv1alpha1.ServerOnPowerState {
			powerOnIssued, err := utils.GetCondition(r.Conditions, biosVersion.Status.Conditions, ConditionUpgradeServerPowerOnIssued)
			if err != nil {
				return false, fmt.Errorf("failed to get Condition for issued powerOn of server: %w", err)
			}
			if powerOnIssued.Status != metav1.ConditionTrue {
				if err := bmcClient.PowerOn(ctx, server.Spec.SystemURI); err != nil {
					return false, fmt.Errorf("failed to power on server: %w", err)
				}
				if err := r.Conditions.Update(
					powerOnIssued,
					conditionutils.UpdateStatus(corev1.ConditionTrue),
					conditionutils.UpdateReason(ReasonServerPowerOnIssued),
					conditionutils.UpdateMessage("Issued PowerOn request to the server via BMC"),
				); err != nil {
					return false, fmt.Errorf("failed to update issued power on condition: %w", err)
				}
				return false, r.updateStatus(ctx, biosVersion, biosVersion.Status.State, biosVersion.Status.UpgradeTask, powerOnIssued)
			}
			log.V(1).Info("Server in powered off state, retrying", "Server", server.Name)
			return false, nil
		}
		// Check for pending component upgrade BEFORE issuing upgrade to avoid interrupting staged firmware
		hasPending, err := bmcClient.CheckBMCPendingComponentUpgrade(ctx, bmc.ComponentTypeBIOS)
		if err != nil {
			if errors.Is(err, bmc.ErrNotSupported) {
				log.V(1).Info("Pending component upgrade check not supported by this vendor, proceeding with upgrade", "Server", server.Name)
			} else {
				log.Error(err, "Failed to check pending component upgrade, requeueing", "Server", server.Name)
				return true, nil
			}
		} else if hasPending {
			log.Info("Pending component upgrade detected, deferring BIOS upgrade to avoid interruption", "Server", server.Name)
			return true, nil
		}
		return false, r.upgradeBIOSVersion(ctx, bmcClient, biosVersion, server, issuedCondition)
	}

	completedCondition, err := utils.GetCondition(r.Conditions, biosVersion.Status.Conditions, constants.ConditionVersionUpgradeCompleted)
	if err != nil {
		return false, err
	}

	if completedCondition.Status != metav1.ConditionTrue {
		log.V(1).Info("Check BIOS version upgrade task status")
		requeue, err := r.checkUpdateBiosUpgradeStatus(ctx, bmcClient, biosVersion, server, completedCondition)
		var TaskFetchFailed *utils.BMCTaskFetchFailedError
		if errors.As(err, &TaskFetchFailed) {
			log.V(1).Info("Failed to fetch BIOS upgrade task status from BMC", "error", err)
			// some vendor detele the task details once upgrade is completed.
			// check the current version and then proceed if version is as per spec
			currentBiosVersion, errVersionFetch := r.getBIOSVersionFromBMC(ctx, bmcClient, server)
			if errVersionFetch != nil {
				// need to give time if BMC is not responding, hence requeue
				log.Error(errors.Join(err, errVersionFetch), "Failed to fetch current BIOS version from BMC after upgrade task fetch failure")
				return true, nil
			}
			if currentBiosVersion == biosVersion.Spec.Version {
				// mark as completed, and procced with the workflow as the task might have been deleted post successful upgrade
				log.V(1).Info("BIOS version shows upgraded successfully even though task fetch failure", "Version", currentBiosVersion)
				if err := r.Conditions.Update(
					completedCondition,
					conditionutils.UpdateStatus(corev1.ConditionTrue),
					conditionutils.UpdateReason(constants.ReasonUpgradeTaskCompleted),
					conditionutils.UpdateMessage("Upgrade Task is missing. BIOS version successfully upgraded to: "+biosVersion.Spec.Version),
				); err != nil {
					return false, fmt.Errorf("failed to update upgrade complete conditions: %w", err)
				}
				return false, r.updateStatus(ctx, biosVersion, biosVersion.Status.State, biosVersion.Status.UpgradeTask, completedCondition)
			}
			log.V(1).Info("BIOS version not updated yet, need to wait for task details", "Version", currentBiosVersion, "DesiredVersion", biosVersion.Spec.Version)
			return requeue, err
		}
		return requeue, err
	}

	rebootPowerOnCondition, err := utils.GetCondition(r.Conditions, biosVersion.Status.Conditions, ConditionUpgradePowerOn)
	if err != nil {
		return false, err
	}

	if rebootPowerOnCondition.Status != metav1.ConditionTrue {
		return false, r.rebootServer(ctx, bmcClient, biosVersion, server)
	}

	condition, err := utils.GetCondition(r.Conditions, biosVersion.Status.Conditions, constants.ConditionVersionUpgradeVerification)
	if err != nil {
		return false, err
	}

	if condition.Status != metav1.ConditionTrue {
		log.V(1).Info("Verifying BIOS version update")

		currentBiosVersion, err := r.getBIOSVersionFromBMC(ctx, bmcClient, server)
		if err != nil {
			return false, err
		}
		if currentBiosVersion != biosVersion.Spec.Version {
			// TODO: Add timeout
			log.V(1).Info("BIOS version not updated", "Version", currentBiosVersion, "DesiredVersion", biosVersion.Spec.Version)
			if condition.Reason == "" {
				if err := r.Conditions.Update(
					condition,
					conditionutils.UpdateStatus(corev1.ConditionFalse),
					conditionutils.UpdateReason(constants.ReasonVersionVerificationFailed),
					conditionutils.UpdateMessage("waiting for BIOS Version update"),
				); err != nil {
					return false, fmt.Errorf("failed to update the verification condition: %w", err)
				}
			}
			log.V(1).Info("Waiting for BIOS version to reflect the new version")
			return true, nil
		}

		if err := r.Conditions.Update(
			condition,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason(constants.ReasonVersionUpdateVerified),
			conditionutils.UpdateMessage("BIOS Version updated"),
		); err != nil {
			return false, fmt.Errorf("failed to update conditions: %w", err)
		}

		return false, r.updateStatus(ctx, biosVersion, systemv1alpha1.BIOSVersionStateCompleted, biosVersion.Status.UpgradeTask, condition)
	}

	log.V(1).Info("Unknown Conditions found", "Condition", condition.Type)
	return false, nil
}

func (r *BIOSVersionReconciler) processFailedState(ctx context.Context, biosVersion *systemv1alpha1.BIOSVersion, server *metalv1alpha1.Server) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if utils.ShouldRetryReconciliation(biosVersion) {
		log.V(1).Info("Retrying BIOSVersion as per annotation")
		biosVersionBase := biosVersion.DeepCopy()
		biosVersion.Status.FailedAttempts = 0
		biosVersion.Status.State = systemv1alpha1.BIOSVersionStatePending
		biosVersion.Status.ObservedGeneration = biosVersion.Generation
		annotations := biosVersion.GetAnnotations()
		retryCondition, err := utils.GetCondition(r.Conditions, biosVersion.Status.Conditions, constants.ConditionRetryOfFailedResourceIssued)
		if err != nil {
			return true, fmt.Errorf("failed to get retry condition for BIOSVersion: %w", err)
		}
		// update only once
		if retryCondition.Status != metav1.ConditionTrue {
			err := r.Conditions.Update(retryCondition,
				conditionutils.UpdateStatus(metav1.ConditionTrue),
				conditionutils.UpdateReason(constants.ReasonRetryOfFailedResourceIssued),
				conditionutils.UpdateMessage(annotations[constants.OperationAnnotation]),
			)
			if err != nil {
				return true, fmt.Errorf("failed to update retry condition for BIOSVersion: %w", err)
			}
		}
		biosVersion.Status.Conditions = []metav1.Condition{*retryCondition}
		if err := r.Status().Patch(ctx, biosVersion, client.MergeFrom(biosVersionBase)); err != nil {
			return true, fmt.Errorf("failed to patch BIOSVersion status for retrying: %w", err)
		}
		return true, nil
	}
	var maxAttempts int32
	if biosVersion.Spec.RetryPolicy != nil && biosVersion.Spec.RetryPolicy.MaxAttempts != nil {
		// if RetryPolicy is given (even if MaxAttempts is 0), do not use the default value.
		maxAttempts = *biosVersion.Spec.RetryPolicy.MaxAttempts
	} else if r.DefaultFailedAutoRetryCount > 0 {
		// set the retry to this, if the optional RetryPolicy is not given and default retry count is set on the reconciler.
		maxAttempts = r.DefaultFailedAutoRetryCount
	}
	if maxAttempts > 0 {
		if biosVersion.Status.ObservedGeneration != biosVersion.Generation {
			// if the generation has changed, it means the spec has been updated after the failure, we can reset the retry count and retry.
			biosVersion.Status.FailedAttempts = 0
		}
		if biosVersion.Status.FailedAttempts < maxAttempts {
			log.V(1).Info("Retrying BIOSVersion automatically", "FailedAttempts", biosVersion.Status.FailedAttempts)
			biosVersionBase := biosVersion.DeepCopy()
			biosVersion.Status.State = systemv1alpha1.BIOSVersionStatePending
			biosVersion.Status.ObservedGeneration = biosVersion.Generation
			retryCondition, err := utils.GetCondition(r.Conditions, biosVersion.Status.Conditions, constants.ConditionRetryOfFailedResourceIssued)
			if err != nil {
				return true, fmt.Errorf("failed to get Retry condition for BIOSVersion: %w", err)
			}
			if retryCondition.Status == metav1.ConditionTrue {
				// keep the condition if it's already true,
				// otherwise SET resource will patch the retry annotation again.
				biosVersion.Status.Conditions = []metav1.Condition{*retryCondition}
			} else {
				biosVersion.Status.Conditions = nil
			}
			biosVersion.Status.FailedAttempts++
			if err := r.Status().Patch(ctx, biosVersion, client.MergeFrom(biosVersionBase)); err != nil {
				return true, fmt.Errorf("failed to patch BIOSVersion status for auto-retrying: %w", err)
			}
			return true, nil
		}
	}

	// Keep status consistent when retries are disabled or exhausted.
	if biosVersion.Status.FailedAttempts != 0 &&
		(maxAttempts == 0 || biosVersion.Status.ObservedGeneration != biosVersion.Generation) {
		biosVersionBase := biosVersion.DeepCopy()
		biosVersion.Status.FailedAttempts = 0
		biosVersion.Status.ObservedGeneration = biosVersion.Generation
		if err := r.Status().Patch(ctx, biosVersion, client.MergeFrom(biosVersionBase)); err != nil {
			return true, fmt.Errorf("failed to patch BIOSVersion status for disabled auto-retry: %w", err)
		}
	}
	log.V(1).Info("Failed to upgrade BIOSVersion", "BIOSVersion", biosVersion.Name, "Status", biosVersion.Status, "Server", server.Name)
	return false, nil
}

func (r *BIOSVersionReconciler) handleBMCReset(
	ctx context.Context,
	bmcClient bmc.BMC,
	biosVersion *systemv1alpha1.BIOSVersion,
	server *metalv1alpha1.Server,
) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	// reset BMC if not already done
	resetBMC, err := utils.GetCondition(r.Conditions, biosVersion.Status.Conditions, constants.ConditionResetIssued)
	if err != nil {
		return false, fmt.Errorf("failed to get condition for reset of BMC of server: %w", err)
	}

	if resetBMC.Status != metav1.ConditionTrue {
		// once the server is powered on, reset the BMC to make sure its in stable state
		// this avoids problems with some BMCs that hang up in subsequent operations
		if resetBMC.Reason != constants.ReasonResetIssued {
			if err := utils.ResetBMCOfServer(ctx, r.Client, server, bmcClient); err == nil {
				// mark reset to be issued, wait for next reconcile
				if err := r.Conditions.Update(
					resetBMC,
					conditionutils.UpdateStatus(corev1.ConditionFalse),
					conditionutils.UpdateReason(constants.ReasonResetIssued),
					conditionutils.UpdateMessage("Issued BMC reset to stabilize BMC of the server"),
				); err != nil {
					return false, fmt.Errorf("failed to update reset BMC condition: %w", err)
				}
				return false, r.updateStatus(ctx, biosVersion, biosVersion.Status.State, nil, resetBMC)
			} else {
				log.Error(err, "Failed to reset BMC of the server")
				return false, err
			}
		} else if server.Spec.BMCRef != nil {
			// we need to wait until the BMC resource annotation is removed
			bmcObj := &metalv1alpha1.BMC{}
			if err := r.Get(ctx, client.ObjectKey{Name: server.Spec.BMCRef.Name}, bmcObj); err != nil {
				log.Error(err, "Failed to get referred server's Manager")
				return false, err
			}
			annotations := bmcObj.GetAnnotations()
			if annotations != nil {
				if op, ok := annotations[metalv1alpha1.OperationAnnotation]; ok {
					if op == metalv1alpha1.GracefulRestartBMC {
						log.V(1).Info("Waiting for BMC reset as annotation on BMC object is set")
						return false, nil
					}
				}
			}
		}
		if err := r.Conditions.Update(
			resetBMC,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason(constants.ReasonResetIssued),
			conditionutils.UpdateMessage("BMC reset to stabilize BMC of the server is completed"),
		); err != nil {
			return false, fmt.Errorf("failed to update power on server condition: %w", err)
		}
		return false, r.updateStatus(ctx, biosVersion, biosVersion.Status.State, nil, resetBMC)
	}
	return true, nil
}

func (r *BIOSVersionReconciler) getBIOSVersionFromBMC(ctx context.Context, bmcClient bmc.BMC, server *metalv1alpha1.Server) (string, error) {
	currentBiosVersion, err := bmcClient.GetBiosVersion(ctx, server.Spec.SystemURI)
	if err != nil {
		return "", fmt.Errorf("failed to get BIOS version: %w", err)
	}

	return currentBiosVersion, nil
}

func (r *BIOSVersionReconciler) cleanup(ctx context.Context, bmcClient bmc.BMC, biosVersion *systemv1alpha1.BIOSVersion, server *metalv1alpha1.Server) error {
	log := ctrl.LoggerFrom(ctx)
	currentBiosVersion, err := r.getBIOSVersionFromBMC(ctx, bmcClient, server)
	if err != nil {
		return err
	}

	if currentBiosVersion == biosVersion.Spec.Version {
		if err := r.cleanupServerMaintenanceReferences(ctx, biosVersion); err != nil {
			return err
		}

		log.V(1).Info("Upgraded BIOS version", "Version", currentBiosVersion, "Server", server.Name)
		return r.updateStatus(ctx, biosVersion, systemv1alpha1.BIOSVersionStateCompleted, nil, nil)
	}
	retryFailedCondition, err := utils.GetCondition(r.Conditions, biosVersion.Status.Conditions, constants.ConditionRetryOfFailedResourceIssued)
	if err != nil {
		return fmt.Errorf("failed to get retry condition for BIOSVersion: %w", err)
	}
	if retryFailedCondition.Status == metav1.ConditionTrue {
		return r.updateStatus(ctx, biosVersion, systemv1alpha1.BIOSVersionStateInProgress, nil, retryFailedCondition)
	}
	return r.updateStatus(ctx, biosVersion, systemv1alpha1.BIOSVersionStateInProgress, nil, nil)
}

func (r *BIOSVersionReconciler) getServerMaintenanceForRef(ctx context.Context, serverMaintenanceRef *metalv1alpha1.ObjectReference) (*maintenancev1alpha1.ServerMaintenance, error) {
	if serverMaintenanceRef == nil {
		return nil, fmt.Errorf("server maintenance reference is nil")
	}

	serverMaintenance := &maintenancev1alpha1.ServerMaintenance{}
	if err := r.Get(ctx, client.ObjectKey{Name: serverMaintenanceRef.Name, Namespace: r.ManagerNamespace}, serverMaintenance); err != nil {
		return serverMaintenance, err
	}

	return serverMaintenance, nil
}

func (r *BIOSVersionReconciler) updateStatus(
	ctx context.Context,
	biosVersion *systemv1alpha1.BIOSVersion,
	state systemv1alpha1.BIOSVersionState,
	upgradeTask *api.Task,
	condition *metav1.Condition,
) error {
	if biosVersion.Status.State == state && condition == nil && upgradeTask == nil {
		return nil
	}

	biosVersionBase := biosVersion.DeepCopy()
	biosVersion.Status.State = state
	biosVersion.Status.ObservedGeneration = biosVersion.Generation

	if condition != nil {
		if err := r.Conditions.UpdateSlice(
			&biosVersion.Status.Conditions,
			condition.Type,
			conditionutils.UpdateStatus(condition.Status),
			conditionutils.UpdateReason(condition.Reason),
			conditionutils.UpdateMessage(condition.Message),
		); err != nil {
			return fmt.Errorf("failed to patch BIOSVersion condition: %w", err)
		}
	} else {
		biosVersion.Status.Conditions = []metav1.Condition{}
	}

	biosVersion.Status.UpgradeTask = upgradeTask

	if err := r.Status().Patch(ctx, biosVersion, client.MergeFrom(biosVersionBase)); err != nil {
		return fmt.Errorf("failed to patch BIOSVersion status: %w", err)
	}

	return nil
}

func (r *BIOSVersionReconciler) patchServerMaintenanceRef(ctx context.Context, biosVersion *systemv1alpha1.BIOSVersion, serverMaintenance *maintenancev1alpha1.ServerMaintenance) error {
	biosVersionsBase := biosVersion.DeepCopy()

	if serverMaintenance == nil {
		biosVersion.Spec.ServerMaintenanceRef = nil
	} else {
		biosVersion.Spec.ServerMaintenanceRef = &metalv1alpha1.ObjectReference{
			Namespace: serverMaintenance.Namespace,
			Name:      serverMaintenance.Name,
		}
	}

	if err := r.Patch(ctx, biosVersion, client.MergeFrom(biosVersionsBase)); err != nil {
		return err
	}

	return nil
}

// rebootServer issues a single BMC power-cycle reset request to reboot the server (rather than
// separate PowerOff/PowerOn commands) and tracks the transition through 3 conditions, since BMC
// power state changes are asynchronous and must not be re-issued on every reconcile:
//  1. ConditionUpgradeRebootIssued - the reset request has been issued to the BMC (issued once).
//  2. ConditionUpgradeRebootObservedOff - best-effort evidence the server left the powered-on
//     state after the request (the resync interval may be too coarse to always observe this).
//  3. ConditionUpgradePowerOn - the server has returned to the powered-on state, i.e. the reboot
//     is confirmed complete.
func (r *BIOSVersionReconciler) rebootServer(ctx context.Context, bmcClient bmc.BMC, biosVersion *systemv1alpha1.BIOSVersion, server *metalv1alpha1.Server) error {
	log := ctrl.LoggerFrom(ctx)
	rebootIssuedCondition, err := utils.GetCondition(r.Conditions, biosVersion.Status.Conditions, ConditionUpgradeRebootIssued)
	if err != nil {
		return fmt.Errorf("failed to get RebootIssued condition: %w", err)
	}

	if rebootIssuedCondition.Status != metav1.ConditionTrue {
		// only issue a reboot if the server is currently on - a server that is already off does
		// not need a power-cycle.
		switch server.Status.PowerState {
		case metalv1alpha1.ServerOnPowerState:
			if err := bmcClient.Reset(ctx, server.Spec.SystemURI, schemas.GracefulRestartResetType); err != nil {
				return fmt.Errorf("failed to issue server reboot: %w", err)
			}
		case metalv1alpha1.ServerOffPowerState:
			if err := bmcClient.PowerOn(ctx, server.Spec.SystemURI); err != nil {
				return fmt.Errorf("failed to power on server: %w", err)
			}
		default:
			return fmt.Errorf("server is in an unexpected power state: %s", server.Status.PowerState)
		}
		if err := r.Conditions.Update(
			rebootIssuedCondition,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason(ReasonRebootIssued),
			conditionutils.UpdateMessage("Issued a power-cycle reboot request to the server via BMC"),
		); err != nil {
			return fmt.Errorf("failed to update RebootIssued condition: %w", err)
		}
		log.V(1).Info("Reconciled BIOSVersion. Issued reboot request to Server", "Server", server.Name)
		return r.updateStatus(ctx, biosVersion, biosVersion.Status.State, biosVersion.Status.UpgradeTask, rebootIssuedCondition)
	}

	if timedOut, err := r.checkRebootTimeout(ctx, biosVersion, rebootIssuedCondition); timedOut || err != nil {
		return err
	}

	// Best-effort: opportunistically record if we happen to observe the server having left
	// the powered-on state. This is NOT a gate for progressing to the PowerOn check below -
	// the resync/watch interval may be too coarse to ever catch this transient window (e.g.
	// controller-runtime coalesces rapid Off->On transitions into a single reconcile), and
	// requiring it before checking PowerOn would deadlock the reboot flow in that case.
	rebootObservedOffCondition, err := utils.GetCondition(r.Conditions, biosVersion.Status.Conditions, ConditionUpgradeRebootObservedOff)
	if err != nil {
		return fmt.Errorf("failed to get RebootObservedOff condition: %w", err)
	}
	if rebootObservedOffCondition.Status != metav1.ConditionTrue && server.Status.PowerState != metalv1alpha1.ServerOnPowerState {
		if err := r.Conditions.Update(
			rebootObservedOffCondition,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason(ReasonRebootObservedOff),
			conditionutils.UpdateMessage("Observed server left the powered-on state after reboot request"),
		); err != nil {
			return fmt.Errorf("failed to update RebootObservedOff condition: %w", err)
		}
		return r.updateStatus(ctx, biosVersion, biosVersion.Status.State, biosVersion.Status.UpgradeTask, rebootObservedOffCondition)
	}

	rebootPowerOnCondition, err := utils.GetCondition(r.Conditions, biosVersion.Status.Conditions, ConditionUpgradePowerOn)
	if err != nil {
		return fmt.Errorf("failed to get PowerOn condition: %w", err)
	}

	if rebootPowerOnCondition.Status != metav1.ConditionTrue {
		if server.Status.PowerState == metalv1alpha1.ServerOnPowerState {
			if err := r.Conditions.Update(
				rebootPowerOnCondition,
				conditionutils.UpdateStatus(corev1.ConditionTrue),
				conditionutils.UpdateReason(ReasonRebootPowerOn),
				conditionutils.UpdateMessage("Server has completed reboot and returned to power on state"),
			); err != nil {
				return fmt.Errorf("failed to update reboot server powerOn condition: %w", err)
			}
			return r.updateStatus(ctx, biosVersion, biosVersion.Status.State, biosVersion.Status.UpgradeTask, rebootPowerOnCondition)
		}
		log.V(1).Info("Reconciled BIOSVersion. Waiting for server to power back on after reboot", "Server", server.Name)
		return nil
	}

	return nil
}

// checkRebootTimeout fails the BIOSVersion upgrade if the server has not completed its reboot
// within r.RebootTimeoutExpiry of the reboot request being issued.
func (r *BIOSVersionReconciler) checkRebootTimeout(ctx context.Context, biosVersion *systemv1alpha1.BIOSVersion, rebootIssuedCondition *metav1.Condition) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	startTime := rebootIssuedCondition.LastTransitionTime.Time
	if !time.Now().After(startTime.Add(r.RebootTimeoutExpiry)) {
		return false, nil
	}
	log.V(1).Info("Timeout while waiting for server reboot to complete")
	rebootTimedOut, err := utils.GetCondition(r.Conditions, biosVersion.Status.Conditions, ConditionUpgradeRebootTimedOut)
	if err != nil {
		return false, fmt.Errorf("failed to get Condition for reboot timeout: %w", err)
	}
	if err := r.Conditions.Update(
		rebootTimedOut,
		conditionutils.UpdateStatus(corev1.ConditionTrue),
		conditionutils.UpdateReason(ReasonRebootTimedOut),
		conditionutils.UpdateMessage(fmt.Sprintf("Timeout after: %v. rebootIssuedAt: %v. timedOut on: %v", r.RebootTimeoutExpiry, startTime, time.Now().String())),
	); err != nil {
		return false, fmt.Errorf("failed to update reboot timeout condition: %w", err)
	}
	return true, r.updateStatus(ctx, biosVersion, systemv1alpha1.BIOSVersionStateFailed, biosVersion.Status.UpgradeTask, rebootTimedOut)
}

func (r *BIOSVersionReconciler) requestServerMaintenance(ctx context.Context, biosVersion *systemv1alpha1.BIOSVersion, server *metalv1alpha1.Server) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if biosVersion.Spec.ServerMaintenanceRef != nil {
		if _, err := utils.GetServerMaintenanceForObjectReference(ctx, r.Client, biosVersion.Spec.ServerMaintenanceRef); apierrors.IsNotFound(err) {
			log.V(1).Info("Referenced ServerMaintenance no longer exists, clearing ref to allow re-creation")
			if err = r.patchServerMaintenanceRef(ctx, biosVersion, nil); err != nil {
				return false, fmt.Errorf("failed to clear stale ServerMaintenance ref: %w", err)
			}
			return true, nil // requeue to re-create
		} else if err != nil {
			return false, fmt.Errorf("failed to verify ServerMaintenance existence: %w", err)
		}
		condition, err := utils.GetCondition(r.Conditions, biosVersion.Status.Conditions, constants.ConditionServerMaintenanceCreated)
		if err != nil {
			return false, err
		}
		if condition.Status == metav1.ConditionTrue {
			return false, nil
		}
		if err := r.Conditions.Update(
			condition,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason(constants.ReasonMaintenanceCreated),
			conditionutils.UpdateMessage(fmt.Sprintf("Created/Present %v at %v", biosVersion.Spec.ServerMaintenanceRef.Name, time.Now())),
		); err != nil {
			return false, fmt.Errorf("failed to update creating ServerMaintenance condition: %w", err)
		}
		if err := r.updateStatus(ctx, biosVersion, biosVersion.Status.State, biosVersion.Status.UpgradeTask, condition); err != nil {
			return false, fmt.Errorf("failed to patch BIOSVersion conditions: %w", err)
		}
		return true, nil
	}

	serverMaintenance := &maintenancev1alpha1.ServerMaintenance{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: r.ManagerNamespace,
			Name:      biosVersion.Name,
		},
	}

	opResult, err := controllerutil.CreateOrPatch(ctx, r.Client, serverMaintenance, func() error {
		if biosVersion.Spec.ServerMaintenancePolicy != nil {
			serverMaintenance.Spec.Policy = *biosVersion.Spec.ServerMaintenancePolicy
		}
		serverMaintenance.Spec.ServerRef = &corev1.LocalObjectReference{Name: server.Name}
		if serverMaintenance.Status.State != maintenancev1alpha1.ServerMaintenanceStateInMaintenance && serverMaintenance.Status.State != "" {
			serverMaintenance.Status.State = ""
		}
		return controllerutil.SetControllerReference(biosVersion, serverMaintenance, r.Client.Scheme())
	})
	if err != nil {
		return false, fmt.Errorf("failed to create or patch serverMaintenance: %w", err)
	}
	log.V(1).Info("Created ServerMaintenance", "ServerMaintenance", client.ObjectKeyFromObject(serverMaintenance), "Operation", opResult)

	if err = r.patchServerMaintenanceRef(ctx, biosVersion, serverMaintenance); err != nil {
		return false, fmt.Errorf("failed to patch ServerMaintenance ref in BIOSVersion status: %w", err)
	}

	log.V(1).Info("Patched ServerMaintenance on BIOSVersion")
	return true, nil
}

func (r *BIOSVersionReconciler) checkUpdateBiosUpgradeStatus(
	ctx context.Context,
	bmcClient bmc.BMC,
	biosVersion *systemv1alpha1.BIOSVersion,
	server *metalv1alpha1.Server,
	completedCondition *metav1.Condition,
) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	var taskURI string
	if biosVersion.Status.UpgradeTask != nil {
		taskURI = biosVersion.Status.UpgradeTask.URI
	}
	taskCurrentStatus, err := func() (*schemas.Task, error) {
		if taskURI == "" {
			return nil, fmt.Errorf("invalid task URI. uri provided: '%v'", taskURI)
		}
		return bmcClient.GetBiosUpgradeTask(ctx, server.Status.Manufacturer, taskURI)
	}()
	if err != nil {
		return false, &utils.BMCTaskFetchFailedError{
			TaskURI:  taskURI,
			Resource: "BIOSUpgrade",
			Err:      err,
		}
	}
	log.V(1).Info("BIOS upgrade task current status", "TaskState", taskCurrentStatus.TaskState)

	upgradeCurrentTaskStatus := &api.Task{
		URI:             taskURI,
		State:           api.TaskState(taskCurrentStatus.TaskState),
		Status:          api.Health(taskCurrentStatus.TaskStatus),
		PercentComplete: int32(gofish.Deref(taskCurrentStatus.PercentComplete)),
	}

	// use checkpoint in case the job has stalled and we need to requeue
	transition := &conditionutils.FieldsTransition{
		IncludeStatus:  true,
		IncludeReason:  true,
		IncludeMessage: true,
	}
	checkpoint, err := transition.Checkpoint(r.Conditions, *completedCondition)
	if err != nil {
		return false, fmt.Errorf("failed to create checkpoint for Condition. %w", err)
	}

	if taskCurrentStatus.TaskState == schemas.KilledTaskState ||
		taskCurrentStatus.TaskState == schemas.ExceptionTaskState ||
		taskCurrentStatus.TaskState == schemas.CancelledTaskState ||
		(taskCurrentStatus.TaskStatus != schemas.OKHealth && taskCurrentStatus.TaskStatus != "") {
		message := fmt.Sprintf(
			"Upgrade Bios task has failed. with message %v check '%v' for details",
			taskCurrentStatus.Messages,
			taskURI,
		)
		if err := r.Conditions.Update(
			completedCondition,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason(constants.ReasonUpgradeTaskFailed),
			conditionutils.UpdateMessage(message),
		); err != nil {
			return false, fmt.Errorf("failed to update conditions: %w", err)
		}

		return false, r.updateStatus(ctx, biosVersion, systemv1alpha1.BIOSVersionStateFailed, upgradeCurrentTaskStatus, completedCondition)
	}

	if taskCurrentStatus.TaskState == schemas.CompletedTaskState {
		if err := r.Conditions.Update(
			completedCondition,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason(constants.ReasonUpgradeTaskCompleted),
			conditionutils.UpdateMessage("BIOS version successfully upgraded to: "+biosVersion.Spec.Version),
		); err != nil {
			return false, fmt.Errorf("failed to update conditions: %w", err)
		}

		return false, r.updateStatus(ctx, biosVersion, biosVersion.Status.State, upgradeCurrentTaskStatus, completedCondition)
	}

	// in-progress task states
	if err := r.Conditions.Update(
		completedCondition,
		conditionutils.UpdateStatus(corev1.ConditionFalse),
		conditionutils.UpdateReason(taskCurrentStatus.TaskState),
		conditionutils.UpdateMessage(
			fmt.Sprintf("BIOS upgrade in state: %v: PercentageCompleted %d",
				taskCurrentStatus.TaskState,
				upgradeCurrentTaskStatus.PercentComplete),
		),
	); err != nil {
		return false, fmt.Errorf("failed to update conditions: %w", err)
	}

	ok, err := checkpoint.Transitioned(r.Conditions, *completedCondition)
	if !ok && err == nil {
		log.V(1).Info("BIOS upgrade task has not progressed, retrying")
		// The upgrade job has stalled or is too slow. We need to requeue with exponential backoff.
		return true, nil
	}

	// TODO: Fail the state after certain timeout
	return false, r.updateStatus(ctx, biosVersion, biosVersion.Status.State, upgradeCurrentTaskStatus, completedCondition)
}

func (r *BIOSVersionReconciler) upgradeBIOSVersion(
	ctx context.Context,
	bmcClient bmc.BMC,
	biosVersion *systemv1alpha1.BIOSVersion,
	server *metalv1alpha1.Server,
	issuedCondition *metav1.Condition,
) error {
	log := ctrl.LoggerFrom(ctx)
	var username, password string
	if biosVersion.Spec.Image.SecretRef != nil {
		var err error
		username, password, err = utils.GetImageCredentialsForSecretRef(ctx, r.Client, biosVersion.Spec.Image.SecretRef)
		if err != nil {
			return fmt.Errorf("failed to get image credentials ref for: %w", err)
		}
	}

	var forceUpdate bool
	if biosVersion.Spec.UpdatePolicy != nil && *biosVersion.Spec.UpdatePolicy == api.UpdatePolicyForce {
		forceUpdate = true
	}

	parameters := &schemas.UpdateServiceSimpleUpdateParameters{
		ForceUpdate:      forceUpdate,
		ImageURI:         biosVersion.Spec.Image.URI,
		Password:         password,
		Username:         username,
		TransferProtocol: schemas.TransferProtocolType(biosVersion.Spec.Image.TransferProtocol),
	}

	taskMonitor, isFatal, err := func() (string, bool, error) {
		return bmcClient.UpgradeBiosVersion(ctx, server.Status.Manufacturer, parameters)
	}()

	upgradeCurrentTaskStatus := &api.Task{URI: taskMonitor}

	if isFatal {
		log.Error(err, "Failed to issue BIOS upgrade", "Version", biosVersion.Spec.Version, "Server", server.Name)
		if errCond := r.Conditions.Update(
			issuedCondition,
			conditionutils.UpdateStatus(corev1.ConditionFalse),
			conditionutils.UpdateReason(constants.ReasonUpgradeIssueFailed),
			conditionutils.UpdateMessage("Fatal error occurred. Upgrade might still go through on server."),
		); errCond != nil {
			log.Error(errCond, "Failed to update conditions")
			err := r.updateStatus(ctx, biosVersion, systemv1alpha1.BIOSVersionStateFailed, upgradeCurrentTaskStatus, issuedCondition)
			return errors.Join(errCond, err)
		}

		return r.updateStatus(ctx, biosVersion, systemv1alpha1.BIOSVersionStateFailed, upgradeCurrentTaskStatus, issuedCondition)
	}
	if err != nil {
		log.Error(err, "Failed to issue BIOS upgrade", "Version", biosVersion.Spec.Version, "Server", server.Name)
		return err
	}
	if errCond := r.Conditions.Update(
		issuedCondition,
		conditionutils.UpdateStatus(corev1.ConditionTrue),
		conditionutils.UpdateReason(constants.ReasonUpgradeIssued),
		conditionutils.UpdateMessage(fmt.Sprintf("Task to upgrade has been created %v", taskMonitor)),
	); errCond != nil {
		log.Error(errCond, "Failed to update conditions")
		if errCond := r.Conditions.Update(
			issuedCondition,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason(constants.ReasonUpgradeIssued),
			conditionutils.UpdateMessage(fmt.Sprintf("Task to upgrade has been created %v", taskMonitor)),
		); errCond != nil {
			log.Error(errCond, "Failed to update conditions")
			err := r.updateStatus(ctx, biosVersion, systemv1alpha1.BIOSVersionStateFailed, upgradeCurrentTaskStatus, issuedCondition)
			return errors.Join(errCond, err)
		}
	}

	return r.updateStatus(ctx, biosVersion, biosVersion.Status.State, upgradeCurrentTaskStatus, issuedCondition)
}

func (r *BIOSVersionReconciler) enqueueBiosVersionByServerRefs(ctx context.Context, obj client.Object) []ctrl.Request {
	log := ctrl.LoggerFrom(ctx)
	host := obj.(*metalv1alpha1.Server)

	// don't requeue if host in wrong state
	if host.Status.State == metalv1alpha1.ServerStateDiscovery ||
		host.Status.State == metalv1alpha1.ServerStateError ||
		host.Status.State == metalv1alpha1.ServerStateInitial {
		return nil
	}

	biosVersionList := &systemv1alpha1.BIOSVersionList{}
	if err := r.List(ctx, biosVersionList); err != nil {
		log.Error(err, "Failed to list BIOSVersionList")
		return nil
	}

	for _, biosVersion := range biosVersionList.Items {
		if biosVersion.Spec.ServerRef == nil || biosVersion.Spec.ServerRef.Name != host.Name {
			continue
		}
		// states where we do not need to requeue for host changes
		if biosVersion.Spec.ServerMaintenanceRef == nil ||
			biosVersion.Status.State == systemv1alpha1.BIOSVersionStateCompleted ||
			biosVersion.Status.State == systemv1alpha1.BIOSVersionStateFailed {
			return nil
		}
		return []ctrl.Request{{
			NamespacedName: types.NamespacedName{Namespace: biosVersion.Namespace, Name: biosVersion.Name},
		}}
	}
	return nil
}

func (r *BIOSVersionReconciler) enqueueBiosSettingsByBMC(ctx context.Context, obj client.Object) []ctrl.Request {
	log := ctrl.LoggerFrom(ctx)
	bmcObj := obj.(*metalv1alpha1.BMC)

	serverList := &metalv1alpha1.ServerList{}
	if err := clientutils.ListAndFilter(ctx, r.Client, serverList, func(object client.Object) (bool, error) {
		server := object.(*metalv1alpha1.Server)
		return server.Spec.BMCRef != nil && server.Spec.BMCRef.Name == bmcObj.Name, nil
	}); err != nil {
		log.Error(err, "Failed to list Servers created by this BMC resource", "BMC", bmcObj.Name)
		return nil
	}

	serverMap := make(map[string]struct{})
	for _, server := range serverList.Items {
		serverMap[server.Name] = struct{}{}
	}

	biosVersionList := &systemv1alpha1.BIOSVersionList{}
	if err := clientutils.ListAndFilter(ctx, r.Client, biosVersionList, func(object client.Object) (bool, error) {
		biosVersion := object.(*systemv1alpha1.BIOSVersion)
		if biosVersion.Spec.ServerRef == nil {
			return false, nil
		}
		if _, exists := serverMap[biosVersion.Spec.ServerRef.Name]; !exists {
			return false, nil
		}
		return true, nil
	}); err != nil {
		log.Error(err, "Failed to list BIOSVersion objects created by this BMC resource", "BMC", bmcObj.Name)
		return nil
	}

	reqs := make([]ctrl.Request, 0)
	for _, biosVersion := range biosVersionList.Items {
		if biosVersion.Status.State == systemv1alpha1.BIOSVersionStateInProgress {
			resetBMC, err := utils.GetCondition(r.Conditions, biosVersion.Status.Conditions, constants.ConditionResetIssued)
			if err != nil {
				log.Error(err, "Failed to get reset BMC condition")
				continue
			}
			if resetBMC.Status == metav1.ConditionTrue {
				continue
			}
			// enqueue only if the BMC reset is requested for this BMC
			if resetBMC.Reason == constants.ReasonResetIssued {
				reqs = append(reqs, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: biosVersion.Namespace, Name: biosVersion.Name}})
			}
		}
	}
	return reqs
}

// SetupWithManager sets up the controller with the Manager.
func (r *BIOSVersionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&systemv1alpha1.BIOSVersion{}).
		Owns(&maintenancev1alpha1.ServerMaintenance{}).
		Watches(&metalv1alpha1.Server{}, handler.EnqueueRequestsFromMapFunc(r.enqueueBiosVersionByServerRefs)).
		Watches(&metalv1alpha1.BMC{}, handler.EnqueueRequestsFromMapFunc(r.enqueueBiosSettingsByBMC)).
		Complete(r)
}
