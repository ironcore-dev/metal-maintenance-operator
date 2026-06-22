// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ExpectedNeighbor defines an expected LLDP neighbor on a network interface.
type ExpectedNeighbor struct {
	// SystemName is the LLDP system name of the expected neighbor (e.g. switch hostname).
	// +required
	SystemName string `json:"systemName"`
	// PortID is the LLDP port identifier of the expected neighbor.
	// +required
	PortID string `json:"portID"`
}

// ExpectedInterface defines the expected state of a server network interface.
type ExpectedInterface struct {
	// MACAddress is the MAC address of the interface and acts as the primary key.
	// +required
	MACAddress string `json:"macAddress"`
	// CarrierStatus is the expected operational carrier status (e.g. "up").
	// If omitted, carrier status is not checked.
	// +optional
	CarrierStatus string `json:"carrierStatus,omitempty"`
	// Neighbors lists the LLDP neighbors that must all be present on this interface.
	// If omitted or empty, neighbor presence is not checked.
	// +optional
	Neighbors []ExpectedNeighbor `json:"neighbors,omitempty"`
}

// ExpectedNetworkSpec defines the expected network wiring for servers.
type ExpectedNetworkSpec struct {
	// Interfaces is the list of expected network interfaces, keyed by MAC address.
	// +optional
	Interfaces []ExpectedInterface `json:"interfaces,omitempty"`
}

// ServerReadinessCheckSpec defines the desired state of ServerReadinessCheck.
type ServerReadinessCheckSpec struct {
	// ServerSelector specifies a label selector to identify the servers to validate.
	// +required
	ServerSelector metav1.LabelSelector `json:"serverSelector"`
	// Network defines the expected network wiring for selected servers.
	// +optional
	Network ExpectedNetworkSpec `json:"network,omitempty"`
}

// InterfaceMismatch describes a single wiring validation failure on a network interface.
type InterfaceMismatch struct {
	// MACAddress is the MAC address of the interface that failed validation.
	MACAddress string `json:"macAddress"`
	// Message describes the mismatch.
	Message string `json:"message"`
}

// ServerReadinessStatus holds the per-server validation result.
type ServerReadinessStatus struct {
	// Name is the name of the Server resource.
	Name string `json:"name"`
	// Ready is true when all expected interfaces and neighbors were found.
	Ready bool `json:"ready"`
	// Mismatches lists validation failures for this server.
	// +optional
	Mismatches []InterfaceMismatch `json:"mismatches,omitempty"`
}

// ServerReadinessCheckStatus defines the observed state of ServerReadinessCheck.
type ServerReadinessCheckStatus struct {
	// Servers holds the per-server validation results.
	// +optional
	Servers []ServerReadinessStatus `json:"servers,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced

// ServerReadinessCheck is the Schema for the serverreadinesschecks API.
type ServerReadinessCheck struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ServerReadinessCheckSpec   `json:"spec,omitempty"`
	Status ServerReadinessCheckStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ServerReadinessCheckList contains a list of ServerReadinessCheck.
type ServerReadinessCheckList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ServerReadinessCheck `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ServerReadinessCheck{}, &ServerReadinessCheckList{})
}
