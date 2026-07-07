// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"encoding/json"
	"testing"

	"github.com/go-logr/logr"
)

const severityCritical = "Critical"

// -- OriginOfConditionRef --

// TestOriginOfConditionRef_SpecObjectForm covers the Redfish-spec-compliant
// shape: OriginOfCondition is an object with @odata.id.
func TestOriginOfConditionRef_SpecObjectForm(t *testing.T) {
	raw := []byte(`{"@odata.id":"/redfish/v1/Chassis/1/Sensors/Fan1"}`)
	var o originOfConditionRef
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if o.ODataID != "/redfish/v1/Chassis/1/Sensors/Fan1" {
		t.Errorf("ODataID: got %q", o.ODataID)
	}
}

// TestOriginOfConditionRef_DellPlainString covers Dell iDRAC's variant:
// OriginOfCondition is a bare string instead of an object.
func TestOriginOfConditionRef_DellPlainString(t *testing.T) {
	raw := []byte(`"/redfish/v1/Chassis/System.Embedded.1/Sensors/Temp0"`)
	var o originOfConditionRef
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if o.ODataID != "/redfish/v1/Chassis/System.Embedded.1/Sensors/Temp0" {
		t.Errorf("ODataID: got %q", o.ODataID)
	}
}

// TestOriginOfConditionRef_EmptyObject covers the upstream-fix scenario:
// some BMCs send an object with an empty @odata.id when no specific origin
// is known. Must not error; ODataID stays empty.
func TestOriginOfConditionRef_EmptyObject(t *testing.T) {
	raw := []byte(`{"@odata.id":""}`)
	var o originOfConditionRef
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if o.ODataID != "" {
		t.Errorf("ODataID: got %q, want empty", o.ODataID)
	}
}

// TestOriginOfConditionRef_EmptyString covers Dell sending an empty string.
func TestOriginOfConditionRef_EmptyString(t *testing.T) {
	raw := []byte(`""`)
	var o originOfConditionRef
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if o.ODataID != "" {
		t.Errorf("ODataID: got %q, want empty", o.ODataID)
	}
}

// TestOriginOfConditionRef_Garbage covers malformed input.
func TestOriginOfConditionRef_Garbage(t *testing.T) {
	raw := []byte(`[1,2,3]`)
	var o originOfConditionRef
	if err := json.Unmarshal(raw, &o); err == nil {
		t.Fatal("expected error for non-object/non-string input")
	}
}

// -- wireEvent unmarshalling + toEvent --

// TestWireEvent_ModernRedfish covers Redfish v1.5+ shape: MessageId +
// MessageSeverity instead of the deprecated EventId/Severity, with object
// OriginOfCondition.
func TestWireEvent_ModernRedfish(t *testing.T) {
	raw := []byte(`{
		"MessageId": "EventLog.1.0.PowerSupplyFailure",
		"MessageSeverity": "Critical",
		"Message": "PSU 1 failed",
		"OriginOfCondition": {"@odata.id": "/redfish/v1/Chassis/1/PowerSupplies/1"}
	}`)
	var w wireEvent
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	ev := w.toEvent()
	if ev.EventID != "EventLog.1.0.PowerSupplyFailure" {
		t.Errorf("EventID: got %q — expected MessageId as fallback when EventId is absent", ev.EventID)
	}
	if ev.MessageID != "EventLog.1.0.PowerSupplyFailure" {
		t.Errorf("MessageID: got %q (wire MessageId must land in Event.MessageID)", ev.MessageID)
	}
	if ev.Severity != severityCritical {
		t.Errorf("Severity: got %q (MessageSeverity fallback broken)", ev.Severity)
	}
	if ev.OriginOfCondition != "/redfish/v1/Chassis/1/PowerSupplies/1" {
		t.Errorf("OriginOfCondition: got %q", ev.OriginOfCondition)
	}
	if ev.Message != "PSU 1 failed" {
		t.Errorf("Message: got %q", ev.Message)
	}
}

