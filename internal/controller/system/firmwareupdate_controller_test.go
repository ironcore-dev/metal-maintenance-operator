// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package system

import (
	"context"
	"fmt"
	"time"

	"github.com/ironcore-dev/controller-utils/conditionutils"
	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/maintenance/v1alpha1"
	systemv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/system/v1alpha1"
	"github.com/ironcore-dev/metal-operator/bmc"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	mockserver "github.com/ironcore-dev/metal-operator/bmc/mock/server"
	"github.com/stmcginnis/gofish/schemas"
	constants "github.com/ironcore-dev/metal-maintenance-operator/internal/constants"
	"k8s.io/utils/ptr"

	. "sigs.k8s.io/controller-runtime/pkg/envtest/komega"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// dellSystemID is the mock server's System resource folder name (see
// metal-operator/bmc/mock/server/data/Systems/437XR1138R2) whose Manufacturer
// is overridden to "Dell Inc." for this Describe block via
// mockserver.WithSystemOverride, so BMC vendor detection resolves to
// bmc.DellRedfishBMC instead of the vendor-neutral RedfishBaseBMC.
const dellSystemID = "437XR1138R2"

var _ = Describe("FirmwareUpdate Controller", func() {
	ns := SetupTest(nil, mockserver.WithSystemOverride(dellSystemID, map[string]any{"Manufacturer": "Dell Inc."}))

	var (
		server    *metalv1alpha1.Server
		bmcSecret *metalv1alpha1.BMCSecret
	)

	BeforeEach(func(ctx SpecContext) {
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

		By("Creating a Server")
		server = &metalv1alpha1.Server{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-maintenance-",
			},
			Spec: metalv1alpha1.ServerSpec{
				SystemUUID: "38947555-7742-3448-3784-823347823834",
				BMC: &metalv1alpha1.BMCAccess{
					// ProtocolRedfish (not ProtocolRedfishLocal) is required here:
					// only bmc.NewRedfishBMCClient (used for ProtocolRedfish) performs
					// vendor detection and dispatches to bmc.DellRedfishBMC. RedfishLocal
					// always returns the vendor-neutral RedfishBaseBMC wrapper.
					Protocol: metalv1alpha1.Protocol{
						Name: metalv1alpha1.ProtocolRedfish,
						Port: MockServerPort,
					},
					Address: MockServerIP,
					BMCSecretRef: v1.LocalObjectReference{
						Name: bmcSecret.Name,
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, server)).To(Succeed())

		By("Ensuring that the Server is in available state")
		Eventually(UpdateStatus(server, func() {
			server.Status.State = metalv1alpha1.ServerStateAvailable
		})).Should(Succeed())

		By("Ensuring that the Server's SystemURI has been discovered")
		// simcontrollers.ServerReconciler discovers and patches Spec.SystemURI
		// asynchronously. Waiting for it here avoids a race where
		// FirmwareUpdateDell issues its first repository check against an
		// empty SystemURI, which 404s and is treated as a fatal error.
		Eventually(Object(server)).Should(
			HaveField("Spec.SystemURI", Not(BeEmpty())),
		)
	})

	AfterEach(func(ctx SpecContext) {
		Expect(k8sClient.Delete(ctx, server)).To(Succeed())
		Expect(k8sClient.Delete(ctx, bmcSecret)).To(Succeed())
		EnsureCleanState()
		mockServers[0].ResetDellRepositoryUpdate()
	})

	It("should apply a pending repository firmware update through to completion", func(ctx SpecContext) {
		By("Ensuring that the server has Available state")
		Eventually(Object(server)).Should(
			HaveField("Status.State", metalv1alpha1.ServerStateAvailable),
		)

		By("Creating a FirmwareUpdateDell")
		fwUpdate := &systemv1alpha1.FirmwareUpdate{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-",
				Namespace:    ns.Name,
			},
			Spec: systemv1alpha1.FirmwareUpdateSpec{
				FirmwareUpdateTemplate: systemv1alpha1.FirmwareUpdateTemplate{
					DellRepository: &systemv1alpha1.DellFirmwareRepository{
						ShareType:   systemv1alpha1.DellShareTypeHTTPS,
						Address:     "downloads.dell.com",
						CatalogFile: "Catalog.xml",
					},
					ServerMaintenancePolicy: ptr.To(maintenancev1alpha1.ServerMaintenancePolicyEnforced),
				},
				ServerRef: &v1.LocalObjectReference{Name: server.Name},
			},
		}
		Expect(k8sClient.Create(ctx, fwUpdate)).To(Succeed())

		By("Ensuring that the FirmwareUpdateDell has entered InProgress state")
		Eventually(Object(fwUpdate)).Should(
			HaveField("Status.State", systemv1alpha1.FirmwareUpdateStateInProgress),
		)

		By("Ensuring that the ServerMaintenance resource has been created")
		var serverMaintenanceList maintenancev1alpha1.ServerMaintenanceList
		Eventually(ObjectList(&serverMaintenanceList)).Should(HaveField("Items", Not(BeEmpty())))

		serverMaintenance := &maintenancev1alpha1.ServerMaintenance{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns.Name,
				Name:      fwUpdate.Name,
			},
		}
		Eventually(Get(serverMaintenance)).Should(Succeed())

		By("Ensuring that the ServerMaintenance has been referenced by FirmwareUpdateDell")
		Eventually(Object(fwUpdate)).Should(
			HaveField("Status.ServerMaintenanceRef", &metalv1alpha1.ObjectReference{
				Namespace: serverMaintenance.Namespace,
				Name:      serverMaintenance.Name,
			}),
		)

		By("Ensuring that the ServerMaintenance is in InMaintenance state")
		Eventually(Object(serverMaintenance)).Should(
			HaveField("Status.State", maintenancev1alpha1.ServerMaintenanceStateInMaintenance),
		)

		By("Ensuring that the repository-based firmware update has completed")
		Eventually(Object(fwUpdate)).Should(
			HaveField("Status.State", systemv1alpha1.FirmwareUpdateStateCompleted),
		)

		By("Ensuring that the FirmwareUpdateDell has removed the ServerMaintenance reference")
		Eventually(Object(fwUpdate)).Should(
			HaveField("Status.ServerMaintenanceRef", BeNil()),
		)
		Consistently(Object(fwUpdate)).Should(
			HaveField("Status.ServerMaintenanceRef", BeNil()),
		)

		By("Ensuring that the ServerMaintenance has been deleted")
		Eventually(Get(serverMaintenance)).Should(Satisfy(apierrors.IsNotFound))

		By("Deleting the FirmwareUpdateDell")
		Expect(k8sClient.Delete(ctx, fwUpdate)).To(Succeed())

		By("Ensuring that the FirmwareUpdateDell has been removed")
		Eventually(Get(fwUpdate)).Should(Satisfy(apierrors.IsNotFound))
		Consistently(Get(fwUpdate)).Should(Satisfy(apierrors.IsNotFound))
	})

	It("should mark Failed when the repository check job fails", func(ctx SpecContext) {
		By("Ensuring that the server has Available state")
		Eventually(Object(server)).Should(
			HaveField("Status.State", metalv1alpha1.ServerStateAvailable),
		)

		By("Creating a FirmwareUpdateDell with a catalog file that triggers a failing job")
		fwUpdate := &systemv1alpha1.FirmwareUpdate{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-",
				Namespace:    ns.Name,
			},
			Spec: systemv1alpha1.FirmwareUpdateSpec{
				FirmwareUpdateTemplate: systemv1alpha1.FirmwareUpdateTemplate{
					DellRepository: &systemv1alpha1.DellFirmwareRepository{
						ShareType:   systemv1alpha1.DellShareTypeHTTPS,
						Address:     "downloads.dell.com",
						CatalogFile: "fail-catalog.xml",
					},
					ServerMaintenancePolicy: ptr.To(maintenancev1alpha1.ServerMaintenancePolicyEnforced),
				},
				ServerRef: &v1.LocalObjectReference{Name: server.Name},
			},
		}
		Expect(k8sClient.Create(ctx, fwUpdate)).To(Succeed())

		By("Ensuring that the FirmwareUpdateDell has entered Failed state")
		Eventually(Object(fwUpdate)).Should(
			HaveField("Status.State", systemv1alpha1.FirmwareUpdateStateFailed),
		)

		By("Ensuring that no ServerMaintenance resource has been created")
		var serverMaintenanceList maintenancev1alpha1.ServerMaintenanceList
		Consistently(ObjectList(&serverMaintenanceList)).Should(HaveField("Items", BeEmpty()))

		By("Ensuring that the Server has not entered Maintenance state")
		Consistently(Object(server)).Should(
			HaveField("Status.State", metalv1alpha1.ServerStateAvailable),
		)

		By("Deleting the FirmwareUpdateDell")
		Expect(k8sClient.Delete(ctx, fwUpdate)).To(Succeed())

		By("Ensuring that the FirmwareUpdateDell has been removed")
		Eventually(Get(fwUpdate)).Should(Satisfy(apierrors.IsNotFound))
	})
})
// fakeStuckJobBMC is a minimal bmc.BMC implementation used to unit-test
// handleStuckScheduledJob in isolation, without spinning up the full mock
// Redfish HTTP server. Embedding bmc.BMC as a nil interface satisfies the
// entire (large) BMC method set; only the two methods handleStuckScheduledJob
// actually calls (GetSystemInfo and PowerOn) are overridden below. Calling any
// other method on this fake would panic, which is fine since this test never
// exercises them.
type fakeStuckJobBMC struct {
	bmc.BMC

	powerState   metalv1alpha1.ServerPowerState
	powerOnCalls int
	powerOnErr   error
}

