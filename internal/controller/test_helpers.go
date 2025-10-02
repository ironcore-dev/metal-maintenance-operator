// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	maintencev1alpha1 "github.com/ironcore-dev/maintenance-operator/api/v1alpha1"
)

// TestHelpers provides utility functions for testing

// CreateTestAnsibleJob creates a basic AnsibleJob for testing
func CreateTestAnsibleJob(name, namespace string) *maintencev1alpha1.AnsibleJob {
	return &maintencev1alpha1.AnsibleJob{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: maintencev1alpha1.AnsibleJobSpec{
			Playbook:     "test.yml",
			PlaybookRepo: "https://github.com/test/playbooks.git",
			RolesRepo:    "https://github.com/test/roles.git",
			Inventory: maintencev1alpha1.AnsibleInventory{
				Inline: "[test]\nlocalhost ansible_connection=local",
			},
			ExtraVars: []maintencev1alpha1.KeyValue{
				{Name: "test_var", Value: "test_value"},
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
							Name:  "ansible-runner",
							Image: "quay.io/ansible/ansible-runner:latest",
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
