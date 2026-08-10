// Package api contains shared types used across the maintenance-operator API groups.
// +kubebuilder:object:generate=true
package api

import (
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/stmcginnis/gofish/schemas"
	corev1 "k8s.io/api/core/v1"
)

// ServerMaintenanceRefItem is a reference to a ServerMaintenance object.
type ServerMaintenanceRefItem struct {
	// ServerMaintenanceRef is a reference to a ServerMaintenance object that the BMCSettings has requested for the referred server.
	// +optional
	ServerMaintenanceRef *metalv1alpha1.ObjectReference `json:"serverMaintenanceRef,omitempty"`
}

// RetryPolicy defines the retry behavior on transient failures.
type RetryPolicy struct {
	// MaxAttempts is the maximum number of automatic retry attempts after failure.
	// 0 means no automatic retries. Must be between 0 and 10 inclusive.
	// If not set, the operator-level default is used.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10
	// +optional
	MaxAttempts *int32 `json:"maxAttempts,omitempty"`
}

type SettingsFlowItem struct {
	// Name is the name of the flow item.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=1000
	Name string `json:"name"`

	// Settings contains software (e.g. BIOS, BMC) settings as a map.
	// +optional
	Settings map[string]string `json:"settings,omitempty"`

	// Priority defines the order of applying the settings. Lower numbers have higher priority (i.e. lower numbers are applied first).
	// +required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=2147483645
	Priority int32 `json:"priority"`
}

type UpdatePolicy string

const (
	UpdatePolicyForce UpdatePolicy = "Force"
)

type ImageSpec struct {
	// SecretRef is a reference to the Secret containing the credentials to access the image URI.
	// +optional
	SecretRef *corev1.SecretReference `json:"secretRef,omitempty"`

	// TransferProtocol is the network protocol used to retrieve the image URI.
	// +optional
	TransferProtocol string `json:"transferProtocol,omitempty"`

	// URI is the URI of the software image to install.
	// +required
	URI string `json:"URI"`
}

type Task struct {
	// URI is the URI of the task created by the BMC for the BIOS upgrade.
	// +optional
	URI string `json:"URI,omitempty"`

	// State is the current state of the task.
	// +optional
	State schemas.TaskState `json:"state,omitempty"`

	// Status is the current status of the task.
	// +optional
	Status schemas.Health `json:"status,omitempty"`

	// PercentComplete is the percentage of completion of the task.
	// +optional
	PercentComplete int32 `json:"percentageComplete,omitempty"`
}

type Variable struct {
	// Key is the name of the variable to be used in the BMCSettingsTemplate format.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +required
	Key string `json:"key"`

	// ValueFrom defines a simple single source for the variable value.
	// +required
	ValueFrom *VariableSourceValueFrom `json:"valueFrom"`
}

// +kubebuilder:validation:XValidation:rule="(has(self.fieldRef) ? 1 : 0) + (has(self.objectFieldRef) ? 1 : 0) + (has(self.configMapKeyRef) ? 1 : 0) + (has(self.secretKeyRef) ? 1 : 0) == 1",message="exactly one of fieldRef, objectFieldRef, configMapKeyRef, or secretKeyRef must be provided"
// +kubebuilder:validation:XValidation:rule="!has(self.objectFieldRef) || self.objectFieldRef.kind == 'BMC'",message="objectFieldRef.kind must be 'BMC'"
type VariableSourceValueFrom struct {
	// FieldRef sources the value from a field of the BMCSettings object itself (e.g. spec.bmcRef.name).
	// Only string-typed fields are supported; integer, bool, or map fields will cause a resolution error.
	// +optional
	FieldRef *FieldRefSelector `json:"fieldRef,omitempty"`

	// ObjectFieldRef sources the value from a field of a named related object.
	// The kind must be "BMC". Supports dot-path navigation and bracket notation for map keys
	// containing dots or slashes (e.g. metadata.labels[kubernetes.metal.cloud.sap/nodename]).
	// +optional
	ObjectFieldRef *ObjectFieldRefSelector `json:"objectFieldRef,omitempty"`

	// ConfigMapKeyRef points to a namespaced ConfigMap key.
	// +optional
	ConfigMapKeyRef *NamespacedKeySelector `json:"configMapKeyRef,omitempty"`

	// SecretKeyRef points to a namespaced Secret key.
	// +optional
	SecretKeyRef *NamespacedKeySelector `json:"secretKeyRef,omitempty"`
}

type FieldRefSelector struct {
	// FieldPath is the path of the field on the BMCSettings object to select (e.g. spec.bmcRef.name).
	// Only string-typed fields are supported; integer, bool, or map fields will cause a resolution error.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +required
	FieldPath string `json:"fieldPath"`
}

// ObjectFieldRefSelector selects a field from a named cluster-scoped object.
// It is intentionally generic; the allowed kinds are constrained at the usage site
// via kubebuilder CEL rules on the parent type.
type ObjectFieldRefSelector struct {
	// Kind is the API kind of the object to read the field from (e.g. "BMC").
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +required
	Kind string `json:"kind"`

	// Name is the name of the object to read the field from.
	// Supports $(VAR) substitution using previously resolved variables in declaration order.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Name string `json:"name"`

	// FieldPath is the path of the field to select on the target object.
	// Supports dot-path navigation (e.g. metadata.name) and bracket notation for map
	// keys containing dots or slashes (e.g. metadata.labels[topology.kubernetes.io/region]).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	// +required
	FieldPath string `json:"fieldPath"`
}

type NamespacedKeySelector struct {
	// Name is the referenced object name.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Name string `json:"name"`

	// Namespace is the referenced object namespace.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +required
	Namespace string `json:"namespace"`

	// Key is the key within the referenced object.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Key string `json:"key"`
}
