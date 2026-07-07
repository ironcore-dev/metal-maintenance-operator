// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package subscriptions_test

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/subscriptions"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/stmcginnis/gofish/schemas"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

// req is a shorthand for ctrl.Request{NamespacedName: {Name: name}} —
// the BMC objects we test against are cluster-scoped, so namespace is
// irrelevant.
func req() ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Name: testBMCName}}
}

// -- Setup / validation --

func TestBMCReconciler_InitForTest_RequiresFields(t *testing.T) {
	bare := &subscriptions.BMCReconciler{} // missing everything
	if err := subscriptions.InitForTest(bare); err == nil {
		t.Fatal("InitForTest with empty reconciler should fail")
	}

	cases := []struct {
		name    string
		mutator func(*subscriptions.BMCReconciler)
	}{
		{"Client", func(r *subscriptions.BMCReconciler) { r.Client = nil }},
		{"Config", func(r *subscriptions.BMCReconciler) { r.Config = nil }},
		{"Resolver", func(r *subscriptions.BMCReconciler) { r.Resolver = nil }},
		{"Factory", func(r *subscriptions.BMCReconciler) { r.Factory = nil }},
		{"ReceiverURL", func(r *subscriptions.BMCReconciler) { r.ReceiverURL = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newClientWith(t)
			r := &subscriptions.BMCReconciler{
				Client:      c,
				Config:      func() *subscriptions.Config { return &subscriptions.Config{} },
				Resolver:    &fakeResolver{},
				Factory:     &fakeFactory{client: &fakeClient{}},
				ReceiverURL: "http://r:1",
			}
			tc.mutator(r)
			if err := subscriptions.InitForTest(r); err == nil {
				t.Errorf("missing %s: InitForTest should fail", tc.name)
			}
		})
	}
}

func TestBMCReconciler_InitForTest_RejectsRelativeReceiverURL(t *testing.T) {
	c := newClientWith(t)
	r := &subscriptions.BMCReconciler{
		Client:      c,
		Config:      func() *subscriptions.Config { return &subscriptions.Config{} },
		Resolver:    &fakeResolver{},
		Factory:     &fakeFactory{client: &fakeClient{}},
		ReceiverURL: "/no-scheme",
	}
	if err := subscriptions.InitForTest(r); err == nil {
		t.Fatal("InitForTest with relative ReceiverURL should fail")
	}
}

func TestBMCReconciler_InitForTest_RejectsBadSubscriberID(t *testing.T) {
	for _, bad := range []string{"has/slash", "has?query", "has#frag", " has-space "} {
		t.Run(bad, func(t *testing.T) {
			c := newClientWith(t)
			r := &subscriptions.BMCReconciler{
				Client:       c,
				Config:       func() *subscriptions.Config { return &subscriptions.Config{} },
				Resolver:     &fakeResolver{},
				Factory:      &fakeFactory{client: &fakeClient{}},
				ReceiverURL:  "http://r:1",
				SubscriberID: bad,
			}
			if err := subscriptions.InitForTest(r); err == nil {
				t.Errorf("SubscriberID %q should be rejected", bad)
			}
		})
	}
}

// -- Reconcile: EventBased mode, no existing subscriptions --

