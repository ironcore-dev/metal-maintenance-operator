// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package prometheus

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

const subscriptionStatusName = "bmc_event_subscription_status"

// SubscriptionStatusSink records the push-subscription status of each BMC as
// the redfish_bmc_event_subscription_status gauge.
//
// Label set: {hostname, vendor, model, reason}.
// Value is 1 when the BMC has an active push subscription, 0 otherwise.
// The reason label explains why push is not active (excluded, service_disabled,
// no_submit_test_event, probe_failed) or confirms it is active (subscribed).
type SubscriptionStatusSink struct {
	gauge *prometheus.GaugeVec

	mu      sync.Mutex
	tracked map[string]prometheus.Labels // bmcName → current label set
}

// SubscriptionStatusRecorder is the interface the subscription reconciler uses
// to record per-BMC subscription status. Kept separate from the Prometheus
// implementation so the reconciler package has no prometheus dependency.
type SubscriptionStatusRecorder interface {
	RecordSubscriptionStatus(bmcName, vendor, model, reason string)
	ForgetSubscriptionStatus(bmcName string)
}

var _ SubscriptionStatusRecorder = (*SubscriptionStatusSink)(nil)

func NewSubscriptionStatusSink(reg prometheus.Registerer) (*SubscriptionStatusSink, error) {
	s := &SubscriptionStatusSink{
		tracked: make(map[string]prometheus.Labels),
	}
	s.gauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace,
		Name:      subscriptionStatusName,
		Help: "Redfish event subscription status per BMC: 1 = active push subscription, " +
			"0 = pull scraping mode. The reason label explains why push is not active.",
	}, []string{labelHostname, "vendor", "model", "reason"})
	if err := reg.Register(s.gauge); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *SubscriptionStatusSink) RecordSubscriptionStatus(bmcName, vendor, model, reason string) {
	var v float64
	if reason == "subscribed" {
		v = 1
	}
	labels := prometheus.Labels{
		labelHostname: bmcName,
		"vendor":      vendor,
		"model":       model,
		"reason":      reason,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.tracked[bmcName]; ok && !labelsEqual(prev, labels) {
		s.gauge.Delete(prev)
	}
	s.tracked[bmcName] = labels
	s.gauge.With(labels).Set(v)
}

// ForgetSubscriptionStatus removes the series for the given BMC. Idempotent.
func (s *SubscriptionStatusSink) ForgetSubscriptionStatus(bmcName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.tracked[bmcName]; ok {
		s.gauge.Delete(prev)
		delete(s.tracked, bmcName)
	}
}

func labelsEqual(a, b prometheus.Labels) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
