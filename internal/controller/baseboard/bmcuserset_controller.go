// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package baseboard

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/ironcore-dev/controller-utils/clientutils"
	baseboardv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/baseboard/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

const (
	bmcUserSetFinalizer = "baseboard.metal.ironcore.dev/bmcuserset"

	conditionTypeReady = "Ready"
)

// BMCUserSetReconciler reconciles a BMCUserSet object.
type BMCUserSetReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcusersets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcusersets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcusersets/finalizers,verbs=update
// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcusers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch

func (r *BMCUserSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	set := &baseboardv1alpha1.BMCUserSet{}
	if err := r.Get(ctx, req.NamespacedName, set); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !set.DeletionTimestamp.IsZero() {
		return r.delete(ctx, set)
	}
	return r.reconcile(ctx, set)
}

func (r *BMCUserSetReconciler) reconcile(ctx context.Context, set *baseboardv1alpha1.BMCUserSet) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)

	if modified, err := clientutils.PatchEnsureFinalizer(ctx, r.Client, set, bmcUserSetFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}

	serverList, err := r.listServers(ctx, set)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Build the desired set of child BMCUser names.
	desired := make(map[string]*metalv1alpha1.Server, len(serverList.Items))
	for i := range serverList.Items {
		srv := &serverList.Items[i]
		if srv.Spec.BMCRef == nil {
			log.V(1).Info("Skipping server without BMCRef", "server", srv.Name)
			continue
		}
		desired[childName(set.Name, srv.Name)] = srv
	}

	// List existing owned children.
	existingList := &baseboardv1alpha1.BMCUserList{}
	if err := r.List(ctx, existingList); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list BMCUsers: %w", err)
	}
	owned := make(map[string]*baseboardv1alpha1.BMCUser)
	for i := range existingList.Items {
		u := &existingList.Items[i]
		if !isOwnedBy(u, set) {
			continue
		}
		owned[u.Name] = u
	}

	// Delete stale children (server no longer selected).
	for name, u := range owned {
		if _, ok := desired[name]; !ok {
			log.Info("Deleting stale BMCUser", "name", name)
			if err := r.Delete(ctx, u); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, fmt.Errorf("failed to delete stale BMCUser %s: %w", name, err)
			}
		}
	}

	// Create or update children.
	for name, srv := range desired {
		tmpl := set.Spec.Template
		if existing, ok := owned[name]; ok {
			// Patch if spec drifted.
			if existing.Spec.RoleID != tmpl.RoleID ||
				existing.Spec.Description != tmpl.Description ||
				!rotationPeriodsEqual(existing.Spec.RotationPeriod, tmpl.RotationPeriod) {
				base := existing.DeepCopy()
				existing.Spec.RoleID = tmpl.RoleID
				existing.Spec.Description = tmpl.Description
				existing.Spec.RotationPeriod = tmpl.RotationPeriod
				if err := r.Patch(ctx, existing, client.MergeFrom(base)); err != nil {
					return ctrl.Result{}, fmt.Errorf("failed to patch BMCUser %s: %w", name, err)
				}
			}
		} else {
			u := &baseboardv1alpha1.BMCUser{
				ObjectMeta: metav1.ObjectMeta{
					Name: name,
				},
				Spec: baseboardv1alpha1.BMCUserSpec{
					UserName:       tmpl.UserName,
					RoleID:         tmpl.RoleID,
					Description:    tmpl.Description,
					RotationPeriod: tmpl.RotationPeriod,
					BMCRef: &corev1.LocalObjectReference{
						Name: srv.Spec.BMCRef.Name,
					},
				},
			}
			if err := controllerutil.SetControllerReference(set, u, r.Scheme); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to set owner reference on BMCUser %s: %w", name, err)
			}
			log.Info("Creating BMCUser", "name", name)
			if err := r.Create(ctx, u); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to create BMCUser %s: %w", name, err)
			}
		}
	}

	return r.updateStatus(ctx, set, desired, owned)
}

