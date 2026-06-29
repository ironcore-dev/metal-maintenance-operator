// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	planOwnerLabel = "maintenance.metal.ironcore.dev/plan"
	bmcNameLabel   = "maintenance.metal.ironcore.dev/bmc"
)

// MaintenancePlanReconciler creates and tracks MaintenancePlanRuns for selected servers.
type MaintenancePlanReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=maintenanceplans,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=maintenanceplans/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=maintenanceplans/finalizers,verbs=update
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=maintenanceplanruns,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=bmcs,verbs=get;list;watch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=bmcsettings;bmcversions;biossettings;biosversions,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *MaintenancePlanReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	plan := &maintenancev1alpha1.MaintenancePlan{}
	if err := r.Get(ctx, req.NamespacedName, plan); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	original := plan.DeepCopy()

	result, err := r.reconcilePlan(ctx, plan)

	if patchErr := r.Status().Patch(ctx, plan, client.MergeFrom(original)); patchErr != nil {
		return ctrl.Result{}, patchErr
	}

	return result, err
}

// bmcGroup holds the BMC and all servers that share it.
type bmcGroup struct {
	bmc     *metalv1alpha1.BMC
	servers []*metalv1alpha1.Server
}

func (r *MaintenancePlanReconciler) reconcilePlan(ctx context.Context, plan *maintenancev1alpha1.MaintenancePlan) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("plan", plan.Name)

	selector, err := metav1.LabelSelectorAsSelector(&plan.Spec.ServerSelector)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("invalid serverSelector: %w", err)
	}

	serverList := &metalv1alpha1.ServerList{}
	if err := r.List(ctx, serverList, &client.ListOptions{LabelSelector: selector}); err != nil {
		return ctrl.Result{}, fmt.Errorf("list servers: %w", err)
	}

	// Group servers by their BMC name. Servers without a BMCRef are skipped.
	groups, err := r.groupByBMC(ctx, serverList.Items)
	if err != nil {
		return ctrl.Result{}, err
	}

	// List existing runs for this plan, keyed by BMC name.
	existingRuns := &maintenancev1alpha1.MaintenancePlanRunList{}
	if err := r.List(ctx, existingRuns, client.MatchingLabels{planOwnerLabel: plan.Name}); err != nil {
		return ctrl.Result{}, fmt.Errorf("list runs: %w", err)
	}
	existingByBMC := make(map[string]*maintenancev1alpha1.MaintenancePlanRun, len(existingRuns.Items))
	for i := range existingRuns.Items {
		run := &existingRuns.Items[i]
		existingByBMC[run.Spec.BMCRef.Name] = run
	}

	// Count active runs to enforce maxConcurrent.
	activeCount := int32(0)
	for i := range existingRuns.Items {
		run := &existingRuns.Items[i]
		if run.Status.Phase != maintenancev1alpha1.MaintenancePlanRunPhaseSucceeded &&
			run.Status.Phase != maintenancev1alpha1.MaintenancePlanRunPhaseFailed {
			activeCount++
		}
	}

	// Check completed runs for firmware regression. If any BMC or server BIOS
	// firmware has been rolled back below the target version, delete all final-stage
	// CRs (both BMC-scoped and server-scoped) then delete the run. This clears the
	// way for a new run that captures the rolled-back baseline and re-executes all
	// required intermediate hops.
	//
	// BMC-scoped final CRs must be removed so the BMCSettings/BMCVersion webhooks
	// accept new CRs (they reject duplicates targeting the same BMC).
	// Server-scoped final CRs must be removed so the BIOSSettings/BIOSVersion
	// webhooks accept new CRs (they reject duplicates targeting the same server).
	//
	// We fetch BMC and server objects directly rather than relying on groups so the
	// check works even when no servers currently match the plan's serverSelector.
	for bmcName, run := range existingByBMC {
		if run.Status.Phase != maintenancev1alpha1.MaintenancePlanRunPhaseSucceeded {
			continue
		}

		reason, bmcRegressed, biosRegressed, err := r.detectFirmwareRegression(ctx, run)
		if err != nil {
			logger.Error(err, "failed to check firmware regression", "run", run.Name)
			continue
		}
		if reason == "" {
			continue
		}

		if bmcRegressed {
			if err := r.deleteBMCScopedFinalCRs(ctx, run); err != nil {
				logger.Error(err, "failed to delete BMC-scoped final CRs before deleting regressed run", "run", run.Name)
				continue
			}
		}
		if biosRegressed {
			if err := r.deleteServerScopedFinalCRs(ctx, run); err != nil {
				logger.Error(err, "failed to delete server-scoped final CRs before deleting regressed run", "run", run.Name)
				continue
			}
		}
		if err := r.Delete(ctx, run); err != nil && !apierrors.IsNotFound(err) {
			logger.Error(err, "failed to delete regressed run", "run", run.Name)
			continue
		}
		delete(existingByBMC, bmcName)
		logger.Info("deleted run due to firmware regression", "run", run.Name, "reason", reason)
		r.Recorder.Eventf(plan, corev1.EventTypeWarning, "FirmwareRegression",
			"firmware regression detected for BMC %s: %s — restarting run", bmcName, reason)
	}

	// Create one run per unique BMC that doesn't have one yet.
	for bmcName, group := range groups {
		if _, exists := existingByBMC[bmcName]; exists {
			continue
		}

		if activeCount >= plan.Spec.MaxConcurrent {
			logger.Info("maxConcurrent reached, deferring run creation", "activeRuns", activeCount)
			break
		}

		run, err := r.buildRun(plan, group)
		if err != nil {
			logger.Error(err, "failed to build run", "bmc", bmcName)
			continue
		}

		if err := r.Create(ctx, run); err != nil {
			if !apierrors.IsAlreadyExists(err) {
				logger.Error(err, "failed to create run", "bmc", bmcName)
				continue
			}
		} else {
			logger.Info("created run", "run", run.Name, "bmc", bmcName)
			r.Recorder.Eventf(plan, corev1.EventTypeNormal, "RunCreated",
				"created MaintenancePlanRun %s for BMC %s", run.Name, bmcName)
			activeCount++
		}
	}

	return ctrl.Result{}, r.updatePlanStatus(ctx, plan, existingRuns)
}

