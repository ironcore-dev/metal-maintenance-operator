// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package system

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ironcore-dev/controller-utils/clientutils"
	"github.com/ironcore-dev/controller-utils/conditionutils"
	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/maintenance/v1alpha1"
	systemv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/system/v1alpha1"
	constants "github.com/ironcore-dev/metal-maintenance-operator/internal/constants"
	utils "github.com/ironcore-dev/metal-maintenance-operator/internal/utils"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/ironcore-dev/metal-operator/bmc"
	"github.com/ironcore-dev/metal-operator/pkg/bmcutils"
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

// FirmwareUpdateReconciler reconciles a FirmwareUpdate object.
const (
	FirmwareUpdateFinalizer = "system.metal.ironcore.dev/firmwareupdate"

	ConditionUnsupportedVendor = "UnsupportedVendor"
	ReasonUnsupportedVendor    = "UnsupportedVendor"
)

// vendorHandler is implemented by each vendor-specific handler and drives the
// three reconciliation phases (Pending, InProgress, Completed) that contain
// vendor-specific BMC logic.
type vendorHandler interface {
	handlePending(ctx context.Context, fw *systemv1alpha1.FirmwareUpdate, bmcClient bmc.BMC, r *FirmwareUpdateReconciler, server *metalv1alpha1.Server) (bool, error)
	handleInProgress(ctx context.Context, fw *systemv1alpha1.FirmwareUpdate, bmcClient bmc.BMC, r *FirmwareUpdateReconciler, server *metalv1alpha1.Server) (bool, error)
	handleCompleted(ctx context.Context, fw *systemv1alpha1.FirmwareUpdate, bmcClient bmc.BMC, r *FirmwareUpdateReconciler, server *metalv1alpha1.Server) (bool, error)
}

// vendorHandlers maps server manufacturers to their respective handler
// implementations. Servers whose manufacturer string is not present in the map
// are transitioned to Failed with an UnsupportedVendor condition.
var vendorHandlers = map[string]vendorHandler{
	"Dell Inc.": &dellHandler{},
	"HPE":       &otbHandler{},
	"Lenovo":    &otbHandler{},
}

type FirmwareUpdateReconciler struct {
	client.Client
	ManagerNamespace            string
	DefaultProtocol             metalv1alpha1.ProtocolScheme
	SkipCertValidation          bool
	Scheme                      *runtime.Scheme
	BMCOptions                  bmc.Options
	ResyncInterval              time.Duration
	Conditions                  *conditionutils.Accessor
	DefaultFailedAutoRetryCount int32
	// MaxRepositoryPasses bounds how many times a dry-run repository check may
	// find further packages pending (and thus re-enter InProgress) before the
	// FirmwareUpdate is marked Failed instead.
	MaxRepositoryPasses int32
}

// +kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=firmwareupdates,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=firmwareupdates/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=firmwareupdates/finalizers,verbs=update
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=servermaintenances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=servermaintenances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *FirmwareUpdateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	fw := &systemv1alpha1.FirmwareUpdate{}
	if err := r.Get(ctx, req.NamespacedName, fw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.V(1).Info("Reconciling FirmwareUpdate")

	return r.reconcileExists(ctx, fw)
}

func (r *FirmwareUpdateReconciler) reconcileExists(ctx context.Context, fw *systemv1alpha1.FirmwareUpdate) (ctrl.Result, error) {
	ok, err := r.shouldDelete(ctx, fw)
	if err != nil {
		return ctrl.Result{}, err
	}
	if ok {
		return r.delete(ctx, fw)
	}
	return r.reconcile(ctx, fw)
}

func (r *FirmwareUpdateReconciler) shouldDelete(ctx context.Context, fw *systemv1alpha1.FirmwareUpdate) (bool, error) {
	isProgressing := func() (bool, error) {
		if fw.Status.State != systemv1alpha1.FirmwareUpdateStateInProgress {
			return false, nil
		}
		if fw.Spec.ServerRef != nil {
			if _, err := utils.GetServerByName(ctx, r.Client, fw.Spec.ServerRef.Name); apierrors.IsNotFound(err) {
				return false, nil
			}
		}
		if fw.Status.ServerMaintenanceRef == nil {
			return false, nil
		}
		return utils.IsAnyServerMaintenanceActive(ctx, r.Client, []metalv1alpha1.ObjectReference{*fw.Status.ServerMaintenanceRef})
	}
	return utils.ShouldProceedWithDeletion(ctx, fw, FirmwareUpdateFinalizer, isProgressing)
}

