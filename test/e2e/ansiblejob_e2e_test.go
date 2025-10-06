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
			ansibleJobName = "e2e-test"
			testNamespace  = "ansiblejob-e2e-test"
		)

		BeforeEach(func() {
			By("Installing CRDs")
			cmd := exec.Command("make", "install")
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

			By("Creating test namespace")
			cmd = exec.Command("kubectl", "create", "ns", testNamespace, "--context", "kind-maintenance-operator-test-e2e")
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create test namespace")
		})

		AfterEach(func() {
			By("Cleaning up test resources")
			cmd := exec.Command("kubectl", "delete", "ansiblejob", ansibleJobName, "-n", testNamespace, "--timeout=60s", "--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)

			By("Cleaning up test namespace")
			cmd = exec.Command("kubectl", "delete", "ns", testNamespace, "--timeout=60s", "--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)

			By("Uninstalling CRDs")
			cmd = exec.Command("make", "uninstall")
			_, _ = utils.Run(cmd)
		})

		It("should successfully create and complete an AnsibleJob with inline inventory", func() {
			By("Creating an AnsibleJob manifest")
			ansibleJobManifest := fmt.Sprintf(`
apiVersion: maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s
  namespace: %s
spec:
  playbook: hello_world.yml
  playbookRepo: https://github.com/ansible/ansible-tower-samples
  playbookGitRef: master
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
        - name: cpu
          quantity: 500m
        - name: memory
          quantity: 512Mi
      requests:
        - name: cpu
          quantity: 100m
        - name: memory
          quantity: 256Mi
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
				cmd := exec.Command("kubectl", "get", "ansiblejob", ansibleJobName, "-n", testNamespace, "--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "AnsibleJob should exist")
				g.Expect(output).To(ContainSubstring(ansibleJobName))
			}
			Eventually(verifyAnsibleJobExists, timeout, interval).Should(Succeed())

			By("Checking that the AnsibleJob status progresses")
			verifyAnsibleJobStatus := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ansiblejob", ansibleJobName, "-n", testNamespace,
					"-o", "jsonpath={.status.phase}", "--context", "kind-maintenance-operator-test-e2e")
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
			verifyKubernetesJobExists := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "jobs", "-n", testNamespace, "-l",
					fmt.Sprintf("ansible-job=%s", ansibleJobName), "--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring(ansibleJobName), "Kubernetes Job should be created")
			}
			Eventually(verifyKubernetesJobExists, timeout, interval).Should(Succeed())

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
apiVersion: maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s-configmap
  namespace: %s
spec:
  playbook: hello_world.yml
  playbookRepo: https://github.com/ansible/ansible-tower-samples
  playbookGitRef: master
  inventory:
    configMapRef:
      name: test-inventory
      key: hosts
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
				cmd := exec.Command("kubectl", "get", "ansiblejob", fmt.Sprintf("%s-configmap", ansibleJobName), "-n", testNamespace, "--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "AnsibleJob should exist")
				g.Expect(output).To(ContainSubstring(fmt.Sprintf("%s-configmap", ansibleJobName)))
			}
			Eventually(verifyAnsibleJobExists, timeout, interval).Should(Succeed())

			By("Verifying that a Kubernetes Job is created")
			verifyKubernetesJobExists := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "jobs", "-n", testNamespace, "-l",
					fmt.Sprintf("ansible-job=%s-configmap", ansibleJobName), "--context", "kind-maintenance-operator-test-e2e")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring(fmt.Sprintf("%s-configmap", ansibleJobName)), "Kubernetes Job should be created")
			}
			Eventually(verifyKubernetesJobExists, timeout, interval).Should(Succeed())

			By("Cleaning up test resources")
			os.Remove(manifestFile)
			os.Remove(configMapFile)
			cmd = exec.Command("kubectl", "delete", "ansiblejob", fmt.Sprintf("%s-configmap", ansibleJobName), "-n", testNamespace, "--timeout=60s", "--context", "kind-maintenance-operator-test-e2e")
			_, _ = utils.Run(cmd)
		})
	})
})
