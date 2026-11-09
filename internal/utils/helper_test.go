// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package utils_test

import (
	"context"
	"errors"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/ironcore-dev/controller-utils/conditionutils"
	"github.com/ironcore-dev/metal-maintenance-operator/api"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/constants"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/utils"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/ironcore-dev/metal-operator/bmc"
	"github.com/stmcginnis/gofish/schemas"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestUtils(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Utils Suite")
}

// fakePowerCycleBMC is a minimal bmc.BMC implementation used to unit-test
// HandleServerPowerCycle's state machine without a real Redfish server.
// Embedding bmc.BMC as a nil interface satisfies the entire (large) BMC
// method set; only the methods HandleServerPowerCycle actually calls are
// overridden below.
type fakePowerCycleBMC struct {
	bmc.BMC

	powerState metalv1alpha1.ServerPowerState

	powerOffCalls      int
	forcePowerOffCalls int
	powerOnCalls       int

	powerOffErr      error
	forcePowerOffErr error
	powerOnErr       error
}

func (f *fakePowerCycleBMC) GetSystemInfo(_ context.Context, _ string) (bmc.SystemInfo, error) {
	return bmc.SystemInfo{PowerState: schemas.PowerState(f.powerState)}, nil
}

func (f *fakePowerCycleBMC) PowerOff(_ context.Context, _ string) error {
	f.powerOffCalls++
	return f.powerOffErr
}

func (f *fakePowerCycleBMC) ForcePowerOff(_ context.Context, _ string) error {
	f.forcePowerOffCalls++
	if f.forcePowerOffErr == nil {
		f.powerState = metalv1alpha1.ServerOffPowerState
	}
	return f.forcePowerOffErr
}

func (f *fakePowerCycleBMC) PowerOn(_ context.Context, _ string) error {
	f.powerOnCalls++
	return f.powerOnErr
}

// powerCycleHarness drives HandleServerPowerCycle across multiple simulated
// reconciles, persisting condition updates back into conditions the same way
// a real controller's status patch would, so step() can be called repeatedly
// to walk the state machine forward.
type powerCycleHarness struct {
	t *testing.T

	acc        *conditionutils.Accessor
	conditions []metav1.Condition
	bmcClient  *fakePowerCycleBMC
	server     *metalv1alpha1.Server

	powerOffTimeout time.Duration
	giveUpTimeout   time.Duration
	giveUpCalls     int
	giveUpErr       error
}

func newPowerCycleHarness(t *testing.T, initialPowerState metalv1alpha1.ServerPowerState) *powerCycleHarness {
	t.Helper()
	return &powerCycleHarness{
		t:         t,
		acc:       conditionutils.NewAccessor(conditionutils.AccessorOptions{}),
		bmcClient: &fakePowerCycleBMC{powerState: initialPowerState},
		server: &metalv1alpha1.Server{
			Spec: metalv1alpha1.ServerSpec{SystemURI: "/redfish/v1/Systems/1"},
		},
	}
}

func (h *powerCycleHarness) step() (bool, error) {
	h.t.Helper()
	args := utils.ServerRetryResetArgs{
		Conditions:       h.acc,
		StatusConditions: h.conditions,
		BMCClient:        h.bmcClient,
		Server:           h.server,
		Persist: func(_ context.Context, condition *metav1.Condition) error {
			return h.acc.UpdateSlice(&h.conditions, condition.Type,
				conditionutils.UpdateStatus(condition.Status),
				conditionutils.UpdateReason(condition.Reason),
				conditionutils.UpdateMessage(condition.Message),
			)
		},
		PowerOffTimeout: h.powerOffTimeout,
		GiveUpTimeout:   h.giveUpTimeout,
		GiveUp: func(_ context.Context) error {
			h.giveUpCalls++
			return h.giveUpErr
		},
	}
	return utils.HandleServerPowerCycle(context.Background(), args)
}

func (h *powerCycleHarness) condition(condType string) *metav1.Condition {
	h.t.Helper()
	cond, err := utils.GetCondition(h.acc, h.conditions, condType)
	if err != nil {
		h.t.Fatalf("GetCondition(%s): %v", condType, err)
	}
	return cond
}

// stepUntilReadyOrN calls step() until it reports ready, an error occurs, or
// maxSteps is exceeded (to avoid an infinite loop if the state machine never
// converges, which would indicate a bug).
func (h *powerCycleHarness) stepUntilReadyOrN(maxSteps int) (bool, error) {
	h.t.Helper()
	for range maxSteps {
		ready, err := h.step()
		if err != nil || ready {
			return ready, err
		}
	}
	h.t.Fatalf("HandleServerPowerCycle did not converge within %d steps", maxSteps)
	return false, nil
}

func TestHandleServerPowerCycle_AlreadyComplete(t *testing.T) {
	h := newPowerCycleHarness(t, metalv1alpha1.ServerOnPowerState)
	h.conditions = []metav1.Condition{{
		Type:   constants.ConditionServerResetIssued,
		Status: metav1.ConditionTrue,
		Reason: constants.ReasonResetIssued,
	}}

	ready, err := h.step()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ready {
		t.Fatal("expected ready=true when ConditionServerResetIssued is already True")
	}
	if h.bmcClient.powerOffCalls != 0 || h.bmcClient.powerOnCalls != 0 {
		t.Fatalf("expected no BMC calls, got powerOffCalls=%d powerOnCalls=%d",
			h.bmcClient.powerOffCalls, h.bmcClient.powerOnCalls)
	}
}

func TestHandleServerPowerCycle_FullHappyPath(t *testing.T) {
	h := newPowerCycleHarness(t, metalv1alpha1.ServerOnPowerState)

	// Step 1: server is On -> issues PowerOff.
	ready, err := h.step()
	if err != nil || ready {
		t.Fatalf("step 1: ready=%v err=%v, want ready=false err=nil", ready, err)
	}
	if h.bmcClient.powerOffCalls != 1 {
		t.Fatalf("expected PowerOff to be issued once, got %d calls", h.bmcClient.powerOffCalls)
	}

	// Step 2: still waiting for the BMC to report Off.
	ready, err = h.step()
	if err != nil || ready {
		t.Fatalf("step 2: ready=%v err=%v, want ready=false err=nil", ready, err)
	}
	if h.bmcClient.powerOffCalls != 1 {
		t.Fatalf("PowerOff must not be re-issued while waiting, got %d calls", h.bmcClient.powerOffCalls)
	}

	// Server actually powers off now.
	h.bmcClient.powerState = metalv1alpha1.ServerOffPowerState

	// Step 3: confirms off, marks powerOffCondition True.
	ready, err = h.step()
	if err != nil || ready {
		t.Fatalf("step 3: ready=%v err=%v, want ready=false err=nil", ready, err)
	}
	if got := h.condition(constants.ConditionServerResetPowerOffIssued).Status; got != metav1.ConditionTrue {
		t.Fatalf("expected ConditionServerResetPowerOffIssued=True, got %v", got)
	}

	// Step 4: issues PowerOn.
	ready, err = h.step()
	if err != nil || ready {
		t.Fatalf("step 4: ready=%v err=%v, want ready=false err=nil", ready, err)
	}
	if h.bmcClient.powerOnCalls != 1 {
		t.Fatalf("expected PowerOn to be issued once, got %d calls", h.bmcClient.powerOnCalls)
	}

	// Step 5: still waiting for the BMC to report On.
	ready, err = h.step()
	if err != nil || ready {
		t.Fatalf("step 5: ready=%v err=%v, want ready=false err=nil", ready, err)
	}
	if h.bmcClient.powerOnCalls != 1 {
		t.Fatalf("PowerOn must not be re-issued while waiting, got %d calls", h.bmcClient.powerOnCalls)
	}

	// Server actually powers back on now.
	h.bmcClient.powerState = metalv1alpha1.ServerOnPowerState

	// Step 6: confirms on - marks both powerOnCondition and resetCondition True in the same
	// call (no need to wait for a separate reconcile between the two), but still reports
	// ready=false so the caller does not bundle its own next steps into this reconcile.
	ready, err = h.step()
	if err != nil || ready {
		t.Fatalf("step 6: ready=%v err=%v, want ready=false err=nil", ready, err)
	}
	if got := h.condition(constants.ConditionServerResetPowerOnIssued).Status; got != metav1.ConditionTrue {
		t.Fatalf("expected ConditionServerResetPowerOnIssued=True, got %v", got)
	}
	if got := h.condition(constants.ConditionServerResetIssued).Status; got != metav1.ConditionTrue {
		t.Fatalf("expected ConditionServerResetIssued=True, got %v", got)
	}

	// Step 7: the next reconcile sees resetCondition already True and reports ready.
	ready, err = h.step()
	if err != nil {
		t.Fatalf("step 7: unexpected error: %v", err)
	}
	if !ready {
		t.Fatal("expected ready=true once ConditionServerResetIssued is True")
	}

	if h.bmcClient.powerOffCalls != 1 || h.bmcClient.powerOnCalls != 1 {
		t.Fatalf("expected exactly one PowerOff and one PowerOn over the whole cycle, got powerOffCalls=%d powerOnCalls=%d",
			h.bmcClient.powerOffCalls, h.bmcClient.powerOnCalls)
	}
}

func TestHandleServerPowerCycle_AlreadyOffSkipsPowerOff(t *testing.T) {
	h := newPowerCycleHarness(t, metalv1alpha1.ServerOffPowerState)

	// Step 1: server is already Off -> powerOffCondition is marked True immediately,
	// without ever calling PowerOff.
	ready, err := h.step()
	if err != nil || ready {
		t.Fatalf("step 1: ready=%v err=%v, want ready=false err=nil", ready, err)
	}
	if h.bmcClient.powerOffCalls != 0 {
		t.Fatalf("expected PowerOff to never be called when server already Off, got %d calls", h.bmcClient.powerOffCalls)
	}
	if got := h.condition(constants.ConditionServerResetPowerOffIssued).Status; got != metav1.ConditionTrue {
		t.Fatalf("expected ConditionServerResetPowerOffIssued=True, got %v", got)
	}

	// Server powers on right away so the power-on stage can complete without waiting.
	h.bmcClient.powerState = metalv1alpha1.ServerOnPowerState

	ready, err = h.stepUntilReadyOrN(5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ready {
		t.Fatal("expected the cycle to eventually report ready")
	}
	if h.bmcClient.powerOffCalls != 0 {
		t.Fatalf("PowerOff should never have been called, got %d calls", h.bmcClient.powerOffCalls)
	}
}

func TestHandleServerPowerCycle_ForcePowerOffEscalation(t *testing.T) {
	h := newPowerCycleHarness(t, metalv1alpha1.ServerOnPowerState)
	h.powerOffTimeout = time.Minute

	// powerOffCondition was issued well before PowerOffTimeout elapsed, and the server is
	// still reporting On (graceful power-off never completed).
	h.conditions = []metav1.Condition{{
		Type:               constants.ConditionServerResetPowerOffIssued,
		Status:             metav1.ConditionFalse,
		Reason:             constants.ReasonResetIssued,
		LastTransitionTime: metav1.NewTime(time.Now().Add(-2 * h.powerOffTimeout)),
	}}

	ready, err := h.step()
	if err != nil || ready {
		t.Fatalf("ready=%v err=%v, want ready=false err=nil", ready, err)
	}
	if h.bmcClient.forcePowerOffCalls != 1 {
		t.Fatalf("expected ForcePowerOff to be issued once after PowerOffTimeout elapsed, got %d calls",
			h.bmcClient.forcePowerOffCalls)
	}
	if got := h.condition(constants.ConditionServerResetForcePowerOffIssued).Status; got != metav1.ConditionTrue {
		t.Fatalf("expected ConditionServerResetForcePowerOffIssued=True, got %v", got)
	}

	// Forced power-off (simulated by the fake) actually took effect; next step should
	// confirm Off and move on without issuing a second ForcePowerOff.
	ready, err = h.step()
	if err != nil || ready {
		t.Fatalf("ready=%v err=%v, want ready=false err=nil", ready, err)
	}
	if h.bmcClient.forcePowerOffCalls != 1 {
		t.Fatalf("ForcePowerOff must not be re-issued, got %d calls", h.bmcClient.forcePowerOffCalls)
	}
	if got := h.condition(constants.ConditionServerResetPowerOffIssued).Status; got != metav1.ConditionTrue {
		t.Fatalf("expected ConditionServerResetPowerOffIssued=True, got %v", got)
	}
}

func TestHandleServerPowerCycle_GivesUpDuringPowerOff(t *testing.T) {
	h := newPowerCycleHarness(t, metalv1alpha1.ServerOnPowerState)
	h.giveUpTimeout = time.Minute

	// powerOffCondition was issued long before GiveUpTimeout elapsed, and the server
	// never actually powered off.
	h.conditions = []metav1.Condition{{
		Type:               constants.ConditionServerResetPowerOffIssued,
		Status:             metav1.ConditionFalse,
		Reason:             constants.ReasonResetIssued,
		LastTransitionTime: metav1.NewTime(time.Now().Add(-2 * h.giveUpTimeout)),
	}}

	ready, err := h.step()
	if err == nil {
		t.Fatal("expected an error when giving up on a stuck power-cycle")
	}
	if ready {
		t.Fatal("expected ready=false when giving up")
	}
	if h.giveUpCalls != 1 {
		t.Fatalf("expected GiveUp to be called once, got %d calls", h.giveUpCalls)
	}
	resetCond := h.condition(constants.ConditionServerResetIssued)
	if resetCond.Status != metav1.ConditionFalse || resetCond.Reason != constants.ReasonServerResetGaveUp {
		t.Fatalf("expected ConditionServerResetIssued={False, ServerResetGaveUp}, got {%v, %v}",
			resetCond.Status, resetCond.Reason)
	}
}

// TestHandleServerPowerCycle_PowerOnStageHasIndependentGiveUpBudget is a regression test for
// the per-stage give-up timer: time already spent waiting in the power-off stage must not
// count against the power-on stage's own GiveUpTimeout budget.
func TestHandleServerPowerCycle_PowerOnStageHasIndependentGiveUpBudget(t *testing.T) {
	h := newPowerCycleHarness(t, metalv1alpha1.ServerOffPowerState)
	h.giveUpTimeout = time.Minute

	h.conditions = []metav1.Condition{
		{
			// The power-off stage completed long ago - well past GiveUpTimeout - but that must
			// not matter since this stage is already done (Status=True).
			Type:               constants.ConditionServerResetPowerOffIssued,
			Status:             metav1.ConditionTrue,
			Reason:             constants.ReasonResetIssued,
			LastTransitionTime: metav1.NewTime(time.Now().Add(-10 * h.giveUpTimeout)),
		},
		{
			// The power-on stage was only just issued.
			Type:               constants.ConditionServerResetPowerOnIssued,
			Status:             metav1.ConditionFalse,
			Reason:             constants.ReasonResetIssued,
			LastTransitionTime: metav1.Now(),
		},
	}

	ready, err := h.step()
	if err != nil {
		t.Fatalf("unexpected error: %v (power-on stage should not give up based on the power-off stage's age)", err)
	}
	if ready {
		t.Fatal("expected ready=false, still waiting for power-on to be confirmed")
	}
	if h.giveUpCalls != 0 {
		t.Fatalf("expected GiveUp to not be called, got %d calls", h.giveUpCalls)
	}
}

func TestHandleServerPowerCycle_SelfHealsAfterGivingUp(t *testing.T) {
	h := newPowerCycleHarness(t, metalv1alpha1.ServerOnPowerState)
	h.giveUpTimeout = time.Minute

	// A previous call already gave up while waiting for power-on to be confirmed.
	h.conditions = []metav1.Condition{
		{
			Type:   constants.ConditionServerResetIssued,
			Status: metav1.ConditionFalse,
			Reason: constants.ReasonServerResetGaveUp,
		},
		{
			Type:               constants.ConditionServerResetPowerOffIssued,
			Status:             metav1.ConditionTrue,
			Reason:             constants.ReasonResetIssued,
			LastTransitionTime: metav1.NewTime(time.Now().Add(-2 * h.giveUpTimeout)),
		},
		{
			Type:               constants.ConditionServerResetPowerOnIssued,
			Status:             metav1.ConditionFalse,
			Reason:             constants.ReasonResetIssued,
			LastTransitionTime: metav1.NewTime(time.Now().Add(-2 * h.giveUpTimeout)),
		},
	}

	// The server has since recovered on its own (power state is On).
	ready, err := h.stepUntilReadyOrN(5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ready {
		t.Fatal("expected the cycle to self-heal and report ready once the server recovered")
	}
	if h.giveUpCalls != 0 {
		t.Fatalf("expected GiveUp to not be called once the server already recovered, got %d calls", h.giveUpCalls)
	}
}

// --- HandleBMCReset ---

type bmcResetHarness struct {
	t *testing.T

	acc       *conditionutils.Accessor
	condition *metav1.Condition

	recentlyReset bool
	resetPending  bool
	issueCalls    int
	persistCalls  int
}

func newBMCResetHarness(t *testing.T) *bmcResetHarness {
	t.Helper()
	acc := conditionutils.NewAccessor(conditionutils.AccessorOptions{})
	cond := &metav1.Condition{Type: constants.ConditionResetIssued, Status: metav1.ConditionFalse}
	return &bmcResetHarness{t: t, acc: acc, condition: cond}
}

func (h *bmcResetHarness) step() (bool, error) {
	h.t.Helper()
	args := utils.BMCResetArgs{
		Condition: h.condition,
		Reason:    constants.ReasonResetIssued,
		RecentlyReset: func(_ context.Context) (bool, error) {
			return h.recentlyReset, nil
		},
		IssueReset: func(_ context.Context) error {
			h.issueCalls++
			return nil
		},
		ResetPending: func(_ context.Context) (bool, error) {
			return h.resetPending, nil
		},
		Persist: func(_ context.Context) error {
			h.persistCalls++
			return nil
		},
	}
	return utils.HandleBMCReset(context.Background(), h.acc, args)
}

func TestHandleBMCReset_AlreadyComplete(t *testing.T) {
	h := newBMCResetHarness(t)
	h.condition.Status = metav1.ConditionTrue

	ready, err := h.step()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ready {
		t.Fatal("expected ready=true when Condition is already True")
	}
	if h.issueCalls != 0 {
		t.Fatalf("expected IssueReset to not be called, got %d calls", h.issueCalls)
	}
}

func TestHandleBMCReset_SkipsWhenRecentlyReset(t *testing.T) {
	h := newBMCResetHarness(t)
	h.recentlyReset = true

	ready, err := h.step()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ready {
		t.Fatal("expected ready=true when the BMC was recently reset")
	}
	if h.issueCalls != 0 {
		t.Fatalf("expected IssueReset to not be called when recently reset, got %d calls", h.issueCalls)
	}
	if h.condition.Status != metav1.ConditionTrue || h.condition.Reason != constants.ReasonResetIssued {
		t.Fatalf("expected Condition={True, %s}, got {%v, %v}",
			constants.ReasonResetIssued, h.condition.Status, h.condition.Reason)
	}
}

func TestHandleBMCReset_FullFlow(t *testing.T) {
	h := newBMCResetHarness(t)
	h.resetPending = true

	// Step 1: issues the reset.
	ready, err := h.step()
	if err != nil {
		t.Fatalf("step 1: unexpected error: %v", err)
	}
	if ready {
		t.Fatal("step 1: expected ready=false on the call that issues the reset")
	}
	if h.issueCalls != 1 {
		t.Fatalf("expected IssueReset to be called once, got %d calls", h.issueCalls)
	}
	if h.condition.Status != metav1.ConditionFalse || h.condition.Reason != constants.ReasonResetIssued {
		t.Fatalf("expected Condition={False, %s}, got {%v, %v}",
			constants.ReasonResetIssued, h.condition.Status, h.condition.Reason)
	}

	// Step 2: reset is still pending.
	ready, err = h.step()
	if err != nil {
		t.Fatalf("step 2: unexpected error: %v", err)
	}
	if ready {
		t.Fatal("step 2: expected ready=false while reset is still pending")
	}
	if h.issueCalls != 1 {
		t.Fatalf("IssueReset must not be called again while pending, got %d calls", h.issueCalls)
	}

	// The reset settles.
	h.resetPending = false

	// Step 3: this is the exact call that marks Condition True - per the documented return
	// contract, ready must still be false here; the caller must wait for the next reconcile.
	ready, err = h.step()
	if err != nil {
		t.Fatalf("step 3: unexpected error: %v", err)
	}
	if ready {
		t.Fatal("step 3: expected ready=false on the call that completes the reset (per documented contract)")
	}
	if h.condition.Status != metav1.ConditionTrue || h.condition.Reason != constants.ReasonResetIssued {
		t.Fatalf("expected Condition={True, %s}, got {%v, %v}",
			constants.ReasonResetIssued, h.condition.Status, h.condition.Reason)
	}

	// Step 4: the next reconcile sees Condition already True and reports ready.
	ready, err = h.step()
	if err != nil {
		t.Fatalf("step 4: unexpected error: %v", err)
	}
	if !ready {
		t.Fatal("step 4: expected ready=true once Condition is already True")
	}
}

func TestHandleBMCReset_PropagatesResetPendingError(t *testing.T) {
	h := newBMCResetHarness(t)

	wantErr := errors.New("bmc unreachable")
	args := utils.BMCResetArgs{
		Condition: h.condition,
		Reason:    constants.ReasonResetIssued,
		RecentlyReset: func(_ context.Context) (bool, error) {
			return false, nil
		},
		IssueReset: func(_ context.Context) error {
			return nil
		},
		ResetPending: func(_ context.Context) (bool, error) {
			return false, wantErr
		},
		Persist: func(_ context.Context) error {
			return nil
		},
	}

	// First call issues the reset.
	if _, err := utils.HandleBMCReset(context.Background(), h.acc, args); err != nil {
		t.Fatalf("unexpected error issuing reset: %v", err)
	}

	// Second call hits the failing ResetPending check.
	ready, err := utils.HandleBMCReset(context.Background(), h.acc, args)
	if ready {
		t.Fatal("expected ready=false on error")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected error to wrap %v, got %v", wantErr, err)
	}
}

var _ = Describe("SortedSettingsFlowGroups", func() {
	It("returns items sorted by ascending priority", func() {
		flow := []api.SettingsFlowItem{
			{Name: "c", Priority: 3},
			{Name: "a", Priority: 1},
			{Name: "b", Priority: 2},
		}
		sorted := utils.SortedSettingsFlowGroups(flow)
		Expect(sorted).To(HaveLen(3))
		Expect(sorted[0].Name).To(Equal("a"))
		Expect(sorted[1].Name).To(Equal("b"))
		Expect(sorted[2].Name).To(Equal("c"))
	})

	It("preserves relative order for equal priorities (stable sort)", func() {
		flow := []api.SettingsFlowItem{
			{Name: "x", Priority: 1},
			{Name: "y", Priority: 1},
		}
		sorted := utils.SortedSettingsFlowGroups(flow)
		Expect(sorted[0].Name).To(Equal("x"))
		Expect(sorted[1].Name).To(Equal("y"))
	})

	It("does not modify the original slice", func() {
		flow := []api.SettingsFlowItem{
			{Name: "b", Priority: 2},
			{Name: "a", Priority: 1},
		}
		_ = utils.SortedSettingsFlowGroups(flow)
		Expect(flow[0].Name).To(Equal("b"))
	})
})
