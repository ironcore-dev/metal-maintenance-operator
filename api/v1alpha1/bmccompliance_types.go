// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.
type BMCComplianceState string

const (
	BMCComplianceStateCompliant    BMCComplianceState = "Compliant"
	BMCComplianceStateNonCompliant BMCComplianceState = "NonCompliant"
	BMCComplianceStateOutOfSupport BMCComplianceState = "OutOfSupport"
	BMCComplianceStateCritical     BMCComplianceState = "Critical"
	BMCComplianceStateUnknown      BMCComplianceState = "Unknown"
)

type BMCCompliancePolicy string

const (

	// BMCCompliancePolicyStrict is a strict compliance policy that ensures the BMC-Version is equal to the defined version.
	BMCCompliancePolicyStrict BMCCompliancePolicy = "Strict"
	// BMCCompliancePolicyRange is a range compliance policy that allows a range of BMC-Versions.
	BMCCompliancePolicyRange BMCCompliancePolicy = "Range"
	// BMCCompliancePolicyMinimum is a minimum compliance policy that ensures the BMC-Version is greater than or equal to the defined version.
	BMCCompliancePolicyMinimum BMCCompliancePolicy = "Minimum"
)

// BMCComplianceSpec defines the desired state of BMCCompliance.
type VersionRange struct {
	// Min defines the minimum version in the range.
	Min string `json:"min,omitempty"`
	// Max defines the maximum version in the range.
	Max string `json:"max,omitempty"`
}

// BMCComplianceSpec defines the desired state of BMCCompliance.
type BMCComplianceSpec struct {
	// BMCRef is a reference to a specific BMC to generate the compliance object.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="serverRef is immutable"
	BMCRef *corev1.LocalObjectReference `json:"bmcRef,omitempty"`
	// TargetVersion is the exact version for Strict compliance policy.
	TargetVersion string `json:"targetVersion,omitempty"`
	// VersionRange is the range of versions for Range compliance policy. Or the minimum version for Minimum compliance policy.
	VersionRange *VersionRange `json:"versionRange,omitempty"`
	// OutOfSupportVersion is the version that is considered out of support. provided by the HW vendor
	OutOfSupportVersion string `json:"outOfSupportVersion,omitempty"`
	// CriticalVersions are the versions that are considered critical.
	CriticalVersions []string `json:"criticalVersions,omitempty"`
	// CompliancePolicy defines the compliance policy to use for the BMC.
	CompliancePolicy BMCCompliancePolicy `json:"compliancePolicy,omitempty"`
}

// BMCComplianceStatus defines the observed state of BMCCompliance.
type BMCComplianceStatus struct {
	State BMCComplianceState `json:"state,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// BMCCompliance is the Schema for the bmccompliances API.
type BMCCompliance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BMCComplianceSpec   `json:"spec,omitempty"`
	Status BMCComplianceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BMCComplianceList contains a list of BMCCompliance.
type BMCComplianceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BMCCompliance `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BMCCompliance{}, &BMCComplianceList{})
}
