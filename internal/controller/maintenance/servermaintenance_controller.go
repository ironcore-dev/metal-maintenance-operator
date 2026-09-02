// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package maintenance

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ironcore-dev/controller-utils/clientutils"
	"github.com/ironcore-dev/controller-utils/metautils"
	serverMaintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/maintenance/v1alpha1"
	controllerutils "github.com/ironcore-dev/metal-maintenance-operator/internal/utils"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	serverMaintenanceFinalizer = "maintenance.metal.ironcore.dev/servermaintenance"
	serverRefField             = "spec.serverRef.name"
	trueValue                  = "true"
)

// ServerMaintenanceReconciler reconciles a ServerMaintenance object.
type ServerMaintenanceReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	ResyncInterval time.Duration
}

// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=servermaintenances,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=servermaintenances/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=servermaintenances/finalizers,verbs=update
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=serverclaims,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=serverbootconfigurations,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *ServerMaintenanceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	maintenance := &serverMaintenancev1alpha1.ServerMaintenance{}
	if err := r.Get(ctx, req.NamespacedName, maintenance); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return r.reconcileExists(ctx, maintenance)
}

func (r *ServerMaintenanceReconciler) reconcileExists(ctx context.Context, maintenance *serverMaintenancev1alpha1.ServerMaintenance) (ctrl.Result, error) {
	if !maintenance.DeletionTimestamp.IsZero() {
		return r.delete(ctx, maintenance)
	}
	return r.reconcile(ctx, maintenance)
}

