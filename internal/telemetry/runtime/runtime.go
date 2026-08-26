// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// Package runtime wires the event-push telemetry pipeline onto an
// existing controller-runtime manager.
//
// AddTo registers: a live ConfigMap loader, the Redfish event receiver,
// the subscription reconciler, and (optionally) the readiness bridge
// for Critical events.
//
// It does NOT run a metric poller, scheduler, or sensor walker. Those
// live in an external redfish-exporter that scrapes BMCs via the
// discovery endpoint. The operator publishes inventory and reacts to
// Critical events; the exporter publishes sensor metrics.
package runtime

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/stmcginnis/gofish"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/criticalevent"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/events"
	promsink "github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink/prometheus"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/subscriptions"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	metalbmc "github.com/ironcore-dev/metal-operator/bmc"
)

// Options configures the event-push pipeline. Required fields are noted
// inline; everything else has a default.
type Options struct {
	ConfigName      string // ConfigMap holding the telemetry config (required).
	ConfigNamespace string // Namespace of the ConfigMap (required).

	ReceiverURL string // Externally-reachable base URL BMCs POST to (required).
	EventsAddr  string // Listen address for the event receiver, e.g. ":9092" (required).

	InsecureTLS bool

	// Defaults to "metal-maintenance-operator".
	SubscriberID string

	// EnableCriticalEventHandler turns on the Critical-event → Server
	// condition writer
	EnableCriticalEventHandler bool
}

// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=bmcs,verbs=get;list;patch;watch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=bmcsecrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers,verbs=get;list;watch
// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=servers/status,verbs=get;patch
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch

// AddTo registers the event-push pipeline on the given manager.
func AddTo(mgr manager.Manager, opts Options) error {
	if err := validateOptions(&opts); err != nil {
		return err
	}
	if err := validateReceiverURL(opts.ReceiverURL); err != nil {
		return err
	}
	if mgr == nil {
		return fmt.Errorf("manager is required")
	}

	// Register on controller-runtime's global metrics.Registry so the
	// manager's /metrics endpoint serves our series alongside the
	// standard controller_runtime_* and workqueue_* metrics.
	eventSink, err := promsink.NewEventSink(ctrlmetrics.Registry)
	if err != nil {
		return fmt.Errorf("init event sink: %w", err)
	}
	metricSink, err := promsink.NewMetricReportSink(ctrlmetrics.Registry)
	if err != nil {
		return fmt.Errorf("init metric-report sink: %w", err)
	}
	testSink, err := promsink.NewTestEventSink(ctrlmetrics.Registry)
	if err != nil {
		return fmt.Errorf("init test-event sink: %w", err)
	}

	// Register the Server-by-BMCRef index unconditionally: the subscription
	// reconciler uses it to look up Server.status.manufacturer/model when
	// BMC.status.manufacturer is empty (e.g. Dell BMCs).
	if err := mgr.GetFieldIndexer().IndexField(
		context.Background(),
		&metalv1alpha1.Server{},
		criticalevent.BMCRefField,
		func(obj client.Object) []string {
			s := obj.(*metalv1alpha1.Server)
			if s.Spec.BMCRef == nil {
				return nil
			}
			return []string{s.Spec.BMCRef.Name}
		},
	); err != nil {
		return fmt.Errorf("index Server by %s: %w", criticalevent.BMCRefField, err)
	}

	if opts.EnableCriticalEventHandler {
		handler := &criticalevent.ConditionHandler{
			Client: mgr.GetClient(),
			Log:    ctrl.Log.WithName("telemetry").WithName("readiness"),
		}
		// Attach the readiness bridge directly to the Prometheus sink so
		// every Critical event writes the CriticalEventReceived
		// condition on the matching Server.
		eventSink.OnCritical = handler.HandleCritical
	}

	loader := &ConfigLoader{
		Cache:     mgr.GetCache(),
		Client:    mgr.GetClient(),
		Namespace: opts.ConfigNamespace,
		Name:      opts.ConfigName,
		Log:       ctrl.Log.WithName("telemetry").WithName("config"),
	}
	if err := mgr.Add(loader); err != nil {
		return fmt.Errorf("add config loader: %w", err)
	}

	receiver, err := events.New(events.Config{
		Addr:             opts.EventsAddr,
		EventSink:        eventSink,
		MetricReportSink: metricSink,
		SubscriberID:     opts.SubscriberID,
		Log:              ctrl.Log.WithName("telemetry").WithName("events"),
	})
	if err != nil {
		return fmt.Errorf("init event receiver: %w", err)
	}
	if err := mgr.Add(receiver); err != nil {
		return fmt.Errorf("add event receiver: %w", err)
	}

	// BMCReconciler watches metalv1alpha1.BMC objects through the
	// controller-runtime cache and converges each BMC's Redfish event
	// subscriptions. See internal/telemetry/subscriptions/reconciler.go.
	resolver := &subscriptions.CacheResolver{Reader: mgr.GetClient()}
	subReconciler := &subscriptions.BMCReconciler{
		Client:           mgr.GetClient(),
		Config:           loader.Config,
		Resolver:         resolver,
		Factory:          &subscriptionClientFactory{insecureTLS: opts.InsecureTLS},
		Sink:             eventSink,
		MetricReportSink: metricSink,
		TestRecorder:     testSink,
		ReceiverURL:      opts.ReceiverURL,
		SubscriberID:     opts.SubscriberID,
		// ReconcileInterval and PerBMCTimeout intentionally unset:
		// the reconciler reads them from the live ConfigMap at use
		// site, falling back to its internal defaults when the
		// ConfigMap doesn't provide a value. See subscriptions/manager.go.
		Log: ctrl.Log.WithName("telemetry").WithName("subscriptions"),
	}
	if err := subReconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("setup subscription reconciler: %w", err)
	}

	// Wire the receiver's test notifier to the reconciler so round-trip
	// confirmations update the health-check metric.
	receiver.SetTestNotifier(subReconciler)

	return nil
}

