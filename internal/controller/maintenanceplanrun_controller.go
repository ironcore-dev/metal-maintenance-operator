// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"time"

	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	planRunOwnerLabel = "maintenance.metal.ironcore.dev/plan-run"
	stageNameLabel    = "maintenance.metal.ironcore.dev/stage-name"
	serverNameLabel   = "maintenance.metal.ironcore.dev/server-name"
	planRunFinalizer  = "maintenance.metal.ironcore.dev/plan-run"
)

// isBMCScoped returns true for stages whose child CR targets the BMC (not a Server).
func isBMCScoped(kind maintenancev1alpha1.StageKind) bool {
	return kind == maintenancev1alpha1.StageKindBMCSettings ||
		kind == maintenancev1alpha1.StageKindBMCVersion
}

// MaintenancePlanRunReconciler drives a single BMC's maintenance pipeline.
type MaintenancePlanRunReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=maintenanceplanruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=maintenanceplanruns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=maintenanceplanruns/finalizers,verbs=update
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=bmcsettings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=bmcversions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=biossettings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=biosversions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=bmcs,verbs=get;list;watch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch

func (r *MaintenancePlanRunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	run := &maintenancev1alpha1.MaintenancePlanRun{}
	if err := r.Get(ctx, req.NamespacedName, run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if run.DeletionTimestamp != nil {
		return r.reconcileDelete(ctx, run)
	}

	if !controllerutil.ContainsFinalizer(run, planRunFinalizer) {
		controllerutil.AddFinalizer(run, planRunFinalizer)
		if err := r.Update(ctx, run); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	original := run.DeepCopy()

	result, err := r.reconcileRun(ctx, run)

	if patchErr := r.Status().Patch(ctx, run, client.MergeFrom(original)); patchErr != nil {
		logger.Error(patchErr, "failed to patch MaintenancePlanRun status")
		return ctrl.Result{}, patchErr
	}

	return result, err
}

func (r *MaintenancePlanRunReconciler) reconcileRun(ctx context.Context, run *maintenancev1alpha1.MaintenancePlanRun) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("run", run.Name)

	// Succeeded runs with drift monitoring enabled: check for regressions.
	if run.Status.Phase == maintenancev1alpha1.MaintenancePlanRunPhaseSucceeded &&
		run.Spec.DriftPolicy != maintenancev1alpha1.DriftPolicyDisabled {
		return r.reconcileDrift(ctx, run)
	}

	if run.Status.Phase == maintenancev1alpha1.MaintenancePlanRunPhaseSucceeded ||
		run.Status.Phase == maintenancev1alpha1.MaintenancePlanRunPhaseFailed {
		return ctrl.Result{}, nil
	}

	if run.Status.Phase == "" {
		run.Status.Phase = maintenancev1alpha1.MaintenancePlanRunPhasePending
		run.Status.StageStatuses = make([]maintenancev1alpha1.StageStatus, len(run.Spec.Stages))
		for i, s := range run.Spec.Stages {
			run.Status.StageStatuses[i] = maintenancev1alpha1.StageStatus{
				Name:  s.Name,
				Phase: maintenancev1alpha1.StagePhasePending,
			}
		}
	}

	if run.Status.Phase == maintenancev1alpha1.MaintenancePlanRunPhasePending {
		run.Status.Phase = maintenancev1alpha1.MaintenancePlanRunPhaseRunning
		now := metav1.Now()
		run.Status.StartTime = &now
		logger.Info("starting run")
	}

	for i := range run.Spec.Stages {
		stage := &run.Spec.Stages[i]
		status := &run.Status.StageStatuses[i]

		switch status.Phase {
		case maintenancev1alpha1.StagePhaseSucceeded, maintenancev1alpha1.StagePhaseSkipped:
			run.Status.CurrentStageIndex = int32(i + 1)
			continue

		case maintenancev1alpha1.StagePhaseFailed:
			run.Status.Phase = maintenancev1alpha1.MaintenancePlanRunPhaseFailed
			return ctrl.Result{}, nil

		case maintenancev1alpha1.StagePhasePending:
			if isBMCScoped(stage.Kind) {
				return r.startBMCStage(ctx, run, stage, status)
			}
			return r.startServerStage(ctx, run, stage, status)

		case maintenancev1alpha1.StagePhaseRunning:
			if isBMCScoped(stage.Kind) {
				return r.pollBMCStage(ctx, run, stage, status)
			}
			return r.pollServerStage(ctx, run, stage, status)
		}
	}

	// All stages done — assign drift policies in status and patch child CRs.
	r.assignStageDriftPolicies(run)
	r.patchStageDriftPolicies(ctx, run)

	run.Status.Phase = maintenancev1alpha1.MaintenancePlanRunPhaseSucceeded
	now := metav1.Now()
	run.Status.CompletionTime = &now
	logger.Info("run completed successfully")
	r.Recorder.Event(run, corev1.EventTypeNormal, "RunSucceeded", "all stages completed successfully")
	return ctrl.Result{}, nil
}

// ── Drift detection and recovery ─────────────────────────────────────────────

// reconcileDrift is called on every reconcile of a Succeeded run when drift
// monitoring is active. The child CRs report drift by self-transitioning from
// their terminal state back to Pending (via their own driftPolicy=Observe
// reconciler). We detect this and either re-activate the child (Reconcile
// policy) or surface a condition (Observe policy).
func (r *MaintenancePlanRunReconciler) reconcileDrift(ctx context.Context, run *maintenancev1alpha1.MaintenancePlanRun) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("run", run.Name)

	driftIdx := -1
	for i := range run.Status.StageStatuses {
		ss := &run.Status.StageStatuses[i]
		if ss.StageDriftPolicy != maintenancev1alpha1.StageDriftPolicyObserve {
			continue
		}
		drifted, err := r.stageHasDrifted(ctx, run, &run.Spec.Stages[i], ss)
		if err != nil {
			return ctrl.Result{}, err
		}
		if drifted && driftIdx == -1 {
			driftIdx = i
		}
	}

	if driftIdx == -1 {
		apimeta.RemoveStatusCondition(&run.Status.Conditions, maintenancev1alpha1.ConditionTypeDriftDetected)
		return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
	}

	logger.Info("drift detected", "earliestStage", run.Spec.Stages[driftIdx].Name)

	switch run.Spec.DriftPolicy {
	case maintenancev1alpha1.DriftPolicyReconcile:
		// Re-activate each child CR from the earliest dirty stage by patching
		// spec.driftPolicy: "" — the child CR's reconciler then picks it up and
		// re-applies the configuration, preserving condition history.
		if err := r.reactivateStageCRs(ctx, run, driftIdx); err != nil {
			return ctrl.Result{}, err
		}
		// Reset run stage statuses from the drifted stage so we re-enter the
		// normal execution path once the child CRs complete.
		for i := driftIdx; i < len(run.Status.StageStatuses); i++ {
			run.Status.StageStatuses[i] = maintenancev1alpha1.StageStatus{
				Name:  run.Spec.Stages[i].Name,
				Phase: maintenancev1alpha1.StagePhasePending,
			}
		}
		run.Status.Phase = maintenancev1alpha1.MaintenancePlanRunPhaseRunning
		run.Status.CompletionTime = nil
		run.Spec.Trigger = maintenancev1alpha1.RunTriggerDriftRemediation
		apimeta.RemoveStatusCondition(&run.Status.Conditions, maintenancev1alpha1.ConditionTypeDriftDetected)
		logger.Info("re-executing from drifted stage", "stage", run.Spec.Stages[driftIdx].Name)
		r.Recorder.Eventf(run, corev1.EventTypeWarning, "DriftRemediation",
			"drift detected at stage %s — re-activating child CRs and re-executing", run.Spec.Stages[driftIdx].Name)
		return ctrl.Result{Requeue: true}, nil

	case maintenancev1alpha1.DriftPolicyObserve:
		apimeta.SetStatusCondition(&run.Status.Conditions, metav1.Condition{
			Type:               maintenancev1alpha1.ConditionTypeDriftDetected,
			Status:             metav1.ConditionTrue,
			Reason:             "DriftDetected",
			Message:            fmt.Sprintf("drift detected at stage %s", run.Spec.Stages[driftIdx].Name),
			LastTransitionTime: metav1.Now(),
		})
		r.Recorder.Eventf(run, corev1.EventTypeWarning, "DriftDetected",
			"drift detected at stage %s (observe only — no action taken)", run.Spec.Stages[driftIdx].Name)
		return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
	}

	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

// reactivateStageCRs patches spec.driftPolicy: "" on all child CRs from
// driftIdx onwards, re-activating them so their reconciler re-applies state.
// This preserves condition history and avoids orphaned object cleanup.
func (r *MaintenancePlanRunReconciler) reactivateStageCRs(
	ctx context.Context,
	run *maintenancev1alpha1.MaintenancePlanRun,
	fromIdx int,
) error {
	for i := fromIdx; i < len(run.Spec.Stages); i++ {
		stage := &run.Spec.Stages[i]
		ss := &run.Status.StageStatuses[i]

		if isBMCScoped(stage.Kind) {
			if err := r.patchDriftPolicy(ctx, stage.Kind, bmcCRName(run.Name, stage.Name), metalv1alpha1.DriftPolicyActive); err != nil {
				return err
			}
		} else {
			for _, srv := range ss.ServerStatuses {
				if srv.Phase == maintenancev1alpha1.StagePhaseSkipped {
					continue
				}
				name := serverCRName(run.Name, stage.Name, srv.ServerRef.Name)
				if err := r.patchDriftPolicy(ctx, stage.Kind, name, metalv1alpha1.DriftPolicyActive); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// patchDriftPolicy patches spec.driftPolicy on a single child CR.
func (r *MaintenancePlanRunReconciler) patchDriftPolicy(
	ctx context.Context,
	kind maintenancev1alpha1.StageKind,
	name string,
	policy metalv1alpha1.DriftPolicy,
) error {
	key := types.NamespacedName{Name: name}
	switch kind {
	case maintenancev1alpha1.StageKindBMCSettings:
		obj := &metalv1alpha1.BMCSettings{}
		if err := r.Get(ctx, key, obj); err != nil {
			return client.IgnoreNotFound(err)
		}
		patch := client.MergeFrom(obj.DeepCopy())
		obj.Spec.DriftPolicy = policy
		return r.Patch(ctx, obj, patch)
	case maintenancev1alpha1.StageKindBMCVersion:
		obj := &metalv1alpha1.BMCVersion{}
		if err := r.Get(ctx, key, obj); err != nil {
			return client.IgnoreNotFound(err)
		}
		patch := client.MergeFrom(obj.DeepCopy())
		obj.Spec.DriftPolicy = policy
		return r.Patch(ctx, obj, patch)
	case maintenancev1alpha1.StageKindBIOSSettings:
		obj := &metalv1alpha1.BIOSSettings{}
		if err := r.Get(ctx, key, obj); err != nil {
			return client.IgnoreNotFound(err)
		}
		patch := client.MergeFrom(obj.DeepCopy())
		obj.Spec.DriftPolicy = policy
		return r.Patch(ctx, obj, patch)
	case maintenancev1alpha1.StageKindBIOSVersion:
		obj := &metalv1alpha1.BIOSVersion{}
		if err := r.Get(ctx, key, obj); err != nil {
			return client.IgnoreNotFound(err)
		}
		patch := client.MergeFrom(obj.DeepCopy())
		obj.Spec.DriftPolicy = policy
		return r.Patch(ctx, obj, patch)
	}
	return nil
}

// assignStageDriftPolicies records the drift monitoring mode for each completed
// stage in the run status AND patches the actual child CR's spec.driftPolicy
// so the upstream metal-operator reconciler enforces the same mode.
func (r *MaintenancePlanRunReconciler) assignStageDriftPolicies(run *maintenancev1alpha1.MaintenancePlanRun) {
	lastVersionIdx := map[maintenancev1alpha1.StageKind]int{}
	for i, s := range run.Spec.Stages {
		if s.Kind == maintenancev1alpha1.StageKindBMCVersion ||
			s.Kind == maintenancev1alpha1.StageKindBIOSVersion {
			lastVersionIdx[s.Kind] = i
		}
	}

	for i := range run.Status.StageStatuses {
		ss := &run.Status.StageStatuses[i]
		if ss.Phase == maintenancev1alpha1.StagePhaseSkipped {
			continue
		}
		stage := &run.Spec.Stages[i]
		switch stage.Kind {
		case maintenancev1alpha1.StageKindBMCSettings, maintenancev1alpha1.StageKindBIOSSettings:
			ss.StageDriftPolicy = maintenancev1alpha1.StageDriftPolicyObserve
		case maintenancev1alpha1.StageKindBMCVersion, maintenancev1alpha1.StageKindBIOSVersion:
			if lastVersionIdx[stage.Kind] == i {
				ss.StageDriftPolicy = maintenancev1alpha1.StageDriftPolicyObserve
			} else {
				ss.StageDriftPolicy = maintenancev1alpha1.StageDriftPolicySuspend
			}
		}
	}
}

// patchStageDriftPolicies patches spec.driftPolicy on all child CRs once the
// run has succeeded. Called after assignStageDriftPolicies updates the status.
// This is a best-effort operation — errors are logged but do not block completion.
func (r *MaintenancePlanRunReconciler) patchStageDriftPolicies(ctx context.Context, run *maintenancev1alpha1.MaintenancePlanRun) {
	logger := log.FromContext(ctx).WithValues("run", run.Name)
	for i := range run.Status.StageStatuses {
		ss := &run.Status.StageStatuses[i]
		if ss.StageDriftPolicy == "" {
			continue
		}
		stage := &run.Spec.Stages[i]

		var driftPolicyValue metalv1alpha1.DriftPolicy
		switch ss.StageDriftPolicy {
		case maintenancev1alpha1.StageDriftPolicyObserve:
			driftPolicyValue = metalv1alpha1.DriftPolicyObserve
		case maintenancev1alpha1.StageDriftPolicySuspend:
			driftPolicyValue = metalv1alpha1.DriftPolicySuspend
		}

		if isBMCScoped(stage.Kind) {
			if err := r.patchDriftPolicy(ctx, stage.Kind, bmcCRName(run.Name, stage.Name), driftPolicyValue); err != nil {
				logger.Error(err, "failed to patch driftPolicy on BMC child CR", "stage", stage.Name)
			}
		} else {
			for _, srv := range ss.ServerStatuses {
				if srv.Phase == maintenancev1alpha1.StagePhaseSkipped {
					continue
				}
				name := serverCRName(run.Name, stage.Name, srv.ServerRef.Name)
				if err := r.patchDriftPolicy(ctx, stage.Kind, name, driftPolicyValue); err != nil {
					logger.Error(err, "failed to patch driftPolicy on server child CR", "stage", stage.Name, "server", srv.ServerRef.Name)
				}
			}
		}
	}
}

// stageHasDrifted returns true when a child CR for this Observe-mode stage has
// self-reported drift by transitioning back to Pending state. The upstream
// metal-operator reconciler (in Observe mode) detects the hardware regression
// and sets the state to Pending without applying changes.
func (r *MaintenancePlanRunReconciler) stageHasDrifted(
	ctx context.Context,
	run *maintenancev1alpha1.MaintenancePlanRun,
	stage *maintenancev1alpha1.PlanStage,
	status *maintenancev1alpha1.StageStatus,
) (bool, error) {
	if isBMCScoped(stage.Kind) {
		return r.bmcHasDrifted(ctx, stage.Kind, bmcCRName(run.Name, stage.Name))
	}
	for _, ss := range status.ServerStatuses {
		if ss.Phase == maintenancev1alpha1.StagePhaseSkipped {
			continue
		}
		drifted, err := r.serverHasDrifted(ctx, stage.Kind, serverCRName(run.Name, stage.Name, ss.ServerRef.Name))
		if err != nil {
			return false, err
		}
		if drifted {
			return true, nil
		}
	}
	return false, nil
}

// bmcHasDrifted returns true when the child CR has self-reported drift
// by transitioning back to Pending (the upstream Observe-mode reconciler
// detects the regression and sets Pending without applying changes).
func (r *MaintenancePlanRunReconciler) bmcHasDrifted(ctx context.Context, kind maintenancev1alpha1.StageKind, name string) (bool, error) {
	key := types.NamespacedName{Name: name}
	switch kind {
	case maintenancev1alpha1.StageKindBMCSettings:
		obj := &metalv1alpha1.BMCSettings{}
		if err := r.Get(ctx, key, obj); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		return obj.Status.State == metalv1alpha1.BMCSettingsStatePending, nil
	case maintenancev1alpha1.StageKindBMCVersion:
		obj := &metalv1alpha1.BMCVersion{}
		if err := r.Get(ctx, key, obj); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		return obj.Status.State == metalv1alpha1.BMCVersionStatePending, nil
	}
	return false, nil
}

func (r *MaintenancePlanRunReconciler) serverHasDrifted(ctx context.Context, kind maintenancev1alpha1.StageKind, name string) (bool, error) {
	key := types.NamespacedName{Name: name}
	switch kind {
	case maintenancev1alpha1.StageKindBIOSSettings:
		obj := &metalv1alpha1.BIOSSettings{}
		if err := r.Get(ctx, key, obj); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		return obj.Status.State == metalv1alpha1.BIOSSettingsStatePending, nil
	case maintenancev1alpha1.StageKindBIOSVersion:
		obj := &metalv1alpha1.BIOSVersion{}
		if err := r.Get(ctx, key, obj); err != nil {
			return false, client.IgnoreNotFound(err)
		}
		return obj.Status.State == metalv1alpha1.BIOSVersionStatePending, nil
	}
	return false, nil
}

// ── BMC-scoped stage (one child CR for the whole BMC) ─────────────────────────

func (r *MaintenancePlanRunReconciler) startBMCStage(
	ctx context.Context,
	run *maintenancev1alpha1.MaintenancePlanRun,
	stage *maintenancev1alpha1.PlanStage,
	status *maintenancev1alpha1.StageStatus,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("run", run.Name, "stage", stage.Name)

	if skip, reason := r.shouldSkipBMC(run, stage); skip {
		logger.Info("skipping BMC stage", "reason", reason)
		status.Phase = maintenancev1alpha1.StagePhaseSkipped
		status.Message = reason
		return ctrl.Result{Requeue: true}, nil
	}

	obj, err := r.buildBMCObject(run, stage)
	if err != nil {
		status.Phase = maintenancev1alpha1.StagePhaseFailed
		status.Message = fmt.Sprintf("failed to build child object: %v", err)
		return ctrl.Result{}, nil
	}

	if err := r.Create(ctx, obj); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, fmt.Errorf("create child %s/%s: %w", stage.Kind, obj.GetName(), err)
		}
		logger.Info("child CR already exists, adopting", "name", obj.GetName())
	}

	now := metav1.Now()
	status.Phase = maintenancev1alpha1.StagePhaseRunning
	status.StartTime = &now
	status.ChildRef = &corev1.ObjectReference{
		APIVersion: "metal.ironcore.dev/v1alpha1",
		Kind:       string(stage.Kind),
		Name:       obj.GetName(),
	}
	logger.Info("created BMC child CR", "name", obj.GetName())
	return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
}

func (r *MaintenancePlanRunReconciler) pollBMCStage(
	ctx context.Context,
	run *maintenancev1alpha1.MaintenancePlanRun,
	stage *maintenancev1alpha1.PlanStage,
	status *maintenancev1alpha1.StageStatus,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("run", run.Name, "stage", stage.Name)

	childName := bmcCRName(run.Name, stage.Name)
	done, failed, msg, err := r.checkCRStatus(ctx, stage.Kind, childName)
	if err != nil {
		return ctrl.Result{}, err
	}

	if failed {
		logger.Info("BMC stage failed", "message", msg)
		status.Phase = maintenancev1alpha1.StagePhaseFailed
		status.Message = msg
		now := metav1.Now()
		status.CompletionTime = &now
		r.Recorder.Eventf(run, corev1.EventTypeWarning, "StageFailed",
			"stage %s failed: %s", stage.Name, msg)
		return ctrl.Result{}, nil
	}

	if done {
		logger.Info("BMC stage succeeded")
		status.Phase = maintenancev1alpha1.StagePhaseSucceeded
		now := metav1.Now()
		status.CompletionTime = &now
		r.Recorder.Eventf(run, corev1.EventTypeNormal, "StageSucceeded",
			"stage %s completed", stage.Name)
		return ctrl.Result{Requeue: true}, nil
	}

	return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
}

// ── Server-scoped stage (one child CR per server) ──────────────────────────────

func (r *MaintenancePlanRunReconciler) startServerStage(
	ctx context.Context,
	run *maintenancev1alpha1.MaintenancePlanRun,
	stage *maintenancev1alpha1.PlanStage,
	status *maintenancev1alpha1.StageStatus,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("run", run.Name, "stage", stage.Name)

	// Initialise per-server status if this is the first visit.
	if len(status.ServerStatuses) == 0 {
		status.ServerStatuses = make([]maintenancev1alpha1.ServerStageStatus, len(run.Spec.ServerRefs))
		for i, ref := range run.Spec.ServerRefs {
			status.ServerStatuses[i] = maintenancev1alpha1.ServerStageStatus{
				ServerRef: ref,
				Phase:     maintenancev1alpha1.StagePhasePending,
			}
		}
	}

	// Start each server's child CR if not yet started.
	for i := range status.ServerStatuses {
		ss := &status.ServerStatuses[i]
		if ss.Phase != maintenancev1alpha1.StagePhasePending {
			continue
		}

		serverName := ss.ServerRef.Name
		if skip, reason := r.shouldSkipServer(run, stage, serverName); skip {
			logger.Info("skipping server stage", "server", serverName, "reason", reason)
			ss.Phase = maintenancev1alpha1.StagePhaseSkipped
			ss.Message = reason
			continue
		}

		obj, err := r.buildServerObject(run, stage, serverName)
		if err != nil {
			ss.Phase = maintenancev1alpha1.StagePhaseFailed
			ss.Message = fmt.Sprintf("failed to build child object: %v", err)
			continue
		}

		if err := r.Create(ctx, obj); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				return ctrl.Result{}, fmt.Errorf("create child %s/%s: %w", stage.Kind, obj.GetName(), err)
			}
			logger.Info("server child CR already exists, adopting", "name", obj.GetName(), "server", serverName)
		}

		now := metav1.Now()
		ss.Phase = maintenancev1alpha1.StagePhaseRunning
		ss.StartTime = &now
		ss.ChildRef = &corev1.ObjectReference{
			APIVersion: "metal.ironcore.dev/v1alpha1",
			Kind:       string(stage.Kind),
			Name:       obj.GetName(),
		}
		logger.Info("created server child CR", "name", obj.GetName(), "server", serverName)
	}

	now := metav1.Now()
	status.Phase = maintenancev1alpha1.StagePhaseRunning
	status.StartTime = &now
	return r.aggregateServerStage(status)
}

func (r *MaintenancePlanRunReconciler) pollServerStage(
	ctx context.Context,
	run *maintenancev1alpha1.MaintenancePlanRun,
	stage *maintenancev1alpha1.PlanStage,
	status *maintenancev1alpha1.StageStatus,
) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("run", run.Name, "stage", stage.Name)

	for i := range status.ServerStatuses {
		ss := &status.ServerStatuses[i]
		if ss.Phase != maintenancev1alpha1.StagePhaseRunning {
			continue
		}

		serverName := ss.ServerRef.Name
		childName := serverCRName(run.Name, stage.Name, serverName)
		done, failed, msg, err := r.checkCRStatus(ctx, stage.Kind, childName)
		if err != nil {
			return ctrl.Result{}, err
		}

		if failed {
			logger.Info("server stage failed", "server", serverName, "message", msg)
			ss.Phase = maintenancev1alpha1.StagePhaseFailed
			ss.Message = msg
			now := metav1.Now()
			ss.CompletionTime = &now
			r.Recorder.Eventf(run, corev1.EventTypeWarning, "StageFailed",
				"stage %s failed for server %s: %s", stage.Name, serverName, msg)
			continue
		}

		if done {
			logger.Info("server stage succeeded", "server", serverName)
			ss.Phase = maintenancev1alpha1.StagePhaseSucceeded
			now := metav1.Now()
			ss.CompletionTime = &now
			r.Recorder.Eventf(run, corev1.EventTypeNormal, "StageSucceeded",
				"stage %s completed for server %s", stage.Name, serverName)
		}
	}

	return r.aggregateServerStage(status)
}