// groupByBMC resolves each server's BMCRef and groups servers by BMC name.
// Servers without a BMCRef are silently skipped; BMC lookup failures are logged and skipped.
func (r *MaintenancePlanReconciler) groupByBMC(ctx context.Context, servers []metalv1alpha1.Server) (map[string]*bmcGroup, error) {
	logger := log.FromContext(ctx)
	groups := make(map[string]*bmcGroup)

	for i := range servers {
		server := &servers[i]
		if server.Spec.BMCRef == nil {
			logger.Info("skipping server with no BMCRef", "server", server.Name)
			continue
		}
		bmcName := server.Spec.BMCRef.Name

		if _, ok := groups[bmcName]; !ok {
			bmc := &metalv1alpha1.BMC{}
			if err := r.Get(ctx, types.NamespacedName{Name: bmcName}, bmc); err != nil {
				logger.Error(err, "failed to get BMC for server — skipping", "server", server.Name, "bmc", bmcName)
				continue
			}
			groups[bmcName] = &bmcGroup{bmc: bmc}
		}
		groups[bmcName].servers = append(groups[bmcName].servers, server)
	}

	return groups, nil
}

// buildRun constructs a MaintenancePlanRun for a BMC group.
func (r *MaintenancePlanReconciler) buildRun(
	plan *maintenancev1alpha1.MaintenancePlan,
	group *bmcGroup,
) (*maintenancev1alpha1.MaintenancePlanRun, error) {
	if len(group.servers) == 0 {
		return nil, fmt.Errorf("BMC %s has no associated servers", group.bmc.Name)
	}

	serverRefs := make([]corev1.LocalObjectReference, len(group.servers))
	baselineBIOSVersions := make(map[string]string, len(group.servers))
	for i, srv := range group.servers {
		serverRefs[i] = corev1.LocalObjectReference{Name: srv.Name}
		if srv.Status.BIOSVersion != "" {
			baselineBIOSVersions[srv.Name] = srv.Status.BIOSVersion
		}
	}

	runName := fmt.Sprintf("%s-%s", plan.Name, group.bmc.Name)

	run := &maintenancev1alpha1.MaintenancePlanRun{
		ObjectMeta: metav1.ObjectMeta{
			Name: runName,
			Labels: map[string]string{
				planOwnerLabel: plan.Name,
				bmcNameLabel:   group.bmc.Name,
			},
		},
		Spec: maintenancev1alpha1.MaintenancePlanRunSpec{
			PlanRef:              corev1.LocalObjectReference{Name: plan.Name},
			BMCRef:               corev1.LocalObjectReference{Name: group.bmc.Name},
			ServerRefs:           serverRefs,
			BaselineBMCVersion:   group.bmc.Status.FirmwareVersion,
			BaselineBIOSVersions: baselineBIOSVersions,
			Trigger:              maintenancev1alpha1.RunTriggerInitial,
			Stages:               plan.Spec.Stages,
		},
	}

	if err := ctrl.SetControllerReference(plan, run, r.Scheme); err != nil {
		return nil, fmt.Errorf("set owner reference: %w", err)
	}

	return run, nil
}

