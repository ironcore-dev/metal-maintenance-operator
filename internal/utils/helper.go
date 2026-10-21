// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// Package utils provides shared helper functions for baseboard and system controllers.
// It consolidates utilities that were previously scattered across metal-operator's internal packages,
// which are not importable from outside that module.
package utils

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"maps"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/ironcore-dev/controller-utils/conditionutils"
	"github.com/ironcore-dev/metal-maintenance-operator/api"
	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/maintenance/v1alpha1"
	"github.com/ironcore-dev/metal-maintenance-operator/third_party/expansion"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/ironcore-dev/metal-operator/bmc"
	"github.com/stmcginnis/gofish/schemas"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/constants"
)

// BMCTaskFetchFailedError is returned when fetching a BMC task fails.
type BMCTaskFetchFailedError struct {
	TaskURI  string
	Resource string
	Err      error
}

func (e BMCTaskFetchFailedError) Error() string {
	return e.Err.Error()
}

// MultiErrorTracker wraps multiple errors with an identifier.
type MultiErrorTracker struct {
	Identifier string
	Err        error
}

func (e MultiErrorTracker) Error() string {
	return e.Err.Error()
}

func (e MultiErrorTracker) Unwrap() error {
	return e.Err
}

// --- ServerMaintenance helpers ---

// IsAnyServerMaintenanceActive returns true if any referenced ServerMaintenance is in InMaintenance state.
func IsAnyServerMaintenanceActive(ctx context.Context, c client.Reader, refs []metalv1alpha1.ObjectReference) (bool, error) {
	for _, ref := range refs {
		sm := &maintenancev1alpha1.ServerMaintenance{}
		if err := c.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: ref.Namespace}, sm); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return false, fmt.Errorf("failed to get ServerMaintenance %s/%s: %w", ref.Namespace, ref.Name, err)
		}
		if !sm.DeletionTimestamp.IsZero() {
			continue
		}
		if sm.Status.State == maintenancev1alpha1.ServerMaintenanceStateInMaintenance {
			return true, nil
		}
	}
	return false, nil
}

// GetServerMaintenanceForObjectReference fetches a ServerMaintenance by ObjectReference.
func GetServerMaintenanceForObjectReference(ctx context.Context, c client.Client, ref *metalv1alpha1.ObjectReference) (*maintenancev1alpha1.ServerMaintenance, error) {
	if ref == nil {
		return nil, fmt.Errorf("got nil reference")
	}
	maintenance := &maintenancev1alpha1.ServerMaintenance{}
	if err := c.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: ref.Namespace}, maintenance); err != nil {
		return nil, fmt.Errorf("failed to get ServerMaintenance: %w", err)
	}
	return maintenance, nil
}

// --- Server Parked-state helpers ---

// ServerMaintenanceOwnerAnnotation records which owner (a ServerMaintenance,
// BIOSSettings, or BMCSettings object, encoded as a "namespace/name" key via
// ServerMaintenanceOwnerKey) currently owns a Server's Parked state.
// metal-operator's Parked state (unlike ServerMaintenanceRef) has no concept
// of an owning object, so ownership is tracked here via this annotation to
// give the same "single active claimant" semantics ServerMaintenanceRef used
// to provide.
const ServerMaintenanceOwnerAnnotation = "maintenance.metal.ironcore.dev/server-maintenance"

// ServerMaintenanceOwnerKey returns the "namespace/name" key used to record
// ownership of a Server's Parked state, see ServerMaintenanceOwnerAnnotation.
func ServerMaintenanceOwnerKey(namespace, name string) string {
	return client.ObjectKey{Namespace: namespace, Name: name}.String()
}

// IsServerParkedForOwner reports whether server is currently Parked and its
// park was requested by the owner identified by ownerKey (see
// ServerMaintenanceOwnerKey).
func IsServerParkedForOwner(server *metalv1alpha1.Server, ownerKey string) bool {
	if server.Status.State != metalv1alpha1.ServerStateParked {
		return false
	}
	return server.GetAnnotations()[ServerMaintenanceOwnerAnnotation] == ownerKey
}

// GetServerPowerState queries the BMC directly for the server's current power state.
// Server.Status.PowerState must not be used for this: metal-operator stops refreshing
// Server.Status while the server is parked for maintenance, so it can be arbitrarily stale
// for the entire duration of a maintenance window - exactly when callers here need to
// observe real power transitions (e.g. confirming a reboot completed).
func GetServerPowerState(ctx context.Context, bmcClient bmc.BMC, server *metalv1alpha1.Server) (metalv1alpha1.ServerPowerState, error) {
	systemInfo, err := bmcClient.GetSystemInfo(ctx, server.Spec.SystemURI)
	if err != nil {
		return "", fmt.Errorf("failed to get system info for server %s: %w", server.Name, err)
	}
	return metalv1alpha1.ServerPowerState(systemInfo.PowerState), nil
}

// IsServerInPowerState reports whether the server is currently in the given power state,
// queried live from the BMC (see GetServerPowerState for why Server.Status.PowerState
// cannot be used here).
func IsServerInPowerState(ctx context.Context, bmcClient bmc.BMC, server *metalv1alpha1.Server, state metalv1alpha1.ServerPowerState) (bool, error) {
	current, err := GetServerPowerState(ctx, bmcClient, server)
	if err != nil {
		return false, err
	}
	return current == state, nil
}

// --- Deletion helpers ---

// ShouldProceedWithDeletion returns true when obj should proceed with deletion.
// isProgressing is called only when the object has the finalizer; it returns true
// when deletion should be postponed (e.g. actively progressing under maintenance).
func ShouldProceedWithDeletion(
	ctx context.Context,
	obj client.Object,
	finalizer string,
	isProgressing func() (bool, error),
) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if obj.GetDeletionTimestamp().IsZero() {
		return false, nil
	}
	if controllerutil.ContainsFinalizer(obj, finalizer) {
		progressing, err := isProgressing()
		if err != nil {
			return false, err
		}
		if progressing {
			log.V(1).Info("Postponing deletion: resource is progressing under active maintenance")
			return false, nil
		}
	}
	log.V(1).Info("Proceeding with deletion")
	return true, nil
}

// --- Condition helpers ---

