// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package readiness

import (
	"context"
	"fmt"
	"strings"

	clientutils "github.com/ironcore-dev/controller-utils/clientutils"
	discoveryv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/discovery/v1alpha1"
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
	serverWiringFinalizer     = "readiness.metal.ironcore.dev/serverwiring"
	networkReadyConditionType = "NetworkReady"

	reasonMatch            = "Match"
	reasonNoExpectedSpec   = "NoExpectedSpec"
	reasonInterfaceMissing = "InterfaceMissing"
	reasonCarrierDown      = "CarrierDown"
	reasonNeighborMismatch = "NeighborMismatch"

	// serverRefNameField is the field index path used to map Server names back to ServerWirings.
	serverRefNameField = ".spec.serverRef.name"
)

// ServerWiringReconciler reconciles a ServerWiring object.
type ServerWiringReconciler struct {
	client.Client
	Scheme             *runtime.Scheme
	DiscoveryNamespace string
}

// +kubebuilder:rbac:groups=readiness.metal.ironcore.dev,resources=serverwirings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=readiness.metal.ironcore.dev,resources=serverwirings/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=readiness.metal.ironcore.dev,resources=serverwirings/finalizers,verbs=update
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=discovery.metal.ironcore.dev,resources=metadata,verbs=get;list;watch

func (r *ServerWiringReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling ServerWiring", "name", req.NamespacedName)

	wiring := &readinessv1alpha1.ServerWiring{}
	if err := r.Get(ctx, req.NamespacedName, wiring); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return r.reconcileExists(ctx, wiring)
}

func (r *ServerWiringReconciler) reconcileDelete(ctx context.Context, wiring *readinessv1alpha1.ServerWiring) (ctrl.Result, error) {
	server := &metalv1alpha1.Server{}
	if err := r.Get(ctx, client.ObjectKey{Name: wiring.Spec.ServerRef.Name}, server); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("getting server %s: %w", wiring.Spec.ServerRef.Name, err)
		}
	} else {
		serverBase := server.DeepCopy()
		apimeta.RemoveStatusCondition(&server.Status.Conditions, networkReadyConditionType)
		if err := r.Status().Patch(ctx, server, client.MergeFrom(serverBase)); err != nil {
			return ctrl.Result{}, fmt.Errorf("clearing NetworkReady condition on server %s: %w", server.Name, err)
		}
	}

	if err := clientutils.PatchRemoveFinalizer(ctx, r.Client, wiring, serverWiringFinalizer); err != nil {
		return ctrl.Result{}, fmt.Errorf("removing finalizer: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *ServerWiringReconciler) reconcileExists(ctx context.Context, wiring *readinessv1alpha1.ServerWiring) (ctrl.Result, error) {
	if _, err := clientutils.PatchEnsureFinalizer(ctx, r.Client, wiring, serverWiringFinalizer); err != nil {
		return ctrl.Result{}, fmt.Errorf("adding finalizer: %w", err)
	}

	if wiring.GetDeletionTimestamp() != nil {
		return r.reconcileDelete(ctx, wiring)
	}

	server := &metalv1alpha1.Server{}
	if err := r.Get(ctx, client.ObjectKey{Name: wiring.Spec.ServerRef.Name}, server); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("getting server %s: %w", wiring.Spec.ServerRef.Name, err)
	}

	// Metadata is namespaced and by convention named after the Server,
	// written by the discovery controller into DiscoveryNamespace.
	meta := &discoveryv1alpha1.Metadata{}
	metaFound := true
	if err := r.Get(ctx, client.ObjectKey{Name: wiring.Spec.ServerRef.Name, Namespace: r.DiscoveryNamespace}, meta); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("getting metadata for server %s: %w", wiring.Spec.ServerRef.Name, err)
		}
		metaFound = false
	}

	hasSpec := len(wiring.Spec.Network.Interfaces) > 0
	mismatches, ready := r.validateServer(meta, metaFound, wiring)

	if err := r.setNetworkReadyCondition(ctx, server, ready, hasSpec, mismatches); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, r.updateStatus(ctx, wiring, ready, mismatches)
}

