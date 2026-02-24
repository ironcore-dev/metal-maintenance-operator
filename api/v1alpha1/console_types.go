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
	// PendingOperations tracks in-flight vendor operations.
	PendingOperations []PendingOperation `json:"pendingOperations,omitempty"`
}

// PendingOperation tracks an in-flight vendor operation.
type PendingOperation struct {
	// ServerName is the name of the Server resource.
	ServerName string `json:"serverName"`
	// Hostname is the DNS name used for the server in the vendor console.
	Hostname string `json:"hostname"`
	// IP is the BMC IP address of the server.
	IP string `json:"ip"`
	// OperationType is the type of operation (Import or Remove).
	OperationType OperationType `json:"operationType"`
	// JobID is the vendor-specific job identifier for tracking.
	JobID string `json:"jobId,omitempty"`
	// Status is the current status of the operation.
	Status JobStatus `json:"status"`
	// StartTime is when the operation was initiated.
	StartTime metav1.Time `json:"startTime"`
	// LastChecked is when the job status was last polled.
	LastChecked metav1.Time `json:"lastChecked,omitempty"`
	// RetryCount tracks how many times the operation has been retried.
	RetryCount int32 `json:"retryCount,omitempty"`
	// Message provides human-readable status information.
	Message string `json:"message,omitempty"`
}

// OperationType defines the type of vendor operation.
type OperationType string

const (
	// OperationTypeImport represents importing a server into the console.
	OperationTypeImport OperationType = "Import"
	// OperationTypeRemove represents removing a server from the console.
	OperationTypeRemove OperationType = "Remove"
)

// JobStatus defines the status of a vendor operation.
type JobStatus string

const (
	// JobStatusPending indicates the operation has been queued but not started.
	JobStatusPending JobStatus = "Pending"
	// JobStatusRunning indicates the operation is in progress.
	JobStatusRunning JobStatus = "Running"
	// JobStatusCompleted indicates the operation completed successfully.
	JobStatusCompleted JobStatus = "Completed"
	// JobStatusFailed indicates the operation failed.
	JobStatusFailed JobStatus = "Failed"
	// JobStatusTimedOut indicates the operation exceeded the timeout period.
	JobStatusTimedOut JobStatus = "TimedOut"
)

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
