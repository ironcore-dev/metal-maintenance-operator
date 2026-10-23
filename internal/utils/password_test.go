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
			for range iterations {
				pw, err := GenerateHTMLSafePassword(m, tc.maxLen, tc.minLen, fallback)
				if err != nil {
					t.Fatalf("manufacturer=%s case=%s: unexpected error: %v", m, tc.name, err)
				}
				runes := len([]rune(pw))
				if runes < floor {
					t.Fatalf("manufacturer=%s case=%s: length %d below floor %d (pw=%q)", m, tc.name, runes, floor, pw)
				}
				if tc.maxLen > 0 {
					if esc := EscapedLen(pw); esc > int(tc.maxLen) {
						t.Fatalf("manufacturer=%s case=%s: escaped length %d exceeds maxLen %d (pw=%q)", m, tc.name, esc, tc.maxLen, pw)
					}
					if runes > int(tc.maxLen) {
						t.Fatalf("manufacturer=%s case=%s: raw length %d exceeds maxLen %d (pw=%q)", m, tc.name, runes, tc.maxLen, pw)
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
		name    string
		pw      string
		maxLen  uint
		minLen  uint
		wantErr bool
	}{
		{"within bounds", "abcdefgh", 30, 8, false},
		{"too short", "abc", 30, 8, true},
		{"too long raw", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 20, 8, true}, // 32 chars > 20
		{"escaped overflow", "aaaaaaaaaaaaaaaaaa&", 20, 8, true},          // 18 + "&"(6) = 24 > 20
		{"escaped within", "aaaaaaaaaaaa&", 20, 8, false},                 // 12 + 6 = 18 <= 20
		{"no max skips max check", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 0, 8, false},
		{"no min skips min check", "ab", 30, 0, false},
		{"both zero", "anything", 0, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePasswordLength(tc.pw, tc.maxLen, tc.minLen)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidatePasswordLength(%q, %d, %d) = nil, want error", tc.pw, tc.maxLen, tc.minLen)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidatePasswordLength(%q, %d, %d) = %v, want nil", tc.pw, tc.maxLen, tc.minLen, err)
			}
		})
	}
}
