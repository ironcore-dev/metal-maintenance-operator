// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"encoding/json"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	maintencev1alpha1 "github.com/ironcore-dev/maintenance-operator/api/v1alpha1"
)

const (
	defaultAnsibleRunnerImage = "quay.io/ansible/ansible-runner:latest"
	defaultServiceAccount     = "default"
	defaultBackoffLimit       = int32(1)
)

func (r *AnsibleJobReconciler) createAnsibleJob(ansibleJob *maintencev1alpha1.AnsibleJob) *batchv1.Job {
	jobName := fmt.Sprintf("%s-job", ansibleJob.Name)

	// Get image from spec or use default
	image := defaultAnsibleRunnerImage
	if ansibleJob.Spec.JobTemplate != nil && ansibleJob.Spec.JobTemplate.Image != "" {
		image = ansibleJob.Spec.JobTemplate.Image
	}

	// Get service account
	serviceAccount := defaultServiceAccount
	if ansibleJob.Spec.JobTemplate != nil && ansibleJob.Spec.JobTemplate.ServiceAccountName != "" {
		serviceAccount = ansibleJob.Spec.JobTemplate.ServiceAccountName
	}

	// Get backoff limit
	backoffLimit := defaultBackoffLimit
	if ansibleJob.Spec.JobTemplate != nil && ansibleJob.Spec.JobTemplate.BackoffLimit != nil {
		backoffLimit = *ansibleJob.Spec.JobTemplate.BackoffLimit
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: ansibleJob.Namespace,
			Labels: map[string]string{
				"app":         "ansible-runner",
				"ansible-job": ansibleJob.Name,
				"maintenance.metal.ironcore.dev/managed-by": "maintenance-operator",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoffLimit,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":         "ansible-runner",
						"ansible-job": ansibleJob.Name,
					},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: serviceAccount,
					RestartPolicy:      corev1.RestartPolicyNever,
					InitContainers:     r.createInitContainers(ansibleJob),
					Containers:         r.createAnsibleRunnerContainer(ansibleJob, image),
					Volumes:            r.createVolumes(ansibleJob),
				},
			},
		},
	}

	return job
}

func (r *AnsibleJobReconciler) createInitContainers(ansibleJob *maintencev1alpha1.AnsibleJob) []corev1.Container {
	// Single init container to prepare ansible-runner environment
	setupCommand := fmt.Sprintf(`
		# Create ansible-runner directory structure
		mkdir -p /runner/project /runner/inventory /runner/env

		# Clone playbook repository directly to project
		git clone %s /runner/project
		cd /runner/project
		if [ -n "%s" ]; then
			git checkout %s
		fi

		# Clone roles if specified
		if [ -n "%s" ]; then
			mkdir -p /runner/project/roles
			git clone %s /tmp/roles
			cd /tmp/roles
			if [ -n "%s" ]; then
				git checkout %s
			fi
			# Copy roles to project (assuming standard roles/ structure)
			cp -r roles/* /runner/project/roles/ 2>/dev/null || cp -r . /runner/project/roles/
		fi`,
		ansibleJob.Spec.PlaybookRepo,
		ansibleJob.Spec.PlaybookGitRef,
		ansibleJob.Spec.PlaybookGitRef,
		ansibleJob.Spec.RolesRepo,
		ansibleJob.Spec.RolesRepo,
		ansibleJob.Spec.RolesGitRef,
		ansibleJob.Spec.RolesGitRef,
	)

	// Add extra vars if they exist
	if len(ansibleJob.Spec.ExtraVars) > 0 {
		extraVarsMap := make(map[string]string)
		for _, kv := range ansibleJob.Spec.ExtraVars {
			extraVarsMap[kv.Name] = kv.Value
		}
		if extraVarsJSON, err := json.Marshal(extraVarsMap); err == nil {
			setupCommand += fmt.Sprintf(`
		# Create extravars file for ansible-runner
		cat > /runner/env/extravars << 'EOF'
%s
EOF`, string(extraVarsJSON))
		}
	}

	return []corev1.Container{{
		Name:    "setup-ansible-runner",
		Image:   "alpine/git:latest",
		Command: []string{"/bin/sh", "-c"},
		Args:    []string{setupCommand},
		VolumeMounts: []corev1.VolumeMount{{
			Name:      "runner-workspace",
			MountPath: "/runner",
		}},
		// Resource constraints for init container
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    *parseQuantity("50m"),
				corev1.ResourceMemory: *parseQuantity("128Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    *parseQuantity("200m"),
				corev1.ResourceMemory: *parseQuantity("256Mi"),
			},
		},
	}}
}

