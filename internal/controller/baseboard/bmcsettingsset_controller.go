// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package baseboard

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ironcore-dev/controller-utils/clientutils"
	baseboardv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/baseboard/v1alpha1"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/constants"
	utils "github.com/ironcore-dev/metal-maintenance-operator/internal/utils"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	BMCSettingsSetFinalizer = "baseboard.metal.ironcore.dev/bmcsettingsset"
)

type BMCSettingsSetReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	ResyncInterval time.Duration
}

// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcsettingssets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcsettingssets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcsettingssets/finalizers,verbs=update
// +kubebuilder:rbac:groups=baseboard.metal.ironcore.dev,resources=bmcsettings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=bmcs,verbs=get;list;watch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=servermaintenances,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *BMCSettingsSetReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	bmcSettingsSet := &baseboardv1alpha1.BMCSettingsSet{}
	if err := r.Get(ctx, req.NamespacedName, bmcSettingsSet); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("Reconciling bmcSettingsSet")

	return r.reconcileExist(ctx, bmcSettingsSet)
}

func (r *BMCSettingsSetReconciler) reconcileExist(
	ctx context.Context,
	bmcSettingsSet *baseboardv1alpha1.BMCSettingsSet,
) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	if !bmcSettingsSet.DeletionTimestamp.IsZero() {
		log.V(1).Info("Object is being deleted")
		return r.delete(ctx, bmcSettingsSet)
	}
	log.V(1).Info("Object exists and is not being deleted")
	return r.reconcile(ctx, bmcSettingsSet)
}

func (r *BMCSettingsSetReconciler) reconcile(
	ctx context.Context,
	bmcSettingsSet *baseboardv1alpha1.BMCSettingsSet,
) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	if utils.ShouldIgnoreReconciliation(bmcSettingsSet) {
		log.V(1).Info("Skipped BMCSettingsSet reconciliation")
		return ctrl.Result{}, nil
	}
	if modified, err := clientutils.PatchEnsureFinalizer(ctx, r.Client, bmcSettingsSet, BMCSettingsSetFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}

	bmcList, err := r.getBMCsBySelector(ctx, bmcSettingsSet)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to get BMCs through bmclabel selector %w", err)
	}

	return r.handleBMCSettings(ctx, bmcList, bmcSettingsSet)
}

func (r *BMCSettingsSetReconciler) delete(
	ctx context.Context,
	bmcSettingsSet *baseboardv1alpha1.BMCSettingsSet,
) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	if !controllerutil.ContainsFinalizer(bmcSettingsSet, BMCSettingsSetFinalizer) {
		return ctrl.Result{}, nil
	}

	ownedBMCSettings, err := r.getOwnedBMCSettings(ctx, bmcSettingsSet)
	if err != nil {
		log.Error(err, "Failed to list owned BMCSettings")
		return ctrl.Result{}, fmt.Errorf("failed to get owned BMCSettings resources %w", err)
	}
	delTableBMCSettings := map[string]struct{}{}
	for _, bmcSettings := range ownedBMCSettings.Items {
		if len(bmcSettings.Spec.ServerMaintenanceRefs) == 0 {
			delTableBMCSettings[bmcSettings.Name] = struct{}{}
			continue
		}
		refs := make([]metalv1alpha1.ObjectReference, 0, len(bmcSettings.Spec.ServerMaintenanceRefs))
		for _, item := range bmcSettings.Spec.ServerMaintenanceRefs {
			if item.ServerMaintenanceRef != nil {
				refs = append(refs, *item.ServerMaintenanceRef)
			}
		}
		active, err := utils.IsAnyServerMaintenanceActive(ctx, r.Client, refs)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to check maintenance state for BMCSettings %s: %w", bmcSettings.Name, err)
		}
		if !active {
			delTableBMCSettings[bmcSettings.Name] = struct{}{}
		}
	}
	if len(ownedBMCSettings.Items) != len(delTableBMCSettings) || int32(len(ownedBMCSettings.Items)) != bmcSettingsSet.Status.AvailableBMCSettings {
		currentStatus := r.getOwnedBMCSettingsSetStatus(ownedBMCSettings)
		err = r.updateStatus(ctx, currentStatus, bmcSettingsSet)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to update current BMCSettingsSet Status %w", err)
		}
		log.V(1).Info("Updated BMCSettingsSet state", "Status", currentStatus)

		// Handle propagation of retry annotation to child when parent is being deleted.
		// That way the deleted annotations can be passed to children before parent is deleted.
		if err := r.handleRetryAnnotationPropagation(ctx, bmcSettingsSet); err != nil {
			return ctrl.Result{}, err
		}
		log.Info("Waiting on the created BMCSettings to reach terminal status")
		return ctrl.Result{}, nil

	}

	log.V(1).Info("Ensuring that the finalizer is removed")
	if modified, err := clientutils.PatchEnsureNoFinalizer(ctx, r.Client, bmcSettingsSet, BMCSettingsSetFinalizer); err != nil || modified {
		return ctrl.Result{}, err
	}

	log.V(1).Info("BMCSettingsSet is deleted")
	return ctrl.Result{}, nil
}

