// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	. "sigs.k8s.io/controller-runtime/pkg/envtest/komega"

	maintenance "github.com/ironcore-dev/maintenance-operator/api/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("Console Controller", func() {
	ns := SetupTest()

	console := &maintenance.Console{}
	dellServer := &metalv1alpha1.Server{}
	dellSecret := &corev1.Secret{}
	dellBMC := &metalv1alpha1.BMC{}
	bmcSecret := &metalv1alpha1.BMCSecret{}

	Context("When reconciling a resource", func() {
		ctx := context.Background()

		BeforeEach(func() {
			By("Creating a BMCSecret")
			bmcSecret = &metalv1alpha1.BMCSecret{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-",
					Namespace:    ns.Name,
				},
				Data: map[string][]byte{
					metalv1alpha1.BMCSecretUsernameKeyName: []byte("foo"),
					metalv1alpha1.BMCSecretPasswordKeyName: []byte("bar"),
				},
			}
			Expect(k8sClient.Create(ctx, bmcSecret)).To(Succeed())

			By("Creating a BMC")
			dellBMC = &metalv1alpha1.BMC{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-bmc-",
					Namespace:    ns.Name,
				},
				Spec: metalv1alpha1.BMCSpec{
					EndpointRef: &corev1.LocalObjectReference{Name: "foo"},

					BMCSecretRef: corev1.LocalObjectReference{
						Name: bmcSecret.Name,
					},
					Protocol: metalv1alpha1.Protocol{
						Name: metalv1alpha1.ProtocolRedfishLocal,
						Port: 8000,
					},
				},
			}
			Expect(k8sClient.Create(ctx, dellBMC)).To(Succeed())

			By("Updating BMC Status with IP address")
			Eventually(UpdateStatus(dellBMC, func() {
				dellBMC.Status.IP = metalv1alpha1.MustParseIP("127.0.0.1")
			})).Should(Succeed())

			By("Creating a Server")
			dellServer = &metalv1alpha1.Server{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "node001-bb001",
					Namespace: ns.Name,
					Labels: map[string]string{
						"metal.ironcore.dev/Manufacturer": "Dell",
					},
				},
				Spec: metalv1alpha1.ServerSpec{
					UUID:       "38947555-7742-3448-3784-823347823834",
					SystemUUID: "38947555-7742-3448-3784-823347823834",
					BMCRef: &corev1.LocalObjectReference{
						Name: dellBMC.Name,
					},
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
				},
			}
			Expect(k8sClient.Create(ctx, dellServer)).Should(Succeed())
		})

		AfterEach(func() {
			By("Cleanup the specific resource instance Console")
			if console.Name != "" {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, console))).To(Succeed())
			}
			By("Cleanup the specific resource instance Server")
			Expect(k8sClient.Delete(ctx, dellServer)).To(Succeed())
			By("Cleanup the specific resource instance Secret")
			if dellSecret.Name != "" {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, dellSecret))).To(Succeed())
			}
			By("Cleanup the specific resource instance BMC")
			Expect(k8sClient.Delete(ctx, dellBMC)).To(Succeed())
			By("Cleanup the specific resource instance BMCSecret")
			Expect(k8sClient.Delete(ctx, bmcSecret)).To(Succeed())
		})

		It("should successfully reconcile the resource", func() {
			By("Creating Console credential secret")
			dellSecret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-bmc-secret",
					Namespace: ns.Name,
				},
				Data: map[string][]byte{
					metalv1alpha1.BMCSecretUsernameKeyName: []byte("admin"),
					metalv1alpha1.BMCSecretPasswordKeyName: []byte("password"),
				},
			}
			Expect(k8sClient.Create(ctx, dellSecret)).To(Succeed())

			By("Creating a Console resource")
			console = &maintenance.Console{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-console",
					Namespace: ns.Name,
				},
				Spec: maintenance.ConsoleSpec{
					ServerSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"metal.ironcore.dev/Manufacturer": "Dell",
						},
					},
					ConsoleURL:             "http://127.0.0.1:8000",
					Manufacturer:           "Dell Inc.",
					BMCCredentialSecretRef: corev1.LocalObjectReference{Name: dellSecret.Name},
				},
			}
			Expect(k8sClient.Create(ctx, console)).To(Succeed())

			By("Verifying the reconciliation creates correct status")
			Eventually(Object(console)).Should(SatisfyAll(
				HaveField("Status.TotalServers", int32(1)),
				HaveField("Status.ManagedServers", int32(1)),
				HaveField("Status.UnmanagedServers", int32(0)),
			))

			By("Creating a second Server resource")
			secondServer := &metalv1alpha1.Server{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "node002-bb001",
					Namespace: ns.Name,
					Labels: map[string]string{
						"metal.ironcore.dev/Manufacturer": "Dell",
					},
				},
				Spec: metalv1alpha1.ServerSpec{
					UUID:       "48947555-7742-3448-3784-823347823835",
					SystemUUID: "48947555-7742-3448-3784-823347823835",
					BMCRef: &corev1.LocalObjectReference{
						Name: dellBMC.Name,
					},
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
				},
			}
			Expect(k8sClient.Create(ctx, secondServer)).Should(Succeed())
			DeferCleanup(k8sClient.Delete, secondServer)

			By("Verifying status updates for the second server")
			Eventually(Object(console)).Should(SatisfyAll(
				HaveField("Status.TotalServers", int32(2)),
				HaveField("Status.ManagedServers", int32(2)),
				HaveField("Status.UnmanagedServers", int32(0)),
			))
		})
	})

	Context("When handling edge cases", func() {
		ctx := context.Background()

		It("should handle empty server selector", func() {
			By("Creating Console credential secret")
			emptySecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "empty-secret-",
					Namespace:    ns.Name,
				},
				Data: map[string][]byte{
					"username": []byte("admin"),
					"password": []byte("password"),
				},
			}
			Expect(k8sClient.Create(ctx, emptySecret)).To(Succeed())
			DeferCleanup(k8sClient.Delete, emptySecret)

			By("Creating a Console with selector matching no servers")
			emptyConsole := &maintenance.Console{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "empty-console-",
					Namespace:    ns.Name,
				},
				Spec: maintenance.ConsoleSpec{
					ServerSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"nonexistent": "label",
						},
					},
					ConsoleURL:             "http://127.0.0.1:8000",
					Manufacturer:           "Dell Inc.",
					BMCCredentialSecretRef: corev1.LocalObjectReference{Name: emptySecret.Name},
				},
			}
			Expect(k8sClient.Create(ctx, emptyConsole)).To(Succeed())
			DeferCleanup(k8sClient.Delete, emptyConsole)

			By("Verifying the Console status shows zero servers")
			Eventually(Object(emptyConsole)).Should(SatisfyAll(
				HaveField("Status.TotalServers", int32(0)),
				HaveField("Status.ManagedServers", int32(0)),
				HaveField("Status.UnmanagedServers", int32(0)),
			))
		})

		It("should handle Console with missing credential secret", func() {
			By("Creating a Console referencing non-existent secret")
			noSecretConsole := &maintenance.Console{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "no-secret-console-",
					Namespace:    ns.Name,
				},
				Spec: maintenance.ConsoleSpec{
					ServerSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"test": "label",
						},
					},
					ConsoleURL:             "http://127.0.0.1:8000",
					Manufacturer:           "Dell Inc.",
					BMCCredentialSecretRef: corev1.LocalObjectReference{Name: "nonexistent-secret"},
				},
			}
			Expect(k8sClient.Create(ctx, noSecretConsole)).To(Succeed())
			DeferCleanup(k8sClient.Delete, noSecretConsole)

			By("Verifying the Console status remains at zero due to secret error")
			Consistently(Object(noSecretConsole), "2s").Should(SatisfyAll(
				HaveField("Status.TotalServers", int32(0)),
				HaveField("Status.ManagedServers", int32(0)),
			))
		})
	})
})
