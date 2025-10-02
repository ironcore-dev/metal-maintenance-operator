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
	var initContainers []corev1.Container

	// Init container to clone playbook repository
	initContainers = append(initContainers, corev1.Container{
		Name:    "clone-playbooks",
		Image:   "alpine/git:latest",
		Command: []string{"/bin/sh", "-c"},
		Args: []string{
			fmt.Sprintf(`
				git clone %s /tmp/playbooks
				if [ -n "%s" ]; then
					cd /tmp/playbooks && git checkout %s
				fi
			`, ansibleJob.Spec.PlaybookRepo, ansibleJob.Spec.PlaybookGitRef, ansibleJob.Spec.PlaybookGitRef),
		},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "repos",
				MountPath: "/tmp",
			},
		},
	})

	// Init container to clone roles repository if specified
	if ansibleJob.Spec.RolesRepo != "" {
		initContainers = append(initContainers, corev1.Container{
			Name:    "clone-roles",
			Image:   "alpine/git:latest",
			Command: []string{"/bin/sh", "-c"},
			Args: []string{
				fmt.Sprintf(`
					git clone %s /tmp/roles
					if [ -n "%s" ]; then
						cd /tmp/roles && git checkout %s
					fi
				`, ansibleJob.Spec.RolesRepo, ansibleJob.Spec.RolesGitRef, ansibleJob.Spec.RolesGitRef),
			},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      "repos",
					MountPath: "/tmp",
				},
			},
		})
	}

	// Init container to setup ansible-runner directory structure
	setupCommand := `
		mkdir -p /runner/project /runner/inventory /runner/env
		# Copy playbooks to the project root directory (not in a playbooks subdirectory)
		cp -r /tmp/playbooks/* /runner/project/ 2>/dev/null || true
		if [ -d "/tmp/roles" ]; then
			mkdir -p /runner/project/roles
			cp -r /tmp/roles/roles/* /runner/project/roles/ 2>/dev/null || true
		fi
	`

	// Add extra vars to the setup if they exist
	if len(ansibleJob.Spec.ExtraVars) > 0 {
		// Convert KeyValue list to map for JSON marshaling
		extraVarsMap := make(map[string]string)
		for _, kv := range ansibleJob.Spec.ExtraVars {
			extraVarsMap[kv.Name] = kv.Value
		}
		extraVarsJSON, err := json.Marshal(extraVarsMap)
		if err == nil {
			// Create the extravars file that ansible-runner will automatically pick up
			setupCommand += fmt.Sprintf(`
		cat > /runner/env/extravars << 'EOF'
%s
EOF
			`, string(extraVarsJSON))
		}
	}

	initContainers = append(initContainers, corev1.Container{
		Name:    "setup-runner",
		Image:   "busybox:latest",
		Command: []string{"/bin/sh", "-c"},
		Args:    []string{setupCommand},
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "repos",
				MountPath: "/tmp",
			},
			{
				Name:      "runner-input",
				MountPath: "/runner",
			},
		},
	})

	return initContainers
}

func (r *AnsibleJobReconciler) createAnsibleRunnerContainer(ansibleJob *maintencev1alpha1.AnsibleJob, image string) []corev1.Container {
	args := []string{
		"run",
		"/runner",
		"--playbook", ansibleJob.Spec.Playbook,
	}

	// Add inventory if specified
	if ansibleJob.Spec.Inventory.Inline != "" {
		args = append(args, "--inventory", "/runner/inventory/hosts")
	}

	// Add limit if specified
	if ansibleJob.Spec.Limit != "" {
		args = append(args, "--limit", ansibleJob.Spec.Limit)
	}

	// Note: Extra vars are handled via the extra_vars.json file created in init container
	// ansible-runner will automatically pick up /runner/env/extravars if it exists

	container := corev1.Container{
		Name:    "ansible-runner",
		Image:   image,
		Command: []string{"ansible-runner"},
		Args:    args,
		VolumeMounts: []corev1.VolumeMount{
			{
				Name:      "runner-input",
				MountPath: "/runner",
			},
		},
	}

	// Add inventory volume mount if using inline inventory
	if ansibleJob.Spec.Inventory.Inline != "" {
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      "inventory",
			MountPath: "/runner/inventory",
		})
	}

	// Add resources if specified
	if ansibleJob.Spec.JobTemplate != nil && ansibleJob.Spec.JobTemplate.Resources != nil {
		container.Resources = corev1.ResourceRequirements{
			Limits:   convertToResourceList(ansibleJob.Spec.JobTemplate.Resources.Limits),
			Requests: convertToResourceList(ansibleJob.Spec.JobTemplate.Resources.Requests),
		}
	}

	return []corev1.Container{container}
}

func (r *AnsibleJobReconciler) createVolumes(ansibleJob *maintencev1alpha1.AnsibleJob) []corev1.Volume {
	volumes := []corev1.Volume{
		{
			Name: "repos",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
		{
			Name: "runner-input",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	}

	// Add inventory volume if inline inventory is specified
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
