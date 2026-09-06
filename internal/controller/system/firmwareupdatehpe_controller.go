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

// FirmwareUpdateHPEReconciler reconciles a FirmwareUpdateHPE object.
//
// This mirrors the Dell (PR #170) and Lenovo scaffold controllers, but drives HPE iLO's Install
// Set mechanism. Unlike Dell/Lenovo, iLO has no whole-SPP ingest action, so the controller:
//
//  1. reads the SPP `manifest/metadata.json` (served at Spec.SPP.BaseURI),
//  2. diffs it against /redfish/v1/UpdateService/FirmwareInventory (join by device Target GUID),
//  3. stages each applicable .fwpkg into iLO's ComponentRepository via AddFromUri,
//  4. builds and invokes an iLO Install Set, and
//  5. tracks the UpdateTaskQueue to convergence.
//
// Mechanism, diff, and payloads are documented in:
// https://github.com/shyamsundart14/metal-maintenance-operator/blob/main/docs/hpe-spp-manifest.md
//
// NOTE: This is a scaffold/skeleton. The vendor-neutral state machine and the ServerMaintenance
// reboot-safety gating are wired up; the SPP-diff and the iLO Redfish calls are marked TODO(hpe)
// and stubbed via the hpeInstallSetUpdater interface below, which a metal-operator HPE iLO client
// (plus an SPP-manifest reader) must implement.
const (
	FirmwareUpdateHPEFinalizer = "system.metal.ironcore.dev/firmwareupdatehpe"

	// ConditionHPERepositoryCheckCompleted tracks the read-only dry-run diff (SPP manifest vs
	// FirmwareInventory) used to discover whether any applicable components are pending.
	ConditionHPERepositoryCheckCompleted = "HPERepositoryCheckCompleted"
	// ConditionHPEInstallSetInvoked tracks the apply (Install Set Invoke).
	ConditionHPEInstallSetInvoked = "HPEInstallSetInvoked"
	// ConditionHPEInstallSetCompleted tracks the applied Install Set reaching a terminal state.
	ConditionHPEInstallSetCompleted = "HPEInstallSetCompleted"

	ReasonHPERepositoryCheckFailed = "HPERepositoryCheckFailed"
	ReasonHPEInstallSetInvoked     = "HPEInstallSetInvokedOnBMC"
	ReasonHPEInstallSetCompleted   = "HPEInstallSetCompleted"
	ReasonHPEInstallSetFailed      = "HPEInstallSetFailed"
)

// hpeComponentUpdate is the resolved diff result for one component (post SPP-manifest parse +
// FirmwareInventory diff).
type hpeComponentUpdate struct {
	Target           string
	DeviceName       string
	InstalledVersion string
	AvailableVersion string
	// Filename is the on-disk payload from metadata.json `Package.Files[].Name` (the single Files
	// entry whose TargetGUIDs contains Target) — NOT `FirmwareImages[].FileName`, which names an
	// artifact inside the .fwpkg (e.g. …pldm.signed). Covers .fwpkg and non-fwpkg firmware
	// (.vme/.flash/.bin) uniformly.
	Filename string
	// ImageURI is <BaseURI>/packages/<Filename>, handed to AddFromUri.
	ImageURI string
	// SHA256 and SizeBytes come from the same Package.Files[] entry — used for integrity
	// verification and the ~1 GB ComponentRepository budget check before staging.
	SHA256        string
	SizeBytes     int64
	ResetRequired bool
}

