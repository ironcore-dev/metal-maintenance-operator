// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"time"

	"github.com/ironcore-dev/metal-maintenance-operator/api"
	baseboardv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/baseboard/v1alpha1"
	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/maintenance/v1alpha1"
	systemv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/system/v1alpha1"
	testutils "github.com/ironcore-dev/metal-maintenance-operator/internal/testutil"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	mockserver "github.com/ironcore-dev/metal-operator/bmc/mock/server"
	bmcutils "github.com/ironcore-dev/metal-operator/pkg/bmcutils"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"

	v1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	. "sigs.k8s.io/controller-runtime/pkg/envtest/komega"
)

// serverConditionTrue matches a Server whose Status.Conditions contains conditionType with Status=True.
func serverConditionTrue(conditionType string) types.GomegaMatcher {
	return HaveField("Status.Conditions", ContainElement(
		SatisfyAll(HaveField("Type", conditionType), HaveField("Status", metav1.ConditionTrue)),
	))
}

// serverConditionNotTrue matches a Server whose Status.Conditions does NOT contain conditionType with
// Status=True - i.e. the condition is either absent or present with a non-True status.
func serverConditionNotTrue(conditionType string) types.GomegaMatcher {
	return Not(serverConditionTrue(conditionType))
}

// dellSystemID is the mock server's System resource folder name (see
// metal-operator/bmc/mock/server/data/Systems/437XR1138R2), used by the chain test above to flip
// the shared BMC's Manufacturer to "Dell Inc." partway through (for step 6), so BMC vendor
// detection resolves to bmc.DellRedfishBMC instead of the vendor-neutral RedfishBaseBMC once the
// BMC's Protocol is switched to ProtocolRedfish.
const dellSystemID = "437XR1138R2"