// GetCondition finds a condition in a slice, returning a False stub if not found.
func GetCondition(acc *conditionutils.Accessor, conditions []metav1.Condition, conditionType string) (*metav1.Condition, error) {
	condition := &metav1.Condition{}
	condFound, err := acc.FindSlice(conditions, conditionType, condition)
	if err != nil {
		return nil, fmt.Errorf("failed to find Condition %v. error: %w", conditionType, err)
	}
	if !condFound {
		condition.Type = conditionType
		if err := acc.Update(condition, conditionutils.UpdateStatus(corev1.ConditionFalse)); err != nil {
			return condition, fmt.Errorf("failed to create/update new Condition %v. error: %w", conditionType, err)
		}
	}
	return condition, nil
}

// --- Server helpers ---

// GetServerByName returns a Server by name.
func GetServerByName(ctx context.Context, c client.Client, serverName string) (*metalv1alpha1.Server, error) {
	server := &metalv1alpha1.Server{}
	if err := c.Get(ctx, client.ObjectKey{Name: serverName}, server); err != nil {
		return nil, err
	}
	return server, nil
}

// --- Annotation helpers ---

// ShouldIgnoreReconciliation checks if the object has an ignore-reconciliation annotation.
func ShouldIgnoreReconciliation(obj client.Object) bool {
	val, found := obj.GetAnnotations()[constants.OperationAnnotation]
	if !found {
		return false
	}
	return slices.Contains([]string{
		constants.OperationAnnotationIgnore,
		constants.OperationAnnotationIgnoreChildAndSelf,
		constants.OperationAnnotationIgnorePropagated,
	}, val)
}

// ShouldChildIgnoreReconciliation checks if the parent's annotation requests child ignore.
func ShouldChildIgnoreReconciliation(parentObj client.Object) bool {
	val, found := parentObj.GetAnnotations()[constants.OperationAnnotation]
	if !found {
		return false
	}
	return val == constants.OperationAnnotationIgnoreChild || val == constants.OperationAnnotationIgnoreChildAndSelf
}

// IsChildIgnoredThroughSets checks if the child carries a propagated ignore annotation.
func IsChildIgnoredThroughSets(childObj client.Object) bool {
	annotations := childObj.GetAnnotations()
	valChildIgnore, found := annotations[constants.OperationAnnotation]
	if !found {
		return false
	}
	return valChildIgnore == constants.OperationAnnotationIgnorePropagated
}

// ShouldRetryReconciliation checks if the object has a retry-failed annotation.
func ShouldRetryReconciliation(obj client.Object) bool {
	val, found := obj.GetAnnotations()[constants.OperationAnnotation]
	if !found {
		return false
	}
	return val == constants.OperationAnnotationRetry || val == constants.OperationAnnotationRetryPropagated
}

// ShouldChildRetryReconciliation checks if the parent's annotation requests child retry.
func ShouldChildRetryReconciliation(parentObj client.Object) bool {
	val, found := parentObj.GetAnnotations()[constants.OperationAnnotation]
	if !found {
		return false
	}
	return val == constants.OperationAnnotationRetryChild || val == constants.OperationAnnotationRetryChildAndSelf
}

// IsChildRetryThroughSets checks if the child carries a propagated retry annotation.
func IsChildRetryThroughSets(childObj client.Object) bool {
	annotations := childObj.GetAnnotations()
	valChildRetry, found := annotations[constants.OperationAnnotation]
	if !found {
		return false
	}
	return valChildRetry == constants.OperationAnnotationRetryPropagated
}

// --- Annotation propagation helpers ---

