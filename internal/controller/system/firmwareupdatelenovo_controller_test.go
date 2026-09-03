// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package system

import (
	"context"

	"github.com/ironcore-dev/controller-utils/conditionutils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	systemv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/system/v1alpha1"
)

var _ = Describe("FirmwareUpdateLenovo Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default", // TODO(user):Modify as needed
		}
		firmwareupdatelenovo := &systemv1alpha1.FirmwareUpdateLenovo{}

		BeforeEach(func() {
			By("creating the custom resource for the Kind FirmwareUpdateLenovo")
			err := k8sClient.Get(ctx, typeNamespacedName, firmwareupdatelenovo)
			if err != nil && errors.IsNotFound(err) {
				// serverRef and repository.repoURI are required by the CRD schema.
				resource := &systemv1alpha1.FirmwareUpdateLenovo{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: systemv1alpha1.FirmwareUpdateLenovoSpec{
						FirmwareUpdateLenovoTemplate: systemv1alpha1.FirmwareUpdateLenovoTemplate{
							Repository: systemv1alpha1.LenovoRepositorySpec{
								RepoURI: "https://10.0.0.1/firmware/sr650v3",
							},
						},
						ServerRef: &corev1.LocalObjectReference{Name: "test-server"},
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance.
			resource := &systemv1alpha1.FirmwareUpdateLenovo{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance FirmwareUpdateLenovo")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})
		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &FirmwareUpdateLenovoReconciler{
				Client:     k8sClient,
				Scheme:     k8sClient.Scheme(),
				Conditions: conditionutils.NewAccessor(conditionutils.AccessorOptions{}),
			}

			// The first reconcile adds the finalizer and returns without contacting the (absent)
			// server, so it should not error. Fuller behaviour (dry-run, maintenance gating, the
			// Lenovo Redfish calls) is exercised once the metal-operator Lenovo BMC client lands
			// and the stub in firmwareupdatelenovo_stub.go is replaced. TODO(lenovo).
			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
