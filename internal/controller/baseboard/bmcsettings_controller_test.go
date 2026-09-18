// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package baseboard

import (
	"context"
	"errors"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	. "sigs.k8s.io/controller-runtime/pkg/envtest/komega"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/ironcore-dev/controller-utils/metautils"
	"github.com/ironcore-dev/metal-maintenance-operator/api"
	baseboardv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/baseboard/v1alpha1"
	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/maintenance/v1alpha1"
	constants "github.com/ironcore-dev/metal-maintenance-operator/internal/constants"
	testutils "github.com/ironcore-dev/metal-maintenance-operator/internal/testutil"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/ironcore-dev/metal-operator/bmc"
	bmcutils "github.com/ironcore-dev/metal-operator/pkg/bmcutils"
	"github.com/stmcginnis/gofish/schemas"
)

var _ = Describe("BMCSettings Controller", func() {
	ns := SetupTest(nil)

	var (
		server    *metalv1alpha1.Server
		bmc       *metalv1alpha1.BMC
		bmcSecret *metalv1alpha1.BMCSecret
	)

	BeforeEach(func(ctx SpecContext) {
		By("Creating a BMCSecret")
		bmcSecret = &metalv1alpha1.BMCSecret{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-bmc-secret-",
			},
			Data: map[string][]byte{
				metalv1alpha1.BMCSecretUsernameKeyName: []byte("foo"),
				metalv1alpha1.BMCSecretPasswordKeyName: []byte("bar"),
			},
		}
		Expect(k8sClient.Create(ctx, bmcSecret)).To(Succeed())

		By("Creating a BMC resource")
		bmc = &metalv1alpha1.BMC{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-bmc-",
			},
			Spec: metalv1alpha1.BMCSpec{
				Endpoint: &metalv1alpha1.InlineEndpoint{
					IP:         metalv1alpha1.MustParseIP(MockServerIP),
					MACAddress: "23:11:8A:33:CF:EA",
				},
				Protocol: metalv1alpha1.Protocol{
					Name: metalv1alpha1.ProtocolRedfishLocal,
					Port: MockServerPort,
				},
				BMCSecretRef: v1.LocalObjectReference{
					Name: bmcSecret.Name,
				},
			},
		}
		Expect(k8sClient.Create(ctx, bmc)).To(Succeed())

		By("Ensuring that the Server resource will be created")
		server = &metalv1alpha1.Server{
			ObjectMeta: metav1.ObjectMeta{
				Name: bmcutils.GetServerNameFromBMCandIndex(0, bmc),
			},
			Spec: metalv1alpha1.ServerSpec{
				BMCRef: &v1.LocalObjectReference{Name: bmc.Name},
			},
		}
		Expect(k8sClient.Create(ctx, server)).To(Succeed())

		By("Ensuring that the Server is in an available state")
		Eventually(UpdateStatus(server, func() {
			server.Status.State = metalv1alpha1.ServerStateAvailable
		})).Should(Succeed())

		Eventually(UpdateStatus(bmc, func() {
			bmc.Status.State = metalv1alpha1.BMCStateEnabled
		})).Should(Succeed())
	})

	AfterEach(func(ctx SpecContext) {
		Expect(k8sClient.Delete(ctx, bmc)).To(Succeed())
		// The simulated BMC controller deletes its discovered Server on BMC
		// deletion, so the Server may already be gone by the time we get here.
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, server))).To(Succeed())
		Expect(k8sClient.Delete(ctx, bmcSecret)).To(Succeed())
		EnsureCleanState()
		mockServers[0].ResetBMCSettings("BMC")
	})

	It("should successfully patch BMCSettings reference to referred BMC", func(ctx SpecContext) {
		By("Creating a BMCSettings")
		settings := &baseboardv1alpha1.BMCSettings{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-bmc-",
			},
			Spec: baseboardv1alpha1.BMCSettingsSpec{
				BMCRef: &v1.LocalObjectReference{Name: bmc.Name},
				BMCSettingsTemplate: baseboardv1alpha1.BMCSettingsTemplate{
					SettingsTemplate: api.SettingsTemplate{
						Version:                 "1.45.455b66-rev4",
						ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
					},
				}},
		}
		Expect(k8sClient.Create(ctx, settings)).To(Succeed())

		Eventually(Object(settings)).Should(SatisfyAny(
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied),
		))

		// cleanup
		Expect(k8sClient.Delete(ctx, settings)).To(Succeed())
	})

	It("should move to completed if no BMCSettings changes to referred BMC", func(ctx SpecContext) {
		By("Creating a BMCSettings")
		settings := &baseboardv1alpha1.BMCSettings{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-bmc-nochange",
			},
			Spec: baseboardv1alpha1.BMCSettingsSpec{
				BMCRef: &v1.LocalObjectReference{Name: bmc.Name},
				BMCSettingsTemplate: baseboardv1alpha1.BMCSettingsTemplate{
					SettingsTemplate: api.SettingsTemplate{
						Version:                 "1.45.455b66-rev4",
						ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
					},
				}},
		}
		Expect(k8sClient.Create(ctx, settings)).To(Succeed())

		Eventually(Object(settings)).Should(SatisfyAll(
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied),
		))

		By("Deleting the BMCSettings")
		Expect(k8sClient.Delete(ctx, settings)).To(Succeed())
	})

	It("should update the setting if BMCSettings changes requested in Available State", func(ctx SpecContext) {
		bmcSetting := map[string]string{"abc": "changed-bmc-setting"}

		By("update the server state to Available state")
		Eventually(UpdateStatus(server, func() {
			server.Status.State = metalv1alpha1.ServerStateAvailable
			server.Status.PowerState = metalv1alpha1.ServerOffPowerState
		})).Should(Succeed())

		By("Creating a BMCSettings")
		settings := &baseboardv1alpha1.BMCSettings{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-bmc-change",
			},
			Spec: baseboardv1alpha1.BMCSettingsSpec{
				BMCRef: &v1.LocalObjectReference{Name: bmc.Name},
				BMCSettingsTemplate: baseboardv1alpha1.BMCSettingsTemplate{
					SettingsTemplate: api.SettingsTemplate{
						Version:                 "1.45.455b66-rev4",
						SettingsFlow:            []api.SettingsFlowItem{{Name: "flow1", Priority: 1, Settings: bmcSetting}},
						ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
					},
				}},
		}
		Expect(k8sClient.Create(ctx, settings)).To(Succeed())

		By("Ensuring that the BMCSettings has reached next state")
		Eventually(Object(settings)).Should(SatisfyAny(
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateInProgress),
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied),
		))
		Eventually(Object(settings)).Should(SatisfyAll(
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied),
		))

		By("Ensuring that the Maintenance resource has been deleted")
		var serverMaintenanceList maintenancev1alpha1.ServerMaintenanceList
		Eventually(ObjectList(&serverMaintenanceList)).Should(HaveField("Items", BeEmpty()))
		Consistently(ObjectList(&serverMaintenanceList)).Should(HaveField("Items", BeEmpty()))
		Consistently(Object(settings)).Should(SatisfyAll(
			HaveField("Spec.ServerMaintenanceRefs", BeNil()),
		))

		By("Deleting the BMCSettings")
		Expect(k8sClient.Delete(ctx, settings)).To(Succeed())

		// cleanup
		Eventually(Object(server)).Should(testutils.ServerNotParked)
	})

	It("should create maintenance and wait for its approval before applying settings", func(ctx SpecContext) {
		bmcSetting := map[string]string{"abc": "changed-to-req-server-maintenance-through-ownerapproved"}

		// Put server in reserved state and create a BMC setting with OwnerApproved policy that needs reboot
		By("Creating an Ignition secret")
		ignitionSecret := &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:    ns.Name,
				GenerateName: "test-",
			},
			Data: nil,
		}
		Expect(k8sClient.Create(ctx, ignitionSecret)).To(Succeed())

		By("Creating a ServerClaim")
		serverClaim := &metalv1alpha1.ServerClaim{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:    ns.Name,
				GenerateName: "test-",
			},
			Spec: metalv1alpha1.ServerClaimSpec{
				Power:             metalv1alpha1.PowerOn,
				ServerRef:         &v1.LocalObjectReference{Name: server.Name},
				IgnitionSecretRef: &v1.LocalObjectReference{Name: ignitionSecret.Name},
				Image:             "foo:bar",
			},
		}
		Expect(k8sClient.Create(ctx, serverClaim)).To(Succeed())

		By("Ensuring that the Server has been claimed")
		Eventually(Update(server, func() {
			server.Spec.ServerClaimRef = &metalv1alpha1.ImmutableObjectReference{
				Name:      serverClaim.Name,
				Namespace: serverClaim.Namespace,
			}
		})).Should(Succeed())
		Eventually(UpdateStatus(server, func() {
			server.Status.State = metalv1alpha1.ServerStateReserved
		})).Should(Succeed())

		By("Creating a BMCSettings")
		settings := &baseboardv1alpha1.BMCSettings{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-bmc-change",
			},
			Spec: baseboardv1alpha1.BMCSettingsSpec{
				BMCRef: &v1.LocalObjectReference{Name: bmc.Name},
				BMCSettingsTemplate: baseboardv1alpha1.BMCSettingsTemplate{
					SettingsTemplate: api.SettingsTemplate{
						Version:                 "1.45.455b66-rev4",
						SettingsFlow:            []api.SettingsFlowItem{{Name: "flow1", Priority: 1, Settings: bmcSetting}},
						ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyOwnerApproval,
					},
				}},
		}
		Expect(k8sClient.Create(ctx, settings)).To(Succeed())

		Eventually(Object(settings)).Should(SatisfyAny(
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateInProgress),
		))

		By("Ensuring that the Maintenance resource has been created")
		var serverMaintenanceList maintenancev1alpha1.ServerMaintenanceList
		Eventually(ObjectList(&serverMaintenanceList)).Should(HaveField("Items", HaveLen(1)))

		serverMaintenance := &maintenancev1alpha1.ServerMaintenance{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns.Name,
				Name:      serverMaintenanceList.Items[0].Name,
			},
		}
		Eventually(Get(serverMaintenance)).Should(Succeed())

		By("Ensuring that the Maintenance resource has been referenced by BMCSettings resource")
		Eventually(Object(settings)).Should(
			HaveField("Spec.ServerMaintenanceRefs",
				[]api.ServerMaintenanceRefItem{{
					ServerMaintenanceRef: &metalv1alpha1.ObjectReference{
						Namespace: serverMaintenance.Namespace,
						Name:      serverMaintenance.Name,
					}}}),
		)

		Eventually(Object(settings)).Should(SatisfyAny(
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateInProgress),
		))

		By("Approving the maintenance")
		Eventually(Update(serverClaim, func() {
			metautils.SetAnnotation(serverClaim, maintenancev1alpha1.ServerMaintenanceApprovedLabelKey, trueValue)
			metautils.SetLabel(serverClaim, maintenancev1alpha1.ServerMaintenanceApprovedLabelKey, trueValue)
		})).Should(Succeed())

		Eventually(Object(settings)).Should(SatisfyAny(
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateInProgress),
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied),
		))

		Eventually(Object(settings)).Should(SatisfyAll(
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied),
		))

		By("Ensuring that the Maintenance resource has been deleted")
		Eventually(ObjectList(&serverMaintenanceList)).Should(HaveField("Items", BeEmpty()))
		Consistently(ObjectList(&serverMaintenanceList)).Should(HaveField("Items", BeEmpty()))
		Consistently(Object(settings)).Should(SatisfyAll(
			HaveField("Spec.ServerMaintenanceRefs", BeNil()),
		))

		By("Deleting the BMCSettings")
		Expect(k8sClient.Delete(ctx, settings)).To(Succeed())

		// cleanup
		Expect(k8sClient.Delete(ctx, serverClaim)).To(Succeed())
		Eventually(Update(server, func() {
			server.Spec.ServerClaimRef = nil
		})).Should(Succeed())
		Eventually(Object(server)).Should(SatisfyAll(
			testutils.ServerNotParked,
			HaveField("Status.State", Not(Equal(metalv1alpha1.ServerStateReserved))),
		))
	})

	It("should wait for upgrade and reconcile when BMCSettings version is correct", func(ctx SpecContext) {
		bmcSetting := map[string]string{"fooreboot": "145"}

		By("Updating the server state to Available")
		Eventually(UpdateStatus(server, func() {
			server.Status.State = metalv1alpha1.ServerStateAvailable
			server.Status.PowerState = metalv1alpha1.ServerOffPowerState
		})).Should(Succeed())

		By("Creating a BMCSettings")
		settings := &baseboardv1alpha1.BMCSettings{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-bmc-upgrade",
			},
			Spec: baseboardv1alpha1.BMCSettingsSpec{
				BMCRef: &v1.LocalObjectReference{Name: bmc.Name},
				BMCSettingsTemplate: baseboardv1alpha1.BMCSettingsTemplate{
					SettingsTemplate: api.SettingsTemplate{
						Version:                 "2.45.455b66-rev4",
						SettingsFlow:            []api.SettingsFlowItem{{Name: "flow1", Priority: 1, Settings: bmcSetting}},
						ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
					},
				}},
		}
		Expect(k8sClient.Create(ctx, settings)).To(Succeed())

		By("Ensuring that the BMCSettings resource state is Pending while waiting for version upgrade")
		Eventually(Object(settings)).Should(
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStatePending),
		)

		By("Ensuring that the serverMaintenance not ref. while waiting for upgrade")
		Consistently(Object(settings)).Should(SatisfyAll(
			HaveField("Spec.ServerMaintenanceRefs", BeNil()),
		))

		By("Simulate the server BMCSettings version update by matching the spec version")
		Eventually(Update(settings, func() {
			settings.Spec.Version = "1.45.455b66-rev4"
		})).Should(Succeed())

		By("Ensuring that the BMCSettings resource has completed upgrade and moved to InProgress")
		Eventually(Object(settings)).Should(
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateInProgress),
		)

		By("Ensuring that the Maintenance resource has been created")
		var serverMaintenanceList maintenancev1alpha1.ServerMaintenanceList
		Eventually(ObjectList(&serverMaintenanceList)).Should(HaveField("Items", HaveLen(1)))

		serverMaintenance := &maintenancev1alpha1.ServerMaintenance{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns.Name,
				Name:      serverMaintenanceList.Items[0].Name,
			},
		}
		Eventually(Get(serverMaintenance)).Should(Succeed())

		By("Ensuring that the BMCSettings resource has moved to next state")
		Eventually(Object(settings)).Should(SatisfyAny(
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateInProgress),
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied),
		))
		Eventually(Object(settings)).Should(SatisfyAll(
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied),
		))

		By("Ensuring that the Maintenance resource has been deleted")
		Eventually(ObjectList(&serverMaintenanceList)).Should(HaveField("Items", BeEmpty()))
		Consistently(ObjectList(&serverMaintenanceList)).Should(HaveField("Items", BeEmpty()))
		Consistently(Object(settings)).Should(SatisfyAll(
			HaveField("Spec.ServerMaintenanceRefs", BeNil()),
		))

		By("Deleting the BMCSetting resource")
		Expect(k8sClient.Delete(ctx, settings)).To(Succeed())

		By("Ensuring that the BMCSettings resource is removed")
		Eventually(Get(settings)).Should(Satisfy(apierrors.IsNotFound))
		Consistently(Get(settings)).Should(Satisfy(apierrors.IsNotFound))

		Eventually(Object(server)).Should(testutils.ServerNotParked)
	})

	It("should allow retry using annotation", func(ctx SpecContext) {
		// Settings that do not require reboot (mocked in bmc/redfish_local.go)
		bmcSetting := map[string]string{"fooreboot": "145"}

		By("Updating the server state to Available")
		Eventually(UpdateStatus(server, func() {
			server.Status.State = metalv1alpha1.ServerStateAvailable
			server.Status.PowerState = metalv1alpha1.ServerOffPowerState
		})).Should(Succeed())

		By("Creating a BMCSettings")
		settings := &baseboardv1alpha1.BMCSettings{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-bmc-upgrade",
			},
			Spec: baseboardv1alpha1.BMCSettingsSpec{
				BMCRef: &v1.LocalObjectReference{Name: bmc.Name},
				BMCSettingsTemplate: baseboardv1alpha1.BMCSettingsTemplate{
					SettingsTemplate: api.SettingsTemplate{
						Version:                 "1.45.455b66-rev4",
						SettingsFlow:            []api.SettingsFlowItem{{Name: "flow1", Priority: 1, Settings: bmcSetting}},
						ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
					},
				}},
		}
		Expect(k8sClient.Create(ctx, settings)).To(Succeed())

		By("Moving to Failed state")
		Eventually(UpdateStatus(settings, func() {
			settings.Status.State = baseboardv1alpha1.BMCSettingsStateFailed
		})).Should(Succeed())

		Eventually(Update(settings, func() {
			settings.Annotations = map[string]string{
				constants.OperationAnnotation: constants.OperationAnnotationRetry,
			}
		})).Should(Succeed())

		Eventually(Object(settings)).Should(
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateInProgress),
		)

		Eventually(Object(settings)).Should(
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied),
		)

		By("Ensuring that the Maintenance resource has been deleted")
		var serverMaintenanceList maintenancev1alpha1.ServerMaintenanceList
		Eventually(ObjectList(&serverMaintenanceList)).Should(HaveField("Items", BeEmpty()))

		// cleanup
		Expect(k8sClient.Delete(ctx, settings)).To(Succeed())
		Eventually(Object(server)).Should(testutils.ServerNotParked)
	})

	It("should replace missing BMCSettings ref in server", func(ctx SpecContext) {
		// Settings that do not require reboot (mocked in bmc/redfish_local.go)
		bmcSetting := map[string]string{"fooreboot": "145"}

		By("Updating the server state to Available")
		Eventually(UpdateStatus(server, func() {
			server.Status.State = metalv1alpha1.ServerStateAvailable
			server.Status.PowerState = metalv1alpha1.ServerOffPowerState
		})).Should(Succeed())

		By("Creating a BMCSettings")
		settings := &baseboardv1alpha1.BMCSettings{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-bmc-upgrade",
			},
			Spec: baseboardv1alpha1.BMCSettingsSpec{
				BMCRef: &v1.LocalObjectReference{Name: bmc.Name},
				BMCSettingsTemplate: baseboardv1alpha1.BMCSettingsTemplate{
					SettingsTemplate: api.SettingsTemplate{
						Version:                 "1.45.455b66-rev4",
						SettingsFlow:            []api.SettingsFlowItem{{Name: "flow1", Priority: 1, Settings: bmcSetting}},
						ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
					},
				}},
		}
		Expect(k8sClient.Create(ctx, settings)).To(Succeed())

		Expect(k8sClient.Delete(ctx, settings)).To(Succeed())
		By("Forcing deletion of the object by removing finalizers")
		Eventually(func() error {
			err := Update(settings, func() {
				settings.Finalizers = []string{}
			})()
			if apierrors.IsNotFound(err) {
				return nil
			}
			return err
		}).Should(Succeed())
		By("check if maintenance has been created on the server and delete if its present")
		var serverMaintenanceList maintenancev1alpha1.ServerMaintenanceList
		cleanupStaleServerMaintenance := func() error {
			_, err := ObjectList(&serverMaintenanceList)()
			if err != nil {
				return err
			}
			for _, item := range serverMaintenanceList.Items {
				if len(item.OwnerReferences) == 0 || item.OwnerReferences[0].UID != settings.UID {
					continue
				}
				By(fmt.Sprintf("Deleting the ServerMaintenance created by BMCSettings %v", item.Name))
				if err := client.IgnoreNotFound(k8sClient.Delete(ctx, &item)); err != nil {
					return err
				}
				Eventually(func() error {
					err := Update(&item, func() {
						item.Finalizers = []string{}
					})()
					if apierrors.IsNotFound(err) {
						return nil
					}
					return err
				}).Should(Succeed())
			}

			if err := Get(server)(); err != nil {
				return err
			}
			return nil
		}
		Eventually(cleanupStaleServerMaintenance).Should(Succeed())
		Consistently(cleanupStaleServerMaintenance).Should(Succeed())

		By("creation of new BMCSettings with same spec")
		bmcSettings2 := &baseboardv1alpha1.BMCSettings{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-bmc-recreate-",
			},
			Spec: baseboardv1alpha1.BMCSettingsSpec{
				BMCRef: &v1.LocalObjectReference{Name: bmc.Name},
				BMCSettingsTemplate: baseboardv1alpha1.BMCSettingsTemplate{
					SettingsTemplate: api.SettingsTemplate{
						Version:                 "1.45.455b66-rev4",
						SettingsFlow:            []api.SettingsFlowItem{{Name: "flow1", Priority: 1, Settings: bmcSetting}},
						ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
					},
				}},
		}
		Expect(k8sClient.Create(ctx, bmcSettings2)).To(Succeed())

		Eventually(Object(bmcSettings2)).Should(SatisfyAny(
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateInProgress),
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied),
		))

		Eventually(Object(bmcSettings2)).Should(SatisfyAll(
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied),
		))

		By("Ensuring that the Maintenance resource has been deleted")
		Eventually(ObjectList(&serverMaintenanceList)).Should(HaveField("Items", BeEmpty()))

		Expect(k8sClient.Delete(ctx, bmcSettings2)).To(Succeed())
		Eventually(Get(bmcSettings2)).Should(Satisfy(apierrors.IsNotFound))
		Eventually(Object(server)).Should(testutils.ServerNotParked)
	})

	It("Should allow retry using annotation", func(ctx SpecContext) {
		// settings which does not reboot. mocked at
		// metal-operator/bmc/redfish_local.go defaultMockedBMCSetting
		bmcSetting := map[string]string{"UnknownData": "145"}

		failedAutoRetryCount := 2

		By("update the server state to Available  state")
		Eventually(UpdateStatus(server, func() {
			server.Status.State = metalv1alpha1.ServerStateAvailable
			server.Status.PowerState = metalv1alpha1.ServerOffPowerState
		})).Should(Succeed())

		By("Creating a BMCSetting")
		bmcSettings := &baseboardv1alpha1.BMCSettings{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:    ns.Name,
				GenerateName: "test-bmc-upgrade",
				Annotations: map[string]string{
					constants.OperationAnnotation: constants.OperationAnnotationRetry,
				},
			},
			Spec: baseboardv1alpha1.BMCSettingsSpec{
				BMCRef: &v1.LocalObjectReference{Name: bmc.Name},
				BMCSettingsTemplate: baseboardv1alpha1.BMCSettingsTemplate{
					SettingsTemplate: api.SettingsTemplate{
						Version:                 "1.45.455b66-rev4",
						SettingsFlow:            []api.SettingsFlowItem{{Name: "flow1", Priority: 1, Settings: bmcSetting}},
						ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
						RetryPolicy:             &api.RetryPolicy{MaxAttempts: new(int32(failedAutoRetryCount))},
					},
				}},
		}
		Expect(k8sClient.Create(ctx, bmcSettings)).To(Succeed())

		By("Ensuring that the BMC setting has started retry and FailedAttempts is set")
		Eventually(func(g Gomega) bool {
			g.Expect(Get(bmcSettings)()).To(Succeed())
			return bmcSettings.Status.FailedAttempts > int32(0)
		}).WithPolling((1 * time.Millisecond)).Should(BeTrue())

		Eventually(Object(bmcSettings)).Should(SatisfyAll(
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateFailed),
			HaveField("Status.FailedAttempts", Equal(int32(failedAutoRetryCount))),
		))

		Eventually(Object(bmcSettings)).Should(
			HaveField("ObjectMeta.Annotations", Not(HaveKey(constants.OperationAnnotation))),
		)

		By("Ensuring that the BMC setting has not been changed")
		Consistently(Object(bmcSettings), "250ms").Should(SatisfyAll(
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateFailed),
			HaveField("Status.FailedAttempts", Equal(int32(failedAutoRetryCount))),
		))

		// cleanup
		Expect(k8sClient.Delete(ctx, bmcSettings)).To(Succeed())
		// clean up maintenance if any, as the test not auto delete child objects
		var serverMaintenanceList maintenancev1alpha1.ServerMaintenanceList
		Expect(k8sClient.List(ctx, &serverMaintenanceList)).To(Succeed())
		for _, maintenance := range serverMaintenanceList.Items {
			if metav1.IsControlledBy(&maintenance, bmcSettings) {
				Expect(k8sClient.Delete(ctx, &maintenance)).To(Succeed())
			}
		}
		Eventually(Object(server)).Should(testutils.ServerNotParked)
	})

	It("should apply BMCSettings with a value resolved from a Secret variable", func(ctx SpecContext) {
		By("Creating a Secret containing the setting value")
		varSecret := &v1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:    ns.Name,
				GenerateName: "test-var-secret-",
			},
			Data: map[string][]byte{
				"bmc-setting": []byte("changed-via-secret"),
			},
		}
		Expect(k8sClient.Create(ctx, varSecret)).To(Succeed())
		DeferCleanup(k8sClient.Delete, varSecret)

		By("Creating a BMCSettings with a secretKeyRef variable")
		settings := &baseboardv1alpha1.BMCSettings{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-bmc-var-secret-",
			},
			Spec: baseboardv1alpha1.BMCSettingsSpec{
				BMCRef: &v1.LocalObjectReference{Name: bmc.Name},
				BMCSettingsTemplate: baseboardv1alpha1.BMCSettingsTemplate{
					SettingsTemplate: api.SettingsTemplate{
						Version:                 "1.45.455b66-rev4",
						SettingsFlow:            []api.SettingsFlowItem{{Name: "flow1", Priority: 1, Settings: map[string]string{"abc": "$(SETTING_VAL)"}}},
						ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
						Variables: []api.Variable{
							{
								Key: "SETTING_VAL",
								ValueFrom: &api.VariableSourceValueFrom{
									SecretKeyRef: &api.NamespacedKeySelector{
										Name:      varSecret.Name,
										Namespace: ns.Name,
										Key:       "bmc-setting",
									},
								},
							},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, settings)).To(Succeed())

		By("Ensuring that the BMCSettings reaches Applied state after variable resolution")
		Eventually(Object(settings)).Should(SatisfyAll(
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied),
		))

		By("Ensuring the resolved secret value was written to the BMC (not the raw placeholder)")
		Expect(mockServers[0].GetBMCSettingAttr("BMC")).To(HaveKeyWithValue("abc", "changed-via-secret"))
		Expect(k8sClient.Delete(ctx, settings)).To(Succeed())
	})

	It("should apply BMCSettings with a value resolved from a ConfigMap variable", func(ctx SpecContext) {
		By("Creating a ConfigMap containing the setting value")
		varCM := &v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:    ns.Name,
				GenerateName: "test-var-cm-",
			},
			Data: map[string]string{
				"bmc-setting": "changed-via-configmap",
			},
		}
		Expect(k8sClient.Create(ctx, varCM)).To(Succeed())
		DeferCleanup(k8sClient.Delete, varCM)

		By("Creating a BMCSettings with a configMapKeyRef variable")
		settings := &baseboardv1alpha1.BMCSettings{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-bmc-var-cm-",
			},
			Spec: baseboardv1alpha1.BMCSettingsSpec{
				BMCRef: &v1.LocalObjectReference{Name: bmc.Name},
				BMCSettingsTemplate: baseboardv1alpha1.BMCSettingsTemplate{
					SettingsTemplate: api.SettingsTemplate{
						Version:                 "1.45.455b66-rev4",
						SettingsFlow:            []api.SettingsFlowItem{{Name: "flow1", Priority: 1, Settings: map[string]string{"abc": "$(SETTING_VAL)"}}},
						ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
						Variables: []api.Variable{
							{
								Key: "SETTING_VAL",
								ValueFrom: &api.VariableSourceValueFrom{
									ConfigMapKeyRef: &api.NamespacedKeySelector{
										Name:      varCM.Name,
										Namespace: ns.Name,
										Key:       "bmc-setting",
									},
								},
							},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, settings)).To(Succeed())

		By("Ensuring that the BMCSettings reaches Applied state after variable resolution")
		Eventually(Object(settings)).Should(SatisfyAll(
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied),
		))

		By("Ensuring the resolved ConfigMap value was written to the BMC (not the raw placeholder)")
		Expect(mockServers[0].GetBMCSettingAttr("BMC")).To(HaveKeyWithValue("abc", "changed-via-configmap"))
		Expect(k8sClient.Delete(ctx, settings)).To(Succeed())
	})

	It("should apply BMCSettings with a value resolved from a fieldRef variable", func(ctx SpecContext) {
		By("Creating a BMCSettings with a fieldRef variable pointing to spec.bmcRef.name")
		settings := &baseboardv1alpha1.BMCSettings{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-bmc-var-field-",
			},
			Spec: baseboardv1alpha1.BMCSettingsSpec{
				BMCRef: &v1.LocalObjectReference{Name: bmc.Name},
				BMCSettingsTemplate: baseboardv1alpha1.BMCSettingsTemplate{
					SettingsTemplate: api.SettingsTemplate{
						Version:                 "1.45.455b66-rev4",
						SettingsFlow:            []api.SettingsFlowItem{{Name: "flow1", Priority: 1, Settings: map[string]string{"abc": "$(BMC_NAME)"}}},
						ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
						Variables: []api.Variable{
							{
								Key: "BMC_NAME",
								ValueFrom: &api.VariableSourceValueFrom{
									FieldRef: &api.FieldRefSelector{
										FieldPath: "spec.bmcRef.name",
									},
								},
							},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, settings)).To(Succeed())

		By("Ensuring that the BMCSettings reaches Applied state with the field value substituted")
		Eventually(Object(settings)).Should(SatisfyAll(
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied),
		))

		By("Ensuring the resolved field value (BMC object name) was written to the BMC")
		Expect(mockServers[0].GetBMCSettingAttr("BMC")).To(HaveKeyWithValue("abc", bmc.Name))

		Expect(k8sClient.Delete(ctx, settings)).To(Succeed())
	})

	It("should apply BMCSettings with a single value composed from multiple variables", func(ctx SpecContext) {
		By("Creating a ConfigMap containing the domain part")
		domainCM := &v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:    ns.Name,
				GenerateName: "test-var-domain-cm-",
			},
			Data: map[string]string{
				"search-domain": "example.com",
			},
		}
		Expect(k8sClient.Create(ctx, domainCM)).To(Succeed())
		DeferCleanup(k8sClient.Delete, domainCM)

		By("Creating a BMCSettings where 'abc' is built from $(BmcName).$(SearchDomain)")
		settings := &baseboardv1alpha1.BMCSettings{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-bmc-var-multi-",
			},
			Spec: baseboardv1alpha1.BMCSettingsSpec{
				BMCRef: &v1.LocalObjectReference{Name: bmc.Name},
				BMCSettingsTemplate: baseboardv1alpha1.BMCSettingsTemplate{
					SettingsTemplate: api.SettingsTemplate{
						Version:                 "1.45.455b66-rev4",
						SettingsFlow:            []api.SettingsFlowItem{{Name: "flow1", Priority: 1, Settings: map[string]string{"abc": "$(BmcName).$(SearchDomain)"}}},
						ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
						// Both placeholders resolved from different sources into one value.
						Variables: []api.Variable{
							{
								Key: "BmcName",
								ValueFrom: &api.VariableSourceValueFrom{
									FieldRef: &api.FieldRefSelector{
										FieldPath: "spec.bmcRef.name",
									},
								},
							},
							{
								Key: "SearchDomain",
								ValueFrom: &api.VariableSourceValueFrom{
									ConfigMapKeyRef: &api.NamespacedKeySelector{
										Name:      domainCM.Name,
										Namespace: ns.Name,
										Key:       "search-domain",
									},
								},
							},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, settings)).To(Succeed())

		By("Ensuring that the BMCSettings reaches Applied state with both variables substituted")
		Eventually(Object(settings)).Should(SatisfyAll(
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied),
		))

		By("Ensuring both resolved variable values were concatenated and written to the BMC")
		Expect(mockServers[0].GetBMCSettingAttr("BMC")).To(HaveKeyWithValue("abc", bmc.Name+".example.com"))

		Expect(k8sClient.Delete(ctx, settings)).To(Succeed())
	})

	It("should apply BMCSettings where a later variable key references an earlier variable (chaining)", func(ctx SpecContext) {
		// This mirrors the sample YAML pattern:
		//   - key: BmcName          → fieldRef: spec.bmcRef.name  → e.g. "test-bmc-xxxxx"
		//   - key: LicenseKey       → configMapKeyRef.key: "$(BmcName)"
		//                              i.e. the ConfigMap key is the resolved BmcName
		//   settings: abc: "$(LicenseKey)"

		By("Creating a ConfigMap whose key is the BMC object name")
		// We don't know the generated bmc name yet, so we create the ConfigMap after
		// the bmc name is known from the outer BeforeEach.
		licensesCM := &v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:    ns.Name,
				GenerateName: "test-licenses-cm-",
			},
			// The key is the BMC object name; value is the license string.
			Data: map[string]string{
				bmc.Name: "license-key-for-" + bmc.Name,
			},
		}
		Expect(k8sClient.Create(ctx, licensesCM)).To(Succeed())
		DeferCleanup(k8sClient.Delete, licensesCM)

		By("Creating a BMCSettings with chained variables: BmcName feeds into the ConfigMap key for LicenseKey")
		settings := &baseboardv1alpha1.BMCSettings{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-bmc-var-chain-",
			},
			Spec: baseboardv1alpha1.BMCSettingsSpec{
				BMCRef: &v1.LocalObjectReference{Name: bmc.Name},
				BMCSettingsTemplate: baseboardv1alpha1.BMCSettingsTemplate{
					SettingsTemplate: api.SettingsTemplate{
						Version:                 "1.45.455b66-rev4",
						SettingsFlow:            []api.SettingsFlowItem{{Name: "flow1", Priority: 1, Settings: map[string]string{"abc": "$(LicenseKey)"}}},
						ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
						Variables: []api.Variable{
							{
								// Step 1: resolve BmcName from the object's own field.
								Key: "BmcName",
								ValueFrom: &api.VariableSourceValueFrom{
									FieldRef: &api.FieldRefSelector{
										FieldPath: "spec.bmcRef.name",
									},
								},
							},
							{
								// Step 2: use the already-resolved $(BmcName) as the ConfigMap key.
								Key: "LicenseKey",
								ValueFrom: &api.VariableSourceValueFrom{
									ConfigMapKeyRef: &api.NamespacedKeySelector{
										Name:      licensesCM.Name,
										Namespace: ns.Name,
										Key:       "$(BmcName)", // expanded to bmc.Name at resolution time
									},
								},
							},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, settings)).To(Succeed())

		By("Ensuring that the BMCSettings reaches Applied state — chained variable resolved correctly")
		Eventually(Object(settings)).Should(SatisfyAll(
			HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied),
		))

		By("Ensuring the chained variable (LicenseKey looked up via BmcName) was written to the BMC")
		Expect(mockServers[0].GetBMCSettingAttr("BMC")).To(HaveKeyWithValue("abc", "license-key-for-"+bmc.Name))

		Expect(k8sClient.Delete(ctx, settings)).To(Succeed())
	})
})

