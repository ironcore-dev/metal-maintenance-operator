// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	clientutils "github.com/ironcore-dev/controller-utils/clientutils"
	readinessv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/readiness/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	networkWiringCheckFinalizer = "readiness.metal.ironcore.dev/networkwiringcheck"
	networkReadyConditionType   = "NetworkReady"
	networkNotReadyTaintKey     = "metal.ironcore.dev/network-not-ready"

	reasonMatch            = "Match"
	reasonNoExpectedSpec   = "NoExpectedSpec"
	reasonInterfaceMissing = "InterfaceMissing"
	reasonCarrierDown      = "CarrierDown"
	reasonNeighborMismatch = "NeighborMismatch"

	// serverRefNameField is the field index path used to map Server names back to NetworkWiringChecks.
	serverRefNameField = ".spec.serverRef.name"
)

// NetworkWiringCheckReconciler reconciles a NetworkWiringCheck object.
type NetworkWiringCheckReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=readiness.metal.ironcore.dev,resources=networkwiringchecks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=readiness.metal.ironcore.dev,resources=networkwiringchecks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=readiness.metal.ironcore.dev,resources=networkwiringchecks/finalizers,verbs=update
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=serverreadinessrules,verbs=get;list;watch;create;update;patch;delete

func (r *NetworkWiringCheckReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling NetworkWiringCheck", "name", req.NamespacedName)

	check := &readinessv1alpha1.NetworkWiringCheck{}
	if err := r.Get(ctx, req.NamespacedName, check); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if check.GetDeletionTimestamp() != nil {
		return r.reconcileDelete(ctx, check)
	}
	return r.reconcileExists(ctx, check)
}

func (r *NetworkWiringCheckReconciler) reconcileDelete(ctx context.Context, check *readinessv1alpha1.NetworkWiringCheck) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	ruleName := networkWiringCheckRuleName(check)
	rule := &metalv1alpha1.ServerReadinessRule{}
	if err := r.Get(ctx, client.ObjectKey{Name: ruleName}, rule); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("getting ServerReadinessRule %s: %w", ruleName, err)
		}
	} else {
		logger.Info("Deleting ServerReadinessRule", "name", ruleName)
		if err := r.Delete(ctx, rule); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("deleting ServerReadinessRule %s: %w", ruleName, err)
		}
	}

	server := &metalv1alpha1.Server{}
	if err := r.Get(ctx, client.ObjectKey{Name: check.Spec.ServerRef.Name}, server); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("getting server %s: %w", check.Spec.ServerRef.Name, err)
		}
	} else {
		serverBase := server.DeepCopy()
		apimeta.RemoveStatusCondition(&server.Status.Conditions, networkReadyConditionType)
		if err := r.Status().Patch(ctx, server, client.MergeFrom(serverBase)); err != nil {
			return ctrl.Result{}, fmt.Errorf("clearing NetworkReady condition on server %s: %w", server.Name, err)
		}
	}

	if err := clientutils.PatchRemoveFinalizer(ctx, r.Client, check, networkWiringCheckFinalizer); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *NetworkWiringCheckReconciler) reconcileExists(ctx context.Context, check *readinessv1alpha1.NetworkWiringCheck) (ctrl.Result, error) {
	if _, err := clientutils.PatchEnsureFinalizer(ctx, r.Client, check, networkWiringCheckFinalizer); err != nil {
		return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
	}

	if err := r.ensureReadinessRule(ctx, check); err != nil {
		return ctrl.Result{}, err
	}

	server := &metalv1alpha1.Server{}
	if err := r.Get(ctx, client.ObjectKey{Name: check.Spec.ServerRef.Name}, server); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("getting server %s: %w", check.Spec.ServerRef.Name, err)
	}

	hasSpec := len(check.Spec.Network.Interfaces) > 0
	mismatches, ready := r.validateServer(server, check)

	if err := r.setNetworkReadyCondition(ctx, server, ready, hasSpec, mismatches); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, r.updateStatus(ctx, check, ready, mismatches)
}