func TestReconcile_EventBased_NoExisting_CreatesBoth(t *testing.T) {
	c := newClientWith(t, bmcObject(testBMCName, vendorDellInc, modelR650))
	res := &fakeResolver{}
	res.set(makeResolved(), nil)
	fc := &fakeClient{}
	r := newRec(t, c, dellVendorMatch(), res, &fakeFactory{client: fc})

	if _, err := r.Reconcile(context.Background(), req()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	creates := fc.snapshotCreates()
	if len(creates) != 2 {
		t.Fatalf("create calls: got %d, want 2", len(creates))
	}
	formats := []string{string(creates[0].format), string(creates[1].format)}
	sort.Strings(formats)
	want := []string{string(schemas.EventEventFormatType), string(schemas.MetricReportEventFormatType)}
	for i := range want {
		if formats[i] != want[i] {
			t.Errorf("create format[%d]: got %q, want %q", i, formats[i], want[i])
		}
	}
	for _, ev := range creates {
		switch ev.format {
		case schemas.MetricReportEventFormatType:
			if ev.dest != metricReportDest {
				t.Errorf("metrics destination: got %q", ev.dest)
			}
		case schemas.EventEventFormatType:
			if ev.dest != alertsDest {
				t.Errorf("alerts destination: got %q", ev.dest)
			}
		}
	}
}

// -- Reconcile: EventBased mode, both subscriptions already present --

func TestReconcile_EventBased_BothPresent_NoChange(t *testing.T) {
	c := newClientWith(t, bmcObject(testBMCName, vendorDellInc, ""))
	res := &fakeResolver{}
	res.set(makeResolved(), nil)
	fc := &fakeClient{}
	fc.setSubscriptions([]subscriptions.Subscription{
		{URI: "/redfish/v1/EventService/Subscriptions/1", Destination: metricReportDest, EventFormat: eventFormatMetricReport},
		{URI: "/redfish/v1/EventService/Subscriptions/2", Destination: alertsDest, EventFormat: eventFormatEvent},
	})
	r := newRec(t, c, dellVendorMatch(), res, &fakeFactory{client: fc})

	if _, err := r.Reconcile(context.Background(), req()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if c := fc.snapshotCreates(); len(c) != 0 {
		t.Errorf("unexpected creates: %+v", c)
	}
	if d := fc.snapshotDeletes(); len(d) != 0 {
		t.Errorf("unexpected deletes: %v", d)
	}
}

// -- Reconcile: EventBased mode, only one format present --

func TestReconcile_EventBased_OneMissing_CreatesOne(t *testing.T) {
	c := newClientWith(t, bmcObject(testBMCName, vendorDellInc, ""))
	res := &fakeResolver{}
	res.set(makeResolved(), nil)
	fc := &fakeClient{}
	fc.setSubscriptions([]subscriptions.Subscription{
		{URI: "/redfish/v1/EventService/Subscriptions/1", Destination: metricReportDest, EventFormat: eventFormatMetricReport},
	})
	r := newRec(t, c, dellVendorMatch(), res, &fakeFactory{client: fc})

	if _, err := r.Reconcile(context.Background(), req()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	creates := fc.snapshotCreates()
	if len(creates) != 1 {
		t.Fatalf("creates: got %d, want 1", len(creates))
	}
	if creates[0].format != schemas.EventEventFormatType {
		t.Errorf("expected Event format, got %q", creates[0].format)
	}
}

// -- Reconcile: dedupe within a single format --

func TestReconcile_EventBased_DuplicateMetrics_DeletesExtras(t *testing.T) {
	c := newClientWith(t, bmcObject(testBMCName, vendorDellInc, ""))
	res := &fakeResolver{}
	res.set(makeResolved(), nil)
	fc := &fakeClient{}
	fc.setSubscriptions([]subscriptions.Subscription{
		// Two MetricReport subscriptions, both ours — must dedupe.
		{URI: "/redfish/v1/EventService/Subscriptions/9", Destination: metricReportDest, EventFormat: eventFormatMetricReport},
		{URI: "/redfish/v1/EventService/Subscriptions/3", Destination: metricReportDest, EventFormat: eventFormatMetricReport},
		// Single Event subscription, no dedupe needed.
		{URI: "/redfish/v1/EventService/Subscriptions/5", Destination: alertsDest, EventFormat: eventFormatEvent},
	})
	r := newRec(t, c, dellVendorMatch(), res, &fakeFactory{client: fc})

	if _, err := r.Reconcile(context.Background(), req()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// pickAndExtras keeps the lexicographically smallest URI, so /3 stays
	// and /9 goes.
	deletes := fc.snapshotDeletes()
	if len(deletes) != 1 || deletes[0] != "/redfish/v1/EventService/Subscriptions/9" {
		t.Errorf("expected to delete /9 only, got %v", deletes)
	}
	if c := fc.snapshotCreates(); len(c) != 0 {
		t.Errorf("dedup case must not create anything: %+v", c)
	}
}

// -- Reconcile: stale-host cleanup, regardless of delivery mode --

func TestReconcile_StaleHost_DeletedRegardlessOfMode(t *testing.T) {
	c := newClientWith(t, bmcObject(testBMCName, vendorDellInc, ""))
	res := &fakeResolver{}
	res.set(makeResolved(), nil)
	fc := &fakeClient{}
	fc.setSubscriptions([]subscriptions.Subscription{
		// Old receiver host. Same path pattern, same BMC name. Must be deleted.
		{URI: "/redfish/v1/EventService/Subscriptions/old-m", Destination: "http://OLD-recv:9092/serverevents/metricsreport/bmc-1", EventFormat: eventFormatMetricReport},
		{URI: "/redfish/v1/EventService/Subscriptions/old-a", Destination: "http://OLD-recv:9092/serverevents/alerts/bmc-1", EventFormat: eventFormatEvent},
		// Foreign subscription pointing somewhere unrelated — must be left alone.
		{URI: "/redfish/v1/EventService/Subscriptions/foreign", Destination: "https://prometheus.example.com/alertmanager", EventFormat: eventFormatEvent},
	})
	r := newRec(t, c, dellVendorMatch(), res, &fakeFactory{client: fc})

	if _, err := r.Reconcile(context.Background(), req()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	deletes := fc.snapshotDeletes()
	if len(deletes) != 2 {
		t.Fatalf("expected 2 stale deletes, got %v", deletes)
	}
	for _, want := range []string{
		"/redfish/v1/EventService/Subscriptions/old-a",
		"/redfish/v1/EventService/Subscriptions/old-m",
	} {
		if !slices.Contains(deletes, want) {
			t.Errorf("missing delete for stale subscription %q; deletes=%v", want, deletes)
		}
	}
	if slices.Contains(deletes, "/redfish/v1/EventService/Subscriptions/foreign") {
		t.Error("foreign subscription was deleted")
	}
	// Stale ones were deleted; new ones get created for the current receiver.
	if c := fc.snapshotCreates(); len(c) != 2 {
		t.Errorf("after stale cleanup: creates=%d, want 2", len(c))
	}
}

// TestReconcile_FormatMismatchClassifiedAsStale pins the fix for the
// convergence bug where an entry whose URL path says "metricsreport" but
// whose EventFormat says "Event" (or vice versa) was skipped by classify
// — meaning ensureSubscriptions would create a replacement while the
// malformed entry lingered forever, eventually exhausting BMC quotas.
// Now such entries must land in the stale bucket and get deleted.
func TestReconcile_FormatMismatchClassifiedAsStale(t *testing.T) {
	c := newClientWith(t, bmcObject(testBMCName, vendorDellInc, ""))
	res := &fakeResolver{}
	res.set(makeResolved(), nil)
	fc := &fakeClient{}
	fc.setSubscriptions([]subscriptions.Subscription{
		// Path says metricsreport, but EventFormat is Event — corrupt.
		{URI: "/redfish/v1/EventService/Subscriptions/bad-m", Destination: metricReportDest, EventFormat: eventFormatEvent},
		// Path says alerts, but EventFormat is MetricReport — corrupt.
		{URI: "/redfish/v1/EventService/Subscriptions/bad-a", Destination: alertsDest, EventFormat: eventFormatMetricReport},
	})
	r := newRec(t, c, dellVendorMatch(), res, &fakeFactory{client: fc})

	if _, err := r.Reconcile(context.Background(), req()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	deletes := fc.snapshotDeletes()
	if len(deletes) != 2 {
		t.Fatalf("expected 2 mismatched-format deletes, got %v", deletes)
	}
	for _, want := range []string{
		"/redfish/v1/EventService/Subscriptions/bad-a",
		"/redfish/v1/EventService/Subscriptions/bad-m",
	} {
		if !slices.Contains(deletes, want) {
			t.Errorf("missing delete for mismatched subscription %q; deletes=%v", want, deletes)
		}
	}
	// The mismatched entries were removed; ensureSubscriptions then
	// creates the correct pair.
	if c := fc.snapshotCreates(); len(c) != 2 {
		t.Errorf("after mismatch cleanup: creates=%d, want 2", len(c))
	}
}

// -- Reconcile: unmatched BMC, delete our subscriptions --

func TestReconcile_UnmatchedBMC_DeletesCurrent(t *testing.T) {
	// Vendor "Lenovo" is not in the Dell-only eventBasedHardware policy,
	// so any of our existing subscriptions on that BMC must be deleted.
	c := newClientWith(t, bmcObject(testBMCName, "Lenovo", ""))
	res := &fakeResolver{}
	res.set(makeResolved(), nil)
	fc := &fakeClient{}
	fc.setSubscriptions([]subscriptions.Subscription{
		{URI: subURIMetric, Destination: metricReportDest, EventFormat: eventFormatMetricReport},
		{URI: subURIAlert, Destination: alertsDest, EventFormat: eventFormatEvent},
	})
	r := newRec(t, c, dellVendorMatch(), res, &fakeFactory{client: fc})

	if _, err := r.Reconcile(context.Background(), req()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	deletes := fc.snapshotDeletes()
	if len(deletes) != 2 {
		t.Fatalf("polling-mode deletes: got %d, want 2", len(deletes))
	}
	if c := fc.snapshotCreates(); len(c) != 0 {
		t.Errorf("polling mode must not create: %+v", c)
	}
}

// -- Reconcile: BMC deleted (IsNotFound) tears down and forgets sink --

func TestReconcile_BMCDeleted_TearsDownAndForgets(t *testing.T) {
	// Empty fake client: Get returns IsNotFound, triggering the
	// teardown branch.
	c := newClientWith(t)
	res := &fakeResolver{}
	res.set(makeResolved(), nil)
	fc := &fakeClient{}
	fc.setSubscriptions([]subscriptions.Subscription{
		{URI: subURIMetric, Destination: metricReportDest, EventFormat: eventFormatMetricReport},
		{URI: subURIAlert, Destination: alertsDest, EventFormat: eventFormatEvent},
	})
	fs := &fakeSink{}
	fms := &fakeMetricSink{}
	r := newRec(t, c, dellVendorMatch(), res, &fakeFactory{client: fc})
	r.Sink = fs
	r.MetricReportSink = fms

	result, err := r.Reconcile(context.Background(), req())
	if err != nil {
		t.Fatalf("Reconcile (IsNotFound): %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Errorf("RequeueAfter on delete: got %v, want 0", result.RequeueAfter)
	}
	if got := fc.snapshotDeletes(); len(got) != 2 {
		t.Errorf("delete calls: got %d, want 2", len(got))
	}
	if got := fs.snapshotForgotten(); len(got) != 1 || got[0] != testBMCName {
		t.Errorf("Sink.Forget: got %v, want [bmc-1]", got)
	}
	if got := fms.snapshotForgotten(); len(got) != 1 || got[0] != testBMCName {
		t.Errorf("MetricReportSink.Forget: got %v, want [bmc-1]", got)
	}
}

// -- Reconcile: finalizer lifecycle --

// TestReconcile_BMCBeingDeleted_TearsDownAndRemovesFinalizer pins the
// primary correctness fix for the pre-finalizer teardown bug: while the
// BMC object is being deleted but still present (thanks to our
// finalizer), tearDownOne runs against a resolvable object, then the
// finalizer is stripped so k8s completes the delete.
func TestReconcile_BMCBeingDeleted_TearsDownAndRemovesFinalizer(t *testing.T) {
	seed := bmcObjectBeingDeleted(testBMCName, vendorDellInc, modelR650)
	c := newClientWith(t, seed)
	res := &fakeResolver{}
	res.set(makeResolved(), nil)
	fc := &fakeClient{}
	fc.setSubscriptions([]subscriptions.Subscription{
		{URI: subURIMetric, Destination: metricReportDest, EventFormat: eventFormatMetricReport},
		{URI: subURIAlert, Destination: alertsDest, EventFormat: eventFormatEvent},
	})
	fs := &fakeSink{}
	fms := &fakeMetricSink{}
	r := newRec(t, c, dellVendorMatch(), res, &fakeFactory{client: fc})
	r.Sink = fs
	r.MetricReportSink = fms

	if _, err := r.Reconcile(context.Background(), req()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := fc.snapshotDeletes(); len(got) != 2 {
		t.Errorf("delete calls: got %d, want 2", len(got))
	}
	if got := fs.snapshotForgotten(); len(got) != 1 || got[0] != testBMCName {
		t.Errorf("Sink.Forget: got %v, want [bmc-1]", got)
	}
	if got := fms.snapshotForgotten(); len(got) != 1 || got[0] != testBMCName {
		t.Errorf("MetricReportSink.Forget: got %v, want [bmc-1]", got)
	}
	// Finalizer must be gone so k8s can complete the delete. The fake
	// client honours DeletionTimestamp + empty finalizers → deletion.
	got := &metalv1alpha1.BMC{}
	err := c.Get(context.Background(), types.NamespacedName{Name: testBMCName}, got)
	switch err {
	case nil:
		if slices.Contains(got.Finalizers, subsFinalizer) {
			t.Errorf("finalizer still present after teardown: %v", got.Finalizers)
		}
	default:
		// NotFound is also fine — the fake client may have completed the
		// delete already. Anything else is a bug.
	}
}

// TestReconcile_EventBased_AddsFinalizer pins that a happy-path reconcile
// attaches the finalizer to the BMC BEFORE the subscription creates,
// so a crash mid-reconcile doesn't leave an orphaned subscription.
func TestReconcile_EventBased_AddsFinalizer(t *testing.T) {
	c := newClientWith(t, bmcObject(testBMCName, vendorDellInc, modelR650))
	res := &fakeResolver{}
	res.set(makeResolved(), nil)
	fc := &fakeClient{}
	r := newRec(t, c, dellVendorMatch(), res, &fakeFactory{client: fc})

	if _, err := r.Reconcile(context.Background(), req()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got := &metalv1alpha1.BMC{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: testBMCName}, got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !slices.Contains(got.Finalizers, subsFinalizer) {
		t.Errorf("finalizer not added; got %v", got.Finalizers)
	}
}

// TestReconcile_UnmatchedBMC_RemovesStaleFinalizer covers the transition
// case: a BMC that was previously event-based (carries our finalizer)
// no longer matches an eventBasedHardware row. The reconciler must
// delete its subscriptions AND strip the finalizer so future deletes
// aren't blocked by an operator that no longer manages the BMC.
func TestReconcile_UnmatchedBMC_RemovesStaleFinalizer(t *testing.T) {
	// Lenovo isn't matched by dellVendorMatch, but the BMC carries the
	// finalizer from a prior "subscribed" pass.
	seed := bmcObjectWithFinalizer(testBMCName, "Lenovo", "")
	c := newClientWith(t, seed)
	res := &fakeResolver{}
	res.set(makeResolved(), nil)
	fc := &fakeClient{}
	fc.setSubscriptions([]subscriptions.Subscription{
		{URI: subURIMetric, Destination: metricReportDest, EventFormat: eventFormatMetricReport},
	})
	r := newRec(t, c, dellVendorMatch(), res, &fakeFactory{client: fc})

	if _, err := r.Reconcile(context.Background(), req()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got := fc.snapshotDeletes(); len(got) != 1 {
		t.Errorf("delete calls: got %d, want 1", len(got))
	}
	got := &metalv1alpha1.BMC{}
	if err := c.Get(context.Background(), types.NamespacedName{Name: testBMCName}, got); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if slices.Contains(got.Finalizers, subsFinalizer) {
		t.Errorf("stale finalizer not removed: %v", got.Finalizers)
	}
}

// -- Reconcile: Resolver / Factory / list errors are swallowed --

func TestReconcile_ResolverError_SkipsBMC(t *testing.T) {
	c := newClientWith(t, bmcObject(testBMCName, vendorDellInc, ""))
	res := &fakeResolver{}
	res.set(nil, errors.New("apiserver wedged"))
	fc := &fakeClient{}
	fac := &fakeFactory{client: fc}
	r := newRec(t, c, dellVendorMatch(), res, fac)

	if _, err := r.Reconcile(context.Background(), req()); err != nil {
		t.Fatalf("Reconcile should not propagate resolver error: %v", err)
	}
	if fac.calls.Load() > 0 {
		t.Errorf("factory called %d times, expected 0", fac.calls.Load())
	}
}

func TestReconcile_FactoryError_SkipsBMC(t *testing.T) {
	c := newClientWith(t, bmcObject(testBMCName, vendorDellInc, ""))
	res := &fakeResolver{}
	res.set(makeResolved(), nil)
	fc := &fakeClient{}
	fac := &fakeFactory{client: fc, err: errors.New("dial timeout")}
	r := newRec(t, c, dellVendorMatch(), res, fac)

	if _, err := r.Reconcile(context.Background(), req()); err != nil {
		t.Fatalf("Reconcile should not propagate factory error: %v", err)
	}
	if got := fc.snapshotCreates(); len(got) != 0 {
		t.Errorf("factory failed but creates happened: %+v", got)
	}
}

func TestReconcile_ListSubscriptionsError_SkipsBMC(t *testing.T) {
	c := newClientWith(t, bmcObject(testBMCName, vendorDellInc, ""))
	res := &fakeResolver{}
	res.set(makeResolved(), nil)
	fc := &fakeClient{}
	fc.listErr = errors.New("list 500")
	r := newRec(t, c, dellVendorMatch(), res, &fakeFactory{client: fc})

	if _, err := r.Reconcile(context.Background(), req()); err != nil {
		t.Fatalf("Reconcile should not propagate list error: %v", err)
	}
	if c := fc.snapshotCreates(); len(c) != 0 {
		t.Errorf("list failed but creates happened: %+v", c)
	}
	if d := fc.snapshotDeletes(); len(d) != 0 {
		t.Errorf("lister failed but deletes happened: %v", d)
	}
}

// -- Reconcile: BMC with invalid IP is skipped before opening a client --

func TestReconcile_BMCNotReady_SkipsClient(t *testing.T) {
	c := newClientWith(t, bmcObject(testBMCName, vendorDellInc, ""))
	res := &fakeResolver{}
	// Resolved with zero-value IP (not valid) — reconcile should
	// short-circuit before constructing a BMC client.
	notReady := &subscriptions.Resolved{
		BMC: &metalv1alpha1.BMC{ObjectMeta: metav1.ObjectMeta{Name: testBMCName}},
	}
	res.set(notReady, nil)
	fc := &fakeClient{}
	fac := &fakeFactory{client: fc}
	r := newRec(t, c, dellVendorMatch(), res, fac)

	if _, err := r.Reconcile(context.Background(), req()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if fac.calls.Load() > 0 {
		t.Errorf("factory called for BMC with invalid IP: calls=%d", fac.calls.Load())
	}
}

// -- Reconcile: successful pass returns RequeueAfter > 0 --

func TestReconcile_Success_ReturnsRequeueAfter(t *testing.T) {
	c := newClientWith(t, bmcObject(testBMCName, vendorDellInc, ""))
	res := &fakeResolver{}
	res.set(makeResolved(), nil)
	fc := &fakeClient{}
	r := newRec(t, c, dellVendorMatch(), res, &fakeFactory{client: fc})

	result, err := r.Reconcile(context.Background(), req())
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Errorf("RequeueAfter: got 0, want non-zero (periodic re-sweep)")
	}
}

// TestReconcile_NilConfig_DefersInsteadOfTearingDown pins the startup-
// race guard. Before this guard, a Reconcile that fired before
// ConfigLoader's first successful load would call SubscribeToBMC(ref,
// nil) → false → deleteSubscriptions, tearing live subscriptions off
// every event-capable BMC in the fleet on every operator restart. The
// reconciler must defer instead.
func TestReconcile_NilConfig_DefersInsteadOfTearingDown(t *testing.T) {
	c := newClientWith(t, bmcObject(testBMCName, vendorDellInc, modelR650))
	res := &fakeResolver{}
	res.set(makeResolved(), nil)
	fc := &fakeClient{}
	// Seed some existing subscriptions the reconciler MUST NOT touch
	// while the config is nil.
	fc.setSubscriptions([]subscriptions.Subscription{
		{URI: "/redfish/v1/EventService/Subscriptions/a", Destination: metricReportDest, EventFormat: eventFormatMetricReport},
		{URI: "/redfish/v1/EventService/Subscriptions/b", Destination: alertsDest, EventFormat: eventFormatEvent},
	})
	r := newRec(t, c, dellVendorMatch(), res, &fakeFactory{client: fc})
	// Force the "config not yet loaded" condition.
	r.Config = func() *subscriptions.Config { return nil }

	result, err := r.Reconcile(context.Background(), req())
	if err != nil {
		t.Fatalf("Reconcile with nil config: %v", err)
	}
	if result.RequeueAfter == 0 {
		t.Errorf("RequeueAfter: got 0, want non-zero (defer for config load)")
	}
	// No BMC-side calls at all — the reconciler must not have even
	// constructed a client, let alone deleted subscriptions.
	if got := fc.snapshotDeletes(); len(got) != 0 {
		t.Errorf("nil config triggered deletes: %v (want none)", got)
	}
	if got := fc.snapshotCreates(); len(got) != 0 {
		t.Errorf("nil config triggered creates: %v (want none)", got)
	}
}

// -- SubscriberID disambiguation --

// TestSubscriberID_DestinationIncludesSegment confirms the reconciler
// POSTs destinations of the shape
// /serverevents/<subscriberID>/{kind}/<bmcName> when SubscriberID is set.
func TestSubscriberID_DestinationIncludesSegment(t *testing.T) {
	c := newClientWith(t, bmcObject(testBMCName, vendorDellInc, ""))
	res := &fakeResolver{}
	res.set(makeResolved(), nil)
	fc := &fakeClient{}
	r := newRec(t, c, dellVendorMatch(), res, &fakeFactory{client: fc})
	r.SubscriberID = "redfish-exporter"
	// SubscriberID is read from the parsed URL path only inside
	// destinationFor; init validated it on first build. Re-init so the
	// path generator picks up the new value.
	if err := subscriptions.InitForTest(r); err != nil {
		t.Fatalf("re-InitForTest: %v", err)
	}

	if _, err := r.Reconcile(context.Background(), req()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, ev := range fc.snapshotCreates() {
		if !strings.Contains(ev.dest, "/serverevents/redfish-exporter/") {
			t.Errorf("destination %q missing the subscriber segment", ev.dest)
		}
	}
}

// TestSubscriberID_IgnoresForeignSubscriptions confirms that when a BMC
// already has a subscription owned by a different subscriber (e.g. the
// operator's), this reconciler neither adopts it nor deletes it as
// stale. It creates its own pair fresh.
func TestSubscriberID_IgnoresForeignSubscriptions(t *testing.T) {
	c := newClientWith(t, bmcObject(testBMCName, vendorDellInc, ""))
	res := &fakeResolver{}
	res.set(makeResolved(), nil)
	fc := &fakeClient{}
	fc.setSubscriptions([]subscriptions.Subscription{
		// Owned by a different subscriber on the SAME receiver host.
		{
			URI:         "/redfish/v1/EventService/Subscriptions/operator-1",
			Destination: "http://recv:9092/serverevents/metal-maintenance-operator/alerts/bmc-1",
			EventFormat: eventFormatEvent,
		},
	})
	r := newRec(t, c, dellVendorMatch(), res, &fakeFactory{client: fc})
	r.SubscriberID = "redfish-exporter"
	if err := subscriptions.InitForTest(r); err != nil {
		t.Fatalf("re-InitForTest: %v", err)
	}

	if _, err := r.Reconcile(context.Background(), req()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if c := fc.snapshotCreates(); len(c) != 2 {
		t.Errorf("creates: got %d, want 2 (own pair)", len(c))
	}
	for _, deleted := range fc.snapshotDeletes() {
		if deleted == "/redfish/v1/EventService/Subscriptions/operator-1" {
			t.Errorf("foreign-subscriber subscription was deleted: %s", deleted)
		}
	}
}

// TestSubscriberID_EmptyProducesBareShape pins the reconciler's URL
// shape when SubscriberID is unset: the /serverevents/{kind}/{bmcName}
// segment carries no subscriber path component. Callers that don't need
// to disambiguate against a second subscriber leave this empty.
func TestSubscriberID_EmptyProducesBareShape(t *testing.T) {
	c := newClientWith(t, bmcObject(testBMCName, vendorDellInc, ""))
	res := &fakeResolver{}
	res.set(makeResolved(), nil)
	fc := &fakeClient{}
	r := newRec(t, c, dellVendorMatch(), res, &fakeFactory{client: fc})
	// SubscriberID intentionally unset.

	if _, err := r.Reconcile(context.Background(), req()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	for _, ev := range fc.snapshotCreates() {
		stripped := strings.TrimPrefix(ev.dest, testReceiverURL)
		if !strings.HasPrefix(stripped, "/serverevents/metricsreport/") &&
			!strings.HasPrefix(stripped, "/serverevents/alerts/") {
			t.Errorf("bare destination shape broken: %q", ev.dest)
		}
	}
}
