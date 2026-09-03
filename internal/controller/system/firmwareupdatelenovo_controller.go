// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package system

import (
	"context"
	"fmt"
	"time"

	"github.com/ironcore-dev/controller-utils/clientutils"
	"github.com/ironcore-dev/controller-utils/conditionutils"
	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/maintenance/v1alpha1"
	systemv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/system/v1alpha1"
	constants "github.com/ironcore-dev/metal-maintenance-operator/internal/constants"
	utils "github.com/ironcore-dev/metal-maintenance-operator/internal/utils"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
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

// FirmwareUpdateLenovoReconciler reconciles a FirmwareUpdateLenovo object.
//
// This mirrors the Dell FirmwareUpdateDell controller (PR ironcore-dev/metal-maintenance-operator#170)
// but drives Lenovo XCC's OEM Redfish repository action instead of Dell's InstallFromRepository:
//
//	POST /redfish/v1/UpdateService/Actions/Oem/LenovoUpdateService.UpdateFromRepository { RepoURI, ... }
//	POST .../Oem/LenovoUpdateService.GetRepoUpdateDetail   (read-only dry-run)
//	POST .../Oem/LenovoUpdateService.BundleRollback         (rollback)
//
// The mechanism (device-verified) and the repository JSON layout are documented in:
// https://github.com/shyamsundart14/metal-maintenance-operator/blob/main/docs/lenovo-redfish-updatefromrepository.md
//
// NOTE: This is a scaffold/skeleton. The vendor-neutral state machine and the ServerMaintenance
// reboot-safety gating are wired up, but the Lenovo Redfish calls themselves are marked with
// TODO(lenovo) and are stubbed via the lenovoRepositoryUpdater interface below, which a
// metal-operator Lenovo BMC client must implement (mirroring bmc.FirmwareUpdaterDell).
const (
	FirmwareUpdateLenovoFinalizer = "system.metal.ironcore.dev/firmwareupdatelenovo"

	// ConditionRepositoryCheckIssued/Completed track the read-only dry-run
	// (GetRepoUpdateDetail) used to discover whether any packages in the
	// configured repository are pending installation.
	ConditionLenovoRepositoryCheckIssued    = "LenovoRepositoryCheckIssued"
	ConditionLenovoRepositoryCheckCompleted = "LenovoRepositoryCheckCompleted"

	// ConditionRepositoryUpdateIssued/Completed track the apply
	// (UpdateFromRepository) call that actually installs the pending packages.
	ConditionLenovoRepositoryUpdateIssued    = "LenovoRepositoryUpdateIssued"
	ConditionLenovoRepositoryUpdateCompleted = "LenovoRepositoryUpdateCompleted"

	ReasonLenovoRepositoryCheckIssued     = "LenovoRepositoryCheckIssuedToBMC"
	ReasonLenovoRepositoryCheckCompleted  = "LenovoRepositoryCheckCompleted"
	ReasonLenovoRepositoryCheckFailed     = "LenovoRepositoryCheckFailed"
	ReasonLenovoRepositoryUpdateIssued    = "LenovoRepositoryUpdateIssuedToBMC"
	ReasonLenovoRepositoryUpdateCompleted = "LenovoRepositoryUpdateCompleted"
	ReasonLenovoRepositoryUpdateFailed    = "LenovoRepositoryUpdateFailed"
)

// lenovoRepositoryUpdater is the Lenovo counterpart of metal-operator's bmc.FirmwareUpdaterDell.
// It is defined here so this controller compiles ahead of the metal-operator BMC client gaining
// Lenovo UpdateFromRepository support. Once metal-operator exposes a bmc.FirmwareUpdaterLenovo (or
// equivalent), this local interface should be replaced by a type assertion on the BMC client, and
// the stubbed methods in newLenovoRepositoryUpdater removed.
//
// TODO(lenovo): replace with a real metal-operator BMC client capability.
type lenovoRepositoryUpdater interface {
	// GetRepoUpdateDetail performs the read-only dry-run against RepoURI and reports whether any
	// applicable packages are pending (hasPending), the Task tracking the query (taskID), whether
	// the failure is fatal (isFatal), and any error.
	GetRepoUpdateDetail(ctx context.Context, systemURI string, params lenovoRepositoryParameters) (bool, string, bool, error)
	// UpdateFromRepository triggers the apply. XCC self-inventories the host and updates all
	// applicable components; the reboot is decided by XCC per component (no client reboot flag).
	// Returns the Task id, whether a failure is fatal, and any error.
	UpdateFromRepository(ctx context.Context, systemURI string, params lenovoRepositoryParameters) (string, bool, error)
	// GetTask returns the current state of a previously issued Redfish Task, plus whether it has
	// reached a terminal state and whether it failed.
	GetTask(ctx context.Context, systemURI, taskID string) (*systemv1alpha1.RepositoryTask, bool, bool, error)
}

// lenovoRepositoryParameters is the resolved input to the Lenovo OEM action (post secret lookup).
type lenovoRepositoryParameters struct {
	RepoURI      string
	RepoUserName string
	RepoPassword string
	RepoMountOpt string
	GroupRequest bool
}

// FirmwareUpdateLenovoReconciler reconciles a FirmwareUpdateLenovo object.
type FirmwareUpdateLenovoReconciler struct {
	client.Client
	ManagerNamespace            string
	DefaultProtocol             metalv1alpha1.ProtocolScheme
	SkipCertValidation          bool
	Scheme                      *runtime.Scheme
	ResyncInterval              time.Duration
	Conditions                  *conditionutils.Accessor
	DefaultFailedAutoRetryCount int32
	// MaxRepositoryPasses bounds how many times a dry-run repository check may find further packages
	// pending (and thus re-enter InProgress) before the FirmwareUpdateLenovo is marked Failed.
	MaxRepositoryPasses int32
}

// +kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=firmwareupdatelenovoes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=firmwareupdatelenovoes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=firmwareupdatelenovoes/finalizers,verbs=update
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=servermaintenances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=servermaintenances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to move the current state
// of the cluster closer to the desired state.
func (r *FirmwareUpdateLenovoReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	fwUpdate := &systemv1alpha1.FirmwareUpdateLenovo{}
	if err := r.Get(ctx, req.NamespacedName, fwUpdate); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.V(1).Info("Reconciling FirmwareUpdateLenovo")

	return r.reconcileExists(ctx, fwUpdate)
}

func (r *FirmwareUpdateLenovoReconciler) reconcileExists(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateLenovo) (ctrl.Result, error) {
	ok, err := r.shouldDelete(ctx, fwUpdate)
	if err != nil {
		return ctrl.Result{}, err
	}
	if ok {
		return r.delete(ctx, fwUpdate)
	}
	return r.reconcile(ctx, fwUpdate)
}

func (r *FirmwareUpdateLenovoReconciler) shouldDelete(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateLenovo) (bool, error) {
	isProgressing := func() (bool, error) {
		if fwUpdate.Status.State != systemv1alpha1.FirmwareUpdateLenovoStateInProgress {
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
	return utils.ShouldProceedWithDeletion(ctx, fwUpdate, FirmwareUpdateLenovoFinalizer, isProgressing)
}

func (r *FirmwareUpdateLenovoReconciler) delete(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateLenovo) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("Deleting FirmwareUpdateLenovo")
	defer log.V(1).Info("Deleted FirmwareUpdateLenovo")

	if !controllerutil.ContainsFinalizer(fwUpdate, FirmwareUpdateLenovoFinalizer) {
		return ctrl.Result{}, nil
	}

	// TODO(lenovo): optionally clean up the owned ServerMaintenance here (mirroring the Dell
	// controller's cleanupServerMaintenanceReferences), so an in-flight maintenance is released
	// when the FirmwareUpdateLenovo is deleted.

	log.V(1).Info("Ensuring that the finalizer is removed")
	if modified, err := clientutils.PatchEnsureNoFinalizer(ctx, r.Client, fwUpdate, FirmwareUpdateLenovoFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *FirmwareUpdateLenovoReconciler) reconcile(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateLenovo) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	if utils.ShouldIgnoreReconciliation(fwUpdate) {
		log.V(1).Info("Skipped FirmwareUpdateLenovo reconciliation")
		return ctrl.Result{}, nil
	}

	if modified, err := clientutils.PatchEnsureFinalizer(ctx, r.Client, fwUpdate, FirmwareUpdateLenovoFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}

	requeue, err := r.transitionState(ctx, fwUpdate)
	if err != nil {
		return ctrl.Result{}, err
	}
	if requeue {
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}

	log.V(1).Info("Reconciled FirmwareUpdateLenovo")
	return ctrl.Result{}, nil
}

// transitionState drives the vendor-neutral state machine, identical in shape to the Dell
// controller: the dry-run check runs ungated (Pending/Completed), and only the apply pass
// (InProgress) is gated on ServerMaintenance.
func (r *FirmwareUpdateLenovoReconciler) transitionState(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateLenovo) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if fwUpdate.Spec.ServerRef == nil {
		return false, fmt.Errorf("FirmwareUpdateLenovo does not have a ServerRef")
	}

	server, err := utils.GetServerByName(ctx, r.Client, fwUpdate.Spec.ServerRef.Name)
	if err != nil {
		return false, fmt.Errorf("failed to fetch server: %w", err)
	}

	// TODO(lenovo): obtain the Lenovo repository updater from the metal-operator BMC client, e.g.
	//   bmcClient, err := bmcutils.GetBMCClientForServer(ctx, r.Client, server, r.DefaultProtocol, r.SkipCertValidation, r.BMCOptions)
	//   updater, ok := bmcClient.(bmc.FirmwareUpdaterLenovo)  // once metal-operator exposes it
	// For now use the stub so the state machine is exercised end-to-end without a live XCC.
	updater := newLenovoRepositoryUpdater()

	switch fwUpdate.Status.State {
	case "", systemv1alpha1.FirmwareUpdateLenovoStatePending:
		if utils.ShouldRetryReconciliation(fwUpdate) {
			fwUpdateBase := fwUpdate.DeepCopy()
			annotations := fwUpdate.GetAnnotations()
			delete(annotations, constants.OperationAnnotation)
			fwUpdate.SetAnnotations(annotations)
			if err := r.Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase)); err != nil {
				return true, fmt.Errorf("failed to patch FirmwareUpdateLenovo for retrying: %w", err)
			}
			return false, nil
		}
		return r.processRepositoryCheck(ctx, updater, fwUpdate, server)
	case systemv1alpha1.FirmwareUpdateLenovoStateCompleted:
		return r.processRepositoryCheck(ctx, updater, fwUpdate, server)
	case systemv1alpha1.FirmwareUpdateLenovoStateInProgress:
		return r.processInProgress(ctx, updater, fwUpdate, server)
	case systemv1alpha1.FirmwareUpdateLenovoStateFailed:
		return r.processFailedState(ctx, fwUpdate, server)
	}

	log.V(1).Info("Unknown State found", "State", fwUpdate.Status.State)
	return false, nil
}

