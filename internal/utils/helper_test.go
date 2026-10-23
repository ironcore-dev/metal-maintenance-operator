// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package utils_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/utils"
)

var _ = Describe("ValidateFieldPathCasing", func() {
	It("rejects segments with uppercase-initial letters", func() {
		Expect(utils.ValidateFieldPathCasing("spec.BMCRef.name")).To(HaveOccurred())
		Expect(utils.ValidateFieldPathCasing("spec.BadField")).To(HaveOccurred())
	})

	It("accepts lowercase-initial segments", func() {
		Expect(utils.ValidateFieldPathCasing("spec.bmcRef.name")).To(Succeed())
		Expect(utils.ValidateFieldPathCasing("metadata.name")).To(Succeed())
	})

	It("tolerates empty segments from leading or doubled dots", func() {
		Expect(utils.ValidateFieldPathCasing(".spec.name")).To(Succeed())
		Expect(utils.ValidateFieldPathCasing("spec..name")).To(Succeed())
	})

	It("exempts bracket map-key syntax regardless of casing", func() {
		Expect(utils.ValidateFieldPathCasing("metadata.labels[topology.kubernetes.io/region]")).To(Succeed())
		Expect(utils.ValidateFieldPathCasing("metadata.annotations[ClusterID]")).To(Succeed())
	})

	It("exempts the segment immediately after annotations or labels in dot notation", func() {
		Expect(utils.ValidateFieldPathCasing("metadata.annotations.ClusterID")).To(Succeed())
		Expect(utils.ValidateFieldPathCasing("metadata.labels.MyLabel")).To(Succeed())
	})
})
