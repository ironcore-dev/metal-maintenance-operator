// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package baseboard

import (
	baseboardv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/baseboard/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	. "sigs.k8s.io/controller-runtime/pkg/envtest/komega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("BMCUserRotation Controller", func() {
	_ = SetupTest(nil)

	var bmcObj *metalv1alpha1.BMC
	var bmcSecret *metalv1alpha1.BMCSecret

	BeforeEach(func(ctx SpecContext) {
		By("Creating a BMCSecret")
		bmcSecret = &metalv1alpha1.BMCSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "rotation-test-bmc-secret",
			},
			Data: map[string][]byte{
				"username": []byte("admin"),
				"password": []byte("adminpass"),
			},
		}
		Expect(k8sClient.Create(ctx, bmcSecret)).To(Succeed())
		Eventually(Get(bmcSecret)).Should(Succeed())

		By("Creating a BMC resource")
		bmcObj = &metalv1alpha1.BMC{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "rotation-test-bmc-",
			},
			Spec: metalv1alpha1.BMCSpec{
				Endpoint: &metalv1alpha1.InlineEndpoint{
					IP:         metalv1alpha1.MustParseIP(MockServerIP),
					MACAddress: "23:11:8A:33:CF:EB",
				},
				Protocol: metalv1alpha1.Protocol{
					Name: metalv1alpha1.ProtocolRedfishLocal,
					Port: MockServerPort,
				},
				BMCSecretRef: v1.LocalObjectReference{
					Name: bmcSecret.Name,
				},
			},
		}
		Expect(k8sClient.Create(ctx, bmcObj)).To(Succeed())
		Eventually(Get(bmcObj)).Should(Succeed())
	})

	AfterEach(func() {
		EnsureCleanState()
	})

	It("should execute DualAccount rotation when peer exists", func(ctx SpecContext) {
		By("Creating two OperatorAdmin BMCUsers")
		adminA := &baseboardv1alpha1.BMCUser{
			ObjectMeta: metav1.ObjectMeta{Name: "rotation-admin-a"},
			Spec: baseboardv1alpha1.BMCUserSpec{
				UserName: "rotation-admin-a",
				RoleID:   "Administrator",
				Type:     baseboardv1alpha1.BMCUserTypeOperatorAdmin,
				BMCRef:   &v1.LocalObjectReference{Name: bmcObj.Name},
			},
		}
		adminB := &baseboardv1alpha1.BMCUser{
			ObjectMeta: metav1.ObjectMeta{Name: "rotation-admin-b"},
			Spec: baseboardv1alpha1.BMCUserSpec{
				UserName: "rotation-admin-b",
				RoleID:   "Administrator",
				Type:     baseboardv1alpha1.BMCUserTypeOperatorAdmin,
				BMCRef:   &v1.LocalObjectReference{Name: bmcObj.Name},
			},
		}
		Expect(k8sClient.Create(ctx, adminA)).To(Succeed())
		Expect(k8sClient.Create(ctx, adminB)).To(Succeed())

		By("Waiting for both users to have EffectiveBMCSecretRef")
		Eventually(Object(adminA), "8s").Should(HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())))
		Eventually(Object(adminB), "8s").Should(HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())))

		initialSecretNameA := adminA.Status.EffectiveBMCSecretRef.Name

		By("Creating a DualAccount BMCUserRotation for user A")
		rotation := &baseboardv1alpha1.BMCUserRotation{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "rotation-dual-",
			},
			Spec: baseboardv1alpha1.BMCUserRotationSpec{
				BMCUserRef:    v1.LocalObjectReference{Name: adminA.Name},
				Type:          baseboardv1alpha1.RotationStrategyDualAccount,
				TriggeredAt:   metav1.Now(),
				TriggerReason: baseboardv1alpha1.BMCUserRotationTriggerAnnotation,
			},
		}
		Expect(k8sClient.Create(ctx, rotation)).To(Succeed())

		By("Waiting for rotation to succeed")
		Eventually(Object(rotation), "10s").Should(
			HaveField("Status.Phase", baseboardv1alpha1.BMCUserRotationPhaseSucceeded),
		)

		By("Verifying BMCUser EffectiveBMCSecretRef was updated")
		Eventually(Object(adminA), "4s").Should(
			HaveField("Status.EffectiveBMCSecretRef.Name", Not(Equal(initialSecretNameA))),
		)

		By("Verifying BMC.Spec.BMCSecretRef was updated")
		Eventually(Object(bmcObj), "4s").Should(
			HaveField("Spec.BMCSecretRef.Name", Not(Equal(bmcSecret.Name))),
		)

		By("Cleaning up")
		Expect(k8sClient.Delete(ctx, adminA)).To(Succeed())
		Expect(k8sClient.Delete(ctx, adminB)).To(Succeed())
		secretList := &metalv1alpha1.BMCSecretList{}
		Expect(k8sClient.List(ctx, secretList)).To(Succeed())
		for _, s := range secretList.Items {
			for _, ref := range s.OwnerReferences {
				if ref.UID == adminA.UID || ref.UID == adminB.UID {
					Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &s))).To(Succeed())
				}
			}
		}
	})

	It("should fail DualAccount rotation when no peer exists", func(ctx SpecContext) {
		By("Creating a single OperatorAdmin BMCUser")
		singleAdmin := &baseboardv1alpha1.BMCUser{
			ObjectMeta: metav1.ObjectMeta{Name: "rotation-single-admin"},
			Spec: baseboardv1alpha1.BMCUserSpec{
				UserName: "rotation-single-admin",
				RoleID:   "Administrator",
				Type:     baseboardv1alpha1.BMCUserTypeOperatorAdmin,
				BMCRef:   &v1.LocalObjectReference{Name: bmcObj.Name},
			},
		}
		Expect(k8sClient.Create(ctx, singleAdmin)).To(Succeed())

		By("Waiting for EffectiveBMCSecretRef")
		Eventually(Object(singleAdmin), "8s").Should(HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())))

		By("Creating a DualAccount rotation with no peer")
		rotation := &baseboardv1alpha1.BMCUserRotation{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "rotation-nopeer-",
			},
			Spec: baseboardv1alpha1.BMCUserRotationSpec{
				BMCUserRef:    v1.LocalObjectReference{Name: singleAdmin.Name},
				Type:          baseboardv1alpha1.RotationStrategyDualAccount,
				TriggeredAt:   metav1.Now(),
				TriggerReason: baseboardv1alpha1.BMCUserRotationTriggerAnnotation,
			},
		}
		Expect(k8sClient.Create(ctx, rotation)).To(Succeed())

		By("Waiting for rotation to fail")
		Eventually(Object(rotation), "8s").Should(SatisfyAll(
			HaveField("Status.Phase", baseboardv1alpha1.BMCUserRotationPhaseFailed),
			HaveField("Status.Message", ContainSubstring("No peer OperatorAdmin BMCUser found")),
		))

		By("Cleaning up")
		Expect(k8sClient.Delete(ctx, singleAdmin)).To(Succeed())
		secretList := &metalv1alpha1.BMCSecretList{}
		Expect(k8sClient.List(ctx, secretList)).To(Succeed())
		for _, s := range secretList.Items {
			for _, ref := range s.OwnerReferences {
				if ref.UID == singleAdmin.UID {
					Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &s))).To(Succeed())
				}
			}
		}
	})

	It("should execute SingleAccount rotation", func(ctx SpecContext) {
		By("Creating a non-admin BMCUser")
		readonlyUser := &baseboardv1alpha1.BMCUser{
			ObjectMeta: metav1.ObjectMeta{Name: "rotation-readonly"},
			Spec: baseboardv1alpha1.BMCUserSpec{
				UserName: "rotation-readonly",
				RoleID:   "ReadOnly",
				BMCRef:   &v1.LocalObjectReference{Name: bmcObj.Name},
			},
		}
		Expect(k8sClient.Create(ctx, readonlyUser)).To(Succeed())

		By("Waiting for EffectiveBMCSecretRef")
		Eventually(Object(readonlyUser), "8s").Should(HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())))
		initialSecretName := readonlyUser.Status.EffectiveBMCSecretRef.Name

		By("Creating a SingleAccount rotation")
		rotation := &baseboardv1alpha1.BMCUserRotation{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "rotation-single-",
			},
			Spec: baseboardv1alpha1.BMCUserRotationSpec{
				BMCUserRef:    v1.LocalObjectReference{Name: readonlyUser.Name},
				Type:          baseboardv1alpha1.RotationStrategySingleAccount,
				TriggeredAt:   metav1.Now(),
				TriggerReason: baseboardv1alpha1.BMCUserRotationTriggerAnnotation,
			},
		}
		Expect(k8sClient.Create(ctx, rotation)).To(Succeed())

		By("Waiting for rotation to succeed")
		Eventually(Object(rotation), "10s").Should(
			HaveField("Status.Phase", baseboardv1alpha1.BMCUserRotationPhaseSucceeded),
		)

		By("Verifying EffectiveBMCSecretRef was updated")
		Eventually(Object(readonlyUser), "4s").Should(
			HaveField("Status.EffectiveBMCSecretRef.Name", Not(Equal(initialSecretName))),
		)
		Expect(rotation.Status.NewSecretRef).NotTo(BeNil())

		By("Cleaning up")
		Expect(k8sClient.Delete(ctx, readonlyUser)).To(Succeed())
		secretList := &metalv1alpha1.BMCSecretList{}
		Expect(k8sClient.List(ctx, secretList)).To(Succeed())
		for i := range secretList.Items {
			s := &secretList.Items[i]
			for _, ref := range s.OwnerReferences {
				if ref.UID == readonlyUser.UID {
					Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, s))).To(Succeed())
				}
			}
		}
	})
})