// TestWireEvent_LegacyRedfish covers pre-v1.5 firmware: EventId + Severity
// (no MessageId/MessageSeverity), still with object OriginOfCondition.
func TestWireEvent_LegacyRedfish(t *testing.T) {
	raw := []byte(`{
		"EventId": "SE0001",
		"Severity": "Warning",
		"Message": "Fan slow",
		"OriginOfCondition": {"@odata.id": "/redfish/v1/Chassis/1/Sensors/Fan1"}
	}`)
	var w wireEvent
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	ev := w.toEvent()
	if ev.EventID != "SE0001" {
		t.Errorf("EventID: got %q (legacy EventId fallback broken)", ev.EventID)
	}
	if ev.MessageID != "" {
		t.Errorf("MessageID: got %q, want empty (legacy firmware has no MessageId)", ev.MessageID)
	}
	if ev.Severity != "Warning" {
		t.Errorf("Severity: got %q (legacy Severity fallback broken)", ev.Severity)
	}
	if ev.OriginOfCondition != "/redfish/v1/Chassis/1/Sensors/Fan1" {
		t.Errorf("OriginOfCondition: got %q", ev.OriginOfCondition)
	}
}

// TestWireEvent_DellIDRAC covers Dell iDRAC: OriginOfCondition is a plain
// string, plus typically legacy EventId/Severity fields.
func TestWireEvent_DellIDRAC(t *testing.T) {
	raw := []byte(`{
		"EventId": "CPU0001",
		"Severity": "Critical",
		"Message": "CPU temperature critical",
		"OriginOfCondition": "/redfish/v1/Chassis/System.Embedded.1/Sensors/Temp0"
	}`)
	var w wireEvent
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	ev := w.toEvent()
	if ev.OriginOfCondition != "/redfish/v1/Chassis/System.Embedded.1/Sensors/Temp0" {
		t.Errorf("OriginOfCondition: got %q (Dell string form broken)", ev.OriginOfCondition)
	}
	if ev.EventID != "CPU0001" {
		t.Errorf("EventID: got %q", ev.EventID)
	}
	if ev.Severity != severityCritical {
		t.Errorf("Severity: got %q", ev.Severity)
	}
}

// TestWireEvent_BothFields covers BMCs sending both shapes (some
// firmware does this during the transition). Severity: the new
// MessageSeverity is the spec-blessed successor for the deprecated
// Severity — new wins. Identity: EventId (per-instance) and MessageId
// (message-type) mean DIFFERENT things — Event.EventID must reflect
// the per-instance identity so distinct occurrences dedup separately,
// while Event.MessageID gets only the message-type value.
func TestWireEvent_BothFields(t *testing.T) {
	raw := []byte(`{
		"EventId": "instance-1",
		"MessageId": "IPMI.1.0.PSGoodToBad",
		"Severity": "Warning",
		"MessageSeverity": "Critical",
		"Message": "x",
		"OriginOfCondition": {"@odata.id": "/x"}
	}`)
	var w wireEvent
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	ev := w.toEvent()
	if ev.EventID != "instance-1" {
		t.Errorf("EventID: got %q, want instance-1 (per-instance EventId must win over message-type MessageId)", ev.EventID)
	}
	if ev.MessageID != "IPMI.1.0.PSGoodToBad" {
		t.Errorf("MessageID: got %q, want the message-type value", ev.MessageID)
	}
	if ev.Severity != severityCritical {
		t.Errorf("Severity: got %q, want Critical (MessageSeverity must win)", ev.Severity)
	}
}

// TestWireEvent_AllEmpty covers a defensive case: an event with no
// identifiable fields. Should still parse without error; downstream
// dedupe by EventID will skip empty EventIDs.
func TestWireEvent_AllEmpty(t *testing.T) {
	raw := []byte(`{}`)
	var w wireEvent
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	ev := w.toEvent()
	if ev.EventID != "" || ev.Severity != "" || ev.Message != "" {
		t.Errorf("expected zero values, got %+v", ev)
	}
}

// -- wireMetricReport unmarshalling + toSamples --