// aggregateServerStage computes the stage-level phase from all per-server statuses
// and returns the appropriate ctrl.Result.
func (r *MaintenancePlanRunReconciler) aggregateServerStage(status *maintenancev1alpha1.StageStatus) (ctrl.Result, error) {
	anyFailed := false
	anyRunning := false
	allSkipped := len(status.ServerStatuses) > 0

	for _, ss := range status.ServerStatuses {
		switch ss.Phase {
		case maintenancev1alpha1.StagePhaseFailed:
			anyFailed = true
			allSkipped = false
		case maintenancev1alpha1.StagePhaseRunning, maintenancev1alpha1.StagePhasePending:
			anyRunning = true
			allSkipped = false
		case maintenancev1alpha1.StagePhaseSucceeded:
			allSkipped = false
		}
	}

	now := metav1.Now()

	if anyFailed {
		// Propagate the first failure message to the stage level for observability.
		for _, ss := range status.ServerStatuses {
			if ss.Phase == maintenancev1alpha1.StagePhaseFailed && ss.Message != "" {
				status.Message = ss.Message
				break
			}
		}
		status.Phase = maintenancev1alpha1.StagePhaseFailed
		status.CompletionTime = &now
		return ctrl.Result{}, nil
	}
	if anyRunning {
		return ctrl.Result{RequeueAfter: 15 * time.Second}, nil
	}
	if allSkipped {
		status.Phase = maintenancev1alpha1.StagePhaseSkipped
		status.CompletionTime = &now
		return ctrl.Result{Requeue: true}, nil
	}

	// All servers succeeded (or a mix of succeeded and skipped).
	status.Phase = maintenancev1alpha1.StagePhaseSucceeded
	status.CompletionTime = &now
	return ctrl.Result{Requeue: true}, nil
}