// HandleIgnoreAnnotationPropagation propagates the ignore annotation from parent to owned children.
func HandleIgnoreAnnotationPropagation(ctx context.Context, c client.Client, parentObj client.Object, ownedObjects client.ObjectList) error {
	log := ctrl.LoggerFrom(ctx)
	var errs []error
	_ = apimeta.EachListItem(ownedObjects, func(obj runtime.Object) error {
		childObj, ok := obj.(client.Object)
		if !ok {
			errs = append(errs, fmt.Errorf("item in list is not a client.Object: %T", obj))
			return nil
		}
		if !childObj.GetDeletionTimestamp().IsZero() {
			return nil
		}
		opResult, err := controllerutil.CreateOrPatch(ctx, c, childObj, func() error {
			annotations := childObj.GetAnnotations()
			if !ShouldChildIgnoreReconciliation(parentObj) && IsChildIgnoredThroughSets(childObj) && annotations != nil {
				delete(annotations, constants.OperationAnnotation)
				childObj.SetAnnotations(annotations)
			}
			_, operationAnnotationChildFound := annotations[constants.OperationAnnotation]
			if ShouldChildIgnoreReconciliation(parentObj) && !operationAnnotationChildFound {
				if annotations == nil {
					annotations = make(map[string]string)
				}
				annotations[constants.OperationAnnotation] = constants.OperationAnnotationIgnorePropagated
				childObj.SetAnnotations(annotations)
			}
			return nil
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to propagate ignore annotation to child %s: %w", childObj.GetName(), err))
		}
		if opResult != controllerutil.OperationResultNone {
			log.V(1).Info("Patched Child's annotations for ignore operation", "ChildResource", childObj.GetName(), "Operation", opResult)
		}
		return nil
	})
	return errors.Join(errs...)
}

// HandleRetryAnnotationPropagation propagates the retry annotation from parent to owned children.
func HandleRetryAnnotationPropagation(ctx context.Context, c client.Client, parentObj client.Object, ownedObjects client.ObjectList) error {
	log := ctrl.LoggerFrom(ctx)
	var errs []error
	_ = apimeta.EachListItem(ownedObjects, func(obj runtime.Object) error {
		cObj, ok := obj.(client.Object)
		if !ok {
			errs = append(errs, fmt.Errorf("item in list is not a client.Object: %T", obj))
			return nil
		}
		childObj := cObj.DeepCopyObject().(client.Object)
		if err := c.Get(ctx, client.ObjectKeyFromObject(cObj), childObj); err != nil {
			errs = append(errs, fmt.Errorf("failed to fetch latest child %s: %w", cObj.GetName(), err))
			return nil
		}
		if !childObj.GetDeletionTimestamp().IsZero() {
			return nil
		}
		log.V(1).Info("Child's annotations check", "ChildResource", childObj.GetName())
		opResult, err := controllerutil.CreateOrPatch(ctx, c, childObj, func() error {
			annotations := childObj.GetAnnotations()
			if !ShouldChildRetryReconciliation(parentObj) && IsChildRetryThroughSets(childObj) && annotations != nil {
				delete(annotations, constants.OperationAnnotation)
				childObj.SetAnnotations(annotations)
			}
			v := reflect.ValueOf(childObj).Elem()
			statusField := v.FieldByName("Status")
			if statusField.IsValid() {
				conditionsField := statusField.FieldByName("Conditions")
				if conditionsField.IsValid() {
					conditions, ok := reflect.TypeAssert[[]metav1.Condition](conditionsField)
					if ok {
						acc := conditionutils.NewAccessor(conditionutils.AccessorOptions{})
						retriedCondition, err := GetCondition(acc, conditions, constants.ConditionRetryOfFailedResourceIssued)
						if err == nil && retriedCondition != nil &&
							retriedCondition.Status == metav1.ConditionTrue &&
							retriedCondition.Message == constants.OperationAnnotationRetryPropagated {
							return nil
						}
					}
				}
			}
			_, operationAnnotationChildFound := annotations[constants.OperationAnnotation]
			if ShouldChildRetryReconciliation(parentObj) && !operationAnnotationChildFound {
				if annotations == nil {
					annotations = make(map[string]string)
				}
				annotations[constants.OperationAnnotation] = constants.OperationAnnotationRetryPropagated
				childObj.SetAnnotations(annotations)
			}
			return nil
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to propagate retry annotation to child %s: %w", childObj.GetName(), err))
		}
		if opResult != controllerutil.OperationResultNone {
			log.V(1).Info("Patched Child's annotations to retry annotation", "ChildResource", childObj.GetName(), "Operation", opResult)
		}
		return nil
	})
	return errors.Join(errs...)
}

// --- Event filter helpers ---

// EnqueueFromChildObjUpdatesExceptAnnotation filters update events, suppressing pure annotation-propagation changes.
func EnqueueFromChildObjUpdatesExceptAnnotation(e event.UpdateEvent) bool {
	isNil := func(arg any) bool {
		if v := reflect.ValueOf(arg); !v.IsValid() || ((v.Kind() == reflect.Pointer ||
			v.Kind() == reflect.Interface ||
			v.Kind() == reflect.Slice ||
			v.Kind() == reflect.Map ||
			v.Kind() == reflect.Chan ||
			v.Kind() == reflect.Func) && v.IsNil()) {
			return true
		}
		return false
	}
	if isNil(e.ObjectOld) || isNil(e.ObjectNew) {
		return false
	}
	newAnnotations := IsChildIgnoredThroughSets(e.ObjectNew)
	oldAnnotations := IsChildIgnoredThroughSets(e.ObjectOld)
	if newAnnotations != oldAnnotations {
		oldCopy := e.ObjectOld.DeepCopyObject().(client.Object)
		oldCopy.SetAnnotations(e.ObjectNew.GetAnnotations())
		return !reflect.DeepEqual(oldCopy, e.ObjectNew)
	}
	return true
}

// LabelChangeOrAnyFieldChangeInObject returns true if labels or any of the provided fields changed.
func LabelChangeOrAnyFieldChangeInObject(e event.UpdateEvent, oldFields, newFields []any) bool {
	isNil := func(arg any) bool {
		if v := reflect.ValueOf(arg); !v.IsValid() || ((v.Kind() == reflect.Pointer ||
			v.Kind() == reflect.Interface ||
			v.Kind() == reflect.Slice ||
			v.Kind() == reflect.Map ||
			v.Kind() == reflect.Chan ||
			v.Kind() == reflect.Func) && v.IsNil()) {
			return true
		}
		return false
	}
	if isNil(e.ObjectOld) || isNil(e.ObjectNew) {
		return false
	}
	if !maps.Equal(e.ObjectNew.GetLabels(), e.ObjectOld.GetLabels()) {
		return true
	}
	if len(oldFields) != len(newFields) {
		return true
	}
	for i := range oldFields {
		if !reflect.DeepEqual(oldFields[i], newFields[i]) {
			return true
		}
	}
	return false
}

// bmcNativeConditionReset and bmcNativeReasonResetComplete mirror metal-operator's own
// (unexported, not importable across the module boundary) internal/controller/conditions.go
// constants ConditionReset = "Reset" and ReasonResetComplete = "ResetComplete". metal-operator's
// BMCReconciler sets these on the BMC object itself once a reset it issued has completed. Keep
// these string literals in sync by hand if metal-operator ever renames them.
const (
	bmcNativeConditionReset      = "Reset"
	bmcNativeReasonResetComplete = "ResetComplete"
)

// IsBMCRecentlyReset reports whether bmcObj's own "Reset" condition (maintained by
// metal-operator's BMCReconciler) shows a reset completed within the given interval.
func IsBMCRecentlyReset(bmcObj *metalv1alpha1.BMC, within time.Duration) bool {
	if bmcObj == nil || within <= 0 {
		return false
	}
	cond := apimeta.FindStatusCondition(bmcObj.Status.Conditions, bmcNativeConditionReset)
	if cond == nil || cond.Status != metav1.ConditionFalse || cond.Reason != bmcNativeReasonResetComplete {
		return false
	}
	return time.Since(cond.LastTransitionTime.Time) < within
}

// BMCWasRecentlyReset is IsBMCRecentlyReset for callers that only have a Server, not the BMC
// object itself.
func BMCWasRecentlyReset(ctx context.Context, kClient client.Client, server *metalv1alpha1.Server, within time.Duration) (bool, error) {
	if within <= 0 || server.Spec.BMCRef == nil {
		return false, nil
	}
	bmcObj := &metalv1alpha1.BMC{}
	if err := kClient.Get(ctx, client.ObjectKey{Name: server.Spec.BMCRef.Name}, bmcObj); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to get referred server's BMC: %w", err)
	}
	return IsBMCRecentlyReset(bmcObj, within), nil
}

// BMCHasPendingReset reports whether bmcObj currently carries the GracefulRestartBMC operation
// annotation, meaning a previously issued reset has not yet been picked up/cleared by
// metal-operator's BMCReconciler.
func BMCHasPendingReset(bmcObj *metalv1alpha1.BMC) bool {
	annotations := bmcObj.GetAnnotations()
	if annotations == nil {
		return false
	}
	op, ok := annotations[metalv1alpha1.OperationAnnotation]
	return ok && op == metalv1alpha1.GracefulRestartBMC
}

// IsBMCResetAnnotationPending is BMCHasPendingReset for callers that only have a Server, not the
// BMC object itself.
func IsBMCResetAnnotationPending(ctx context.Context, kClient client.Client, server *metalv1alpha1.Server) (bool, error) {
	if server.Spec.BMCRef == nil {
		return false, nil
	}
	bmcObj := &metalv1alpha1.BMC{}
	if err := kClient.Get(ctx, client.ObjectKey{Name: server.Spec.BMCRef.Name}, bmcObj); err != nil {
		return false, err
	}
	return BMCHasPendingReset(bmcObj), nil
}

// IssueBMCReset requests a graceful reset of bmcObj by setting the GracefulRestartBMC operation
// annotation on it.
func IssueBMCReset(ctx context.Context, kClient client.Client, bmcObj *metalv1alpha1.BMC) error {
	log := ctrl.LoggerFrom(ctx)
	annotations := bmcObj.GetAnnotations()
	if annotations != nil {
		if op, ok := annotations[metalv1alpha1.OperationAnnotation]; ok {
			if op == metalv1alpha1.GracefulRestartBMC {
				log.V(1).Info("Waiting for BMC reset as annotation on BMC object is set")
				return nil
			}
			return fmt.Errorf("unknown annotation on BMC object for operation annotation %v", op)
		}
	}
	log.V(1).Info("Setting annotation on BMC resource to trigger with BMC reset")
	bmcBase := bmcObj.DeepCopy()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[metalv1alpha1.OperationAnnotation] = metalv1alpha1.GracefulRestartBMC
	bmcObj.SetAnnotations(annotations)
	return kClient.Patch(ctx, bmcObj, client.MergeFrom(bmcBase))
}

// BMCResetArgs bundles the callbacks HandleBMCReset needs from each caller to drive the shared
// "InProgress"-state BMC stabilization reset state machine, abstracting over how each controller
// stores its own status/conditions and how it reaches the BMC (directly, or via a Server).
type BMCResetArgs struct {
	// Condition is the CR's own tracked condition for this reset (e.g. ConditionResetIssued or
	// ConditionRetryResetIssued), already fetched via GetCondition. Mutated in place.
	Condition *metav1.Condition
	// Reason is the Reason string used to mark the condition "issued" and, later, "complete"
	// (normally constants.ReasonResetIssued).
	Reason string
	// RecentlyReset reports whether the target BMC was already reset within the configured
	// cooldown window, letting the caller skip issuing a redundant reset.
	RecentlyReset func(ctx context.Context) (bool, error)
	// IssueReset performs the actual reset action against the target BMC/server. Returning nil
	// means the reset was issued (or one was already pending); any waiting/confirmation happens
	// via ResetPending on subsequent calls.
	IssueReset func(ctx context.Context) error
	// ResetPending reports whether a previously-issued reset is still in progress.
	ResetPending func(ctx context.Context) (bool, error)
	// Persist patches the owning CR's status so the updated Condition is saved.
	Persist func(ctx context.Context) error
	// SkipRecentlyResetCheck skips the RecentlyReset check to force a reset
	SkipRecentlyResetCheck bool
}

// HandleBMCReset implements the shared "reset the BMC once, then wait for it to settle" state
// machine. It returns (ready, err):
//   - ready is true if args.Condition was already ConditionTrue when this call started (i.e. a
//     previous call already completed the reset and this is a later reconcile catching up), or
//     if the RecentlyReset check found a reset was already done recently and none is needed now -
//     in both cases the caller may proceed immediately within the same reconcile.
//   - ready is false on every other call, including the exact call that marks args.Condition
//     ConditionTrue upon confirming the reset has settled: the caller must wait for the next
//     reconcile (triggered by the status patch) before treating the reset as complete, instead of
//     bundling the "complete" condition update with any of its own subsequent steps.
//   - err is non-nil only on unexpected failures, never to signal "still waiting".
func HandleBMCReset(ctx context.Context, conditions *conditionutils.Accessor, args BMCResetArgs) (bool, error) {
	if args.Condition.Status == metav1.ConditionTrue {
		return true, nil
	}

	if args.Condition.Reason != args.Reason {
		if !args.SkipRecentlyResetCheck {
			recentlyReset, err := args.RecentlyReset(ctx)
			if err != nil {
				return false, fmt.Errorf("failed to check last BMC reset time: %w", err)
			}
			if recentlyReset {
				if err := conditions.Update(
					args.Condition,
					conditionutils.UpdateStatus(corev1.ConditionTrue),
					conditionutils.UpdateReason(args.Reason),
					conditionutils.UpdateMessage("Skipped BMC reset: BMC was already reset recently"),
				); err != nil {
					return false, fmt.Errorf("failed to update reset BMC condition: %w", err)
				}
				return true, args.Persist(ctx)
			}
		}
		if err := args.IssueReset(ctx); err != nil {
			return false, err
		}
		if err := conditions.Update(
			args.Condition,
			conditionutils.UpdateStatus(corev1.ConditionFalse),
			conditionutils.UpdateReason(args.Reason),
			conditionutils.UpdateMessage("Issued BMC reset to stabilize BMC of the server"),
		); err != nil {
			return false, fmt.Errorf("failed to update reset BMC condition: %w", err)
		}
		return false, args.Persist(ctx)
	}

	pending, err := args.ResetPending(ctx)
	if err != nil {
		return false, err
	}
	if pending {
		return false, nil
	}

	if err := conditions.Update(
		args.Condition,
		conditionutils.UpdateStatus(corev1.ConditionTrue),
		conditionutils.UpdateReason(args.Reason),
		conditionutils.UpdateMessage("BMC reset to stabilize BMC of the server is completed"),
	); err != nil {
		return false, fmt.Errorf("failed to update reset BMC condition: %w", err)
	}
	return false, args.Persist(ctx)
}

// ResetBMCOfServer triggers a graceful BMC reset for the server.
func ResetBMCOfServer(ctx context.Context, kClient client.Client, server *metalv1alpha1.Server, bmcClient bmc.BMC) error {
	log := ctrl.LoggerFrom(ctx)
	if server.Spec.BMCRef != nil {
		key := client.ObjectKey{Name: server.Spec.BMCRef.Name}
		bmcObj := &metalv1alpha1.BMC{}
		if err := kClient.Get(ctx, key, bmcObj); err != nil {
			log.Error(err, "Failed to get referred server's Manager")
			return err
		}
		return IssueBMCReset(ctx, kClient, bmcObj)
	} else if server.Spec.BMC != nil {
		bmcManager, err := bmcClient.GetManager("")
		if err != nil {
			return fmt.Errorf("failed to get manager to reset BMC: %w", err)
		}
		log.V(1).Info("Resetting through redfish to stabilize BMC of the server")
		if err := bmcClient.ResetManager(ctx, bmcManager.UUID, schemas.GracefulRestartResetType); err != nil {
			return fmt.Errorf("failed to get manager to reset BMC: %w", err)
		}
		return nil
	}
	return fmt.Errorf("no BMC reference or inline BMC details found in server spec to reset BMC")
}

// ServerRetryResetArgs bundles the callbacks ResetServerForRetry needs from each caller to drive
// the shared "power off, confirm, power on, confirm" server stabilization state machine used
// before retrying a Failed resource, abstracting over how each controller stores its own
// status/conditions.
type ServerRetryResetArgs struct {
	// Conditions is used to fetch/update the CR's own tracked conditions.
	Conditions *conditionutils.Accessor
	// StatusConditions is the CR's own tracked conditions slice.
	StatusConditions []metav1.Condition
	// BMCClient is used to query/change the power state of Server.
	BMCClient bmc.BMC
	// Server is the server being power-cycled before the retry.
	Server *metalv1alpha1.Server
	// Persist patches the owning CR's status so the given, just-updated condition is saved.
	Persist func(ctx context.Context, condition *metav1.Condition) error

	// PowerOffTimeout bounds how long to wait for a graceful power-off to complete before
	// escalating to a forced power-off, measured since the graceful power-off was issued.
	// A value <= 0 disables escalation (wait indefinitely for the graceful power-off, subject
	// only to GiveUpTimeout below).
	PowerOffTimeout time.Duration
	// GiveUpTimeout bounds how long each stage of the power-cycle (waiting for the power-off to
	// complete, and separately, waiting for the power-on to complete) may individually take,
	// measured since that stage was first issued (or recorded as already-off, for the power-off
	// stage), before giving up and calling GiveUp instead of continuing to wait/retry. Each stage
	// gets its own independent budget, so time spent stuck in one does not eat into the other's.
	// A value <= 0 disables the give-up check (wait indefinitely).
	GiveUpTimeout time.Duration
	// GiveUp is called (repeatedly, idempotently) once GiveUpTimeout is exceeded. Required if GiveUpTimeout > 0.
	GiveUp func(ctx context.Context) error
}

// HandleServerPowerCycle power-cycles args.Server (power off, confirm off, power on, confirm on)
// before a Failed resource is retried, tracking progress via the shared
// ConditionServerResetPowerOffIssued/ConditionServerResetPowerOnIssued/ConditionServerResetIssued
// conditions.
func HandleServerPowerCycle(ctx context.Context, args ServerRetryResetArgs) (bool, error) {
	log := ctrl.LoggerFrom(ctx)

	resetCondition, err := GetCondition(args.Conditions, args.StatusConditions, constants.ConditionServerResetIssued)
	if err != nil {
		return false, fmt.Errorf("failed to get condition for server reset before retry: %w", err)
	}
	if resetCondition.Status == metav1.ConditionTrue {
		return true, nil
	}

	powerOffCondition, err := GetCondition(args.Conditions, args.StatusConditions, constants.ConditionServerResetPowerOffIssued)
	if err != nil {
		return false, fmt.Errorf("failed to get condition for server power-off before retry: %w", err)
	}
	powerOnCondition, err := GetCondition(args.Conditions, args.StatusConditions, constants.ConditionServerResetPowerOnIssued)
	if err != nil {
		return false, fmt.Errorf("failed to get condition for server power-on before retry: %w", err)
	}

	// giveUpIfStuck reports whether the current stage (identified by stageCondition, one of
	// powerOffCondition/powerOnCondition) has been stuck for longer than args.GiveUpTimeout since
	// that stage was first issued, and if so, invokes args.GiveUp and returns an error so the
	// caller backs off exponentially.
	giveUpIfStuck := func(stageCondition *metav1.Condition) error {
		if args.GiveUpTimeout <= 0 || stageCondition.Reason != constants.ReasonResetIssued {
			return nil
		}
		if time.Since(stageCondition.LastTransitionTime.Time) < args.GiveUpTimeout {
			return nil
		}
		log.V(1).Info("Server did not recover within the give-up timeout, giving up on retry",
			"Server", args.Server.Name, "GiveUpTimeout", args.GiveUpTimeout)
		if err := args.GiveUp(ctx); err != nil {
			return fmt.Errorf("failed to give up on server power-cycle before retry: %w", err)
		}
		if err := args.Conditions.Update(
			resetCondition,
			conditionutils.UpdateStatus(corev1.ConditionFalse),
			conditionutils.UpdateReason(constants.ReasonServerResetGaveUp),
			conditionutils.UpdateMessage(fmt.Sprintf(
				"Server did not recover within %v, gave up retrying automatically", args.GiveUpTimeout)),
		); err != nil {
			return fmt.Errorf("failed to update server reset condition after giving up: %w", err)
		}
		if err := args.Persist(ctx, resetCondition); err != nil {
			return err
		}
		return fmt.Errorf("server did not recover within %v, gave up retrying", args.GiveUpTimeout)
	}

	if powerOffCondition.Status != metav1.ConditionTrue {
		return handleServerPowerOffStage(ctx, args, powerOffCondition, giveUpIfStuck)
	}

	return handleServerPowerOnStage(ctx, args, resetCondition, powerOnCondition, giveUpIfStuck)
}

// handleServerPowerOffStage drives the power-off half of HandleServerPowerCycle's state machine:
// issuing the graceful power-off, escalating to a forced power-off after args.PowerOffTimeout,
// giving up via giveUpIfStuck, and marking powerOffCondition complete once confirmed off.
func handleServerPowerOffStage(
	ctx context.Context,
	args ServerRetryResetArgs,
	powerOffCondition *metav1.Condition,
	giveUpIfStuck func(*metav1.Condition) error,
) (bool, error) {
	log := ctrl.LoggerFrom(ctx)

	if powerOffCondition.Reason != constants.ReasonResetIssued {
		currentPowerState, err := GetServerPowerState(ctx, args.BMCClient, args.Server)
		if err != nil {
			return false, fmt.Errorf("failed to check server power state: %w", err)
		}
		if currentPowerState == metalv1alpha1.ServerOffPowerState {
			// already off, nothing to power off - mark this stage done and proceed to PowerOn below.
			if err := args.Conditions.Update(
				powerOffCondition,
				conditionutils.UpdateStatus(corev1.ConditionTrue),
				conditionutils.UpdateReason(constants.ReasonResetIssued),
				conditionutils.UpdateMessage("Server is already off, no power-off needed before retry"),
			); err != nil {
				return false, fmt.Errorf("failed to update server power-off condition: %w", err)
			}
			return false, args.Persist(ctx, powerOffCondition)
		}
		if err := args.BMCClient.PowerOff(ctx, args.Server.Spec.SystemURI); err != nil {
			return false, fmt.Errorf("failed to power off server before retry: %w", err)
		}
		if err := args.Conditions.Update(
			powerOffCondition,
			conditionutils.UpdateStatus(corev1.ConditionFalse),
			conditionutils.UpdateReason(constants.ReasonResetIssued),
			conditionutils.UpdateMessage("Issued power-off request to the server before retrying"),
		); err != nil {
			return false, fmt.Errorf("failed to update server power-off condition: %w", err)
		}
		return false, args.Persist(ctx, powerOffCondition)
	}

	inOffState, err := IsServerInPowerState(ctx, args.BMCClient, args.Server, metalv1alpha1.ServerOffPowerState)
	if err != nil {
		return false, fmt.Errorf("failed to check server power state: %w", err)
	}
	if !inOffState {
		// Still stuck - escalate to a forced power-off, then give up, in order of severity.
		if args.PowerOffTimeout > 0 && time.Since(powerOffCondition.LastTransitionTime.Time) > args.PowerOffTimeout {
			forceOffCondition, err := GetCondition(args.Conditions, args.StatusConditions, constants.ConditionServerResetForcePowerOffIssued)
			if err != nil {
				return false, fmt.Errorf("failed to get condition for forced server power-off before retry: %w", err)
			}
			if forceOffCondition.Status != metav1.ConditionTrue {
				log.V(1).Info("Graceful power-off did not complete in time, escalating to forced power-off",
					"Server", args.Server.Name, "PowerOffTimeout", args.PowerOffTimeout)
				if err := args.BMCClient.ForcePowerOff(ctx, args.Server.Spec.SystemURI); err != nil {
					return false, fmt.Errorf("failed to force power off server before retry: %w", err)
				}
				if err := args.Conditions.Update(
					forceOffCondition,
					conditionutils.UpdateStatus(corev1.ConditionTrue),
					conditionutils.UpdateReason(constants.ReasonForcePowerOffIssued),
					conditionutils.UpdateMessage(fmt.Sprintf(
						"Graceful power-off did not complete within %v, escalated to a forced power-off", args.PowerOffTimeout)),
				); err != nil {
					return false, fmt.Errorf("failed to update forced server power-off condition: %w", err)
				}
				return false, args.Persist(ctx, forceOffCondition)
			}
		}
		if err := giveUpIfStuck(powerOffCondition); err != nil {
			return false, err
		}
		log.V(1).Info("Waiting for server to power off before retrying", "Server", args.Server.Name)
		return false, nil
	}
	if err := args.Conditions.Update(
		powerOffCondition,
		conditionutils.UpdateStatus(corev1.ConditionTrue),
		conditionutils.UpdateReason(constants.ReasonResetIssued),
		conditionutils.UpdateMessage("Server confirmed powered off before retry"),
	); err != nil {
		return false, fmt.Errorf("failed to update server power-off condition: %w", err)
	}
	return false, args.Persist(ctx, powerOffCondition)
}

// handleServerPowerOnStage drives the power-on half of HandleServerPowerCycle's state machine:
// issuing the power-on, giving up via giveUpIfStuck, and marking powerOnCondition/resetCondition
// complete once confirmed on.
func handleServerPowerOnStage(
	ctx context.Context,
	args ServerRetryResetArgs,
	resetCondition, powerOnCondition *metav1.Condition,
	giveUpIfStuck func(*metav1.Condition) error,
) (bool, error) {
	log := ctrl.LoggerFrom(ctx)

	if powerOnCondition.Reason != constants.ReasonResetIssued {
		if err := args.BMCClient.PowerOn(ctx, args.Server.Spec.SystemURI); err != nil {
			return false, fmt.Errorf("failed to power on server before retry: %w", err)
		}
		if err := args.Conditions.Update(
			powerOnCondition,
			conditionutils.UpdateStatus(corev1.ConditionFalse),
			conditionutils.UpdateReason(constants.ReasonResetIssued),
			conditionutils.UpdateMessage("Issued power-on request to the server before retrying"),
		); err != nil {
			return false, fmt.Errorf("failed to update server power-on condition: %w", err)
		}
		return false, args.Persist(ctx, powerOnCondition)
	}

	inOnState, err := IsServerInPowerState(ctx, args.BMCClient, args.Server, metalv1alpha1.ServerOnPowerState)
	if err != nil {
		return false, fmt.Errorf("failed to check server power state: %w", err)
	}
	if !inOnState {
		if err := giveUpIfStuck(powerOnCondition); err != nil {
			return false, err
		}
		log.V(1).Info("Waiting for server to power back on before retrying", "Server", args.Server.Name)
		return false, nil
	}

	if err := args.Conditions.Update(
		powerOnCondition,
		conditionutils.UpdateStatus(corev1.ConditionTrue),
		conditionutils.UpdateReason(constants.ReasonResetIssued),
		conditionutils.UpdateMessage("Server confirmed powered back on before retry"),
	); err != nil {
		return false, fmt.Errorf("failed to update server power-on condition: %w", err)
	}
	if err := args.Persist(ctx, powerOnCondition); err != nil {
		return false, err
	}

	if err := args.Conditions.Update(
		resetCondition,
		conditionutils.UpdateStatus(corev1.ConditionTrue),
		conditionutils.UpdateReason(constants.ReasonResetIssued),
		conditionutils.UpdateMessage("Server reset completed before retrying"),
	); err != nil {
		return false, fmt.Errorf("failed to update server reset condition: %w", err)
	}
	return false, args.Persist(ctx, resetCondition)
}

// --- Image credential helpers ---

// GetImageCredentialsForSecretRef fetches username/password from a SecretReference.
func GetImageCredentialsForSecretRef(ctx context.Context, c client.Client, secretRef *corev1.SecretReference) (string, string, error) {
	if secretRef == nil {
		return "", "", fmt.Errorf("got nil secretRef")
	}
	secret := &corev1.Secret{}
	if err := c.Get(ctx, client.ObjectKey{Namespace: secretRef.Namespace, Name: secretRef.Name}, secret); err != nil {
		return "", "", err
	}
	username, ok := secret.Data[metalv1alpha1.BMCSecretUsernameKeyName]
	if !ok {
		return "", "", fmt.Errorf("no username found in secret")
	}
	password, ok := secret.Data[metalv1alpha1.BMCSecretPasswordKeyName]
	if !ok {
		return "", "", fmt.Errorf("no password found in secret")
	}
	return string(username), string(password), nil
}

// --- Variable resolution helpers ---

// ResolveVariables resolves the Variables list into a flat key→value map.
func ResolveVariables(
	ctx context.Context,
	c client.Client,
	owner client.Object,
	variables []api.Variable,
) (map[string]string, error) {
	resolved := make(map[string]string, len(variables))
	for _, v := range variables {
		value, err := resolveVariable(ctx, c, owner, v, resolved)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve variable %q: %w", v.Key, err)
		}
		resolved[v.Key] = SubstituteVars(value, resolved)
	}
	return resolved, nil
}

