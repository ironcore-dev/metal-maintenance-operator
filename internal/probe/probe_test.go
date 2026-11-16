// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package probe_test

import (
	"github.com/ironcore-dev/metal-maintenance-operator/internal/registry"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ProbeAgent", func() {
	It("should register its collected data with the registry", func() {
		By("waiting for the agent to post its data")
		var server registry.Server
		Eventually(func(g Gomega) {
			var ok bool
			server, ok = registryServer.Load(systemUUID)
			g.Expect(ok).To(BeTrue())
			g.Expect(server.Timestamp).NotTo(BeNil())
		}).Should(Succeed())

		By("ensuring the collected data is populated")
		Expect(server.NetworkInterfaces).NotTo(BeEmpty())
		Expect(server.CPU).NotTo(BeEmpty())
	})
})
