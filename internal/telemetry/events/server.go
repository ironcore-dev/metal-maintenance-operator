// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// Package events implements an HTTP receiver for Redfish Event-format
// subscriptions. BMCs post here on their own cadence (or on threshold
// triggers) instead of us polling them.
//
// Destination URL shape, set by the subscription manager:
//
//	http://<receiver>:<port>/serverevents/alerts/{bmcName}
//	http://<receiver>:<port>/serverevents/<subscriberID>/alerts/{bmcName}         // when Config.SubscriberID is set
//	http://<receiver>:<port>/serverevents/metricsreport/{bmcName}
//	http://<receiver>:<port>/serverevents/<subscriberID>/metricsreport/{bmcName}  // when Config.SubscriberID is set
package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

const (
	// maxBodyBytes caps incoming POSTs. 1 MB matches the upstream metal-operator limit
	maxBodyBytes    = 1 << 20
	shutdownTimeout = 5 * time.Second
	pathPrefix      = "/serverevents/"
)

// Config for the Receiver.
type Config struct {
	Addr             string                // Listen address, required (e.g. ":9092").
	EventSink        sink.EventSink        // nil → alerts route returns 503.
	MetricReportSink sink.MetricReportSink // nil → metricsreport route returns 503.
	// SubscriberID scopes the receiver's route under a per-subscriber
	// path segment.
	SubscriberID string
	Log          logr.Logger
}

// Receiver is an HTTP server that decodes Redfish event POSTs and
// forwards them to the configured event sink.
type Receiver struct {
	cfg         Config
	mux         *http.ServeMux
	srv         *http.Server
	log         logr.Logger
	alertsPath  string
	metricsPath string
}

var (
	_ manager.Runnable               = (*Receiver)(nil)
	_ manager.LeaderElectionRunnable = (*Receiver)(nil)
)

// NeedLeaderElection returns false so every operator pod binds the
// receive port, not just the leader. The Service that fronts this
// receiver selects on control-plane labels and load-balances BMC
// POSTs across all backend pods; without this method, controller-
// runtime's default puts the Receiver in the leader-only group and
// (replicas-1)/replicas of BMC POSTs land on pods that never bound
// the port. Even at replicas=1, rolling updates create a transient
// two-pod window with the same failure mode.
func (r *Receiver) NeedLeaderElection() bool { return false }

// New constructs a Receiver.
func New(cfg Config) (*Receiver, error) {
	if cfg.Addr == "" {
		return nil, errors.New("events: Addr is required")
	}
	log := cfg.Log
	if (log == logr.Logger{}) {
		log = logr.Discard()
	}
	r := &Receiver{cfg: cfg, log: log}
	r.alertsPath = alertsPathFor(cfg.SubscriberID)
	r.metricsPath = metricsPathFor(cfg.SubscriberID)
	r.routes()
	r.srv = &http.Server{
		Addr:              cfg.Addr,
		Handler:           r.mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return r, nil
}

func (r *Receiver) routes() {
	mux := http.NewServeMux()
	mux.HandleFunc(r.alertsPath, r.handleAlerts)
	mux.HandleFunc(r.metricsPath, r.handleMetricReports)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	r.mux = mux
}

func alertsPathFor(subscriberID string) string {
	if subscriberID == "" {
		return pathPrefix + "alerts/"
	}
	return pathPrefix + subscriberID + "/alerts/"
}

func metricsPathFor(subscriberID string) string {
	if subscriberID == "" {
		return pathPrefix + "metricsreport/"
	}
	return pathPrefix + subscriberID + "/metricsreport/"
}

// Start listens on cfg.Addr and serves until ctx is cancelled.
func (r *Receiver) Start(ctx context.Context) error {
	r.log.Info("Started events receiver", "addr", r.cfg.Addr)

	errCh := make(chan error, 1)
	go func() {
		if err := r.srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("events receiver ListenAndServe: %w", err)
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := r.srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("events receiver Shutdown: %w", err)
		}
		r.log.Info("Events receiver stopped")
		<-errCh
		return nil
	case err := <-errCh:
		return err
	}
}