// hpeInstallSetUpdater is the HPE counterpart of the Dell/Lenovo repository updaters. It is
// defined here so this controller compiles ahead of the metal-operator BMC client gaining HPE
// iLO Install Set support and an SPP-manifest reader. Replace with the real capabilities once
// available. TODO(hpe).
type hpeInstallSetUpdater interface {
	// ComputeUpdateSet reads the SPP manifest (metadata.json) at baseURI, GETs the server's
	// FirmwareInventory, and returns the applicable update set. Join by Target GUID; gates:
	// FirmwareInventory Updateable, Target match, UpdatableBy in {Bmc,Uefi}, version_gt. The
	// payload filename/SHA256/size come from the matched component's Package.Files[] entry
	// (whose TargetGUIDs contains the Target). Read-only — never touches the host.
	ComputeUpdateSet(ctx context.Context, systemURI, baseURI, user, pass string) ([]hpeComponentUpdate, error)
	// StageComponent has iLO pull one .fwpkg into its ComponentRepository (AddFromUri,
	// UpdateRepository=true, UpdateTarget=false) — staged, not flashed.
	StageComponent(ctx context.Context, systemURI, imageURI string) error
	// CreateInstallSet builds an ordered Install Set Sequence (ApplyUpdate + control steps) from
	// the staged components and returns its id.
	CreateInstallSet(ctx context.Context, systemURI, name string, set []hpeComponentUpdate) (installSetID string, err error)
	// InvokeInstallSet triggers HpeComponentInstallSet.Invoke for the given set.
	InvokeInstallSet(ctx context.Context, systemURI, installSetID string) error
	// InstallSetState reports whether the invoked set has reached a terminal state and whether it
	// failed, by polling the UpdateTaskQueue.
	InstallSetState(ctx context.Context, systemURI, installSetID string) (terminal bool, failed bool, err error)
}

// FirmwareUpdateHPEReconciler reconciles a FirmwareUpdateHPE object.
type FirmwareUpdateHPEReconciler struct {
	client.Client
	ManagerNamespace            string
	DefaultProtocol             metalv1alpha1.ProtocolScheme
	SkipCertValidation          bool
	Scheme                      *runtime.Scheme
	ResyncInterval              time.Duration
	Conditions                  *conditionutils.Accessor
	DefaultFailedAutoRetryCount int32
	// MaxRepositoryPasses bounds how many times a dry-run diff may find further components pending
	// before the FirmwareUpdateHPE is marked Failed.
	MaxRepositoryPasses int32
}

// +kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=firmwareupdatehpes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=firmwareupdatehpes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=firmwareupdatehpes/finalizers,verbs=update
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch;update
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=servermaintenances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=servermaintenances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop.
func (r *FirmwareUpdateHPEReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	fwUpdate := &systemv1alpha1.FirmwareUpdateHPE{}
	if err := r.Get(ctx, req.NamespacedName, fwUpdate); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.V(1).Info("Reconciling FirmwareUpdateHPE")
	return r.reconcileExists(ctx, fwUpdate)
}

func (r *FirmwareUpdateHPEReconciler) reconcileExists(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateHPE) (ctrl.Result, error) {
	ok, err := r.shouldDelete(ctx, fwUpdate)
	if err != nil {
		return ctrl.Result{}, err
	}
	if ok {
		return r.delete(ctx, fwUpdate)
	}
	return r.reconcile(ctx, fwUpdate)
}

