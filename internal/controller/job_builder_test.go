// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	ansiblev1alpha1 "github.com/ironcore-dev/maintenance-operator/api/ansible/v1alpha1"
	testutils "github.com/ironcore-dev/maintenance-operator/test/utils"
)

var _ = Describe("Job Builder", func() {
	var (
		reconciler *AnsibleJobReconciler
		ansibleJob *ansiblev1alpha1.AnsibleJob
		scheme     *runtime.Scheme
	)

	BeforeEach(func() {
		scheme = runtime.NewScheme()
		reconciler = &AnsibleJobReconciler{
			Scheme: scheme,
		}

		ansibleJob = &ansiblev1alpha1.AnsibleJob{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-job",
				Namespace: "default",
			},
			Spec: ansiblev1alpha1.AnsibleJobSpec{
				Playbook: ansiblev1alpha1.PlaybookSpec{
					Name:       "site.yml",
					Repository: "https://github.com/test/playbooks.git",
				},
				Roles: &ansiblev1alpha1.RolesSpec{
					Repository: "https://github.com/test/roles.git",
				},
				Inventory: ansiblev1alpha1.AnsibleInventory{
					Inline: "[servers]\nweb1 ansible_host=10.0.1.10",
				},
				ExtraVars: []ansiblev1alpha1.KeyValue{
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

		It("should create streamlined init container for ansible-runner setup", func() {
			job := reconciler.createAnsibleJob(ansibleJob)

			initContainers := job.Spec.Template.Spec.InitContainers
			Expect(initContainers).To(HaveLen(1))

			// Check single streamlined setup container
			setupContainer := initContainers[0]
			Expect(setupContainer.Name).To(Equal("setup-ansible-runner"))
			Expect(setupContainer.Image).To(Equal("alpine/git@sha256:1dd70a5eed7f9b17aecd66756d138137d6818061c4fefefa5859b07f760e68fe"))
			// Git cloning is handled in init container since ansible-runner doesn't support --scm-url
			Expect(setupContainer.Args[0]).To(ContainSubstring("mkdir -p /runner/inventory /runner/env"))
			Expect(setupContainer.Args[0]).To(ContainSubstring("git clone"))
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
			// ansible-runner doesn't support SCM arguments, git cloning handled in init container
			Expect(container.Args).NotTo(ContainElement("--scm-url"))
			// Extra vars are now handled via /runner/env/extravars file, not command line arguments
			Expect(container.Args).NotTo(ContainElement("--extra-vars"))
		})

		It("should create volumes for repositories and runner input", func() {
			job := reconciler.createAnsibleJob(ansibleJob)

			volumes := job.Spec.Template.Spec.Volumes
			Expect(volumes).To(HaveLen(3)) // runner-workspace, tmp, inventory

			volumeNames := make([]string, len(volumes))
			for i, vol := range volumes {
				volumeNames[i] = vol.Name
			}
			Expect(volumeNames).To(ContainElement("runner-workspace"))
			Expect(volumeNames).To(ContainElement("tmp"))
			Expect(volumeNames).To(ContainElement("inventory"))
		})

		It("should handle custom job template settings", func() {
			ansibleJob.Spec.JobTemplate = &ansiblev1alpha1.JobTemplateSpec{
				Image:              "custom/ansible-runner:v1.0",
				ServiceAccountName: "custom-sa",
				BackoffLimit:       &[]int32{5}[0],
				Resources: &corev1.ResourceRequirements{
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
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
			Expect(volumeNames).To(ContainElement("runner-workspace"))
			Expect(volumeNames).To(ContainElement("tmp"))
			Expect(volumeNames).To(ContainElement("inventory"))
		})

		It("should not create inventory volume when no inline inventory", func() {
			ansibleJob.Spec.Inventory.Inline = ""
			volumes := reconciler.createVolumes(ansibleJob)

			Expect(volumes).To(HaveLen(2)) // runner-workspace + tmp only
			volumeNames := make([]string, len(volumes))
			for i, vol := range volumes {
				volumeNames[i] = vol.Name
			}
			Expect(volumeNames).To(ContainElement("runner-workspace"))
			Expect(volumeNames).To(ContainElement("tmp"))
			Expect(volumeNames).NotTo(ContainElement("inventory"))
		})
	})

	Context("Init Container Creation", func() {
		It("should create init containers with correct setup commands", func() {
			initContainers := reconciler.createInitContainers(ansibleJob)

			Expect(initContainers).To(HaveLen(1))

			// Test single streamlined setup container
			setupContainer := initContainers[0]
			Expect(setupContainer.Name).To(Equal("setup-ansible-runner"))
			// Git cloning is handled in init container since ansible-runner doesn't support --scm-url
			Expect(setupContainer.Args[0]).To(ContainSubstring("mkdir -p /runner/inventory /runner/env"))
			Expect(setupContainer.Args[0]).To(ContainSubstring("git clone"))
		})

		It("should handle git ref in init container git clone", func() {
			ansibleJob.Spec.Playbook.GitRef = "v1.0.0"
			ansibleJob.Spec.Roles.GitRef = "v1.0.0"

			// Check that init container handles git checkout of specific ref
			initContainers := reconciler.createInitContainers(ansibleJob)
			Expect(initContainers).To(HaveLen(1))

			setupContainer := initContainers[0]
			Expect(setupContainer.Args[0]).To(ContainSubstring("git checkout v1.0.0"))
		})

		It("should create only 1 container when no roles repo", func() {
			ansibleJob.Spec.Roles = nil
			initContainers := reconciler.createInitContainers(ansibleJob)

			Expect(initContainers).To(HaveLen(1))
			Expect(initContainers[0].Name).To(Equal("setup-ansible-runner"))
		})
	})

	Context("Utility Functions", func() {
		Describe("shellEscape", func() {
			It("should wrap strings in single quotes", func() {
				Expect(shellEscape("hello world")).To(Equal("'hello world'"))
				Expect(shellEscape("hello'world")).To(Equal("'hello'world'"))
				Expect(shellEscape("hello\"world")).To(Equal("'hello\"world'"))
				Expect(shellEscape("hello$world")).To(Equal("'hello$world'"))
				Expect(shellEscape("hello`world")).To(Equal("'hello`world'"))
			})

			It("should handle empty strings", func() {
				Expect(shellEscape("")).To(Equal("''"))
			})

			It("should handle strings with only special characters", func() {
				Expect(shellEscape("'")).To(Equal("'''"))
				Expect(shellEscape("''")).To(Equal("''''"))
			})
		})

		Describe("getInitContainerImage", func() {
			It("should return default image when none specified", func() {
				ansibleJob := &ansiblev1alpha1.AnsibleJob{}
				image := getInitContainerImage(ansibleJob)
				Expect(image).To(Equal("alpine/git@sha256:1dd70a5eed7f9b17aecd66756d138137d6818061c4fefefa5859b07f760e68fe"))
			})

			It("should return custom image when specified", func() {
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					Spec: ansiblev1alpha1.AnsibleJobSpec{
						JobTemplate: &ansiblev1alpha1.JobTemplateSpec{
							InitImage: "custom-git:latest",
						},
					},
				}
				image := getInitContainerImage(ansibleJob)
				Expect(image).To(Equal("custom-git:latest"))
			})

			It("should handle empty JobTemplate", func() {
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					Spec: ansiblev1alpha1.AnsibleJobSpec{
						JobTemplate: &ansiblev1alpha1.JobTemplateSpec{},
					},
				}
				image := getInitContainerImage(ansibleJob)
				Expect(image).To(Equal("alpine/git@sha256:1dd70a5eed7f9b17aecd66756d138137d6818061c4fefefa5859b07f760e68fe"))
			})
		})

		Describe("validateGitURL", func() {
			It("should accept valid HTTPS URLs", func() {
				Expect(validateGitURL("https://github.com/user/repo.git")).To(Succeed())
				Expect(validateGitURL("https://gitlab.com/user/repo")).To(Succeed())
				Expect(validateGitURL("https://bitbucket.org/user/repo.git")).To(Succeed())
			})

			It("should accept valid SSH URLs", func() {
				Expect(validateGitURL("git@github.com:user/repo.git")).To(Succeed())
				Expect(validateGitURL("ssh://git@gitlab.com/user/repo.git")).To(Succeed())
			})

			It("should accept valid git protocol URLs", func() {
				Expect(validateGitURL("git://github.com/user/repo.git")).To(Succeed())
			})

			It("should reject invalid protocols", func() {
				Expect(validateGitURL("ftp://example.com/repo.git")).To(HaveOccurred())
				Expect(validateGitURL("file:///local/repo")).To(HaveOccurred())
			})

			It("should reject malformed URLs", func() {
				Expect(validateGitURL("not-a-url")).To(HaveOccurred())
				Expect(validateGitURL("")).To(Succeed()) // Empty URLs are allowed
				Expect(validateGitURL("http://")).To(HaveOccurred())
			})

			It("should reject URLs that are too short", func() {
				Expect(validateGitURL("git")).To(HaveOccurred())
				Expect(validateGitURL("a")).To(HaveOccurred())
				Expect(validateGitURL("ab")).To(HaveOccurred())
				Expect(validateGitURL("abc")).To(HaveOccurred())
			})

			It("should reject URLs with suspicious patterns", func() {
				Expect(validateGitURL("https://github.com/user/repo.git; rm -rf /")).To(HaveOccurred())
				Expect(validateGitURL("https://github.com/user/repo.git && malicious-command")).To(HaveOccurred())
				Expect(validateGitURL("https://github.com/user/repo.git | cat")).To(HaveOccurred())
				Expect(validateGitURL("https://github.com/user/repo.git$PWD")).To(HaveOccurred())
				Expect(validateGitURL("https://github.com/user/repo.git`whoami`")).To(HaveOccurred())
				Expect(validateGitURL("https://github.com/user/repo.git$(whoami)")).To(HaveOccurred())
				Expect(validateGitURL("https://github.com/user/repo.git || echo")).To(HaveOccurred())
			})
		})

		Describe("createVolumes edge cases", func() {
			It("should handle multiple volume mount scenarios", func() {
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					Spec: ansiblev1alpha1.AnsibleJobSpec{
						Inventory: ansiblev1alpha1.AnsibleInventory{
							ConfigMapRef: &corev1.LocalObjectReference{
								Name: "test-configmap",
							},
							SecretRef: &corev1.LocalObjectReference{
								Name: "test-secret",
							},
						},
					},
				}
				reconciler := &AnsibleJobReconciler{}
				volumes := reconciler.createVolumes(ansibleJob)

				// Should include runner-workspace, tmp, and inventory volumes
				Expect(len(volumes)).To(BeNumerically(">=", 3))

				// Check for required volume names
				volumeNames := make([]string, len(volumes))
				for i, vol := range volumes {
					volumeNames[i] = vol.Name
				}
				Expect(volumeNames).To(ContainElement("runner-workspace"))
				Expect(volumeNames).To(ContainElement("tmp"))
				Expect(volumeNames).To(ContainElement("inventory"))
			})

			It("should handle empty inventory configuration", func() {
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					Spec: ansiblev1alpha1.AnsibleJobSpec{
						Inventory: ansiblev1alpha1.AnsibleInventory{
							// No inline, ConfigMapKeyRef, or SecretKeyRef
						},
					},
				}
				reconciler := &AnsibleJobReconciler{}
				volumes := reconciler.createVolumes(ansibleJob)

				// Should only include runner-workspace and tmp volumes
				Expect(volumes).To(HaveLen(2))

				volumeNames := make([]string, len(volumes))
				for i, vol := range volumes {
					volumeNames[i] = vol.Name
				}
				Expect(volumeNames).To(ContainElement("runner-workspace"))
				Expect(volumeNames).To(ContainElement("tmp"))
				Expect(volumeNames).NotTo(ContainElement("inventory"))
			})

			It("should create ConfigMap volume for ConfigMapRef only", func() {
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					Spec: ansiblev1alpha1.AnsibleJobSpec{
						Inventory: ansiblev1alpha1.AnsibleInventory{
							ConfigMapRef: &corev1.LocalObjectReference{
								Name: "test-configmap",
							},
						},
					},
				}
				reconciler := &AnsibleJobReconciler{}
				volumes := reconciler.createVolumes(ansibleJob)

				// Should include runner-workspace, tmp, and inventory volumes
				Expect(volumes).To(HaveLen(3))

				// Find the inventory volume
				var inventoryVolume *corev1.Volume
				for i := range volumes {
					if volumes[i].Name == testutils.InventoryVolumeName {
						inventoryVolume = &volumes[i]
						break
					}
				}
				Expect(inventoryVolume).NotTo(BeNil())

				// Verify ConfigMap volume source with hardcoded 'hosts' key
				Expect(inventoryVolume.VolumeSource.ConfigMap).NotTo(BeNil())
				Expect(inventoryVolume.VolumeSource.Secret).To(BeNil())
				Expect(inventoryVolume.VolumeSource.ConfigMap.Name).To(Equal("test-configmap"))
				Expect(inventoryVolume.VolumeSource.ConfigMap.Items).To(HaveLen(1))
				Expect(inventoryVolume.VolumeSource.ConfigMap.Items[0].Key).To(Equal("hosts"))
				Expect(inventoryVolume.VolumeSource.ConfigMap.Items[0].Path).To(Equal("hosts"))
			})

			It("should create Secret volume for SecretRef only", func() {
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					Spec: ansiblev1alpha1.AnsibleJobSpec{
						Inventory: ansiblev1alpha1.AnsibleInventory{
							SecretRef: &corev1.LocalObjectReference{
								Name: "test-secret",
							},
						},
					},
				}
				reconciler := &AnsibleJobReconciler{}
				volumes := reconciler.createVolumes(ansibleJob)

				// Should include runner-workspace, tmp, and inventory volumes
				Expect(volumes).To(HaveLen(3))

				// Find the inventory volume
				var inventoryVolume *corev1.Volume
				for i := range volumes {
					if volumes[i].Name == testutils.InventoryVolumeName {
						inventoryVolume = &volumes[i]
						break
					}
				}
				Expect(inventoryVolume).NotTo(BeNil())

				// Verify Secret volume source with hardcoded 'hosts' key
				Expect(inventoryVolume.VolumeSource.Secret).NotTo(BeNil())
				Expect(inventoryVolume.VolumeSource.ConfigMap).To(BeNil())
				Expect(inventoryVolume.VolumeSource.Secret.SecretName).To(Equal("test-secret"))
				Expect(inventoryVolume.VolumeSource.Secret.Items).To(HaveLen(1))
				Expect(inventoryVolume.VolumeSource.Secret.Items[0].Key).To(Equal("hosts"))
				Expect(inventoryVolume.VolumeSource.Secret.Items[0].Path).To(Equal("hosts"))
			})

			It("should prioritize Inline over ConfigMapRef", func() {
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					ObjectMeta: metav1.ObjectMeta{
						Name: "priority-test",
					},
					Spec: ansiblev1alpha1.AnsibleJobSpec{
						Inventory: ansiblev1alpha1.AnsibleInventory{
							Inline: "[all]\nlocalhost",
							ConfigMapRef: &corev1.LocalObjectReference{
								Name: "should-be-ignored",
							},
						},
					},
				}
				reconciler := &AnsibleJobReconciler{}
				volumes := reconciler.createVolumes(ansibleJob)

				// Should include runner-workspace, tmp, and inventory volumes
				Expect(volumes).To(HaveLen(3))

				// Find the inventory volume
				var inventoryVolume *corev1.Volume
				for i := range volumes {
					if volumes[i].Name == testutils.InventoryVolumeName {
						inventoryVolume = &volumes[i]
						break
					}
				}
				Expect(inventoryVolume).NotTo(BeNil())

				// Should use ConfigMap for inline inventory (created by controller)
				Expect(inventoryVolume.VolumeSource.ConfigMap).NotTo(BeNil())
				Expect(inventoryVolume.VolumeSource.ConfigMap.Name).To(Equal("priority-test-inventory"))
				// Inline inventory doesn't use Items mapping
				Expect(inventoryVolume.VolumeSource.ConfigMap.Items).To(BeEmpty())
			})

			It("should prioritize ConfigMapRef over SecretRef", func() {
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					Spec: ansiblev1alpha1.AnsibleJobSpec{
						Inventory: ansiblev1alpha1.AnsibleInventory{
							ConfigMapRef: &corev1.LocalObjectReference{
								Name: "configmap-wins",
							},
							SecretRef: &corev1.LocalObjectReference{
								Name: "should-be-ignored",
							},
						},
					},
				}
				reconciler := &AnsibleJobReconciler{}
				volumes := reconciler.createVolumes(ansibleJob)

				// Should include runner-workspace, tmp, and inventory volumes
				Expect(volumes).To(HaveLen(3))

				// Find the inventory volume
				var inventoryVolume *corev1.Volume
				for i := range volumes {
					if volumes[i].Name == testutils.InventoryVolumeName {
						inventoryVolume = &volumes[i]
						break
					}
				}
				Expect(inventoryVolume).NotTo(BeNil())

				// Should use ConfigMap, not Secret
				Expect(inventoryVolume.VolumeSource.ConfigMap).NotTo(BeNil())
				Expect(inventoryVolume.VolumeSource.Secret).To(BeNil())
				Expect(inventoryVolume.VolumeSource.ConfigMap.Name).To(Equal("configmap-wins"))
			})
		})

		Describe("needsInventoryMount function", func() {
			It("should return true for inline inventory", func() {
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					Spec: ansiblev1alpha1.AnsibleJobSpec{
						Inventory: ansiblev1alpha1.AnsibleInventory{
							Inline: "[all]\nlocalhost",
						},
					},
				}
				reconciler := &AnsibleJobReconciler{}
				Expect(reconciler.needsInventoryMount(ansibleJob)).To(BeTrue())
			})

			It("should return true for ConfigMapRef", func() {
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					Spec: ansiblev1alpha1.AnsibleJobSpec{
						Inventory: ansiblev1alpha1.AnsibleInventory{
							ConfigMapRef: &corev1.LocalObjectReference{
								Name: "test-configmap",
							},
						},
					},
				}
				reconciler := &AnsibleJobReconciler{}
				Expect(reconciler.needsInventoryMount(ansibleJob)).To(BeTrue())
			})

			It("should return true for SecretRef", func() {
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					Spec: ansiblev1alpha1.AnsibleJobSpec{
						Inventory: ansiblev1alpha1.AnsibleInventory{
							SecretRef: &corev1.LocalObjectReference{
								Name: "test-secret",
							},
						},
					},
				}
				reconciler := &AnsibleJobReconciler{}
				Expect(reconciler.needsInventoryMount(ansibleJob)).To(BeTrue())
			})

			It("should return false when no inventory sources are specified", func() {
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					Spec: ansiblev1alpha1.AnsibleJobSpec{
						Inventory: ansiblev1alpha1.AnsibleInventory{
							// All fields empty
						},
					},
				}
				reconciler := &AnsibleJobReconciler{}
				Expect(reconciler.needsInventoryMount(ansibleJob)).To(BeFalse())
			})
		})

		Describe("createAnsibleJob edge cases", func() {
			It("should handle JobTemplate with all nil/empty fields", func() {
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "edge-case-job",
						Namespace: "test-namespace",
					},
					Spec: ansiblev1alpha1.AnsibleJobSpec{
						Playbook:    ansiblev1alpha1.PlaybookSpec{Name: "playbook.yml", Repository: "https://github.com/test/playbooks.git"},
						JobTemplate: &ansiblev1alpha1.JobTemplateSpec{
							// All fields empty/nil
						},
						Inventory: ansiblev1alpha1.AnsibleInventory{
							Inline: "test-inventory",
						},
					},
				}

				reconciler := &AnsibleJobReconciler{}
				job := reconciler.createAnsibleJob(ansibleJob)

				// Should use defaults when JobTemplate fields are empty
				Expect(job.Spec.Template.Spec.ServiceAccountName).To(Equal(defaultServiceAccount))
				Expect(*job.Spec.BackoffLimit).To(Equal(defaultBackoffLimit))
				Expect(job.Spec.Template.Spec.Containers[0].Image).To(Equal(defaultAnsibleRunnerImage))
				Expect(job.Spec.ActiveDeadlineSeconds).To(BeNil()) // No timeout specified
			})

			It("should handle JobTemplate with empty strings", func() {
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "empty-strings-job",
						Namespace: "test-namespace",
					},
					Spec: ansiblev1alpha1.AnsibleJobSpec{
						Playbook: ansiblev1alpha1.PlaybookSpec{Name: "playbook.yml", Repository: "https://github.com/test/playbooks.git"},
						JobTemplate: &ansiblev1alpha1.JobTemplateSpec{
							Image:              "", // Empty string
							ServiceAccountName: "", // Empty string
						},
						Inventory: ansiblev1alpha1.AnsibleInventory{
							Inline: "test-inventory",
						},
					},
				}

				reconciler := &AnsibleJobReconciler{}
				job := reconciler.createAnsibleJob(ansibleJob)

				// Should use defaults when strings are empty
				Expect(job.Spec.Template.Spec.ServiceAccountName).To(Equal(defaultServiceAccount))
				Expect(job.Spec.Template.Spec.Containers[0].Image).To(Equal(defaultAnsibleRunnerImage))
			})

			It("should handle zero timeout correctly", func() {
				timeout := int32(0)
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "zero-timeout-job",
						Namespace: "test-namespace",
					},
					Spec: ansiblev1alpha1.AnsibleJobSpec{
						Playbook:       ansiblev1alpha1.PlaybookSpec{Name: "playbook.yml", Repository: "https://github.com/test/playbooks.git"},
						TimeoutSeconds: &timeout,
						Inventory: ansiblev1alpha1.AnsibleInventory{
							Inline: "test-inventory",
						},
					},
				}

				reconciler := &AnsibleJobReconciler{}
				job := reconciler.createAnsibleJob(ansibleJob)

				// Should set ActiveDeadlineSeconds to 0
				Expect(job.Spec.ActiveDeadlineSeconds).NotTo(BeNil())
				Expect(*job.Spec.ActiveDeadlineSeconds).To(Equal(int64(0)))
			})
		})

		Describe("createAnsibleRunnerContainer edge cases", func() {
			It("should handle inventory without any mount when needsInventoryMount returns false", func() {
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					Spec: ansiblev1alpha1.AnsibleJobSpec{
						Playbook:  ansiblev1alpha1.PlaybookSpec{Name: "test.yml", Repository: "https://github.com/test/playbooks.git"},
						Inventory: ansiblev1alpha1.AnsibleInventory{
							// No inline, ConfigMapKeyRef, or SecretKeyRef
						},
					},
				}

				reconciler := &AnsibleJobReconciler{}
				containers := reconciler.createAnsibleRunnerContainer(ansibleJob, "test-image")

				Expect(containers).To(HaveLen(1))
				container := containers[0]

				// Should only have runner-workspace and tmp mounts, no inventory mount
				Expect(container.VolumeMounts).To(HaveLen(2))
				mountPaths := make([]string, len(container.VolumeMounts))
				for i, mount := range container.VolumeMounts {
					mountPaths[i] = mount.MountPath
				}
				Expect(mountPaths).To(ContainElement("/runner"))
				Expect(mountPaths).To(ContainElement("/tmp"))
				Expect(mountPaths).NotTo(ContainElement("/runner/inventory"))
			})

			It("should handle custom resource requirements correctly", func() {
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					Spec: ansiblev1alpha1.AnsibleJobSpec{
						Playbook: ansiblev1alpha1.PlaybookSpec{Name: "test.yml", Repository: "https://github.com/test/playbooks.git"},
						JobTemplate: &ansiblev1alpha1.JobTemplateSpec{
							Resources: &corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("2"),
									corev1.ResourceMemory: resource.MustParse("4Gi"),
								},
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("2Gi"),
								},
							},
						},
						Inventory: ansiblev1alpha1.AnsibleInventory{
							Inline: "test-inventory",
						},
					},
				}

				reconciler := &AnsibleJobReconciler{}
				containers := reconciler.createAnsibleRunnerContainer(ansibleJob, "test-image")

				Expect(containers).To(HaveLen(1))
				container := containers[0]

				// Should have custom resource requirements
				Expect(container.Resources.Limits).NotTo(BeEmpty())
				Expect(container.Resources.Requests).NotTo(BeEmpty())

				// Check specific resource values
				cpuLimit := container.Resources.Limits[corev1.ResourceCPU]
				memLimit := container.Resources.Limits[corev1.ResourceMemory]
				cpuRequest := container.Resources.Requests[corev1.ResourceCPU]
				memRequest := container.Resources.Requests[corev1.ResourceMemory]

				Expect(cpuLimit.String()).To(Equal("2"))
				Expect(memLimit.String()).To(Equal("4Gi"))
				Expect(cpuRequest.String()).To(Equal("1"))
				Expect(memRequest.String()).To(Equal("2Gi"))
			})

			It("should use default resources when JobTemplate.Resources is nil", func() {
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					Spec: ansiblev1alpha1.AnsibleJobSpec{
						Playbook: ansiblev1alpha1.PlaybookSpec{Name: "test.yml", Repository: "https://github.com/test/playbooks.git"},
						JobTemplate: &ansiblev1alpha1.JobTemplateSpec{
							Resources: nil, // Explicitly nil
						},
						Inventory: ansiblev1alpha1.AnsibleInventory{
							Inline: "test-inventory",
						},
					},
				}

				reconciler := &AnsibleJobReconciler{}
				containers := reconciler.createAnsibleRunnerContainer(ansibleJob, "test-image")

				Expect(containers).To(HaveLen(1))
				container := containers[0]

				// Should have default resource requirements
				cpuRequest := container.Resources.Requests[corev1.ResourceCPU]
				memRequest := container.Resources.Requests[corev1.ResourceMemory]
				cpuLimit := container.Resources.Limits[corev1.ResourceCPU]
				memLimit := container.Resources.Limits[corev1.ResourceMemory]

				Expect(cpuRequest.String()).To(Equal("100m"))
				Expect(memRequest.String()).To(Equal("256Mi"))
				Expect(cpuLimit.String()).To(Equal("500m"))
				Expect(memLimit.String()).To(Equal("512Mi"))
			})

			It("should use default resources when JobTemplate is nil", func() {
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					Spec: ansiblev1alpha1.AnsibleJobSpec{
						Playbook:    ansiblev1alpha1.PlaybookSpec{Name: "test.yml", Repository: "https://github.com/test/playbooks.git"},
						JobTemplate: nil, // Explicitly nil
						Inventory: ansiblev1alpha1.AnsibleInventory{
							Inline: "test-inventory",
						},
					},
				}

				reconciler := &AnsibleJobReconciler{}
				containers := reconciler.createAnsibleRunnerContainer(ansibleJob, "test-image")

				Expect(containers).To(HaveLen(1))
				container := containers[0]

				// Should have default resource requirements
				cpuRequest := container.Resources.Requests[corev1.ResourceCPU]
				memRequest := container.Resources.Requests[corev1.ResourceMemory]
				cpuLimit := container.Resources.Limits[corev1.ResourceCPU]
				memLimit := container.Resources.Limits[corev1.ResourceMemory]

				Expect(cpuRequest.String()).To(Equal("100m"))
				Expect(memRequest.String()).To(Equal("256Mi"))
				Expect(cpuLimit.String()).To(Equal("500m"))
				Expect(memLimit.String()).To(Equal("512Mi"))
			})
		})
	})

	Context("parseQuantity function", func() {
		It("should parse valid quantity strings correctly", func() {
			testCases := []struct {
				input    string
				expected string
			}{
				{"100m", "100m"},
				{"1Gi", "1Gi"},
				{"256Mi", "256Mi"},
				{"500", "500"},
				{"1.5", "1500m"},
			}

			for _, tc := range testCases {
				result := parseQuantity(tc.input)
				Expect(result).NotTo(BeNil())
				Expect(result.String()).To(Equal(tc.expected))
			}
		})

		It("should return default quantity for invalid strings", func() {
			invalidInputs := []string{
				"invalid",
				"",
				"abc123",
				"1.2.3Gi",
				"not-a-number",
			}

			for _, input := range invalidInputs {
				result := parseQuantity(input)
				Expect(result).NotTo(BeNil())
				// Default quantity should be zero
				Expect(result.IsZero()).To(BeTrue())
			}
		})

		It("should handle edge cases", func() {
			// Test zero value
			result := parseQuantity("0")
			Expect(result).NotTo(BeNil())
			Expect(result.IsZero()).To(BeTrue())

			// Test negative value (should be invalid and return default)
			result = parseQuantity("-100m")
			Expect(result).NotTo(BeNil())
			// ParseQuantity actually allows negative values, so this should work
			expected := resource.MustParse("-100m")
			Expect(result.Equal(expected)).To(BeTrue())
		})
	})
})
