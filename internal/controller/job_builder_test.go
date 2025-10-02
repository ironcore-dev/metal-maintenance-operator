// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	maintencev1alpha1 "github.com/ironcore-dev/maintenance-operator/api/v1alpha1"
)

var _ = Describe("Job Builder", func() {
	var (
		reconciler *AnsibleJobReconciler
		ansibleJob *maintencev1alpha1.AnsibleJob
		scheme     *runtime.Scheme
	)

	BeforeEach(func() {
		scheme = runtime.NewScheme()
		reconciler = &AnsibleJobReconciler{
			Scheme: scheme,
		}

		ansibleJob = &maintencev1alpha1.AnsibleJob{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-job",
				Namespace: "default",
			},
			Spec: maintencev1alpha1.AnsibleJobSpec{
				Playbook:     "site.yml",
				PlaybookRepo: "https://github.com/test/playbooks.git",
				RolesRepo:    "https://github.com/test/roles.git",
				Inventory: maintencev1alpha1.AnsibleInventory{
					Inline: "[servers]\nweb1 ansible_host=10.0.1.10",
				},
				ExtraVars: []maintencev1alpha1.KeyValue{
					{Name: "test_var", Value: "test_value"},
					{Name: "environment", Value: "test"},
				},
			},
		}
	})

	Context("Ansible Job Creation", func() {
		It("should create job with correct ansible-runner configuration", func() {
			job := reconciler.createAnsibleJob(ansibleJob)
			Expect(job).NotTo(BeNil())
			Expect(job.Name).To(Equal("test-job-job"))
			Expect(job.Namespace).To(Equal("default"))
			Expect(job.Labels["ansible-job"]).To(Equal("test-job"))
			Expect(job.Labels["app"]).To(Equal("ansible-runner"))
		})

		It("should create init containers for repository cloning", func() {
			job := reconciler.createAnsibleJob(ansibleJob)

			initContainers := job.Spec.Template.Spec.InitContainers
			Expect(initContainers).To(HaveLen(3))

			// Check clone-playbooks container
			Expect(initContainers[0].Name).To(Equal("clone-playbooks"))
			Expect(initContainers[0].Image).To(Equal("alpine/git:latest"))
			Expect(initContainers[0].Args[0]).To(ContainSubstring("git clone https://github.com/test/playbooks.git"))

			// Check clone-roles container
			Expect(initContainers[1].Name).To(Equal("clone-roles"))
			Expect(initContainers[1].Image).To(Equal("alpine/git:latest"))
			Expect(initContainers[1].Args[0]).To(ContainSubstring("git clone https://github.com/test/roles.git"))

			// Check setup-runner container
			Expect(initContainers[2].Name).To(Equal("setup-runner"))
			Expect(initContainers[2].Image).To(Equal("busybox:latest"))
		})

		It("should create ansible-runner container with correct arguments", func() {
			job := reconciler.createAnsibleJob(ansibleJob)

			containers := job.Spec.Template.Spec.Containers
			Expect(containers).To(HaveLen(1))

			container := containers[0]
			Expect(container.Name).To(Equal("ansible-runner"))
			Expect(container.Image).To(Equal(defaultAnsibleRunnerImage))
			Expect(container.Command).To(Equal([]string{"ansible-runner"}))
			Expect(container.Args).To(ContainElement("run"))
			Expect(container.Args).To(ContainElement("/runner"))
			Expect(container.Args).To(ContainElement("--playbook"))
			Expect(container.Args).To(ContainElement("site.yml"))
			// Note: Extra vars are now handled via /runner/env/extravars file, not command line arguments
			Expect(container.Args).NotTo(ContainElement("--extra-vars"))
		})

		It("should create volumes for repositories and runner input", func() {
			job := reconciler.createAnsibleJob(ansibleJob)

			volumes := job.Spec.Template.Spec.Volumes
			Expect(volumes).To(HaveLen(3)) // repos, runner-input, inventory

			volumeNames := make([]string, len(volumes))
			for i, vol := range volumes {
				volumeNames[i] = vol.Name
			}
			Expect(volumeNames).To(ContainElement("repos"))
			Expect(volumeNames).To(ContainElement("runner-input"))
			Expect(volumeNames).To(ContainElement("inventory"))
		})

		It("should handle custom job template settings", func() {
			ansibleJob.Spec.JobTemplate = &maintencev1alpha1.JobTemplateSpec{
				Image:              "custom/ansible-runner:v1.0",
				ServiceAccountName: "custom-sa",
				BackoffLimit:       &[]int32{5}[0],
				Resources: &maintencev1alpha1.ResourceRequirements{
					Limits: []maintencev1alpha1.ResourceQuantity{
						{Name: "cpu", Quantity: "2"},
						{Name: "memory", Quantity: "4Gi"},
					},
					Requests: []maintencev1alpha1.ResourceQuantity{
						{Name: "cpu", Quantity: "1"},
						{Name: "memory", Quantity: "2Gi"},
					},
				},
			}

			job := reconciler.createAnsibleJob(ansibleJob)

			Expect(*job.Spec.BackoffLimit).To(Equal(int32(5)))
			Expect(job.Spec.Template.Spec.ServiceAccountName).To(Equal("custom-sa"))

			container := job.Spec.Template.Spec.Containers[0]
			Expect(container.Image).To(Equal("custom/ansible-runner:v1.0"))
			Expect(container.Resources.Limits).To(HaveKey(corev1.ResourceName("cpu")))
			Expect(container.Resources.Requests).To(HaveKey(corev1.ResourceName("memory")))
		})
	})

	Context("Volume Creation", func() {
		It("should create basic volumes for ansible-runner", func() {
			volumes := reconciler.createVolumes(ansibleJob)

			Expect(volumes).To(HaveLen(3))
			volumeNames := make([]string, len(volumes))
			for i, vol := range volumes {
				volumeNames[i] = vol.Name
			}
			Expect(volumeNames).To(ContainElement("repos"))
			Expect(volumeNames).To(ContainElement("runner-input"))
			Expect(volumeNames).To(ContainElement("inventory"))
		})

		It("should not create inventory volume when no inline inventory", func() {
			ansibleJob.Spec.Inventory.Inline = ""
			volumes := reconciler.createVolumes(ansibleJob)

			Expect(volumes).To(HaveLen(2))
			volumeNames := make([]string, len(volumes))
			for i, vol := range volumes {
				volumeNames[i] = vol.Name
			}
			Expect(volumeNames).To(ContainElement("repos"))
			Expect(volumeNames).To(ContainElement("runner-input"))
			Expect(volumeNames).NotTo(ContainElement("inventory"))
		})
	})

	Context("Init Container Creation", func() {
		It("should create init containers with correct git commands", func() {
			initContainers := reconciler.createInitContainers(ansibleJob)

			Expect(initContainers).To(HaveLen(3))

			// Test playbook clone container
			playbookContainer := initContainers[0]
			Expect(playbookContainer.Name).To(Equal("clone-playbooks"))
			Expect(playbookContainer.Args[0]).To(ContainSubstring("git clone https://github.com/test/playbooks.git"))

			// Test roles clone container
			rolesContainer := initContainers[1]
			Expect(rolesContainer.Name).To(Equal("clone-roles"))
			Expect(rolesContainer.Args[0]).To(ContainSubstring("git clone https://github.com/test/roles.git"))

			// Test setup container
			setupContainer := initContainers[2]
			Expect(setupContainer.Name).To(Equal("setup-runner"))
			Expect(setupContainer.Args[0]).To(ContainSubstring("mkdir -p /runner/project"))
		})

		It("should handle git ref in clone commands", func() {
			ansibleJob.Spec.PlaybookGitRef = "v1.0.0"
			ansibleJob.Spec.RolesGitRef = "v1.0.0"
			initContainers := reconciler.createInitContainers(ansibleJob)

			playbookContainer := initContainers[0]
			Expect(playbookContainer.Args[0]).To(ContainSubstring("git checkout v1.0.0"))

			rolesContainer := initContainers[1]
			Expect(rolesContainer.Args[0]).To(ContainSubstring("git checkout v1.0.0"))
		})

		It("should create only 2 containers when no roles repo", func() {
			ansibleJob.Spec.RolesRepo = ""
			initContainers := reconciler.createInitContainers(ansibleJob)

			Expect(initContainers).To(HaveLen(2))
			Expect(initContainers[0].Name).To(Equal("clone-playbooks"))
			Expect(initContainers[1].Name).To(Equal("setup-runner"))
		})
	})

	Context("Resource Conversion", func() {
		Describe("convertToResourceList", func() {
			It("should handle nil input", func() {
				result := convertToResourceList(nil)
				Expect(result).To(BeNil())
			})

			It("should handle empty list", func() {
				result := convertToResourceList([]maintencev1alpha1.ResourceQuantity{})
				Expect(result).NotTo(BeNil())
				Expect(result).To(BeEmpty())
			})

			It("should convert valid resource values", func() {
				resources := []maintencev1alpha1.ResourceQuantity{
					{Name: "cpu", Quantity: "1"},
					{Name: "memory", Quantity: "2Gi"},
					{Name: "storage", Quantity: "10G"},
				}
				result := convertToResourceList(resources)

				Expect(result).To(HaveLen(3))
				Expect(result).To(HaveKey(corev1.ResourceName("cpu")))
				Expect(result).To(HaveKey(corev1.ResourceName("memory")))
				Expect(result).To(HaveKey(corev1.ResourceName("storage")))

				// Verify the actual values
				cpuQuantity := result[corev1.ResourceName("cpu")]
				memoryQuantity := result[corev1.ResourceName("memory")]
				storageQuantity := result[corev1.ResourceName("storage")]

				Expect(cpuQuantity.String()).To(Equal("1"))
				Expect(memoryQuantity.String()).To(Equal("2Gi"))
				Expect(storageQuantity.String()).To(Equal("10G"))
			})

			It("should handle invalid resource values gracefully", func() {
				resources := []maintencev1alpha1.ResourceQuantity{
					{Name: "cpu", Quantity: "1"},
					{Name: "memory", Quantity: "invalid-value"},
					{Name: "storage", Quantity: "10G"},
				}
				result := convertToResourceList(resources)

				Expect(result).To(HaveLen(3))
				Expect(result).To(HaveKey(corev1.ResourceName("cpu")))
				Expect(result).To(HaveKey(corev1.ResourceName("memory")))
				Expect(result).To(HaveKey(corev1.ResourceName("storage")))

				// Valid values should be parsed correctly
				cpuQuantity := result[corev1.ResourceName("cpu")]
				storageQuantity := result[corev1.ResourceName("storage")]
				memoryQuantity := result[corev1.ResourceName("memory")]

				Expect(cpuQuantity.String()).To(Equal("1"))
				Expect(storageQuantity.String()).To(Equal("10G"))

				// Invalid value should result in zero quantity
				Expect(memoryQuantity.IsZero()).To(BeTrue())
			})

			It("should handle mix of valid and invalid quantities", func() {
				resources := []maintencev1alpha1.ResourceQuantity{
					{Name: "cpu", Quantity: "500m"},
					{Name: "memory", Quantity: "not-a-quantity"},
					{Name: "storage", Quantity: "1Ti"},
				}
				result := convertToResourceList(resources)

				Expect(result).To(HaveLen(3))

				// Valid values
				cpuQuantity := result[corev1.ResourceName("cpu")]
				memoryQuantity := result[corev1.ResourceName("memory")]
				storageQuantity := result[corev1.ResourceName("storage")]

				Expect(cpuQuantity.String()).To(Equal("500m"))
				// Invalid memory quantity should result in zero/empty quantity
				Expect(memoryQuantity.IsZero()).To(BeTrue())
				Expect(storageQuantity.String()).To(Equal("1Ti"))
			})

			It("should handle various quantity formats", func() {
				resources := []maintencev1alpha1.ResourceQuantity{
					{Name: "cpu", Quantity: "100m"},              // millicpu
					{Name: "memory", Quantity: "512Mi"},          // mebibytes
					{Name: "ephemeral-storage", Quantity: "1Gi"}, // gibibytes
					{Name: "nvidia.com/gpu", Quantity: "1"},      // custom resource
				}
				result := convertToResourceList(resources)

				Expect(result).To(HaveLen(4))

				cpuQuantity := result[corev1.ResourceName("cpu")]
				memoryQuantity := result[corev1.ResourceName("memory")]
				storageQuantity := result[corev1.ResourceName("ephemeral-storage")]
				gpuQuantity := result[corev1.ResourceName("nvidia.com/gpu")]

				Expect(cpuQuantity.String()).To(Equal("100m"))
				Expect(memoryQuantity.String()).To(Equal("512Mi"))
				Expect(storageQuantity.String()).To(Equal("1Gi"))
				Expect(gpuQuantity.String()).To(Equal("1"))
			})
		})
	})
})