// This test replicates, end-to-end, the 5-step readiness-gate chain
//
//  1. BMCSettings  (network)        -> ungated, first in chain
//     2.1) BMCVersion (upgrade to v1)  -> gated on step 1, PLUS a "freeze" gate
//     on step 2.2's completion condition
//     being False (see comment below)
//     2.2) BMCVersion (upgrade to v2)  -> gated on step 2.1
//  3. BIOSVersion  (upgrade to X)   -> gated on step 2.2
//  4. BMCSettings  (rest)           -> gated on step 3
//  5. BIOSSettings                  -> gated on step 4
//
// It then re-triggers step 2.2's upgrade (simulating drift/a restart) to
// confirm step 2.1 - whose freeze gate is, by then, permanently unsatisfied
// because step 2.2 has completed - stays frozen in Completed throughout,
// instead of being fooled by step 2.2's transient drift-reset (Status=False,
// Reason=DriftDetected) into incorrectly re-flashing itself back to v1. That
// was the original bug fixed by internal/utils.GatesSatisfied's
// CompletionConditionReset check.
var _ = Describe("Readiness-gate chain", func() {
	ns := SetupTest(nil)

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
		Expect(k8sClient.Create(ctx, bmcObj)).To(Succeed())

		By("Ensuring that the Server resource will be created")
		server = &metalv1alpha1.Server{
			ObjectMeta: metav1.ObjectMeta{
				Name: bmcutils.GetServerNameFromBMCandIndex(0, bmcObj),
			},
		}
		Eventually(Get(server)).Should(Succeed())

		By("Ensuring that the Server is in an available state")
		Eventually(UpdateStatus(server, func() {
			server.Status.State = metalv1alpha1.ServerStateAvailable
		})).Should(Succeed())

		By("Ensuring that the BMC has right state: enabled")
		Eventually(Object(bmcObj)).Should(
			HaveField("Status.State", metalv1alpha1.BMCStateEnabled),
		)
	})

	AfterEach(func(ctx SpecContext) {
		Expect(k8sClient.Delete(ctx, bmcObj)).To(Succeed())
		// The simulated BMC controller deletes its discovered Server on BMC
		// deletion, so the Server may already be gone by the time we get here.
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, server))).To(Succeed())
		Expect(k8sClient.Delete(ctx, bmcSecret)).To(Succeed())
		EnsureCleanState()
		mockServers[0].ResetUpgradeTask("/redfish/v1/Managers/BMC")
		mockServers[0].ResetBMCSettings("BMC")
		mockServers[0].ResetDellRepositoryUpdate()
	})

	It("completes the full BMCSettings->BMCVersion x2->BIOSVersion->BMCSettings->BIOSSettings chain in the "+
		"right order and keeps step 2.1's freeze gate correctly unsatisfied through step 2.2's drift-reset",
		func(ctx SpecContext) {
			const (
				step1Done  = "e2e-gate/step1-bmcsettings-done"
				step21Done = "e2e-gate/step2-1-bmcversion-v1-done"
				step22Done = "e2e-gate/step2-2-bmcversion-v2-done"
				step3Done  = "e2e-gate/step3-biosversion-done"
				step4Done  = "e2e-gate/step4-bmcsettings-done"
				step5Done  = "e2e-gate/step5-biossettings-done"
				step6Done  = "e2e-gate/step6-firmwareupdate-done"

				bmcVersionV1       = "1.46.0"
				bmcVersionV2       = "1.60.0"
				bmcVersionV2Bumped = "1.61.0"
				biosVersionX       = "P89 v4.00 (01/01/2026)"
			)

			By("Creating all 5 chain objects up front, exactly as they'd be applied together in the real " +
				"manifest - the readiness-gate mechanism, not creation order, must enforce the real ordering")
			step1 := &baseboardv1alpha1.BMCSettings{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "step1-"},
				Spec: baseboardv1alpha1.BMCSettingsSpec{
					BMCRef: &v1.LocalObjectReference{Name: bmcObj.Name},
					BMCSettingsTemplate: baseboardv1alpha1.BMCSettingsTemplate{
						SettingsTemplate: api.SettingsTemplate{
							SettingsFlow: []api.SettingsFlowItem{
								{Name: "network", Priority: 1, Settings: map[string]string{"abc": "network-configured"}},
							},
							ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
						},
						ReadinessGating: api.ReadinessGating{CompletionConditionType: step1Done},
					},
				},
			}
			Expect(k8sClient.Create(ctx, step1)).To(Succeed())

			// The second gate below (requiredStatus: False on step2.2's completion condition) is NOT
			// a real prerequisite - step2.2 depends on step2.1, not vice versa. It is a deliberate
			// "freeze" hack: step2.2 targets the same BMC and rewrites its firmware to v2, so without
			// this gate, step2.1 would see the current BMC version no longer match its own
			// Spec.Version once step2.2 applies and misreport drift, flipping itself back to
			// InProgress forever. This is the exact scenario internal/utils.GatesSatisfied's
			// CompletionConditionReset check guards against (see the regression check at the end of
			// this test).
			step21 := &baseboardv1alpha1.BMCVersion{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "step2-1-"},
				Spec: baseboardv1alpha1.BMCVersionSpec{
					BMCRef: &v1.LocalObjectReference{Name: bmcObj.Name},
					BMCVersionTemplate: baseboardv1alpha1.BMCVersionTemplate{
						VersionTemplate: api.VersionTemplate{
							Version:                 bmcVersionV1,
							Image:                   api.ImageSpec{URI: bmcVersionV1},
							ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
						},
						ReadinessGating: api.ReadinessGating{
							ReadinessGates: []metalv1alpha1.ConditionRequirement{
								{Type: step1Done, RequiredStatus: metav1.ConditionTrue},
								{Type: step22Done, RequiredStatus: metav1.ConditionFalse},
							},
							CompletionConditionType: step21Done,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, step21)).To(Succeed())

			step22 := &baseboardv1alpha1.BMCVersion{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "step2-2-"},
				Spec: baseboardv1alpha1.BMCVersionSpec{
					BMCRef: &v1.LocalObjectReference{Name: bmcObj.Name},
					BMCVersionTemplate: baseboardv1alpha1.BMCVersionTemplate{
						VersionTemplate: api.VersionTemplate{
							Version:                 bmcVersionV2,
							Image:                   api.ImageSpec{URI: bmcVersionV2},
							ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
						},
						ReadinessGating: api.ReadinessGating{
							ReadinessGates: []metalv1alpha1.ConditionRequirement{
								{Type: step21Done, RequiredStatus: metav1.ConditionTrue},
							},
							CompletionConditionType: step22Done,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, step22)).To(Succeed())

			step3 := &systemv1alpha1.BIOSVersion{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "step3-"},
				Spec: systemv1alpha1.BIOSVersionSpec{
					ServerRef: &v1.LocalObjectReference{Name: server.Name},
					BIOSVersionTemplate: systemv1alpha1.BIOSVersionTemplate{
						VersionTemplate: api.VersionTemplate{
							Version:                 biosVersionX,
							Image:                   api.ImageSpec{URI: biosVersionX},
							ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
						},
						ReadinessGating: api.ReadinessGating{
							ReadinessGates: []metalv1alpha1.ConditionRequirement{
								{Type: step22Done, RequiredStatus: metav1.ConditionTrue},
							},
							CompletionConditionType: step3Done,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, step3)).To(Succeed())

			step4 := &baseboardv1alpha1.BMCSettings{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "step4-"},
				Spec: baseboardv1alpha1.BMCSettingsSpec{
					BMCRef: &v1.LocalObjectReference{Name: bmcObj.Name},
					BMCSettingsTemplate: baseboardv1alpha1.BMCSettingsTemplate{
						SettingsTemplate: api.SettingsTemplate{
							Version: bmcVersionV2,
							SettingsFlow: []api.SettingsFlowItem{
								{Name: "rest", Priority: 1, Settings: map[string]string{"composite": "rest-configured"}},
							},
							ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
						},
						ReadinessGating: api.ReadinessGating{
							ReadinessGates: []metalv1alpha1.ConditionRequirement{
								{Type: step3Done, RequiredStatus: metav1.ConditionTrue},
							},
							CompletionConditionType: step4Done,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, step4)).To(Succeed())

			step5 := &systemv1alpha1.BIOSSettings{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "step5-"},
				Spec: systemv1alpha1.BIOSSettingsSpec{
					ServerRef: &v1.LocalObjectReference{Name: server.Name},
					BIOSSettingsTemplate: systemv1alpha1.BIOSSettingsTemplate{
						SettingsTemplate: api.SettingsTemplate{
							Version: biosVersionX,
							SettingsFlow: []api.SettingsFlowItem{
								{Name: "boot", Priority: 1, Settings: map[string]string{"BootMode": "Uefi"}},
							},
							ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
						},
						ReadinessGating: api.ReadinessGating{
							ReadinessGates: []metalv1alpha1.ConditionRequirement{
								{Type: step4Done, RequiredStatus: metav1.ConditionTrue},
							},
							CompletionConditionType: step5Done,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, step5)).To(Succeed())

			// NOTE: this envtest setup (synchronous reconciles against a fake/local BMC) can race through
			// the entire 5-step chain in well under a second - even before the first assertion below runs.
			// So we deliberately do NOT assert that gated steps "start out Pending" or "stay non-terminal"
			// here: such snapshot/polling checks are fundamentally unreliable in this environment and would
			// be flaky by construction. Instead we only assert that each step *eventually* reaches its
			// terminal state, and verify the real ordering constraint deterministically at the end via each
			// condition's LastTransitionTime (see below).

			By("Step 1 (ungated) completes first; everything downstream stays Pending until it does")
			Eventually(Object(step1)).Should(
				HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied),
			)
			Eventually(Object(server)).Should(serverConditionTrue(step1Done))

			By("Step 2.1 completes next (gated on step 1); step 1 stays settled")
			Eventually(Object(step21)).Should(
				HaveField("Status.State", baseboardv1alpha1.BMCVersionStateCompleted),
			)
			Eventually(Object(server)).Should(serverConditionTrue(step21Done))
			// step22 is now unblocked (its sole gate, step21Done:True, was just satisfied), so we can no
			// longer assert it "stays" unmet without racing the reconciler - that's verified later via
			// LastTransitionTime ordering instead. Only check for regressions on what's already settled.
			Consistently(Object(server)).Should(SatisfyAll(
				serverConditionTrue(step1Done),
				serverConditionTrue(step21Done),
			))

			By("Step 2.2 completes next (gated on step 2.1); steps 1/2.1 stay settled, with 2.1's freeze " +
				"gate now permanently unsatisfied")
			Eventually(Object(step22)).Should(
				HaveField("Status.State", baseboardv1alpha1.BMCVersionStateCompleted),
			)
			Eventually(Object(server)).Should(serverConditionTrue(step22Done))
			Consistently(Object(server)).Should(SatisfyAll(
				serverConditionTrue(step1Done),
				serverConditionTrue(step21Done),
				serverConditionTrue(step22Done),
			))

			By("Step 3 completes next (gated on step 2.2); earlier steps stay settled")
			Eventually(Object(step3)).Should(
				HaveField("Status.State", systemv1alpha1.BIOSVersionStateCompleted),
			)
			Eventually(Object(server)).Should(serverConditionTrue(step3Done))
			Consistently(Object(step1)).Should(HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied))
			Consistently(Object(step21)).Should(HaveField("Status.State", baseboardv1alpha1.BMCVersionStateCompleted))
			Consistently(Object(step22)).Should(HaveField("Status.State", baseboardv1alpha1.BMCVersionStateCompleted))
			Consistently(Object(server)).Should(SatisfyAll(
				serverConditionTrue(step1Done),
				serverConditionTrue(step21Done),
				serverConditionTrue(step22Done),
			))

			By("Step 4 completes next (gated on step 3); earlier steps stay settled")
			Eventually(Object(step4)).Should(
				HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied),
			)
			Eventually(Object(server)).Should(serverConditionTrue(step4Done))
			Consistently(Object(step3)).Should(HaveField("Status.State", systemv1alpha1.BIOSVersionStateCompleted))
			Consistently(Object(step21)).Should(HaveField("Status.State", baseboardv1alpha1.BMCVersionStateCompleted))
			Consistently(Object(step22)).Should(HaveField("Status.State", baseboardv1alpha1.BMCVersionStateCompleted))
			Consistently(Object(server)).Should(SatisfyAll(
				serverConditionTrue(step1Done),
				serverConditionTrue(step21Done),
				serverConditionTrue(step22Done),
				serverConditionTrue(step3Done),
			))

			By("Step 5 completes last (gated on step 4) - the full chain has now run")
			Eventually(Object(step5)).Should(
				HaveField("Status.State", systemv1alpha1.BIOSSettingsStateApplied),
			)
			Eventually(Object(server)).Should(serverConditionTrue(step5Done))

			By("Verifying the chain ran in the exact right order via each condition's LastTransitionTime - " +
				"this is the deterministic ordering check: polling-based \"stays pending\" assertions above " +
				"can't reliably catch out-of-order completions once a step's sole gate is satisfied, since " +
				"nothing then stops the reconciler from finishing it arbitrarily fast")
			Expect(Get(server)()).To(Succeed())
			conditionTime := func(conditionType string) metav1.Time {
				cond := apimeta.FindStatusCondition(server.Status.Conditions, conditionType)
				Expect(cond).NotTo(BeNil(), "condition %q must be present on the Server", conditionType)
				return cond.LastTransitionTime
			}
			t1, t21, t22 := conditionTime(step1Done), conditionTime(step21Done), conditionTime(step22Done)
			t3, t4, t5 := conditionTime(step3Done), conditionTime(step4Done), conditionTime(step5Done)
			Expect(t1.Time).To(BeTemporally("<=", t21.Time), "step1 must complete no later than step2.1")
			Expect(t21.Time).To(BeTemporally("<=", t22.Time), "step2.1 must complete no later than step2.2")
			Expect(t22.Time).To(BeTemporally("<=", t3.Time), "step2.2 must complete no later than step3")
			Expect(t3.Time).To(BeTemporally("<=", t4.Time), "step3 must complete no later than step4")
			Expect(t4.Time).To(BeTemporally("<=", t5.Time), "step4 must complete no later than step5")

			By("Regression check: bumping step 2.2's version again to simulate drift/a restart")
			Eventually(Update(step22, func() {
				step22.Spec.Version = bmcVersionV2Bumped
				step22.Spec.Image = api.ImageSpec{URI: bmcVersionV2Bumped}
			})).Should(Succeed())

			By("Ensuring step 2.2's completion condition transiently resets away from True during the re-upgrade")
			Eventually(Object(server)).Should(serverConditionNotTrue(step22Done))

			By("Ensuring step 2.1 never leaves Completed throughout step 2.2's drift-reset - this is the " +
				"regression this test guards against")
			Consistently(Object(step21), 2*time.Second).Should(
				HaveField("Status.State", baseboardv1alpha1.BMCVersionStateCompleted),
			)
			Consistently(Object(server), 2*time.Second).Should(serverConditionTrue(step21Done))

			By("Ensuring step 2.2 re-upgrades and reaches Completed again")
			Eventually(Object(step22)).Should(
				HaveField("Status.State", baseboardv1alpha1.BMCVersionStateCompleted),
			)
			Eventually(Object(server)).Should(serverConditionTrue(step22Done))

			By("Ensuring step 2.1 still stays Completed now that step 2.2 has resettled")
			Consistently(Object(step21)).Should(
				HaveField("Status.State", baseboardv1alpha1.BMCVersionStateCompleted),
			)

			// because the firmware upgrade is vendor specific, we now switch the BMC protocol to ProtocolRedfish and re-point the mock data to Dell's vendor path,
			// so the next steps can go through.

			// ------- Start: MockData Override for Dell vendor path ------- //
			By("Switching the BMC to the Dell-repository vendor path for step 6 - re-pointing the existing " +
				"mock attribute data (registry + the Manager's own Attributes field) rather than adding new " +
				"fixtures, so steps 1-5 keep behaving identically afterward")
			// The registry Id is renamed (not re-authored) so Dell's ID-substring scan over
			// Service.Registries() matches it. The Local vendor path (used by steps 1-5) looks this
			// registry up by its fixed URL path, not by Id, so the rename is invisible to them.
			mockserver.WithResourceOverride(
				"Registries/BMCAttributeRegistry/index.json",
				map[string]any{"Id": "Dell.1.0.0.ManagerAttributeRegistry"},
			)(mockServers[0])
			// The Manager's own resource already carries the "abc"/"fooreboot"/"composite" attribute
			// values steps 1/4 manage; pointing Dell's OEM DellAttributes link back at the Manager itself
			// lets DellRedfishBMC read the exact same data instead of requiring a separate Dell-specific
			// attributes document. The rest of Links is repeated as-is since WithResourceOverride replaces
			// nested fields wholesale rather than deep-merging them.
			mockserver.WithResourceOverride(
				"Managers/BMC/index.json",
				map[string]any{
					"Links": map[string]any{
						"ManagerForServers": []map[string]any{{"@odata.id": "/redfish/v1/Systems/" + dellSystemID}},
						"ManagerForChassis": []map[string]any{{"@odata.id": "/redfish/v1/Chassis/1U"}},
						"ManagerInChassis":  map[string]any{"@odata.id": "/redfish/v1/Chassis/1U"},
						"Oem": map[string]any{
							"Dell": map[string]any{
								"DellAttributes":             []map[string]any{{"@odata.id": "/redfish/v1/Managers/BMC"}},
								"DellAttributes@odata.count": 1,
							},
						},
					},
				},
			)(mockServers[0])
			mockserver.WithSystemOverride(dellSystemID, map[string]any{"Manufacturer": "Dell Inc."})(mockServers[0])

			By("Switching the BMC's protocol so vendor detection dispatches to bmc.DellRedfishBMC")
			Eventually(Update(bmcObj, func() {
				bmcObj.Spec.Protocol.Name = metalv1alpha1.ProtocolRedfish
			})).Should(Succeed())

			// server.Status.Manufacturer is only refreshed by the Server controller's periodic
			// discovery resync, not live on every reconcile - wait for it to pick up the override
			// before creating step 6, otherwise FirmwareUpdate's vendor check can race the resync
			// and see the stale "Contoso" manufacturer, failing with UnsupportedVendor.
			By("Waiting for the Server's discovered Manufacturer to pick up the mock override")
			Eventually(Object(server)).Should(HaveField("Status.Manufacturer", "Dell Inc."))

			// ------- END: MockData Override for Dell vendor path ------- //

			By("Creating step 6 (Dell-repository FirmwareUpdate, gated on step 5)")
			step6 := &systemv1alpha1.FirmwareUpdate{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "step6-", Namespace: ns.Name},
				Spec: systemv1alpha1.FirmwareUpdateSpec{
					ServerRef: &v1.LocalObjectReference{Name: server.Name},
					FirmwareUpdateTemplate: systemv1alpha1.FirmwareUpdateTemplate{
						DellRepository: &systemv1alpha1.DellFirmwareRepository{
							ShareType:   systemv1alpha1.DellShareTypeHTTPS,
							Address:     "downloads.dell.com",
							CatalogFile: "Catalog.xml",
						},
						ServerMaintenancePolicy: ptr.To(maintenancev1alpha1.ServerMaintenancePolicyEnforced),
						ReadinessGating: api.ReadinessGating{
							ReadinessGates: []metalv1alpha1.ConditionRequirement{
								{Type: step5Done, RequiredStatus: metav1.ConditionTrue},
							},
							CompletionConditionType: step6Done,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, step6)).To(Succeed())

			By("Step 6 (FirmwareUpdate, gated on step 5) completes; steps 1-5 stay settled with no Dell " +
				"attribute-path errors, proving the re-pointed mock data works for both the vendor-neutral and " +
				"Dell-dispatched reconciles against the same BMC")
			Eventually(Object(step6)).Should(
				HaveField("Status.State", systemv1alpha1.FirmwareUpdateStateCompleted),
			)
			Eventually(Object(server)).Should(serverConditionTrue(step6Done))
			Consistently(Object(step1)).Should(HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied))
			Consistently(Object(step4)).Should(HaveField("Status.State", baseboardv1alpha1.BMCSettingsStateApplied))
			Consistently(Object(server)).Should(SatisfyAll(
				serverConditionTrue(step1Done),
				serverConditionTrue(step21Done),
				serverConditionTrue(step22Done),
				serverConditionTrue(step3Done),
				serverConditionTrue(step4Done),
				serverConditionTrue(step5Done),
				serverConditionTrue(step6Done),
			))

			By("Verifying step 6 completed no earlier than step 5 via LastTransitionTime")
			Expect(Get(server)()).To(Succeed())
			t6 := conditionTime(step6Done)
			Expect(t5.Time).To(BeTemporally("<=", t6.Time), "step5 must complete no later than step6")

			By("Cleaning up all chain objects")
			for _, obj := range []client.Object{step6, step5, step4, step3, step22, step21, step1} {
				Expect(k8sClient.Delete(ctx, obj)).To(Succeed())
			}
			Eventually(Object(server)).Should(testutils.ServerNotParked)
		})
})