// ── Skip logic ────────────────────────────────────────────────────────────────

func (r *MaintenancePlanRunReconciler) shouldSkipBMC(
	run *maintenancev1alpha1.MaintenancePlanRun,
	stage *maintenancev1alpha1.PlanStage,
) (bool, string) {
	target := bmcStageVersion(stage)
	baseline := run.Spec.BaselineBMCVersion
	if target == "" || baseline == "" {
		return false, ""
	}
	// Settings stages apply to a specific firmware version: skip only when the
	// firmware has already moved past this version (target < baseline).
	// Version upgrade stages: skip when already at or beyond the target (target <= baseline).
	if stage.Kind == maintenancev1alpha1.StageKindBMCSettings {
		if target < baseline {
			return true, fmt.Sprintf("baseline %s > target %s", baseline, target)
		}
	} else {
		if target <= baseline {
			return true, fmt.Sprintf("baseline %s >= target %s", baseline, target)
		}
	}
	return false, ""
}

func (r *MaintenancePlanRunReconciler) shouldSkipServer(
	run *maintenancev1alpha1.MaintenancePlanRun,
	stage *maintenancev1alpha1.PlanStage,
	serverName string,
) (bool, string) {
	target := serverStageVersion(stage)
	baseline := run.Spec.BaselineBIOSVersions[serverName]
	if target == "" || baseline == "" {
		return false, ""
	}
	// Same logic: settings skip only when firmware has moved past this version.
	if stage.Kind == maintenancev1alpha1.StageKindBIOSSettings {
		if target < baseline {
			return true, fmt.Sprintf("baseline %s > target %s for server %s", baseline, target, serverName)
		}
	} else {
		if target <= baseline {
			return true, fmt.Sprintf("baseline %s >= target %s for server %s", baseline, target, serverName)
		}
	}
	return false, ""
}

