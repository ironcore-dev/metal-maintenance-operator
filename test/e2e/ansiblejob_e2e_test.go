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

	Context("Direct Ansible Runner Mode", func() {
		var (
			ansibleJobName = "e2e-direct-test"
			testNamespace  = "ansiblejob-e2e-test"
		)

		BeforeEach(func() {
			By("Creating test namespace")
			cmd := exec.Command("kubectl", "create", "ns", testNamespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create test namespace")
		})

		AfterEach(func() {
			By("Cleaning up test resources")
			cmd := exec.Command("kubectl", "delete", "ansiblejob", ansibleJobName, "-n", testNamespace, "--timeout=60s")
			_, _ = utils.Run(cmd)

			By("Cleaning up test namespace")
			cmd = exec.Command("kubectl", "delete", "ns", testNamespace, "--timeout=60s")
			_, _ = utils.Run(cmd)
		})

		It("should successfully create and complete an AnsibleJob", func() {
			By("Creating an AnsibleJob manifest")
			ansibleJobManifest := fmt.Sprintf(`
apiVersion: maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s
  namespace: %s
spec:
  playbook: ping.yml
  playbookRepo:
    url: https://github.com/sap/foundation
    branch: main
  rolesRepo:
    url: https://github.com/sap/baremetal
    branch: main
  inventory: |
    [all]
    localhost ansible_connection=local
  extraVars:
    target_hosts: localhost
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
			cmd := exec.Command("kubectl", "apply", "-f", manifestFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply AnsibleJob manifest")

			By("Checking that the AnsibleJob is created")
			verifyAnsibleJobExists := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ansiblejob", ansibleJobName, "-n", testNamespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "AnsibleJob should exist")
				g.Expect(output).To(ContainSubstring(ansibleJobName))
			}
			Eventually(verifyAnsibleJobExists, timeout, interval).Should(Succeed())

			By("Checking that the AnsibleJob status progresses")
			verifyAnsibleJobStatus := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ansiblejob", ansibleJobName, "-n", testNamespace,
					"-o", "jsonpath={.status.phase}")
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
					fmt.Sprintf("ansiblejob=%s", ansibleJobName))
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(ContainSubstring(ansibleJobName), "Kubernetes Job should be created")
			}
			Eventually(verifyKubernetesJobExists, timeout, interval).Should(Succeed())

			By("Cleaning up the manifest file")
			os.Remove(manifestFile)
		})
	})

	Context("AWX Mode", func() {
		var (
			ansibleJobName = "e2e-awx-test"
			testNamespace  = "ansiblejob-awx-e2e-test"
		)

		BeforeEach(func() {
			By("Creating test namespace")
			cmd := exec.Command("kubectl", "create", "ns", testNamespace)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create test namespace")

			By("Creating AWX credentials secret")
			awxSecretManifest := fmt.Sprintf(`
apiVersion: v1
kind: Secret
metadata:
  name: awx-credentials
  namespace: %s
type: Opaque
stringData:
  username: admin
  password: password
`, testNamespace)

			secretFile := filepath.Join("/tmp", "awx-secret.yaml")
			err = os.WriteFile(secretFile, []byte(awxSecretManifest), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write secret manifest")

			cmd = exec.Command("kubectl", "apply", "-f", secretFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create AWX secret")

			os.Remove(secretFile)
		})

		AfterEach(func() {
			By("Cleaning up test resources")
			cmd := exec.Command("kubectl", "delete", "ansiblejob", ansibleJobName, "-n", testNamespace, "--timeout=60s")
			_, _ = utils.Run(cmd)

			By("Cleaning up test namespace")
			cmd = exec.Command("kubectl", "delete", "ns", testNamespace, "--timeout=60s")
			_, _ = utils.Run(cmd)
		})

		It("should successfully create an AnsibleJob with AWX configuration", func() {
			By("Creating an AnsibleJob manifest with AWX configuration")
			ansibleJobManifest := fmt.Sprintf(`
apiVersion: maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s
  namespace: %s
spec:
  playbook: ping.yml
  playbookRepo:
    url: https://github.com/sap/foundation
    branch: main
  rolesRepo:
    url: https://github.com/sap/baremetal
    branch: main
  awx:
    url: https://awx.example.com
    credentials:
      secretRef:
        name: awx-credentials
    jobTemplateId: 123
    inventory: Production
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
			cmd := exec.Command("kubectl", "apply", "-f", manifestFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply AnsibleJob manifest")

			By("Checking that the AnsibleJob is created")
			verifyAnsibleJobExists := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ansiblejob", ansibleJobName, "-n", testNamespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "AnsibleJob should exist")
				g.Expect(output).To(ContainSubstring(ansibleJobName))
			}
			Eventually(verifyAnsibleJobExists, timeout, interval).Should(Succeed())

			By("Verifying AWX configuration is set correctly")
			cmd = exec.Command("kubectl", "get", "ansiblejob", ansibleJobName, "-n", testNamespace,
				"-o", "jsonpath={.spec.awx.url}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(output)).To(Equal("https://awx.example.com"))

			By("Cleaning up the manifest file")
			os.Remove(manifestFile)
		})

		It("should successfully create an AnsibleJob with AWX template name", func() {
			By("Creating an AnsibleJob manifest with AWX template name")
			ansibleJobManifest := fmt.Sprintf(`
apiVersion: maintenance.metal.ironcore.dev/v1alpha1
kind: AnsibleJob
metadata:
  name: %s-name
  namespace: %s
spec:
  playbook: ping.yml
  playbookRepo:
    url: https://github.com/sap/foundation
    branch: main
  rolesRepo:
    url: https://github.com/sap/baremetal
    branch: main
  awx:
    url: https://awx.example.com
    credentials:
      secretRef:
        name: awx-credentials
    jobTemplateName: "Maintenance Template"
    inventory: Production
  resources:
    limits:
      cpu: 500m
      memory: 512Mi
    requests:
      cpu: 100m
      memory: 256Mi
`, ansibleJobName, testNamespace)

			manifestFile := filepath.Join("/tmp", fmt.Sprintf("%s-name.yaml", ansibleJobName))
			err := os.WriteFile(manifestFile, []byte(ansibleJobManifest), 0644)
			Expect(err).NotTo(HaveOccurred(), "Failed to write manifest file")

			By("Applying the AnsibleJob manifest")
			cmd := exec.Command("kubectl", "apply", "-f", manifestFile)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to apply AnsibleJob manifest")

			By("Checking that the AnsibleJob is created")
			verifyAnsibleJobExists := func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ansiblejob", fmt.Sprintf("%s-name", ansibleJobName), "-n", testNamespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "AnsibleJob should exist")
				g.Expect(output).To(ContainSubstring(fmt.Sprintf("%s-name", ansibleJobName)))
			}
			Eventually(verifyAnsibleJobExists, timeout, interval).Should(Succeed())

			By("Verifying AWX template name is set correctly")
			cmd = exec.Command("kubectl", "get", "ansiblejob", fmt.Sprintf("%s-name", ansibleJobName),
				"-n", testNamespace, "-o", "jsonpath={.spec.awx.jobTemplateName}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(output)).To(Equal("Maintenance Template"))

			By("Cleaning up the manifest file and resource")
			os.Remove(manifestFile)
			cmd = exec.Command("kubectl", "delete", "ansiblejob", fmt.Sprintf("%s-name", ansibleJobName),
				"-n", testNamespace, "--timeout=60s")
			_, _ = utils.Run(cmd)
		})
	})
})
