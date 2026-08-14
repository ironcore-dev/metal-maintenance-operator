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
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
)

// FirmwareUpdateDellReconciler reconciles a FirmwareUpdateDell object.
const (
	FirmwareUpdateDellFinalizer = "system.metal.ironcore.dev/firmwareupdatedell"

	// ConditionRepositoryCheckIssued/Completed track the dry-run
	// (ApplyUpdate=false) InstallFromRepository call used to discover whether
	// any packages in the configured catalog are pending installation.
	ConditionRepositoryCheckIssued    = "RepositoryCheckIssued"
	ConditionRepositoryCheckCompleted = "RepositoryCheckCompleted"

	// ConditionRepositoryUpdateIssued/Completed track the apply
	// (ApplyUpdate=true) InstallFromRepository call that actually installs the
	// pending packages.
	ConditionRepositoryUpdateIssued    = "RepositoryUpdateIssued"
	ConditionRepositoryUpdateCompleted = "RepositoryUpdateCompleted"

	// ConditionComponentJobsCompleted tracks the per-component iDRAC jobs
	// spawned by the apply call.
	ConditionComponentJobsCompleted = "ComponentJobsCompleted"

	ReasonRepositoryCheckIssued     = "RepositoryCheckIssuedToBMC"
	ReasonRepositoryCheckCompleted  = "RepositoryCheckCompleted"
	ReasonRepositoryCheckFailed     = "RepositoryCheckFailed"
	ReasonRepositoryUpdateIssued    = "RepositoryUpdateIssuedToBMC"
	ReasonRepositoryUpdateCompleted = "RepositoryUpdateCompleted"
	ReasonRepositoryUpdateFailed    = "RepositoryUpdateFailed"
	ReasonComponentJobsCompleted    = "ComponentJobsCompleted"
	ReasonComponentJobFailed        = "ComponentJobFailed"
)

type FirmwareUpdateDellReconciler struct {
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
	// FirmwareUpdateDell is marked Failed instead.
	MaxRepositoryPasses int32
}

// +kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=firmwareupdatedells,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=firmwareupdatedells/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=firmwareupdatedells/finalizers,verbs=update
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=servermaintenances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=servermaintenances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *FirmwareUpdateDellReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	fwUpdate := &systemv1alpha1.FirmwareUpdateDell{}
	if err := r.Get(ctx, req.NamespacedName, fwUpdate); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.V(1).Info("Reconciling FirmwareUpdateDell")

	return r.reconcileExists(ctx, fwUpdate)
}

func (r *FirmwareUpdateDellReconciler) reconcileExists(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateDell) (ctrl.Result, error) {
	ok, err := r.shouldDelete(ctx, fwUpdate)
	if err != nil {
		return ctrl.Result{}, err
	}
	if ok {
		return r.delete(ctx, fwUpdate)
	}
	return r.reconcile(ctx, fwUpdate)
}

func (r *FirmwareUpdateDellReconciler) shouldDelete(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateDell) (bool, error) {
	isProgressing := func() (bool, error) {
		if fwUpdate.Status.State != systemv1alpha1.FirmwareUpdateDellStateInProgress {
			return false, nil
		}
		if fwUpdate.Spec.ServerRef != nil {
			if _, err := utils.GetServerByName(ctx, r.Client, fwUpdate.Spec.ServerRef.Name); apierrors.IsNotFound(err) {
				return false, nil
			}
		}
		if fwUpdate.Spec.ServerMaintenanceRef == nil {
			return false, nil
		}
		return utils.IsAnyServerMaintenanceActive(ctx, r.Client, []metalv1alpha1.ObjectReference{*fwUpdate.Spec.ServerMaintenanceRef})
	}
	return utils.ShouldProceedWithDeletion(ctx, fwUpdate, FirmwareUpdateDellFinalizer, isProgressing)
}

func (r *FirmwareUpdateDellReconciler) delete(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateDell) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("Deleting FirmwareUpdateDell")
	defer log.V(1).Info("Deleted FirmwareUpdateDell")

	if !controllerutil.ContainsFinalizer(fwUpdate, FirmwareUpdateDellFinalizer) {
		return ctrl.Result{}, nil
	}

	log.V(1).Info("Ensuring that the finalizer is removed")
	if modified, err := clientutils.PatchEnsureNoFinalizer(ctx, r.Client, fwUpdate, FirmwareUpdateDellFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *FirmwareUpdateDellReconciler) cleanupServerMaintenanceReferences(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateDell) error {
	log := ctrl.LoggerFrom(ctx)
	if fwUpdate.Spec.ServerMaintenanceRef == nil {
		return nil
	}

	serverMaintenance, err := r.getServerMaintenanceForRef(ctx, fwUpdate.Spec.ServerMaintenanceRef)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to get referred ServerMaintenance: %w", err)
	}

	if serverMaintenance.DeletionTimestamp.IsZero() {
		if metav1.IsControlledBy(serverMaintenance, fwUpdate) {
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
		log.V(1).Info("Cleaning up ServerMaintenance ref in FirmwareUpdateDell as the object is gone")
		if err := r.patchServerMaintenanceRef(ctx, fwUpdate, nil); err != nil {
			return fmt.Errorf("failed to clean up serverMaintenance ref in FirmwareUpdateDell status: %w", err)
		}
	}
	return nil
}

func (r *FirmwareUpdateDellReconciler) reconcile(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateDell) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	if utils.ShouldIgnoreReconciliation(fwUpdate) {
		log.V(1).Info("Skipped FirmwareUpdateDell reconciliation")
		return ctrl.Result{}, nil
	}

	if modified, err := clientutils.PatchEnsureFinalizer(ctx, r.Client, fwUpdate, FirmwareUpdateDellFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}

	requeue, err := r.transitionState(ctx, fwUpdate)
	if err != nil {
		return ctrl.Result{}, err
	}
	if requeue {
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}

	log.V(1).Info("Reconciled FirmwareUpdateDell")
	return ctrl.Result{}, nil
}

