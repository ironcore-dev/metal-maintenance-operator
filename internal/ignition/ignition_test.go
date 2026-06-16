// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package ignition

import (
	"testing"

	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestIgnition(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Ignition Suite")
}

var _ = Describe("Ignition", func() {
	Context("Sanitization Provider", func() {
		reportBaseURL := "http://example.org"
		prov := &SanitizationProvider{
			ReportBaseURL: reportBaseURL,
		}

		const expectedIgnition = `variant: fcos
version: "1.3.0"
storage:
    files:
      - path: /sanitizer/config
        contents:
            inline: |
                {
                    "reportURL": "http://example.org/my-uid"
                }`

		It("should correctly render the ignition", func(ctx SpecContext) {
			data, err := prov.Ignition(ctx, &metalv1alpha1.Server{}, "my-uid")
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(Equal(expectedIgnition))
		})
	})
})