// MergeSettingsFlow merges all SettingsFlow items into a single flat settings map.
// It does not consider the item Priority; if the same setting key is present in
// multiple items, the value from the later item in the list wins.
func MergeSettingsFlow(flow []api.SettingsFlowItem) map[string]string {
	merged := make(map[string]string)
	for _, item := range flow {
		maps.Copy(merged, item.Settings)
	}
	return merged
}

// ApplyVariables substitutes $(KEY) placeholders in settings map keys and values.
// Keys are substituted first and checked for collisions (two distinct original
// keys resolving to the same substituted key), before values are substituted,
// so a collision is reported as an error instead of silently dropping a setting.
func ApplyVariables(settingsMap map[string]string, resolved map[string]string) (map[string]string, error) {
	if len(resolved) == 0 {
		return settingsMap, nil
	}

	// First loop: substitute keys and ensure the result stays collision-free.
	keys := make(map[string]string, len(settingsMap))
	for k := range settingsMap {
		newKey := SubstituteVars(k, resolved)
		if existing, ok := keys[newKey]; ok {
			return nil, fmt.Errorf("variable substitution produced duplicate setting key %q from keys %q and %q", newKey, existing, k)
		}
		keys[newKey] = k
	}

	// Second loop: substitute values now that the key mapping is known-safe.
	out := make(map[string]string, len(settingsMap))
	for newKey, oldKey := range keys {
		out[newKey] = SubstituteVars(settingsMap[oldKey], resolved)
	}
	return out, nil
}

