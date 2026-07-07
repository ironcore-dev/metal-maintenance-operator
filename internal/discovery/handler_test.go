// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package discovery_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/discovery"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// newClient builds a fake controller-runtime client populated with the
// supplied BMC objects. Mirrors the helper in poller/resolver_test.go.
func newClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	s := runtime.NewScheme()
	if err := metalv1alpha1.AddToScheme(s); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

// readyBMC builds a BMC fixture that should appear in the SD output.
func readyBMC(name string, opts ...func(*metalv1alpha1.BMC)) *metalv1alpha1.BMC {
	b := &metalv1alpha1.BMC{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ""},
		Spec: metalv1alpha1.BMCSpec{
			BMCSecretRef: corev1.LocalObjectReference{Name: name + "-creds"},
			Protocol: metalv1alpha1.Protocol{
				Name: metalv1alpha1.ProtocolRedfish,
				Port: 443,
			},
		},
		Status: metalv1alpha1.BMCStatus{
			IP:              metalv1alpha1.MustParseIP("10.0.0.1"),
			Manufacturer:    "Dell",
			Model:           "PowerEdge R750",
			SerialNumber:    "ABCD1234",
			FirmwareVersion: "5.10.30.10",
		},
	}
	for _, o := range opts {
		o(b)
	}
	return b
}

// -- Render() pure-function tests --

func TestRender_HappyPath(t *testing.T) {
	b := readyBMC("bmc-1")
	got := discovery.Render([]metalv1alpha1.BMC{*b})

	if len(got) != 1 {
		t.Fatalf("targets: got %d, want 1: %+v", len(got), got)
	}
	if len(got[0].Targets) != 1 || got[0].Targets[0] != "10.0.0.1:443" {
		t.Errorf("targets: got %v, want [10.0.0.1:443]", got[0].Targets)
	}
	labels := got[0].Labels
	for k, v := range map[string]string{
		"__meta_bmc_name":             "bmc-1",
		"__meta_bmc_vendor":           "Dell",
		"__meta_bmc_model":            "PowerEdge R750",
		"__meta_bmc_serial":           "ABCD1234",
		"__meta_bmc_firmware":         "5.10.30.10",
		"__meta_bmc_protocol":         "https",
		"__meta_bmc_secret_name":      "bmc-1-creds",
		"__meta_bmc_secret_namespace": "",
	} {
		if labels[k] != v {
			t.Errorf("label %s: got %q, want %q", k, labels[k], v)
		}
	}
}

func TestRender_DropsBMCsWithoutIP(t *testing.T) {
	noIP := readyBMC("no-ip", func(b *metalv1alpha1.BMC) {
		b.Status.IP = metalv1alpha1.IP{}
	})
	got := discovery.Render([]metalv1alpha1.BMC{*noIP})
	if len(got) != 0 {
		t.Errorf("BMC without IP should be omitted; got %+v", got)
	}
}

func TestRender_DropsDeletionPendingBMCs(t *testing.T) {
	now := metav1.NewTime(time.Now())
	deleting := readyBMC("deleting", func(b *metalv1alpha1.BMC) {
		b.DeletionTimestamp = &now
		b.Finalizers = []string{"a-finalizer-keeping-it-alive"} // required by fake client
	})
	got := discovery.Render([]metalv1alpha1.BMC{*deleting})
	if len(got) != 0 {
		t.Errorf("deletion-pending BMC should be omitted; got %+v", got)
	}
}

func TestRender_OmitsPortWhenZero(t *testing.T) {
	b := readyBMC("noport", func(b *metalv1alpha1.BMC) {
		b.Spec.Protocol.Port = 0
	})
	got := discovery.Render([]metalv1alpha1.BMC{*b})
	if len(got) != 1 || got[0].Targets[0] != "10.0.0.1" {
		t.Errorf("zero-port should yield bare host; got %v", got[0].Targets)
	}
}