func (f *fakeStuckJobBMC) GetSystemInfo(_ context.Context, _ string) (bmc.SystemInfo, error) {
	return bmc.SystemInfo{PowerState: schemas.PowerState(f.powerState)}, nil
}

func (f *fakeStuckJobBMC) PowerOn(_ context.Context, _ string) error {
	f.powerOnCalls++
	return f.powerOnErr
}

var _ = Describe("handleStuckScheduledJob", func() {
	const powerOnTimeout = time.Minute

	var (
		dh          *dellHandler
		r           *FirmwareUpdateReconciler
		server      *metalv1alpha1.Server
		fw          *systemv1alpha1.FirmwareUpdate
		fakeBMCImpl *fakeStuckJobBMC
	)

	BeforeEach(func() {
		dh = &dellHandler{}
		r = &FirmwareUpdateReconciler{
			Conditions:                 conditionutils.NewAccessor(conditionutils.AccessorOptions{}),
			ScheduledJobPowerOnTimeout: powerOnTimeout,
		}
		server = &metalv1alpha1.Server{
			Spec: metalv1alpha1.ServerSpec{SystemURI: "/redfish/v1/Systems/1"},
		}
		fw = &systemv1alpha1.FirmwareUpdate{}
		fakeBMCImpl = &fakeStuckJobBMC{powerState: metalv1alpha1.ServerOffPowerState}
	})

	// withIssuedCondition sets fw.Status.Conditions so ConditionScheduledJobStuckPowerOnIssued
	// is already True, simulating a PowerOn already issued for an ongoing incident.
	withIssuedCondition := func() {
		fw.Status.Conditions = []metav1.Condition{{
			Type:               ConditionScheduledJobStuckPowerOnIssued,
			Status:             metav1.ConditionTrue,
			Reason:             ReasonScheduledJobStuckPowerOnIssued,
			Message:            "Job(s) remained Scheduled with server powered off; issued PowerOn",
			LastTransitionTime: metav1.Now(),
		}}
	}

	It("does nothing when ScheduledJobPowerOnTimeout is disabled", func() {
		r.ScheduledJobPowerOnTimeout = 0
		stuckSince := metav1.NewTime(time.Now().Add(-2 * powerOnTimeout))
		fw.Status.LastProgressTime = &stuckSince

		condition, err := dh.handleStuckScheduledJob(context.Background(), fakeBMCImpl, fw, r, server, []string{"JID_1"})
		Expect(err).NotTo(HaveOccurred())
		Expect(condition).To(BeNil())
		Expect(fakeBMCImpl.powerOnCalls).To(Equal(0))
	})

	It("does nothing when no component job is stuck in Scheduled state", func() {
		condition, err := dh.handleStuckScheduledJob(context.Background(), fakeBMCImpl, fw, r, server, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(condition).To(BeNil())
		Expect(fakeBMCImpl.powerOnCalls).To(Equal(0))
	})

	It("does nothing while the no-progress timeout has not yet elapsed", func() {
		recentProgress := metav1.NewTime(time.Now().Add(-powerOnTimeout / 2))
		fw.Status.LastProgressTime = &recentProgress

		condition, err := dh.handleStuckScheduledJob(context.Background(), fakeBMCImpl, fw, r, server, []string{"JID_1"})
		Expect(err).NotTo(HaveOccurred())
		Expect(condition).To(BeNil())
		Expect(fakeBMCImpl.powerOnCalls).To(Equal(0))
	})

	It("turns the server on when a job is stuck Scheduled, the server is off, and the timeout has elapsed", func() {
		stuckSince := metav1.NewTime(time.Now().Add(-2 * powerOnTimeout))
		fw.Status.LastProgressTime = &stuckSince
		fw.Status.DellStatus = &systemv1alpha1.DellFirmwareUpdateStatus{
			ComponentJobs: []systemv1alpha1.RepositoryJob{{JobID: "JID_1", State: "Scheduled"}},
		}

		condition, err := dh.handleStuckScheduledJob(context.Background(), fakeBMCImpl, fw, r, server, []string{"JID_1"})
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeBMCImpl.powerOnCalls).To(Equal(1), "PowerOn should have been issued exactly once")

		Expect(condition).NotTo(BeNil())
		Expect(condition.Type).To(Equal(ConditionScheduledJobStuckPowerOnIssued))
		Expect(condition.Status).To(Equal(metav1.ConditionTrue))
	})

	It("defers issuing PowerOn when the Scheduled job was not yet observed as Scheduled in status", func() {
		stuckSince := metav1.NewTime(time.Now().Add(-2 * powerOnTimeout))
		fw.Status.LastProgressTime = &stuckSince
		// JID_1 is newly discovered this reconcile - ComponentJobs doesn't
		// reflect it as Scheduled yet, so it must not be treated as stuck
		// even though LastProgressTime looks stale for unrelated reasons.

		condition, err := dh.handleStuckScheduledJob(context.Background(), fakeBMCImpl, fw, r, server, []string{"JID_1"})
		Expect(err).NotTo(HaveOccurred())
		Expect(condition).To(BeNil())
		Expect(fakeBMCImpl.powerOnCalls).To(Equal(0), "PowerOn must not be issued before the job is confirmed Scheduled in status")
	})

	It("does not issue a repeat PowerOn call while the server is still booting", func() {
		withIssuedCondition()
		// Server is still Off (e.g. still booting) even though PowerOn was
		// already issued for this incident.
		fakeBMCImpl.powerState = metalv1alpha1.ServerOffPowerState

		condition, err := dh.handleStuckScheduledJob(context.Background(), fakeBMCImpl, fw, r, server, []string{"JID_1"})
		Expect(err).NotTo(HaveOccurred())
		Expect(condition).To(BeNil())
		Expect(fakeBMCImpl.powerOnCalls).To(Equal(0), "PowerOn must not be called again for the same ongoing incident")
	})

	It("resolves the condition back to False once the server is confirmed powered on", func() {
		withIssuedCondition()
		fakeBMCImpl.powerState = metalv1alpha1.ServerOnPowerState

		condition, err := dh.handleStuckScheduledJob(context.Background(), fakeBMCImpl, fw, r, server, []string{"JID_1"})
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeBMCImpl.powerOnCalls).To(Equal(0))

		Expect(condition).NotTo(BeNil())
		Expect(condition.Type).To(Equal(ConditionScheduledJobStuckPowerOnIssued))
		Expect(condition.Status).To(Equal(metav1.ConditionFalse))
	})

	It("resolves the condition back to False once no component job remains Scheduled", func() {
		withIssuedCondition()
		fakeBMCImpl.powerState = metalv1alpha1.ServerOffPowerState

		condition, err := dh.handleStuckScheduledJob(context.Background(), fakeBMCImpl, fw, r, server, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeBMCImpl.powerOnCalls).To(Equal(0))

		Expect(condition).NotTo(BeNil())
		Expect(condition.Type).To(Equal(ConditionScheduledJobStuckPowerOnIssued))
		Expect(condition.Status).To(Equal(metav1.ConditionFalse))
	})
})

