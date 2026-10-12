// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/ironcore-dev/metal-maintenance-operator/api"
	baseboardv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/baseboard/v1alpha1"
	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/maintenance/v1alpha1"
)

var _ = Describe("BMCSettingsSet Webhook", func() {
	It("should deny creation when a fieldRef uses Go struct capitalisation", func(ctx SpecContext) {
		By("Creating a BMCSettingsSet with spec.BMCRef.name (wrong casing)")
		bad := &baseboardv1alpha1.BMCSettingsSet{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "test-set-bad-casing-"},
			Spec: baseboardv1alpha1.BMCSettingsSetSpec{
				BMCSelector: metav1.LabelSelector{},
				BMCSettingsTemplate: baseboardv1alpha1.BMCSettingsTemplate{
					SettingsTemplate: api.SettingsTemplate{
						Version:                 "1.0",
						ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
						Variables: []api.Variable{
							{
								Key: "BmcName",
								ValueFrom: &api.VariableSourceValueFrom{
									FieldRef: &api.FieldRefSelector{
										FieldPath: "spec.BMCRef.name",
									},
								},
							},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, bad)).To(
			MatchError(ContainSubstring("uses a Go struct field name")),
		)
	})

	It("should deny creation when an objectFieldRef uses Go struct capitalisation", func(ctx SpecContext) {
		By("Creating a BMCSettingsSet with objectFieldRef.fieldPath spec.BMCRef.name (wrong casing)")
		bad := &baseboardv1alpha1.BMCSettingsSet{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "test-set-bad-casing-obj-"},
			Spec: baseboardv1alpha1.BMCSettingsSetSpec{
				BMCSelector: metav1.LabelSelector{},
				BMCSettingsTemplate: baseboardv1alpha1.BMCSettingsTemplate{
					SettingsTemplate: api.SettingsTemplate{
						Version:                 "1.0",
						ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
						Variables: []api.Variable{
							{
								Key: "BmcName",
								ValueFrom: &api.VariableSourceValueFrom{
									ObjectFieldRef: &api.ObjectFieldRefSelector{
										Kind:      "BMC",
										Name:      "some-bmc",
										FieldPath: "spec.BMCRef.name",
									},
								},
							},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, bad)).To(
			MatchError(ContainSubstring("uses a Go struct field name")),
		)
	})

	It("should allow creation when fieldRef uses correct JSON casing", func(ctx SpecContext) {
		By("Creating a BMCSettingsSet with spec.bmcRef.name (correct casing)")
		good := &baseboardv1alpha1.BMCSettingsSet{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "test-set-good-casing-"},
			Spec: baseboardv1alpha1.BMCSettingsSetSpec{
				BMCSelector: metav1.LabelSelector{},
				BMCSettingsTemplate: baseboardv1alpha1.BMCSettingsTemplate{
					SettingsTemplate: api.SettingsTemplate{
						Version:                 "1.0",
						ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
						Variables: []api.Variable{
							{
								Key: "BmcName",
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
		Expect(k8sClient.Create(ctx, good)).To(Succeed())
	})

	It("should allow update when fieldRef uses correct JSON casing", func(ctx SpecContext) {
		By("Creating a valid BMCSettingsSet with no variables")
		obj := &baseboardv1alpha1.BMCSettingsSet{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "test-set-update-good-"},
			Spec: baseboardv1alpha1.BMCSettingsSetSpec{
				BMCSelector: metav1.LabelSelector{},
				BMCSettingsTemplate: baseboardv1alpha1.BMCSettingsTemplate{
					SettingsTemplate: api.SettingsTemplate{
						Version:                 "1.0",
						ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())

		By("Updating with a correctly-cased fieldRef")
		obj.Spec.BMCSettingsTemplate.Variables = []api.Variable{
			{
				Key: "BmcName",
				ValueFrom: &api.VariableSourceValueFrom{
					FieldRef: &api.FieldRefSelector{FieldPath: "spec.bmcRef.name"},
				},
			},
		}
		Expect(k8sClient.Update(ctx, obj)).To(Succeed())
	})

	It("should deny update when a fieldRef introduces Go struct capitalisation", func(ctx SpecContext) {
		By("Creating a valid BMCSettingsSet")
		obj := &baseboardv1alpha1.BMCSettingsSet{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "test-set-update-bad-"},
			Spec: baseboardv1alpha1.BMCSettingsSetSpec{
				BMCSelector: metav1.LabelSelector{},
				BMCSettingsTemplate: baseboardv1alpha1.BMCSettingsTemplate{
					SettingsTemplate: api.SettingsTemplate{
						Version:                 "1.0",
						ServerMaintenancePolicy: maintenancev1alpha1.ServerMaintenancePolicyEnforced,
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, obj)).To(Succeed())

		By("Updating with a badly-cased fieldRef")
		obj.Spec.BMCSettingsTemplate.Variables = []api.Variable{
			{
				Key: "BmcName",
				ValueFrom: &api.VariableSourceValueFrom{
					FieldRef: &api.FieldRefSelector{FieldPath: "spec.BMCRef.name"},
				},
			},
		}
		Expect(k8sClient.Update(ctx, obj)).To(
			MatchError(ContainSubstring("uses a Go struct field name")),
		)
	})
})