func TestRender_DefaultsSchemeToHTTPS(t *testing.T) {
	b := readyBMC("noscheme", func(b *metalv1alpha1.BMC) {
		b.Spec.Protocol.Scheme = ""
	})
	got := discovery.Render([]metalv1alpha1.BMC{*b})
	if got[0].Labels["__meta_bmc_protocol"] != "https" {
		t.Errorf("default protocol: got %q, want https", got[0].Labels["__meta_bmc_protocol"])
	}
}

func TestRender_OmitsEmptyOptionalLabels(t *testing.T) {
	// A BMC just-discovered, before status has populated.
	b := &metalv1alpha1.BMC{
		ObjectMeta: metav1.ObjectMeta{Name: "fresh"},
		Spec: metalv1alpha1.BMCSpec{
			BMCSecretRef: corev1.LocalObjectReference{Name: "fresh-creds"},
		},
		Status: metalv1alpha1.BMCStatus{
			IP: metalv1alpha1.MustParseIP("10.0.0.2"),
		},
	}
	got := discovery.Render([]metalv1alpha1.BMC{*b})
	if len(got) != 1 {
		t.Fatalf("expected 1 target: %+v", got)
	}
	labels := got[0].Labels
	// Required labels present...
	for _, k := range []string{"__meta_bmc_name", "__meta_bmc_protocol", "__meta_bmc_secret_name"} {
		if _, ok := labels[k]; !ok {
			t.Errorf("missing required label %s in %+v", k, labels)
		}
	}
	// ...optional empty fields omitted (not emitted as "").
	for _, k := range []string{"__meta_bmc_vendor", "__meta_bmc_model", "__meta_bmc_serial", "__meta_bmc_firmware"} {
		if v, ok := labels[k]; ok {
			t.Errorf("optional empty label %s should be omitted, got %q", k, v)
		}
	}
}

// -- HTTP handler tests --

func TestServeHTTP_GETReturnsSDJSON(t *testing.T) {
	c := newClient(t, readyBMC("bmc-1"), readyBMC("bmc-2"))
	h := &discovery.Handler{Client: c}

	req := httptest.NewRequest(http.MethodGet, discovery.Path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type: got %q, want application/json", got)
	}

	var got []discovery.Target
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("targets: got %d, want 2: %+v", len(got), got)
	}
	// Sort by name so the assertion is order-independent (the fake client
	// returns objects in name order, but we shouldn't depend on it).
	sort.Slice(got, func(i, j int) bool {
		return got[i].Labels["__meta_bmc_name"] < got[j].Labels["__meta_bmc_name"]
	})
	if got[0].Labels["__meta_bmc_name"] != "bmc-1" {
		t.Errorf("first target name: %q", got[0].Labels["__meta_bmc_name"])
	}
}

func TestServeHTTP_RejectsNonGET(t *testing.T) {
	h := &discovery.Handler{Client: newClient(t)}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(method, discovery.Path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: got status %d, want 405", method, rec.Code)
		}
		if rec.Header().Get("Allow") != http.MethodGet {
			t.Errorf("%s: missing Allow: GET header", method)
		}
	}
}

func TestServeHTTP_ListError(t *testing.T) {
	// errClient always errors on List, simulating a cache that hasn't
	// synced yet or an API outage.
	h := &discovery.Handler{Client: &errListClient{err: errors.New("cache not synced")}}
	req := httptest.NewRequest(http.MethodGet, discovery.Path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status: got %d, want 500", rec.Code)
	}
}

func TestServeHTTP_EmptyInventoryReturnsEmptyArray(t *testing.T) {
	// Prometheus expects a JSON array even when empty — null breaks
	// some HTTP SD client implementations.
	h := &discovery.Handler{Client: newClient(t)}
	req := httptest.NewRequest(http.MethodGet, discovery.Path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	body := strings.TrimSpace(rec.Body.String())
	if body != "[]" {
		t.Errorf("empty inventory body: got %q, want %q", body, "[]")
	}
}

// errListClient is a minimal client.Client that returns the given error
// from List. Other methods panic — we don't exercise them.
type errListClient struct {
	client.Client
	err error
}

func (c *errListClient) List(_ context.Context, _ client.ObjectList, _ ...client.ListOption) error {
	return c.err
}
