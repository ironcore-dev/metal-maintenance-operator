// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/constants"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("Sanitized Server", func() {
	It("should patch the claim to say it's sanitized", func(ctx SpecContext) {
		By("creating a claim")
		claim := &metalv1alpha1.ServerClaim{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:    sanitizationNamespace,
				GenerateName: "sanitized-",
			},
			Spec: metalv1alpha1.ServerClaimSpec{
				Power: metalv1alpha1.PowerOn,
				Image: sanitizationImage,
			},
		}
		Expect(k8sClient.Create(ctx, claim)).To(Succeed())
		DeferCleanup(k8sClient.Delete, claim)

		By("posting to the server")
		req, err := http.NewRequest(http.MethodPost, fmt.Sprintf(
			"%s/sanitizations/%s",
			sanitizedServerBaseURL,
			claim.Name,
		), strings.NewReader("{}"))
		Expect(err).NotTo(HaveOccurred())
		res, err := http.DefaultClient.Do(req)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.StatusCode).To(Equal(http.StatusOK))

		By("checking that the claim has been marked as sanitized")
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(claim), claim)).To(Succeed())
		Expect(claim.Labels).To(HaveKeyWithValue(constants.SanitizedLabel, "true"))
	})
})