// handleServerMaintenance is the reboot-safety gate. It is vendor-neutral and mirrors the Dell
// controller exactly: request a ServerMaintenance if none, then refuse to proceed until the Server
// reaches Maintenance state (granted by the OwnerApproval/Enforced policy in metal-operator). This
// is what makes reboot handling workload-agnostic (ESXi / KVM / bare-metal K8s worker): whoever
// owns the ServerClaim drains the workload and sets metal.ironcore.dev/maintenance-approved.
func (r *FirmwareUpdateLenovoReconciler) handleServerMaintenance(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateLenovo, server *metalv1alpha1.Server) (bool, error) {
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

	// In this metal-operator version the "in maintenance" state is Parked, owned via an annotation
	// (ServerSpec.ServerMaintenanceRef / ServerStateMaintenance were removed, see metal-operator
	// PR #203 adaptation). Gate on the Server being Parked for *this* ServerMaintenance.
	ownerKey := utils.ServerMaintenanceOwnerKey(r.ManagerNamespace, fwUpdate.Spec.ServerMaintenanceRef.Name)
	if !utils.IsServerParkedForOwner(server, ownerKey) {
		log.V(1).Info("Server is not parked for maintenance, waiting", "ServerState", server.Status.State, "Server", server.Name)
		if condition.Status != metav1.ConditionTrue {
			if err := r.Conditions.Update(
				condition,
				conditionutils.UpdateStatus(corev1.ConditionTrue),
				conditionutils.UpdateReason(constants.ReasonMaintenanceWaiting),
				conditionutils.UpdateMessage(fmt.Sprintf("Waiting for approval of %v", fwUpdate.Spec.ServerMaintenanceRef.Name)),
			); err != nil {
				return false, fmt.Errorf("failed to update ServerMaintenance waiting condition: %w", err)
			}
			if err := r.updateStatus(ctx, fwUpdate, fwUpdate.Status.State, condition); err != nil {
				return false, fmt.Errorf("failed to patch FirmwareUpdateLenovo ServerMaintenance waiting conditions: %w", err)
			}
		}
		return false, nil
	}

	// TODO(lenovo): mirror the Dell controller's BMC-reset-to-stabilize step here
	// (handleBMCReset) once the Lenovo BMC client is wired in, to avoid XCCs that hang on
	// subsequent operations after entering maintenance.

	if condition.Reason != constants.ReasonMaintenanceApproved {
		if err := r.Conditions.Update(
			condition,
			conditionutils.UpdateStatus(corev1.ConditionFalse),
			conditionutils.UpdateReason(constants.ReasonMaintenanceApproved),
			conditionutils.UpdateMessage("Server is now in Maintenance mode"),
		); err != nil {
			return false, fmt.Errorf("failed to update ServerMaintenance approved condition: %w", err)
		}
		if err := r.updateStatus(ctx, fwUpdate, fwUpdate.Status.State, condition); err != nil {
			return false, fmt.Errorf("failed to patch FirmwareUpdateLenovo ServerMaintenance approved conditions: %w", err)
		}
		return false, nil
	}

	return true, nil
}