func (r *FirmwareUpdateHPEReconciler) shouldDelete(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateHPE) (bool, error) {
	isProgressing := func() (bool, error) {
		if fwUpdate.Status.State != systemv1alpha1.FirmwareUpdateHPEStateInProgress {
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
	return utils.ShouldProceedWithDeletion(ctx, fwUpdate, FirmwareUpdateHPEFinalizer, isProgressing)
}

func (r *FirmwareUpdateHPEReconciler) delete(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateHPE) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("Deleting FirmwareUpdateHPE")
	defer log.V(1).Info("Deleted FirmwareUpdateHPE")

	if !controllerutil.ContainsFinalizer(fwUpdate, FirmwareUpdateHPEFinalizer) {
		return ctrl.Result{}, nil
	}
	// TODO(hpe): optionally clean up the owned ServerMaintenance here (mirroring the Dell
	// controller), so an in-flight maintenance is released when the FirmwareUpdateHPE is deleted.
	if modified, err := clientutils.PatchEnsureNoFinalizer(ctx, r.Client, fwUpdate, FirmwareUpdateHPEFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *FirmwareUpdateHPEReconciler) reconcile(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateHPE) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	if utils.ShouldIgnoreReconciliation(fwUpdate) {
		log.V(1).Info("Skipped FirmwareUpdateHPE reconciliation")
		return ctrl.Result{}, nil
	}
	if modified, err := clientutils.PatchEnsureFinalizer(ctx, r.Client, fwUpdate, FirmwareUpdateHPEFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}
	requeue, err := r.transitionState(ctx, fwUpdate)
	if err != nil {
		return ctrl.Result{}, err
	}
	if requeue {
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}
	log.V(1).Info("Reconciled FirmwareUpdateHPE")
	return ctrl.Result{}, nil
}

// transitionState drives the vendor-neutral state machine (same shape as Dell/Lenovo): the
// dry-run diff runs ungated (Pending/Completed), and only the apply pass (InProgress) is gated on
// ServerMaintenance.
func (r *FirmwareUpdateHPEReconciler) transitionState(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateHPE) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if fwUpdate.Spec.ServerRef == nil {
		return false, fmt.Errorf("FirmwareUpdateHPE does not have a ServerRef")
	}
	server, err := utils.GetServerByName(ctx, r.Client, fwUpdate.Spec.ServerRef.Name)
	if err != nil {
		return false, fmt.Errorf("failed to fetch server: %w", err)
	}

	// TODO(hpe): obtain the HPE iLO updater from the metal-operator BMC client + an SPP-manifest
	// reader. For now use the stub so the state machine is exercised end-to-end without a live iLO.
	updater := newHPEInstallSetUpdater()

	switch fwUpdate.Status.State {
	case "", systemv1alpha1.FirmwareUpdateHPEStatePending:
		if utils.ShouldRetryReconciliation(fwUpdate) {
			fwUpdateBase := fwUpdate.DeepCopy()
			annotations := fwUpdate.GetAnnotations()
			delete(annotations, constants.OperationAnnotation)
			fwUpdate.SetAnnotations(annotations)
			if err := r.Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase)); err != nil {
				return true, fmt.Errorf("failed to patch FirmwareUpdateHPE for retrying: %w", err)
			}
			return false, nil
		}
		return r.processRepositoryCheck(ctx, updater, fwUpdate, server)
	case systemv1alpha1.FirmwareUpdateHPEStateCompleted:
		return r.processRepositoryCheck(ctx, updater, fwUpdate, server)
	case systemv1alpha1.FirmwareUpdateHPEStateInProgress:
		return r.processInProgress(ctx, updater, fwUpdate, server)
	case systemv1alpha1.FirmwareUpdateHPEStateFailed:
		return r.processFailedState(ctx, fwUpdate, server)
	}
	log.V(1).Info("Unknown State found", "State", fwUpdate.Status.State)
	return false, nil
}

// handleServerMaintenance is the reboot-safety gate — vendor-neutral, mirroring Dell/Lenovo.
// Request a ServerMaintenance if none, then refuse to proceed until the Server is parked for
// maintenance (metal-operator's Parked state, owned via the ServerMaintenanceOwner annotation).
// This is what makes reboot handling workload-agnostic (ESXi / KVM / bare-metal K8s worker).
func (r *FirmwareUpdateHPEReconciler) handleServerMaintenance(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateHPE, server *metalv1alpha1.Server) (bool, error) {
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
				return false, fmt.Errorf("failed to patch FirmwareUpdateHPE ServerMaintenance waiting conditions: %w", err)
			}
		}
		return false, nil
	}

	if condition.Reason != constants.ReasonMaintenanceApproved {
		if err := r.Conditions.Update(
			condition,
			conditionutils.UpdateStatus(corev1.ConditionFalse),
			conditionutils.UpdateReason(constants.ReasonMaintenanceApproved),
			conditionutils.UpdateMessage("Server is now parked for Maintenance"),
		); err != nil {
			return false, fmt.Errorf("failed to update ServerMaintenance approved condition: %w", err)
		}
		if err := r.updateStatus(ctx, fwUpdate, fwUpdate.Status.State, condition); err != nil {
			return false, fmt.Errorf("failed to patch FirmwareUpdateHPE ServerMaintenance approved conditions: %w", err)
		}
		return false, nil
	}
	return true, nil
}