// updatePlanStatus aggregates run outcomes into the plan's status.
func (r *MaintenancePlanReconciler) updatePlanStatus(
	ctx context.Context,
	plan *maintenancev1alpha1.MaintenancePlan,
	runs *maintenancev1alpha1.MaintenancePlanRunList,
) error {
	var active, succeeded, failed int32
	for i := range runs.Items {
		switch runs.Items[i].Status.Phase {
		case maintenancev1alpha1.MaintenancePlanRunPhaseSucceeded:
			succeeded++
		case maintenancev1alpha1.MaintenancePlanRunPhaseFailed:
			failed++
		default:
			active++
		}
	}

	plan.Status.TotalRuns = int32(len(runs.Items))
	plan.Status.ActiveRuns = active
	plan.Status.SucceededRuns = succeeded
	plan.Status.FailedRuns = failed
	plan.Status.ObservedGeneration = plan.Generation

	switch {
	case failed > 0:
		plan.Status.Phase = maintenancev1alpha1.MaintenancePlanPhaseFailed
	case active > 0:
		plan.Status.Phase = maintenancev1alpha1.MaintenancePlanPhaseActive
	case succeeded == plan.Status.TotalRuns && plan.Status.TotalRuns > 0:
		plan.Status.Phase = maintenancev1alpha1.MaintenancePlanPhaseCompleted
	default:
		plan.Status.Phase = maintenancev1alpha1.MaintenancePlanPhasePending
	}

	return nil
}

// targetBMCVersion returns the version from the last BMCVersion stage in the run, or "".
func targetBMCVersion(run *maintenancev1alpha1.MaintenancePlanRun) string {
	for i := len(run.Spec.Stages) - 1; i >= 0; i-- {
		s := &run.Spec.Stages[i]
		if s.Kind == maintenancev1alpha1.StageKindBMCVersion && s.Template.BMCVersion != nil {
			return s.Template.BMCVersion.Version
		}
	}
	return ""
}

// targetBIOSVersion returns the version from the last BIOSVersion stage in the run, or "".
func targetBIOSVersion(run *maintenancev1alpha1.MaintenancePlanRun) string {
	for i := len(run.Spec.Stages) - 1; i >= 0; i-- {
		s := &run.Spec.Stages[i]
		if s.Kind == maintenancev1alpha1.StageKindBIOSVersion && s.Template.BIOSVersion != nil {
			return s.Template.BIOSVersion.Version
		}
	}
	return ""
}

