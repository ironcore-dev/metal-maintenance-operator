// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "sigs.k8s.io/controller-runtime/pkg/envtest/komega"

	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// makeRunWithDrift creates a run with the given drift policy.
func makeRunWithDrift(name, bmcName, serverName string, policy maintenancev1alpha1.DriftPolicy, stages []maintenancev1alpha1.PlanStage) *maintenancev1alpha1.MaintenancePlanRun {
	return &maintenancev1alpha1.MaintenancePlanRun{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: maintenancev1alpha1.MaintenancePlanRunSpec{
			PlanRef:     corev1.LocalObjectReference{Name: "test-plan"},
			BMCRef:      corev1.LocalObjectReference{Name: bmcName},
			ServerRefs:  []corev1.LocalObjectReference{{Name: serverName}},
			DriftPolicy: policy,
			Stages:      stages,
		},
	}
}

var _ = Describe("Drift detection", func() {
	SetupMaintenanceTest()

	ctx := context.Background()

	Describe("assignStageDriftPolicies", func() {
		It("assigns Observe to a single terminal version stage", func() {
			r := &MaintenancePlanRunReconciler{}
			run := makeRun("dp-single", "b", "s", "7.00", "",
				[]maintenancev1alpha1.PlanStage{minimalBMCVersionStage("bmc-fw", "7.10")})
			run.Status.StageStatuses = []maintenancev1alpha1.StageStatus{
				{Name: "bmc-fw", Phase: maintenancev1alpha1.StagePhaseSucceeded},
			}
			r.assignStageDriftPolicies(run)
			Expect(run.Status.StageStatuses[0].StageDriftPolicy).To(Equal(maintenancev1alpha1.StageDriftPolicyObserve))
		})

		It("assigns Suspend to intermediate version hops, Observe to terminal", func() {
			r := &MaintenancePlanRunReconciler{}
			run := makeRun("dp-hops", "b", "s", "7.00", "",
				[]maintenancev1alpha1.PlanStage{
					minimalBMCVersionStage("bmc-fw-1", "7.05"),
					minimalBMCVersionStage("bmc-fw-2", "7.10"),
				})
			run.Status.StageStatuses = []maintenancev1alpha1.StageStatus{
				{Name: "bmc-fw-1", Phase: maintenancev1alpha1.StagePhaseSucceeded},
				{Name: "bmc-fw-2", Phase: maintenancev1alpha1.StagePhaseSucceeded},
			}
			r.assignStageDriftPolicies(run)
			Expect(run.Status.StageStatuses[0].StageDriftPolicy).To(Equal(maintenancev1alpha1.StageDriftPolicySuspend))
			Expect(run.Status.StageStatuses[1].StageDriftPolicy).To(Equal(maintenancev1alpha1.StageDriftPolicyObserve))
		})

		It("assigns Observe to settings stages regardless of position", func() {
			r := &MaintenancePlanRunReconciler{}
			run := makeRun("dp-settings", "b", "s", "7.00", "1.0",
				[]maintenancev1alpha1.PlanStage{
					{Name: "bmc-pre", Kind: maintenancev1alpha1.StageKindBMCSettings,
						Template: maintenancev1alpha1.StageTemplate{BMCSettings: &maintenancev1alpha1.PlanBMCSettingsTemplate{Version: "7.10"}}},
					minimalBMCVersionStage("bmc-fw", "7.10"),
					{Name: "bios-pre", Kind: maintenancev1alpha1.StageKindBIOSSettings,
						Template: maintenancev1alpha1.StageTemplate{BIOSSettings: &metalv1alpha1.BIOSSettingsTemplate{Version: "2.0"}}},
				})
			run.Status.StageStatuses = []maintenancev1alpha1.StageStatus{
				{Name: "bmc-pre", Phase: maintenancev1alpha1.StagePhaseSucceeded},
				{Name: "bmc-fw", Phase: maintenancev1alpha1.StagePhaseSucceeded},
				{Name: "bios-pre", Phase: maintenancev1alpha1.StagePhaseSucceeded},
			}
			r.assignStageDriftPolicies(run)
			Expect(run.Status.StageStatuses[0].StageDriftPolicy).To(Equal(maintenancev1alpha1.StageDriftPolicyObserve))
			Expect(run.Status.StageStatuses[1].StageDriftPolicy).To(Equal(maintenancev1alpha1.StageDriftPolicyObserve))
			Expect(run.Status.StageStatuses[2].StageDriftPolicy).To(Equal(maintenancev1alpha1.StageDriftPolicyObserve))
		})

		It("does not assign a policy to skipped stages", func() {
			r := &MaintenancePlanRunReconciler{}
			run := makeRun("dp-skip", "b", "s", "9.99", "",
				[]maintenancev1alpha1.PlanStage{minimalBMCVersionStage("bmc-fw", "7.10")})
			run.Status.StageStatuses = []maintenancev1alpha1.StageStatus{
				{Name: "bmc-fw", Phase: maintenancev1alpha1.StagePhaseSkipped},
			}
			r.assignStageDriftPolicies(run)
			Expect(run.Status.StageStatuses[0].StageDriftPolicy).To(BeEmpty())
		})
	})

	Describe("DriftPolicy=Disabled (default)", func() {
		It("run stays Succeeded and does not re-execute after leaf CR regresses", func() {
			run := makeRunWithDrift("run-disabled", "bmc-d", "srv-d",
				maintenancev1alpha1.DriftPolicyDisabled,
				[]maintenancev1alpha1.PlanStage{minimalBMCVersionStage("bmc-fw", "7.10")})
			run.Spec.BaselineBMCVersion = "7.00"
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			// Let the run complete.
			leaf := &metalv1alpha1.BMCVersion{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bmcCRName(run.Name, "bmc-fw")}, leaf)).To(Succeed())
			}).Should(Succeed())
			DeferCleanup(k8sClient.Delete, leaf)
			Eventually(UpdateStatus(leaf, func() { leaf.Status.State = metalv1alpha1.BMCVersionStateCompleted })).Should(Succeed())
			Eventually(Object(run)).Should(HaveField("Status.Phase", Equal(maintenancev1alpha1.MaintenancePlanRunPhaseSucceeded)))

			// Simulate out-of-band regression.
			Eventually(UpdateStatus(leaf, func() { leaf.Status.State = metalv1alpha1.BMCVersionStatePending })).Should(Succeed())

			// Run must remain Succeeded — disabled means no drift monitoring.
			Consistently(Object(run), "3s").Should(
				HaveField("Status.Phase", Equal(maintenancev1alpha1.MaintenancePlanRunPhaseSucceeded)),
			)
		})
	})

	Describe("DriftPolicy=Observe", func() {
		It("sets DriftDetected condition when leaf regresses, clears it when restored", func() {
			run := makeRunWithDrift("run-observe", "bmc-ob", "srv-ob",
				maintenancev1alpha1.DriftPolicyObserve,
				[]maintenancev1alpha1.PlanStage{minimalBMCVersionStage("bmc-fw", "7.10")})
			run.Spec.BaselineBMCVersion = "7.00"
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			leaf := &metalv1alpha1.BMCVersion{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bmcCRName(run.Name, "bmc-fw")}, leaf)).To(Succeed())
			}).Should(Succeed())
			DeferCleanup(k8sClient.Delete, leaf)
			Eventually(UpdateStatus(leaf, func() { leaf.Status.State = metalv1alpha1.BMCVersionStateCompleted })).Should(Succeed())
			Eventually(Object(run)).Should(HaveField("Status.Phase", Equal(maintenancev1alpha1.MaintenancePlanRunPhaseSucceeded)))

			// Drift policies should be assigned on completion.
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: run.Name}, run)).To(Succeed())
				g.Expect(run.Status.StageStatuses[0].StageDriftPolicy).To(Equal(maintenancev1alpha1.StageDriftPolicyObserve))
			}).Should(Succeed())

			// Simulate regression.
			Eventually(UpdateStatus(leaf, func() { leaf.Status.State = metalv1alpha1.BMCVersionStatePending })).Should(Succeed())

			// Condition must be set; run must stay Succeeded (no re-execution).
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: run.Name}, run)).To(Succeed())
				cond := findCondition(run.Status.Conditions, maintenancev1alpha1.ConditionTypeDriftDetected)
				g.Expect(cond).NotTo(BeNil())
				g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			}).Should(Succeed())
			Expect(run.Status.Phase).To(Equal(maintenancev1alpha1.MaintenancePlanRunPhaseSucceeded))

			// Restore the leaf CR.
			Eventually(UpdateStatus(leaf, func() { leaf.Status.State = metalv1alpha1.BMCVersionStateCompleted })).Should(Succeed())

			// Condition must be cleared.
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: run.Name}, run)).To(Succeed())
				cond := findCondition(run.Status.Conditions, maintenancev1alpha1.ConditionTypeDriftDetected)
				g.Expect(cond).To(BeNil())
			}).Should(Succeed())
		})
	})

	Describe("DriftPolicy=Reconcile", func() {
		It("re-runs from the drifted stage and succeeds again", func() {
			run := makeRunWithDrift("run-reconcile", "bmc-rc", "srv-rc",
				maintenancev1alpha1.DriftPolicyReconcile,
				[]maintenancev1alpha1.PlanStage{
					minimalBMCVersionStage("bmc-fw-1", "7.05"),
					minimalBMCVersionStage("bmc-fw-2", "7.10"),
				})
			run.Spec.BaselineBMCVersion = "7.00"
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			// Complete both stages.
			for _, stageName := range []string{"bmc-fw-1", "bmc-fw-2"} {
				leaf := &metalv1alpha1.BMCVersion{}
				Eventually(func(g Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bmcCRName(run.Name, stageName)}, leaf)).To(Succeed())
				}).Should(Succeed())
				DeferCleanup(k8sClient.Delete, leaf)
				Eventually(UpdateStatus(leaf, func() { leaf.Status.State = metalv1alpha1.BMCVersionStateCompleted })).Should(Succeed())
			}
			Eventually(Object(run)).Should(HaveField("Status.Phase", Equal(maintenancev1alpha1.MaintenancePlanRunPhaseSucceeded)))

			// bmc-fw-1 is Suspend (superseded); bmc-fw-2 is Observe (terminal).
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: run.Name}, run)).To(Succeed())
				g.Expect(run.Status.StageStatuses[0].StageDriftPolicy).To(Equal(maintenancev1alpha1.StageDriftPolicySuspend))
				g.Expect(run.Status.StageStatuses[1].StageDriftPolicy).To(Equal(maintenancev1alpha1.StageDriftPolicyObserve))
			}).Should(Succeed())

			// Regress the terminal stage.
			terminalLeaf := &metalv1alpha1.BMCVersion{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bmcCRName(run.Name, "bmc-fw-2")}, terminalLeaf)).To(Succeed())
			Eventually(UpdateStatus(terminalLeaf, func() { terminalLeaf.Status.State = metalv1alpha1.BMCVersionStatePending })).Should(Succeed())

			// Run should re-enter Running.
			Eventually(Object(run)).Should(HaveField("Status.Phase", Equal(maintenancev1alpha1.MaintenancePlanRunPhaseRunning)))

			// Complete the re-run of bmc-fw-2.
			Eventually(UpdateStatus(terminalLeaf, func() { terminalLeaf.Status.State = metalv1alpha1.BMCVersionStateCompleted })).Should(Succeed())

			// Run must reach Succeeded again.
			Eventually(Object(run)).Should(HaveField("Status.Phase", Equal(maintenancev1alpha1.MaintenancePlanRunPhaseSucceeded)))
		})

		It("only re-runs from the earliest dirty stage, not the whole pipeline", func() {
			run := makeRunWithDrift("run-earliest", "bmc-early", "srv-early",
				maintenancev1alpha1.DriftPolicyReconcile,
				[]maintenancev1alpha1.PlanStage{
					minimalBMCVersionStage("bmc-fw-1", "7.05"),
					minimalBMCVersionStage("bmc-fw-2", "7.10"),
				})
			run.Spec.BaselineBMCVersion = "7.00"
			Expect(k8sClient.Create(ctx, run)).To(Succeed())
			DeferCleanup(k8sClient.Delete, run)

			for _, stageName := range []string{"bmc-fw-1", "bmc-fw-2"} {
				leaf := &metalv1alpha1.BMCVersion{}
				Eventually(func(g Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bmcCRName(run.Name, stageName)}, leaf)).To(Succeed())
				}).Should(Succeed())
				DeferCleanup(k8sClient.Delete, leaf)
				Eventually(UpdateStatus(leaf, func() { leaf.Status.State = metalv1alpha1.BMCVersionStateCompleted })).Should(Succeed())
			}
			Eventually(Object(run)).Should(HaveField("Status.Phase", Equal(maintenancev1alpha1.MaintenancePlanRunPhaseSucceeded)))

			// Regress only the terminal (second) stage.
			terminalLeaf := &metalv1alpha1.BMCVersion{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: bmcCRName(run.Name, "bmc-fw-2")}, terminalLeaf)).To(Succeed())
			Eventually(UpdateStatus(terminalLeaf, func() { terminalLeaf.Status.State = metalv1alpha1.BMCVersionStatePending })).Should(Succeed())

			Eventually(Object(run)).Should(HaveField("Status.Phase", Equal(maintenancev1alpha1.MaintenancePlanRunPhaseRunning)))

			// Stage 0 (bmc-fw-1) must still be Succeeded — it was not reset.
			// Stage 1 (bmc-fw-2) must be Pending or Running — it was reset and re-executing.
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: run.Name}, run)).To(Succeed())
				g.Expect(run.Status.StageStatuses[0].Phase).To(Equal(maintenancev1alpha1.StagePhaseSucceeded))
				g.Expect(run.Status.StageStatuses[1].Phase).To(Or(
					Equal(maintenancev1alpha1.StagePhasePending),
					Equal(maintenancev1alpha1.StagePhaseRunning),
				))
			}).Should(Succeed())
		})
	})

	Describe("Plan-level DriftPolicy propagation", func() {
		It("copies DriftPolicy from plan to run at creation", func() {
			secret := newTestBMCSecret(ctx, "plan-drift")
			bmc := newTestBMC(ctx, "bmc-plan-drift", "7.00", secret.Name)
			newTestServer(ctx, "server-plan-drift", "", map[string]string{"drift-prop": "yes"}, bmc.Name)

			plan := &maintenancev1alpha1.MaintenancePlan{
				ObjectMeta: metav1.ObjectMeta{Name: "plan-drift"},
				Spec: maintenancev1alpha1.MaintenancePlanSpec{
					ServerSelector: metav1.LabelSelector{MatchLabels: map[string]string{"drift-prop": "yes"}},
					MaxConcurrent:  5,
					DriftPolicy:    maintenancev1alpha1.DriftPolicyObserve,
					Stages:         []maintenancev1alpha1.PlanStage{minimalBMCVersionStage("bmc-fw", "7.10")},
				},
			}
			Expect(k8sClient.Create(ctx, plan)).To(Succeed())
			DeferCleanup(k8sClient.Delete, plan)

			run := &maintenancev1alpha1.MaintenancePlanRun{}
			Eventually(func(g Gomega) {
				list := &maintenancev1alpha1.MaintenancePlanRunList{}
				g.Expect(k8sClient.List(ctx, list, client.MatchingLabels{planOwnerLabel: plan.Name})).To(Succeed())
				g.Expect(list.Items).To(HaveLen(1))
				*run = list.Items[0]
			}).Should(Succeed())
			Expect(run.Spec.DriftPolicy).To(Equal(maintenancev1alpha1.DriftPolicyObserve))
		})
	})
})

// findCondition returns the condition with the given type, or nil.
func findCondition(conditions []metav1.Condition, condType string) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == condType {
			return &conditions[i]
		}
	}
	return nil
}
