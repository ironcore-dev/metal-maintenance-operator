// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// Package controller contains the Kubernetes controller implementations.
package controller

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/ironcore-dev/controller-utils/conditionutils"
	ansiblev1alpha1 "github.com/ironcore-dev/maintenance-operator/api/ansible/v1alpha1"
)

// AnsibleJobReconciler reconciles a AnsibleJob object
type AnsibleJobReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// setCondition sets or updates a condition in the AnsibleJob status using controller-utils
func (r *AnsibleJobReconciler) setCondition(ansibleJob *ansiblev1alpha1.AnsibleJob, conditionType string, status metav1.ConditionStatus, reason, message string) {
	conditionutils.MustUpdateSlice(&ansibleJob.Status.Conditions, conditionType,
		conditionutils.UpdateStatus(status),
		conditionutils.UpdateReason(reason),
		conditionutils.UpdateMessage(message),
		conditionutils.UpdateObserved(ansibleJob),
	)
}

// updateConditionsForPhase updates all relevant conditions based on the current phase
func (r *AnsibleJobReconciler) updateConditionsForPhase(ansibleJob *ansiblev1alpha1.AnsibleJob, phase ansiblev1alpha1.AnsibleJobPhase) {
	switch phase {
	case ansiblev1alpha1.AnsibleJobPhasePending:
		r.setCondition(ansibleJob, ansiblev1alpha1.AnsibleJobConditionReady, metav1.ConditionFalse, ansiblev1alpha1.ReasonJobCreated, "Job is being created")
		r.setCondition(ansibleJob, ansiblev1alpha1.AnsibleJobConditionProgressing, metav1.ConditionTrue, ansiblev1alpha1.ReasonJobCreated, "Job creation in progress")
		r.Recorder.Event(ansibleJob, corev1.EventTypeNormal, ansiblev1alpha1.ReasonJobCreated, "AnsibleJob is being created")
	case ansiblev1alpha1.AnsibleJobPhaseRunning:
		r.setCondition(ansibleJob, ansiblev1alpha1.AnsibleJobConditionReady, metav1.ConditionFalse, ansiblev1alpha1.ReasonJobRunning, "Job is running")
		r.setCondition(ansibleJob, ansiblev1alpha1.AnsibleJobConditionProgressing, metav1.ConditionTrue, ansiblev1alpha1.ReasonJobRunning, "Job is actively running")
		r.Recorder.Event(ansibleJob, corev1.EventTypeNormal, ansiblev1alpha1.ReasonJobRunning, "AnsibleJob is actively running")
	case ansiblev1alpha1.AnsibleJobPhaseSucceeded:
		r.setCondition(ansibleJob, ansiblev1alpha1.AnsibleJobConditionReady, metav1.ConditionTrue, ansiblev1alpha1.ReasonJobSucceeded, "Job completed successfully")
		r.setCondition(ansibleJob, ansiblev1alpha1.AnsibleJobConditionProgressing, metav1.ConditionFalse, ansiblev1alpha1.ReasonJobSucceeded, "Job completed")
		r.setCondition(ansibleJob, ansiblev1alpha1.AnsibleJobConditionSucceeded, metav1.ConditionTrue, ansiblev1alpha1.ReasonJobSucceeded, "Job completed successfully")
		r.Recorder.Event(ansibleJob, corev1.EventTypeNormal, ansiblev1alpha1.ReasonJobSucceeded, "AnsibleJob completed successfully")
	case ansiblev1alpha1.AnsibleJobPhaseFailed:
		r.setCondition(ansibleJob, ansiblev1alpha1.AnsibleJobConditionReady, metav1.ConditionFalse, ansiblev1alpha1.ReasonJobFailed, "Job failed")
		r.setCondition(ansibleJob, ansiblev1alpha1.AnsibleJobConditionProgressing, metav1.ConditionFalse, ansiblev1alpha1.ReasonJobFailed, "Job stopped progressing")
		r.setCondition(ansibleJob, ansiblev1alpha1.AnsibleJobConditionFailed, metav1.ConditionTrue, ansiblev1alpha1.ReasonJobFailed, "Job failed to complete")
		r.Recorder.Event(ansibleJob, corev1.EventTypeWarning, ansiblev1alpha1.ReasonJobFailed, "AnsibleJob failed to complete")
	}
}

