// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package prometheus_test

import (
	"context"
	"sync"
	"testing"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink"
	psink "github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink/prometheus"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

const (
	monitorMetricName = "redfish_monitor_reading"

	unitCel        = "Cel"
	metricPower    = "Power"
	metricInletID  = "Inlet"
	metricTypeTemp = "Temperature"
)

// metricMatch describes the label tuple to look up. Empty string fields
// wildcard-match — useful when a test only cares about one or two labels.
type metricMatch struct {
	hostname      string
	metricID      string
	valueType     string
	unit          string
	originContext string
}

func gatherMetricValue(t *testing.T, reg prometheus.Gatherer, m metricMatch) (float64, bool) {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != monitorMetricName {
			continue
		}
		for _, sample := range f.GetMetric() {
			if matchesMetric(sample, m) {
				return sample.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}

func gatherMetricCount(t *testing.T, reg prometheus.Gatherer) int {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() == monitorMetricName {
			return len(f.GetMetric())
		}
	}
	return 0
}

func matchesMetric(s *dto.Metric, m metricMatch) bool {
	got := map[string]string{}
	for _, l := range s.GetLabel() {
		got[l.GetName()] = l.GetValue()
	}
	if m.hostname != "" && got["hostname"] != m.hostname {
		return false
	}
	if m.metricID != "" && got["metric_id"] != m.metricID {
		return false
	}
	if m.valueType != "" && got["type"] != m.valueType {
		return false
	}
	if m.unit != "" && got["unit"] != m.unit {
		return false
	}
	if m.originContext != "" && got["origin_context"] != m.originContext {
		return false
	}
	return true
}

func TestMetricReportSink_PublishSetsGauge(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewMetricReportSink(reg)
	if err != nil {
		t.Fatalf("NewMetricReportSink: %v", err)
	}
	if err := s.PublishSamples(context.Background(), bmc1, []sink.Sample{
		{MetricID: "AverageConsumedWatts", Value: 100, Unit: "W", Type: "Gauge"},
		{MetricID: metricInletID, Value: 22.5, Unit: unitCel, Type: metricTypeTemp},
	}); err != nil {
		t.Fatalf("PublishSamples: %v", err)
	}
	if v, ok := gatherMetricValue(t, reg, metricMatch{
		hostname: bmc1, metricID: "AverageConsumedWatts", unit: "W", valueType: "Gauge",
	}); !ok || v != 100 {
		t.Errorf("AverageConsumedWatts: got %v (ok=%v), want 100", v, ok)
	}
	if v, ok := gatherMetricValue(t, reg, metricMatch{
		hostname: bmc1, metricID: metricInletID, unit: unitCel, valueType: metricTypeTemp,
	}); !ok || v != 22.5 {
		t.Errorf("Inlet: got %v (ok=%v), want 22.5", v, ok)
	}
}

// TestMetricReportSink_RepublishOverwrites pins last-writer-wins: same
// {hostname, metric_id, type, unit, origin_context} → second push
// replaces the first.
func TestMetricReportSink_RepublishOverwrites(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, _ := psink.NewMetricReportSink(reg)

	_ = s.PublishSamples(context.Background(), bmc1, []sink.Sample{
		{MetricID: metricPower, Value: 100, Unit: "W"},
	})
	_ = s.PublishSamples(context.Background(), bmc1, []sink.Sample{
		{MetricID: metricPower, Value: 250, Unit: "W"},
	})
	if v, ok := gatherMetricValue(t, reg, metricMatch{
		hostname: bmc1, metricID: metricPower, unit: "W",
	}); !ok || v != 250 {
		t.Errorf("Power: got %v (ok=%v), want 250 (last write wins)", v, ok)
	}
	if n := gatherMetricCount(t, reg); n != 1 {
		t.Errorf("series count: got %d, want 1", n)
	}
}

// TestMetricReportSink_DistinctMetricPropertyIsDistinctSeries pins the
// option-(a) contract: the same MetricID reported for two physical
// sensors (different MetricProperty) yields two distinct series. Without
// origin_context in the label set, the second push would overwrite the
// first and a multi-sensor reading would be silently lost.
func TestMetricReportSink_DistinctMetricPropertyIsDistinctSeries(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, _ := psink.NewMetricReportSink(reg)

	_ = s.PublishSamples(context.Background(), bmc1, []sink.Sample{
		{MetricID: metricTypeTemp, Value: 22, Unit: unitCel, MetricProperty: "/Chassis/1/Sensors/Inlet"},
		{MetricID: metricTypeTemp, Value: 38, Unit: unitCel, MetricProperty: "/Chassis/1/Sensors/Outlet"},
	})

	if n := gatherMetricCount(t, reg); n != 2 {
		t.Fatalf("series count: got %d, want 2 (one per sensor)", n)
	}
	if v, ok := gatherMetricValue(t, reg, metricMatch{
		hostname: bmc1, metricID: metricTypeTemp, originContext: "/Chassis/1/Sensors/Inlet",
	}); !ok || v != 22 {
		t.Errorf("Inlet temperature: got %v (ok=%v), want 22", v, ok)
	}
	if v, ok := gatherMetricValue(t, reg, metricMatch{
		hostname: bmc1, metricID: metricTypeTemp, originContext: "/Chassis/1/Sensors/Outlet",
	}); !ok || v != 38 {
		t.Errorf("Outlet temperature: got %v (ok=%v), want 38", v, ok)
	}
}

// TestMetricReportSink_LabelSchemaMatchesProduction pins the exact label
// set the metric carries — {hostname, metric_id, type, unit,
// origin_context} — matching metal-operator's redfish_monitor_reading.
func TestMetricReportSink_LabelSchemaMatchesProduction(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, _ := psink.NewMetricReportSink(reg)
	_ = s.PublishSamples(context.Background(), bmc1, []sink.Sample{
		{MetricID: metricTypeTemp, Value: 22, Unit: unitCel, Type: metricTypeTemp, MetricProperty: "/sensors/Inlet"},
	})

	want := map[string]bool{
		"hostname":       true,
		"metric_id":      true,
		"type":           true,
		"unit":           true,
		"origin_context": true,
	}
	families, _ := reg.Gather()
	var found bool
	for _, f := range families {
		if f.GetName() != monitorMetricName {
			continue
		}
		found = true
		for _, sample := range f.GetMetric() {
			got := map[string]bool{}
			for _, l := range sample.GetLabel() {
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
		t.Errorf("metric %q not registered", monitorMetricName)
	}
}

func TestMetricReportSink_EmptyBatchIsNoop(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, _ := psink.NewMetricReportSink(reg)
	if err := s.PublishSamples(context.Background(), bmc1, nil); err != nil {
		t.Fatalf("PublishSamples: %v", err)
	}
	if n := gatherMetricCount(t, reg); n != 0 {
		t.Errorf("series count: got %d, want 0", n)
	}
}

func TestMetricReportSink_EmptyMetricIDSkipped(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, _ := psink.NewMetricReportSink(reg)
	_ = s.PublishSamples(context.Background(), bmc1, []sink.Sample{
		{MetricID: "", Value: 99},
		{MetricID: "Good", Value: 42},
	})
	if n := gatherMetricCount(t, reg); n != 1 {
		t.Errorf("series count: got %d, want 1 (empty-ID row dropped)", n)
	}
}

func TestMetricReportSink_ForgetDeletesAllSeriesForBMC(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, _ := psink.NewMetricReportSink(reg)

	_ = s.PublishSamples(context.Background(), bmc1, []sink.Sample{
		{MetricID: metricPower, Value: 100, Unit: "W"},
		{MetricID: metricInletID, Value: 22, Unit: unitCel},
	})
	_ = s.PublishSamples(context.Background(), "bmc-2", []sink.Sample{
		{MetricID: metricPower, Value: 80, Unit: "W"},
	})

	s.Forget(bmc1)

	if _, ok := gatherMetricValue(t, reg, metricMatch{hostname: bmc1, metricID: metricPower}); ok {
		t.Errorf("bmc-1 Power series should be deleted")
	}
	if _, ok := gatherMetricValue(t, reg, metricMatch{hostname: bmc1, metricID: metricInletID}); ok {
		t.Errorf("bmc-1 Inlet series should be deleted")
	}
	if v, ok := gatherMetricValue(t, reg, metricMatch{hostname: "bmc-2", metricID: metricPower}); !ok || v != 80 {
		t.Errorf("bmc-2 Power: got %v (ok=%v), want 80", v, ok)
	}
}

func TestMetricReportSink_ForgetUnknownBMCIsNoop(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, _ := psink.NewMetricReportSink(reg)

	_ = s.PublishSamples(context.Background(), bmc1, []sink.Sample{
		{MetricID: metricPower, Value: 100, Unit: "W"},
	})
	s.Forget("never-seen")
	if v, ok := gatherMetricValue(t, reg, metricMatch{hostname: bmc1, metricID: metricPower}); !ok || v != 100 {
		t.Errorf("bmc-1 Power untouched: got %v (ok=%v)", v, ok)
	}
}

func TestMetricReportSink_ConcurrentPublishIsRaceFree(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, _ := psink.NewMetricReportSink(reg)

	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			bmc := "bmc-" + string(rune('a'+i))
			for n := range 50 {
				_ = s.PublishSamples(context.Background(), bmc, []sink.Sample{
					{MetricID: metricPower, Value: float64(n), Unit: "W"},
				})
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
