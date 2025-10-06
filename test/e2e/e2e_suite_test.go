// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ironcore-dev/maintenance-operator/test/utils"
)

var (
	// Optional Environment Variables:
	// - CERT_MANAGER_INSTALL_SKIP=true: Skips CertManager installation during test setup.
	// These variables are useful if CertManager is already installed, avoiding
	// re-installation and conflicts.
	skipCertManagerInstall = os.Getenv("CERT_MANAGER_INSTALL_SKIP") == "true"
	// isCertManagerAlreadyInstalled will be set true when CertManager CRDs be found on the cluster
	isCertManagerAlreadyInstalled = false

	// projectImage is the name of the image which will be build and loaded
	// with the code source changes to be tested.
	projectImage = "maintenance-operator:e2e-test"
)

// TestE2E runs the end-to-end (e2e) test suite for the project. These tests execute in an isolated,
// temporary environment to validate project changes with the purpose of being used in CI jobs.
// The default setup requires Kind, builds/loads the Manager Docker image locally, and installs
// CertManager.
func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	_, _ = fmt.Fprintf(GinkgoWriter, "Starting maintenance-operator e2e test suite\n")
	RunSpecs(t, "e2e suite")
}

var _ = BeforeSuite(func() {
	// Skip Docker build and Kind loading when using existing cluster (e.g., with Tilt)
	skipDockerOps := os.Getenv("SKIP_DOCKER_OPS") == "true"

	if !skipDockerOps {
		By("building the manager(Operator) image")
		cmd := exec.Command("make", "docker-build", fmt.Sprintf("IMG=%s", projectImage))
		_, err := utils.Run(cmd)
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to build the manager(Operator) image")

		// TODO(user): If you want to change the e2e test vendor from Kind, ensure the image is
		// built and available before running the tests. Also, remove the following block.
		By("loading the manager(Operator) image on Kind")
		err = utils.LoadImageToKindClusterWithName(projectImage)
		ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to load the manager(Operator) image into Kind")
	} else {
		_, _ = fmt.Fprintf(GinkgoWriter, "Skipping Docker build and image loading (using existing cluster setup)...\n")
	}

	// The tests-e2e are intended to run on a temporary cluster that is created and destroyed for testing.
	// To prevent errors when tests run in environments with CertManager already installed,
	// we check for its presence before execution.

	By("verifying cluster is ready")
	Eventually(func() error {
		cmd := exec.Command("kubectl", "get", "nodes")
		_, err := utils.Run(cmd)
		return err
	}).WithTimeout(2*time.Minute).WithPolling(5*time.Second).Should(Succeed(), "Cluster should be ready")

	// Setup CertManager before the suite if not skipped and if not already installed
	if !skipCertManagerInstall {
		By("checking if cert manager is installed already")
		isCertManagerAlreadyInstalled = utils.IsCertManagerCRDsInstalled()
		if !isCertManagerAlreadyInstalled {
			_, _ = fmt.Fprintf(GinkgoWriter, "Installing CertManager...\n")
			Expect(utils.InstallCertManager()).To(Succeed(), "Failed to install CertManager")
		} else {
			_, _ = fmt.Fprintf(GinkgoWriter, "WARNING: CertManager is already installed. Skipping installation...\n")
		}
	}

	// Deploy the operator to the cluster
	By("deploying the maintenance-operator")
	cmd := exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", projectImage))
	cmd.Env = append(os.Environ(), fmt.Sprintf("KUBECTL=kubectl --context %s", "kind-maintenance-operator-test-e2e"))
	_, err := utils.Run(cmd)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Failed to deploy the maintenance-operator")

	// Wait for the operator to be ready
	By("waiting for maintenance-operator to be ready")
	Eventually(func() error {
		cmd := exec.Command("kubectl", "get", "deployment", "maintenance-operator-controller-manager", "-n", "maintenance-operator-system", "--context", "kind-maintenance-operator-test-e2e")
		_, err := utils.Run(cmd)
		return err
	}).WithTimeout(2*time.Minute).WithPolling(5*time.Second).Should(Succeed(), "maintenance-operator deployment should be created")

	Eventually(func() bool {
		cmd := exec.Command("kubectl", "get", "deployment", "maintenance-operator-controller-manager", "-n", "maintenance-operator-system", "--context", "kind-maintenance-operator-test-e2e", "-o", "jsonpath={.status.readyReplicas}")
		output, err := utils.Run(cmd)
		if err != nil {
			return false
		}
		return strings.TrimSpace(output) == "1"
	}).WithTimeout(3*time.Minute).WithPolling(10*time.Second).Should(BeTrue(), "maintenance-operator should be ready")
})

var _ = AfterSuite(func() {
	// Undeploy the operator
	By("undeploying the maintenance-operator")
	cmd := exec.Command("make", "undeploy")
	_, _ = utils.Run(cmd)

	// Teardown CertManager after the suite if not skipped and if it was not already installed
	if !skipCertManagerInstall && !isCertManagerAlreadyInstalled {
		_, _ = fmt.Fprintf(GinkgoWriter, "Uninstalling CertManager...\n")
		utils.UninstallCertManager()
	}
})