func (r *NetworkWiringCheckReconciler) ensureReadinessRule(ctx context.Context, check *readinessv1alpha1.NetworkWiringCheck) error {
	logger := log.FromContext(ctx)
	ruleName := networkWiringCheckRuleName(check)

	existing := &metalv1alpha1.ServerReadinessRule{}
	err := r.Get(ctx, client.ObjectKey{Name: ruleName}, existing)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("getting ServerReadinessRule %s: %w", ruleName, err)
	}

	if apierrors.IsNotFound(err) {
		logger.Info("Creating ServerReadinessRule", "name", ruleName)
		return r.Create(ctx, buildReadinessRule(ruleName, check))
	}

	desired := buildReadinessRule(ruleName, check)
	if !reflect.DeepEqual(existing.Spec, desired.Spec) || !reflect.DeepEqual(existing.Labels, desired.Labels) {
		base := existing.DeepCopy()
		existing.Spec = desired.Spec
		existing.Labels = desired.Labels
		logger.Info("Updating ServerReadinessRule", "name", ruleName)
		if err := r.Patch(ctx, existing, client.MergeFrom(base)); err != nil {
			return fmt.Errorf("patching ServerReadinessRule %s: %w", ruleName, err)
		}
	}
	return nil
}

func buildReadinessRule(name string, check *readinessv1alpha1.NetworkWiringCheck) *metalv1alpha1.ServerReadinessRule {
	selector := check.Spec.ServerSelector
	if selector.MatchLabels == nil {
		selector.MatchLabels = map[string]string{}
	}
	return &metalv1alpha1.ServerReadinessRule{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"readiness.metal.ironcore.dev/owner-namespace": check.Namespace,
				"readiness.metal.ironcore.dev/owner-name":      check.Name,
			},
		},
		Spec: metalv1alpha1.ServerReadinessRuleSpec{
			Conditions: []metalv1alpha1.ConditionRequirement{
				{
					Type:           networkReadyConditionType,
					RequiredStatus: metav1.ConditionTrue,
				},
			},
			EnforcementMode: metalv1alpha1.EnforcementModeContinuous,
			Taint: metalv1alpha1.Taint{
				Key:    networkNotReadyTaintKey,
				Effect: metalv1alpha1.TaintEffectNoBind,
			},
			ServerSelector: selector,
		},
	}
}

func (r *NetworkWiringCheckReconciler) validateServer(server *metalv1alpha1.Server, check *readinessv1alpha1.NetworkWiringCheck) (mismatches []readinessv1alpha1.InterfaceMismatch, ready bool) {
	ready = true

	if len(check.Spec.Network.Interfaces) == 0 {
		return nil, true
	}

	actualByMAC := make(map[string]metalv1alpha1.NetworkInterface, len(server.Status.NetworkInterfaces))
	for _, nic := range server.Status.NetworkInterfaces {
		actualByMAC[strings.ToLower(nic.MACAddress)] = nic
	}

	for _, expected := range check.Spec.Network.Interfaces {
		mac := strings.ToLower(expected.MACAddress)
		actual, found := actualByMAC[mac]
		if !found {
			ready = false
			mismatches = append(mismatches, readinessv1alpha1.InterfaceMismatch{
				MACAddress: expected.MACAddress,
				Reason:     reasonInterfaceMissing,
				Message:    "interface not found",
			})
			continue
		}

		if expected.CarrierStatus != "" && actual.CarrierStatus != expected.CarrierStatus {
			ready = false
			mismatches = append(mismatches, readinessv1alpha1.InterfaceMismatch{
				MACAddress: expected.MACAddress,
				Reason:     reasonCarrierDown,
				Message:    fmt.Sprintf("carrierStatus: expected %q, got %q", expected.CarrierStatus, actual.CarrierStatus),
			})
		}

		type neighborKey struct{ system, port string }
		actualNeighbors := make(map[neighborKey]struct{}, len(actual.Neighbors))
		for _, n := range actual.Neighbors {
			actualNeighbors[neighborKey{n.SystemName, n.PortID}] = struct{}{}
		}
		for _, expectedNeighbor := range expected.Neighbors {
			key := neighborKey{expectedNeighbor.SystemName, expectedNeighbor.PortID}
			if _, ok := actualNeighbors[key]; !ok {
				ready = false
				mismatches = append(mismatches, readinessv1alpha1.InterfaceMismatch{
					MACAddress: expected.MACAddress,
					Reason:     reasonNeighborMismatch,
					Message:    fmt.Sprintf("LLDP neighbor not found: systemName=%q portID=%q", expectedNeighbor.SystemName, expectedNeighbor.PortID),
				})
			}
		}
	}

	return mismatches, ready
}