func bmcStageVersion(stage *maintenancev1alpha1.PlanStage) string {
	switch stage.Kind {
	case maintenancev1alpha1.StageKindBMCSettings:
		if stage.Template.BMCSettings != nil {
			return stage.Template.BMCSettings.Version
		}
	case maintenancev1alpha1.StageKindBMCVersion:
		if stage.Template.BMCVersion != nil {
			return stage.Template.BMCVersion.Version
		}
	}
	return ""
}

func serverStageVersion(stage *maintenancev1alpha1.PlanStage) string {
	switch stage.Kind {
	case maintenancev1alpha1.StageKindBIOSSettings:
		if stage.Template.BIOSSettings != nil {
			return stage.Template.BIOSSettings.Version
		}
	case maintenancev1alpha1.StageKindBIOSVersion:
		if stage.Template.BIOSVersion != nil {
			return stage.Template.BIOSVersion.Version
		}
	}
	return ""
}

// ── Child CR construction ──────────────────────────────────────────────────────

func (r *MaintenancePlanRunReconciler) buildBMCObject(
	run *maintenancev1alpha1.MaintenancePlanRun,
	stage *maintenancev1alpha1.PlanStage,
) (client.Object, error) {
	name := bmcCRName(run.Name, stage.Name)
	lbls := map[string]string{
		planRunOwnerLabel: run.Name,
		stageNameLabel:    stage.Name,
	}

	switch stage.Kind {
	case maintenancev1alpha1.StageKindBMCSettings:
		if stage.Template.BMCSettings == nil {
			return nil, fmt.Errorf("stage %s: missing bmcSettings template", stage.Name)
		}
		return &metalv1alpha1.BMCSettings{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: lbls},
			Spec: metalv1alpha1.BMCSettingsSpec{
				BMCSettingsTemplate: metalv1alpha1.BMCSettingsTemplate{
					Version:                 stage.Template.BMCSettings.Version,
					SettingsMap:             stage.Template.BMCSettings.Settings,
					RetryPolicy:             stage.Template.BMCSettings.RetryPolicy,
					ServerMaintenancePolicy: stage.Template.BMCSettings.ServerMaintenancePolicy,
				},
				BMCRef: &corev1.LocalObjectReference{Name: run.Spec.BMCRef.Name},
			},
		}, nil

	case maintenancev1alpha1.StageKindBMCVersion:
		if stage.Template.BMCVersion == nil {
			return nil, fmt.Errorf("stage %s: missing bmcVersion template", stage.Name)
		}
		return &metalv1alpha1.BMCVersion{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: lbls},
			Spec: metalv1alpha1.BMCVersionSpec{
				BMCVersionTemplate: *stage.Template.BMCVersion,
				BMCRef:             &corev1.LocalObjectReference{Name: run.Spec.BMCRef.Name},
			},
		}, nil
	}

	return nil, fmt.Errorf("stage %s: kind %s is not BMC-scoped", stage.Name, stage.Kind)
}

