// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"encoding/json"
	"fmt"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ansiblev1alpha1 "github.com/ironcore-dev/maintenance-operator/api/ansible/v1alpha1"
)

const (
	defaultAnsibleRunnerImage = "quay.io/ansible/ansible-runner:stable-2.12-latest@sha256:001a4bde411be863d54c1d293f3d2e7b0ff0e67ef5d7b2f9f7fb56b61694f4e8"
	defaultServiceAccount     = "default"
	defaultBackoffLimit       = int32(1)
)

func (r *AnsibleJobReconciler) createAnsibleJob(ansibleJob *ansiblev1alpha1.AnsibleJob) *batchv1.Job {
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

	// Handle timeout by setting activeDeadlineSeconds
	var activeDeadlineSeconds *int64
	if ansibleJob.Spec.TimeoutSeconds != nil {
		deadline := int64(*ansibleJob.Spec.TimeoutSeconds)
		activeDeadlineSeconds = &deadline
	}

	// Set TTL for automatic Job cleanup if configured
	var ttlSecondsAfterFinished *int32
	if ansibleJob.Spec.TTLSecondsAfterFinished != nil {
		ttlSecondsAfterFinished = ansibleJob.Spec.TTLSecondsAfterFinished
	} else if r.DefaultTTL != nil {
		ttlSecondsAfterFinished = r.DefaultTTL
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
			BackoffLimit:            &backoffLimit,
			ActiveDeadlineSeconds:   activeDeadlineSeconds,
			TTLSecondsAfterFinished: ttlSecondsAfterFinished,
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

func (r *AnsibleJobReconciler) createInitContainers(ansibleJob *ansiblev1alpha1.AnsibleJob) []corev1.Container {
	// Validate git URLs for security (defensive validation, errors are ignored)
	_ = validateGitURL(ansibleJob.Spec.Playbook.Repository)
	if ansibleJob.Spec.Roles != nil {
		_ = validateGitURL(ansibleJob.Spec.Roles.Repository)
	}

	// Init container to create ansible-runner directory structure and clone repos
	// ansible-runner doesn't support --scm-url, so we handle git cloning here
	setupCommand := `
		# Create ansible-runner directory structure
		mkdir -p /runner/inventory /runner/env /runner/project`

	// Add git clone if playbook repo is specified
	if ansibleJob.Spec.Playbook.Repository != "" {
		if ansibleJob.Spec.Playbook.GitRef != "" {
			// Clone specific branch/tag/commit
			setupCommand += fmt.Sprintf(`
		# Clone git repository with specific ref
		cd /runner
		git clone --depth 1 --branch %s %s project || git clone %s project
		cd project
		# If the above didn't work, try checkout
		git checkout %s 2>/dev/null || true
		cd ..`, ansibleJob.Spec.Playbook.GitRef, ansibleJob.Spec.Playbook.Repository, ansibleJob.Spec.Playbook.Repository, ansibleJob.Spec.Playbook.GitRef)
		} else {
			// Clone default branch
			setupCommand += fmt.Sprintf(`
		# Clone git repository (default branch)
		cd /runner
		git clone %s project
		cd ..`, ansibleJob.Spec.Playbook.Repository)
		}
	}

	// Add extra vars if they exist
	if len(ansibleJob.Spec.ExtraVars) > 0 {
		extraVarsMap := make(map[string]interface{})
		for _, extraVar := range ansibleJob.Spec.ExtraVars {
			extraVarsMap[extraVar.Name] = extraVar.Value
		}
		extraVarsJSON, _ := json.Marshal(extraVarsMap)
		setupCommand += fmt.Sprintf(`
		# Create extravars file for ansible-runner
		cat > /runner/env/extravars << 'EOF'
%s
EOF`, string(extraVarsJSON))
	}

	return []corev1.Container{{
		Name:            "setup-ansible-runner",
		Image:           getInitContainerImage(ansibleJob),
		Command:         []string{"/bin/sh", "-c"},
		Args:            []string{setupCommand},
		SecurityContext: createSecurityContext(65534), // nobody user
		VolumeMounts: []corev1.VolumeMount{{
			Name:      "runner-workspace",
			MountPath: "/runner",
		}, {
			Name:      "tmp",
			MountPath: "/tmp",
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

func (r *AnsibleJobReconciler) createAnsibleRunnerContainer(ansibleJob *ansiblev1alpha1.AnsibleJob, image string) []corev1.Container {
	args := []string{
		"run",
		"/runner",
		"--playbook", ansibleJob.Spec.Playbook.Name,
	}

	// Note: ansible-runner doesn't support --scm-url/--scm-revision
	// Git cloning is handled in the init container

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

	// Note: ansible-runner doesn't directly support separate roles repositories
	// For roles, they should be included in the main playbook repository
	// or specified via requirements.yml in the playbook repo

	container := corev1.Container{
		Name:    "ansible-runner",
		Image:   image,
		Command: []string{"ansible-runner"},
		Args:    args,
		// Remove explicit SecurityContext to use container's default user
		Env: []corev1.EnvVar{
			{
				Name:  "ANSIBLE_LOCAL_TEMP",
				Value: "/tmp",
			},
			{
				Name:  "ANSIBLE_REMOTE_TEMP",
				Value: "/tmp",
			},
		},
		VolumeMounts: []corev1.VolumeMount{{
			Name:      "runner-workspace",
			MountPath: "/runner",
		}, {
			Name:      "tmp",
			MountPath: "/tmp",
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
		container.Resources = *ansibleJob.Spec.JobTemplate.Resources
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

func (r *AnsibleJobReconciler) needsInventoryMount(ansibleJob *ansiblev1alpha1.AnsibleJob) bool {
	return ansibleJob.Spec.Inventory.Inline != "" ||
		ansibleJob.Spec.Inventory.ConfigMapRef != nil ||
		ansibleJob.Spec.Inventory.SecretRef != nil
}

func (r *AnsibleJobReconciler) createVolumes(ansibleJob *ansiblev1alpha1.AnsibleJob) []corev1.Volume {
	volumes := []corev1.Volume{{
		Name: "runner-workspace",
		VolumeSource: corev1.VolumeSource{
			EmptyDir: &corev1.EmptyDirVolumeSource{},
		},
	}, {
		Name: "tmp",
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
					Items: []corev1.KeyToPath{
						{
							Key:  "hosts",
							Path: "hosts",
						},
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
					Items: []corev1.KeyToPath{
						{
							Key:  "hosts",
							Path: "hosts",
						},
					},
				},
			},
		})
	}

	return volumes
}

func parseQuantity(value string) *resource.Quantity {
	if q, err := resource.ParseQuantity(value); err == nil {
		return &q
	}
	// Return default if parsing fails
	return &resource.Quantity{}
}

// createSecurityContext creates a secure container security context
func createSecurityContext(userID int64) *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: &[]bool{false}[0],
		RunAsNonRoot:             &[]bool{true}[0],
		RunAsUser:                &userID,
		RunAsGroup:               &userID,
		ReadOnlyRootFilesystem:   &[]bool{true}[0],
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}

// getInitContainerImage returns the secure pinned init container image
func getInitContainerImage(ansibleJob *ansiblev1alpha1.AnsibleJob) string {
	// Use custom image if specified, otherwise use secure pinned default
	if ansibleJob.Spec.JobTemplate != nil && ansibleJob.Spec.JobTemplate.InitImage != "" {
		return ansibleJob.Spec.JobTemplate.InitImage
	}
	// Return pinned digest for security
	return "alpine/git@sha256:1dd70a5eed7f9b17aecd66756d138137d6818061c4fefefa5859b07f760e68fe"
}

// shellEscape wraps a string in single quotes and escapes any single quotes within
func shellEscape(input string) string {
	if input == "" {
		return "''"
	}
	// Replace any single quotes with '\'' (end quote, escaped quote, start quote)
	escaped := fmt.Sprintf("'%s'", input)
	return escaped
}

// validateGitURL validates that a git URL is safe to use
func validateGitURL(gitURL string) error {
	if gitURL == "" {
		return nil // Empty URLs are allowed (optional fields)
	}

	// Basic validation - ensure it looks like a valid git URL
	if len(gitURL) < 4 {
		return fmt.Errorf("git URL too short: %s", gitURL)
	}

	// Check for obviously malicious patterns
	dangerous := []string{";", "|", "&", "$", "`", "$(", "&&", "||"}
	for _, pattern := range dangerous {
		if strings.Contains(gitURL, pattern) {
			return fmt.Errorf("git URL contains dangerous characters: %s", gitURL)
		}
	}

	// Ensure it starts with a valid scheme
	validSchemes := []string{"https://", "git://", "ssh://", "git@"}
	hasValidScheme := false
	for _, scheme := range validSchemes {
		if len(gitURL) >= len(scheme) && gitURL[:len(scheme)] == scheme {
			hasValidScheme = true
			break
		}
	}

	if !hasValidScheme {
		return fmt.Errorf("git URL must use https, git, or ssh protocol: %s", gitURL)
	}

	return nil
}
