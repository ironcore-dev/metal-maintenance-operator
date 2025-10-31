// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AnsibleJobSpec defines the desired state of AnsibleJob
type AnsibleJobSpec struct {
	// Playbook defines the playbook configuration
	// +required
	Playbook PlaybookSpec `json:"playbook"`

	// Roles defines the roles repository configuration
	// +optional
	Roles *RolesSpec `json:"roles,omitempty"`

	// Inventory defines the target hosts
	// +required
	Inventory AnsibleInventory `json:"inventory"`

	// ExtraVars are additional variables to pass to the playbook
	// +optional
	// +listType=atomic
	ExtraVars []KeyValue `json:"extraVars,omitempty"`

	// Limit restricts the playbook run to specific hosts
	// +optional
	Limit string `json:"limit,omitempty"`

	// TimeoutSeconds specifies the job timeout
	// +optional
	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`

	// JobTemplate allows customization of the Kubernetes Job
	// +optional
	JobTemplate *JobTemplateSpec `json:"jobTemplate,omitempty"`

	// TTLSecondsAfterFinished limits the lifetime of an AnsibleJob that has finished
	// execution (either Succeeded or Failed). If this field is set, ttlSecondsAfterFinished
	// seconds after the AnsibleJob finishes, it is eligible to be automatically deleted.
	// When the AnsibleJob is being deleted, its lifecycle guarantees are honored.
	// If this field is unset, the AnsibleJob won't be automatically deleted.
	// If this field is set to zero, the AnsibleJob becomes eligible to be deleted
	// immediately after it finishes.
	// +optional
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`
}

// PlaybookSpec defines the playbook configuration
type PlaybookSpec struct {
	// Name specifies the playbook file to run
	// +required
	Name string `json:"name"`

	// Repository is the Git repository containing playbooks
	// +required
	// +kubebuilder:validation:Pattern=`^https://.*\.git$`
	Repository string `json:"repository"`

	// GitRef specifies the branch, tag, or commit to use for the playbook repository
	// +optional
	GitRef string `json:"gitRef,omitempty"`
}

// RolesSpec defines the roles repository configuration
type RolesSpec struct {
	// Repository is the Git repository containing roles
	// +required
	// +kubebuilder:validation:Pattern=`^https://.*\.git$`
	Repository string `json:"repository"`

	// GitRef specifies the branch, tag, or commit to use for the roles repository
	// +optional
	GitRef string `json:"gitRef,omitempty"`
}

// AnsibleInventory defines the target hosts for playbook execution
type AnsibleInventory struct {
	// Inline inventory as YAML/JSON string
	// +optional
	Inline string `json:"inline,omitempty"`

	// ConfigMapRef references a ConfigMap containing the inventory under the 'hosts' key
	// +optional
	ConfigMapRef *corev1.LocalObjectReference `json:"configMapRef,omitempty"`

	// SecretRef references a Secret containing the inventory under the 'hosts' key
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`
}

// JobTemplateSpec allows customization of the Kubernetes Job
type JobTemplateSpec struct {
	// Image is the container image to use for ansible-runner
	// +optional
	Image string `json:"image,omitempty"`

	// InitImage is the container image to use for the init container (git setup)
	// Defaults to alpine/git with pinned digest for security
	// +optional
	InitImage string `json:"initImage,omitempty"`

	// ServiceAccountName for the Job
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`

	// BackoffLimit specifies the number of retries before marking this job failed
	// +optional
	BackoffLimit *int32 `json:"backoffLimit,omitempty"`

	// Resources specifies the compute resource requirements
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
}

// KeyValue represents a key-value pair for extra variables
type KeyValue struct {
	// Name is the variable name
	// +required
	Name string `json:"name"`

	// Value is the variable value
	// +required
	Value string `json:"value"`
}

