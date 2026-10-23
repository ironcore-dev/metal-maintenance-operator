// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package system

import (
	"fmt"
	"net/netip"

	"github.com/ironcore-dev/metal-maintenance-operator/api"
	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/maintenance/v1alpha1"
	systemv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/system/v1alpha1"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/constants"
	testutils "github.com/ironcore-dev/metal-maintenance-operator/internal/testutil"
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

var _ = Describe("FirmwareUpdateSet Controller", func() {
	var MockServerIPAddrs = []netip.AddrPort{
		netip.MustParseAddrPort(fmt.Sprintf("%s:%d", MockServerIP, MockServerPort)),
		netip.MustParseAddrPort(fmt.Sprintf("%s:%d", MockServerIP, MockServerPort+1)),
		netip.MustParseAddrPort(fmt.Sprintf("%s:%d", MockServerIP, MockServerPort+2)),
	}

	ns := SetupTest(MockServerIPAddrs, mockserver.WithSystemOverride(dellSystemID, map[string]any{"Manufacturer": "Dell Inc."}))

	var (
		server01  *metalv1alpha1.Server
		server02  *metalv1alpha1.Server
		server03  *metalv1alpha1.Server
		bmcSecret *metalv1alpha1.BMCSecret
	)

	newServer := func(generateName string, port int32, manufacturer string) *metalv1alpha1.Server {
		return &metalv1alpha1.Server{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: generateName,
				Labels: map[string]string{
					"metal.ironcore.dev/Manufacturer": manufacturer,
				},
			},
			Spec: metalv1alpha1.ServerSpec{
				SystemUUID: "38947555-7742-3448-3784-823347823834",
				BMC: &metalv1alpha1.BMCAccess{
					Protocol: metalv1alpha1.Protocol{
						Name: metalv1alpha1.ProtocolRedfish,
						Port: port,
					},
					Address: MockServerIP,
					BMCSecretRef: v1.LocalObjectReference{
						Name: bmcSecret.Name,
					},
				},
			},
		}
	}

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

		By("Creating Server01 that does not match the selector")
		server01 = newServer("test-server01-", MockServerPort, "foo")
		Expect(k8sClient.Create(ctx, server01)).To(Succeed())

		By("Creating Server02 that matches the selector")
		server02 = newServer("test-server02-", MockServerPort+1, "bar")
		Expect(k8sClient.Create(ctx, server02)).To(Succeed())

		By("Creating Server03 that matches the selector")
		server03 = newServer("test-server03-", MockServerPort+2, "bar")
		Expect(k8sClient.Create(ctx, server03)).To(Succeed())

		By("Patching servers to Available so they can be Parked for maintenance")
		Eventually(UpdateStatus(server01, func() {
			server01.Status.State = metalv1alpha1.ServerStateAvailable
		})).Should(Succeed())
		Eventually(UpdateStatus(server02, func() {
			server02.Status.State = metalv1alpha1.ServerStateAvailable
		})).Should(Succeed())
		Eventually(UpdateStatus(server03, func() {
			server03.Status.State = metalv1alpha1.ServerStateAvailable
		})).Should(Succeed())

		By("Ensuring that the Servers' SystemURI has been discovered")
		Eventually(Object(server01)).Should(HaveField("Spec.SystemURI", Not(BeEmpty())))
		Eventually(Object(server02)).Should(HaveField("Spec.SystemURI", Not(BeEmpty())))
		Eventually(Object(server03)).Should(HaveField("Spec.SystemURI", Not(BeEmpty())))
	})

	AfterEach(func(ctx SpecContext) {
		Expect(k8sClient.Delete(ctx, server01)).To(Succeed())
		Expect(k8sClient.Delete(ctx, server02)).To(Succeed())
		Expect(k8sClient.Delete(ctx, server03)).To(Succeed())
		Expect(k8sClient.Delete(ctx, bmcSecret)).To(Succeed())
		EnsureCleanState()
		for _, ms := range mockServers {
			ms.ResetDellRepositoryUpdate()
		}
	})

	It("should successfully reconcile the resource", func(ctx SpecContext) {
		By("Creating a FirmwareUpdateSet")
		fwUpdateSet := &systemv1alpha1.FirmwareUpdateSet{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-firmwareupdate-set-",
				Namespace:    ns.Name,
			},
			Spec: systemv1alpha1.FirmwareUpdateSetSpec{
				FirmwareUpdateTemplate: systemv1alpha1.FirmwareUpdateTemplate{
					DellRepository: &systemv1alpha1.DellFirmwareRepository{
						ShareType:   systemv1alpha1.DellShareTypeHTTPS,
						Address:     "downloads.dell.com",
						CatalogFile: "Catalog.xml",
					},
					ServerMaintenancePolicy: ptr.To(maintenancev1alpha1.ServerMaintenancePolicyEnforced),
				},
				ServerSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{
						"metal.ironcore.dev/Manufacturer": "bar",
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, fwUpdateSet)).To(Succeed())

		By("Checking that a FirmwareUpdate has been created for each matching Server")
		fwUpdate02 := &systemv1alpha1.FirmwareUpdate{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns.Name,
				Name:      fwUpdateSet.Name + "-" + server02.Name,
			},
		}
		Eventually(Get(fwUpdate02)).Should(Succeed())

		fwUpdate03 := &systemv1alpha1.FirmwareUpdate{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns.Name,
				Name:      fwUpdateSet.Name + "-" + server03.Name,
			},
		}
		Eventually(Get(fwUpdate03)).Should(Succeed())

		By("Checking that no FirmwareUpdate has been created for the non-matching Server")
		fwUpdate01 := &systemv1alpha1.FirmwareUpdate{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns.Name,
				Name:      fwUpdateSet.Name + "-" + server01.Name,
			},
		}
		Consistently(Get(fwUpdate01)).Should(Satisfy(apierrors.IsNotFound))

		By("Checking that the FirmwareUpdates are owned by the FirmwareUpdateSet")
		Expect(fwUpdate02.OwnerReferences).To(ContainElement(metav1.OwnerReference{
			APIVersion:         "system.metal.ironcore.dev/v1alpha1",
			Kind:               "FirmwareUpdateSet",
			Name:               fwUpdateSet.Name,
			UID:                fwUpdateSet.UID,
			Controller:         new(true),
			BlockOwnerDeletion: new(true),
		}))
		Expect(fwUpdate03.OwnerReferences).To(ContainElement(metav1.OwnerReference{
			APIVersion:         "system.metal.ironcore.dev/v1alpha1",
			Kind:               "FirmwareUpdateSet",
			Name:               fwUpdateSet.Name,
			UID:                fwUpdateSet.UID,
			Controller:         new(true),
			BlockOwnerDeletion: new(true),
		}))

		By("Checking that the FirmwareUpdateSet status reflects the selected Servers")
		Eventually(Object(fwUpdateSet)).Should(SatisfyAll(
			HaveField("Status.FullyLabeledServers", BeNumerically("==", 2)),
			HaveField("Status.AvailableFirmwareUpdate", BeNumerically("==", 2)),
		))

		By("Checking that both FirmwareUpdates reach Completed state")
		Eventually(Object(fwUpdate02)).Should(
			HaveField("Status.State", systemv1alpha1.FirmwareUpdateStateCompleted),
		)
		Eventually(Object(fwUpdate03)).Should(
			HaveField("Status.State", systemv1alpha1.FirmwareUpdateStateCompleted),
		)

		By("Checking that the FirmwareUpdateSet status has been updated to Completed")
		Eventually(Object(fwUpdateSet)).Should(SatisfyAll(
			HaveField("Status.FullyLabeledServers", BeNumerically("==", 2)),
			HaveField("Status.AvailableFirmwareUpdate", BeNumerically("==", 2)),
			HaveField("Status.CompletedFirmwareUpdate", BeNumerically("==", 2)),
			HaveField("Status.InProgressFirmwareUpdate", BeNumerically("==", 0)),
			HaveField("Status.FailedFirmwareUpdate", BeNumerically("==", 0)),
		))

		By("Removing the matching label from Server03")
		Eventually(Update(server03, func() {
			server03.Labels = map[string]string{}
		})).Should(Succeed())

		By("Checking that the orphaned FirmwareUpdate has been pruned")
		Eventually(Get(fwUpdate03)).Should(Satisfy(apierrors.IsNotFound))

		By("Checking that the FirmwareUpdateSet status reflects the pruned Server")
		Eventually(Object(fwUpdateSet)).Should(SatisfyAll(
			HaveField("Status.FullyLabeledServers", BeNumerically("==", 1)),
			HaveField("Status.AvailableFirmwareUpdate", BeNumerically("==", 1)),
			HaveField("Status.CompletedFirmwareUpdate", BeNumerically("==", 1)),
		))

		By("Deleting the FirmwareUpdateSet")
		Expect(k8sClient.Delete(ctx, fwUpdateSet)).To(Succeed())

		By("Ensuring that the FirmwareUpdateSet has been removed")
		Eventually(Get(fwUpdateSet)).Should(Satisfy(apierrors.IsNotFound))

		Eventually(Object(server01)).Should(testutils.ServerNotParked)
		Eventually(Object(server02)).Should(testutils.ServerNotParked)
		Eventually(Object(server03)).Should(testutils.ServerNotParked)
	})

	It("should successfully reconcile the resource when servers are deleted/created", func(ctx SpecContext) {
		By("Creating a FirmwareUpdateSet")
		fwUpdateSet := &systemv1alpha1.FirmwareUpdateSet{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-firmwareupdate-set-",
				Namespace:    ns.Name,
			},
			Spec: systemv1alpha1.FirmwareUpdateSetSpec{
				FirmwareUpdateTemplate: systemv1alpha1.FirmwareUpdateTemplate{
					DellRepository: &systemv1alpha1.DellFirmwareRepository{
						ShareType:   systemv1alpha1.DellShareTypeHTTPS,
						Address:     "downloads.dell.com",
						CatalogFile: "Catalog.xml",
					},
					ServerMaintenancePolicy: ptr.To(maintenancev1alpha1.ServerMaintenancePolicyEnforced),
				},
				ServerSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{
						"metal.ironcore.dev/Manufacturer": "bar",
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, fwUpdateSet)).To(Succeed())

		fwUpdate02 := &systemv1alpha1.FirmwareUpdate{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns.Name,
				Name:      fwUpdateSet.Name + "-" + server02.Name,
			},
		}
		fwUpdate03 := &systemv1alpha1.FirmwareUpdate{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns.Name,
				Name:      fwUpdateSet.Name + "-" + server03.Name,
			},
		}

		By("Checking that the FirmwareUpdates have been created")
		Eventually(Get(fwUpdate02)).Should(Succeed())
		Eventually(Get(fwUpdate03)).Should(Succeed())

		By("Checking that both FirmwareUpdates reach Completed state")
		Eventually(Object(fwUpdate02)).Should(
			HaveField("Status.State", systemv1alpha1.FirmwareUpdateStateCompleted),
		)
		Eventually(Object(fwUpdate03)).Should(
			HaveField("Status.State", systemv1alpha1.FirmwareUpdateStateCompleted),
		)

		By("Checking that the status has been updated")
		Eventually(Object(fwUpdateSet)).Should(SatisfyAll(
			HaveField("Status.FullyLabeledServers", BeNumerically("==", 2)),
			HaveField("Status.AvailableFirmwareUpdate", BeNumerically("==", 2)),
			HaveField("Status.CompletedFirmwareUpdate", BeNumerically("==", 2)),
			HaveField("Status.FailedFirmwareUpdate", BeNumerically("==", 0)),
		))

		By("Deleting Server02")
		Expect(k8sClient.Delete(ctx, server02)).To(Succeed())
		Eventually(Get(server02)).Should(Satisfy(apierrors.IsNotFound))

		By("Checking that the FirmwareUpdate for the deleted Server has been pruned")
		Eventually(Get(fwUpdate02)).ShouldNot(Succeed())
		Eventually(Get(fwUpdate03)).Should(Succeed())

		By("Checking that the status has been updated")
		Eventually(Object(fwUpdateSet)).Should(SatisfyAll(
			HaveField("Status.FullyLabeledServers", BeNumerically("==", 1)),
			HaveField("Status.AvailableFirmwareUpdate", BeNumerically("==", 1)),
			HaveField("Status.CompletedFirmwareUpdate", BeNumerically("==", 1)),
			HaveField("Status.FailedFirmwareUpdate", BeNumerically("==", 0)),
		))

		By("Recreating Server02")
		mockServers[1].ResetDellRepositoryUpdate()
		server02.ResourceVersion = ""
		Expect(k8sClient.Create(ctx, server02)).Should(Succeed())
		Eventually(UpdateStatus(server02, func() {
			server02.Status.State = metalv1alpha1.ServerStateAvailable
		})).Should(Succeed())
		Eventually(Object(server02)).Should(HaveField("Spec.SystemURI", Not(BeEmpty())))

		By("Checking that the FirmwareUpdate has been recreated")
		Eventually(Get(fwUpdate02)).Should(Succeed())

		By("Checking that the recreated FirmwareUpdate reaches Completed state")
		Eventually(Object(fwUpdate02)).Should(
			HaveField("Status.State", systemv1alpha1.FirmwareUpdateStateCompleted),
		)

		By("Checking that the status has been updated")
		Eventually(Object(fwUpdateSet)).Should(SatisfyAll(
			HaveField("Status.FullyLabeledServers", BeNumerically("==", 2)),
			HaveField("Status.AvailableFirmwareUpdate", BeNumerically("==", 2)),
			HaveField("Status.CompletedFirmwareUpdate", BeNumerically("==", 2)),
			HaveField("Status.FailedFirmwareUpdate", BeNumerically("==", 0)),
		))

		By("Updating the label of Server01 to match the selector")
		Eventually(Update(server01, func() {
			server01.Labels = map[string]string{
				"metal.ironcore.dev/Manufacturer": "bar",
			}
		})).Should(Succeed())

		fwUpdate01 := &systemv1alpha1.FirmwareUpdate{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns.Name,
				Name:      fwUpdateSet.Name + "-" + server01.Name,
			},
		}
		By("Checking that a 3rd FirmwareUpdate has been created")
		Eventually(Get(fwUpdate01)).Should(Succeed())

		By("Checking that the 3rd FirmwareUpdate reaches Completed state")
		Eventually(Object(fwUpdate01)).Should(
			HaveField("Status.State", systemv1alpha1.FirmwareUpdateStateCompleted),
		)

		By("Checking that the status has been updated")
		Eventually(Object(fwUpdateSet)).Should(SatisfyAll(
			HaveField("Status.FullyLabeledServers", BeNumerically("==", 3)),
			HaveField("Status.AvailableFirmwareUpdate", BeNumerically("==", 3)),
			HaveField("Status.CompletedFirmwareUpdate", BeNumerically("==", 3)),
			HaveField("Status.FailedFirmwareUpdate", BeNumerically("==", 0)),
		))

		// cleanup
		// Wait for each FirmwareUpdate to release its ServerMaintenance before
		// deleting it: shouldDelete only blocks deletion while InProgress, so
		// deleting a Completed FirmwareUpdate that is still holding an
		// unreleased ServerMaintenanceRef (the drift-check reconcile that
		// clears it hasn't run yet) would orphan the ServerMaintenance and
		// leave the Server permanently Parked.
		Eventually(Object(fwUpdate01)).Should(HaveField("Status.ServerMaintenanceRef", BeNil()))
		Eventually(Object(fwUpdate02)).Should(HaveField("Status.ServerMaintenanceRef", BeNil()))
		Eventually(Object(fwUpdate03)).Should(HaveField("Status.ServerMaintenanceRef", BeNil()))

		Expect(k8sClient.Delete(ctx, fwUpdateSet)).To(Succeed())
		Expect(k8sClient.Delete(ctx, fwUpdate01)).To(Succeed())
		Expect(k8sClient.Delete(ctx, fwUpdate02)).To(Succeed())
		Expect(k8sClient.Delete(ctx, fwUpdate03)).To(Succeed())
		Eventually(Object(server01)).Should(testutils.ServerNotParked)
		Eventually(Object(server02)).Should(testutils.ServerNotParked)
		Eventually(Object(server03)).Should(testutils.ServerNotParked)
	})

	It("Should successfully retry failed state child resources", func(ctx SpecContext) {
		failedAutoRetryCount := 2
		By("Creating a FirmwareUpdateSet with a catalog file that triggers a failing job")
		fwUpdateSet := &systemv1alpha1.FirmwareUpdateSet{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-firmwareupdate-set-",
				Namespace:    ns.Name,
			},
			Spec: systemv1alpha1.FirmwareUpdateSetSpec{
				FirmwareUpdateTemplate: systemv1alpha1.FirmwareUpdateTemplate{
					DellRepository: &systemv1alpha1.DellFirmwareRepository{
						ShareType:   systemv1alpha1.DellShareTypeHTTPS,
						Address:     "downloads.dell.com",
						CatalogFile: "fail-catalog.xml",
					},
					ServerMaintenancePolicy: ptr.To(maintenancev1alpha1.ServerMaintenancePolicyEnforced),
					RetryPolicy:             &api.RetryPolicy{MaxAttempts: new(int32(failedAutoRetryCount))},
				},
				ServerSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{
						"metal.ironcore.dev/Manufacturer": "bar",
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, fwUpdateSet)).To(Succeed())

		fwUpdate02 := &systemv1alpha1.FirmwareUpdate{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns.Name,
				Name:      fwUpdateSet.Name + "-" + server02.Name,
			},
		}
		fwUpdate03 := &systemv1alpha1.FirmwareUpdate{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns.Name,
				Name:      fwUpdateSet.Name + "-" + server03.Name,
			},
		}

		By("Checking that the FirmwareUpdates have been created")
		Eventually(Get(fwUpdate02)).Should(Succeed())
		Eventually(Get(fwUpdate03)).Should(Succeed())

		By("Ensuring that both FirmwareUpdates have failed")
		Eventually(Object(fwUpdate02)).Should(SatisfyAll(
			HaveField("Status.State", systemv1alpha1.FirmwareUpdateStateFailed),
			HaveField("Status.FailedAttempts", Equal(int32(failedAutoRetryCount))),
		))
		Eventually(Object(fwUpdate03)).Should(SatisfyAll(
			HaveField("Status.State", systemv1alpha1.FirmwareUpdateStateFailed),
			HaveField("Status.FailedAttempts", Equal(int32(failedAutoRetryCount))),
		))

		By("Checking that the status has been updated to Failed")
		Eventually(Object(fwUpdateSet)).Should(SatisfyAll(
			HaveField("Status.FullyLabeledServers", BeNumerically("==", 2)),
			HaveField("Status.AvailableFirmwareUpdate", BeNumerically("==", 2)),
			HaveField("Status.FailedFirmwareUpdate", BeNumerically("==", 2)),
		))

		By("Ensuring that the FirmwareUpdates have not changed")
		Consistently(Object(fwUpdate02), "50ms").Should(SatisfyAll(
			HaveField("Status.State", systemv1alpha1.FirmwareUpdateStateFailed),
			HaveField("Status.FailedAttempts", Equal(int32(failedAutoRetryCount))),
		))
		Consistently(Object(fwUpdate03), "50ms").Should(SatisfyAll(
			HaveField("Status.State", systemv1alpha1.FirmwareUpdateStateFailed),
			HaveField("Status.FailedAttempts", Equal(int32(failedAutoRetryCount))),
		))

		By("Updating the FirmwareUpdateSet with the retry annotation")
		Eventually(Update(fwUpdateSet, func() {
			fwUpdateSet.Annotations = map[string]string{
				constants.OperationAnnotation: constants.OperationAnnotationRetryChildAndSelf,
			}
		})).Should(Succeed())

		By("Ensuring that the FirmwareUpdates have been retried")
		Eventually(Object(fwUpdate02)).Should(SatisfyAll(
			HaveField("Status.State", Not(Equal(systemv1alpha1.FirmwareUpdateStateFailed))),
			HaveField("Status.FailedAttempts", Not(Equal(int32(failedAutoRetryCount)))),
		))
		Eventually(Object(fwUpdate03)).Should(SatisfyAll(
			HaveField("Status.State", Not(Equal(systemv1alpha1.FirmwareUpdateStateFailed))),
			HaveField("Status.FailedAttempts", Not(Equal(int32(failedAutoRetryCount)))),
		))

		// cleanup
		Expect(k8sClient.Delete(ctx, fwUpdateSet)).To(Succeed())
		Expect(k8sClient.Delete(ctx, fwUpdate02)).To(Succeed())
		Expect(k8sClient.Delete(ctx, fwUpdate03)).To(Succeed())
		Eventually(Object(server01)).Should(testutils.ServerNotParked)
		Eventually(Object(server02)).Should(testutils.ServerNotParked)
		Eventually(Object(server03)).Should(testutils.ServerNotParked)
	})
})
