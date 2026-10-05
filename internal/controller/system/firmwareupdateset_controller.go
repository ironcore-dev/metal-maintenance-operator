// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package system

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/ironcore-dev/controller-utils/clientutils"
	systemv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/system/v1alpha1"
	utils "github.com/ironcore-dev/metal-maintenance-operator/internal/utils"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

const (
	firmwareUpdateSetFinalizer = "system.metal.ironcore.dev/firmwareupdateset"
)

// FirmwareUpdateSetReconciler reconciles a FirmwareUpdateSet object
type FirmwareUpdateSetReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	ResyncInterval time.Duration
}

// +kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=firmwareupdatesets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=firmwareupdatesets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=firmwareupdatesets/finalizers,verbs=update
// +kubebuilder:rbac:groups=system.metal.ironcore.dev,resources=firmwareupdates,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=servermaintenances,verbs=get;list;watch

// firmwareUpdateAdapter builds the utils.ChildAdapter for FirmwareUpdate children, closing
// over the owning set so ApplyTemplate can read its current template.
func firmwareUpdateAdapter(set *systemv1alpha1.FirmwareUpdateSet) utils.ChildAdapter[*systemv1alpha1.FirmwareUpdate] {
	return utils.ChildAdapter[*systemv1alpha1.FirmwareUpdate]{
		NewChild: func(name string) *systemv1alpha1.FirmwareUpdate {
			return &systemv1alpha1.FirmwareUpdate{ObjectMeta: metav1.ObjectMeta{Name: name}}
		},
		TargetName: func(c *systemv1alpha1.FirmwareUpdate) string {
			if c.Spec.ServerRef == nil {
				return ""
			}
			return c.Spec.ServerRef.Name
		},
		SetTargetRef: func(c *systemv1alpha1.FirmwareUpdate, serverName string) {
			c.Spec.ServerRef = &corev1.LocalObjectReference{Name: serverName}
		},
		ApplyTemplate: func(c *systemv1alpha1.FirmwareUpdate) {
			c.Spec.FirmwareUpdateTemplate = *set.Spec.FirmwareUpdateTemplate.DeepCopy()
		},
		IsInProgress: func(c *systemv1alpha1.FirmwareUpdate) bool {
			return c.Status.State == systemv1alpha1.FirmwareUpdateStateInProgress
		},
		State: func(c *systemv1alpha1.FirmwareUpdate) utils.ChildState {
			switch c.Status.State {
			case systemv1alpha1.FirmwareUpdateStateCompleted:
				return utils.ChildSucceeded
			case systemv1alpha1.FirmwareUpdateStateFailed:
				return utils.ChildFailed
			case systemv1alpha1.FirmwareUpdateStateInProgress:
				return utils.ChildInProgress
			default:
				return utils.ChildPending
			}
		},
		MaintenanceRefs: func(c *systemv1alpha1.FirmwareUpdate) []metalv1alpha1.ObjectReference {
			if c.Status.ServerMaintenanceRef == nil {
				return nil
			}
			return []metalv1alpha1.ObjectReference{*c.Status.ServerMaintenanceRef}
		},
	}
}

func (r *FirmwareUpdateSetReconciler) childManager() *utils.ChildManager[*systemv1alpha1.FirmwareUpdate] {
	return &utils.ChildManager[*systemv1alpha1.FirmwareUpdate]{Client: r.Client}
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *FirmwareUpdateSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	set := &systemv1alpha1.FirmwareUpdateSet{}
	if err := r.Get(ctx, req.NamespacedName, set); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return r.reconcileExists(ctx, set)
}

func (r *FirmwareUpdateSetReconciler) reconcileExists(ctx context.Context, set *systemv1alpha1.FirmwareUpdateSet) (ctrl.Result, error) {
	if !set.DeletionTimestamp.IsZero() {
		return r.delete(ctx, set)
	}
	return r.reconcile(ctx, set)
}

