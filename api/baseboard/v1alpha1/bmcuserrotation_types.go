// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// BMCUserRotationPhase describes the current phase of a BMCUserRotation.
// +kubebuilder:validation:Enum=Pending;InProgress;Succeeded;Failed
type BMCUserRotationPhase string

const (
	BMCUserRotationPhasePending    BMCUserRotationPhase = "Pending"
	BMCUserRotationPhaseInProgress BMCUserRotationPhase = "InProgress"
	BMCUserRotationPhaseSucceeded  BMCUserRotationPhase = "Succeeded"
	BMCUserRotationPhaseFailed     BMCUserRotationPhase = "Failed"
)

// BMCUserRotationTrigger describes what caused a rotation to be initiated.
type BMCUserRotationTrigger string

const (
	BMCUserRotationTriggerPeriod     BMCUserRotationTrigger = "RotationPeriod"
	BMCUserRotationTriggerAnnotation BMCUserRotationTrigger = "Annotation"
	BMCUserRotationTriggerExpiry     BMCUserRotationTrigger = "PasswordExpiry"
)

// BMCUserRotationSpec defines the desired state of BMCUserRotation.
// Spec is immutable after creation.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="BMCUserRotationSpec is immutable"
type BMCUserRotationSpec struct {
	// BMCUserRef references the BMCUser this rotation was triggered for.
	// +required
	BMCUserRef corev1.LocalObjectReference `json:"bmcUserRef"`

	// Type is the rotation strategy used for this rotation event.
	// +kubebuilder:validation:Enum=DualAccount;SingleAccount
	// +required
	Type RotationStrategy `json:"type"`

	// TriggeredAt is the timestamp when the rotation was initiated.
	// +required
	TriggeredAt metav1.Time `json:"triggeredAt"`

	// TriggerReason describes what caused this rotation.
	// +kubebuilder:validation:Enum=RotationPeriod;Annotation;PasswordExpiry
	// +required
	TriggerReason BMCUserRotationTrigger `json:"triggerReason"`
}

// BMCUserRotationStatus defines the observed state of BMCUserRotation.
type BMCUserRotationStatus struct {
	// Phase is the current lifecycle phase of this rotation.
	// +kubebuilder:default=Pending
	Phase BMCUserRotationPhase `json:"phase,omitempty"`

	// CompletedAt is the timestamp when the rotation finished (succeeded or failed).
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// NewSecretRef references the BMCSecret created by this rotation.
	// Set on success.
	// +optional
	NewSecretRef *corev1.LocalObjectReference `json:"newSecretRef,omitempty"`

	// Message provides a human-readable description of the current state,
	// especially useful when Phase is Failed.
	// +optional
	Message string `json:"message,omitempty"`

	// Conditions reflects the progress of this rotation.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=bmcur
// +kubebuilder:printcolumn:name="BMCUser",type=string,JSONPath=`.spec.bmcUserRef.name`
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="Trigger",type=string,JSONPath=`.spec.triggerReason`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="TriggeredAt",type=date,JSONPath=`.spec.triggeredAt`
// +kubebuilder:printcolumn:name="CompletedAt",type=date,JSONPath=`.status.completedAt`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// BMCUserRotation is the Schema for the bmcuserrotations API.
// Each object represents one rotation event for a BMCUser. Spec is immutable after creation.
type BMCUserRotation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BMCUserRotationSpec   `json:"spec,omitempty"`
	Status BMCUserRotationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BMCUserRotationList contains a list of BMCUserRotation.
type BMCUserRotationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BMCUserRotation `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &BMCUserRotation{}, &BMCUserRotationList{})
		return nil
	})
}
