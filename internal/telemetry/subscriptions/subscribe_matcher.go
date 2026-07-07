// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package subscriptions

import (
	"strings"

	"github.com/blang/semver/v4"
)

// SubscribeToBMC decides whether the reconciler should keep Redfish
// event subscriptions on this BMC. It's a pure function of the BMC's
// vendor/model/firmware (as reported by the metal-operator BMC
// controller) and the operator's ConfigMap policy.
func SubscribeToBMC(ref BMCRef, cfg *Config) bool {
	if cfg == nil {
		return false
	}
	// An unknown vendor (informer hasn't populated Status yet, or BMC
	// never reported one) does not qualify — we have no signal that
	// this BMC supports subscriptions.
	if ref.Vendor == "" {
		return false
	}
	for _, hw := range cfg.EventBasedHardware {
		if !vendorMatches(hw.Vendor, ref.Vendor) {
			continue
		}
		if !modelsMatch(hw.Models, ref.Model) {
			continue
		}
		if !firmwareSatisfies(hw.MinFirmware, ref.FirmwareVersion) {
			continue
		}
		return true
	}
	return false
}

// vendorMatches compares the configured vendor against the BMC's
// reported manufacturer.
func vendorMatches(configured, reported string) bool {
	if configured == "" || reported == "" {
		return false
	}
	return configured == reported
}

// modelsMatch returns true if model is in the configured list, or if
// the list contains "*". Comparison is case-insensitive — vendor
// product naming has the same casing chaos as vendor strings.
func modelsMatch(models []string, reported string) bool {
	if len(models) == 0 {
		// Defensive: schema validation rejects empty lists, but under-
		// specified rows should never match.
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

// firmwareSatisfies returns true when minFirmware is empty, or when
// reported parses as a semver >= minFirmware.
func firmwareSatisfies(minFirmware, reported string) bool {
	if minFirmware == "" {
		return true
	}
	if reported == "" {
		return false
	}
	min, err := semver.ParseTolerant(minFirmware)
	if err != nil {
		// Config validation rejects invalid semver, but treat it as
		// "never matches" if one reaches us anyway.
		return false
	}
	got, err := semver.ParseTolerant(reported)
	if err != nil {
		return false
	}
	return got.GTE(min)
}