// TestWireMetricReport_GofishFixture decodes the canonical Redfish
// MetricReport example shape (lifted from gofish's schema test fixture).
// Pins the happy path: spec-compliant body → one Sample with parsed value
// and the metric property carried through.
func TestWireMetricReport_GofishFixture(t *testing.T) {
	raw := []byte(`{
		"@odata.type": "#MetricReport.v1_5_0.MetricReport",
		"Id": "AvgPlatformPowerUsage",
		"Name": "Average Platform Power Usage metric report",
		"MetricValues": [
			{
				"MetricId": "AverageConsumedWatts",
				"MetricValue": "100",
				"Timestamp": "2016-11-08T12:25:00-05:00",
				"MetricProperty": "/redfish/v1/Chassis/Tray_1/Power#/0/PowerConsumedWatts"
			}
		]
	}`)
	var got wireMetricReport
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	samples := got.toSamples(logr.Discard())
	if len(samples) != 1 {
		t.Fatalf("samples: got %d, want 1 (%+v)", len(samples), samples)
	}
	s := samples[0]
	if s.MetricID != "AverageConsumedWatts" || s.Value != 100 ||
		s.MetricProperty != "/redfish/v1/Chassis/Tray_1/Power#/0/PowerConsumedWatts" {
		t.Errorf("sample fields: %+v", s)
	}
	// Gofish fixture omits Units — Sample.Unit must stay empty (no guessing).
	if s.Unit != "" {
		t.Errorf("Unit: got %q, want empty", s.Unit)
	}
}

// TestWireMetricReport_AcceptsUnitsField covers the vendor-extended shape
// (metal-operator's own decoder includes Units). Both shapes coexist on
// the wire and must round-trip.
func TestWireMetricReport_AcceptsUnitsField(t *testing.T) {
	raw := []byte(`{
		"MetricValues": [
			{"MetricId": "Inlet", "MetricValue": "23.5", "Units": "Cel"}
		]
	}`)
	var got wireMetricReport
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	samples := got.toSamples(logr.Discard())
	if len(samples) != 1 || samples[0].Unit != "Cel" || samples[0].Value != 23.5 {
		t.Errorf("sample: %+v", samples)
	}
}

// TestWireMetricReport_DropsUnparseableRows covers the offline-sensor
// scenario: a BMC emits "N/A" / "Unknown" / blank when a sensor cannot
// report. The whole batch must NOT fail — only the bad rows drop, the
// rest become Samples.
func TestWireMetricReport_DropsUnparseableRows(t *testing.T) {
	raw := []byte(`{
		"MetricValues": [
			{"MetricId": "FanSpeed", "MetricValue": "1200"},
			{"MetricId": "FanSpeed2", "MetricValue": "N/A"},
			{"MetricId": "FanSpeed3", "MetricValue": ""},
			{"MetricId": "FanSpeed4", "MetricValue": "Unknown"},
			{"MetricId": "PowerWatts", "MetricValue": "315.0"}
		]
	}`)
	var got wireMetricReport
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	samples := got.toSamples(logr.Discard())
	if len(samples) != 2 {
		t.Fatalf("samples: got %d, want 2 (the two parseable rows)", len(samples))
	}
	if samples[0].MetricID != "FanSpeed" || samples[1].MetricID != "PowerWatts" {
		t.Errorf("expected only parseable rows survived: %+v", samples)
	}
}

// TestWireMetricReport_EmptyMetricValues covers the "BMC sent a heartbeat
// MetricReport with nothing in it" case. toSamples must return nil, not
// an empty allocated slice (downstream sinks gate on len()).
func TestWireMetricReport_EmptyMetricValues(t *testing.T) {
	raw := []byte(`{"Id": "EmptyReport", "MetricValues": []}`)
	var got wireMetricReport
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if samples := got.toSamples(logr.Discard()); samples != nil {
		t.Errorf("samples: got %+v, want nil", samples)
	}
}

// TestWireMetricReport_MissingMetricValues covers a body that omits the
// MetricValues key entirely — same expectation as the empty array case.
func TestWireMetricReport_MissingMetricValues(t *testing.T) {
	raw := []byte(`{"Id": "JustAHeartbeat"}`)
	var got wireMetricReport
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if samples := got.toSamples(logr.Discard()); samples != nil {
		t.Errorf("samples: got %+v, want nil", samples)
	}
}
