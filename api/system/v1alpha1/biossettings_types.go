// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ironcore-dev/metal-maintenance-operator/api"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

// BIOSSettingsTemplate defines the template for BIOS settings to be applied.
type BIOSSettingsTemplate struct {
	api.SettingsTemplate `json:",inline"`
}

// BIOSSettingsSpec defines the desired state of BIOSSettings.
// +kubebuilder:validation:XValidation:rule="size(self.version) > 0",message="version is required"
type BIOSSettingsSpec struct {
	// BIOSSettingsTemplate defines the template for BIOS Settings to be applied on the servers.
	BIOSSettingsTemplate `json:",inline"`

	// ServerMaintenanceRef is a reference to a ServerMaintenance object that BIOSSettings has requested for the referred server.
	// +optional
	ServerMaintenanceRef *metalv1alpha1.ObjectReference `json:"serverMaintenanceRef,omitempty"`

	// ServerRef is a reference to a specific server to apply the BIOS settings on.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="serverRef is immutable"
	// +required
	ServerRef *corev1.LocalObjectReference `json:"serverRef,omitempty"`
}

// BIOSSettingsState specifies the current state of the BIOS Settings update.
type BIOSSettingsState string

const (
	// BIOSSettingsStatePending specifies that the BIOS settings update is waiting.
	BIOSSettingsStatePending BIOSSettingsState = "Pending"
	// BIOSSettingsStateInProgress specifies that the BIOS settings update is in progress.
	BIOSSettingsStateInProgress BIOSSettingsState = "InProgress"
	// BIOSSettingsStateApplied specifies that the BIOS settings have been applied.
	BIOSSettingsStateApplied BIOSSettingsState = "Applied"
	// BIOSSettingsStateFailed specifies that the BIOS settings update has failed.
	BIOSSettingsStateFailed BIOSSettingsState = "Failed"
)

// BIOSSettingsFlowState describes the state of a single settings-flow priority step.
type BIOSSettingsFlowState string

const (
	// BIOSSettingsFlowStatePending specifies that the BIOS settings update for the current priority is pending.
	BIOSSettingsFlowStatePending BIOSSettingsFlowState = "Pending"
	// BIOSSettingsFlowStateInProgress specifies that the BIOS settings update for the current priority is in progress.
	BIOSSettingsFlowStateInProgress BIOSSettingsFlowState = "InProgress"
	// BIOSSettingsFlowStateApplied specifies that the BIOS settings for the current priority have been applied.
	BIOSSettingsFlowStateApplied BIOSSettingsFlowState = "Applied"
	// BIOSSettingsFlowStateFailed specifies that the BIOS settings update has failed.
	BIOSSettingsFlowStateFailed BIOSSettingsFlowState = "Failed"
)

// BIOSSettingsStatus defines the observed state of BIOSSettings.
type BIOSSettingsStatus struct {
	// State represents the current state of the BIOS settings update.
	// +optional
	State BIOSSettingsState `json:"state,omitempty"`

	// FlowState is a list of individual BIOSSettings operation flows.
	FlowState []BIOSSettingsFlowStatus `json:"flowState,omitempty"`

	// LastAppliedTime represents the timestamp when the last setting was successfully applied.
	// +optional
	LastAppliedTime *metav1.Time `json:"lastAppliedTime,omitempty"`

	// FailedAttempts is the number of automatic retry attempts made after failure.
	// +optional
	FailedAttempts int32 `json:"failedAttempts,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represents the latest available observations of the BIOSSettings's current state.
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
}

// BIOSSettingsFlowStatus describes the per-priority-step reconciliation state.
type BIOSSettingsFlowStatus struct {
	// State represents the current state of the BIOS settings update for the current priority.
	// +optional
	State BIOSSettingsFlowState `json:"flowState,omitempty"`

	// Name identifies the current priority settings from the spec.
	// +optional
	Name string `json:"name,omitempty"`

	// Priority identifies the settings priority from the spec.
	// +optional
	Priority int32 `json:"priority"`

	// Conditions represents the latest available observations of the BIOSSettings's current Flowstate.
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`

	// LastAppliedTime represents the timestamp when the last setting was successfully applied.
	// +optional
	LastAppliedTime *metav1.Time `json:"lastAppliedTime,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=bioss
// +kubebuilder:printcolumn:name="BIOSVersion",type=string,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="ServerRef",type=string,JSONPath=`.spec.serverRef.name`
// +kubebuilder:printcolumn:name="ServerMaintenanceRef",type=string,JSONPath=`.spec.serverMaintenanceRef.name`
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="AppliedOn",type=date,JSONPath=`.status.lastAppliedTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// BIOSSettings is the Schema for the biossettings API.
type BIOSSettings struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BIOSSettingsSpec   `json:"spec,omitempty"`
	Status BIOSSettingsStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BIOSSettingsList contains a list of BIOSSettings.
type BIOSSettingsList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BIOSSettings `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BIOSSettings{}, &BIOSSettingsList{})
}
