// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package subscriptions_test

import (
	"strings"
	"testing"
	"time"

	. "github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/subscriptions"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// minimalValid returns a Config that passes all validation rules.
func minimalValid() *Config {
	return &Config{
		APIVersion:                    APIVersion,
		SubscriptionReconcileInterval: 30 * time.Second,
	}
}

func TestValidate_Valid(t *testing.T) {
	if errs := Validate(minimalValid()); len(errs) != 0 {
		t.Fatalf("expected no errors, got: %v", errs)
	}
}

func TestValidate_APIVersionMismatch(t *testing.T) {
	cfg := minimalValid()
	cfg.APIVersion = "telemetry.metal.ironcore.dev/v2alpha1"
	errs := Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected error for apiVersion mismatch, got none")
	}
	assertFieldError(t, errs, "apiVersion")
}

func TestValidate_IntervalTooShort(t *testing.T) {
	cfg := minimalValid()
	cfg.SubscriptionReconcileInterval = 1 * time.Second
	errs := Validate(cfg)
	assertFieldError(t, errs, "subscriptionReconcileInterval")
}

func TestValidate_IntervalTooLong(t *testing.T) {
	cfg := minimalValid()
	cfg.SubscriptionReconcileInterval = 25 * time.Hour
	errs := Validate(cfg)
	assertFieldError(t, errs, "subscriptionReconcileInterval")
}

// TestValidate_PerBMCTimeoutRange pins the per-BMC timeout bounds.
func TestValidate_PerBMCTimeoutRange(t *testing.T) {
	cases := []struct {
		name    string
		val     time.Duration
		wantErr bool
	}{
		{"unset (zero) is fine — defaults apply", 0, false},
		{"too short", 100 * time.Millisecond, true},
		{"too long", 30 * time.Minute, true},
		{"reasonable", 25 * time.Second, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := minimalValid()
			cfg.PerBMCTimeout = tc.val
			errs := Validate(cfg)
			if tc.wantErr {
				assertFieldError(t, errs, "perBMCTimeout")
			} else if len(errs) != 0 {
				t.Errorf("unexpected errors: %v", errs)
			}
		})
	}
}

func TestValidate_EventBasedHardware_MissingVendor(t *testing.T) {
	cfg := minimalValid()
	cfg.EventBasedHardware = []HardwareMatch{{Models: []string{"*"}}}
	errs := Validate(cfg)
	assertFieldError(t, errs, "eventBasedHardware[0].vendor")
}

// TestValidate_EventBasedHardware_NonCanonicalVendor pins the contract:
// short forms ("Dell", "dell", "Hewlett Packard Enterprise") are
// rejected because metal-operator only ever writes the canonical
// bmc.Manufacturer constants ("Dell Inc.", "HPE", "Lenovo",
// "Supermicro") to BMC.Status.Manufacturer. Accepting other variants
// here would silently leave those rows un-matchable at runtime.
func TestValidate_EventBasedHardware_NonCanonicalVendor(t *testing.T) {
	for _, vendor := range []string{"Dell", "dell", "DELL", "Hewlett Packard Enterprise", "lenovo", " Dell Inc. ", "HPE "} {
		t.Run(vendor, func(t *testing.T) {
			cfg := minimalValid()
			cfg.EventBasedHardware = []HardwareMatch{{Vendor: vendor, Models: []string{"*"}}}
			errs := Validate(cfg)
			assertFieldError(t, errs, "eventBasedHardware[0].vendor")
		})
	}
}

func TestValidate_EventBasedHardware_EmptyModels(t *testing.T) {
	cfg := minimalValid()
	cfg.EventBasedHardware = []HardwareMatch{{Vendor: vendorDellInc, Models: []string{}}}
	errs := Validate(cfg)
	assertFieldError(t, errs, "eventBasedHardware[0].models")
}

func TestValidate_EventBasedHardware_MultipleRowsSameVendor(t *testing.T) {
	cfg := minimalValid()
	cfg.EventBasedHardware = []HardwareMatch{
		{Vendor: vendorDellInc, Models: []string{"PowerEdge R750"}},
		{Vendor: vendorDellInc, Models: []string{"PowerEdge R640"}},
	}
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("multiple rows for same vendor should be allowed, got: %v", errs)
	}
}

func TestValidate_EventBasedHardware_BadSemver(t *testing.T) {
	cfg := minimalValid()
	cfg.EventBasedHardware = []HardwareMatch{{Vendor: vendorDellInc, Models: []string{"*"}, MinFirmware: "not-semver"}}
	errs := Validate(cfg)
	assertFieldError(t, errs, "eventBasedHardware[0].minFirmware")
}

func TestValidate_EventBasedHardware_ValidSemver(t *testing.T) {
	cfg := minimalValid()
	cfg.EventBasedHardware = []HardwareMatch{{Vendor: vendorDellInc, Models: []string{"*"}, MinFirmware: firmware5_10_0}}
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("unexpected errors with valid semver: %v", errs)
	}
}

func TestValidate_EventBasedHardware_TestMessageIdRequiredWhenIntervalSet(t *testing.T) {
	cfg := minimalValid()
	cfg.TestEventInterval = time.Minute
	cfg.EventBasedHardware = []HardwareMatch{{Vendor: vendorDellInc, Models: []string{"*"}}}
	errs := Validate(cfg)
	assertFieldError(t, errs, "testMessageId")
}

