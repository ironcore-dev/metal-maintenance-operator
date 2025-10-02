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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	maintencev1alpha1 "github.com/ironcore-dev/maintenance-operator/api/v1alpha1"
)

// AnsibleJobReconciler reconciles a AnsibleJob object
type AnsibleJobReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=ansiblejobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=ansiblejobs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=maintenance.metal.ironcore.dev,resources=ansiblejobs/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *AnsibleJobReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the AnsibleJob instance
	var ansibleJob maintencev1alpha1.AnsibleJob
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
	case maintencev1alpha1.AnsibleJobPhasePending:
		// Check if job should be created
		return r.createKubernetesJob(ctx, &ansibleJob)
	case maintencev1alpha1.AnsibleJobPhaseRunning:
		// Monitor the running job
		return r.monitorJob(ctx, &ansibleJob)
	case maintencev1alpha1.AnsibleJobPhaseSucceeded, maintencev1alpha1.AnsibleJobPhaseFailed:
		// Job is complete, nothing to do
		return ctrl.Result{}, nil
	default:
		logger.Info("Unknown phase", "phase", ansibleJob.Status.Phase)
		return ctrl.Result{}, nil
	}
}

func (r *AnsibleJobReconciler) initializeJob(ctx context.Context, ansibleJob *maintencev1alpha1.AnsibleJob) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Initializing AnsibleJob", "name", ansibleJob.Name)

	// Initialize job status
	ansibleJob.Status.Phase = maintencev1alpha1.AnsibleJobPhasePending
	ansibleJob.Status.StartTime = &metav1.Time{Time: time.Now()}

	if err := r.Status().Update(ctx, ansibleJob); err != nil {
		logger.Error(err, "Failed to update AnsibleJob status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{Requeue: true}, nil
}

func (r *AnsibleJobReconciler) createKubernetesJob(ctx context.Context, ansibleJob *maintencev1alpha1.AnsibleJob) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Check if job already exists
	existingJob := &batchv1.Job{}
	jobName := fmt.Sprintf("%s-job", ansibleJob.Name)
	err := r.Get(ctx, types.NamespacedName{Name: jobName, Namespace: ansibleJob.Namespace}, existingJob)

	if err == nil {
		// Job already exists, update status to running
		ansibleJob.Status.JobName = jobName
		ansibleJob.Status.Phase = maintencev1alpha1.AnsibleJobPhaseRunning
		if err := r.Status().Update(ctx, ansibleJob); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	if !errors.IsNotFound(err) {
		logger.Error(err, "Failed to get Job")
		return ctrl.Result{}, err
	}

	// Create inventory ConfigMap if using inline inventory
	if ansibleJob.Spec.Inventory.Inline != "" {
		if err := r.createInventoryConfigMap(ctx, ansibleJob); err != nil {
			logger.Error(err, "Failed to create inventory ConfigMap")
			return ctrl.Result{}, err
		}
	}

	// Create the Kubernetes job to run the Ansible playbook
	job := r.createAnsibleJob(ansibleJob)

	// Set controller reference
	if err := controllerutil.SetControllerReference(ansibleJob, job, r.Scheme); err != nil {
		logger.Error(err, "Failed to set controller reference")
		return ctrl.Result{}, err
	}

	// Create the job
	if err := r.Create(ctx, job); err != nil {
		logger.Error(err, "Failed to create Job")
		return ctrl.Result{}, err
	}

	logger.Info("Created Kubernetes Job", "job", job.Name)

	// Update status
	ansibleJob.Status.JobName = job.Name
	ansibleJob.Status.Phase = maintencev1alpha1.AnsibleJobPhaseRunning
	if err := r.Status().Update(ctx, ansibleJob); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
}

func (r *AnsibleJobReconciler) monitorJob(ctx context.Context, ansibleJob *maintencev1alpha1.AnsibleJob) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Get the job
	job := &batchv1.Job{}
	err := r.Get(ctx, types.NamespacedName{Name: ansibleJob.Status.JobName, Namespace: ansibleJob.Namespace}, job)
	if err != nil {
		logger.Error(err, "Failed to get Job")
		return ctrl.Result{}, err
	}

	// Check job status
	if job.Status.CompletionTime != nil {
		// Job completed successfully
		ansibleJob.Status.Phase = maintencev1alpha1.AnsibleJobPhaseSucceeded
		ansibleJob.Status.CompletionTime = job.Status.CompletionTime
		ansibleJob.Status.Message = "Job completed successfully"
	} else if job.Status.Failed > 0 {
		// Job failed
		ansibleJob.Status.Phase = maintencev1alpha1.AnsibleJobPhaseFailed
		ansibleJob.Status.CompletionTime = &metav1.Time{Time: time.Now()}
		ansibleJob.Status.Message = "Job failed"
	} else {
		// Job still running
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Update status
	if err := r.Status().Update(ctx, ansibleJob); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("Job monitoring complete", "phase", ansibleJob.Status.Phase)
	return ctrl.Result{}, nil
}

// createInventoryConfigMap creates a ConfigMap for inline inventory
func (r *AnsibleJobReconciler) createInventoryConfigMap(ctx context.Context, ansibleJob *maintencev1alpha1.AnsibleJob) error {
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
	if err := controllerutil.SetControllerReference(ansibleJob, configMap, r.Scheme); err != nil {
		return fmt.Errorf("failed to set controller reference: %w", err)
	}

	// Create the ConfigMap
	if err := r.Create(ctx, configMap); err != nil {
		return fmt.Errorf("failed to create ConfigMap: %w", err)
	}

	logger.Info("Created inventory ConfigMap", "configMap", configMapName)
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *AnsibleJobReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&maintencev1alpha1.AnsibleJob{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}
