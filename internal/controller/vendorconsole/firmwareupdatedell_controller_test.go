// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
//
// SPDX-License-Identifier: Apache-2.0

package vendorconsole

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	. "sigs.k8s.io/controller-runtime/pkg/envtest/komega"

	vendorconsolev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/vendorconsole/v1alpha1"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/hwmgr"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

// SetupFirmwareNamespace creates a fresh namespace per test and starts a manager
// with FirmwareUpdateDELLReconciler registered against the suite-level mock server.
func SetupFirmwareNamespace() *corev1.Namespace {
	ns := &corev1.Namespace{}
	BeforeEach(func(ctx SpecContext) {
		mgrCtx, cancel := context.WithCancel(context.Background())
		DeferCleanup(cancel)

		*ns = corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "fw-test-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		DeferCleanup(k8sClient.Delete, ns)

		k8sManager, err := newTestManager(cfg)
		Expect(err).NotTo(HaveOccurred())

		Expect((&FirmwareUpdateDELLReconciler{
			Client:           k8sManager.GetClient(),
			Scheme:           k8sManager.GetScheme(),
			ManagerNamespace: ns.Name,
			OMEConfig: &hwmgr.MgrConfig{
				InsecureSkipVerify: true,
				ReuseConnections:   false,
			},
			ResyncInterval: 200 * time.Millisecond,
		}).SetupWithManager(k8sManager)).To(Succeed())

		go func() {
			defer GinkgoRecover()
			Expect(k8sManager.Start(mgrCtx)).To(Succeed())
		}()
	})
	return ns
}