var _ = Describe("classifyKey", func() {
	const (
		uri          = "/redfish/v1/Managers/BMC/Settings"
		storedETag   = `W/"v1"`
		currentETag  = `W/"v1"`
		changedETag  = `W/"v2"`
		desiredValue = "ntp.example.com"
	)

	storedEntry := func(etag, vHash string) baseboardv1alpha1.BMCSettingsApplyResultEntry {
		return baseboardv1alpha1.BMCSettingsApplyResultEntry{URI: uri, ETag: etag, ValueHash: vHash}
	}

	It("falls back to value-map GET when no stored entry", func() {
		result := classifyKey("k", desiredValue, baseboardv1alpha1.BMCSettingsApplyResultEntry{}, "", false, false, baseboardv1alpha1.WriteOnlyDriftPolicyConservative, testHMACKey)
		Expect(result.needsValueGet).To(BeTrue())
		Expect(result.needsApply).To(BeFalse())
	})

	It("falls back to value-map GET when stored ETag is empty", func() {
		result := classifyKey("k", desiredValue, storedEntry("", ""), currentETag, true, false, baseboardv1alpha1.WriteOnlyDriftPolicyConservative, testHMACKey)
		Expect(result.needsValueGet).To(BeTrue())
	})

	It("falls back to value-map GET when currentETag is empty (POST-based resource gone)", func() {
		result := classifyKey("k", desiredValue, storedEntry(storedETag, hmacValueHash(testHMACKey, desiredValue)), "", true, false, baseboardv1alpha1.WriteOnlyDriftPolicyConservative, testHMACKey)
		Expect(result.needsValueGet).To(BeTrue())
	})

	It("fast-path skips key when ETag unchanged and value fingerprint unchanged", func() {
		result := classifyKey("k", desiredValue, storedEntry(storedETag, hmacValueHash(testHMACKey, desiredValue)), currentETag, true, false, baseboardv1alpha1.WriteOnlyDriftPolicyConservative, testHMACKey)
		Expect(result.needsApply).To(BeFalse())
		Expect(result.needsValueGet).To(BeFalse())
	})

	It("triggers re-apply when ETag unchanged but desired value changed (Secret rotation)", func() {
		result := classifyKey("k", "new-ntp.example.com", storedEntry(storedETag, hmacValueHash(testHMACKey, desiredValue)), currentETag, true, false, baseboardv1alpha1.WriteOnlyDriftPolicyConservative, testHMACKey)
		Expect(result.needsApply).To(BeTrue())
		Expect(result.needsValueGet).To(BeFalse())
	})

	It("requests value-map GET when ETag changed for readable key", func() {
		result := classifyKey("k", desiredValue, storedEntry(storedETag, hmacValueHash(testHMACKey, desiredValue)), changedETag, true, false, baseboardv1alpha1.WriteOnlyDriftPolicyConservative, testHMACKey)
		Expect(result.needsValueGet).To(BeTrue())
	})

	Context("write-only key, conservative policy, ETag changed", func() {
		It("skips when desired value fingerprint unchanged", func() {
			result := classifyKey("k", desiredValue, storedEntry(storedETag, hmacValueHash(testHMACKey, desiredValue)), changedETag, true, true, baseboardv1alpha1.WriteOnlyDriftPolicyConservative, testHMACKey)
			Expect(result.needsApply).To(BeFalse())
			Expect(result.needsValueGet).To(BeFalse())
		})

		It("re-applies when desired value fingerprint changed", func() {
			result := classifyKey("k", "rotated-value", storedEntry(storedETag, hmacValueHash(testHMACKey, desiredValue)), changedETag, true, true, baseboardv1alpha1.WriteOnlyDriftPolicyConservative, testHMACKey)
			Expect(result.needsApply).To(BeTrue())
		})
	})

	Context("write-only key, strict policy", func() {
		It("always re-applies regardless of ETag and value fingerprint", func() {
			result := classifyKey("k", desiredValue, storedEntry(storedETag, hmacValueHash(testHMACKey, desiredValue)), currentETag, true, true, baseboardv1alpha1.WriteOnlyDriftPolicyStrict, testHMACKey)
			Expect(result.needsApply).To(BeTrue())
		})
	})
})

