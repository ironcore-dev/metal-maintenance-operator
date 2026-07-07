// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// Package sink defines the consumer interfaces telemetry sinks must satisfy.
// Concrete sinks (Prometheus) live in subpackages.
//
// EventSink consumes Redfish Event-format pushes (alerts). MetricReportSink
// consumes Redfish MetricReport-format pushes (numeric readings).
package sink

import "context"

// Event is one decoded Redfish event. Wire-level vendor quirks (Redfish
// v1.5 field renames, Dell iDRAC plain-string OriginOfCondition) are
// resolved by the events package before reaching a sink.
type Event struct {
	EventID           string
	MessageID         string
	Message           string
	Severity          string
	EventTimestamp    string
	OriginOfCondition string
}

// EventSink consumes a batch of events from one BMC. Implementations must
// be safe for concurrent use. The same EventID may appear in successive
// batches while the event is active — counting sinks dedupe.
type EventSink interface {
	PublishEvents(ctx context.Context, bmcName string, events []Event) error
	Forget(bmcName string)
}

// Sample is one decoded MetricReport reading.
type Sample struct {
	MetricID       string
	Value          float64
	Unit           string
	Type           string // Redfish MetricValueKind, e.g. "Temperature", "Gauge"; empty when the BMC omits it.
	MetricProperty string
	Timestamp      string
}

// MetricReportSink consumes a batch of samples from one BMC.
type MetricReportSink interface {
	PublishSamples(ctx context.Context, bmcName string, samples []Sample) error
	Forget(bmcName string)
}
