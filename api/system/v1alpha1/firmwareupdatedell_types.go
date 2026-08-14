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

// FirmwareUpdateDellState describes the current state of a FirmwareUpdateDell.
type FirmwareUpdateDellState string

const (
	// FirmwareUpdateDellStatePending specifies that the repository-based firmware update is waiting.
	FirmwareUpdateDellStatePending FirmwareUpdateDellState = "Pending"
	// FirmwareUpdateDellStateInProgress specifies that the repository-based firmware update is in progress.
	FirmwareUpdateDellStateInProgress FirmwareUpdateDellState = "InProgress"
	// FirmwareUpdateDellStateCompleted specifies that the repository-based firmware update has been completed.
	FirmwareUpdateDellStateCompleted FirmwareUpdateDellState = "Completed"
	// FirmwareUpdateDellStateFailed specifies that the repository-based firmware update has failed.
	FirmwareUpdateDellStateFailed FirmwareUpdateDellState = "Failed"
)

// DellShareType is the type of network share hosting the Dell update repository/catalog.
type DellShareType string

const (
	DellShareTypeNFS   DellShareType = "NFS"
	DellShareTypeCIFS  DellShareType = "CIFS"
	DellShareTypeHTTP  DellShareType = "HTTP"
	DellShareTypeHTTPS DellShareType = "HTTPS"
)

// RepositorySpec describes the network share hosting Dell's update repository/catalog, as
// consumed by DellSoftwareInstallationService.InstallFromRepository.
type RepositorySpec struct {
	// ShareType is the type of network share hosting the repository.
	// +kubebuilder:validation:Enum=NFS;CIFS;HTTP;HTTPS
	// +required
	ShareType DellShareType `json:"shareType"`

	// Address is the share's IP address or hostname (e.g. downloads.dell.com).
	// +required
	Address string `json:"address"`

	// ShareName is the network share name. Not required for HTTP/HTTPS catalogs.
	// +optional
	ShareName string `json:"shareName,omitempty"`

	// CatalogFile is the catalog file name within the share. Defaults to "Catalog.xml".
	// +optional
	CatalogFile string `json:"catalogFile,omitempty"`

	// Workgroup is the CIFS workgroup, if applicable.
	// +optional
	Workgroup string `json:"workgroup,omitempty"`

	// SecretRef references the credentials used to authenticate against the share, if required.
	// +optional
	SecretRef *corev1.SecretReference `json:"secretRef,omitempty"`

	// IgnoreCertWarning, if true, ignores certificate warnings for HTTPS shares.
	// +optional
	IgnoreCertWarning *bool `json:"ignoreCertWarning,omitempty"`
}

// FirmwareUpdateDellTemplate defines the desired repository-based firmware update parameters.
type FirmwareUpdateDellTemplate struct {
	// Repository describes the network share hosting the update repository/catalog.
	// +required
	Repository RepositorySpec `json:"repository"`

	// ApplySameVersions, if true, re-applies packages already at the same version.
	// +optional
	ApplySameVersions *bool `json:"applySameVersions,omitempty"`

	// ApplyDowngradeVersions, if true, allows applying packages older than the currently installed version.
	// +optional
	ApplyDowngradeVersions *bool `json:"applyDowngradeVersions,omitempty"`
}

