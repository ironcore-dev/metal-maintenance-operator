// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/maintenance/v1alpha1"
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

	err := r.reconcilePlan(ctx, plan)

	if patchErr := r.Status().Patch(ctx, plan, client.MergeFrom(original)); patchErr != nil {
		return ctrl.Result{}, patchErr
	}

	return ctrl.Result{}, err
}

// bmcGroup holds the BMC and all servers that share it.
type bmcGroup struct {
	bmc     *metalv1alpha1.BMC
	servers []*metalv1alpha1.Server
}

func (r *MaintenancePlanReconciler) reconcilePlan(ctx context.Context, plan *maintenancev1alpha1.MaintenancePlan) error {
	logger := log.FromContext(ctx).WithValues("plan", plan.Name)

	selector, err := metav1.LabelSelectorAsSelector(&plan.Spec.ServerSelector)
	if err != nil {
		return fmt.Errorf("invalid serverSelector: %w", err)
	}

	serverList := &metalv1alpha1.ServerList{}
	if err := r.List(ctx, serverList, &client.ListOptions{LabelSelector: selector}); err != nil {
		return fmt.Errorf("list servers: %w", err)
	}

	// Group servers by their BMC name. Servers without a BMCRef are skipped.
	groups := r.groupByBMC(ctx, serverList.Items)

	// List existing runs for this plan, keyed by BMC name.
	existingRuns := &maintenancev1alpha1.MaintenancePlanRunList{}
	if err := r.List(ctx, existingRuns, client.MatchingLabels{planOwnerLabel: plan.Name}); err != nil {
		return fmt.Errorf("list runs: %w", err)
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

	r.updatePlanStatus(plan, existingRuns)
	return nil
}

// groupByBMC resolves each server's BMCRef and groups servers by BMC name.
// Servers without a BMCRef are silently skipped; BMC lookup failures are logged and skipped.
func (r *MaintenancePlanReconciler) groupByBMC(ctx context.Context, servers []metalv1alpha1.Server) map[string]*bmcGroup {
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

	return groups
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
	plan *maintenancev1alpha1.MaintenancePlan,
	runs *maintenancev1alpha1.MaintenancePlanRunList,
) {
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

	return ctrl.NewControllerManagedBy(mgr).
		For(&maintenancev1alpha1.MaintenancePlan{}).
		Watches(&maintenancev1alpha1.MaintenancePlanRun{}, enqueueForRun).
		Watches(&metalv1alpha1.Server{}, enqueueForServer).
		Complete(r)
}
