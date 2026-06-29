// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func minimalRunForUnit(bmcName, serverName string) *maintenancev1alpha1.MaintenancePlanRun {
	return &maintenancev1alpha1.MaintenancePlanRun{
		ObjectMeta: metav1.ObjectMeta{Name: "unit-run"},
		Spec: maintenancev1alpha1.MaintenancePlanRunSpec{
			PlanRef:    corev1.LocalObjectReference{Name: "plan"},
			BMCRef:     corev1.LocalObjectReference{Name: bmcName},
			ServerRefs: []corev1.LocalObjectReference{{Name: serverName}},
		},
	}
}

var _ = Describe("bmcStageVersion (unit)", func() {
	DescribeTable("extracts version from BMC-scoped stage templates",
		func(stage maintenancev1alpha1.PlanStage, want string) {
			Expect(bmcStageVersion(&stage)).To(Equal(want))
		},
		Entry("BMCSettings with template", maintenancev1alpha1.PlanStage{
			Kind:     maintenancev1alpha1.StageKindBMCSettings,
			Template: maintenancev1alpha1.StageTemplate{BMCSettings: &maintenancev1alpha1.PlanBMCSettingsTemplate{Version: "7.10"}},
		}, "7.10"),
		Entry("BMCSettings with nil template", maintenancev1alpha1.PlanStage{
			Kind:     maintenancev1alpha1.StageKindBMCSettings,
			Template: maintenancev1alpha1.StageTemplate{BMCSettings: nil},
		}, ""),
		Entry("BMCVersion with template", maintenancev1alpha1.PlanStage{
			Kind: maintenancev1alpha1.StageKindBMCVersion,
			Template: maintenancev1alpha1.StageTemplate{BMCVersion: &metalv1alpha1.BMCVersionTemplate{
				Version: "7.10", Image: metalv1alpha1.ImageSpec{URI: "x"},
			}},
		}, "7.10"),
		Entry("BMCVersion with nil template", maintenancev1alpha1.PlanStage{
			Kind:     maintenancev1alpha1.StageKindBMCVersion,
			Template: maintenancev1alpha1.StageTemplate{BMCVersion: nil},
		}, ""),
		Entry("BIOS kind returns empty", maintenancev1alpha1.PlanStage{
			Kind: maintenancev1alpha1.StageKindBIOSVersion,
			Template: maintenancev1alpha1.StageTemplate{BIOSVersion: &metalv1alpha1.BIOSVersionTemplate{
				Version: "2.0", Image: metalv1alpha1.ImageSpec{URI: "x"},
			}},
		}, ""),
	)
})

var _ = Describe("serverStageVersion (unit)", func() {
	DescribeTable("extracts version from Server-scoped stage templates",
		func(stage maintenancev1alpha1.PlanStage, want string) {
			Expect(serverStageVersion(&stage)).To(Equal(want))
		},
		Entry("BIOSSettings with template", maintenancev1alpha1.PlanStage{
			Kind:     maintenancev1alpha1.StageKindBIOSSettings,
			Template: maintenancev1alpha1.StageTemplate{BIOSSettings: &metalv1alpha1.BIOSSettingsTemplate{Version: "2.10"}},
		}, "2.10"),
		Entry("BIOSSettings with nil template", maintenancev1alpha1.PlanStage{
			Kind:     maintenancev1alpha1.StageKindBIOSSettings,
			Template: maintenancev1alpha1.StageTemplate{BIOSSettings: nil},
		}, ""),
		Entry("BIOSVersion with template", maintenancev1alpha1.PlanStage{
			Kind: maintenancev1alpha1.StageKindBIOSVersion,
			Template: maintenancev1alpha1.StageTemplate{BIOSVersion: &metalv1alpha1.BIOSVersionTemplate{
				Version: "2.10", Image: metalv1alpha1.ImageSpec{URI: "x"},
			}},
		}, "2.10"),
		Entry("BIOSVersion with nil template", maintenancev1alpha1.PlanStage{
			Kind:     maintenancev1alpha1.StageKindBIOSVersion,
			Template: maintenancev1alpha1.StageTemplate{BIOSVersion: nil},
		}, ""),
		Entry("BMC kind returns empty", maintenancev1alpha1.PlanStage{
			Kind: maintenancev1alpha1.StageKindBMCVersion,
			Template: maintenancev1alpha1.StageTemplate{BMCVersion: &metalv1alpha1.BMCVersionTemplate{
				Version: "7.0", Image: metalv1alpha1.ImageSpec{URI: "x"},
			}},
		}, ""),
	)
})

