// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "sigs.k8s.io/controller-runtime/pkg/envtest/komega"

	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/maintenance/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// makeRun builds a minimal MaintenancePlanRun for a single server behind a BMC.
func makeRun(name, bmcName, serverName, baselineBMC, baselineBIOS string, stages []maintenancev1alpha1.PlanStage) *maintenancev1alpha1.MaintenancePlanRun {
	biosVersions := map[string]string{}
	if baselineBIOS != "" {
		biosVersions[serverName] = baselineBIOS
	}
	return &maintenancev1alpha1.MaintenancePlanRun{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: maintenancev1alpha1.MaintenancePlanRunSpec{
			PlanRef:              corev1.LocalObjectReference{Name: "test-plan"},
			BMCRef:               corev1.LocalObjectReference{Name: bmcName},
			ServerRefs:           []corev1.LocalObjectReference{{Name: serverName}},
			BaselineBMCVersion:   baselineBMC,
			BaselineBIOSVersions: biosVersions,
			Stages:               stages,
		},
	}
}

// makeMultiServerRun builds a run with multiple servers sharing one BMC.
func makeMultiServerRun(name, bmcName string, servers []string, biosVersions map[string]string, stages []maintenancev1alpha1.PlanStage) *maintenancev1alpha1.MaintenancePlanRun {
	refs := make([]corev1.LocalObjectReference, len(servers))
	for i, s := range servers {
		refs[i] = corev1.LocalObjectReference{Name: s}
	}
	return &maintenancev1alpha1.MaintenancePlanRun{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: maintenancev1alpha1.MaintenancePlanRunSpec{
			PlanRef:              corev1.LocalObjectReference{Name: "test-plan"},
			BMCRef:               corev1.LocalObjectReference{Name: bmcName},
			ServerRefs:           refs,
			BaselineBMCVersion:   "",
			BaselineBIOSVersions: biosVersions,
			Stages:               stages,
		},
	}
}

