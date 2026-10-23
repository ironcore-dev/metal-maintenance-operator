// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package subscriptions_test

import (
	"testing"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/subscriptions"
)

// excludeCfg returns a Config preloaded with the given exclude rows.
func excludeCfg(rows ...subscriptions.ExcludeRule) *subscriptions.Config {
	return &subscriptions.Config{ExcludeHardware: rows}
}

// overrideCfg returns a Config preloaded with the given override rows.
func overrideCfg(rows ...subscriptions.TestOverride) *subscriptions.Config {
	return &subscriptions.Config{TestEventOverrides: rows}
}

// -- IsExcluded --

func TestIsExcluded_NilConfig_NotExcluded(t *testing.T) {
	if subscriptions.IsExcluded(subscriptions.BMCRef{Name: testBMCName, Vendor: vendorDellInc}, nil) {
		t.Error("nil cfg: want not excluded")
	}
}

func TestIsExcluded_NoRows_NotExcluded(t *testing.T) {
	cfg := &subscriptions.Config{}
	if subscriptions.IsExcluded(subscriptions.BMCRef{Vendor: vendorDellInc, Model: modelR650}, cfg) {
		t.Error("empty cfg: want not excluded")
	}
}

func TestIsExcluded_EmptyVendor_NotExcluded(t *testing.T) {
	// A BMC whose Status.Manufacturer hasn't been populated yet must NOT be
	// excluded — we haven't identified it yet, so the probe decides.
	cfg := excludeCfg(subscriptions.ExcludeRule{Vendor: vendorDellInc, Models: []string{"*"}})
	if subscriptions.IsExcluded(subscriptions.BMCRef{Vendor: ""}, cfg) {
		t.Error("empty vendor: want not excluded (let probe decide)")
	}
}

// -- vendor matching --

func TestIsExcluded_VendorMatch_Canonical(t *testing.T) {
	cfg := excludeCfg(subscriptions.ExcludeRule{
		Vendor: vendorDellInc,
		Models: []string{"*"},
	})
	t.Run("match/Dell Inc.", func(t *testing.T) {
		if !subscriptions.IsExcluded(subscriptions.BMCRef{Vendor: vendorDellInc, Model: modelR650}, cfg) {
			t.Error("canonical vendor: want excluded")
		}
	})
	// Non-canonical reported values must NOT match.
	for _, v := range []string{"Dell", "DELL", "dell", "  Dell Inc.  "} {
		t.Run("no-match/"+v, func(t *testing.T) {
			if subscriptions.IsExcluded(subscriptions.BMCRef{Vendor: v, Model: modelR650}, cfg) {
				t.Errorf("vendor %q excluded: want not excluded (non-canonical)", v)
			}
		})
	}
}

func TestIsExcluded_VendorMismatch_NotExcluded(t *testing.T) {
	cfg := excludeCfg(subscriptions.ExcludeRule{
		Vendor: vendorDellInc,
		Models: []string{"*"},
	})
	if subscriptions.IsExcluded(subscriptions.BMCRef{Vendor: vendorHPE, Model: "ProLiant"}, cfg) {
		t.Error("vendor mismatch: want not excluded")
	}
}

// -- model matching --

func TestIsExcluded_WildcardModel_ExcludesAny(t *testing.T) {
	cfg := excludeCfg(subscriptions.ExcludeRule{
		Vendor: vendorDellInc,
		Models: []string{"*"},
	})
	for _, m := range []string{modelR650, modelPowerEdgeR750, "anything"} {
		t.Run(m, func(t *testing.T) {
			if !subscriptions.IsExcluded(subscriptions.BMCRef{Vendor: vendorDellInc, Model: m}, cfg) {
				t.Errorf("model %q: want excluded (wildcard)", m)
			}
		})
	}
}

func TestIsExcluded_SpecificModel_MatchesExact(t *testing.T) {
	cfg := excludeCfg(subscriptions.ExcludeRule{
		Vendor: vendorDellInc,
		Models: []string{"PowerEdge R650", modelPowerEdgeR750},
	})
	cases := []struct {
		model        string
		wantExcluded bool
	}{
		{"PowerEdge R650", true},
		{modelPowerEdgeR750, true},
		{"powerEDGE r650", true},     // case-insensitive
		{"  PowerEdge R650  ", true}, // whitespace tolerant
		{"PowerEdge R840", false},    // not in list
		{modelR650, false},           // partial match → no
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			got := subscriptions.IsExcluded(subscriptions.BMCRef{Vendor: vendorDellInc, Model: tc.model}, cfg)
			if tc.wantExcluded && !got {
				t.Errorf("model %q: want excluded", tc.model)
			}
			if !tc.wantExcluded && got {
				t.Errorf("model %q: want not excluded", tc.model)
			}
		})
	}
}

