// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package prometheus

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// metricNamespace is the redfish_* prefix shared with metal-operator's
	// production metrics and the SAP redfish-exporter, so dashboards and
	// alert rules built against either continue to apply to our series.
	metricNamespace = "redfish"

	eventCounterName   = "event_alert_total"
	monitorReadingName = "monitor_reading"

	labelHostname  = "hostname"
	labelSeverity  = "severity"
	labelMessageID = "message_id"
	labelComponent = "component"

	severityCritical = "Critical"
)

// OnCriticalFunc is invoked for every Critical-severity event after
// counter mutation.
type OnCriticalFunc func(ctx context.Context, bmcName string, event sink.Event) error

// maxSeenIDsPerBMC bounds dedup state per BMC. Once exceeded, oldest
// entry evicts FIFO so EventIDs reused after a BMC reset/firmware-update
// become eligible to be counted again — process-lifetime memory would
// silently drop those legitimate events.
const maxSeenIDsPerBMC = 4096

// alertLabelKey is the label tuple a counter series is identified by.
// Forget needs every tuple a BMC has ever published so it can call
// counter.Delete on each — that's why we track them in `series` below.
type alertLabelKey struct {
	severity  string
	messageID string
	component string
}

// EventSink publishes Redfish events as the redfish_event_alert_total
// counter, deduplicated by EventID per BMC so resent pushes for the
// same active event don't double-count. Optionally fires OnCritical for
// every Critical-severity event so the readiness bridge can attach
// without a wrapper layer.
//
// Label set matches metal-operator's production schema:
// {hostname, severity, message_id, component}. component is derived from
// the trailing segment of OriginOfCondition.
type EventSink struct {
	counter *prometheus.CounterVec
	log     logr.Logger

	OnCritical OnCriticalFunc

	mu         sync.Mutex
	seenForBMC map[string]*seenSet
	series     map[string]map[alertLabelKey]struct{}
}

// seenSet is a bounded FIFO-eviction set of strings.
type seenSet struct {
	cap   int
	items map[string]struct{}
	order []string
}

func newSeenSet(cap int) *seenSet {
	return &seenSet{
		cap:   cap,
		items: make(map[string]struct{}, cap),
		order: make([]string, 0, cap),
	}
}

func (s *seenSet) contains(id string) bool {
	_, ok := s.items[id]
	return ok
}

func (s *seenSet) add(id string) {
	if _, ok := s.items[id]; ok {
		return
	}
	if len(s.order) >= s.cap {
		evict := s.order[0]
		s.order = s.order[1:]
		delete(s.items, evict)
	}
	s.items[id] = struct{}{}
	s.order = append(s.order, id)
}

var _ sink.EventSink = (*EventSink)(nil)

func NewEventSink(reg prometheus.Registerer) (*EventSink, error) {
	return NewEventSinkWithLogger(reg, logr.Discard())
}

func NewEventSinkWithLogger(reg prometheus.Registerer, log logr.Logger) (*EventSink, error) {
	s := &EventSink{
		log:        log,
		seenForBMC: make(map[string]*seenSet),
		series:     make(map[string]map[alertLabelKey]struct{}),
	}
	s.counter = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: metricNamespace,
		Name:      eventCounterName,
		Help:      "Total count of Redfish alerts/events received, labelled by hostname, severity, message_id, and originating component.",
	}, []string{labelHostname, labelSeverity, labelMessageID, labelComponent})
	if err := reg.Register(s.counter); err != nil {
		return nil, err
	}
	return s, nil
}

// PublishEvents dedupes each event by EventID per BMC and increments
// redfish_event_alert_total for previously-unseen entries.
func (s *EventSink) PublishEvents(ctx context.Context, bmcName string, events []sink.Event) error {
	if len(events) == 0 {
		return nil
	}
	s.mu.Lock()
	seenIDs := s.ensureSeenSet(bmcName)
	keys := s.ensureSeriesMap(bmcName)

	var toDispatch []sink.Event
	for _, ev := range events {
		// EventID-less events can't be deduped reliably; skip rather
		// than miscount.
		if ev.EventID == "" {
			continue
		}
		if seenIDs.contains(ev.EventID) {
			continue
		}
		if s.OnCritical != nil && strings.EqualFold(ev.Severity, severityCritical) {
			// Defer commit until OnCritical succeeds — see method doc.
			toDispatch = append(toDispatch, ev)
			continue
		}
		seenIDs.add(ev.EventID)
		s.recordEventLocked(bmcName, ev, keys)
	}
	s.mu.Unlock()

	var failed []error
	for _, ev := range toDispatch {
		if err := s.OnCritical(ctx, bmcName, ev); err != nil {
			s.log.Error(err, "Critical-event handler failed; event will be retried on the next BMC push",
				"hostname", bmcName, "eventID", ev.EventID)
			failed = append(failed, fmt.Errorf("critical event %q: %w", ev.EventID, err))
			continue
		}
		s.commitCritical(bmcName, ev)
	}
	if len(failed) > 0 {
		return errors.Join(failed...)
	}
	return nil
}