func (r *BMCSettingsSetReconciler) getOwnedBMCSettings(
	ctx context.Context,
	bmcSettingsSet *baseboardv1alpha1.BMCSettingsSet,
) (*baseboardv1alpha1.BMCSettingsList, error) {
	bmcSettingsList := &baseboardv1alpha1.BMCSettingsList{}
	if err := clientutils.ListAndFilterControlledBy(ctx, r.Client, bmcSettingsSet, bmcSettingsList); err != nil {
		return nil, err
	}
	return bmcSettingsList, nil
}

func (r *BMCSettingsSetReconciler) getOwnedBMCSettingsSetStatus(
	bmcSettingsList *baseboardv1alpha1.BMCSettingsList,
) *baseboardv1alpha1.BMCSettingsSetStatus {
	currentStatus := &baseboardv1alpha1.BMCSettingsSetStatus{}
	currentStatus.AvailableBMCSettings = int32(len(bmcSettingsList.Items))
	for _, bmcSettings := range bmcSettingsList.Items {
		switch bmcSettings.Status.State {
		case baseboardv1alpha1.BMCSettingsStateApplied:
			currentStatus.CompletedBMCSettings += 1
		case baseboardv1alpha1.BMCSettingsStateFailed:
			currentStatus.FailedBMCSettings += 1
		case baseboardv1alpha1.BMCSettingsStateInProgress:
			currentStatus.InProgressBMCSettings += 1
		case baseboardv1alpha1.BMCSettingsStatePending, "":
			currentStatus.PendingBMCSettings += 1
		}
	}
	return currentStatus
}

func (r *BMCSettingsSetReconciler) updateStatus(
	ctx context.Context,
	currentStatus *baseboardv1alpha1.BMCSettingsSetStatus,
	bmcSettingsSet *baseboardv1alpha1.BMCSettingsSet,
) error {
	log := ctrl.LoggerFrom(ctx)
	bmcSettingsSetBase := bmcSettingsSet.DeepCopy()
	bmcSettingsSet.Status = *currentStatus
	if err := r.Status().Patch(ctx, bmcSettingsSet, client.MergeFrom(bmcSettingsSetBase)); err != nil {
		return err
	}
	log.V(1).Info("Updated bmcSettingsSet state ", "new state", currentStatus)
	return nil
}

func (r *BMCSettingsSetReconciler) getBMCsBySelector(
	ctx context.Context,
	bmcSettingsSet *baseboardv1alpha1.BMCSettingsSet,
) (*metalv1alpha1.BMCList, error) {
	selector, err := metav1.LabelSelectorAsSelector(&bmcSettingsSet.Spec.BMCSelector)
	if err != nil {
		return nil, err
	}

	bmcList := &metalv1alpha1.BMCList{}
	if err := r.List(ctx, bmcList, client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, err
	}
	return bmcList, nil

}

func (r *BMCSettingsSetReconciler) handleBMCSettings(
	ctx context.Context,
	bmcList *metalv1alpha1.BMCList,
	bmcSettingsSet *baseboardv1alpha1.BMCSettingsSet,
) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	ownedBMCSettings, err := r.getOwnedBMCSettings(ctx, bmcSettingsSet)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := r.createMissingBMCSettings(ctx, bmcList, ownedBMCSettings, bmcSettingsSet); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to create missing BMCSettings resources %w", err)
	}
	log.V(1).Info("Summary of BMCs and BMCSettings", "BMC count", len(bmcList.Items),
		"BMCSettings count", len(ownedBMCSettings.Items))

	if err := r.deleteOrphanedBMCSettings(ctx, bmcList, ownedBMCSettings); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to delete orphaned BMCSettings resource %w", err)
	}

	var pendingPatchingSettings bool
	if pendingPatchingSettings, err = r.patchBMCSettingsFromTemplate(ctx, bmcSettingsSet, ownedBMCSettings); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to patch BMCSettings from template %w", err)
	}

	log.V(1).Info("Updating BMCSettingsSet status")
	currentStatus := r.getOwnedBMCSettingsSetStatus(ownedBMCSettings)
	currentStatus.FullyLabeledBMCs = int32(len(bmcList.Items))
	if err := r.updateStatus(ctx, currentStatus, bmcSettingsSet); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to update current BMCSettingsSet Status %w", err)
	}

	// handle retry annotation - remove the annotation after retrying reconciliation
	if err := r.handleRetryAnnotationPropagation(ctx, bmcSettingsSet); err != nil {
		return ctrl.Result{}, err
	}

	if currentStatus.FullyLabeledBMCs != currentStatus.AvailableBMCSettings || pendingPatchingSettings {
		log.V(1).Info("Waiting for all BMCSettings to be created/Patched for the labeled BMCs", "Status", currentStatus)
		return ctrl.Result{RequeueAfter: r.ResyncInterval}, nil
	}

	return ctrl.Result{}, nil
}

