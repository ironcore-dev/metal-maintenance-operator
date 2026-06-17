// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/maintenance/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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
			// Intermediate version hops are deleted once completed and snapshotted.
			// Best-effort: errors are logged but do not block progress.
			if status.Phase == maintenancev1alpha1.StagePhaseSucceeded &&
				r.isIntermediateStage(run, i) && status.AppliedSpec == nil {
				r.snapshotAndDeleteIntermediateCR(ctx, run, stage, status)
			}
			run.Status.CurrentStageIndex = int32(i + 1)
			continue

		case maintenancev1alpha1.StagePhaseFailed:
			run.Status.Phase = maintenancev1alpha1.MaintenancePlanRunPhaseFailed
			return ctrl.Result{}, nil

		case maintenancev1alpha1.StagePhasePending:
			if isBMCScoped(stage.Kind) {
				return r.startBMCStage(ctx, run, stage, status, i)
			}
			return r.startServerStage(ctx, run, stage, status, i)

		case maintenancev1alpha1.StagePhaseRunning:
			if isBMCScoped(stage.Kind) {
				return r.pollBMCStage(ctx, run, stage, status)
			}
			return r.pollServerStage(ctx, run, stage, status)
		}
	}

	run.Status.Phase = maintenancev1alpha1.MaintenancePlanRunPhaseSucceeded
	now := metav1.Now()
	run.Status.CompletionTime = &now
	logger.Info("run completed successfully")
	r.Recorder.Event(run, corev1.EventTypeNormal, "RunSucceeded", "all stages completed successfully")
	return ctrl.Result{}, nil
}

// isIntermediateStage returns true when a later stage of the same kind exists,
// meaning this stage is superseded and its CR should be cleaned up once complete.
func (r *MaintenancePlanRunReconciler) isIntermediateStage(run *maintenancev1alpha1.MaintenancePlanRun, stageIdx int) bool {
	kind := run.Spec.Stages[stageIdx].Kind
	for i := stageIdx + 1; i < len(run.Spec.Stages); i++ {
		if run.Spec.Stages[i].Kind == kind {
			return true
		}
	}
	return false
}

// snapshotAndDeleteIntermediateCR captures the spec of a completed intermediate-hop child CR
// into the stage status, then deletes the CR. Best-effort: errors are logged but do not
// block run progression. AppliedSpec is set to a non-nil sentinel even if the CR is absent,
// so we don't re-attempt on subsequent reconciles.
func (r *MaintenancePlanRunReconciler) snapshotAndDeleteIntermediateCR(
	ctx context.Context,
	run *maintenancev1alpha1.MaintenancePlanRun,
	stage *maintenancev1alpha1.PlanStage,
	status *maintenancev1alpha1.StageStatus,
) {
	logger := log.FromContext(ctx).WithValues("run", run.Name, "stage", stage.Name)

	if isBMCScoped(stage.Kind) {
		name := bmcCRName(run.Name, stage.Name)
		spec, err := r.fetchCRSpec(ctx, stage.Kind, name)
		if err != nil {
			logger.Error(err, "failed to fetch intermediate BMC CR spec for snapshot")
			return
		}
		// Mark as snapshotted whether or not CR existed.
		if spec != nil {
			status.AppliedSpec = spec
		} else {
			status.AppliedSpec = &runtime.RawExtension{Raw: []byte("{}")}
		}
		if err := r.deleteCRByName(ctx, stage.Kind, name); err != nil {
			logger.Error(err, "failed to delete intermediate BMC CR")
		}
	} else {
		// For server-scoped stages, record the first non-skipped server's spec as audit sample.
		for si := range status.ServerStatuses {
			ss := &status.ServerStatuses[si]
			if ss.Phase == maintenancev1alpha1.StagePhaseSkipped {
				continue
			}
			name := serverCRName(run.Name, stage.Name, ss.ServerRef.Name)
			if status.AppliedSpec == nil {
				spec, err := r.fetchCRSpec(ctx, stage.Kind, name)
				if err != nil {
					logger.Error(err, "failed to fetch intermediate server CR spec for snapshot", "server", ss.ServerRef.Name)
				} else if spec != nil {
					status.AppliedSpec = spec
				}
			}
			if err := r.deleteCRByName(ctx, stage.Kind, name); err != nil {
				logger.Error(err, "failed to delete intermediate server CR", "server", ss.ServerRef.Name)
			}
		}
		if status.AppliedSpec == nil {
			status.AppliedSpec = &runtime.RawExtension{Raw: []byte("{}")}
		}
	}
}

