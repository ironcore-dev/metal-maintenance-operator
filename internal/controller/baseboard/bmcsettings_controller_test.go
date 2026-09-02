// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package baseboard

import (
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
	bmcutils "github.com/ironcore-dev/metal-operator/pkg/bmcutils"
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