func (r *FirmwareUpdateDellReconciler) transitionState(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateDell) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if fwUpdate.Spec.ServerRef == nil {
		return false, fmt.Errorf("FirmwareUpdateDell does not have a ServerRef")
	}

	server, err := utils.GetServerByName(ctx, r.Client, fwUpdate.Spec.ServerRef.Name)
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

	updater, ok := bmcClient.(bmc.FirmwareUpdaterDell)
	if !ok {
		return false, fmt.Errorf("repository-based firmware update not supported by this vendor: %w", bmc.ErrNotSupported)
	}

	switch fwUpdate.Status.State {
	case "", systemv1alpha1.FirmwareUpdateDellStatePending:
		// remove the retry annotation if it's present as we are retrying now
		if utils.ShouldRetryReconciliation(fwUpdate) {
			fwUpdateBase := fwUpdate.DeepCopy()
			annotations := fwUpdate.GetAnnotations()
			delete(annotations, constants.OperationAnnotation)
			fwUpdate.SetAnnotations(annotations)
			if err := r.Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase)); err != nil {
				return true, fmt.Errorf("failed to patch FirmwareUpdateDell for retrying: %w", err)
			}
			log.V(1).Info("Removed retry annotation from FirmwareUpdateDell for retrying", "FirmwareUpdateDell", fwUpdate.Annotations)
			return false, nil
		}
		return r.processRepositoryCheck(ctx, updater, fwUpdate, server)
	case systemv1alpha1.FirmwareUpdateDellStateCompleted:
		return r.processRepositoryCheck(ctx, updater, fwUpdate, server)
	case systemv1alpha1.FirmwareUpdateDellStateInProgress:
		return r.processInProgress(ctx, bmcClient, updater, fwUpdate, server)
	case systemv1alpha1.FirmwareUpdateDellStateFailed:
		return r.processFailedState(ctx, fwUpdate, server)
	}

	log.V(1).Info("Unknown State found", "State", fwUpdate.Status.State)
	return false, nil
}

func (r *FirmwareUpdateDellReconciler) handleServerMaintenance(ctx context.Context, bmcClient bmc.BMC, fwUpdate *systemv1alpha1.FirmwareUpdateDell, server *metalv1alpha1.Server) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if fwUpdate.Spec.ServerMaintenanceRef == nil {
		if requeue, err := r.requestServerMaintenance(ctx, fwUpdate, server); err != nil || requeue {
			return false, err
		}
	}

	condition, err := utils.GetCondition(r.Conditions, fwUpdate.Status.Conditions, constants.ConditionServerMaintenanceWaiting)
	if err != nil {
		return false, err
	}

	if server.Status.State != metalv1alpha1.ServerStateMaintenance {
		log.V(1).Info("Server is not in maintenance, waiting", "ServerState", server.Status.State, "Server", server.Name)
		if condition.Status != metav1.ConditionTrue {
			if err := r.Conditions.Update(
				condition,
				conditionutils.UpdateStatus(corev1.ConditionTrue),
				conditionutils.UpdateReason(constants.ReasonMaintenanceWaiting),
				conditionutils.UpdateMessage(fmt.Sprintf("Waiting for approval of %v", fwUpdate.Spec.ServerMaintenanceRef.Name)),
			); err != nil {
				return false, fmt.Errorf("failed to update creating ServerMaintenance condition: %w", err)
			}
			if err := r.updateStatus(ctx, fwUpdate, fwUpdate.Status.State, condition); err != nil {
				return false, fmt.Errorf("failed to patch FirmwareUpdateDell ServerMaintenance waiting conditions: %w", err)
			}
		}
		return false, nil
	}

	if server.Spec.ServerMaintenanceRef == nil || server.Spec.ServerMaintenanceRef.Name != fwUpdate.Spec.ServerMaintenanceRef.Name || server.Spec.ServerMaintenanceRef.Namespace != fwUpdate.Spec.ServerMaintenanceRef.Namespace {
		log.V(1).Info("Server is already in maintenance", "Server", server.Name)
		if condition.Status != metav1.ConditionTrue {
			if err := r.Conditions.Update(
				condition,
				conditionutils.UpdateStatus(corev1.ConditionTrue),
				conditionutils.UpdateReason(constants.ReasonMaintenanceWaiting),
				conditionutils.UpdateMessage(fmt.Sprintf("Waiting for approval of %v", fwUpdate.Spec.ServerMaintenanceRef.Name)),
			); err != nil {
				return false, fmt.Errorf("failed to update creating ServerMaintenance condition: %w", err)
			}
			if err := r.updateStatus(ctx, fwUpdate, fwUpdate.Status.State, condition); err != nil {
				return false, fmt.Errorf("failed to patch FirmwareUpdateDell ServerMaintenance waiting conditions: %w", err)
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
		if err := r.updateStatus(ctx, fwUpdate, fwUpdate.Status.State, condition); err != nil {
			return false, fmt.Errorf("failed to patch FirmwareUpdateDell ServerMaintenance waiting conditions: %w", err)
		}
		return false, nil
	}

	if ok, err := r.handleBMCReset(ctx, bmcClient, fwUpdate, server); !ok || err != nil {
		return false, err
	}
	return true, nil
}

func (r *FirmwareUpdateDellReconciler) handleBMCReset(
	ctx context.Context,
	bmcClient bmc.BMC,
	fwUpdate *systemv1alpha1.FirmwareUpdateDell,
	server *metalv1alpha1.Server,
) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	// reset BMC if not already done
	resetBMC, err := utils.GetCondition(r.Conditions, fwUpdate.Status.Conditions, constants.ConditionResetIssued)
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
				return false, r.updateStatus(ctx, fwUpdate, fwUpdate.Status.State, resetBMC)
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
		return false, r.updateStatus(ctx, fwUpdate, fwUpdate.Status.State, resetBMC)
	}
	return true, nil
}

