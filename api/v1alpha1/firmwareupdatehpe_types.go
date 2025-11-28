// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type FirmwareBundleUpdateOptions string

const (
	// FirmwareBundleUpdateServerProfileTemplate indicates that the firmware update should be applied through server profile template.
	FirmwareBundleUpdateServerProfileTemplate FirmwareBundleUpdateOptions = "ServerProfileTemplate"
	// FirmwareBundleUpdateServerProfile indicates that the firmware update should be applied directly to the server profile.
	FirmwareBundleUpdateServerProfile FirmwareBundleUpdateOptions = "ServerProfile"
)

// FirmwareUpdateHPESpec defines the desired state of FirmwareUpdateHPE.
type FirmwareUpdateHPESpec struct {
	// OMEURL is the URL of the Dell OpenManage Enterprise (OME) instance.
	// +required
	// +kubebuilder:validation:Pattern=`^https?://[a-zA-Z0-9.-]+(:[0-9]+)?(/.*)?$`
	OVURL string `json:"ovURL"`

	// secretRef is a reference to the Kubernetes Secret (of type SecretTypeBasicAuth) object that contains the credentials
	// to access the HPE OneView (OV). This secret includes sensitive information such as usernames and passwords.
	// +required
	SecretRef *corev1.LocalObjectReference `json:"secretRef"`

	// ServerSelector specifies a label selector to identify the servers that are to be selected.
	// +required
	ServerSelector metav1.LabelSelector `json:"serverSelector"`

	// ServerMaintenancePolicy is a maintenance policy to be enforced on the server managed by referred BMC.
	// +optional
	ServerMaintenancePolicy metalv1alpha1.ServerMaintenancePolicy `json:"serverMaintenancePolicy,omitempty"`

	// ServerMaintenanceRefs are references to a ServerMaintenance objects that Controller has requested for the each of the related server.
	// +optional
	ServerMaintenanceRefs []metalv1alpha1.ServerMaintenanceRefItem `json:"serverMaintenanceRefs,omitempty"`

	// FirmwareBundle is the identifier of the firmware image to be used for the update.
	// find the name and version of the firmware bundle in HPE OneView UI.
	// kubebuilder:validation:Required
	// +required
	FirmwareBundle *FirmwareBundleHPE `json:"firmwareBundle,omitempty"`

	// FirmwareBundleUpdateOptions defines how the firmware bundle is to be applied to the server.
	// Possible values are 'ServerProfileTemplate' and 'ServerProfile'.
	// kubebuilder:validation:CEL:rule="!has(oldSelf.status) || !has(oldSelf.status.state) || oldSelf.status.state != 'InProgress' || self == oldSelf",message="spec is immutable when firmware update is in progress"
	// kubebuilder:validation:Enum=ServerProfileTemplate;ServerProfile
	// +kubebuilder:Default=ServerProfile
	// +optional
	FirmwareBundleUpdateOptions FirmwareBundleUpdateOptions `json:"firmwareBundleUpdateOptions,omitempty"`
}

// +kubebuilder:validation:CEL:rule="has(self.name) == has(self.version)",message="name and version must be specified together"
// +kubebuilder:validation:CEL:rule="has(self.uuid) || (has(self.name) && has(self.version))",message="either uuid or both name and version must be provided"
type FirmwareBundleHPE struct {
	// Name is the name of the firmware bundle.
	// if provided along with Version, it helps to uniquely identify the firmware bundle.
	// +optional
	Name string `json:"name,omitempty"`
	// Version is the version of the firmware bundle.
	// if provided along with name, it helps to uniquely identify the firmware bundle.
	// +optional
	Version string `json:"version,omitempty"`

	// UUID is the UUID of the firmware bundle.
	// this is optional, if provided, it helps to uniquely identify the firmware bundle.
	// +optional
	UUID string `json:"uuid,omitempty"`
}

type HPEJob struct {
	// Id is the unique identifier for the job created in OME.
	Id string `json:"jobId,omitempty"`
	// Name is the name of the job.
	Name string `json:"name,omitempty"`
	// Status represents the current status of the job.
	// +optional
	Status string `json:"status,omitempty"`
}

type HPEUpdateStatus struct {
	ServerPowerTaskStatus     *HPEJob `json:"serverPowerTaskStatus,omitempty"`
	PatchFirmwareBundleStatus *HPEJob `json:"patchFirmwareBuncleStatus,omitempty"`
	UpdateTaskStatus          *HPEJob `json:"updateTaskStatus,omitempty"`
	ServerOVUUID              string  `json:"serverUUID,omitempty"`
	ServerName                string  `json:"serverName,omitempty"`
}

// FirmwareUpdateHPEStatus defines the observed state of FirmwareUpdateHPE.
type FirmwareUpdateHPEStatus struct {

	// State represents the current state of the bios configuration task.
	// +optional
	State FirmwareUpdateState `json:"state,omitempty"`

	// UpdateTask contains the state of the Update Task created by the OV for the firmware upgrade.
	// +optional
	UpdateTask []HPEUpdateStatus `json:"updateTask,omitempty"`

	// ServerCount is the total number of servers selected by the ServerSelector.
	// +optional
	ServerCount int32 `json:"serverCount,omitempty"`

	// InPendingServerCount is the total number of servers that are currently waiting of serverMaintenance to be approved.
	// +optional
	InPendingServerCount int32 `json:"inPendingServerCount,omitempty"`

	// InProgressServerCount is the total number of servers that are currently in progress of firmware update.
	// +optional
	InProgressServerCount int32 `json:"inProgressServerCount,omitempty"`

	// CompletedServerCount is the total number of servers that have completed the firmware update.
	// +optional
	CompletedServerCount int32 `json:"completedServerCount,omitempty"`

	// FailedServerCount is the total number of servers that have failed the firmware update.
	// +optional
	FailedServerCount int32 `json:"failedServerCount,omitempty"`

	// Conditions represents the latest available observations of the Bios version upgrade state.
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// FirmwareUpdateHPE is the Schema for the firmwareupdatehpes API.
type FirmwareUpdateHPE struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FirmwareUpdateHPESpec   `json:"spec,omitempty"`
	Status FirmwareUpdateHPEStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FirmwareUpdateHPEList contains a list of FirmwareUpdateHPE.
type FirmwareUpdateHPEList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FirmwareUpdateHPE `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FirmwareUpdateHPE{}, &FirmwareUpdateHPEList{})
}