// fetchCRSpec reads a child CR and returns its spec as a RawExtension for audit storage.
// Returns nil, nil when the CR no longer exists.
func (r *MaintenancePlanRunReconciler) fetchCRSpec(ctx context.Context, kind maintenancev1alpha1.StageKind, name string) (*runtime.RawExtension, error) {
	key := types.NamespacedName{Name: name}
	var specObj interface{}

	switch kind {
	case maintenancev1alpha1.StageKindBMCSettings:
		obj := &metalv1alpha1.BMCSettings{}
		if err := r.Get(ctx, key, obj); err != nil {
			return nil, client.IgnoreNotFound(err)
		}
		specObj = obj.Spec
	case maintenancev1alpha1.StageKindBMCVersion:
		obj := &metalv1alpha1.BMCVersion{}
		if err := r.Get(ctx, key, obj); err != nil {
			return nil, client.IgnoreNotFound(err)
		}
		specObj = obj.Spec
	case maintenancev1alpha1.StageKindBIOSSettings:
		obj := &metalv1alpha1.BIOSSettings{}
		if err := r.Get(ctx, key, obj); err != nil {
			return nil, client.IgnoreNotFound(err)
		}
		specObj = obj.Spec
	case maintenancev1alpha1.StageKindBIOSVersion:
		obj := &metalv1alpha1.BIOSVersion{}
		if err := r.Get(ctx, key, obj); err != nil {
			return nil, client.IgnoreNotFound(err)
		}
		specObj = obj.Spec
	default:
		return nil, nil
	}

	if specObj == nil {
		return nil, nil
	}
	raw, err := json.Marshal(specObj)
	if err != nil {
		return nil, fmt.Errorf("marshal spec: %w", err)
	}
	return &runtime.RawExtension{Raw: raw}, nil
}

// deleteCRByName force-deletes a child CR by kind and name.
func (r *MaintenancePlanRunReconciler) deleteCRByName(ctx context.Context, kind maintenancev1alpha1.StageKind, name string) error {
	key := types.NamespacedName{Name: name}
	switch kind {
	case maintenancev1alpha1.StageKindBMCSettings:
		obj := &metalv1alpha1.BMCSettings{}
		if err := r.Get(ctx, key, obj); err != nil {
			return client.IgnoreNotFound(err)
		}
		return r.forceDelete(ctx, obj)
	case maintenancev1alpha1.StageKindBMCVersion:
		obj := &metalv1alpha1.BMCVersion{}
		if err := r.Get(ctx, key, obj); err != nil {
			return client.IgnoreNotFound(err)
		}
		return r.forceDelete(ctx, obj)
	case maintenancev1alpha1.StageKindBIOSSettings:
		obj := &metalv1alpha1.BIOSSettings{}
		if err := r.Get(ctx, key, obj); err != nil {
			return client.IgnoreNotFound(err)
		}
		return r.forceDelete(ctx, obj)
	case maintenancev1alpha1.StageKindBIOSVersion:
		obj := &metalv1alpha1.BIOSVersion{}
		if err := r.Get(ctx, key, obj); err != nil {
			return client.IgnoreNotFound(err)
		}
		return r.forceDelete(ctx, obj)
	}
	return nil
}

// ── BMC-scoped stage (one child CR for the whole BMC) ─────────────────────────