func TestIsExcluded_EmptyModelsList_NotExcluded(t *testing.T) {
	// Defensive: validation rejects empty Models, but the function must not panic.
	cfg := excludeCfg(subscriptions.ExcludeRule{Vendor: vendorDellInc, Models: []string{}})
	if subscriptions.IsExcluded(subscriptions.BMCRef{Vendor: vendorDellInc, Model: modelR650}, cfg) {
		t.Error("empty models: want not excluded")
	}
}

// -- firmware gating (MinFirmware on ExcludeRule means "exclude when firmware is BELOW this") --

func TestIsExcluded_MinFirmwareUnset_AlwaysExcluded(t *testing.T) {
	cfg := excludeCfg(subscriptions.ExcludeRule{
		Vendor: vendorDellInc,
		Models: []string{"*"},
		// MinFirmware omitted: exclude for all firmware versions
	})
	got := subscriptions.IsExcluded(subscriptions.BMCRef{
		Vendor: vendorDellInc, Model: modelR650, FirmwareVersion: "garbage",
	}, cfg)
	if !got {
		t.Error("want excluded (no firmware constraint means always excluded)")
	}
}

func TestIsExcluded_FirmwareGate(t *testing.T) {
	// MinFirmware="5.10.0" means "exclude firmware BELOW 5.10.0".
	// Firmware at or above the floor is NOT excluded.
	cfg := excludeCfg(subscriptions.ExcludeRule{
		Vendor:      vendorDellInc,
		Models:      []string{"*"},
		MinFirmware: firmware5_10_0,
	})
	cases := []struct {
		firmware     string
		wantExcluded bool
		why          string
	}{
		{firmware5_10_0, false, "exact match — at floor, not excluded"},
		{"5.10.5", false, "patch above — not excluded"},
		{"6.0.0", false, "major above — not excluded"},
		{"5.9.99", true, "patch below — excluded"},
		{"4.99.99", true, "major below — excluded"},
		{"", true, "missing firmware — excluded (safe default)"},
		{"not-semver", true, "unparseable — excluded (safe default)"},
		{"5.10", false, "two-component semver (tolerant parse) — not excluded"},
	}
	for _, tc := range cases {
		t.Run(tc.why, func(t *testing.T) {
			got := subscriptions.IsExcluded(subscriptions.BMCRef{
				Vendor: vendorDellInc, Model: modelR650, FirmwareVersion: tc.firmware,
			}, cfg)
			if tc.wantExcluded && !got {
				t.Errorf("firmware %q: want excluded (%s)", tc.firmware, tc.why)
			}
			if !tc.wantExcluded && got {
				t.Errorf("firmware %q: want not excluded (%s)", tc.firmware, tc.why)
			}
		})
	}
}

// -- first-match-wins --

func TestIsExcluded_FirstMatchingRowWins(t *testing.T) {
	cfg := excludeCfg(
		subscriptions.ExcludeRule{Vendor: vendorDellInc, Models: []string{"*"}},
		subscriptions.ExcludeRule{Vendor: vendorDellInc, Models: []string{modelR650}, MinFirmware: "5.0.0"},
	)
	if !subscriptions.IsExcluded(subscriptions.BMCRef{
		Vendor: vendorDellInc, Model: modelR650, FirmwareVersion: "6.0.0",
	}, cfg) {
		t.Error("want excluded (first matching row should win)")
	}
}

func TestIsExcluded_LaterRow_StillExcludes(t *testing.T) {
	cfg := excludeCfg(
		subscriptions.ExcludeRule{Vendor: vendorDellInc, Models: []string{"*"}},
		subscriptions.ExcludeRule{Vendor: vendorHPE, Models: []string{"*"}},
	)
	if !subscriptions.IsExcluded(subscriptions.BMCRef{Vendor: vendorHPE, Model: "ProLiant"}, cfg) {
		t.Error("want excluded (later row matches)")
	}
}

