// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	clientutils "github.com/ironcore-dev/controller-utils/clientutils"
	maintenancealpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	serverReadinessCheckFinalizer = "maintenance.metal.ironcore.dev/serverreadinesscheck"
	networkReadyConditionType     = "NetworkReady"
	networkNotReadyTaintKey       = "metal.ironcore.dev/network-not-ready"
)

// ServerReadinessCheckReconciler reconciles a ServerReadinessCheck object.
type ServerReadinessCheckReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=serverreadinesschecks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=serverreadinesschecks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=serverreadinesschecks/finalizers,verbs=update
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=serverreadinessrules,verbs=get;list;watch;create;update;patch;delete

func (r *ServerReadinessCheckReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling ServerReadinessCheck", "name", req.NamespacedName)

	check := &maintenancealpha1.ServerReadinessCheck{}
	if err := r.Get(ctx, req.NamespacedName, check); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if check.GetDeletionTimestamp() != nil {
		return r.reconcileDelete(ctx, check)
	}
	return r.reconcileExists(ctx, check)
}

func (r *ServerReadinessCheckReconciler) reconcileDelete(ctx context.Context, check *maintenancealpha1.ServerReadinessCheck) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	ruleName := readinessRuleName(check)
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

	if err := clientutils.PatchRemoveFinalizer(ctx, r.Client, check, serverReadinessCheckFinalizer); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *ServerReadinessCheckReconciler) reconcileExists(ctx context.Context, check *maintenancealpha1.ServerReadinessCheck) (ctrl.Result, error) {
	if _, err := clientutils.PatchEnsureFinalizer(ctx, r.Client, check, serverReadinessCheckFinalizer); err != nil {
		return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
	}

	if err := r.ensureReadinessRule(ctx, check); err != nil {
		return ctrl.Result{}, err
	}

	servers, err := r.listMatchingServers(ctx, check)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Patching each server's NetworkReady condition may trigger re-enqueue via the Server watch.
	// This is harmless: the condition patch is idempotent and the reconcile will produce the same result.
	var serverStatuses []maintenancealpha1.ServerReadinessStatus
	for i := range servers {
		status := r.validateServer(&servers[i], check)
		serverStatuses = append(serverStatuses, status)
		if err := r.setNetworkReadyCondition(ctx, &servers[i], status); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, r.updateStatus(ctx, check, serverStatuses)
}

func (r *ServerReadinessCheckReconciler) ensureReadinessRule(ctx context.Context, check *maintenancealpha1.ServerReadinessCheck) error {
	logger := log.FromContext(ctx)
	ruleName := readinessRuleName(check)

	existing := &metalv1alpha1.ServerReadinessRule{}
	err := r.Get(ctx, client.ObjectKey{Name: ruleName}, existing)
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("getting ServerReadinessRule %s: %w", ruleName, err)
	}

	desired := buildReadinessRule(ruleName, check)

	if apierrors.IsNotFound(err) {
		logger.Info("Creating ServerReadinessRule", "name", ruleName)
		return r.Create(ctx, desired)
	}

	if !reflect.DeepEqual(existing.Spec.ServerSelector, normalizeSelector(check.Spec.ServerSelector)) {
		logger.Info("ServerReadinessRule selector diverged, recreating", "name", ruleName)
		if err := r.Delete(ctx, existing); err != nil {
			return fmt.Errorf("deleting diverged ServerReadinessRule %s: %w", ruleName, err)
		}
		return r.Create(ctx, desired)
	}

	return nil
}

func buildReadinessRule(name string, check *maintenancealpha1.ServerReadinessCheck) *metalv1alpha1.ServerReadinessRule {
	return &metalv1alpha1.ServerReadinessRule{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"maintenance.metal.ironcore.dev/owner-namespace": check.Namespace,
				"maintenance.metal.ironcore.dev/owner-name":      check.Name,
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
			ServerSelector: normalizeSelector(check.Spec.ServerSelector),
		},
	}
}

func (r *ServerReadinessCheckReconciler) listMatchingServers(ctx context.Context, check *maintenancealpha1.ServerReadinessCheck) ([]metalv1alpha1.Server, error) {
	selector, err := metav1.LabelSelectorAsSelector(&check.Spec.ServerSelector)
	if err != nil {
		return nil, fmt.Errorf("parsing serverSelector: %w", err)
	}
	serverList := &metalv1alpha1.ServerList{}
	if err := r.List(ctx, serverList, &client.ListOptions{LabelSelector: selector}); err != nil {
		return nil, fmt.Errorf("listing servers: %w", err)
	}
	return serverList.Items, nil
}