// +kubebuilder:rbac:groups=ansible.maintenance.metal.ironcore.dev,resources=ansiblejobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=ansible.maintenance.metal.ironcore.dev,resources=ansiblejobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=ansible.maintenance.metal.ironcore.dev,resources=ansiblejobs/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *AnsibleJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the AnsibleJob instance
	var ansibleJob ansiblev1alpha1.AnsibleJob
	if err := r.Get(ctx, req.NamespacedName, &ansibleJob); err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			logger.Info("AnsibleJob resource not found. Ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get AnsibleJob")
		return ctrl.Result{}, err
	}

	// Handle different phases
	switch ansibleJob.Status.Phase {
	case "":
		// Initialize the job
		return r.initializeJob(ctx, &ansibleJob)
	case ansiblev1alpha1.AnsibleJobPhasePending:
		// Check if job should be created
		return r.createJob(ctx, &ansibleJob)
	case ansiblev1alpha1.AnsibleJobPhaseRunning:
		// Monitor the running job
		return r.monitorJob(ctx, &ansibleJob)
	case ansiblev1alpha1.AnsibleJobPhaseSucceeded, ansiblev1alpha1.AnsibleJobPhaseFailed:
		// Job is complete, nothing to do
		return ctrl.Result{}, nil
	default:
		logger.Info("Unknown phase", "phase", ansibleJob.Status.Phase)
		return ctrl.Result{}, nil
	}
}

func (r *AnsibleJobReconciler) initializeJob(ctx context.Context, ansibleJob *ansiblev1alpha1.AnsibleJob) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Initializing AnsibleJob", "name", ansibleJob.Name)

	// Create a patch base before modifying the status
	patch := client.MergeFrom(ansibleJob.DeepCopy())

	// Initialize job status
	ansibleJob.Status.Phase = ansiblev1alpha1.AnsibleJobPhasePending
	ansibleJob.Status.StartTime = &metav1.Time{Time: time.Now()}
	ansibleJob.Status.ObservedGeneration = ansibleJob.Generation

	// Update conditions for pending phase
	r.updateConditionsForPhase(ansibleJob, ansiblev1alpha1.AnsibleJobPhasePending)

	if err := r.Status().Patch(ctx, ansibleJob, patch); err != nil {
		logger.Error(err, "Failed to patch AnsibleJob status")
		// Use exponential backoff for initialization failures
		retryCount := r.getRetryCountFromConditions(ansibleJob)
		return ctrl.Result{RequeueAfter: r.calculateBackoffDelay(retryCount)}, nil
	}

	return ctrl.Result{Requeue: true}, nil
}

func (r *AnsibleJobReconciler) createJob(ctx context.Context, ansibleJob *ansiblev1alpha1.AnsibleJob) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Check if job already exists
	existingJob := &batchv1.Job{}
	jobName := fmt.Sprintf("%s-job", ansibleJob.Name)
	err := r.Get(ctx, types.NamespacedName{Name: jobName, Namespace: ansibleJob.Namespace}, existingJob)

	if err == nil {
		// Job already exists, update status to running
		patch := client.MergeFrom(ansibleJob.DeepCopy())

		ansibleJob.Status.JobName = jobName
		ansibleJob.Status.Phase = ansiblev1alpha1.AnsibleJobPhaseRunning
		ansibleJob.Status.ObservedGeneration = ansibleJob.Generation

		// Update conditions for running phase
		r.updateConditionsForPhase(ansibleJob, ansiblev1alpha1.AnsibleJobPhaseRunning)

		if updateErr := r.Status().Patch(ctx, ansibleJob, patch); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
		return ctrl.Result{RequeueAfter: r.calculateRequeueAfter(ansibleJob)}, nil
	}

	if !errors.IsNotFound(err) {
		logger.Error(err, "Failed to get Job")
		return ctrl.Result{}, err
	}

	// Create inventory ConfigMap if using inline inventory
	if ansibleJob.Spec.Inventory.Inline != "" {
		if createErr := r.createInventoryConfigMap(ctx, ansibleJob); createErr != nil {
			logger.Error(createErr, "Failed to create inventory ConfigMap")
			return ctrl.Result{}, createErr
		}
	}

	// Create the Kubernetes job to run the Ansible playbook
	job := r.createAnsibleJob(ansibleJob)

	// Set controller reference
	if setRefErr := controllerutil.SetControllerReference(ansibleJob, job, r.Scheme); setRefErr != nil {
		logger.Error(setRefErr, "Failed to set controller reference")
		return ctrl.Result{}, setRefErr
	}

	// Create or patch the job using client.Patch semantics
	if _, createJobErr := controllerutil.CreateOrPatch(ctx, r.Client, job, func() error {
		// Ensure controller reference is set in the mutation function
		return controllerutil.SetControllerReference(ansibleJob, job, r.Scheme)
	}); createJobErr != nil {
		logger.Error(createJobErr, "Failed to create or patch Job")
		return ctrl.Result{}, createJobErr
	}

	logger.Info("Created Kubernetes Job", "job", job.Name)

	// Update status with both job name and phase in single call
	patch := client.MergeFrom(ansibleJob.DeepCopy())

	ansibleJob.Status.JobName = job.Name
	ansibleJob.Status.Phase = ansiblev1alpha1.AnsibleJobPhaseRunning
	ansibleJob.Status.ObservedGeneration = ansibleJob.Generation

	// Update conditions for running phase
	r.updateConditionsForPhase(ansibleJob, ansiblev1alpha1.AnsibleJobPhaseRunning)

	if statusUpdateErr := r.Status().Patch(ctx, ansibleJob, patch); statusUpdateErr != nil {
		return ctrl.Result{}, statusUpdateErr
	}

	return ctrl.Result{RequeueAfter: r.calculateRequeueAfter(ansibleJob)}, nil
}

