// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package readiness

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "sigs.k8s.io/controller-runtime/pkg/envtest/komega"

	readiness "github.com/ironcore-dev/metal-maintenance-operator/api/readiness/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("ServerCheck Controller", func() {
	ns := SetupNamespace()

	ctx := context.Background()

	// makeServer creates a minimal Server with the given name and NIC status.
	makeServer := func(name string, nics []metalv1alpha1.NetworkInterface) *metalv1alpha1.Server {
		s := &metalv1alpha1.Server{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns.Name,
				Labels:    map[string]string{"test-pool": "alpha"},
			},
			Spec: metalv1alpha1.ServerSpec{
				SystemUUID: "aaaaaaaa-0000-0000-0000-" + name,
			},
		}
		Expect(k8sClient.Create(ctx, s)).To(Succeed())
		Eventually(UpdateStatus(s, func() {
			s.Status.NetworkInterfaces = nics
		})).Should(Succeed())
		return s
	}

	Context("basic lifecycle", func() {
		It("adds a finalizer and creates a ServerReadinessRule", func() {
			check := &readiness.ServerCheck{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "sc-", Namespace: ns.Name},
				Spec: readiness.ServerCheckSpec{
					ServerSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"test-pool": "alpha"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, check)).To(Succeed())
			DeferCleanup(func() { _ = client.IgnoreNotFound(k8sClient.Delete(ctx, check)) })

			By("expecting the finalizer to be set")
			Eventually(Object(check)).Should(
				HaveField("Finalizers", ContainElement(serverCheckFinalizer)),
			)

			By("expecting a ServerReadinessRule to be created")
			ruleName := serverCheckRuleName(check)
			rule := &metalv1alpha1.ServerReadinessRule{}
			Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKey{Name: ruleName}, rule)
			}, "10s").Should(Succeed())
			Expect(rule.Spec.EnforcementMode).To(Equal(metalv1alpha1.EnforcementModeContinuous))
			Expect(rule.Spec.Taint.Key).To(Equal(networkNotReadyTaintKey))
		})

		It("deletes the ServerReadinessRule and removes the finalizer on deletion", func() {
			check := &readiness.ServerCheck{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "sc-del-", Namespace: ns.Name},
				Spec: readiness.ServerCheckSpec{
					ServerSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"test-pool": "alpha"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, check)).To(Succeed())

			ruleName := serverCheckRuleName(check)
			Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKey{Name: ruleName}, &metalv1alpha1.ServerReadinessRule{})
			}, "10s").Should(Succeed())

			By("deleting the ServerCheck")
			Expect(k8sClient.Delete(ctx, check)).To(Succeed())

			By("expecting the ServerReadinessRule to be deleted")
			Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKey{Name: ruleName}, &metalv1alpha1.ServerReadinessRule{})
			}, "10s").Should(MatchError(ContainSubstring("not found")))

			By("expecting the ServerCheck to be gone")
			Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKeyFromObject(check), &readiness.ServerCheck{})
			}, "10s").Should(MatchError(ContainSubstring("not found")))
		})
	})

	Context("network validation", func() {
		It("marks a server ready when all expected interfaces are present", func() {
			server := makeServer("srv-ready-000000000000", []metalv1alpha1.NetworkInterface{
				{MACAddress: "aa:bb:cc:dd:ee:01", CarrierStatus: "up"},
			})
			DeferCleanup(k8sClient.Delete, server)

			check := &readiness.ServerCheck{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "sc-ready-", Namespace: ns.Name},
				Spec: readiness.ServerCheckSpec{
					ServerSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"test-pool": "alpha"},
					},
					Network: readiness.ExpectedNetworkSpec{
						Interfaces: []readiness.ExpectedInterface{
							{MACAddress: "aa:bb:cc:dd:ee:01", CarrierStatus: "up"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, check)).To(Succeed())
			DeferCleanup(k8sClient.Delete, check)

			Eventually(Object(check)).Should(
				HaveField("Status.Servers", ContainElement(SatisfyAll(
					HaveField("Name", server.Name),
					HaveField("Ready", BeTrue()),
					HaveField("Mismatches", BeEmpty()),
				))),
			)

			Eventually(Object(server)).Should(
				HaveField("Status.Conditions", ContainElement(SatisfyAll(
					HaveField("Type", networkReadyConditionType),
					HaveField("Status", metav1.ConditionTrue),
					HaveField("Reason", reasonMatch),
				))),
			)
		})

		It("reports a mismatch when an expected interface is missing", func() {
			server := makeServer("srv-nomic-000000000000", []metalv1alpha1.NetworkInterface{
				{MACAddress: "11:22:33:44:55:66"},
			})
			DeferCleanup(k8sClient.Delete, server)

			check := &readiness.ServerCheck{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "sc-miss-", Namespace: ns.Name},
				Spec: readiness.ServerCheckSpec{
					ServerSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"test-pool": "alpha"},
					},
					Network: readiness.ExpectedNetworkSpec{
						Interfaces: []readiness.ExpectedInterface{
							{MACAddress: "aa:bb:cc:dd:ee:ff"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, check)).To(Succeed())
			DeferCleanup(k8sClient.Delete, check)

			Eventually(Object(check)).Should(
				HaveField("Status.Servers", ContainElement(SatisfyAll(
					HaveField("Name", server.Name),
					HaveField("Ready", BeFalse()),
					HaveField("Mismatches", ContainElement(
						HaveField("Message", ContainSubstring("interface not found")),
					)),
				))),
			)

			Eventually(Object(server)).Should(
				HaveField("Status.Conditions", ContainElement(SatisfyAll(
					HaveField("Type", networkReadyConditionType),
					HaveField("Status", metav1.ConditionFalse),
					HaveField("Reason", reasonInterfaceMissing),
				))),
			)
		})

		It("reports a mismatch when carrier status does not match", func() {
			server := makeServer("srv-carrier-00000000000", []metalv1alpha1.NetworkInterface{
				{MACAddress: "aa:bb:cc:00:00:01", CarrierStatus: "down"},
			})
			DeferCleanup(k8sClient.Delete, server)

			check := &readiness.ServerCheck{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "sc-carrier-", Namespace: ns.Name},
				Spec: readiness.ServerCheckSpec{
					ServerSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"test-pool": "alpha"},
					},
					Network: readiness.ExpectedNetworkSpec{
						Interfaces: []readiness.ExpectedInterface{
							{MACAddress: "aa:bb:cc:00:00:01", CarrierStatus: "up"},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, check)).To(Succeed())
			DeferCleanup(k8sClient.Delete, check)

			Eventually(Object(check)).Should(
				HaveField("Status.Servers", ContainElement(SatisfyAll(
					HaveField("Name", server.Name),
					HaveField("Ready", BeFalse()),
					HaveField("Mismatches", ContainElement(SatisfyAll(
						HaveField("Message", ContainSubstring("carrierStatus")),
						HaveField("Reason", reasonCarrierDown),
					))),
				))),
			)
		})

		It("reports a mismatch when an expected LLDP neighbor is missing", func() {
			server := makeServer("srv-lldp-000000000000", []metalv1alpha1.NetworkInterface{
				{
					MACAddress: "aa:bb:cc:00:01:01",
					Neighbors: []metalv1alpha1.LLDPNeighbor{
						{SystemName: "switch-a", PortID: "Ethernet1"},
					},
				},
			})
			DeferCleanup(k8sClient.Delete, server)

			check := &readiness.ServerCheck{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "sc-lldp-", Namespace: ns.Name},
				Spec: readiness.ServerCheckSpec{
					ServerSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"test-pool": "alpha"},
					},
					Network: readiness.ExpectedNetworkSpec{
						Interfaces: []readiness.ExpectedInterface{
							{
								MACAddress: "aa:bb:cc:00:01:01",
								Neighbors: []readiness.ExpectedNeighbor{
									{SystemName: "switch-a", PortID: "Ethernet1"},
									{SystemName: "switch-b", PortID: "Ethernet99"},
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, check)).To(Succeed())
			DeferCleanup(k8sClient.Delete, check)

			Eventually(Object(check)).Should(
				HaveField("Status.Servers", ContainElement(SatisfyAll(
					HaveField("Name", server.Name),
					HaveField("Ready", BeFalse()),
					HaveField("Mismatches", ContainElement(SatisfyAll(
						HaveField("Message", ContainSubstring("switch-b")),
						HaveField("Reason", reasonNeighborMismatch),
					))),
				))),
			)
		})

		It("sets NoExpectedSpec reason when no interfaces are configured", func() {
			server := makeServer("srv-nospec-00000000000", []metalv1alpha1.NetworkInterface{
				{MACAddress: "aa:bb:cc:00:02:01"},
			})
			DeferCleanup(k8sClient.Delete, server)

			check := &readiness.ServerCheck{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "sc-nospec-", Namespace: ns.Name},
				Spec: readiness.ServerCheckSpec{
					ServerSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"test-pool": "alpha"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, check)).To(Succeed())
			DeferCleanup(k8sClient.Delete, check)

			Eventually(Object(server)).Should(
				HaveField("Status.Conditions", ContainElement(SatisfyAll(
					HaveField("Type", networkReadyConditionType),
					HaveField("Status", metav1.ConditionTrue),
					HaveField("Reason", reasonNoExpectedSpec),
				))),
			)
		})

		It("matches zero servers when selector does not match", func() {
			check := &readiness.ServerCheck{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "sc-empty-", Namespace: ns.Name},
				Spec: readiness.ServerCheckSpec{
					ServerSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"nonexistent": "label"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, check)).To(Succeed())
			DeferCleanup(k8sClient.Delete, check)

			Eventually(Object(check)).Should(
				HaveField("Finalizers", ContainElement(serverCheckFinalizer)),
			)
			Consistently(Object(check), "2s").Should(
				HaveField("Status.Servers", BeEmpty()),
			)
		})
	})
})
