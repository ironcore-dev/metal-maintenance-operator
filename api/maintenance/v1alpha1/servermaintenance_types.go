// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// ServerMaintenanceNeededLabelKey is a label key used to indicate that a server requires maintenance.
	ServerMaintenanceNeededLabelKey = "metal.ironcore.dev/maintenance-needed"
	// ServerMaintenanceReasonAnnotationKey is an annotation key used to store the reason for a server maintenance.
	ServerMaintenanceReasonAnnotationKey = "metal.ironcore.dev/maintenance-reason"
	// ServerMaintenanceApprovedLabelKey is a label key used to store the approved status for a server maintenance.
	ServerMaintenanceApprovedLabelKey = "metal.ironcore.dev/maintenance-approved"
	// ServerMaintenanceRequestedLabelKey is a label key used to request server maintenance.
	ServerMaintenanceRequestedLabelKey = "metal.ironcore.dev/maintenance-requested"
)

// ServerMaintenanceSpec defines the desired state of a ServerMaintenance.
type ServerMaintenanceSpec struct {
	// Policy specifies the maintenance policy to be enforced on the server.
	// +required
	// +kubebuilder:validation:Enum=OwnerApproval;Enforced
	Policy ServerMaintenancePolicy `json:"policy"`

	// ServerRef is a reference to the server that is to be maintained.
	// +required
	// +kubebuilder:validation:XValidation:rule="self.name != ''",message="serverRef.name must not be empty"
	ServerRef *corev1.LocalObjectReference `json:"serverRef"`

	// LocatorLED specifies the desired state of the server's locator LED during maintenance.
	// When maintenance ends, the locator LED is turned off.
	// +optional
	LocatorLED metalv1alpha1.IndicatorLED `json:"locatorLED,omitempty"`

	// Priority determines ordering when multiple ServerMaintenance resources target the same server.
	// Higher values are processed first. If priorities are equal, older resources are processed first.
	// If omitted, priority is treated as 0.
	// +kubebuilder:default=0
	// +optional
	Priority int32 `json:"priority,omitempty"`
}

// ServerMaintenancePolicy specifies the maintenance policy to be enforced on the server.
type ServerMaintenancePolicy string

const (
	// ServerMaintenancePolicyOwnerApproval specifies that the maintenance policy requires owner approval.
	ServerMaintenancePolicyOwnerApproval ServerMaintenancePolicy = "OwnerApproval"
	// ServerMaintenancePolicyEnforced specifies that the maintenance policy is enforced.
	ServerMaintenancePolicyEnforced ServerMaintenancePolicy = "Enforced"
)

// ServerMaintenanceStatus defines the observed state of a ServerMaintenance.
type ServerMaintenanceStatus struct {
	// State specifies the current state of the server maintenance.
	State ServerMaintenanceState `json:"state,omitempty"`
}

// ServerMaintenanceState specifies the current state of the server maintenance.
type ServerMaintenanceState string

const (
	// ServerMaintenanceStatePending specifies that the server maintenance is pending.
	ServerMaintenanceStatePending ServerMaintenanceState = "Pending"
	// ServerMaintenanceStateInMaintenance specifies that the server is in maintenance.
	ServerMaintenanceStateInMaintenance ServerMaintenanceState = "InMaintenance"
	// ServerMaintenanceStateFailed specifies that the server maintenance has failed.
	ServerMaintenanceStateFailed ServerMaintenanceState = "Failed"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=sm
// +kubebuilder:printcolumn:name="Server",type="string",JSONPath=".spec.serverRef.name"
// +kubebuilder:printcolumn:name="Policy",type="string",JSONPath=`.spec.policy`
// +kubebuilder:printcolumn:name="Reason",type="string",JSONPath=`.metadata.annotations.metal\.ironcore\.dev\/maintenance-reason`
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Priority",type="integer",JSONPath=`.spec.priority`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ServerMaintenance is the Schema for the ServerMaintenance API.
type ServerMaintenance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ServerMaintenanceSpec   `json:"spec,omitempty"`
	Status ServerMaintenanceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ServerMaintenanceList contains a list of ServerMaintenance.
type ServerMaintenanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ServerMaintenance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ServerMaintenance{}, &ServerMaintenanceList{})
}
