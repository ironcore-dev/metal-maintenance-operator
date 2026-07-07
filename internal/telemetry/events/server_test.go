// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package events_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/events"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink"
)

// recordingEventSink records every PublishEvents call for assertions.
type recordingEventSink struct {
	mu    sync.Mutex
	calls []recordedEvents
	err   error
}

type recordedEvents struct {
	bmcName string
	events  []sink.Event
}

func (s *recordingEventSink) PublishEvents(_ context.Context, name string, evs []sink.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, recordedEvents{bmcName: name, events: evs})
	return s.err
}

func (s *recordingEventSink) Forget(_ string) {}

func (s *recordingEventSink) lastCall() recordedEvents {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[len(s.calls)-1]
}

// newReceiver constructs a Receiver wired to an optional event sink. A
// nil sink leaves the field interface-nil so the no-sink fallback path
// is exercised.
func newReceiver(t *testing.T, eSink *recordingEventSink) http.Handler {
	t.Helper()
	cfg := events.Config{Addr: ":0"}
	if eSink != nil {
		cfg.EventSink = eSink
	}
	r, err := events.New(cfg)
	if err != nil {
		t.Fatalf("events.New: %v", err)
	}
	return r.Handler()
}

func doPost(t *testing.T, h http.Handler, url, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Result()
}

func TestNew_RequiresAddr(t *testing.T) {
	if _, err := events.New(events.Config{}); err == nil {
		t.Fatal("expected error for empty Addr")
	}
}

// -- alerts route --

func TestAlerts_HappyPath_EventsField(t *testing.T) {
	eSink := &recordingEventSink{}
	h := newReceiver(t, eSink)
	res := doPost(t, h, "/serverevents/alerts/bmc-1",
		`{"Events":[{"EventId":"E1","Severity":"Warning","Message":"hot"}]}`)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("status: got %d, want 204", res.StatusCode)
	}
	got := eSink.lastCall()
	if got.bmcName != "bmc-1" || len(got.events) != 1 || got.events[0].EventID != "E1" {
		t.Errorf("unexpected publish: %+v", got)
	}
}

func TestAlerts_HappyPath_AlertsField(t *testing.T) {
	eSink := &recordingEventSink{}
	h := newReceiver(t, eSink)
	res := doPost(t, h, "/serverevents/alerts/bmc-1",
		`{"Alerts":[{"EventId":"A1","Severity":"Critical","Message":"dead"}]}`)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("status: got %d, want 204", res.StatusCode)
	}
	got := eSink.lastCall()
	if len(got.events) != 1 || got.events[0].EventID != "A1" {
		t.Errorf("unexpected publish: %+v", got)
	}
}

func TestAlerts_BothFieldsEventsWins(t *testing.T) {
	eSink := &recordingEventSink{}
	h := newReceiver(t, eSink)
	res := doPost(t, h, "/serverevents/alerts/bmc-1",
		`{"Events":[{"EventId":"E1"}],"Alerts":[{"EventId":"A1"}]}`)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("status: got %d, want 204", res.StatusCode)
	}
	got := eSink.lastCall()
	if len(got.events) != 1 {
		t.Fatalf("expected 1 event (Events wins, Alerts ignored), got %d: %+v", len(got.events), got.events)
	}
	if got.events[0].EventID != "E1" {
		t.Errorf("expected EventID E1 (from Events), got %q", got.events[0].EventID)
	}
}

func TestAlerts_EmptyPayload(t *testing.T) {
	eSink := &recordingEventSink{}
	h := newReceiver(t, eSink)
	res := doPost(t, h, "/serverevents/alerts/bmc-1", `{}`)
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("status: got %d, want 204", res.StatusCode)
	}
	// Sink should have been called with a nil/empty slice.
	if len(eSink.calls) != 1 {
		t.Fatalf("expected one sink call, got %d", len(eSink.calls))
	}
	if len(eSink.calls[0].events) != 0 {
		t.Errorf("expected empty events slice, got %+v", eSink.calls[0].events)
	}
}

func TestAlerts_NoSinkReturns503(t *testing.T) {
	h := newReceiver(t, nil)
	res := doPost(t, h, "/serverevents/alerts/bmc-1", `{}`)
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want 503", res.StatusCode)
	}
}