var _ = Describe("refreshedETags", func() {
	const uri = "/redfish/v1/Managers/BMC/Settings"

	It("returns nil when no ETags changed", func() {
		stored := map[string]baseboardv1alpha1.BMCSettingsApplyResultEntry{
			"key1": {URI: uri, ETag: `W/"v1"`, ValueHash: "hash1"},
		}
		current := map[string]string{uri: `W/"v1"`}
		updated := refreshedETags(stored, current, nil)
		Expect(updated).To(BeNil())
	})

	It("returns updated entry when ETag changed and key is not in diff", func() {
		stored := map[string]baseboardv1alpha1.BMCSettingsApplyResultEntry{
			"key1": {URI: uri, ETag: `W/"v1"`, ValueHash: "hash1"},
		}
		current := map[string]string{uri: `W/"v2"`}
		updated := refreshedETags(stored, current, map[string]struct{}{})
		Expect(updated).To(HaveKey("key1"))
		Expect(updated["key1"].ETag).To(Equal(`W/"v2"`))
		Expect(updated["key1"].ValueHash).To(Equal("hash1"))
	})

	It("does not include keys that are in the diff set", func() {
		stored := map[string]baseboardv1alpha1.BMCSettingsApplyResultEntry{
			"key1": {URI: uri, ETag: `W/"v1"`, ValueHash: "hash1"},
		}
		current := map[string]string{uri: `W/"v2"`}
		diffKeys := map[string]struct{}{"key1": {}}
		updated := refreshedETags(stored, current, diffKeys)
		Expect(updated).To(BeNil())
	})

	It("does not include keys where currentETag is empty", func() {
		stored := map[string]baseboardv1alpha1.BMCSettingsApplyResultEntry{
			"key1": {URI: uri, ETag: `W/"v1"`, ValueHash: "hash1"},
		}
		current := map[string]string{uri: ""}
		updated := refreshedETags(stored, current, nil)
		Expect(updated).To(BeNil())
	})
})