func (r *AnsibleJobReconciler) monitorJob(ctx context.Context, ansibleJob *ansiblev1alpha1.AnsibleJob) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Get the job
	job := &batchv1.Job{}
	err := r.Get(ctx, types.NamespacedName{Name: ansibleJob.Status.JobName, Namespace: ansibleJob.Namespace}, job)
	if err != nil {
		logger.Error(err, "Failed to get Job")
		return ctrl.Result{}, err
	}

	// Check job status and update only if phase changes
	var newPhase ansiblev1alpha1.AnsibleJobPhase
	var completionTime *metav1.Time

	if job.Status.CompletionTime != nil {
		// Job completed successfully
		newPhase = ansiblev1alpha1.AnsibleJobPhaseSucceeded
		completionTime = job.Status.CompletionTime
	} else if job.Status.Failed > 0 {
		// Job failed
		newPhase = ansiblev1alpha1.AnsibleJobPhaseFailed
		completionTime = &metav1.Time{Time: time.Now()}
	} else {
		// Job still running - no status update needed
		return ctrl.Result{RequeueAfter: r.calculateRequeueAfter(ansibleJob)}, nil
	}

	// Only update status if phase actually changed
	if ansibleJob.Status.Phase != newPhase {
		patch := client.MergeFrom(ansibleJob.DeepCopy())

		ansibleJob.Status.Phase = newPhase
		ansibleJob.Status.CompletionTime = completionTime
		ansibleJob.Status.ObservedGeneration = ansibleJob.Generation

		// Update conditions for the new phase
		r.updateConditionsForPhase(ansibleJob, newPhase)

		// Update status with patch
		if updateErr := r.Status().Patch(ctx, ansibleJob, patch); updateErr != nil {
			return ctrl.Result{}, updateErr
		}
	}

	logger.Info("Job monitoring complete", "phase", ansibleJob.Status.Phase)
	return ctrl.Result{}, nil
}