func (r *BMCUserSetReconciler) delete(ctx context.Context, set *baseboardv1alpha1.BMCUserSet) (ctrl.Result, error) {
	// Owned BMCUsers are garbage-collected via owner references; just remove finalizer.
	if modified, err := clientutils.PatchEnsureNoFinalizer(ctx, r.Client, set, bmcUserSetFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (r *BMCUserSetReconciler) updateStatus(
	ctx context.Context,
	set *baseboardv1alpha1.BMCUserSet,
	desired map[string]*metalv1alpha1.Server,
	owned map[string]*baseboardv1alpha1.BMCUser,
) (ctrl.Result, error) {
	total := int32(len(desired))
	var ready, pending int32
	for name := range desired {
		u, exists := owned[name]
		if exists && isUserReady(u) {
			ready++
		} else {
			pending++
		}
	}

	base := set.DeepCopy()
	set.Status.TotalServers = total
	set.Status.ReadyServers = ready
	set.Status.PendingServers = pending

	readyStatus := metav1.ConditionFalse
	readyReason := "Pending"
	readyMsg := fmt.Sprintf("%d/%d servers ready", ready, total)
	if total > 0 && ready == total {
		readyStatus = metav1.ConditionTrue
		readyReason = "AllReady"
		readyMsg = fmt.Sprintf("All %d servers ready", total)
	}
	apimeta.SetStatusCondition(&set.Status.Conditions, metav1.Condition{
		Type:               conditionTypeReady,
		Status:             readyStatus,
		Reason:             readyReason,
		Message:            readyMsg,
		ObservedGeneration: set.Generation,
	})

	if err := r.Status().Patch(ctx, set, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to patch BMCUserSet status: %w", err)
	}

	if pending > 0 {
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

func (r *BMCUserSetReconciler) listServers(ctx context.Context, set *baseboardv1alpha1.BMCUserSet) (*metalv1alpha1.ServerList, error) {
	selector, err := metav1.LabelSelectorAsSelector(&set.Spec.ServerSelector)
	if err != nil {
		return nil, fmt.Errorf("invalid server selector: %w", err)
	}
	list := &metalv1alpha1.ServerList{}
	if err := r.List(ctx, list, &client.ListOptions{LabelSelector: selector}); err != nil {
		return nil, fmt.Errorf("failed to list servers: %w", err)
	}
	return list, nil
}

func (r *BMCUserSetReconciler) findSetsForServer(ctx context.Context, obj client.Object) []reconcile.Request {
	setList := &baseboardv1alpha1.BMCUserSetList{}
	if err := r.List(ctx, setList); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for _, set := range setList.Items {
		selector, err := metav1.LabelSelectorAsSelector(&set.Spec.ServerSelector)
		if err != nil {
			continue
		}
		if selector.Matches(labels.Set(obj.GetLabels())) {
			requests = append(requests, reconcile.Request{
				NamespacedName: client.ObjectKey{Name: set.Name},
			})
		}
	}
	return requests
}

// SetupWithManager sets up the controller with the Manager.
func (r *BMCUserSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&baseboardv1alpha1.BMCUserSet{}).
		Owns(&baseboardv1alpha1.BMCUser{}).
		Watches(&metalv1alpha1.Server{},
			handler.EnqueueRequestsFromMapFunc(r.findSetsForServer)).
		Named("bmcuserset").
		Complete(r)
}

// childName returns the deterministic name for a child BMCUser.
// Format: <set>--<server> (double dash to reduce collision risk with names
// that themselves contain dashes). Kubernetes names are capped at 253 chars;
// we keep it simple and truncate the server portion if needed.
func childName(setName, serverName string) string {
	name := setName + "--" + serverName
	if len(name) > 253 {
		name = name[:253]
	}
	return name
}

func isOwnedBy(u *baseboardv1alpha1.BMCUser, set *baseboardv1alpha1.BMCUserSet) bool {
	for _, ref := range u.OwnerReferences {
		if ref.UID == set.UID {
			return true
		}
	}
	return false
}

func isUserReady(u *baseboardv1alpha1.BMCUser) bool {
	for _, c := range u.Status.Conditions {
		if c.Type == conditionTypeReady {
			return c.Status == metav1.ConditionTrue
		}
	}
	return false
}

func rotationPeriodsEqual(a, b *metav1.Duration) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Duration == b.Duration
}