func (r *FirmwareUpdateReconciler) delete(ctx context.Context, fw *systemv1alpha1.FirmwareUpdate) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("Deleting FirmwareUpdate")
	defer log.V(1).Info("Deleted FirmwareUpdate")

	if !controllerutil.ContainsFinalizer(fw, FirmwareUpdateFinalizer) {
		return ctrl.Result{}, nil
	}

	log.V(1).Info("Ensuring that the finalizer is removed")
	if modified, err := clientutils.PatchEnsureNoFinalizer(ctx, r.Client, fw, FirmwareUpdateFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *FirmwareUpdateReconciler) cleanupServerMaintenanceReferences(ctx context.Context, fw *systemv1alpha1.FirmwareUpdate) error {
	log := ctrl.LoggerFrom(ctx)
	if fw.Status.ServerMaintenanceRef == nil {
		return nil
	}

	serverMaintenance, err := r.getServerMaintenanceForRef(ctx, fw.Status.ServerMaintenanceRef)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to get referred ServerMaintenance: %w", err)
	}

	if serverMaintenance.DeletionTimestamp.IsZero() {
		if metav1.IsControlledBy(serverMaintenance, fw) {
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
		log.V(1).Info("Cleaning up ServerMaintenance ref in FirmwareUpdate as the object is gone")
		if err := r.patchServerMaintenanceRef(ctx, fw, nil); err != nil {
			return fmt.Errorf("failed to clean up serverMaintenance ref in FirmwareUpdate status: %w", err)
		}
	}
	return nil
}

func (r *FirmwareUpdateReconciler) reconcile(ctx context.Context, fw *systemv1alpha1.FirmwareUpdate) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	if utils.ShouldIgnoreReconciliation(fw) {
		log.V(1).Info("Skipped FirmwareUpdate reconciliation")
		return ctrl.Result{}, nil
	}

	if modified, err := clientutils.PatchEnsureFinalizer(ctx, r.Client, fw, FirmwareUpdateFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}

	requeue, err := r.transitionState(ctx, fw)
	if err != nil {
		return ctrl.Result{}, err
	}
	if requeue {
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}

	log.V(1).Info("Reconciled FirmwareUpdate")
	return ctrl.Result{}, nil
}

func (r *FirmwareUpdateReconciler) transitionState(ctx context.Context, fw *systemv1alpha1.FirmwareUpdate) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if fw.Spec.ServerRef == nil {
		return false, fmt.Errorf("FirmwareUpdate does not have a ServerRef")
	}

	server, err := utils.GetServerByName(ctx, r.Client, fw.Spec.ServerRef.Name)
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

	h, ok := vendorHandlers[server.Status.Manufacturer]
	if !ok {
		if server.Status.Manufacturer == "" {
			log.V(1).Info("Server manufacturer not yet discovered, requeuing", "Server", server.Name)
			return true, nil
		}
		return false, r.failUnsupportedVendor(ctx, fw, server)
	}

	switch fw.Status.State {
	case "", systemv1alpha1.FirmwareUpdateStatePending:
		// remove the retry annotation if it's present as we are retrying now
		if utils.ShouldRetryReconciliation(fw) {
			fwBase := fw.DeepCopy()
			annotations := fw.GetAnnotations()
			delete(annotations, constants.OperationAnnotation)
			fw.SetAnnotations(annotations)
			if err := r.Patch(ctx, fw, client.MergeFrom(fwBase)); err != nil {
				return true, fmt.Errorf("failed to patch FirmwareUpdate for retrying: %w", err)
			}
			log.V(1).Info("Removed retry annotation from FirmwareUpdate for retrying", "FirmwareUpdate", fw.Annotations)
			return false, nil
		}
		return h.handlePending(ctx, fw, bmcClient, r, server)
	case systemv1alpha1.FirmwareUpdateStateCompleted:
		if deleted, err := r.handleTTL(ctx, fw); err != nil || deleted {
			return false, err
		}
		return h.handleCompleted(ctx, fw, bmcClient, r, server)
	case systemv1alpha1.FirmwareUpdateStateInProgress:
		if exceeded, err := r.checkProgressDeadline(ctx, fw); err != nil || exceeded {
			return false, err
		}
		return h.handleInProgress(ctx, fw, bmcClient, r, server)
	case systemv1alpha1.FirmwareUpdateStateFailed:
		return r.processFailedState(ctx, fw)
	}

	log.V(1).Info("Unknown State found", "State", fw.Status.State)
	return false, nil
}