func validateOptions(opts *Options) error {
	if opts.ConfigName == "" {
		return fmt.Errorf("ConfigName is required")
	}
	if opts.ConfigNamespace == "" {
		return fmt.Errorf("ConfigNamespace is required")
	}
	if opts.ReceiverURL == "" {
		return fmt.Errorf("ReceiverURL is required")
	}
	if opts.EventsAddr == "" {
		return fmt.Errorf("EventsAddr is required")
	}
	if opts.SubscriberID == "" {
		opts.SubscriberID = "metal-maintenance-operator"
	}
	return nil
}

// validateReceiverURL rejects scheme-less or relative URLs that url.Parse
// silently accepts ("collector", "/events")
func validateReceiverURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid ReceiverURL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("invalid ReceiverURL %q: must be absolute (scheme://host[:port][/path])", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid ReceiverURL %q: scheme must be http or https", raw)
	}
	return nil
}

type subscriptionClientFactory struct {
	insecureTLS bool
}

// extendedClient augments metalbmc.BMC with ListEventSubscriptions,
// which upstream bmc.BMC does not expose. Reads the EventService
// subscriptions collection via the *gofish.APIClient accessor added by
// metal-operator PR #966.
type extendedClient struct {
	metalbmc.BMC
	api *gofish.APIClient
}

func (c *extendedClient) ListEventSubscriptions(_ context.Context) ([]subscriptions.Subscription, error) {
	svc := c.api.GetService()
	if svc == nil {
		return nil, fmt.Errorf("list subscriptions: no service root")
	}
	es, err := svc.EventService()
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: event service: %w", err)
	}
	dests, err := es.Subscriptions()
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	out := make([]subscriptions.Subscription, 0, len(dests))
	for _, d := range dests {
		out = append(out, subscriptions.Subscription{
			URI:         d.ODataID,
			Destination: d.Destination,
			EventFormat: string(d.EventFormatType),
		})
	}
	return out, nil
}

func (c *extendedClient) SubmitTestEvent(_ context.Context, params subscriptions.TestEventParams) error {
	svc := c.api.GetService()
	if svc == nil {
		return fmt.Errorf("submit test event: no service root")
	}
	es, err := svc.EventService()
	if err != nil {
		return fmt.Errorf("submit test event: event service: %w", err)
	}
	if es.SubmitTestEventTarget == "" {
		return fmt.Errorf("submit test event: no target URL")
	}

	// Build the payload manually. gofish's EventServiceSubmitTestEventParameters
	// uses omitempty on MessageArgs (iLO rejects absent field). OriginOfCondition
	// is sent as a plain string: HPE and Lenovo require it in that form (iLO/XCC
	// reject the {"@odata.id": ...} link-object form with a type error). Dell
	// rows omit OriginOfCondition entirely via config, so the format never
	// matters for Dell.
	payload := map[string]any{
		"EventId":        "1",
		"EventTimestamp": time.Now().UTC().Format(time.RFC3339),
		"MessageId":      params.MessageId,
		"Message":        "metal-maintenance-operator pipeline health check",
		"MessageArgs":    []string{},
	}
	if params.Severity != "" {
		payload["Severity"] = params.Severity
	}
	if params.OriginOfCondition != "" {
		payload["OriginOfCondition"] = params.OriginOfCondition
	}

	resp, err := c.api.Post(es.SubmitTestEventTarget, payload)
	if resp != nil {
		_ = resp.Body.Close()
	}
	return err
}

func (f *subscriptionClientFactory) NewClient(ctx context.Context, r *subscriptions.Resolved) (subscriptions.Client, error) {
	opts := metalbmc.Options{
		Endpoint:    subscriptions.BMCEndpoint(r.BMC),
		Username:    r.Username,
		Password:    r.Password,
		BasicAuth:   true,
		InsecureTLS: f.insecureTLS,
	}
	base, err := metalbmc.NewRedfishBMCClient(ctx, opts)
	if err != nil {
		return nil, err
	}
	return wrapClient(base)
}

// NewTestEventClient wraps an already-connected metalbmc.BMC (e.g. from
// metalbmc.NewRedfishBMCClient) into a subscriptions.Client. It exists for
// callers outside the controller-runtime reconcile loop — such as CLI
// tooling under hack/ — that want to invoke SubmitTestEvent through the
// exact same code path the operator's health check uses, without needing
// a *subscriptions.Resolved / metalv1alpha1.BMC object.
func NewTestEventClient(base metalbmc.BMC) (subscriptions.Client, error) {
	return wrapClient(base)
}

func wrapClient(base metalbmc.BMC) (subscriptions.Client, error) {
	accessor, ok := base.(interface{ Client() *gofish.APIClient })
	if !ok {
		return nil, fmt.Errorf("bmc client %T does not expose Client() *gofish.APIClient; "+
			"an OEM VendorFactory must embed *bmc.RedfishBaseBMC for subscription listing to work", base)
	}
	return &extendedClient{BMC: base, api: accessor.Client()}, nil
}
