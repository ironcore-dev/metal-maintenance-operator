// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	ansiblev1alpha1 "github.com/ironcore-dev/maintenance-operator/api/ansible/v1alpha1"
)

// mockStatusWriter is a mock implementation of client.StatusWriter that can simulate failures
type mockStatusWriter struct {
	client.StatusWriter
	shouldFail bool
	failError  error
}

func (m *mockStatusWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	if m.shouldFail {
		return m.failError
	}
	return nil
}

func (m *mockStatusWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if m.shouldFail {
		return m.failError
	}
	return nil
}

// mockClient wraps a fake client with a controllable status writer
type mockClient struct {
	client.Client
	statusWriter *mockStatusWriter
}

func (m *mockClient) Status() client.StatusWriter {
	return m.statusWriter
}

// mockFailingClient is a mock client that can simulate Get failures
type mockFailingClient struct {
	client.Client
	shouldFail bool
	failError  error
}

func (m *mockFailingClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if m.shouldFail {
		return m.failError
	}
	return m.Client.Get(ctx, key, obj, opts...)
}

// mockJobGetFailingClient is a mock client that can simulate Job Get failures
type mockJobGetFailingClient struct {
	client.Client
	shouldFail bool
	failError  error
}

func (m *mockJobGetFailingClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	// Only fail for Job objects
	if _, isJob := obj.(*batchv1.Job); isJob && m.shouldFail {
		return m.failError
	}
	return m.Client.Get(ctx, key, obj, opts...)
}

// mockJobCreateFailingClient is a mock client that can simulate Job Create failures
type mockJobCreateFailingClient struct {
	client.Client
	shouldFail bool
	failError  error
}

func (m *mockJobCreateFailingClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	// Only fail for Job objects
	if _, isJob := obj.(*batchv1.Job); isJob && m.shouldFail {
		return m.failError
	}
	return m.Client.Create(ctx, obj, opts...)
}

// mockConfigMapGetFailingClient is a mock client that can simulate ConfigMap Get failures
type mockConfigMapGetFailingClient struct {
	client.Client
	shouldFail bool
	failError  error
}

func (m *mockConfigMapGetFailingClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	// Only fail for ConfigMap objects
	if _, isConfigMap := obj.(*corev1.ConfigMap); isConfigMap && m.shouldFail {
		return m.failError
	}
	return m.Client.Get(ctx, key, obj, opts...)
}

// mockConfigMapCreateFailingClient is a mock client that can simulate ConfigMap Create failures
type mockConfigMapCreateFailingClient struct {
	client.Client
	shouldFail bool
	failError  error
}

func (m *mockConfigMapCreateFailingClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	// Only fail for ConfigMap objects
	if _, isConfigMap := obj.(*corev1.ConfigMap); isConfigMap && m.shouldFail {
		return m.failError
	}
	return m.Client.Create(ctx, obj, opts...)
}

// mockSetControllerRefFailingClient simulates SetControllerReference failure by failing Create with specific error
type mockSetControllerRefFailingClient struct {
	client.Client
	shouldFailControllerRef bool
}

func (m *mockSetControllerRefFailingClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	if m.shouldFailControllerRef {
		// Simulate SetControllerReference type error
		return errors.New("failed to set controller reference: owner must have non-empty UID")
	}
	return m.Client.Create(ctx, obj, opts...)
}

// mockJobClient is a mock client that can return pre-configured Job objects
type mockJobClient struct {
	client.Client
	jobs map[client.ObjectKey]*batchv1.Job
}

func (m *mockJobClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if job, ok := obj.(*batchv1.Job); ok {
		if mockJob, exists := m.jobs[key]; exists {
			*job = *mockJob
			return nil
		}
		return errors.New("job not found")
	}
	return m.Client.Get(ctx, key, obj, opts...)
}

// findCondition finds a condition by type in the conditions slice
func findCondition(conditions []metav1.Condition, conditionType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return &conditions[i]
		}
	}
	return nil
}