func TestValidate_EventBasedHardware_TestMessageIdNotRequiredWithoutInterval(t *testing.T) {
	cfg := minimalValid()
	cfg.EventBasedHardware = []HardwareMatch{{Vendor: vendorDellInc, Models: []string{"*"}}}
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("unexpected errors without testEventInterval: %v", errs)
	}
}

func TestValidate_TestEventInterval_Range(t *testing.T) {
	cases := []struct {
		name    string
		val     time.Duration
		wantErr bool
	}{
		{"zero disables, no error", 0, false},
		{"below min (30s)", 30 * time.Second, true},
		{"at min (1m)", time.Minute, false},
		{"valid (1h)", time.Hour, false},
		{"at max (7d)", 7 * 24 * time.Hour, false},
		{"above max (8d)", 8 * 24 * time.Hour, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := minimalValid()
			cfg.TestEventInterval = tc.val
			errs := Validate(cfg)
			if tc.wantErr {
				assertFieldError(t, errs, "testEventInterval")
			} else if len(errs) != 0 {
				t.Errorf("unexpected errors: %v", errs)
			}
		})
	}
}

func TestValidate_TestMessageId_RequiredWhenIntervalSet(t *testing.T) {
	cfg := minimalValid()
	cfg.TestEventInterval = time.Hour
	cfg.EventBasedHardware = []HardwareMatch{{Vendor: vendorDellInc, Models: []string{"*"}}}
	errs := Validate(cfg)
	assertFieldError(t, errs, "testMessageId")
}

func TestValidate_TestMessageId_NotRequiredWhenIntervalZero(t *testing.T) {
	cfg := minimalValid()
	cfg.EventBasedHardware = []HardwareMatch{{Vendor: vendorDellInc, Models: []string{"*"}}}
	if errs := Validate(cfg); len(errs) != 0 {
		t.Errorf("testMessageId should not be required when interval is zero, got: %v", errs)
	}
}

func TestValidate_TestMessageId_AnyNonEmptyAccepted(t *testing.T) {
	// Format validation is deliberately omitted: vendors use different
	// conventions (e.g. iDRAC accepts "SYS1000", HPE uses full registry
	// paths). The BMC will reject a bad value at runtime.
	for _, id := range []string{"SYS1000", "SYS.1.0.SYS1000", "iDRAC.2.9.RAC0182", "Base.1.12.GeneralError", "anything"} {
		t.Run(id, func(t *testing.T) {
			cfg := minimalValid()
			cfg.TestEventInterval = time.Hour
			cfg.EventBasedHardware = []HardwareMatch{{Vendor: vendorDellInc, Models: []string{"*"}, TestMessageId: id}}
			if errs := Validate(cfg); len(errs) != 0 {
				t.Errorf("non-empty testMessageId %q should be accepted, got: %v", id, errs)
			}
		})
	}
}

func TestValidate_TestEventTimeout_Range(t *testing.T) {
	cases := []struct {
		name    string
		val     time.Duration
		wantErr bool
	}{
		{"zero defaults, no error", 0, false},
		{"below min (1s)", time.Second, true},
		{"at min (5s)", 5 * time.Second, false},
		{"valid (30s)", 30 * time.Second, false},
		{"at max (5m)", 5 * time.Minute, false},
		{"above max (10m)", 10 * time.Minute, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := minimalValid()
			cfg.TestEventTimeout = tc.val
			errs := Validate(cfg)
			if tc.wantErr {
				assertFieldError(t, errs, "testEventTimeout")
			} else if len(errs) != 0 {
				t.Errorf("unexpected errors: %v", errs)
			}
		})
	}
}

func TestParse_UnknownField(t *testing.T) {
	raw := []byte(`
apiVersion: telemetry.metal.ironcore.dev/v1alpha1
subscriptionReconcileInterval: 30s
unknownTopLevel: oops
`)
	_, errs := Parse(raw)
	if len(errs) == 0 {
		t.Fatal("expected strict-YAML error for unknown field")
	}
}

func TestParse_RoundTrip(t *testing.T) {
	raw := []byte(`
apiVersion: telemetry.metal.ironcore.dev/v1alpha1
subscriptionReconcileInterval: 1m
perBMCTimeout: 25s
eventBasedHardware:
  - vendor: Dell Inc.
    models: ["*"]
    minFirmware: 5.10.0
`)
	cfg, errs := Parse(raw)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if cfg.SubscriptionReconcileInterval != time.Minute {
		t.Errorf("subscriptionReconcileInterval: got %v, want %v", cfg.SubscriptionReconcileInterval, time.Minute)
	}
	if cfg.PerBMCTimeout != 25*time.Second {
		t.Errorf("perBMCTimeout: got %v, want 25s", cfg.PerBMCTimeout)
	}
	if len(cfg.EventBasedHardware) != 1 {
		t.Errorf("eventBasedHardware: got %d, want 1", len(cfg.EventBasedHardware))
	}
}

// assertFieldError checks that at least one error in errs mentions the given field path.
func assertFieldError(t *testing.T, errs field.ErrorList, fieldPath string) {
	t.Helper()
	if len(errs) == 0 {
		t.Fatalf("expected error for field %q, got none", fieldPath)
	}
	for _, e := range errs {
		if strings.Contains(e.Field, fieldPath) || strings.Contains(e.Error(), fieldPath) {
			return
		}
	}
	t.Errorf("expected error mentioning field %q; got:\n%v", fieldPath, errs)
}
