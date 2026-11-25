// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"github.com/ironcore-dev/metal-operator/bmc"
)

const (
	// passwordMargin is the number of characters initially subtracted from a BMC's
	// reported MaxPasswordLength when choosing the generation length. It leaves room
	// for the HTML-escaping growth of special characters: only '&' can occur in a
	// generated password and it expands 1 -> 6 (`&`), i.e. +5 each. The margin
	// is a starting estimate; GenerateHTMLSafePassword shrinks the length further on
	// any candidate whose escaped form still overflows, so correctness does not depend
	// on this value.
	passwordMargin = 8
	// maxPasswordRetries bounds the regenerate-and-shrink loop.
	maxPasswordRetries = 8
	// minPasswordFloor is the lowest length we will ever request. bmc.GenerateSecurePassword
	// needs at least 4 characters to satisfy its complexity rule; 8 is a safe, common minimum.
	minPasswordFloor = 8
	// defaultPasswordLength is used when the BMC reports no MaxPasswordLength and the
	// caller supplies no fallback.
	defaultPasswordLength = 20
)

// vendorMaxPasswordLength caps the effective maximum password length per vendor,
// regardless of what the BMC's Redfish AccountService reports. Some BMCs
// (notably Dell iDRAC) over-report MaxPasswordLength — e.g. iDRAC reports 122,
// a Redfish/schema buffer size, while the account service actually rejects
// passwords longer than ~20 characters. Trusting the reported value produced an
// over-length credential the BMC silently rejected, leaving the account created
// but unable to authenticate. The cap is applied as min(reported, vendor cap).
// A vendor absent from this map is uncapped (the reported value is used as-is).
// These are the built-in defaults; a BMCUserConfig ConfigMap can override them
// per vendor (see PasswordPolicy).
var vendorMaxPasswordLength = map[bmc.Manufacturer]uint{
	bmc.ManufacturerDell: 20,
}

// PasswordPolicy carries the per-reconcile, optionally config-overridden knobs
// for password generation and validation. The zero value is valid and means
// "use all built-in defaults". Callers (the BMCUser controller) build one from
// the live BMCUserConfig ConfigMap, falling back to built-ins for unset fields.
type PasswordPolicy struct {
	// MaxLengthCap, when non-nil, overrides the built-in per-vendor maximum
	// length cap (vendorMaxPasswordLength) for the manufacturer in play. nil
	// falls back to the built-in cap (or no cap for an uncapped vendor).
	MaxLengthCap *uint

	// SpecialChars, when non-nil, restricts generated passwords to this set of
	// special characters (letters and digits are always allowed): any generated
	// special character outside the set is rejected, forcing regeneration. nil
	// keeps the generator's vendor-default character set.
	SpecialChars *string

	// FallbackLength, when non-nil and > 0, overrides the length used when the
	// BMC reports no MaxPasswordLength. nil falls back to the caller-supplied
	// fallback and ultimately defaultPasswordLength.
	FallbackLength *uint
}

// effectiveMaxLength returns the password length ceiling to honour for a vendor:
// the BMC-reported maxLen, further lowered to the applicable cap when one exists.
// The cap is policy.MaxLengthCap when set, else the built-in per-vendor cap. A
// reported maxLen of 0 (not reported) stays 0 only when there is no cap;
// otherwise the cap becomes the effective maximum.
func effectiveMaxLength(maxLen uint, manufacturer bmc.Manufacturer, policy PasswordPolicy) uint {
	cap, ok := resolveCap(manufacturer, policy)
	if !ok {
		return maxLen
	}
	if maxLen == 0 || maxLen > cap {
		return cap
	}
	return maxLen
}

// resolveCap returns the applicable max-length cap and whether one exists:
// policy override first, then the built-in per-vendor cap.
func resolveCap(manufacturer bmc.Manufacturer, policy PasswordPolicy) (uint, bool) {
	if policy.MaxLengthCap != nil {
		return *policy.MaxLengthCap, true
	}
	cap, ok := vendorMaxPasswordLength[manufacturer]
	return cap, ok
}