// createInventoryConfigMap creates a ConfigMap for inline inventory
func (r *AnsibleJobReconciler) createInventoryConfigMap(ctx context.Context, ansibleJob *ansiblev1alpha1.AnsibleJob) error {
	logger := log.FromContext(ctx)

	configMapName := fmt.Sprintf("%s-inventory", ansibleJob.Name)

	// Check if ConfigMap already exists
	existingConfigMap := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{Name: configMapName, Namespace: ansibleJob.Namespace}, existingConfigMap)
	if err == nil {
		// ConfigMap already exists
		logger.Info("Inventory ConfigMap already exists", "configMap", configMapName)
		return nil
	}

	if !errors.IsNotFound(err) {
		return fmt.Errorf("failed to check for existing ConfigMap: %w", err)
	}

	// Create the ConfigMap
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: ansibleJob.Namespace,
			Labels: map[string]string{
				"app":         "ansible-runner",
				"ansible-job": ansibleJob.Name,
			},
		},
		Data: map[string]string{
			"hosts": ansibleJob.Spec.Inventory.Inline,
		},
	}

	// Set controller reference so ConfigMap gets cleaned up when AnsibleJob is deleted
	if setRefErr := controllerutil.SetControllerReference(ansibleJob, configMap, r.Scheme); setRefErr != nil {
		return fmt.Errorf("failed to set controller reference: %w", setRefErr)
	}

	// Create or patch the ConfigMap using client.Patch semantics
	if _, createErr := controllerutil.CreateOrPatch(ctx, r.Client, configMap, func() error {
		// Ensure controller reference and data are set in the mutation function
		configMap.Data = map[string]string{
			"hosts": ansibleJob.Spec.Inventory.Inline,
		}
		return controllerutil.SetControllerReference(ansibleJob, configMap, r.Scheme)
	}); createErr != nil {
		return fmt.Errorf("failed to create or patch ConfigMap: %w", createErr)
	}

	logger.Info("Created inventory ConfigMap", "configMap", configMapName)
	return nil
}

// calculateRequeueAfter returns adaptive requeue timing based on job age and phase
func (r *AnsibleJobReconciler) calculateRequeueAfter(ansibleJob *ansiblev1alpha1.AnsibleJob) time.Duration {
	if ansibleJob.Status.StartTime == nil {
		return 5 * time.Second // Quick initial check for new jobs
	}

	age := time.Since(ansibleJob.Status.StartTime.Time)
	switch {
	case age < 2*time.Minute:
		return 10 * time.Second // Poll frequently for new jobs
	case age < 10*time.Minute:
		return 30 * time.Second // Standard polling for active jobs
	default:
		return 60 * time.Second // Slower polling for long-running jobs
	}
}

// calculateBackoffDelay returns exponential backoff delay for error scenarios
func (r *AnsibleJobReconciler) calculateBackoffDelay(retryCount int) time.Duration {
	if retryCount <= 0 {
		return 5 * time.Second
	}

	// Cap retry count to prevent integer overflow in bit shifting
	// For retryCount > 20, we would already hit the 5-minute cap anyway
	if retryCount > 20 {
		return 5 * time.Minute
	}

	// Exponential backoff: 5s, 10s, 20s, 40s, capped at 5 minutes
	// Use safer bit shifting to avoid gosec G115 integer overflow warning
	if retryCount <= 1 {
		return 5 * time.Second
	}
	//nolint:gosec // retryCount is already bounds-checked above
	shiftValue := uint(retryCount - 1)
	if shiftValue > 63 { // Cap to prevent overflow on 64-bit systems
		shiftValue = 63
	}
	delay := time.Duration(5) * time.Second * time.Duration(1<<shiftValue)
	maxDelay := 5 * time.Minute
	if delay > maxDelay {
		return maxDelay
	}
	return delay
}

// getRetryCountFromConditions extracts retry count from conditions for exponential backoff
func (r *AnsibleJobReconciler) getRetryCountFromConditions(ansibleJob *ansiblev1alpha1.AnsibleJob) int {
	// For now, use a simple heuristic based on existing status
	// In a more complete implementation, this would track retry attempts in conditions
	if ansibleJob.Status.StartTime != nil {
		age := time.Since(ansibleJob.Status.StartTime.Time)
		// Rough estimate: more failed attempts for older jobs that haven't progressed
		if age > 5*time.Minute && ansibleJob.Status.Phase == ansiblev1alpha1.AnsibleJobPhasePending {
			return 3 // High retry count for stuck jobs
		} else if age > 2*time.Minute && ansibleJob.Status.Phase == ansiblev1alpha1.AnsibleJobPhasePending {
			return 1 // Some retries for slow jobs
		}
	}
	return 0 // No retries for new jobs
}

// SetupWithManager sets up the controller with the Manager.
func (r *AnsibleJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&ansiblev1alpha1.AnsibleJob{}).
		Owns(&batchv1.Job{}).
		Owns(&corev1.ConfigMap{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 5, // Optimize for concurrent ansible job reconciliation
		}).
		Complete(r)
}
