// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package prometheus

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	pipelineTestResultName    = "event_pipeline_test_last_result"
	pipelineTestTimestampName = "event_pipeline_test_timestamp"
)

// TestEventSink records the most recent pipeline health-check result per BMC
// as the redfish_event_pipeline_test_last_result gauge (1 = success, 0 = failure)
// and the Unix timestamp of the last successful check as
// redfish_event_pipeline_test_timestamp{result="success"}.
//
// Label set for both metrics: {hostname}. The timestamp metric additionally
// carries {result="success"} so dashboard queries can filter unambiguously.
type TestEventSink struct {
	gauge     *prometheus.GaugeVec
	timestamp *prometheus.GaugeVec

	mu      sync.Mutex
	tracked map[string]struct{}
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
		tracked: make(map[string]struct{}),
	}
	s.gauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace,
		Name:      pipelineTestResultName,
		Help:      "Most recent end-to-end pipeline health-check result per BMC: 1 = success, 0 = failure.",
	}, []string{labelHostname})
	if err := reg.Register(s.gauge); err != nil {
		return nil, err
	}
	s.timestamp = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace,
		Name:      pipelineTestTimestampName,
		Help:      "Unix timestamp (seconds) of the last successful end-to-end pipeline health-check per BMC.",
	}, []string{labelHostname, "result"})
	if err := reg.Register(s.timestamp); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *TestEventSink) RecordTestResult(bmcName, result string) {
	var v float64
	if result == "success" {
		v = 1
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.tracked[bmcName] = struct{}{}
	s.gauge.With(prometheus.Labels{labelHostname: bmcName}).Set(v)
	if result == "success" {
		s.timestamp.With(prometheus.Labels{labelHostname: bmcName, "result": "success"}).SetToCurrentTime()
	}
}

// Forget removes the series for the given BMC. Idempotent.
func (s *TestEventSink) Forget(bmcName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tracked, bmcName)
	s.gauge.Delete(prometheus.Labels{labelHostname: bmcName})
	s.timestamp.Delete(prometheus.Labels{labelHostname: bmcName, "result": "success"})
}