func (r *MaintenancePlanRunReconciler) buildServerObject(
	run *maintenancev1alpha1.MaintenancePlanRun,
	stage *maintenancev1alpha1.PlanStage,
	serverName string,
) (client.Object, error) {
	name := serverCRName(run.Name, stage.Name, serverName)
	lbls := map[string]string{
		planRunOwnerLabel: run.Name,
		stageNameLabel:    stage.Name,
		serverNameLabel:   serverName,
	}

	switch stage.Kind {
	case maintenancev1alpha1.StageKindBIOSSettings:
		if stage.Template.BIOSSettings == nil {
			return nil, fmt.Errorf("stage %s: missing biosSettings template", stage.Name)
		}
		return &metalv1alpha1.BIOSSettings{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: lbls},
			Spec: metalv1alpha1.BIOSSettingsSpec{
				BIOSSettingsTemplate: *stage.Template.BIOSSettings,
				ServerRef:            &corev1.LocalObjectReference{Name: serverName},
			},
		}, nil

	case maintenancev1alpha1.StageKindBIOSVersion:
		if stage.Template.BIOSVersion == nil {
			return nil, fmt.Errorf("stage %s: missing biosVersion template", stage.Name)
		}
		return &metalv1alpha1.BIOSVersion{
			ObjectMeta: metav1.ObjectMeta{Name: name, Labels: lbls},
			Spec: metalv1alpha1.BIOSVersionSpec{
				BIOSVersionTemplate: *stage.Template.BIOSVersion,
				ServerRef:           &corev1.LocalObjectReference{Name: serverName},
			},
		}, nil
	}

	return nil, fmt.Errorf("stage %s: kind %s is not Server-scoped", stage.Name, stage.Kind)
}

