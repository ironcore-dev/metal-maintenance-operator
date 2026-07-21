// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package prometheus_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink"
	psink "github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink/prometheus"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

const (
	alertMetricName = "redfish_event_alert_total"

	alert1EventID   = "Alert1"
	alert2EventID   = "Alert2"
	psGoodToBadMsg  = "IPMI.1.0.PSGoodToBad"
	resetMePleaseID = "reset-me-please"
	bmc1            = "bmc-1"

	sevWarning  = "Warning"
	sevCritical = "Critical"
)

// gatherEventCount returns the number of distinct label combinations for
// the alert counter in reg.
func gatherEventCount(t *testing.T, reg prometheus.Gatherer) int {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() == alertMetricName {
			return len(f.GetMetric())
		}
	}
	return 0
}

// gatherEventValue returns the counter value for one specific label tuple.
// Pass empty messageID/component to wildcard-match — useful for tests that
// don't care about the per-event labels and just want the value for a
// given hostname/severity.
func gatherEventValue(t *testing.T, reg prometheus.Gatherer, hostname, severity, messageID, component string) (float64, bool) {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != alertMetricName {
			continue
		}
		for _, m := range f.GetMetric() {
			if matchesEvent(m, hostname, severity, messageID, component) {
				return m.GetCounter().GetValue(), true
			}
		}
	}
	return 0, false
}

// sumEventValue returns the sum of counter values across every series
// matching hostname+severity (any message_id/component). Tests that
// publish multiple distinct EventIDs for the same severity use this to
// assert total counts.
func sumEventValue(t *testing.T, reg prometheus.Gatherer, hostname, severity string) float64 {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var total float64
	for _, f := range families {
		if f.GetName() != alertMetricName {
			continue
		}
		for _, m := range f.GetMetric() {
			if matchesEvent(m, hostname, severity, "", "") {
				total += m.GetCounter().GetValue()
			}
		}
	}
	return total
}

func matchesEvent(m *dto.Metric, hostname, severity, messageID, component string) bool {
	got := map[string]string{}
	for _, l := range m.GetLabel() {
		got[l.GetName()] = l.GetValue()
	}
	if got["hostname"] != hostname || got["severity"] != severity {
		return false
	}
	if messageID != "" && got["message_id"] != messageID {
		return false
	}
	if component != "" && got["component"] != component {
		return false
	}
	return true
}

// -- tests --

func TestEventSink_PublishEvents_CountsOnce(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewEventSink(reg)
	if err != nil {
		t.Fatalf("NewEventSink: %v", err)
	}

	events := []sink.Event{
		{EventID: alert1EventID, MessageID: alert1EventID, Severity: sevWarning, OriginOfCondition: "/redfish/v1/Chassis/1/Sensors/Fan1"},
		{EventID: alert2EventID, MessageID: alert2EventID, Severity: sevCritical, OriginOfCondition: "/redfish/v1/Systems/1/Memory/DIMM3"},
	}
	if err := s.PublishEvents(context.Background(), bmc1, events); err != nil {
		t.Fatalf("PublishEvents: %v", err)
	}

	// Two distinct EventIDs → two series (label set now includes message_id).
	if got := gatherEventCount(t, reg); got != 2 {
		t.Errorf("series count: got %d, want 2", got)
	}
	if v, ok := gatherEventValue(t, reg, bmc1, sevWarning, alert1EventID, "Fan1"); !ok || v != 1 {
		t.Errorf("Alert1/Fan1: got %v ok=%v, want 1", v, ok)
	}
	if v, ok := gatherEventValue(t, reg, bmc1, sevCritical, alert2EventID, "DIMM3"); !ok || v != 1 {
		t.Errorf("Alert2/DIMM3: got %v ok=%v, want 1", v, ok)
	}
}

