// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ironcore-dev/metal-maintenance-operator/api"
	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/maintenance/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

// FirmwareUpdateLenovoState describes the current state of a FirmwareUpdateLenovo.
type FirmwareUpdateLenovoState string

const (
	// FirmwareUpdateLenovoStatePending specifies that the repository-based firmware update is waiting.
	FirmwareUpdateLenovoStatePending FirmwareUpdateLenovoState = "Pending"
	// FirmwareUpdateLenovoStateInProgress specifies that the repository-based firmware update is in progress.
	FirmwareUpdateLenovoStateInProgress FirmwareUpdateLenovoState = "InProgress"
	// FirmwareUpdateLenovoStateCompleted specifies that the repository-based firmware update has been completed.
	FirmwareUpdateLenovoStateCompleted FirmwareUpdateLenovoState = "Completed"
	// FirmwareUpdateLenovoStateFailed specifies that the repository-based firmware update has failed.
	FirmwareUpdateLenovoStateFailed FirmwareUpdateLenovoState = "Failed"
)

// LenovoRepositorySpec describes the firmware repository XCC self-orchestrates against via the
// Lenovo OEM Redfish action LenovoUpdateService.UpdateFromRepository.
//
// Unlike Dell (which points iDRAC at a share plus a Catalog.xml), Lenovo XCC is pointed at the
// repository *directory* via a single RepoURI: XCC reads the bundle's root <bundle>_index.json
// catalog itself and self-inventories the host to select applicable payloads. The metadata layout
// and matching mechanism are documented in:
// https://github.com/shyamsundart14/metal-maintenance-operator/blob/main/docs/lenovo-redfish-updatefromrepository.md
type LenovoRepositorySpec struct {
	// RepoURI is the repository server address XCC pulls the firmware bundle from. It is the only
	// mandatory input to UpdateFromRepository. Point it at the bundle directory (which holds the
	// root <bundle>_index.json catalog + per-component metadata + payloads/), not at a file, e.g.
	// "https://10.0.0.1/firmware/sr650v3". Supported transports: CIFS / NFS / HTTP / HTTPS / SFTP.
	//
	// Note: CIFS/NFS/HTTPS are license-gated on XCC (Enterprise / Platinum / Premier by generation);
	// plain HTTP is not listed as gated. See the design doc referenced above.
	// +required
	RepoURI string `json:"repoURI"`

	// SecretRef references the credentials (username/password) used to authenticate against the
	// repository server, if required. Maps to the RepoUserName / RepoPassword action parameters.
	// +optional
	SecretRef *corev1.SecretReference `json:"secretRef,omitempty"`

	// MountOptions are passed through to the XCC as the RepoMountOpt action parameter, used for
	// CIFS/NFS mounts (e.g. "vers=3.0").
	// +optional
	MountOptions string `json:"mountOptions,omitempty"`

	// GroupRequest maps to the UpdateFromRepository GroupRequest parameter (whether the request
	// originates from a group service). Defaults to false.
	// +optional
	GroupRequest *bool `json:"groupRequest,omitempty"`
}

// FirmwareUpdateLenovoTemplate defines the desired repository-based firmware update parameters.
type FirmwareUpdateLenovoTemplate struct {
	// Repository describes the firmware repository XCC self-orchestrates against.
	// +required
	Repository LenovoRepositorySpec `json:"repository"`
}

// FirmwareUpdateLenovoSpec defines the desired state of FirmwareUpdateLenovo.
type FirmwareUpdateLenovoSpec struct {
	// FirmwareUpdateLenovoTemplate defines the template to be applied on the server.
	FirmwareUpdateLenovoTemplate `json:",inline"`

	// ServerMaintenanceRef is a reference to a ServerMaintenance object that the controller has
	// requested for the referred server. Reboot safety is delegated entirely to this
	// ServerMaintenance (there is no reboot-control parameter on UpdateFromRepository) — the apply
	// pass is gated until the Server enters Maintenance state, exactly as the Dell FirmwareUpdateDell
	// controller does.
	// +optional
	ServerMaintenanceRef *metalv1alpha1.ObjectReference `json:"serverMaintenanceRef,omitempty"`

	// ServerMaintenancePolicy is a maintenance policy to be enforced on the server
	// (OwnerApproval | Enforced).
	// +optional
	ServerMaintenancePolicy *maintenancev1alpha1.ServerMaintenancePolicy `json:"serverMaintenancePolicy,omitempty"`

	// ServerRef is a reference to a specific server to apply the repository-based firmware update on.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="serverRef is immutable"
	// +required
	ServerRef *corev1.LocalObjectReference `json:"serverRef"`

	// RetryPolicy defines the retry behavior for automatic retries on transient failures.
	// +optional
	RetryPolicy *api.RetryPolicy `json:"retryPolicy,omitempty"`
}

// RepositoryTask represents the Redfish Task returned by UpdateFromRepository (and the dry-run
// GetRepoUpdateDetail). State is intentionally a plain string (the raw Redfish TaskState) so
// consumers of this API do not need to depend on the gofish module.
type RepositoryTask struct {
	// TaskID is the Redfish Task identifier tracking the repository operation.
	// +optional
	TaskID string `json:"taskID,omitempty"`

	// Name is the Task's display name.
	// +optional
	Name string `json:"name,omitempty"`

	// State is the XCC-reported raw Redfish TaskState string.
	// +optional
	State string `json:"state,omitempty"`

	// Message is the XCC-reported status message.
	// +optional
	Message string `json:"message,omitempty"`

	// PercentComplete is the XCC-reported completion percentage.
	// +optional
	PercentComplete int32 `json:"percentComplete,omitempty"`
}

// FirmwareUpdateLenovoStatus defines the observed state of FirmwareUpdateLenovo.
type FirmwareUpdateLenovoStatus struct {
	// State represents the current state of the repository-based firmware update.
	// +optional
	State FirmwareUpdateLenovoState `json:"state,omitempty"`

	// CheckTask contains the state of the dry-run GetRepoUpdateDetail task.
	// +optional
	CheckTask *RepositoryTask `json:"checkTask,omitempty"`

	// UpdateTask contains the state of the main UpdateFromRepository apply task.
	// +optional
	UpdateTask *RepositoryTask `json:"updateTask,omitempty"`

	// PassCount is the number of check->apply->track->recheck passes completed so far. It bounds
	// the internal convergence loop.
	// +optional
	PassCount int32 `json:"passCount,omitempty"`

	// FailedAttempts is the number of automatic retry attempts made after failure.
	// +optional
	FailedAttempts int32 `json:"failedAttempts,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represents the latest available observations of the repository-based firmware
	// update state.
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=fwul
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="ServerRef",type=string,JSONPath=`.spec.serverRef.name`
// +kubebuilder:printcolumn:name="ServerMaintenanceRef",type=string,JSONPath=`.spec.serverMaintenanceRef.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// FirmwareUpdateLenovo is the Schema for the firmwareupdatelenovoes API.
type FirmwareUpdateLenovo struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FirmwareUpdateLenovoSpec   `json:"spec,omitempty"`
	Status FirmwareUpdateLenovoStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FirmwareUpdateLenovoList contains a list of FirmwareUpdateLenovo.
type FirmwareUpdateLenovoList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FirmwareUpdateLenovo `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FirmwareUpdateLenovo{}, &FirmwareUpdateLenovoList{})
}