// NormalizeManufacturer maps a BMC-reported manufacturer string to the canonical
// bmc.Manufacturer constant used for vendor-specific password handling. The
// metal-operator password generator and length validator match the manufacturer
// against exact constants (e.g. bmc.ManufacturerDell == "Dell Inc."), but BMC
// firmware reports the value inconsistently (e.g. "Dell", "DELL", "Dell Inc.").
// Without normalization a value like "Dell" misses the Dell config and silently
// falls back to the generic character set and length rules, producing credentials
// an iDRAC rejects (wrong special characters, over-length). Matching is
// case-insensitive on a leading token so vendor variants collapse to one constant.
// An unrecognized value is returned unchanged so the generator's own "default"
// fallback still applies.
func NormalizeManufacturer(raw string) bmc.Manufacturer {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case s == "":
		return bmc.Manufacturer(raw)
	case strings.HasPrefix(s, "dell"):
		return bmc.ManufacturerDell
	case strings.HasPrefix(s, "lenovo"):
		return bmc.ManufacturerLenovo
	case strings.HasPrefix(s, "hpe") || strings.HasPrefix(s, "hewlett"):
		return bmc.ManufacturerHPE
	case strings.HasPrefix(s, "supermicro"):
		return bmc.ManufacturerSupermicro
	case strings.HasPrefix(s, "fsas") || strings.HasPrefix(s, "fujitsu"):
		return bmc.ManufacturerFujitsu
	case strings.HasPrefix(s, "bull"):
		return bmc.ManufacturerBull
	default:
		return bmc.Manufacturer(raw)
	}
}

// EscapedLen reports the length of pw as it appears on the wire after Go's
// encoding/json HTML escaping (the encoder gofish uses for Redfish PATCH bodies).
// This mirrors how a BMC that counts the escaped payload (e.g. Dell iDRAC) measures
// a password, so it is the authoritative length to compare against MaxPasswordLength.
func EscapedLen(pw string) int {
	b, err := json.Marshal(pw)
	if err != nil {
		return len(pw)
	}
	// Strip the surrounding quotes added by json.Marshal.
	return len(b) - 2
}

// ValidatePasswordLength checks a password against a BMC's reported length limits
// using the built-in defaults (no config override). See ValidatePasswordLengthWithPolicy.
func ValidatePasswordLength(pw string, maxLen, minLen uint, manufacturer bmc.Manufacturer) error {
	return ValidatePasswordLengthWithPolicy(pw, maxLen, minLen, manufacturer, PasswordPolicy{})
}

// ValidatePasswordLengthWithPolicy checks a password against a BMC's reported length limits.
// For Dell iDRAC, it compares the HTML-escaped length (see EscapedLen) against maxLen,
// matching how iDRAC counts the PATCH payload. For all other manufacturers the raw rune
// length is compared against maxLen (those BMCs do not HTML-escape the payload). The
// min-length check always uses the raw rune count. A zero maxLen or minLen means the
// firmware did not report that bound and the check is skipped. The reported maxLen is
// first lowered to the applicable cap (policy override, else built-in per-vendor cap) so
// an over-reported limit (e.g. iDRAC's 122) is not trusted. It returns a non-nil error
// describing the violation when the password is out of range.
func ValidatePasswordLengthWithPolicy(pw string, maxLen, minLen uint, manufacturer bmc.Manufacturer, policy PasswordPolicy) error {
	maxLen = effectiveMaxLength(maxLen, manufacturer, policy)
	if maxLen > 0 {
		var effective int
		if manufacturer == bmc.ManufacturerDell {
			effective = EscapedLen(pw)
		} else {
			effective = len([]rune(pw))
		}
		if effective > int(maxLen) {
			return fmt.Errorf("password length %d exceeds BMC MaxPasswordLength %d", effective, maxLen)
		}
	}
	if minLen > 0 {
		if runes := len([]rune(pw)); runes < int(minLen) {
			return fmt.Errorf("password length %d is below BMC MinPasswordLength %d", runes, minLen)
		}
	}
	return nil
}

// GenerateHTMLSafePassword generates a password using the built-in defaults (no
// config override). See GenerateHTMLSafePasswordWithPolicy.
func GenerateHTMLSafePassword(manufacturer bmc.Manufacturer, maxLen, minLen uint, fallbackLen int) (string, error) {
	return GenerateHTMLSafePasswordWithPolicy(manufacturer, maxLen, minLen, fallbackLen, PasswordPolicy{})
}

