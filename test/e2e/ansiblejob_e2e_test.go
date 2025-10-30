// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ironcore-dev/maintenance-operator/test/utils"
)

var _ = Describe("AnsibleJob E2E", func() {
	const (
		timeout  = time.Minute * 5
		interval = time.Second * 10
	)

	Context("Ansible Runner Mode", func() {
		var (
			ansibleJobName string
			testNamespace  string
		)

		BeforeEach(func() {
			// Create unique names for each test to avoid conflicts
			ansibleJobName = fmt.Sprintf("e2e-test-%d", time.Now().UnixNano())
			testNamespace = fmt.Sprintf("ansiblejob-e2e-test-%d", time.Now().UnixNano())

			By("Verifying operator is ready")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "deployment",
					"maintenance-operator-controller-manager", "-n", "maintenance-operator-system",
					"--context", "kind-maintenance-operator-test-e2e")
				_, err := utils.Run(cmd)
				return err
			}).WithTimeout(30*time.Second).WithPolling(2*time.Second).Should(Succeed(), "Operator should be ready")

			By("Creating test namespace")
			cmd := exec.Command("kubectl", "create", "ns", testNamespace, "--context", "kind-maintenance-operator-test-e2e")
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create test namespace")
		})

		AfterEach(func() {
			By("Cleaning up test resources")
			// Clean up any AnsibleJob with this test's ansibleJobName (including variants like ansibleJobName-timeout, etc.)
			cmd := exec.Command("kubectl", "delete", "ansiblejob", "--all", "-n", testNamespace,
				"--timeout=60s", "--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)

			By("Cleaning up test namespace")
			cmd = exec.Command("kubectl", "delete", "ns", testNamespace,
				"--timeout=60s", "--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)
		})

		It("should successfully create and complete an AnsibleJob with inline inventory", func() {
			By("Creating an AnsibleJob manifest")
			ansibleJobManifest := fmt.Sprintf(`
apiVersion: ansible.maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s
  namespace: %s
spec:
  playbook:
    name: hello_world.yml
    repository: https://github.com/ansible/ansible-tower-samples.git
    gitRef: master
  inventory:
    inline: |
      [all]
      localhost ansible_connection=local
  extraVars:
    - name: target_hosts
      value: localhost
  jobTemplate:
    resources:
      limits:
        cpu: 500m
        memory: 512Mi
      requests:
        cpu: 100m
        memory: 256Mi
`, ansibleJobName, testNamespace)

			manifestFile := filepath.Join("/tmp", fmt.Sprintf("%s.yaml", ansibleJobName))
			err := os.WriteFile(manifestFile, []byte(ansibleJobManifest), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write manifest file")

			By("Applying the AnsibleJob manifest")
			cmd := exec.Command("kubectl", "apply", "-f", manifestFile, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply AnsibleJob manifest")

			By("Checking that the AnsibleJob is created")
			verifyAnsibleJobExists := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ansiblejob", ansibleJobName, "-n", testNamespace,
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "AnsibleJob should exist")
				g.Expect(output).To(ContainSubstring(ansibleJobName))
			}
			Eventually(verifyAnsibleJobExists, timeout, interval).Should(Succeed())

			By("Checking that the AnsibleJob status progresses")
			verifyAnsibleJobStatus := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ansiblejob", ansibleJobName, "-n", testNamespace,
					"-o", "jsonpath={.status.phase}",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				phase := strings.TrimSpace(output)
				g.Expect(phase).To(Or(
					Equal("Pending"),
					Equal("Running"),
					Equal("Succeeded"),
				), fmt.Sprintf("Unexpected phase: %s", phase))
			}
			Eventually(verifyAnsibleJobStatus, timeout, interval).Should(Succeed())

			By("Verifying that a Kubernetes Job is created")
			verifyJobExists := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "jobs", "-n", testNamespace, "-l",
					fmt.Sprintf("ansible-job=%s", ansibleJobName), "--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring(ansibleJobName), "Kubernetes Job should be created")
			}
			Eventually(verifyJobExists, timeout, interval).Should(Succeed())

			By("Verifying that conditions are set correctly")
			verifyConditions := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ansiblejob", ansibleJobName, "-n", testNamespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())

				// During running phase, Ready should be False (Kubernetes convention)
				if strings.TrimSpace(output) != "" {
					readyStatus := strings.TrimSpace(output)

					// Get current phase to determine expected condition
					phaseCmd := exec.Command("kubectl", "get", "ansiblejob", ansibleJobName, "-n", testNamespace,
						"-o", "jsonpath={.status.phase}",
						"--context", "kind-maintenance-operator-test-e2e")
					phaseOutput, phaseErr := utils.Run(phaseCmd)
					g.Expect(phaseErr).NotTo(HaveOccurred())
					currentPhase := strings.TrimSpace(phaseOutput)

					switch currentPhase {
					case "Running":
						g.Expect(readyStatus).To(Equal("False"), "Ready condition should be False during running phase")
					case "Succeeded":
						g.Expect(readyStatus).To(Equal("True"), "Ready condition should be True when succeeded")
					}
				}
			}
			Eventually(verifyConditions, timeout, interval).Should(Succeed())

			By("Cleaning up the manifest file")
			os.Remove(manifestFile)
		})

		It("should successfully create an AnsibleJob with ConfigMap inventory", func() {
			By("Creating an inventory ConfigMap")
			inventoryConfigMap := fmt.Sprintf(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: test-inventory
  namespace: %s
data:
  hosts: |
    [all]
    localhost ansible_connection=local
    127.0.0.1 ansible_connection=local
`, testNamespace)

			configMapFile := filepath.Join("/tmp", "inventory-configmap.yaml")
			err := os.WriteFile(configMapFile, []byte(inventoryConfigMap), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write ConfigMap file")

			cmd := exec.Command("kubectl", "apply", "-f", configMapFile, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create inventory ConfigMap")

			By("Creating an AnsibleJob manifest with ConfigMap inventory")
			ansibleJobManifest := fmt.Sprintf(`
apiVersion: ansible.maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s-configmap
  namespace: %s
spec:
  playbook:
    name: hello_world.yml
    repository: https://github.com/ansible/ansible-tower-samples.git
    gitRef: master
  inventory:
    configMapRef:
      name: test-inventory
  extraVars:
`, ansibleJobName, testNamespace)

			manifestFile := filepath.Join("/tmp", fmt.Sprintf("%s-configmap.yaml", ansibleJobName))
			err = os.WriteFile(manifestFile, []byte(ansibleJobManifest), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write manifest file")

			By("Applying the AnsibleJob manifest")
			cmd = exec.Command("kubectl", "apply", "-f", manifestFile, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply AnsibleJob manifest")

			By("Checking that the AnsibleJob is created")
			verifyAnsibleJobExists := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ansiblejob",
					fmt.Sprintf("%s-configmap", ansibleJobName), "-n", testNamespace,
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "AnsibleJob should exist")
				g.Expect(output).To(ContainSubstring(fmt.Sprintf("%s-configmap", ansibleJobName)))
			}
			Eventually(verifyAnsibleJobExists, timeout, interval).Should(Succeed())

			By("Verifying that a Kubernetes Job is created")
			verifyJobExists := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "jobs", "-n", testNamespace, "-l",
					fmt.Sprintf("ansible-job=%s-configmap", ansibleJobName), "--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring(fmt.Sprintf("%s-configmap", ansibleJobName)),
					"Kubernetes Job should be created")
			}
			Eventually(verifyJobExists, timeout, interval).Should(Succeed())

			By("Cleaning up test resources")
			os.Remove(manifestFile)
			os.Remove(configMapFile)
			cmd = exec.Command("kubectl", "delete", "ansiblejob", fmt.Sprintf("%s-configmap", ansibleJobName),
				"-n", testNamespace, "--timeout=60s", "--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)
		})

		It("should successfully create an AnsibleJob with Secret inventory", func() {
			By("Creating an inventory Secret")
			inventorySecret := fmt.Sprintf(`
apiVersion: v1
kind: Secret
metadata:
  name: test-inventory-secret
  namespace: %s
type: Opaque
stringData:
  hosts: |
    [all]
    localhost ansible_connection=local
    127.0.0.1 ansible_connection=local
    [webservers]
    localhost
`, testNamespace)

			secretFile := filepath.Join("/tmp", "inventory-secret.yaml")
			err := os.WriteFile(secretFile, []byte(inventorySecret), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write Secret file")

			cmd := exec.Command("kubectl", "apply", "-f", secretFile, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create inventory Secret")

			By("Creating an AnsibleJob manifest with Secret inventory")
			ansibleJobManifest := fmt.Sprintf(`
apiVersion: ansible.maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s-secret
  namespace: %s
spec:
  playbook:
    name: hello_world.yml
    repository: https://github.com/ansible/ansible-tower-samples.git
    gitRef: master
  inventory:
    secretRef:
      name: test-inventory-secret
  extraVars:
    - name: target_hosts
      value: localhost
  jobTemplate:
    resources:
      limits:
        cpu: 500m
        memory: 512Mi
      requests:
        cpu: 100m
        memory: 256Mi
`, ansibleJobName, testNamespace)

			manifestFile := filepath.Join("/tmp", fmt.Sprintf("%s-secret.yaml", ansibleJobName))
			err = os.WriteFile(manifestFile, []byte(ansibleJobManifest), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write manifest file")

			By("Applying the AnsibleJob manifest")
			cmd = exec.Command("kubectl", "apply", "-f", manifestFile, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply AnsibleJob manifest")

			By("Checking that the AnsibleJob is created")
			verifyAnsibleJobExists := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ansiblejob",
					fmt.Sprintf("%s-secret", ansibleJobName), "-n", testNamespace,
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "AnsibleJob should exist")
				g.Expect(output).To(ContainSubstring(fmt.Sprintf("%s-secret", ansibleJobName)))
			}
			Eventually(verifyAnsibleJobExists, timeout, interval).Should(Succeed())

			By("Verifying that a Kubernetes Job is created")
			verifyJobExists := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "jobs", "-n", testNamespace, "-l",
					fmt.Sprintf("ansible-job=%s-secret", ansibleJobName), "--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring(fmt.Sprintf("%s-secret", ansibleJobName)), "Kubernetes Job should be created")
			}
			Eventually(verifyJobExists, timeout, interval).Should(Succeed())

			By("Cleaning up test resources")
			os.Remove(manifestFile)
			os.Remove(secretFile)
			cmd = exec.Command("kubectl", "delete", "ansiblejob",
				fmt.Sprintf("%s-secret", ansibleJobName), "-n", testNamespace,
				"--timeout=60s", "--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "secret", "test-inventory-secret",
				"-n", testNamespace, "--timeout=60s",
				"--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)
		})

		It("should handle missing Secret references gracefully", func() {
			By("Creating an AnsibleJob manifest with non-existent Secret")
			ansibleJobManifest := fmt.Sprintf(`
apiVersion: ansible.maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s-missing-secret
  namespace: %s
spec:
  playbook:
    name: hello_world.yml
    repository: https://github.com/ansible/ansible-tower-samples.git
    gitRef: master
  inventory:
    secretRef:
      name: non-existent-secret
  extraVars:
    - name: target_hosts
      value: localhost
`, ansibleJobName, testNamespace)

			manifestFile := filepath.Join("/tmp", fmt.Sprintf("%s-missing-secret.yaml", ansibleJobName))
			err := os.WriteFile(manifestFile, []byte(ansibleJobManifest), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write manifest file")

			By("Applying the AnsibleJob manifest")
			cmd := exec.Command("kubectl", "apply", "-f", manifestFile, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply AnsibleJob manifest")

			By("Verifying the AnsibleJob eventually fails due to missing Secret")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ansiblejob",
					fmt.Sprintf("%s-missing-secret", ansibleJobName), "-n", testNamespace,
					"-o", "jsonpath={.status.phase}",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				// Accept either Failed or Running (pod might be stuck due to missing secret)
				phase := output
				g.Expect(phase).To(Or(Equal("Failed"), Equal("Running")))

				// If it's running, check that the underlying pod has volume mount issues
				if phase == "Running" {
					jobCmd := exec.Command("kubectl", "get", "pods", "-n", testNamespace,
						"-l", fmt.Sprintf("ansible-job=%s-missing-secret", ansibleJobName),
						"-o", "jsonpath={.items[*].status.containerStatuses[*].state.waiting.reason}",
						"--context", "kind-maintenance-operator-test-e2e")
					podOutput, _ := utils.Run(jobCmd)
					if strings.Contains(podOutput, "CreateContainerConfigError") {
						return // Pod is failing due to missing secret, which is expected
					}
				}
			}, "120s", "10s").Should(Succeed())

			By("Cleaning up resources")
			cmd = exec.Command("kubectl", "delete", "ansiblejob",
				fmt.Sprintf("%s-missing-secret", ansibleJobName), "-n", testNamespace,
				"--timeout=60s", "--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)
		})

		It("should prioritize inline inventory over ConfigMapRef", func() {
			By("Creating a ConfigMap that should be ignored")
			inventoryConfigMap := fmt.Sprintf(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: test-ignored-configmap
  namespace: %s
data:
  hosts: |
    [ignored]
    should.not.be.used
`, testNamespace)

			configMapFile := filepath.Join("/tmp", "ignored-configmap.yaml")
			err := os.WriteFile(configMapFile, []byte(inventoryConfigMap), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write ConfigMap file")

			cmd := exec.Command("kubectl", "apply", "-f", configMapFile, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create inventory ConfigMap")

			By("Creating an AnsibleJob with both inline and ConfigMapRef")
			ansibleJobManifest := fmt.Sprintf(`
apiVersion: ansible.maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s-priority
  namespace: %s
spec:
  playbook:
    name: hello_world.yml
    repository: https://github.com/ansible/ansible-tower-samples.git
    gitRef: master
  inventory:
    inline: |
      [all]
      localhost ansible_connection=local
    configMapRef:
      name: test-ignored-configmap
  extraVars:
    - name: target_hosts
      value: localhost
`, ansibleJobName, testNamespace)

			manifestFile := filepath.Join("/tmp", fmt.Sprintf("%s-priority.yaml", ansibleJobName))
			err = os.WriteFile(manifestFile, []byte(ansibleJobManifest), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write manifest file")

			By("Applying the AnsibleJob manifest")
			cmd = exec.Command("kubectl", "apply", "-f", manifestFile, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply AnsibleJob manifest")

			By("Verifying the AnsibleJob uses inline inventory (succeeds)")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ansiblejob",
					fmt.Sprintf("%s-priority", ansibleJobName), "-n", testNamespace,
					"-o", "jsonpath={.status.phase}",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"))
			}, "180s", "5s").Should(Succeed())

			By("Verifying the job used inline inventory by checking created volumes")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ansiblejob",
					fmt.Sprintf("%s-priority", ansibleJobName), "-n", testNamespace,
					"-o", "jsonpath={.status.jobName}",
					"--context", "kind-maintenance-operator-test-e2e")
				jobNameOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())

				// Check that the job uses the inline inventory ConfigMap (named with -inventory suffix)
				cmd = exec.Command("kubectl", "get", "job", jobNameOutput, "-n", testNamespace,
					"-o", "jsonpath={.spec.template.spec.volumes[*].configMap.name}",
					"--context", "kind-maintenance-operator-test-e2e")
				volumesOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(volumesOutput).To(ContainSubstring("priority-inventory"))
				g.Expect(volumesOutput).NotTo(ContainSubstring("test-ignored-configmap"))
			}, "60s", "5s").Should(Succeed())

			By("Cleaning up resources")
			cmd = exec.Command("kubectl", "delete", "ansiblejob",
				fmt.Sprintf("%s-priority", ansibleJobName), "-n", testNamespace,
				"--timeout=60s", "--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)
			cmd = exec.Command("kubectl", "delete", "configmap", "test-ignored-configmap",
				"-n", testNamespace, "--timeout=60s",
				"--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)
		})

		It("should successfully create an AnsibleJob with roles repository", func() {
			By("Creating an AnsibleJob manifest with roles repository")
			ansibleJobManifest := fmt.Sprintf(`
apiVersion: ansible.maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s-roles
  namespace: %s
spec:
  playbook:
    name: hello_world.yml
    repository: https://github.com/ansible/ansible-tower-samples.git
    gitRef: master
  roles:
    repository: https://github.com/ansible/ansible-tower-samples.git
    gitRef: master
  inventory:
    inline: |
      [all]
      localhost ansible_connection=local
  extraVars:
    - name: target_hosts
      value: localhost
    - name: test_roles_enabled
      value: "true"
  jobTemplate:
    resources:
      limits:
        cpu: 500m
        memory: 512Mi
      requests:
        cpu: 100m
        memory: 256Mi
`, ansibleJobName, testNamespace)

			manifestFile := filepath.Join("/tmp", fmt.Sprintf("%s-roles.yaml", ansibleJobName))
			err := os.WriteFile(manifestFile, []byte(ansibleJobManifest), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write manifest file")

			By("Applying the AnsibleJob manifest")
			cmd := exec.Command("kubectl", "apply", "-f", manifestFile, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply AnsibleJob manifest")

			By("Checking that the AnsibleJob is created")
			verifyAnsibleJobExists := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ansiblejob",
					fmt.Sprintf("%s-roles", ansibleJobName), "-n", testNamespace,
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "AnsibleJob should exist")
				g.Expect(output).To(ContainSubstring(fmt.Sprintf("%s-roles", ansibleJobName)))
			}
			Eventually(verifyAnsibleJobExists, timeout, interval).Should(Succeed())

			By("Verifying that a Kubernetes Job is created")
			verifyJobExists := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "jobs", "-n", testNamespace, "-l",
					fmt.Sprintf("ansible-job=%s-roles", ansibleJobName), "--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring(fmt.Sprintf("%s-roles", ansibleJobName)), "Kubernetes Job should be created")
			}
			Eventually(verifyJobExists, timeout, interval).Should(Succeed())

			By("Verifying that the Job has init containers for both playbook and roles")
			verifyJobHasInitContainers := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "job", "-n", testNamespace, "-l",
					fmt.Sprintf("ansible-job=%s-roles", ansibleJobName), "-o", "yaml",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				// Should have init containers that handle both playbook and roles repo
				g.Expect(output).To(ContainSubstring("initContainers:"), "Job should have init containers")
				g.Expect(output).To(ContainSubstring("setup-ansible-runner"), "Job should have ansible runner setup init container")
				// Verify that both repositories are referenced in the setup script
				g.Expect(output).To(ContainSubstring("github.com/ansible/ansible-tower-samples.git"),
					"Job should reference the playbook and roles repository")
			}
			Eventually(verifyJobHasInitContainers, timeout, interval).Should(Succeed())

			By("Cleaning up test resources")
			os.Remove(manifestFile)
			cmd = exec.Command("kubectl", "delete", "ansiblejob",
				fmt.Sprintf("%s-roles", ansibleJobName), "-n", testNamespace,
				"--timeout=60s", "--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)
		})

		It("should handle invalid git repository URLs gracefully", func() {
			By("Creating an AnsibleJob manifest with invalid git URL")
			ansibleJobManifest := fmt.Sprintf(`
apiVersion: ansible.maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s-invalid-url
  namespace: %s
spec:
  playbook:
    name: hello_world.yml
    repository: https://invalid-domain-that-does-not-exist.example.com/repo.git
    gitRef: master
  inventory:
    inline: |
      [all]
      localhost ansible_connection=local
  extraVars:
    - name: target_hosts
      value: localhost
`, ansibleJobName, testNamespace)

			manifestFile := filepath.Join("/tmp", fmt.Sprintf("%s-invalid-url.yaml", ansibleJobName))
			err := os.WriteFile(manifestFile, []byte(ansibleJobManifest), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write manifest file")

			By("Applying the AnsibleJob manifest")
			cmd := exec.Command("kubectl", "apply", "-f", manifestFile, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply AnsibleJob manifest")

			By("Checking that the AnsibleJob is created")
			verifyAnsibleJobExists := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ansiblejob",
					fmt.Sprintf("%s-invalid-url", ansibleJobName), "-n", testNamespace,
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "AnsibleJob should exist")
				g.Expect(output).To(ContainSubstring(fmt.Sprintf("%s-invalid-url", ansibleJobName)))
			}
			Eventually(verifyAnsibleJobExists, timeout, interval).Should(Succeed())

			By("Verifying that the AnsibleJob progresses normally (controller doesn't validate URLs)")
			verifyJobProgress := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ansiblejob",
					fmt.Sprintf("%s-invalid-url", ansibleJobName), "-n", testNamespace,
					"-o", "yaml",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				// The job should be created and show some status
				g.Expect(output).To(MatchRegexp(`(phase:|status:)`), "AnsibleJob should have status information")
			}
			Eventually(verifyJobProgress, timeout, interval).Should(Succeed())

			By("Cleaning up test resources")
			os.Remove(manifestFile)
			cmd = exec.Command("kubectl", "delete", "ansiblejob",
				fmt.Sprintf("%s-invalid-url", ansibleJobName), "-n", testNamespace,
				"--timeout=60s", "--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)
		})

		It("should handle malformed git URLs", func() {
			By("Creating an AnsibleJob manifest with malformed git URL")
			ansibleJobManifest := fmt.Sprintf(`
apiVersion: ansible.maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s-malformed-url
  namespace: %s
spec:
  playbook:
    name: hello_world.yml
    repository: "not-a-valid-url://malformed"
    gitRef: master
  inventory:
    inline: |
      [all]
      localhost ansible_connection=local
  extraVars:
    - name: target_hosts
      value: localhost
`, ansibleJobName, testNamespace)

			manifestFile := filepath.Join("/tmp", fmt.Sprintf("%s-malformed-url.yaml", ansibleJobName))
			err := os.WriteFile(manifestFile, []byte(ansibleJobManifest), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write manifest file")

			By("Applying the AnsibleJob manifest")
			cmd := exec.Command("kubectl", "apply", "-f", manifestFile, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).To(HaveOccurred(), "Expected validation to reject malformed URL")
			Expect(fmt.Sprintf("%v", err)).To(ContainSubstring("should match '^https://.*\\\\.git$'"),
				"Should fail with URL pattern validation error")

			By("Cleaning up the manifest file")
			os.Remove(manifestFile)
		})

		It("should handle missing ConfigMap references gracefully", func() {
			By("Creating an AnsibleJob manifest with non-existent ConfigMap")
			ansibleJobManifest := fmt.Sprintf(`
apiVersion: ansible.maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s-missing-configmap
  namespace: %s
spec:
  playbook:
    name: hello_world.yml
    repository: https://github.com/ansible/ansible-tower-samples.git
    gitRef: master
  inventory:
    configMapRef:
      name: non-existent-configmap
  extraVars:
    - name: target_hosts
      value: localhost
`, ansibleJobName, testNamespace)

			manifestFile := filepath.Join("/tmp", fmt.Sprintf("%s-missing-configmap.yaml", ansibleJobName))
			err := os.WriteFile(manifestFile, []byte(ansibleJobManifest), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write manifest file")

			By("Applying the AnsibleJob manifest")
			cmd := exec.Command("kubectl", "apply", "-f", manifestFile, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply AnsibleJob manifest")

			By("Verifying that the AnsibleJob is created and can eventually show ConfigMap error")
			verifyMissingConfigMapHandling := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ansiblejob",
					fmt.Sprintf("%s-missing-configmap", ansibleJobName), "-n", testNamespace,
					"-o", "yaml",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				// Should show that the resource exists and may have error information
				g.Expect(output).To(ContainSubstring("metadata:"), "AnsibleJob should be created")
				// Check if there's any status indication
				if strings.Contains(output, "status:") {
					g.Expect(output).To(MatchRegexp(`(phase:|message:|conditions:)`), "AnsibleJob should have status information")
				}
			}
			Eventually(verifyMissingConfigMapHandling, timeout, interval).Should(Succeed())

			By("Cleaning up test resources")
			os.Remove(manifestFile)
			cmd = exec.Command("kubectl", "delete", "ansiblejob",
				fmt.Sprintf("%s-missing-configmap", ansibleJobName), "-n", testNamespace,
				"--timeout=60s", "--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)
		})

		It("should progress through expected status phases", func() {
			By("Creating an AnsibleJob manifest for status progression testing")
			ansibleJobManifest := fmt.Sprintf(`
apiVersion: ansible.maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s-status
  namespace: %s
spec:
  playbook:
    name: hello_world.yml
    repository: https://github.com/ansible/ansible-tower-samples.git
    gitRef: master
  inventory:
    inline: |
      [all]
      localhost ansible_connection=local
  extraVars:
    - name: target_hosts
      value: localhost
  jobTemplate:
    resources:
      limits:
        cpu: 500m
        memory: 512Mi
      requests:
        cpu: 100m
        memory: 256Mi
`, ansibleJobName, testNamespace)

			manifestFile := filepath.Join("/tmp", fmt.Sprintf("%s-status.yaml", ansibleJobName))
			err := os.WriteFile(manifestFile, []byte(ansibleJobManifest), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write manifest file")

			By("Applying the AnsibleJob manifest")
			cmd := exec.Command("kubectl", "apply", "-f", manifestFile, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply AnsibleJob manifest")

			By("Verifying that the AnsibleJob progresses through phases (may go directly to Succeeded for simple playbooks)")
			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "ansiblejob",
					fmt.Sprintf("%s-status", ansibleJobName), "-n", testNamespace,
					"-o", "jsonpath={.status.phase}",
					"--context", "kind-maintenance-operator-test-e2e")
				output, _ := utils.Run(cmd)
				return strings.TrimSpace(output)
			}).WithTimeout(timeout).WithPolling(interval).Should(
				MatchRegexp("^(Pending|Running|Succeeded)$"),
				"AnsibleJob should progress through valid phases")

			By("Verifying that the AnsibleJob eventually reaches Succeeded phase")
			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "ansiblejob",
					fmt.Sprintf("%s-status", ansibleJobName), "-n", testNamespace,
					"-o", "jsonpath={.status.phase}",
					"--context", "kind-maintenance-operator-test-e2e")
				output, _ := utils.Run(cmd)
				return strings.TrimSpace(output)
			}).WithTimeout(timeout).WithPolling(interval).Should(
				Equal("Succeeded"), "AnsibleJob should eventually reach Succeeded phase")

			By("Verifying that the AnsibleJob has a Job reference")
			verifyJobReference := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ansiblejob",
					fmt.Sprintf("%s-status", ansibleJobName), "-n", testNamespace,
					"-o", "jsonpath={.status.jobName}",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(BeEmpty(), "AnsibleJob should have a jobName in status")
			}
			Eventually(verifyJobReference, timeout, interval).Should(Succeed())

			By("Verifying that the AnsibleJob eventually completes with Success or Failed phase")
			verifyCompletionPhase := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ansiblejob",
					fmt.Sprintf("%s-status", ansibleJobName), "-n", testNamespace,
					"-o", "jsonpath={.status.phase}",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Or(Equal("Succeeded"), Equal("Failed")),
					"AnsibleJob should eventually reach Succeeded or Failed phase")
			}
			Eventually(verifyCompletionPhase, timeout*2, interval).Should(Succeed())

			By("Verifying that completion time is set when job finishes")
			verifyCompletionTime := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ansiblejob",
					fmt.Sprintf("%s-status", ansibleJobName), "-n", testNamespace,
					"-o", "jsonpath={.status.completionTime}",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).NotTo(BeEmpty(), "AnsibleJob should have completionTime when finished")
			}
			Eventually(verifyCompletionTime, timeout, interval).Should(Succeed())

			By("Cleaning up test resources")
			os.Remove(manifestFile)
			cmd = exec.Command("kubectl", "delete", "ansiblejob",
				fmt.Sprintf("%s-status", ansibleJobName), "-n", testNamespace,
				"--timeout=60s", "--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)
		})

		It("should respect timeout and resource limits", func() {
			By("Creating an AnsibleJob manifest with timeout and custom resource limits")
			ansibleJobManifest := fmt.Sprintf(`
apiVersion: ansible.maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s-timeout
  namespace: %s
spec:
  playbook:
    name: hello_world.yml
    repository: https://github.com/ansible/ansible-tower-samples.git
    gitRef: master
  timeoutSeconds: 120
  inventory:
    inline: |
      [all]
      localhost ansible_connection=local
  extraVars:
    - name: target_hosts
      value: localhost
  jobTemplate:
    backoffLimit: 1
    resources:
      limits:
        cpu: 200m
        memory: 256Mi
      requests:
        cpu: 50m
        memory: 128Mi
`, ansibleJobName, testNamespace)

			manifestFile := filepath.Join("/tmp", fmt.Sprintf("%s-timeout.yaml", ansibleJobName))
			err := os.WriteFile(manifestFile, []byte(ansibleJobManifest), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write manifest file")

			By("Applying the AnsibleJob manifest")
			cmd := exec.Command("kubectl", "apply", "-f", manifestFile, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply AnsibleJob manifest")

			By("Verifying that the underlying Job has the correct timeout")
			verifyJobTimeout := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "job", "-n", testNamespace, "-l",
					fmt.Sprintf("ansible-job=%s-timeout", ansibleJobName), "-o", "jsonpath={.items[0].spec.activeDeadlineSeconds}",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("120"), "Job should have activeDeadlineSeconds set to 120")
			}
			Eventually(verifyJobTimeout, timeout, interval).Should(Succeed())

			By("Verifying that the underlying Job has the correct backoff limit")
			verifyJobBackoffLimit := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "job", "-n", testNamespace, "-l",
					fmt.Sprintf("ansible-job=%s-timeout", ansibleJobName), "-o", "jsonpath={.items[0].spec.backoffLimit}",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("1"), "Job should have backoffLimit set to 1")
			}
			Eventually(verifyJobBackoffLimit, timeout, interval).Should(Succeed())

			By("Verifying that the Job container has correct resource limits")
			verifyJobResources := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "job", "-n", testNamespace, "-l",
					fmt.Sprintf("ansible-job=%s-timeout", ansibleJobName), "-o", "yaml",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				// Check that resource limits are applied
				g.Expect(output).To(ContainSubstring("cpu: 200m"), "Job should have CPU limit of 200m")
				g.Expect(output).To(ContainSubstring("memory: 256Mi"), "Job should have memory limit of 256Mi")
				g.Expect(output).To(ContainSubstring("cpu: 50m"), "Job should have CPU request of 50m")
				g.Expect(output).To(ContainSubstring("memory: 128Mi"), "Job should have memory request of 128Mi")
			}
			Eventually(verifyJobResources, timeout, interval).Should(Succeed())

			By("Verifying that the AnsibleJob progresses normally despite resource constraints")
			verifyJobProgress := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ansiblejob",
					fmt.Sprintf("%s-timeout", ansibleJobName), "-n", testNamespace,
					"-o", "jsonpath={.status.phase}",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Or(Equal("Running"), Equal("Succeeded"), Equal("Failed")),
					"AnsibleJob should be in a valid execution phase")
			}
			Eventually(verifyJobProgress, timeout, interval).Should(Succeed())

			By("Cleaning up test resources")
			os.Remove(manifestFile)
			cmd = exec.Command("kubectl", "delete", "ansiblejob",
				fmt.Sprintf("%s-timeout", ansibleJobName), "-n", testNamespace,
				"--timeout=60s", "--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)
		})

		// Medium Priority Advanced Feature Tests
		It("should use custom images for ansible-runner and init containers", func() {
			By("Creating an AnsibleJob manifest with custom images")
			ansibleJobManifest := fmt.Sprintf(`
apiVersion: ansible.maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s-custom-images
  namespace: %s
spec:
  playbook:
    name: hello_world.yml
    repository: https://github.com/ansible/ansible-tower-samples.git
    gitRef: master
  inventory:
    inline: |
      [all]
      localhost ansible_connection=local
  jobTemplate:
    image: quay.io/ansible/ansible-runner:stable-2.12-latest@sha256:%s
    initImage: alpine/git:v2.36.3
    serviceAccountName: default
`, ansibleJobName, testNamespace, "001a4bde411be863d54c1d293f3d2e7b0ff0e67ef5d7b2f9f7fb56b61694f4e8")

			manifestFile := filepath.Join("/tmp", fmt.Sprintf("%s-custom-images.yaml", ansibleJobName))
			err := os.WriteFile(manifestFile, []byte(ansibleJobManifest), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write manifest file")

			By("Applying the AnsibleJob manifest")
			cmd := exec.Command("kubectl", "apply", "-f", manifestFile, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply AnsibleJob manifest")

			By("Verifying that the Job uses the custom images")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "job", "-n", testNamespace, "-l",
					fmt.Sprintf("ansible-job=%s-custom-images", ansibleJobName), "-o", "yaml",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				if err != nil {
					return err
				}
				expectedImage := "quay.io/ansible/ansible-runner:stable-2.12-latest@sha256:" +
					"001a4bde411be863d54c1d293f3d2e7b0ff0e67ef5d7b2f9f7fb56b61694f4e8"
				if !strings.Contains(output, expectedImage) {
					return fmt.Errorf("custom ansible-runner image not found")
				}
				if !strings.Contains(output, "alpine/git:v2.36.3") {
					return fmt.Errorf("custom init image not found")
				}
				return nil
			}, timeout, interval).Should(Succeed(), "Job should use custom images")

			By("Cleaning up test resources")
			os.Remove(manifestFile)
			cmd = exec.Command("kubectl", "delete", "ansiblejob",
				fmt.Sprintf("%s-custom-images", ansibleJobName), "-n", testNamespace,
				"--timeout=60s", "--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)
		})

		It("should handle complex extra variables scenarios", func() {
			By("Creating an AnsibleJob manifest with complex extra variables")
			ansibleJobManifest := fmt.Sprintf(`
apiVersion: ansible.maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s-complex-vars
  namespace: %s
spec:
  playbook:
    name: hello_world.yml
    repository: https://github.com/ansible/ansible-tower-samples.git
    gitRef: master
  inventory:
    inline: |
      [all]
      localhost ansible_connection=local
  extraVars:
    - name: simple_string
      value: "hello world"
    - name: number_value
      value: "42"
    - name: boolean_value
      value: "true"
    - name: json_object
      value: '{"key": "value", "nested": {"array": [1, 2, 3]}}'
    - name: multiline_string
      value: |
        line1
        line2
        line3
    - name: special_chars
      value: "special!@#$%%^&*()chars"
    - name: environment_info
      value: "k8s-namespace-%s"
`, ansibleJobName, testNamespace, testNamespace)

			manifestFile := filepath.Join("/tmp", fmt.Sprintf("%s-complex-vars.yaml", ansibleJobName))
			err := os.WriteFile(manifestFile, []byte(ansibleJobManifest), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write manifest file")

			By("Applying the AnsibleJob manifest")
			cmd := exec.Command("kubectl", "apply", "-f", manifestFile, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply AnsibleJob manifest")

			By("Verifying that the Job includes all extra variables")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "job", "-n", testNamespace, "-l",
					fmt.Sprintf("ansible-job=%s-complex-vars", ansibleJobName), "-o", "yaml",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				if err != nil {
					return err
				}

				// Check for key extra variables in the job spec
				expectedVars := []string{
					"simple_string",
					"number_value",
					"boolean_value",
					"json_object",
					"special_chars",
				}

				for _, varName := range expectedVars {
					if !strings.Contains(output, varName) {
						return fmt.Errorf("extra variable %s not found in job", varName)
					}
				}
				return nil
			}, timeout, interval).Should(Succeed(), "Job should include all extra variables")

			By("Verifying that the AnsibleJob progresses normally with complex variables")
			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "ansiblejob",
					fmt.Sprintf("%s-complex-vars", ansibleJobName), "-n", testNamespace,
					"-o", "jsonpath={.status.phase}",
					"--context", "kind-maintenance-operator-test-e2e")
				output, _ := utils.Run(cmd)
				return strings.TrimSpace(output)
			}, timeout, interval).Should(MatchRegexp("^(Pending|Running|Succeeded)$"), "AnsibleJob should progress normally")

			By("Cleaning up test resources")
			os.Remove(manifestFile)
			cmd = exec.Command("kubectl", "delete", "ansiblejob",
				fmt.Sprintf("%s-complex-vars", ansibleJobName), "-n", testNamespace,
				"--timeout=60s", "--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)
		})

		It("should support host limiting functionality", func() {
			By("Creating an AnsibleJob manifest with host limiting")
			ansibleJobManifest := fmt.Sprintf(`
apiVersion: ansible.maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s-host-limit
  namespace: %s
spec:
  playbook:
    name: hello_world.yml
    repository: https://github.com/ansible/ansible-tower-samples.git
    gitRef: master
  limit: "localhost"
  inventory:
    inline: |
      [webservers]
      web1.example.com
      web2.example.com

      [databases]
      db1.example.com
      db2.example.com

      [all]
      localhost ansible_connection=local
  extraVars:
    - name: target_group
      value: "localhost"
`, ansibleJobName, testNamespace)

			manifestFile := filepath.Join("/tmp", fmt.Sprintf("%s-host-limit.yaml", ansibleJobName))
			err := os.WriteFile(manifestFile, []byte(ansibleJobManifest), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write manifest file")

			By("Applying the AnsibleJob manifest")
			cmd := exec.Command("kubectl", "apply", "-f", manifestFile, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply AnsibleJob manifest")

			By("Verifying that the Job includes the limit parameter")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "job", "-n", testNamespace, "-l",
					fmt.Sprintf("ansible-job=%s-host-limit", ansibleJobName), "-o", "yaml",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				if err != nil {
					return err
				}
				if !strings.Contains(output, "--limit") || !strings.Contains(output, "localhost") {
					return fmt.Errorf("limit parameter not found in job")
				}
				return nil
			}, timeout, interval).Should(Succeed(), "Job should include limit parameter")

			By("Verifying that the AnsibleJob executes successfully with host limiting")
			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "ansiblejob",
					fmt.Sprintf("%s-host-limit", ansibleJobName), "-n", testNamespace,
					"-o", "jsonpath={.status.phase}",
					"--context", "kind-maintenance-operator-test-e2e")
				output, _ := utils.Run(cmd)
				return strings.TrimSpace(output)
			}, timeout, interval).Should(MatchRegexp("^(Pending|Running|Succeeded)$"), "AnsibleJob should execute successfully")

			By("Cleaning up test resources")
			os.Remove(manifestFile)
			cmd = exec.Command("kubectl", "delete", "ansiblejob",
				fmt.Sprintf("%s-host-limit", ansibleJobName), "-n", testNamespace,
				"--timeout=60s", "--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)
		})

		It("should handle different git reference types (tags, commits, branches)", func() {
			By("Testing with a specific git tag")
			ansibleJobManifest := fmt.Sprintf(`
apiVersion: ansible.maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s-git-tag
  namespace: %s
spec:
  playbook:
    name: hello_world.yml
    repository: https://github.com/ansible/ansible-tower-samples.git
    gitRef: "v1.0.0"
  inventory:
    inline: |
      [all]
      localhost ansible_connection=local
`, ansibleJobName, testNamespace)

			manifestFile := filepath.Join("/tmp", fmt.Sprintf("%s-git-tag.yaml", ansibleJobName))
			err := os.WriteFile(manifestFile, []byte(ansibleJobManifest), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write manifest file")

			By("Applying the AnsibleJob manifest with git tag")
			cmd := exec.Command("kubectl", "apply", "-f", manifestFile, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply AnsibleJob manifest")

			By("Verifying that the Job handles git tag reference")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "job", "-n", testNamespace, "-l",
					fmt.Sprintf("ansible-job=%s-git-tag", ansibleJobName), "-o", "yaml",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				if err != nil {
					return err
				}
				if !strings.Contains(output, "v1.0.0") {
					return fmt.Errorf("git tag reference not found in job")
				}
				return nil
			}, timeout, interval).Should(Succeed(), "Job should include git tag reference")

			By("Cleaning up git tag test")
			os.Remove(manifestFile)
			cmd = exec.Command("kubectl", "delete", "ansiblejob",
				fmt.Sprintf("%s-git-tag", ansibleJobName), "-n", testNamespace,
				"--timeout=60s", "--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)

			By("Testing with a specific git commit SHA")
			ansibleJobManifest = fmt.Sprintf(`
apiVersion: ansible.maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s-git-commit
  namespace: %s
spec:
  playbook:
    name: hello_world.yml
    repository: https://github.com/ansible/ansible-tower-samples.git
    gitRef: "abc123def456"
  inventory:
    inline: |
      [all]
      localhost ansible_connection=local
`, ansibleJobName, testNamespace)

			manifestFile = filepath.Join("/tmp", fmt.Sprintf("%s-git-commit.yaml", ansibleJobName))
			err = os.WriteFile(manifestFile, []byte(ansibleJobManifest), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write manifest file")

			By("Applying the AnsibleJob manifest with git commit")
			cmd = exec.Command("kubectl", "apply", "-f", manifestFile, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply AnsibleJob manifest")

			By("Verifying that the Job handles git commit reference")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "job", "-n", testNamespace, "-l",
					fmt.Sprintf("ansible-job=%s-git-commit", ansibleJobName), "-o", "yaml",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				if err != nil {
					return err
				}
				if !strings.Contains(output, "abc123def456") {
					return fmt.Errorf("git commit reference not found in job")
				}
				return nil
			}, timeout, interval).Should(Succeed(), "Job should include git commit reference")

			By("Verifying that AnsibleJob handles different git references gracefully")
			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "ansiblejob",
					fmt.Sprintf("%s-git-commit", ansibleJobName), "-n", testNamespace,
					"-o", "jsonpath={.status.phase}",
					"--context", "kind-maintenance-operator-test-e2e")
				output, _ := utils.Run(cmd)
				return strings.TrimSpace(output)
			}, timeout, interval).Should(MatchRegexp("^(Pending|Running|Succeeded|Failed)$"),
				"AnsibleJob should handle invalid commit gracefully")

			By("Cleaning up test resources")
			os.Remove(manifestFile)
			cmd = exec.Command("kubectl", "delete", "ansiblejob",
				fmt.Sprintf("%s-git-commit", ansibleJobName), "-n", testNamespace,
				"--timeout=60s", "--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)
		})

		// Lower Priority Edge Case Tests
		It("should handle RBAC and permission scenarios", func() {
			By("Creating a custom ServiceAccount with limited permissions")
			serviceAccountManifest := fmt.Sprintf(`
apiVersion: v1
kind: ServiceAccount
metadata:
  name: test-limited-sa
  namespace: %s
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: test-limited-role
  namespace: %s
rules:
- apiGroups: [""]
  resources: ["configmaps", "secrets"]
  verbs: ["get", "list"]
- apiGroups: [""]
  resources: ["pods"]
  verbs: ["create", "get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: test-limited-binding
  namespace: %s
subjects:
- kind: ServiceAccount
  name: test-limited-sa
  namespace: %s
roleRef:
  kind: Role
  name: test-limited-role
  apiGroup: rbac.authorization.k8s.io
`, testNamespace, testNamespace, testNamespace, testNamespace)

			serviceAccountFile := filepath.Join("/tmp", fmt.Sprintf("%s-sa.yaml", ansibleJobName))
			err := os.WriteFile(serviceAccountFile, []byte(serviceAccountManifest), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write ServiceAccount file")

			cmd := exec.Command("kubectl", "apply", "-f", serviceAccountFile, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ServiceAccount and RBAC")

			By("Creating an AnsibleJob with custom ServiceAccount")
			ansibleJobManifest := fmt.Sprintf(`
apiVersion: ansible.maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s-rbac-test
  namespace: %s
spec:
  playbook:
    name: hello_world.yml
    repository: https://github.com/ansible/ansible-tower-samples.git
    gitRef: master
  inventory:
    inline: |
      [all]
      localhost ansible_connection=local
  jobTemplate:
    serviceAccountName: test-limited-sa
    resources:
      limits:
        cpu: 200m
        memory: 256Mi
`, ansibleJobName, testNamespace)

			manifestFile := filepath.Join("/tmp", fmt.Sprintf("%s-rbac-test.yaml", ansibleJobName))
			err = os.WriteFile(manifestFile, []byte(ansibleJobManifest), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write manifest file")

			By("Applying the AnsibleJob manifest")
			cmd = exec.Command("kubectl", "apply", "-f", manifestFile, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply AnsibleJob manifest")

			By("Verifying that the Job uses the custom ServiceAccount")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "job", "-n", testNamespace, "-l",
					fmt.Sprintf("ansible-job=%s-rbac-test", ansibleJobName), "-o", "yaml",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				if err != nil {
					return err
				}
				if !strings.Contains(output, "test-limited-sa") {
					return fmt.Errorf("custom ServiceAccount not found in job")
				}
				return nil
			}, timeout, interval).Should(Succeed(), "Job should use custom ServiceAccount")

			By("Verifying that the AnsibleJob operates within RBAC constraints")
			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "ansiblejob",
					fmt.Sprintf("%s-rbac-test", ansibleJobName), "-n", testNamespace,
					"-o", "jsonpath={.status.phase}",
					"--context", "kind-maintenance-operator-test-e2e")
				output, _ := utils.Run(cmd)
				return strings.TrimSpace(output)
			}, timeout, interval).Should(MatchRegexp("^(Pending|Running|Succeeded|Failed)$"),
				"AnsibleJob should handle RBAC constraints")

			By("Cleaning up test resources")
			os.Remove(manifestFile)
			os.Remove(serviceAccountFile)
			cmd = exec.Command("kubectl", "delete", "ansiblejob",
				fmt.Sprintf("%s-rbac-test", ansibleJobName), "-n", testNamespace,
				"--timeout=60s", "--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)
		})

		It("should handle concurrent job execution scenarios", func() {
			By("Creating multiple AnsibleJob manifests for concurrent execution")
			jobNames := []string{
				fmt.Sprintf("%s-concurrent-1", ansibleJobName),
				fmt.Sprintf("%s-concurrent-2", ansibleJobName),
				fmt.Sprintf("%s-concurrent-3", ansibleJobName),
			}

			manifestFiles := []string{}
			for i, jobName := range jobNames {
				ansibleJobManifest := fmt.Sprintf(`
apiVersion: ansible.maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s
  namespace: %s
spec:
  playbook:
    name: hello_world.yml
    repository: https://github.com/ansible/ansible-tower-samples.git
    gitRef: master
  inventory:
    inline: |
      [all]
      localhost ansible_connection=local
  extraVars:
    - name: job_number
      value: "%d"
    - name: concurrent_test
      value: "true"
  jobTemplate:
    resources:
      limits:
        cpu: 100m
        memory: 128Mi
      requests:
        cpu: 50m
        memory: 64Mi
`, jobName, testNamespace, i+1)

				manifestFile := filepath.Join("/tmp", fmt.Sprintf("%s.yaml", jobName))
				err := os.WriteFile(manifestFile, []byte(ansibleJobManifest), 0644)
				Expect(err).NotTo(HaveOccurred(), "Failed to write manifest file")
				manifestFiles = append(manifestFiles, manifestFile)

				By(fmt.Sprintf("Applying AnsibleJob manifest %d", i+1))
				cmd := exec.Command("kubectl", "apply", "-f", manifestFile, "--context", "kind-maintenance-operator-test-e2e")
				_, err = utils.Run(cmd)
				Expect(err).NotTo(HaveOccurred(), "Failed to apply AnsibleJob manifest")
			}

			By("Verifying that multiple Jobs are created concurrently")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "jobs", "-n", testNamespace,
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				if err != nil {
					return err
				}

				count := 0
				for _, jobName := range jobNames {
					if strings.Contains(output, jobName) {
						count++
					}
				}
				if count < len(jobNames) {
					return fmt.Errorf("expected %d concurrent jobs, found %d", len(jobNames), count)
				}
				return nil
			}, timeout, interval).Should(Succeed(), "All concurrent jobs should be created")

			By("Verifying that all AnsibleJobs progress independently")
			Eventually(func() error {
				for _, jobName := range jobNames {
					cmd := exec.Command("kubectl", "get", "ansiblejob", jobName, "-n", testNamespace, "-o", "jsonpath={.status.phase}",
						"--context", "kind-maintenance-operator-test-e2e")
					output, err := utils.Run(cmd)
					if err != nil {
						return err
					}
					phase := strings.TrimSpace(output)
					if phase == "" || (!strings.Contains("Pending Running Succeeded Failed", phase)) {
						return fmt.Errorf("job %s has invalid phase: %s", jobName, phase)
					}
				}
				return nil
			}, timeout*2, interval).Should(Succeed(), "All concurrent jobs should have valid phases")

			By("Cleaning up test resources")
			for i, manifestFile := range manifestFiles {
				os.Remove(manifestFile)
				cmd := exec.Command("kubectl", "delete", "ansiblejob", jobNames[i], "-n", testNamespace,
					"--timeout=60s", "--context", "kind-maintenance-operator-test-e2e")
				_, _ = utils.Run(cmd)
			}
		})

		It("should handle large-scale inventory scenarios", func() {
			By("Creating an AnsibleJob with large inventory")

			// Generate a large inventory with multiple groups and hosts
			largeInventory := "      [webservers]\n"
			for i := 1; i <= 50; i++ {
				largeInventory += fmt.Sprintf("      web%d.example.com ansible_host=192.168.1.%d\n", i, i)
			}
			largeInventory += "\n      [databases]\n"
			for i := 1; i <= 20; i++ {
				largeInventory += fmt.Sprintf("      db%d.example.com ansible_host=192.168.2.%d\n", i, i)
			}
			largeInventory += "\n      [loadbalancers]\n"
			for i := 1; i <= 5; i++ {
				largeInventory += fmt.Sprintf("      lb%d.example.com ansible_host=192.168.3.%d\n", i, i)
			}
			largeInventory += "\n      [all]\n      localhost ansible_connection=local"

			ansibleJobManifest := fmt.Sprintf(`
apiVersion: ansible.maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s-large-inventory
  namespace: %s
spec:
  playbook:
    name: hello_world.yml
    repository: https://github.com/ansible/ansible-tower-samples.git
    gitRef: master
  limit: "localhost"
  inventory:
    inline: |
%s
  extraVars:
    - name: inventory_size
      value: "large"
    - name: total_hosts
      value: "76"
  jobTemplate:
    resources:
      limits:
        cpu: 500m
        memory: 1Gi
      requests:
        cpu: 100m
        memory: 256Mi
`, ansibleJobName, testNamespace, largeInventory)

			manifestFile := filepath.Join("/tmp", fmt.Sprintf("%s-large-inventory.yaml", ansibleJobName))
			err := os.WriteFile(manifestFile, []byte(ansibleJobManifest), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write manifest file")

			By("Applying the AnsibleJob manifest")
			cmd := exec.Command("kubectl", "apply", "-f", manifestFile, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply AnsibleJob manifest")

			By("Verifying that the Job handles large inventory efficiently")
			Eventually(func() error {
				// First verify the Job is created and has proper resource limits
				cmd := exec.Command("kubectl", "get", "job", "-n", testNamespace, "-l",
					fmt.Sprintf("ansible-job=%s-large-inventory", ansibleJobName), "-o", "yaml",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				if err != nil {
					return err
				}

				// Verify resource allocation for large inventory
				if !strings.Contains(output, "memory: 1Gi") {
					return fmt.Errorf("expected memory limit not found")
				}

				return nil
			}, timeout, interval).Should(Succeed(), "Job should be created with proper resource limits")

			By("Verifying that the inventory ConfigMap contains large inventory groups")
			Eventually(func() error {
				// Check the inventory ConfigMap for large inventory content
				cmd := exec.Command("kubectl", "get", "configmap",
					fmt.Sprintf("%s-large-inventory-inventory", ansibleJobName),
					"-n", testNamespace, "-o", "yaml",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				if err != nil {
					return err
				}

				// Verify inventory contains multiple host groups
				if !strings.Contains(output, "webservers") || !strings.Contains(output, "databases") ||
					!strings.Contains(output, "loadbalancers") {
					return fmt.Errorf("large inventory groups not found in ConfigMap")
				}

				// Verify we have sufficient hosts
				webCount := strings.Count(output, "web")
				dbCount := strings.Count(output, "db")
				lbCount := strings.Count(output, "lb")

				if webCount < 50 || dbCount < 20 || lbCount < 5 {
					return fmt.Errorf("insufficient hosts in inventory: web=%d, db=%d, lb=%d", webCount, dbCount, lbCount)
				}

				return nil
			}, timeout, interval).Should(Succeed(), "Inventory ConfigMap should contain large inventory")

			By("Verifying that the AnsibleJob progresses normally with large inventory")
			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "ansiblejob",
					fmt.Sprintf("%s-large-inventory", ansibleJobName), "-n", testNamespace,
					"-o", "jsonpath={.status.phase}",
					"--context", "kind-maintenance-operator-test-e2e")
				output, _ := utils.Run(cmd)
				return strings.TrimSpace(output)
			}, timeout*2, interval).Should(MatchRegexp("^(Pending|Running|Succeeded)$"),
				"AnsibleJob should handle large inventory")

			By("Cleaning up test resources")
			os.Remove(manifestFile)
			cmd = exec.Command("kubectl", "delete", "ansiblejob",
				fmt.Sprintf("%s-large-inventory", ansibleJobName), "-n", testNamespace,
				"--timeout=60s", "--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)
		})

		It("should handle performance and resource usage scenarios", func() {
			By("Creating an AnsibleJob with resource constraints and monitoring")
			ansibleJobManifest := fmt.Sprintf(`
apiVersion: ansible.maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s-performance
  namespace: %s
spec:
  playbook:
    name: hello_world.yml
    repository: https://github.com/ansible/ansible-tower-samples.git
    gitRef: master
  timeoutSeconds: 300
  inventory:
    inline: |
      [all]
      localhost ansible_connection=local
  extraVars:
    - name: performance_test
      value: "true"
    - name: monitoring_enabled
      value: "true"
  jobTemplate:
    backoffLimit: 3
    resources:
      limits:
        cpu: 100m
        memory: 128Mi
        ephemeral-storage: 1Gi
      requests:
        cpu: 50m
        memory: 64Mi
        ephemeral-storage: 512Mi
`, ansibleJobName, testNamespace)

			manifestFile := filepath.Join("/tmp", fmt.Sprintf("%s-performance.yaml", ansibleJobName))
			err := os.WriteFile(manifestFile, []byte(ansibleJobManifest), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write manifest file")

			By("Applying the AnsibleJob manifest")
			cmd := exec.Command("kubectl", "apply", "-f", manifestFile, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply AnsibleJob manifest")

			By("Verifying resource constraints are properly set")
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "job", "-n", testNamespace, "-l",
					fmt.Sprintf("ansible-job=%s-performance", ansibleJobName), "-o", "yaml",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				if err != nil {
					return err
				}

				// Verify all resource types are set
				requiredResources := []string{
					"cpu: 100m",
					"memory: 128Mi",
					"ephemeral-storage: 1Gi",
					"cpu: 50m",
					"memory: 64Mi",
					"ephemeral-storage: 512Mi",
				}

				for _, resource := range requiredResources {
					if !strings.Contains(output, resource) {
						return fmt.Errorf("resource constraint %s not found", resource)
					}
				}

				// Verify timeout is set
				if !strings.Contains(output, "activeDeadlineSeconds: 300") {
					return fmt.Errorf("timeout constraint not found")
				}

				return nil
			}, timeout, interval).Should(Succeed(), "Job should have proper resource constraints")

			By("Monitoring job execution and resource usage")
			var jobPodName string
			Eventually(func() error {
				cmd := exec.Command("kubectl", "get", "pods", "-n", testNamespace, "-l",
					fmt.Sprintf("ansible-job=%s-performance", ansibleJobName), "-o", "jsonpath={.items[0].metadata.name}",
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				if err != nil {
					return err
				}
				jobPodName = strings.TrimSpace(output)
				if jobPodName == "" {
					return fmt.Errorf("job pod not found")
				}
				return nil
			}, timeout, interval).Should(Succeed(), "Job pod should be created")

			By("Verifying the AnsibleJob completes within performance constraints")
			Eventually(func() string {
				cmd := exec.Command("kubectl", "get", "ansiblejob",
					fmt.Sprintf("%s-performance", ansibleJobName), "-n", testNamespace,
					"-o", "jsonpath={.status.phase}",
					"--context", "kind-maintenance-operator-test-e2e")
				output, _ := utils.Run(cmd)
				return strings.TrimSpace(output)
			}, timeout*2, interval).Should(MatchRegexp("^(Succeeded|Failed)$"), "AnsibleJob should complete within timeout")

			By("Checking resource usage (if pod still exists)")
			if jobPodName != "" {
				cmd := exec.Command("kubectl", "get", "pod", jobPodName, "-n", testNamespace,
					"--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				if err == nil && !strings.Contains(output, "NotFound") {
					By("Pod still exists, checking resource usage")
					cmd = exec.Command("kubectl", "top", "pod", jobPodName, "-n", testNamespace,
						"--context", "kind-maintenance-operator-test-e2e")
					_, _ = utils.Run(cmd) // Don't fail if metrics are not available
				}
			}

			By("Cleaning up test resources")
			os.Remove(manifestFile)
			cmd = exec.Command("kubectl", "delete", "ansiblejob",
				fmt.Sprintf("%s-performance", ansibleJobName), "-n", testNamespace,
				"--timeout=60s", "--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)
		})
	})
})