func (r *MaintenancePlanRunReconciler) startBMCStage(
	ctx context.Context,
	run *maintenancev1alpha1.MaintenancePlanRun,
	stage *maintenancev1alpha1.PlanStage,
	status *maintenancev1alpha1.StageStatus,
	stageIdx int,
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
	stageIdx int,
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

// currentBMCVersion returns the effective current BMC version by scanning
// completed stage statuses for the last succeeded BMCVersion stage. Falls back
// to the baseline recorded at run creation if no upgrade has completed yet.
func currentBMCVersion(run *maintenancev1alpha1.MaintenancePlanRun) string {
	current := run.Spec.BaselineBMCVersion
	for i, ss := range run.Status.StageStatuses {
		if ss.Phase != maintenancev1alpha1.StagePhaseSucceeded {
			continue
		}
		if i >= len(run.Spec.Stages) {
			break
		}
		if run.Spec.Stages[i].Kind == maintenancev1alpha1.StageKindBMCVersion {
			if v := bmcStageVersion(&run.Spec.Stages[i]); v != "" {
				current = v
			}
		}
	}
	return current
}

// currentBIOSVersion returns the effective current BIOS version for a server
// by scanning completed stage statuses for the last succeeded BIOSVersion
// stage for that server. Falls back to the baseline if no upgrade has run yet.
func currentBIOSVersion(run *maintenancev1alpha1.MaintenancePlanRun, serverName string) string {
	current := run.Spec.BaselineBIOSVersions[serverName]
	for i, ss := range run.Status.StageStatuses {
		if ss.Phase != maintenancev1alpha1.StagePhaseSucceeded {
			continue
		}
		if i >= len(run.Spec.Stages) {
			break
		}
		if run.Spec.Stages[i].Kind != maintenancev1alpha1.StageKindBIOSVersion {
			continue
		}
		for _, srv := range ss.ServerStatuses {
			if srv.ServerRef.Name == serverName && srv.Phase == maintenancev1alpha1.StagePhaseSucceeded {
				if v := serverStageVersion(&run.Spec.Stages[i]); v != "" {
					current = v
				}
			}
		}
	}
	return current
}

func (r *MaintenancePlanRunReconciler) shouldSkipBMC(
	run *maintenancev1alpha1.MaintenancePlanRun,
	stage *maintenancev1alpha1.PlanStage,
) (bool, string) {
	target := bmcStageVersion(stage)
	current := currentBMCVersion(run)
	if target == "" || current == "" {
		return false, ""
	}
	if stage.Kind == maintenancev1alpha1.StageKindBMCSettings {
		if target != current {
			return true, fmt.Sprintf("settings version %s != current %s, skipping", target, current)
		}
	} else {
		if target == current {
			return true, fmt.Sprintf("already at target %s, skipping", target)
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
	current := currentBIOSVersion(run, serverName)
	if target == "" || current == "" {
		return false, ""
	}
	if stage.Kind == maintenancev1alpha1.StageKindBIOSSettings {
		if target != current {
			return true, fmt.Sprintf("settings version %s != current %s for server %s, skipping", target, current, serverName)
		}
	} else {
		if target == current {
			return true, fmt.Sprintf("already at target %s for server %s, skipping", target, serverName)
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

// deleteChildCRs deletes intermediate-stage child CRs owned by this run.
// Final-stage CRs (those whose StageStatus has no AppliedSpec snapshot) are
// left in place — they represent the desired hardware state and should persist
// independently of the run. The validating webhook blocks deletion of InProgress
// CRs; we add the force-delete annotation before each delete call.
func (r *MaintenancePlanRunReconciler) deleteChildCRs(ctx context.Context, run *maintenancev1alpha1.MaintenancePlanRun) error {
	matchRun := client.MatchingLabels{planRunOwnerLabel: run.Name}

	// Build the set of CR names that belong to final stages (AppliedSpec is nil).
	// These CRs are kept; only intermediate-stage CRs (AppliedSpec non-nil) are deleted.
	keepNames := map[string]struct{}{}
	for i, ss := range run.Status.StageStatuses {
		if ss.AppliedSpec != nil {
			continue // intermediate — will be deleted
		}
		stage := &run.Spec.Stages[i]
		if isBMCScoped(stage.Kind) {
			keepNames[bmcCRName(run.Name, stage.Name)] = struct{}{}
		} else {
			for _, srv := range run.Spec.ServerRefs {
				keepNames[serverCRName(run.Name, stage.Name, srv.Name)] = struct{}{}
			}
		}
	}

	shouldKeep := func(name string) bool {
		_, ok := keepNames[name]
		return ok
	}

	bmcSettingsList := &metalv1alpha1.BMCSettingsList{}
	if err := r.List(ctx, bmcSettingsList, matchRun); err != nil {
		return fmt.Errorf("listing BMCSettings for deletion: %w", err)
	}
	for i := range bmcSettingsList.Items {
		obj := &bmcSettingsList.Items[i]
		if shouldKeep(obj.Name) {
			continue
		}
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
		if shouldKeep(obj.Name) {
			continue
		}
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
		if shouldKeep(obj.Name) {
			continue
		}
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
		if shouldKeep(obj.Name) {
			continue
		}
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