// processRepositoryCheck drives the read-only dry-run RepositoryCheck used
// while the FirmwareUpdateDell is Pending (first-time check) or Completed
// (periodic drift-detection). The check is a plain Redfish call that neither
// changes the system nor requires a reboot, so it is safe to issue without
// ever requesting ServerMaintenance. Only once the check confirms packages
// are actually pending installation does this transition into InProgress,
// where the update is actually applied.
func (r *FirmwareUpdateDellReconciler) processRepositoryCheck(ctx context.Context, updater bmc.FirmwareUpdaterDell, fwUpdate *systemv1alpha1.FirmwareUpdateDell, server *metalv1alpha1.Server) (bool, error) {
	checkIssued, err := utils.GetCondition(r.Conditions, fwUpdate.Status.Conditions, ConditionRepositoryCheckIssued)
	if err != nil {
		return false, err
	}
	if checkIssued.Status != metav1.ConditionTrue {
		return r.issueRepositoryCheck(ctx, updater, fwUpdate, server, checkIssued)
	}

	checkCompleted, err := utils.GetCondition(r.Conditions, fwUpdate.Status.Conditions, ConditionRepositoryCheckCompleted)
	if err != nil {
		return false, err
	}
	return r.pollRepositoryCheck(ctx, updater, fwUpdate, server, checkCompleted)
}

// processInProgress drives the actual repository-based firmware update once
// processRepositoryCheck has confirmed packages are pending installation. It
// requests/waits for ServerMaintenance, resets the BMC, applies the update,
// and tracks the component jobs it spawns. Once the apply completes, control
// is handed back to Completed, whose dry-run check confirms convergence (or
// discovers further pending packages and re-enters InProgress).
func (r *FirmwareUpdateDellReconciler) processInProgress(ctx context.Context, bmcClient bmc.BMC, updater bmc.FirmwareUpdaterDell, fwUpdate *systemv1alpha1.FirmwareUpdateDell, server *metalv1alpha1.Server) (bool, error) {
	if ok, err := r.handleServerMaintenance(ctx, bmcClient, fwUpdate, server); err != nil || !ok {
		return false, err
	}

	updateIssued, err := utils.GetCondition(r.Conditions, fwUpdate.Status.Conditions, ConditionRepositoryUpdateIssued)
	if err != nil {
		return false, err
	}
	if updateIssued.Status != metav1.ConditionTrue {
		return r.issueRepositoryUpdate(ctx, updater, fwUpdate, server, updateIssued)
	}

	updateCompleted, err := utils.GetCondition(r.Conditions, fwUpdate.Status.Conditions, ConditionRepositoryUpdateCompleted)
	if err != nil {
		return false, err
	}
	if updateCompleted.Status != metav1.ConditionTrue {
		return r.pollRepositoryUpdate(ctx, updater, fwUpdate, server, updateCompleted)
	}

	componentsCompleted, err := utils.GetCondition(r.Conditions, fwUpdate.Status.Conditions, ConditionComponentJobsCompleted)
	if err != nil {
		return false, err
	}
	if componentsCompleted.Status != metav1.ConditionTrue {
		return r.trackComponentJobs(ctx, updater, fwUpdate, componentsCompleted)
	}

	// This pass's apply and component-job tracking are done. Hand back to
	// Completed: its dry-run check is what actually re-verifies convergence
	// (and re-enters InProgress, bounded by MaxRepositoryPasses, if further
	// packages are found pending).
	ctrl.LoggerFrom(ctx).V(1).Info("Repository update pass completed, handing back to Completed for re-verification", "Server", server.Name)
	fwUpdateBase := fwUpdate.DeepCopy()
	fwUpdate.Status.State = systemv1alpha1.FirmwareUpdateDellStateCompleted
	fwUpdate.Status.ObservedGeneration = fwUpdate.Generation
	fwUpdate.Status.Conditions = []metav1.Condition{}
	fwUpdate.Status.CheckJob = nil
	fwUpdate.Status.UpdateJob = nil
	fwUpdate.Status.ComponentJobs = nil
	fwUpdate.Status.BaselineJobIDs = nil
	// PassCount is intentionally preserved (not reset) here: it is only reset
	// once a repository check actually confirms convergence (no packages
	// pending), so persistently-pending catalogs remain bounded by
	// MaxRepositoryPasses across multiple apply attempts.
	return false, r.Status().Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase))
}

func (r *FirmwareUpdateDellReconciler) issueRepositoryCheck(ctx context.Context, updater bmc.FirmwareUpdaterDell, fwUpdate *systemv1alpha1.FirmwareUpdateDell, server *metalv1alpha1.Server, condition *metav1.Condition) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	parameters, err := r.buildRepositoryParameters(ctx, fwUpdate, false)
	if err != nil {
		return false, fmt.Errorf("failed to build repository parameters: %w", err)
	}

	jobID, isFatal, err := updater.InstallFirmwareFromRepository(ctx, server.Spec.SystemURI, parameters)
	if err != nil {
		if isFatal {
			log.Error(err, "Failed to issue repository check", "Server", server.Name)
			if condErr := r.Conditions.Update(
				condition,
				conditionutils.UpdateStatus(corev1.ConditionFalse),
				conditionutils.UpdateReason(ReasonRepositoryCheckFailed),
				conditionutils.UpdateMessage(fmt.Sprintf("Failed to issue repository check: %v", err)),
			); condErr != nil {
				return false, errors.Join(err, condErr)
			}
			return false, r.updateStatus(ctx, fwUpdate, systemv1alpha1.FirmwareUpdateDellStateFailed, condition)
		}
		return false, err
	}

	if err := r.Conditions.Update(
		condition,
		conditionutils.UpdateStatus(corev1.ConditionTrue),
		conditionutils.UpdateReason(ReasonRepositoryCheckIssued),
		conditionutils.UpdateMessage(fmt.Sprintf("Issued repository check job %v", jobID)),
	); err != nil {
		return false, fmt.Errorf("failed to update RepositoryCheckIssued condition: %w", err)
	}

	return false, r.patchProgress(ctx, fwUpdate, fwUpdate.Status.State, condition, func(status *systemv1alpha1.FirmwareUpdateDellStatus) {
		status.CheckJob = &systemv1alpha1.RepositoryJob{JobID: jobID}
	})
}

