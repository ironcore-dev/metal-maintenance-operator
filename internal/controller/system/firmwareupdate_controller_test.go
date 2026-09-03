// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package system

import (
	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/maintenance/v1alpha1"
	systemv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/system/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	mockserver "github.com/ironcore-dev/metal-operator/bmc/mock/server"
	"k8s.io/utils/ptr"

	. "sigs.k8s.io/controller-runtime/pkg/envtest/komega"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// dellSystemID is the mock server's System resource folder name (see
// metal-operator/bmc/mock/server/data/Systems/437XR1138R2) whose Manufacturer
// is overridden to "Dell Inc." for this Describe block via
// mockserver.WithSystemOverride, so BMC vendor detection resolves to
// bmc.DellRedfishBMC instead of the vendor-neutral RedfishBaseBMC.
const dellSystemID = "437XR1138R2"

var _ = Describe("FirmwareUpdate Controller", func() {
	ns := SetupTest(nil, mockserver.WithSystemOverride(dellSystemID, map[string]any{"Manufacturer": "Dell Inc."}))

	var (
		server    *metalv1alpha1.Server
		bmcSecret *metalv1alpha1.BMCSecret
	)

	BeforeEach(func(ctx SpecContext) {
		By("Creating a BMCSecret")
		bmcSecret = &metalv1alpha1.BMCSecret{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-",
			},
			Data: map[string][]byte{
				metalv1alpha1.BMCSecretUsernameKeyName: []byte("foo"),
				metalv1alpha1.BMCSecretPasswordKeyName: []byte("bar"),
			},
		}
		Expect(k8sClient.Create(ctx, bmcSecret)).To(Succeed())

		By("Creating a Server")
		server = &metalv1alpha1.Server{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-maintenance-",
			},
			Spec: metalv1alpha1.ServerSpec{
				SystemUUID: "38947555-7742-3448-3784-823347823834",
				BMC: &metalv1alpha1.BMCAccess{
					// ProtocolRedfish (not ProtocolRedfishLocal) is required here:
					// only bmc.NewRedfishBMCClient (used for ProtocolRedfish) performs
					// vendor detection and dispatches to bmc.DellRedfishBMC. RedfishLocal
					// always returns the vendor-neutral RedfishBaseBMC wrapper.
					Protocol: metalv1alpha1.Protocol{
						Name: metalv1alpha1.ProtocolRedfish,
						Port: MockServerPort,
					},
					Address: MockServerIP,
					BMCSecretRef: v1.LocalObjectReference{
						Name: bmcSecret.Name,
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, server)).To(Succeed())

		By("Ensuring that the Server is in available state")
		Eventually(UpdateStatus(server, func() {
			server.Status.State = metalv1alpha1.ServerStateAvailable
		})).Should(Succeed())

		By("Ensuring that the Server's SystemURI has been discovered")
		// simcontrollers.ServerReconciler discovers and patches Spec.SystemURI
		// asynchronously. Waiting for it here avoids a race where
		// FirmwareUpdateDell issues its first repository check against an
		// empty SystemURI, which 404s and is treated as a fatal error.
		Eventually(Object(server)).Should(
			HaveField("Spec.SystemURI", Not(BeEmpty())),
		)
	})

	AfterEach(func(ctx SpecContext) {
		Expect(k8sClient.Delete(ctx, server)).To(Succeed())
		Expect(k8sClient.Delete(ctx, bmcSecret)).To(Succeed())
		EnsureCleanState()
		mockServers[0].ResetDellRepositoryUpdate()
	})

	It("should apply a pending repository firmware update through to completion", func(ctx SpecContext) {
		By("Ensuring that the server has Available state")
		Eventually(Object(server)).Should(
			HaveField("Status.State", metalv1alpha1.ServerStateAvailable),
		)

		By("Creating a FirmwareUpdateDell")
		fwUpdate := &systemv1alpha1.FirmwareUpdate{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-",
			},
			Spec: systemv1alpha1.FirmwareUpdateSpec{
				FirmwareUpdateTemplate: systemv1alpha1.FirmwareUpdateTemplate{
					Repository: &systemv1alpha1.FirmwareRepository{
						ShareType:   systemv1alpha1.DellShareTypeHTTPS,
						Address:     "downloads.dell.com",
						CatalogFile: "Catalog.xml",
					},
					ServerMaintenancePolicy: ptr.To(maintenancev1alpha1.ServerMaintenancePolicyEnforced),
				},
				ServerRef: &v1.LocalObjectReference{Name: server.Name},
			},
		}
		Expect(k8sClient.Create(ctx, fwUpdate)).To(Succeed())

		By("Ensuring that the FirmwareUpdateDell has entered InProgress state")
		Eventually(Object(fwUpdate)).Should(
			HaveField("Status.State", systemv1alpha1.FirmwareUpdateStateInProgress),
		)

		By("Ensuring that the ServerMaintenance resource has been created")
		var serverMaintenanceList maintenancev1alpha1.ServerMaintenanceList
		Eventually(ObjectList(&serverMaintenanceList)).Should(HaveField("Items", Not(BeEmpty())))

		serverMaintenance := &maintenancev1alpha1.ServerMaintenance{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns.Name,
				Name:      fwUpdate.Name,
			},
		}
		Eventually(Get(serverMaintenance)).Should(Succeed())

		By("Ensuring that the ServerMaintenance has been referenced by FirmwareUpdateDell")
		Eventually(Object(fwUpdate)).Should(
			HaveField("Status.ServerMaintenanceRef", &metalv1alpha1.ObjectReference{
				Namespace: serverMaintenance.Namespace,
				Name:      serverMaintenance.Name,
			}),
		)

		By("Ensuring that the ServerMaintenance is in InMaintenance state")
		Eventually(Object(serverMaintenance)).Should(
			HaveField("Status.State", maintenancev1alpha1.ServerMaintenanceStateInMaintenance),
		)

		By("Ensuring that the repository-based firmware update has completed")
		Eventually(Object(fwUpdate)).Should(
			HaveField("Status.State", systemv1alpha1.FirmwareUpdateStateCompleted),
		)

		By("Ensuring that the FirmwareUpdateDell has removed the ServerMaintenance reference")
		Eventually(Object(fwUpdate)).Should(
			HaveField("Status.ServerMaintenanceRef", BeNil()),
		)
		Consistently(Object(fwUpdate)).Should(
			HaveField("Status.ServerMaintenanceRef", BeNil()),
		)

		By("Ensuring that the ServerMaintenance has been deleted")
		Eventually(Get(serverMaintenance)).Should(Satisfy(apierrors.IsNotFound))

		By("Deleting the FirmwareUpdateDell")
		Expect(k8sClient.Delete(ctx, fwUpdate)).To(Succeed())

		By("Ensuring that the FirmwareUpdateDell has been removed")
		Eventually(Get(fwUpdate)).Should(Satisfy(apierrors.IsNotFound))
		Consistently(Get(fwUpdate)).Should(Satisfy(apierrors.IsNotFound))
	})

	It("should mark Failed when the repository check job fails", func(ctx SpecContext) {
		By("Ensuring that the server has Available state")
		Eventually(Object(server)).Should(
			HaveField("Status.State", metalv1alpha1.ServerStateAvailable),
		)

		By("Creating a FirmwareUpdateDell with a catalog file that triggers a failing job")
		fwUpdate := &systemv1alpha1.FirmwareUpdate{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-",
			},
			Spec: systemv1alpha1.FirmwareUpdateSpec{
				FirmwareUpdateTemplate: systemv1alpha1.FirmwareUpdateTemplate{
					Repository: &systemv1alpha1.FirmwareRepository{
						ShareType:   systemv1alpha1.DellShareTypeHTTPS,
						Address:     "downloads.dell.com",
						CatalogFile: "fail-catalog.xml",
					},
					ServerMaintenancePolicy: ptr.To(maintenancev1alpha1.ServerMaintenancePolicyEnforced),
				},
				ServerRef: &v1.LocalObjectReference{Name: server.Name},
			},
		}
		Expect(k8sClient.Create(ctx, fwUpdate)).To(Succeed())

		By("Ensuring that the FirmwareUpdateDell has entered Failed state")
		Eventually(Object(fwUpdate)).Should(
			HaveField("Status.State", systemv1alpha1.FirmwareUpdateStateFailed),
		)

		By("Ensuring that no ServerMaintenance resource has been created")
		var serverMaintenanceList maintenancev1alpha1.ServerMaintenanceList
		Consistently(ObjectList(&serverMaintenanceList)).Should(HaveField("Items", BeEmpty()))

		By("Ensuring that the Server has not entered Maintenance state")
		Consistently(ObjectList(&serverMaintenanceList)).Should(HaveField("Items", BeEmpty()))

		By("Deleting the FirmwareUpdateDell")
		Expect(k8sClient.Delete(ctx, fwUpdate)).To(Succeed())

		By("Ensuring that the FirmwareUpdateDell has been removed")
		Eventually(Get(fwUpdate)).Should(Satisfy(apierrors.IsNotFound))
	})
})