var _ = Describe("buildBMCObject (unit)", func() {
	r := &MaintenancePlanRunReconciler{}

	It("returns error for non-BMC kind", func() {
		run := minimalRunForUnit("bmc", "srv")
		_, err := r.buildBMCObject(run, &maintenancev1alpha1.PlanStage{
			Name: "s", Kind: maintenancev1alpha1.StageKindBIOSVersion,
		}, 0)
		Expect(err).To(MatchError(ContainSubstring("not BMC-scoped")))
	})

	It("builds BMCSettings correctly", func() {
		run := minimalRunForUnit("my-bmc", "my-srv")
		stage := &maintenancev1alpha1.PlanStage{
			Name: "pre",
			Kind: maintenancev1alpha1.StageKindBMCSettings,
			Template: maintenancev1alpha1.StageTemplate{
				BMCSettings: &maintenancev1alpha1.PlanBMCSettingsTemplate{
					Version: "7.10", Settings: map[string]string{"k": "v"},
				},
			},
		}
		obj, err := r.buildBMCObject(run, stage, 0)
		Expect(err).NotTo(HaveOccurred())
		bmcSettings, ok := obj.(*metalv1alpha1.BMCSettings)
		Expect(ok).To(BeTrue())
		Expect(bmcSettings.Name).To(Equal(bmcCRName(run.Name, stage.Name)))
		Expect(bmcSettings.Spec.BMCRef.Name).To(Equal("my-bmc"))
		Expect(bmcSettings.Spec.Version).To(Equal("7.10"))
		Expect(bmcSettings.Spec.SettingsMap).To(HaveKeyWithValue("k", "v"))
		Expect(bmcSettings.Labels[planRunOwnerLabel]).To(Equal(run.Name))
	})

	It("builds BMCVersion correctly", func() {
		run := minimalRunForUnit("my-bmc", "my-srv")
		stage := &maintenancev1alpha1.PlanStage{
			Name: "fw",
			Kind: maintenancev1alpha1.StageKindBMCVersion,
			Template: maintenancev1alpha1.StageTemplate{
				BMCVersion: &metalv1alpha1.BMCVersionTemplate{
					Version: "7.10", Image: metalv1alpha1.ImageSpec{URI: "x"},
				},
			},
		}
		obj, err := r.buildBMCObject(run, stage, 0)
		Expect(err).NotTo(HaveOccurred())
		bmcVersion, ok := obj.(*metalv1alpha1.BMCVersion)
		Expect(ok).To(BeTrue())
		Expect(bmcVersion.Spec.Version).To(Equal("7.10"))
		Expect(bmcVersion.Spec.BMCRef.Name).To(Equal("my-bmc"))
	})

	It("returns error when BMCSettings template is nil", func() {
		run := minimalRunForUnit("bmc", "srv")
		_, err := r.buildBMCObject(run, &maintenancev1alpha1.PlanStage{
			Name:     "s",
			Kind:     maintenancev1alpha1.StageKindBMCSettings,
			Template: maintenancev1alpha1.StageTemplate{BMCSettings: nil},
		}, 0)
		Expect(err).To(MatchError(ContainSubstring("missing bmcSettings template")))
	})

	It("returns error when BMCVersion template is nil", func() {
		run := minimalRunForUnit("bmc", "srv")
		_, err := r.buildBMCObject(run, &maintenancev1alpha1.PlanStage{
			Name:     "s",
			Kind:     maintenancev1alpha1.StageKindBMCVersion,
			Template: maintenancev1alpha1.StageTemplate{BMCVersion: nil},
		}, 0)
		Expect(err).To(MatchError(ContainSubstring("missing bmcVersion template")))
	})
})