// GenerateHTMLSafePasswordWithPolicy generates a password for the given manufacturer that
// honours the BMC's reported password-length requirements: its HTML-escaped length
// never exceeds the effective max (when > 0) and its length is never below the firmware's
// minLen (nor below an internal floor). A reported maxLen == 0 means the firmware did not
// report a maximum, in which case the policy fallback length, else fallbackLen, else an
// internal default is used. The effective max is the reported value lowered to the
// applicable cap (policy override, else built-in per-vendor cap). When policy.SpecialChars
// is set, any candidate containing a special character outside that set is rejected and
// regenerated. On a candidate whose escaped length overflows it shrinks the requested
// length and regenerates, up to a bounded number of attempts, returning an error rather
// than silently emitting an unusable credential.
func GenerateHTMLSafePasswordWithPolicy(manufacturer bmc.Manufacturer, maxLen, minLen uint, fallbackLen int, policy PasswordPolicy) (string, error) {
	// Lower the reported maximum to the applicable cap so an over-reported limit
	// (e.g. iDRAC reporting 122 while the account service accepts far fewer) does
	// not drive generation of an over-length credential the BMC will reject.
	maxLen = effectiveMaxLength(maxLen, manufacturer, policy)
	floor := max(minPasswordFloor, int(minLen))

	// A policy-supplied fallback length overrides the caller's fallbackLen when
	// the BMC reports no maximum.
	if policy.FallbackLength != nil && *policy.FallbackLength > 0 {
		fallbackLen = int(*policy.FallbackLength)
	}

	var length int
	if maxLen == 0 {
		length = fallbackLen
		if length <= 0 {
			length = defaultPasswordLength
		}
		length = max(length, floor)
	} else {
		if int(maxLen) < floor {
			return "", fmt.Errorf("BMC MaxPasswordLength %d is below the minimum required length %d", maxLen, floor)
		}
		length = max(int(maxLen)-passwordMargin, floor)
	}

	var lastErr error
	for range maxPasswordRetries {
		pw, err := bmc.GenerateSecurePassword(manufacturer, length)
		if err != nil {
			return "", fmt.Errorf("failed to generate secure password: %w", err)
		}
		// Substitute any special character outside a policy-supplied allow-list
		// with a random allowed one (used to exclude characters a given firmware
		// rejects, e.g. '/'). Substitution rather than rejection is required
		// because rejection sampling almost never succeeds when the generator's
		// charset is broader than the allow-list. Complexity is preserved: letters
		// and digits are untouched, and every special character ends up in the
		// allowed set (which the config guarantees is non-empty).
		if policy.SpecialChars != nil {
			pw, err = remapSpecialChars(pw, *policy.SpecialChars)
			if err != nil {
				return "", err
			}
		}
		if maxLen == 0 {
			return pw, nil
		}
		overflow := EscapedLen(pw) - int(maxLen)
		if overflow <= 0 {
			return pw, nil
		}
		lastErr = fmt.Errorf("generated password escaped length %d exceeds MaxPasswordLength %d", EscapedLen(pw), maxLen)
		// Shrink by the measured overflow (at least one char) and retry, but never
		// below the floor. At the floor we keep retrying: a fresh random draw with
		// fewer escapable characters will usually fit within maxLen.
		length = max(length-max(overflow, 1), floor)
	}
	return "", fmt.Errorf("failed to generate a password within MaxPasswordLength %d after %d attempts: %w", maxLen, maxPasswordRetries, lastErr)
}

// remapSpecialChars returns pw with every special (non-alphanumeric) character
// that is absent from allowed replaced by a uniformly-random character drawn
// from allowed. Letters and digits are left untouched. allowed must be
// non-empty (enforced by config validation). Because the generator guarantees at
// least one special character and allowed is non-empty, the result still
// contains a special character from the allowed set, preserving complexity.
func remapSpecialChars(pw, allowed string) (string, error) {
	allowedRunes := []rune(allowed)
	out := []rune(pw)
	for i, c := range out {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		if strings.ContainsRune(allowed, c) {
			continue
		}
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(allowedRunes))))
		if err != nil {
			return "", fmt.Errorf("failed to pick replacement special character: %w", err)
		}
		out[i] = allowedRunes[n.Int64()]
	}
	return string(out), nil
}