// processRepositoryCheck drives the read-only dry-run diff (SPP manifest vs FirmwareInventory)
// while Pending (first check) or Completed (periodic drift-detection). It neither changes the
// system nor reboots, so it needs no ServerMaintenance. Only once components are pending does it
// transition into InProgress.
func (r *FirmwareUpdateHPEReconciler) processRepositoryCheck(ctx context.Context, updater hpeInstallSetUpdater, fwUpdate *systemv1alpha1.FirmwareUpdateHPE, server *metalv1alpha1.Server) (bool, error) {
	log := ctrl.LoggerFrom(ctx)

	user, pass, err := r.sppCredentials(ctx, fwUpdate)
	if err != nil {
		return false, err
	}

	set, err := updater.ComputeUpdateSet(ctx, server.Spec.SystemURI, fwUpdate.Spec.SPP.BaseURI, user, pass)
	if err != nil {
		return false, r.failWith(ctx, fwUpdate, ConditionHPERepositoryCheckCompleted, ReasonHPERepositoryCheckFailed,
			fmt.Sprintf("Failed to compute update set from SPP manifest: %v", err))
	}

	if len(set) == 0 {
		log.V(1).Info("SPP-based firmware up to date", "Server", server.Name)
		fwUpdateBase := fwUpdate.DeepCopy()
		fwUpdate.Status.State = systemv1alpha1.FirmwareUpdateHPEStateCompleted
		fwUpdate.Status.ObservedGeneration = fwUpdate.Generation
		fwUpdate.Status.UpdateComponents = nil
		fwUpdate.Status.PassCount = 0
		return false, r.Status().Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase))
	}

	passCount := fwUpdate.Status.PassCount + 1
	if r.MaxRepositoryPasses > 0 && passCount > r.MaxRepositoryPasses {
		log.Info("Exceeded maximum update passes, marking as Failed", "PassCount", passCount)
		return false, r.failWith(ctx, fwUpdate, ConditionHPERepositoryCheckCompleted, ReasonHPERepositoryCheckFailed,
			fmt.Sprintf("Exceeded maximum of %d update passes", r.MaxRepositoryPasses))
	}

	log.V(1).Info("SPP diff found pending components, entering InProgress", "Server", server.Name, "count", len(set), "PassCount", passCount)
	fwUpdateBase := fwUpdate.DeepCopy()
	fwUpdate.Status.State = systemv1alpha1.FirmwareUpdateHPEStateInProgress
	fwUpdate.Status.ObservedGeneration = fwUpdate.Generation
	fwUpdate.Status.PassCount = passCount
	fwUpdate.Status.UpdateComponents = toComponentUpdates(set)
	fwUpdate.Status.Conditions = []metav1.Condition{}
	return false, r.Status().Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase))
}

