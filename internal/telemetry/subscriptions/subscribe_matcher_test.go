// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package subscriptions_test

import (
	"testing"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/subscriptions"
)

// cfgWith returns a Config preloaded with the given hardware rows.
func cfgWith(rows ...subscriptions.HardwareMatch) *subscriptions.Config {
	return &subscriptions.Config{EventBasedHardware: rows}
}

func TestSubscribeToBMC_NilConfig_IsFalse(t *testing.T) {
	if subscriptions.SubscribeToBMC(subscriptions.BMCRef{Name: testBMCName, Vendor: vendorDellInc}, nil) {
		t.Error("nil cfg matched: want false")
	}
}

func TestSubscribeToBMC_NoRows_IsFalse(t *testing.T) {
	// No eventBasedHardware rows → nothing subscribes. Default-deny.
	cfg := &subscriptions.Config{}
	if subscriptions.SubscribeToBMC(subscriptions.BMCRef{Vendor: vendorDellInc, Model: modelR650}, cfg) {
		t.Error("empty cfg matched: want false")
	}
}

// -- vendor matching --

// TestSubscribeToBMC_VendorMatch_CanonicalExact pins that both sides of
// the comparison are canonical metal-operator bmc.Manufacturer values
// (matched on the ConfigMap side by Validate, and produced by
// metal-operator on the BMC.Status side). Match is plain string equality
// — short forms or case variations do NOT match, because they'd also
// never appear in BMC.Status.Manufacturer.
func TestSubscribeToBMC_VendorMatch_CanonicalExact(t *testing.T) {
	cfg := cfgWith(subscriptions.HardwareMatch{
		Vendor: vendorDellInc,
		Models: []string{"*"},
	})
	t.Run("match/Dell Inc.", func(t *testing.T) {
		if !subscriptions.SubscribeToBMC(subscriptions.BMCRef{Vendor: vendorDellInc, Model: modelR650}, cfg) {
			t.Error("canonical vendor: want true")
		}
	})
	// Non-canonical reported values must NOT match — metal-operator
	// will never write these to BMC.Status.Manufacturer, so accepting
	// them here would diverge from what Validate enforces.
	for _, v := range []string{"Dell", "DELL", "dell", "  Dell Inc.  "} {
		t.Run("no-match/"+v, func(t *testing.T) {
			if subscriptions.SubscribeToBMC(subscriptions.BMCRef{Vendor: v, Model: modelR650}, cfg) {
				t.Errorf("vendor %q matched: want false (non-canonical reported value)", v)
			}
		})
	}
}

func TestSubscribeToBMC_VendorMismatch_IsFalse(t *testing.T) {
	cfg := cfgWith(subscriptions.HardwareMatch{
		Vendor: vendorDellInc,
		Models: []string{"*"},
	})
	if subscriptions.SubscribeToBMC(subscriptions.BMCRef{Vendor: vendorHPE, Model: "ProLiant"}, cfg) {
		t.Error("vendor mismatch matched: want false")
	}
}

func TestSubscribeToBMC_EmptyVendor_NeverMatches(t *testing.T) {
	// A BMC whose Status.Manufacturer hasn't been populated yet must NOT
	// silently match — that would push events at a BMC we can't identify.
	cfg := cfgWith(subscriptions.HardwareMatch{
		Vendor: vendorDellInc,
		Models: []string{"*"},
	})
	if subscriptions.SubscribeToBMC(subscriptions.BMCRef{Vendor: ""}, cfg) {
		t.Error("empty vendor matched: want false")
	}
}

// -- model matching --

func TestSubscribeToBMC_WildcardModel_MatchesAny(t *testing.T) {
	cfg := cfgWith(subscriptions.HardwareMatch{
		Vendor: vendorDellInc,
		Models: []string{"*"},
	})
	for _, m := range []string{modelR650, modelPowerEdgeR750, "anything"} {
		t.Run(m, func(t *testing.T) {
			if !subscriptions.SubscribeToBMC(subscriptions.BMCRef{Vendor: vendorDellInc, Model: m}, cfg) {
				t.Errorf("model %q: want true (wildcard)", m)
			}
		})
	}
}

