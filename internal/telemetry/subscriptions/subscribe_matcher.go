// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package subscriptions

import (
	"strings"

	"github.com/blang/semver/v4"
)

// IsExcluded reports whether the BMC should be kept out of event-based
// delivery based on the excludeHardware opt-out list. Returns false when the
// vendor is not yet known (informer hasn't populated Status) so that the
// EventService probe — not a vendor mismatch — makes the final call.
func IsExcluded(ref BMCRef, cfg *Config) bool {
	if cfg == nil || ref.Vendor == "" {
		return false
	}
	for _, ex := range cfg.ExcludeHardware {
		if !vendorMatches(ex.Vendor, ref.Vendor) {
			continue
		}
		if !modelsMatch(ex.Models, ref.Model) {
			continue
		}
		if !excludeFirmwareSatisfies(ex.MinFirmware, ref.FirmwareVersion) {
			continue
		}
		return true
	}
	return false
}

// TestOverrideFor returns the first TestOverride row that matches the BMC,
// or nil when no row matches (caller should auto-discover a MessageId).
func TestOverrideFor(ref BMCRef, cfg *Config) *TestOverride {
	if cfg == nil {
		return nil
	}
	for i := range cfg.TestEventOverrides {
		ov := &cfg.TestEventOverrides[i]
		if !vendorMatches(ov.Vendor, ref.Vendor) {
			continue
		}
		if !modelsMatch(ov.Models, ref.Model) {
			continue
		}
		return ov
	}
	return nil
}

// vendorMatches compares the configured vendor against the BMC's reported manufacturer.
func vendorMatches(configured, reported string) bool {
	if configured == "" || reported == "" {
		return false
	}
	return configured == reported
}

// modelsMatch returns true if model is in the configured list, or if
// the list contains "*". Comparison is case-insensitive.
func modelsMatch(models []string, reported string) bool {
	if len(models) == 0 {
		return false
	}
	reported = strings.TrimSpace(reported)
	for _, m := range models {
		if m == "*" {
			return true
		}
		if strings.EqualFold(strings.TrimSpace(m), reported) {
			return true
		}
	}
	return false
}

// excludeFirmwareSatisfies returns true when minFirmware is empty (no floor),
// or when the reported firmware is BELOW the floor — meaning the exclude rule
// applies to old firmware only ("exclude this model if firmware < X").
// An empty reported version with a non-empty floor is treated as "unknown,
// apply the exclusion conservatively".
func excludeFirmwareSatisfies(minFirmware, reported string) bool {
	if minFirmware == "" {
		return true
	}
	min, err := semver.ParseTolerant(minFirmware)
	if err != nil {
		return true
	}
	if reported == "" {
		return true
	}
	got, err := semver.ParseTolerant(reported)
	if err != nil {
		return true
	}
	return got.LT(min)
}