var _ = Describe("MaintenancePlanRun Controller", func() {
	SetupMaintenanceTest()

	ctx := context.Background()

	Describe("Phase initialisation", func() {
		It("transitions from empty phase to Running", func() {
			run := makeRun("run-init", "bmc-x", "srv-x", "", "",
				[]maintenancev1alpha1.PlanStage{minimalBMCVersionStage("bmc-fw", "7.10.00")})
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			Eventually(Object(run)).Should(
				HaveField("Status.Phase", Equal(maintenancev1alpha1.MaintenancePlanRunPhaseRunning)),
			)
		})

		It("initialises one StageStatus entry per stage", func() {
			run := makeRun("run-stages-init", "bmc-x", "srv-x", "", "", []maintenancev1alpha1.PlanStage{
				minimalBMCVersionStage("stage-a", "7.10.00"),
				minimalBMCVersionStage("stage-b", "7.20.00"),
			})
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: run.Name}, run)).To(Succeed())
				g.Expect(run.Status.StageStatuses).To(HaveLen(2))
				g.Expect(run.Status.StageStatuses[0].Name).To(Equal("stage-a"))
				g.Expect(run.Status.StageStatuses[1].Name).To(Equal("stage-b"))
			}).Should(Succeed())
		})
	})

	Describe("BMC-scoped stage — version-aware skip", func() {
		It("skips when target <= baseline", func() {
			run := makeRun("run-bmc-skip", "bmc-x", "srv-x", "7.10.00", "",
				[]maintenancev1alpha1.PlanStage{minimalBMCVersionStage("bmc-fw", "7.05.00")})
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: run.Name}, run)).To(Succeed())
				g.Expect(run.Status.StageStatuses).NotTo(BeEmpty())
				g.Expect(run.Status.StageStatuses[0].Phase).To(Equal(maintenancev1alpha1.StagePhaseSkipped))
			}).Should(Succeed())
		})

		It("does not skip when target > baseline", func() {
			run := makeRun("run-bmc-noskip", "bmc-x", "srv-x", "7.00.00", "",
				[]maintenancev1alpha1.PlanStage{minimalBMCVersionStage("bmc-fw", "7.10.00")})
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: run.Name}, run)).To(Succeed())
				g.Expect(run.Status.StageStatuses).NotTo(BeEmpty())
				g.Expect(run.Status.StageStatuses[0].Phase).NotTo(Equal(maintenancev1alpha1.StagePhaseSkipped))
			}).Should(Succeed())
		})

		It("succeeds immediately when all stages are skipped", func() {
			run := makeRun("run-all-skip", "bmc-x", "srv-x", "9.99.99", "",
				[]maintenancev1alpha1.PlanStage{
					minimalBMCVersionStage("bmc-fw-1", "7.00.00"),
					minimalBMCVersionStage("bmc-fw-2", "7.10.00"),
				},
			)
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			Eventually(Object(run)).Should(
				HaveField("Status.Phase", Equal(maintenancev1alpha1.MaintenancePlanRunPhaseSucceeded)),
			)
		})
	})

	Describe("BMC-scoped stage — leaf CR creation and polling", func() {
		It("creates a BMCVersion leaf CR keyed by run+stage", func() {
			run := makeRun("run-leaf-bmc", "bmc-leaf", "srv-leaf", "7.00.00", "",
				[]maintenancev1alpha1.PlanStage{minimalBMCVersionStage("bmc-fw", "7.10.00")})
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			expectedName := bmcCRName(run.Name, "bmc-fw")
			leafCR := &metalv1alpha1.BMCVersion{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: expectedName}, leafCR)).To(Succeed())
			}).Should(Succeed())
			DeferCleanup(k8sClient.Delete, leafCR)

			Expect(leafCR.Spec.Version).To(Equal("7.10.00"))
			Expect(leafCR.Spec.BMCRef.Name).To(Equal("bmc-leaf"))
			Expect(leafCR.Labels[planRunOwnerLabel]).To(Equal(run.Name))
		})

		It("marks stage Succeeded when BMCVersion reaches Completed", func() {
			run := makeRun("run-poll-bmc-ok", "bmc-poll", "srv-poll", "7.00.00", "",
				[]maintenancev1alpha1.PlanStage{minimalBMCVersionStage("bmc-fw", "7.10.00")})
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			leaf := &metalv1alpha1.BMCVersion{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bmcCRName(run.Name, "bmc-fw")}, leaf)).To(Succeed())
			}).Should(Succeed())
			DeferCleanup(k8sClient.Delete, leaf)

			Eventually(UpdateStatus(leaf, func() { leaf.Status.State = metalv1alpha1.BMCVersionStateCompleted })).Should(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: run.Name}, run)).To(Succeed())
				g.Expect(run.Status.StageStatuses[0].Phase).To(Equal(maintenancev1alpha1.StagePhaseSucceeded))
			}).Should(Succeed())
		})

		It("marks run Failed when BMCVersion reaches Failed", func() {
			run := makeRun("run-poll-bmc-fail", "bmc-pfail", "srv-pfail", "7.00.00", "",
				[]maintenancev1alpha1.PlanStage{minimalBMCVersionStage("bmc-fw", "7.10.00")})
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			leaf := &metalv1alpha1.BMCVersion{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bmcCRName(run.Name, "bmc-fw")}, leaf)).To(Succeed())
			}).Should(Succeed())
			DeferCleanup(k8sClient.Delete, leaf)

			Eventually(UpdateStatus(leaf, func() { leaf.Status.State = metalv1alpha1.BMCVersionStateFailed })).Should(Succeed())

			Eventually(Object(run)).Should(HaveField("Status.Phase", Equal(maintenancev1alpha1.MaintenancePlanRunPhaseFailed)))
		})

		It("marks BMCSettings stage Succeeded when Applied", func() {
			stage := maintenancev1alpha1.PlanStage{
				Name: "bmc-pre",
				Kind: maintenancev1alpha1.StageKindBMCSettings,
				Template: maintenancev1alpha1.StageTemplate{
					BMCSettings: &maintenancev1alpha1.PlanBMCSettingsTemplate{Version: "7.10.00"},
				},
			}
			run := makeRun("run-bmcs-ok", "bmc-bsok", "srv-bsok", "7.00.00", "",
				[]maintenancev1alpha1.PlanStage{stage})
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			leaf := &metalv1alpha1.BMCSettings{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bmcCRName(run.Name, "bmc-pre")}, leaf)).To(Succeed())
			}).Should(Succeed())
			DeferCleanup(k8sClient.Delete, leaf)

			Eventually(UpdateStatus(leaf, func() { leaf.Status.State = metalv1alpha1.BMCSettingsStateApplied })).Should(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: run.Name}, run)).To(Succeed())
				g.Expect(run.Status.StageStatuses[0].Phase).To(Equal(maintenancev1alpha1.StagePhaseSucceeded))
			}).Should(Succeed())
		})

		It("marks BMCSettings stage Failed when Failed state", func() {
			stage := maintenancev1alpha1.PlanStage{
				Name:     "bmc-pre-fail",
				Kind:     maintenancev1alpha1.StageKindBMCSettings,
				Template: maintenancev1alpha1.StageTemplate{BMCSettings: &maintenancev1alpha1.PlanBMCSettingsTemplate{Version: "7.10.00"}},
			}
			run := makeRun("run-bmcs-fail", "bmc-bsfail", "srv-bsfail", "7.00.00", "",
				[]maintenancev1alpha1.PlanStage{stage})
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			leaf := &metalv1alpha1.BMCSettings{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bmcCRName(run.Name, "bmc-pre-fail")}, leaf)).To(Succeed())
			}).Should(Succeed())
			DeferCleanup(k8sClient.Delete, leaf)

			Eventually(UpdateStatus(leaf, func() { leaf.Status.State = metalv1alpha1.BMCSettingsStateFailed })).Should(Succeed())

			Eventually(Object(run)).Should(HaveField("Status.Phase", Equal(maintenancev1alpha1.MaintenancePlanRunPhaseFailed)))
		})
	})

	Describe("Server-scoped stage — single server", func() {
		It("creates a BIOSVersion leaf CR keyed by run+stage+server", func() {
			run := makeRun("run-leaf-bios", "bmc-biosleaf", "srv-biosleaf", "", "1.0.0",
				[]maintenancev1alpha1.PlanStage{minimalBIOSVersionStage("2.5.0")})
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			expectedName := serverCRName(run.Name, "bios-fw", "srv-biosleaf")
			leaf := &metalv1alpha1.BIOSVersion{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: expectedName}, leaf)).To(Succeed())
			}).Should(Succeed())
			DeferCleanup(k8sClient.Delete, leaf)

			Expect(leaf.Spec.Version).To(Equal("2.5.0"))
			Expect(leaf.Spec.ServerRef.Name).To(Equal("srv-biosleaf"))
		})

		It("skips BIOSVersion when target <= baseline for that server", func() {
			run := makeRun("run-bios-skip", "bmc-x", "srv-x", "", "2.0.0",
				[]maintenancev1alpha1.PlanStage{minimalBIOSVersionStage("1.0.0")})
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: run.Name}, run)).To(Succeed())
				g.Expect(run.Status.StageStatuses).NotTo(BeEmpty())
				g.Expect(run.Status.StageStatuses[0].Phase).To(Equal(maintenancev1alpha1.StagePhaseSkipped))
			}).Should(Succeed())
		})

		It("marks stage Succeeded when BIOSVersion reaches Completed", func() {
			run := makeRun("run-poll-bios-ok", "bmc-bpoll", "srv-bpoll", "", "1.0.0",
				[]maintenancev1alpha1.PlanStage{minimalBIOSVersionStage("2.5.0")})
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			leafName := serverCRName(run.Name, "bios-fw", "srv-bpoll")
			leaf := &metalv1alpha1.BIOSVersion{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: leafName}, leaf)).To(Succeed())
			}).Should(Succeed())
			DeferCleanup(k8sClient.Delete, leaf)

			Eventually(UpdateStatus(leaf, func() { leaf.Status.State = metalv1alpha1.BIOSVersionStateCompleted })).Should(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: run.Name}, run)).To(Succeed())
				g.Expect(run.Status.StageStatuses[0].Phase).To(Equal(maintenancev1alpha1.StagePhaseSucceeded))
			}).Should(Succeed())
		})

		It("marks stage Failed and run Failed when BIOSVersion reaches Failed", func() {
			run := makeRun("run-poll-bios-fail", "bmc-bpfail", "srv-bpfail", "", "1.0.0",
				[]maintenancev1alpha1.PlanStage{minimalBIOSVersionStage("2.5.0")})
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			leafName := serverCRName(run.Name, "bios-fw", "srv-bpfail")
			leaf := &metalv1alpha1.BIOSVersion{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: leafName}, leaf)).To(Succeed())
			}).Should(Succeed())
			DeferCleanup(k8sClient.Delete, leaf)

			Eventually(UpdateStatus(leaf, func() { leaf.Status.State = metalv1alpha1.BIOSVersionStateFailed })).Should(Succeed())

			Eventually(Object(run)).Should(HaveField("Status.Phase", Equal(maintenancev1alpha1.MaintenancePlanRunPhaseFailed)))
		})

		It("marks BIOSSettings stage Succeeded when Applied", func() {
			stage := maintenancev1alpha1.PlanStage{
				Name: "bios-pre",
				Kind: maintenancev1alpha1.StageKindBIOSSettings,
				Template: maintenancev1alpha1.StageTemplate{
					BIOSSettings: &metalv1alpha1.BIOSSettingsTemplate{
						Version:      "2.5.0",
						SettingsFlow: []metalv1alpha1.SettingsFlowItem{{Name: "g1", Priority: 1}},
					},
				},
			}
			run := makeRun("run-bioss-ok", "bmc-biossok", "srv-biossok", "", "1.0.0",
				[]maintenancev1alpha1.PlanStage{stage})
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			leafName := serverCRName(run.Name, "bios-pre", "srv-biossok")
			leaf := &metalv1alpha1.BIOSSettings{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: leafName}, leaf)).To(Succeed())
			}).Should(Succeed())
			DeferCleanup(k8sClient.Delete, leaf)

			Eventually(UpdateStatus(leaf, func() { leaf.Status.State = metalv1alpha1.BIOSSettingsStateApplied })).Should(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: run.Name}, run)).To(Succeed())
				g.Expect(run.Status.StageStatuses[0].Phase).To(Equal(maintenancev1alpha1.StagePhaseSucceeded))
			}).Should(Succeed())
		})

		It("marks BIOSSettings stage Failed when Failed", func() {
			stage := maintenancev1alpha1.PlanStage{
				Name:     "bios-pre-fail",
				Kind:     maintenancev1alpha1.StageKindBIOSSettings,
				Template: maintenancev1alpha1.StageTemplate{BIOSSettings: &metalv1alpha1.BIOSSettingsTemplate{Version: "2.5.0"}},
			}
			run := makeRun("run-bioss-fail", "bmc-biossfail", "srv-biossfail", "", "1.0.0",
				[]maintenancev1alpha1.PlanStage{stage})
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			leafName := serverCRName(run.Name, "bios-pre-fail", "srv-biossfail")
			leaf := &metalv1alpha1.BIOSSettings{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: leafName}, leaf)).To(Succeed())
			}).Should(Succeed())
			DeferCleanup(k8sClient.Delete, leaf)

			Eventually(UpdateStatus(leaf, func() { leaf.Status.State = metalv1alpha1.BIOSSettingsStateFailed })).Should(Succeed())

			Eventually(Object(run)).Should(HaveField("Status.Phase", Equal(maintenancev1alpha1.MaintenancePlanRunPhaseFailed)))
		})
	})

	Describe("Server-scoped stage — multi-server fan-out", func() {
		It("creates one leaf CR per server", func() {
			run := makeMultiServerRun("run-multi-bios", "bmc-multi", []string{"srv-a", "srv-b"},
				map[string]string{"srv-a": "1.0.0", "srv-b": "1.0.0"},
				[]maintenancev1alpha1.PlanStage{minimalBIOSVersionStage("2.5.0")},
			)
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			for _, srv := range []string{"srv-a", "srv-b"} {
				name := serverCRName(run.Name, "bios-fw", srv)
				leaf := &metalv1alpha1.BIOSVersion{}
				Eventually(func(g Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: name}, leaf)).To(Succeed())
				}).Should(Succeed())
				DeferCleanup(k8sClient.Delete, leaf)
				Expect(leaf.Spec.ServerRef.Name).To(Equal(srv))
			}
		})

		It("skips per-server independently based on that server's baseline", func() {
			// srv-a has old BIOS → will execute; srv-b has new BIOS → will skip
			run := makeMultiServerRun("run-per-server-skip", "bmc-pss", []string{"srv-pss-a", "srv-pss-b"},
				map[string]string{"srv-pss-a": "1.0.0", "srv-pss-b": "9.9.9"},
				[]maintenancev1alpha1.PlanStage{minimalBIOSVersionStage("2.5.0")},
			)
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			// Wait for srv-a leaf to be created (not skipped)
			leafA := &metalv1alpha1.BIOSVersion{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name: serverCRName(run.Name, "bios-fw", "srv-pss-a"),
				}, leafA)).To(Succeed())
			}).Should(Succeed())
			DeferCleanup(k8sClient.Delete, leafA)

			// srv-b must never get a leaf CR (it was skipped)
			Consistently(func(g Gomega) {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name: serverCRName(run.Name, "bios-fw", "srv-pss-b"),
				}, &metalv1alpha1.BIOSVersion{})
				g.Expect(err).To(MatchError(ContainSubstring("not found")))
			}, "2s").Should(Succeed())

			// Check srv-b status entry is Skipped
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: run.Name}, run)).To(Succeed())
				g.Expect(run.Status.StageStatuses).NotTo(BeEmpty())
				srvBStatus := findServerStatus(run.Status.StageStatuses[0], "srv-pss-b")
				g.Expect(srvBStatus).NotTo(BeNil())
				g.Expect(srvBStatus.Phase).To(Equal(maintenancev1alpha1.StagePhaseSkipped))
			}).Should(Succeed())
		})

		It("stage succeeds only after all servers complete", func() {
			run := makeMultiServerRun("run-multi-complete", "bmc-mc", []string{"srv-mc-1", "srv-mc-2"},
				map[string]string{"srv-mc-1": "1.0.0", "srv-mc-2": "1.0.0"},
				[]maintenancev1alpha1.PlanStage{minimalBIOSVersionStage("2.5.0")},
			)
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			// Complete srv-mc-1 first
			leaf1 := &metalv1alpha1.BIOSVersion{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name: serverCRName(run.Name, "bios-fw", "srv-mc-1"),
				}, leaf1)).To(Succeed())
			}).Should(Succeed())
			DeferCleanup(k8sClient.Delete, leaf1)
			Eventually(UpdateStatus(leaf1, func() { leaf1.Status.State = metalv1alpha1.BIOSVersionStateCompleted })).Should(Succeed())

			// Stage must still be Running (srv-mc-2 not done yet)
			Consistently(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: run.Name}, run)).To(Succeed())
				if len(run.Status.StageStatuses) > 0 {
					g.Expect(run.Status.StageStatuses[0].Phase).NotTo(Equal(maintenancev1alpha1.StagePhaseSucceeded))
				}
			}, "2s").Should(Succeed())

			// Now complete srv-mc-2
			leaf2 := &metalv1alpha1.BIOSVersion{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name: serverCRName(run.Name, "bios-fw", "srv-mc-2"),
				}, leaf2)).To(Succeed())
			}).Should(Succeed())
			DeferCleanup(k8sClient.Delete, leaf2)
			Eventually(UpdateStatus(leaf2, func() { leaf2.Status.State = metalv1alpha1.BIOSVersionStateCompleted })).Should(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: run.Name}, run)).To(Succeed())
				g.Expect(run.Status.StageStatuses[0].Phase).To(Equal(maintenancev1alpha1.StagePhaseSucceeded))
			}).Should(Succeed())
		})

		It("stage fails if any server's leaf CR fails", func() {
			run := makeMultiServerRun("run-multi-fail", "bmc-mf", []string{"srv-mf-1", "srv-mf-2"},
				map[string]string{"srv-mf-1": "1.0.0", "srv-mf-2": "1.0.0"},
				[]maintenancev1alpha1.PlanStage{minimalBIOSVersionStage("2.5.0")},
			)
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			leaf1 := &metalv1alpha1.BIOSVersion{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{
					Name: serverCRName(run.Name, "bios-fw", "srv-mf-1"),
				}, leaf1)).To(Succeed())
			}).Should(Succeed())
			DeferCleanup(k8sClient.Delete, leaf1)
			Eventually(UpdateStatus(leaf1, func() { leaf1.Status.State = metalv1alpha1.BIOSVersionStateFailed })).Should(Succeed())

			Eventually(Object(run)).Should(HaveField("Status.Phase", Equal(maintenancev1alpha1.MaintenancePlanRunPhaseFailed)))
		})
	})

	Describe("BMC and BIOS stages intermingled — independent skip", func() {
		It("skips BMC but executes BIOS stages when baselines differ", func() {
			run := makeRun("run-mixed-skip", "bmc-mix", "srv-mix",
				"7.10.00", // BMC already at target
				"1.0.0",   // BIOS below target
				[]maintenancev1alpha1.PlanStage{
					minimalBMCVersionStage("bmc-fw", "7.10.00"),
					minimalBIOSVersionStage("2.5.0"),
				},
			)
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			// BMC stage skipped, BIOS leaf should be created
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: run.Name}, run)).To(Succeed())
				g.Expect(run.Status.StageStatuses).To(HaveLen(2))
				g.Expect(run.Status.StageStatuses[0].Phase).To(Equal(maintenancev1alpha1.StagePhaseSkipped))
			}).Should(Succeed())

			leafName := serverCRName(run.Name, "bios-fw", "srv-mix")
			leaf := &metalv1alpha1.BIOSVersion{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: leafName}, leaf)).To(Succeed())
			}).Should(Succeed())
			DeferCleanup(k8sClient.Delete, leaf)
		})
	})

	Describe("Multi-stage pipeline ordering", func() {
		It("second stage only starts after first succeeds", func() {
			run := makeRun("run-ordered", "bmc-ord", "srv-ord", "7.00.00", "",
				[]maintenancev1alpha1.PlanStage{
					minimalBMCVersionStage("stage-1", "7.10.00"),
					minimalBMCVersionStage("stage-2", "7.20.00"),
				},
			)
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			leaf1 := &metalv1alpha1.BMCVersion{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bmcCRName(run.Name, "stage-1")}, leaf1)).To(Succeed())
			}).Should(Succeed())
			DeferCleanup(k8sClient.Delete, leaf1)

			Consistently(func(g Gomega) {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: bmcCRName(run.Name, "stage-2")}, &metalv1alpha1.BMCVersion{})
				g.Expect(err).To(MatchError(ContainSubstring("not found")))
			}, "2s").Should(Succeed())

			Eventually(UpdateStatus(leaf1, func() { leaf1.Status.State = metalv1alpha1.BMCVersionStateCompleted })).Should(Succeed())

			leaf2 := &metalv1alpha1.BMCVersion{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bmcCRName(run.Name, "stage-2")}, leaf2)).To(Succeed())
			}).Should(Succeed())
			DeferCleanup(k8sClient.Delete, leaf2)
		})

		It("halts on first stage failure", func() {
			run := makeRun("run-halt", "bmc-halt", "srv-halt", "7.00.00", "",
				[]maintenancev1alpha1.PlanStage{
					minimalBMCVersionStage("stage-x", "7.10.00"),
					minimalBMCVersionStage("stage-y", "7.20.00"),
				},
			)
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			leaf1 := &metalv1alpha1.BMCVersion{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bmcCRName(run.Name, "stage-x")}, leaf1)).To(Succeed())
			}).Should(Succeed())
			DeferCleanup(k8sClient.Delete, leaf1)

			Eventually(UpdateStatus(leaf1, func() { leaf1.Status.State = metalv1alpha1.BMCVersionStateFailed })).Should(Succeed())

			Eventually(Object(run)).Should(HaveField("Status.Phase", Equal(maintenancev1alpha1.MaintenancePlanRunPhaseFailed)))

			Consistently(func(g Gomega) {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: bmcCRName(run.Name, "stage-y")}, &metalv1alpha1.BMCVersion{})
				g.Expect(err).To(MatchError(ContainSubstring("not found")))
			}, "2s").Should(Succeed())
		})
	})

	Describe("Terminal phase no-op", func() {
		It("does not re-process a Succeeded run", func() {
			run := makeRun("run-terminal-ok", "bmc-x", "srv-x", "9.99.99", "",
				[]maintenancev1alpha1.PlanStage{minimalBMCVersionStage("bmc-fw", "7.00.00")})
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			Eventually(Object(run)).Should(HaveField("Status.Phase", Equal(maintenancev1alpha1.MaintenancePlanRunPhaseSucceeded)))
			Consistently(Object(run), "3s").Should(HaveField("Status.Phase", Equal(maintenancev1alpha1.MaintenancePlanRunPhaseSucceeded)))
		})
	})

	Describe("Nil template error paths", func() {
		It("fails stage when BMCVersion template is nil", func() {
			run := makeRun("run-nil-bmcv", "bmc-x", "srv-x", "7.00.00", "",
				[]maintenancev1alpha1.PlanStage{{
					Name:     "bad",
					Kind:     maintenancev1alpha1.StageKindBMCVersion,
					Template: maintenancev1alpha1.StageTemplate{BMCVersion: nil},
				}})
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: run.Name}, run)).To(Succeed())
				g.Expect(run.Status.StageStatuses).NotTo(BeEmpty())
				g.Expect(run.Status.StageStatuses[0].Phase).To(Equal(maintenancev1alpha1.StagePhaseFailed))
				g.Expect(run.Status.StageStatuses[0].Message).To(ContainSubstring("missing bmcVersion template"))
			}).Should(Succeed())
		})

		It("fails stage when BMCSettings template is nil", func() {
			run := makeRun("run-nil-bmcs", "bmc-x", "srv-x", "7.00.00", "",
				[]maintenancev1alpha1.PlanStage{{
					Name:     "bad",
					Kind:     maintenancev1alpha1.StageKindBMCSettings,
					Template: maintenancev1alpha1.StageTemplate{BMCSettings: nil},
				}})
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: run.Name}, run)).To(Succeed())
				g.Expect(run.Status.StageStatuses).NotTo(BeEmpty())
				g.Expect(run.Status.StageStatuses[0].Phase).To(Equal(maintenancev1alpha1.StagePhaseFailed))
				g.Expect(run.Status.StageStatuses[0].Message).To(ContainSubstring("missing bmcSettings template"))
			}).Should(Succeed())
		})

		It("fails stage when BIOSVersion template is nil", func() {
			run := makeRun("run-nil-biosv", "bmc-x", "srv-x", "", "1.0.0",
				[]maintenancev1alpha1.PlanStage{{
					Name:     "bad",
					Kind:     maintenancev1alpha1.StageKindBIOSVersion,
					Template: maintenancev1alpha1.StageTemplate{BIOSVersion: nil},
				}})
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: run.Name}, run)).To(Succeed())
				g.Expect(run.Status.StageStatuses).NotTo(BeEmpty())
				g.Expect(run.Status.StageStatuses[0].Phase).To(Equal(maintenancev1alpha1.StagePhaseFailed))
				g.Expect(run.Status.StageStatuses[0].Message).To(ContainSubstring("missing biosVersion template"))
			}).Should(Succeed())
		})

		It("fails stage when BIOSSettings template is nil", func() {
			run := makeRun("run-nil-bioss", "bmc-x", "srv-x", "", "1.0.0",
				[]maintenancev1alpha1.PlanStage{{
					Name:     "bad",
					Kind:     maintenancev1alpha1.StageKindBIOSSettings,
					Template: maintenancev1alpha1.StageTemplate{BIOSSettings: nil},
				}})
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: run.Name}, run)).To(Succeed())
				g.Expect(run.Status.StageStatuses).NotTo(BeEmpty())
				g.Expect(run.Status.StageStatuses[0].Phase).To(Equal(maintenancev1alpha1.StagePhaseFailed))
				g.Expect(run.Status.StageStatuses[0].Message).To(ContainSubstring("missing biosSettings template"))
			}).Should(Succeed())
		})
	})

	Describe("reconcileDelete", func() {
		It("removes finalizer and allows deletion", func() {
			run := makeRun("run-delete", "bmc-x", "srv-x", "9.99.99", "",
				[]maintenancev1alpha1.PlanStage{minimalBMCVersionStage("bmc-fw", "7.00.00")})
			Expect(k8sClient.Create(ctx, run)).To(Succeed())

			Eventually(Object(run)).Should(HaveField("Status.Phase", Equal(maintenancev1alpha1.MaintenancePlanRunPhaseSucceeded)))

			Expect(k8sClient.Delete(ctx, run)).To(Succeed())
			Eventually(func(g Gomega) {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: run.Name}, run)
				g.Expect(err).To(MatchError(ContainSubstring("not found")))
			}).Should(Succeed())
		})
	})

	Describe("leafCRName and serverLeafCRName helpers", func() {
		It("leafCRName produces deterministic names", func() {
			Expect(bmcCRName("my-run", "stage-1")).To(Equal("my-run-stage-1"))
		})
		It("serverLeafCRName produces deterministic names", func() {
			Expect(serverCRName("my-run", "bios-fw", "server-1")).To(Equal("my-run-bios-fw-server-1"))
		})
	})
})

// findServerStatus returns the ServerStageStatus for the named server, or nil.
func findServerStatus(s maintenancev1alpha1.StageStatus, serverName string) *maintenancev1alpha1.ServerStageStatus {
	for i := range s.ServerStatuses {
		if s.ServerStatuses[i].ServerRef.Name == serverName {
			return &s.ServerStatuses[i]
		}
	}
	return nil
}