// commitCritical re-acquires the mutex to add a successfully-dispatched
// Critical event to the dedup set and increment its counter. Idempotent
// under a concurrent PublishEvents on the same BMC via the contains()
// re-check.
func (s *EventSink) commitCritical(bmcName string, ev sink.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	seenIDs := s.ensureSeenSet(bmcName)
	keys := s.ensureSeriesMap(bmcName)
	if seenIDs.contains(ev.EventID) {
		return
	}
	seenIDs.add(ev.EventID)
	s.recordEventLocked(bmcName, ev, keys)
}

// ensureSeenSet returns the bounded EventID set for the BMC, creating
// it lazily. Caller must hold s.mu.
func (s *EventSink) ensureSeenSet(bmcName string) *seenSet {
	if set, ok := s.seenForBMC[bmcName]; ok {
		return set
	}
	set := newSeenSet(maxSeenIDsPerBMC)
	s.seenForBMC[bmcName] = set
	return set
}

// ensureSeriesMap returns the per-BMC label-key set used by Forget,
// creating it lazily. Caller must hold s.mu.
func (s *EventSink) ensureSeriesMap(bmcName string) map[alertLabelKey]struct{} {
	if m, ok := s.series[bmcName]; ok {
		return m
	}
	m := make(map[alertLabelKey]struct{})
	s.series[bmcName] = m
	return m
}

// recordEventLocked increments the counter, records the label tuple,
// and logs. Caller must hold s.mu.
func (s *EventSink) recordEventLocked(bmcName string, ev sink.Event, keys map[alertLabelKey]struct{}) {
	key := alertLabelKey{
		severity:  canonicalSeverity(ev.Severity),
		messageID: ev.MessageID,
		component: componentFromOrigin(ev.OriginOfCondition),
	}
	keys[key] = struct{}{}
	s.counter.With(prometheus.Labels{
		labelHostname:  bmcName,
		labelSeverity:  key.severity,
		labelMessageID: key.messageID,
		labelComponent: key.component,
	}).Inc()
	s.log.Info("Counted Redfish event",
		"hostname", bmcName,
		"severity", key.severity,
		"messageID", key.messageID,
		"component", key.component)
}

// Forget deletes every counter series and dedup state for the BMC.
// Safe to call on never-published BMCs.
func (s *EventSink) Forget(bmcName string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	keys := s.series[bmcName]
	delete(s.seenForBMC, bmcName)
	delete(s.series, bmcName)

	for key := range keys {
		s.counter.Delete(prometheus.Labels{
			labelHostname:  bmcName,
			labelSeverity:  key.severity,
			labelMessageID: key.messageID,
			labelComponent: key.component,
		})
	}
}

// canonicalSeverity normalises the Redfish-defined severity vocabulary
// (Critical / Warning / OK / Info) to a fixed casing so "Critical" and
// "critical" don't fragment across two Prometheus series. Values outside
// the known set pass through as-is — we don't invent labels for vendor
// extensions.
func canonicalSeverity(s string) string {
	switch strings.ToLower(s) {
	case "critical":
		return "Critical"
	case "warning":
		return "Warning"
	case "ok":
		return "OK"
	case "info":
		return "Info"
	}
	return s
}

// componentFromOrigin returns the trailing path segment of a Redfish
// OriginOfCondition URI. Empty input yields "system" so the label is
// never empty.
func componentFromOrigin(origin string) string {
	origin = strings.TrimRight(origin, "/")
	if origin == "" {
		return "system"
	}
	if i := strings.LastIndex(origin, "/"); i >= 0 {
		return origin[i+1:]
	}
	return origin
}