// checkProgressDeadline is exercised directly (bypassing the manager/BMC mock
// server) since it only touches fw.Spec/fw.Status and the k8s client - this
// is a regression test for the bug where a FirmwareUpdate stuck retrying
// forever in Pending or Completed (e.g. a BMC error that's silently retried)
// never hit any deadline and left the server in Maintenance indefinitely in completed state.
var _ = Describe("checkProgressDeadline", func() {
	var (
		r  *FirmwareUpdateReconciler
		ns *v1.Namespace
	)

	BeforeEach(func(ctx SpecContext) {
		r = &FirmwareUpdateReconciler{
			Client:     k8sClient,
			Conditions: conditionutils.NewAccessor(conditionutils.AccessorOptions{}),
		}

		ns = &v1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "test-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
	})

	AfterEach(func(ctx SpecContext) {
		Expect(k8sClient.Delete(ctx, ns)).To(Succeed())
	})

	// newFirmwareUpdate creates a FirmwareUpdate with the given
	// ProgressDeadlineSeconds and persists the given Status (via a Status
	// subresource update) before returning it. ProgressDeadlineSeconds must be
	// set at Create time (a plain Spec field, not persisted again afterwards)
	// and Status must be persisted via its own Update call, since
	// checkProgressDeadline (via updateStatus) computes its patch from a fresh
	// DeepCopy of the passed object and the API server's response overwrites
	// the in-memory object with whatever is actually stored - any Status field
	// set only in memory beforehand would otherwise be silently lost.
	newFirmwareUpdate := func(ctx SpecContext, progressDeadlineSeconds *int32, status systemv1alpha1.FirmwareUpdateStatus) *systemv1alpha1.FirmwareUpdate {
		fw := &systemv1alpha1.FirmwareUpdate{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "test-",
				Namespace:    ns.Name,
			},
			Spec: systemv1alpha1.FirmwareUpdateSpec{
				ServerRef: &v1.LocalObjectReference{Name: "does-not-matter"},
				FirmwareUpdateTemplate: systemv1alpha1.FirmwareUpdateTemplate{
					ProgressDeadlineSeconds: progressDeadlineSeconds,
				},
			},
		}
		Expect(k8sClient.Create(ctx, fw)).To(Succeed())
		fw.Status = status
		Expect(k8sClient.Status().Update(ctx, fw)).To(Succeed())
		return fw
	}

	for _, state := range []systemv1alpha1.FirmwareUpdateState{
		systemv1alpha1.FirmwareUpdateStatePending,
		systemv1alpha1.FirmwareUpdateStateCompleted,
		systemv1alpha1.FirmwareUpdateStateInProgress,
	} {
		It(fmt.Sprintf("transitions a FirmwareUpdate stuck in %s to Failed once the deadline is exceeded", state), func(ctx SpecContext) {
			stuckSince := metav1.NewTime(time.Now().Add(-2 * time.Minute))
			maintenanceRef := &metalv1alpha1.ObjectReference{Namespace: ns.Name, Name: "some-maintenance"}
			fw := newFirmwareUpdate(ctx, new(int32(60)), systemv1alpha1.FirmwareUpdateStatus{
				State:                state,
				LastProgressTime:     &stuckSince,
				ServerMaintenanceRef: maintenanceRef,
			})

			exceeded, err := r.checkProgressDeadline(ctx, fw)
			Expect(err).NotTo(HaveOccurred())
			Expect(exceeded).To(BeTrue())
			Expect(fw.Status.State).To(Equal(systemv1alpha1.FirmwareUpdateStateFailed))
			Expect(fw.Status.Conditions).To(ContainElement(SatisfyAll(
				HaveField("Type", constants.ConditionProgressDeadlineExceeded),
				HaveField("Status", metav1.ConditionTrue),
			)))

			// checkProgressDeadline must never touch ServerMaintenanceRef: keeping
			// the server in Maintenance once permanently Failed is deliberate (a
			// human should investigate before the server rejoins the pool).
			Expect(fw.Status.ServerMaintenanceRef).To(Equal(maintenanceRef))
		})
	}

	It("does nothing while the deadline has not yet elapsed", func(ctx SpecContext) {
		recentProgress := metav1.NewTime(time.Now().Add(-time.Second))
		fw := newFirmwareUpdate(ctx, new(int32(3600)), systemv1alpha1.FirmwareUpdateStatus{
			State:            systemv1alpha1.FirmwareUpdateStateCompleted,
			LastProgressTime: &recentProgress,
		})

		exceeded, err := r.checkProgressDeadline(ctx, fw)
		Expect(err).NotTo(HaveOccurred())
		Expect(exceeded).To(BeFalse())
		Expect(fw.Status.State).To(Equal(systemv1alpha1.FirmwareUpdateStateCompleted))
	})

	It("does nothing when ProgressDeadlineSeconds is unset", func(ctx SpecContext) {
		stuckSince := metav1.NewTime(time.Now().Add(-24 * time.Hour))
		fw := &systemv1alpha1.FirmwareUpdate{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns.Name, Name: "unpersisted"},
			Status: systemv1alpha1.FirmwareUpdateStatus{
				State:            systemv1alpha1.FirmwareUpdateStateCompleted,
				LastProgressTime: &stuckSince,
			},
		}

		exceeded, err := r.checkProgressDeadline(ctx, fw)
		Expect(err).NotTo(HaveOccurred())
		Expect(exceeded).To(BeFalse())
	})

	It("does nothing when LastProgressTime has never been set", func(ctx SpecContext) {
		fw := newFirmwareUpdate(ctx, new(int32(1)), systemv1alpha1.FirmwareUpdateStatus{
			State: systemv1alpha1.FirmwareUpdateStateCompleted,
		})

		exceeded, err := r.checkProgressDeadline(ctx, fw)
		Expect(err).NotTo(HaveOccurred())
		Expect(exceeded).To(BeFalse())
	})
})
