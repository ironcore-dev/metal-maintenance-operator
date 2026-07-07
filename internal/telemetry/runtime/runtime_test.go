// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package runtime_test

import (
	"reflect"
	"strings"
	"testing"

	telemetryruntime "github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/runtime"
)

const (
	testReceiverURL = "http://r:9092"
	testEventsAddr  = ":9092"
)

// AddTo's option validation runs before any manager access. We exercise
// validation by triggering AddTo against a nil manager — option errors
// surface first, then the nil-manager check, so we can assert errors
// either mention or don't mention specific option names.

func TestAddTo_RequiresConfigName(t *testing.T) {
	err := telemetryruntime.AddTo(nil, telemetryruntime.Options{
		ConfigNamespace: "ns",
		ReceiverURL:     testReceiverURL,
		EventsAddr:      testEventsAddr,
	})
	if err == nil {
		t.Fatal("expected error for missing ConfigName")
	}
	if !strings.Contains(err.Error(), "ConfigName") {
		t.Errorf("error doesn't mention ConfigName: %v", err)
	}
}

func TestAddTo_RequiresConfigNamespace(t *testing.T) {
	err := telemetryruntime.AddTo(nil, telemetryruntime.Options{
		ConfigName:  "cm",
		ReceiverURL: testReceiverURL,
		EventsAddr:  testEventsAddr,
	})
	if err == nil {
		t.Fatal("expected error for missing ConfigNamespace")
	}
	if !strings.Contains(err.Error(), "ConfigNamespace") {
		t.Errorf("error doesn't mention ConfigNamespace: %v", err)
	}
}

func TestAddTo_RequiresReceiverURL(t *testing.T) {
	err := telemetryruntime.AddTo(nil, telemetryruntime.Options{
		ConfigName:      "cm",
		ConfigNamespace: "ns",
		EventsAddr:      testEventsAddr,
	})
	if err == nil {
		t.Fatal("expected error for missing ReceiverURL")
	}
	if !strings.Contains(err.Error(), "ReceiverURL") {
		t.Errorf("error doesn't mention ReceiverURL: %v", err)
	}
}

func TestAddTo_RequiresEventsAddr(t *testing.T) {
	err := telemetryruntime.AddTo(nil, telemetryruntime.Options{
		ConfigName:      "cm",
		ConfigNamespace: "ns",
		ReceiverURL:     testReceiverURL,
		// EventsAddr deliberately empty
	})
	if err == nil {
		t.Fatal("expected error for missing EventsAddr")
	}
	if !strings.Contains(err.Error(), "EventsAddr") {
		t.Errorf("error doesn't mention EventsAddr: %v", err)
	}
}

// TestAddTo_PolicyKnobsAreNotOptions pins S5: per-BMC timeout, reconcile
// cadence, and ConfigMap poll interval are NOT Options fields. They live
// in the telemetry ConfigMap (or have been removed entirely in the case
// of the poll interval, since the loader uses a Watch). A future PR that
// re-adds them as fields will break this test.
func TestAddTo_PolicyKnobsAreNotOptions(t *testing.T) {
	optsType := reflect.TypeFor[telemetryruntime.Options]()
	for _, bad := range []string{"ConfigPollInterval", "PerBMCTimeout", "SubscriptionReconcileInterval"} {
		if _, ok := optsType.FieldByName(bad); ok {
			t.Errorf("unexpected Options field %q — policy knobs must not be plumbed here", bad)
		}
	}
}

// TestAddTo_OptionsCarryThrough is a smoke test that Options struct fields
// preserve their values. Catches accidental renames.
func TestAddTo_OptionsCarryThrough(t *testing.T) {
	opts := telemetryruntime.Options{
		ConfigName:                 "cm",
		ConfigNamespace:            "ns",
		ReceiverURL:                "http://x:9092",
		EventsAddr:                 ":9092",
		InsecureTLS:                true,
		SubscriberID:               "metal-maintenance-operator",
		EnableCriticalEventHandler: true,
	}
	if opts.ConfigName != "cm" ||
		opts.ConfigNamespace != "ns" ||
		opts.ReceiverURL != "http://x:9092" ||
		opts.EventsAddr != ":9092" ||
		opts.InsecureTLS != true ||
		opts.SubscriberID != "metal-maintenance-operator" ||
		opts.EnableCriticalEventHandler != true {
		t.Error("Options struct field assignment broken")
	}
}

// -- Critical-event handler options --

// The observable default for SubscriberID is asserted in
// validate_internal_test.go's TestValidateOptions_DefaultsSubscriberID.
// That's a same-package test that reads the value validateOptions
// mutated; from here we can only check that a missing SubscriberID
// doesn't itself surface as a validation error.
func TestAddTo_MissingSubscriberIDIsNotAValidationError(t *testing.T) {
	err := telemetryruntime.AddTo(nil, telemetryruntime.Options{
		ConfigName:      "cm",
		ConfigNamespace: "ns",
		ReceiverURL:     testReceiverURL,
		EventsAddr:      testEventsAddr,
	})
	if err == nil {
		t.Fatal("expected error for nil manager")
	}
	if strings.Contains(err.Error(), "SubscriberID") {
		t.Errorf("unexpected SubscriberID validation error: %v", err)
	}
}