// processRepositoryCheck drives the read-only dry-run (GetRepoUpdateDetail) used while the
// FirmwareUpdateLenovo is Pending (first-time check) or Completed (periodic drift-detection). The
// check neither changes the system nor requires a reboot, so it is safe to issue without ever
// requesting ServerMaintenance. Only once the check confirms packages are pending does this
// transition into InProgress, where the update is actually applied.
func (r *FirmwareUpdateLenovoReconciler) processRepositoryCheck(ctx context.Context, updater lenovoRepositoryUpdater, fwUpdate *systemv1alpha1.FirmwareUpdateLenovo, server *metalv1alpha1.Server) (bool, error) {
	log := ctrl.LoggerFrom(ctx)

	params, err := r.buildRepositoryParameters(ctx, fwUpdate)
	if err != nil {
		return false, fmt.Errorf("failed to build repository parameters: %w", err)
	}

	// TODO(lenovo): drive this via a check-issued/check-completed condition pair and poll the Task,
	// mirroring the Dell controller's issueRepositoryCheck/pollRepositoryCheck. Kept as a single
	// call here for skeleton clarity.
	hasPending, taskID, isFatal, err := updater.GetRepoUpdateDetail(ctx, server.Spec.SystemURI, params)
	if err != nil {
		if isFatal {
			return false, r.failWith(ctx, fwUpdate, ConditionLenovoRepositoryCheckCompleted, ReasonLenovoRepositoryCheckFailed,
				fmt.Sprintf("Failed to issue repository check: %v", err))
		}
		return true, nil
	}

	if !hasPending {
		log.V(1).Info("Repository-based firmware update up to date", "Server", server.Name)
		fwUpdateBase := fwUpdate.DeepCopy()
		fwUpdate.Status.State = systemv1alpha1.FirmwareUpdateLenovoStateCompleted
		fwUpdate.Status.ObservedGeneration = fwUpdate.Generation
		fwUpdate.Status.CheckTask = &systemv1alpha1.RepositoryTask{TaskID: taskID}
		fwUpdate.Status.PassCount = 0
		return false, r.Status().Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase))
	}

	passCount := fwUpdate.Status.PassCount + 1
	if r.MaxRepositoryPasses > 0 && passCount > r.MaxRepositoryPasses {
		log.Info("Exceeded maximum repository update passes, marking as Failed", "PassCount", passCount)
		return false, r.failWith(ctx, fwUpdate, ConditionLenovoRepositoryCheckCompleted, ReasonLenovoRepositoryCheckFailed,
			fmt.Sprintf("Exceeded maximum of %d repository update passes", r.MaxRepositoryPasses))
	}

	log.V(1).Info("Repository check found pending packages, entering InProgress", "Server", server.Name, "PassCount", passCount)
	fwUpdateBase := fwUpdate.DeepCopy()
	fwUpdate.Status.State = systemv1alpha1.FirmwareUpdateLenovoStateInProgress
	fwUpdate.Status.ObservedGeneration = fwUpdate.Generation
	fwUpdate.Status.PassCount = passCount
	fwUpdate.Status.CheckTask = &systemv1alpha1.RepositoryTask{TaskID: taskID}
	fwUpdate.Status.Conditions = []metav1.Condition{}
	return false, r.Status().Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase))
}

