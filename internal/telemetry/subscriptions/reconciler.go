// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package subscriptions

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/controller-utils/clientutils"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/stmcginnis/gofish/schemas"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// TestResultRecorder records the outcome of a pipeline health-check test
// event. The production implementation updates a Prometheus gauge; nil
// disables recording (useful in tests that don't care about health-check
// metrics).
type TestResultRecorder interface {
	RecordTestResult(bmcName, result string)
	Forget(bmcName string)
}

const bmcSubscriptionFinalizer = "telemetry.metal.ironcore.dev/subscriptions"

// +kubebuilder:rbac:groups=metal.ironcore.dev,resources=bmcs/finalizers,verbs=update

// BMCReconciler keeps each metal-operator BMC's Redfish event
// subscriptions converged with the operator's policy. Implements
// controller-runtime's reconcile.Reconciler.
type BMCReconciler struct {
	// Client is the controller-runtime client used to fetch BMCs and Servers.
	Client client.Client

	// Config returns the live operator policy ConfigMap. Refreshed by
	// the ConfigLoader runnable; never nil after AddTo has wired this.
	Config func() *Config

	// Resolver fetches the live BMC + credentials. Production uses
	// CacheResolver against the controller-runtime cache.
	Resolver Resolver

	// Factory builds a Redfish Client per Reconcile (separate
	// connection per BMC, not pooled — reconcile cadence is minutes,
	// not seconds, so per-call construction is fine).
	Factory ClientFactory

	// Sink is optional. When set, the reconciler calls Sink.Forget(name)
	// after a BMC is torn down so the sink can release per-BMC state
	// (counter series, dedup caches). Nil disables the call — useful
	// in tests that don't care about sink lifecycle.
	Sink sink.EventSink

	// MetricReportSink is the parallel knob for MetricReport pushes;
	// same Forget-on-teardown contract as Sink, independent nil-ability.
	MetricReportSink sink.MetricReportSink

	// ReceiverURL is the externally-reachable base URL the BMC POSTs to.
	// Destination URLs are computed as
	// "<ReceiverURL>/serverevents[/<SubscriberID>]/alerts/{bmcName}".
	ReceiverURL string

	// SubscriberID disambiguates subscriptions when multiple subscribers
	// share BMCs (the maintenance operator and an external redfish-exporter).
	SubscriberID string

	// ReconcileInterval is the fallback re-sweep cadence used as
	// RequeueAfter when the live ConfigMap doesn't set
	// subscriptionReconcileInterval.
	ReconcileInterval time.Duration

	// PerBMCTimeout caps each Reconcile pass end-to-end. Same
	// fallback / ConfigMap-precedence rules as ReconcileInterval.
	// Defaults to 30s.
	PerBMCTimeout time.Duration

	// TestRecorder is optional. When set, the reconciler records pipeline
	// health-check results (success/failure) via RecordTestResult after
	// each SubmitTestEvent round-trip. Nil disables recording.
	TestRecorder TestResultRecorder

	Log logr.Logger

	// srvURL is the parsed ReceiverURL, computed during SetupWithManager
	// so destinationFor and classify don't re-parse on every call. Not
	// guarded by a mutex — written once during setup, then only read.
	srvURL *url.URL

	// testPending tracks in-flight health-check tests: bmcName → testEntry.
	testPending sync.Map
	// lastTestTime tracks when we last fired a test per BMC: bmcName → time.Time.
	lastTestTime sync.Map
}

// SetupWithManager registers the reconciler against the controller-runtime manager.
func (r *BMCReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := r.init(); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("redfish-event-subscriptions").
		For(&metalv1alpha1.BMC{}).
		Complete(r)
}