func (r *ServerMaintenanceReconciler) reconcile(ctx context.Context, maintenance *serverMaintenancev1alpha1.ServerMaintenance) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.V(1).Info("Reconciling ServerMaintenance")

	if maintenance.Spec.ServerRef == nil {
		log.V(1).Info("ServerRef is nil, skipping reconciliation")
		return ctrl.Result{}, nil
	}

	if controllerutils.ShouldIgnoreReconciliation(maintenance) {
		log.V(1).Info("Skipped ServerMaintenance reconciliation")
		return ctrl.Result{}, nil
	}

	server := &metalv1alpha1.Server{}
	if err := r.Get(ctx, client.ObjectKey{Name: maintenance.Spec.ServerRef.Name}, server); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to get Server: %w", err)
	}
	// This needs to be checked because "Enforced" Maintenance Policy evicts the server from the current maintenance
	// and assigns it to itself, causing some other maintenance to be InMaintenance.
	// In this case, we should not reconcile the maintenance because it is not the one holding the maintenance on server.
	if owner, ok := server.GetAnnotations()[controllerutils.ServerMaintenanceOwnerAnnotation]; ok && owner != serverMaintenanceOwnerKey(maintenance) {
		log.V(1).Info("Server owned by another ServerMaintenance, skipping", "Server", server.Name)
		if maintenance.Status.State != serverMaintenancev1alpha1.ServerMaintenanceStatePending {
			if modified, err := r.patchMaintenanceState(ctx, maintenance, serverMaintenancev1alpha1.ServerMaintenanceStatePending); err != nil || modified {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	if modified, err := clientutils.PatchEnsureFinalizer(ctx, r.Client, maintenance, serverMaintenanceFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}

	if maintenance.Status.State == "" {
		if modified, err := r.patchMaintenanceState(ctx, maintenance, serverMaintenancev1alpha1.ServerMaintenanceStatePending); err != nil || modified {
			return ctrl.Result{}, err
		}
	}
	return r.ensureServerMaintenanceStateTransition(ctx, maintenance)
}

func (r *ServerMaintenanceReconciler) ensureServerMaintenanceStateTransition(ctx context.Context, maintenance *serverMaintenancev1alpha1.ServerMaintenance) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	switch maintenance.Status.State {
	case serverMaintenancev1alpha1.ServerMaintenanceStatePending:
		return r.handlePendingState(ctx, maintenance)
	case serverMaintenancev1alpha1.ServerMaintenanceStateInMaintenance:
		return r.handleInMaintenanceState(ctx, maintenance)
	case serverMaintenancev1alpha1.ServerMaintenanceStateFailed:
		return r.handleFailedState(ctx, maintenance)
	default:
		log.V(1).Info("Unknown ServerMaintenance state, skipping reconciliation", "State", maintenance.Status.State)
		return ctrl.Result{}, nil
	}
}

func (r *ServerMaintenanceReconciler) handlePendingState(ctx context.Context, maintenance *serverMaintenancev1alpha1.ServerMaintenance) (result ctrl.Result, err error) {
	log := logf.FromContext(ctx)
	server, err := controllerutils.GetServerByName(ctx, r.Client, maintenance.Spec.ServerRef.Name)
	if err != nil {
		return ctrl.Result{}, err
	}

	deferMaintenance, err := r.shouldDeferToHigherPriorityMaintenance(ctx, maintenance)
	if err != nil {
		return ctrl.Result{}, err
	}
	if deferMaintenance {
		log.V(1).Info("Deferring maintenance because higher-priority maintenance is pending", "Server", server.Name, "Priority", maintenance.Spec.Priority)
		return ctrl.Result{}, nil
	}

	if server.Spec.ServerClaimRef == nil {
		log.V(1).Info("Server has no ServerClaim, move to maintenance state right away", "Server", server.Name)
		if owned, err := r.requestServerPark(ctx, maintenance, server); err != nil || !owned {
			return ctrl.Result{}, err
		}
		if !controllerutils.IsServerParkedForOwner(server, serverMaintenanceOwnerKey(maintenance)) {
			log.V(1).Info("Waiting for Server to reach Parked state", "Server", server.Name, "State", server.Status.State)
			return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
		}
		_, err = r.patchMaintenanceState(ctx, maintenance, serverMaintenancev1alpha1.ServerMaintenanceStateInMaintenance)
		return ctrl.Result{}, err
	}

	serverClaim := &metalv1alpha1.ServerClaim{}
	if err := r.Get(ctx, client.ObjectKey{Name: server.Spec.ServerClaimRef.Name, Namespace: server.Spec.ServerClaimRef.Namespace}, serverClaim); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("failed to get ServerClaim: %w", err)
		}
		log.V(1).Info("ServerClaim gone")
		return ctrl.Result{}, nil
	}
	patch := client.MergeFrom(serverClaim.DeepCopy())
	if serverClaim.Labels == nil {
		serverClaim.Labels = make(map[string]string)
	}
	serverClaim.Labels[serverMaintenancev1alpha1.ServerMaintenanceNeededLabelKey] = trueValue
	if serverClaim.Annotations == nil {
		serverClaim.Annotations = make(map[string]string)
	}

	if err := r.Patch(ctx, serverClaim, patch); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to patch ServerClaim: %w", err)
	}
	log.V(1).Info("Patched ServerClaim labels and annotations", "ServerClaim", client.ObjectKeyFromObject(serverClaim))
	if maintenance.Spec.Policy == serverMaintenancev1alpha1.ServerMaintenancePolicyOwnerApproval {
		labels := serverClaim.GetLabels()
		_, hasLabel := labels[serverMaintenancev1alpha1.ServerMaintenanceApprovedLabelKey]

		if hasLabel {
			log.V(1).Info("Server approved for maintenance", "Server", server.Name)
			if owned, err := r.requestServerPark(ctx, maintenance, server); err != nil || !owned {
				return ctrl.Result{}, err
			}
			if !controllerutils.IsServerParkedForOwner(server, serverMaintenanceOwnerKey(maintenance)) {
				log.V(1).Info("Waiting for Server to reach Parked state", "Server", server.Name, "State", server.Status.State)
				return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
			}
			_, err = r.patchMaintenanceState(ctx, maintenance, serverMaintenancev1alpha1.ServerMaintenanceStateInMaintenance)
			return ctrl.Result{}, err
		}
		log.V(1).Info("Server not approved for maintenance, waiting for approval", "Server", server.Name)
		return ctrl.Result{}, nil
	}

	if maintenance.Spec.Policy == serverMaintenancev1alpha1.ServerMaintenancePolicyEnforced {
		log.V(1).Info("Enforcing maintenance", "Server", server.Name)
		if owned, err := r.requestServerPark(ctx, maintenance, server); err != nil || !owned {
			return ctrl.Result{}, err
		}
		if !controllerutils.IsServerParkedForOwner(server, serverMaintenanceOwnerKey(maintenance)) {
			log.V(1).Info("Waiting for Server to reach Parked state", "Server", server.Name, "State", server.Status.State)
			return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
		}
		if modified, err := r.patchMaintenanceState(ctx, maintenance, serverMaintenancev1alpha1.ServerMaintenanceStateInMaintenance); err != nil || modified {
			return ctrl.Result{}, err
		}
	}

	log.V(1).Info("Reconciled ServerMaintenance in Pending state")
	return ctrl.Result{}, nil
}

