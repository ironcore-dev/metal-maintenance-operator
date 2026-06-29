// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package maintenance

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/constants"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gstruct"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	. "sigs.k8s.io/controller-runtime/pkg/envtest/komega"
)

var _ = Describe("Server Sanitization Controller", func() {
	It("should sanitize a server after release", func(ctx SpecContext) {
		By("Creating a BMCSecret")
		bmcSecret := &metalv1alpha1.BMCSecret{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "bmc-secret-",
			},
			Data: map[string][]byte{
				metalv1alpha1.BMCSecretUsernameKeyName: []byte("foo"),
				metalv1alpha1.BMCSecretPasswordKeyName: []byte("bar"),
			},
		}
		Expect(k8sClient.Create(ctx, bmcSecret)).To(Succeed())
		DeferCleanup(k8sClient.Delete, bmcSecret)

		By("Creating a Server")
		server := &metalv1alpha1.Server{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "server-",
			},
			Spec: metalv1alpha1.ServerSpec{
				SystemUUID: "38947555-7742-3448-3784-823347823834",
				BMC: &metalv1alpha1.BMCAccess{
					Protocol: metalv1alpha1.Protocol{
						Name: metalv1alpha1.ProtocolRedfishLocal,
						Port: 8000,
					},
					Address: "127.0.0.1",
					BMCSecretRef: corev1.LocalObjectReference{
						Name: bmcSecret.Name,
					},
				},
				ReclaimPolicy: metalv1alpha1.ServerReclaimPolicyRetain,
				ServerClaimRef: &metalv1alpha1.ImmutableObjectReference{
					Namespace: "does-not-exist",
					Name:      "does-not-exist",
				},
			},
		}
		Expect(k8sClient.Create(ctx, server)).To(Succeed())

		By("Patching the server to released state")
		Eventually(UpdateStatus(server, func() {
			server.Status.State = metalv1alpha1.ServerStateReleased
		})).Should(Succeed())

		By("Waiting for the server to report sanitization required & server claim ref removed")
		Eventually(Object(server)).Should(SatisfyAll(
			HaveField("Status.Conditions", ContainElement(gstruct.MatchFields(gstruct.IgnoreExtras, gstruct.Fields{
				"Type":   Equal(conditionTypeSanitized),
				"Status": Equal(metav1.ConditionFalse),
			}))),
			HaveField("Spec.ServerClaimRef", BeNil()),
		))

		By("Updating the server to available state")
		Eventually(UpdateStatus(server, func() {
			server.Status.State = metalv1alpha1.ServerStateAvailable
		})).Should(Succeed())

		By("Waiting for a sanitization claim to appear")
		claimList := &metalv1alpha1.ServerClaimList{}
		Eventually(ObjectList(claimList,
			client.InNamespace(sanitizationNamespace),
			client.MatchingLabels{sanitizationForUIDLabel: string(server.UID)},
		)).Should(HaveField("Items", ConsistOf(
			HaveField("Spec", gstruct.MatchFields(gstruct.IgnoreExtras, gstruct.Fields{
				"Power":             Equal(metalv1alpha1.PowerOn),
				"ServerRef":         Equal(&corev1.LocalObjectReference{Name: server.Name}),
				"Image":             Equal(sanitizationImage),
				"IgnitionSecretRef": Not(BeNil()),
			})))),
		)
		claim := &claimList.Items[0]

		By("Inspecting the claim and the ignition secret to have a shared UUID as name")
		sanitizationUIDUUID, err := uuid.Parse(claim.Name)
		Expect(err).NotTo(HaveOccurred())
		sanitizationUID := sanitizationUIDUUID.String()
		Expect(claim.Spec.IgnitionSecretRef.Name).To(Equal(sanitizationUID))

		By("Getting the corresponding ignition for the claim")
		ignitionSecret := &corev1.Secret{}
		ignitionSecretKey := client.ObjectKey{Namespace: sanitizationNamespace, Name: sanitizationUID}
		Expect(k8sClient.Get(ctx, ignitionSecretKey, ignitionSecret)).To(Succeed())

		By("inspecting the ignition")
		Expect(ignitionSecret.Data).To(
			HaveKeyWithValue("ignition", fmt.Appendf(nil, "%s/%s", server.UID, sanitizationUID)))

		By("Setting the sanitization succeeded label on the claim")
		Eventually(Update(claim, func() {
			claim.Labels[constants.SanitizedLabel] = "true"
		})).Should(Succeed())

		By("Waiting for the server to report sanitized")
		Eventually(Object(server)).Should(SatisfyAll(
			HaveField("Status.Conditions", ContainElement(gstruct.MatchFields(gstruct.IgnoreExtras, gstruct.Fields{
				"Type":   Equal(conditionTypeSanitized),
				"Status": Equal(metav1.ConditionTrue),
			}))),
		))

		By("waiting for the claim & ignition to be deleted")
		Eventually(Get(claim)).Should(Satisfy(apierrors.IsNotFound))
		Eventually(Get(ignitionSecret)).Should(Satisfy(apierrors.IsNotFound))
	})
})
