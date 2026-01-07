// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	"github.com/ironcore-dev/metal-operator/bmc"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// ConsoleSpec defines the desired state of Console.
type ConsoleSpec struct {
	// ServerSelector specifies a label selector to identify the servers that are to be selected.
	// +required
	ServerSelector metav1.LabelSelector `json:"serverSelector"`
	// ConsoleURL is the URL of the server management console.
	ConsoleURL string `json:"consoleURL"`
	// Manufacturer is the manufacturer of the server management console (e.g., "Dell", "HPE", "Lenovo").
	Manufacturer bmc.Manufacturer `json:"manufacturer"`
	// BMCCredentialSecretRef references the secret containing BMC credentials.
	BMCCredentialSecretRef v1.LocalObjectReference `json:"bmcCredentialSecretRef,omitempty"`
}

// ConsoleStatus defines the observed state of Console.
type ConsoleStatus struct {
	// ManagedServers number of managed servers.
	ManagedServers int32 `json:"managedServers,omitempty"`
	// UnmanagedServers number of unmanaged servers.
	UnmanagedServers int32 `json:"unmanagedServers,omitempty"`
	// TotalServers total number of servers.
	TotalServers int32 `json:"totalServers,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// Console is the Schema for the consoles API.
type Console struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ConsoleSpec   `json:"spec,omitempty"`
	Status ConsoleStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ConsoleList contains a list of Console.
type ConsoleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Console `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Console{}, &ConsoleList{})
}