func TestAlerts_WrongMethod(t *testing.T) {
	h := newReceiver(t, &recordingEventSink{})
	req := httptest.NewRequest(http.MethodGet, "/serverevents/alerts/bmc-1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want 405", rec.Code)
	}
}

func TestAlerts_BadJSON(t *testing.T) {
	h := newReceiver(t, &recordingEventSink{})
	res := doPost(t, h, "/serverevents/alerts/bmc-1", "{not json")
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", res.StatusCode)
	}
}

func TestAlerts_MissingBMCName(t *testing.T) {
	h := newReceiver(t, &recordingEventSink{})
	res := doPost(t, h, "/serverevents/alerts/", "{}")
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", res.StatusCode)
	}
}

// -- healthz --

func TestHealthz(t *testing.T) {
	h := newReceiver(t, &recordingEventSink{})
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if strings.TrimSpace(string(body)) != "ok" {
		t.Errorf("body: got %q, want ok", body)
	}
}

// -- lifecycle: Start/Shutdown smoke test against a real listener --

func TestReceiver_StartReturnsOnCtxCancel(t *testing.T) {
	r, err := events.New(events.Config{
		Addr:      "127.0.0.1:0",
		EventSink: &recordingEventSink{},
	})
	if err != nil {
		t.Fatalf("events.New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()

	// Give Start a moment to bind, then cancel.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start returned: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Start did not return within 3s after ctx cancel")
	}
}

func TestReceiver_AcceptsRealPOST(t *testing.T) {
	eSink := &recordingEventSink{}
	r, err := events.New(events.Config{
		Addr:      "127.0.0.1:0",
		EventSink: eSink,
	})
	if err != nil {
		t.Fatalf("events.New: %v", err)
	}
	ts := httptest.NewServer(r.Handler())
	defer ts.Close()

	res, err := http.Post(ts.URL+"/serverevents/alerts/bmc-z",
		"application/json",
		bytes.NewReader([]byte(`{"Events":[{"EventId":"E1"}]}`)))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("status: got %d, want 204", res.StatusCode)
	}
	_ = res.Body.Close()
	if len(eSink.calls) != 1 {
		t.Errorf("sink calls: got %d, want 1", len(eSink.calls))
	}
}

// -- MetricReport route --

// recordingMetricSink mirrors recordingEventSink for the metricsreport path.
type recordingMetricSink struct {
	mu    sync.Mutex
	calls []recordedSamples
	err   error
}

type recordedSamples struct {
	bmcName string
	samples []sink.Sample
}

func (s *recordingMetricSink) PublishSamples(_ context.Context, name string, samples []sink.Sample) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, recordedSamples{bmcName: name, samples: samples})
	return s.err
}

func (s *recordingMetricSink) Forget(_ string) {}

func (s *recordingMetricSink) lastCall() recordedSamples {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[len(s.calls)-1]
}

// newReceiverWithMetrics builds a receiver wired to a metric sink. The
// event sink is left nil to keep tests focused on the metricsreport path
// (alerts handler returns 503 if exercised — that's tested elsewhere).
func newReceiverWithMetrics(t *testing.T, mSink *recordingMetricSink) http.Handler {
	t.Helper()
	cfg := events.Config{Addr: ":0"}
	if mSink != nil {
		cfg.MetricReportSink = mSink
	}
	r, err := events.New(cfg)
	if err != nil {
		t.Fatalf("events.New: %v", err)
	}
	return r.Handler()
}

// TestMetricsReport_HappyPath uses the gofish-canonical MetricReport body
// shape and asserts the parsed samples reach the sink with the expected
// {MetricID, Value, MetricProperty} triple intact.
func TestMetricsReport_HappyPath(t *testing.T) {
	mSink := &recordingMetricSink{}
	h := newReceiverWithMetrics(t, mSink)
	body := `{
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
	}`
	res := doPost(t, h, "/serverevents/metricsreport/bmc-1", body)
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("status: got %d, want 204", res.StatusCode)
	}
	got := mSink.lastCall()
	if got.bmcName != "bmc-1" || len(got.samples) != 1 {
		t.Fatalf("call: %+v", got)
	}
	s := got.samples[0]
	if s.MetricID != "AverageConsumedWatts" || s.Value != 100 ||
		s.MetricProperty != "/redfish/v1/Chassis/Tray_1/Power#/0/PowerConsumedWatts" {
		t.Errorf("sample: %+v", s)
	}
}

