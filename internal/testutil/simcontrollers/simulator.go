// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// Package simcontrollers provides lightweight, test-only stand-ins for metal-operator's
// real BMCReconciler/ServerReconciler. Go's internal package visibility rules prevent
// importing those reconcilers directly from this module (they live under metal-operator's
// internal/controller package), so instead these simulators poll the (mock) Redfish server
// through the public bmc.BMC client - exactly like the real controllers do - and keep
// BMC/Server status fields in sync automatically. This removes the need for tests to
// manually patch power state, firmware version, or maintenance state at "exact moments",
// and for tests to manually create Server objects for a BMC (BMCReconciler discovers and
// creates them, mirroring metal-operator's BMCReconciler.discoverServers), and cleans up
// the Server objects it created when the owning BMC is deleted.
//
// It is imported by both internal/controller/baseboard and
// internal/controller/system test suites so BMC/Server simulation stays
// consistent across both packages, while every other controller under test is the real,
// production reconciler from this repository.
//
// Only the fields tests actually rely on are synced (BMC PowerState/FirmwareVersion,
// Server PowerState, and Server Status.State transitions to/from Parked driven by
// the metalv1alpha1.OperationAnnotation "park"/"unpark" requests that this repo's
// real ServerMaintenanceReconciler issues).
package simcontrollers

import (
	"context"
	"strings"
	"time"

	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/ironcore-dev/metal-operator/bmc"
	"github.com/ironcore-dev/metal-operator/pkg/bmcutils"
	"github.com/stmcginnis/gofish/schemas"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// BMCFinalizer mirrors metal-operator's real BMCFinalizer
// ("metal.ironcore.dev/bmc"): it exists purely so BMC deletion goes through
// the same two-phase (DeletionTimestamp-set-then-finalizer-removed) process
// as production, not to gate on any Server cleanup - sim-bmc has nothing of
// its own to clean up on delete, since discovered Servers are the test's
// responsibility (as their AfterEach hooks already do), mirroring how
// metal-operator itself never deletes them either - that's left to the real
// Kubernetes garbage collector via the owner reference set in
// discoverServers, which envtest doesn't run.
const BMCFinalizer = "sim-bmc.metal-maintenance-operator.ironcore.dev/cleanup"

// BMCReconciler is a test-only stand-in for metal-operator's real BMCReconciler.
type BMCReconciler struct {
	client.Client
	DefaultProtocol    metalv1alpha1.ProtocolScheme
	SkipCertValidation bool
	BMCOptions         bmc.Options
	ResyncInterval     time.Duration
}

func (r *BMCReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&metalv1alpha1.BMC{}).
		Named("sim-bmc").
		Complete(r)
}

