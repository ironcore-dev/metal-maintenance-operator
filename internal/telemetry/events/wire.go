// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"encoding/json"
	"strconv"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink"
)

// What this layer handles:
//
//  1. Redfish v1.5+ deprecated `EventId`/`Severity` in favour of
//     `MessageId`/`MessageSeverity`. Modern firmware (most BMCs from
//     ~2022 onward) sends only the new names. We accept both, and
//     toEvent collapses to a single value with MessageId / MessageSeverity
//     winning when both are present.
//
//  2. The Redfish spec says `OriginOfCondition` is an object
//     `{"@odata.id": "..."}`. Dell iDRAC sends a plain string. Standard
//     json.Unmarshal into a `string` field fails on the object form;
//     vice versa fails on Dell. originOfConditionRef's UnmarshalJSON
//     accepts either and resolves to a single ODataID string.
type wireEvent struct {
	EventID         string               `json:"EventId"`
	MessageID       string               `json:"MessageId"`
	Message         string               `json:"Message"`
	Severity        string               `json:"Severity"`        // deprecated since Redfish v1.5
	MessageSeverity string               `json:"MessageSeverity"` // preferred since Redfish v1.5
	EventTimestamp  string               `json:"EventTimestamp"`
	Origin          originOfConditionRef `json:"OriginOfCondition"`
}

type originOfConditionRef struct {
	ODataID string
}

// UnmarshalJSON tries the spec object form first, then falls back to a
// plain string. Both shapes resolve to the same ODataID field downstream.
func (o *originOfConditionRef) UnmarshalJSON(data []byte) error {
	var ref struct {
		ODataID string `json:"@odata.id"`
	}
	if err := json.Unmarshal(data, &ref); err == nil {
		o.ODataID = ref.ODataID
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	o.ODataID = s
	return nil
}

// toEvent collapses a wire-format event into the canonical sink.Event.
//
// Severity: MessageSeverity (Redfish v1.5+) wins over the deprecated
// Severity — a straight successor field per DSP0268.
//
// The identity fields split by purpose:
//
//   - MessageID carries wire MessageId only — never the per-instance
//     EventId. This is the Prometheus-label-safe field: MessageId
//     values are bounded by the vendor's message registry (dozens per
//     vendor), so promoting them to a label doesn't explode
//     cardinality. Legacy firmware without a MessageId yields an empty
//     MessageID rather than leaking per-instance IDs.
//
//   - EventID is the per-instance identifier used by the dedup set.
//     Prefers the wire EventId (per-instance by spec: "uniquely
//     identifies the event for the source that generated it") and
//     falls back to MessageId only when the BMC omitted EventId.
//     Without this ordering, two distinct real failures of the same
//     message type (two PSU failures sharing MessageId
//     "IPMI.1.0.PSGoodToBad") would dedup to one and the second
//     would be silently dropped.
//
// OriginOfCondition object → ODataID string via originOfConditionRef.
func (e wireEvent) toEvent() sink.Event {
	out := sink.Event{
		Message:           e.Message,
		MessageID:         e.MessageID,
		EventTimestamp:    e.EventTimestamp,
		OriginOfCondition: e.Origin.ODataID,
	}
	if e.MessageSeverity != "" {
		out.Severity = e.MessageSeverity
	} else {
		out.Severity = e.Severity
	}
	if e.EventID != "" {
		out.EventID = e.EventID
	} else {
		out.EventID = e.MessageID
	}
	return out
}

// wireMetricReport is the wire shape of a Redfish MetricReport push.
type wireMetricReport struct {
	ID           string            `json:"Id,omitempty"`
	Name         string            `json:"Name,omitempty"`
	Timestamp    string            `json:"Timestamp,omitempty"`
	MetricValues []wireMetricValue `json:"MetricValues,omitempty"`
}

type wireMetricValue struct {
	MetricID        string `json:"MetricId"`
	MetricValue     string `json:"MetricValue"`
	MetricProperty  string `json:"MetricProperty,omitempty"`
	Units           string `json:"Units,omitempty"`
	MetricValueKind string `json:"MetricValueKind,omitempty"`
	Timestamp       string `json:"Timestamp,omitempty"`
}

// toSamples parses MetricValue strings into floats.
func (r wireMetricReport) toSamples(log logr.Logger) []sink.Sample {
	if len(r.MetricValues) == 0 {
		return nil
	}
	out := make([]sink.Sample, 0, len(r.MetricValues))
	for _, mv := range r.MetricValues {
		v, err := strconv.ParseFloat(mv.MetricValue, 64)
		if err != nil {
			log.V(2).Info("Dropped unparseable MetricValue",
				"metricID", mv.MetricID, "value", mv.MetricValue)
			continue
		}
		out = append(out, sink.Sample{
			MetricID:       mv.MetricID,
			Value:          v,
			Unit:           mv.Units,
			Type:           mv.MetricValueKind,
			MetricProperty: mv.MetricProperty,
			Timestamp:      mv.Timestamp,
		})
	}
	return out
}
