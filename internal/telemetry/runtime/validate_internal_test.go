// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package runtime

import "testing"

// TestValidateOptions_DefaultsSubscriberID directly observes the default
// value validateOptions writes back into opts.SubscriberID. Subscription
// coexistence in production depends on this default never silently
// changing — a same-package test is the only way to assert the observed
// value (external tests can only inspect error strings, which say
// nothing about the mutated field).
func TestValidateOptions_DefaultsSubscriberID(t *testing.T) {
	opts := Options{
		ConfigName:      "cm",
		ConfigNamespace: "ns",
		ReceiverURL:     "http://r:9092",
		EventsAddr:      ":9092",
	}
	if err := validateOptions(&opts); err != nil {
		t.Fatalf("validateOptions: %v", err)
	}
	if opts.SubscriberID != "metal-maintenance-operator" {
		t.Errorf("SubscriberID default: got %q, want %q",
			opts.SubscriberID, "metal-maintenance-operator")
	}
}

// TestValidateOptions_PreservesExplicitSubscriberID: caller-provided
// values must not be overwritten.
func TestValidateOptions_PreservesExplicitSubscriberID(t *testing.T) {
	opts := Options{
		ConfigName:      "cm",
		ConfigNamespace: "ns",
		ReceiverURL:     "http://r:9092",
		EventsAddr:      ":9092",
		SubscriberID:    "custom-id",
	}
	if err := validateOptions(&opts); err != nil {
		t.Fatalf("validateOptions: %v", err)
	}
	if opts.SubscriberID != "custom-id" {
		t.Errorf("SubscriberID: got %q, want caller value preserved", opts.SubscriberID)
	}
}