// testHMACKey is a fixed 32-byte key used in unit tests.
var testHMACKey = make([]byte, 32) // zero key is valid for HMAC-SHA256

// fakeEtagDriftClient is an in-memory stand-in for the etagDriftClient interface used in etagAwareDiff unit tests.
type fakeEtagDriftClient struct {
	// etags maps resource URI -> current ETag. "" or absent means the fetch
	// should behave as if the resource is unreachable (e.g. 404 for a POST-based
	// ephemeral resource).
	etags map[string]string
	// fetchErr, if set, makes FetchETags return an error (simulating a transient
	// BMC failure); the caller must fall back to the full value-map GET.
	fetchErr error
	// values holds the BMC's current readable attribute values. Keys absent from
	// this map are treated as write-only (the BMC does not return them).
	values map[string]any

	// fetchETagsCalls and getValuesCalls record how many times each method was
	// invoked, so tests can assert the fast-path really avoided a BMC call.
	fetchETagsCalls int
	getValuesCalls  int
	lastGetKeys     map[string]string
}

func (f *fakeEtagDriftClient) FetchETags(_ context.Context, uris []string) (map[string]string, error) {
	f.fetchETagsCalls++
	if f.fetchErr != nil {
		return nil, f.fetchErr
	}
	result := make(map[string]string, len(uris))
	for _, u := range uris {
		result[u] = f.etags[u]
	}
	return result, nil
}