func (r *NetworkWiringCheckReconciler) setNetworkReadyCondition(ctx context.Context, server *metalv1alpha1.Server, ready, hasSpec bool, mismatches []readinessv1alpha1.InterfaceMismatch) error {
	serverBase := server.DeepCopy()

	condition := metav1.Condition{
		Type:               networkReadyConditionType,
		ObservedGeneration: server.Generation,
	}
	switch {
	case !hasSpec:
		condition.Status = metav1.ConditionTrue
		condition.Reason = reasonNoExpectedSpec
		condition.Message = "No expected network interfaces configured"
	case ready:
		condition.Status = metav1.ConditionTrue
		condition.Reason = reasonMatch
		condition.Message = "All expected network interfaces and neighbors are present"
	default:
		msgs := make([]string, 0, len(mismatches))
		for _, m := range mismatches {
			msgs = append(msgs, fmt.Sprintf("[%s] %s", m.MACAddress, m.Message))
		}
		condition.Status = metav1.ConditionFalse
		condition.Reason = dominantReason(mismatches)
		condition.Message = strings.Join(msgs, "; ")
	}

	apimeta.SetStatusCondition(&server.Status.Conditions, condition)

	if err := r.Status().Patch(ctx, server, client.MergeFrom(serverBase)); err != nil {
		return fmt.Errorf("patching server %s status: %w", server.Name, err)
	}
	return nil
}

func (r *NetworkWiringCheckReconciler) updateStatus(ctx context.Context, check *readinessv1alpha1.NetworkWiringCheck, ready bool, mismatches []readinessv1alpha1.InterfaceMismatch) error {
	checkBase := check.DeepCopy()
	check.Status.Ready = ready
	check.Status.Mismatches = mismatches
	if err := r.Status().Patch(ctx, check, client.MergeFrom(checkBase)); err != nil {
		return fmt.Errorf("patching NetworkWiringCheck status: %w", err)
	}
	return nil
}

func (r *NetworkWiringCheckReconciler) enqueueFromServer(ctx context.Context, obj client.Object) []ctrl.Request {
	checkList := &readinessv1alpha1.NetworkWiringCheckList{}
	if err := r.List(ctx, checkList,
		client.MatchingFields{serverRefNameField: obj.GetName()},
	); err != nil {
		return nil
	}
	requests := make([]ctrl.Request, 0, len(checkList.Items))
	for _, check := range checkList.Items {
		requests = append(requests, ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name:      check.Name,
				Namespace: check.Namespace,
			},
		})
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *NetworkWiringCheckReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&readinessv1alpha1.NetworkWiringCheck{},
		serverRefNameField,
		func(obj client.Object) []string {
			check := obj.(*readinessv1alpha1.NetworkWiringCheck)
			if check.Spec.ServerRef.Name == "" {
				return nil
			}
			return []string{check.Spec.ServerRef.Name}
		},
	); err != nil {
		return fmt.Errorf("indexing %s: %w", serverRefNameField, err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&readinessv1alpha1.NetworkWiringCheck{}).
		Watches(&metalv1alpha1.Server{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueFromServer)).
		Named("networkwiringcheck").
		Complete(r)
}

// networkWiringCheckRuleName returns the cluster-scoped ServerReadinessRule name for this check.
func networkWiringCheckRuleName(check *readinessv1alpha1.NetworkWiringCheck) string {
	return fmt.Sprintf("mmo-%s-%s", check.Namespace, check.Name)
}

// dominantReason returns the highest-priority reason across a set of mismatches.
// Priority: InterfaceMissing > CarrierDown > NeighborMismatch.
func dominantReason(mismatches []readinessv1alpha1.InterfaceMismatch) string {
	reason := reasonNeighborMismatch
	for _, m := range mismatches {
		switch m.Reason {
		case reasonInterfaceMissing:
			return reasonInterfaceMissing
		case reasonCarrierDown:
			reason = reasonCarrierDown
		}
	}
	return reason
}