func (r *FirmwareUpdateDellReconciler) pollRepositoryCheck(ctx context.Context, updater bmc.FirmwareUpdaterDell, fwUpdate *systemv1alpha1.FirmwareUpdateDell, server *metalv1alpha1.Server, condition *metav1.Condition) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if fwUpdate.Status.CheckJob == nil || fwUpdate.Status.CheckJob.JobID == "" {
		return false, fmt.Errorf("missing check job ID while polling repository check")
	}

	job, err := updater.GetJob(ctx, "", fwUpdate.Status.CheckJob.JobID)
	if err != nil {
		log.V(1).Info("Failed to fetch repository check job, retrying", "error", err)
		return true, nil
	}
	repoJob := toRepositoryJob(job)

	if !job.IsTerminal() {
		return true, r.patchProgress(ctx, fwUpdate, fwUpdate.Status.State, nil, func(status *systemv1alpha1.FirmwareUpdateDellStatus) {
			status.CheckJob = &repoJob
		})
	}

	if job.IsFailed() {
		if err := r.Conditions.Update(
			condition,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason(ReasonRepositoryCheckFailed),
			conditionutils.UpdateMessage(fmt.Sprintf("Repository check job failed: %v", job.Message)),
		); err != nil {
			return false, fmt.Errorf("failed to update RepositoryCheckCompleted condition: %w", err)
		}
		return false, r.patchProgress(ctx, fwUpdate, systemv1alpha1.FirmwareUpdateDellStateFailed, condition, func(status *systemv1alpha1.FirmwareUpdateDellStatus) {
			status.CheckJob = &repoJob
		})
	}

	hasPending, _, err := updater.GetRepositoryUpdateList(ctx, server.Spec.SystemURI)
	if err != nil {
		log.V(1).Info("Failed to fetch repository update list, retrying", "error", err)
		return true, nil
	}

	if !hasPending {
		log.V(1).Info("Repository-based firmware update up to date", "Server", server.Name)
		if err := r.cleanupServerMaintenanceReferences(ctx, fwUpdate); err != nil {
			return false, err
		}
		// Record the successful convergence explicitly (mirroring the
		// RepositoryUpdate success path) instead of silently wiping
		// conditions, so `kubectl describe` shows why/when the last check
		// concluded rather than just the bare Completed state.
		if err := r.Conditions.Update(
			condition,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason(ReasonRepositoryCheckCompleted),
			conditionutils.UpdateMessage("Repository check found no packages pending installation"),
		); err != nil {
			return false, fmt.Errorf("failed to update RepositoryCheckCompleted condition: %w", err)
		}
		fwUpdateBase := fwUpdate.DeepCopy()
		fwUpdate.Status.State = systemv1alpha1.FirmwareUpdateDellStateCompleted
		fwUpdate.Status.ObservedGeneration = fwUpdate.Generation
		// Conditions are reset to only this one (rather than merged via
		// patchProgress) because the stale RepositoryCheckIssued condition
		// must not survive: processRepositoryCheck keys off it to decide
		// whether to issue a fresh check on the next periodic drift-check
		// reconcile of the Completed state.
		fwUpdate.Status.Conditions = []metav1.Condition{*condition}
		fwUpdate.Status.CheckJob = nil
		fwUpdate.Status.UpdateJob = nil
		fwUpdate.Status.ComponentJobs = nil
		fwUpdate.Status.ComponentJobsSummary = nil
		fwUpdate.Status.BaselineJobIDs = nil
		fwUpdate.Status.PassCount = 0
		return false, r.Status().Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase))
	}

	// Packages are pending installation: bound how many times we allow a
	// check to (re-)discover pending packages before giving up.
	passCount := fwUpdate.Status.PassCount + 1
	if r.MaxRepositoryPasses > 0 && passCount > r.MaxRepositoryPasses {
		log.Info("Exceeded maximum repository update passes, marking as Failed", "PassCount", passCount, "MaxRepositoryPasses", r.MaxRepositoryPasses)
		if err := r.Conditions.Update(
			condition,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason(ReasonRepositoryCheckFailed),
			conditionutils.UpdateMessage(fmt.Sprintf("Exceeded maximum of %d repository update passes", r.MaxRepositoryPasses)),
		); err != nil {
			return false, fmt.Errorf("failed to update RepositoryCheckCompleted condition: %w", err)
		}
		fwUpdateBase := fwUpdate.DeepCopy()
		fwUpdate.Status.State = systemv1alpha1.FirmwareUpdateDellStateFailed
		fwUpdate.Status.ObservedGeneration = fwUpdate.Generation
		fwUpdate.Status.PassCount = passCount
		fwUpdate.Status.CheckJob = &repoJob
		fwUpdate.Status.Conditions = []metav1.Condition{*condition}
		return false, r.Status().Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase))
	}

	// The dry-run check confirmed packages are pending installation: hand off
	// to InProgress, which is where ServerMaintenance is requested and the
	// update is actually applied. Check-phase conditions/job-tracking are
	// wiped since they no longer apply once InProgress takes over.
	log.V(1).Info("Repository check found pending packages, entering InProgress", "Server", server.Name, "PassCount", passCount)
	fwUpdateBase := fwUpdate.DeepCopy()
	fwUpdate.Status.State = systemv1alpha1.FirmwareUpdateDellStateInProgress
	fwUpdate.Status.ObservedGeneration = fwUpdate.Generation
	fwUpdate.Status.PassCount = passCount
	fwUpdate.Status.Conditions = []metav1.Condition{}
	fwUpdate.Status.CheckJob = nil
	return false, r.Status().Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase))
}