func (r *AnsibleJobReconciler) createAnsibleRunnerContainer(ansibleJob *maintencev1alpha1.AnsibleJob, image string) []corev1.Container {
	args := []string{
		"run",
		"/runner",
		"--playbook", ansibleJob.Spec.Playbook,
	}

	// Add inventory source
	if ansibleJob.Spec.Inventory.Inline != "" {
		args = append(args, "--inventory", "/runner/inventory/hosts")
	} else if ansibleJob.Spec.Inventory.ConfigMapRef != nil {
		args = append(args, "--inventory", "/runner/inventory/hosts")
	} else if ansibleJob.Spec.Inventory.SecretRef != nil {
		args = append(args, "--inventory", "/runner/inventory/hosts")
	}

	// Add limit if specified
	if ansibleJob.Spec.Limit != "" {
		args = append(args, "--limit", ansibleJob.Spec.Limit)
	}

	container := corev1.Container{
		Name:    "ansible-runner",
		Image:   image,
		Command: []string{"ansible-runner"},
		Args:    args,
		VolumeMounts: []corev1.VolumeMount{{
			Name:      "runner-workspace",
			MountPath: "/runner",
		}},
	}

	// Add inventory volume mount if needed
	if r.needsInventoryMount(ansibleJob) {
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      "inventory",
			MountPath: "/runner/inventory",
		})
	}

	// Add resources if specified, otherwise use sensible defaults
	if ansibleJob.Spec.JobTemplate != nil && ansibleJob.Spec.JobTemplate.Resources != nil {
		container.Resources = corev1.ResourceRequirements{
			Limits:   convertToResourceList(ansibleJob.Spec.JobTemplate.Resources.Limits),
			Requests: convertToResourceList(ansibleJob.Spec.JobTemplate.Resources.Requests),
		}
	} else {
		// Apply sensible default resource constraints to prevent resource exhaustion
		container.Resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    *parseQuantity("100m"),
				corev1.ResourceMemory: *parseQuantity("256Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    *parseQuantity("500m"),
				corev1.ResourceMemory: *parseQuantity("512Mi"),
			},
		}
	}

	return []corev1.Container{container}
}

func (r *AnsibleJobReconciler) needsInventoryMount(ansibleJob *maintencev1alpha1.AnsibleJob) bool {
	return ansibleJob.Spec.Inventory.Inline != "" ||
		ansibleJob.Spec.Inventory.ConfigMapRef != nil ||
		ansibleJob.Spec.Inventory.SecretRef != nil
}

func (r *AnsibleJobReconciler) createVolumes(ansibleJob *maintencev1alpha1.AnsibleJob) []corev1.Volume {
	volumes := []corev1.Volume{{
		Name: "runner-workspace",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}}

	// Add inventory volume based on source type
	if ansibleJob.Spec.Inventory.Inline != "" {
		volumes = append(volumes, corev1.Volume{
			Name: "inventory",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: fmt.Sprintf("%s-inventory", ansibleJob.Name),
					},
				},
			},
		})
	} else if ansibleJob.Spec.Inventory.ConfigMapRef != nil {
		volumes = append(volumes, corev1.Volume{
			Name: "inventory",
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: ansibleJob.Spec.Inventory.ConfigMapRef.Name,
					},
				},
			},
		})
	} else if ansibleJob.Spec.Inventory.SecretRef != nil {
		volumes = append(volumes, corev1.Volume{
			Name: "inventory",
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName: ansibleJob.Spec.Inventory.SecretRef.Name,
				},
			},
		})
	}

	return volumes
}

func convertToResourceList(resources []maintencev1alpha1.ResourceQuantity) corev1.ResourceList {
	if resources == nil {
		return nil
	}

	resourceList := make(corev1.ResourceList)
	for _, res := range resources {
		resourceList[corev1.ResourceName(res.Name)] = *parseQuantity(res.Quantity)
	}
	return resourceList
}

func parseQuantity(value string) *resource.Quantity {
	if q, err := resource.ParseQuantity(value); err == nil {
		return &q
	}
	// Return default if parsing fails
	return &resource.Quantity{}
}