// TestEventSink_DistinctEventIDsSameSeverity_AreDistinctSeries pins the
// production-parity label set: message_id is part of the labels, so
// distinct EventIDs land on distinct series. (The pre-rename design
// collapsed these onto one series; this test guards the new contract.)
func TestEventSink_DistinctEventIDsSameSeverity_AreDistinctSeries(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewEventSink(reg)
	if err != nil {
		t.Fatalf("NewEventSink: %v", err)
	}

	_ = s.PublishEvents(context.Background(), bmc1, []sink.Event{
		{EventID: alert1EventID, MessageID: alert1EventID, Severity: sevWarning},
		{EventID: alert2EventID, MessageID: alert2EventID, Severity: sevWarning},
		{EventID: "Alert3", MessageID: "Alert3", Severity: sevWarning},
	})

	if got := gatherEventCount(t, reg); got != 3 {
		t.Fatalf("series count: got %d, want 3 (one per EventID)", got)
	}
	if v := sumEventValue(t, reg, bmc1, sevWarning); v != 3 {
		t.Errorf("Warning sum: got %v, want 3", v)
	}
}

func TestEventSink_DedupesAcrossPolls(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewEventSink(reg)
	if err != nil {
		t.Fatalf("NewEventSink: %v", err)
	}

	batch := []sink.Event{{EventID: alert1EventID, MessageID: alert1EventID, Severity: sevWarning}}

	_ = s.PublishEvents(context.Background(), bmc1, batch)
	_ = s.PublishEvents(context.Background(), bmc1, batch)
	_ = s.PublishEvents(context.Background(), bmc1, batch)

	v, ok := gatherEventValue(t, reg, bmc1, sevWarning, alert1EventID, "system")
	if !ok {
		t.Fatal("Warning series missing")
	}
	if v != 1 {
		t.Errorf("dedupe failed: counter=%v, want 1", v)
	}
}

func TestEventSink_NewEventsAreCounted(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewEventSink(reg)
	if err != nil {
		t.Fatalf("NewEventSink: %v", err)
	}

	_ = s.PublishEvents(context.Background(), bmc1, []sink.Event{
		{EventID: alert1EventID, MessageID: alert1EventID, Severity: sevWarning},
	})
	_ = s.PublishEvents(context.Background(), bmc1, []sink.Event{
		{EventID: alert1EventID, MessageID: alert1EventID, Severity: sevWarning},  // already seen
		{EventID: alert2EventID, MessageID: alert2EventID, Severity: sevCritical}, // NEW
	})

	if got := gatherEventCount(t, reg); got != 2 {
		t.Errorf("series count: got %d, want 2", got)
	}
	if v, _ := gatherEventValue(t, reg, bmc1, sevCritical, alert2EventID, "system"); v != 1 {
		t.Errorf("Critical: got %v, want 1", v)
	}
}

func TestEventSink_EmptyBatchIsNoop(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewEventSink(reg)
	if err != nil {
		t.Fatalf("NewEventSink: %v", err)
	}
	if err := s.PublishEvents(context.Background(), bmc1, nil); err != nil {
		t.Errorf("nil batch: %v", err)
	}
	if err := s.PublishEvents(context.Background(), bmc1, []sink.Event{}); err != nil {
		t.Errorf("empty batch: %v", err)
	}
	if got := gatherEventCount(t, reg); got != 0 {
		t.Errorf("empty batch should not create series; got %d", got)
	}
}

func TestEventSink_EmptyEventIDIsSkipped(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewEventSink(reg)
	if err != nil {
		t.Fatalf("NewEventSink: %v", err)
	}
	_ = s.PublishEvents(context.Background(), bmc1, []sink.Event{
		{EventID: "", Severity: sevWarning},
		{EventID: "Real", MessageID: "Real", Severity: sevWarning},
	})
	if got := gatherEventCount(t, reg); got != 1 {
		t.Errorf("series count: got %d, want 1 (empty EventID dropped)", got)
	}
	if v, _ := gatherEventValue(t, reg, bmc1, sevWarning, "Real", "system"); v != 1 {
		t.Errorf("Warning counter: got %v, want 1", v)
	}
}

