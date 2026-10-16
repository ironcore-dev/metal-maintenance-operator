// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package prometheus_test

import (
	"sync"
	"testing"

	psink "github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink/prometheus"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func labelVal(s *dto.Metric, name string) string {
	for _, l := range s.GetLabel() {
		if l.GetName() == name {
			return l.GetValue()
		}
	}
	return ""
}

const pipelineTestMetricName = "redfish_event_pipeline_test_last_result"

func gatherTestValue(t *testing.T, reg prometheus.Gatherer, bmcName string) (float64, bool) {
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
			if labelVal(s, "hostname") == bmcName {
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

func TestTestEventSink_RecordSuccess(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewTestEventSink(reg)
	if err != nil {
		t.Fatalf("NewTestEventSink: %v", err)
	}
	s.RecordTestResult(bmc1, "success")
	v, ok := gatherTestValue(t, reg, bmc1)
	if !ok {
		t.Fatal("series not found")
	}
	if v != 1 {
		t.Errorf("value: got %v, want 1 (success)", v)
	}
}

func TestTestEventSink_RecordFailure(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, _ := psink.NewTestEventSink(reg)
	s.RecordTestResult(bmc1, "failure")
	v, ok := gatherTestValue(t, reg, bmc1)
	if !ok {
		t.Fatal("series not found")
	}
	if v != 0 {
		t.Errorf("value: got %v, want 0 (failure)", v)
	}
}

func TestTestEventSink_OnlyOneSeriesPerBMC(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, _ := psink.NewTestEventSink(reg)
	s.RecordTestResult(bmc1, "success")
	s.RecordTestResult(bmc1, "failure")
	if n := gatherTestCount(t, reg); n != 1 {
		t.Errorf("series count: got %d, want 1", n)
	}
	v, _ := gatherTestValue(t, reg, bmc1)
	if v != 0 {
		t.Errorf("value after failure: got %v, want 0", v)
	}
}

func TestTestEventSink_FlipSuccessFailureSuccess(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, _ := psink.NewTestEventSink(reg)
	s.RecordTestResult(bmc1, "success")
	s.RecordTestResult(bmc1, "failure")
	s.RecordTestResult(bmc1, "success")
	if n := gatherTestCount(t, reg); n != 1 {
		t.Errorf("series count: got %d, want 1", n)
	}
	v, _ := gatherTestValue(t, reg, bmc1)
	if v != 1 {
		t.Errorf("value after flip back to success: got %v, want 1", v)
	}
}

func TestTestEventSink_ForgetDeletesSeriesForBMC(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, _ := psink.NewTestEventSink(reg)
	s.RecordTestResult(bmc1, "success")
	s.RecordTestResult("bmc-2", "failure")

	s.Forget(bmc1)

	if _, ok := gatherTestValue(t, reg, bmc1); ok {
		t.Error("bmc-1 series should be deleted")
	}
	if _, ok := gatherTestValue(t, reg, "bmc-2"); !ok {
		t.Error("bmc-2 series should remain")
	}
}

func TestTestEventSink_ForgetUnknownIsNoop(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, _ := psink.NewTestEventSink(reg)
	s.RecordTestResult(bmc1, "success")
	s.Forget("never-seen")
	if _, ok := gatherTestValue(t, reg, bmc1); !ok {
		t.Error("bmc-1 series should be untouched")
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
				s.RecordTestResult(bmcName, "failure")
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