// detectFirmwareRegression returns a human-readable reason (non-empty when
// regression is detected), plus flags indicating whether BMC and/or BIOS
// firmware has regressed below the plan's target version.
func (r *MaintenancePlanReconciler) detectFirmwareRegression(ctx context.Context, run *maintenancev1alpha1.MaintenancePlanRun) (reason string, bmcRegressed, biosRegressed bool, err error) {
	logger := log.FromContext(ctx).WithValues("run", run.Name)

	if target := targetBMCVersion(run); target != "" {
		bmc := &metalv1alpha1.BMC{}
		if fetchErr := r.Get(ctx, types.NamespacedName{Name: run.Spec.BMCRef.Name}, bmc); fetchErr != nil {
			logger.Error(fetchErr, "failed to get BMC for regression check", "bmc", run.Spec.BMCRef.Name)
		} else if bmc.Status.FirmwareVersion < target {
			bmcRegressed = true
			reason += fmt.Sprintf("BMC %s firmware at %s < target %s; ", run.Spec.BMCRef.Name, bmc.Status.FirmwareVersion, target)
		}
	}

	if target := targetBIOSVersion(run); target != "" {
		for _, ref := range run.Spec.ServerRefs {
			srv := &metalv1alpha1.Server{}
			if fetchErr := r.Get(ctx, types.NamespacedName{Name: ref.Name}, srv); fetchErr != nil {
				logger.Error(fetchErr, "failed to get Server for BIOS regression check", "server", ref.Name)
				continue
			}
			if srv.Status.BIOSVersion != "" && srv.Status.BIOSVersion < target {
				biosRegressed = true
				reason += fmt.Sprintf("server %s BIOS at %s < target %s; ", ref.Name, srv.Status.BIOSVersion, target)
				break
			}
		}
	}

	return reason, bmcRegressed, biosRegressed, nil
}