// processInProgress drives the actual repository-based firmware update once the dry-run confirmed
// packages are pending. It gates on ServerMaintenance (reboot safety), then issues
// UpdateFromRepository and tracks its Task to completion, handing back to Completed for
// re-verification once done.
func (r *FirmwareUpdateLenovoReconciler) processInProgress(ctx context.Context, updater lenovoRepositoryUpdater, fwUpdate *systemv1alpha1.FirmwareUpdateLenovo, server *metalv1alpha1.Server) (bool, error) {
	log := ctrl.LoggerFrom(ctx)

	// GATE: do not touch the host until it is safe to reboot.
	if ok, err := r.handleServerMaintenance(ctx, fwUpdate, server); err != nil || !ok {
		return false, err
	}

	updateIssued, err := utils.GetCondition(r.Conditions, fwUpdate.Status.Conditions, ConditionLenovoRepositoryUpdateIssued)
	if err != nil {
		return false, err
	}

	if updateIssued.Status != metav1.ConditionTrue {
		params, err := r.buildRepositoryParameters(ctx, fwUpdate)
		if err != nil {
			return false, fmt.Errorf("failed to build repository parameters: %w", err)
		}
		// This POST is the reboot-causing apply. It only runs after the maintenance gate above.
		taskID, isFatal, err := updater.UpdateFromRepository(ctx, server.Spec.SystemURI, params)
		if err != nil {
			if isFatal {
				return false, r.failWith(ctx, fwUpdate, ConditionLenovoRepositoryUpdateCompleted, ReasonLenovoRepositoryUpdateFailed,
					fmt.Sprintf("Failed to issue repository update: %v", err))
			}
			return true, nil
		}
		if err := r.Conditions.Update(
			updateIssued,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason(ReasonLenovoRepositoryUpdateIssued),
			conditionutils.UpdateMessage(fmt.Sprintf("Issued UpdateFromRepository task %v", taskID)),
		); err != nil {
			return false, fmt.Errorf("failed to update RepositoryUpdateIssued condition: %w", err)
		}
		return false, r.patchProgress(ctx, fwUpdate, fwUpdate.Status.State, updateIssued, func(status *systemv1alpha1.FirmwareUpdateLenovoStatus) {
			status.UpdateTask = &systemv1alpha1.RepositoryTask{TaskID: taskID}
		})
	}

	// Poll the apply Task to completion.
	if fwUpdate.Status.UpdateTask == nil || fwUpdate.Status.UpdateTask.TaskID == "" {
		return false, fmt.Errorf("missing update task ID while polling repository update")
	}
	task, terminal, failed, err := updater.GetTask(ctx, server.Spec.SystemURI, fwUpdate.Status.UpdateTask.TaskID)
	if err != nil {
		log.V(1).Info("Failed to fetch repository update task, retrying", "error", err)
		return true, nil
	}
	if !terminal {
		return true, r.patchProgress(ctx, fwUpdate, fwUpdate.Status.State, nil, func(status *systemv1alpha1.FirmwareUpdateLenovoStatus) {
			status.UpdateTask = task
		})
	}
	if failed {
		return false, r.failWith(ctx, fwUpdate, ConditionLenovoRepositoryUpdateCompleted, ReasonLenovoRepositoryUpdateFailed,
			fmt.Sprintf("UpdateFromRepository task failed: %v", task.Message))
	}

	// Apply done: hand back to Completed, whose dry-run re-verifies convergence (bounded by
	// MaxRepositoryPasses if the repo still reports pending packages).
	log.V(1).Info("Repository update pass completed, handing back to Completed for re-verification", "Server", server.Name)
	if err := r.cleanupServerMaintenanceReferences(ctx, fwUpdate); err != nil {
		return false, err
	}
	fwUpdateBase := fwUpdate.DeepCopy()
	fwUpdate.Status.State = systemv1alpha1.FirmwareUpdateLenovoStateCompleted
	fwUpdate.Status.ObservedGeneration = fwUpdate.Generation
	fwUpdate.Status.Conditions = []metav1.Condition{}
	fwUpdate.Status.CheckTask = nil
	fwUpdate.Status.UpdateTask = task
	return false, r.Status().Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase))
}