func (f *fakeEtagDriftClient) GetBMCAttributeValues(_ context.Context, req bmc.GetBMCAttributeValuesRequest) (schemas.SettingsAttributes, error) {
	f.getValuesCalls++
	f.lastGetKeys = req.Attributes
	result := schemas.SettingsAttributes{}
	for key := range req.Attributes {
		if val, ok := f.values[key]; ok {
			result[key] = val
		}
		// Absent from f.values => write-only; simply not included in the response,
		// matching the real BMC's behaviour for write-only keys.
	}
	return result, nil
}

var _ = Describe("etagAwareDiff", func() {
	const (
		uri         = "/redfish/v1/Managers/BMC/Settings"
		storedETag  = `W/"v1"`
		changedETag = `W/"v2"`
	)

	var r *BMCSettingsReconciler

	BeforeEach(func() {
		r = &BMCSettingsReconciler{}
	})

	storedEntry := func(value string) baseboardv1alpha1.BMCSettingsApplyResultEntry {
		return baseboardv1alpha1.BMCSettingsApplyResultEntry{
			URI: uri, ETag: storedETag, ValueHash: hmacValueHash(testHMACKey, value),
		}
	}

	Context("first reconcile — no stored ETags", func() {
		It("performs the full value-map GET and does not call FetchETags", func(ctx SpecContext) {
			client := &fakeEtagDriftClient{values: map[string]any{"ntp": "ntp.example.com"}}
			diff, err := r.etagAwareDiff(ctx, client, "uuid", map[string]string{"ntp": "ntp.example.com"},
				nil, baseboardv1alpha1.WriteOnlyDriftPolicyConservative)
			Expect(err).NotTo(HaveOccurred())
			Expect(diff).To(BeEmpty())
			Expect(client.fetchETagsCalls).To(Equal(0))
			Expect(client.getValuesCalls).To(Equal(1))
		})
	})

	Context("ETag unchanged", func() {
		It("skips the key without any BMC GET when the desired value is also unchanged", func(ctx SpecContext) {
			client := &fakeEtagDriftClient{etags: map[string]string{uri: storedETag}}
			stored := map[string]baseboardv1alpha1.BMCSettingsApplyResultEntry{"ntp": storedEntry("ntp.example.com")}

			diff, err := r.etagAwareDiff(ctx, client, "uuid", map[string]string{"ntp": "ntp.example.com"},
				stored, baseboardv1alpha1.WriteOnlyDriftPolicyConservative)

			Expect(err).NotTo(HaveOccurred())
			Expect(diff).To(BeEmpty())
			Expect(client.fetchETagsCalls).To(Equal(1))
			Expect(client.getValuesCalls).To(Equal(0), "fast-path must not issue a value-map GET")
		})

		It("re-applies when the desired value changed (Secret/ConfigMap rotation), despite ETag match", func(ctx SpecContext) {
			client := &fakeEtagDriftClient{etags: map[string]string{uri: storedETag}}
			stored := map[string]baseboardv1alpha1.BMCSettingsApplyResultEntry{"ntp": storedEntry("old.example.com")}

			diff, err := r.etagAwareDiff(ctx, client, "uuid", map[string]string{"ntp": "new.example.com"},
				stored, baseboardv1alpha1.WriteOnlyDriftPolicyConservative)

			Expect(err).NotTo(HaveOccurred())
			Expect(diff).To(HaveKeyWithValue("ntp", "new.example.com"))
			Expect(client.getValuesCalls).To(Equal(0), "value change is detected without a GET")
		})
	})

	Context("ETag changed, readable key", func() {
		It("issues a value-map GET and finds no drift, leaving the key out of the diff", func(ctx SpecContext) {
			client := &fakeEtagDriftClient{
				etags:  map[string]string{uri: changedETag},
				values: map[string]any{"ntp": "ntp.example.com"},
			}
			stored := map[string]baseboardv1alpha1.BMCSettingsApplyResultEntry{"ntp": storedEntry("ntp.example.com")}

			diff, err := r.etagAwareDiff(ctx, client, "uuid", map[string]string{"ntp": "ntp.example.com"},
				stored, baseboardv1alpha1.WriteOnlyDriftPolicyConservative)

			Expect(err).NotTo(HaveOccurred())
			Expect(diff).To(BeEmpty())
			Expect(client.getValuesCalls).To(Equal(1), "an ETag change on a readable key must be confirmed via GET")
		})

		It("issues a value-map GET and re-applies when the value actually drifted", func(ctx SpecContext) {
			client := &fakeEtagDriftClient{
				etags:  map[string]string{uri: changedETag},
				values: map[string]any{"ntp": "someone-else.example.com"},
			}
			stored := map[string]baseboardv1alpha1.BMCSettingsApplyResultEntry{"ntp": storedEntry("ntp.example.com")}

			diff, err := r.etagAwareDiff(ctx, client, "uuid", map[string]string{"ntp": "ntp.example.com"},
				stored, baseboardv1alpha1.WriteOnlyDriftPolicyConservative)

			Expect(err).NotTo(HaveOccurred())
			Expect(diff).To(HaveKeyWithValue("ntp", "ntp.example.com"))
		})
	})

	Context("ETag changed, write-only key", func() {
		It("Conservative: skips re-apply when the desired value fingerprint is unchanged", func(ctx SpecContext) {
			client := &fakeEtagDriftClient{
				etags:  map[string]string{uri: changedETag},
				values: map[string]any{}, // write-only key: absent from GET response
			}
			stored := map[string]baseboardv1alpha1.BMCSettingsApplyResultEntry{"password": storedEntry("s3cret")}

			diff, err := r.etagAwareDiff(ctx, client, "uuid", map[string]string{"password": "s3cret"},
				stored, baseboardv1alpha1.WriteOnlyDriftPolicyConservative)

			Expect(err).NotTo(HaveOccurred())
			Expect(diff).To(BeEmpty(), "conservative mode accepts the undetectable drift risk")
		})

		It("Conservative: treats nil-valued key (e.g. BMC returns password:null) same as absent — skips re-apply when value unchanged", func(ctx SpecContext) {
			// Simulates Dell iDRAC returning NTPConfigGroup.1.NTP1SecurityKey: null.
			// The key IS in the map but has no readable value.
			client := &fakeEtagDriftClient{
				etags:  map[string]string{uri: changedETag},
				values: map[string]any{"password": nil}, // present but null — write-only behaviour
			}
			stored := map[string]baseboardv1alpha1.BMCSettingsApplyResultEntry{"password": storedEntry("s3cret")}

			diff, err := r.etagAwareDiff(ctx, client, "uuid", map[string]string{"password": "s3cret"},
				stored, baseboardv1alpha1.WriteOnlyDriftPolicyConservative)

			Expect(err).NotTo(HaveOccurred())
			Expect(diff).To(BeEmpty(), "nil value treated as write-only: conservative skips when value fingerprint unchanged")
		})

		It("Strict: skips re-apply for nil key when ETag unchanged (no drift signal)", func(ctx SpecContext) {
			// Strict with no ETag change: no drift evidence, skip to avoid endless resets.
			client := &fakeEtagDriftClient{
				etags:  map[string]string{uri: storedETag}, // ETag UNCHANGED
				values: map[string]any{"password": nil},    // null value — write-only
			}
			stored := map[string]baseboardv1alpha1.BMCSettingsApplyResultEntry{"password": storedEntry("s3cret")}

			diff, err := r.etagAwareDiff(ctx, client, "uuid", map[string]string{"password": "s3cret"},
				stored, baseboardv1alpha1.WriteOnlyDriftPolicyStrict)

			Expect(err).NotTo(HaveOccurred())
			Expect(diff).To(BeEmpty(), "Strict: no ETag change → no re-apply even for null write-only key")
		})

		It("Strict: re-applies nil key when ETag changed (drift signal present)", func(ctx SpecContext) {
			// ETag changed on the resource → Strict assumes our write-only key drifted.
			client := &fakeEtagDriftClient{
				etags:  map[string]string{uri: changedETag}, // ETag CHANGED
				values: map[string]any{"password": nil},     // null value — write-only
			}
			stored := map[string]baseboardv1alpha1.BMCSettingsApplyResultEntry{"password": storedEntry("s3cret")}

			diff, err := r.etagAwareDiff(ctx, client, "uuid", map[string]string{"password": "s3cret"},
				stored, baseboardv1alpha1.WriteOnlyDriftPolicyStrict)

			Expect(err).NotTo(HaveOccurred())
			Expect(diff).To(HaveKeyWithValue("password", "s3cret"), "Strict: ETag changed → re-apply write-only null key")
		})

		It("Conservative: re-applies when the desired value fingerprint changed", func(ctx SpecContext) {
			client := &fakeEtagDriftClient{
				etags:  map[string]string{uri: changedETag},
				values: map[string]any{},
			}
			stored := map[string]baseboardv1alpha1.BMCSettingsApplyResultEntry{"password": storedEntry("old-secret")}

			diff, err := r.etagAwareDiff(ctx, client, "uuid", map[string]string{"password": "rotated-secret"},
				stored, baseboardv1alpha1.WriteOnlyDriftPolicyConservative)

			Expect(err).NotTo(HaveOccurred())
			Expect(diff).To(HaveKeyWithValue("password", "rotated-secret"))
		})

		It("Strict: always re-applies regardless of ETag state or value fingerprint", func(ctx SpecContext) {
			client := &fakeEtagDriftClient{
				etags:  map[string]string{uri: storedETag}, // unchanged ETag
				values: map[string]any{},
			}
			stored := map[string]baseboardv1alpha1.BMCSettingsApplyResultEntry{"password": storedEntry("s3cret")}

			diff, err := r.etagAwareDiff(ctx, client, "uuid", map[string]string{"password": "s3cret"},
				stored, baseboardv1alpha1.WriteOnlyDriftPolicyStrict)

			Expect(err).NotTo(HaveOccurred())
			Expect(diff).To(HaveKeyWithValue("password", "s3cret"))
		})
	})

	Context("POST-based key", func() {
		It("always takes the idempotency fast-path regardless of ETag state (IsPost=true, value unchanged)", func(ctx SpecContext) {
			client := &fakeEtagDriftClient{
				etags:  map[string]string{uri: storedETag}, // unchanged
				values: map[string]any{"cert": "same-cert"},
			}
			stored := map[string]baseboardv1alpha1.BMCSettingsApplyResultEntry{
				"POST /certs": {URI: uri, ETag: storedETag, ValueHash: hmacValueHash(testHMACKey, "same-cert"), IsPost: true},
			}

			diff, err := r.etagAwareDiff(ctx, client, "uuid", map[string]string{"POST /certs": "same-cert"},
				stored, baseboardv1alpha1.WriteOnlyDriftPolicyConservative)

			Expect(err).NotTo(HaveOccurred())
			Expect(diff).To(BeEmpty())
			Expect(client.getValuesCalls).To(Equal(0), "POST-based keys must not issue a GET (idempotency fast-path)")
		})

		It("falls back to re-POST without GET when value changed (IsPost=true, value changed)", func(ctx SpecContext) {
			client := &fakeEtagDriftClient{
				etags:  map[string]string{}, // FetchETags returns "" for the URI (404)
				values: map[string]any{"cert": "same-cert"},
			}
			stored := map[string]baseboardv1alpha1.BMCSettingsApplyResultEntry{
				"POST /certs": {URI: uri, ETag: storedETag, ValueHash: hmacValueHash(testHMACKey, "old-cert"), IsPost: true},
			}

			diff, err := r.etagAwareDiff(ctx, client, "uuid", map[string]string{"POST /certs": "new-cert"},
				stored, baseboardv1alpha1.WriteOnlyDriftPolicyConservative)

			Expect(err).NotTo(HaveOccurred())
			Expect(diff).To(HaveKeyWithValue("POST /certs", "new-cert"), "POST key must be re-applied when value changed")
			Expect(client.getValuesCalls).To(Equal(0), "POST keys must not issue a GET even when re-applying")
		})

		// Write-only POST keys are create-only; skip re-apply when the value fingerprint is unchanged.
		It("write-only POST: skips re-apply when desired value fingerprint is unchanged", func(ctx SpecContext) {
			// The BMC never returns subscription data in GET responses.
			client := &fakeEtagDriftClient{
				etags:  map[string]string{}, // no ETag for POST resources
				values: map[string]any{},    // write-only: absent from GET response
			}
			postKey := "POST /redfish/v1/EventService/Subscriptions"
			payload := `{"Context":"test","Destination":"https://test.example.com/events","Protocol":"Redfish"}`
			stored := map[string]baseboardv1alpha1.BMCSettingsApplyResultEntry{
				postKey: {
					URI:       "/redfish/v1/EventService/Subscriptions/1",
					ETag:      "", // POST responses typically have no ETag
					ValueHash: hmacValueHash(testHMACKey, payload),
					IsPost:    true,
				},
			}

			diff, err := r.etagAwareDiff(ctx, client, "uuid", map[string]string{postKey: payload},
				stored, baseboardv1alpha1.WriteOnlyDriftPolicyConservative)

			Expect(err).NotTo(HaveOccurred())
			Expect(diff).To(BeEmpty(), "POST key must not be re-applied when desired value is unchanged")
			Expect(client.getValuesCalls).To(Equal(0), "POST keys must not issue a GET")
		})

		It("write-only POST: skips re-apply even when IsPost flag is missing (legacy entry — key name prefix used)", func(ctx SpecContext) {
			// Simulates a BMCSettings written by an older version of the controller
			// where IsPost was not yet stored (omitempty on false value).
			client := &fakeEtagDriftClient{
				etags:  map[string]string{},
				values: map[string]any{},
			}
			postKey := "POST /redfish/v1/EventService/Subscriptions"
			payload := `{"Context":"test","Destination":"https://test.example.com/events","Protocol":"Redfish"}`
			stored := map[string]baseboardv1alpha1.BMCSettingsApplyResultEntry{
				postKey: {
					URI:       "/redfish/v1/EventService/Subscriptions/1",
					ETag:      "",
					ValueHash: hmacValueHash(testHMACKey, payload),
					IsPost:    false, // as if deserialized from JSON without isPost field
				},
			}

			diff, err := r.etagAwareDiff(ctx, client, "uuid", map[string]string{postKey: payload},
				stored, baseboardv1alpha1.WriteOnlyDriftPolicyConservative)

			Expect(err).NotTo(HaveOccurred())
			Expect(diff).To(BeEmpty(), "POST key must not be re-applied even when IsPost flag is absent (uses key prefix)")
			Expect(client.getValuesCalls).To(Equal(0), "POST keys must not issue a GET")
		})

		It("write-only POST: re-applies when desired value changed (e.g. new subscription destination)", func(ctx SpecContext) {
			client := &fakeEtagDriftClient{
				etags:  map[string]string{},
				values: map[string]any{},
			}
			postKey := "POST /redfish/v1/EventService/Subscriptions"
			oldPayload := `{"Context":"test","Destination":"https://old.example.com/events","Protocol":"Redfish"}`
			newPayload := `{"Context":"test","Destination":"https://new.example.com/events","Protocol":"Redfish"}`
			stored := map[string]baseboardv1alpha1.BMCSettingsApplyResultEntry{
				postKey: {
					URI:       "/redfish/v1/EventService/Subscriptions/1",
					ETag:      "",
					ValueHash: hmacValueHash(testHMACKey, oldPayload),
					IsPost:    true,
				},
			}

			diff, err := r.etagAwareDiff(ctx, client, "uuid", map[string]string{postKey: newPayload},
				stored, baseboardv1alpha1.WriteOnlyDriftPolicyConservative)

			Expect(err).NotTo(HaveOccurred())
			Expect(diff).To(HaveKeyWithValue(postKey, newPayload), "POST key must be re-applied when payload changed")
		})

		It("write-only POST: re-applies when no stored ValueHash (first idempotency guard)", func(ctx SpecContext) {
			client := &fakeEtagDriftClient{
				etags:  map[string]string{},
				values: map[string]any{},
			}
			postKey := "POST /redfish/v1/EventService/Subscriptions"
			payload := `{"Context":"test","Destination":"https://test.example.com/events","Protocol":"Redfish"}`
			// Simulates a BMCSettings applied before the ValueHash was captured.
			stored := map[string]baseboardv1alpha1.BMCSettingsApplyResultEntry{
				postKey: {
					URI:       "/redfish/v1/EventService/Subscriptions/1",
					ETag:      "",
					ValueHash: "", // no stored hash
					IsPost:    true,
				},
			}

			diff, err := r.etagAwareDiff(ctx, client, "uuid", map[string]string{postKey: payload},
				stored, baseboardv1alpha1.WriteOnlyDriftPolicyConservative)

			Expect(err).NotTo(HaveOccurred())
			Expect(diff).To(HaveKeyWithValue(postKey, payload), "POST key must be re-applied when ValueHash is absent")
		})
	})

	Context("ETag fetch unavailable", func() {
		It("falls back to full value-map GET without a surfaced error when FetchETags errors", func(ctx SpecContext) {
			client := &fakeEtagDriftClient{
				fetchErr: errors.New("boom"),
				values:   map[string]any{"ntp": "ntp.example.com"},
			}
			stored := map[string]baseboardv1alpha1.BMCSettingsApplyResultEntry{"ntp": storedEntry("ntp.example.com")}

			diff, err := r.etagAwareDiff(ctx, client, "uuid", map[string]string{"ntp": "ntp.example.com"},
				stored, baseboardv1alpha1.WriteOnlyDriftPolicyConservative)

			Expect(err).NotTo(HaveOccurred())
			Expect(diff).To(BeEmpty())
			Expect(client.getValuesCalls).To(Equal(1), "must fall back to the value-map GET")
		})
	})

	Context("partial AppliedETags", func() {
		It("uses the fast-path for keys with stored entries and the GET path for keys without", func(ctx SpecContext) {
			client := &fakeEtagDriftClient{
				etags: map[string]string{uri: storedETag},
				values: map[string]any{
					"missing-key": "some-value",
				},
			}
			stored := map[string]baseboardv1alpha1.BMCSettingsApplyResultEntry{
				"ntp": storedEntry("ntp.example.com"),
				// "missing-key" intentionally has no stored entry.
			}

			diff, err := r.etagAwareDiff(ctx, client, "uuid", map[string]string{
				"ntp":         "ntp.example.com",
				"missing-key": "some-value",
			}, stored, baseboardv1alpha1.WriteOnlyDriftPolicyConservative)

			Expect(err).NotTo(HaveOccurred())
			Expect(diff).To(BeEmpty())
			Expect(client.lastGetKeys).To(HaveKey("missing-key"))
			Expect(client.lastGetKeys).NotTo(HaveKey("ntp"), "ntp should have used the fast-path, not the GET")
		})
	})
})

