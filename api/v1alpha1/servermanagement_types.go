// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ServerManagementSpec defines the desired state of ServerManagement.
type ServerManagementSpec struct {
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

// ServerManagementStatus defines the observed state of ServerManagement.
type ServerManagementStatus struct {
	// ManagedServers number of managed servers.
	ManagedServers int32 `json:"managedServers,omitempty"`
	// UnmanagedServers number of unmanaged servers.
	UnmanagedServers int32 `json:"unmanagedServers,omitempty"`
	// TotalServers total number of servers.
	TotalServers int32 `json:"totalServers,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ServerManagement is the Schema for the servermanagements API.
type ServerManagement struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ServerManagementSpec   `json:"spec,omitempty"`
	Status ServerManagementStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ServerManagementList contains a list of ServerManagement.
type ServerManagementList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ServerManagement `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ServerManagement{}, &ServerManagementList{})
}