// deleteBMCScopedFinalCRs deletes the BMC-scoped final-stage CRs (BMCVersion and
// BMCSettings) owned by the given run. These CRs are in a terminal state and were
// silenced with ignore-reconciliation at run completion. Do NOT add
// force-update-or-delete-inprogress — that would un-silence them and give
// metal-operator a window to create a spurious ServerMaintenance.
func (r *MaintenancePlanReconciler) deleteBMCScopedFinalCRs(ctx context.Context, run *maintenancev1alpha1.MaintenancePlanRun) error {
	del := func(obj client.Object, name string) error {
		if err := r.Get(ctx, client.ObjectKey{Name: name}, obj); err != nil {
			return client.IgnoreNotFound(err)
		}
		return client.IgnoreNotFound(r.Delete(ctx, obj))
	}
	for i, ss := range run.Status.StageStatuses {
		if ss.AppliedSpec != nil {
			continue
		}
		stage := &run.Spec.Stages[i]
		switch stage.Kind {
		case maintenancev1alpha1.StageKindBMCSettings:
			if err := del(&metalv1alpha1.BMCSettings{}, bmcCRName(run.Name, stage.Name)); err != nil {
				return err
			}
		case maintenancev1alpha1.StageKindBMCVersion:
			if err := del(&metalv1alpha1.BMCVersion{}, bmcCRName(run.Name, stage.Name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// deleteServerScopedFinalCRs deletes the server-scoped final-stage CRs
// (BIOSVersion and BIOSSettings) owned by the given run. Same reasoning as
// deleteBMCScopedFinalCRs — no force annotation needed or safe to use.
func (r *MaintenancePlanReconciler) deleteServerScopedFinalCRs(ctx context.Context, run *maintenancev1alpha1.MaintenancePlanRun) error {
	del := func(obj client.Object, name string) error {
		if err := r.Get(ctx, client.ObjectKey{Name: name}, obj); err != nil {
			return client.IgnoreNotFound(err)
		}
		return client.IgnoreNotFound(r.Delete(ctx, obj))
	}
	for i, ss := range run.Status.StageStatuses {
		if ss.AppliedSpec != nil {
			continue
		}
		stage := &run.Spec.Stages[i]
		switch stage.Kind {
		case maintenancev1alpha1.StageKindBIOSSettings:
			for _, srv := range run.Spec.ServerRefs {
				if err := del(&metalv1alpha1.BIOSSettings{}, serverCRName(run.Name, stage.Name, srv.Name)); err != nil {
					return err
				}
			}
		case maintenancev1alpha1.StageKindBIOSVersion:
			for _, srv := range run.Spec.ServerRefs {
				if err := del(&metalv1alpha1.BIOSVersion{}, serverCRName(run.Name, stage.Name, srv.Name)); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (r *MaintenancePlanReconciler) SetupWithManager(mgr ctrl.Manager) error {
	enqueueForRun := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		planName, ok := obj.GetLabels()[planOwnerLabel]
		if !ok {
			return nil
		}
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: planName}}}
	})

	enqueueForServer := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		planList := &maintenancev1alpha1.MaintenancePlanList{}
		if err := mgr.GetClient().List(ctx, planList); err != nil {
			return nil
		}
		server, ok := obj.(*metalv1alpha1.Server)
		if !ok {
			return nil
		}
		var requests []reconcile.Request
		for i := range planList.Items {
			plan := &planList.Items[i]
			sel, err := metav1.LabelSelectorAsSelector(&plan.Spec.ServerSelector)
			if err != nil {
				continue
			}
			if sel.Matches(labels.Set(server.Labels)) {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{Name: plan.Name},
				})
			}
		}
		return requests
	})

	enqueueForBMC := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		// When a BMC's firmware status changes, re-check all plans that have a
		// completed run targeting this BMC so regression detection fires promptly.
		runList := &maintenancev1alpha1.MaintenancePlanRunList{}
		if err := mgr.GetClient().List(ctx, runList, client.MatchingLabels{bmcNameLabel: obj.GetName()}); err != nil {
			return nil
		}
		seen := map[string]struct{}{}
		var requests []reconcile.Request
		for i := range runList.Items {
			planName := runList.Items[i].Spec.PlanRef.Name
			if _, ok := seen[planName]; ok {
				continue
			}
			seen[planName] = struct{}{}
			requests = append(requests, reconcile.Request{
				NamespacedName: types.NamespacedName{Name: planName},
			})
		}
		return requests
	})

	enqueueForServerViaRuns := handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		// When a server's BIOS firmware status changes, re-check all plans that have
		// a completed run referencing this server so BIOS regression detection fires
		// promptly — even if the server no longer matches the plan's serverSelector.
		serverName := obj.GetName()
		runList := &maintenancev1alpha1.MaintenancePlanRunList{}
		if err := mgr.GetClient().List(ctx, runList); err != nil {
			return nil
		}
		seen := map[string]struct{}{}
		var requests []reconcile.Request
		for i := range runList.Items {
			run := &runList.Items[i]
			for _, ref := range run.Spec.ServerRefs {
				if ref.Name != serverName {
					continue
				}
				planName := run.Spec.PlanRef.Name
				if _, ok := seen[planName]; !ok {
					seen[planName] = struct{}{}
					requests = append(requests, reconcile.Request{
						NamespacedName: types.NamespacedName{Name: planName},
					})
				}
				break
			}
		}
		return requests
	})

	return ctrl.NewControllerManagedBy(mgr).
		For(&maintenancev1alpha1.MaintenancePlan{}).
		Watches(&maintenancev1alpha1.MaintenancePlanRun{}, enqueueForRun).
		Watches(&metalv1alpha1.Server{}, enqueueForServer).
		Watches(&metalv1alpha1.Server{}, enqueueForServerViaRuns).
		Watches(&metalv1alpha1.BMC{}, enqueueForBMC).
		Complete(r)
}