// processInProgress gates on ServerMaintenance, then stages the components, builds + invokes the
// Install Set, and tracks it to convergence.
func (r *FirmwareUpdateHPEReconciler) processInProgress(ctx context.Context, updater hpeInstallSetUpdater, fwUpdate *systemv1alpha1.FirmwareUpdateHPE, server *metalv1alpha1.Server) (bool, error) {
	log := ctrl.LoggerFrom(ctx)

	// GATE: do not touch the host until it is safe to reboot.
	if ok, err := r.handleServerMaintenance(ctx, fwUpdate, server); err != nil || !ok {
		return false, err
	}

	invoked, err := utils.GetCondition(r.Conditions, fwUpdate.Status.Conditions, ConditionHPEInstallSetInvoked)
	if err != nil {
		return false, err
	}

	if invoked.Status != metav1.ConditionTrue {
		set := fromComponentUpdates(fwUpdate.Status.UpdateComponents)
		// TODO(hpe): manage the ~1 GB ComponentRepository budget here (check FreeSizeBytes;
		// DeleteUnlockedComponents; or fall back to per-component streaming) before staging.
		for i := range set {
			if err := updater.StageComponent(ctx, server.Spec.SystemURI, set[i].ImageURI); err != nil {
				return false, r.failWith(ctx, fwUpdate, ConditionHPEInstallSetCompleted, ReasonHPEInstallSetFailed,
					fmt.Sprintf("Failed to stage component %s: %v", set[i].Filename, err))
			}
		}
		installSetID, err := updater.CreateInstallSet(ctx, server.Spec.SystemURI, fwUpdate.Name, set)
		if err != nil {
			return false, r.failWith(ctx, fwUpdate, ConditionHPEInstallSetCompleted, ReasonHPEInstallSetFailed,
				fmt.Sprintf("Failed to create Install Set: %v", err))
		}
		if err := updater.InvokeInstallSet(ctx, server.Spec.SystemURI, installSetID); err != nil {
			return false, r.failWith(ctx, fwUpdate, ConditionHPEInstallSetCompleted, ReasonHPEInstallSetFailed,
				fmt.Sprintf("Failed to invoke Install Set: %v", err))
		}
		if err := r.Conditions.Update(
			invoked,
			conditionutils.UpdateStatus(corev1.ConditionTrue),
			conditionutils.UpdateReason(ReasonHPEInstallSetInvoked),
			conditionutils.UpdateMessage(fmt.Sprintf("Invoked Install Set %v", installSetID)),
		); err != nil {
			return false, fmt.Errorf("failed to update InstallSetInvoked condition: %w", err)
		}
		fwUpdateBase := fwUpdate.DeepCopy()
		fwUpdate.Status.InstallSetID = installSetID
		if err := r.Conditions.UpdateSlice(&fwUpdate.Status.Conditions, invoked.Type,
			conditionutils.UpdateStatus(invoked.Status), conditionutils.UpdateReason(invoked.Reason),
			conditionutils.UpdateMessage(invoked.Message)); err != nil {
			return false, err
		}
		return true, r.Status().Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase))
	}

	// Poll the Install Set to completion.
	terminal, failed, err := updater.InstallSetState(ctx, server.Spec.SystemURI, fwUpdate.Status.InstallSetID)
	if err != nil {
		log.V(1).Info("Failed to poll Install Set state, retrying", "error", err)
		return true, nil
	}
	if !terminal {
		return true, nil
	}
	if failed {
		return false, r.failWith(ctx, fwUpdate, ConditionHPEInstallSetCompleted, ReasonHPEInstallSetFailed,
			"Install Set apply failed")
	}

	// Apply done: hand back to Completed, whose dry-run re-verifies convergence.
	log.V(1).Info("Install Set completed, handing back to Completed for re-verification", "Server", server.Name)
	if err := r.cleanupServerMaintenanceReferences(ctx, fwUpdate); err != nil {
		return false, err
	}
	fwUpdateBase := fwUpdate.DeepCopy()
	fwUpdate.Status.State = systemv1alpha1.FirmwareUpdateHPEStateCompleted
	fwUpdate.Status.ObservedGeneration = fwUpdate.Generation
	fwUpdate.Status.Conditions = []metav1.Condition{}
	fwUpdate.Status.InstallSetID = ""
	return false, r.Status().Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase))
}

func (r *FirmwareUpdateHPEReconciler) processFailedState(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateHPE, server *metalv1alpha1.Server) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	// TODO(hpe): mirror the Dell controller's manual + automatic retry handling
	// (ShouldRetryReconciliation / RetryPolicy / DefaultFailedAutoRetryCount). Kept minimal here.
	log.V(1).Info("Failed to apply SPP-based firmware update", "FirmwareUpdateHPE", fwUpdate.Name, "Server", server.Name)
	return false, nil
}

func (r *FirmwareUpdateHPEReconciler) failWith(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateHPE, condType, reason, message string) error {
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
	return r.updateStatus(ctx, fwUpdate, systemv1alpha1.FirmwareUpdateHPEStateFailed, condition)
}

func (r *FirmwareUpdateHPEReconciler) sppCredentials(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateHPE) (string, string, error) {
	if fwUpdate.Spec.SPP.SecretRef == nil {
		return "", "", nil
	}
	user, pass, err := utils.GetImageCredentialsForSecretRef(ctx, r.Client, fwUpdate.Spec.SPP.SecretRef)
	if err != nil {
		return "", "", fmt.Errorf("failed to get SPP credentials: %w", err)
	}
	return user, pass, nil
}