// failUnsupportedVendor transitions the FirmwareUpdate to Failed with an
// UnsupportedVendor condition when the server's manufacturer string is not
// present in vendorHandlers.
func (r *FirmwareUpdateReconciler) failUnsupportedVendor(ctx context.Context, fw *systemv1alpha1.FirmwareUpdate, server *metalv1alpha1.Server) error {
	condition, err := utils.GetCondition(r.Conditions, fw.Status.Conditions, ConditionUnsupportedVendor)
	if err != nil {
		return err
	}
	if err := r.Conditions.Update(
		condition,
		conditionutils.UpdateStatus(corev1.ConditionTrue),
		conditionutils.UpdateReason(ReasonUnsupportedVendor),
		conditionutils.UpdateMessage(fmt.Sprintf("Vendor %q is not supported", server.Status.Manufacturer)),
	); err != nil {
		return fmt.Errorf("failed to update UnsupportedVendor condition: %w", err)
	}
	return r.updateStatus(ctx, fw, systemv1alpha1.FirmwareUpdateStateFailed, condition)
}

func (r *FirmwareUpdateReconciler) handleServerMaintenance(ctx context.Context, bmcClient bmc.BMC, fw *systemv1alpha1.FirmwareUpdate, server *metalv1alpha1.Server) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if fw.Status.ServerMaintenanceRef == nil {
		if requeue, err := r.requestServerMaintenance(ctx, fw, server); err != nil || requeue {
			return false, err
		}
	}

	condition, err := utils.GetCondition(r.Conditions, fw.Status.Conditions, constants.ConditionServerMaintenanceWaiting)
	if err != nil {
		return false, err
	}

	active, err := utils.IsAnyServerMaintenanceActive(ctx, r.Client, []metalv1alpha1.ObjectReference{*fw.Status.ServerMaintenanceRef})
	if err != nil {
		return false, fmt.Errorf("failed to check maintenance state: %w", err)
	}
	if !active {
		log.V(1).Info("Server is not in maintenance, waiting", "Server", server.Name)
		if condition.Status != metav1.ConditionTrue {
			if err := r.Conditions.Update(
				condition,
				conditionutils.UpdateStatus(corev1.ConditionTrue),
				conditionutils.UpdateReason(constants.ReasonMaintenanceWaiting),
				conditionutils.UpdateMessage(fmt.Sprintf("Waiting for approval of %v", fw.Status.ServerMaintenanceRef.Name)),
			); err != nil {
				return false, fmt.Errorf("failed to update creating ServerMaintenance condition: %w", err)
			}
			if err := r.updateStatus(ctx, fw, fw.Status.State, condition); err != nil {
				return false, fmt.Errorf("failed to patch FirmwareUpdate ServerMaintenance waiting conditions: %w", err)
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
		if err := r.updateStatus(ctx, fw, fw.Status.State, condition); err != nil {
			return false, fmt.Errorf("failed to patch FirmwareUpdate ServerMaintenance waiting conditions: %w", err)
		}
		return false, nil
	}

	if ok, err := r.handleBMCReset(ctx, bmcClient, fw, server); !ok || err != nil {
		return false, err
	}
	return true, nil
}

func (r *FirmwareUpdateReconciler) handleBMCReset(
	ctx context.Context,
	bmcClient bmc.BMC,
	fw *systemv1alpha1.FirmwareUpdate,
	server *metalv1alpha1.Server,
) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	// reset BMC if not already done
	resetBMC, err := utils.GetCondition(r.Conditions, fw.Status.Conditions, constants.ConditionResetIssued)
	if err != nil {
		return false, fmt.Errorf("failed to get condition for reset of BMC of server: %w", err)
	}

	if resetBMC.Status != metav1.ConditionTrue {
		// once the server is in maintenance, reset the BMC to make sure its in stable state
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
				return false, r.updateStatus(ctx, fw, fw.Status.State, resetBMC)
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
		return false, r.updateStatus(ctx, fw, fw.Status.State, resetBMC)
	}
	return true, nil
}