func (r *ServerMaintenanceReconciler) shouldDeferToHigherPriorityMaintenance(ctx context.Context, maintenance *serverMaintenancev1alpha1.ServerMaintenance) (bool, error) {
	if maintenance.Spec.ServerRef == nil {
		return false, nil
	}

	maintenanceList := &serverMaintenancev1alpha1.ServerMaintenanceList{}
	if err := r.List(ctx, maintenanceList, client.MatchingFields{serverRefField: maintenance.Spec.ServerRef.Name}); err != nil {
		return false, fmt.Errorf("failed to list ServerMaintenances: %w", err)
	}

	for i := range maintenanceList.Items {
		other := &maintenanceList.Items[i]
		if other.Name == maintenance.Name && other.Namespace == maintenance.Namespace {
			continue
		}
		if !other.DeletionTimestamp.IsZero() {
			continue
		}
		if other.Status.State != "" && other.Status.State != serverMaintenancev1alpha1.ServerMaintenanceStatePending {
			continue
		}
		if shouldRunBefore(other, maintenance) {
			return true, nil
		}
	}

	return false, nil
}

func shouldRunBefore(a, b *serverMaintenancev1alpha1.ServerMaintenance) bool {
	if a.Spec.Priority != b.Spec.Priority {
		return a.Spec.Priority > b.Spec.Priority
	}
	if a.Spec.Policy != b.Spec.Policy {
		return a.Spec.Policy == serverMaintenancev1alpha1.ServerMaintenancePolicyEnforced
	}
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.Before(&b.CreationTimestamp)
	}
	if a.Namespace != b.Namespace {
		return a.Namespace < b.Namespace
	}
	return a.Name < b.Name
}

func (r *ServerMaintenanceReconciler) handleInMaintenanceState(ctx context.Context, maintenance *serverMaintenancev1alpha1.ServerMaintenance) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	server, err := controllerutils.GetServerByName(ctx, r.Client, maintenance.Spec.ServerRef.Name)
	if err != nil {
		return ctrl.Result{}, err
	}

	if controllerutils.IsServerParkedForOwner(server, serverMaintenanceOwnerKey(maintenance)) {
		log.V(1).Info("Reconciled ServerMaintenance in InMaintenance state")
		return ctrl.Result{}, nil
	}

	// The Server drifted out of Parked state or lost its ownership annotation out of
	// band; re-assert the park claim so InMaintenance and the Server's actual state
	// don't stay inconsistent.
	owned, err := r.requestServerPark(ctx, maintenance, server)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !owned {
		log.V(1).Info("Server is no longer owned by this maintenance, returning to Pending", "Server", server.Name)
		_, err := r.patchMaintenanceState(ctx, maintenance, serverMaintenancev1alpha1.ServerMaintenanceStatePending)
		return ctrl.Result{}, err
	}

	log.V(1).Info("Server not yet Parked while InMaintenance, waiting", "Server", server.Name, "State", server.Status.State)
	return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
}

// serverMaintenanceOwnerKey returns the "namespace/name" key used to record which
// ServerMaintenance owns a Server's Parked state.
func serverMaintenanceOwnerKey(maintenance *serverMaintenancev1alpha1.ServerMaintenance) string {
	return controllerutils.ServerMaintenanceOwnerKey(maintenance.Namespace, maintenance.Name)
}