func (r *FirmwareUpdateDellReconciler) issueRepositoryUpdate(ctx context.Context, updater bmc.FirmwareUpdaterDell, fwUpdate *systemv1alpha1.FirmwareUpdateDell, server *metalv1alpha1.Server, condition *metav1.Condition) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	// Snapshot the jobs known to the BMC before issuing the apply call, so
	// newly spawned component jobs can be discovered by diffing against this
	// baseline once the apply job itself completes.
	if len(fwUpdate.Status.BaselineJobIDs) == 0 {
		jobIDs, err := updater.ListJobs(ctx, "")
		if err != nil {
			log.V(1).Info("Failed to list jobs for baseline snapshot, retrying", "error", err)
			return true, nil
		}
		if jobIDs == nil {
			jobIDs = []string{}
		}
		return true, r.patchProgress(ctx, fwUpdate, fwUpdate.Status.State, nil, func(status *systemv1alpha1.FirmwareUpdateDellStatus) {
			status.BaselineJobIDs = jobIDs
		})
	}

	parameters, err := r.buildRepositoryParameters(ctx, fwUpdate, true)
	if err != nil {
		return false, fmt.Errorf("failed to build repository parameters: %w", err)
	}

	jobID, isFatal, err := updater.InstallFirmwareFromRepository(ctx, server.Spec.SystemURI, parameters)
	if err != nil {
		if isFatal {
			log.Error(err, "Failed to issue repository update", "Server", server.Name)
			if condErr := r.Conditions.Update(
				condition,
				conditionutils.UpdateStatus(corev1.ConditionFalse),
				conditionutils.UpdateReason(ReasonRepositoryUpdateFailed),
				conditionutils.UpdateMessage(fmt.Sprintf("Failed to issue repository update: %v", err)),
			); condErr != nil {
				return false, errors.Join(err, condErr)
			}
			return false, r.updateStatus(ctx, fwUpdate, systemv1alpha1.FirmwareUpdateDellStateFailed, condition)
		}
		return false, err
	}

	if err := r.Conditions.Update(
		condition,
		conditionutils.UpdateStatus(corev1.ConditionTrue),
		conditionutils.UpdateReason(ReasonRepositoryUpdateIssued),
		conditionutils.UpdateMessage(fmt.Sprintf("Issued repository update job %v", jobID)),
	); err != nil {
		return false, fmt.Errorf("failed to update RepositoryUpdateIssued condition: %w", err)
	}

	return false, r.patchProgress(ctx, fwUpdate, fwUpdate.Status.State, condition, func(status *systemv1alpha1.FirmwareUpdateDellStatus) {
		status.UpdateJob = &systemv1alpha1.RepositoryJob{JobID: jobID}
	})
}

func (r *FirmwareUpdateDellReconciler) pollRepositoryUpdate(ctx context.Context, updater bmc.FirmwareUpdaterDell, fwUpdate *systemv1alpha1.FirmwareUpdateDell, server *metalv1alpha1.Server, condition *metav1.Condition) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if fwUpdate.Status.UpdateJob == nil || fwUpdate.Status.UpdateJob.JobID == "" {
		return false, fmt.Errorf("missing update job ID while polling repository update")
	}

	job, err := updater.GetJob(ctx, "", fwUpdate.Status.UpdateJob.JobID)
	if err != nil {
		log.V(1).Info("Failed to fetch repository update job, retrying", "error", err)
		return true, nil
	}
	repoJob := toRepositoryJob(job)

	if !job.IsTerminal() {
		return true, r.patchProgress(ctx, fwUpdate, fwUpdate.Status.State, nil, func(status *systemv1alpha1.FirmwareUpdateDellStatus) {
			status.UpdateJob = &repoJob
		})
	}

	if job.IsFailed() {
		if err := r.Conditions.Update(
			condition,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason(ReasonRepositoryUpdateFailed),
			conditionutils.UpdateMessage(fmt.Sprintf("Repository update job failed: %v", job.Message)),
		); err != nil {
			return false, fmt.Errorf("failed to update RepositoryUpdateCompleted condition: %w", err)
		}
		return false, r.patchProgress(ctx, fwUpdate, systemv1alpha1.FirmwareUpdateDellStateFailed, condition, func(status *systemv1alpha1.FirmwareUpdateDellStatus) {
			status.UpdateJob = &repoJob
		})
	}

	if err := r.Conditions.Update(
		condition,
		conditionutils.UpdateStatus(corev1.ConditionTrue),
		conditionutils.UpdateReason(ReasonRepositoryUpdateCompleted),
		conditionutils.UpdateMessage("Repository update job completed"),
	); err != nil {
		return false, fmt.Errorf("failed to update RepositoryUpdateCompleted condition: %w", err)
	}
	return false, r.patchProgress(ctx, fwUpdate, fwUpdate.Status.State, condition, func(status *systemv1alpha1.FirmwareUpdateDellStatus) {
		status.UpdateJob = &repoJob
	})
}

func (r *FirmwareUpdateDellReconciler) trackComponentJobs(ctx context.Context, updater bmc.FirmwareUpdaterDell, fwUpdate *systemv1alpha1.FirmwareUpdateDell, condition *metav1.Condition) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	jobIDs, err := updater.ListJobs(ctx, "")
	if err != nil {
		log.V(1).Info("Failed to list jobs for component tracking, retrying", "error", err)
		return true, nil
	}

	known := make(map[string]struct{}, len(fwUpdate.Status.BaselineJobIDs)+1)
	for _, id := range fwUpdate.Status.BaselineJobIDs {
		known[id] = struct{}{}
	}
	if fwUpdate.Status.UpdateJob != nil {
		known[fwUpdate.Status.UpdateJob.JobID] = struct{}{}
	}

	componentJobs := make([]systemv1alpha1.RepositoryJob, 0, len(jobIDs))
	summary := &systemv1alpha1.ComponentJobsSummary{}
	allTerminal := true
	anyFailed := false
	for _, id := range jobIDs {
		if _, ok := known[id]; ok {
			continue
		}
		job, err := updater.GetJob(ctx, "", id)
		if err != nil {
			log.V(1).Info("Failed to fetch component job, retrying", "JobID", id, "error", err)
			return true, nil
		}
		componentJobs = append(componentJobs, toRepositoryJob(job))
		summary.Total++
		switch {
		case job.IsFailed():
			anyFailed = true
			summary.Failed++
		case job.IsCompleted():
			summary.Completed++
		default:
			summary.InProgress++
		}
		if !job.IsTerminal() {
			allTerminal = false
		}
	}

	if !allTerminal {
		return true, r.patchProgress(ctx, fwUpdate, fwUpdate.Status.State, nil, func(status *systemv1alpha1.FirmwareUpdateDellStatus) {
			status.ComponentJobs = componentJobs
			status.ComponentJobsSummary = summary
		})
	}

	if anyFailed {
		if err := r.Conditions.Update(
			condition,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason(ReasonComponentJobFailed),
			conditionutils.UpdateMessage("One or more component firmware jobs failed"),
		); err != nil {
			return false, fmt.Errorf("failed to update ComponentJobsCompleted condition: %w", err)
		}
		return false, r.patchProgress(ctx, fwUpdate, systemv1alpha1.FirmwareUpdateDellStateFailed, condition, func(status *systemv1alpha1.FirmwareUpdateDellStatus) {
			status.ComponentJobs = componentJobs
			status.ComponentJobsSummary = summary
		})
	}

	if err := r.Conditions.Update(
		condition,
		conditionutils.UpdateStatus(corev1.ConditionTrue),
		conditionutils.UpdateReason(ReasonComponentJobsCompleted),
		conditionutils.UpdateMessage("All component firmware jobs completed"),
	); err != nil {
		return false, fmt.Errorf("failed to update ComponentJobsCompleted condition: %w", err)
	}
	return false, r.patchProgress(ctx, fwUpdate, fwUpdate.Status.State, condition, func(status *systemv1alpha1.FirmwareUpdateDellStatus) {
		status.ComponentJobs = componentJobs
		status.ComponentJobsSummary = summary
	})
}