// checkProgressDeadline fails the FirmwareUpdate if no progress was observed within ProgressDeadlineSeconds.
func (r *FirmwareUpdateReconciler) checkProgressDeadline(ctx context.Context, fw *systemv1alpha1.FirmwareUpdate) (bool, error) {
	if fw.Spec.ProgressDeadlineSeconds == nil || fw.Status.LastProgressTime == nil {
		return false, nil
	}
	deadline := time.Duration(*fw.Spec.ProgressDeadlineSeconds) * time.Second
	if time.Since(fw.Status.LastProgressTime.Time) <= deadline {
		return false, nil
	}
	condition, err := utils.GetCondition(r.Conditions, fw.Status.Conditions, constants.ConditionProgressDeadlineExceeded)
	if err != nil {
		return false, err
	}
	if err := r.Conditions.Update(
		condition,
		conditionutils.UpdateStatus(corev1.ConditionTrue),
		conditionutils.UpdateReason(constants.ReasonProgressDeadlineExceeded),
		conditionutils.UpdateMessage(fmt.Sprintf("No progress observed within %ds", *fw.Spec.ProgressDeadlineSeconds)),
	); err != nil {
		return false, fmt.Errorf("failed to update ProgressDeadlineExceeded condition: %w", err)
	}
	return true, r.updateStatus(ctx, fw, systemv1alpha1.FirmwareUpdateStateFailed, condition)
}

// handleTTL deletes a completed FirmwareUpdate if TTLSecondsAfterFinished has elapsed.
// Returns true if the object was deleted.
func (r *FirmwareUpdateReconciler) handleTTL(ctx context.Context, fw *systemv1alpha1.FirmwareUpdate) (bool, error) {
	if fw.Spec.TTLSecondsAfterFinished == nil {
		return false, nil
	}
	if fw.Status.State != systemv1alpha1.FirmwareUpdateStateCompleted {
		return false, nil
	}
	// Use LastProgressTime as a proxy for completion time since we don't have a dedicated field.
	if fw.Status.LastProgressTime == nil {
		return false, nil
	}
	ttl := time.Duration(*fw.Spec.TTLSecondsAfterFinished) * time.Second
	if time.Since(fw.Status.LastProgressTime.Time) < ttl {
		return false, nil
	}
	if err := r.Delete(ctx, fw); err != nil {
		return false, fmt.Errorf("failed to delete FirmwareUpdate after TTL: %w", err)
	}
	return true, nil
}

