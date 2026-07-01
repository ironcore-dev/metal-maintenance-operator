// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
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

// ExpectedNetworkSpec defines the expected network wiring for a server.
type ExpectedNetworkSpec struct {
	// Interfaces is the list of expected network interfaces, keyed by MAC address.
	// +optional
	Interfaces []ExpectedInterface `json:"interfaces,omitempty"`
}

// NetworkWiringCheckSpec defines the desired state of NetworkWiringCheck.
// +kubebuilder:validation:XValidation:rule="self.serverSelector.matchLabels.size() > 0 || self.serverSelector.matchExpressions.size() > 0",message="serverSelector must not be empty; it must match exactly the server named in serverRef"
type NetworkWiringCheckSpec struct {
	// ServerRef references the cluster-scoped Server to validate.
	// +required
	// +kubebuilder:validation:XValidation:rule="self.name != ''",message="serverRef.name must not be empty"
	ServerRef corev1.LocalObjectReference `json:"serverRef"`
	// ServerSelector is a label selector used to build the ServerReadinessRule that gates
	// server availability. It must select exactly the server identified by ServerRef.
	// For single-system servers this is typically the BMC name label; for multi-system
	// servers a unique label is required (see metal-operator#977).
	// +required
	// +kubebuilder:validation:XValidation:rule="self.matchLabels.size() > 0 || self.matchExpressions.size() > 0",message="serverSelector must not be empty; it must match exactly the server named in serverRef"
	ServerSelector metav1.LabelSelector `json:"serverSelector"`
	// Network defines the expected network wiring for the server.
	// +optional
	Network ExpectedNetworkSpec `json:"network,omitempty"`
}

// InterfaceMismatch describes a single wiring validation failure on a network interface.
type InterfaceMismatch struct {
	// MACAddress is the MAC address of the interface that failed validation.
	MACAddress string `json:"macAddress"`
	// Reason is a machine-readable token identifying the failure type.
	// +optional
	Reason string `json:"reason,omitempty"`
	// Message describes the mismatch.
	Message string `json:"message"`
}

// NetworkWiringCheckStatus defines the observed state of NetworkWiringCheck.
type NetworkWiringCheckStatus struct {
	// Ready is true when all expected interfaces and neighbors were found.
	// +kubebuilder:default=false
	Ready bool `json:"ready"`
	// Mismatches lists validation failures for the server.
	// +optional
	Mismatches []InterfaceMismatch `json:"mismatches,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced

// NetworkWiringCheck is the Schema for the networkwiringchecks API.
type NetworkWiringCheck struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetworkWiringCheckSpec   `json:"spec,omitempty"`
	Status NetworkWiringCheckStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// NetworkWiringCheckList contains a list of NetworkWiringCheck.
type NetworkWiringCheckList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetworkWiringCheck `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetworkWiringCheck{}, &NetworkWiringCheckList{})
}