func (r *FirmwareUpdateDellReconciler) processFailedState(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateDell, server *metalv1alpha1.Server) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if utils.ShouldRetryReconciliation(fwUpdate) {
		log.V(1).Info("Retrying FirmwareUpdateDell as per annotation")
		fwUpdateBase := fwUpdate.DeepCopy()
		fwUpdate.Status.FailedAttempts = 0
		fwUpdate.Status.State = systemv1alpha1.FirmwareUpdateDellStatePending
		fwUpdate.Status.ObservedGeneration = fwUpdate.Generation
		fwUpdate.Status.CheckJob = nil
		fwUpdate.Status.UpdateJob = nil
		fwUpdate.Status.ComponentJobs = nil
		fwUpdate.Status.BaselineJobIDs = nil
		fwUpdate.Status.PassCount = 0
		annotations := fwUpdate.GetAnnotations()
		retryCondition, err := utils.GetCondition(r.Conditions, fwUpdate.Status.Conditions, constants.ConditionRetryOfFailedResourceIssued)
		if err != nil {
			return true, fmt.Errorf("failed to get retry condition for FirmwareUpdateDell: %w", err)
		}
		// update only once
		if retryCondition.Status != metav1.ConditionTrue {
			err := r.Conditions.Update(retryCondition,
				conditionutils.UpdateStatus(metav1.ConditionTrue),
				conditionutils.UpdateReason(constants.ReasonRetryOfFailedResourceIssued),
				conditionutils.UpdateMessage(annotations[constants.OperationAnnotation]),
			)
			if err != nil {
				return true, fmt.Errorf("failed to update retry condition for FirmwareUpdateDell: %w", err)
			}
		}
		fwUpdate.Status.Conditions = []metav1.Condition{*retryCondition}
		if err := r.Status().Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase)); err != nil {
			return true, fmt.Errorf("failed to patch FirmwareUpdateDell status for retrying: %w", err)
		}
		return true, nil
	}
	var maxAttempts int32
	if fwUpdate.Spec.RetryPolicy != nil && fwUpdate.Spec.RetryPolicy.MaxAttempts != nil {
		// if RetryPolicy is given (even if MaxAttempts is 0), do not use the default value.
		maxAttempts = *fwUpdate.Spec.RetryPolicy.MaxAttempts
	} else if r.DefaultFailedAutoRetryCount > 0 {
		// set the retry to this, if the optional RetryPolicy is not given and default retry count is set on the reconciler.
		maxAttempts = r.DefaultFailedAutoRetryCount
	}
	if maxAttempts > 0 {
		if fwUpdate.Status.ObservedGeneration != fwUpdate.Generation {
			// if the generation has changed, it means the spec has been updated after the failure, we can reset the retry count and retry.
			fwUpdate.Status.FailedAttempts = 0
		}
		if fwUpdate.Status.FailedAttempts < maxAttempts {
			log.V(1).Info("Retrying FirmwareUpdateDell automatically", "FailedAttempts", fwUpdate.Status.FailedAttempts)
			fwUpdateBase := fwUpdate.DeepCopy()
			fwUpdate.Status.State = systemv1alpha1.FirmwareUpdateDellStatePending
			fwUpdate.Status.ObservedGeneration = fwUpdate.Generation
			fwUpdate.Status.CheckJob = nil
			fwUpdate.Status.UpdateJob = nil
			fwUpdate.Status.ComponentJobs = nil
			fwUpdate.Status.BaselineJobIDs = nil
			fwUpdate.Status.PassCount = 0
			retryCondition, err := utils.GetCondition(r.Conditions, fwUpdate.Status.Conditions, constants.ConditionRetryOfFailedResourceIssued)
			if err != nil {
				return true, fmt.Errorf("failed to get Retry condition for FirmwareUpdateDell: %w", err)
			}
			if retryCondition.Status == metav1.ConditionTrue {
				// keep the condition if it's already true,
				// otherwise SET resource will patch the retry annotation again.
				fwUpdate.Status.Conditions = []metav1.Condition{*retryCondition}
			} else {
				fwUpdate.Status.Conditions = nil
			}
			fwUpdate.Status.FailedAttempts++
			if err := r.Status().Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase)); err != nil {
				return true, fmt.Errorf("failed to patch FirmwareUpdateDell status for auto-retrying: %w", err)
			}
			return true, nil
		}
	}

	// Keep status consistent when retries are disabled or exhausted.
	if fwUpdate.Status.FailedAttempts != 0 &&
		(maxAttempts == 0 || fwUpdate.Status.ObservedGeneration != fwUpdate.Generation) {
		fwUpdateBase := fwUpdate.DeepCopy()
		fwUpdate.Status.FailedAttempts = 0
		fwUpdate.Status.ObservedGeneration = fwUpdate.Generation
		if err := r.Status().Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase)); err != nil {
			return true, fmt.Errorf("failed to patch FirmwareUpdateDell status for disabled auto-retry: %w", err)
		}
	}
	log.V(1).Info("Failed to apply repository-based firmware update", "FirmwareUpdateDell", fwUpdate.Name, "Status", fwUpdate.Status, "Server", server.Name)
	return false, nil
}

