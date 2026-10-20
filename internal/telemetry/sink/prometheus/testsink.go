// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package prometheus

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

const pipelineTestResultName = "event_pipeline_test_last_result"

// TestEventSink records the most recent pipeline health-check result per BMC
// as the redfish_event_pipeline_test_last_result gauge.
//
// Label set: {hostname}. Value is 1 for success, 0 for failure.
type TestEventSink struct {
	gauge *prometheus.GaugeVec

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
}

// Forget removes the series for the given BMC. Idempotent.
func (s *TestEventSink) Forget(bmcName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tracked, bmcName)
	s.gauge.Delete(prometheus.Labels{labelHostname: bmcName})
}