var _ = Describe("buildServerObject (unit)", func() {
	r := &MaintenancePlanRunReconciler{}

	It("returns error for non-Server kind", func() {
		run := minimalRunForUnit("bmc", "srv")
		_, err := r.buildServerObject(run, &maintenancev1alpha1.PlanStage{
			Name: "s", Kind: maintenancev1alpha1.StageKindBMCVersion,
		}, "my-server", 0)
		Expect(err).To(MatchError(ContainSubstring("not Server-scoped")))
	})

	It("builds BIOSSettings correctly", func() {
		run := minimalRunForUnit("my-bmc", "my-srv")
		stage := &maintenancev1alpha1.PlanStage{
			Name: "bios-pre",
			Kind: maintenancev1alpha1.StageKindBIOSSettings,
			Template: maintenancev1alpha1.StageTemplate{
				BIOSSettings: &metalv1alpha1.BIOSSettingsTemplate{
					Version:      "2.5",
					SettingsFlow: []metalv1alpha1.SettingsFlowItem{{Name: "g1", Priority: 1}},
				},
			},
		}
		obj, err := r.buildServerObject(run, stage, "my-srv", 0)
		Expect(err).NotTo(HaveOccurred())
		biosSettings, ok := obj.(*metalv1alpha1.BIOSSettings)
		Expect(ok).To(BeTrue())
		Expect(biosSettings.Name).To(Equal(serverCRName(run.Name, stage.Name, "my-srv")))
		Expect(biosSettings.Spec.Version).To(Equal("2.5"))
		Expect(biosSettings.Spec.ServerRef.Name).To(Equal("my-srv"))
		Expect(biosSettings.Labels[serverNameLabel]).To(Equal("my-srv"))
	})

	It("builds BIOSVersion correctly", func() {
		run := minimalRunForUnit("my-bmc", "my-srv")
		stage := &maintenancev1alpha1.PlanStage{
			Name: "bios-fw",
			Kind: maintenancev1alpha1.StageKindBIOSVersion,
			Template: maintenancev1alpha1.StageTemplate{
				BIOSVersion: &metalv1alpha1.BIOSVersionTemplate{
					Version: "2.5", Image: metalv1alpha1.ImageSpec{URI: "x"},
				},
			},
		}
		obj, err := r.buildServerObject(run, stage, "my-srv", 0)
		Expect(err).NotTo(HaveOccurred())
		biosVersion, ok := obj.(*metalv1alpha1.BIOSVersion)
		Expect(ok).To(BeTrue())
		Expect(biosVersion.Spec.Version).To(Equal("2.5"))
		Expect(biosVersion.Spec.ServerRef.Name).To(Equal("my-srv"))
	})

	It("returns error when BIOSSettings template is nil", func() {
		run := minimalRunForUnit("bmc", "srv")
		_, err := r.buildServerObject(run, &maintenancev1alpha1.PlanStage{
			Name:     "s",
			Kind:     maintenancev1alpha1.StageKindBIOSSettings,
			Template: maintenancev1alpha1.StageTemplate{BIOSSettings: nil},
		}, "srv", 0)
		Expect(err).To(MatchError(ContainSubstring("missing biosSettings template")))
	})

	It("returns error when BIOSVersion template is nil", func() {
		run := minimalRunForUnit("bmc", "srv")
		_, err := r.buildServerObject(run, &maintenancev1alpha1.PlanStage{
			Name:     "s",
			Kind:     maintenancev1alpha1.StageKindBIOSVersion,
			Template: maintenancev1alpha1.StageTemplate{BIOSVersion: nil},
		}, "srv", 0)
		Expect(err).To(MatchError(ContainSubstring("missing biosVersion template")))
	})
})

var _ = Describe("isIntermediateStage (unit)", func() {
	r := &MaintenancePlanRunReconciler{}

	makeRun := func(kinds ...maintenancev1alpha1.StageKind) *maintenancev1alpha1.MaintenancePlanRun {
		run := minimalRunForUnit("bmc", "srv")
		for i, k := range kinds {
			run.Spec.Stages = append(run.Spec.Stages, maintenancev1alpha1.PlanStage{
				Name: fmt.Sprintf("s%d", i), Kind: k,
			})
		}
		return run
	}

	DescribeTable("detects intermediate stages across all kinds",
		func(kinds []maintenancev1alpha1.StageKind, idx int, wantIntermediate bool) {
			run := makeRun(kinds...)
			Expect(r.isIntermediateStage(run, idx)).To(Equal(wantIntermediate))
		},
		Entry("BMCVersion intermediate", []maintenancev1alpha1.StageKind{
			maintenancev1alpha1.StageKindBMCVersion, maintenancev1alpha1.StageKindBMCVersion,
		}, 0, true),
		Entry("BMCVersion final", []maintenancev1alpha1.StageKind{
			maintenancev1alpha1.StageKindBMCVersion, maintenancev1alpha1.StageKindBMCVersion,
		}, 1, false),
		Entry("BIOSVersion intermediate", []maintenancev1alpha1.StageKind{
			maintenancev1alpha1.StageKindBIOSVersion, maintenancev1alpha1.StageKindBIOSVersion,
		}, 0, true),
		Entry("BMCSettings intermediate", []maintenancev1alpha1.StageKind{
			maintenancev1alpha1.StageKindBMCSettings, maintenancev1alpha1.StageKindBMCSettings,
		}, 0, true),
		Entry("BMCSettings final", []maintenancev1alpha1.StageKind{
			maintenancev1alpha1.StageKindBMCSettings, maintenancev1alpha1.StageKindBMCSettings,
		}, 1, false),
		Entry("BIOSSettings intermediate", []maintenancev1alpha1.StageKind{
			maintenancev1alpha1.StageKindBIOSSettings, maintenancev1alpha1.StageKindBIOSSettings,
		}, 0, true),
		Entry("only stage is not intermediate", []maintenancev1alpha1.StageKind{
			maintenancev1alpha1.StageKindBMCVersion,
		}, 0, false),
		Entry("different kinds are not intermediate", []maintenancev1alpha1.StageKind{
			maintenancev1alpha1.StageKindBMCVersion, maintenancev1alpha1.StageKindBIOSVersion,
		}, 0, false),
	)
})

