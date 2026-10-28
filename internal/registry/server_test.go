// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestSweep(t *testing.T) {
	srv := NewServer(logr.Discard(), ":0")

	fresh := metav1.Now()
	stale := metav1.NewTime(time.Now().Add(-srv.staleEntryTTL - time.Minute))
	srv.Store("fresh-uuid", discoveryv1alpha1.Metadata{Timestamp: &fresh})
	srv.Store("stale-uuid", discoveryv1alpha1.Metadata{Timestamp: &stale})
	srv.Store("no-timestamp-uuid", discoveryv1alpha1.Metadata{})

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