func (r *FirmwareUpdateReconciler) processFailedState(ctx context.Context, fw *systemv1alpha1.FirmwareUpdate) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if utils.ShouldRetryReconciliation(fw) {
		log.V(1).Info("Retrying FirmwareUpdate as per annotation")
		fwBase := fw.DeepCopy()
		fw.Status.FailedAttempts = 0
		fw.Status.State = systemv1alpha1.FirmwareUpdateStatePending
		fw.Status.ObservedGeneration = fw.Generation
		fw.Status.CheckJob = nil
		fw.Status.UpdateJob = nil
		fw.Status.ComponentJobs = nil
		fw.Status.BaselineJobIDs = nil
		fw.Status.BaselineJobsCaptured = false
		fw.Status.LastProgressTime = nil
		fw.Status.PassCount = 0
		annotations := fw.GetAnnotations()
		retryCondition, err := utils.GetCondition(r.Conditions, fw.Status.Conditions, constants.ConditionRetryOfFailedResourceIssued)
		if err != nil {
			return true, fmt.Errorf("failed to get retry condition for FirmwareUpdate: %w", err)
		}
		// update only once
		if retryCondition.Status != metav1.ConditionTrue {
			err := r.Conditions.Update(retryCondition,
				conditionutils.UpdateStatus(metav1.ConditionTrue),
				conditionutils.UpdateReason(constants.ReasonRetryOfFailedResourceIssued),
				conditionutils.UpdateMessage(annotations[constants.OperationAnnotation]),
			)
			if err != nil {
				return true, fmt.Errorf("failed to update retry condition for FirmwareUpdate: %w", err)
			}
		}
		fw.Status.Conditions = []metav1.Condition{*retryCondition}
		if err := r.Status().Patch(ctx, fw, client.MergeFrom(fwBase)); err != nil {
			return true, fmt.Errorf("failed to patch FirmwareUpdate status for retrying: %w", err)
		}
		return true, nil
	}
	var maxAttempts int32
	if fw.Spec.RetryPolicy != nil && fw.Spec.RetryPolicy.MaxAttempts != nil {
		// if RetryPolicy is given (even if MaxAttempts is 0), do not use the default value.
		maxAttempts = *fw.Spec.RetryPolicy.MaxAttempts
	} else if r.DefaultFailedAutoRetryCount > 0 {
		// set the retry to this, if the optional RetryPolicy is not given and default retry count is set on the reconciler.
		maxAttempts = r.DefaultFailedAutoRetryCount
	}
	if maxAttempts > 0 {
		if fw.Status.ObservedGeneration != fw.Generation {
			// if the generation has changed, it means the spec has been updated after the failure, we can reset the retry count and retry.
			fw.Status.FailedAttempts = 0
		}
		if fw.Status.FailedAttempts < maxAttempts {
			log.V(1).Info("Retrying FirmwareUpdate automatically", "FailedAttempts", fw.Status.FailedAttempts)
			fwBase := fw.DeepCopy()
			fw.Status.State = systemv1alpha1.FirmwareUpdateStatePending
			fw.Status.ObservedGeneration = fw.Generation
			fw.Status.CheckJob = nil
			fw.Status.UpdateJob = nil
			fw.Status.ComponentJobs = nil
			fw.Status.BaselineJobIDs = nil
			fw.Status.BaselineJobsCaptured = false
			fw.Status.LastProgressTime = nil
			fw.Status.PassCount = 0
			retryCondition, err := utils.GetCondition(r.Conditions, fw.Status.Conditions, constants.ConditionRetryOfFailedResourceIssued)
			if err != nil {
				return true, fmt.Errorf("failed to get Retry condition for FirmwareUpdate: %w", err)
			}
			if retryCondition.Status == metav1.ConditionTrue {
				// keep the condition if it's already true,
				// otherwise SET resource will patch the retry annotation again.
				fw.Status.Conditions = []metav1.Condition{*retryCondition}
			} else {
				fw.Status.Conditions = nil
			}
			fw.Status.FailedAttempts++
			if err := r.Status().Patch(ctx, fw, client.MergeFrom(fwBase)); err != nil {
				return true, fmt.Errorf("failed to patch FirmwareUpdate status for auto-retrying: %w", err)
			}
			return true, nil
		}
	}

	// Keep status consistent when retries are disabled or exhausted.
	if fw.Status.FailedAttempts != 0 &&
		(maxAttempts == 0 || fw.Status.ObservedGeneration != fw.Generation) {
		fwBase := fw.DeepCopy()
		fw.Status.FailedAttempts = 0
		fw.Status.ObservedGeneration = fw.Generation
		if err := r.Status().Patch(ctx, fw, client.MergeFrom(fwBase)); err != nil {
			return true, fmt.Errorf("failed to patch FirmwareUpdate status for disabled auto-retry: %w", err)
		}
	}
	log.V(1).Info("Failed to apply firmware update", "FirmwareUpdate", fw.Name, "Status", fw.Status)
	return false, nil
}