func (r *FirmwareUpdateDellReconciler) getServerMaintenanceForRef(ctx context.Context, serverMaintenanceRef *metalv1alpha1.ObjectReference) (*maintenancev1alpha1.ServerMaintenance, error) {
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
func (r *FirmwareUpdateDellReconciler) updateStatus(
	ctx context.Context,
	fwUpdate *systemv1alpha1.FirmwareUpdateDell,
	state systemv1alpha1.FirmwareUpdateDellState,
	condition *metav1.Condition,
) error {
	fwUpdateBase := fwUpdate.DeepCopy()
	fwUpdate.Status.State = state
	fwUpdate.Status.ObservedGeneration = fwUpdate.Generation

	if condition != nil {
		if err := r.Conditions.UpdateSlice(
			&fwUpdate.Status.Conditions,
			condition.Type,
			conditionutils.UpdateStatus(condition.Status),
			conditionutils.UpdateReason(condition.Reason),
			conditionutils.UpdateMessage(condition.Message),
		); err != nil {
			return fmt.Errorf("failed to patch FirmwareUpdateDell condition: %w", err)
		}
	} else {
		fwUpdate.Status.Conditions = []metav1.Condition{}
		fwUpdate.Status.CheckJob = nil
		fwUpdate.Status.UpdateJob = nil
		fwUpdate.Status.ComponentJobs = nil
		fwUpdate.Status.BaselineJobIDs = nil
		fwUpdate.Status.PassCount = 0
	}

	if err := r.Status().Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase)); err != nil {
		return fmt.Errorf("failed to patch FirmwareUpdateDell status: %w", err)
	}

	return nil
}

// patchProgress patches the top-level State, optionally merges condition into
// the conditions slice (preserving all other conditions), and applies mutate
// to update job-tracking fields (CheckJob/UpdateJob/ComponentJobs/BaselineJobIDs)
// - all in a single status patch.
func (r *FirmwareUpdateDellReconciler) patchProgress(
	ctx context.Context,
	fwUpdate *systemv1alpha1.FirmwareUpdateDell,
	state systemv1alpha1.FirmwareUpdateDellState,
	condition *metav1.Condition,
	mutate func(*systemv1alpha1.FirmwareUpdateDellStatus),
) error {
	fwUpdateBase := fwUpdate.DeepCopy()
	fwUpdate.Status.State = state
	fwUpdate.Status.ObservedGeneration = fwUpdate.Generation

	if condition != nil {
		if err := r.Conditions.UpdateSlice(
			&fwUpdate.Status.Conditions,
			condition.Type,
			conditionutils.UpdateStatus(condition.Status),
			conditionutils.UpdateReason(condition.Reason),
			conditionutils.UpdateMessage(condition.Message),
		); err != nil {
			return fmt.Errorf("failed to patch FirmwareUpdateDell condition: %w", err)
		}
	}

	if mutate != nil {
		mutate(&fwUpdate.Status)
	}

	if err := r.Status().Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase)); err != nil {
		return fmt.Errorf("failed to patch FirmwareUpdateDell status: %w", err)
	}

	return nil
}

func (r *FirmwareUpdateDellReconciler) patchServerMaintenanceRef(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateDell, serverMaintenance *maintenancev1alpha1.ServerMaintenance) error {
	fwUpdateBase := fwUpdate.DeepCopy()

	if serverMaintenance == nil {
		fwUpdate.Spec.ServerMaintenanceRef = nil
	} else {
		fwUpdate.Spec.ServerMaintenanceRef = &metalv1alpha1.ObjectReference{
			Namespace: serverMaintenance.Namespace,
			Name:      serverMaintenance.Name,
		}
	}

	if err := r.Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase)); err != nil {
		return err
	}

	return nil
}

func (r *FirmwareUpdateDellReconciler) requestServerMaintenance(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateDell, server *metalv1alpha1.Server) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if fwUpdate.Spec.ServerMaintenanceRef != nil {
		if _, err := utils.GetServerMaintenanceForObjectReference(ctx, r.Client, fwUpdate.Spec.ServerMaintenanceRef); apierrors.IsNotFound(err) {
			log.V(1).Info("Referenced ServerMaintenance no longer exists, clearing ref to allow re-creation")
			if err = r.patchServerMaintenanceRef(ctx, fwUpdate, nil); err != nil {
				return false, fmt.Errorf("failed to clear stale ServerMaintenance ref: %w", err)
			}
			return true, nil // requeue to re-create
		} else if err != nil {
			return false, fmt.Errorf("failed to verify ServerMaintenance existence: %w", err)
		}
		condition, err := utils.GetCondition(r.Conditions, fwUpdate.Status.Conditions, constants.ConditionServerMaintenanceCreated)
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
			conditionutils.UpdateMessage(fmt.Sprintf("Created/Present %v at %v", fwUpdate.Spec.ServerMaintenanceRef.Name, time.Now())),
		); err != nil {
			return false, fmt.Errorf("failed to update creating ServerMaintenance condition: %w", err)
		}
		if err := r.updateStatus(ctx, fwUpdate, fwUpdate.Status.State, condition); err != nil {
			return false, fmt.Errorf("failed to patch FirmwareUpdateDell conditions: %w", err)
		}
		return true, nil
	}

	serverMaintenance := &maintenancev1alpha1.ServerMaintenance{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: r.ManagerNamespace,
			Name:      fwUpdate.Name,
		},
	}

	opResult, err := controllerutil.CreateOrPatch(ctx, r.Client, serverMaintenance, func() error {
		if fwUpdate.Spec.ServerMaintenancePolicy != nil {
			serverMaintenance.Spec.Policy = *fwUpdate.Spec.ServerMaintenancePolicy
		}
		serverMaintenance.Spec.ServerRef = &corev1.LocalObjectReference{Name: server.Name}
		if serverMaintenance.Status.State != maintenancev1alpha1.ServerMaintenanceStateInMaintenance && serverMaintenance.Status.State != "" {
			serverMaintenance.Status.State = ""
		}
		return controllerutil.SetControllerReference(fwUpdate, serverMaintenance, r.Client.Scheme())
	})
	if err != nil {
		return false, fmt.Errorf("failed to create or patch serverMaintenance: %w", err)
	}
	log.V(1).Info("Created ServerMaintenance", "ServerMaintenance", client.ObjectKeyFromObject(serverMaintenance), "Operation", opResult)

	if err = r.patchServerMaintenanceRef(ctx, fwUpdate, serverMaintenance); err != nil {
		return false, fmt.Errorf("failed to patch ServerMaintenance ref in FirmwareUpdateDell status: %w", err)
	}

	log.V(1).Info("Patched ServerMaintenance on FirmwareUpdateDell")
	return true, nil
}