func (r *BMCReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	bmcObj := &metalv1alpha1.BMC{}
	if err := r.Get(ctx, req.NamespacedName, bmcObj); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !bmcObj.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(bmcObj, BMCFinalizer) {
			return ctrl.Result{}, nil
		}
		bmcBase := bmcObj.DeepCopy()
		controllerutil.RemoveFinalizer(bmcObj, BMCFinalizer)
		if err := r.Patch(ctx, bmcObj, client.MergeFrom(bmcBase)); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(bmcObj, BMCFinalizer) {
		bmcBase := bmcObj.DeepCopy()
		controllerutil.AddFinalizer(bmcObj, BMCFinalizer)
		if err := r.Patch(ctx, bmcObj, client.MergeFrom(bmcBase)); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Pass BMCConnectivityCheckOption to bypass the Enabled-state gate, mirroring
	// metal-operator's real BMCReconciler, which must be able to talk to a
	// freshly-created BMC (default state Pending) in order to bootstrap it.
	bmcClient, err := bmcutils.GetBMCClientFromBMC(ctx, r.Client, bmcObj, r.DefaultProtocol, r.SkipCertValidation, r.BMCOptions, bmcutils.BMCConnectivityCheckOption)
	if err != nil {
		log.V(1).Info("sim-bmc: failed to get BMC client, will retry", "error", err)
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}
	defer bmcClient.Logout()

	// Mimic metal-operator's handleAnnotationOperations/resetBMC: other
	// controllers (e.g. BMCVersionReconciler) request a BMC reset by setting
	// the operation annotation on the BMC and waiting for it to be removed.
	if operation, ok := bmcObj.GetAnnotations()[metalv1alpha1.OperationAnnotation]; ok {
		if resetType, ok := metalv1alpha1.AnnotationToRedfishMapping[operation]; ok {
			if resetType == schemas.GracefulRestartResetType {
				if err := bmcClient.ResetManager(ctx, bmcObj.Spec.BMCUUID, resetType); err != nil {
					log.V(1).Info("sim-bmc: failed to reset manager, will retry", "error", err)
					return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
				}
			}
		}
		bmcBase := bmcObj.DeepCopy()
		annotations := bmcObj.GetAnnotations()
		delete(annotations, metalv1alpha1.OperationAnnotation)
		bmcObj.SetAnnotations(annotations)
		if err := r.Patch(ctx, bmcObj, client.MergeFrom(bmcBase)); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}

	manager, err := bmcClient.GetManager(bmcObj.Spec.BMCUUID)
	if err != nil {
		log.V(1).Info("sim-bmc: failed to get manager, will retry", "error", err)
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}

	powerState := metalv1alpha1.UnknownPowerState
	if manager.PowerState != "" {
		powerState = metalv1alpha1.BMCPowerState(manager.PowerState)
	}

	// Mimic metal-operator's updateBMCStatusDetails: copy the Redfish manager's
	// own reported status into BMC.Status.State, which is what actually
	// bootstraps a freshly-created BMC from Pending to Enabled.
	state := bmcObj.Status.State
	if manager.Status.State != "" {
		state = metalv1alpha1.BMCState(manager.Status.State)
	}

	if bmcObj.Status.State != state || bmcObj.Status.PowerState != powerState || bmcObj.Status.FirmwareVersion != manager.FirmwareVersion {
		bmcBase := bmcObj.DeepCopy()
		bmcObj.Status.State = state
		bmcObj.Status.PowerState = powerState
		bmcObj.Status.FirmwareVersion = manager.FirmwareVersion
		if err := r.Status().Patch(ctx, bmcObj, client.MergeFrom(bmcBase)); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Re-check for deletion right before discovering/creating Server objects:
	// with ResyncInterval this frequent, a Reconcile invocation started before
	// a test's AfterEach deletes the BMC can otherwise still be mid-flight
	// (already past the DeletionTimestamp check above) when the test also
	// deletes the discovered Server, causing CreateOrPatch below to silently
	// resurrect it after the test believes cleanup is done. Since
	// controller-runtime serializes reconciles per object, re-fetching here
	// picks up a concurrently-set DeletionTimestamp and skips discovery.
	latestBMC := &metalv1alpha1.BMC{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(bmcObj), latestBMC); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !latestBMC.DeletionTimestamp.IsZero() {
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}

	if err := r.discoverServers(ctx, bmcClient, bmcObj); err != nil {
		log.V(1).Info("sim-bmc: failed to discover servers, will retry", "error", err)
	}

	return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
}

// discoverServers mimics metal-operator's BMCReconciler.discoverServers: it
// creates/patches a Server resource for each system reported by the BMC, so
// tests don't have to manually create matching Server objects for a BMC.
func (r *BMCReconciler) discoverServers(ctx context.Context, bmcClient bmc.BMC, bmcObj *metalv1alpha1.BMC) error {
	systems, err := bmcClient.GetSystems(ctx)
	if err != nil {
		return err
	}
	for i, s := range systems {
		server := &metalv1alpha1.Server{}
		server.Name = bmcutils.GetServerNameFromBMCandIndex(i, bmcObj)
		if _, err := controllerutil.CreateOrPatch(ctx, r.Client, server, func() error {
			server.Spec.SystemUUID = strings.ToLower(s.UUID)
			server.Spec.SystemURI = s.URI
			server.Spec.BMCRef = &corev1.LocalObjectReference{Name: bmcObj.Name}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// ServerReconciler is a test-only stand-in for metal-operator's real ServerReconciler.
type ServerReconciler struct {
	client.Client
	DefaultProtocol    metalv1alpha1.ProtocolScheme
	SkipCertValidation bool
	BMCOptions         bmc.Options
	ResyncInterval     time.Duration
}

func (r *ServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&metalv1alpha1.Server{}).
		Named("sim-server").
		Complete(r)
}

func (r *ServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	server := &metalv1alpha1.Server{}
	if err := r.Get(ctx, req.NamespacedName, server); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !server.DeletionTimestamp.IsZero() || (server.Spec.BMCRef == nil && server.Spec.BMC == nil) {
		return ctrl.Result{}, nil
	}

	if err := r.syncClaimState(ctx, server); err != nil {
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, err
	}

	bmcClient, err := bmcutils.GetBMCClientForServer(ctx, r.Client, server, r.DefaultProtocol, r.SkipCertValidation, r.BMCOptions)
	if err != nil {
		log.V(1).Info("sim-server: failed to get BMC client, will retry", "error", err)
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}
	defer bmcClient.Logout()

	if server.Spec.SystemURI == "" {
		systems, err := bmcClient.GetSystems(ctx)
		if err != nil {
			log.V(1).Info("sim-server: failed to get systems, will retry", "error", err)
			return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
		}
		for _, system := range systems {
			if strings.EqualFold(system.UUID, server.Spec.SystemUUID) {
				serverBase := server.DeepCopy()
				server.Spec.SystemURI = system.URI
				if err := r.Patch(ctx, server, client.MergeFrom(serverBase)); err != nil {
					return ctrl.Result{}, err
				}
				break
			}
		}
		if server.Spec.SystemURI == "" {
			return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
		}
	}

	// Mimic metal-operator's real ServerReconciler.updateServerStatus: refresh
	// Status.PowerState from the BMC unconditionally, on every reconcile, before
	// any state-specific handling below - the real controller does this too,
	// which is why other controllers (e.g. BIOSSettingsReconciler) that issue
	// direct BMC power commands while a Server is Parked for maintenance can
	// rely on Status.PowerState reflecting the change without waiting for the
	// Server to leave the Parked state first.
	if err := r.updateServerStatus(ctx, bmcClient, server); err != nil {
		log.V(1).Info("Server status update failed, will retry", "error", err)
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}

	if requeue, err := r.syncParkedState(ctx, bmcClient, server); err != nil || requeue {
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, err
	}

	if server.Status.State == metalv1alpha1.ServerStateParked {
		// Parked is an overlay state: while active, normal state-machine progression,
		// boot, and power healing are suspended (mirrors metal-operator's real behavior).
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}

	// Mimic metal-operator's real ServerReconciler.handleAvailableState: an
	// Available Server is force powered-off (Spec.Power set to PowerOff)
	// whenever its observed PowerState isn't already Off, regardless of what
	// Spec.Power currently says - Available Servers are expected to sit powered
	// off until claimed.
	if server.Status.State == metalv1alpha1.ServerStateAvailable &&
		server.Status.PowerState != metalv1alpha1.ServerOffPowerState &&
		server.Spec.Power != metalv1alpha1.PowerOff {
		serverBase := server.DeepCopy()
		server.Spec.Power = metalv1alpha1.PowerOff
		if err := r.Patch(ctx, server, client.MergeFrom(serverBase)); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Mimic metal-operator's real ServerReconciler.ensureServerPowerState: act on
	// Server.Spec.Power (set e.g. by ServerMaintenanceReconciler when powering a
	// Server off/on for maintenance) by issuing the corresponding BMC power
	// command, since without this the mock BMC's power state never actually
	// changes and callers waiting on Server/BIOS power transitions would block
	// forever.
	if server.Spec.Power == metalv1alpha1.PowerOn &&
		server.Status.PowerState != metalv1alpha1.ServerOnPowerState {
		if err := bmcClient.PowerOn(ctx, server.Spec.SystemURI); err != nil {
			log.V(1).Info("sim-server: failed to power on server, will retry", "error", err)
			return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
		}
	} else if server.Spec.Power == metalv1alpha1.PowerOff &&
		server.Status.PowerState != metalv1alpha1.ServerOffPowerState {
		if err := bmcClient.PowerOff(ctx, server.Spec.SystemURI); err != nil {
			log.V(1).Info("sim-server: failed to power off server, will retry", "error", err)
			return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
		}
	}

	return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
}

// updateServerStatus mirrors metal-operator's real ServerReconciler.updateServerStatus:
// it refreshes Status.PowerState from the BMC's reported system info.
func (r *ServerReconciler) updateServerStatus(ctx context.Context, bmcClient bmc.BMC, server *metalv1alpha1.Server) error {
	systemInfo, err := bmcClient.GetSystemInfo(ctx, server.Spec.SystemURI)
	if err != nil {
		return err
	}

	updatedPowerState := metalv1alpha1.ServerPowerState(systemInfo.PowerState)
	if updatedPowerState == server.Status.PowerState && systemInfo.Manufacturer == server.Status.Manufacturer {
		return nil
	}

	serverBase := server.DeepCopy()
	server.Status.PowerState = updatedPowerState
	server.Status.Manufacturer = systemInfo.Manufacturer
	return r.Status().Patch(ctx, server, client.MergeFrom(serverBase))
}

// syncClaimState mirrors metal-operator's real ServerReconciler.handleReservedState/
// handleAvailableState claim-driven transitions: it keeps Server.Status.State in sync
// with Spec.ServerClaimRef. It does nothing while Parked, since Parked is an overlay
// state that suspends normal state-machine progression.
func (r *ServerReconciler) syncClaimState(ctx context.Context, server *metalv1alpha1.Server) error {
	if server.Status.State == metalv1alpha1.ServerStateParked {
		return nil
	}

	// Mirrors metal-operator's real ServerReconciler.handleReservedState under
	// the (CRD-default) Recycle reclaim policy: if the ServerClaim referenced
	// by a Reserved Server has been deleted, clear Spec.ServerClaimRef so the
	// server reverts to Available on the next reconcile. Without this, nothing
	// ever clears ServerClaimRef and the Server would stay Reserved forever
	// after its claim is deleted.
	if server.Status.State == metalv1alpha1.ServerStateReserved && server.Spec.ServerClaimRef != nil {
		claim := &metalv1alpha1.ServerClaim{}
		claimKey := client.ObjectKey{Name: server.Spec.ServerClaimRef.Name, Namespace: server.Spec.ServerClaimRef.Namespace}
		if err := r.Get(ctx, claimKey, claim); err != nil {
			if !apierrors.IsNotFound(err) {
				return err
			}
			serverBase := server.DeepCopy()
			server.Spec.ServerClaimRef = nil
			if err := r.Patch(ctx, server, client.MergeFrom(serverBase)); err != nil {
				return err
			}
			return nil
		}
	}

	// Also mirrors real Server-controller behavior: once a ServerClaim is
	// released, a Server sitting in Reserved reverts to Available.
	if server.Status.State == metalv1alpha1.ServerStateReserved && server.Spec.ServerClaimRef == nil {
		serverBase := server.DeepCopy()
		server.Status.State = metalv1alpha1.ServerStateAvailable
		if err := r.Status().Patch(ctx, server, client.MergeFrom(serverBase)); err != nil {
			return err
		}
		return nil
	}

	// Mirrors metal-operator's real ServerReconciler.handleAvailableState: once a
	// ServerClaim (via sim-serverclaim, see ServerClaimReconciler below) has set
	// Spec.ServerClaimRef, an Available Server transitions to Reserved.
	if server.Status.State == metalv1alpha1.ServerStateAvailable && server.Spec.ServerClaimRef != nil {
		serverBase := server.DeepCopy()
		server.Status.State = metalv1alpha1.ServerStateReserved
		if err := r.Status().Patch(ctx, server, client.MergeFrom(serverBase)); err != nil {
			return err
		}
		return nil
	}

	return nil
}

// syncParkedState mimics metal-operator's real Server controller Parked-state handling
// (handleAnnotationOperations/parkServer/unparkServer/resumeParkedServer): it reacts to
// the metalv1alpha1.OperationAnnotation ("park"/"unpark") requests issued by this repo's
// real ServerMaintenanceReconciler, powers the Server off before confirming Parked, and
// resumes it to its pre-park state (Available/Reserved) once unparked.
func (r *ServerReconciler) syncParkedState(ctx context.Context, bmcClient bmc.BMC, server *metalv1alpha1.Server) (bool, error) {
	log := logf.FromContext(ctx)
	operation := server.GetAnnotations()[metalv1alpha1.OperationAnnotation]

	if operation == metalv1alpha1.OperationAnnotationUnpark {
		if server.Status.State == metalv1alpha1.ServerStateParked {
			target := metalv1alpha1.ServerStateAvailable
			if server.Spec.ServerClaimRef != nil {
				target = metalv1alpha1.ServerStateReserved
			}
			serverBase := server.DeepCopy()
			server.Status.State = target
			if err := r.Status().Patch(ctx, server, client.MergeFrom(serverBase)); err != nil {
				return false, err
			}
		}
		if err := r.removeServerOperationAnnotation(ctx, server); err != nil {
			return false, err
		}
		log.V(1).Info("Server unparked", "Server", server.Name)
		return true, nil
	}

	if operation == metalv1alpha1.OperationAnnotationPark {
		if server.Status.State == metalv1alpha1.ServerStateParked {
			// Already parked; just consume the redundant request, mirroring
			// metal-operator's real standDownParked.
			if err := r.removeServerOperationAnnotation(ctx, server); err != nil {
				return false, err
			}
			return true, nil
		}
		if server.Status.State != metalv1alpha1.ServerStateAvailable && server.Status.State != metalv1alpha1.ServerStateReserved {
			log.V(1).Info("Server park deferred, not in parkable state", "Server", server.Name, "State", server.Status.State)
			return true, nil
		}
		// server.Status.PowerState is already refreshed by updateServerStatus at the top
		// of Reconcile before syncParkedState is called, so it reflects the current BMC state.
		if server.Status.PowerState != metalv1alpha1.ServerOffPowerState {
			if err := bmcClient.PowerOff(ctx, server.Spec.SystemURI); err != nil {
				log.V(1).Info("Server power-off failed while parking, will retry", "error", err)
			}
			return true, nil
		}
		serverBase := server.DeepCopy()
		server.Status.State = metalv1alpha1.ServerStateParked
		if err := r.Status().Patch(ctx, server, client.MergeFrom(serverBase)); err != nil {
			return false, err
		}
		if err := r.removeServerOperationAnnotation(ctx, server); err != nil {
			return false, err
		}
		log.V(1).Info("Server parked", "Server", server.Name)
		return true, nil
	}

	return false, nil
}

// removeServerOperationAnnotation consumes the metalv1alpha1.OperationAnnotation
// request, mirroring metal-operator's real removeOperationAnnotation.
func (r *ServerReconciler) removeServerOperationAnnotation(ctx context.Context, server *metalv1alpha1.Server) error {
	if _, ok := server.GetAnnotations()[metalv1alpha1.OperationAnnotation]; !ok {
		return nil
	}
	serverBase := server.DeepCopy()
	annotations := server.GetAnnotations()
	delete(annotations, metalv1alpha1.OperationAnnotation)
	server.SetAnnotations(annotations)
	return r.Patch(ctx, server, client.MergeFrom(serverBase))
}

// ServerClaimReconciler is a test-only stand-in for metal-operator's real
// ServerClaimReconciler. It only implements the subset of behavior tests here
// rely on: claiming the referenced Server (Spec.ServerClaimRef, mirroring
// claimServer/ensureObjectRefForServer) and, once the Server reaches Reserved
// (driven by ServerReconciler.syncClaimState), propagating the claim's
// desired power state (mirroring ensurePowerStateForServer).
type ServerClaimReconciler struct {
	client.Client
	ResyncInterval time.Duration
}

func (r *ServerClaimReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&metalv1alpha1.ServerClaim{}).
		Named("sim-serverclaim").
		Complete(r)
}

func (r *ServerClaimReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	claim := &metalv1alpha1.ServerClaim{}
	if err := r.Get(ctx, req.NamespacedName, claim); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !claim.DeletionTimestamp.IsZero() || claim.Spec.ServerRef == nil {
		return ctrl.Result{}, nil
	}

	server := &metalv1alpha1.Server{}
	if err := r.Get(ctx, client.ObjectKey{Name: claim.Spec.ServerRef.Name}, server); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if server.Spec.ServerClaimRef == nil {
		serverBase := server.DeepCopy()
		server.Spec.ServerClaimRef = &metalv1alpha1.ImmutableObjectReference{
			Namespace: claim.Namespace,
			Name:      claim.Name,
		}
		if err := r.Patch(ctx, server, client.MergeFrom(serverBase)); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}

	if server.Status.State == metalv1alpha1.ServerStateReserved && server.Spec.Power != claim.Spec.Power {
		serverBase := server.DeepCopy()
		server.Spec.Power = claim.Spec.Power
		if err := r.Patch(ctx, server, client.MergeFrom(serverBase)); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
}