// -- TestOverrideFor --

func TestTestOverrideFor_NilConfig_Nil(t *testing.T) {
	got := subscriptions.TestOverrideFor(subscriptions.BMCRef{Vendor: vendorDellInc}, nil)
	if got != nil {
		t.Errorf("nil cfg: want nil, got %+v", got)
	}
}

func TestTestOverrideFor_NoRows_Nil(t *testing.T) {
	got := subscriptions.TestOverrideFor(subscriptions.BMCRef{Vendor: vendorDellInc}, &subscriptions.Config{})
	if got != nil {
		t.Errorf("empty cfg: want nil, got %+v", got)
	}
}

func TestTestOverrideFor_VendorMatch_ReturnsOverride(t *testing.T) {
	cfg := overrideCfg(subscriptions.TestOverride{
		Vendor:        vendorDellInc,
		Models:        []string{"*"},
		TestMessageId: testMessageId,
	})
	got := subscriptions.TestOverrideFor(subscriptions.BMCRef{Vendor: vendorDellInc, Model: modelR650}, cfg)
	if got == nil {
		t.Fatal("vendor match: want non-nil override")
	}
	if got.TestMessageId != testMessageId {
		t.Errorf("TestMessageId: got %q, want %q", got.TestMessageId, testMessageId)
	}
}

func TestTestOverrideFor_VendorMismatch_Nil(t *testing.T) {
	cfg := overrideCfg(subscriptions.TestOverride{Vendor: vendorDellInc, Models: []string{"*"}})
	got := subscriptions.TestOverrideFor(subscriptions.BMCRef{Vendor: vendorHPE, Model: "ProLiant"}, cfg)
	if got != nil {
		t.Errorf("vendor mismatch: want nil, got %+v", got)
	}
}

func TestTestOverrideFor_WildcardModel_MatchesAny(t *testing.T) {
	cfg := overrideCfg(subscriptions.TestOverride{Vendor: vendorDellInc, Models: []string{"*"}, TestMessageId: testMessageId})
	for _, m := range []string{modelR650, modelPowerEdgeR750, "anything"} {
		t.Run(m, func(t *testing.T) {
			got := subscriptions.TestOverrideFor(subscriptions.BMCRef{Vendor: vendorDellInc, Model: m}, cfg)
			if got == nil {
				t.Errorf("model %q: want non-nil (wildcard)", m)
			}
		})
	}
}

func TestTestOverrideFor_SpecificModel_MatchesExact(t *testing.T) {
	cfg := overrideCfg(subscriptions.TestOverride{
		Vendor: vendorDellInc,
		Models: []string{"PowerEdge R650", modelPowerEdgeR750},
	})
	cases := []struct {
		model   string
		wantNil bool
	}{
		{"PowerEdge R650", false},
		{modelPowerEdgeR750, false},
		{"powerEDGE r650", false},     // case-insensitive
		{"  PowerEdge R650  ", false}, // whitespace tolerant
		{"PowerEdge R840", true},      // not in list
		{modelR650, true},             // partial match → no
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			got := subscriptions.TestOverrideFor(subscriptions.BMCRef{Vendor: vendorDellInc, Model: tc.model}, cfg)
			if tc.wantNil && got != nil {
				t.Errorf("model %q: got non-nil, want nil", tc.model)
			}
			if !tc.wantNil && got == nil {
				t.Errorf("model %q: got nil, want non-nil", tc.model)
			}
		})
	}
}

func TestTestOverrideFor_FirstMatchWins(t *testing.T) {
	first := subscriptions.TestOverride{
		Vendor:        vendorDellInc,
		Models:        []string{"*"},
		TestMessageId: "first.1.0.First",
	}
	second := subscriptions.TestOverride{
		Vendor:        vendorDellInc,
		Models:        []string{modelR650},
		TestMessageId: "second.1.0.Second",
	}
	cfg := overrideCfg(first, second)
	got := subscriptions.TestOverrideFor(subscriptions.BMCRef{Vendor: vendorDellInc, Model: modelR650}, cfg)
	if got == nil {
		t.Fatal("want non-nil override")
	}
	if got.TestMessageId != "first.1.0.First" {
		t.Errorf("first-match-wins: got %q, want first row", got.TestMessageId)
	}
}
