// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ServerManagementConsoleSetSpec defines the desired state of ServerManagementConsoleSet.
type ServerManagementConsoleSetSpec struct {
	// ServerSelector specifies a label selector to identify the servers that are to be selected.
	// +required
	ServerSelector metav1.LabelSelector `json:"serverSelector"`
	// ConsoleURL is the URL of the server management console.
	ConsoleURL string `json:"consoleURL"`
	// Manufacturer is the manufacturer of the server management console (e.g., "Dell", "HPE", "Lenovo").
	Manufacturer string `json:"manufacturer"`
	// LenovoCredentialSecretRef references the secret containing Lenovo credentials.
	LenovoCredentialSecretRef string `json:"lenovoCredentialSecretRef,omitempty"`
	// DellCredentialSecretRef references the secret containing Dell credentials.
	DellCredentialSecretRef string `json:"dellCredentialSecretRef,omitempty"`
	// HPECredentialSecretRef references the secret containing HPE credentials.
	HPECredentialSecretRef string `json:"hpeCredentialSecretRef,omitempty"`
}

type ServerManagementConsoleSetState string

const (
	// ServerManagementConsoleSetStateActive indicates that the server management console is active and operational.
	ServerManagementConsoleSetStateActive ServerManagementConsoleSetState = "Active"
	// ServerManagementConsoleSetStateInactive indicates that the server management console is inactive and not operational.
	ServerManagementConsoleSetStateInactive ServerManagementConsoleSetState = "Inactive"
	// ServerManagementConsoleSetStateError indicates that the server management console is in an error state.
	ServerManagementConsoleSetStateError ServerManagementConsoleSetState = "Error"
)

// ServerManagementConsoleSetStatus defines the observed state of ServerManagementConsoleSet.
type ServerManagementConsoleSetStatus struct {
	// State represents the current state of the server management console.
	// +optional
	State            ServerManagementConsoleSetState `json:"state,omitempty"`
	ManagedServers   int32                           `json:"managedServers,omitempty"`
	UnmanagedServers int32                           `json:"unmanagedServers,omitempty"`
	TotalServers     int32                           `json:"totalServers,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ServerManagementConsoleSet is the Schema for the ServerManagementConsoleSets API.
type ServerManagementConsoleSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ServerManagementConsoleSetSpec   `json:"spec,omitempty"`
	Status ServerManagementConsoleSetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ServerManagementConsoleSetList contains a list of ServerManagementConsoleSet.
type ServerManagementConsoleSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ServerManagementConsoleSet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ServerManagementConsoleSet{}, &ServerManagementConsoleSetList{})
}
