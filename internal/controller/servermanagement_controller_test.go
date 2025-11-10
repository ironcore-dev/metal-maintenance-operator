// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package controller

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	. "sigs.k8s.io/controller-runtime/pkg/envtest/komega"

	vendorconsolev1alpha1 "github.com/ironcore-dev/maintenance-operator/api/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("ServerManagement Controller", func() {
	ns := SetupTest()

	serverManagement := &vendorconsolev1alpha1.ServerManagement{}
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
				},
				Spec: metalv1alpha1.BMCSpec{
					EndpointRef: &v1.LocalObjectReference{Name: "foo"},

					BMCSecretRef: v1.LocalObjectReference{
						Name: bmcSecret.Name,
					},
					Protocol: metalv1alpha1.Protocol{
						Name: metalv1alpha1.ProtocolRedfishLocal,
						Port: 8000,
					},
				},
			}
			Expect(k8sClient.Create(ctx, dellBMC)).To(Succeed())

			By("Creating a Server01")
			dellServer = &metalv1alpha1.Server{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node001-bb001",
					Labels: map[string]string{
						"metal.ironcore.dev/Manufacturer": "Dell",
					},
				},
				Spec: metalv1alpha1.ServerSpec{
					UUID:       "38947555-7742-3448-3784-823347823834",
					SystemUUID: "38947555-7742-3448-3784-823347823834",
					BMCRef: &v1.LocalObjectReference{
						Name: dellBMC.Name,
					},
					BMC: &metalv1alpha1.BMCAccess{
						Protocol: metalv1alpha1.Protocol{
							Name: metalv1alpha1.ProtocolRedfishLocal,
							Port: 8000,
						},
						Address: "127.0.0.1",
						BMCSecretRef: v1.LocalObjectReference{
							Name: bmcSecret.Name,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, dellServer)).Should(Succeed())
		})

		AfterEach(func() {
			By("Cleanup the specific resource instance ServerManagement")
			Expect(k8sClient.Delete(ctx, serverManagement)).To(Succeed())
			By("Cleanup the specific resource instance Server")
			Expect(k8sClient.Delete(ctx, dellServer)).To(Succeed())
			By("Cleanup the specific resource instance BMCSecret")
			Expect(k8sClient.Delete(ctx, dellSecret)).To(Succeed())
			By("Cleanup the specific resource instance BMC")
			Expect(k8sClient.Delete(ctx, dellBMC)).To(Succeed())
			By("Cleanup the specific resource instance BMCSecret")
			Expect(k8sClient.Delete(ctx, bmcSecret)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Creating necessary prerequisite resources")
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

			By("Creating a ServerManagement resource")
			serverManagement = &vendorconsolev1alpha1.ServerManagement{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-server-management",
					Namespace: ns.Name,
				},
				Spec: vendorconsolev1alpha1.ServerManagementSpec{
					ServerSelector: metav1.LabelSelector{
						MatchLabels: map[string]string{
							"metal.ironcore.dev/Manufacturer": "Dell",
						},
					},
					ConsoleURL:              "http://127.0.0.1:8000",
					Manufacturer:            "Dell Inc.",
					DellCredentialSecretRef: v1.LocalObjectReference{Name: dellSecret.Name},
				},
			}
			Expect(k8sClient.Create(ctx, serverManagement)).To(Succeed())
			By("Verifying the reconciliation logic")
			Eventually(Object(serverManagement)).Should(SatisfyAll(
				HaveField("Status.TotalServers", int32(1)),
				HaveField("Status.ManagedServers", int32(1)),
			))

			By("Creating a second Server resource")
			secondServer := &metalv1alpha1.Server{
				ObjectMeta: metav1.ObjectMeta{
					Name: "node002-bb001",
					Labels: map[string]string{
						"metal.ironcore.dev/Manufacturer": "Dell",
					},
				},
				Spec: metalv1alpha1.ServerSpec{
					UUID:       "48947555-7742-3448-3784-823347823835",
					SystemUUID: "48947555-7742-3448-3784-823347823835",
					BMCRef: &v1.LocalObjectReference{
						Name: dellBMC.Name,
					},
					BMC: &metalv1alpha1.BMCAccess{
						Protocol: metalv1alpha1.Protocol{
							Name: metalv1alpha1.ProtocolRedfishLocal,
							Port: 8000,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, secondServer)).Should(Succeed())
			By("Verifying the reconciliation logic updates for the second server")
			Eventually(Object(serverManagement)).Should(SatisfyAll(
				HaveField("Status.TotalServers", int32(2)),
				HaveField("Status.ManagedServers", int32(2)),
			))
		})
	})
})
