// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"testing"

	"github.com/ironcore-dev/metal-operator/bmc"
)

func TestEscapedLen(t *testing.T) {
	cases := map[string]int{
		"ab":    2,
		"a&b":   8, // '&' -> "&" (6 chars) + 'a' + 'b'
		"a&&b":  14,
		"plain": 5,
	}
	for in, want := range cases {
		if got := EscapedLen(in); got != want {
			t.Errorf("EscapedLen(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestGenerateHTMLSafePassword(t *testing.T) {
	manufacturers := []bmc.Manufacturer{
		bmc.ManufacturerDell,
		bmc.ManufacturerHPE,
		bmc.ManufacturerLenovo,
		bmc.Manufacturer("Unknown"),
	}
	cases := []struct {
		name   string
		maxLen uint
		minLen uint
	}{
		{"idrac", 127, 8},
		{"mock", 30, 8},
		{"small", 20, 8},
		{"no-max", 0, 0},
		{"high-min", 30, 20},
	}

	const iterations = 1000
	const fallback = 20

	for _, m := range manufacturers {
		for _, tc := range cases {
			floor := max(minPasswordFloor, int(tc.minLen))
			// Mirror the production vendor cap: Dell is clamped to 20 regardless of
			// the reported max (including when none is reported).
			effMax := tc.maxLen
			if m == bmc.ManufacturerDell {
				if effMax == 0 || effMax > 20 {
					effMax = 20
				}
			}
			for range iterations {
				pw, err := GenerateHTMLSafePassword(m, tc.maxLen, tc.minLen, fallback)
				if err != nil {
					t.Fatalf("manufacturer=%s case=%s: unexpected error: %v", m, tc.name, err)
				}
				runes := len([]rune(pw))
				if runes < floor {
					t.Fatalf("manufacturer=%s case=%s: length %d below floor %d (pw=%q)", m, tc.name, runes, floor, pw)
				}
				if effMax > 0 {
					if esc := EscapedLen(pw); esc > int(effMax) {
						t.Fatalf("manufacturer=%s case=%s: escaped length %d exceeds effective maxLen %d (pw=%q)", m, tc.name, esc, effMax, pw)
					}
					if runes > int(effMax) {
						t.Fatalf("manufacturer=%s case=%s: raw length %d exceeds effective maxLen %d (pw=%q)", m, tc.name, runes, effMax, pw)
					}
				} else {
					want := max(fallback, floor)
					if runes != want {
						t.Fatalf("manufacturer=%s case=%s: no-max length %d, want %d (pw=%q)", m, tc.name, runes, want, pw)
					}
				}
			}
		}
	}
}

func TestGenerateHTMLSafePasswordMinExceedsMax(t *testing.T) {
	// Firmware reporting a minimum larger than its maximum is nonsensical; we must
	// error rather than emit an out-of-range password.
	if _, err := GenerateHTMLSafePassword(bmc.ManufacturerDell, 8, 20, 20); err == nil {
		t.Fatal("expected error when MinPasswordLength exceeds MaxPasswordLength, got nil")
	}
}

func TestValidatePasswordLength(t *testing.T) {
	cases := []struct {
		name         string
		pw           string
		maxLen       uint
		minLen       uint
		manufacturer bmc.Manufacturer
		wantErr      bool
	}{
		// Dell uses HTML-escaped length for max check.
		{"dell within bounds", "abcdefgh", 30, 8, bmc.ManufacturerDell, false},
		{"dell too short", "abc", 30, 8, bmc.ManufacturerDell, true},
		{"dell too long raw", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 20, 8, bmc.ManufacturerDell, true}, // 32 chars > 20
		{"dell escaped overflow", "aaaaaaaaaaaaaaaaaa&", 20, 8, bmc.ManufacturerDell, true},          // 18 + "&"(6) = 24 > 20
		{"dell escaped within", "aaaaaaaaaaaa&", 20, 8, bmc.ManufacturerDell, false},                 // 12 + 6 = 18 <= 20
		// Non-Dell uses raw rune count for max check — '&' does not inflate.
		{"lenovo escaped no overflow", "aaaaaaaaaaaaaaaaaa&", 20, 8, bmc.ManufacturerLenovo, false}, // 19 runes <= 20
		{"lenovo too long raw", "aaaaaaaaaaaaaaaaaaaaaaa", 20, 8, bmc.ManufacturerLenovo, true},     // 23 runes > 20
		{"hpe escaped no overflow", "aaaaaaaaaaaaaaaaaa&", 20, 8, bmc.ManufacturerHPE, false},       // 19 runes <= 20
		// Common cases. Use a non-capped vendor (Lenovo) for the "no max" case:
		// Dell carries a vendor cap (20) that applies even when maxLen==0.
		{"no max skips max check", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 0, 8, bmc.ManufacturerLenovo, false},
		{"no min skips min check", "ab", 30, 0, bmc.ManufacturerDell, false},
		{"both zero non-capped vendor", "anything", 0, 0, bmc.ManufacturerLenovo, false},
		// Dell vendor cap (20) is enforced regardless of the reported maxLen, even
		// when the BMC over-reports or reports nothing. These guard the real bug:
		// iDRAC reporting MaxPasswordLength=122 must not permit a 114-char password.
		{"dell cap enforced when over-reported", "aaaaaaaaaaaaaaaaaaaaaaaaa", 122, 8, bmc.ManufacturerDell, true}, // 25 > cap 20
		{"dell cap enforced when unreported", "aaaaaaaaaaaaaaaaaaaaaaaaa", 0, 8, bmc.ManufacturerDell, true},      // 25 > cap 20
		{"dell at cap boundary", "aaaaaaaaaaaaaaaaaaaa", 122, 8, bmc.ManufacturerDell, false},                     // 20 == cap 20
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePasswordLength(tc.pw, tc.maxLen, tc.minLen, tc.manufacturer)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidatePasswordLength(%q, %d, %d, %s) = nil, want error", tc.pw, tc.maxLen, tc.minLen, tc.manufacturer)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidatePasswordLength(%q, %d, %d, %s) = %v, want nil", tc.pw, tc.maxLen, tc.minLen, tc.manufacturer, err)
			}
		})
	}
}