// TestMetricsReport_UnparseableRowsDropped covers the mixed-batch case:
// one stuck sensor must not 400 the whole batch — the parseable rows
// still reach the sink. The wire decoder already pins this contract;
// this test confirms the route preserves it end-to-end.
func TestMetricsReport_UnparseableRowsDropped(t *testing.T) {
	mSink := &recordingMetricSink{}
	h := newReceiverWithMetrics(t, mSink)
	body := `{
		"MetricValues": [
			{"MetricId": "Good", "MetricValue": "42"},
			{"MetricId": "Bad",  "MetricValue": "N/A"}
		]
	}`
	res := doPost(t, h, "/serverevents/metricsreport/bmc-1", body)
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("status: got %d, want 204", res.StatusCode)
	}
	got := mSink.lastCall()
	if len(got.samples) != 1 || got.samples[0].MetricID != "Good" {
		t.Errorf("samples: %+v", got.samples)
	}
}

func TestMetricsReport_NoSinkReturns503(t *testing.T) {
	h := newReceiverWithMetrics(t, nil)
	res := doPost(t, h, "/serverevents/metricsreport/bmc-1", "{}")
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status: got %d, want 503", res.StatusCode)
	}
}

func TestMetricsReport_WrongMethod(t *testing.T) {
	h := newReceiverWithMetrics(t, &recordingMetricSink{})
	req := httptest.NewRequest(http.MethodGet, "/serverevents/metricsreport/bmc-1", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d, want 405", rec.Code)
	}
}

func TestMetricsReport_BadJSON(t *testing.T) {
	h := newReceiverWithMetrics(t, &recordingMetricSink{})
	res := doPost(t, h, "/serverevents/metricsreport/bmc-1", "{not json")
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", res.StatusCode)
	}
}

func TestMetricsReport_MissingBMCName(t *testing.T) {
	h := newReceiverWithMetrics(t, &recordingMetricSink{})
	res := doPost(t, h, "/serverevents/metricsreport/", "{}")
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", res.StatusCode)
	}
}

// TestMetricsReport_BodyTooLarge asserts the 1 MB cap fires the same as
// alerts does. The receiver shares readBody, so the path is identical;
// the test pins that we wired it (and didn't accidentally bypass).
func TestMetricsReport_BodyTooLarge(t *testing.T) {
	h := newReceiverWithMetrics(t, &recordingMetricSink{})
	// 1 MB + 1 byte of valid-looking JSON padding.
	big := bytes.Repeat([]byte("a"), (1<<20)+1)
	body := append([]byte(`{"Id":"x","Name":"`), big...)
	body = append(body, []byte(`"}`)...)
	req := httptest.NewRequest(http.MethodPost, "/serverevents/metricsreport/bmc-1", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status: got %d, want 413", rec.Code)
	}
}

// TestReceiver_NeedLeaderElectionFalse pins that the receiver runs on
// every operator pod, not just the leader. controller-runtime's default
// treats plain manager.Runnables as leader-only; without this method
// declared false, only the leader would bind the receive port while
// the Service in front continues to load-balance BMC POSTs across
// every pod — dropping (replicas-1)/replicas of Redfish events on
// steady state, and dropping events transiently during any rolling
// update.
func TestReceiver_NeedLeaderElectionFalse(t *testing.T) {
	r, err := events.New(events.Config{Addr: ":0"})
	if err != nil {
		t.Fatalf("events.New: %v", err)
	}
	ler, ok := any(r).(manager.LeaderElectionRunnable)
	if !ok {
		t.Fatal("Receiver must implement manager.LeaderElectionRunnable so its per-pod semantics are explicit; the default (leader-only) is wrong for a Service-fronted receiver")
	}
	if ler.NeedLeaderElection() {
		t.Error("NeedLeaderElection returned true; every pod must bind the receive port")
	}
}