func TestSubscribeToBMC_SpecificModel_MatchesExact(t *testing.T) {
	cfg := cfgWith(subscriptions.HardwareMatch{
		Vendor: vendorDellInc,
		Models: []string{"PowerEdge R650", modelPowerEdgeR750},
	})
	cases := []struct {
		model string
		want  bool
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
			got := subscriptions.SubscribeToBMC(subscriptions.BMCRef{Vendor: vendorDellInc, Model: tc.model}, cfg)
			if got != tc.want {
				t.Errorf("model %q: got %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}

func TestSubscribeToBMC_EmptyModelsList_DoesNotMatch(t *testing.T) {
	// Defensive: schema validation rejects empty Models, but the function
	// must not panic or wildcard-match if it somehow sees one.
	cfg := cfgWith(subscriptions.HardwareMatch{Vendor: vendorDellInc, Models: []string{}})
	if subscriptions.SubscribeToBMC(subscriptions.BMCRef{Vendor: vendorDellInc, Model: modelR650}, cfg) {
		t.Error("empty models matched: want false")
	}
}

// -- firmware gating --

func TestSubscribeToBMC_MinFirmwareUnset_AlwaysSatisfied(t *testing.T) {
	cfg := cfgWith(subscriptions.HardwareMatch{
		Vendor: vendorDellInc,
		Models: []string{"*"},
		// MinFirmware omitted
	})
	// Even with unparseable firmware, no constraint means match.
	got := subscriptions.SubscribeToBMC(subscriptions.BMCRef{
		Vendor: vendorDellInc, Model: modelR650, FirmwareVersion: "garbage",
	}, cfg)
	if !got {
		t.Error("want true (no firmware constraint)")
	}
}

func TestSubscribeToBMC_FirmwareGate(t *testing.T) {
	cfg := cfgWith(subscriptions.HardwareMatch{
		Vendor:      vendorDellInc,
		Models:      []string{"*"},
		MinFirmware: firmware5_10_0,
	})
	cases := []struct {
		firmware string
		want     bool
		why      string
	}{
		{firmware5_10_0, true, "exact match"},
		{"5.10.5", true, "patch above"},
		{"6.0.0", true, "major above"},
		{"5.9.99", false, "patch below"},
		{"4.99.99", false, "major below"},
		{"", false, "missing firmware"},
		{"not-semver", false, "unparseable"},
		{"5.10", true, "two-component semver (tolerant parse)"},
	}
	for _, tc := range cases {
		t.Run(tc.why, func(t *testing.T) {
			got := subscriptions.SubscribeToBMC(subscriptions.BMCRef{
				Vendor: vendorDellInc, Model: modelR650, FirmwareVersion: tc.firmware,
			}, cfg)
			if got != tc.want {
				t.Errorf("firmware %q: got %v, want %v (%s)", tc.firmware, got, tc.want, tc.why)
			}
		})
	}
}

// -- first-match-wins --

func TestSubscribeToBMC_FirstMatchingRowWins(t *testing.T) {
	// Two rows for the same vendor with different firmware constraints.
	// The first row matches less strictly; it must win even when the
	// second row would also match.
	cfg := cfgWith(
		subscriptions.HardwareMatch{Vendor: vendorDellInc, Models: []string{"*"}},
		subscriptions.HardwareMatch{Vendor: vendorDellInc, Models: []string{modelR650}, MinFirmware: "5.0.0"},
	)
	if !subscriptions.SubscribeToBMC(subscriptions.BMCRef{
		Vendor: vendorDellInc, Model: modelR650, FirmwareVersion: "6.0.0",
	}, cfg) {
		t.Error("want true (first matching row should win)")
	}
}

func TestSubscribeToBMC_LaterRow_StillMatches(t *testing.T) {
	// Earlier row doesn't match (different vendor); later row does.
	cfg := cfgWith(
		subscriptions.HardwareMatch{Vendor: vendorDellInc, Models: []string{"*"}},
		subscriptions.HardwareMatch{Vendor: vendorHPE, Models: []string{"*"}},
	)
	if !subscriptions.SubscribeToBMC(subscriptions.BMCRef{Vendor: vendorHPE, Model: "ProLiant"}, cfg) {
		t.Error("want true (later row matches)")
	}
}
