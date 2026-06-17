// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// StageDriftPolicy records what drift monitoring mode is active for a completed stage's
// child CR. Set by the run controller after each stage succeeds.
// +kubebuilder:validation:Enum=Observe;Suspend;""
type StageDriftPolicy string

const (
	// StageDriftPolicyObserve means the child CR is watched for regressions.
	// Applied to terminal version stages and all settings stages.
	StageDriftPolicyObserve StageDriftPolicy = "Observe"
	// StageDriftPolicySuspend means the child CR is superseded (an intermediate
	// version hop) and regressions to it are not meaningful.
	StageDriftPolicySuspend StageDriftPolicy = "Suspend"
)

// ConditionTypeDriftDetected is set on a MaintenancePlanRun when drift is detected
// on one or more completed stages and driftPolicy is Observe.
const ConditionTypeDriftDetected = "DriftDetected"

// RunTrigger indicates why a MaintenancePlanRun was created.
// +kubebuilder:validation:Enum=Initial;DriftRemediation
type RunTrigger string

const (
	// RunTriggerInitial means this run was created as part of the first-time
	// maintenance pipeline for the BMC.
	RunTriggerInitial RunTrigger = "Initial"
	// RunTriggerDriftRemediation means this run was created to remediate drift
	// detected on a previously completed run.
	RunTriggerDriftRemediation RunTrigger = "DriftRemediation"
)

// MaintenancePlanRunPhase represents the lifecycle state of a single MaintenancePlanRun.
// +kubebuilder:validation:Enum=Pending;Running;Succeeded;Failed
type MaintenancePlanRunPhase string

const (
	// MaintenancePlanRunPhasePending means the run has been created and is waiting to start.
	MaintenancePlanRunPhasePending MaintenancePlanRunPhase = "Pending"
	// MaintenancePlanRunPhaseRunning means the run is actively executing stages.
	MaintenancePlanRunPhaseRunning MaintenancePlanRunPhase = "Running"
	// MaintenancePlanRunPhaseSucceeded means all stages completed (or were skipped).
	MaintenancePlanRunPhaseSucceeded MaintenancePlanRunPhase = "Succeeded"
	// MaintenancePlanRunPhaseFailed means a stage failed and the run has halted.
	MaintenancePlanRunPhaseFailed MaintenancePlanRunPhase = "Failed"
)

// StagePhase is the execution state of a single stage within a run.
// +kubebuilder:validation:Enum=Pending;Running;Skipped;Succeeded;Failed
type StagePhase string

const (
	// StagePhasePending means the stage has not started yet.
	StagePhasePending StagePhase = "Pending"
	// StagePhaseRunning means the child CR has been created and is being watched.
	StagePhaseRunning StagePhase = "Running"
	// StagePhaseSkipped means the stage was skipped because the target version is already met.
	StagePhaseSkipped StagePhase = "Skipped"
	// StagePhaseSucceeded means the child CR reached a terminal success state.
	StagePhaseSucceeded StagePhase = "Succeeded"
	// StagePhaseFailed means the child CR reached a terminal failure state.
	StagePhaseFailed StagePhase = "Failed"
)

// ServerStageStatus captures the execution state for one server within a Server-scoped stage.
type ServerStageStatus struct {
	// ServerRef identifies the server this status entry belongs to.
	// +required
	ServerRef corev1.LocalObjectReference `json:"serverRef"`

	// Phase is the current execution phase for this server's child CR.
	// +required
	Phase StagePhase `json:"phase"`

	// ChildRef is the object reference to the child CR created for this server (if any).
	// +optional
	ChildRef *corev1.ObjectReference `json:"childRef,omitempty"`

	// StartTime is when the child CR was created for this server.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when this server's child CR reached a terminal phase.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Message is a human-readable description of the current state for this server.
	// +optional
	Message string `json:"message,omitempty"`
}