func (r *FirmwareUpdateLenovoReconciler) processFailedState(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateLenovo, server *metalv1alpha1.Server) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	// TODO(lenovo): mirror the Dell controller's manual + automatic retry handling
	// (ShouldRetryReconciliation / RetryPolicy / DefaultFailedAutoRetryCount). Kept minimal here.
	log.V(1).Info("Failed to apply repository-based firmware update", "FirmwareUpdateLenovo", fwUpdate.Name, "Server", server.Name)
	return false, nil
}

// failWith records a failure condition and transitions to Failed.
func (r *FirmwareUpdateLenovoReconciler) failWith(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateLenovo, condType, reason, message string) error {
	condition, err := utils.GetCondition(r.Conditions, fwUpdate.Status.Conditions, condType)
	if err != nil {
		return err
	}
	if err := r.Conditions.Update(
		condition,
		conditionutils.UpdateStatus(corev1.ConditionTrue),
		conditionutils.UpdateReason(reason),
		conditionutils.UpdateMessage(message),
	); err != nil {
		return fmt.Errorf("failed to update failure condition: %w", err)
	}
	return r.updateStatus(ctx, fwUpdate, systemv1alpha1.FirmwareUpdateLenovoStateFailed, condition)
}

func (r *FirmwareUpdateLenovoReconciler) cleanupServerMaintenanceReferences(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateLenovo) error {
	log := ctrl.LoggerFrom(ctx)
	if fwUpdate.Spec.ServerMaintenanceRef == nil {
		return nil
	}
	serverMaintenance := &maintenancev1alpha1.ServerMaintenance{}
	err := r.Get(ctx, client.ObjectKey{Name: fwUpdate.Spec.ServerMaintenanceRef.Name, Namespace: r.ManagerNamespace}, serverMaintenance)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to get referred ServerMaintenance: %w", err)
	}
	if err == nil && serverMaintenance.DeletionTimestamp.IsZero() && metav1.IsControlledBy(serverMaintenance, fwUpdate) {
		log.V(1).Info("Deleting ServerMaintenance", "ServerMaintenance", client.ObjectKeyFromObject(serverMaintenance))
		if err := r.Delete(ctx, serverMaintenance); err != nil {
			return err
		}
	}
	return r.patchServerMaintenanceRef(ctx, fwUpdate, nil)
}

