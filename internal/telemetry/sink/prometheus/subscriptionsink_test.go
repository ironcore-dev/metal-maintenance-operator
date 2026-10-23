// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package prometheus_test

import (
	"sync"
	"testing"

	psink "github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink/prometheus"
	"github.com/prometheus/client_golang/prometheus"
)

const subscriptionStatusMetricName = "redfish_bmc_event_subscription_status"

// gatherSubscriptionValue returns the gauge value and whether the series was
// found for the given bmcName and reason combination.
func gatherSubscriptionValue(t *testing.T, reg prometheus.Gatherer, bmcName, reason string) (float64, bool) {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != subscriptionStatusMetricName {
			continue
		}
		for _, s := range f.GetMetric() {
			if labelVal(s, "hostname") == bmcName && labelVal(s, "reason") == reason {
				return s.GetGauge().GetValue(), true
			}
		}
	}
	return 0, false
}

// gatherSubscriptionCount returns the total number of series for the
// subscription status metric.
func gatherSubscriptionCount(t *testing.T, reg prometheus.Gatherer) int {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() == subscriptionStatusMetricName {
			return len(f.GetMetric())
		}
	}
	return 0
}

func TestSubscriptionStatusSink_SubscribedValueOne(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, err := psink.NewSubscriptionStatusSink(reg)
	if err != nil {
		t.Fatalf("NewSubscriptionStatusSink: %v", err)
	}
	s.RecordSubscriptionStatus(bmc1, "Dell Inc.", "R650", "subscribed")
	v, ok := gatherSubscriptionValue(t, reg, bmc1, "subscribed")
	if !ok {
		t.Fatal("series not found")
	}
	if v != 1 {
		t.Errorf("value: got %v, want 1 (subscribed)", v)
	}
}

func TestSubscriptionStatusSink_UnsubscribedValueZero(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, _ := psink.NewSubscriptionStatusSink(reg)
	s.RecordSubscriptionStatus(bmc1, "Dell Inc.", "R650", "service_disabled")
	v, ok := gatherSubscriptionValue(t, reg, bmc1, "service_disabled")
	if !ok {
		t.Fatal("series not found")
	}
	if v != 0 {
		t.Errorf("value: got %v, want 0 (not subscribed)", v)
	}
}

func TestSubscriptionStatusSink_LabelChangeCleansOldSeries(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, _ := psink.NewSubscriptionStatusSink(reg)

	// First record with reason=service_disabled.
	s.RecordSubscriptionStatus(bmc1, "Dell Inc.", "R650", "service_disabled")
	if n := gatherSubscriptionCount(t, reg); n != 1 {
		t.Fatalf("series count after first record: got %d, want 1", n)
	}

	// Now change reason to subscribed — old series must be deleted.
	s.RecordSubscriptionStatus(bmc1, "Dell Inc.", "R650", "subscribed")
	if n := gatherSubscriptionCount(t, reg); n != 1 {
		t.Fatalf("series count after label change: got %d, want 1 (old deleted, new created)", n)
	}
	// Old series gone.
	if _, ok := gatherSubscriptionValue(t, reg, bmc1, "service_disabled"); ok {
		t.Error("old series with reason=service_disabled should be deleted")
	}
	// New series present with value 1.
	v, ok := gatherSubscriptionValue(t, reg, bmc1, "subscribed")
	if !ok {
		t.Error("new series with reason=subscribed not found")
	}
	if v != 1 {
		t.Errorf("new series value: got %v, want 1", v)
	}
}

func TestSubscriptionStatusSink_ForgetRemovesSeries(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, _ := psink.NewSubscriptionStatusSink(reg)
	s.RecordSubscriptionStatus(bmc1, "Dell Inc.", "R650", "subscribed")
	s.RecordSubscriptionStatus("bmc-2", "HPE", "iLO 6", "service_disabled")

	s.ForgetSubscriptionStatus(bmc1)

	if _, ok := gatherSubscriptionValue(t, reg, bmc1, "subscribed"); ok {
		t.Error("bmc-1 series should be deleted after Forget")
	}
	if _, ok := gatherSubscriptionValue(t, reg, "bmc-2", "service_disabled"); !ok {
		t.Error("bmc-2 series should remain after bmc-1 Forget")
	}
}

func TestSubscriptionStatusSink_ForgetUnknownIsNoop(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, _ := psink.NewSubscriptionStatusSink(reg)
	s.RecordSubscriptionStatus(bmc1, "Dell Inc.", "R650", "subscribed")
	s.ForgetSubscriptionStatus("never-seen")
	if _, ok := gatherSubscriptionValue(t, reg, bmc1, "subscribed"); !ok {
		t.Error("bmc-1 series should be untouched after Forget of unknown BMC")
	}
}

func TestSubscriptionStatusSink_DuplicateRegistrationFails(t *testing.T) {
	reg := prometheus.NewRegistry()
	if _, err := psink.NewSubscriptionStatusSink(reg); err != nil {
		t.Fatalf("first registration: %v", err)
	}
	if _, err := psink.NewSubscriptionStatusSink(reg); err == nil {
		t.Error("second registration should fail")
	}
}

func TestSubscriptionStatusSink_ConcurrentIsRaceFree(t *testing.T) {
	reg := prometheus.NewRegistry()
	s, _ := psink.NewSubscriptionStatusSink(reg)

	var wg sync.WaitGroup
	reasons := []string{"subscribed", "service_disabled", "excluded", "probe_failed", "no_submit_test_event"}
	for i := range 4 {
		wg.Go(func() {
			bmcName := "bmc-" + string(rune('a'+i))
			for j := range 50 {
				s.RecordSubscriptionStatus(bmcName, "vendor", "model", reasons[j%len(reasons)])
			}
		})
	}
	wg.Go(func() {
		for range 25 {
			s.ForgetSubscriptionStatus("bmc-c")
		}
	})
	wg.Wait()
}