func (r *FirmwareUpdateReconciler) getServerMaintenanceForRef(ctx context.Context, serverMaintenanceRef *metalv1alpha1.ObjectReference) (*maintenancev1alpha1.ServerMaintenance, error) {
	if serverMaintenanceRef == nil {
		return nil, fmt.Errorf("server maintenance reference is nil")
	}

	serverMaintenance := &maintenancev1alpha1.ServerMaintenance{}
	if err := r.Get(ctx, client.ObjectKey{Name: serverMaintenanceRef.Name, Namespace: r.ManagerNamespace}, serverMaintenance); err != nil {
		return serverMaintenance, err
	}

	return serverMaintenance, nil
}

// updateStatus patches the top-level State and, if condition is non-nil,
// merges it into the conditions slice. If condition is nil, this represents a
// transition to a new top-level phase (Pending->InProgress, ->Completed): all
// conditions and per-pass job-tracking fields from the previous phase are
// wiped since they no longer apply.
func (r *FirmwareUpdateReconciler) updateStatus(
	ctx context.Context,
	fw *systemv1alpha1.FirmwareUpdate,
	state systemv1alpha1.FirmwareUpdateState,
	condition *metav1.Condition,
) error {
	fwBase := fw.DeepCopy()
	fw.Status.State = state
	fw.Status.ObservedGeneration = fw.Generation

	if condition != nil {
		if err := r.Conditions.UpdateSlice(
			&fw.Status.Conditions,
			condition.Type,
			conditionutils.UpdateStatus(condition.Status),
			conditionutils.UpdateReason(condition.Reason),
			conditionutils.UpdateMessage(condition.Message),
		); err != nil {
			return fmt.Errorf("failed to patch FirmwareUpdate condition: %w", err)
		}
	} else {
		fw.Status.Conditions = []metav1.Condition{}
		fw.Status.CheckJob = nil
		fw.Status.UpdateJob = nil
		fw.Status.ComponentJobs = nil
		fw.Status.BaselineJobIDs = nil
		fw.Status.BaselineJobsCaptured = false
		fw.Status.LastProgressTime = nil
		fw.Status.PassCount = 0
	}

	if err := r.Status().Patch(ctx, fw, client.MergeFrom(fwBase)); err != nil {
		return fmt.Errorf("failed to patch FirmwareUpdate status: %w", err)
	}

	return nil
}

// patchProgress patches the top-level State, optionally merges condition into
// the conditions slice (preserving all other conditions), and applies mutate
// to update job-tracking fields (CheckJob/UpdateJob/ComponentJobs/BaselineJobIDs)
// - all in a single status patch.
func (r *FirmwareUpdateReconciler) patchProgress(
	ctx context.Context,
	fw *systemv1alpha1.FirmwareUpdate,
	state systemv1alpha1.FirmwareUpdateState,
	condition *metav1.Condition,
	mutate func(*systemv1alpha1.FirmwareUpdateStatus),
) error {
	fwBase := fw.DeepCopy()
	fw.Status.State = state
	fw.Status.ObservedGeneration = fw.Generation

	if condition != nil {
		if err := r.Conditions.UpdateSlice(
			&fw.Status.Conditions,
			condition.Type,
			conditionutils.UpdateStatus(condition.Status),
			conditionutils.UpdateReason(condition.Reason),
			conditionutils.UpdateMessage(condition.Message),
		); err != nil {
			return fmt.Errorf("failed to patch FirmwareUpdate condition: %w", err)
		}
	}

	if mutate != nil {
		mutate(&fw.Status)
	}

	now := metav1.Now()
	fw.Status.LastProgressTime = &now

	if err := r.Status().Patch(ctx, fw, client.MergeFrom(fwBase)); err != nil {
		return fmt.Errorf("failed to patch FirmwareUpdate status: %w", err)
	}

	return nil
}