// TestEventSink_LabelSchemaMatchesProduction pins the exact label set
// the metric carries — {hostname, severity, message_id, component} —
// matching metal-operator's redfish_event_alert_total. Any future
// rename or addition that breaks downstream dashboards trips this test.
func TestEventSink_LabelSchemaMatchesProduction(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewEventSink(reg)
	if err != nil {
		t.Fatalf("NewEventSink: %v", err)
	}
	_ = s.PublishEvents(context.Background(), bmc1, []sink.Event{
		{EventID: "E1", MessageID: "E1", Severity: sevWarning, OriginOfCondition: "/redfish/v1/Chassis/1/Sensors/Fan1"},
	})

	want := map[string]bool{
		"hostname":   true,
		"severity":   true,
		"message_id": true,
		"component":  true,
	}
	families, _ := reg.Gather()
	var found bool
	for _, f := range families {
		if f.GetName() != alertMetricName {
			continue
		}
		found = true
		for _, m := range f.GetMetric() {
			got := map[string]bool{}
			for _, l := range m.GetLabel() {
				got[l.GetName()] = true
			}
			if len(got) != len(want) {
				t.Errorf("label count: got %d, want %d (got=%v)", len(got), len(want), got)
			}
			for k := range want {
				if !got[k] {
					t.Errorf("missing required label %q (got=%v)", k, got)
				}
			}
			for k := range got {
				if !want[k] {
					t.Errorf("unexpected label %q (got=%v)", k, got)
				}
			}
		}
	}
	if !found {
		t.Errorf("metric %q not registered", alertMetricName)
	}
}

// TestEventSink_SeverityIsCanonicalised guards against label
// fragmentation: BMCs report severity in varying casings ("Critical",
// "critical", "CRITICAL"). The sink must land all variants on one
// canonical severity rather than three distinct label values.
func TestEventSink_SeverityIsCanonicalised(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewEventSink(reg)
	if err != nil {
		t.Fatalf("NewEventSink: %v", err)
	}
	_ = s.PublishEvents(context.Background(), bmc1, []sink.Event{
		{EventID: "E1", MessageID: "E1", Severity: sevCritical},
		{EventID: "E2", MessageID: "E2", Severity: "critical"},
		{EventID: "E3", MessageID: "E3", Severity: "CRITICAL"},
	})

	if got := gatherEventCount(t, reg); got != 3 {
		t.Errorf("series count: got %d, want 3", got)
	}
	for _, id := range []string{"E1", "E2", "E3"} {
		if v, ok := gatherEventValue(t, reg, bmc1, sevCritical, id, "system"); !ok || v != 1 {
			t.Errorf("%s under canonical Critical: got %v ok=%v, want 1", id, v, ok)
		}
	}
	if _, ok := gatherEventValue(t, reg, bmc1, "critical", "E2", "system"); ok {
		t.Error("uncanonicalised severity 'critical' leaked into a label")
	}
	if _, ok := gatherEventValue(t, reg, bmc1, "CRITICAL", "E3", "system"); ok {
		t.Error("uncanonicalised severity 'CRITICAL' leaked into a label")
	}
}

// TestEventSink_UnknownSeverityPassesThrough covers the escape hatch:
// vendor-specific severity values outside the Redfish enum reach the
// label as-is rather than being silently rewritten.
func TestEventSink_UnknownSeverityPassesThrough(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewEventSink(reg)
	if err != nil {
		t.Fatalf("NewEventSink: %v", err)
	}
	_ = s.PublishEvents(context.Background(), bmc1, []sink.Event{
		{EventID: "E1", MessageID: "E1", Severity: "Fatal"},
	})
	if _, ok := gatherEventValue(t, reg, bmc1, "Fatal", "E1", "system"); !ok {
		t.Error("unknown severity 'Fatal' was rewritten instead of passed through")
	}
}

// TestEventSink_LegacyFirmware_MessageIDEmpty_LabelIsEmpty pins the
// bounded-cardinality contract: legacy Redfish firmware sends only the
// deprecated EventId (per-instance, unbounded) with no MessageId. The
// dedup identity uses EventID, but the message_id label MUST come from
// the empty MessageID — landing the counter under an empty label — so
// unbounded EventIds can't grow the series space per BMC.
func TestEventSink_LegacyFirmware_MessageIDEmpty_LabelIsEmpty(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewEventSink(reg)
	if err != nil {
		t.Fatalf("NewEventSink: %v", err)
	}
	// Two distinct legacy events: different EventID (dedup keys), but
	// both have empty MessageID. They must collapse onto ONE series
	// with message_id="".
	_ = s.PublishEvents(context.Background(), bmc1, []sink.Event{
		{EventID: "SEL0001", MessageID: "", Severity: sevWarning},
		{EventID: "SEL0002", MessageID: "", Severity: sevWarning},
	})

	if got := gatherEventCount(t, reg); got != 1 {
		t.Errorf("series count: got %d, want 1 (two events with empty MessageID must share a series)", got)
	}
	v, ok := gatherEventValue(t, reg, bmc1, sevWarning, "", "system")
	if !ok {
		t.Fatal("expected series with empty message_id label")
	}
	if v != 2 {
		t.Errorf("counter: got %v, want 2 (both events counted)", v)
	}
}

