// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

// DriftPolicy controls how a completed MaintenancePlanRun responds when a child
// CR regresses from a terminal state back to Pending (indicating out-of-band change).
// +kubebuilder:validation:Enum=Reconcile;Observe;Disabled
type DriftPolicy string

const (
	// DriftPolicyReconcile means the run controller re-executes from the earliest
	// dirty stage whenever drift is detected.
	DriftPolicyReconcile DriftPolicy = "Reconcile"
	// DriftPolicyObserve means the run controller surfaces a DriftDetected condition
	// but takes no hardware action.
	DriftPolicyObserve DriftPolicy = "Observe"
	// DriftPolicyDisabled means drift is not monitored. The run stays in its terminal
	// phase and the child CRs are not watched after completion.
	DriftPolicyDisabled DriftPolicy = "Disabled"
)

// StageKind identifies which metal-operator child CRD a stage targets.
// +kubebuilder:validation:Enum=BMCSettings;BMCVersion;BIOSSettings;BIOSVersion
type StageKind string

const (
	StageKindBMCSettings  StageKind = "BMCSettings"
	StageKindBMCVersion   StageKind = "BMCVersion"
	StageKindBIOSSettings StageKind = "BIOSSettings"
	StageKindBIOSVersion  StageKind = "BIOSVersion"
)

// StageTemplate holds the spec payload for a single stage. Exactly one field
// must be set and it must match the stage's Kind.
type StageTemplate struct {
	// BMCSettings is the template used when Kind is BMCSettings.
	// +optional
	BMCSettings *PlanBMCSettingsTemplate `json:"bmcSettings,omitempty"`

	// BMCVersion is the template used when Kind is BMCVersion.
	// +optional
	BMCVersion *metalv1alpha1.BMCVersionTemplate `json:"bmcVersion,omitempty"`

	// BIOSSettings is the template used when Kind is BIOSSettings.
	// +optional
	BIOSSettings *metalv1alpha1.BIOSSettingsTemplate `json:"biosSettings,omitempty"`

	// BIOSVersion is the template used when Kind is BIOSVersion.
	// +optional
	BIOSVersion *metalv1alpha1.BIOSVersionTemplate `json:"biosVersion,omitempty"`
}

// PlanStage defines a single ordered step in the maintenance pipeline.
type PlanStage struct {
	// Name is the unique identifier for this stage within the plan.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Name string `json:"name"`

	// Kind is the type of metal-operator child CRD to create for this stage.
	// +required
	Kind StageKind `json:"kind"`

	// Template contains the spec payload for the child CR.
	// +required
	Template StageTemplate `json:"template"`
}

// MaintenancePlanSpec defines the desired maintenance pipeline for a fleet of servers.
type MaintenancePlanSpec struct {
	// ServerSelector selects the Server objects this plan applies to.
	// +required
	ServerSelector metav1.LabelSelector `json:"serverSelector"`

	// MaxConcurrent is the maximum number of MaintenancePlanRuns that may be
	// in a non-terminal phase at the same time.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	// +optional
	MaxConcurrent int32 `json:"maxConcurrent,omitempty"`

	// Stages is the ordered list of maintenance steps to execute.
	// +kubebuilder:validation:MinItems=1
	// +required
	Stages []PlanStage `json:"stages"`

	// DriftPolicy controls how the run controller responds when a completed child CR
	// regresses back to Pending after the run has succeeded.
	// Defaults to Disabled so drift monitoring must be explicitly opted in to.
	// +kubebuilder:default=Disabled
	// +optional
	DriftPolicy DriftPolicy `json:"driftPolicy,omitempty"`
}

// MaintenancePlanPhase represents the overall lifecycle state of a MaintenancePlan.
// +kubebuilder:validation:Enum=Pending;Active;Completed;Failed
type MaintenancePlanPhase string

const (
	// MaintenancePlanPhasePending means the plan has been created but no runs have started.
	MaintenancePlanPhasePending MaintenancePlanPhase = "Pending"
	// MaintenancePlanPhaseActive means at least one run is in progress.
	MaintenancePlanPhaseActive MaintenancePlanPhase = "Active"
	// MaintenancePlanPhaseCompleted means all servers have been processed successfully.
	MaintenancePlanPhaseCompleted MaintenancePlanPhase = "Completed"
	// MaintenancePlanPhaseFailed means one or more runs failed.
	MaintenancePlanPhaseFailed MaintenancePlanPhase = "Failed"
)

// MaintenancePlanStatus defines the observed state of MaintenancePlan.
type MaintenancePlanStatus struct {
	// Phase is the high-level state of this plan.
	// +optional
	Phase MaintenancePlanPhase `json:"phase,omitempty"`

	// TotalRuns is the total number of MaintenancePlanRuns created for this plan.
	// +optional
	TotalRuns int32 `json:"totalRuns,omitempty"`

	// ActiveRuns is the number of runs currently in a non-terminal phase.
	// +optional
	ActiveRuns int32 `json:"activeRuns,omitempty"`

	// SucceededRuns is the number of runs that completed successfully.
	// +optional
	SucceededRuns int32 `json:"succeededRuns,omitempty"`

	// FailedRuns is the number of runs that failed.
	// +optional
	FailedRuns int32 `json:"failedRuns,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the plan's state.
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=mplan
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Total",type=integer,JSONPath=`.status.totalRuns`
// +kubebuilder:printcolumn:name="Active",type=integer,JSONPath=`.status.activeRuns`
// +kubebuilder:printcolumn:name="Succeeded",type=integer,JSONPath=`.status.succeededRuns`
// +kubebuilder:printcolumn:name="Failed",type=integer,JSONPath=`.status.failedRuns`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MaintenancePlan is the Schema for the maintenanceplans API.
type MaintenancePlan struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MaintenancePlanSpec   `json:"spec,omitempty"`
	Status MaintenancePlanStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MaintenancePlanList contains a list of MaintenancePlan.
type MaintenancePlanList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MaintenancePlan `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MaintenancePlan{}, &MaintenancePlanList{})
}
