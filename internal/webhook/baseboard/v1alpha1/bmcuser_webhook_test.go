// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	. "sigs.k8s.io/controller-runtime/pkg/envtest/komega"

	baseboardv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/baseboard/v1alpha1"
)

var _ = Describe("BMCUser Webhook", func() {
	var validator BMCUserValidator

	BeforeEach(func() {
		validator = BMCUserValidator{Client: k8sClient}
		SetClient(k8sClient)
	})

	AfterEach(func() {
		Expect(k8sClient.DeleteAllOf(ctx, &baseboardv1alpha1.BMCUser{})).To(Succeed())
		Expect(k8sClient.DeleteAllOf(ctx, &baseboardv1alpha1.BMCUserSet{})).To(Succeed())
	})

	// Helper: create a BMCUserSet so that BMCUsers can reference it as an owner.
	makeSet := func(name string) *baseboardv1alpha1.BMCUserSet {
		set := &baseboardv1alpha1.BMCUserSet{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: baseboardv1alpha1.BMCUserSetSpec{
				BMCSelector: metav1.LabelSelector{MatchLabels: map[string]string{"test": name}},
				Template: baseboardv1alpha1.BMCUserTemplate{
					UserName: "admin",
					RoleID:   "Administrator",
				},
			},
		}
		Expect(k8sClient.Create(ctx, set)).To(Succeed())
		return set
	}

	// Helper: build a BMCUser spec, optionally owned by a BMCUserSet.
	makeUser := func(name, bmcName string, userType baseboardv1alpha1.BMCUserType, owner *baseboardv1alpha1.BMCUserSet) *baseboardv1alpha1.BMCUser {
		u := &baseboardv1alpha1.BMCUser{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: baseboardv1alpha1.BMCUserSpec{
				UserName: name,
				RoleID:   "Administrator",
				Type:     userType,
				BMCRef:   &corev1.LocalObjectReference{Name: bmcName},
			},
		}
		if owner != nil {
			controllerRef(owner, u)
		}
		return u
	}

	Context("ServiceAccount type", func() {
		It("should always allow ServiceAccount users regardless of existing OperatorAdmins", func(ctx SpecContext) {
			existing := makeUser("existing-admin", "bmc-1", baseboardv1alpha1.BMCUserTypeOperatorAdmin, nil)
			Expect(k8sClient.Create(ctx, existing)).To(Succeed())

			incoming := makeUser("new-svc", "bmc-1", baseboardv1alpha1.BMCUserTypeServiceAccount, nil)
			_, err := validator.ValidateCreate(ctx, incoming)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("OperatorAdmin type — no BMCRef", func() {
		It("should allow when BMCRef is nil", func(ctx SpecContext) {
			u := &baseboardv1alpha1.BMCUser{
				ObjectMeta: metav1.ObjectMeta{Name: "no-bmc-ref"},
				Spec: baseboardv1alpha1.BMCUserSpec{
					UserName: "admin",
					RoleID:   "Administrator",
					Type:     baseboardv1alpha1.BMCUserTypeOperatorAdmin,
				},
			}
			_, err := validator.ValidateCreate(ctx, u)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("OperatorAdmin type — different BMC", func() {
		It("should allow OperatorAdmin on a different BMC", func(ctx SpecContext) {
			existing := makeUser("admin-bmc1", "bmc-1", baseboardv1alpha1.BMCUserTypeOperatorAdmin, nil)
			Expect(k8sClient.Create(ctx, existing)).To(Succeed())

			incoming := makeUser("admin-bmc2", "bmc-2", baseboardv1alpha1.BMCUserTypeOperatorAdmin, nil)
			_, err := validator.ValidateCreate(ctx, incoming)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("OperatorAdmin type — standalone (no owner)", func() {
		It("should deny a second standalone OperatorAdmin on the same BMC", func(ctx SpecContext) {
			existing := makeUser("admin-a", "bmc-1", baseboardv1alpha1.BMCUserTypeOperatorAdmin, nil)
			Expect(k8sClient.Create(ctx, existing)).To(Succeed())

			incoming := makeUser("admin-b", "bmc-1", baseboardv1alpha1.BMCUserTypeOperatorAdmin, nil)
			_, err := validator.ValidateCreate(ctx, incoming)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("OperatorAdmin type — DualAccount pair (same BMCUserSet)", func() {
		It("should allow the -a / -b pair owned by the same BMCUserSet", func(ctx SpecContext) {
			set := makeSet("set-1")

			userA := makeUser("set-1-bmc-1-a", "bmc-1", baseboardv1alpha1.BMCUserTypeOperatorAdmin, set)
			Expect(k8sClient.Create(ctx, userA)).To(Succeed())

			userB := makeUser("set-1-bmc-1-b", "bmc-1", baseboardv1alpha1.BMCUserTypeOperatorAdmin, set)
			_, err := validator.ValidateCreate(ctx, userB)
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("OperatorAdmin type — two different BMCUserSets on the same BMC", func() {
		It("should deny a user from a second BMCUserSet targeting the same BMC", func(ctx SpecContext) {
			set1 := makeSet("set-1")
			set2 := makeSet("set-2")

			userA := makeUser("set-1-bmc-1-a", "bmc-1", baseboardv1alpha1.BMCUserTypeOperatorAdmin, set1)
			Expect(k8sClient.Create(ctx, userA)).To(Succeed())

			userB := makeUser("set-2-bmc-1-a", "bmc-1", baseboardv1alpha1.BMCUserTypeOperatorAdmin, set2)
			_, err := validator.ValidateCreate(ctx, userB)
			Expect(err).To(HaveOccurred())
		})
	})

	Context("OperatorAdmin type — update path", func() {
		It("should allow updating a user that is already the sole OperatorAdmin on its BMC", func(ctx SpecContext) {
			set := makeSet("set-upd")
			u := makeUser("upd-a", "bmc-3", baseboardv1alpha1.BMCUserTypeOperatorAdmin, set)
			Expect(k8sClient.Create(ctx, u)).To(Succeed())

			_, err := validator.ValidateUpdate(ctx, u, u)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should allow finalizer-only update even when a conflicting OperatorAdmin exists", func(ctx SpecContext) {
			set1 := makeSet("set-fin1")
			set2 := makeSet("set-fin2")

			// set1 user is already stored in the API server
			userA := makeUser("fin-bmc1-a", "bmc-fin", baseboardv1alpha1.BMCUserTypeOperatorAdmin, set1)
			Expect(k8sClient.Create(ctx, userA)).To(Succeed())

			// set2 user exists in memory only (not yet admitted)
			userB := makeUser("fin-bmc1-b", "bmc-fin", baseboardv1alpha1.BMCUserTypeOperatorAdmin, set2)

			// Simulate a finalizer-only update: old == new except for finalizers
			oldObj := userB.DeepCopy()
			newObj := userB.DeepCopy()
			newObj.Finalizers = []string{"test-finalizer"}

			// Type, BMCRef, and owner are unchanged — must pass even though set1 conflicts
			_, err := validator.ValidateUpdate(ctx, oldObj, newObj)
			Expect(err).NotTo(HaveOccurred())
		})
	})
})

// controllerRef sets a controller owner reference from owner onto obj.
func controllerRef(owner *baseboardv1alpha1.BMCUserSet, obj *baseboardv1alpha1.BMCUser) {
	t := true
	obj.OwnerReferences = []metav1.OwnerReference{
		{
			APIVersion:         baseboardv1alpha1.SchemeGroupVersion.String(),
			Kind:               "BMCUserSet",
			Name:               owner.Name,
			UID:                owner.UID,
			Controller:         &t,
			BlockOwnerDeletion: &t,
		},
	}
}