// TestEventSink_LabelIsMessageID_NotEventID pins the modern-firmware
// case: when both fields are set and differ, the label MUST reflect the
// stable MessageID, not the per-instance EventID.
func TestEventSink_LabelIsMessageID_NotEventID(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewEventSink(reg)
	if err != nil {
		t.Fatalf("NewEventSink: %v", err)
	}
	_ = s.PublishEvents(context.Background(), bmc1, []sink.Event{
		{EventID: "instance-1", MessageID: psGoodToBadMsg, Severity: sevCritical},
	})

	if _, ok := gatherEventValue(t, reg, bmc1, sevCritical, psGoodToBadMsg, "system"); !ok {
		t.Error("label picked up EventID instead of MessageID")
	}
	if _, ok := gatherEventValue(t, reg, bmc1, sevCritical, "instance-1", "system"); ok {
		t.Error("EventID leaked into message_id label")
	}
}

func TestEventSink_Forget_DeletesAllSeriesForBMC(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewEventSink(reg)
	if err != nil {
		t.Fatalf("NewEventSink: %v", err)
	}
	batch := []sink.Event{
		{EventID: alert1EventID, MessageID: alert1EventID, Severity: sevWarning},
		{EventID: alert2EventID, MessageID: alert2EventID, Severity: sevCritical},
	}
	_ = s.PublishEvents(context.Background(), bmc1, batch)
	_ = s.PublishEvents(context.Background(), "bmc-2", batch)

	// 2 BMCs × 2 EventIDs → 4 series.
	if got := gatherEventCount(t, reg); got != 4 {
		t.Fatalf("setup: got %d series, want 4", got)
	}
	s.Forget(bmc1)
	if got := gatherEventCount(t, reg); got != 2 {
		t.Errorf("after Forget(bmc-1): got %d series, want 2", got)
	}
	if _, ok := gatherEventValue(t, reg, bmc1, sevWarning, alert1EventID, "system"); ok {
		t.Error("bmc-1 Warning series still present after Forget")
	}
	if _, ok := gatherEventValue(t, reg, "bmc-2", sevWarning, alert1EventID, "system"); !ok {
		t.Error("bmc-2 series was deleted by Forget(bmc-1)")
	}

	// After Forget, re-publishing the same event must count from 1 again.
	_ = s.PublishEvents(context.Background(), bmc1, batch)
	v, _ := gatherEventValue(t, reg, bmc1, sevWarning, alert1EventID, "system")
	if v != 1 {
		t.Errorf("after Forget+republish: counter=%v, want 1", v)
	}
}

func TestEventSink_Forget_UnknownBMCIsNoop(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewEventSink(reg)
	if err != nil {
		t.Fatalf("NewEventSink: %v", err)
	}
	s.Forget("never-published")
}

func TestEventSink_NewEventSink_DuplicateRegistrationFails(t *testing.T) {
	reg := prometheus.NewRegistry()
	if _, err := psink.NewEventSink(reg); err != nil {
		t.Fatalf("first NewEventSink: %v", err)
	}
	if _, err := psink.NewEventSink(reg); err == nil {
		t.Error("second NewEventSink on same registry should fail")
	}
}

func TestEventSink_ConcurrentSafe(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewEventSink(reg)
	if err != nil {
		t.Fatalf("NewEventSink: %v", err)
	}

	const goroutines = 16
	const iterations = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range iterations {
				_ = s.PublishEvents(context.Background(), bmc1, []sink.Event{
					{EventID: alert1EventID, MessageID: alert1EventID, Severity: sevWarning},
				})
			}
		}()
	}
	wg.Wait()

	// 800 concurrent publishes of the same EventID; dedup → counter == 1.
	v, ok := gatherEventValue(t, reg, bmc1, sevWarning, alert1EventID, "system")
	if !ok || v != 1 {
		t.Errorf("concurrent dedup failed: counter=%v ok=%v, want 1", v, ok)
	}
}

