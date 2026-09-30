// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BMCUserSetSpec defines the desired state of BMCUserSet.
type BMCUserSetSpec struct {
	// ServerSelector selects the Server resources whose BMCs should receive the user.
	// +required
	ServerSelector metav1.LabelSelector `json:"serverSelector"`

	// Template describes the BMCUser to create on each matched server.
	// The fields mirror BMCUserSpec, except BMCRef and BMCSecretRef which are
	// managed by the controller per server.
	// +required
	Template BMCUserSetTemplate `json:"template"`
}

// BMCUserSetTemplate describes the per-server BMCUser that the set creates.
// It mirrors BMCUserSpec minus the per-instance fields BMCRef and BMCSecretRef.
type BMCUserSetTemplate struct {
	// UserName is the username of the BMC user.
	UserName string `json:"userName"`

	// RoleID is the ID of the role to assign to the user.
	RoleID string `json:"roleID"`

	// Description is a description for the BMC user.
	// +optional
	Description string `json:"description,omitempty"`

	// RotationPeriod defines how often the password should be rotated.
	// If not set, the password will not be rotated.
	// +optional
	RotationPeriod *metav1.Duration `json:"rotationPeriod,omitempty"`
}

// BMCUserSetStatus defines the observed state of BMCUserSet.
type BMCUserSetStatus struct {
	// TotalServers is the number of Server resources currently matching the selector.
	TotalServers int32 `json:"totalServers,omitempty"`

	// ReadyServers is the number of servers where the BMCUser is Ready.
	ReadyServers int32 `json:"readyServers,omitempty"`

	// PendingServers is the number of servers where the BMCUser is not yet Ready.
	PendingServers int32 `json:"pendingServers,omitempty"`

	// Conditions reflect the overall state of the BMCUserSet.
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=bmcus
// +kubebuilder:printcolumn:name="Total",type=integer,JSONPath=".status.totalServers"
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=".status.readyServers"
// +kubebuilder:printcolumn:name="Pending",type=integer,JSONPath=".status.pendingServers"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// BMCUserSet is the Schema for the bmcusersets API.
type BMCUserSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BMCUserSetSpec   `json:"spec,omitempty"`
	Status BMCUserSetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BMCUserSetList contains a list of BMCUserSet.
type BMCUserSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BMCUserSet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BMCUserSet{}, &BMCUserSetList{})
}