func (r *FirmwareUpdateReconciler) patchServerMaintenanceRef(ctx context.Context, fw *systemv1alpha1.FirmwareUpdate, serverMaintenance *maintenancev1alpha1.ServerMaintenance) error {
	fwBase := fw.DeepCopy()

	if serverMaintenance == nil {
		fw.Status.ServerMaintenanceRef = nil
	} else {
		fw.Status.ServerMaintenanceRef = &metalv1alpha1.ObjectReference{
			Namespace: serverMaintenance.Namespace,
			Name:      serverMaintenance.Name,
		}
	}

	if err := r.Status().Patch(ctx, fw, client.MergeFrom(fwBase)); err != nil {
		return err
	}

	return nil
}

func (r *FirmwareUpdateReconciler) requestServerMaintenance(ctx context.Context, fw *systemv1alpha1.FirmwareUpdate, server *metalv1alpha1.Server) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if fw.Status.ServerMaintenanceRef != nil {
		if _, err := utils.GetServerMaintenanceForObjectReference(ctx, r.Client, fw.Status.ServerMaintenanceRef); apierrors.IsNotFound(err) {
			log.V(1).Info("Referenced ServerMaintenance no longer exists, clearing ref to allow re-creation")
			if err = r.patchServerMaintenanceRef(ctx, fw, nil); err != nil {
				return false, fmt.Errorf("failed to clear stale ServerMaintenance ref: %w", err)
			}
			return true, nil // requeue to re-create
		} else if err != nil {
			return false, fmt.Errorf("failed to verify ServerMaintenance existence: %w", err)
		}
		condition, err := utils.GetCondition(r.Conditions, fw.Status.Conditions, constants.ConditionServerMaintenanceCreated)
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
			conditionutils.UpdateMessage(fmt.Sprintf("Created/Present %v at %v", fw.Status.ServerMaintenanceRef.Name, time.Now())),
		); err != nil {
			return false, fmt.Errorf("failed to update creating ServerMaintenance condition: %w", err)
		}
		if err := r.updateStatus(ctx, fw, fw.Status.State, condition); err != nil {
			return false, fmt.Errorf("failed to patch FirmwareUpdate conditions: %w", err)
		}
		return true, nil
	}

	serverMaintenance := &maintenancev1alpha1.ServerMaintenance{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: r.ManagerNamespace,
			Name:      fw.Name,
		},
	}

	opResult, err := controllerutil.CreateOrPatch(ctx, r.Client, serverMaintenance, func() error {
		if fw.Spec.ServerMaintenancePolicy != nil {
			serverMaintenance.Spec.Policy = *fw.Spec.ServerMaintenancePolicy
		}
		serverMaintenance.Spec.ServerRef = &corev1.LocalObjectReference{Name: server.Name}
		if serverMaintenance.Status.State != maintenancev1alpha1.ServerMaintenanceStateInMaintenance && serverMaintenance.Status.State != "" {
			serverMaintenance.Status.State = ""
		}
		return controllerutil.SetControllerReference(fw, serverMaintenance, r.Client.Scheme())
	})
	if err != nil {
		return false, fmt.Errorf("failed to create or patch serverMaintenance: %w", err)
	}
	log.V(1).Info("Created ServerMaintenance", "ServerMaintenance", client.ObjectKeyFromObject(serverMaintenance), "Operation", opResult)

	if err = r.patchServerMaintenanceRef(ctx, fw, serverMaintenance); err != nil {
		return false, fmt.Errorf("failed to patch ServerMaintenance ref in FirmwareUpdate status: %w", err)
	}

	log.V(1).Info("Patched ServerMaintenance on FirmwareUpdate")
	return true, nil
}

