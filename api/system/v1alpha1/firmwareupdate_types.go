// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/ironcore-dev/metal-maintenance-operator/api"
	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/maintenance/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

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

// FirmwareUpdateTemplate defines the desired firmware update parameters.
// TODO: Add support for HPE and Lenovo firmware update images, which are not repository-based.
type FirmwareUpdateTemplate struct {
	// DellRepository describes the network share hosting the Dell update repository/catalog.
	// +optional
	DellRepository *DellFirmwareRepository `json:"dellRepository,omitempty"`

	// ServerMaintenancePolicy is a maintenance policy to be enforced on the server.
	// +optional
	ServerMaintenancePolicy *maintenancev1alpha1.ServerMaintenancePolicy `json:"serverMaintenancePolicy,omitempty"`

	// RetryPolicy defines the retry behavior for automatic retries on transient failures.
	// +optional
	RetryPolicy *api.RetryPolicy `json:"retryPolicy,omitempty"`
}

// FirmwareUpdateSpec defines the desired state of FirmwareUpdate.
type FirmwareUpdateSpec struct {
	// FirmwareUpdateTemplate defines the template to be applied on the server.
	FirmwareUpdateTemplate `json:",inline"`

	// ServerRef is a reference to a specific server to apply the firmware update on.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="serverRef is immutable"
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

	// DellStatus contains status fields specific to Dell's repository-based firmware update
	// mechanism. Populated only when Spec.DellRepository is set.
	// +optional
	DellStatus *DellFirmwareUpdateStatus `json:"dellStatus,omitempty"`

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
// +kubebuilder:resource:scope=Namespaced,shortName=fwu
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
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &FirmwareUpdate{}, &FirmwareUpdateList{})
		return nil
	})
}
