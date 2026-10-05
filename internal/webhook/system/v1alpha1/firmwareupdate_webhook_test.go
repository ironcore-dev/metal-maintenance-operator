// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	. "sigs.k8s.io/controller-runtime/pkg/envtest/komega"

	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/maintenance/v1alpha1"
	systemv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/system/v1alpha1"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/constants"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

var _ = Describe("FirmwareUpdate Webhook", func() {
	var (
		fwUpdateV1 *systemv1alpha1.FirmwareUpdate
		validator  FirmwareUpdateValidator
	)

	newFirmwareUpdate := func(serverName string) *systemv1alpha1.FirmwareUpdate {
		return &systemv1alpha1.FirmwareUpdate{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-",
				Namespace:    metav1.NamespaceDefault,
			},
			Spec: systemv1alpha1.FirmwareUpdateSpec{
				FirmwareUpdateTemplate: systemv1alpha1.FirmwareUpdateTemplate{
					DellRepository: &systemv1alpha1.DellFirmwareRepository{
						ShareType:   systemv1alpha1.DellShareTypeHTTPS,
						Address:     "downloads.dell.com",
						CatalogFile: "Catalog.xml",
					},
				},
				ServerRef: &v1.LocalObjectReference{Name: serverName},
			},
		}
	}

	BeforeEach(func(ctx SpecContext) {
		validator = FirmwareUpdateValidator{Client: k8sClient}
		By("Creating a FirmwareUpdate")
		fwUpdateV1 = newFirmwareUpdate("foo")
		Expect(k8sClient.Create(ctx, fwUpdateV1)).To(Succeed())
	})

	AfterEach(func(ctx SpecContext) {
		By("Deleting the FirmwareUpdate resources")
		Expect(k8sClient.DeleteAllOf(ctx, &systemv1alpha1.FirmwareUpdate{}, client.InNamespace(metav1.NamespaceDefault))).To(Succeed())
		By("Deleting Server resources if created")
		Expect(client.IgnoreNotFound(k8sClient.DeleteAllOf(ctx, &metalv1alpha1.Server{}))).To(Succeed())
		By("Deleting ServerMaintenance resources if created")
		Expect(client.IgnoreNotFound(k8sClient.DeleteAllOf(ctx, &maintenancev1alpha1.ServerMaintenance{}, client.InNamespace(metav1.NamespaceDefault)))).To(Succeed())
	})

	It("should deny creation if a Server already has a FirmwareUpdate", func(ctx SpecContext) {
		By("Creating another FirmwareUpdate targeting the same Server")
		fwUpdateV2 := newFirmwareUpdate("foo")
		Expect(validator.ValidateCreate(ctx, fwUpdateV2)).Error().To(HaveOccurred())
	})

	It("should allow creating a FirmwareUpdate for a Server without one", func(ctx SpecContext) {
		By("Creating a FirmwareUpdate targeting a new Server")
		fwUpdateV2 := newFirmwareUpdate("bar")
		Expect(k8sClient.Create(ctx, fwUpdateV2)).To(Succeed())
	})

	It("should deny update if spec.serverRef becomes duplicate", func(ctx SpecContext) {
		By("Creating a FirmwareUpdate with a different ServerRef")
		fwUpdateV2 := newFirmwareUpdate("bar")
		Expect(k8sClient.Create(ctx, fwUpdateV2)).To(Succeed())

		By("Updating fwUpdateV2 with a conflicting ServerRef")
		fwUpdateV2Updated := fwUpdateV2.DeepCopy()
		fwUpdateV2Updated.Spec.ServerRef = &v1.LocalObjectReference{Name: "foo"}
		Expect(validator.ValidateUpdate(ctx, fwUpdateV2, fwUpdateV2Updated)).Error().To(HaveOccurred())
	})

	It("should allow update if a different field changes", func(ctx SpecContext) {
		By("Creating a FirmwareUpdate with a different ServerRef")
		fwUpdateV2 := newFirmwareUpdate("bar")
		Expect(k8sClient.Create(ctx, fwUpdateV2)).To(Succeed())

		By("Updating fwUpdateV2's non-conflicting fields")
		fwUpdateV2Updated := fwUpdateV2.DeepCopy()
		fwUpdateV2Updated.Spec.DellRepository.CatalogFile = "OtherCatalog.xml"
		Expect(validator.ValidateUpdate(ctx, fwUpdateV2, fwUpdateV2Updated)).Error().ToNot(HaveOccurred())
	})

	It("should allow deletion", func(ctx SpecContext) {
		Expect(validator.ValidateDelete(ctx, fwUpdateV1)).Error().ToNot(HaveOccurred())
	})

	It("should not allow update of an in-progress FirmwareUpdate, but should allow forcefully updating it", func(ctx SpecContext) {
		By("Creating a ServerMaintenance in InMaintenance state")
		sm := &maintenancev1alpha1.ServerMaintenance{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-sm-",
				Namespace:    metav1.NamespaceDefault,
			},
			Spec: maintenancev1alpha1.ServerMaintenanceSpec{
				Policy:    maintenancev1alpha1.ServerMaintenancePolicyEnforced,
				ServerRef: &v1.LocalObjectReference{Name: "foo"},
			},
		}
		Expect(k8sClient.Create(ctx, sm)).To(Succeed())
		Eventually(UpdateStatus(sm, func() {
			sm.Status.State = maintenancev1alpha1.ServerMaintenanceStateInMaintenance
		})).Should(Succeed())

		By("Patching the FirmwareUpdate V1 to InProgress state with a ServerMaintenanceRef")
		Eventually(UpdateStatus(fwUpdateV1, func() {
			fwUpdateV1.Status.State = systemv1alpha1.FirmwareUpdateStateInProgress
			fwUpdateV1.Status.ServerMaintenanceRef = &metalv1alpha1.ObjectReference{Name: sm.Name, Namespace: sm.Namespace}
		})).Should(Succeed())

		By("Denying the spec update of an in-progress FirmwareUpdate")
		fwUpdateV1Updated := fwUpdateV1.DeepCopy()
		fwUpdateV1Updated.Spec.DellRepository.CatalogFile = "OtherCatalog.xml"
		Expect(validator.ValidateUpdate(ctx, fwUpdateV1, fwUpdateV1Updated)).Error().To(HaveOccurred())

		By("Allowing the spec update of an in-progress FirmwareUpdate with force-update annotation")
		fwUpdateV1Updated.Annotations = map[string]string{constants.OperationAnnotation: constants.OperationAnnotationForceUpdateInProgress}
		Expect(validator.ValidateUpdate(ctx, fwUpdateV1, fwUpdateV1Updated)).Error().ToNot(HaveOccurred())

		By("Ensuring the FirmwareUpdate V1 is back to Completed state")
		Eventually(UpdateStatus(fwUpdateV1, func() {
			fwUpdateV1.Status.State = systemv1alpha1.FirmwareUpdateStateCompleted
			fwUpdateV1.Status.ServerMaintenanceRef = nil
		})).Should(Succeed())

		Eventually(UpdateStatus(sm, func() {
			sm.Status.State = maintenancev1alpha1.ServerMaintenanceStatePending
		})).Should(Succeed())
	})

	It("should deny deletion of an in-progress FirmwareUpdate, but should allow forcefully deleting it", func(ctx SpecContext) {
		By("Creating a ServerMaintenance in InMaintenance state")
		sm := &maintenancev1alpha1.ServerMaintenance{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-sm-",
				Namespace:    metav1.NamespaceDefault,
			},
			Spec: maintenancev1alpha1.ServerMaintenanceSpec{
				Policy:    maintenancev1alpha1.ServerMaintenancePolicyEnforced,
				ServerRef: &v1.LocalObjectReference{Name: "foo"},
			},
		}
		Expect(k8sClient.Create(ctx, sm)).To(Succeed())
		Eventually(UpdateStatus(sm, func() {
			sm.Status.State = maintenancev1alpha1.ServerMaintenanceStateInMaintenance
		})).Should(Succeed())

		By("Patching the FirmwareUpdate V1 to InProgress state with a ServerMaintenanceRef")
		Eventually(UpdateStatus(fwUpdateV1, func() {
			fwUpdateV1.Status.State = systemv1alpha1.FirmwareUpdateStateInProgress
			fwUpdateV1.Status.ServerMaintenanceRef = &metalv1alpha1.ObjectReference{Name: sm.Name, Namespace: sm.Namespace}
		})).Should(Succeed())

		By("Denying the deletion of an in-progress FirmwareUpdate")
		Expect(validator.ValidateDelete(ctx, fwUpdateV1)).Error().To(HaveOccurred())

		By("Allowing the forceful deletion of an in-progress FirmwareUpdate")
		fwUpdateV1Forced := fwUpdateV1.DeepCopy()
		fwUpdateV1Forced.Annotations = map[string]string{constants.OperationAnnotation: constants.OperationAnnotationForceUpdateOrDeleteInProgress}
		Expect(validator.ValidateDelete(ctx, fwUpdateV1Forced)).Error().ToNot(HaveOccurred())

		By("Ensuring the FirmwareUpdate V1 is back to Completed state")
		Eventually(UpdateStatus(fwUpdateV1, func() {
			fwUpdateV1.Status.State = systemv1alpha1.FirmwareUpdateStateCompleted
			fwUpdateV1.Status.ServerMaintenanceRef = nil
		})).Should(Succeed())

		Eventually(UpdateStatus(sm, func() {
			sm.Status.State = maintenancev1alpha1.ServerMaintenanceStatePending
		})).Should(Succeed())
	})
})