func (r *FirmwareUpdateLenovoReconciler) patchServerMaintenanceRef(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateLenovo, serverMaintenance *maintenancev1alpha1.ServerMaintenance) error {
	fwUpdateBase := fwUpdate.DeepCopy()
	if serverMaintenance == nil {
		fwUpdate.Spec.ServerMaintenanceRef = nil
	} else {
		fwUpdate.Spec.ServerMaintenanceRef = &metalv1alpha1.ObjectReference{
			Namespace: serverMaintenance.Namespace,
			Name:      serverMaintenance.Name,
		}
	}
	return r.Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase))
}

func (r *FirmwareUpdateLenovoReconciler) requestServerMaintenance(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateLenovo, server *metalv1alpha1.Server) (bool, error) {
	log := ctrl.LoggerFrom(ctx)

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
		return false, fmt.Errorf("failed to patch ServerMaintenance ref in FirmwareUpdateLenovo: %w", err)
	}
	return true, nil
}

// buildRepositoryParameters translates the FirmwareUpdateLenovo's Repository spec (and, if
// configured, its Secret credentials) into the resolved UpdateFromRepository parameters.
func (r *FirmwareUpdateLenovoReconciler) buildRepositoryParameters(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateLenovo) (lenovoRepositoryParameters, error) {
	repo := fwUpdate.Spec.Repository

	var username, password string
	if repo.SecretRef != nil {
		var err error
		username, password, err = utils.GetImageCredentialsForSecretRef(ctx, r.Client, repo.SecretRef)
		if err != nil {
			return lenovoRepositoryParameters{}, fmt.Errorf("failed to get repository credentials: %w", err)
		}
	}

	groupRequest := false
	if repo.GroupRequest != nil {
		groupRequest = *repo.GroupRequest
	}

	return lenovoRepositoryParameters{
		RepoURI:      repo.RepoURI,
		RepoUserName: username,
		RepoPassword: password,
		RepoMountOpt: repo.MountOptions,
		GroupRequest: groupRequest,
	}, nil
}

