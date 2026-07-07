// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package subscriptions_test

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/subscriptions"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/stmcginnis/gofish/schemas"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// -- fakes (shared across reconciler_test.go and other _test.go files) --

type fakeResolver struct {
	mu  sync.Mutex
	out map[string]*subscriptions.Resolved
	err map[string]error
}

func (r *fakeResolver) set(resolved *subscriptions.Resolved, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.out == nil {
		r.out = map[string]*subscriptions.Resolved{}
	}
	if r.err == nil {
		r.err = map[string]error{}
	}
	r.out[testBMCName] = resolved
	r.err[testBMCName] = err
}

func (r *fakeResolver) Resolve(_ context.Context, name string) (*subscriptions.Resolved, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.err[name]; err != nil {
		return nil, err
	}
	return r.out[name], nil
}

type fakeClient struct {
	mu          sync.Mutex
	createCalls []createCall
	deleteCalls []string
	createErr   error
	deleteErr   error
	createURI   string // returned by CreateEventSubscription on success
	subs        []subscriptions.Subscription
	listErr     error
	logouts     atomic.Int32
}

type createCall struct {
	dest   string
	format schemas.EventFormatType
}

func (c *fakeClient) CreateEventSubscription(_ context.Context, dest string, format schemas.EventFormatType, _ schemas.DeliveryRetryPolicy) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.createCalls = append(c.createCalls, createCall{dest: dest, format: format})
	if c.createErr != nil {
		return "", c.createErr
	}
	uri := c.createURI
	if uri == "" {
		uri = "/redfish/v1/EventService/Subscriptions/auto"
	}
	return uri, nil
}

func (c *fakeClient) DeleteEventSubscription(_ context.Context, uri string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deleteCalls = append(c.deleteCalls, uri)
	return c.deleteErr
}

func (c *fakeClient) Logout() { c.logouts.Add(1) }

func (c *fakeClient) ListEventSubscriptions(_ context.Context) ([]subscriptions.Subscription, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.listErr != nil {
		return nil, c.listErr
	}
	out := make([]subscriptions.Subscription, len(c.subs))
	copy(out, c.subs)
	return out, nil
}

func (c *fakeClient) setSubscriptions(subs []subscriptions.Subscription) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subs = subs
}

func (c *fakeClient) snapshotCreates() []createCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]createCall, len(c.createCalls))
	copy(out, c.createCalls)
	return out
}

func (c *fakeClient) snapshotDeletes() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.deleteCalls))
	copy(out, c.deleteCalls)
	sort.Strings(out)
	return out
}

type fakeFactory struct {
	client *fakeClient
	err    error
	calls  atomic.Int32
}

func (f *fakeFactory) NewClient(_ context.Context, _ *subscriptions.Resolved) (subscriptions.Client, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return f.client, nil
}

// fakeSink records every Forget call so tests can pin that BMC removal
// signals the sink. PublishEvents is a no-op — the subscription
// reconciler never calls it.
type fakeSink struct {
	mu        sync.Mutex
	forgotten []string
}

func (s *fakeSink) PublishEvents(_ context.Context, _ string, _ []sink.Event) error {
	return nil
}

func (s *fakeSink) Forget(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forgotten = append(s.forgotten, name)
}

func (s *fakeSink) snapshotForgotten() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.forgotten))
	copy(out, s.forgotten)
	return out
}

// fakeMetricSink mirrors fakeSink for the MetricReportSink contract.
// PublishSamples is a no-op — the subscription reconciler never calls it
// directly; the receiver does.
type fakeMetricSink struct {
	mu        sync.Mutex
	forgotten []string
}

func (s *fakeMetricSink) PublishSamples(_ context.Context, _ string, _ []sink.Sample) error {
	return nil
}

func (s *fakeMetricSink) Forget(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.forgotten = append(s.forgotten, name)
}

func (s *fakeMetricSink) snapshotForgotten() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.forgotten))
	copy(out, s.forgotten)
	return out
}

// -- helpers --

// makeResolved builds a Resolved with a valid IP so the reconciler's
// "not yet ready" guard doesn't short-circuit.
func makeResolved() *subscriptions.Resolved {
	return &subscriptions.Resolved{
		BMC: &metalv1alpha1.BMC{
			ObjectMeta: metav1.ObjectMeta{Name: testBMCName},
			Status: metalv1alpha1.BMCStatus{
				IP:           metalv1alpha1.MustParseIP("10.0.0.1"),
				Manufacturer: "TestVendor",
			},
		},
		Username: "u",
		Password: "p",
	}
}

// reconcilerScheme is a minimal scheme that knows about metalv1alpha1.BMC
// so the fake client can read/write BMC objects.
func reconcilerScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := metalv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return s
}

// bmcObject builds a BMC suitable for fake-client seeding. Vendor goes
// into Status.Manufacturer; the reconciler reads it via bmcRefFromObject.
func bmcObject(name, vendor, model string) *metalv1alpha1.BMC {
	return &metalv1alpha1.BMC{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: metalv1alpha1.BMCStatus{
			IP:           metalv1alpha1.MustParseIP("10.0.0.1"),
			Manufacturer: vendor,
			Model:        model,
		},
	}
}

// bmcObjectWithFinalizer builds a BMC that already carries the
// subscription finalizer — the state a BMC is in after we've decided
// to subscribe it. Tests that exercise the delete path or the
// stale-finalizer cleanup path seed with this.
func bmcObjectWithFinalizer(name, vendor, model string) *metalv1alpha1.BMC {
	b := bmcObject(name, vendor, model)
	b.Finalizers = []string{subsFinalizer}
	return b
}

// bmcObjectBeingDeleted returns a BMC with DeletionTimestamp set and the
// subscription finalizer present — the state Reconcile sees on the
// initial delete pass. The fake client rejects objects with a
// DeletionTimestamp unless there's also at least one finalizer, so this
// helper always sets both together.
func bmcObjectBeingDeleted(name, vendor, model string) *metalv1alpha1.BMC {
	b := bmcObjectWithFinalizer(name, vendor, model)
	now := metav1.Now()
	b.DeletionTimestamp = &now
	return b
}

// newClientWith builds a fake controller-runtime client seeded with the
// given objects. Use bmcObject(...) to construct seed BMCs.
func newClientWith(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	return fake.NewClientBuilder().WithScheme(reconcilerScheme(t)).WithObjects(objs...).Build()
}

// dellVendorMatch is the canonical EventBased policy used by most
// retargeted tests: any Dell BMC is event-eligible.
func dellVendorMatch() *subscriptions.Config {
	return &subscriptions.Config{EventBasedHardware: []subscriptions.HardwareMatch{
		{Vendor: vendorDellInc, Models: []string{"*"}},
	}}
}

// newRec builds a fully-initialised BMCReconciler for unit tests.
// ReconcileInterval is set to time.Hour by default so a successful
// reconcile's RequeueAfter doesn't drive surprise re-runs in tests.
func newRec(t *testing.T, c client.Client, cfg *subscriptions.Config, res *fakeResolver, fac *fakeFactory) *subscriptions.BMCReconciler {
	t.Helper()
	r := &subscriptions.BMCReconciler{
		Client:            c,
		Config:            func() *subscriptions.Config { return cfg },
		Resolver:          res,
		Factory:           fac,
		ReceiverURL:       testReceiverURL,
		ReconcileInterval: time.Hour,
		PerBMCTimeout:     time.Second,
	}
	if err := subscriptions.InitForTest(r); err != nil {
		t.Fatalf("InitForTest: %v", err)
	}
	return r
}
