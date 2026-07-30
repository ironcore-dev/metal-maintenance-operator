// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package prometheus

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const pipelineTestTimestampName = "event_pipeline_test_timestamp"

// testSeriesKey identifies one redfish_event_pipeline_test_timestamp series.
type testSeriesKey struct{ result string }

// TestEventSink records the timestamp of the most recent pipeline health-check
// result per BMC as the redfish_event_pipeline_test_timestamp gauge.
//
// Label set: {hostname, result}. result is "success" or "failure".
// The gauge value is a Unix timestamp (seconds) so dashboards can compute
// time-since-last-success with `time() - redfish_event_pipeline_test_timestamp{result="success"}`.
type TestEventSink struct {
	gauge *prometheus.GaugeVec

	mu     sync.Mutex
	series map[string]map[testSeriesKey]struct{}
}

// TestResultRecorder is the interface the subscription reconciler uses to
// record pipeline health-check results. Kept separate from the Prometheus
// implementation so the reconciler package has no prometheus dependency.
type TestResultRecorder interface {
	RecordTestResult(bmcName, result string)
	Forget(bmcName string)
}

var _ TestResultRecorder = (*TestEventSink)(nil)

func NewTestEventSink(reg prometheus.Registerer) (*TestEventSink, error) {
	s := &TestEventSink{
		series: make(map[string]map[testSeriesKey]struct{}),
	}
	s.gauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace,
		Name:      pipelineTestTimestampName,
		Help:      "Unix timestamp of the most recent end-to-end pipeline health check per BMC and result (success/failure).",
	}, []string{labelHostname, "result"})
	if err := reg.Register(s.gauge); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *TestEventSink) RecordTestResult(bmcName, result string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	keys, ok := s.series[bmcName]
	if !ok {
		keys = make(map[testSeriesKey]struct{})
		s.series[bmcName] = keys
	}
	key := testSeriesKey{result: result}
	keys[key] = struct{}{}
	s.gauge.With(prometheus.Labels{
		labelHostname: bmcName,
		"result":      result,
	}).Set(float64(time.Now().Unix()))
}

// Forget removes all series for the given BMC. Idempotent.
func (s *TestEventSink) Forget(bmcName string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	keys := s.series[bmcName]
	delete(s.series, bmcName)
	for key := range keys {
		s.gauge.Delete(prometheus.Labels{
			labelHostname: bmcName,
			"result":      key.result,
		})
	}
}