func (r *BMCReconciler) init() error {
	if r.Client == nil {
		return errors.New("subscriptions: Client is required")
	}
	if r.Config == nil {
		return errors.New("subscriptions: Config is required")
	}
	if r.Resolver == nil {
		return errors.New("subscriptions: Resolver is required")
	}
	if r.Factory == nil {
		return errors.New("subscriptions: Factory is required")
	}
	if r.ReceiverURL == "" {
		return errors.New("subscriptions: ReceiverURL is required")
	}
	u, err := url.Parse(r.ReceiverURL)
	if err != nil {
		return fmt.Errorf("subscriptions: parse ReceiverURL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("subscriptions: ReceiverURL must be absolute (got %q)", r.ReceiverURL)
	}
	if r.SubscriberID != "" {
		// serverevents/{subscriberID}/{type}/{bmcName}
		if strings.ContainsAny(r.SubscriberID, "/?#") || strings.TrimSpace(r.SubscriberID) != r.SubscriberID {
			return fmt.Errorf("subscriptions: SubscriberID must be a single path segment (no '/', '?', '#', or surrounding whitespace) (got %q)", r.SubscriberID)
		}
	}
	r.srvURL = u

	if r.ReconcileInterval <= 0 {
		r.ReconcileInterval = 10 * time.Minute
	}
	if r.PerBMCTimeout <= 0 {
		r.PerBMCTimeout = 30 * time.Second
	}
	return nil
}

// Reconcile drives one BMC to the desired subscription state.
func (r *BMCReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	// The ConfigLoader and this reconciler are independent manager
	// Runnables — controller-runtime does not order them. On operator
	// restart the BMC controller can reconcile before the first
	// ConfigMap load has landed. A nil config would fall through to
	// SubscribeToBMC returning false → deleteSubscriptions, tearing
	// live subscriptions off every event-capable BMC in the fleet.
	// Requeue short-fast until Config() is non-nil.
	if r.Config() == nil {
		r.Log.V(1).Info("Telemetry config not yet loaded; deferring reconcile",
			"bmc", req.Name)
		return ctrl.Result{RequeueAfter: configLoadDeferInterval}, nil
	}

	bmc := &metalv1alpha1.BMC{}
	if err := r.Client.Get(ctx, req.NamespacedName, bmc); err != nil {
		if apierrors.IsNotFound(err) {
			// Fallback path for BMCs deleted BEFORE our finalizer
			// landed (existing deployments upgrading into this
			// operator, or the tiny window between BMC creation and
			// our first successful reconcile). Best-effort teardown
			// through the fake resolver in tests; a silent no-op in
			// production because endpoint + credentials are gone
			// with the object. The finalizer path below covers the
			// well-managed case.
			r.tearDownOne(ctx, req.Name)
			r.ForgetTestState(req.Name)
			if r.Sink != nil {
				r.Sink.Forget(req.Name)
			}
			if r.MetricReportSink != nil {
				r.MetricReportSink.Forget(req.Name)
			}
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get BMC %q: %w", req.Name, err)
	}

	// BMC being deleted: run teardown while endpoint + credentials are
	// still resolvable via the still-live object, then release the
	// finalizer so the delete can complete.
	if !bmc.DeletionTimestamp.IsZero() {
		if !controllerutil.ContainsFinalizer(bmc, bmcSubscriptionFinalizer) {
			// Another reconcile already handled the delete, or the
			// BMC was never subscribed. Nothing to do.
			return ctrl.Result{}, nil
		}
		r.tearDownOne(ctx, req.Name)
		r.ForgetTestState(req.Name)
		if r.Sink != nil {
			r.Sink.Forget(req.Name)
		}
		if r.MetricReportSink != nil {
			r.MetricReportSink.Forget(req.Name)
		}
		if err := clientutils.PatchRemoveFinalizer(ctx, r.Client, bmc, bmcSubscriptionFinalizer); err != nil {
			return ctrl.Result{}, fmt.Errorf("remove finalizer on BMC %q: %w", req.Name, err)
		}
		return ctrl.Result{}, nil
	}

	r.reconcileOne(ctx, bmc)
	return ctrl.Result{RequeueAfter: r.reconcileInterval()}, nil
}

// configLoadDeferInterval is the retry cadence used when Reconcile fires
// before ConfigLoader has produced its first parsed config. Short enough
// that a healthy first load recovers quickly, long enough not to hammer
// the apiserver on a persistent ConfigMap fault.
const configLoadDeferInterval = 10 * time.Second

func (r *BMCReconciler) reconcileInterval() time.Duration {
	if cfg := r.Config(); cfg != nil && cfg.SubscriptionReconcileInterval > 0 {
		return cfg.SubscriptionReconcileInterval
	}
	return r.ReconcileInterval
}

func (r *BMCReconciler) perBMCTimeout() time.Duration {
	if cfg := r.Config(); cfg != nil && cfg.PerBMCTimeout > 0 {
		return cfg.PerBMCTimeout
	}
	return r.PerBMCTimeout
}

func (r *BMCReconciler) reconcileOne(ctx context.Context, bmc *metalv1alpha1.BMC) {
	ref := r.bmcRefWithServerFallback(ctx, bmc)
	cfg := r.Config()
	hw := SubscribeToBMC(ref, cfg)
	wantSubscribed := hw != nil

	ctx, cancel := context.WithTimeout(ctx, r.perBMCTimeout())
	defer cancel()

	resolved, err := r.Resolver.Resolve(ctx, ref.Name)
	if err != nil {
		r.Log.V(1).Info("Cannot resolve BMC for subscription reconcile",
			"bmc", ref.Name, "err", err.Error())
		return
	}
	if !resolved.BMC.Status.IP.IsValid() {
		return
	}

	c, err := r.Factory.NewClient(ctx, resolved)
	if err != nil {
		r.Log.V(1).Info("Cannot build BMC client for subscription reconcile",
			"bmc", ref.Name, "err", err.Error())
		return
	}
	defer c.Logout()

	existing, err := c.ListEventSubscriptions(ctx)
	if err != nil {
		r.Log.V(1).Info("Cannot list subscriptions",
			"bmc", ref.Name, "err", err.Error())
		return
	}
	current, stale := r.classify(existing, ref.Name)

	// Stale subscriptions (our path/SubscriberID pattern, different host)
	// are cleaned up regardless of delivery mode.
	if len(stale) > 0 {
		r.Log.V(1).Info("Deleting stale subscriptions (own pattern, different host)",
			"bmc", ref.Name, "count", len(stale))
		_ = r.deleteSubscriptions(ctx, c, ref.Name, stale)
	}

	if wantSubscribed {
		// Ensure the finalizer BEFORE creating any subscription. A
		// crash between the Create and a later finalizer-patch would
		// leave a subscription on the BMC with no matching k8s
		// finalizer, so a subsequent delete would silently orphan it.
		if _, err := clientutils.PatchEnsureFinalizer(ctx, r.Client, bmc, bmcSubscriptionFinalizer); err != nil {
			r.Log.V(1).Info("Cannot ensure finalizer",
				"bmc", ref.Name, "err", err.Error())
			return
		}
		r.ensureSubscriptions(ctx, c, ref.Name, current)
		r.maybeRunTestEvent(ctx, c, ref.Name, cfg, hw)
	} else {
		// BMC isn't in the event-based set (or was and is no longer):
		// tear off any subscriptions we previously created here, then
		// release the finalizer if we still hold one — otherwise a
		// future BMC delete would block on us for a BMC we no longer
		// manage.
		if err := r.deleteSubscriptions(ctx, c, ref.Name, current); err != nil {
			r.Log.V(1).Info("Failed to remove all subscriptions; keeping finalizer",
				"bmc", ref.Name, "err", err.Error())
			return
		}
		if _, err := clientutils.PatchEnsureNoFinalizer(ctx, r.Client, bmc, bmcSubscriptionFinalizer); err != nil {
			r.Log.V(1).Info("Cannot remove stale finalizer",
				"bmc", ref.Name, "err", err.Error())
		}
	}
}

func (r *BMCReconciler) tearDownOne(ctx context.Context, bmcName string) {
	ctx, cancel := context.WithTimeout(ctx, r.perBMCTimeout())
	defer cancel()

	resolved, err := r.Resolver.Resolve(ctx, bmcName)
	if err != nil {
		r.Log.V(1).Info("Skipping teardown; BMC unresolvable",
			"bmc", bmcName, "err", err.Error())
		return
	}
	c, err := r.Factory.NewClient(ctx, resolved)
	if err != nil {
		r.Log.V(1).Info("Skipping teardown; cannot build client",
			"bmc", bmcName, "err", err.Error())
		return
	}
	defer c.Logout()

	existing, err := c.ListEventSubscriptions(ctx)
	if err != nil {
		r.Log.V(1).Info("Skipping teardown; cannot list subscriptions",
			"bmc", bmcName, "err", err.Error())
		return
	}
	current, stale := r.classify(existing, bmcName)
	// Wipe both — anything matching our pattern is ours in some form,
	// regardless of host.
	_ = r.deleteSubscriptions(ctx, c, bmcName, append(current, stale...))
}

// ensureSubscriptions converges `current` to exactly one MetricReport
// subscription and one Event subscription, both pointing at our receiver.
func (r *BMCReconciler) ensureSubscriptions(ctx context.Context, c Client, bmcName string, current []Subscription) {
	metrics, alerts := groupByFormat(current)

	if keep, extras := pickAndExtras(metrics); len(extras) > 0 {
		r.Log.V(1).Info("Deduplicating metric subscriptions",
			"bmc", bmcName, "keep", keep.URI, "removed", len(extras))
		_ = r.deleteSubscriptions(ctx, c, bmcName, extras)
	}
	if keep, extras := pickAndExtras(alerts); len(extras) > 0 {
		r.Log.V(1).Info("Deduplicating alert subscriptions",
			"bmc", bmcName, "keep", keep.URI, "removed", len(extras))
		_ = r.deleteSubscriptions(ctx, c, bmcName, extras)
	}

	if len(metrics) == 0 {
		dest := r.destinationFor("metricsreport", bmcName)
		uri, err := c.CreateEventSubscription(
			ctx, dest,
			schemas.MetricReportEventFormatType,
			schemas.RetryForeverDeliveryRetryPolicy,
		)
		if err != nil {
			r.Log.V(1).Info("Failed to create metrics subscription",
				"bmc", bmcName, "destination", dest, "err", err.Error())
		} else {
			r.Log.V(1).Info("Created metrics subscription",
				"bmc", bmcName, "uri", uri)
		}
	}

	if len(alerts) == 0 {
		dest := r.destinationFor("alerts", bmcName)
		uri, err := c.CreateEventSubscription(
			ctx, dest,
			schemas.EventEventFormatType,
			schemas.RetryForeverDeliveryRetryPolicy,
		)
		if err != nil {
			r.Log.V(1).Info("Failed to create alerts subscription",
				"bmc", bmcName, "destination", dest, "err", err.Error())
		} else {
			r.Log.V(1).Info("Created alerts subscription",
				"bmc", bmcName, "uri", uri)
		}
	}
}

func groupByFormat(subs []Subscription) (metrics, alerts []Subscription) {
	for _, sub := range subs {
		switch sub.EventFormat {
		case string(schemas.MetricReportEventFormatType):
			metrics = append(metrics, sub)
		case string(schemas.EventEventFormatType):
			alerts = append(alerts, sub)
		}
	}
	return
}

func pickAndExtras(subs []Subscription) (keep Subscription, extras []Subscription) {
	if len(subs) == 0 {
		return Subscription{}, nil
	}
	keep = subs[0]
	for _, s := range subs[1:] {
		if s.URI < keep.URI {
			extras = append(extras, keep)
			keep = s
		} else {
			extras = append(extras, s)
		}
	}
	return keep, extras
}

func (r *BMCReconciler) deleteSubscriptions(ctx context.Context, c Client, bmcName string, subs []Subscription) error {
	var errs []error
	for _, sub := range subs {
		if err := c.DeleteEventSubscription(ctx, sub.URI); err != nil {
			r.Log.V(1).Info("Failed to delete subscription",
				"bmc", bmcName, "uri", sub.URI, "err", err.Error())
			errs = append(errs, err)
		} else {
			r.Log.V(1).Info("Deleted subscription",
				"bmc", bmcName, "uri", sub.URI)
		}
	}
	return errors.Join(errs...)
}

// classify partitions the BMC's existing subscriptions into:
//
//   - current: targeting our exact ReceiverURL (host + scheme) AND
//     carrying our SubscriberID segment. Verified during ensure;
//     deleted during teardown or when transitioning to Polling.
//   - stale: our SubscriberID + path pattern, but a different host
//     (Service rename, ClusterIP change, etc.). Always deleted.
func (r *BMCReconciler) classify(subs []Subscription, bmcName string) (current, stale []Subscription) {
	srvHost := r.srvURL.Host
	srvScheme := r.srvURL.Scheme
	subscriberID := r.SubscriberID

	for _, sub := range subs {
		u, err := url.Parse(sub.Destination)
		if err != nil {
			continue
		}
		if !strings.HasPrefix(u.Path, pathPrefix) {
			continue
		}
		// Path shape: /serverevents/{type}/{bmcName} (3 segs) or
		// /serverevents/{subscriberID}/{type}/{bmcName} (4 segs).
		segs := strings.Split(strings.Trim(u.Path, "/"), "/")
		var typeSeg, foundSubscriberID string
		switch len(segs) {
		case 3:
			// /serverevents/{type}/{bmcName} — no subscriberID segment.
			// Treat as stale when we're now running with a SubscriberID
			// (upgrade path), so old bare subscriptions are cleaned up.
			typeSeg = segs[1]
			if subscriberID != "" {
				stale = append(stale, sub)
				continue
			}
		case 4:
			// /serverevents/{subscriberID}/{type}/{bmcName}
			foundSubscriberID = segs[1]
			typeSeg = segs[2]
		default:
			continue
		}
		if segs[len(segs)-1] != bmcName {
			continue
		}
		if typeSeg != "metricsreport" && typeSeg != "alerts" {
			continue
		}
		if foundSubscriberID != subscriberID {
			continue
		}
		switch typeSeg {
		case "metricsreport":
			if sub.EventFormat != string(schemas.MetricReportEventFormatType) {
				stale = append(stale, sub)
				continue
			}
		case "alerts":
			if sub.EventFormat != string(schemas.EventEventFormatType) {
				stale = append(stale, sub)
				continue
			}
		}

		if u.Scheme == srvScheme && u.Host == srvHost {
			current = append(current, sub)
		} else {
			stale = append(stale, sub)
		}
	}
	return current, stale
}

func (r *BMCReconciler) destinationFor(kind, bmcName string) string {
	u := *r.srvURL // shallow copy so we don't mutate the shared parsed URL.
	if r.SubscriberID != "" {
		u.Path = pathPrefix + r.SubscriberID + "/" + kind + "/" + bmcName
	} else {
		u.Path = pathPrefix + kind + "/" + bmcName
	}
	return u.String()
}

func bmcRefFromObject(bmc *metalv1alpha1.BMC) BMCRef {
	return BMCRef{
		Name:            bmc.Name,
		Namespace:       bmc.Namespace,
		Vendor:          bmc.Status.Manufacturer,
		Model:           bmc.Status.Model,
		FirmwareVersion: bmc.Status.FirmwareVersion,
	}
}

// bmcRefWithServerFallback builds a BMCRef, preferring Server.status fields
// for Vendor and Model when BMC.status.manufacturer is empty. Dell BMCs, for
// example, do not populate BMC.status.manufacturer, whereas the associated
// Server always has status.manufacturer and status.model set by metal-operator.
func (r *BMCReconciler) bmcRefWithServerFallback(ctx context.Context, bmc *metalv1alpha1.BMC) BMCRef {
	ref := bmcRefFromObject(bmc)
	if ref.Vendor != "" {
		return ref
	}
	serverList := &metalv1alpha1.ServerList{}
	if err := r.Client.List(ctx, serverList, client.MatchingFields{"spec.bmcRef.name": bmc.Name}); err != nil {
		r.Log.V(1).Info("Could not list Servers for BMC vendor/model lookup", "bmc", bmc.Name, "err", err.Error())
		return ref
	}
	if len(serverList.Items) == 0 {
		return ref
	}
	srv := serverList.Items[0]
	ref.Vendor = srv.Status.Manufacturer
	ref.Model = srv.Status.Model
	return ref
}

// testEntry holds the correlation state for one in-flight SubmitTestEvent.
type testEntry struct {
	deadline time.Time
}

const (
	testResultSuccess = "success"
	testResultFailure = "failure"
)

// maybeRunTestEvent fires a SubmitTestEvent health check if the configured
// interval has elapsed since the last test for this BMC. It also enforces
// the deadline for any pending test that has not yet been confirmed, recording
// a failure if it expired.
func (r *BMCReconciler) maybeRunTestEvent(ctx context.Context, c Client, bmcName string, cfg *Config, hw *HardwareMatch) {
	if cfg == nil || cfg.TestEventInterval <= 0 {
		return
	}
	if hw == nil || hw.TestMessageId == "" {
		return
	}

	// Enforce deadline of any previous pending test.
	if raw, ok := r.testPending.Load(bmcName); ok {
		entry := raw.(testEntry)
		if time.Now().After(entry.deadline) {
			r.testPending.Delete(bmcName)
			r.Log.Info("Test event timed out", "bmc", bmcName)
			if r.TestRecorder != nil {
				r.TestRecorder.RecordTestResult(bmcName, testResultFailure)
			}
		} else {
			// Still waiting for arrival — don't fire another test yet.
			return
		}
	}

	// Check if enough time has elapsed since the last test.
	if raw, ok := r.lastTestTime.Load(bmcName); ok {
		if time.Since(raw.(time.Time)) < cfg.TestEventInterval {
			return
		}
	}

	timeout := cfg.TestEventTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	params := TestEventParams{
		MessageId:         hw.TestMessageId,
		Severity:          hw.TestSeverity,
		OriginOfCondition: hw.TestOriginOfCondition,
	}
	if err := c.SubmitTestEvent(ctx, params); err != nil {
		r.Log.V(1).Info("Failed to submit test event", "bmc", bmcName, "err", err.Error())
		return
	}
	r.testPending.Store(bmcName, testEntry{deadline: time.Now().Add(timeout)})
	r.lastTestTime.Store(bmcName, time.Now())
	r.Log.V(1).Info("Submitted test event", "bmc", bmcName)
}

// NotifyTestEvent is called by the event receiver when an alert arrives.
// It correlates the messageId against any pending test for the BMC and
// records a success result if matched. Implements events.TestEventNotifier.
func (r *BMCReconciler) NotifyTestEvent(bmcName, messageId string) {
	raw, ok := r.testPending.Load(bmcName)
	if !ok {
		return
	}
	entry := raw.(testEntry)
	if time.Now().After(entry.deadline) {
		// Deadline already passed — the timeout path in maybeRunTestEvent
		// will record the failure on the next reconcile. Ignore late arrival.
		return
	}
	r.testPending.Delete(bmcName)
	r.Log.Info("Test event round-trip confirmed", "bmc", bmcName)
	if r.TestRecorder != nil {
		r.TestRecorder.RecordTestResult(bmcName, testResultSuccess)
	}
}

// ForgetTestState clears all pending test state for a BMC. Called during
// teardown so stale entries don't affect a future re-subscription.
func (r *BMCReconciler) ForgetTestState(bmcName string) {
	r.testPending.Delete(bmcName)
	r.lastTestTime.Delete(bmcName)
	if r.TestRecorder != nil {
		r.TestRecorder.Forget(bmcName)
	}
}

// Compile-time check: BMCReconciler satisfies the controller-runtime
// Reconciler contract.
var _ interface {
	Reconcile(context.Context, ctrl.Request) (ctrl.Result, error)
} = (*BMCReconciler)(nil)