func TestEventSink_ReusedEventIDAfterEviction_IsCounted(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewEventSink(reg)
	if err != nil {
		t.Fatalf("NewEventSink: %v", err)
	}

	if err := s.PublishEvents(context.Background(), bmc1,
		[]sink.Event{{EventID: resetMePleaseID, MessageID: resetMePleaseID, Severity: sevWarning}}); err != nil {
		t.Fatalf("PublishEvents seed: %v", err)
	}

	// Overflow the per-BMC dedup cap (4096) so resetMePleaseID evicts.
	flood := make([]sink.Event, 4096)
	for i := range flood {
		id := fmt.Sprintf("flood-%d", i)
		flood[i] = sink.Event{EventID: id, MessageID: id, Severity: sevWarning}
	}
	if err := s.PublishEvents(context.Background(), bmc1, flood); err != nil {
		t.Fatalf("PublishEvents flood: %v", err)
	}

	// Re-submit resetMePleaseID — MUST count again after eviction.
	if err := s.PublishEvents(context.Background(), bmc1,
		[]sink.Event{{EventID: resetMePleaseID, MessageID: resetMePleaseID, Severity: sevWarning}}); err != nil {
		t.Fatalf("PublishEvents reuse: %v", err)
	}

	// The seed and the reuse share labels (same EventID, same component),
	// so they sit on the same series and the counter for that series == 2.
	v, ok := gatherEventValue(t, reg, bmc1, sevWarning, resetMePleaseID, "system")
	if !ok {
		t.Fatal("reset-me-please series missing")
	}
	if v != 2 {
		t.Errorf("reuse after eviction: series counter=%v, want 2 (seed + reuse)", v)
	}
}

// -- OnCritical callback --

type onCriticalRecorder struct {
	mu   sync.Mutex
	bmcs []string
	evs  []sink.Event
	err  error
}

func (r *onCriticalRecorder) handle(_ context.Context, bmcName string, ev sink.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bmcs = append(r.bmcs, bmcName)
	r.evs = append(r.evs, ev)
	return r.err
}

func (r *onCriticalRecorder) snapshot() (bmcs []string, evs []sink.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bmcs...), append([]sink.Event(nil), r.evs...)
}

func TestEventSink_OnCritical_FiresForCriticalEvent(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewEventSink(reg)
	if err != nil {
		t.Fatalf("NewEventSink: %v", err)
	}
	rec := &onCriticalRecorder{}
	s.OnCritical = rec.handle

	if err := s.PublishEvents(context.Background(), bmc1,
		[]sink.Event{{EventID: "E1", MessageID: "E1", Severity: sevCritical, Message: "crit"}}); err != nil {
		t.Fatal(err)
	}

	bmcs, evs := rec.snapshot()
	if len(evs) != 1 {
		t.Fatalf("OnCritical fired %d times, want 1", len(evs))
	}
	if bmcs[0] != bmc1 || evs[0].EventID != "E1" {
		t.Errorf("OnCritical got (%q, %q), want (bmc-1, E1)", bmcs[0], evs[0].EventID)
	}
}

func TestEventSink_OnCritical_SeverityCaseInsensitive(t *testing.T) {
	for _, sev := range []string{"Critical", "critical", "CRITICAL", "cRiTiCaL"} {
		t.Run(sev, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			s, err := psink.NewEventSink(reg)
			if err != nil {
				t.Fatalf("NewEventSink: %v", err)
			}
			rec := &onCriticalRecorder{}
			s.OnCritical = rec.handle

			if err := s.PublishEvents(context.Background(), bmc1,
				[]sink.Event{{EventID: "E1", MessageID: "E1", Severity: sev}}); err != nil {
				t.Fatal(err)
			}
			_, evs := rec.snapshot()
			if len(evs) != 1 {
				t.Errorf("severity %q: OnCritical fired %d times, want 1", sev, len(evs))
			}
		})
	}
}

func TestEventSink_OnCritical_SkipsNonCritical(t *testing.T) {
	for _, sev := range []string{sevWarning, "OK", "Info", "Fatal", "", "something"} {
		t.Run(sev, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			s, err := psink.NewEventSink(reg)
			if err != nil {
				t.Fatalf("NewEventSink: %v", err)
			}
			rec := &onCriticalRecorder{}
			s.OnCritical = rec.handle

			if err := s.PublishEvents(context.Background(), bmc1,
				[]sink.Event{{EventID: "E1", MessageID: "E1", Severity: sev}}); err != nil {
				t.Fatal(err)
			}
			_, evs := rec.snapshot()
			if len(evs) != 0 {
				t.Errorf("severity %q: OnCritical fired %d times, want 0", sev, len(evs))
			}
		})
	}
}