func (r *FirmwareUpdateSetReconciler) delete(ctx context.Context, set *systemv1alpha1.FirmwareUpdateSet) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("Deleting FirmwareUpdateSet")
	if !controllerutil.ContainsFinalizer(set, firmwareUpdateSetFinalizer) {
		return ctrl.Result{}, nil
	}

	if err := r.handleIgnoreAnnotationPropagation(ctx, set); err != nil {
		return ctrl.Result{}, err
	}

	owned, err := r.getOwnedFirmwareUpdates(ctx, set)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get owned FirmwareUpdates: %w", err)
	}

	adapter := firmwareUpdateAdapter(set)
	deletable, err := r.childManager().DeletableCount(ctx, adapter, owned)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to compute deletable FirmwareUpdates: %w", err)
	}

	if deletable != len(owned) || int32(len(owned)) != set.Status.AvailableFirmwareUpdate {
		status := toFirmwareUpdateSetStatus(r.childManager().Aggregate(adapter, owned))
		if err = r.patchStatus(ctx, status, set); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to patch FirmwareUpdateSet status: %w", err)
		}
		log.V(1).Info("Patched FirmwareUpdateSet status", "Status", status)

		if err := r.handleRetryAnnotationPropagation(ctx, set); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("Waiting for the created FirmwareUpdates to reach terminal status")
		return ctrl.Result{}, nil
	}

	log.V(1).Info("Ensuring that the finalizer is removed")
	if modified, err := clientutils.PatchEnsureNoFinalizer(ctx, r.Client, set, firmwareUpdateSetFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}

	log.V(1).Info("Deleted FirmwareUpdateSet")
	return ctrl.Result{}, nil
}

func (r *FirmwareUpdateSetReconciler) handleIgnoreAnnotationPropagation(ctx context.Context, set *systemv1alpha1.FirmwareUpdateSet) error {
	log := ctrl.LoggerFrom(ctx)
	owned, err := r.getOwnedFirmwareUpdates(ctx, set)
	if err != nil {
		return err
	}
	if len(owned) == 0 {
		log.V(1).Info("No FirmwareUpdate found, skipping ignore annotation propagation")
		return nil
	}
	return utils.HandleIgnoreAnnotationPropagation(ctx, r.Client, set, toFirmwareUpdateList(owned))
}

func (r *FirmwareUpdateSetReconciler) handleRetryAnnotationPropagation(ctx context.Context, set *systemv1alpha1.FirmwareUpdateSet) error {
	log := ctrl.LoggerFrom(ctx)
	owned, err := r.getOwnedFirmwareUpdates(ctx, set)
	if err != nil {
		return err
	}
	if len(owned) == 0 {
		log.V(1).Info("No FirmwareUpdate found, skipping retry annotation propagation")
		return nil
	}
	return utils.HandleRetryAnnotationPropagation(ctx, r.Client, set, toFirmwareUpdateList(owned))
}

func (r *FirmwareUpdateSetReconciler) reconcile(ctx context.Context, set *systemv1alpha1.FirmwareUpdateSet) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("Reconciling FirmwareUpdateSet")

	if err := r.handleIgnoreAnnotationPropagation(ctx, set); err != nil {
		return ctrl.Result{}, err
	}

	if utils.ShouldIgnoreReconciliation(set) {
		log.V(1).Info("Skipped FirmwareUpdateSet reconciliation")
		return ctrl.Result{}, nil
	}

	if modified, err := clientutils.PatchEnsureFinalizer(ctx, r.Client, set, firmwareUpdateSetFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}

	servers, err := r.getServersBySelector(ctx, set)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get servers by selector: %w", err)
	}

	owned, err := r.getOwnedFirmwareUpdates(ctx, set)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get owned FirmwareUpdates: %w", err)
	}

	log.V(1).Info("Summary of Servers and FirmwareUpdates", "ServerCount", len(servers.Items), "FirmwareUpdateCount", len(owned))

	adapter := firmwareUpdateAdapter(set)
	childManager := r.childManager()

	if err := childManager.EnsureForServers(ctx, adapter, set, servers.Items, owned); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to create FirmwareUpdates: %w", err)
	}

	if err := childManager.DeleteOrphans(ctx, adapter, servers.Items, owned); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to delete orphaned FirmwareUpdates: %w", err)
	}

	pending, err := childManager.PatchFromTemplate(ctx, adapter, owned)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to patch FirmwareUpdate spec from template: %w", err)
	}

	log.V(1).Info("Updating the status of FirmwareUpdateSet")
	status := toFirmwareUpdateSetStatus(childManager.Aggregate(adapter, owned))
	status.FullyLabeledServers = int32(len(servers.Items))

	if err := r.patchStatus(ctx, status, set); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update FirmwareUpdateSet status: %w", err)
	}
	log.V(1).Info("Patched FirmwareUpdateSet status", "Status", status)

	if err := r.handleRetryAnnotationPropagation(ctx, set); err != nil {
		return ctrl.Result{}, err
	}

	if status.FullyLabeledServers != status.AvailableFirmwareUpdate || pending {
		log.V(1).Info("Waiting for all FirmwareUpdates to be created/Patched for the labeled Servers", "Status", status)
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}

	log.V(1).Info("Reconciled FirmwareUpdateSet")
	return ctrl.Result{}, nil
}

