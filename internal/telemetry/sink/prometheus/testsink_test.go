// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package prometheus_test

import (
	"sync"
	"testing"

	psink "github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink/prometheus"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

const pipelineTestMetricName = "redfish_event_pipeline_test_timestamp"

func gatherTestValue(t *testing.T, reg prometheus.Gatherer, bmcName, result string) (float64, bool) {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != pipelineTestMetricName {
			continue
		}
		for _, s := range f.GetMetric() {
			if labelVal(s, "hostname") == bmcName && labelVal(s, "result") == result {
				return s.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}

func gatherTestCount(t *testing.T, reg prometheus.Gatherer) int {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() == pipelineTestMetricName {
			return len(f.GetMetric())
		}
	}
	return 0
}

func labelVal(s *dto.Metric, name string) string {
	for _, l := range s.GetLabel() {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}

func TestTestEventSink_RecordSuccess(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewTestEventSink(reg)
	if err != nil {
		t.Fatalf("NewTestEventSink: %v", err)
	}
	s.RecordTestResult(bmc1, "success")
	v, ok := gatherTestValue(t, reg, bmc1, "success")
	if !ok {
		t.Fatal("success series not found")
	}
	if v <= 0 {
		t.Errorf("timestamp should be positive, got %v", v)
	}
}

func TestTestEventSink_BothResultsCoexist(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, _ := psink.NewTestEventSink(reg)
	s.RecordTestResult(bmc1, "success")
	s.RecordTestResult(bmc1, "failure")
	if n := gatherTestCount(t, reg); n != 2 {
		t.Errorf("series count: got %d, want 2 (success + failure)", n)
	}
	if _, ok := gatherTestValue(t, reg, bmc1, "success"); !ok {
		t.Error("success series missing")
	}
	if _, ok := gatherTestValue(t, reg, bmc1, "failure"); !ok {
		t.Error("failure series missing")
	}
}

func TestTestEventSink_ForgetDeletesAllSeriesForBMC(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, _ := psink.NewTestEventSink(reg)
	s.RecordTestResult(bmc1, "success")
	s.RecordTestResult(bmc1, "failure")
	s.RecordTestResult("bmc-2", "success")

	s.Forget(bmc1)

	if _, ok := gatherTestValue(t, reg, bmc1, "success"); ok {
		t.Error("bmc-1 success series should be deleted")
	}
	if _, ok := gatherTestValue(t, reg, bmc1, "failure"); ok {
		t.Error("bmc-1 failure series should be deleted")
	}
	if _, ok := gatherTestValue(t, reg, "bmc-2", "success"); !ok {
		t.Error("bmc-2 success series should remain")
	}
}

func TestTestEventSink_ForgetUnknownIsNoop(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, _ := psink.NewTestEventSink(reg)
	s.RecordTestResult(bmc1, "success")
	s.Forget("never-seen")
	if _, ok := gatherTestValue(t, reg, bmc1, "success"); !ok {
		t.Error("bmc-1 success series should be untouched")
	}
}

func TestTestEventSink_DuplicateRegistrationFails(t *testing.T) {
	reg := prometheus.NewRegistry()
	if _, err := psink.NewTestEventSink(reg); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	if _, err := psink.NewTestEventSink(reg); err == nil {
		t.Error("second registration should fail")
	}
}

func TestTestEventSink_ConcurrentIsRaceFree(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, _ := psink.NewTestEventSink(reg)

	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			bmcName := "bmc-" + string(rune('a'+i))
			for range 50 {
				s.RecordTestResult(bmcName, "success")
			}
		})
	}
	wg.Go(func() {
		for range 25 {
			s.Forget("bmc-c")
		}
	})
	wg.Wait()
}