func (r *BMCSettingsSetReconciler) createMissingBMCSettings(
	ctx context.Context,
	bmcList *metalv1alpha1.BMCList,
	bmcSettingsList *baseboardv1alpha1.BMCSettingsList,
	bmcSettingsSet *baseboardv1alpha1.BMCSettingsSet,
) error {
	log := ctrl.LoggerFrom(ctx)
	bmcWithSettings := make(map[string]struct{})
	for _, bmcSettings := range bmcSettingsList.Items {
		bmcWithSettings[bmcSettings.Spec.BMCRef.Name] = struct{}{}
	}

	var errs []error
	for _, bmc := range bmcList.Items {
		if _, ok := bmcWithSettings[bmc.Name]; !ok {
			existingList := &baseboardv1alpha1.BMCSettingsList{}
			if err := r.List(ctx, existingList, client.MatchingFields{constants.BMCRefField: bmc.Name}); err != nil {
				log.Error(err, "Failed to list existing BMCSettings", "BMC", bmc.Name)
				errs = append(errs, err)
				continue
			}
			if len(existingList.Items) > 0 {
				log.V(1).Info("Found existing BMCSettings for BMC", "BMC", bmc.Name, "BMCSettings", existingList.Items[0].Name)
				continue
			}

			// generate a deterministic k8s conform name for bmcsettings, so that a
			// re-reconcile on a stale cache cannot create duplicate children.
			newBMCSettingsName := utils.VersionSetChildName(bmcSettingsSet.Name, bmc.Name)
			newBMCSettings := &baseboardv1alpha1.BMCSettings{
				ObjectMeta: metav1.ObjectMeta{
					Name: newBMCSettingsName,
				}}

			// create/patch new Settings
			opResult, err := controllerutil.CreateOrPatch(ctx, r.Client, newBMCSettings, func() error {
				newBMCSettings.Spec.BMCSettingsTemplate = *bmcSettingsSet.Spec.BMCSettingsTemplate.DeepCopy()
				newBMCSettings.Spec.BMCRef = &corev1.LocalObjectReference{Name: bmc.Name}
				return controllerutil.SetControllerReference(bmcSettingsSet, newBMCSettings, r.Client.Scheme())
			})

			if err != nil {
				errs = append(errs, err)
			}
			log.V(1).Info("Created BMCSettings", "BMCSettings", newBMCSettings.Name, "bmc ref", bmc.Name, "Operation", opResult)
		}
	}
	return errors.Join(errs...)
}

func (r *BMCSettingsSetReconciler) deleteOrphanedBMCSettings(
	ctx context.Context,
	bmcList *metalv1alpha1.BMCList,
	bmcSettingsList *baseboardv1alpha1.BMCSettingsList,
) error {
	log := ctrl.LoggerFrom(ctx)
	bmcMap := make(map[string]struct{})
	for _, bmc := range bmcList.Items {
		bmcMap[bmc.Name] = struct{}{}
	}
	var errs []error
	for _, bmcSettings := range bmcSettingsList.Items {
		if _, ok := bmcMap[bmcSettings.Spec.BMCRef.Name]; !ok {
			if bmcSettings.Status.State == baseboardv1alpha1.BMCSettingsStateInProgress && len(bmcSettings.Spec.ServerMaintenanceRefs) > 0 {
				refs := make([]metalv1alpha1.ObjectReference, 0, len(bmcSettings.Spec.ServerMaintenanceRefs))
				for _, item := range bmcSettings.Spec.ServerMaintenanceRefs {
					if item.ServerMaintenanceRef != nil {
						refs = append(refs, *item.ServerMaintenanceRef)
					}
				}
				active, err := utils.IsAnyServerMaintenanceActive(ctx, r.Client, refs)
				if err != nil {
					errs = append(errs, fmt.Errorf("failed to check maintenance state for BMCSettings %s: %w", bmcSettings.Name, err))
					continue
				}
				if active {
					log.V(1).Info("Waiting for BMCSettings maintenance to complete before deletion",
						"BMCSettings", bmcSettings.Name, "status", bmcSettings.Status)
					continue
				}
			}
			if err := r.Delete(ctx, &bmcSettings); err != nil {
				errs = append(errs, err)
			}
		}
	}

	return errors.Join(errs...)

}