func (r *ServerWiringReconciler) validateServer(meta *discoveryv1alpha1.Metadata, metaFound bool, wiring *readinessv1alpha1.ServerWiring) (mismatches []readinessv1alpha1.InterfaceMismatch, ready bool) {
	if len(wiring.Spec.Network.Interfaces) == 0 {
		return nil, true
	}

	if !metaFound {
		for _, expected := range wiring.Spec.Network.Interfaces {
			mismatches = append(mismatches, readinessv1alpha1.InterfaceMismatch{
				MACAddress: expected.MACAddress,
				Reason:     reasonInterfaceMissing,
				Message:    "no discovery Metadata found for this server",
			})
		}
		return mismatches, false
	}

	// Index observed NICs by MAC address for O(1) lookup.
	observedNICs := make(map[string]discoveryv1alpha1.NetworkInterface, len(meta.NetworkInterfaces))
	for _, nic := range meta.NetworkInterfaces {
		observedNICs[strings.ToLower(nic.MACAddress)] = nic
	}

	// Build MAC→neighbors map: interface name→MAC from observed NICs, then
	// walk LLDP interfaces to map by MAC.
	nameToMAC := make(map[string]string, len(meta.NetworkInterfaces))
	for _, nic := range meta.NetworkInterfaces {
		nameToMAC[nic.Name] = strings.ToLower(nic.MACAddress)
	}
	lldpByMAC := make(map[string][]discoveryv1alpha1.LLDPNeighbor)
	for _, iface := range meta.LLDP {
		if mac, ok := nameToMAC[iface.Name]; ok {
			lldpByMAC[mac] = iface.Neighbors
		}
	}

	for _, expected := range wiring.Spec.Network.Interfaces {
		mac := strings.ToLower(expected.MACAddress)
		observed, found := observedNICs[mac]
		if !found {
			mismatches = append(mismatches, readinessv1alpha1.InterfaceMismatch{
				MACAddress: expected.MACAddress,
				Reason:     reasonInterfaceMissing,
				Message:    fmt.Sprintf("interface not found in discovery metadata (mac %s)", expected.MACAddress),
			})
			continue
		}

		if expected.CarrierStatus != "" && observed.CarrierStatus != expected.CarrierStatus {
			mismatches = append(mismatches, readinessv1alpha1.InterfaceMismatch{
				MACAddress: expected.MACAddress,
				Reason:     reasonCarrierDown,
				Message:    fmt.Sprintf("carrierStatus: expected %q, got %q", expected.CarrierStatus, observed.CarrierStatus),
			})
		}

		type neighborKey struct{ system, port string }
		observedNeighbors := make(map[neighborKey]struct{}, len(lldpByMAC[mac]))
		for _, n := range lldpByMAC[mac] {
			observedNeighbors[neighborKey{n.SystemName, n.PortID}] = struct{}{}
		}
		for _, wantNeighbor := range expected.Neighbors {
			key := neighborKey{wantNeighbor.SystemName, wantNeighbor.PortID}
			if _, ok := observedNeighbors[key]; !ok {
				mismatches = append(mismatches, readinessv1alpha1.InterfaceMismatch{
					MACAddress: expected.MACAddress,
					Reason:     reasonNeighborMismatch,
					Message:    fmt.Sprintf("LLDP neighbor not found: systemName=%q portID=%q", wantNeighbor.SystemName, wantNeighbor.PortID),
				})
			}
		}
	}

	return mismatches, len(mismatches) == 0
}

func (r *ServerWiringReconciler) setNetworkReadyCondition(ctx context.Context, server *metalv1alpha1.Server, ready, hasSpec bool, mismatches []readinessv1alpha1.InterfaceMismatch) error {
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

func (r *ServerWiringReconciler) updateStatus(ctx context.Context, wiring *readinessv1alpha1.ServerWiring, ready bool, mismatches []readinessv1alpha1.InterfaceMismatch) error {
	wiringBase := wiring.DeepCopy()
	wiring.Status.Ready = ready
	wiring.Status.Mismatches = mismatches
	if err := r.Status().Patch(ctx, wiring, client.MergeFrom(wiringBase)); err != nil {
		return fmt.Errorf("patching ServerWiring status: %w", err)
	}
	return nil
}

func (r *ServerWiringReconciler) enqueueFromServer(ctx context.Context, obj client.Object) []ctrl.Request {
	wiringList := &readinessv1alpha1.ServerWiringList{}
	if err := r.List(ctx, wiringList,
		client.MatchingFields{serverRefNameField: obj.GetName()},
	); err != nil {
		log.FromContext(ctx).Error(err, "Failed to list ServerWirings for Server", "server", obj.GetName())
		return nil
	}
	requests := make([]ctrl.Request, 0, len(wiringList.Items))
	for _, wiring := range wiringList.Items {
		requests = append(requests, ctrl.Request{
			NamespacedName: client.ObjectKey{
				Name:      wiring.Name,
				Namespace: wiring.Namespace,
			},
		})
	}
	return requests
}

// enqueueFromMetadata maps a Metadata event to ServerWirings that reference a
// Server of the same name (Metadata is namespaced and named after the Server).
func (r *ServerWiringReconciler) enqueueFromMetadata(ctx context.Context, obj client.Object) []ctrl.Request {
	return r.enqueueFromServer(ctx, obj)
}

// SetupWithManager sets up the controller with the Manager.
func (r *ServerWiringReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&readinessv1alpha1.ServerWiring{},
		serverRefNameField,
		func(obj client.Object) []string {
			wiring := obj.(*readinessv1alpha1.ServerWiring)
			if wiring.Spec.ServerRef.Name == "" {
				return nil
			}
			return []string{wiring.Spec.ServerRef.Name}
		},
	); err != nil {
		return fmt.Errorf("indexing %s: %w", serverRefNameField, err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&readinessv1alpha1.ServerWiring{}).
		Watches(&metalv1alpha1.Server{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueFromServer)).
		Watches(&discoveryv1alpha1.Metadata{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueFromMetadata)).
		Named("serverwiring").
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
