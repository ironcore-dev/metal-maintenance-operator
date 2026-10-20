// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package baseboard

import (
	"time"

	baseboardv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/baseboard/v1alpha1"
	utils "github.com/ironcore-dev/metal-maintenance-operator/internal/utils"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	. "sigs.k8s.io/controller-runtime/pkg/envtest/komega"
)

var _ = Describe("BMCUser Controller", func() {
	_ = SetupTest(nil)

	var bmcObj *metalv1alpha1.BMC
	var bmcSecret *metalv1alpha1.BMCSecret

	BeforeEach(func(ctx SpecContext) {
		By("Creating a BMCSecret for the User")
		bmcSecret = &metalv1alpha1.BMCSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-user-secret",
			},
			Data: map[string][]byte{
				"username": []byte("admin"),
				"password": []byte("adminpass"),
			},
		}
		Expect(k8sClient.Create(ctx, bmcSecret)).To(Succeed())
		By("Ensuring that the BMCSecret has been created")
		Eventually(Get(bmcSecret)).Should(Succeed())

		By("Creating a BMC resource")
		bmcObj = &metalv1alpha1.BMC{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-bmc-",
			},
			Spec: metalv1alpha1.BMCSpec{
				Endpoint: &metalv1alpha1.InlineEndpoint{
					IP:         metalv1alpha1.MustParseIP(MockServerIP),
					MACAddress: "23:11:8A:33:CF:EA",
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
		By("Ensuring that the BMC resource has been created")
		Eventually(Get(bmcObj)).Should(Succeed())
	})

	AfterEach(func() {
		EnsureCleanState()
	})

	It("should create a BMC user and secret", func(ctx SpecContext) {
		By("Creating a User resource")
		user := &baseboardv1alpha1.BMCUser{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-user",
			},
			Spec: baseboardv1alpha1.BMCUserSpec{
				UserName: "user",
				RoleID:   "ReadOnly",
				BMCRef: &v1.LocalObjectReference{
					Name: bmcObj.Name,
				},
			},
		}
		Expect(k8sClient.Create(ctx, user)).To(Succeed())
		By("Ensuring that the User resource has been created")
		Eventually(Get(user)).Should(Succeed())

		By("Ensuring that the User resource has been patched with the BMC secret reference")
		Eventually(Object(user)).Should(SatisfyAll(
			HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())),
		))

		By("Ensuring the effective bmcSecret has been created")
		effectiveSecret := &metalv1alpha1.BMCSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name: user.Status.EffectiveBMCSecretRef.Name,
			},
		}
		Eventually(Get(effectiveSecret)).Should(Succeed())
		Eventually(Object(effectiveSecret)).Should(SatisfyAll(
			HaveField("OwnerReferences", ContainElement(metav1.OwnerReference{
				APIVersion:         "baseboard.metal.ironcore.dev/v1alpha1",
				Kind:               "BMCUser",
				Name:               user.Name,
				UID:                user.UID,
				Controller:         new(true),
				BlockOwnerDeletion: new(true),
			})),
		))

		By("Ensuring the effective bmcSecret has the correct data")
		Expect(effectiveSecret.Data).To(HaveKeyWithValue("username", []byte("user")))
		password := string(effectiveSecret.Data["password"])
		// The redfish mock server reports MaxPasswordLength=30. The generator leaves a
		// margin below the max and may shrink further so the HTML-escaped password stays
		// within the limit (see utils.GenerateHTMLSafePassword), so assert the bound
		// rather than an exact length.
		Expect(utils.EscapedLen(password)).To(BeNumerically("<=", 30))
		Expect(len(password)).To(BeNumerically(">=", 8))
		Expect(len(password)).To(BeNumerically("<=", 30))

		By("Deleting the User resource and ensuring the effective secret is deleted")

		Expect(k8sClient.Delete(ctx, user)).To(Succeed())
		By("Ensuring that the User resource has been deleted")
		Eventually(Get(user)).Should(Satisfy(apierrors.IsNotFound))
		By("Ensuring that the effective BMCSecret has been deleted")
		Expect(k8sClient.Delete(ctx, effectiveSecret)).To(Succeed())
		Eventually(Get(effectiveSecret)).Should(Satisfy(apierrors.IsNotFound))
	})

	It("should not apply a user-supplied password that exceeds the BMC length limit", func(ctx SpecContext) {
		By("Creating a BMCSecret with an over-length password (mock MaxPasswordLength is 30)")
		longSecret := &metalv1alpha1.BMCSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "over-length-secret",
			},
			Data: map[string][]byte{
				"username": []byte("longpwuser"),
				"password": []byte("0000000000000000000000000000000000000000"), // 40 chars > 30
			},
		}
		Expect(k8sClient.Create(ctx, longSecret)).To(Succeed())
		Eventually(Get(longSecret)).Should(Succeed())

		By("Creating a User resource referencing the over-length secret")
		user := &baseboardv1alpha1.BMCUser{
			ObjectMeta: metav1.ObjectMeta{
				Name: "over-length-user",
			},
			Spec: baseboardv1alpha1.BMCUserSpec{
				UserName: "longpwuser",
				RoleID:   "ReadOnly",
				BMCSecretRef: &v1.LocalObjectReference{
					Name: longSecret.Name,
				},
				BMCRef: &v1.LocalObjectReference{
					Name: bmcObj.Name,
				},
			},
		}
		Expect(k8sClient.Create(ctx, user)).To(Succeed())
		Eventually(Get(user)).Should(Succeed())

		By("Ensuring the PasswordValid condition is set to False")
		Eventually(func(g Gomega) {
			fetched := &baseboardv1alpha1.BMCUser{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: user.Name}, fetched)).To(Succeed())
			cond := apimeta.FindStatusCondition(fetched.Status.Conditions, "PasswordValid")
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(cond.Reason).To(Equal("PasswordInvalid"))
		}, "5s", "250ms").Should(Succeed())

		By("Ensuring the BMC account was not created")
		Expect(mockServers[0].GetAccountNames()).ShouldNot(HaveKey("longpwuser"))

		Expect(k8sClient.Delete(ctx, user)).To(Succeed())
		Eventually(Get(user)).Should(Satisfy(apierrors.IsNotFound))
		Expect(k8sClient.Delete(ctx, longSecret)).To(Succeed())
	})

	It("should not apply an updated user-supplied password that exceeds the BMC length limit", func(ctx SpecContext) {
		By("Creating a BMCSecret with a valid password")
		validSecret := &metalv1alpha1.BMCSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "update-valid-secret",
			},
			Data: map[string][]byte{
				"username": []byte("updatepwuser"),
				"password": []byte("validpass123"),
			},
		}
		Expect(k8sClient.Create(ctx, validSecret)).To(Succeed())
		Eventually(Get(validSecret)).Should(Succeed())

		By("Creating a User resource referencing the valid secret")
		user := &baseboardv1alpha1.BMCUser{
			ObjectMeta: metav1.ObjectMeta{
				Name: "update-pw-user",
			},
			Spec: baseboardv1alpha1.BMCUserSpec{
				UserName: "updatepwuser",
				RoleID:   "ReadOnly",
				BMCSecretRef: &v1.LocalObjectReference{
					Name: validSecret.Name,
				},
				BMCRef: &v1.LocalObjectReference{
					Name: bmcObj.Name,
				},
			},
		}
		Expect(k8sClient.Create(ctx, user)).To(Succeed())
		Eventually(Get(user)).Should(Succeed())

		By("Ensuring the account was created and the credential became effective")
		Eventually(mockServers[0].GetAccountNames, "5s").Should(HaveKey("updatepwuser"))
		Eventually(Object(user), "5s").Should(HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())))

		By("Creating an over-length BMCSecret and pointing the User at it")
		longSecret := &metalv1alpha1.BMCSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "update-over-length-secret",
			},
			Data: map[string][]byte{
				"username": []byte("updatepwuser"),
				"password": []byte("0000000000000000000000000000000000000000"), // 40 chars > 30
			},
		}
		Expect(k8sClient.Create(ctx, longSecret)).To(Succeed())
		Eventually(Get(longSecret)).Should(Succeed())
		Eventually(Update(user, func() {
			user.Spec.BMCSecretRef = &v1.LocalObjectReference{Name: longSecret.Name}
		})).Should(Succeed())

		By("Ensuring the PasswordValid condition flips to False")
		Eventually(func(g Gomega) {
			fetched := &baseboardv1alpha1.BMCUser{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: user.Name}, fetched)).To(Succeed())
			cond := apimeta.FindStatusCondition(fetched.Status.Conditions, "PasswordValid")
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(cond.Reason).To(Equal("PasswordInvalid"))
		}, "5s", "250ms").Should(Succeed())

		By("Ensuring the BMC account password was not changed to the over-length value")
		Consistently(func() bool {
			return mockServers[0].Authenticate("updatepwuser", "validpass123")
		}, "1s").Should(BeTrue())

		Expect(k8sClient.Delete(ctx, user)).To(Succeed())
		Eventually(Get(user)).Should(Satisfy(apierrors.IsNotFound))
		Expect(k8sClient.Delete(ctx, validSecret)).To(Succeed())
		Expect(k8sClient.Delete(ctx, longSecret)).To(Succeed())
	})

	It("should just create additional BMC users", func(ctx SpecContext) {
		user01 := &baseboardv1alpha1.BMCUser{
			ObjectMeta: metav1.ObjectMeta{
				Name: "user01",
			},
			Spec: baseboardv1alpha1.BMCUserSpec{
				UserName: "user01",
				RoleID:   "Readonly",
				BMCRef: &v1.LocalObjectReference{
					Name: bmcObj.Name,
				},
			},
		}
		Expect(k8sClient.Create(ctx, user01)).To(Succeed())
		Eventually(Get(user01)).Should(Succeed())
		By("Ensuring that the User resource has EffectiveBMCSecretRef")
		Eventually(Object(user01), "4s").Should(SatisfyAll(
			HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())),
			HaveField("Status.ID", Not(BeEmpty())),
		))
		By("Ensuring that the BMCSecret has been created")
		effectiveSecret := &metalv1alpha1.BMCSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name: user01.Status.EffectiveBMCSecretRef.Name,
			},
		}
		Eventually(Get(effectiveSecret)).Should(Succeed())
		Expect(effectiveSecret.Data).To(HaveKeyWithValue("username", []byte("user01")))
		Expect(effectiveSecret.Data).To(HaveKeyWithValue("password", Not(BeEmpty())))

		By("Creating a second user with the same BMCRef")
		user02 := &baseboardv1alpha1.BMCUser{
			ObjectMeta: metav1.ObjectMeta{
				Name: "user02",
			},
			Spec: baseboardv1alpha1.BMCUserSpec{
				UserName: "user02",
				RoleID:   "Readonly",
				BMCRef: &v1.LocalObjectReference{
					Name: bmcObj.Name,
				},
			},
		}
		Expect(k8sClient.Create(ctx, user02)).To(Succeed())
		Eventually(Get(user02)).Should(Succeed())
		By("Ensuring that the User resource has EffectiveBMCSecretRef")
		Eventually(Object(user02)).Should(SatisfyAll(
			HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())),
			HaveField("Status.ID", Not(BeEmpty())),
		))
		By("Ensuring that the BMCSecret has been created")
		effectiveSecret02 := &metalv1alpha1.BMCSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name: user02.Status.EffectiveBMCSecretRef.Name,
			},
		}
		Eventually(Get(effectiveSecret02)).Should(Succeed())

		user03Secret := metalv1alpha1.BMCSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "user03-secret",
			},
			Data: map[string][]byte{
				"username": []byte("user03"),
				"password": []byte("userpass"),
			},
		}

		By("Creating a BMCSecret for the third User")
		Expect(k8sClient.Create(ctx, &user03Secret)).To(Succeed())
		Eventually(Get(&user03Secret)).Should(Succeed())

		By("Creating a third user with a pre-existing BMCSecretRef")
		user03 := &baseboardv1alpha1.BMCUser{
			ObjectMeta: metav1.ObjectMeta{
				Name: "user03",
			},
			Spec: baseboardv1alpha1.BMCUserSpec{
				UserName: "user03",
				RoleID:   "Readonly",
				BMCRef: &v1.LocalObjectReference{
					Name: bmcObj.Name,
				},
				BMCSecretRef: &v1.LocalObjectReference{
					Name: user03Secret.Name,
				},
			},
		}
		Expect(k8sClient.Create(ctx, user03)).To(Succeed())
		Eventually(Get(user03)).Should(Succeed())
		By("Ensuring that the User resource has EffectiveBMCSecretRef")
		Eventually(Object(user03)).Should(SatisfyAll(
			HaveField("Status.EffectiveBMCSecretRef", Equal(&v1.LocalObjectReference{
				Name: user03Secret.Name,
			})),
			HaveField("Status.ID", Not(BeEmpty())),
		))
		Expect(k8sClient.Delete(ctx, user01)).To(Succeed())
		Expect(k8sClient.Delete(ctx, user02)).To(Succeed())
		Expect(k8sClient.Delete(ctx, user03)).To(Succeed())
		Expect(k8sClient.Delete(ctx, &user03Secret)).To(Succeed())
		Expect(k8sClient.Delete(ctx, effectiveSecret)).To(Succeed())
		Expect(k8sClient.Delete(ctx, effectiveSecret02)).To(Succeed())
	})

	It("should rotate password if rotationPeriod is set", func(ctx SpecContext) {
		adminUser := &baseboardv1alpha1.BMCUser{
			ObjectMeta: metav1.ObjectMeta{
				Name: "admin-user",
			},
			Spec: baseboardv1alpha1.BMCUserSpec{
				UserName: "admin-user",
				RoleID:   "Administrator",
				BMCRef: &v1.LocalObjectReference{
					Name: bmcObj.Name,
				},
				RotationPeriod: &metav1.Duration{
					Duration: 1 * time.Second,
				},
			},
		}
		By("Creating a User resource")
		Expect(k8sClient.Create(ctx, adminUser)).To(Succeed())
		By("Ensuring that the User resource has been created")
		Eventually(Get(adminUser)).Should(Succeed())

		By("Ensuring that a new secret with new password has been rotated and set to EffectiveBMCSecretRef")
		Eventually(Object(adminUser), "4s").Should(SatisfyAll(
			HaveField("Status.LastRotation", Not(BeNil())),
			HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())),
			HaveField("Status.EffectiveBMCSecretRef.Name", Not(Equal(bmcSecret.Name))),
		))
		newSecret := &metalv1alpha1.BMCSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name: adminUser.Status.EffectiveBMCSecretRef.Name,
			},
		}
		Eventually(Get(newSecret)).Should(Succeed())
		Expect(newSecret.Data).To(Not(HaveKeyWithValue("password", []byte("bar"))))
		Expect(k8sClient.Delete(ctx, adminUser)).To(Succeed())
		// The 1s rotation period may trigger additional rotations that create
		// extra BMCSecrets before we can delete. Clean up all remaining secrets
		// owned by this user since envtest has no garbage collector.
		secretList := &metalv1alpha1.BMCSecretList{}
		Expect(k8sClient.List(ctx, secretList)).To(Succeed())
		for _, s := range secretList.Items {
			for _, ref := range s.OwnerReferences {
				if ref.UID == adminUser.UID {
					Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &s))).To(Succeed())
				}
			}
		}
	})

	It("should delete BMC user and secret on User deletion", func(ctx SpecContext) {
		mockServers[0].ResetAccounts()
		By("Creating a User resource")
		user := &baseboardv1alpha1.BMCUser{
			ObjectMeta: metav1.ObjectMeta{
				Name: "delete-user",
			},
			Spec: baseboardv1alpha1.BMCUserSpec{
				UserName: "deleteUser",
				RoleID:   "ReadOnly",
				BMCRef: &v1.LocalObjectReference{
					Name: bmcObj.Name,
				},
			},
		}
		Expect(k8sClient.Create(ctx, user)).To(Succeed())
		By("Ensuring that the User resource has been created")
		Eventually(Get(user)).Should(Succeed())
		Eventually(mockServers[0].GetAccountNames).Should(HaveKey("deleteUser"))

		By("Ensuring that the User resource has been patched with the BMC secret reference")
		Eventually(Object(user)).Should(SatisfyAll(
			HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())),
		))

		By("Ensuring the effective bmcSecret has been created")
		effectiveSecret := &metalv1alpha1.BMCSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name: user.Status.EffectiveBMCSecretRef.Name,
			},
		}
		Eventually(Get(effectiveSecret)).Should(Succeed())

		By("Deleting the User resource")
		Expect(k8sClient.Delete(ctx, user)).To(Succeed())

		By("Ensuring that the User resource has been deleted")
		Eventually(Get(user)).ShouldNot(Succeed())

		By("Ensuring that the BMC user has been deleted")
		Eventually(mockServers[0].GetAccountNames).ShouldNot(HaveKey("deleteUser"))

		By("Ensuring that the effective BMCSecret has been deleted")
		Expect(k8sClient.Delete(ctx, effectiveSecret)).To(Succeed())
	})

	It("should rotate password if OperationAnnotationRotateCredentials is set", func(ctx SpecContext) {
		By("Creating a User resource")
		user := &baseboardv1alpha1.BMCUser{
			ObjectMeta: metav1.ObjectMeta{
				Name: "annotated-user",
			},
			Spec: baseboardv1alpha1.BMCUserSpec{
				UserName: "annotated-user",
				RoleID:   "ReadOnly",
				BMCRef: &v1.LocalObjectReference{
					Name: bmcObj.Name,
				},
			},
		}
		Expect(k8sClient.Create(ctx, user)).To(Succeed())
		By("Ensuring that the User resource has been created")
		Eventually(Get(user)).Should(Succeed())

		By("Ensuring that the User resource has EffectiveBMCSecretRef")
		Eventually(Object(user)).Should(SatisfyAll(
			HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())),
		))

		initialSecretName := ""
		By("Getting the initial effective secret name")
		Eventually(Object(user)).Should(WithTransform(func(u *baseboardv1alpha1.BMCUser) string {
			initialSecretName = u.Status.EffectiveBMCSecretRef.Name
			return initialSecretName
		}, Not(BeEmpty())))

		By("Adding the rotation annotation to the User resource")
		Eventually(Object(user)).Should(SatisfyAll(
			HaveField("ObjectMeta.Annotations", BeNil()),
		))
		Eventually(Update(user, func() {
			user.Annotations = map[string]string{
				metalv1alpha1.OperationAnnotation: metalv1alpha1.OperationAnnotationRotateCredentials,
			}
		})).Should(Succeed())

		By("Ensuring that a new secret with new password has been rotated and set to EffectiveBMCSecretRef")
		Eventually(Object(user), "4s").Should(SatisfyAll(
			HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())),
			HaveField("Status.EffectiveBMCSecretRef.Name", Not(Equal(initialSecretName))),
		))

		newSecret := &metalv1alpha1.BMCSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name: user.Status.EffectiveBMCSecretRef.Name,
			},
		}
		Eventually(Get(newSecret)).Should(Succeed())

		By("Checking that the rotation annotation has been removed")
		Eventually(Object(user)).Should(SatisfyAll(
			HaveField("ObjectMeta.Annotations", BeNil()),
		))

		Expect(k8sClient.Delete(ctx, user)).To(Succeed())
		Expect(k8sClient.Delete(ctx, newSecret)).To(Succeed())
		Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &metalv1alpha1.BMCSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name: initialSecretName,
			},
		}))).To(Succeed())
	})

	It("should create a Failed BMCUserRotation when only one OperatorAdmin BMCUser exists for a BMC", func(ctx SpecContext) {
		By("Creating a single OperatorAdmin BMCUser with a short rotation period")
		singleAdmin := &baseboardv1alpha1.BMCUser{
			ObjectMeta: metav1.ObjectMeta{
				Name: "single-operator-admin",
			},
			Spec: baseboardv1alpha1.BMCUserSpec{
				UserName: "single-admin",
				RoleID:   "Administrator",
				Type:     baseboardv1alpha1.BMCUserTypeOperatorAdmin,
				BMCRef: &v1.LocalObjectReference{
					Name: bmcObj.Name,
				},
				RotationPeriod: &metav1.Duration{
					Duration: 1 * time.Second,
				},
			},
		}
		Expect(k8sClient.Create(ctx, singleAdmin)).To(Succeed())
		Eventually(Get(singleAdmin)).Should(Succeed())

		By("Waiting for EffectiveBMCSecretRef to be set (initial setup still works)")
		Eventually(Object(singleAdmin), "4s").Should(SatisfyAll(
			HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())),
		))
		initialSecretName := singleAdmin.Status.EffectiveBMCSecretRef.Name

		By("Waiting for a BMCUserRotation to be created and fail with no-peer message")
		rotationList := &baseboardv1alpha1.BMCUserRotationList{}
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.List(ctx, rotationList)).To(Succeed())
			var found bool
			for _, rot := range rotationList.Items {
				if rot.Spec.BMCUserRef.Name == singleAdmin.Name &&
					rot.Status.Phase == baseboardv1alpha1.BMCUserRotationPhaseFailed {
					found = true
				}
			}
			g.Expect(found).To(BeTrue(), "expected a Failed BMCUserRotation for single-operator-admin")
		}, "8s").Should(Succeed())

		By("Ensuring EffectiveBMCSecretRef has NOT changed (rotation was blocked)")
		Consistently(Object(singleAdmin), "2s").Should(
			HaveField("Status.EffectiveBMCSecretRef.Name", Equal(initialSecretName)),
		)

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

	It("should update BMC.Spec.BMCSecretRef when OperatorAdmin type is set and password is rotated", func(ctx SpecContext) {
		By("Creating two OperatorAdmin BMCUsers (rotation pair) with short rotation period")
		adminUserA := &baseboardv1alpha1.BMCUser{
			ObjectMeta: metav1.ObjectMeta{
				Name: "operator-admin-a",
			},
			Spec: baseboardv1alpha1.BMCUserSpec{
				UserName: "operator-admin-a",
				RoleID:   "Administrator",
				Type:     baseboardv1alpha1.BMCUserTypeOperatorAdmin,
				BMCRef: &v1.LocalObjectReference{
					Name: bmcObj.Name,
				},
				RotationPeriod: &metav1.Duration{
					Duration: 1 * time.Second,
				},
			},
		}
		adminUserB := &baseboardv1alpha1.BMCUser{
			ObjectMeta: metav1.ObjectMeta{
				Name: "operator-admin-b",
			},
			Spec: baseboardv1alpha1.BMCUserSpec{
				UserName: "operator-admin-b",
				RoleID:   "Administrator",
				Type:     baseboardv1alpha1.BMCUserTypeOperatorAdmin,
				BMCRef: &v1.LocalObjectReference{
					Name: bmcObj.Name,
				},
				RotationPeriod: &metav1.Duration{
					Duration: 1 * time.Second,
				},
			},
		}
		Expect(k8sClient.Create(ctx, adminUserA)).To(Succeed())
		Expect(k8sClient.Create(ctx, adminUserB)).To(Succeed())
		Eventually(Get(adminUserA)).Should(Succeed())
		Eventually(Get(adminUserB)).Should(Succeed())

		By("Waiting for EffectiveBMCSecretRef to be set for both users")
		Eventually(Object(adminUserA), "4s").Should(SatisfyAll(
			HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())),
		))

		By("Ensuring BMC.Spec.BMCSecretRef matches one of the effective secrets after rotation")
		Eventually(Object(adminUserA), "8s").Should(SatisfyAll(
			HaveField("Status.LastRotation", Not(BeNil())),
		))
		Eventually(Object(bmcObj), "4s").Should(SatisfyAll(
			HaveField("Spec.BMCSecretRef.Name", Not(Equal(bmcSecret.Name))),
		))

		By("Cleaning up both BMCUsers and all owned secrets")
		Expect(k8sClient.Delete(ctx, adminUserA)).To(Succeed())
		Expect(k8sClient.Delete(ctx, adminUserB)).To(Succeed())
		secretList := &metalv1alpha1.BMCSecretList{}
		Expect(k8sClient.List(ctx, secretList)).To(Succeed())
		for _, s := range secretList.Items {
			for _, ref := range s.OwnerReferences {
				if ref.UID == adminUserA.UID || ref.UID == adminUserB.UID {
					Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, &s))).To(Succeed())
				}
			}
		}
	})

	It("should not delete BMC account on deletion when DeletionPolicy=Retain and credential is live", func(ctx SpecContext) {
		mockServers[0].ResetAccounts()
		By("Creating an OperatorAdmin BMCUser with DeletionPolicy=Retain")
		retain := "Retain"
		adminUser := &baseboardv1alpha1.BMCUser{
			ObjectMeta: metav1.ObjectMeta{
				Name: "retain-admin-user",
			},
			Spec: baseboardv1alpha1.BMCUserSpec{
				UserName:       "retain-admin",
				RoleID:         "Administrator",
				Type:           baseboardv1alpha1.BMCUserTypeOperatorAdmin,
				DeletionPolicy: retain,
				BMCRef: &v1.LocalObjectReference{
					Name: bmcObj.Name,
				},
			},
		}
		Expect(k8sClient.Create(ctx, adminUser)).To(Succeed())
		Eventually(Get(adminUser)).Should(Succeed())

		By("Waiting for the OperatorAdmin user to be bootstrapped (EffectiveBMCSecretRef set)")
		Eventually(Object(adminUser), "8s").Should(HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())))

		By("Promoting the user's credential to BMC.Spec.BMCSecretRef to make it 'live'")
		Eventually(Get(adminUser)).Should(Succeed())
		bmcBase := bmcObj.DeepCopy()
		bmcObj.Spec.BMCSecretRef = v1.LocalObjectReference{Name: adminUser.Status.EffectiveBMCSecretRef.Name}
		Expect(k8sClient.Patch(ctx, bmcObj, client.MergeFrom(bmcBase))).To(Succeed())
		Eventually(Object(bmcObj)).Should(HaveField("Spec.BMCSecretRef.Name", Equal(adminUser.Status.EffectiveBMCSecretRef.Name)))

		By("Deleting the BMCUser — it should stall because the credential is live")
		Expect(k8sClient.Delete(ctx, adminUser)).To(Succeed())

		By("Verifying the BMCUser is NOT immediately deleted (controller requeues)")
		Consistently(Get(adminUser), "2s", "500ms").Should(Succeed())

		By("Verifying the BMC account still exists on the mock server")
		Expect(mockServers[0].GetAccountNames()).To(HaveKey("retain-admin"))
	})

})
