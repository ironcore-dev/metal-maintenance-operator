// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// Package utils provides shared helper functions for bmcmaintenance and servermaintenance controllers.
// It consolidates utilities that were previously scattered across metal-operator's internal packages,
// which are not importable from outside that module.
package utils

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"reflect"
	"slices"
	"strings"

	"github.com/ironcore-dev/controller-utils/conditionutils"
	"github.com/ironcore-dev/metal-maintenance-operator/third_party/expansion"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/ironcore-dev/metal-operator/bmc"
	"github.com/stmcginnis/gofish/schemas"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
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

// BMCUnAvailableError is returned when the BMC is not in an available state.
type BMCUnAvailableError struct {
	Message string
}

func (e BMCUnAvailableError) Error() string {
	return e.Message
}

// BMCClientOptions controls optional behaviour of BMC client creation.
type BMCClientOptions int

const (
	BMCConnectivityCheckOption BMCClientOptions = 1
)

// CreateBMCClientOption is a functional option for BMC client creation.
type CreateBMCClientOption func(*createBMCClientConfig)

type createBMCClientConfig struct {
	registryURL string
}

// WithRegistryURL configures the BMC client to use a registry URL after SetBootOverride.
func WithRegistryURL(url string) CreateBMCClientOption {
	return func(c *createBMCClientConfig) {
		c.registryURL = url
	}
}

const (
	bmcSecretUsernameKey = "username"
	bmcSecretPasswordKey = "password"
)

// --- ServerMaintenance helpers ---

// IsAnyServerMaintenanceActive returns true if any referenced ServerMaintenance is in InMaintenance state.
func IsAnyServerMaintenanceActive(ctx context.Context, c client.Client, refs []metalv1alpha1.ObjectReference) (bool, error) {
	for _, ref := range refs {
		sm := &metalv1alpha1.ServerMaintenance{}
		if err := c.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: ref.Namespace}, sm); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return false, fmt.Errorf("failed to get ServerMaintenance %s/%s: %w", ref.Namespace, ref.Name, err)
		}
		if !sm.DeletionTimestamp.IsZero() {
			continue
		}
		if sm.Status.State == metalv1alpha1.ServerMaintenanceStateInMaintenance {
			return true, nil
		}
	}
	return false, nil
}

// GetServerMaintenanceForObjectReference fetches a ServerMaintenance by ObjectReference.
func GetServerMaintenanceForObjectReference(ctx context.Context, c client.Client, ref *metalv1alpha1.ObjectReference) (*metalv1alpha1.ServerMaintenance, error) {
	if ref == nil {
		return nil, fmt.Errorf("got nil reference")
	}
	maintenance := &metalv1alpha1.ServerMaintenance{}
	if err := c.Get(ctx, client.ObjectKey{Name: ref.Name, Namespace: ref.Namespace}, maintenance); err != nil {
		return nil, fmt.Errorf("failed to get ServerMaintenance: %w", err)
	}
	return maintenance, nil
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
	val, found := obj.GetAnnotations()[metalv1alpha1.OperationAnnotation]
	if !found {
		return false
	}
	return slices.Contains([]string{
		metalv1alpha1.OperationAnnotationIgnore,
		metalv1alpha1.OperationAnnotationIgnoreChildAndSelf,
		metalv1alpha1.OperationAnnotationIgnorePropagated,
	}, val)
}

// ShouldChildIgnoreReconciliation checks if the parent's annotation requests child ignore.
func ShouldChildIgnoreReconciliation(parentObj client.Object) bool {
	val, found := parentObj.GetAnnotations()[metalv1alpha1.OperationAnnotation]
	if !found {
		return false
	}
	return val == metalv1alpha1.OperationAnnotationIgnoreChild || val == metalv1alpha1.OperationAnnotationIgnoreChildAndSelf
}

// IsChildIgnoredThroughSets checks if the child carries a propagated ignore annotation.
func IsChildIgnoredThroughSets(childObj client.Object) bool {
	annotations := childObj.GetAnnotations()
	valChildIgnore, found := annotations[metalv1alpha1.OperationAnnotation]
	if !found {
		return false
	}
	return valChildIgnore == metalv1alpha1.OperationAnnotationIgnorePropagated
}

// ShouldRetryReconciliation checks if the object has a retry-failed annotation.
func ShouldRetryReconciliation(obj client.Object) bool {
	val, found := obj.GetAnnotations()[metalv1alpha1.OperationAnnotation]
	if !found {
		return false
	}
	return val == metalv1alpha1.OperationAnnotationRetryFailed || val == metalv1alpha1.OperationAnnotationRetryFailedPropagated
}