// ── Child CR status polling ────────────────────────────────────────────────────

func (r *MaintenancePlanRunReconciler) checkCRStatus(
	ctx context.Context,
	kind maintenancev1alpha1.StageKind,
	name string,
) (done, failed bool, message string, err error) {
	key := types.NamespacedName{Name: name}

	switch kind {
	case maintenancev1alpha1.StageKindBMCSettings:
		obj := &metalv1alpha1.BMCSettings{}
		if err = r.Get(ctx, key, obj); err != nil {
			return false, false, "", client.IgnoreNotFound(err)
		}
		switch obj.Status.State {
		case metalv1alpha1.BMCSettingsStateApplied:
			return true, false, "", nil
		case metalv1alpha1.BMCSettingsStateFailed:
			return true, true, "BMCSettings reached Failed state", nil
		}

	case maintenancev1alpha1.StageKindBMCVersion:
		obj := &metalv1alpha1.BMCVersion{}
		if err = r.Get(ctx, key, obj); err != nil {
			return false, false, "", client.IgnoreNotFound(err)
		}
		switch obj.Status.State {
		case metalv1alpha1.BMCVersionStateCompleted:
			return true, false, "", nil
		case metalv1alpha1.BMCVersionStateFailed:
			return true, true, "BMCVersion reached Failed state", nil
		}

	case maintenancev1alpha1.StageKindBIOSSettings:
		obj := &metalv1alpha1.BIOSSettings{}
		if err = r.Get(ctx, key, obj); err != nil {
			return false, false, "", client.IgnoreNotFound(err)
		}
		switch obj.Status.State {
		case metalv1alpha1.BIOSSettingsStateApplied:
			return true, false, "", nil
		case metalv1alpha1.BIOSSettingsStateFailed:
			return true, true, "BIOSSettings reached Failed state", nil
		}

	case maintenancev1alpha1.StageKindBIOSVersion:
		obj := &metalv1alpha1.BIOSVersion{}
		if err = r.Get(ctx, key, obj); err != nil {
			return false, false, "", client.IgnoreNotFound(err)
		}
		switch obj.Status.State {
		case metalv1alpha1.BIOSVersionStateCompleted:
			return true, false, "", nil
		case metalv1alpha1.BIOSVersionStateFailed:
			return true, true, "BIOSVersion reached Failed state", nil
		}
	}

	return false, false, "", nil
}