// requestServerPark requests that the given Server be parked for this maintenance.
// It re-fetches the latest Server state to avoid acting on a stale copy, and returns whether
// this ServerMaintenance owns (or successfully acquired) the park request. Callers must only
// transition to InMaintenance when the returned bool is true.
//
// metal-operator's Parked state has no concept of an owning object (it's a plain
// annotation), so ownership is tracked here via controllerutils.ServerMaintenanceOwnerAnnotation
// to preserve the same "single active claimant" semantics ServerMaintenanceRef used to give us.
func (r *ServerMaintenanceReconciler) requestServerPark(ctx context.Context, maintenance *serverMaintenancev1alpha1.ServerMaintenance, server *metalv1alpha1.Server) (bool, error) {
	log := logf.FromContext(ctx)

	latest := &metalv1alpha1.Server{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(server), latest); err != nil {
		return false, fmt.Errorf("failed to get latest Server: %w", err)
	}

	key := serverMaintenanceOwnerKey(maintenance)
	if owner, ok := latest.GetAnnotations()[controllerutils.ServerMaintenanceOwnerAnnotation]; ok {
		*server = *latest
		if owner == key {
			if latest.Status.State == metalv1alpha1.ServerStateParked ||
				latest.GetAnnotations()[metalv1alpha1.OperationAnnotation] == metalv1alpha1.OperationAnnotationPark {
				log.V(1).Info("Server is already owned by this maintenance", "Server", latest.Name)
				return true, nil
			}
			metav1.SetMetaDataAnnotation(&latest.ObjectMeta, metalv1alpha1.OperationAnnotation, metalv1alpha1.OperationAnnotationPark)
			if err := r.Update(ctx, latest); err != nil {
				if apierrors.IsConflict(err) {
					log.V(1).Info("Conflict while re-requesting park for owned Server, will retry", "Server", latest.Name)
					return false, nil
				}
				return false, fmt.Errorf("failed to re-request park for server: %w", err)
			}
			*server = *latest
			log.V(1).Info("Re-requested Server park for this maintenance", "Server", latest.Name)
			return true, nil
		}
		log.V(1).Info("Server is already owned by another ServerMaintenance", "Server", latest.Name, "Owner", owner)
		return false, nil
	}

	metav1.SetMetaDataAnnotation(&latest.ObjectMeta, controllerutils.ServerMaintenanceOwnerAnnotation, key)
	metav1.SetMetaDataAnnotation(&latest.ObjectMeta, metalv1alpha1.OperationAnnotation, metalv1alpha1.OperationAnnotationPark)
	// use update to not overwrite the claim if another maintenance was quicker
	if err := r.Update(ctx, latest); err != nil {
		if apierrors.IsConflict(err) {
			log.V(1).Info("Conflict while claiming Server, will retry", "Server", latest.Name)
			return false, nil
		}
		return false, fmt.Errorf("failed to request park for server: %w", err)
	}
	*server = *latest
	log.V(1).Info("Requested Server park for maintenance", "Server", latest.Name)
	return true, nil
}

func (r *ServerMaintenanceReconciler) handleFailedState(ctx context.Context, _ *serverMaintenancev1alpha1.ServerMaintenance) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.V(1).Info("Reconciled ServerMaintenance in Failed state")
	return ctrl.Result{}, nil
}

func (r *ServerMaintenanceReconciler) delete(ctx context.Context, maintenance *serverMaintenancev1alpha1.ServerMaintenance) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.V(1).Info("Deleting ServerMaintenance")
	if !controllerutil.ContainsFinalizer(maintenance, serverMaintenanceFinalizer) {
		return ctrl.Result{}, nil
	}
	if maintenance.Spec.ServerRef == nil {
		return ctrl.Result{}, nil
	}
	server, err := controllerutils.GetServerByName(ctx, r.Client, maintenance.Spec.ServerRef.Name)
	if err == nil {
		if err := r.cleanup(ctx, maintenance, server); err != nil {
			return ctrl.Result{}, err
		}
	} else if apierrors.IsNotFound(err) {
		log.V(1).Info("Server not found, skipping cleanup", "Server", maintenance.Spec.ServerRef.Name)
	} else {
		return ctrl.Result{}, err
	}

	log.V(1).Info("Removed dependencies")
	log.V(1).Info("Ensuring that the finalizer is removed")
	if modified, err := clientutils.PatchEnsureNoFinalizer(ctx, r.Client, maintenance, serverMaintenanceFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}
	log.V(1).Info("Ensured that the finalizer is removed")
	log.V(1).Info("Deleted ServerMaintenance")
	return ctrl.Result{}, nil
}