// ShouldChildRetryReconciliation checks if the parent's annotation requests child retry.
func ShouldChildRetryReconciliation(parentObj client.Object) bool {
	val, found := parentObj.GetAnnotations()[metalv1alpha1.OperationAnnotation]
	if !found {
		return false
	}
	return val == metalv1alpha1.OperationAnnotationRetryChild || val == metalv1alpha1.OperationAnnotationRetryChildAndSelf
}

// IsChildRetryThroughSets checks if the child carries a propagated retry annotation.
func IsChildRetryThroughSets(childObj client.Object) bool {
	annotations := childObj.GetAnnotations()
	valChildRetry, found := annotations[metalv1alpha1.OperationAnnotation]
	if !found {
		return false
	}
	return valChildRetry == metalv1alpha1.OperationAnnotationRetryFailedPropagated
}

// --- Annotation propagation helpers ---

// HandleIgnoreAnnotationPropagation propagates the ignore annotation from parent to owned children.
func HandleIgnoreAnnotationPropagation(ctx context.Context, c client.Client, parentObj client.Object, ownedObjects client.ObjectList) error {
	log := ctrl.LoggerFrom(ctx)
	var errs []error
	_ = meta.EachListItem(ownedObjects, func(obj runtime.Object) error {
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
				delete(annotations, metalv1alpha1.OperationAnnotation)
				childObj.SetAnnotations(annotations)
			}
			_, operationAnnotationChildFound := annotations[metalv1alpha1.OperationAnnotation]
			if ShouldChildIgnoreReconciliation(parentObj) && !operationAnnotationChildFound {
				if annotations == nil {
					annotations = make(map[string]string)
				}
				annotations[metalv1alpha1.OperationAnnotation] = metalv1alpha1.OperationAnnotationIgnorePropagated
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
	_ = meta.EachListItem(ownedObjects, func(obj runtime.Object) error {
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
				delete(annotations, metalv1alpha1.OperationAnnotation)
				childObj.SetAnnotations(annotations)
			}
			v := reflect.ValueOf(childObj).Elem()
			statusField := v.FieldByName("Status")
			if statusField.IsValid() {
				conditionsField := statusField.FieldByName("Conditions")
				if conditionsField.IsValid() {
					conditions, ok := conditionsField.Interface().([]metav1.Condition)
					if ok {
						acc := conditionutils.NewAccessor(conditionutils.AccessorOptions{})
						retriedCondition, err := GetCondition(acc, conditions, constants.ConditionRetryOfFailedResourceIssued)
						if err == nil && retriedCondition != nil &&
							retriedCondition.Status == metav1.ConditionTrue &&
							retriedCondition.Message == metalv1alpha1.OperationAnnotationRetryFailedPropagated {
							return nil
						}
					}
				}
			}
			_, operationAnnotationChildFound := annotations[metalv1alpha1.OperationAnnotation]
			if ShouldChildRetryReconciliation(parentObj) && !operationAnnotationChildFound {
				if annotations == nil {
					annotations = make(map[string]string)
				}
				annotations[metalv1alpha1.OperationAnnotation] = metalv1alpha1.OperationAnnotationRetryFailedPropagated
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
		if v := reflect.ValueOf(arg); !v.IsValid() || ((v.Kind() == reflect.Ptr ||
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
		if v := reflect.ValueOf(arg); !v.IsValid() || ((v.Kind() == reflect.Ptr ||
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

// --- BMC client helpers (replaces metal-operator/internal/bmcutils) ---

// GetProtocolScheme returns scheme if set, otherwise defaultScheme.
func GetProtocolScheme(scheme metalv1alpha1.ProtocolScheme, defaultScheme metalv1alpha1.ProtocolScheme) metalv1alpha1.ProtocolScheme {
	if scheme != "" {
		return scheme
	}
	return defaultScheme
}

// GetBMCCredentialsFromSecret extracts username and password from a BMCSecret.
func GetBMCCredentialsFromSecret(secret *metalv1alpha1.BMCSecret) (string, string, error) {
	username, err := getValueFromSecret(secret, bmcSecretUsernameKey)
	if err != nil {
		return "", "", err
	}
	password, err := getValueFromSecret(secret, bmcSecretPasswordKey)
	if err != nil {
		return "", "", err
	}
	return username, password, nil
}

func getValueFromSecret(secret *metalv1alpha1.BMCSecret, key string) (string, error) {
	if secret == nil {
		return "", errors.New("secret cannot be nil")
	}
	if value, ok := secret.Data[key]; ok {
		return string(value), nil
	}
	if valueStr, ok := secret.StringData[key]; ok {
		return valueStr, nil
	}
	return "", fmt.Errorf("cannot find value in BMCSecret '%s' for key '%s' in data nor in stringData", secret.Name, key)
}

// GetBMCFromBMCName fetches a BMC by name.
func GetBMCFromBMCName(ctx context.Context, c client.Client, bmcName string) (*metalv1alpha1.BMC, error) {
	bmcObj := &metalv1alpha1.BMC{}
	if err := c.Get(ctx, client.ObjectKey{Name: bmcName}, bmcObj); err != nil {
		return nil, fmt.Errorf("failed to get bmc %q: %w", bmcName, err)
	}
	return bmcObj, nil
}

// GetBMCCredentialsForBMCSecretName fetches credentials from a named BMCSecret.
func GetBMCCredentialsForBMCSecretName(ctx context.Context, c client.Client, bmcSecretName string) (string, string, error) {
	bmcSecret := &metalv1alpha1.BMCSecret{}
	if err := c.Get(ctx, client.ObjectKey{Name: bmcSecretName}, bmcSecret); err != nil {
		return "", "", fmt.Errorf("failed to get bmc secret: %w", err)
	}
	return GetBMCCredentialsFromSecret(bmcSecret)
}

// GetBMCAddressForBMC returns the IP address of the given BMC.
func GetBMCAddressForBMC(ctx context.Context, c client.Client, bmcObj *metalv1alpha1.BMC) (string, error) {
	if bmcObj.Spec.EndpointRef != nil {
		endpoint := &metalv1alpha1.Endpoint{}
		if err := c.Get(ctx, client.ObjectKey{Name: bmcObj.Spec.EndpointRef.Name}, endpoint); err != nil {
			return "", fmt.Errorf("failed to get Endpoints for BMC: %w", err)
		}
		return endpoint.Spec.IP.String(), nil
	}
	if bmcObj.Spec.Endpoint != nil {
		return bmcObj.Spec.Endpoint.IP.String(), nil
	}
	return "", nil
}

// GetBMCClientForServer creates a BMC client for the server's underlying BMC.
func GetBMCClientForServer(ctx context.Context, c client.Client, server *metalv1alpha1.Server, defaultProtocol metalv1alpha1.ProtocolScheme, skipCertValidation bool, options bmc.Options, opts ...CreateBMCClientOption) (bmc.BMC, error) {
	if server.Spec.BMCRef != nil {
		b := &metalv1alpha1.BMC{}
		if err := c.Get(ctx, client.ObjectKey{Name: server.Spec.BMCRef.Name}, b); err != nil {
			return nil, err
		}
		anyOpts := make([]any, len(opts))
		for i, o := range opts {
			anyOpts[i] = o
		}
		return GetBMCClientFromBMC(ctx, c, b, defaultProtocol, skipCertValidation, options, anyOpts...)
	}
	if server.Spec.BMC != nil {
		bmcSecret := &metalv1alpha1.BMCSecret{}
		if err := c.Get(ctx, client.ObjectKey{Name: server.Spec.BMC.BMCSecretRef.Name}, bmcSecret); err != nil {
			return nil, err
		}
		protocolScheme := GetProtocolScheme(server.Spec.BMC.Protocol.Scheme, defaultProtocol)
		return CreateBMCClient(ctx, c, protocolScheme, server.Spec.BMC.Protocol.Name, server.Spec.BMC.Address, server.Spec.BMC.Protocol.Port, bmcSecret, options, skipCertValidation, opts...)
	}
	return nil, fmt.Errorf("server %s has neither a BMCRef nor a BMC configured", server.Name)
}

// GetBMCClientFromBMC creates a BMC client from a BMC object.
func GetBMCClientFromBMC(ctx context.Context, c client.Client, bmcObj *metalv1alpha1.BMC, defaultProtocol metalv1alpha1.ProtocolScheme, skipCertValidation bool, options bmc.Options, opts ...any) (bmc.BMC, error) {
	var address string
	var bmcClientOpts []BMCClientOptions
	var createOpts []CreateBMCClientOption
	for _, o := range opts {
		switch v := o.(type) {
		case BMCClientOptions:
			bmcClientOpts = append(bmcClientOpts, v)
		case CreateBMCClientOption:
			createOpts = append(createOpts, v)
		}
	}
	if !slices.Contains(bmcClientOpts, BMCConnectivityCheckOption) {
		if bmcObj.Status.State != metalv1alpha1.BMCStateEnabled && bmcObj.Status.State != "" {
			return nil, BMCUnAvailableError{Message: fmt.Sprintf("BMC %s is not in enabled state: current state: %s", bmcObj.Name, bmcObj.Status.State)}
		}
	}
	if bmcObj.Spec.EndpointRef != nil {
		endpoint := &metalv1alpha1.Endpoint{}
		if err := c.Get(ctx, client.ObjectKey{Name: bmcObj.Spec.EndpointRef.Name}, endpoint); err != nil {
			return nil, fmt.Errorf("failed to get Endpoints for BMC: %w", err)
		}
		address = endpoint.Spec.IP.String()
	}
	if bmcObj.Spec.Endpoint != nil {
		address = bmcObj.Spec.Endpoint.IP.String()
	}
	bmcSecret := &metalv1alpha1.BMCSecret{}
	if err := c.Get(ctx, client.ObjectKey{Name: bmcObj.Spec.BMCSecretRef.Name}, bmcSecret); err != nil {
		return nil, fmt.Errorf("failed to get BMC secret: %w", err)
	}
	protocolScheme := GetProtocolScheme(bmcObj.Spec.Protocol.Scheme, defaultProtocol)
	return CreateBMCClient(ctx, c, protocolScheme, bmcObj.Spec.Protocol.Name, address, bmcObj.Spec.Protocol.Port, bmcSecret, options, skipCertValidation, createOpts...)
}

// CreateBMCClient creates a BMC client from explicit connection parameters.
func CreateBMCClient(
	ctx context.Context,
	_ client.Client,
	protocolScheme metalv1alpha1.ProtocolScheme,
	bmcProtocol metalv1alpha1.ProtocolName,
	address string,
	port int32,
	bmcSecret *metalv1alpha1.BMCSecret,
	bmcOptions bmc.Options,
	skipCertValidation bool,
	opts ...CreateBMCClientOption,
) (bmc.BMC, error) {
	cfg := &createBMCClientConfig{}
	for _, o := range opts {
		o(cfg)
	}
	bmcOptions.Endpoint = fmt.Sprintf("%s://%s", protocolScheme, net.JoinHostPort(address, fmt.Sprintf("%d", port)))
	var err error
	bmcOptions.Username, bmcOptions.Password, err = GetBMCCredentialsFromSecret(bmcSecret)
	if err != nil {
		return nil, fmt.Errorf("failed to get credentials from BMC secret: %w", err)
	}
	bmcOptions.InsecureTLS = skipCertValidation
	log := ctrl.LoggerFrom(ctx)
	log.V(1).Info("Creating BMC client", "Protocol", bmcProtocol, "Address", bmcOptions.Endpoint, "Username", bmcOptions.Username)
	switch bmcProtocol {
	case metalv1alpha1.ProtocolRedfish:
		return bmc.NewRedfishBMCClient(ctx, bmcOptions)
	case metalv1alpha1.ProtocolRedfishLocal:
		return bmc.NewRedfishLocalBMCClient(ctx, bmcOptions)
	case metalv1alpha1.ProtocolRedfishWithRegistryPatch:
		return bmc.NewRedfishLocalBMCClientWithRegistry(ctx, bmcOptions, cfg.registryURL)
	default:
		return nil, fmt.Errorf("unsupported BMC protocol %s", bmcProtocol)
	}
}

// GetServerNameFromBMCandIndex derives the server name for a given BMC and index.
func GetServerNameFromBMCandIndex(index int, bmcObj *metalv1alpha1.BMC) string {
	return fmt.Sprintf("%s-%s-%d", bmcObj.Name, "system", index)
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
	variables []metalv1alpha1.Variable,
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

// ApplyVariables substitutes $(KEY) placeholders in settings map values.
func ApplyVariables(settingsMap map[string]string, resolved map[string]string) map[string]string {
	if len(resolved) == 0 {
		return settingsMap
	}
	out := make(map[string]string, len(settingsMap))
	for k, v := range settingsMap {
		out[k] = SubstituteVars(v, resolved)
	}
	return out
}

func SubstituteVars(s string, resolved map[string]string) string {
	return expansion.Expand(s, expansion.MappingFuncFor(resolved))
}

func resolveVariable(
	ctx context.Context,
	c client.Client,
	owner client.Object,
	v metalv1alpha1.Variable,
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