func (r *FirmwareUpdateHPEReconciler) cleanupServerMaintenanceReferences(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateHPE) error {
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

func (r *FirmwareUpdateHPEReconciler) patchServerMaintenanceRef(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateHPE, serverMaintenance *maintenancev1alpha1.ServerMaintenance) error {
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

func (r *FirmwareUpdateHPEReconciler) requestServerMaintenance(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateHPE, server *metalv1alpha1.Server) (bool, error) {
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
		return false, fmt.Errorf("failed to patch ServerMaintenance ref in FirmwareUpdateHPE: %w", err)
	}
	return true, nil
}

// updateStatus patches the top-level State and, if condition is non-nil, merges it.
func (r *FirmwareUpdateHPEReconciler) updateStatus(ctx context.Context, fwUpdate *systemv1alpha1.FirmwareUpdateHPE, state systemv1alpha1.FirmwareUpdateHPEState, condition *metav1.Condition) error {
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
			return fmt.Errorf("failed to patch FirmwareUpdateHPE condition: %w", err)
		}
	}
	if err := r.Status().Patch(ctx, fwUpdate, client.MergeFrom(fwUpdateBase)); err != nil {
		return fmt.Errorf("failed to patch FirmwareUpdateHPE status: %w", err)
	}
	return nil
}

func toComponentUpdates(set []hpeComponentUpdate) []systemv1alpha1.ComponentUpdate {
	out := make([]systemv1alpha1.ComponentUpdate, 0, len(set))
	for _, c := range set {
		out = append(out, systemv1alpha1.ComponentUpdate{
			Target:           c.Target,
			DeviceName:       c.DeviceName,
			InstalledVersion: c.InstalledVersion,
			AvailableVersion: c.AvailableVersion,
			Filename:         c.Filename,
		})
	}
	return out
}

func fromComponentUpdates(set []systemv1alpha1.ComponentUpdate) []hpeComponentUpdate {
	out := make([]hpeComponentUpdate, 0, len(set))
	for _, c := range set {
		out = append(out, hpeComponentUpdate{
			Target:           c.Target,
			DeviceName:       c.DeviceName,
			InstalledVersion: c.InstalledVersion,
			AvailableVersion: c.AvailableVersion,
			Filename:         c.Filename,
		})
	}
	return out
}

func (r *FirmwareUpdateHPEReconciler) enqueueByServerRefs(ctx context.Context, obj client.Object) []ctrl.Request {
	log := ctrl.LoggerFrom(ctx)
	host := obj.(*metalv1alpha1.Server)
	if host.Status.State == metalv1alpha1.ServerStateDiscovery ||
		host.Status.State == metalv1alpha1.ServerStateError ||
		host.Status.State == metalv1alpha1.ServerStateInitial {
		return nil
	}
	if host.Status.State != metalv1alpha1.ServerStateParked {
		return nil
	}
	fwUpdateList := &systemv1alpha1.FirmwareUpdateHPEList{}
	if err := r.List(ctx, fwUpdateList); err != nil {
		log.Error(err, "Failed to list FirmwareUpdateHPEList")
		return nil
	}
	for _, fwUpdate := range fwUpdateList.Items {
		if fwUpdate.Spec.ServerRef == nil || fwUpdate.Spec.ServerRef.Name != host.Name {
			continue
		}
		if fwUpdate.Spec.ServerMaintenanceRef == nil ||
			fwUpdate.Status.State == systemv1alpha1.FirmwareUpdateHPEStateCompleted ||
			fwUpdate.Status.State == systemv1alpha1.FirmwareUpdateHPEStateFailed {
			return nil
		}
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
func (r *FirmwareUpdateHPEReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&systemv1alpha1.FirmwareUpdateHPE{}).
		Named("system-firmwareupdatehpe").
		Owns(&maintenancev1alpha1.ServerMaintenance{}).
		Watches(&metalv1alpha1.Server{}, handler.EnqueueRequestsFromMapFunc(r.enqueueByServerRefs)).
		Complete(r)
}