// toFirmwareUpdateSetStatus maps aggregated utils.ChildCounts onto the FirmwareUpdateSet's
// concrete status fields.
func toFirmwareUpdateSetStatus(counts utils.ChildCounts) *systemv1alpha1.FirmwareUpdateSetStatus {
	return &systemv1alpha1.FirmwareUpdateSetStatus{
		AvailableFirmwareUpdate:  counts.Available,
		PendingFirmwareUpdate:    counts.Pending,
		InProgressFirmwareUpdate: counts.InProgress,
		CompletedFirmwareUpdate:  counts.Succeeded,
		FailedFirmwareUpdate:     counts.Failed,
	}
}

// toFirmwareUpdateList rewraps an already-fetched owned slice into a list object, as
// required by the utils annotation-propagation helpers.
func toFirmwareUpdateList(owned []*systemv1alpha1.FirmwareUpdate) *systemv1alpha1.FirmwareUpdateList {
	list := &systemv1alpha1.FirmwareUpdateList{Items: make([]systemv1alpha1.FirmwareUpdate, len(owned))}
	for i, fwUpdate := range owned {
		list.Items[i] = *fwUpdate
	}
	return list
}

func (r *FirmwareUpdateSetReconciler) getOwnedFirmwareUpdates(ctx context.Context, set *systemv1alpha1.FirmwareUpdateSet) ([]*systemv1alpha1.FirmwareUpdate, error) {
	fwUpdateList := &systemv1alpha1.FirmwareUpdateList{}
	if err := clientutils.ListAndFilterControlledBy(ctx, r.Client, set, fwUpdateList); err != nil {
		return nil, err
	}
	owned := make([]*systemv1alpha1.FirmwareUpdate, len(fwUpdateList.Items))
	for i := range fwUpdateList.Items {
		owned[i] = &fwUpdateList.Items[i]
	}
	return owned, nil
}

func (r *FirmwareUpdateSetReconciler) getServersBySelector(ctx context.Context, set *systemv1alpha1.FirmwareUpdateSet) (*metalv1alpha1.ServerList, error) {
	selector, err := metav1.LabelSelectorAsSelector(&set.Spec.ServerSelector)
	if err != nil {
		return nil, err
	}
	servers := &metalv1alpha1.ServerList{}
	if err := r.List(ctx, servers, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, err
	}
	return servers, nil
}

func (r *FirmwareUpdateSetReconciler) patchStatus(ctx context.Context, status *systemv1alpha1.FirmwareUpdateSetStatus, set *systemv1alpha1.FirmwareUpdateSet) error {
	setBase := set.DeepCopy()
	set.Status = *status

	if err := r.Status().Patch(ctx, set, client.MergeFrom(setBase)); err != nil {
		return err
	}
	return nil
}

func (r *FirmwareUpdateSetReconciler) enqueueByServer(ctx context.Context, obj client.Object) []ctrl.Request {
	log := ctrl.LoggerFrom(ctx)
	server := obj.(*metalv1alpha1.Server)

	setList := &systemv1alpha1.FirmwareUpdateSetList{}
	if err := r.List(ctx, setList); err != nil {
		log.Error(err, "Failed to list FirmwareUpdateSet")
		return nil
	}

	reqs := make([]ctrl.Request, 0)
	for _, set := range setList.Items {
		selector, err := metav1.LabelSelectorAsSelector(&set.Spec.ServerSelector)
		if err != nil {
			log.Error(err, "Failed to convert label selector")
			return nil
		}
		// If the Server label matches the selector, enqueue the request
		if selector.Matches(labels.Set(server.GetLabels())) {
			reqs = append(reqs, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: set.Namespace, Name: set.Name}})
			continue
		}
		// if the label has been removed, still enqueue if we currently own a FirmwareUpdate for this server
		owned, err := r.getOwnedFirmwareUpdates(ctx, &set)
		if err != nil {
			log.Error(err, "Failed to get owned FirmwareUpdates")
			return nil
		}
		for _, fwUpdate := range owned {
			if fwUpdate.Spec.ServerRef != nil && fwUpdate.Spec.ServerRef.Name == server.Name {
				reqs = append(reqs, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: set.Namespace, Name: set.Name}})
				break
			}
		}
	}
	return reqs
}

// SetupWithManager sets up the controller with the Manager.
func (r *FirmwareUpdateSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&systemv1alpha1.FirmwareUpdateSet{}).
		Owns(&systemv1alpha1.FirmwareUpdate{}).
		Watches(&metalv1alpha1.Server{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueByServer),
			builder.WithPredicates(predicate.LabelChangedPredicate{})).
		Complete(r)
}