// TestEventSink_OnCritical_ErrorIsSurfaced pins the at-least-once
// contract: a failed OnCritical must propagate as a non-nil
// PublishEvents return so the HTTP receiver replies 500 and the BMC
// retries. The counter must NOT increment (otherwise a retry would
// double-count), and the EventID must NOT be in the dedup set (checked
// implicitly by the next test, which republishes and expects a fresh
// OnCritical fire).
func TestEventSink_OnCritical_ErrorIsSurfaced(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewEventSink(reg)
	if err != nil {
		t.Fatalf("NewEventSink: %v", err)
	}
	rec := &onCriticalRecorder{err: assertOnCriticalErr}
	s.OnCritical = rec.handle

	err = s.PublishEvents(context.Background(), bmc1,
		[]sink.Event{{EventID: "E1", MessageID: "E1", Severity: sevCritical}})
	if err == nil {
		t.Fatal("OnCritical failure must propagate as non-nil PublishEvents return")
	}
	if _, ok := gatherEventValue(t, reg, bmc1, sevCritical, "E1", "system"); ok {
		t.Errorf("Critical counter incremented on OnCritical failure; want no series")
	}
}

// TestEventSink_OnCriticalError_ReturnsErrorAndDoesNotDedupe pins the
// retry contract in detail: after a failed OnCritical the EventID is
// not in the dedup set, so the next PublishEvents call for the same
// EventID fires OnCritical again. This is what makes BMC-side retries
// (via the HTTP 500 the receiver returns) actually recover the
// readiness signal.
func TestEventSink_OnCriticalError_ReturnsErrorAndDoesNotDedupe(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewEventSink(reg)
	if err != nil {
		t.Fatalf("NewEventSink: %v", err)
	}

	// First call: OnCritical fails.
	failing := &onCriticalRecorder{err: assertOnCriticalErr}
	s.OnCritical = failing.handle
	batch := []sink.Event{{EventID: "E1", MessageID: "E1", Severity: sevCritical}}
	if err := s.PublishEvents(context.Background(), bmc1, batch); err == nil {
		t.Fatal("expected error from failing OnCritical")
	}
	if _, ok := gatherEventValue(t, reg, bmc1, sevCritical, "E1", "system"); ok {
		t.Errorf("counter incremented despite OnCritical failure")
	}

	// Swap in a succeeding OnCritical; simulate the BMC's retry.
	succeeding := &onCriticalRecorder{}
	s.OnCritical = succeeding.handle
	if err := s.PublishEvents(context.Background(), bmc1, batch); err != nil {
		t.Fatalf("retry PublishEvents: %v", err)
	}
	_, evs := succeeding.snapshot()
	if len(evs) != 1 {
		t.Fatalf("retry: OnCritical fired %d times, want 1 (dedup should have released the EventID)", len(evs))
	}
	if v, ok := gatherEventValue(t, reg, bmc1, sevCritical, "E1", "system"); !ok || v != 1 {
		t.Errorf("counter after retry: got %v ok=%v, want 1", v, ok)
	}
}

