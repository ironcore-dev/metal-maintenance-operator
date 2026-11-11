// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package prometheus

import (
	"context"
	"sync"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	labelMetricID      = "metric_id"
	labelType          = "type"
	labelUnit          = "unit"
	labelOriginContext = "origin_context"

	// defaultMaxMetricBMCs bounds how many distinct BMC names the MetricReportSink
	// will track before dropping incoming samples for unknown hosts.
	defaultMaxMetricBMCs = 2000
	// defaultMaxMetricSeriesPerBMC bounds how many distinct
	// {metric_id,type,unit,origin_context} label tuples will be minted per BMC.
	// Real BMCs report tens to low-hundreds of distinct sensors; 1000 is well
	// above that while keeping total series under 2 million.
	defaultMaxMetricSeriesPerBMC = 1000
)

// metricSeriesKey identifies one redfish_monitor_reading time series.
// Forget walks every key a BMC has touched and calls gauge.Delete so
// stale series don't linger across BMC removal.
type metricSeriesKey struct {
	metricID      string
	valueType     string
	unit          string
	originContext string
}

// MetricReportSink publishes Redfish MetricReport pushes as the
// redfish_monitor_reading gauge.
//
// Label set matches metal-operator's production schema:
// {hostname, metric_id, type, unit, origin_context}. origin_context
// carries MetricProperty so per-sensor readings (same MetricId reported
// for multiple physical sensors) stay distinct.
type MetricReportSink struct {
	gauge *prometheus.GaugeVec
	log   logr.Logger

	maxBMCs         int
	maxSeriesPerBMC int

	mu     sync.Mutex
	series map[string]map[metricSeriesKey]struct{}
}

var _ sink.MetricReportSink = (*MetricReportSink)(nil)

func NewMetricReportSink(reg prometheus.Registerer) (*MetricReportSink, error) {
	return NewMetricReportSinkWithLogger(reg, logr.Discard())
}

func NewMetricReportSinkWithLogger(reg prometheus.Registerer, log logr.Logger) (*MetricReportSink, error) {
	return NewMetricReportSinkWithOptions(reg, log, defaultMaxMetricBMCs, defaultMaxMetricSeriesPerBMC)
}

// NewMetricReportSinkWithOptions constructs a MetricReportSink with explicit
// cardinality caps. maxBMCs bounds distinct BMC names; maxSeriesPerBMC bounds
// distinct label tuples per BMC. Samples exceeding either cap are dropped.
func NewMetricReportSinkWithOptions(reg prometheus.Registerer, log logr.Logger, maxBMCs, maxSeriesPerBMC int) (*MetricReportSink, error) {
	s := &MetricReportSink{
		log:             log,
		maxBMCs:         maxBMCs,
		maxSeriesPerBMC: maxSeriesPerBMC,
		series:          make(map[string]map[metricSeriesKey]struct{}),
	}
	s.gauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace,
		Name:      monitorReadingName,
		Help:      "Latest value pushed via Redfish MetricReport event, labelled by hostname, metric_id, type, unit, and origin_context (the source MetricProperty).",
	}, []string{labelHostname, labelMetricID, labelType, labelUnit, labelOriginContext})
	if err := reg.Register(s.gauge); err != nil {
		return nil, err
	}
	return s, nil
}

// PublishSamples holds the lock across BOTH the series-map mutation
// and gauge.Set so a concurrent Forget can't delete bookkeeping
// between us recording the key and writing the gauge.
func (s *MetricReportSink) PublishSamples(_ context.Context, bmcName string, samples []sink.Sample) error {
	if len(samples) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	keys, ok := s.series[bmcName]
	if !ok {
		if len(s.series) >= s.maxBMCs {
			s.log.V(1).Info("Dropping samples: BMC name limit reached", "hostname", bmcName, "limit", s.maxBMCs)
			return nil
		}
		keys = make(map[metricSeriesKey]struct{})
		s.series[bmcName] = keys
	}

	for _, sm := range samples {
		if sm.MetricID == "" {
			continue
		}
		key := metricSeriesKey{
			metricID:      sm.MetricID,
			valueType:     sm.Type,
			unit:          sm.Unit,
			originContext: sm.MetricProperty,
		}
		if _, exists := keys[key]; !exists && len(keys) >= s.maxSeriesPerBMC {
			s.log.V(1).Info("Dropping sample: per-BMC series limit reached",
				"hostname", bmcName, "metricID", sm.MetricID, "limit", s.maxSeriesPerBMC)
			continue
		}
		keys[key] = struct{}{}
		s.gauge.With(prometheus.Labels{
			labelHostname:      bmcName,
			labelMetricID:      key.metricID,
			labelType:          key.valueType,
			labelUnit:          key.unit,
			labelOriginContext: key.originContext,
		}).Set(sm.Value)
	}
	s.log.V(2).Info("Published MetricReport samples",
		"hostname", bmcName, "count", len(samples))
	return nil
}

// Forget is idempotent — safe to call on never-published BMCs.
func (s *MetricReportSink) Forget(bmcName string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	keys := s.series[bmcName]
	delete(s.series, bmcName)

	for key := range keys {
		s.gauge.Delete(prometheus.Labels{
			labelHostname:      bmcName,
			labelMetricID:      key.metricID,
			labelType:          key.valueType,
			labelUnit:          key.unit,
			labelOriginContext: key.originContext,
		})
	}
}
