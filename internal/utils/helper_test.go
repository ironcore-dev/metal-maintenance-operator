// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package utils_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ironcore-dev/metal-maintenance-operator/api"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/utils"
)

func TestUtils(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Utils Suite")
}

var _ = Describe("SortedSettingsFlowGroups", func() {
	It("returns items sorted by ascending priority", func() {
		flow := []api.SettingsFlowItem{
			{Name: "c", Priority: 3},
			{Name: "a", Priority: 1},
			{Name: "b", Priority: 2},
		}
		sorted := utils.SortedSettingsFlowGroups(flow)
		Expect(sorted).To(HaveLen(3))
		Expect(sorted[0].Name).To(Equal("a"))
		Expect(sorted[1].Name).To(Equal("b"))
		Expect(sorted[2].Name).To(Equal("c"))
	})

	It("preserves relative order for equal priorities (stable sort)", func() {
		flow := []api.SettingsFlowItem{
			{Name: "x", Priority: 1},
			{Name: "y", Priority: 1},
		}
		sorted := utils.SortedSettingsFlowGroups(flow)
		Expect(sorted[0].Name).To(Equal("x"))
		Expect(sorted[1].Name).To(Equal("y"))
	})

	It("does not modify the original slice", func() {
		flow := []api.SettingsFlowItem{
			{Name: "b", Priority: 2},
			{Name: "a", Priority: 1},
		}
		_ = utils.SortedSettingsFlowGroups(flow)
		Expect(flow[0].Name).To(Equal("b"))
	})
})

var _ = Describe("FilterDiffByFlowItem", func() {
	It("returns the flow item's value for matching diff keys", func() {
		diff := map[string]any{
			"cert":  "final-cert",
			"ldap":  "ldap-url",
			"other": "value",
		}
		item := api.SettingsFlowItem{
			Name:     "cert-step",
			Priority: 1,
			Settings: map[string]string{"cert": "intermediate-cert"},
		}
		filtered := utils.FilterDiffByFlowItem(diff, item)
		Expect(filtered).To(HaveLen(1))
		Expect(filtered).To(HaveKeyWithValue("cert", "intermediate-cert"))
	})

	It("returns empty map when no diff keys match the flow item", func() {
		diff := map[string]any{"unrelated": "value"}
		item := api.SettingsFlowItem{
			Settings: map[string]string{"cert": ""},
		}
		filtered := utils.FilterDiffByFlowItem(diff, item)
		Expect(filtered).To(BeEmpty())
	})

	It("returns empty map when diff is empty", func() {
		item := api.SettingsFlowItem{
			Settings: map[string]string{"cert": ""},
		}
		filtered := utils.FilterDiffByFlowItem(map[string]any{}, item)
		Expect(filtered).To(BeEmpty())
	})
})
