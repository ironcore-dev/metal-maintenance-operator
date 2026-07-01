// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
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
	baselineNetworkFinalizer  = "readiness.metal.ironcore.dev/baselinenetwork"
	networkReadyConditionType = "NetworkReady"

	reasonMatch            = "Match"
	reasonNoExpectedSpec   = "NoExpectedSpec"
	reasonInterfaceMissing = "InterfaceMissing"
	reasonCarrierDown      = "CarrierDown"
	reasonNeighborMismatch = "NeighborMismatch"

	// serverRefNameField is the field index path used to map Server names back to BaselineNetworks.
	serverRefNameField = ".spec.serverRef.name"
)

// BaselineNetworkReconciler reconciles a BaselineNetwork object.
type BaselineNetworkReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=readiness.metal.ironcore.dev,resources=baselinenetworks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=readiness.metal.ironcore.dev,resources=baselinenetworks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=readiness.metal.ironcore.dev,resources=baselinenetworks/finalizers,verbs=update
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers/status,verbs=get;update;patch

func (r *BaselineNetworkReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling BaselineNetwork", "name", req.NamespacedName)

	baseline := &readinessv1alpha1.BaselineNetwork{}
	if err := r.Get(ctx, req.NamespacedName, baseline); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return r.reconcileExists(ctx, baseline)
}

func (r *BaselineNetworkReconciler) reconcileDelete(ctx context.Context, baseline *readinessv1alpha1.BaselineNetwork) (ctrl.Result, error) {
	server := &metalv1alpha1.Server{}
	if err := r.Get(ctx, client.ObjectKey{Name: baseline.Spec.ServerRef.Name}, server); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("getting server %s: %w", baseline.Spec.ServerRef.Name, err)
		}
	} else {
		serverBase := server.DeepCopy()
		apimeta.RemoveStatusCondition(&server.Status.Conditions, networkReadyConditionType)
		if err := r.Status().Patch(ctx, server, client.MergeFrom(serverBase)); err != nil {
			return ctrl.Result{}, fmt.Errorf("clearing NetworkReady condition on server %s: %w", server.Name, err)
		}
	}

	if err := clientutils.PatchRemoveFinalizer(ctx, r.Client, baseline, baselineNetworkFinalizer); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *BaselineNetworkReconciler) reconcileExists(ctx context.Context, baseline *readinessv1alpha1.BaselineNetwork) (ctrl.Result, error) {
	if _, err := clientutils.PatchEnsureFinalizer(ctx, r.Client, baseline, baselineNetworkFinalizer); err != nil {
		return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
	}

	if baseline.GetDeletionTimestamp() != nil {
		return r.reconcileDelete(ctx, baseline)
	}

	server := &metalv1alpha1.Server{}
	if err := r.Get(ctx, client.ObjectKey{Name: baseline.Spec.ServerRef.Name}, server); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("getting server %s: %w", baseline.Spec.ServerRef.Name, err)
	}

	hasSpec := len(baseline.Spec.Network.Interfaces) > 0
	mismatches, ready := r.validateServer(server, baseline)

	if err := r.setNetworkReadyCondition(ctx, server, ready, hasSpec, mismatches); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, r.updateStatus(ctx, baseline, ready, mismatches)
}

func (r *BaselineNetworkReconciler) validateServer(server *metalv1alpha1.Server, baseline *readinessv1alpha1.BaselineNetwork) (mismatches []readinessv1alpha1.InterfaceMismatch, ready bool) {
	ready = true

	if len(baseline.Spec.Network.Interfaces) == 0 {
		return nil, true
	}

	actualByMAC := make(map[string]metalv1alpha1.NetworkInterface, len(server.Status.NetworkInterfaces))
	for _, nic := range server.Status.NetworkInterfaces {
		actualByMAC[strings.ToLower(nic.MACAddress)] = nic
	}

	for _, expected := range baseline.Spec.Network.Interfaces {
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

func (r *BaselineNetworkReconciler) setNetworkReadyCondition(ctx context.Context, server *metalv1alpha1.Server, ready, hasSpec bool, mismatches []readinessv1alpha1.InterfaceMismatch) error {
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

func (r *BaselineNetworkReconciler) updateStatus(ctx context.Context, baseline *readinessv1alpha1.BaselineNetwork, ready bool, mismatches []readinessv1alpha1.InterfaceMismatch) error {
	baselineBase := baseline.DeepCopy()
	baseline.Status.Ready = ready
	baseline.Status.Mismatches = mismatches
	if err := r.Status().Patch(ctx, baseline, client.MergeFrom(baselineBase)); err != nil {
		return fmt.Errorf("patching BaselineNetwork status: %w", err)
	}
	return nil
}

func (r *BaselineNetworkReconciler) enqueueFromServer(ctx context.Context, obj client.Object) []ctrl.Request {
	baselineList := &readinessv1alpha1.BaselineNetworkList{}
	if err := r.List(ctx, baselineList,
		client.MatchingFields{serverRefNameField: obj.GetName()},
	); err != nil {
		return nil
	}
	requests := make([]ctrl.Request, 0, len(baselineList.Items))
	for _, baseline := range baselineList.Items {
		requests = append(requests, ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name:      baseline.Name,
				Namespace: baseline.Namespace,
			},
		})
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *BaselineNetworkReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&readinessv1alpha1.BaselineNetwork{},
		serverRefNameField,
		func(obj client.Object) []string {
			baseline := obj.(*readinessv1alpha1.BaselineNetwork)
			if baseline.Spec.ServerRef.Name == "" {
				return nil
			}
			return []string{baseline.Spec.ServerRef.Name}
		},
	); err != nil {
		return fmt.Errorf("indexing %s: %w", serverRefNameField, err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&readinessv1alpha1.BaselineNetwork{}).
		Watches(&metalv1alpha1.Server{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueFromServer)).
		Named("baselinenetwork").
		Complete(r)
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
