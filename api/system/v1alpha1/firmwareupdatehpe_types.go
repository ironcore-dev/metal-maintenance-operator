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

// FirmwareUpdateHPEState describes the current state of a FirmwareUpdateHPE.
type FirmwareUpdateHPEState string

const (
	// FirmwareUpdateHPEStatePending specifies that the SPP-based firmware update is waiting.
	FirmwareUpdateHPEStatePending FirmwareUpdateHPEState = "Pending"
	// FirmwareUpdateHPEStateInProgress specifies that the SPP-based firmware update is in progress.
	FirmwareUpdateHPEStateInProgress FirmwareUpdateHPEState = "InProgress"
	// FirmwareUpdateHPEStateCompleted specifies that the SPP-based firmware update has been completed.
	FirmwareUpdateHPEStateCompleted FirmwareUpdateHPEState = "Completed"
	// FirmwareUpdateHPEStateFailed specifies that the SPP-based firmware update has failed.
	FirmwareUpdateHPEStateFailed FirmwareUpdateHPEState = "Failed"
)

// HPESPPSpec describes the extracted HPE Service Pack for ProLiant (SPP) the controller diffs
// against the server and stages into the iLO ComponentRepository.
//
// Unlike Dell (iDRAC reads Catalog.xml) and Lenovo (XCC reads the repo _index.json), HPE iLO has
// NO whole-SPP ingest action: the controller reads the SPP's `manifest/metadata.json` itself,
// diffs it against /redfish/v1/UpdateService/FirmwareInventory (joining on the component Target
// GUID), then stages each applicable .fwpkg into iLO via AddFromUri and applies them with an iLO
// Install Set. The mechanism, the diff, and the payloads are documented in:
// https://github.com/shyamsundart14/metal-maintenance-operator/blob/main/docs/hpe-spp-manifest.md
type HPESPPSpec struct {
	// BaseURI is the HTTP(S) base URL where the extracted SPP is served (reachable by iLO). The
	// controller reads `<BaseURI>/manifest/metadata.json` for the catalog, and hands iLO
	// `<BaseURI>/packages/<file>.fwpkg` URIs via AddFromUri to stage components.
	// +required
	BaseURI string `json:"baseURI"`

	// SecretRef references credentials for the SPP server, if it requires authentication.
	// +optional
	SecretRef *corev1.SecretReference `json:"secretRef,omitempty"`
}

// FirmwareUpdateHPETemplate defines the desired SPP-based firmware update parameters.
type FirmwareUpdateHPETemplate struct {
	// SPP describes the HPE Service Pack for ProLiant the controller diffs and applies.
	// +required
	SPP HPESPPSpec `json:"spp"`
}

// FirmwareUpdateHPESpec defines the desired state of FirmwareUpdateHPE.
type FirmwareUpdateHPESpec struct {
	// FirmwareUpdateHPETemplate defines the template to be applied on the server.
	FirmwareUpdateHPETemplate `json:",inline"`

	// ServerMaintenanceRef is a reference to a ServerMaintenance object that the controller has
	// requested for the referred server. Reboot safety is delegated entirely to this
	// ServerMaintenance (the iLO Install Set reboots the host and there is no client apply-time
	// control) — the apply pass is gated until the Server is parked for maintenance, exactly as
	// the Dell and Lenovo controllers do.
	// +optional
	ServerMaintenanceRef *metalv1alpha1.ObjectReference `json:"serverMaintenanceRef,omitempty"`

	// ServerMaintenancePolicy is a maintenance policy to be enforced on the server
	// (OwnerApproval | Enforced).
	// +optional
	ServerMaintenancePolicy *maintenancev1alpha1.ServerMaintenancePolicy `json:"serverMaintenancePolicy,omitempty"`

	// ServerRef is a reference to a specific server to apply the SPP-based firmware update on.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="serverRef is immutable"
	// +required
	ServerRef *corev1.LocalObjectReference `json:"serverRef"`

	// RetryPolicy defines the retry behavior for automatic retries on transient failures.
	// +optional
	RetryPolicy *api.RetryPolicy `json:"retryPolicy,omitempty"`
}

// ComponentUpdate is one component the diff selected for update: the device it targets, the
// versions involved, and the payload staged into the iLO ComponentRepository.
type ComponentUpdate struct {
	// Target is the HPE device Target GUID — the join key between the SPP manifest, the iLO
	// FirmwareInventory, and the ComponentRepository.
	// +optional
	Target string `json:"target,omitempty"`

	// DeviceName is the human-readable device name from the SPP manifest.
	// +optional
	DeviceName string `json:"deviceName,omitempty"`

	// InstalledVersion is the version currently reported by FirmwareInventory.
	// +optional
	InstalledVersion string `json:"installedVersion,omitempty"`

	// AvailableVersion is the version offered by the SPP manifest.
	// +optional
	AvailableVersion string `json:"availableVersion,omitempty"`

	// Filename is the payload staged into the iLO ComponentRepository and referenced by the
	// Install Set Sequence. Resolved from the SPP manifest's `Package.Files[].Name` (the entry
	// whose TargetGUIDs contains Target) — not `FirmwareImages[].FileName`.
	// +optional
	Filename string `json:"filename,omitempty"`

	// State is the per-component state as reported by the iLO UpdateTaskQueue.
	// +optional
	State string `json:"state,omitempty"`
}

// FirmwareUpdateHPEStatus defines the observed state of FirmwareUpdateHPE.
type FirmwareUpdateHPEStatus struct {
	// State represents the current state of the SPP-based firmware update.
	// +optional
	State FirmwareUpdateHPEState `json:"state,omitempty"`

	// InstallSetID is the id of the iLO Install Set created for the current pass.
	// +optional
	InstallSetID string `json:"installSetID,omitempty"`

	// UpdateComponents is the applicable update set computed by the dry-run diff (and tracked
	// through apply).
	// +optional
	UpdateComponents []ComponentUpdate `json:"updateComponents,omitempty"`

	// PassCount is the number of check->apply->recheck passes completed so far. It bounds the
	// internal convergence loop.
	// +optional
	PassCount int32 `json:"passCount,omitempty"`

	// FailedAttempts is the number of automatic retry attempts made after failure.
	// +optional
	FailedAttempts int32 `json:"failedAttempts,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represents the latest available observations of the SPP-based firmware update
	// state.
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=fwuh
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="ServerRef",type=string,JSONPath=`.spec.serverRef.name`
// +kubebuilder:printcolumn:name="ServerMaintenanceRef",type=string,JSONPath=`.spec.serverMaintenanceRef.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

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
