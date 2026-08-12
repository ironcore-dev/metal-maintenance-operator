// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ironcore-dev/metal-maintenance-operator/api"
)

// BMCSettingsTemplate defines the template for BMC settings to be applied.
// +kubebuilder:validation:XValidation:rule="!has(self.variables) || self.variables.all(v, self.variables.filter(w, w.key == v.key).size() == 1)",message="variable keys must be unique"
type BMCSettingsTemplate struct {
	api.SettingsTemplate `json:",inline"`
}

// BMCSettingsSpec defines the desired state of BMCSettings.
// +kubebuilder:validation:XValidation:rule="size(self.version) > 0",message="version is required"
type BMCSettingsSpec struct {
	BMCSettingsTemplate `json:",inline"`

	// ServerMaintenanceRefs are references to ServerMaintenance objects which are created by the controller for each
	// server that needs to be updated with the BMC settings.
	// +optional
	ServerMaintenanceRefs []api.ServerMaintenanceRefItem `json:"serverMaintenanceRefs,omitempty"`

	// BMCRef is a reference to a specific BMC to apply settings to.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="bmcRef is immutable"
	// +required
	BMCRef *corev1.LocalObjectReference `json:"bmcRef,omitempty"`
}

// BMCSettingsState specifies the current state of the server maintenance.
type BMCSettingsState string

const (
	// BMCSettingsStatePending specifies that the BMC settings update is waiting.
	BMCSettingsStatePending BMCSettingsState = "Pending"
	// BMCSettingsStateInProgress specifies that the BMC settings changes are in progress.
	BMCSettingsStateInProgress BMCSettingsState = "InProgress"
	// BMCSettingsStateApplied specifies that the BMC settings have been applied.
	BMCSettingsStateApplied BMCSettingsState = "Applied"
	// BMCSettingsStateFailed specifies that the BMC settings update has failed.
	BMCSettingsStateFailed BMCSettingsState = "Failed"
)

// BMCSettingsApplyResultEntry holds the URI, ETag, and value hash from the last
// successful apply of a single settings key. Used for ETag-based drift detection.
type BMCSettingsApplyResultEntry struct {
	// URI is the Redfish resource URI from the apply response.
	// For PATCH operations this is the request URI; for POST operations this is
	// the Location header value pointing to the created resource.
	// +optional
	URI string `json:"uri,omitempty"`

	// ETag is the drift-detection token captured after the last successful apply.
	// Either a real ETag returned by the BMC (e.g. W/"20B77DA6") or a SHA-256
	// hash of the GET response body prefixed with "hash:sha256:" for BMCs that
	// do not return ETag headers.
	// +optional
	ETag string `json:"etag,omitempty"`

	// ValueHash is the SHA-256 hash of the effective (resolved) value at apply time.
	// Used to detect desired-state changes from ConfigMap/Secret rotation independent
	// of BMC-side drift.
	// +optional
	ValueHash string `json:"valueHash,omitempty"`
}

// BMCSettingsStatus defines the observed state of BMCSettings.
type BMCSettingsStatus struct {
	// State represents the current state of the BMC configuration task.
	// +optional
	State BMCSettingsState `json:"state,omitempty"`

	// FailedAttempts is the number of automatic retry attempts made after failure.
	// +optional
	FailedAttempts int32 `json:"failedAttempts,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// AppliedETags stores the URI, ETag, and value hash from the last successful
	// apply for each settings key. Used for ETag-based drift detection during
	// verification to avoid unnecessary re-applies and to reliably detect
	// write-only field drift (e.g. passwords) and POST-created resource drift
	// (e.g. certificates).
	// +optional
	AppliedETags map[string]BMCSettingsApplyResultEntry `json:"appliedETags,omitempty"`

	// Conditions represents the latest available observations of the BMC Settings Resource state.
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="BMCVersion",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="BMCRef",type=string,JSONPath=`.spec.bmcRef.name`

// BMCSettings is the Schema for the BMCSettings API.
type BMCSettings struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BMCSettingsSpec   `json:"spec,omitempty"`
	Status BMCSettingsStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BMCSettingsList contains a list of BMCSettings.
type BMCSettingsList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BMCSettings `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BMCSettings{}, &BMCSettingsList{})
}
