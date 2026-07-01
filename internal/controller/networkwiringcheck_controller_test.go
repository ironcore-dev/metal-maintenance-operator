// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "sigs.k8s.io/controller-runtime/pkg/envtest/komega"

	readiness "github.com/ironcore-dev/metal-maintenance-operator/api/readiness/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var _ = Describe("NetworkWiringCheck Controller", func() {
	ns := SetupNamespace()

	ctx := context.Background()

	// makeServer creates a minimal Server with the given name and NIC status.
	makeServer := func(name string, nics []metalv1alpha1.NetworkInterface) *metalv1alpha1.Server {
		s := &metalv1alpha1.Server{
			ObjectMeta: metav1.ObjectMeta{
				Name:   name,
				Labels: map[string]string{"test-server": name},
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

	makeCheck := func(server *metalv1alpha1.Server, network readiness.ExpectedNetworkSpec, nameSuffix string) *readiness.NetworkWiringCheck {
		return &readiness.NetworkWiringCheck{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "nwc-" + nameSuffix + "-", Namespace: ns.Name},
			Spec: readiness.NetworkWiringCheckSpec{
				ServerRef: corev1.LocalObjectReference{Name: server.Name},
				ServerSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{"test-server": server.Name},
				},
				Network: network,
			},
		}
	}

	Context("basic lifecycle", func() {
		It("adds a finalizer and creates a ServerReadinessRule", func() {
			server := makeServer("srv-lifecycle-000000000", nil)
			DeferCleanup(k8sClient.Delete, server)

			check := makeCheck(server, readiness.ExpectedNetworkSpec{}, "lifecycle")
			Expect(k8sClient.Create(ctx, check)).To(Succeed())
			DeferCleanup(func() { _ = client.IgnoreNotFound(k8sClient.Delete(ctx, check)) })

			By("expecting the finalizer to be set")
			Eventually(Object(check)).Should(
				HaveField("Finalizers", ContainElement(networkWiringCheckFinalizer)),
			)

			By("expecting a ServerReadinessRule to be created")
			ruleName := networkWiringCheckRuleName(check)
			rule := &metalv1alpha1.ServerReadinessRule{}
			Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKey{Name: ruleName}, rule)
			}, "10s").Should(Succeed())
			Expect(rule.Spec.EnforcementMode).To(Equal(metalv1alpha1.EnforcementModeContinuous))
			Expect(rule.Spec.Taint.Key).To(Equal(networkNotReadyTaintKey))
		})

		It("deletes the ServerReadinessRule and removes the finalizer on deletion", func() {
			server := makeServer("srv-delete-0000000000", nil)
			DeferCleanup(k8sClient.Delete, server)

			check := makeCheck(server, readiness.ExpectedNetworkSpec{}, "delete")
			Expect(k8sClient.Create(ctx, check)).To(Succeed())

			ruleName := networkWiringCheckRuleName(check)
			Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKey{Name: ruleName}, &metalv1alpha1.ServerReadinessRule{})
			}, "10s").Should(Succeed())

			By("deleting the NetworkWiringCheck")
			Expect(k8sClient.Delete(ctx, check)).To(Succeed())

			By("expecting the ServerReadinessRule to be deleted")
			Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKey{Name: ruleName}, &metalv1alpha1.ServerReadinessRule{})
			}, "10s").Should(MatchError(ContainSubstring("not found")))

			By("expecting the NetworkWiringCheck to be gone")
			Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKeyFromObject(check), &readiness.NetworkWiringCheck{})
			}, "10s").Should(MatchError(ContainSubstring("not found")))
		})
	})

	Context("network validation", func() {
		It("marks a server ready when all expected interfaces are present", func() {
			server := makeServer("srv-ready-000000000000", []metalv1alpha1.NetworkInterface{
				{MACAddress: "aa:bb:cc:dd:ee:01", CarrierStatus: "up"},
			})
			DeferCleanup(k8sClient.Delete, server)

			check := makeCheck(server, readiness.ExpectedNetworkSpec{
				Interfaces: []readiness.ExpectedInterface{
					{MACAddress: "aa:bb:cc:dd:ee:01", CarrierStatus: "up"},
				},
			}, "ready")
			Expect(k8sClient.Create(ctx, check)).To(Succeed())
			DeferCleanup(k8sClient.Delete, check)

			Eventually(Object(check)).Should(SatisfyAll(
				HaveField("Status.Ready", BeTrue()),
				HaveField("Status.Mismatches", BeEmpty()),
			))

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

			check := makeCheck(server, readiness.ExpectedNetworkSpec{
				Interfaces: []readiness.ExpectedInterface{
					{MACAddress: "aa:bb:cc:dd:ee:ff"},
				},
			}, "miss")
			Expect(k8sClient.Create(ctx, check)).To(Succeed())
			DeferCleanup(k8sClient.Delete, check)

			Eventually(Object(check)).Should(SatisfyAll(
				HaveField("Status.Ready", BeFalse()),
				HaveField("Status.Mismatches", ContainElement(
					HaveField("Message", ContainSubstring("interface not found")),
				)),
			))

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

			check := makeCheck(server, readiness.ExpectedNetworkSpec{
				Interfaces: []readiness.ExpectedInterface{
					{MACAddress: "aa:bb:cc:00:00:01", CarrierStatus: "up"},
				},
			}, "carrier")
			Expect(k8sClient.Create(ctx, check)).To(Succeed())
			DeferCleanup(k8sClient.Delete, check)

			Eventually(Object(check)).Should(SatisfyAll(
				HaveField("Status.Ready", BeFalse()),
				HaveField("Status.Mismatches", ContainElement(SatisfyAll(
					HaveField("Message", ContainSubstring("carrierStatus")),
					HaveField("Reason", reasonCarrierDown),
				))),
			))
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

			check := makeCheck(server, readiness.ExpectedNetworkSpec{
				Interfaces: []readiness.ExpectedInterface{
					{
						MACAddress: "aa:bb:cc:00:01:01",
						Neighbors: []readiness.ExpectedNeighbor{
							{SystemName: "switch-a", PortID: "Ethernet1"},
							{SystemName: "switch-b", PortID: "Ethernet99"},
						},
					},
				},
			}, "lldp")
			Expect(k8sClient.Create(ctx, check)).To(Succeed())
			DeferCleanup(k8sClient.Delete, check)

			Eventually(Object(check)).Should(SatisfyAll(
				HaveField("Status.Ready", BeFalse()),
				HaveField("Status.Mismatches", ContainElement(SatisfyAll(
					HaveField("Message", ContainSubstring("switch-b")),
					HaveField("Reason", reasonNeighborMismatch),
				))),
			))
		})

		It("sets NoExpectedSpec reason when no interfaces are configured", func() {
			server := makeServer("srv-nospec-00000000000", []metalv1alpha1.NetworkInterface{
				{MACAddress: "aa:bb:cc:00:02:01"},
			})
			DeferCleanup(k8sClient.Delete, server)

			check := makeCheck(server, readiness.ExpectedNetworkSpec{}, "nospec")
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

		It("does nothing when the referenced server does not exist", func() {
			check := &readiness.NetworkWiringCheck{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "nwc-noserver-", Namespace: ns.Name},
				Spec: readiness.NetworkWiringCheckSpec{
					ServerRef: corev1.LocalObjectReference{Name: "nonexistent-server"},
					ServerSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{"test-server": "nonexistent-server"},
					},
				},
			}
			Expect(k8sClient.Create(ctx, check)).To(Succeed())
			DeferCleanup(k8sClient.Delete, check)

			Eventually(Object(check)).Should(
				HaveField("Finalizers", ContainElement(networkWiringCheckFinalizer)),
			)
			Consistently(Object(check), "2s").Should(
				HaveField("Status.Ready", BeFalse()),
			)
		})
	})
})