// buildRepositoryParameters translates the FirmwareUpdateDell's Repository
// spec (and, if configured, its Secret credentials) into bmc.RepositoryUpdateParameters.
func (r *FirmwareUpdateDellReconciler) buildRepositoryParameters(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateDell, applyUpdate bool) (*bmc.RepositoryUpdateParameters, error) {
	repo := fwUpdate.Spec.Repository

	var username, password string
	if repo.SecretRef != nil {
		var err error
		username, password, err = utils.GetImageCredentialsForSecretRef(ctx, r.Client, repo.SecretRef)
		if err != nil {
			return nil, fmt.Errorf("failed to get repository credentials: %w", err)
		}
	}

	catalogFile := repo.CatalogFile
	if catalogFile == "" {
		catalogFile = "Catalog.xml"
	}

	return &bmc.RepositoryUpdateParameters{
		ShareType:              string(repo.ShareType),
		IPAddress:              repo.Address,
		ShareName:              repo.ShareName,
		CatalogFile:            catalogFile,
		UserName:               username,
		Password:               password,
		Workgroup:              repo.Workgroup,
		IgnoreCertWarning:      ptr.Deref(repo.IgnoreCertWarning, false),
		ApplyUpdate:            applyUpdate,
		RebootNeeded:           applyUpdate,
		ApplySameVersions:      ptr.Deref(fwUpdate.Spec.ApplySameVersions, false),
		ApplyDowngradeVersions: ptr.Deref(fwUpdate.Spec.ApplyDowngradeVersions, false),
	}, nil
}

func toRepositoryJob(job *bmc.DellJob) systemv1alpha1.RepositoryJob {
	return systemv1alpha1.RepositoryJob{
		JobID:           job.ID,
		Name:            job.Name,
		JobType:         job.JobType,
		State:           job.State,
		Message:         job.Message,
		PercentComplete: job.PercentComplete,
	}
}

func (r *FirmwareUpdateDellReconciler) enqueueFirmwareUpdateDellByServerRefs(ctx context.Context, obj client.Object) []ctrl.Request {
	log := ctrl.LoggerFrom(ctx)
	host := obj.(*metalv1alpha1.Server)

	// don't requeue if host in wrong state
	if host.Status.State == metalv1alpha1.ServerStateDiscovery ||
		host.Status.State == metalv1alpha1.ServerStateError ||
		host.Status.State == metalv1alpha1.ServerStateInitial {
		return nil
	}

	// don't requeue if host does not have Maintenance
	if host.Spec.ServerMaintenanceRef == nil {
		return nil
	}

	fwUpdateList := &systemv1alpha1.FirmwareUpdateDellList{}
	if err := r.List(ctx, fwUpdateList); err != nil {
		log.Error(err, "Failed to list FirmwareUpdateDellList")
		return nil
	}

	for _, fwUpdate := range fwUpdateList.Items {
		if fwUpdate.Spec.ServerRef == nil || fwUpdate.Spec.ServerRef.Name != host.Name {
			continue
		}
		// states where we do not need to requeue for host changes
		if fwUpdate.Spec.ServerMaintenanceRef == nil ||
			fwUpdate.Status.State == systemv1alpha1.FirmwareUpdateDellStateCompleted ||
			fwUpdate.Status.State == systemv1alpha1.FirmwareUpdateDellStateFailed {
			return nil
		}
		if fwUpdate.Spec.ServerMaintenanceRef.Name != host.Spec.ServerMaintenanceRef.Name {
			return nil
		}
		return []ctrl.Request{{
			NamespacedName: types.NamespacedName{Namespace: fwUpdate.Namespace, Name: fwUpdate.Name},
		}}
	}
	return nil
}

func (r *FirmwareUpdateDellReconciler) enqueueFirmwareUpdateDellByBMC(ctx context.Context, obj client.Object) []ctrl.Request {
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

	fwUpdateList := &systemv1alpha1.FirmwareUpdateDellList{}
	if err := clientutils.ListAndFilter(ctx, r.Client, fwUpdateList, func(object client.Object) (bool, error) {
		fwUpdate := object.(*systemv1alpha1.FirmwareUpdateDell)
		if fwUpdate.Spec.ServerRef == nil {
			return false, nil
		}
		if _, exists := serverMap[fwUpdate.Spec.ServerRef.Name]; !exists {
			return false, nil
		}
		return true, nil
	}); err != nil {
		log.Error(err, "Failed to list FirmwareUpdateDell objects created by this BMC resource", "BMC", bmcObj.Name)
		return nil
	}

	reqs := make([]ctrl.Request, 0)
	for _, fwUpdate := range fwUpdateList.Items {
		if fwUpdate.Status.State == systemv1alpha1.FirmwareUpdateDellStateInProgress {
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
func (r *FirmwareUpdateDellReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&systemv1alpha1.FirmwareUpdateDell{}).
		Owns(&maintenancev1alpha1.ServerMaintenance{}).
		Watches(&metalv1alpha1.Server{}, handler.EnqueueRequestsFromMapFunc(r.enqueueFirmwareUpdateDellByServerRefs)).
		Watches(&metalv1alpha1.BMC{}, handler.EnqueueRequestsFromMapFunc(r.enqueueFirmwareUpdateDellByBMC)).
		Complete(r)
}
