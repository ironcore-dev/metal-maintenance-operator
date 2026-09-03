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

// DellShareType is the type of network share hosting the Dell update repository/catalog.
type DellShareType string

const (
	DellShareTypeNFS   DellShareType = "NFS"
	DellShareTypeCIFS  DellShareType = "CIFS"
	DellShareTypeHTTP  DellShareType = "HTTP"
	DellShareTypeHTTPS DellShareType = "HTTPS"
)

// RepositoryJob represents a Dell iDRAC job resource tracking a repository-based firmware
// operation. State is intentionally a plain string mirroring bmc.DellJob.
type RepositoryJob struct {
	// +optional
	JobID string `json:"jobID,omitempty"`
	// +optional
	Name string `json:"name,omitempty"`
	// +optional
	JobType string `json:"jobType,omitempty"`
	// +optional
	State string `json:"state,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// +optional
	PercentComplete int32 `json:"percentComplete,omitempty"`
}

// ComponentJobsSummary tallies per-component jobs by completion state.
type ComponentJobsSummary struct {
	// +optional
	Total int32 `json:"total,omitempty"`
	// +optional
	Completed int32 `json:"completed,omitempty"`
	// +optional
	InProgress int32 `json:"inProgress,omitempty"`
	// +optional
	Failed int32 `json:"failed,omitempty"`
}

// FirmwareUpdateState describes the current state of a FirmwareUpdate.
type FirmwareUpdateState string

const (
	// FirmwareUpdateStatePending specifies that the firmware update is waiting.
	FirmwareUpdateStatePending FirmwareUpdateState = "Pending"
	// FirmwareUpdateStateInProgress specifies that the firmware update is in progress.
	FirmwareUpdateStateInProgress FirmwareUpdateState = "InProgress"
	// FirmwareUpdateStateCompleted specifies that the firmware update has been completed.
	FirmwareUpdateStateCompleted FirmwareUpdateState = "Completed"
	// FirmwareUpdateStateFailed specifies that the firmware update has failed.
	FirmwareUpdateStateFailed FirmwareUpdateState = "Failed"
)

// FirmwareRepository describes the network share hosting Dell's update repository/catalog, as
// consumed by DellSoftwareInstallationService.InstallFromRepository.
type FirmwareRepository struct {
	// ShareType is the type of network share hosting the repository.
	// +kubebuilder:validation:Enum=NFS;CIFS;HTTP;HTTPS
	// +required
	ShareType DellShareType `json:"shareType"`

	// Address is the share's hostname or IP address (e.g. downloads.dell.com).
	// +optional
	Address string `json:"address,omitempty"`

	// ShareName is the network share name. Not required for HTTP/HTTPS catalogs.
	// +optional
	ShareName string `json:"shareName,omitempty"`

	// CatalogFile is the catalog file name within the share. Defaults to "Catalog.xml".
	// +optional
	CatalogFile string `json:"catalogFile,omitempty"`

	// Workgroup is the CIFS workgroup, if applicable.
	// +optional
	Workgroup string `json:"workgroup,omitempty"`

	// CredentialsRef references the credentials used to authenticate against the share, if required.
	// +optional
	CredentialsRef *corev1.SecretReference `json:"credentialsRef,omitempty"`

	// IgnoreCertWarning, if true, ignores certificate warnings for HTTPS shares.
	// +optional
	IgnoreCertWarning *bool `json:"ignoreCertWarning,omitempty"`

	// RebootNeeded, if true, allows the BMC to reboot the server to apply updates.
	// +optional
	RebootNeeded bool `json:"rebootNeeded,omitempty"`

	// ApplySameVersions, if true, re-applies packages already at the same version.
	// +optional
	ApplySameVersions *bool `json:"applySameVersions,omitempty"`

	// ApplyDowngradeVersions, if true, allows applying packages older than the currently installed version.
	// +optional
	ApplyDowngradeVersions *bool `json:"applyDowngradeVersions,omitempty"`
}

// FirmwareUpdateTemplate defines the desired firmware update parameters.
// +kubebuilder:validation:XValidation:rule="has(self.repository) != has(self.image)",message="exactly one of repository or image must be set"
type FirmwareUpdateTemplate struct {
	// Repository describes the network share hosting the Dell update repository/catalog.
	// +optional
	Repository *FirmwareRepository `json:"repository,omitempty"`

	// Image describes the OTB firmware image parameters (HPE, Lenovo).
	// +optional
	Image *api.ImageSpec `json:"image,omitempty"`

	// ServerMaintenancePolicy is a maintenance policy to be enforced on the server.
	// +optional
	ServerMaintenancePolicy *maintenancev1alpha1.ServerMaintenancePolicy `json:"serverMaintenancePolicy,omitempty"`

	// RetryPolicy defines the retry behavior for automatic retries on transient failures.
	// +optional
	RetryPolicy *api.RetryPolicy `json:"retryPolicy,omitempty"`
}

// FirmwareUpdateSpec defines the desired state of FirmwareUpdate.
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.serverRef) || self.serverRef == oldSelf.serverRef",message="serverRef is immutable"
type FirmwareUpdateSpec struct {
	// FirmwareUpdateTemplate defines the template to be applied on the server.
	FirmwareUpdateTemplate `json:",inline"`

	// ServerRef is a reference to a specific server to apply the firmware update on.
	// +required
	ServerRef *corev1.LocalObjectReference `json:"serverRef"`

	// ProgressDeadlineSeconds is the maximum time in seconds to wait without observable forward
	// progress before the update is marked Failed. Defaults to 3600 (1 hour).
	// +kubebuilder:default=3600
	// +optional
	ProgressDeadlineSeconds *int32 `json:"progressDeadlineSeconds,omitempty"`

	// TTLSecondsAfterFinished, if set, causes the FirmwareUpdate to be deleted that many seconds
	// after it reaches Completed state. Failed objects are retained for operator inspection.
	// +optional
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`
}

// FirmwareUpdateStatus defines the observed state of FirmwareUpdate.
type FirmwareUpdateStatus struct {
	// State represents the current state of the firmware update.
	// +optional
	State FirmwareUpdateState `json:"state,omitempty"`

	// ServerMaintenanceRef is a reference to the ServerMaintenance object the controller created for this update.
	// +optional
	ServerMaintenanceRef *metalv1alpha1.ObjectReference `json:"serverMaintenanceRef,omitempty"`

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

	// BaselineJobsCaptured is true once BaselineJobIDs has been successfully populated for the current pass.
	// +optional
	BaselineJobsCaptured bool `json:"baselineJobsCaptured,omitempty"`

	// LastProgressTime records the last time the controller observed forward progress.
	// Used together with ProgressDeadlineSeconds to detect stalled updates.
	// +optional
	LastProgressTime *metav1.Time `json:"lastProgressTime,omitempty"`

	// PassCount is the number of check->apply->track->recheck passes completed so far.
	// +optional
	PassCount int32 `json:"passCount,omitempty"`

	// FailedAttempts is the number of automatic retry attempts made after failure.
	// +optional
	FailedAttempts int32 `json:"failedAttempts,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represents the latest available observations of the firmware update state.
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=fwu
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="ServerRef",type=string,JSONPath=`.spec.serverRef.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// FirmwareUpdate is the Schema for the firmwareupdates API.
type FirmwareUpdate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FirmwareUpdateSpec   `json:"spec,omitempty"`
	Status FirmwareUpdateStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FirmwareUpdateList contains a list of FirmwareUpdate.
type FirmwareUpdateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FirmwareUpdate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FirmwareUpdate{}, &FirmwareUpdateList{})
}
