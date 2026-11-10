// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package prometheus_test

import (
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink"
	psink "github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink/prometheus"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	activeMetricName = "redfish_event_active"
	bmcA             = "bmc-a"
	bmcB             = "bmc-b"
	comp1Origin      = "/redfish/v1/Systems/Self/Storage/1"
	comp2Origin      = "/redfish/v1/Systems/Self/Storage/2"
	msgFanDegraded   = "Fan.Degraded"
	msgPSBad         = "IPMI.1.0.PSGoodToBad"
)

func newTracker(t *testing.T) (*psink.ActiveAlertTracker, prometheus.Gatherer) {
	t.Helper()
	reg := prometheus.NewRegistry()
	tracker, err := psink.NewActiveAlertTracker(reg, 30*time.Minute, logr.Discard())
	if err != nil {
		t.Fatalf("NewActiveAlertTracker: %v", err)
	}
	return tracker, reg
}

// gatherActiveValue returns the gauge value for a specific label tuple.
// Returns (0, false) when the series does not exist.
func gatherActiveValue(t *testing.T, reg prometheus.Gatherer, hostname, severity, messageID, component string) (float64, bool) {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != activeMetricName {
			continue
		}
		for _, m := range f.GetMetric() {
			if matchesEvent(m, hostname, severity, messageID, component) {
				return m.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}

// countActiveSeries returns how many distinct label sets are present in the gauge.
func countActiveSeries(t *testing.T, reg prometheus.Gatherer) int {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() == activeMetricName {
			return len(f.GetMetric())
		}
	}
	return 0
}

// -- tests --

func TestActiveAlertTracker_HandleEvents_SetsGauge(t *testing.T) {
	tracker, reg := newTracker(t)

	tracker.HandleEvents(bmcA, []sink.Event{
		{EventID: "W1", MessageID: msgFanDegraded, Severity: "Warning", OriginOfCondition: comp1Origin},
		{EventID: "C1", MessageID: msgPSBad, Severity: "Critical", OriginOfCondition: comp2Origin},
		{EventID: "F1", MessageID: "Vendor.Fatal", Severity: "Fatal", OriginOfCondition: comp1Origin},
	})

	if v, ok := gatherActiveValue(t, reg, bmcA, "Warning", msgFanDegraded, "1"); !ok || v != 1 {
		t.Errorf("Warning gauge: got %v ok=%v, want 1", v, ok)
	}
	if v, ok := gatherActiveValue(t, reg, bmcA, "Critical", msgPSBad, "2"); !ok || v != 1 {
		t.Errorf("Critical gauge: got %v ok=%v, want 1", v, ok)
	}
	if v, ok := gatherActiveValue(t, reg, bmcA, "Fatal", "Vendor.Fatal", "1"); !ok || v != 1 {
		t.Errorf("Fatal gauge: got %v ok=%v, want 1", v, ok)
	}
}

func TestActiveAlertTracker_HandleEvents_EmptySeverityIsNoop(t *testing.T) {
	tracker, reg := newTracker(t)

	tracker.HandleEvents(bmcA, []sink.Event{
		{EventID: "RC1", MessageID: "ResourceEvent.1.0.ResourceCreated", Severity: ""},
	})

	if n := countActiveSeries(t, reg); n != 0 {
		t.Errorf("empty severity should create no series, got %d", n)
	}
}

func TestActiveAlertTracker_HandleEvents_OKClearsMatchingComponent(t *testing.T) {
	tracker, reg := newTracker(t)

	// Set two active alerts on different components.
	tracker.HandleEvents(bmcA, []sink.Event{
		{EventID: "W1", MessageID: msgFanDegraded, Severity: "Warning", OriginOfCondition: comp1Origin},
		{EventID: "C1", MessageID: msgPSBad, Severity: "Critical", OriginOfCondition: comp2Origin},
	})
	if n := countActiveSeries(t, reg); n != 2 {
		t.Fatalf("setup: want 2 series, got %d", n)
	}

	// OK event for component "1" clears only that component.
	tracker.HandleEvents(bmcA, []sink.Event{
		{EventID: "OK1", MessageID: "Fan.OK", Severity: "OK", OriginOfCondition: comp1Origin},
	})

	if v, ok := gatherActiveValue(t, reg, bmcA, "Warning", msgFanDegraded, "1"); !ok || v != 0 {
		t.Errorf("component=1 after OK: got %v ok=%v, want gauge=0", v, ok)
	}
	if v, ok := gatherActiveValue(t, reg, bmcA, "Critical", msgPSBad, "2"); !ok || v != 1 {
		t.Errorf("component=2 after OK for different component: got %v ok=%v, want gauge=1", v, ok)
	}
}

func TestActiveAlertTracker_HandleEvents_InfoClearsMatchingComponent(t *testing.T) {
	tracker, reg := newTracker(t)

	tracker.HandleEvents(bmcA, []sink.Event{
		{EventID: "W1", MessageID: msgFanDegraded, Severity: "Warning", OriginOfCondition: comp1Origin},
		{EventID: "C1", MessageID: msgPSBad, Severity: "Critical", OriginOfCondition: comp2Origin},
	})

	tracker.HandleEvents(bmcA, []sink.Event{
		{EventID: "I1", MessageID: "Fan.Info", Severity: "Info", OriginOfCondition: comp1Origin},
	})

	if v, ok := gatherActiveValue(t, reg, bmcA, "Warning", msgFanDegraded, "1"); !ok || v != 0 {
		t.Errorf("component=1 after Info: got %v ok=%v, want gauge=0", v, ok)
	}
	if v, ok := gatherActiveValue(t, reg, bmcA, "Critical", msgPSBad, "2"); !ok || v != 1 {
		t.Errorf("component=2 should still be 1 after Info on different component: got %v ok=%v", v, ok)
	}
}

func TestActiveAlertTracker_HandleEvents_ResendRefreshesLastSeen(t *testing.T) {
	tracker, reg := newTracker(t)

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker.SetNowFn(func() time.Time { return base })

	tracker.HandleEvents(bmcA, []sink.Event{
		{EventID: "W1", MessageID: msgFanDegraded, Severity: "Warning", OriginOfCondition: comp1Origin},
	})

	// Advance to 25m — near TTL but not past it.
	tracker.SetNowFn(func() time.Time { return base.Add(25 * time.Minute) })

	// Resend refreshes lastSeen to t=25m.
	tracker.HandleEvents(bmcA, []sink.Event{
		{EventID: "W1", MessageID: msgFanDegraded, Severity: "Warning", OriginOfCondition: comp1Origin},
	})

	// Advance to 40m — this would have expired if lastSeen was still t=0.
	tracker.SetNowFn(func() time.Time { return base.Add(40 * time.Minute) })
	tracker.ExpireStale()

	if v, ok := gatherActiveValue(t, reg, bmcA, "Warning", msgFanDegraded, "1"); !ok || v != 1 {
		t.Errorf("after resend at 25m, at 40m gauge should still be 1: got %v ok=%v", v, ok)
	}

	// Advance to 60m — now past TTL from the resend at 25m.
	tracker.SetNowFn(func() time.Time { return base.Add(60 * time.Minute) })
	tracker.ExpireStale()

	if v, ok := gatherActiveValue(t, reg, bmcA, "Warning", msgFanDegraded, "1"); !ok || v != 0 {
		t.Errorf("60m after base (35m past resend at 25m), gauge should be 0: got %v ok=%v", v, ok)
	}
}

func TestActiveAlertTracker_ExpireStale_SetsGaugeToZero(t *testing.T) {
	tracker, reg := newTracker(t)

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	tracker.SetNowFn(func() time.Time { return base })

	tracker.HandleEvents(bmcA, []sink.Event{
		{EventID: "W1", MessageID: msgFanDegraded, Severity: "Warning", OriginOfCondition: comp1Origin},
	})

	if v, ok := gatherActiveValue(t, reg, bmcA, "Warning", msgFanDegraded, "1"); !ok || v != 1 {
		t.Fatalf("setup: gauge should be 1, got %v ok=%v", v, ok)
	}

	// Advance clock past TTL.
	tracker.SetNowFn(func() time.Time { return base.Add(31 * time.Minute) })
	tracker.ExpireStale()

	if v, ok := gatherActiveValue(t, reg, bmcA, "Warning", msgFanDegraded, "1"); !ok || v != 0 {
		t.Errorf("after TTL expiry: got %v ok=%v, want gauge=0", v, ok)
	}
}

func TestActiveAlertTracker_Forget_ClearsOneBMCNotOther(t *testing.T) {
	tracker, reg := newTracker(t)

	events := []sink.Event{
		{EventID: "W1", MessageID: msgFanDegraded, Severity: "Warning", OriginOfCondition: comp1Origin},
	}
	tracker.HandleEvents(bmcA, events)
	tracker.HandleEvents(bmcB, events)

	if n := countActiveSeries(t, reg); n != 2 {
		t.Fatalf("setup: want 2 series (one per BMC), got %d", n)
	}

	tracker.Forget(bmcA)

	// bmc-a series should be gone (gauge.Delete removes from registry).
	if n := countActiveSeries(t, reg); n != 1 {
		t.Errorf("after Forget(bmc-a): want 1 series, got %d", n)
	}
	if _, ok := gatherActiveValue(t, reg, bmcA, "Warning", msgFanDegraded, "1"); ok {
		t.Error("bmc-a series still present after Forget")
	}
	if v, ok := gatherActiveValue(t, reg, bmcB, "Warning", msgFanDegraded, "1"); !ok || v != 1 {
		t.Errorf("bmc-b should still be 1 after Forget(bmc-a): got %v ok=%v", v, ok)
	}
}

func TestActiveAlertTracker_Forget_NeverPublishedBMCIsNoop(t *testing.T) {
	tracker, _ := newTracker(t)
	tracker.Forget("never-published") // must not panic
}

func TestActiveAlertTracker_DuplicateRegistration_ReturnsError(t *testing.T) {
	reg := prometheus.NewRegistry()
	if _, err := psink.NewActiveAlertTracker(reg, 30*time.Minute, logr.Discard()); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	if _, err := psink.NewActiveAlertTracker(reg, 30*time.Minute, logr.Discard()); err == nil {
		t.Error("second registration on same registry should return error")
	}
}

func TestActiveAlertTracker_ConcurrentHandleAndForget(t *testing.T) {
	tracker, _ := newTracker(t)

	const goroutines = 16
	const iterations = 50
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	for range goroutines {
		go func() {
			defer wg.Done()
			for i := range iterations {
				var sev string
				if i%2 == 0 {
					sev = "Warning"
				} else {
					sev = "Critical"
				}
				tracker.HandleEvents(bmcA, []sink.Event{
					{EventID: "E1", MessageID: msgFanDegraded, Severity: sev, OriginOfCondition: comp1Origin},
				})
			}
		}()
		go func() {
			defer wg.Done()
			for range iterations {
				tracker.Forget(bmcA)
			}
		}()
	}
	wg.Wait()
	// Reach here without data race (run with -race).
}
