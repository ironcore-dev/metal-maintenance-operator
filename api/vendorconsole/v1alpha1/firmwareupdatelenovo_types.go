// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// FirmwarePayloadSourceType selects which sub-field of FirmwarePayload the
// controller passes to LXCA when importing the update package.
// +kubebuilder:validation:Enum=URL;Upload
type FirmwarePayloadSourceType string

const (
	// FirmwarePayloadSourceURL tells the controller to instruct LXCA to
	// download the payload from FirmwarePayload.URL via
	// POST /files/updateRepositories/firmware/import.
	FirmwarePayloadSourceURL FirmwarePayloadSourceType = "URL"
	// FirmwarePayloadSourceUpload is reserved for a future flow where the
	// operator uploads a payload directly into LXCA. Not implemented yet;
	// declared here to keep the enum forward-compatible.
	FirmwarePayloadSourceUpload FirmwarePayloadSourceType = "Upload"
)

// FirmwarePayload describes the firmware bundle (a UXSP or single package)
// that LXCA should acquire before applying updates.
type FirmwarePayload struct {
	// SourceType controls which sub-field of this struct is used.
	// +required
	SourceType FirmwarePayloadSourceType `json:"sourceType"`

	// URL is the location of a UXSP or firmware package that LXCA can
	// download itself. Required when SourceType is "URL".
	// +optional
	URL string `json:"url,omitempty"`

	// Checksum is an optional hash of the payload passed through to LXCA
	// where the endpoint supports validation.
	// +optional
	Checksum string `json:"checksum,omitempty"`
}

// CompliancePolicySpec describes the LXCA compliance policy the controller
// creates (or reuses) to gate the firmware update job.
type CompliancePolicySpec struct {
	// Name is the LXCA compliance policy name. If a policy with this name
	// already exists on the LXCA appliance the controller reuses it.
	// +required
	Name string `json:"name"`

	// Description is set on the LXCA policy when the controller creates one.
	// +optional
	Description string `json:"description,omitempty"`
}

// UpdateActivation mirrors LXCA's activation semantics on
// POST /updatableComponents. Only Immediate is implemented; the enum is
// declared broader to leave room for Prioritized without a schema break.
// +kubebuilder:validation:Enum=Immediate;Prioritized
type UpdateActivation string

const (
	// UpdateActivationImmediate applies updates as soon as they are staged.
	UpdateActivationImmediate UpdateActivation = "Immediate"
	// UpdateActivationPrioritized stages updates but defers activation until
	// LXCA determines a good moment; currently not implemented by this
	// controller.
	UpdateActivationPrioritized UpdateActivation = "Prioritized"
)

// MaintenanceRef records a ServerMaintenance resource this CR created so the
// controller can clean it up on completion or deletion.
type MaintenanceRef struct {
	// ServerName is the name of the target metalv1alpha1.Server.
	ServerName string `json:"serverName"`
	// MaintenanceName is the name of the ServerMaintenance the controller
	// created; it lives in the same namespace as this CR.
	MaintenanceName string `json:"maintenanceName"`
}

// FirmwareUpdateLenovoSpec defines the desired state of FirmwareUpdateLenovo.
type FirmwareUpdateLenovoSpec struct {
	// LXCAURL is the base URL of the target Lenovo XClarity Administrator.
	// +required
	// +kubebuilder:validation:Pattern=`^https?://.+$`
	LXCAURL string `json:"lxcaURL"`

	// SecretRef points at a Secret in the same namespace as this CR that
	// holds LXCA credentials. The Secret must contain "username" and
	// "password" keys; the controller writes back "token" and "sessionID"
	// keys after login so subsequent reconciles can reuse the session.
	// +required
	SecretRef corev1.LocalObjectReference `json:"secretRef"`

	// ServerSelector picks the metalv1alpha1/v1alpha1 Server objects to
	// flash. Servers whose Status.Manufacturer is not "Lenovo" cause the
	// update to transition to Failed.
	// +required
	ServerSelector metav1.LabelSelector `json:"serverSelector"`

	// FirmwarePayload describes the firmware bundle LXCA should import into
	// its repository before applying updates.
	// +required
	FirmwarePayload FirmwarePayload `json:"firmwarePayload"`

	// CompliancePolicy describes the LXCA compliance policy the controller
	// should create (or reuse) for this update.
	// +required
	CompliancePolicy CompliancePolicySpec `json:"compliancePolicy"`

	// UpdateAction maps to LXCA's activation parameter on
	// POST /updatableComponents. Defaults to Immediate.
	// +kubebuilder:default=Immediate
	// +optional
	UpdateAction UpdateActivation `json:"updateAction,omitempty"`

	// ServerMaintenancePolicy is passed through to each ServerMaintenance
	// resource the controller creates while the flash is running. Defaults
	// to Enforced.
	// +kubebuilder:default=Enforced
	// +optional
	ServerMaintenancePolicy metalv1alpha1.ServerMaintenancePolicy `json:"serverMaintenancePolicy,omitempty"`
}

// FirmwareUpdateLenovoStatus defines the observed state of FirmwareUpdateLenovo.
type FirmwareUpdateLenovoStatus struct {
	// State is the top-level lifecycle state.
	// +optional
	State FirmwareUpdateState `json:"state,omitempty"`

	// CompliancePolicyID is the ID LXCA assigned to the compliance policy
	// this controller created or reused.
	// +optional
	CompliancePolicyID string `json:"compliancePolicyID,omitempty"`

	// UpdateJobID is the LXCA task ID returned from
	// POST /updatableComponents.
	// +optional
	UpdateJobID string `json:"updateJobID,omitempty"`

	// RepositoryJobID is the LXCA task ID returned from
	// POST /files/updateRepositories/firmware/import while the payload is
	// being imported. It is cleared once the import completes.
	// +optional
	RepositoryJobID string `json:"repositoryJobID,omitempty"`

	// ServerCount is the number of Servers matched by the selector at the
	// moment the update started.
	// +optional
	ServerCount int32 `json:"serverCount,omitempty"`

	// MaintenanceRefs tracks ServerMaintenance resources the controller
	// created; they are removed on completion or on CR deletion.
	// +optional
	MaintenanceRefs []MaintenanceRef `json:"maintenanceRefs,omitempty"`

	// Conditions represent the current state of the FirmwareUpdateLenovo
	// resource. The controller maintains a single "UpdateCompleted" type.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="lxcaURL",type=string,JSONPath=`.spec.lxcaURL`
// +kubebuilder:printcolumn:name="serverCount",type=integer,JSONPath=`.status.serverCount`
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.status) || !has(oldSelf.status.state) || oldSelf.status.state != 'InProgress' || self.spec.firmwarePayload == oldSelf.spec.firmwarePayload",message="firmwarePayload is immutable while status.state is InProgress"

// FirmwareUpdateLenovo is the Schema for the firmwareupdatelenovoes API.
type FirmwareUpdateLenovo struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of FirmwareUpdateLenovo
	// +required
	Spec FirmwareUpdateLenovoSpec `json:"spec"`

	// status defines the observed state of FirmwareUpdateLenovo
	// +optional
	Status FirmwareUpdateLenovoStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// FirmwareUpdateLenovoList contains a list of FirmwareUpdateLenovo.
type FirmwareUpdateLenovoList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []FirmwareUpdateLenovo `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FirmwareUpdateLenovo{}, &FirmwareUpdateLenovoList{})
}