func (r *FirmwareUpdateReconciler) enqueueFirmwareUpdateByServerRefs(ctx context.Context, obj client.Object) []ctrl.Request {
	log := ctrl.LoggerFrom(ctx)
	host := obj.(*metalv1alpha1.Server)

	// don't requeue if host in wrong state
	if host.Status.State == metalv1alpha1.ServerStateDiscovery ||
		host.Status.State == metalv1alpha1.ServerStateError ||
		host.Status.State == metalv1alpha1.ServerStateInitial {
		return nil
	}

	// don't requeue if host has no active ServerMaintenance
	maintenanceList := &maintenancev1alpha1.ServerMaintenanceList{}
	if err := r.List(ctx, maintenanceList, client.MatchingFields{constants.ServerRefField: host.Name}); err != nil {
		log.Error(err, "Failed to list ServerMaintenances for server", "Server", host.Name)
		return nil
	}
	activeMaintNames := make(map[string]struct{})
	for _, sm := range maintenanceList.Items {
		if sm.Status.State == maintenancev1alpha1.ServerMaintenanceStateInMaintenance {
			activeMaintNames[sm.Name] = struct{}{}
		}
	}
	if len(activeMaintNames) == 0 {
		return nil
	}

	fwUpdateList := &systemv1alpha1.FirmwareUpdateList{}
	if err := r.List(ctx, fwUpdateList); err != nil {
		log.Error(err, "Failed to list FirmwareUpdateList")
		return nil
	}

	reqs := make([]ctrl.Request, 0)
	for _, fwUpdate := range fwUpdateList.Items {
		if fwUpdate.Spec.ServerRef == nil || fwUpdate.Spec.ServerRef.Name != host.Name {
			continue
		}
		// states where we do not need to requeue for host changes
		if fwUpdate.Status.ServerMaintenanceRef == nil ||
			fwUpdate.Status.State == systemv1alpha1.FirmwareUpdateStateCompleted ||
			fwUpdate.Status.State == systemv1alpha1.FirmwareUpdateStateFailed {
			continue
		}
		if _, ok := activeMaintNames[fwUpdate.Status.ServerMaintenanceRef.Name]; !ok {
			continue
		}
		reqs = append(reqs, ctrl.Request{
			NamespacedName: types.NamespacedName{Namespace: fwUpdate.Namespace, Name: fwUpdate.Name},
		})
	}
	return reqs
}

func (r *FirmwareUpdateReconciler) enqueueFirmwareUpdateByBMC(ctx context.Context, obj client.Object) []ctrl.Request {
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

	fwUpdateList := &systemv1alpha1.FirmwareUpdateList{}
	if err := clientutils.ListAndFilter(ctx, r.Client, fwUpdateList, func(object client.Object) (bool, error) {
		fwUpdate := object.(*systemv1alpha1.FirmwareUpdate)
		if fwUpdate.Spec.ServerRef == nil {
			return false, nil
		}
		if _, exists := serverMap[fwUpdate.Spec.ServerRef.Name]; !exists {
			return false, nil
		}
		return true, nil
	}); err != nil {
		log.Error(err, "Failed to list FirmwareUpdate objects created by this BMC resource", "BMC", bmcObj.Name)
		return nil
	}

	reqs := make([]ctrl.Request, 0)
	for _, fwUpdate := range fwUpdateList.Items {
		if fwUpdate.Status.State == systemv1alpha1.FirmwareUpdateStateInProgress {
			resetBMC, err := utils.GetCondition(r.Conditions, fwUpdate.Status.Conditions, constants.ConditionResetIssued)
			if err != nil {
				log.Error(err, "Failed to get reset BMC condition")
				continue
			}
			if resetBMC.Status == metav1.ConditionTrue {
				continue
			}
			// enqueue only if the BMC reset is requested for this BMC
			if resetBMC.Reason == constants.ReasonResetIssued {
				reqs = append(reqs, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: fwUpdate.Namespace, Name: fwUpdate.Name}})
			}
		}
	}
	return reqs
}

// SetupWithManager sets up the controller with the Manager.
func (r *FirmwareUpdateReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&systemv1alpha1.FirmwareUpdate{}).
		Owns(&maintenancev1alpha1.ServerMaintenance{}).
		Watches(&metalv1alpha1.Server{}, handler.EnqueueRequestsFromMapFunc(r.enqueueFirmwareUpdateByServerRefs)).
		Watches(&metalv1alpha1.BMC{}, handler.EnqueueRequestsFromMapFunc(r.enqueueFirmwareUpdateByBMC)).
		Complete(r)
}