func (r *ServerMaintenanceReconciler) cleanup(ctx context.Context, maintenance *serverMaintenancev1alpha1.ServerMaintenance, server *metalv1alpha1.Server) error {
	log := logf.FromContext(ctx)
	if server == nil {
		return nil
	}

	if owner := server.GetAnnotations()[controllerutils.ServerMaintenanceOwnerAnnotation]; owner == serverMaintenanceOwnerKey(maintenance) {
		if err := r.unparkServerForMaintenance(ctx, server); err != nil {
			return fmt.Errorf("failed to request unpark for Server: %w", err)
		}
	}

	// Boot config and claim-label cleanup run outside the ownership guard so a retry
	// after unparkServerForMaintenance removes the owner annotation still completes them.
	if server.Spec.BootConfigurationRef != nil {
		config := &metalv1alpha1.ServerBootConfiguration{
			ObjectMeta: metav1.ObjectMeta{
				Name:      server.Spec.BootConfigurationRef.Name,
				Namespace: server.Spec.BootConfigurationRef.Namespace,
			},
		}
		if err := r.Delete(ctx, config); err != nil {
			if !apierrors.IsNotFound(err) {
				return fmt.Errorf("failed to delete ServerBootConfiguration: %w", err)
			}
			log.V(1).Info("ServerBootConfiguration already deleted", "Config", client.ObjectKeyFromObject(config))
		}
		if err := r.removeBootConfigRefFromServer(ctx, config, server); err != nil {
			return fmt.Errorf("failed to remove ServerMaintenance boot config ref from Server: %w", err)
		}
		log.V(1).Info("Removed ServerMaintenance boot configuration ref from Server", "Server", server.Name)
	}

	if server.Spec.ServerClaimRef == nil {
		return nil
	}
	serverMaintenancesList := &serverMaintenancev1alpha1.ServerMaintenanceList{}
	if err := r.List(ctx, serverMaintenancesList, client.MatchingFields{serverRefField: server.Name}); err != nil {
		return fmt.Errorf("failed to list ServerMaintenances for Server %s: %w", server.Name, err)
	}
	activeItems := serverMaintenancesList.Items[:0]
	for i := range serverMaintenancesList.Items {
		m := &serverMaintenancesList.Items[i]
		if m.Name == maintenance.Name && m.Namespace == maintenance.Namespace {
			continue
		}
		if !m.DeletionTimestamp.IsZero() {
			continue
		}
		activeItems = append(activeItems, *m)
	}
	serverMaintenancesList.Items = activeItems
	if len(serverMaintenancesList.Items) == 0 {
		serverClaim := &metalv1alpha1.ServerClaim{}
		if err := r.Get(ctx, client.ObjectKey{Name: server.Spec.ServerClaimRef.Name, Namespace: server.Spec.ServerClaimRef.Namespace}, serverClaim); err != nil {
			if apierrors.IsNotFound(err) {
				return nil
			}
			return fmt.Errorf("failed to get ServerClaim: %w", err)
		}
		serverClaimBase := serverClaim.DeepCopy()
		metautils.DeleteLabels(serverClaim, []string{
			serverMaintenancev1alpha1.ServerMaintenanceApprovedLabelKey,
			serverMaintenancev1alpha1.ServerMaintenanceNeededLabelKey,
		})
		if err := r.Patch(ctx, serverClaim, client.MergeFrom(serverClaimBase)); err != nil {
			return fmt.Errorf("failed to patch ServerClaim labels: %w", err)
		}
	} else {
		log.V(1).Info("Postponing the removal of approval labels as other maintenances are in queue", "Server", server.Name)
	}
	return nil
}

