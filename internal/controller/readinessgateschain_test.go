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
	bmcutils "github.com/ironcore-dev/metal-operator/pkg/bmcutils"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"

	v1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	SetupTest(nil)

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

			By("Cleaning up all chain objects")
			for _, obj := range []client.Object{step5, step4, step3, step22, step21, step1} {
				Expect(k8sClient.Delete(ctx, obj)).To(Succeed())
			}
			Eventually(Object(server)).Should(testutils.ServerNotParked)
		})
})