// AnsibleJobStatus defines the observed state of AnsibleJob
type AnsibleJobStatus struct {
	// Phase represents the current phase of the job
	// +optional
	Phase AnsibleJobPhase `json:"phase,omitempty"`

	// ObservedGeneration reflects the generation of the most recently observed AnsibleJob
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// JobName is the name of the created Kubernetes Job
	// +optional
	JobName string `json:"jobName,omitempty"`

	// StartTime is when the job started
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// CompletionTime is when the job completed
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// JobID is the ansible-runner job ID
	// +optional
	JobID string `json:"jobId,omitempty"`

	// Conditions represent the latest available observations
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// Results contains the results from the ansible execution
	// +optional
	Results *AnsibleResults `json:"results,omitempty"`
}

// AnsibleJobPhase represents the current phase of the job
type AnsibleJobPhase string

const (
	// AnsibleJobPhaseUninitialized indicates the job has not been initialized yet
	AnsibleJobPhaseUninitialized AnsibleJobPhase = ""
	// AnsibleJobPhasePending indicates the job is waiting to be scheduled
	AnsibleJobPhasePending AnsibleJobPhase = "Pending"
	// AnsibleJobPhaseRunning indicates the job is currently executing
	AnsibleJobPhaseRunning AnsibleJobPhase = "Running"
	// AnsibleJobPhaseSucceeded indicates the job completed successfully
	AnsibleJobPhaseSucceeded AnsibleJobPhase = "Succeeded"
	// AnsibleJobPhaseFailed indicates the job failed
	AnsibleJobPhaseFailed AnsibleJobPhase = "Failed"
)

// Standard condition types following Kubernetes conventions
const (
	// AnsibleJobConditionReady indicates whether the AnsibleJob is ready to execute
	AnsibleJobConditionReady = "Ready"

	// AnsibleJobConditionProgressing indicates whether the AnsibleJob is actively progressing
	AnsibleJobConditionProgressing = "Progressing"

	// AnsibleJobConditionSucceeded indicates whether the AnsibleJob completed successfully
	AnsibleJobConditionSucceeded = "Succeeded"

	// AnsibleJobConditionFailed indicates whether the AnsibleJob failed
	AnsibleJobConditionFailed = "Failed"
)

// Condition reasons following Kubernetes naming conventions
const (
	// ReasonJobCreated indicates the Kubernetes Job has been created
	ReasonJobCreated = "JobCreated"

	// ReasonJobRunning indicates the Job is actively running
	ReasonJobRunning = "JobRunning"

	// ReasonJobSucceeded indicates the Job completed successfully
	ReasonJobSucceeded = "JobSucceeded"

	// ReasonJobFailed indicates the Job failed to complete
	ReasonJobFailed = "JobFailed"

	// ReasonInvalidSpec indicates the AnsibleJob specification is invalid
	ReasonInvalidSpec = "InvalidSpec"

	// ReasonResourceError indicates an error creating required resources
	ReasonResourceError = "ResourceError"

	// ReasonInventoryError indicates an error with the inventory configuration
	ReasonInventoryError = "InventoryError"

	// ReasonPlaybookError indicates an error with the playbook execution
	ReasonPlaybookError = "PlaybookError"
)

// AnsibleResults contains the results from the ansible execution
type AnsibleResults struct {
	// ExitCode from the ansible-runner execution
	// +optional
	ExitCode int32 `json:"exitCode,omitempty"`

	// Stats contains the execution statistics as JSON string
	// +kubebuilder:pruning:PreserveUnknownFields
	// +optional
	Stats string `json:"stats,omitempty"`

	// ArtifactsPath is the path to the artifacts from the run
	// +optional
	ArtifactsPath string `json:"artifactsPath,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Playbook",type=string,JSONPath=`.spec.playbook.name`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// AnsibleJob is the Schema for the ansiblejobs API
type AnsibleJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AnsibleJobSpec   `json:"spec,omitempty"`
	Status AnsibleJobStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// AnsibleJobList contains a list of AnsibleJob
type AnsibleJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AnsibleJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AnsibleJob{}, &AnsibleJobList{})
}