var _ = Describe("shouldSkipBMC (unit)", func() {
	r := &MaintenancePlanRunReconciler{}

	It("returns false when baseline is empty", func() {
		run := minimalRunForUnit("b", "s")
		stage := &maintenancev1alpha1.PlanStage{
			Kind: maintenancev1alpha1.StageKindBMCVersion,
			Template: maintenancev1alpha1.StageTemplate{BMCVersion: &metalv1alpha1.BMCVersionTemplate{
				Version: "7.10", Image: metalv1alpha1.ImageSpec{URI: "x"},
			}},
		}
		skip, _ := r.shouldSkipBMC(run, stage)
		Expect(skip).To(BeFalse())
	})

	It("returns true when target <= baseline", func() {
		run := minimalRunForUnit("b", "s")
		run.Spec.BaselineBMCVersion = "7.10"
		stage := &maintenancev1alpha1.PlanStage{
			Kind: maintenancev1alpha1.StageKindBMCVersion,
			Template: maintenancev1alpha1.StageTemplate{BMCVersion: &metalv1alpha1.BMCVersionTemplate{
				Version: "7.05", Image: metalv1alpha1.ImageSpec{URI: "x"},
			}},
		}
		skip, msg := r.shouldSkipBMC(run, stage)
		Expect(skip).To(BeTrue())
		Expect(msg).To(ContainSubstring("7.10"))
	})

	It("returns false when target > baseline", func() {
		run := minimalRunForUnit("b", "s")
		run.Spec.BaselineBMCVersion = "7.00"
		stage := &maintenancev1alpha1.PlanStage{
			Kind: maintenancev1alpha1.StageKindBMCVersion,
			Template: maintenancev1alpha1.StageTemplate{BMCVersion: &metalv1alpha1.BMCVersionTemplate{
				Version: "7.10", Image: metalv1alpha1.ImageSpec{URI: "x"},
			}},
		}
		skip, _ := r.shouldSkipBMC(run, stage)
		Expect(skip).To(BeFalse())
	})
})

var _ = Describe("shouldSkipServer (unit)", func() {
	r := &MaintenancePlanRunReconciler{}

	It("returns false when server has no baseline", func() {
		run := minimalRunForUnit("b", "s")
		run.Spec.BaselineBIOSVersions = map[string]string{} // no entry for "s"
		stage := &maintenancev1alpha1.PlanStage{
			Kind: maintenancev1alpha1.StageKindBIOSVersion,
			Template: maintenancev1alpha1.StageTemplate{BIOSVersion: &metalv1alpha1.BIOSVersionTemplate{
				Version: "2.5", Image: metalv1alpha1.ImageSpec{URI: "x"},
			}},
		}
		skip, _ := r.shouldSkipServer(run, stage, "s")
		Expect(skip).To(BeFalse())
	})

	It("returns true when target <= baseline for that server", func() {
		run := minimalRunForUnit("b", "s")
		run.Spec.BaselineBIOSVersions = map[string]string{"s": "2.5"}
		stage := &maintenancev1alpha1.PlanStage{
			Kind: maintenancev1alpha1.StageKindBIOSVersion,
			Template: maintenancev1alpha1.StageTemplate{BIOSVersion: &metalv1alpha1.BIOSVersionTemplate{
				Version: "2.0", Image: metalv1alpha1.ImageSpec{URI: "x"},
			}},
		}
		skip, msg := r.shouldSkipServer(run, stage, "s")
		Expect(skip).To(BeTrue())
		Expect(msg).To(ContainSubstring("server s"))
	})

	It("does not skip server-a when only server-b is at target", func() {
		run := minimalRunForUnit("b", "s")
		run.Spec.BaselineBIOSVersions = map[string]string{"srv-b": "2.5"}
		stage := &maintenancev1alpha1.PlanStage{
			Kind: maintenancev1alpha1.StageKindBIOSVersion,
			Template: maintenancev1alpha1.StageTemplate{BIOSVersion: &metalv1alpha1.BIOSVersionTemplate{
				Version: "2.5", Image: metalv1alpha1.ImageSpec{URI: "x"},
			}},
		}
		// srv-a has no baseline → not skipped
		skip, _ := r.shouldSkipServer(run, stage, "srv-a")
		Expect(skip).To(BeFalse())
		// srv-b is at target → skipped
		skip, _ = r.shouldSkipServer(run, stage, "srv-b")
		Expect(skip).To(BeTrue())
	})
})