func SubstituteVars(s string, resolved map[string]string) string {
	return expansion.Expand(s, expansion.MappingFuncFor(resolved))
}

func resolveVariable(
	ctx context.Context,
	c client.Client,
	owner client.Object,
	v api.Variable,
	resolved map[string]string,
) (string, error) {
	if v.ValueFrom == nil {
		return "", fmt.Errorf("valueFrom is required")
	}
	switch {
	case v.ValueFrom.FieldRef != nil:
		return ResolveFieldRef(owner, v.ValueFrom.FieldRef.FieldPath)
	case v.ValueFrom.ObjectFieldRef != nil:
		ref := v.ValueFrom.ObjectFieldRef
		name := SubstituteVars(ref.Name, resolved)
		switch ref.Kind {
		case "BMC":
			bmcObj := &metalv1alpha1.BMC{}
			if err := c.Get(ctx, client.ObjectKey{Name: name}, bmcObj); err != nil {
				return "", fmt.Errorf("objectFieldRef kind=BMC: failed to get BMC %q: %w", name, err)
			}
			return ResolveFieldRef(bmcObj, ref.FieldPath)
		default:
			return "", fmt.Errorf("objectFieldRef kind %q is not supported", ref.Kind)
		}
	case v.ValueFrom.SecretKeyRef != nil:
		ref := v.ValueFrom.SecretKeyRef
		name := SubstituteVars(ref.Name, resolved)
		namespace := SubstituteVars(ref.Namespace, resolved)
		key := SubstituteVars(ref.Key, resolved)
		secret := &corev1.Secret{}
		if err := c.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, secret); err != nil {
			return "", fmt.Errorf("failed to get secret %s/%s: %w", namespace, name, err)
		}
		val, ok := secret.Data[key]
		if !ok {
			return "", fmt.Errorf("key %q not found in secret %s/%s", key, namespace, name)
		}
		return string(val), nil
	case v.ValueFrom.ConfigMapKeyRef != nil:
		ref := v.ValueFrom.ConfigMapKeyRef
		name := SubstituteVars(ref.Name, resolved)
		namespace := SubstituteVars(ref.Namespace, resolved)
		key := SubstituteVars(ref.Key, resolved)
		cm := &corev1.ConfigMap{}
		if err := c.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, cm); err != nil {
			return "", fmt.Errorf("failed to get configmap %s/%s: %w", namespace, name, err)
		}
		val, ok := cm.Data[key]
		if !ok {
			return "", fmt.Errorf("key %q not found in configmap %s/%s", key, namespace, name)
		}
		return val, nil
	default:
		return "", fmt.Errorf("no source specified in valueFrom")
	}
}