// StageStatus captures the observed state of one stage in a run.
type StageStatus struct {
	// Name matches the stage name from MaintenancePlanSpec.Stages.
	// +required
	Name string `json:"name"`

	// Phase is the current execution phase of this stage.
	// +required
	Phase StagePhase `json:"phase"`

	// ChildRef is the object reference to the child CR for BMC-scoped stages (BMCSettings/BMCVersion).
	// Empty for Server-scoped stages; see ServerStatuses instead.
	// +optional
	ChildRef *corev1.ObjectReference `json:"childRef,omitempty"`

	// ServerStatuses holds per-server execution state for Server-scoped stages
	// (BIOSSettings/BIOSVersion). Empty for BMC-scoped stages.
	// +optional
	ServerStatuses []ServerStageStatus `json:"serverStatuses,omitempty"`

	// StartTime is when this stage began executing.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when this stage reached a terminal phase.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Message is a human-readable description of the current stage state.
	// +optional
	Message string `json:"message,omitempty"`

	// StageDriftPolicy records the drift monitoring mode applied to this stage's
	// child CR after completion. Empty while the stage is pending or running.
	// +optional
	StageDriftPolicy StageDriftPolicy `json:"stageDriftPolicy,omitempty"`
}

// MaintenancePlanRunSpec defines the input for a single BMC's maintenance run.
type MaintenancePlanRunSpec struct {
	// PlanRef is the MaintenancePlan that generated this run.
	// +required
	PlanRef corev1.LocalObjectReference `json:"planRef"`

	// BMCRef is the BMC object that this run targets.
	// One run is created per unique BMC matched by the plan's serverSelector.
	// +required
	BMCRef corev1.LocalObjectReference `json:"bmcRef"`

	// ServerRefs are all Server objects that share this BMC.
	// BMC-scoped stages (BMCSettings/BMCVersion) execute once for the BMC.
	// Server-scoped stages (BIOSSettings/BIOSVersion) fan out one child CR per server.
	// +kubebuilder:validation:MinItems=1
	// +required
	ServerRefs []corev1.LocalObjectReference `json:"serverRefs"`

	// BaselineBMCVersion is the BMC firmware version observed at run creation time.
	// Used for per-stage version-aware skip evaluation for BMC-scoped stages.
	// +optional
	BaselineBMCVersion string `json:"baselineBMCVersion,omitempty"`

	// BaselineBIOSVersions maps each server name to its BIOS firmware version
	// observed at run creation time. Used for per-server, per-stage version-aware
	// skip evaluation for Server-scoped stages.
	// +optional
	BaselineBIOSVersions map[string]string `json:"baselineBIOSVersions,omitempty"`

	// DriftPolicy is copied from the parent MaintenancePlan at run creation time.
	// Controls whether and how the controller monitors completed stages for regressions.
	// +kubebuilder:default=Disabled
	// +optional
	DriftPolicy DriftPolicy `json:"driftPolicy,omitempty"`

	// Trigger records why this run was created.
	// +kubebuilder:default=Initial
	// +optional
	Trigger RunTrigger `json:"trigger,omitempty"`

	// Stages is a snapshot of the plan's stage list at run creation time.
	// Immutable after creation.
	// +kubebuilder:validation:MinItems=1
	// +required
	Stages []PlanStage `json:"stages"`
}

// MaintenancePlanRunStatus defines the observed state of MaintenancePlanRun.
type MaintenancePlanRunStatus struct {
	// Phase is the high-level state of this run.
	// +optional
	Phase MaintenancePlanRunPhase `json:"phase,omitempty"`

	// CurrentStageIndex is the zero-based index of the stage currently being executed.
	// +optional
	CurrentStageIndex int32 `json:"currentStageIndex,omitempty"`

	// StageStatuses holds per-stage execution state, indexed in the same order as Spec.Stages.
	// +optional
	StageStatuses []StageStatus `json:"stageStatuses,omitempty"`

	// StartTime is when the run transitioned from Pending to Running.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the run reached a terminal phase.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// ObservedGeneration is the most recent generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent the latest available observations of the run's state.
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=mplanrun
// +kubebuilder:printcolumn:name="Plan",type=string,JSONPath=`.spec.planRef.name`
// +kubebuilder:printcolumn:name="BMC",type=string,JSONPath=`.spec.bmcRef.name`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Stage",type=integer,JSONPath=`.status.currentStageIndex`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MaintenancePlanRun is the Schema for the maintenanceplanruns API.
type MaintenancePlanRun struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MaintenancePlanRunSpec   `json:"spec,omitempty"`
	Status MaintenancePlanRunStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MaintenancePlanRunList contains a list of MaintenancePlanRun.
type MaintenancePlanRunList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MaintenancePlanRun `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MaintenancePlanRun{}, &MaintenancePlanRunList{})
}