func (r *ServerMaintenanceReconciler) removeBootConfigRefFromServer(ctx context.Context, config *metalv1alpha1.ServerBootConfiguration, server *metalv1alpha1.Server) error {
	if ref := server.Spec.BootConfigurationRef; ref == nil || ref.Name != config.Name || ref.Namespace != config.Namespace {
		return nil
	}
	serverBase := server.DeepCopy()
	server.Spec.BootConfigurationRef = nil
	if err := r.Patch(ctx, server, client.MergeFrom(serverBase)); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func (r *ServerMaintenanceReconciler) unparkServerForMaintenance(ctx context.Context, server *metalv1alpha1.Server) error {
	serverBase := server.DeepCopy()
	metautils.DeleteAnnotation(server, controllerutils.ServerMaintenanceOwnerAnnotation)
	metav1.SetMetaDataAnnotation(&server.ObjectMeta, metalv1alpha1.OperationAnnotation, metalv1alpha1.OperationAnnotationUnpark)
	if err := r.Patch(ctx, server, client.MergeFrom(serverBase)); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to patch unpark request for server: %w", err)
	}
	return nil
}

func (r *ServerMaintenanceReconciler) patchMaintenanceState(ctx context.Context, maintenance *serverMaintenancev1alpha1.ServerMaintenance, state serverMaintenancev1alpha1.ServerMaintenanceState) (bool, error) {
	if maintenance == nil {
		return false, fmt.Errorf("ServerMaintenance is nil")
	}
	if maintenance.Status.State == state {
		return false, nil
	}
	maintenanceBase := maintenance.DeepCopy()
	maintenance.Status.State = state
	if err := r.Status().Patch(ctx, maintenance, client.MergeFrom(maintenanceBase)); err != nil {
		return false, fmt.Errorf("failed to patch ServerMaintenance status: %w", err)
	}
	return true, nil
}

func (r *ServerMaintenanceReconciler) enqueueMaintenanceByServerRefs() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
		log := logf.FromContext(ctx)
		server, ok := object.(*metalv1alpha1.Server)
		if !ok {
			log.Error(nil, "Expected object to be a Server", "object", object)
			return nil
		}

		var req []reconcile.Request
		if server.Status.State == metalv1alpha1.ServerStateInitial {
			return nil
		}

		if owner, ok := server.GetAnnotations()[controllerutils.ServerMaintenanceOwnerAnnotation]; ok {
			if ns, name, found := strings.Cut(owner, "/"); found {
				req = append(req, reconcile.Request{
					NamespacedName: types.NamespacedName{Namespace: ns, Name: name},
				})
			}
		}

		maintenanceList := &serverMaintenancev1alpha1.ServerMaintenanceList{}
		if err := r.List(ctx, maintenanceList, client.MatchingFields{serverRefField: server.Name}); err != nil {
			log.Error(err, "Failed to list ServerMaintenances")
			return req
		}
		for _, maintenance := range maintenanceList.Items {
			req = append(req, reconcile.Request{
				NamespacedName: types.NamespacedName{Namespace: maintenance.Namespace, Name: maintenance.Name},
			})
		}
		return req
	})
}

func (r *ServerMaintenanceReconciler) enqueueMaintenanceByClaimRefs() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, object client.Object) []reconcile.Request {
		log := logf.FromContext(ctx)
		claim, ok := object.(*metalv1alpha1.ServerClaim)
		if !ok {
			log.Error(nil, "Expected object to be a ServerClaim", "object", object)
			return nil
		}

		if _, ok := claim.Labels[serverMaintenancev1alpha1.ServerMaintenanceNeededLabelKey]; !ok {
			return nil
		}

		if claim.Spec.ServerRef == nil || claim.Spec.ServerRef.Name == "" {
			return nil
		}

		maintenanceList := &serverMaintenancev1alpha1.ServerMaintenanceList{}
		if err := r.List(ctx, maintenanceList, client.MatchingFields{serverRefField: claim.Spec.ServerRef.Name}); err != nil {
			log.Error(err, "Failed to list ServerMaintenances")
			return nil
		}

		var req []reconcile.Request
		for _, maintenance := range maintenanceList.Items {
			req = append(req, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Namespace: maintenance.Namespace,
					Name:      maintenance.Name,
				},
			})
		}
		return req
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *ServerMaintenanceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&serverMaintenancev1alpha1.ServerMaintenance{}).
		Owns(&metalv1alpha1.ServerBootConfiguration{}).
		Watches(&metalv1alpha1.Server{}, r.enqueueMaintenanceByServerRefs()).
		Watches(&metalv1alpha1.ServerClaim{}, r.enqueueMaintenanceByClaimRefs()).
		Complete(r)
}