func ResolveFieldRef(obj client.Object, fieldPath string) (string, error) {
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return "", fmt.Errorf("failed to convert object for fieldRef: %w", err)
	}
	var mapKey string
	path := fieldPath
	if idx := strings.Index(path, "["); idx != -1 {
		if !strings.HasSuffix(path, "]") {
			return "", fmt.Errorf("fieldPath %q has unclosed bracket", fieldPath)
		}
		mapKey = path[idx+1 : len(path)-1]
		path = strings.TrimSuffix(path[:idx], ".")
	}
	parts := strings.Split(path, ".")
	if mapKey != "" {
		intermediate, found, err := unstructured.NestedMap(raw, parts...)
		if err != nil {
			return "", fmt.Errorf("failed to navigate fieldPath %q: %w", fieldPath, err)
		}
		if !found {
			return "", fmt.Errorf("fieldPath %q not found on object %s", fieldPath, obj.GetName())
		}
		val, ok := intermediate[mapKey]
		if !ok {
			return "", fmt.Errorf("fieldPath %q: map key %q not found on object %s", fieldPath, mapKey, obj.GetName())
		}
		s, ok := val.(string)
		if !ok {
			return "", fmt.Errorf("fieldPath %q: map key %q is not a string on object %s", fieldPath, mapKey, obj.GetName())
		}
		return s, nil
	}
	val, found, err := unstructured.NestedString(raw, parts...)
	if err != nil {
		return "", fmt.Errorf("failed to navigate fieldPath %q: %w", fieldPath, err)
	}
	if !found {
		return "", fmt.Errorf("fieldPath %q not found on object %s", fieldPath, obj.GetName())
	}
	return val, nil
}