func (r *ServerReadinessCheckReconciler) validateServer(server *metalv1alpha1.Server, check *maintenancealpha1.ServerReadinessCheck) maintenancealpha1.ServerReadinessStatus {
	status := maintenancealpha1.ServerReadinessStatus{Name: server.Name, Ready: true}

	// Index actual NICs by MAC for O(1) lookup
	actualByMAC := make(map[string]metalv1alpha1.NetworkInterface, len(server.Status.NetworkInterfaces))
	for _, nic := range server.Status.NetworkInterfaces {
		actualByMAC[strings.ToLower(nic.MACAddress)] = nic
	}

	for _, expected := range check.Spec.Network.Interfaces {
		mac := strings.ToLower(expected.MACAddress)
		actual, found := actualByMAC[mac]
		if !found {
			status.Ready = false
			status.Mismatches = append(status.Mismatches, maintenancealpha1.InterfaceMismatch{
				MACAddress: expected.MACAddress,
				Message:    "interface not found",
			})
			continue
		}

		if expected.CarrierStatus != "" && actual.CarrierStatus != expected.CarrierStatus {
			status.Ready = false
			status.Mismatches = append(status.Mismatches, maintenancealpha1.InterfaceMismatch{
				MACAddress: expected.MACAddress,
				Message:    fmt.Sprintf("carrierStatus: expected %q, got %q", expected.CarrierStatus, actual.CarrierStatus),
			})
		}

		// Index actual neighbors by systemName+portID
		type neighborKey struct{ system, port string }
		actualNeighbors := make(map[neighborKey]struct{}, len(actual.Neighbors))
		for _, n := range actual.Neighbors {
			actualNeighbors[neighborKey{n.SystemName, n.PortID}] = struct{}{}
		}
		for _, expectedNeighbor := range expected.Neighbors {
			key := neighborKey{expectedNeighbor.SystemName, expectedNeighbor.PortID}
			if _, ok := actualNeighbors[key]; !ok {
				status.Ready = false
				status.Mismatches = append(status.Mismatches, maintenancealpha1.InterfaceMismatch{
					MACAddress: expected.MACAddress,
					Message:    fmt.Sprintf("LLDP neighbor not found: systemName=%q portID=%q", expectedNeighbor.SystemName, expectedNeighbor.PortID),
				})
			}
		}
	}

	return status
}

func (r *ServerReadinessCheckReconciler) setNetworkReadyCondition(ctx context.Context, server *metalv1alpha1.Server, status maintenancealpha1.ServerReadinessStatus) error {
	serverBase := server.DeepCopy()

	condition := metav1.Condition{
		Type:               networkReadyConditionType,
		ObservedGeneration: server.Generation,
	}
	if status.Ready {
		condition.Status = metav1.ConditionTrue
		condition.Reason = "NetworkReady"
		condition.Message = "All expected network interfaces and neighbors are present"
	} else {
		msgs := make([]string, 0, len(status.Mismatches))
		for _, m := range status.Mismatches {
			msgs = append(msgs, fmt.Sprintf("[%s] %s", m.MACAddress, m.Message))
		}
		condition.Status = metav1.ConditionFalse
		condition.Reason = "NetworkMismatch"
		condition.Message = strings.Join(msgs, "; ")
	}

	apimeta.SetStatusCondition(&server.Status.Conditions, condition)

	if err := r.Status().Patch(ctx, server, client.MergeFrom(serverBase)); err != nil {
		return fmt.Errorf("patching server %s status: %w", server.Name, err)
	}
	return nil
}

func (r *ServerReadinessCheckReconciler) updateStatus(ctx context.Context, check *maintenancealpha1.ServerReadinessCheck, servers []maintenancealpha1.ServerReadinessStatus) error {
	checkBase := check.DeepCopy()
	check.Status.Servers = servers
	if err := r.Status().Patch(ctx, check, client.MergeFrom(checkBase)); err != nil {
		return fmt.Errorf("patching ServerReadinessCheck status: %w", err)
	}
	return nil
}

func (r *ServerReadinessCheckReconciler) enqueueFromServer(ctx context.Context, obj client.Object) []ctrl.Request {
	checkList := &maintenancealpha1.ServerReadinessCheckList{}
	if err := r.List(ctx, checkList); err != nil {
		return nil
	}
	var requests []ctrl.Request
	for _, check := range checkList.Items {
		selector, err := metav1.LabelSelectorAsSelector(&check.Spec.ServerSelector)
		if err != nil {
			continue
		}
		if selector.Matches(labels.Set(obj.GetLabels())) {
			requests = append(requests, ctrl.Request{
				NamespacedName: client.ObjectKey{
					Name:      check.Name,
					Namespace: check.Namespace,
				},
			})
		}
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *ServerReadinessCheckReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&maintenancealpha1.ServerReadinessCheck{}).
		Watches(&metalv1alpha1.Server{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueFromServer)).
		Named("serverreadinesscheck").
		Complete(r)
}

// readinessRuleName returns the cluster-scoped ServerReadinessRule name owned by this check.
func readinessRuleName(check *maintenancealpha1.ServerReadinessCheck) string {
	return fmt.Sprintf("mmo-%s-%s", check.Namespace, check.Name)
}

// normalizeSelector ensures MatchLabels is never nil, which is required by the
// ServerReadinessRule CRD validation even for a select-all selector.
func normalizeSelector(s metav1.LabelSelector) metav1.LabelSelector {
	if s.MatchLabels == nil {
		s.MatchLabels = map[string]string{}
	}
	return s
}