// updateStatus patches the top-level State and, if condition is non-nil, merges it into the
// conditions slice.
func (r *FirmwareUpdateLenovoReconciler) updateStatus(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateLenovo, state systemv1alpha1.FirmwareUpdateLenovoState, condition *metav1.Condition) error {
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
			return fmt.Errorf("failed to patch FirmwareUpdateLenovo condition: %w", err)
		}
	}
	if err := r.Status().Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase)); err != nil {
		return fmt.Errorf("failed to patch FirmwareUpdateLenovo status: %w", err)
	}
	return nil
}

// patchProgress patches the top-level State, optionally merges condition, and applies mutate to
// update Task-tracking fields — all in a single status patch.
func (r *FirmwareUpdateLenovoReconciler) patchProgress(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateLenovo, state systemv1alpha1.FirmwareUpdateLenovoState, condition *metav1.Condition, mutate func(*systemv1alpha1.FirmwareUpdateLenovoStatus)) error {
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
			return fmt.Errorf("failed to patch FirmwareUpdateLenovo condition: %w", err)
		}
	}
	if mutate != nil {
		mutate(&fwUpdate.Status)
	}
	if err := r.Status().Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase)); err != nil {
		return fmt.Errorf("failed to patch FirmwareUpdateLenovo status: %w", err)
	}
	return nil
}

func (r *FirmwareUpdateLenovoReconciler) enqueueByServerRefs(ctx context.Context, obj client.Object) []ctrl.Request {
	log := ctrl.LoggerFrom(ctx)
	host := obj.(*metalv1alpha1.Server)

	if host.Status.State == metalv1alpha1.ServerStateDiscovery ||
		host.Status.State == metalv1alpha1.ServerStateError ||
		host.Status.State == metalv1alpha1.ServerStateInitial {
		return nil
	}
	// Only react to Servers currently Parked for maintenance (the current metal-operator model;
	// ServerSpec.ServerMaintenanceRef was removed, see PR #203 adaptation).
	if host.Status.State != metalv1alpha1.ServerStateParked {
		return nil
	}

	fwUpdateList := &systemv1alpha1.FirmwareUpdateLenovoList{}
	if err := r.List(ctx, fwUpdateList); err != nil {
		log.Error(err, "Failed to list FirmwareUpdateLenovoList")
		return nil
	}

	for _, fwUpdate := range fwUpdateList.Items {
		if fwUpdate.Spec.ServerRef == nil || fwUpdate.Spec.ServerRef.Name != host.Name {
			continue
		}
		if fwUpdate.Spec.ServerMaintenanceRef == nil ||
			fwUpdate.Status.State == systemv1alpha1.FirmwareUpdateLenovoStateCompleted ||
			fwUpdate.Status.State == systemv1alpha1.FirmwareUpdateLenovoStateFailed {
			return nil
		}
		// Only enqueue if this Server is Parked for this FirmwareUpdateLenovo's ServerMaintenance.
		ownerKey := utils.ServerMaintenanceOwnerKey(r.ManagerNamespace, fwUpdate.Spec.ServerMaintenanceRef.Name)
		if !utils.IsServerParkedForOwner(host, ownerKey) {
			return nil
		}
		return []ctrl.Request{{
			NamespacedName: types.NamespacedName{Namespace: fwUpdate.Namespace, Name: fwUpdate.Name},
		}}
	}
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *FirmwareUpdateLenovoReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&systemv1alpha1.FirmwareUpdateLenovo{}).
		Named("system-firmwareupdatelenovo").
		Owns(&maintenancev1alpha1.ServerMaintenance{}).
		Watches(&metalv1alpha1.Server{}, handler.EnqueueRequestsFromMapFunc(r.enqueueByServerRefs)).
		Complete(r)
}
