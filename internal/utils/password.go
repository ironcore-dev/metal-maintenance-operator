// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"encoding/json"
	"fmt"

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

// ValidatePasswordLength checks a password against a BMC's reported length limits.
// It compares the HTML-escaped length (see EscapedLen) against maxLen — matching how
// Dell iDRAC counts the PATCH payload — and the raw rune length against minLen. A zero
// maxLen or minLen means the firmware did not report that bound and the check is skipped.
// It returns a non-nil error describing the violation when the password is out of range.
func ValidatePasswordLength(pw string, maxLen, minLen uint) error {
	if maxLen > 0 {
		if escaped := EscapedLen(pw); escaped > int(maxLen) {
			return fmt.Errorf("password escaped length %d exceeds BMC MaxPasswordLength %d", escaped, maxLen)
		}
	}
	if minLen > 0 {
		if runes := len([]rune(pw)); runes < int(minLen) {
			return fmt.Errorf("password length %d is below BMC MinPasswordLength %d", runes, minLen)
		}
	}
	return nil
}

// GenerateHTMLSafePassword generates a password for the given manufacturer that
// honours the BMC's reported password-length requirements: its HTML-escaped length
// never exceeds maxLen (when maxLen > 0) and its length is never below the firmware's
// minLen (nor below an internal floor). maxLen == 0 means the firmware did not report a
// maximum, in which case fallbackLen (or an internal default) is used. On a candidate
// whose escaped length overflows maxLen it shrinks the requested length by the overflow
// and regenerates, up to a bounded number of attempts, returning an error rather than
// silently emitting an over-length credential.
func GenerateHTMLSafePassword(manufacturer bmc.Manufacturer, maxLen, minLen uint, fallbackLen int) (string, error) {
	floor := max(minPasswordFloor, int(minLen))

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
