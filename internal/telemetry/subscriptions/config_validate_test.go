// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
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

// TestValidate_OldEventBasedHardwareRejected pins the hard-break requirement:
// a ConfigMap that still uses the old "eventBasedHardware" key must be
// rejected immediately via strict YAML parsing (unknown field).
func TestValidate_OldEventBasedHardwareRejected(t *testing.T) {
	raw := []byte(`
apiVersion: telemetry.metal.ironcore.dev/v1alpha1
subscriptionReconcileInterval: 30s
eventBasedHardware:
  - vendor: Dell Inc.
    models: ["*"]
    testMessageId: FAN.1.0.FAN0001
`)
	_, errs := Parse(raw)
	if len(errs) == 0 {
		t.Fatal("expected strict-YAML error for unknown field 'eventBasedHardware', got none")
	}
}

// -- excludeHardware --

func TestValidate_ExcludeHardware_MissingVendor(t *testing.T) {
	cfg := minimalValid()
	cfg.ExcludeHardware = []ExcludeRule{{Models: []string{"*"}}}
	errs := Validate(cfg)
	assertFieldError(t, errs, "excludeHardware[0].vendor")
}

func TestValidate_ExcludeHardware_NonCanonicalVendor(t *testing.T) {
	for _, vendor := range []string{"Dell", "dell", "DELL", "Hewlett Packard Enterprise", "lenovo", " Dell Inc. ", "HPE "} {
		t.Run(vendor, func(t *testing.T) {
			cfg := minimalValid()
			cfg.ExcludeHardware = []ExcludeRule{{Vendor: vendor, Models: []string{"*"}}}
			errs := Validate(cfg)
			assertFieldError(t, errs, "excludeHardware[0].vendor")
		})
	}
}

func TestValidate_ExcludeHardware_EmptyModels(t *testing.T) {
	cfg := minimalValid()
	cfg.ExcludeHardware = []ExcludeRule{{Vendor: vendorDellInc, Models: []string{}}}
	errs := Validate(cfg)
	assertFieldError(t, errs, "excludeHardware[0].models")
}

func TestValidate_ExcludeHardware_BadSemver(t *testing.T) {
	cfg := minimalValid()
	cfg.ExcludeHardware = []ExcludeRule{{Vendor: vendorDellInc, Models: []string{"*"}, MinFirmware: "not-semver"}}
	errs := Validate(cfg)
	assertFieldError(t, errs, "excludeHardware[0].minFirmware")
}

func TestValidate_ExcludeHardware_ValidSemver(t *testing.T) {
	cfg := minimalValid()
	cfg.ExcludeHardware = []ExcludeRule{{Vendor: vendorDellInc, Models: []string{"*"}, MinFirmware: firmware5_10_0}}
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("unexpected errors with valid semver: %v", errs)
	}
}

func TestValidate_ExcludeHardware_MultipleRowsSameVendor(t *testing.T) {
	cfg := minimalValid()
	cfg.ExcludeHardware = []ExcludeRule{
		{Vendor: vendorDellInc, Models: []string{"PowerEdge R750"}},
		{Vendor: vendorDellInc, Models: []string{"PowerEdge R640"}},
	}
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("multiple rows for same vendor should be allowed, got: %v", errs)
	}
}

// -- testEventOverrides --

func TestValidate_TestEventOverrides_MissingVendor(t *testing.T) {
	cfg := minimalValid()
	cfg.TestEventOverrides = []TestOverride{{Models: []string{"*"}}}
	errs := Validate(cfg)
	assertFieldError(t, errs, "testEventOverrides[0].vendor")
}

func TestValidate_TestEventOverrides_NonCanonicalVendor(t *testing.T) {
	for _, vendor := range []string{"Dell", "dell", "DELL", " Dell Inc. ", "HPE "} {
		t.Run(vendor, func(t *testing.T) {
			cfg := minimalValid()
			cfg.TestEventOverrides = []TestOverride{{Vendor: vendor, Models: []string{"*"}}}
			errs := Validate(cfg)
			assertFieldError(t, errs, "testEventOverrides[0].vendor")
		})
	}
}

func TestValidate_TestEventOverrides_EmptyModels(t *testing.T) {
	cfg := minimalValid()
	cfg.TestEventOverrides = []TestOverride{{Vendor: vendorDellInc, Models: []string{}}}
	errs := Validate(cfg)
	assertFieldError(t, errs, "testEventOverrides[0].models")
}

func TestValidate_TestEventOverrides_TestMessageIdOptional(t *testing.T) {
	// TestMessageId is now optional — the operator auto-discovers it.
	cfg := minimalValid()
	cfg.TestEventInterval = time.Hour
	cfg.TestEventOverrides = []TestOverride{{Vendor: vendorDellInc, Models: []string{"*"}}}
	if errs := Validate(cfg); len(errs) != 0 {
		t.Errorf("testMessageId should be optional in testEventOverrides, got: %v", errs)
	}
}

func TestValidate_TestEventOverrides_AllFieldsValid(t *testing.T) {
	cfg := minimalValid()
	cfg.TestEventInterval = time.Hour
	cfg.TestEventOverrides = []TestOverride{
		{
			Vendor:                vendorDellInc,
			Models:                []string{"*"},
			TestMessageId:         "FAN.1.0.FAN0001",
			TestSeverity:          "Informational",
			TestOriginOfCondition: "/redfish/v1/Managers/1",
		},
	}
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
}

// -- testEventInterval / testEventTimeout --

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

// -- Parse strict YAML --

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
excludeHardware:
  - vendor: Dell Inc.
    models: ["*"]
    minFirmware: 5.10.0
testEventOverrides:
  - vendor: HPE
    models: ["*"]
    testMessageId: iLO.1.0.TestAlert
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
	if len(cfg.ExcludeHardware) != 1 {
		t.Errorf("excludeHardware: got %d, want 1", len(cfg.ExcludeHardware))
	}
	if len(cfg.TestEventOverrides) != 1 {
		t.Errorf("testEventOverrides: got %d, want 1", len(cfg.TestEventOverrides))
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