// Handler exposes the routed mux for testing without binding a socket.
func (r *Receiver) Handler() http.Handler {
	return r.mux
}

func (r *Receiver) handleAlerts(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "Only POST is allowed", http.StatusMethodNotAllowed)
		return
	}
	bmcName := bmcNameFromPath(req.URL.Path, r.alertsPath)
	if bmcName == "" {
		http.Error(w, "Missing BMC name in URL", http.StatusBadRequest)
		return
	}
	if r.cfg.EventSink == nil {
		http.Error(w, "Event sink not configured", http.StatusServiceUnavailable)
		return
	}

	body, err := readBody(req, w)
	if err != nil {
		return
	}

	// Redfish vendors disagree on whether SEL events arrive under
	// "Events" or "Alerts" — accept either and merge.
	var data eventEnvelope
	if err := json.Unmarshal(body, &data); err != nil {
		r.log.V(1).Info("Failed to decode event payload",
			"bmc", bmcName, "err", err.Error())
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	events := data.flatten()
	if err := r.cfg.EventSink.PublishEvents(req.Context(), bmcName, events); err != nil {
		r.log.Error(err, "Event sink publish failed", "bmc", bmcName)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	r.log.V(2).Info("Published events", "bmc", bmcName, "count", len(events))
	w.WriteHeader(http.StatusNoContent)
}

// eventEnvelope accepts both vendor variants of the SEL push payload:
// some BMCs put events under "Events", others under "Alerts".
type eventEnvelope struct {
	Events []wireEvent `json:"Events,omitempty"`
	Alerts []wireEvent `json:"Alerts,omitempty"`
}

func (e eventEnvelope) flatten() []sink.Event {
	src := e.Events
	if len(src) == 0 {
		src = e.Alerts
	}
	if len(src) == 0 {
		return nil
	}
	out := make([]sink.Event, 0, len(src))
	for i := range src {
		out = append(out, src[i].toEvent())
	}
	return out
}

// bmcNameFromPath extracts the BMC name segment from a URL like
// /serverevents/alerts/bmc-1.
func bmcNameFromPath(urlPath, prefix string) string {
	if !strings.HasPrefix(urlPath, prefix) {
		return ""
	}
	tail := strings.TrimPrefix(urlPath, prefix)
	if tail == "" || tail == "." || strings.ContainsRune(tail, '/') {
		return ""
	}
	return tail
}

func (r *Receiver) handleMetricReports(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "Only POST is allowed", http.StatusMethodNotAllowed)
		return
	}
	bmcName := bmcNameFromPath(req.URL.Path, r.metricsPath)
	if bmcName == "" {
		http.Error(w, "Missing BMC name in URL", http.StatusBadRequest)
		return
	}
	if r.cfg.MetricReportSink == nil {
		http.Error(w, "MetricReport sink not configured", http.StatusServiceUnavailable)
		return
	}

	body, err := readBody(req, w)
	if err != nil {
		return
	}

	var data wireMetricReport
	if err := json.Unmarshal(body, &data); err != nil {
		r.log.V(1).Info("Failed to decode metric-report payload",
			"bmc", bmcName, "err", err.Error())
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	samples := data.toSamples(r.log)
	if err := r.cfg.MetricReportSink.PublishSamples(req.Context(), bmcName, samples); err != nil {
		r.log.Error(err, "Metric-report sink publish failed", "bmc", bmcName)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	r.log.V(2).Info("Published samples", "bmc", bmcName, "count", len(samples))
	w.WriteHeader(http.StatusNoContent)
}

func readBody(req *http.Request, w http.ResponseWriter) ([]byte, error) {
	req.Body = http.MaxBytesReader(w, req.Body, maxBodyBytes)
	body, err := io.ReadAll(req.Body)
	if err != nil {
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
			return nil, err
		}
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return nil, err
	}
	return body, nil
}