// FirmwareUpdateDellSpec defines the desired state of FirmwareUpdateDell.
type FirmwareUpdateDellSpec struct {
	// FirmwareUpdateDellTemplate defines the template to be applied on the server.
	FirmwareUpdateDellTemplate `json:",inline"`

	// ServerMaintenanceRef is a reference to a ServerMaintenance object that the controller has requested for the referred server.
	// +optional
	ServerMaintenanceRef *metalv1alpha1.ObjectReference `json:"serverMaintenanceRef,omitempty"`

	// ServerMaintenancePolicy is a maintenance policy to be enforced on the server.
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

// RepositoryJob represents a Dell iDRAC job resource tracking a repository-based firmware
// operation. State is intentionally a plain string (not a gofish schemas.TaskState or
// schemas.JobState), mirroring bmc.DellJob, so consumers of this API do not need to depend on
// the gofish module.
type RepositoryJob struct {
	// JobID is the iDRAC job identifier (e.g. "JID_...").
	// +optional
	JobID string `json:"jobID,omitempty"`

	// Name is the job's display name.
	// +optional
	Name string `json:"name,omitempty"`

	// JobType is the Dell-reported job type (e.g. "RepositoryUpdate", "FirmwareUpdate").
	// +optional
	JobType string `json:"jobType,omitempty"`

	// State is the Dell-reported raw JobState string.
	// +optional
	State string `json:"state,omitempty"`

	// Message is the Dell-reported status message.
	// +optional
	Message string `json:"message,omitempty"`

	// PercentComplete is the Dell-reported completion percentage.
	// +optional
	PercentComplete int32 `json:"percentComplete,omitempty"`
}

// ComponentJobsSummary tallies the current pass's per-component jobs (ComponentJobs) by
// completion state, computed by the controller purely for observability (e.g. printcolumns);
// controller logic drives off ComponentJobs directly rather than this summary.
type ComponentJobsSummary struct {
	// Total is the number of component jobs discovered so far in the current pass.
	// +optional
	Total int32 `json:"total,omitempty"`

	// Completed is the number of component jobs that finished successfully.
	// +optional
	Completed int32 `json:"completed,omitempty"`

	// InProgress is the number of component jobs that have not yet reached a terminal state.
	// +optional
	InProgress int32 `json:"inProgress,omitempty"`

	// Failed is the number of component jobs that finished in a failed state.
	// +optional
	Failed int32 `json:"failed,omitempty"`
}

// FirmwareUpdateDellStatus defines the observed state of FirmwareUpdateDell.
type FirmwareUpdateDellStatus struct {
	// State represents the current state of the repository-based firmware update.
	// +optional
	State FirmwareUpdateDellState `json:"state,omitempty"`

	// CheckJob contains the state of the dry-run catalog-check job.
	// +optional
	CheckJob *RepositoryJob `json:"checkJob,omitempty"`

	// UpdateJob contains the state of the main apply job.
	// +optional
	UpdateJob *RepositoryJob `json:"updateJob,omitempty"`

	// ComponentJobs contains the state of the per-component jobs spawned by the current pass's apply job.
	// +optional
	ComponentJobs []RepositoryJob `json:"componentJobs,omitempty"`

	// ComponentJobsSummary tallies ComponentJobs by completion state.
	// +optional
	ComponentJobsSummary *ComponentJobsSummary `json:"componentJobsSummary,omitempty"`

	// BaselineJobIDs contains the iDRAC job IDs present just before issuing the apply call for the
	// current pass, used to diff and discover newly spawned component jobs.
	// +optional
	BaselineJobIDs []string `json:"baselineJobIDs,omitempty"`

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

	// Conditions represents the latest available observations of the repository-based firmware update state.
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=fwud
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="ServerRef",type=string,JSONPath=`.spec.serverRef.name`
// +kubebuilder:printcolumn:name="ServerMaintenanceRef",type=string,JSONPath=`.spec.serverMaintenanceRef.name`
// +kubebuilder:printcolumn:name="ComponentJobs",type=integer,JSONPath=`.status.componentJobsSummary.total`
// +kubebuilder:printcolumn:name="Completed",type=integer,JSONPath=`.status.componentJobsSummary.completed`,priority=1
// +kubebuilder:printcolumn:name="InProgress",type=integer,JSONPath=`.status.componentJobsSummary.inProgress`,priority=1
// +kubebuilder:printcolumn:name="Failed",type=integer,JSONPath=`.status.componentJobsSummary.failed`,priority=1
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// FirmwareUpdateDell is the Schema for the firmwareupdatedells API.
type FirmwareUpdateDell struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FirmwareUpdateDellSpec   `json:"spec,omitempty"`
	Status FirmwareUpdateDellStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FirmwareUpdateDellList contains a list of FirmwareUpdateDell.
type FirmwareUpdateDellList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FirmwareUpdateDell `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FirmwareUpdateDell{}, &FirmwareUpdateDellList{})
}