// These specs exercise ETag-based drift detection end to end through the real reconcile loop and mock Redfish server.
var _ = Describe("BMCSettings Controller ETag drift detection", func() {
	_ = SetupTest(nil)

	const bmcSettingsURI = "/redfish/v1/Managers/BMC/Settings"

	var (
		server    *metalv1alpha1.Server
		bmcObj    *metalv1alpha1.BMC
		bmcSecret *metalv1alpha1.BMCSecret
	)

	BeforeEach(func(ctx SpecContext) {
		By("Creating a BMCSecret")
		bmcSecret = &metalv1alpha1.BMCSecret{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-bmc-secret-",
			},
			Data: map[string][]byte{
				metalv1alpha1.BMCSecretUsernameKeyName: []byte("foo"),
				metalv1alpha1.BMCSecretPasswordKeyName: []byte("bar"),
			},
		}
		Expect(k8sClient.Create(ctx, bmcSecret)).To(Succeed())

		By("Creating a BMC resource")
		bmcObj = &metalv1alpha1.BMC{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-bmc-",
			},
			Spec: metalv1alpha1.BMCSpec{
				Endpoint: &metalv1alpha1.InlineEndpoint{
					IP:         metalv1alpha1.MustParseIP(MockServerIP),
					MACAddress: "23:11:8A:33:CF:EB",
				},
				Protocol: metalv1alpha1.Protocol{
					Name: metalv1alpha1.ProtocolRedfishLocal,
					Port: MockServerPort,
				},
				BMCSecretRef: v1.LocalObjectReference{
					Name: bmcSecret.Name,
				},
			},
		}
		Expect(k8sClient.Create(ctx, bmcObj)).To(Succeed())

		By("Ensuring that the Server resource will be created")
		server = &metalv1alpha1.Server{
			ObjectMeta: metav1.ObjectMeta{
				Name: bmcutils.GetServerNameFromBMCandIndex(0, bmcObj),
			},
			Spec: metalv1alpha1.ServerSpec{
				BMCRef: &v1.LocalObjectReference{Name: bmcObj.Name},
			},
		}
		Expect(k8sClient.Create(ctx, server)).To(Succeed())

		By("Ensuring that the Server is in an available state")
		Eventually(UpdateStatus(server, func() {
			server.Status.State = metalv1alpha1.ServerStateAvailable
			server.Status.PowerState = metalv1alpha1.ServerOffPowerState
		})).Should(Succeed())

		Eventually(UpdateStatus(bmcObj, func() {
			bmcObj.Status.State = metalv1alpha1.BMCStateEnabled
		})).Should(Succeed())

		// Seed an initial ETag so PATCH responses return a real ETag for the controller to capture.
		mockServers[0].SetResourceETag(bmcSettingsURI, `W/"v0"`)
	})

	AfterEach(func(ctx SpecContext) {
		Expect(k8sClient.Delete(ctx, bmcObj)).To(Succeed())
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, server))).To(Succeed())
		Expect(k8sClient.Delete(ctx, bmcSecret)).To(Succeed())
		EnsureCleanState()
		mockServers[0].ResetBMCSettings("BMC")
	})

	newSettings := func(name string, setting map[string]string) *baseboardv1alpha1.BMCSettings {
		return &baseboardv1alpha1.BMCSettings{
			ObjectMeta: metav1.ObjectMeta{GenerateName: name},
			Spec: baseboardv1alpha1.BMCSettingsSpec{
				BMCRef: &v1.LocalObjectReference{Name: bmcObj.Name},
				BMCSettingsTemplate: baseboardv1alpha1.BMCSettingsTemplate{
					SettingsTemplate: api.SettingsTemplate{
						Version:                 "1.45.455b66-rev4",
						SettingsFlow:            []api.SettingsFlowItem{{Name: "flow1", Priority: 1, Settings: setting}},
						ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
					},
				},
			},
		}
	}

	// waitForSettled waits until the mock BMC reflects the expected value, guarding against async settle races.
	waitForSettled := func(key, want string) {
		Eventually(func() any {
			return mockServers[0].GetBMCSettingAttr("BMC")[key]
		}).Should(Equal(want))
		Consistently(func() any {
			return mockServers[0].GetBMCSettingAttr("BMC")[key]
		}, "300ms").Should(Equal(want))
	}

	It("records URI, ETag, and value hash in status.appliedETags after a successful apply", func(ctx SpecContext) {
		settings := newSettings("test-etag-apply-", map[string]string{"abc": "etag-test-value"})
		Expect(k8sClient.Create(ctx, settings)).To(Succeed())

		Eventually(Object(settings)).Should(HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied))

		Eventually(Object(settings)).Should(HaveField("Status.AppliedETags", HaveKey("abc")))
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(settings), settings)).To(Succeed())
		entry := settings.Status.AppliedETags["abc"]
		Expect(entry.URI).To(Equal(bmcSettingsURI))
		Expect(entry.ETag).NotTo(BeEmpty())
		Expect(entry.ValueHash).To(Equal(hmacValueHash(testHMACKey, "etag-test-value")))

		Expect(k8sClient.Delete(ctx, settings)).To(Succeed())
	})

	It("fast-path: does not correct out-of-band drift when the ETag is unchanged (documented Conservative-style limitation for the readable path)", func(ctx SpecContext) {
		settings := newSettings("test-etag-fastpath-", map[string]string{"abc": "fastpath-value"})
		Expect(k8sClient.Create(ctx, settings)).To(Succeed())
		Eventually(Object(settings)).Should(HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied))
		Eventually(Object(settings)).Should(HaveField("Status.AppliedETags", HaveKey("abc")))
		waitForSettled("abc", "fastpath-value")

		By("Silently drifting the BMC attribute without bumping the ETag")
		mockServers[0].SetBMCSettingAttr("BMC", "abc", "drifted-without-etag-bump")

		By("Ensuring the controller does not notice or correct the drift (ETag unchanged -> fast-path skip)")
		Consistently(func() any {
			return mockServers[0].GetBMCSettingAttr("BMC")["abc"]
		}).Should(Equal("drifted-without-etag-bump"))
		Consistently(Object(settings)).Should(HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied))

		Expect(k8sClient.Delete(ctx, settings)).To(Succeed())
	})

	It("O4: when the ETag changes but the managed value is still correct, refreshes the stored ETag without re-applying", func(ctx SpecContext) {
		settings := newSettings("test-etag-refresh-", map[string]string{"abc": "refresh-value"})
		Expect(k8sClient.Create(ctx, settings)).To(Succeed())
		Eventually(Object(settings)).Should(HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied))
		Eventually(Object(settings)).Should(HaveField("Status.AppliedETags", HaveKey("abc")))
		waitForSettled("abc", "refresh-value")
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(settings), settings)).To(Succeed())
		originalETag := settings.Status.AppliedETags["abc"].ETag

		By("Bumping the resource ETag without changing the managed value (e.g. an unrelated key changed)")
		bumped := mockServers[0].GetResourceETag(bmcSettingsURI)
		Expect(bumped).To(Equal(originalETag))
		mockServers[0].SetResourceETag(bmcSettingsURI, `W/"external-bump-1"`)

		By("Nudging the BMCSettings object so a reconcile re-checks drift")
		Eventually(Update(settings, func() {
			metav1.SetMetaDataAnnotation(&settings.ObjectMeta, "test.metal.ironcore.dev/nudge", fmt.Sprintf("%d", time.Now().UnixNano()))
		})).Should(Succeed())

		By("Ensuring the controller refreshes the stored ETag and does not re-apply")
		Eventually(func() string {
			current := &baseboardv1alpha1.BMCSettings{}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(settings), current); err != nil {
				return ""
			}
			return current.Status.AppliedETags["abc"].ETag
		}).Should(Equal(`W/"external-bump-1"`))
		Consistently(Object(settings)).Should(HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied))
		Expect(mockServers[0].GetBMCSettingAttr("BMC")["abc"]).To(Equal("refresh-value"))

		Expect(k8sClient.Delete(ctx, settings)).To(Succeed())
	})

	It("re-applies when the ETag changed and the managed value actually drifted", func(ctx SpecContext) {
		settings := newSettings("test-etag-drift-", map[string]string{"abc": "drift-value"})
		Expect(k8sClient.Create(ctx, settings)).To(Succeed())
		Eventually(Object(settings)).Should(HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied))
		Eventually(Object(settings)).Should(HaveField("Status.AppliedETags", HaveKey("abc")))
		waitForSettled("abc", "drift-value")

		By("Drifting the value and bumping the ETag, simulating an external change to the managed key")
		mockServers[0].SetBMCSettingAttr("BMC", "abc", "someone-elses-value")
		mockServers[0].SetResourceETag(bmcSettingsURI, `W/"external-bump-2"`)

		By("Nudging the BMCSettings object so a reconcile re-checks drift")
		Eventually(Update(settings, func() {
			metav1.SetMetaDataAnnotation(&settings.ObjectMeta, "test.metal.ironcore.dev/nudge", fmt.Sprintf("%d", time.Now().UnixNano()))
		})).Should(Succeed())

		By("Ensuring the controller detects the drift and re-applies the desired value")
		Eventually(func() any {
			return mockServers[0].GetBMCSettingAttr("BMC")["abc"]
		}).Should(Equal("drift-value"))
		Eventually(Object(settings)).Should(HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied))

		Expect(k8sClient.Delete(ctx, settings)).To(Succeed())
	})
})