var _ = Describe("targetBMCVersion (unit)", func() {
	makeRunWithStages := func(stages ...maintenancev1alpha1.PlanStage) *maintenancev1alpha1.MaintenancePlanRun {
		run := minimalRunForUnit("bmc", "srv")
		run.Spec.Stages = stages
		return run
	}

	It("returns empty when no BMCVersion stage exists", func() {
		run := makeRunWithStages(maintenancev1alpha1.PlanStage{
			Kind:     maintenancev1alpha1.StageKindBMCSettings,
			Template: maintenancev1alpha1.StageTemplate{BMCSettings: &maintenancev1alpha1.PlanBMCSettingsTemplate{Version: "1.0"}},
		})
		Expect(targetBMCVersion(run)).To(BeEmpty())
	})

	It("returns the last BMCVersion stage version", func() {
		run := makeRunWithStages(
			maintenancev1alpha1.PlanStage{
				Kind:     maintenancev1alpha1.StageKindBMCVersion,
				Template: maintenancev1alpha1.StageTemplate{BMCVersion: &metalv1alpha1.BMCVersionTemplate{Version: "3.0", Image: metalv1alpha1.ImageSpec{URI: "x"}}},
			},
			maintenancev1alpha1.PlanStage{
				Kind:     maintenancev1alpha1.StageKindBMCVersion,
				Template: maintenancev1alpha1.StageTemplate{BMCVersion: &metalv1alpha1.BMCVersionTemplate{Version: "5.0", Image: metalv1alpha1.ImageSpec{URI: "x"}}},
			},
		)
		Expect(targetBMCVersion(run)).To(Equal("5.0"))
	})
})

var _ = Describe("targetBIOSVersion (unit)", func() {
	makeRunWithStages := func(stages ...maintenancev1alpha1.PlanStage) *maintenancev1alpha1.MaintenancePlanRun {
		run := minimalRunForUnit("bmc", "srv")
		run.Spec.Stages = stages
		return run
	}

	It("returns empty when no BIOSVersion stage exists", func() {
		run := makeRunWithStages(maintenancev1alpha1.PlanStage{
			Kind:     maintenancev1alpha1.StageKindBIOSSettings,
			Template: maintenancev1alpha1.StageTemplate{BIOSSettings: &metalv1alpha1.BIOSSettingsTemplate{Version: "1.0"}},
		})
		Expect(targetBIOSVersion(run)).To(BeEmpty())
	})

	It("returns the last BIOSVersion stage version", func() {
		run := makeRunWithStages(
			maintenancev1alpha1.PlanStage{
				Kind:     maintenancev1alpha1.StageKindBIOSVersion,
				Template: maintenancev1alpha1.StageTemplate{BIOSVersion: &metalv1alpha1.BIOSVersionTemplate{Version: "2.0", Image: metalv1alpha1.ImageSpec{URI: "x"}}},
			},
			maintenancev1alpha1.PlanStage{
				Kind:     maintenancev1alpha1.StageKindBIOSVersion,
				Template: maintenancev1alpha1.StageTemplate{BIOSVersion: &metalv1alpha1.BIOSVersionTemplate{Version: "4.0", Image: metalv1alpha1.ImageSpec{URI: "x"}}},
			},
		)
		Expect(targetBIOSVersion(run)).To(Equal("4.0"))
	})
})
