// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	discoveryv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/discovery/v1alpha1"
)

func TestRegisterRoundtrip(t *testing.T) {
	now := metav1.Now()
	srv := NewServer(logr.Discard(), ":0")

	payload, err := json.Marshal(RegistrationPayload{
		SystemUUID: "some-uuid",
		Data: discoveryv1alpha1.Metadata{
			Timestamp: &now,
			SystemInfo: discoveryv1alpha1.DMI{
				SystemInformation: discoveryv1alpha1.SystemInformation{ProductName: "EX-1000"},
			},
			NetworkInterfaces: []discoveryv1alpha1.NetworkInterface{{
				Name:       "eth0",
				MACAddress: "00:11:22:33:44:55",
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/register", bytes.NewReader(payload)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, rec.Code)
	}

	data, ok := srv.Load("some-uuid")
	if !ok {
		t.Fatal("expected data to be registered")
	}
	if data.SystemInfo.SystemInformation.ProductName != "EX-1000" {
		t.Fatalf("unexpected product name %q", data.SystemInfo.SystemInformation.ProductName)
	}
	if len(data.NetworkInterfaces) != 1 {
		t.Fatalf("expected 1 network interface, got %d", len(data.NetworkInterfaces))
	}

	srv.Delete("some-uuid")
	if _, ok := srv.Load("some-uuid"); ok {
		t.Fatal("expected data to be deleted")
	}
}

func TestIsFresh(t *testing.T) {
	now := time.Now()
	maxAge := time.Minute
	for _, tt := range []struct {
		name string
		age  time.Duration
		want bool
	}{
		{"current", 0, true},
		{"recent", maxAge - time.Nanosecond, true},
		{"stale boundary", maxAge, false},
		{"stale", maxAge + time.Second, false},
		{"future within tolerance", -MaxClockSkew / 2, true},
		{"future boundary", -MaxClockSkew, true},
		{"future beyond tolerance", -MaxClockSkew - time.Nanosecond, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			timestamp := metav1.NewTime(now.Add(-tt.age))
			if got := IsFresh(&timestamp, now, maxAge); got != tt.want {
				t.Fatalf("IsFresh = %v, want %v", got, tt.want)
			}
		})
	}
	if IsFresh(nil, now, maxAge) {
		t.Fatal("missing timestamp must not be fresh")
	}
}

func TestSweep(t *testing.T) {
	srv := NewServer(logr.Discard(), ":0")

	fresh := metav1.Now()
	stale := metav1.NewTime(time.Now().Add(-srv.staleEntryTTL - time.Minute))
	srv.Store("fresh-uuid", discoveryv1alpha1.Metadata{Timestamp: &fresh})
	srv.Store("stale-uuid", discoveryv1alpha1.Metadata{Timestamp: &stale})
	srv.Store("no-timestamp-uuid", discoveryv1alpha1.Metadata{})
	nearFuture := metav1.NewTime(time.Now().Add(MaxClockSkew / 2))
	farFuture := metav1.NewTime(time.Now().Add(MaxClockSkew + time.Minute))
	srv.Store("near-future-uuid", discoveryv1alpha1.Metadata{Timestamp: &nearFuture})
	srv.Store("far-future-uuid", discoveryv1alpha1.Metadata{Timestamp: &farFuture})

	srv.sweep()

	if _, ok := srv.Load("fresh-uuid"); !ok {
		t.Fatal("expected fresh entry to survive sweep")
	}
	if _, ok := srv.Load("stale-uuid"); ok {
		t.Fatal("expected stale entry to be swept")
	}
	if _, ok := srv.Load("no-timestamp-uuid"); ok {
		t.Fatal("expected entry without timestamp to be swept")
	}
	if _, ok := srv.Load("near-future-uuid"); !ok {
		t.Fatal("expected timestamp within clock-skew tolerance to survive sweep")
	}
	if _, ok := srv.Load("far-future-uuid"); ok {
		t.Fatal("expected timestamp beyond clock-skew tolerance to be swept")
	}
}

func TestRegisterBodyLimit(t *testing.T) {
	const prefix = `{"systemUUID":"test-uuid","data":{},"padding":"`
	const suffix = `"}`
	for _, extra := range []int{0, 1} {
		srv := NewServer(logr.Discard(), ":0")
		body := prefix + strings.Repeat("x", maxRegistryBodyBytes-len(prefix)-len(suffix)+extra) + suffix
		rec := httptest.NewRecorder()
		srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(body)))
		want := http.StatusCreated
		if extra > 0 {
			want = http.StatusBadRequest
		}
		if rec.Code != want {
			t.Fatalf("body size %d: status = %d, want %d", len(body), rec.Code, want)
		}
		_, stored := srv.Load("test-uuid")
		if stored != (extra == 0) {
			t.Fatalf("body size %d: stored = %v", len(body), stored)
		}
	}
}

func TestRegisterValidation(t *testing.T) {
	srv := NewServer(logr.Discard(), ":0")

	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/register", bytes.NewReader([]byte(`{"data":{}}`))))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d for empty systemUUID, got %d", http.StatusBadRequest, rec.Code)
	}

	rec = httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/register", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected status %d for GET, got %d", http.StatusMethodNotAllowed, rec.Code)
	}
}
