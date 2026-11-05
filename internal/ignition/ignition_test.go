// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package ignition

import (
	"strings"
	"testing"
	"time"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/token"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestIgnition(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Ignition Suite")
}

var _ = Describe("Ignition", func() {
	Context("Sanitization Provider", func() {
		const reportBaseURL = "http://example.org"
		const systemUUID = "38947555-7742-3448-3784-823347823834"

		var signingKey []byte
		var prov *SanitizationProvider

		BeforeEach(func() {
			var err error
			signingKey, err = token.GenerateSigningSecret()
			Expect(err).NotTo(HaveOccurred())
			prov = &SanitizationProvider{
				ReportBaseURL: reportBaseURL,
				SigningKey:    signingKey,
				TokenExpiry:   time.Hour,
			}
		})

		server := &metalv1alpha1.Server{
			ObjectMeta: metav1.ObjectMeta{Name: "test-server"},
			Spec:       metalv1alpha1.ServerSpec{SystemUUID: systemUUID},
		}

		It("should correctly render the ignition", func(ctx SpecContext) {
			data, err := prov.Ignition(ctx, server, "my-uid")
			Expect(err).NotTo(HaveOccurred())
			rendered := string(data)

			Expect(rendered).To(ContainSubstring(`"reportURL": "http://example.org/my-uid"`))
			Expect(rendered).To(ContainSubstring(`"token": "`))
		})

		It("should embed a verifiable JWT token", func(ctx SpecContext) {
			data, err := prov.Ignition(ctx, server, "my-uid")
			Expect(err).NotTo(HaveOccurred())

			rendered := string(data)
			// Extract the token value from the rendered JSON
			idx := strings.Index(rendered, `"token": "`)
			Expect(idx).To(BeNumerically(">=", 0))
			rest := rendered[idx+len(`"token": "`):]
			end := strings.Index(rest, `"`)
			Expect(end).To(BeNumerically(">", 0))
			tok := rest[:end]

			// Verify the token is valid and bound to the correct UUID
			extractedUUID, _, valid, err := token.VerifySignedDiscoveryToken(signingKey, tok)
			Expect(err).NotTo(HaveOccurred())
			Expect(valid).To(BeTrue())
			Expect(extractedUUID).To(Equal(systemUUID))
		})
	})
})