var _ = Describe("FirmwareUpdateDELL Controller", func() {
	ns := SetupFirmwareNamespace()

	Context("state transitions", func() {
		ctx := context.Background()

		var omeSecret *corev1.Secret
		var fw *vendorconsolev1alpha1.FirmwareUpdateDELL
		var dellServer *metalv1alpha1.Server

		BeforeEach(func() {
			By("creating OME credential secret")
			omeSecret = &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "ome-secret-",
					Namespace:    ns.Name,
				},
				Data: map[string][]byte{
					vendorconsolev1alpha1.SecretUsernameKeyName: []byte("admin"),
					vendorconsolev1alpha1.SecretPasswordKeyName: []byte("password"),
					vendorconsolev1alpha1.SecretTokenKeyName:    []byte(""),
				},
			}
			Expect(k8sClient.Create(ctx, omeSecret)).To(Succeed())

			By("creating a Dell server matching the selector")
			dellServer = &metalv1alpha1.Server{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "dell-server-",
					Namespace:    ns.Name,
					Labels:       map[string]string{"firmware-test": "true"},
				},
				Spec: metalv1alpha1.ServerSpec{
					SystemUUID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
				},
			}
			Expect(k8sClient.Create(ctx, dellServer)).To(Succeed())

			By("marking server as Dell with SKU matching mock device 1")
			Eventually(UpdateStatus(dellServer, func() {
				dellServer.Status.Manufacturer = string(hwmgr.ManufacturerDell)
				dellServer.Status.SKU = "ABC123456789"
			})).Should(Succeed())
		})

		AfterEach(func() {
			// Delete FirmwareUpdateDELL first and wait for it to be gone before
			// deleting the secret — the controller's delete path calls getVendorConsoleClient
			// which needs the secret, so deleting the secret first causes a reconcile error.
			if fw != nil && fw.Name != "" {
				Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, fw))).To(Succeed())
				Eventually(func() error {
					return client.IgnoreNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(fw), fw))
				}, "15s").Should(Succeed())
				fw = nil
			}
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, dellServer))).To(Succeed())
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, omeSecret))).To(Succeed())
		})

		It("transitions Pending→InProgress when Dell servers match the selector", func() {
			fw = newFirmwareUpdateDELL(ns.Name, omeSecret.Name, map[string]string{"firmware-test": "true"})
			Expect(k8sClient.Create(ctx, fw)).To(Succeed())

			// With all-compliant mock data the controller goes Pending→InProgress→Completed
			// in rapid succession. Assert it reaches at least InProgress (or Completed).
			Eventually(Object(fw), "15s").Should(
				HaveField("Status.State", Or(
					Equal(vendorconsolev1alpha1.FirmwareUpdateStateInProgress),
					Equal(vendorconsolev1alpha1.FirmwareUpdateStateCompleted),
				)),
			)
		})

		It("stays in empty state when no servers match the selector", func() {
			fw = newFirmwareUpdateDELL(ns.Name, omeSecret.Name, map[string]string{"firmware-test": "no-match"})
			Expect(k8sClient.Create(ctx, fw)).To(Succeed())

			Consistently(Object(fw), "2s").Should(
				HaveField("Status.State", vendorconsolev1alpha1.FirmwareUpdateState("")),
			)
		})

		It("transitions to Failed when the selector matches a mix of Dell and non-Dell servers", func() {
			label := map[string]string{"firmware-test": "mixed"}

			nonDell := &metalv1alpha1.Server{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "hpe-server-",
					Namespace:    ns.Name,
					Labels:       label,
				},
				Spec: metalv1alpha1.ServerSpec{SystemUUID: "11111111-2222-3333-4444-555555555555"},
			}
			Expect(k8sClient.Create(ctx, nonDell)).To(Succeed())
			DeferCleanup(k8sClient.Delete, nonDell)
			Eventually(UpdateStatus(nonDell, func() { nonDell.Status.Manufacturer = "HPE" })).Should(Succeed())

			dellMixed := &metalv1alpha1.Server{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "dell-mixed-",
					Namespace:    ns.Name,
					Labels:       label,
				},
				Spec: metalv1alpha1.ServerSpec{SystemUUID: "aaaaaaaa-1111-2222-3333-ffffffffffff"},
			}
			Expect(k8sClient.Create(ctx, dellMixed)).To(Succeed())
			DeferCleanup(k8sClient.Delete, dellMixed)
			Eventually(UpdateStatus(dellMixed, func() {
				dellMixed.Status.Manufacturer = string(hwmgr.ManufacturerDell)
			})).Should(Succeed())

			fw = newFirmwareUpdateDELL(ns.Name, omeSecret.Name, label)
			Expect(k8sClient.Create(ctx, fw)).To(Succeed())

			Eventually(Object(fw), "15s").Should(
				HaveField("Status.State", vendorconsolev1alpha1.FirmwareUpdateStateFailed),
			)
		})

		It("remains in empty state when the OME credential secret is missing", func() {
			fw = newFirmwareUpdateDELL(ns.Name, "does-not-exist", map[string]string{"firmware-test": "true"})
			Expect(k8sClient.Create(ctx, fw)).To(Succeed())

			Consistently(Object(fw), "2s").Should(
				HaveField("Status.State", vendorconsolev1alpha1.FirmwareUpdateState("")),
			)
		})

		It("transitions InProgress→Completed when all devices are already compliant", func() {
			fw = newFirmwareUpdateDELL(ns.Name, omeSecret.Name, map[string]string{"firmware-test": "true"})
			Expect(k8sClient.Create(ctx, fw)).To(Succeed())

			// The baseline/20 compliance report mock already returns all devices compliant,
			// so no firmware update job is needed — the controller should go straight to Completed.
			Eventually(Object(fw), "30s").Should(
				HaveField("Status.State", vendorconsolev1alpha1.FirmwareUpdateStateCompleted),
			)
		})

		It("sets catalog and baseline IDs in status after reconciliation", func() {
			fw = newFirmwareUpdateDELL(ns.Name, omeSecret.Name, map[string]string{"firmware-test": "true"})
			Expect(k8sClient.Create(ctx, fw)).To(Succeed())

			Eventually(Object(fw), "15s").Should(SatisfyAll(
				HaveField("Status.State", vendorconsolev1alpha1.FirmwareUpdateStateCompleted),
				HaveField("Status.Catalog", Not(BeNil())),
				HaveField("Status.Baseline", Not(BeNil())),
			))
		})

		It("retries after transitioning to Failed when retry annotation is set", func() {
			// Use the real Dell server selector so the controller enters handleFailedState.
			fw = newFirmwareUpdateDELL(ns.Name, omeSecret.Name, map[string]string{"firmware-test": "true"})
			Expect(k8sClient.Create(ctx, fw)).To(Succeed())

			// Wait for the finalizer — controller has reconciled at least once.
			Eventually(func() bool {
				_ = k8sClient.Get(ctx, client.ObjectKeyFromObject(fw), fw)
				return len(fw.Finalizers) > 0
			}, "10s").Should(BeTrue())

			By("manually driving status to Failed")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(fw), fw); err != nil {
					return err
				}
				fwBase := fw.DeepCopy()
				fw.Status.State = vendorconsolev1alpha1.FirmwareUpdateStateFailed
				return k8sClient.Status().Patch(ctx, fw, client.MergeFrom(fwBase))
			}).Should(Succeed())

			Eventually(Object(fw), "5s").Should(
				HaveField("Status.State", vendorconsolev1alpha1.FirmwareUpdateStateFailed),
			)

			By("adding retry annotation")
			Eventually(func() error {
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(fw), fw); err != nil {
					return err
				}
				fwBase := fw.DeepCopy()
				if fw.Annotations == nil {
					fw.Annotations = map[string]string{}
				}
				fw.Annotations[metalv1alpha1.OperationAnnotation] = metalv1alpha1.OperationAnnotationRetryChild
				return k8sClient.Patch(ctx, fw, client.MergeFrom(fwBase))
			}).Should(Succeed())

			By("verifying retry annotation is consumed and state is no longer Failed")
			// The retry sets state to Pending and removes the annotation.
			// The controller may then immediately progress to Completed (all compliant mock),
			// so assert it is no longer Failed rather than a specific intermediate state.
			Eventually(Object(fw), "10s").Should(
				Not(HaveField("Status.State", vendorconsolev1alpha1.FirmwareUpdateStateFailed)),
			)
		})
	})
})

func newFirmwareUpdateDELL(_, secretName string, selector map[string]string) *vendorconsolev1alpha1.FirmwareUpdateDELL {
	return &vendorconsolev1alpha1.FirmwareUpdateDELL{
		ObjectMeta: metav1.ObjectMeta{
			// FirmwareUpdateDELL is cluster-scoped — no namespace.
			GenerateName: "fw-dell-",
		},
		Spec: vendorconsolev1alpha1.FirmwareUpdateDELLSpec{
			OMEURL:                "http://127.0.0.1:8000",
			SecretRef:             &corev1.LocalObjectReference{Name: secretName},
			CatalogRepositoryName: "test-repo",
			FirmwareUpgradeConfig: &vendorconsolev1alpha1.FirmwareUpgradeConfig{
				OperationName: "INSTALL_FIRMWARE",
				JobTypeName:   "Update_Task",
			},
			BaselineConfig: &vendorconsolev1alpha1.BaselinesConfig{
				Name:        "test-baseline",
				Description: "test baseline",
			},
			ServerSelector: metav1.LabelSelector{MatchLabels: selector},
		},
	}
}