// TestEventSink_OnCriticalError_NonCriticalsInBatchStillDeduped covers
// a mixed batch: [Warning, Critical] where OnCritical fails. The
// non-Critical event MUST be counted and deduped so a BMC-side retry
// (triggered by the HTTP 500 our error induces) doesn't double-count
// it. Only the failed Critical stays retryable.
func TestEventSink_OnCriticalError_NonCriticalsInBatchStillDeduped(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewEventSink(reg)
	if err != nil {
		t.Fatalf("NewEventSink: %v", err)
	}
	rec := &onCriticalRecorder{err: assertOnCriticalErr}
	s.OnCritical = rec.handle

	batch := []sink.Event{
		{EventID: "W1", MessageID: "W1", Severity: sevWarning},
		{EventID: "C1", MessageID: "C1", Severity: sevCritical},
	}
	if err := s.PublishEvents(context.Background(), bmc1, batch); err == nil {
		t.Fatal("expected error from failing OnCritical")
	}
	// Warning was committed on the first pass.
	if v, ok := gatherEventValue(t, reg, bmc1, sevWarning, "W1", "system"); !ok || v != 1 {
		t.Errorf("Warning counter: got %v ok=%v, want 1", v, ok)
	}
	// Critical was NOT committed.
	if _, ok := gatherEventValue(t, reg, bmc1, sevCritical, "C1", "system"); ok {
		t.Errorf("Critical counter incremented despite OnCritical failure")
	}

	// Simulate BMC retry with the same batch. Warning must be deduped
	// (no second increment); Critical must retry OnCritical.
	rec2 := &onCriticalRecorder{}
	s.OnCritical = rec2.handle
	if err := s.PublishEvents(context.Background(), bmc1, batch); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if v, _ := gatherEventValue(t, reg, bmc1, sevWarning, "W1", "system"); v != 1 {
		t.Errorf("Warning double-counted on retry: got %v, want 1", v)
	}
	if v, _ := gatherEventValue(t, reg, bmc1, sevCritical, "C1", "system"); v != 1 {
		t.Errorf("Critical after retry: got %v, want 1", v)
	}
	_, evs := rec2.snapshot()
	if len(evs) != 1 {
		t.Errorf("retry OnCritical fired %d times, want 1", len(evs))
	}
}

func TestEventSink_OnCritical_NilFieldIsNoop(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewEventSink(reg)
	if err != nil {
		t.Fatalf("NewEventSink: %v", err)
	}
	if err := s.PublishEvents(context.Background(), bmc1,
		[]sink.Event{{EventID: "E1", MessageID: "E1", Severity: sevCritical}}); err != nil {
		t.Fatal(err)
	}
	v, ok := gatherEventValue(t, reg, bmc1, sevCritical, "E1", "system")
	if !ok || v != 1 {
		t.Errorf("Critical series: got=%v ok=%v, want 1", v, ok)
	}
}

// TestEventSink_DistinctEventIDs_SameMessageID_BothCount pins the
// per-instance dedup contract: two events whose wire MessageId matches
// (same message-type) but whose wire EventId differs (distinct
// instances) MUST both count and BOTH fire OnCritical. This guards
// against a subtle earlier bug where dedup used MessageId, so two real
// PSU failures sharing psGoodToBadMsg would silently drop the
// second occurrence — the operator would learn of the first PSU
// failure and never patch a Server condition for the second.
func TestEventSink_DistinctEventIDs_SameMessageID_BothCount(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewEventSink(reg)
	if err != nil {
		t.Fatalf("NewEventSink: %v", err)
	}
	rec := &onCriticalRecorder{}
	s.OnCritical = rec.handle

	// Two events: same MessageID (message-type), different EventID
	// (instance identity).
	_ = s.PublishEvents(context.Background(), bmc1, []sink.Event{
		{EventID: "instance-1", MessageID: psGoodToBadMsg, Severity: sevCritical},
		{EventID: "instance-2", MessageID: psGoodToBadMsg, Severity: sevCritical},
	})

	_, evs := rec.snapshot()
	if len(evs) != 2 {
		t.Errorf("OnCritical fired %d times, want 2 (distinct EventIDs are distinct events)", len(evs))
	}
	// Same MessageID → same counter series → value 2.
	if v, ok := gatherEventValue(t, reg, bmc1, sevCritical, psGoodToBadMsg, "system"); !ok || v != 2 {
		t.Errorf("counter for shared message_id: got %v ok=%v, want 2", v, ok)
	}
}

func TestEventSink_OnCritical_DedupesAcrossBatches(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewEventSink(reg)
	if err != nil {
		t.Fatalf("NewEventSink: %v", err)
	}
	rec := &onCriticalRecorder{}
	s.OnCritical = rec.handle

	batch := []sink.Event{{EventID: "E1", MessageID: "E1", Severity: sevCritical}}
	_ = s.PublishEvents(context.Background(), bmc1, batch)
	_ = s.PublishEvents(context.Background(), bmc1, batch)
	_ = s.PublishEvents(context.Background(), bmc1, batch)

	_, evs := rec.snapshot()
	if len(evs) != 1 {
		t.Errorf("OnCritical fired %d times for the same EventID, want 1", len(evs))
	}
}

var assertOnCriticalErr = errSentinel("apiserver down")

type errSentinel string

func (e errSentinel) Error() string { return string(e) }