func (r *BMCSettingsSetReconciler) patchBMCSettingsFromTemplate(
	ctx context.Context,
	bmcSettingsSet *baseboardv1alpha1.BMCSettingsSet,
	bmcSettingsList *baseboardv1alpha1.BMCSettingsList,
) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if len(bmcSettingsList.Items) == 0 {
		log.V(1).Info("No BMCSettings found, skipping spec template update")
		return false, nil
	}

	var pendingPatchingSettings bool
	var errs []error
	for _, bmcSettings := range bmcSettingsList.Items {
		if bmcSettings.Status.State == baseboardv1alpha1.BMCSettingsStateInProgress {
			log.V(1).Info("Skipping BMCSettings spec patching as it is in InProgress state")
			pendingPatchingSettings = true
			continue
		}

		opResult, err := controllerutil.CreateOrPatch(ctx, r.Client, &bmcSettings, func() error {
			bmcSettings.Spec.BMCSettingsTemplate = *bmcSettingsSet.Spec.BMCSettingsTemplate.DeepCopy()
			return nil
		})
		if err != nil {
			errs = append(errs, err)
		}
		if opResult != controllerutil.OperationResultNone {
			log.V(1).Info("Patched BMCSettings with updated spec", "BMCSettings", bmcSettings.Name, "Operation", opResult)
		}
	}
	return pendingPatchingSettings, errors.Join(errs...)
}

func (r *BMCSettingsSetReconciler) handleRetryAnnotationPropagation(ctx context.Context, set *baseboardv1alpha1.BMCSettingsSet) error {
	log := ctrl.LoggerFrom(ctx)
	ownedBMCSettings, err := r.getOwnedBMCSettings(ctx, set)
	if err != nil {
		return err
	}

	if len(ownedBMCSettings.Items) == 0 {
		log.V(1).Info("No BMCSettings found, skipping retry annotation propagation")
		return nil
	}

	log.V(1).Info("Propagating retry annotation to owned BMCSettings resources")
	return utils.HandleRetryAnnotationPropagation(ctx, r.Client, set, ownedBMCSettings)
}

func (r *BMCSettingsSetReconciler) enqueueByBMC(
	ctx context.Context,
	obj client.Object,
) []ctrl.Request {
	log := ctrl.LoggerFrom(ctx)

	bmc := obj.(*metalv1alpha1.BMC)
	bmcSettingsSetList := &baseboardv1alpha1.BMCSettingsSetList{}

	if err := r.List(ctx, bmcSettingsSetList); err != nil {
		log.Error(err, "Failed to list BMCSettingsSet")
		return nil
	}
	var reqs []ctrl.Request
	for _, bmcSettingsSet := range bmcSettingsSetList.Items {
		selector, err := metav1.LabelSelectorAsSelector(&bmcSettingsSet.Spec.BMCSelector)
		if err != nil {
			log.Error(err, "Failed to parse BMCSelector", "BMCSettingsSet", bmcSettingsSet.Name)
			return nil
		}
		if selector.Matches(labels.Set(bmc.GetLabels())) {
			reqs = append(reqs, ctrl.Request{
				NamespacedName: client.ObjectKey{
					Name:      bmcSettingsSet.Name,
					Namespace: bmcSettingsSet.Namespace,
				},
			})
		} else {
			ownedBMCSettings, err := r.getOwnedBMCSettings(ctx, &bmcSettingsSet)
			if err != nil {
				log.Error(err, "Failed to list owned BMCSettings")
				return nil
			}
			for _, bmcSettings := range ownedBMCSettings.Items {
				if bmcSettings.Spec.BMCRef.Name == bmc.Name {
					reqs = append(reqs, ctrl.Request{
						NamespacedName: client.ObjectKey{
							Name:      bmcSettingsSet.Name,
							Namespace: bmcSettingsSet.Namespace,
						},
					})
				}
			}
		}
	}
	return reqs
}

// SetupWithManager sets up the controller with the Manager.
func (r *BMCSettingsSetReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&baseboardv1alpha1.BMCSettingsSet{}).
		Owns(&baseboardv1alpha1.BMCSettings{}).
		Watches(
			// Watch BMC resources for label changes to trigger reconciliation
			&metalv1alpha1.BMC{},
			handler.EnqueueRequestsFromMapFunc(r.enqueueByBMC),
			builder.WithPredicates(predicate.LabelChangedPredicate{})).
		Named("bmcsettingsset").
		Complete(r)
}
