// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// FirmwareUpdateSetSpec defines the desired state of FirmwareUpdateSet.
type FirmwareUpdateSetSpec struct {
	// ServerSelector specifies a label selector to identify the servers that are to be selected.
	// +required
	ServerSelector metav1.LabelSelector `json:"serverSelector"`

	// FirmwareUpdateTemplate defines the template for the FirmwareUpdate resource to be applied to the servers.
	// +required
	FirmwareUpdateTemplate FirmwareUpdateTemplate `json:"firmwareUpdateTemplate"`
}

// FirmwareUpdateSetStatus defines the observed state of FirmwareUpdateSet.
type FirmwareUpdateSetStatus struct {
	// FullyLabeledServers is the number of servers in the set.
	FullyLabeledServers int32 `json:"fullyLabeledServers,omitempty"`
	// AvailableFirmwareUpdate is the number of FirmwareUpdate resources currently created by the set.
	AvailableFirmwareUpdate int32 `json:"availableFirmwareUpdate,omitempty"`
	// PendingFirmwareUpdate is the total number of pending FirmwareUpdate resources in the set.
	PendingFirmwareUpdate int32 `json:"pendingFirmwareUpdate,omitempty"`
	// InProgressFirmwareUpdate is the total number of FirmwareUpdate resources in the set that are currently in progress.
	InProgressFirmwareUpdate int32 `json:"inProgressFirmwareUpdate,omitempty"`
	// CompletedFirmwareUpdate is the total number of completed FirmwareUpdate resources in the set.
	CompletedFirmwareUpdate int32 `json:"completedFirmwareUpdate,omitempty"`
	// FailedFirmwareUpdate is the total number of failed FirmwareUpdate resources in the set.
	FailedFirmwareUpdate int32 `json:"failedFirmwareUpdate,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=fwus
// +kubebuilder:printcolumn:name="TotalServers",type="string",JSONPath=`.status.fullyLabeledServers`
// +kubebuilder:printcolumn:name="AvailableFirmwareUpdate",type="string",JSONPath=`.status.availableFirmwareUpdate`
// +kubebuilder:printcolumn:name="Pending",type="string",JSONPath=`.status.pendingFirmwareUpdate`
// +kubebuilder:printcolumn:name="InProgress",type="string",JSONPath=`.status.inProgressFirmwareUpdate`
// +kubebuilder:printcolumn:name="Completed",type="string",JSONPath=`.status.completedFirmwareUpdate`
// +kubebuilder:printcolumn:name="Failed",type="string",JSONPath=`.status.failedFirmwareUpdate`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// FirmwareUpdateSet is the Schema for the firmwareupdatesets API.
type FirmwareUpdateSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FirmwareUpdateSetSpec   `json:"spec,omitempty"`
	Status FirmwareUpdateSetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FirmwareUpdateSetList contains a list of FirmwareUpdateSet.
type FirmwareUpdateSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FirmwareUpdateSet `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &FirmwareUpdateSet{}, &FirmwareUpdateSetList{})
		return nil
	})
}
