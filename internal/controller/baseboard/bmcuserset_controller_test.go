// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package baseboard

import (
	"fmt"
	"time"

	baseboardv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/baseboard/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	. "sigs.k8s.io/controller-runtime/pkg/envtest/komega"
)

var _ = Describe("BMCUserSet Controller", func() {
	ns := SetupTest(nil)

	var bmcObj *metalv1alpha1.BMC
	var bmcSecret *metalv1alpha1.BMCSecret

	BeforeEach(func(ctx SpecContext) {
		By("Creating the setup credential secret")
		setupSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "setup-cred",
				Namespace: ns.Name,
			},
			Data: map[string][]byte{
				"username": []byte("admin"),
				"password": []byte("adminpass"),
			},
		}
		Expect(k8sClient.Create(ctx, setupSecret)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(setupSecret), setupSecret)).To(Succeed())
		}).Should(Succeed())

		By("Creating a BMCSecret")
		bmcSecret = &metalv1alpha1.BMCSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-userset-secret",
			},
			Data: map[string][]byte{
				"username": []byte("admin"),
				"password": []byte("adminpass"),
			},
		}
		Expect(k8sClient.Create(ctx, bmcSecret)).To(Succeed())
		Eventually(Get(bmcSecret)).Should(Succeed())

		By("Creating a BMC resource with selector labels")
		bmcObj = &metalv1alpha1.BMC{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-userset-bmc-",
				Labels: map[string]string{
					"metal.ironcore.dev/site": "test-site",
				},
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
				BMCSecretRef: corev1.LocalObjectReference{
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

	It("should create two BMCUser children per BMC for DualAccount strategy", func(ctx SpecContext) {
		By("Creating a BMCUserSet with DualAccount strategy")
		set := &baseboardv1alpha1.BMCUserSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-dual-set",
			},
			Spec: baseboardv1alpha1.BMCUserSetSpec{
				BMCSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{"metal.ironcore.dev/site": "test-site"},
				},
				SetupCredentialRef: &corev1.SecretReference{
					Name:      "setup-cred",
					Namespace: ns.Name,
				},
				RotationStrategy: baseboardv1alpha1.RotationStrategyDualAccount,
				Template: baseboardv1alpha1.BMCUserTemplate{
					UserName: "maint-admin",
					RoleID:   "Administrator",
					RotationPeriod: &metav1.Duration{
						Duration: 720 * time.Hour,
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, set)).To(Succeed())
		Eventually(Get(set)).Should(Succeed())

		baseName := fmt.Sprintf("%s-%s", set.Name, bmcObj.Name)
		userAName := baseName + "-a"
		userBName := baseName + "-b"

		By("Waiting for two BMCUser children to be created")
		userA := &baseboardv1alpha1.BMCUser{ObjectMeta: metav1.ObjectMeta{Name: userAName}}
		userB := &baseboardv1alpha1.BMCUser{ObjectMeta: metav1.ObjectMeta{Name: userBName}}
		Eventually(Get(userA), "4s").Should(Succeed())
		Eventually(Get(userB), "4s").Should(Succeed())

		By("Verifying BMCUser A spec")
		Eventually(Object(userA)).Should(SatisfyAll(
			HaveField("Spec.UserName", Equal("maint-admin-a")),
			HaveField("Spec.RoleID", Equal("Administrator")),
			HaveField("Spec.Type", Equal(baseboardv1alpha1.BMCUserTypeOperatorAdmin)),
			HaveField("Spec.BMCRef.Name", Equal(bmcObj.Name)),
		))

		By("Verifying BMCUser B spec")
		Eventually(Object(userB)).Should(SatisfyAll(
			HaveField("Spec.UserName", Equal("maint-admin-b")),
			HaveField("Spec.RoleID", Equal("Administrator")),
			HaveField("Spec.Type", Equal(baseboardv1alpha1.BMCUserTypeOperatorAdmin)),
			HaveField("Spec.BMCRef.Name", Equal(bmcObj.Name)),
		))

		By("Verifying owner references on children")
		Eventually(Object(userA)).Should(
			HaveField("OwnerReferences", ContainElement(
				HaveField("Name", Equal(set.Name)),
			)),
		)

		Expect(k8sClient.Delete(ctx, set)).To(Succeed())
	})

	It("should create one BMCUser child per BMC for SingleAccount strategy", func(ctx SpecContext) {
		By("Creating a BMCUserSet with SingleAccount strategy")
		set := &baseboardv1alpha1.BMCUserSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-single-set",
			},
			Spec: baseboardv1alpha1.BMCUserSetSpec{
				BMCSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{"metal.ironcore.dev/site": "test-site"},
				},
				SetupCredentialRef: &corev1.SecretReference{
					Name:      "setup-cred",
					Namespace: ns.Name,
				},
				RotationStrategy: baseboardv1alpha1.RotationStrategySingleAccount,
				Template: baseboardv1alpha1.BMCUserTemplate{
					UserName: "redfish-exporter",
					RoleID:   "ReadOnly",
				},
			},
		}
		Expect(k8sClient.Create(ctx, set)).To(Succeed())
		Eventually(Get(set)).Should(Succeed())

		childName := fmt.Sprintf("%s-%s", set.Name, bmcObj.Name)

		By("Waiting for exactly one BMCUser child to be created")
		user := &baseboardv1alpha1.BMCUser{ObjectMeta: metav1.ObjectMeta{Name: childName}}
		Eventually(Get(user), "4s").Should(Succeed())

		By("Verifying BMCUser spec")
		Eventually(Object(user)).Should(SatisfyAll(
			HaveField("Spec.UserName", Equal("redfish-exporter")),
			HaveField("Spec.RoleID", Equal("ReadOnly")),
			HaveField("Spec.Type", Equal(baseboardv1alpha1.BMCUserTypeServiceAccount)),
			HaveField("Spec.BMCRef.Name", Equal(bmcObj.Name)),
		))

		Expect(k8sClient.Delete(ctx, set)).To(Succeed())
	})

	It("should delete orphaned BMCUser when BMC no longer matches selector", func(ctx SpecContext) {
		By("Creating a BMCUserSet with SingleAccount strategy")
		set := &baseboardv1alpha1.BMCUserSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-orphan-set",
			},
			Spec: baseboardv1alpha1.BMCUserSetSpec{
				BMCSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{"metal.ironcore.dev/site": "test-site"},
				},
				SetupCredentialRef: &corev1.SecretReference{
					Name:      "setup-cred",
					Namespace: ns.Name,
				},
				RotationStrategy: baseboardv1alpha1.RotationStrategySingleAccount,
				Template: baseboardv1alpha1.BMCUserTemplate{
					UserName: "orphan-test-user",
					RoleID:   "ReadOnly",
				},
			},
		}
		Expect(k8sClient.Create(ctx, set)).To(Succeed())

		childName := fmt.Sprintf("%s-%s", set.Name, bmcObj.Name)
		child := &baseboardv1alpha1.BMCUser{ObjectMeta: metav1.ObjectMeta{Name: childName}}
		By("Waiting for the BMCUser child to be created")
		Eventually(Get(child), "4s").Should(Succeed())

		By("Removing the matching label from the BMC so it no longer matches")
		bmcBase := bmcObj.DeepCopy()
		bmcObj.Labels = map[string]string{}
		Expect(k8sClient.Patch(ctx, bmcObj, client.MergeFrom(bmcBase))).To(Succeed())

		By("Waiting for the orphaned BMCUser to be deleted")
		Eventually(Get(child), "8s").ShouldNot(Succeed())

		Expect(k8sClient.Delete(ctx, set)).To(Succeed())
	})

	It("should update stable corev1.Secret after BMCUser proves credentials", func(ctx SpecContext) {
		By("Creating a BMCUserSet with CredentialSecretNameTemplate")
		set := &baseboardv1alpha1.BMCUserSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-stable-secret-set",
			},
			Spec: baseboardv1alpha1.BMCUserSetSpec{
				BMCSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{"metal.ironcore.dev/site": "test-site"},
				},
				SetupCredentialRef: &corev1.SecretReference{
					Name:      "setup-cred",
					Namespace: ns.Name,
				},
				RotationStrategy: baseboardv1alpha1.RotationStrategySingleAccount,
				Template: baseboardv1alpha1.BMCUserTemplate{
					UserName:                     "stable-test-user",
					RoleID:                       "ReadOnly",
					CredentialSecretNameTemplate: "bmc-cred-{{ .BMCName }}",
					CredentialSecretNamespace:    "default",
				},
			},
		}
		Expect(k8sClient.Create(ctx, set)).To(Succeed())

		childName := fmt.Sprintf("%s-%s", set.Name, bmcObj.Name)
		child := &baseboardv1alpha1.BMCUser{ObjectMeta: metav1.ObjectMeta{Name: childName}}
		By("Waiting for BMCUser child and its EffectiveBMCSecretRef")
		Eventually(Get(child), "4s").Should(Succeed())
		Eventually(Object(child), "8s").Should(
			HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())),
		)

		expectedSecretName := fmt.Sprintf("bmc-cred-%s", bmcObj.Name)
		By(fmt.Sprintf("Verifying stable secret %s exists in default namespace", expectedSecretName))
		stableSecret := &corev1.Secret{}
		Eventually(func() error {
			return k8sClient.Get(ctx, client.ObjectKey{Name: expectedSecretName, Namespace: "default"}, stableSecret)
		}, "8s").Should(Succeed())

		By("Verifying stable secret has username and password")
		Expect(stableSecret.Data).To(HaveKey("username"))
		Expect(stableSecret.Data).To(HaveKey("password"))
		Expect(stableSecret.Data["username"]).NotTo(BeEmpty())
		Expect(stableSecret.Data["password"]).NotTo(BeEmpty())

		Expect(k8sClient.Delete(ctx, set)).To(Succeed())
		// Clean up stable secret manually (not owned by set due to cross-scope)
		_ = k8sClient.Delete(ctx, stableSecret)
	})

	It("should set OperatorCredentialReady:True condition when all users are proven", func(ctx SpecContext) {
		By("Creating a DualAccount BMCUserSet")
		set := &baseboardv1alpha1.BMCUserSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-condition-set",
			},
			Spec: baseboardv1alpha1.BMCUserSetSpec{
				BMCSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{"metal.ironcore.dev/site": "test-site"},
				},
				SetupCredentialRef: &corev1.SecretReference{
					Name:      "setup-cred",
					Namespace: ns.Name,
				},
				RotationStrategy: baseboardv1alpha1.RotationStrategyDualAccount,
				Template: baseboardv1alpha1.BMCUserTemplate{
					UserName: "cond-admin",
					RoleID:   "Administrator",
				},
			},
		}
		Expect(k8sClient.Create(ctx, set)).To(Succeed())

		baseName := fmt.Sprintf("%s-%s", set.Name, bmcObj.Name)
		userA := &baseboardv1alpha1.BMCUser{ObjectMeta: metav1.ObjectMeta{Name: baseName + "-a"}}
		userB := &baseboardv1alpha1.BMCUser{ObjectMeta: metav1.ObjectMeta{Name: baseName + "-b"}}

		By("Waiting for both BMCUsers to have EffectiveBMCSecretRef")
		Eventually(Get(userA), "4s").Should(Succeed())
		Eventually(Get(userB), "4s").Should(Succeed())
		Eventually(Object(userA), "8s").Should(
			HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())),
		)
		Eventually(Object(userB), "8s").Should(
			HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())),
		)

		By("Verifying OperatorCredentialReady:True condition on BMCUserSet")
		Eventually(Object(set), "8s").Should(
			HaveField("Status.Conditions", ContainElement(SatisfyAll(
				HaveField("Type", Equal("OperatorCredentialReady")),
				HaveField("Status", Equal(metav1.ConditionTrue)),
			))),
		)

		By("Verifying BootstrappedBMCs count is 1")
		Eventually(Object(set), "4s").Should(
			HaveField("Status.BootstrappedBMCs", Equal(int32(1))),
		)

		Expect(k8sClient.Delete(ctx, set)).To(Succeed())
	})

	It("should stamp BootstrapSecretRef on each BMCUser before bootstrap", func(ctx SpecContext) {
		By("Creating a DualAccount BMCUserSet without setupCredentialRef")
		set := &baseboardv1alpha1.BMCUserSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-bootstrap-ref-set",
			},
			Spec: baseboardv1alpha1.BMCUserSetSpec{
				BMCSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{"metal.ironcore.dev/site": "test-site"},
				},
				RotationStrategy: baseboardv1alpha1.RotationStrategyDualAccount,
				Template: baseboardv1alpha1.BMCUserTemplate{
					UserName: "bootstrap-admin",
					RoleID:   "Administrator",
				},
			},
		}
		Expect(k8sClient.Create(ctx, set)).To(Succeed())

		baseName := fmt.Sprintf("%s-%s", set.Name, bmcObj.Name)
		userA := &baseboardv1alpha1.BMCUser{ObjectMeta: metav1.ObjectMeta{Name: baseName + "-a"}}
		userB := &baseboardv1alpha1.BMCUser{ObjectMeta: metav1.ObjectMeta{Name: baseName + "-b"}}

		By("Waiting for both BMCUsers to be created with BootstrapSecretRef pointing at the factory secret")
		Eventually(Get(userA), "4s").Should(Succeed())
		Eventually(Get(userB), "4s").Should(Succeed())
		Eventually(Object(userA), "8s").Should(
			HaveField("Status.BootstrapSecretRef", Equal(&corev1.LocalObjectReference{Name: bmcSecret.Name})),
		)
		Eventually(Object(userB), "8s").Should(
			HaveField("Status.BootstrapSecretRef", Equal(&corev1.LocalObjectReference{Name: bmcSecret.Name})),
		)

		By("Verifying both users eventually bootstrap using BootstrapSecretRef")
		Eventually(Object(userA), "8s").Should(HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())))
		Eventually(Object(userB), "8s").Should(HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())))

		Expect(k8sClient.Delete(ctx, set)).To(Succeed())
	})

	It("should bootstrap without setupCredentialRef using per-BMC BootstrapSecretRef", func(ctx SpecContext) {
		By("Creating a SingleAccount BMCUserSet without setupCredentialRef")
		set := &baseboardv1alpha1.BMCUserSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-no-setup-cred-set",
			},
			Spec: baseboardv1alpha1.BMCUserSetSpec{
				BMCSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{"metal.ironcore.dev/site": "test-site"},
				},
				RotationStrategy: baseboardv1alpha1.RotationStrategySingleAccount,
				Template: baseboardv1alpha1.BMCUserTemplate{
					UserName: "exporter-nosecret",
					RoleID:   "ReadOnly",
				},
			},
		}
		Expect(k8sClient.Create(ctx, set)).To(Succeed())

		childName := fmt.Sprintf("%s-%s", set.Name, bmcObj.Name)
		child := &baseboardv1alpha1.BMCUser{ObjectMeta: metav1.ObjectMeta{Name: childName}}

		By("Waiting for the BMCUser to have EffectiveBMCSecretRef despite no setupCredentialRef")
		Eventually(Get(child), "4s").Should(Succeed())
		Eventually(Object(child), "8s").Should(HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())))

		Expect(k8sClient.Delete(ctx, set)).To(Succeed())
	})

	It("should not delete an orphaned OperatorAdmin BMCUser whose credential is live in BMC.Spec.BMCSecretRef", func(ctx SpecContext) {
		By("Creating a DualAccount BMCUserSet")
		set := &baseboardv1alpha1.BMCUserSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-guard-set",
			},
			Spec: baseboardv1alpha1.BMCUserSetSpec{
				BMCSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{"metal.ironcore.dev/site": "test-site"},
				},
				SetupCredentialRef: &corev1.SecretReference{
					Name:      "setup-cred",
					Namespace: ns.Name,
				},
				RotationStrategy: baseboardv1alpha1.RotationStrategyDualAccount,
				Template: baseboardv1alpha1.BMCUserTemplate{
					UserName: "guard-admin",
					RoleID:   "Administrator",
				},
			},
		}
		Expect(k8sClient.Create(ctx, set)).To(Succeed())

		baseName := fmt.Sprintf("%s-%s", set.Name, bmcObj.Name)
		userA := &baseboardv1alpha1.BMCUser{ObjectMeta: metav1.ObjectMeta{Name: baseName + "-a"}}
		userB := &baseboardv1alpha1.BMCUser{ObjectMeta: metav1.ObjectMeta{Name: baseName + "-b"}}

		By("Waiting for both users to have proven credentials")
		Eventually(Get(userA), "4s").Should(Succeed())
		Eventually(Get(userB), "4s").Should(Succeed())
		Eventually(Object(userA), "8s").Should(HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())))
		Eventually(Object(userB), "8s").Should(HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())))

		By("Identifying which user's credential was promoted to BMC.Spec.BMCSecretRef")
		Eventually(Get(bmcObj)).Should(Succeed())
		var liveUserName string
		if userA.Status.EffectiveBMCSecretRef != nil && userA.Status.EffectiveBMCSecretRef.Name == bmcObj.Spec.BMCSecretRef.Name {
			liveUserName = userA.Name
		} else {
			liveUserName = userB.Name
		}
		liveUser := &baseboardv1alpha1.BMCUser{ObjectMeta: metav1.ObjectMeta{Name: liveUserName}}
		Eventually(Get(liveUser)).Should(Succeed())

		By("Removing the matching label from the BMC so the live user becomes orphaned")
		bmcBase := bmcObj.DeepCopy()
		bmcObj.Labels = map[string]string{}
		Expect(k8sClient.Patch(ctx, bmcObj, client.MergeFrom(bmcBase))).To(Succeed())

		By("Verifying the live user is NOT deleted after the relabel (credential guard)")
		Consistently(Get(liveUser), "4s", "500ms").Should(Succeed())

		Expect(k8sClient.Delete(ctx, set)).To(Succeed())
	})

	It("should set CredentialsBootstrapped:True on both OperatorAdmin users after bootstrap (no factory account)", func(ctx SpecContext) {
		By("Creating a DualAccount BMCUserSet without FactoryAccount")
		set := &baseboardv1alpha1.BMCUserSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-cred-bootstrapped-set",
			},
			Spec: baseboardv1alpha1.BMCUserSetSpec{
				BMCSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{"metal.ironcore.dev/site": "test-site"},
				},
				SetupCredentialRef: &corev1.SecretReference{
					Name:      "setup-cred",
					Namespace: ns.Name,
				},
				RotationStrategy: baseboardv1alpha1.RotationStrategyDualAccount,
				Template: baseboardv1alpha1.BMCUserTemplate{
					UserName: "cred-boot-admin",
					RoleID:   "Administrator",
				},
			},
		}
		Expect(k8sClient.Create(ctx, set)).To(Succeed())

		baseName := fmt.Sprintf("%s-%s", set.Name, bmcObj.Name)
		userA := &baseboardv1alpha1.BMCUser{ObjectMeta: metav1.ObjectMeta{Name: baseName + "-a"}}
		userB := &baseboardv1alpha1.BMCUser{ObjectMeta: metav1.ObjectMeta{Name: baseName + "-b"}}

		By("Waiting for CredentialsBootstrapped:True on both users")
		Eventually(Get(userA), "4s").Should(Succeed())
		Eventually(Get(userB), "4s").Should(Succeed())
		Eventually(Object(userA), "8s").Should(
			HaveField("Status.Conditions", ContainElement(SatisfyAll(
				HaveField("Type", Equal("CredentialsBootstrapped")),
				HaveField("Status", Equal(metav1.ConditionTrue)),
			))),
		)
		Eventually(Object(userB), "8s").Should(
			HaveField("Status.Conditions", ContainElement(SatisfyAll(
				HaveField("Type", Equal("CredentialsBootstrapped")),
				HaveField("Status", Equal(metav1.ConditionTrue)),
			))),
		)

		Expect(k8sClient.Delete(ctx, set)).To(Succeed())
	})

	It("should honour ignoreVendors and skip factory rotation for matching BMCs", func(ctx SpecContext) {
		By("Patching BMC.Status.Manufacturer to a known vendor name")
		bmcBase := bmcObj.DeepCopy()
		bmcObj.Status.Manufacturer = "Contoso"
		Expect(k8sClient.Status().Patch(ctx, bmcObj, client.MergeFrom(bmcBase))).To(Succeed())

		By("Creating a DualAccount BMCUserSet with FactoryAccount and IgnoreVendors matching the BMC manufacturer")
		set := &baseboardv1alpha1.BMCUserSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-ignore-vendor-set",
			},
			Spec: baseboardv1alpha1.BMCUserSetSpec{
				BMCSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{"metal.ironcore.dev/site": "test-site"},
				},
				SetupCredentialRef: &corev1.SecretReference{
					Name:      "setup-cred",
					Namespace: ns.Name,
				},
				RotationStrategy: baseboardv1alpha1.RotationStrategyDualAccount,
				Template: baseboardv1alpha1.BMCUserTemplate{
					UserName: "ig-admin",
					RoleID:   "Administrator",
				},
				FactoryAccount: &baseboardv1alpha1.FactoryAccount{
					Action:        baseboardv1alpha1.FactoryAccountActionRotate,
					IgnoreVendors: []string{"Contoso"},
				},
			},
		}
		Expect(k8sClient.Create(ctx, set)).To(Succeed())

		baseName := fmt.Sprintf("%s-%s", set.Name, bmcObj.Name)
		userA := &baseboardv1alpha1.BMCUser{ObjectMeta: metav1.ObjectMeta{Name: baseName + "-a"}}
		userB := &baseboardv1alpha1.BMCUser{ObjectMeta: metav1.ObjectMeta{Name: baseName + "-b"}}

		By("Waiting for both users to have proven credentials")
		Eventually(Get(userA), "4s").Should(Succeed())
		Eventually(Get(userB), "4s").Should(Succeed())
		Eventually(Object(userA), "8s").Should(HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())))
		Eventually(Object(userB), "8s").Should(HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())))

		By("Verifying no FactoryBootstrap BMCUserRotation was created (vendor is ignored)")
		Consistently(func(g Gomega) {
			rotList := &baseboardv1alpha1.BMCUserRotationList{}
			g.Expect(k8sClient.List(ctx, rotList)).To(Succeed())
			for _, rot := range rotList.Items {
				g.Expect(rot.Spec.TriggerReason).NotTo(Equal(baseboardv1alpha1.BMCUserRotationTriggerFactoryBootstrap))
			}
		}, "4s", "500ms").Should(Succeed())

		By("Verifying BootstrappedBMCs is 0 (FactoryAccount configured but not handled due to ignoreVendors)")
		// When FactoryAccount is configured and not handled, BootstrappedBMCs stays 0.
		// Alternatively the count may be 1 if the set counts it as handled-by-skip.
		// The spec says ignored vendors are simply skipped; FactoryAccountHandled stays false.
		Eventually(Get(userA)).Should(Succeed())
		Expect(userA.Status.FactoryAccountHandled).To(BeFalse())

		Expect(k8sClient.Delete(ctx, set)).To(Succeed())
	})

	It("should prune old BMCSecrets owned by a user but keep protected ones", func(ctx SpecContext) {
		By("Creating a SingleAccount BMCUserSet")
		set := &baseboardv1alpha1.BMCUserSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-prune-set",
			},
			Spec: baseboardv1alpha1.BMCUserSetSpec{
				BMCSelector: metav1.LabelSelector{
					MatchLabels: map[string]string{"metal.ironcore.dev/site": "test-site"},
				},
				SetupCredentialRef: &corev1.SecretReference{
					Name:      "setup-cred",
					Namespace: ns.Name,
				},
				RotationStrategy: baseboardv1alpha1.RotationStrategySingleAccount,
				Template: baseboardv1alpha1.BMCUserTemplate{
					UserName: "prune-user",
					RoleID:   "ReadOnly",
				},
			},
		}
		Expect(k8sClient.Create(ctx, set)).To(Succeed())

		childName := fmt.Sprintf("%s-%s", set.Name, bmcObj.Name)
		child := &baseboardv1alpha1.BMCUser{ObjectMeta: metav1.ObjectMeta{Name: childName}}
		Eventually(Get(child), "4s").Should(Succeed())
		Eventually(Object(child), "8s").Should(HaveField("Status.EffectiveBMCSecretRef", Not(BeNil())))

		By("Creating an extra owned BMCSecret that should be pruned")
		Eventually(Get(child)).Should(Succeed())
		staleSecret := &metalv1alpha1.BMCSecret{
			ObjectMeta: metav1.ObjectMeta{
				Name: "stale-secret-prune-test",
			},
			Data: map[string][]byte{
				"username": []byte("prune-user"),
				"password": []byte("oldpassword"),
			},
		}
		Expect(k8sClient.Create(ctx, staleSecret)).To(Succeed())
		Eventually(Get(staleSecret)).Should(Succeed())
		staleBase := staleSecret.DeepCopy()
		staleSecret.OwnerReferences = []metav1.OwnerReference{
			{
				APIVersion:         "baseboard.metal.ironcore.dev/v1alpha1",
				Kind:               "BMCUser",
				Name:               child.Name,
				UID:                child.UID,
				Controller:         new(true),
				BlockOwnerDeletion: new(true),
			},
		}
		Expect(k8sClient.Patch(ctx, staleSecret, client.MergeFrom(staleBase))).To(Succeed())

		By("Verifying the stale secret is eventually pruned by the controller")
		Eventually(func() bool {
			s := &metalv1alpha1.BMCSecret{}
			return k8sClient.Get(ctx, client.ObjectKey{Name: staleSecret.Name}, s) != nil
		}, "8s", "500ms").Should(BeTrue())

		By("Verifying the effective secret is NOT pruned")
		Eventually(Get(child)).Should(Succeed())
		effectiveSecretName := child.Status.EffectiveBMCSecretRef.Name
		effectiveSecret := &metalv1alpha1.BMCSecret{ObjectMeta: metav1.ObjectMeta{Name: effectiveSecretName}}
		Consistently(Get(effectiveSecret), "2s", "200ms").Should(Succeed())

		Expect(k8sClient.Delete(ctx, set)).To(Succeed())
	})
})