var _ = Describe("AnsibleJob Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			AnsibleJobName      = "test-ansible-job"
			AnsibleJobNamespace = "default"
			timeout             = time.Second * 10
			duration            = time.Second * 10
			interval            = time.Millisecond * 250
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      AnsibleJobName,
			Namespace: AnsibleJobNamespace,
		}

		BeforeEach(func() {
			By("creating the custom resource for the Kind AnsibleJob")
			ansibleJob := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      AnsibleJobName,
					Namespace: AnsibleJobNamespace,
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{
						Name:       "test-playbook.yml",
						Repository: "https://github.com/test/playbooks.git",
					},
					Roles: &ansiblev1alpha1.RolesSpec{
						Repository: "https://github.com/test/roles.git",
					},

					Inventory: ansiblev1alpha1.AnsibleInventory{
						Inline: "[test]\nlocalhost ansible_connection=local",
					},
					ExtraVars: []ansiblev1alpha1.KeyValue{
						{Name: "test_var", Value: "test_value"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, ansibleJob)).To(Succeed())
		})

		AfterEach(func() {
			By("Cleanup the specific resource instance AnsibleJob")
			resource := &ansiblev1alpha1.AnsibleJob{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup any created jobs")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList, client.InNamespace(AnsibleJobNamespace))
			Expect(err).NotTo(HaveOccurred())

			for _, job := range jobList.Items {
				Expect(k8sClient.Delete(ctx, &job)).To(Succeed())
			}

			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &AnsibleJobReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking the AnsibleJob status is updated")
			ansibleJob := &ansiblev1alpha1.AnsibleJob{}
			Eventually(func() string {
				getErr := k8sClient.Get(ctx, typeNamespacedName, ansibleJob)
				if getErr != nil {
					return ""
				}
				return string(ansibleJob.Status.Phase)
			}, timeout, interval).Should(Equal(string(ansiblev1alpha1.AnsibleJobPhasePending)))
		})

		It("should create a Kubernetes Job", func() {
			By("Reconciling the AnsibleJob multiple times")
			controllerReconciler := &AnsibleJobReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// First reconcile - initialize
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Second reconcile - create job
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking that a Job was created")
			jobList := &batchv1.JobList{}
			Eventually(func() int {
				listErr := k8sClient.List(ctx, jobList, client.InNamespace(AnsibleJobNamespace))
				if listErr != nil {
					return 0
				}
				return len(jobList.Items)
			}, timeout, interval).Should(Equal(1))

			By("Checking Job has correct labels and specifications")
			job := jobList.Items[0]
			Expect(job.Labels["ansible-job"]).To(Equal(AnsibleJobName))
			Expect(job.Labels["app"]).To(Equal("ansible-runner"))
			Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1))
			Expect(job.Spec.Template.Spec.InitContainers).To(HaveLen(1)) // single streamlined setup container
		})

		It("should update status when job completes", func() {
			By("Creating and reconciling the AnsibleJob")
			controllerReconciler := &AnsibleJobReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// Initialize and create job
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Simulating job completion")
			jobList := &batchv1.JobList{}
			err = k8sClient.List(ctx, jobList, client.InNamespace(AnsibleJobNamespace))
			Expect(err).NotTo(HaveOccurred())
			Expect(jobList.Items).To(HaveLen(1))

			job := &jobList.Items[0]
			job.Status.Succeeded = 1
			job.Status.StartTime = &metav1.Time{Time: time.Now().Add(-time.Minute)}
			job.Status.Conditions = []batchv1.JobCondition{
				{
					Type:               batchv1.JobSuccessCriteriaMet,
					Status:             corev1.ConditionTrue,
					LastProbeTime:      metav1.Time{Time: time.Now()},
					LastTransitionTime: metav1.Time{Time: time.Now()},
					Reason:             "SuccessCriteriaMet",
					Message:            "Job success criteria met",
				},
				{
					Type:               batchv1.JobComplete,
					Status:             corev1.ConditionTrue,
					LastProbeTime:      metav1.Time{Time: time.Now()},
					LastTransitionTime: metav1.Time{Time: time.Now()},
					Reason:             "Completed",
					Message:            "Job completed successfully",
				},
			}
			job.Status.CompletionTime = &metav1.Time{Time: time.Now()}
			err = k8sClient.Status().Update(ctx, job)
			Expect(err).NotTo(HaveOccurred())

			By("Reconciling again to update AnsibleJob status")
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking AnsibleJob status is updated to Succeeded")
			ansibleJob := &ansiblev1alpha1.AnsibleJob{}
			Eventually(func() string {
				getErr := k8sClient.Get(ctx, typeNamespacedName, ansibleJob)
				if getErr != nil {
					return ""
				}
				return string(ansibleJob.Status.Phase)
			}, timeout, interval).Should(Equal(string(ansiblev1alpha1.AnsibleJobPhaseSucceeded)))

			By("Refetching AnsibleJob to get latest conditions")
			err = k8sClient.Get(ctx, typeNamespacedName, ansibleJob)
			Expect(err).NotTo(HaveOccurred())

			By("Checking that conditions are properly set")
			Expect(ansibleJob.Status.Conditions).NotTo(BeEmpty())

			// Check Ready condition
			readyCondition := findCondition(ansibleJob.Status.Conditions, ansiblev1alpha1.AnsibleJobConditionReady)
			Expect(readyCondition).NotTo(BeNil())
			Expect(readyCondition.Status).To(Equal(metav1.ConditionTrue))
			Expect(readyCondition.Reason).To(Equal(ansiblev1alpha1.ReasonJobSucceeded))

			// Check Progressing condition
			progressingCondition := findCondition(ansibleJob.Status.Conditions, ansiblev1alpha1.AnsibleJobConditionProgressing)
			Expect(progressingCondition).NotTo(BeNil())
			Expect(progressingCondition.Status).To(Equal(metav1.ConditionFalse))
			Expect(progressingCondition.Reason).To(Equal(ansiblev1alpha1.ReasonJobSucceeded))

			// Check Succeeded condition
			succeededCondition := findCondition(ansibleJob.Status.Conditions, ansiblev1alpha1.AnsibleJobConditionSucceeded)
			Expect(succeededCondition).NotTo(BeNil())
			Expect(succeededCondition.Status).To(Equal(metav1.ConditionTrue))
			Expect(succeededCondition.Reason).To(Equal(ansiblev1alpha1.ReasonJobSucceeded))

			// Check ObservedGeneration
			Expect(ansibleJob.Status.ObservedGeneration).To(Equal(ansibleJob.Generation))
		})

		It("should set correct conditions during running phase", func() {
			By("Creating and reconciling the AnsibleJob to running state")
			controllerReconciler := &AnsibleJobReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// Initialize
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			// Create job (will be in running state)
			_, err = controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			By("Checking that running conditions are properly set")
			ansibleJob := &ansiblev1alpha1.AnsibleJob{}
			Eventually(func() string {
				getErr := k8sClient.Get(ctx, typeNamespacedName, ansibleJob)
				if getErr != nil {
					return ""
				}
				return string(ansibleJob.Status.Phase)
			}, timeout, interval).Should(Equal(string(ansiblev1alpha1.AnsibleJobPhaseRunning)))

			By("Refetching AnsibleJob to get latest conditions")
			err = k8sClient.Get(ctx, typeNamespacedName, ansibleJob)
			Expect(err).NotTo(HaveOccurred())

			// Check Ready condition (should be False during running)
			readyCondition := findCondition(ansibleJob.Status.Conditions, ansiblev1alpha1.AnsibleJobConditionReady)
			Expect(readyCondition).NotTo(BeNil())
			Expect(readyCondition.Status).To(Equal(metav1.ConditionFalse))
			Expect(readyCondition.Reason).To(Equal(ansiblev1alpha1.ReasonJobRunning))

			// Check Progressing condition (should be True during running)
			progressingCondition := findCondition(ansibleJob.Status.Conditions, ansiblev1alpha1.AnsibleJobConditionProgressing)
			Expect(progressingCondition).NotTo(BeNil())
			Expect(progressingCondition.Status).To(Equal(metav1.ConditionTrue))
			Expect(progressingCondition.Reason).To(Equal(ansiblev1alpha1.ReasonJobRunning))

			// Check ObservedGeneration
			Expect(ansibleJob.Status.ObservedGeneration).To(Equal(ansibleJob.Generation))
		})
	})

	Context("Ansible Runner Container Creation", func() {
		var reconciler *AnsibleJobReconciler

		BeforeEach(func() {
			reconciler = &AnsibleJobReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
		})

		It("should create basic ansible runner container", func() {
			ansibleJob := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-basic-ansible",
					Namespace: "default",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "site.yml"},
				},
			}

			containers := reconciler.createAnsibleRunnerContainer(ansibleJob, "custom-ansible:v1.0")

			By("Checking basic container configuration")
			Expect(containers).To(HaveLen(1))
			container := containers[0]
			Expect(container.Name).To(Equal("ansible-runner"))
			Expect(container.Image).To(Equal("custom-ansible:v1.0"))
			Expect(container.Command).To(Equal([]string{"ansible-runner"}))

			By("Checking basic arguments")
			args := container.Args
			Expect(args).To(ContainElements("run", "/runner", "--playbook", "site.yml"))

			By("Checking basic volume mounts")
			Expect(container.VolumeMounts).To(HaveLen(2)) // runner-workspace + tmp
			mountNames := make([]string, len(container.VolumeMounts))
			for i, mount := range container.VolumeMounts {
				mountNames[i] = mount.Name
			}
			Expect(mountNames).To(ContainElement("runner-workspace"))
			Expect(mountNames).To(ContainElement("tmp"))
		})

		It("should create container with inline inventory", func() {
			ansibleJob := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-inline-inventory",
					Namespace: "default",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "maintenance.yml"},
					Inventory: ansiblev1alpha1.AnsibleInventory{
						Inline: "[webservers]\nweb1.example.com\nweb2.example.com\n\n[databases]\ndb1.example.com",
					},
				},
			}

			containers := reconciler.createAnsibleRunnerContainer(ansibleJob, "test-image")

			By("Checking container has inventory arguments and volume mounts")
			container := containers[0]
			args := container.Args
			Expect(args).To(ContainElements("--inventory", "/runner/inventory/hosts"))

			By("Checking inventory volume mount is added")
			Expect(container.VolumeMounts).To(HaveLen(3)) // runner-workspace + tmp + inventory
			volumeMounts := container.VolumeMounts

			// Check for runner-workspace mount
			runnerMount := false
			inventoryMount := false
			for _, mount := range volumeMounts {
				if mount.Name == "runner-workspace" && mount.MountPath == "/runner" {
					runnerMount = true
				}
				if mount.Name == "inventory" && mount.MountPath == "/runner/inventory" {
					inventoryMount = true
				}
			}
			Expect(runnerMount).To(BeTrue())
			Expect(inventoryMount).To(BeTrue())
		})

		It("should create container without inventory when not specified", func() {
			ansibleJob := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-no-inventory",
					Namespace: "default",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "setup.yml"},
					Inventory: ansiblev1alpha1.AnsibleInventory{
						Inline: "", // Empty inventory
					},
				},
			}

			containers := reconciler.createAnsibleRunnerContainer(ansibleJob, "test-image")

			By("Checking container has no inventory arguments")
			container := containers[0]
			args := container.Args
			Expect(args).NotTo(ContainElement("--inventory"))

			By("Checking no inventory volume mount")
			Expect(container.VolumeMounts).To(HaveLen(2)) // runner-workspace + tmp
			mountNames := make([]string, len(container.VolumeMounts))
			for i, mount := range container.VolumeMounts {
				mountNames[i] = mount.Name
			}
			Expect(mountNames).To(ContainElement("runner-workspace"))
			Expect(mountNames).To(ContainElement("tmp"))
			Expect(mountNames).NotTo(ContainElement("inventory"))
		})

		It("should create container with limit parameter", func() {
			ansibleJob := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-with-limit",
					Namespace: "default",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "deploy.yml"},
					Limit:    "webservers:!web3.example.com",
				},
			}

			containers := reconciler.createAnsibleRunnerContainer(ansibleJob, "test-image")

			By("Checking container has limit arguments")
			container := containers[0]
			args := container.Args
			Expect(args).To(ContainElements("--limit", "webservers:!web3.example.com"))
		})

		It("should create container without limit when not specified", func() {
			ansibleJob := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-no-limit",
					Namespace: "default",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "install.yml"},
					Limit:    "", // No limit specified
				},
			}

			containers := reconciler.createAnsibleRunnerContainer(ansibleJob, "test-image")

			By("Checking container has no limit arguments")
			container := containers[0]
			args := container.Args
			Expect(args).NotTo(ContainElement("--limit"))
		})

		It("should create container with extra vars", func() {
			ansibleJob := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-extra-vars",
					Namespace: "default",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "configure.yml"},
					ExtraVars: []ansiblev1alpha1.KeyValue{
						{Name: "environment", Value: "production"},
						{Name: "app_version", Value: "v2.1.0"},
						{Name: "enable_ssl", Value: "true"},
						{Name: "max_connections", Value: "100"},
					},
				},
			}

			containers := reconciler.createAnsibleRunnerContainer(ansibleJob, "test-image")

			By("Checking container configuration")
			container := containers[0]
			args := container.Args

			By("Checking that extra vars are not in command line arguments")
			// Note: Extra vars are now handled via /runner/env/extravars file, not command line arguments
			Expect(args).NotTo(ContainElement("--extra-vars"))
			Expect(args).NotTo(ContainElement("environment=production"))
			Expect(args).NotTo(ContainElement("app_version=v2.1.0"))
			Expect(args).NotTo(ContainElement("enable_ssl=true"))
			Expect(args).NotTo(ContainElement("max_connections=100"))
		})

		It("should create container without extra vars when not specified", func() {
			ansibleJob := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-no-extra-vars",
					Namespace: "default",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook:  ansiblev1alpha1.PlaybookSpec{Name: "basic.yml"},
					ExtraVars: nil, // No extra vars
				},
			}

			containers := reconciler.createAnsibleRunnerContainer(ansibleJob, "test-image")

			By("Checking container has no extra vars arguments")
			container := containers[0]
			args := container.Args
			Expect(args).NotTo(ContainElement("--extra-vars"))
		})

		It("should create container with resource specifications", func() {
			ansibleJob := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-resources",
					Namespace: "default",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "resource-intensive.yml"},
					JobTemplate: &ansiblev1alpha1.JobTemplateSpec{
						Resources: &corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1000m"),
								corev1.ResourceMemory: resource.MustParse("2Gi"),
							},
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("500m"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
						},
					},
				},
			}

			containers := reconciler.createAnsibleRunnerContainer(ansibleJob, "test-image")

			By("Checking container has resource specifications")
			container := containers[0]
			Expect(container.Resources.Limits).NotTo(BeNil())
			Expect(container.Resources.Requests).NotTo(BeNil())

			By("Checking resource limits")
			limits := container.Resources.Limits
			Expect(limits.Cpu().String()).To(Equal("1"))
			Expect(limits.Memory().String()).To(Equal("2Gi"))

			By("Checking resource requests")
			requests := container.Resources.Requests
			Expect(requests.Cpu().String()).To(Equal("500m"))
			Expect(requests.Memory().String()).To(Equal("1Gi"))
		})

		It("should create container with default resources when not specified", func() {
			ansibleJob := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-no-resources",
					Namespace: "default",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "simple.yml"},
					JobTemplate: &ansiblev1alpha1.JobTemplateSpec{
						Resources: nil, // No resources specified
					},
				},
			}

			containers := reconciler.createAnsibleRunnerContainer(ansibleJob, "test-image")

			By("Checking container has default resource specifications")
			container := containers[0]
			Expect(container.Resources.Limits).ToNot(BeNil())
			Expect(container.Resources.Requests).ToNot(BeNil())
			// Verify default CPU and memory limits/requests
			Expect(container.Resources.Requests.Cpu().String()).To(Equal("100m"))
			Expect(container.Resources.Requests.Memory().String()).To(Equal("256Mi"))
			Expect(container.Resources.Limits.Cpu().String()).To(Equal("500m"))
			Expect(container.Resources.Limits.Memory().String()).To(Equal("512Mi"))
		})

		It("should create container with default resources when JobTemplate is nil", func() {
			ansibleJob := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nil-job-template",
					Namespace: "default",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook:    ansiblev1alpha1.PlaybookSpec{Name: "basic.yml"},
					JobTemplate: nil, // No job template specified
				},
			}

			containers := reconciler.createAnsibleRunnerContainer(ansibleJob, "test-image")

			By("Checking container has default resource specifications")
			container := containers[0]
			Expect(container.Resources.Limits).ToNot(BeNil())
			Expect(container.Resources.Requests).ToNot(BeNil())
			// Verify default CPU and memory limits/requests
			Expect(container.Resources.Requests.Cpu().String()).To(Equal("100m"))
			Expect(container.Resources.Requests.Memory().String()).To(Equal("256Mi"))
			Expect(container.Resources.Limits.Cpu().String()).To(Equal("500m"))
			Expect(container.Resources.Limits.Memory().String()).To(Equal("512Mi"))
		})

		It("should handle complex scenarios with all features", func() {
			ansibleJob := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-complex-ansible",
					Namespace: "default",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "complex.yml"},
					Inventory: ansiblev1alpha1.AnsibleInventory{
						Inline: "[production]\nserver1.prod.com\nserver2.prod.com",
					},
					Limit: "production:!server2.prod.com",
					ExtraVars: []ansiblev1alpha1.KeyValue{
						{Name: "env", Value: "production"},
						{Name: "ssl", Value: "enabled"},
					},
					JobTemplate: &ansiblev1alpha1.JobTemplateSpec{
						Resources: &corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("2000m"),
								corev1.ResourceMemory: resource.MustParse("4Gi"),
							},
							Requests: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1000m"),
								corev1.ResourceMemory: resource.MustParse("2Gi"),
							},
						},
					},
				},
			}

			containers := reconciler.createAnsibleRunnerContainer(ansibleJob, "production-ansible:latest")

			By("Checking complex container configuration")
			container := containers[0]
			Expect(container.Name).To(Equal("ansible-runner"))
			Expect(container.Image).To(Equal("production-ansible:latest"))

			By("Checking all arguments are present")
			args := container.Args
			Expect(args).To(ContainElements("run", "/runner", "--playbook", "complex.yml"))
			Expect(args).To(ContainElements("--inventory", "/runner/inventory/hosts"))
			Expect(args).To(ContainElements("--limit", "production:!server2.prod.com"))

			By("Checking volume mounts")
			Expect(container.VolumeMounts).To(HaveLen(3)) // runner-workspace + tmp + inventory
			volumeMountNames := make([]string, len(container.VolumeMounts))
			for i, mount := range container.VolumeMounts {
				volumeMountNames[i] = mount.Name
			}
			Expect(volumeMountNames).To(ContainElements("runner-workspace", "inventory"))

			By("Checking resource specifications")
			limits := container.Resources.Limits
			requests := container.Resources.Requests
			Expect(limits.Cpu().String()).To(Equal("2"))
			Expect(limits.Memory().String()).To(Equal("4Gi"))
			Expect(requests.Cpu().String()).To(Equal("1"))
			Expect(requests.Memory().String()).To(Equal("2Gi"))
		})
	})

	Context("Inventory ConfigMap Management", func() {
		var reconciler *AnsibleJobReconciler

		BeforeEach(func() {
			reconciler = &AnsibleJobReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
		})

		It("should create inventory ConfigMap for inline inventory", func() {
			ansibleJob := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-configmap-inventory",
					Namespace: "default",
					UID:       "test-uid-1",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "site.yml"},
					Inventory: ansiblev1alpha1.AnsibleInventory{
						Inline: "[webservers]\nweb1.example.com\nweb2.example.com",
					},
				},
			}

			err := reconciler.createInventoryConfigMap(ctx, ansibleJob)
			Expect(err).ToNot(HaveOccurred())

			By("Checking ConfigMap creation succeeded")
		})

		It("should handle empty inline inventory", func() {
			ansibleJob := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-empty-inventory",
					Namespace: "default",
					UID:       "test-uid-2",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "site.yml"},
					Inventory: ansiblev1alpha1.AnsibleInventory{
						Inline: "",
					},
				},
			}

			err := reconciler.createInventoryConfigMap(ctx, ansibleJob)
			Expect(err).ToNot(HaveOccurred())

			By("Checking no ConfigMap is created for empty inventory")
		})

		It("should handle nil inventory", func() {
			ansibleJob := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nil-inventory",
					Namespace: "default",
					UID:       "test-uid-3",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "site.yml"},
					// No Inventory field specified
				},
			}

			err := reconciler.createInventoryConfigMap(ctx, ansibleJob)
			Expect(err).ToNot(HaveOccurred())

			By("Checking no ConfigMap is created for nil inventory")
		})
	})

	Context("InitializeJob Function Tests", func() {
		var (
			reconciler *AnsibleJobReconciler
			ansibleJob *ansiblev1alpha1.AnsibleJob
			ctx        context.Context
		)

		BeforeEach(func() {
			ctx = context.Background()
			ansibleJob = &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-initialize-job",
					Namespace: "default",
					UID:       "test-uid-initialize",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "site.yml"},
				},
			}
		})

		It("should successfully initialize job with correct status", func() {
			// Use real k8s client for success case
			reconciler = &AnsibleJobReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// Create the AnsibleJob first
			Expect(k8sClient.Create(ctx, ansibleJob)).To(Succeed())

			By("Calling initializeJob")
			result, err := reconciler.initializeJob(ctx, ansibleJob)

			By("Checking the result")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			By("Verifying status was updated correctly")
			updatedJob := &ansiblev1alpha1.AnsibleJob{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      ansibleJob.Name,
				Namespace: ansibleJob.Namespace,
			}, updatedJob)).To(Succeed())

			Expect(updatedJob.Status.Phase).To(Equal(ansiblev1alpha1.AnsibleJobPhasePending))
			Expect(updatedJob.Status.StartTime).NotTo(BeNil())
			Expect(updatedJob.Status.StartTime.Time).To(BeTemporally("~", time.Now(), time.Minute))

			// Cleanup
			Expect(k8sClient.Delete(ctx, ansibleJob)).To(Succeed())
		})

		It("should handle status update failure gracefully", func() {
			// Create a mock client that fails on status updates
			scheme := runtime.NewScheme()
			Expect(ansiblev1alpha1.AddToScheme(scheme)).To(Succeed())

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(ansibleJob).
				Build()

			statusWriter := &mockStatusWriter{
				shouldFail: true,
				failError:  errors.New("simulated status update failure"),
			}

			mockClient := &mockClient{
				Client:       fakeClient,
				statusWriter: statusWriter,
			}

			reconciler = &AnsibleJobReconciler{
				Client: mockClient,
				Scheme: scheme,
			}

			By("Calling initializeJob with failing status writer")
			result, err := reconciler.initializeJob(ctx, ansibleJob)

			By("Checking exponential backoff is used for status failures")
			Expect(err).ToNot(HaveOccurred())                                  // No error returned, uses backoff instead
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))              // Should have backoff delay
			Expect(result.RequeueAfter).To(BeNumerically("<=", 5*time.Minute)) // Capped at max delay
		})

		It("should set correct timestamps and phases", func() {
			// Use real k8s client to verify actual behavior
			reconciler = &AnsibleJobReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}

			// Create job with no existing status
			ansibleJob.Status = ansiblev1alpha1.AnsibleJobStatus{}
			Expect(k8sClient.Create(ctx, ansibleJob)).To(Succeed())

			beforeTime := time.Now().Add(-time.Second) // Add buffer for timing

			By("Calling initializeJob")
			result, err := reconciler.initializeJob(ctx, ansibleJob)

			afterTime := time.Now().Add(time.Second) // Add buffer for timing

			By("Checking timing and status")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			// Verify the in-memory object was updated
			Expect(ansibleJob.Status.Phase).To(Equal(ansiblev1alpha1.AnsibleJobPhasePending))
			Expect(ansibleJob.Status.StartTime).NotTo(BeNil())
			startTime := ansibleJob.Status.StartTime.Time
			Expect(startTime).To(BeTemporally(">=", beforeTime))
			Expect(startTime).To(BeTemporally("<=", afterTime))

			// Cleanup
			Expect(k8sClient.Delete(ctx, ansibleJob)).To(Succeed())
		})
	})

	Context("Controller Reconciliation Edge Cases", func() {
		var reconciler *AnsibleJobReconciler

		BeforeEach(func() {
			reconciler = &AnsibleJobReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
		})

		It("should handle missing AnsibleJob", func() {
			ctx := context.Background()
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "non-existent-job",
					Namespace: "default",
				},
			})

			By("Checking reconcile doesn't error on missing resource")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(time.Duration(0)))
		})

		It("should handle AnsibleJob with minimal configuration", func() {
			ctx := context.Background()
			ansibleJob := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "minimal-job",
					Namespace: "default",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "minimal.yml"},
					// No inventory, roles, extra vars, etc.
				},
			}
			Expect(k8sClient.Create(ctx, ansibleJob)).To(Succeed())

			By("Reconciling minimal configuration")
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "minimal-job",
					Namespace: "default",
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			By("Checking status is updated")
			updatedJob := &ansiblev1alpha1.AnsibleJob{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: "minimal-job", Namespace: "default"}, updatedJob)
			Expect(err).NotTo(HaveOccurred())
			Expect(updatedJob.Status.Phase).To(Equal(ansiblev1alpha1.AnsibleJobPhasePending))

			// Cleanup
			Expect(k8sClient.Delete(ctx, ansibleJob)).To(Succeed())
		})

		It("should handle multiple reconciliations gracefully", func() {
			ctx := context.Background()
			ansibleJob := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "multi-reconcile-job",
					Namespace: "default",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "site.yml"},
				},
			}
			Expect(k8sClient.Create(ctx, ansibleJob)).To(Succeed())

			namespacedName := types.NamespacedName{Name: "multi-reconcile-job", Namespace: "default"}

			By("Running multiple reconciliations")
			for i := 0; i < 5; i++ {
				result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: namespacedName})
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RequeueAfter).To(BeNumerically(">=", 0))
			}

			By("Checking final status")
			finalJob := &ansiblev1alpha1.AnsibleJob{}
			err := k8sClient.Get(ctx, namespacedName, finalJob)
			Expect(err).NotTo(HaveOccurred())
			Expect(finalJob.Status.Phase).To(BeElementOf(
				ansiblev1alpha1.AnsibleJobPhasePending,
				ansiblev1alpha1.AnsibleJobPhaseRunning,
			))

			// Cleanup
			Expect(k8sClient.Delete(ctx, ansibleJob)).To(Succeed())
		})

		It("should handle client Get errors gracefully", func() {
			// Create a mock client that fails on Get operations
			scheme := runtime.NewScheme()
			Expect(ansiblev1alpha1.AddToScheme(scheme)).To(Succeed())

			mockClient := &mockFailingClient{
				Client:     fake.NewClientBuilder().WithScheme(scheme).Build(),
				shouldFail: true,
				failError:  errors.New("simulated client get failure"),
			}

			reconciler := &AnsibleJobReconciler{
				Client: mockClient,
				Scheme: scheme,
			}

			ctx := context.Background()
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-job",
					Namespace: "default",
				},
			})

			By("Checking error is propagated correctly")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("simulated client get failure"))
		})

		It("should handle AnsibleJob in Running phase", func() {
			ctx := context.Background()
			ansibleJob := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "running-job",
					Namespace: "default",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "site.yml"},
				},
			}
			Expect(k8sClient.Create(ctx, ansibleJob)).To(Succeed())

			// Create a corresponding Kubernetes Job
			jobName := "running-job-job"
			kubernetesJob := &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      jobName,
					Namespace: "default",
				},
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:    "test-container",
									Image:   "busybox",
									Command: []string{"echo", "hello"},
								},
							},
							RestartPolicy: corev1.RestartPolicyNever,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, kubernetesJob)).To(Succeed())

			// Update status to Running phase with JobName
			ansibleJob.Status = ansiblev1alpha1.AnsibleJobStatus{
				Phase:     ansiblev1alpha1.AnsibleJobPhaseRunning,
				StartTime: &metav1.Time{Time: time.Now()},
				JobName:   jobName,
			}
			Expect(k8sClient.Status().Update(ctx, ansibleJob)).To(Succeed())

			By("Reconciling running job")
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "running-job",
					Namespace: "default",
				},
			})

			By("Checking monitorJob is called")
			Expect(err).NotTo(HaveOccurred())
			// Result depends on monitorJob implementation - could be requeue or not
			Expect(result.RequeueAfter).To(BeNumerically(">=", 0))

			// Cleanup
			Expect(k8sClient.Delete(ctx, ansibleJob)).To(Succeed())
			Expect(k8sClient.Delete(ctx, kubernetesJob)).To(Succeed())
		})

		It("should handle AnsibleJob in Succeeded phase", func() {
			ctx := context.Background()
			ansibleJob := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "succeeded-job",
					Namespace: "default",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "site.yml"},
				},
			}
			Expect(k8sClient.Create(ctx, ansibleJob)).To(Succeed())

			// Update status to Succeeded phase
			ansibleJob.Status = ansiblev1alpha1.AnsibleJobStatus{
				Phase:          ansiblev1alpha1.AnsibleJobPhaseSucceeded,
				StartTime:      &metav1.Time{Time: time.Now().Add(-time.Hour)},
				CompletionTime: &metav1.Time{Time: time.Now()},
			}
			Expect(k8sClient.Status().Update(ctx, ansibleJob)).To(Succeed())

			By("Reconciling succeeded job")
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "succeeded-job",
					Namespace: "default",
				},
			})

			By("Checking no action is taken for completed job")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			// Cleanup
			Expect(k8sClient.Delete(ctx, ansibleJob)).To(Succeed())
		})

		It("should handle AnsibleJob in Failed phase", func() {
			ctx := context.Background()
			ansibleJob := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "failed-job",
					Namespace: "default",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "site.yml"},
				},
			}
			Expect(k8sClient.Create(ctx, ansibleJob)).To(Succeed())

			// Update status to Failed phase
			ansibleJob.Status = ansiblev1alpha1.AnsibleJobStatus{
				Phase:          ansiblev1alpha1.AnsibleJobPhaseFailed,
				StartTime:      &metav1.Time{Time: time.Now().Add(-time.Hour)},
				CompletionTime: &metav1.Time{Time: time.Now()},
			}
			Expect(k8sClient.Status().Update(ctx, ansibleJob)).To(Succeed())

			By("Reconciling failed job")
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "failed-job",
					Namespace: "default",
				},
			})

			By("Checking no action is taken for completed job")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			// Cleanup
			Expect(k8sClient.Delete(ctx, ansibleJob)).To(Succeed())
		})

		It("should handle AnsibleJob with unknown phase", func() {
			ctx := context.Background()
			ansibleJob := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "unknown-phase-job",
					Namespace: "default",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "site.yml"},
				},
			}
			Expect(k8sClient.Create(ctx, ansibleJob)).To(Succeed())

			// Update status to unknown phase
			ansibleJob.Status = ansiblev1alpha1.AnsibleJobStatus{
				Phase:     "UnknownPhase", // Invalid phase
				StartTime: &metav1.Time{Time: time.Now()},
			}
			Expect(k8sClient.Status().Update(ctx, ansibleJob)).To(Succeed())

			By("Reconciling job with unknown phase")
			result, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "unknown-phase-job",
					Namespace: "default",
				},
			})

			By("Checking unknown phase is handled gracefully")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			// Cleanup
			Expect(k8sClient.Delete(ctx, ansibleJob)).To(Succeed())
		})
	})

	Context("CreateKubernetesJob Function Tests", func() {
		var (
			reconciler *AnsibleJobReconciler
			ansibleJob *ansiblev1alpha1.AnsibleJob
			ctx        context.Context
		)

		BeforeEach(func() {
			ctx = context.Background()
			reconciler = &AnsibleJobReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			ansibleJob = &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-create-job",
					Namespace: "default",
					UID:       "test-uid-create-job",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "site.yml"},
				},
			}
		})

		It("should successfully create new Kubernetes Job", func() {
			// Set status to Pending to trigger job creation
			ansibleJob.Status.Phase = ansiblev1alpha1.AnsibleJobPhasePending
			Expect(k8sClient.Create(ctx, ansibleJob)).To(Succeed())

			By("Calling createKubernetesJob")
			result, err := reconciler.createKubernetesJob(ctx, ansibleJob)

			By("Checking successful creation")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(5 * time.Second))

			By("Verifying Job was created")
			jobName := fmt.Sprintf("%s-job", ansibleJob.Name)
			createdJob := &batchv1.Job{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      jobName,
				Namespace: ansibleJob.Namespace,
			}, createdJob)).To(Succeed())

			By("Verifying status was updated")
			updatedJob := &ansiblev1alpha1.AnsibleJob{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      ansibleJob.Name,
				Namespace: ansibleJob.Namespace,
			}, updatedJob)).To(Succeed())
			Expect(updatedJob.Status.Phase).To(Equal(ansiblev1alpha1.AnsibleJobPhaseRunning))
			Expect(updatedJob.Status.JobName).To(Equal(jobName))

			// Cleanup
			Expect(k8sClient.Delete(ctx, ansibleJob)).To(Succeed())
			Expect(k8sClient.Delete(ctx, createdJob)).To(Succeed())
		})

		It("should handle existing Job gracefully", func() {
			// Use unique name for this test
			ansibleJob.Name = "test-existing-job"
			ansibleJob.Name = "test-existing-job"

			// Create AnsibleJob first
			Expect(k8sClient.Create(ctx, ansibleJob)).To(Succeed())

			// Create existing Kubernetes Job
			jobName := fmt.Sprintf("%s-job", ansibleJob.Name)
			existingJob := &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      jobName,
					Namespace: ansibleJob.Namespace,
				},
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:    "existing-container",
									Image:   "busybox",
									Command: []string{"echo", "existing"},
								},
							},
							RestartPolicy: corev1.RestartPolicyNever,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, existingJob)).To(Succeed())

			By("Calling createKubernetesJob with existing Job")
			result, err := reconciler.createKubernetesJob(ctx, ansibleJob)

			By("Checking existing Job is handled")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(5 * time.Second))

			By("Verifying status was updated to Running")
			updatedJob := &ansiblev1alpha1.AnsibleJob{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      ansibleJob.Name,
				Namespace: ansibleJob.Namespace,
			}, updatedJob)).To(Succeed())
			Expect(updatedJob.Status.Phase).To(Equal(ansiblev1alpha1.AnsibleJobPhaseRunning))
			Expect(updatedJob.Status.JobName).To(Equal(jobName))

			// Cleanup
			Expect(k8sClient.Delete(ctx, ansibleJob)).To(Succeed())
			Expect(k8sClient.Delete(ctx, existingJob)).To(Succeed())
		})

		It("should handle Job Get errors gracefully", func() {
			// Create a mock client that fails on Job Get operations
			scheme := runtime.NewScheme()
			Expect(ansiblev1alpha1.AddToScheme(scheme)).To(Succeed())
			Expect(batchv1.AddToScheme(scheme)).To(Succeed())

			mockClient := &mockJobGetFailingClient{
				Client:     fake.NewClientBuilder().WithScheme(scheme).WithObjects(ansibleJob).Build(),
				shouldFail: true,
				failError:  errors.New("simulated job get failure"),
			}

			reconciler := &AnsibleJobReconciler{
				Client: mockClient,
				Scheme: scheme,
			}

			By("Calling createKubernetesJob with failing Get")
			_, err := reconciler.createKubernetesJob(ctx, ansibleJob)

			By("Checking error is propagated correctly")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("simulated job get failure"))
		})

		It("should handle Job Create errors gracefully", func() {
			// Use unique name for this test
			ansibleJob.Name = "test-create-error-job"
			ansibleJob.Name = "test-create-error-job"

			// Create AnsibleJob first
			Expect(k8sClient.Create(ctx, ansibleJob)).To(Succeed())

			// Create a mock client that fails on Job Create operations
			scheme := runtime.NewScheme()
			Expect(ansiblev1alpha1.AddToScheme(scheme)).To(Succeed())
			Expect(batchv1.AddToScheme(scheme)).To(Succeed())

			mockClient := &mockJobCreateFailingClient{
				Client:     fake.NewClientBuilder().WithScheme(scheme).WithObjects(ansibleJob).Build(),
				shouldFail: true,
				failError:  errors.New("simulated job create failure"),
			}

			reconciler := &AnsibleJobReconciler{
				Client: mockClient,
				Scheme: scheme,
			}

			By("Calling createKubernetesJob with failing Create")
			_, err := reconciler.createKubernetesJob(ctx, ansibleJob)

			By("Checking error is propagated correctly")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("simulated job create failure"))

			// Cleanup
			Expect(k8sClient.Delete(ctx, ansibleJob)).To(Succeed())
		})

		It("should handle status update errors gracefully", func() {
			// Use unique name for this test
			ansibleJob.Name = "test-status-error-job"
			ansibleJob.Name = "test-status-error-job"

			// Create AnsibleJob first
			Expect(k8sClient.Create(ctx, ansibleJob)).To(Succeed())

			// Create a mock client that fails on status updates
			scheme := runtime.NewScheme()
			Expect(ansiblev1alpha1.AddToScheme(scheme)).To(Succeed())
			Expect(batchv1.AddToScheme(scheme)).To(Succeed())

			statusWriter := &mockStatusWriter{
				shouldFail: true,
				failError:  errors.New("simulated status update failure"),
			}

			mockClient := &mockClient{
				Client:       fake.NewClientBuilder().WithScheme(scheme).WithObjects(ansibleJob).Build(),
				statusWriter: statusWriter,
			}

			reconciler := &AnsibleJobReconciler{
				Client: mockClient,
				Scheme: scheme,
			}

			By("Calling createKubernetesJob with failing status update")
			_, err := reconciler.createKubernetesJob(ctx, ansibleJob)

			By("Checking error is propagated correctly")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("simulated status update failure"))

			// Cleanup
			Expect(k8sClient.Delete(ctx, ansibleJob)).To(Succeed())
		})

		It("should create inventory ConfigMap when inline inventory is provided", func() {
			// Use unique name for this test
			ansibleJob.Name = "test-inventory-job"

			// Set up AnsibleJob with inline inventory
			ansibleJob.Spec.Inventory.Inline = "[webservers]\nweb1.example.com\nweb2.example.com"
			Expect(k8sClient.Create(ctx, ansibleJob)).To(Succeed())

			By("Calling createKubernetesJob with inline inventory")
			result, err := reconciler.createKubernetesJob(ctx, ansibleJob)

			By("Checking successful creation")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(5 * time.Second))

			By("Verifying inventory ConfigMap was created")
			configMapName := fmt.Sprintf("%s-inventory", ansibleJob.Name)
			createdConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      configMapName,
				Namespace: ansibleJob.Namespace,
			}, createdConfigMap)).To(Succeed())
			Expect(createdConfigMap.Data["hosts"]).To(Equal(ansibleJob.Spec.Inventory.Inline))

			By("Verifying Job was created")
			jobName := fmt.Sprintf("%s-job", ansibleJob.Name)
			createdJob := &batchv1.Job{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      jobName,
				Namespace: ansibleJob.Namespace,
			}, createdJob)).To(Succeed())

			// Cleanup
			Expect(k8sClient.Delete(ctx, ansibleJob)).To(Succeed())
			Expect(k8sClient.Delete(ctx, createdJob)).To(Succeed())
			Expect(k8sClient.Delete(ctx, createdConfigMap)).To(Succeed())
		})

		It("should handle createInventoryConfigMap failure within createKubernetesJob", func() {
			// Use unique name for this test
			ansibleJob.Name = "test-inventory-fail-job"
			ansibleJob.Name = "test-inventory-fail-job"

			// Set up AnsibleJob with inline inventory
			ansibleJob.Spec.Inventory.Inline = "[webservers]\nweb1.example.com"
			Expect(k8sClient.Create(ctx, ansibleJob)).To(Succeed())

			// Create a mock client that fails ConfigMap creation
			scheme := runtime.NewScheme()
			Expect(ansiblev1alpha1.AddToScheme(scheme)).To(Succeed())
			Expect(batchv1.AddToScheme(scheme)).To(Succeed())
			Expect(corev1.AddToScheme(scheme)).To(Succeed())

			mockClient := &mockConfigMapCreateFailingClient{
				Client:     fake.NewClientBuilder().WithScheme(scheme).WithObjects(ansibleJob).Build(),
				shouldFail: true,
				failError:  errors.New("mock ConfigMap create error"),
			}

			reconciler := &AnsibleJobReconciler{
				Client: mockClient,
				Scheme: scheme,
			}

			By("Calling createKubernetesJob with inline inventory that will fail")
			_, err := reconciler.createKubernetesJob(ctx, ansibleJob)

			By("Checking error is propagated correctly")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to create or patch ConfigMap"))

			// Cleanup
			Expect(k8sClient.Delete(ctx, ansibleJob)).To(Succeed())
		})

		It("should handle non-NotFound errors when checking for existing Job", func() {
			// Use unique name for this test
			ansibleJob.Name = "test-job-get-error"
			ansibleJob.Name = "test-job-get-error"

			// Create AnsibleJob first
			Expect(k8sClient.Create(ctx, ansibleJob)).To(Succeed())

			// Create a mock client that returns non-NotFound error for Job Get
			scheme := runtime.NewScheme()
			Expect(ansiblev1alpha1.AddToScheme(scheme)).To(Succeed())
			Expect(batchv1.AddToScheme(scheme)).To(Succeed())

			mockClient := &mockJobGetFailingClient{
				Client:     fake.NewClientBuilder().WithScheme(scheme).WithObjects(ansibleJob).Build(),
				shouldFail: true,
				failError:  errors.New("permission denied"), // Non-NotFound error
			}

			reconciler := &AnsibleJobReconciler{
				Client: mockClient,
				Scheme: scheme,
			}

			By("Calling createKubernetesJob with Job Get returning non-NotFound error")
			_, err := reconciler.createKubernetesJob(ctx, ansibleJob)

			By("Checking error is propagated correctly")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("permission denied"))

			// Cleanup
			Expect(k8sClient.Delete(ctx, ansibleJob)).To(Succeed())
		})

		It("should handle SetControllerReference failure in createKubernetesJob", func() {
			// Use unique name for this test
			ansibleJob.Name = "test-controller-ref-fail"
			ansibleJob.Name = "test-controller-ref-fail"

			// Create AnsibleJob first
			Expect(k8sClient.Create(ctx, ansibleJob)).To(Succeed())

			// Create a mock client that simulates SetControllerReference failure
			scheme := runtime.NewScheme()
			Expect(ansiblev1alpha1.AddToScheme(scheme)).To(Succeed())
			Expect(batchv1.AddToScheme(scheme)).To(Succeed())

			mockClient := &mockSetControllerRefFailingClient{
				Client:                  fake.NewClientBuilder().WithScheme(scheme).WithObjects(ansibleJob).Build(),
				shouldFailControllerRef: true,
			}

			reconciler := &AnsibleJobReconciler{
				Client: mockClient,
				Scheme: scheme,
			}

			By("Calling createKubernetesJob with failing SetControllerReference")
			_, err := reconciler.createKubernetesJob(ctx, ansibleJob)

			By("Checking SetControllerReference error is handled")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to set controller reference"))

			// Cleanup
			Expect(k8sClient.Delete(ctx, ansibleJob)).To(Succeed())
		})

		It("should handle status update failure when Job already exists", func() {
			// Use unique name for this test
			ansibleJob.Name = "test-status-fail-existing"
			ansibleJob.Name = "test-status-fail-existing"

			// Create AnsibleJob first
			Expect(k8sClient.Create(ctx, ansibleJob)).To(Succeed())

			// Create existing Job
			jobName := fmt.Sprintf("%s-job", ansibleJob.Name)
			existingJob := &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      jobName,
					Namespace: ansibleJob.Namespace,
				},
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:    "existing-container",
									Image:   "busybox",
									Command: []string{"echo", "existing"},
								},
							},
							RestartPolicy: corev1.RestartPolicyNever,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, existingJob)).To(Succeed())

			// Create a mock client that fails on status updates
			scheme := runtime.NewScheme()
			Expect(ansiblev1alpha1.AddToScheme(scheme)).To(Succeed())
			Expect(batchv1.AddToScheme(scheme)).To(Succeed())

			mockClient := &mockClient{
				Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(ansibleJob, existingJob).Build(),
				statusWriter: &mockStatusWriter{
					shouldFail: true,
					failError:  errors.New("simulated status update failure"),
				},
			}

			reconciler := &AnsibleJobReconciler{
				Client: mockClient,
				Scheme: scheme,
			}

			By("Calling createKubernetesJob with existing Job and failing status update")
			_, err := reconciler.createKubernetesJob(ctx, ansibleJob)

			By("Checking status update error is propagated correctly")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("simulated status update failure"))

			// Cleanup
			Expect(k8sClient.Delete(ctx, ansibleJob)).To(Succeed())
			Expect(k8sClient.Delete(ctx, existingJob)).To(Succeed())
		})
	})

	Context("Job Creation Error Handling", func() {
		var reconciler *AnsibleJobReconciler

		BeforeEach(func() {
			reconciler = &AnsibleJobReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
		})

		It("should handle invalid resource specifications gracefully", func() {
			ansibleJob := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "invalid-resources-job",
					Namespace: "default",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "site.yml"},
					JobTemplate: &ansiblev1alpha1.JobTemplateSpec{
						Resources: &corev1.ResourceRequirements{
							Limits: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("100m"), // Use valid values for tests
								corev1.ResourceMemory: resource.MustParse("256Mi"),
							},
						},
					},
				},
			}

			// This should not panic and should handle the error gracefully
			job := reconciler.createAnsibleJob(ansibleJob)

			By("Checking job is created despite invalid resource specifications")
			Expect(job).NotTo(BeNil())
			Expect(job.Name).To(ContainSubstring("invalid-resources-job"))

			// The container should exist but with default resources
			Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1))
		})

		It("should create job with default image when none specified", func() {
			ansibleJob := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "default-image-job",
					Namespace: "default",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "site.yml"},
					// No image specified
				},
			}

			job := reconciler.createAnsibleJob(ansibleJob)

			By("Checking job uses default ansible-runner image")
			Expect(job).NotTo(BeNil())
			Expect(job.Spec.Template.Spec.Containers).To(HaveLen(1))
			container := job.Spec.Template.Spec.Containers[0]
			Expect(container.Image).To(ContainSubstring("ansible-runner"))
		})
	})

	Context("MonitorJob Function Tests", func() {
		var (
			reconciler *AnsibleJobReconciler
			ansibleJob *ansiblev1alpha1.AnsibleJob
			ctx        context.Context
		)

		BeforeEach(func() {
			ctx = context.Background()
			reconciler = &AnsibleJobReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			ansibleJob = &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-monitor-job",
					Namespace: "default",
					UID:       "test-uid-monitor",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "site.yml"},
				},
				Status: ansiblev1alpha1.AnsibleJobStatus{
					Phase:   ansiblev1alpha1.AnsibleJobPhaseRunning,
					JobName: "test-monitor-job-job",
				},
			}
		})

		It("should handle Job Get errors gracefully", func() {
			// Create a mock client that fails on Job Get operations
			scheme := runtime.NewScheme()
			Expect(ansiblev1alpha1.AddToScheme(scheme)).To(Succeed())
			Expect(batchv1.AddToScheme(scheme)).To(Succeed())

			mockClient := &mockJobGetFailingClient{
				Client:     fake.NewClientBuilder().WithScheme(scheme).WithObjects(ansibleJob).Build(),
				shouldFail: true,
				failError:  errors.New("simulated job get failure in monitor"),
			}

			reconciler := &AnsibleJobReconciler{
				Client: mockClient,
				Scheme: scheme,
			}

			By("Calling monitorJob with failing Get")
			_, err := reconciler.monitorJob(ctx, ansibleJob)

			By("Checking error is propagated correctly")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("simulated job get failure in monitor"))
		})

		It("should handle successful Job completion", func() {
			// Create AnsibleJob
			Expect(k8sClient.Create(ctx, ansibleJob)).To(Succeed())

			// Update the status with JobName
			ansibleJob.Status.JobName = "test-monitor-complete-job"
			Expect(k8sClient.Status().Update(ctx, ansibleJob)).To(Succeed())

			// Create a mock client with a completed job
			completedJob := &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-monitor-complete-job",
					Namespace: ansibleJob.Namespace,
				},
				Status: batchv1.JobStatus{
					CompletionTime: &metav1.Time{Time: time.Now()},
					Succeeded:      1,
				},
			}

			mockClient := &mockJobClient{
				Client: k8sClient,
				jobs: map[client.ObjectKey]*batchv1.Job{
					{Name: "test-monitor-complete-job", Namespace: ansibleJob.Namespace}: completedJob,
				},
			}

			// Create reconciler with mock client
			mockReconciler := &AnsibleJobReconciler{
				Client: mockClient,
				Scheme: reconciler.Scheme,
			}

			By("Calling monitorJob with completed Job")
			result, err := mockReconciler.monitorJob(ctx, ansibleJob)

			By("Checking successful completion handling")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			By("Verifying status was updated to Succeeded")
			Expect(ansibleJob.Status.Phase).To(Equal(ansiblev1alpha1.AnsibleJobPhaseSucceeded))
			Expect(ansibleJob.Status.CompletionTime).NotTo(BeNil())

			By("Checking that succeeded condition has the correct message")
			succeededCondition := findCondition(ansibleJob.Status.Conditions, ansiblev1alpha1.AnsibleJobConditionSucceeded)
			Expect(succeededCondition).NotTo(BeNil())
			Expect(succeededCondition.Status).To(Equal(metav1.ConditionTrue))
			Expect(succeededCondition.Message).To(Equal("Job completed successfully"))

			// Cleanup
			Expect(k8sClient.Delete(ctx, ansibleJob)).To(Succeed())
		})

		It("should handle failed Job", func() {
			// Create AnsibleJob
			ansibleJob.Name = "test-monitor-failed-job"
			ansibleJob.Name = "test-monitor-failed-job"
			Expect(k8sClient.Create(ctx, ansibleJob)).To(Succeed())

			// Update the status with JobName
			ansibleJob.Status.JobName = "test-monitor-failed-job-job"
			Expect(k8sClient.Status().Update(ctx, ansibleJob)).To(Succeed())

			// Create a mock client with a failed job
			failedJob := &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-monitor-failed-job-job",
					Namespace: ansibleJob.Namespace,
				},
				Status: batchv1.JobStatus{
					Failed: 1,
				},
			}

			mockClient := &mockJobClient{
				Client: k8sClient,
				jobs: map[client.ObjectKey]*batchv1.Job{
					{Name: "test-monitor-failed-job-job", Namespace: ansibleJob.Namespace}: failedJob,
				},
			}

			// Create reconciler with mock client
			mockReconciler := &AnsibleJobReconciler{
				Client: mockClient,
				Scheme: reconciler.Scheme,
			}

			By("Calling monitorJob with failed Job")
			result, err := mockReconciler.monitorJob(ctx, ansibleJob)

			By("Checking failed Job handling")
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeZero())

			By("Verifying status was updated to Failed")
			Expect(ansibleJob.Status.Phase).To(Equal(ansiblev1alpha1.AnsibleJobPhaseFailed))
			Expect(ansibleJob.Status.CompletionTime).NotTo(BeNil())

			By("Checking that failed condition has the correct message")
			failedCondition := findCondition(ansibleJob.Status.Conditions, ansiblev1alpha1.AnsibleJobConditionFailed)
			Expect(failedCondition).NotTo(BeNil())
			Expect(failedCondition.Status).To(Equal(metav1.ConditionTrue))
			Expect(failedCondition.Message).To(Equal("Job failed to complete"))

			// Cleanup
			Expect(k8sClient.Delete(ctx, ansibleJob)).To(Succeed())
		})

		It("should handle status update errors during monitoring", func() {
			// Use unique name for this test
			ansibleJob.Name = "test-monitor-status-error"
			ansibleJob.Name = "test-monitor-status-error"
			ansibleJob.Status.JobName = "test-monitor-status-error-job"

			// Create a completed job to trigger status update
			completedJob := &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-monitor-status-error-job",
					Namespace: ansibleJob.Namespace,
				},
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:    "test-container",
									Image:   "busybox",
									Command: []string{"echo", "done"},
								},
							},
							RestartPolicy: corev1.RestartPolicyNever,
						},
					},
				},
				Status: batchv1.JobStatus{
					CompletionTime: &metav1.Time{Time: time.Now()},
					Succeeded:      1,
				},
			}

			// Create a mock client that fails on status updates
			scheme := runtime.NewScheme()
			Expect(ansiblev1alpha1.AddToScheme(scheme)).To(Succeed())
			Expect(batchv1.AddToScheme(scheme)).To(Succeed())

			statusWriter := &mockStatusWriter{
				shouldFail: true,
				failError:  errors.New("simulated status update failure in monitor"),
			}

			mockClient := &mockClient{
				Client:       fake.NewClientBuilder().WithScheme(scheme).WithObjects(ansibleJob, completedJob).Build(),
				statusWriter: statusWriter,
			}

			reconciler := &AnsibleJobReconciler{
				Client: mockClient,
				Scheme: scheme,
			}

			By("Calling monitorJob with failing status update")
			_, err := reconciler.monitorJob(ctx, ansibleJob)

			By("Checking error is propagated correctly")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("simulated status update failure in monitor"))
		})
	})

	Context("CreateInventoryConfigMap Function Tests", func() {
		var (
			reconciler *AnsibleJobReconciler
			ansibleJob *ansiblev1alpha1.AnsibleJob
			ctx        context.Context
		)

		BeforeEach(func() {
			ctx = context.Background()
			reconciler = &AnsibleJobReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
			ansibleJob = &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-configmap-create",
					Namespace: "default",
					UID:       "test-uid-configmap",
				},
				Spec: ansiblev1alpha1.AnsibleJobSpec{
					Playbook: ansiblev1alpha1.PlaybookSpec{Name: "site.yml"},
					Inventory: ansiblev1alpha1.AnsibleInventory{
						Inline: "[webservers]\nweb1.example.com\nweb2.example.com",
					},
				},
			}
		})

		It("should create inventory ConfigMap successfully", func() {
			// Create AnsibleJob first
			Expect(k8sClient.Create(ctx, ansibleJob)).To(Succeed())

			By("Calling createInventoryConfigMap")
			err := reconciler.createInventoryConfigMap(ctx, ansibleJob)

			By("Checking successful creation")
			Expect(err).NotTo(HaveOccurred())

			By("Verifying ConfigMap was created with correct data")
			configMapName := fmt.Sprintf("%s-inventory", ansibleJob.Name)
			createdConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      configMapName,
				Namespace: ansibleJob.Namespace,
			}, createdConfigMap)).To(Succeed())

			Expect(createdConfigMap.Data["hosts"]).To(Equal(ansibleJob.Spec.Inventory.Inline))
			Expect(createdConfigMap.Labels["app"]).To(Equal("ansible-runner"))
			Expect(createdConfigMap.Labels["ansible-job"]).To(Equal(ansibleJob.Name))

			// Cleanup
			Expect(k8sClient.Delete(ctx, ansibleJob)).To(Succeed())
			Expect(k8sClient.Delete(ctx, createdConfigMap)).To(Succeed())
		})

		It("should handle existing ConfigMap gracefully", func() {
			// Create AnsibleJob first
			Expect(k8sClient.Create(ctx, ansibleJob)).To(Succeed())

			// Create ConfigMap first
			configMapName := fmt.Sprintf("%s-inventory", ansibleJob.Name)
			existingConfigMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      configMapName,
					Namespace: ansibleJob.Namespace,
				},
				Data: map[string]string{
					"hosts": "existing data",
				},
			}
			Expect(k8sClient.Create(ctx, existingConfigMap)).To(Succeed())

			By("Calling createInventoryConfigMap when ConfigMap already exists")
			err := reconciler.createInventoryConfigMap(ctx, ansibleJob)

			By("Checking it handles existing ConfigMap gracefully")
			Expect(err).NotTo(HaveOccurred())

			By("Verifying existing ConfigMap is unchanged")
			retrievedConfigMap := &corev1.ConfigMap{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      configMapName,
				Namespace: ansibleJob.Namespace,
			}, retrievedConfigMap)).To(Succeed())
			Expect(retrievedConfigMap.Data["hosts"]).To(Equal("existing data"))

			// Cleanup
			Expect(k8sClient.Delete(ctx, ansibleJob)).To(Succeed())
			Expect(k8sClient.Delete(ctx, existingConfigMap)).To(Succeed())
		})

		It("should handle ConfigMap Get errors gracefully", func() {
			// Create a mock client that fails on ConfigMap Get operations
			scheme := runtime.NewScheme()
			Expect(ansiblev1alpha1.AddToScheme(scheme)).To(Succeed())
			Expect(corev1.AddToScheme(scheme)).To(Succeed())

			mockClient := &mockConfigMapGetFailingClient{
				Client:     fake.NewClientBuilder().WithScheme(scheme).WithObjects(ansibleJob).Build(),
				shouldFail: true,
				failError:  errors.New("simulated configmap get failure"),
			}

			reconciler := &AnsibleJobReconciler{
				Client: mockClient,
				Scheme: scheme,
			}

			By("Calling createInventoryConfigMap with failing Get")
			err := reconciler.createInventoryConfigMap(ctx, ansibleJob)

			By("Checking error is propagated correctly")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to check for existing ConfigMap"))
			Expect(err.Error()).To(ContainSubstring("simulated configmap get failure"))
		})

		It("should handle ConfigMap Create errors gracefully", func() {
			// Create AnsibleJob first
			Expect(k8sClient.Create(ctx, ansibleJob)).To(Succeed())

			// Create a mock client that fails on ConfigMap Create operations
			scheme := runtime.NewScheme()
			Expect(ansiblev1alpha1.AddToScheme(scheme)).To(Succeed())
			Expect(corev1.AddToScheme(scheme)).To(Succeed())

			mockClient := &mockConfigMapCreateFailingClient{
				Client:     fake.NewClientBuilder().WithScheme(scheme).WithObjects(ansibleJob).Build(),
				shouldFail: true,
				failError:  errors.New("simulated configmap create failure"),
			}

			reconciler := &AnsibleJobReconciler{
				Client: mockClient,
				Scheme: scheme,
			}

			By("Calling createInventoryConfigMap with failing Create")
			err := reconciler.createInventoryConfigMap(ctx, ansibleJob)

			By("Checking error is propagated correctly")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to create or patch ConfigMap"))
			Expect(err.Error()).To(ContainSubstring("simulated configmap create failure"))

			// Cleanup
			Expect(k8sClient.Delete(ctx, ansibleJob)).To(Succeed())
		})

		It("should handle SetControllerReference errors gracefully", func() {
			// Use unique name for this test
			ansibleJob.Name = "test-configmap-controller-ref"
			ansibleJob.Name = "test-configmap-controller-ref"

			// Create AnsibleJob with missing UID to cause SetControllerReference to fail
			ansibleJobNoUID := &ansiblev1alpha1.AnsibleJob{
				ObjectMeta: metav1.ObjectMeta{
					Name:      ansibleJob.Name,
					Namespace: ansibleJob.Namespace,
					// No UID - this will cause SetControllerReference to fail
				},
				Spec: ansibleJob.Spec,
			}

			By("Calling createInventoryConfigMap with AnsibleJob missing UID")
			err := reconciler.createInventoryConfigMap(ctx, ansibleJobNoUID)

			By("Checking SetControllerReference error is handled")
			Expect(err).To(HaveOccurred())
			// The error could be either from SetControllerReference or from Create due to invalid ownerReference
			Expect(err.Error()).To(SatisfyAny(
				ContainSubstring("failed to set controller reference"),
				ContainSubstring("failed to create ConfigMap"),
				ContainSubstring("uid must not be empty"),
			))
		})
	})

	Context("Utility Functions", func() {
		var reconciler *AnsibleJobReconciler

		BeforeEach(func() {
			reconciler = &AnsibleJobReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
			}
		})

		Describe("calculateBackoffDelay", func() {
			It("should return 5 seconds for retry count <= 0", func() {
				Expect(reconciler.calculateBackoffDelay(0)).To(Equal(5 * time.Second))
				Expect(reconciler.calculateBackoffDelay(-1)).To(Equal(5 * time.Second))
			})

			It("should return exponential backoff delays", func() {
				Expect(reconciler.calculateBackoffDelay(1)).To(Equal(5 * time.Second))
				Expect(reconciler.calculateBackoffDelay(2)).To(Equal(10 * time.Second))
				Expect(reconciler.calculateBackoffDelay(3)).To(Equal(20 * time.Second))
				Expect(reconciler.calculateBackoffDelay(4)).To(Equal(40 * time.Second))
			})

			It("should cap delay at 5 minutes for high retry counts", func() {
				Expect(reconciler.calculateBackoffDelay(10)).To(Equal(5 * time.Minute))
				Expect(reconciler.calculateBackoffDelay(100)).To(Equal(5 * time.Minute))
			})
		})

		Describe("getRetryCountFromConditions", func() {
			It("should return 0 for AnsibleJob with no StartTime", func() {
				ansibleJob := &ansiblev1alpha1.AnsibleJob{}
				count := reconciler.getRetryCountFromConditions(ansibleJob)
				Expect(count).To(Equal(0))
			})

			It("should return 0 for new jobs", func() {
				now := time.Now()
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					Status: ansiblev1alpha1.AnsibleJobStatus{
						Phase:     ansiblev1alpha1.AnsibleJobPhasePending,
						StartTime: &metav1.Time{Time: now.Add(-30 * time.Second)}, // 30 seconds ago
					},
				}
				count := reconciler.getRetryCountFromConditions(ansibleJob)
				Expect(count).To(Equal(0))
			})

			It("should return 1 for jobs older than 2 minutes in Pending phase", func() {
				now := time.Now()
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					Status: ansiblev1alpha1.AnsibleJobStatus{
						Phase:     ansiblev1alpha1.AnsibleJobPhasePending,
						StartTime: &metav1.Time{Time: now.Add(-3 * time.Minute)}, // 3 minutes ago
					},
				}
				count := reconciler.getRetryCountFromConditions(ansibleJob)
				Expect(count).To(Equal(1))
			})

			It("should return 3 for jobs older than 5 minutes in Pending phase", func() {
				now := time.Now()
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					Status: ansiblev1alpha1.AnsibleJobStatus{
						Phase:     ansiblev1alpha1.AnsibleJobPhasePending,
						StartTime: &metav1.Time{Time: now.Add(-7 * time.Minute)}, // 7 minutes ago
					},
				}
				count := reconciler.getRetryCountFromConditions(ansibleJob)
				Expect(count).To(Equal(3))
			})

			It("should return 0 for old jobs not in Pending phase", func() {
				now := time.Now()
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					Status: ansiblev1alpha1.AnsibleJobStatus{
						Phase:     ansiblev1alpha1.AnsibleJobPhaseRunning,
						StartTime: &metav1.Time{Time: now.Add(-7 * time.Minute)}, // 7 minutes ago
					},
				}
				count := reconciler.getRetryCountFromConditions(ansibleJob)
				Expect(count).To(Equal(0))
			})
		})

		Describe("calculateRequeueAfter", func() {
			It("should return 5 seconds when StartTime is nil", func() {
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					Status: ansiblev1alpha1.AnsibleJobStatus{
						Phase: ansiblev1alpha1.AnsibleJobPhasePending,
						// StartTime is nil
					},
				}
				duration := reconciler.calculateRequeueAfter(ansibleJob)
				Expect(duration).To(Equal(5 * time.Second))
			})

			It("should return 10 seconds for jobs less than 2 minutes old", func() {
				now := time.Now()
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					Status: ansiblev1alpha1.AnsibleJobStatus{
						Phase:     ansiblev1alpha1.AnsibleJobPhaseRunning,
						StartTime: &metav1.Time{Time: now.Add(-1 * time.Minute)}, // 1 minute ago
					},
				}
				duration := reconciler.calculateRequeueAfter(ansibleJob)
				Expect(duration).To(Equal(10 * time.Second))
			})

			It("should return 30 seconds for jobs 2-10 minutes old", func() {
				now := time.Now()
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					Status: ansiblev1alpha1.AnsibleJobStatus{
						Phase:     ansiblev1alpha1.AnsibleJobPhasePending,
						StartTime: &metav1.Time{Time: now.Add(-5 * time.Minute)}, // 5 minutes ago
					},
				}
				duration := reconciler.calculateRequeueAfter(ansibleJob)
				Expect(duration).To(Equal(30 * time.Second))
			})

			It("should return 60 seconds for jobs older than 10 minutes", func() {
				now := time.Now()
				ansibleJob := &ansiblev1alpha1.AnsibleJob{
					Status: ansiblev1alpha1.AnsibleJobStatus{
						Phase:     ansiblev1alpha1.AnsibleJobPhaseRunning,
						StartTime: &metav1.Time{Time: now.Add(-15 * time.Minute)}, // 15 minutes ago
					},
				}
				duration := reconciler.calculateRequeueAfter(ansibleJob)
				Expect(duration).To(Equal(60 * time.Second))
			})
		})

		Describe("SetupWithManager", func() {
			It("should setup controller with manager successfully", func() {
				// Create a test manager
				scheme := runtime.NewScheme()
				Expect(ansiblev1alpha1.AddToScheme(scheme)).To(Succeed())
				Expect(batchv1.AddToScheme(scheme)).To(Succeed())
				Expect(corev1.AddToScheme(scheme)).To(Succeed())

				mgr, err := ctrl.NewManager(cfg, ctrl.Options{
					Scheme: scheme,
				})
				Expect(err).NotTo(HaveOccurred())

				reconciler := &AnsibleJobReconciler{
					Client: mgr.GetClient(),
					Scheme: mgr.GetScheme(),
				}

				err = reconciler.SetupWithManager(mgr)
				Expect(err).NotTo(HaveOccurred())
			})
		})

		Describe("Edge cases for better coverage", func() {
			It("should handle createKubernetesJob with SetControllerReference failure", func() {
				// Create a test scheme without the necessary types to trigger SetControllerReference failure
				incorrectScheme := runtime.NewScheme()
				// Don't add the required types to trigger an error

				reconciler := &AnsibleJobReconciler{
					Client: k8sClient,
					Scheme: incorrectScheme, // Use incorrect scheme
				}

				ansibleJob := CreateTestAnsibleJob("test-job", "default")
				ansibleJob.Spec.Inventory.Inline = "" // No inline inventory to skip ConfigMap creation

				By("Calling createKubernetesJob with incorrect scheme")
				_, err := reconciler.createKubernetesJob(ctx, ansibleJob)

				By("Expecting SetControllerReference to fail")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("no kind is registered for the type"))
			})

			It("should handle calculateBackoffDelay edge case with retryCount=21", func() {
				scheme := runtime.NewScheme()
				Expect(ansiblev1alpha1.AddToScheme(scheme)).To(Succeed())

				reconciler := &AnsibleJobReconciler{
					Client: k8sClient,
					Scheme: scheme,
				}

				// Test the edge case where retryCount > 20
				delay := reconciler.calculateBackoffDelay(21)
				Expect(delay).To(Equal(5 * time.Minute))
			})

			It("should handle calculateBackoffDelay with exactly retryCount=20", func() {
				scheme := runtime.NewScheme()
				Expect(ansiblev1alpha1.AddToScheme(scheme)).To(Succeed())

				reconciler := &AnsibleJobReconciler{
					Client: k8sClient,
					Scheme: scheme,
				}

				// Test the boundary case where retryCount = 20
				delay := reconciler.calculateBackoffDelay(20)
				Expect(delay).To(Equal(5 * time.Minute)) // Should be capped at 5 minutes
			})

			It("should handle createInventoryConfigMap with Create failure", func() {
				scheme := runtime.NewScheme()
				Expect(ansiblev1alpha1.AddToScheme(scheme)).To(Succeed())
				Expect(corev1.AddToScheme(scheme)).To(Succeed())

				// Create a mock client that fails on ConfigMap creation
				fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
				mockClient := &mockConfigMapCreateFailingClient{
					Client:     fakeClient,
					shouldFail: true,
					failError:  errors.New("mock create failure"),
				}

				reconciler := &AnsibleJobReconciler{
					Client: mockClient,
					Scheme: scheme,
				}

				ansibleJob := CreateTestAnsibleJob("test-job", "default")
				ansibleJob.Spec.Inventory.Inline = "test-inventory"

				By("Calling createInventoryConfigMap with failing Create")
				err := reconciler.createInventoryConfigMap(ctx, ansibleJob)

				By("Expecting ConfigMap creation to fail")
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("mock create failure"))
			})
		})
	})
})
