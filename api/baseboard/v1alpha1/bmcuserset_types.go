// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// RotationStrategy determines how many BMCUser objects are created per BMC
// and how rotation is executed.
// +kubebuilder:validation:Enum=DualAccount;SingleAccount
type RotationStrategy string

const (
	// RotationStrategyDualAccount creates two BMCUser objects per BMC.
	// The account being rotated is never the one currently in use,
	// eliminating any lockout window. Required for operator admin accounts.
	RotationStrategyDualAccount RotationStrategy = "DualAccount"

	// RotationStrategySingleAccount creates one BMCUser per BMC.
	// A narrow failure window exists between hardware password change and
	// Secret update. Acceptable for non-critical read-only accounts.
	RotationStrategySingleAccount RotationStrategy = "SingleAccount"
)

// BMCUserTemplate defines the fields templated onto each BMCUser created by a BMCUserSet.
type BMCUserTemplate struct {
	// UserName is the base username for the BMC account.
	// For DualAccount strategy, -a and -b suffixes are appended per instance.
	// +required
	UserName string `json:"userName"`

	// RoleID is the Redfish role to assign (e.g. "Administrator", "ReadOnly").
	// +required
	RoleID string `json:"roleID"`

	// RotationPeriod defines how often the password should be rotated.
	// +optional
	RotationPeriod *metav1.Duration `json:"rotationPeriod,omitempty"`

	// CredentialSecretNameTemplate is a Go template for the stable corev1.Secret
	// name written for each BMC after the first successful credential promotion.
	// Available variable: .BMCName. Example: "bmc-operator-cred-{{ .BMCName }}".
	// If empty, no stable Secret is written.
	// +optional
	CredentialSecretNameTemplate string `json:"credentialSecretNameTemplate,omitempty"`

	// CredentialSecretNamespace is the namespace in which stable credential Secrets
	// are created. Defaults to the operator namespace if empty.
	// +optional
	CredentialSecretNamespace string `json:"credentialSecretNamespace,omitempty"`

	// RotationHistoryLimit is the maximum number of completed (Succeeded or Failed)
	// BMCUserRotation objects to retain per BMCUser. Older objects are pruned once
	// the limit is exceeded. Defaults to 10.
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=1
	// +optional
	RotationHistoryLimit *int32 `json:"rotationHistoryLimit,omitempty"`
}

// BMCUserSetSpec defines the desired state of BMCUserSet.
type BMCUserSetSpec struct {
	// BMCSelector selects which BMC objects this set manages.
	// +required
	BMCSelector metav1.LabelSelector `json:"bmcSelector"`

	// SetupCredentialRef references a corev1.Secret (keys: username, password) in the
	// operator namespace used for first contact with a new BMC before operator accounts exist.
	// This credential is used only during the bootstrap phase; once operator accounts
	// are proven it is never used again for that BMC.
	// +required
	SetupCredentialRef corev1.SecretReference `json:"setupCredentialRef"`

	// RotationStrategy determines how many BMCUser objects are created per BMC.
	// DualAccount (default) creates two users per BMC; SingleAccount creates one.
	// +kubebuilder:default=DualAccount
	// +optional
	RotationStrategy RotationStrategy `json:"rotationStrategy,omitempty"`

	// Template defines the common fields for BMCUser objects created by this set.
	// +required
	Template BMCUserTemplate `json:"template"`
}

// BMCUserSetStatus defines the observed state of BMCUserSet.
type BMCUserSetStatus struct {
	// TotalBMCs is the number of BMCs matched by the selector.
	TotalBMCs int32 `json:"totalBMCs,omitempty"`

	// BootstrappedBMCs is the number of BMCs where all operator accounts are proven
	// and the factory account has been handled.
	BootstrappedBMCs int32 `json:"bootstrappedBMCs,omitempty"`

	// PendingBMCs is the number of BMCs still in the bootstrap phase.
	PendingBMCs int32 `json:"pendingBMCs,omitempty"`

	// Conditions reflects the aggregate state of the BMCUserSet.
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=bmcus
// +kubebuilder:printcolumn:name="Strategy",type=string,JSONPath=`.spec.rotationStrategy`
// +kubebuilder:printcolumn:name="Total",type="integer",JSONPath=`.status.totalBMCs`
// +kubebuilder:printcolumn:name="Bootstrapped",type="integer",JSONPath=`.status.bootstrappedBMCs`
// +kubebuilder:printcolumn:name="Pending",type="integer",JSONPath=`.status.pendingBMCs`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// BMCUserSet is the Schema for the bmcusersets API.
type BMCUserSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   BMCUserSetSpec   `json:"spec,omitempty"`
	Status BMCUserSetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// BMCUserSetList contains a list of BMCUserSet.
type BMCUserSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BMCUserSet `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &BMCUserSet{}, &BMCUserSetList{})
		return nil
	})
}