// ── Deletion ──────────────────────────────────────────────────────────────────

func (r *MaintenancePlanRunReconciler) reconcileDelete(ctx context.Context, run *maintenancev1alpha1.MaintenancePlanRun) (ctrl.Result, error) {
	if err := r.deleteChildCRs(ctx, run); err != nil {
		return ctrl.Result{}, err
	}
	controllerutil.RemoveFinalizer(run, planRunFinalizer)
	if err := r.Update(ctx, run); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// deleteChildCRs deletes all child CRs owned by this run. BMCSettings (and other
// child types) block deletion while InProgress via a validating webhook. We add
// the force-delete annotation before each delete call so the webhook permits it.
func (r *MaintenancePlanRunReconciler) deleteChildCRs(ctx context.Context, run *maintenancev1alpha1.MaintenancePlanRun) error {
	matchRun := client.MatchingLabels{planRunOwnerLabel: run.Name}

	bmcSettingsList := &metalv1alpha1.BMCSettingsList{}
	if err := r.List(ctx, bmcSettingsList, matchRun); err != nil {
		return fmt.Errorf("listing BMCSettings for deletion: %w", err)
	}
	for i := range bmcSettingsList.Items {
		obj := &bmcSettingsList.Items[i]
		if err := r.forceDelete(ctx, obj); err != nil {
			return fmt.Errorf("deleting BMCSettings %s: %w", obj.Name, err)
		}
	}

	bmcVersionList := &metalv1alpha1.BMCVersionList{}
	if err := r.List(ctx, bmcVersionList, matchRun); err != nil {
		return fmt.Errorf("listing BMCVersions for deletion: %w", err)
	}
	for i := range bmcVersionList.Items {
		obj := &bmcVersionList.Items[i]
		if err := r.forceDelete(ctx, obj); err != nil {
			return fmt.Errorf("deleting BMCVersion %s: %w", obj.Name, err)
		}
	}

	biosSettingsList := &metalv1alpha1.BIOSSettingsList{}
	if err := r.List(ctx, biosSettingsList, matchRun); err != nil {
		return fmt.Errorf("listing BIOSSettings for deletion: %w", err)
	}
	for i := range biosSettingsList.Items {
		obj := &biosSettingsList.Items[i]
		if err := r.forceDelete(ctx, obj); err != nil {
			return fmt.Errorf("deleting BIOSSettings %s: %w", obj.Name, err)
		}
	}

	biosVersionList := &metalv1alpha1.BIOSVersionList{}
	if err := r.List(ctx, biosVersionList, matchRun); err != nil {
		return fmt.Errorf("listing BIOSVersions for deletion: %w", err)
	}
	for i := range biosVersionList.Items {
		obj := &biosVersionList.Items[i]
		if err := r.forceDelete(ctx, obj); err != nil {
			return fmt.Errorf("deleting BIOSVersion %s: %w", obj.Name, err)
		}
	}

	return nil
}

// forceDelete annotates obj to bypass the InProgress webhook guard then deletes it.
func (r *MaintenancePlanRunReconciler) forceDelete(ctx context.Context, obj client.Object) error {
	base := obj.DeepCopyObject().(client.Object)
	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[metalv1alpha1.OperationAnnotation] = metalv1alpha1.OperationAnnotationForceUpdateOrDeleteInProgress
	obj.SetAnnotations(annotations)
	if err := r.Patch(ctx, obj, client.MergeFrom(base)); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err := r.Delete(ctx, obj); client.IgnoreNotFound(err) != nil {
		return err
	}
	return nil
}

// ── Naming helpers ────────────────────────────────────────────────────────────

// bmcCRName produces a deterministic name for a BMC-scoped child CR.
func bmcCRName(runName, stageName string) string {
	return fmt.Sprintf("%s-%s", runName, stageName)
}

// serverCRName produces a deterministic name for a Server-scoped child CR.
func serverCRName(runName, stageName, serverName string) string {
	return fmt.Sprintf("%s-%s-%s", runName, stageName, serverName)
}

func (r *MaintenancePlanRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	enqueueRunsForChild := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		runName, ok := obj.GetLabels()[planRunOwnerLabel]
		if !ok {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: runName}}}
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&maintenancev1alpha1.MaintenancePlanRun{}).
		Watches(&metalv1alpha1.BMCSettings{}, enqueueRunsForChild).
		Watches(&metalv1alpha1.BMCVersion{}, enqueueRunsForChild).
		Watches(&metalv1alpha1.BIOSSettings{}, enqueueRunsForChild).
		Watches(&metalv1alpha1.BIOSVersion{}, enqueueRunsForChild).
		Complete(r)
}
