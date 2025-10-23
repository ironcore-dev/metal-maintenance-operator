// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	ansiblev1alpha1 "github.com/ironcore-dev/maintenance-operator/api/ansible/v1alpha1"
)

const (
	// InventoryVolumeName is the name of the inventory volume
	InventoryVolumeName = "inventory"
)

// CreateTestAnsibleJob creates a simple AnsibleJob for testing
func CreateTestAnsibleJob(name, namespace string) *ansiblev1alpha1.AnsibleJob {
	return &ansiblev1alpha1.AnsibleJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: ansiblev1alpha1.AnsibleJobSpec{
			Playbook: ansiblev1alpha1.PlaybookSpec{
				Name:       "hello-world.yml",
				Repository: "https://github.com/example/playbooks.git",
			},
			Inventory: ansiblev1alpha1.AnsibleInventory{
				Inline: `
[web_servers]
web1 ansible_host=10.0.1.10
`,
			},
		},
	}
}

// CreateTestJob creates a Kubernetes Job that simulates what the controller would create
func CreateTestJob(name, namespace, ansibleJobName string) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				"app":         "ansible-runner",
				"ansible-job": ansibleJobName,
			},
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					Containers: []corev1.Container{
						{
							Name: "ansible-runner",
							Image: "quay.io/ansible/ansible-runner:stable-2.12-latest@sha256:" +
								"001a4bde411be863d54c1d293f3d2e7b0ff0e67ef5d7b2f9f7fb56b61694f4e8",
						},
					},
				},
			},
		},
	}
}

// CreateTestConfigMap creates a ConfigMap for inventory testing
func CreateTestConfigMap(name, namespace string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: map[string]string{
			"hosts": `
[web_servers]
web1 ansible_host=10.0.1.10
web2 ansible_host=10.0.1.11

[db_servers]
db1 ansible_host=10.0.2.10
`,
		},
	}
}

// CreateTestSecret creates a Secret for credential testing
func CreateTestSecret(name, namespace string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"username": []byte("test-user"),
			"password": []byte("test-password"),
		},
	}
}

// SetJobStatusCompleted sets a job status to completed for testing
func SetJobStatusCompleted(job *batchv1.Job, successful bool) {
	now := metav1.Now()
	if successful {
		job.Status.Succeeded = 1
		job.Status.CompletionTime = &now
	} else {
		job.Status.Failed = 1
	}
}

// GetContainerArgs extracts container arguments for testing
func GetContainerArgs(job *batchv1.Job, containerName string) []string {
	for _, container := range job.Spec.Template.Spec.Containers {
		if container.Name == containerName {
			return container.Args
		}
	}
	return nil
}

// GetInitContainerArgs extracts init container arguments for testing
func GetInitContainerArgs(job *batchv1.Job, containerName string) []string {
	for _, container := range job.Spec.Template.Spec.InitContainers {
		if container.Name == containerName {
			return container.Args
		}
	}
	return nil
}