// SettingKeys returns only the map keys of a SettingsAttributes map for safe logging.
func SettingKeys(attrs schemas.SettingsAttributes) []string {
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	return keys
}

// VersionSetChildName returns a deterministic child resource name for the
// combination of set and target (Server/BMC), so that a re-reconcile on a
// stale cache cannot create duplicate children. Combinations exceeding the
// DNS1123 limit are truncated and suffixed with a stable hash to stay valid.
func VersionSetChildName(setName, targetName string) string {
	name := fmt.Sprintf("%s-%s", setName, targetName)
	if len(name) <= utilvalidation.DNS1123SubdomainMaxLength {
		return name
	}
	// Same scheme as Deployment -> ReplicaSet pod-template-hash:
	// FNV-32a hash, SafeEncodeString keeps it DNS1123-safe.
	hash := fnv.New32a()
	hash.Write([]byte(name)) // hash.Hash never errors on Write
	suffix := "-" + utilrand.SafeEncodeString(fmt.Sprint(hash.Sum32()))
	return name[:utilvalidation.DNS1123SubdomainMaxLength-len(suffix)] + suffix
}

// --- Readiness gate helpers ---

// CompletionConditionReset is the Reason patched onto a Server's CompletionConditionType
// condition (Status=False) by any baseboard/system controller when a previously-Completed object
// detects its live hardware no longer matches its spec ("drift").
const CompletionConditionReset = "DriftDetected"

