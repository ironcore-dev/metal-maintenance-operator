// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package prometheus

import (
	"context"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink"
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const activeAlertGaugeName = "event_active"

// alertKey is the label tuple that uniquely identifies one active fault.
type alertKey struct {
	messageID string
	component string
	severity  string
}

type alertEntry struct {
	lastSeen time.Time
}

// ActiveAlertTracker maintains a redfish_event_active gauge that fires
// while a BMC has an active fault and drops to 0 once:
//   - a TTL elapses with no further events for that fault, OR
//   - an OK/Info clearing event arrives for the same component.
//
// It must be called before EventSink.PublishEvents so it sees OK/Info
// events (which the sink filters out) and re-sends of already-deduped
// EventIDs (which refresh the TTL).
type ActiveAlertTracker struct {
	gauge *prometheus.GaugeVec
	log   logr.Logger
	ttl   time.Duration
	nowFn func() time.Time

	mu     sync.Mutex
	active map[string]map[alertKey]alertEntry // bmcName → key → lastSeen
	series map[string]map[alertKey]struct{}   // bmcName → all keys ever written (for Forget)
}

var (
	_ manager.Runnable               = (*ActiveAlertTracker)(nil)
	_ manager.LeaderElectionRunnable = (*ActiveAlertTracker)(nil)
)

// NewActiveAlertTracker creates and registers the redfish_event_active gauge.
func NewActiveAlertTracker(reg prometheus.Registerer, ttl time.Duration, log logr.Logger) (*ActiveAlertTracker, error) {
	t := &ActiveAlertTracker{
		log:    log,
		ttl:    ttl,
		nowFn:  time.Now,
		active: make(map[string]map[alertKey]alertEntry),
		series: make(map[string]map[alertKey]struct{}),
	}
	t.gauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: metricNamespace,
		Name:      activeAlertGaugeName,
		Help:      "1 while a BMC has an active fault (set to 0 on OK/Info clear or TTL expiry).",
	}, []string{labelHostname, labelSeverity, labelMessageID, labelComponent})
	if err := reg.Register(t.gauge); err != nil {
		return nil, err
	}
	return t, nil
}

// NeedLeaderElection returns false — each pod tracks events it receives
// independently; per-pod TTL expiry must not be suppressed on non-leaders.
func (t *ActiveAlertTracker) NeedLeaderElection() bool { return false }

// Start runs the TTL expiry loop until ctx is cancelled.
func (t *ActiveAlertTracker) Start(ctx context.Context) error {
	interval := max(t.ttl/4, time.Second)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			t.expireStale()
		}
	}
}

// HandleEvents processes a batch of events before they reach EventSink.
// It refreshes TTLs for active faults and clears faults on OK/Info events.
func (t *ActiveAlertTracker) HandleEvents(bmcName string, events []sink.Event) {
	now := t.nowFn()
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, ev := range events {
		sev := canonicalSeverity(ev.Severity)
		if sev == "" {
			continue
		}
		comp := componentFromOrigin(ev.OriginOfCondition)

		if sev == "OK" || sev == "Info" {
			// Clear all active alerts for this (bmcName, component).
			t.clearComponentLocked(bmcName, comp)
			continue
		}

		key := alertKey{messageID: ev.MessageID, component: comp, severity: sev}
		t.upsertLocked(bmcName, key, now)
	}
}

// Forget removes all active-alert gauge series for the given BMC.
// Safe to call for BMCs that were never published.
func (t *ActiveAlertTracker) Forget(bmcName string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	for key := range t.series[bmcName] {
		t.gauge.Delete(prometheus.Labels{
			labelHostname:  bmcName,
			labelSeverity:  key.severity,
			labelMessageID: key.messageID,
			labelComponent: key.component,
		})
	}
	delete(t.active, bmcName)
	delete(t.series, bmcName)
}

// expireStale zeros and removes entries whose lastSeen is older than ttl.
// Caller may hold no locks — this acquires t.mu internally.
func (t *ActiveAlertTracker) expireStale() {
	now := t.nowFn()
	t.mu.Lock()
	defer t.mu.Unlock()

	for bmcName, keys := range t.active {
		for key, entry := range keys {
			if now.Sub(entry.lastSeen) > t.ttl {
				t.gauge.With(prometheus.Labels{
					labelHostname:  bmcName,
					labelSeverity:  key.severity,
					labelMessageID: key.messageID,
					labelComponent: key.component,
				}).Set(0)
				delete(keys, key)
			}
		}
		if len(keys) == 0 {
			delete(t.active, bmcName)
		}
	}
}

// clearComponentLocked zeros and removes all active entries for (bmcName, component).
// Caller must hold t.mu.
func (t *ActiveAlertTracker) clearComponentLocked(bmcName, component string) {
	keys := t.active[bmcName]
	if keys == nil {
		return
	}
	for key := range keys {
		if key.component != component {
			continue
		}
		t.gauge.With(prometheus.Labels{
			labelHostname:  bmcName,
			labelSeverity:  key.severity,
			labelMessageID: key.messageID,
			labelComponent: key.component,
		}).Set(0)
		delete(keys, key)
	}
	if len(keys) == 0 {
		delete(t.active, bmcName)
	}
}

// upsertLocked sets the gauge to 1 and records/refreshes the lastSeen timestamp.
// Caller must hold t.mu.
func (t *ActiveAlertTracker) upsertLocked(bmcName string, key alertKey, now time.Time) {
	if t.active[bmcName] == nil {
		t.active[bmcName] = make(map[alertKey]alertEntry)
	}
	if t.series[bmcName] == nil {
		t.series[bmcName] = make(map[alertKey]struct{})
	}
	t.active[bmcName][key] = alertEntry{lastSeen: now}
	t.series[bmcName][key] = struct{}{}
	t.gauge.With(prometheus.Labels{
		labelHostname:  bmcName,
		labelSeverity:  key.severity,
		labelMessageID: key.messageID,
		labelComponent: key.component,
	}).Set(1)
}
