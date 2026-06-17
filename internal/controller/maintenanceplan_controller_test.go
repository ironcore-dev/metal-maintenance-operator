// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"
	"fmt"

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

// newTestBMCSecret creates and returns a minimal BMCSecret for a test.
func newTestBMCSecret(ctx context.Context, nameSuffix string) *metalv1alpha1.BMCSecret {
	s := &metalv1alpha1.BMCSecret{
		ObjectMeta: metav1.ObjectMeta{Name: "bmc-secret-" + nameSuffix},
		Data: map[string][]byte{
			metalv1alpha1.BMCSecretUsernameKeyName: []byte("user"),
			metalv1alpha1.BMCSecretPasswordKeyName: []byte("pass"),
		},
	}
	Expect(k8sClient.Create(ctx, s)).To(Succeed())
	DeferCleanup(k8sClient.Delete, s)
	return s
}

// newTestBMC creates a cluster-scoped BMC and patches its FirmwareVersion.
func newTestBMC(ctx context.Context, name, firmwareVersion string, secretName string) *metalv1alpha1.BMC {
	bmc := &metalv1alpha1.BMC{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: metalv1alpha1.BMCSpec{
			EndpointRef:  &corev1.LocalObjectReference{Name: "ep-" + name},
			BMCSecretRef: corev1.LocalObjectReference{Name: secretName},
			Protocol: metalv1alpha1.Protocol{
				Name: metalv1alpha1.ProtocolRedfishLocal,
				Port: 8000,
			},
		},
	}
	Expect(k8sClient.Create(ctx, bmc)).To(Succeed())
	DeferCleanup(k8sClient.Delete, bmc)

	if firmwareVersion != "" {
		Eventually(UpdateStatus(bmc, func() {
			bmc.Status.FirmwareVersion = firmwareVersion
		})).Should(Succeed())
	}
	return bmc
}

// newTestServer creates a cluster-scoped Server with a BMCRef and optional BIOS version.
func newTestServer(ctx context.Context, name, biosVersion string, lbls map[string]string, bmcName string) *metalv1alpha1.Server {
	srv := &metalv1alpha1.Server{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: lbls},
		Spec: metalv1alpha1.ServerSpec{
			SystemUUID: fmt.Sprintf("uuid-%s", name),
			BMCRef:     &corev1.LocalObjectReference{Name: bmcName},
		},
	}
	Expect(k8sClient.Create(ctx, srv)).To(Succeed())
	DeferCleanup(k8sClient.Delete, srv)

	if biosVersion != "" {
		Eventually(UpdateStatus(srv, func() {
			srv.Status.BIOSVersion = biosVersion
		})).Should(Succeed())
	}
	return srv
}

// minimalBMCVersionStage returns a BMCVersion stage for plan specs.
func minimalBMCVersionStage(name, version string) maintenancev1alpha1.PlanStage {
	return maintenancev1alpha1.PlanStage{
		Name: name,
		Kind: maintenancev1alpha1.StageKindBMCVersion,
		Template: maintenancev1alpha1.StageTemplate{
			BMCVersion: &metalv1alpha1.BMCVersionTemplate{
				Version: version,
				Image:   metalv1alpha1.ImageSpec{URI: "https://example.com/bmc-" + version + ".bin"},
			},
		},
	}
}

// minimalBIOSVersionStage returns a BIOSVersion stage for plan specs.
func minimalBIOSVersionStage(name, version string) maintenancev1alpha1.PlanStage {
	return maintenancev1alpha1.PlanStage{
		Name: name,
		Kind: maintenancev1alpha1.StageKindBIOSVersion,
		Template: maintenancev1alpha1.StageTemplate{
			BIOSVersion: &metalv1alpha1.BIOSVersionTemplate{
				Version: version,
				Image:   metalv1alpha1.ImageSpec{URI: "https://example.com/bios-" + version + ".bin"},
			},
		},
	}
}

