// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	discoveryv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/discovery/v1alpha1"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/constants"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	. "sigs.k8s.io/controller-runtime/pkg/envtest/komega"
)

var _ = Describe("ServerDiscoveryReconciler", func() {
	ns := SetupTest()

	It("creates a discovery claim with ignition and toleration for an undiscovered server", func(ctx SpecContext) {
		server := newUndiscoveredServer("38947555-7742-3448-3784-823347823834")
		Expect(k8sClient.Create(ctx, server)).To(Succeed())

		By("waiting for the discovery claim to appear")
		claimList := &metalv1alpha1.ServerClaimList{}
		Eventually(ObjectList(claimList, discoveryClaimListOpts(ns.Name, server)...)).Should(
			HaveField("Items", HaveLen(1)))

		claim := &claimList.Items[0]
		Expect(claim.Name).To(Equal(server.Name))
		Expect(claim.Spec.Image).To(Equal("probe-os"))
		Expect(claim.Spec.Tolerations).To(ContainElement(metalv1alpha1.Toleration{
			Key:      constants.UndiscoveredTaintKey,
			Operator: metalv1alpha1.TolerationOperatorExists,
			Effect:   metalv1alpha1.TaintEffectNoBind,
		}))
		Expect(claim.Spec.IgnitionSecretRef).NotTo(BeNil())

		By("checking the ignition secret exists")
		secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
			Namespace: ns.Name,
			Name:      claim.Spec.IgnitionSecretRef.Name,
		}}
		Eventually(Object(secret)).Should(HaveField("Data", HaveKey("ignition")))

		By("not completing discovery without registry data")
		metadata := &discoveryv1alpha1.Metadata{ObjectMeta: metav1.ObjectMeta{
			Namespace: ns.Name,
			Name:      server.Name,
		}}
		Consistently(Get(metadata)).Should(HaveOccurred())
	})

	It("completes discovery when the agent posts data to the registry", func(ctx SpecContext) {
		server := newUndiscoveredServer("43879b2f-b0d4-4d4f-a9cd-70cf2fee57f1")
		Expect(k8sClient.Create(ctx, server)).To(Succeed())

		By("waiting for the discovery claim to appear")
		claimList := &metalv1alpha1.ServerClaimList{}
		Eventually(ObjectList(claimList, discoveryClaimListOpts(ns.Name, server)...)).Should(
			HaveField("Items", HaveLen(1)))

		By("posting discovery data to the registry")
		now := metav1.Now()
		testRegistry.Store(server.Spec.SystemUUID, discoveryv1alpha1.Metadata{
			Timestamp: &now,
			SystemInfo: discoveryv1alpha1.DMI{
				SystemInformation: discoveryv1alpha1.SystemInformation{
					Manufacturer: "ExampleCorp",
					ProductName:  "EX-1000",
					UUID:         server.Spec.SystemUUID,
				},
			},
			NetworkInterfaces: []discoveryv1alpha1.NetworkInterface{{
				Name:       "eth0",
				MACAddress: "00:11:22:33:44:55",
			}},
		})

		By("checking the Metadata object is created")
		metadata := &discoveryv1alpha1.Metadata{ObjectMeta: metav1.ObjectMeta{
			Namespace: ns.Name,
			Name:      server.Name,
		}}
		Eventually(Object(metadata)).Should(SatisfyAll(
			HaveField("SystemInfo.SystemInformation.ProductName", Equal("EX-1000")),
			HaveField("NetworkInterfaces", HaveLen(1)),
		))

		By("checking the Discovered condition is set on the Server")
		Eventually(Object(server)).Should(HaveField("Status.Conditions", ContainElement(SatisfyAll(
			HaveField("Type", constants.ConditionDiscovered),
			HaveField("Status", metav1.ConditionTrue),
		))))

		By("checking the discovery claim is deleted")
		Eventually(ObjectList(claimList, discoveryClaimListOpts(ns.Name, server)...)).Should(
			HaveField("Items", BeEmpty()))
	})

	It("cleans up leftovers without discovering an untainted server", func(ctx SpecContext) {
		server := &metalv1alpha1.Server{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "test-discovery-"},
			Spec: metalv1alpha1.ServerSpec{
				SystemUUID: "a5c75a3f-5b6e-4b1f-8f9e-2b3f0b7e2f9a",
			},
		}
		Expect(k8sClient.Create(ctx, server)).To(Succeed())

		By("never creating a discovery claim")
		claimList := &metalv1alpha1.ServerClaimList{}
		Consistently(ObjectList(claimList, discoveryClaimListOpts(ns.Name, server)...)).Should(
			HaveField("Items", BeEmpty()))

		By("never creating a Metadata object")
		metadata := &discoveryv1alpha1.Metadata{ObjectMeta: metav1.ObjectMeta{
			Namespace: ns.Name,
			Name:      server.Name,
		}}
		Consistently(Get(metadata)).Should(MatchError(ContainSubstring("not found")))
	})
})

func newUndiscoveredServer(systemUUID string) *metalv1alpha1.Server {
	return &metalv1alpha1.Server{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "test-discovery-"},
		Spec: metalv1alpha1.ServerSpec{
			SystemUUID: systemUUID,
			Taints: []metalv1alpha1.Taint{{
				Key:    constants.UndiscoveredTaintKey,
				Effect: metalv1alpha1.TaintEffectNoBind,
			}},
		},
	}
}

func discoveryClaimListOpts(namespace string, server *metalv1alpha1.Server) []client.ListOption {
	return []client.ListOption{
		client.InNamespace(namespace),
		client.MatchingLabels{constants.DiscoveryForUIDLabel: string(server.UID)},
	}
}