func TestNormalizeManufacturer(t *testing.T) {
	cases := map[string]bmc.Manufacturer{
		"Dell":                       bmc.ManufacturerDell,
		"dell":                       bmc.ManufacturerDell,
		"DELL":                       bmc.ManufacturerDell,
		"Dell Inc.":                  bmc.ManufacturerDell,
		"  Dell Inc.  ":              bmc.ManufacturerDell,
		"Lenovo":                     bmc.ManufacturerLenovo,
		"lenovo":                     bmc.ManufacturerLenovo,
		"HPE":                        bmc.ManufacturerHPE,
		"Hewlett Packard Enterprise": bmc.ManufacturerHPE,
		"Supermicro":                 bmc.ManufacturerSupermicro,
		"Fsas":                       bmc.ManufacturerFujitsu,
		"Fujitsu":                    bmc.ManufacturerFujitsu,
		"BULL":                       bmc.ManufacturerBull,
		// Unknown and empty are returned unchanged so the generator's own default applies.
		"Acme": bmc.Manufacturer("Acme"),
		"":     bmc.Manufacturer(""),
	}
	for in, want := range cases {
		if got := NormalizeManufacturer(in); got != want {
			t.Errorf("NormalizeManufacturer(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestDellVariantGeneratesSafePassword guards the real-world bug: a BMC reporting
// "Dell" (not the exact constant "Dell Inc.") must still produce a Dell-safe
// password — one that excludes the generic "default" charset character '/', which
// iDRAC rejects, and whose length honours the Dell vendor cap even when iDRAC
// over-reports MaxPasswordLength (the stuck credential was 114 chars from a
// reported max of 122).
func TestDellVariantGeneratesSafePassword(t *testing.T) {
	const (
		idracReportedMax = 122 // iDRAC over-reports; must be clamped to the Dell cap
		dellCap          = 20
		idracMin         = 8
		fallback         = 20
		iterations       = 2000
	)
	m := NormalizeManufacturer("Dell")
	if m != bmc.ManufacturerDell {
		t.Fatalf("NormalizeManufacturer(\"Dell\") = %q, want %q", m, bmc.ManufacturerDell)
	}
	for range iterations {
		pw, err := GenerateHTMLSafePassword(m, idracReportedMax, idracMin, fallback)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, c := range pw {
			if c == '/' {
				t.Fatalf("Dell password must not contain '/': %q", pw)
			}
		}
		if esc := EscapedLen(pw); esc > dellCap {
			t.Fatalf("escaped length %d exceeds Dell cap %d (reported max was %d): %q", esc, dellCap, idracReportedMax, pw)
		}
	}
}

func TestEffectiveMaxLength(t *testing.T) {
	cap15 := uint(15)
	cases := []struct {
		maxLen       uint
		manufacturer bmc.Manufacturer
		policy       PasswordPolicy
		want         uint
	}{
		{122, bmc.ManufacturerDell, PasswordPolicy{}, 20}, // over-reported -> built-in cap
		{0, bmc.ManufacturerDell, PasswordPolicy{}, 20},   // unreported -> cap becomes the max
		{16, bmc.ManufacturerDell, PasswordPolicy{}, 16},  // reported below cap -> honoured
		{122, bmc.ManufacturerLenovo, PasswordPolicy{}, 122},
		{0, bmc.ManufacturerLenovo, PasswordPolicy{}, 0},
		{40, bmc.Manufacturer("Acme"), PasswordPolicy{}, 40},
		// Policy override takes precedence over the built-in cap and over the reported value.
		{122, bmc.ManufacturerDell, PasswordPolicy{MaxLengthCap: &cap15}, 15},
		{122, bmc.ManufacturerLenovo, PasswordPolicy{MaxLengthCap: &cap15}, 15}, // cap an otherwise-uncapped vendor
		{10, bmc.ManufacturerDell, PasswordPolicy{MaxLengthCap: &cap15}, 10},    // reported below override cap -> honoured
	}
	for _, tc := range cases {
		if got := effectiveMaxLength(tc.maxLen, tc.manufacturer, tc.policy); got != tc.want {
			t.Errorf("effectiveMaxLength(%d, %q, %+v) = %d, want %d", tc.maxLen, tc.manufacturer, tc.policy, got, tc.want)
		}
	}
}

func TestGenerateHTMLSafePasswordWithPolicy(t *testing.T) {
	const iterations = 2000
	allowed := "!#$%*.?-@_" // excludes '/', backtick, '^', etc.
	cap := uint(16)
	fb := uint(24)

	t.Run("SpecialChars restricts output", func(t *testing.T) {
		policy := PasswordPolicy{SpecialChars: &allowed}
		// Use the generic default vendor so the generator's own charset includes
		// characters outside `allowed`, forcing the filter to do real work.
		for range iterations {
			pw, err := GenerateHTMLSafePasswordWithPolicy(bmc.Manufacturer("Acme"), 40, 8, 20, policy)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			for _, c := range pw {
				isAlnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
				if !isAlnum && !containsRune(allowed, c) {
					t.Fatalf("password %q contains disallowed special char %q", pw, c)
				}
			}
		}
	})

	t.Run("MaxLengthCap override applies to an otherwise-uncapped vendor", func(t *testing.T) {
		policy := PasswordPolicy{MaxLengthCap: &cap}
		for range iterations {
			pw, err := GenerateHTMLSafePasswordWithPolicy(bmc.ManufacturerLenovo, 122, 8, 20, policy)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if runes := len([]rune(pw)); runes > int(cap) {
				t.Fatalf("length %d exceeds policy cap %d: %q", runes, cap, pw)
			}
		}
	})

	t.Run("FallbackLength override applies when no max reported", func(t *testing.T) {
		policy := PasswordPolicy{FallbackLength: &fb}
		// Lenovo is uncapped, maxLen 0 -> fallback length used verbatim.
		pw, err := GenerateHTMLSafePasswordWithPolicy(bmc.ManufacturerLenovo, 0, 8, 20, policy)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if runes := len([]rune(pw)); runes != int(fb) {
			t.Fatalf("fallback length %d, want %d: %q", runes, fb, pw)
		}
	})
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}