// GatesSatisfied reports whether every gate in gates is satisfied by conditions. A gate is
// satisfied when a condition of the given Type is present
func GatesSatisfied(conditions []metav1.Condition, gates []metalv1alpha1.ConditionRequirement) (bool, []string) {
	if len(gates) == 0 {
		return true, nil
	}

	var reasons []string
	for _, gate := range gates {
		requiredStatus := gate.RequiredStatus
		if requiredStatus == "" {
			requiredStatus = metav1.ConditionTrue
		}

		condition := apimeta.FindStatusCondition(conditions, gate.Type)
		switch {
		case condition == nil:
			reasons = append(reasons, fmt.Sprintf("condition %q not present", gate.Type))
		case condition.Reason == CompletionConditionReset:
			reasons = append(reasons, fmt.Sprintf("condition %q is a transient drift-reset, not yet settled", gate.Type))
		case condition.Status != requiredStatus:
			reasons = append(reasons, fmt.Sprintf("condition %q is %q, want %q", gate.Type, condition.Status, requiredStatus))
		}
	}

	return len(reasons) == 0, reasons
}

// GatesSatisfiedForServers reports whether gates are satisfied on every server in servers (AND
// semantics across all related servers). It returns the combined, server-prefixed reasons for
// any unsatisfied gates.
func GatesSatisfiedForServers(servers []*metalv1alpha1.Server, gates []metalv1alpha1.ConditionRequirement) (bool, []string) {
	if len(gates) == 0 {
		return true, nil
	}

	if len(servers) == 0 {
		return false, []string{"no related servers found to evaluate readiness gates against"}
	}

	var reasons []string
	for _, server := range servers {
		if satisfied, serverReasons := GatesSatisfied(server.Status.Conditions, gates); !satisfied {
			for _, reason := range serverReasons {
				reasons = append(reasons, fmt.Sprintf("server %q: %s", server.Name, reason))
			}
		}
	}

	return len(reasons) == 0, reasons
}

// PatchServerCondition sets the given condition on the Server's status and patches it. It is a
// no-op returning nil if conditionType is empty. Multiple callers (e.g. sibling firmware/settings
// resources sharing the same Server) may race to patch different condition types on the same
// Server concurrently; this function re-fetches the Server and retries on conflict, using an
// optimistic-lock (resourceVersion-checked) merge patch, so a concurrent writer's unrelated
// condition changes are never silently clobbered by a stale read-modify-write.
func PatchServerCondition(ctx context.Context, c client.Client, server *metalv1alpha1.Server, conditionType string, status metav1.ConditionStatus, reason, message string) error {
	if conditionType == "" {
		return nil
	}

	key := client.ObjectKeyFromObject(server)
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		current := &metalv1alpha1.Server{}
		if err := c.Get(ctx, key, current); err != nil {
			return err
		}
		base := current.DeepCopy()
		apimeta.SetStatusCondition(&current.Status.Conditions, metav1.Condition{
			Type:               conditionType,
			Status:             status,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: current.Generation,
		})
		if err := c.Status().Patch(ctx, current, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
			return err
		}
		current.DeepCopyInto(server)
		return nil
	})
	if err != nil {
		return fmt.Errorf("patching server %s condition %q: %w", server.Name, conditionType, err)
	}
	return nil
}

// RemoveServerCondition removes the given condition type from the Server's status and patches
// it, if present. It is a no-op returning nil if conditionType is empty, the server no longer
// exists, or the condition is already absent.
func RemoveServerCondition(ctx context.Context, c client.Client, server *metalv1alpha1.Server, conditionType string) error {
	if conditionType == "" {
		return nil
	}

	key := client.ObjectKeyFromObject(server)
	err := retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		current := &metalv1alpha1.Server{}
		if err := c.Get(ctx, key, current); err != nil {
			return client.IgnoreNotFound(err)
		}
		if apimeta.FindStatusCondition(current.Status.Conditions, conditionType) == nil {
			return nil
		}
		base := current.DeepCopy()
		apimeta.RemoveStatusCondition(&current.Status.Conditions, conditionType)
		if err := c.Status().Patch(ctx, current, client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
			return err
		}
		current.DeepCopyInto(server)
		return nil
	})
	if err != nil {
		return fmt.Errorf("removing server %s condition %q: %w", server.Name, conditionType, err)
	}
	return nil
}