var _ = Describe("MaintenancePlan Controller", func() {
	SetupMaintenanceTest()

	ctx := context.Background()

	Describe("Run creation — one run per unique BMC", func() {
		It("creates one run for a single server", func() {
			secret := newTestBMCSecret(ctx, "plan-basic")
			bmc := newTestBMC(ctx, "bmc-plan-basic", "7.00.00", secret.Name)
			server := newTestServer(ctx, "server-plan-basic", "1.0.0",
				map[string]string{"hw-vendor": "dell"}, bmc.Name)

			plan := &maintenancev1alpha1.MaintenancePlan{
				ObjectMeta: metav1.ObjectMeta{Name: "plan-basic"},
				Spec: maintenancev1alpha1.MaintenancePlanSpec{
					ServerSelector: metav1.LabelSelector{MatchLabels: map[string]string{"hw-vendor": "dell"}},
					MaxConcurrent:  5,
					Stages:         []maintenancev1alpha1.PlanStage{minimalBMCVersionStage("bmc-fw", "7.10.00")},
				},
			}
			Expect(k8sClient.Create(ctx, plan)).To(Succeed())
			DeferCleanup(k8sClient.Delete, plan)

			runList := &maintenancev1alpha1.MaintenancePlanRunList{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.List(ctx, runList, client.MatchingLabels{planOwnerLabel: plan.Name})).To(Succeed())
				g.Expect(runList.Items).To(HaveLen(1))
			}).Should(Succeed())

			run := &runList.Items[0]
			Expect(run.Spec.PlanRef.Name).To(Equal(plan.Name))
			Expect(run.Spec.BMCRef.Name).To(Equal(bmc.Name))
			Expect(run.Spec.ServerRefs).To(ConsistOf(corev1.LocalObjectReference{Name: server.Name}))
			Expect(run.Spec.BaselineBMCVersion).To(Equal("7.00.00"))
			Expect(run.Spec.BaselineBIOSVersions).To(HaveKeyWithValue(server.Name, "1.0.0"))
		})

		It("creates ONE run for two servers sharing the same BMC", func() {
			secret := newTestBMCSecret(ctx, "plan-shared")
			bmc := newTestBMC(ctx, "bmc-plan-shared", "7.00.00", secret.Name)
			srv1 := newTestServer(ctx, "server-shared-1", "1.0.0", map[string]string{"shared": "yes"}, bmc.Name)
			srv2 := newTestServer(ctx, "server-shared-2", "1.0.0", map[string]string{"shared": "yes"}, bmc.Name)

			plan := &maintenancev1alpha1.MaintenancePlan{
				ObjectMeta: metav1.ObjectMeta{Name: "plan-shared"},
				Spec: maintenancev1alpha1.MaintenancePlanSpec{
					ServerSelector: metav1.LabelSelector{MatchLabels: map[string]string{"shared": "yes"}},
					MaxConcurrent:  5,
					Stages:         []maintenancev1alpha1.PlanStage{minimalBMCVersionStage("bmc-fw", "7.10.00")},
				},
			}
			Expect(k8sClient.Create(ctx, plan)).To(Succeed())
			DeferCleanup(k8sClient.Delete, plan)

			runList := &maintenancev1alpha1.MaintenancePlanRunList{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.List(ctx, runList, client.MatchingLabels{planOwnerLabel: plan.Name})).To(Succeed())
				g.Expect(runList.Items).To(HaveLen(1))
			}).Should(Succeed())

			run := &runList.Items[0]
			Expect(run.Spec.BMCRef.Name).To(Equal(bmc.Name))
			Expect(run.Spec.ServerRefs).To(ConsistOf(
				corev1.LocalObjectReference{Name: srv1.Name},
				corev1.LocalObjectReference{Name: srv2.Name},
			))

			// Must stay at exactly one run.
			Consistently(func(g Gomega) {
				g.Expect(k8sClient.List(ctx, runList, client.MatchingLabels{planOwnerLabel: plan.Name})).To(Succeed())
				g.Expect(runList.Items).To(HaveLen(1))
			}, "3s").Should(Succeed())
		})

		It("creates one run per BMC when servers have distinct BMCs", func() {
			secret := newTestBMCSecret(ctx, "plan-distinct")
			bmc1 := newTestBMC(ctx, "bmc-distinct-1", "", secret.Name)
			bmc2 := newTestBMC(ctx, "bmc-distinct-2", "", secret.Name)
			newTestServer(ctx, "server-distinct-1", "", map[string]string{"distinct": "yes"}, bmc1.Name)
			newTestServer(ctx, "server-distinct-2", "", map[string]string{"distinct": "yes"}, bmc2.Name)

			plan := &maintenancev1alpha1.MaintenancePlan{
				ObjectMeta: metav1.ObjectMeta{Name: "plan-distinct"},
				Spec: maintenancev1alpha1.MaintenancePlanSpec{
					ServerSelector: metav1.LabelSelector{MatchLabels: map[string]string{"distinct": "yes"}},
					MaxConcurrent:  5,
					Stages:         []maintenancev1alpha1.PlanStage{minimalBMCVersionStage("bmc-fw", "7.10.00")},
				},
			}
			Expect(k8sClient.Create(ctx, plan)).To(Succeed())
			DeferCleanup(k8sClient.Delete, plan)

			Eventually(func(g Gomega) {
				list := &maintenancev1alpha1.MaintenancePlanRunList{}
				g.Expect(k8sClient.List(ctx, list, client.MatchingLabels{planOwnerLabel: plan.Name})).To(Succeed())
				g.Expect(list.Items).To(HaveLen(2))
			}).Should(Succeed())
		})

		It("respects maxConcurrent", func() {
			secret := newTestBMCSecret(ctx, "plan-conc")
			bmc1 := newTestBMC(ctx, "bmc-conc-1", "", secret.Name)
			bmc2 := newTestBMC(ctx, "bmc-conc-2", "", secret.Name)
			newTestServer(ctx, "server-conc-1", "", map[string]string{"conc": "yes"}, bmc1.Name)
			newTestServer(ctx, "server-conc-2", "", map[string]string{"conc": "yes"}, bmc2.Name)

			plan := &maintenancev1alpha1.MaintenancePlan{
				ObjectMeta: metav1.ObjectMeta{Name: "plan-conc"},
				Spec: maintenancev1alpha1.MaintenancePlanSpec{
					ServerSelector: metav1.LabelSelector{MatchLabels: map[string]string{"conc": "yes"}},
					MaxConcurrent:  1,
					Stages:         []maintenancev1alpha1.PlanStage{minimalBMCVersionStage("bmc-fw", "7.10.00")},
				},
			}
			Expect(k8sClient.Create(ctx, plan)).To(Succeed())
			DeferCleanup(k8sClient.Delete, plan)

			Eventually(func(g Gomega) {
				list := &maintenancev1alpha1.MaintenancePlanRunList{}
				g.Expect(k8sClient.List(ctx, list, client.MatchingLabels{planOwnerLabel: plan.Name})).To(Succeed())
				g.Expect(list.Items).To(HaveLen(1))
			}).Should(Succeed())

			Consistently(func(g Gomega) {
				list := &maintenancev1alpha1.MaintenancePlanRunList{}
				g.Expect(k8sClient.List(ctx, list, client.MatchingLabels{planOwnerLabel: plan.Name})).To(Succeed())
				g.Expect(list.Items).To(HaveLen(1))
			}, "3s").Should(Succeed())
		})

		It("creates no runs when no servers match the selector", func() {
			plan := &maintenancev1alpha1.MaintenancePlan{
				ObjectMeta: metav1.ObjectMeta{Name: "plan-no-match"},
				Spec: maintenancev1alpha1.MaintenancePlanSpec{
					ServerSelector: metav1.LabelSelector{MatchLabels: map[string]string{"nomatch": "xyz"}},
					MaxConcurrent:  5,
					Stages:         []maintenancev1alpha1.PlanStage{minimalBMCVersionStage("bmc-fw", "7.10.00")},
				},
			}
			Expect(k8sClient.Create(ctx, plan)).To(Succeed())
			DeferCleanup(k8sClient.Delete, plan)

			Consistently(func(g Gomega) {
				list := &maintenancev1alpha1.MaintenancePlanRunList{}
				g.Expect(k8sClient.List(ctx, list, client.MatchingLabels{planOwnerLabel: plan.Name})).To(Succeed())
				g.Expect(list.Items).To(BeEmpty())
			}, "3s").Should(Succeed())
		})

		It("skips a server that has no BMCRef", func() {
			noBMCServer := &metalv1alpha1.Server{
				ObjectMeta: metav1.ObjectMeta{Name: "server-no-bmc", Labels: map[string]string{"no-bmc": "yes"}},
				Spec:       metalv1alpha1.ServerSpec{SystemUUID: "uuid-no-bmc"},
			}
			Expect(k8sClient.Create(ctx, noBMCServer)).To(Succeed())
			DeferCleanup(k8sClient.Delete, noBMCServer)

			plan := &maintenancev1alpha1.MaintenancePlan{
				ObjectMeta: metav1.ObjectMeta{Name: "plan-no-bmc"},
				Spec: maintenancev1alpha1.MaintenancePlanSpec{
					ServerSelector: metav1.LabelSelector{MatchLabels: map[string]string{"no-bmc": "yes"}},
					MaxConcurrent:  5,
					Stages:         []maintenancev1alpha1.PlanStage{minimalBMCVersionStage("bmc-fw", "7.10.00")},
				},
			}
			Expect(k8sClient.Create(ctx, plan)).To(Succeed())
			DeferCleanup(k8sClient.Delete, plan)

			Consistently(func(g Gomega) {
				list := &maintenancev1alpha1.MaintenancePlanRunList{}
				g.Expect(k8sClient.List(ctx, list, client.MatchingLabels{planOwnerLabel: plan.Name})).To(Succeed())
				g.Expect(list.Items).To(BeEmpty())
			}, "3s").Should(Succeed())
		})
	})

	Describe("Status aggregation", func() {
		It("reports Active while a run is in progress", func() {
			secret := newTestBMCSecret(ctx, "plan-status")
			bmc := newTestBMC(ctx, "bmc-plan-status", "", secret.Name)
			newTestServer(ctx, "server-plan-status", "", map[string]string{"status-test": "yes"}, bmc.Name)

			plan := &maintenancev1alpha1.MaintenancePlan{
				ObjectMeta: metav1.ObjectMeta{Name: "plan-status"},
				Spec: maintenancev1alpha1.MaintenancePlanSpec{
					ServerSelector: metav1.LabelSelector{MatchLabels: map[string]string{"status-test": "yes"}},
					MaxConcurrent:  5,
					Stages:         []maintenancev1alpha1.PlanStage{minimalBMCVersionStage("bmc-fw", "7.10.00")},
				},
			}
			Expect(k8sClient.Create(ctx, plan)).To(Succeed())
			DeferCleanup(k8sClient.Delete, plan)

			Eventually(Object(plan)).Should(HaveField("Status.TotalRuns", int32(1)))
			Eventually(Object(plan)).Should(HaveField("Status.Phase", Or(
				Equal(maintenancev1alpha1.MaintenancePlanPhaseActive),
				Equal(maintenancev1alpha1.MaintenancePlanPhasePending),
			)))
		})

		It("reports Completed when all stages skip (run succeeds immediately)", func() {
			secret := newTestBMCSecret(ctx, "plan-complete")
			bmc := newTestBMC(ctx, "bmc-plan-complete", "7.10.00", secret.Name)
			newTestServer(ctx, "server-plan-complete", "2.0.0", map[string]string{"complete-test": "yes"}, bmc.Name)

			plan := &maintenancev1alpha1.MaintenancePlan{
				ObjectMeta: metav1.ObjectMeta{Name: "plan-complete"},
				Spec: maintenancev1alpha1.MaintenancePlanSpec{
					ServerSelector: metav1.LabelSelector{MatchLabels: map[string]string{"complete-test": "yes"}},
					MaxConcurrent:  5,
					Stages:         []maintenancev1alpha1.PlanStage{minimalBMCVersionStage("bmc-fw", "7.05.00")},
				},
			}
			Expect(k8sClient.Create(ctx, plan)).To(Succeed())
			DeferCleanup(k8sClient.Delete, plan)

			Eventually(Object(plan)).Should(SatisfyAll(
				HaveField("Status.TotalRuns", int32(1)),
				HaveField("Status.SucceededRuns", int32(1)),
				HaveField("Status.Phase", Equal(maintenancev1alpha1.MaintenancePlanPhaseCompleted)),
			))
		})

		It("reports Failed when a run fails", func() {
			secret := newTestBMCSecret(ctx, "plan-failed")
			bmc := newTestBMC(ctx, "bmc-plan-failed", "7.00.00", secret.Name)
			newTestServer(ctx, "server-plan-failed", "", map[string]string{"failed-test": "yes"}, bmc.Name)

			plan := &maintenancev1alpha1.MaintenancePlan{
				ObjectMeta: metav1.ObjectMeta{Name: "plan-failed"},
				Spec: maintenancev1alpha1.MaintenancePlanSpec{
					ServerSelector: metav1.LabelSelector{MatchLabels: map[string]string{"failed-test": "yes"}},
					MaxConcurrent:  5,
					Stages:         []maintenancev1alpha1.PlanStage{minimalBMCVersionStage("bmc-fw", "7.10.00")},
				},
			}
			Expect(k8sClient.Create(ctx, plan)).To(Succeed())
			DeferCleanup(k8sClient.Delete, plan)

			runList := &maintenancev1alpha1.MaintenancePlanRunList{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.List(ctx, runList, client.MatchingLabels{planOwnerLabel: plan.Name})).To(Succeed())
				g.Expect(runList.Items).To(HaveLen(1))
			}).Should(Succeed())

			leafName := bmcCRName(runList.Items[0].Name, "bmc-fw")
			leafCR := &metalv1alpha1.BMCVersion{}
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: leafName}, leafCR)).To(Succeed())
			}).Should(Succeed())
			DeferCleanup(k8sClient.Delete, leafCR)

			Eventually(UpdateStatus(leafCR, func() {
				leafCR.Status.State = metalv1alpha1.BMCVersionStateFailed
			})).Should(Succeed())

			Eventually(Object(plan)).Should(SatisfyAll(
				HaveField("Status.FailedRuns", int32(1)),
				HaveField("Status.Phase", Equal(maintenancev1alpha1.MaintenancePlanPhaseFailed)),
			))
		})
	})
})
