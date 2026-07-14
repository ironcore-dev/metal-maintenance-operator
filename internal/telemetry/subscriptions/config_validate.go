// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package subscriptions

import (
	"fmt"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	"github.com/ironcore-dev/metal-operator/bmc"
	goyaml "go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

const (
	minInterval = 5 * time.Second
	maxInterval = 24 * time.Hour

	minPerBMCTimeout = time.Second
	maxPerBMCTimeout = 10 * time.Minute
)

var knownVendors = map[bmc.Manufacturer]struct{}{
	bmc.ManufacturerDell:       {},
	bmc.ManufacturerLenovo:     {},
	bmc.ManufacturerHPE:        {},
	bmc.ManufacturerSupermicro: {},
}

// supportedVendorList returns the canonical vendor strings in a stable
// order for use in error messages. Built once, not allocated per error.
var supportedVendorList = []string{
	string(bmc.ManufacturerDell),
	string(bmc.ManufacturerHPE),
	string(bmc.ManufacturerLenovo),
	string(bmc.ManufacturerSupermicro),
}

func isKnownVendor(v string) bool {
	_, ok := knownVendors[bmc.Manufacturer(v)]
	return ok
}

// Parse decodes raw YAML from the ConfigMap's data field using strict
// unmarshalling (unknown keys are rejected) and then validates all fields.
// It returns a fully-validated Config or a non-empty ErrorList.
func Parse(raw []byte) (*Config, field.ErrorList) {
	var cfg Config
	dec := goyaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, field.ErrorList{
			field.Invalid(field.NewPath(""), string(raw), fmt.Sprintf("YAML parse error: %v", err)),
		}
	}
	if errs := Validate(&cfg); len(errs) > 0 {
		return nil, errs
	}
	return &cfg, nil
}

// Validate checks all semantic constraints on a Config.
// All errors are collected into one pass so the caller sees them all at once.
func Validate(cfg *Config) field.ErrorList {
	var errs field.ErrorList
	root := field.NewPath("")

	// apiVersion
	if cfg.APIVersion != APIVersion {
		errs = append(errs, field.Invalid(root.Child("apiVersion"), cfg.APIVersion,
			fmt.Sprintf("must be %q", APIVersion)))
	}

	if cfg.SubscriptionReconcileInterval == 0 {
		errs = append(errs, field.Required(root.Child("subscriptionReconcileInterval"),
			"subscriptionReconcileInterval is required"))
	} else if cfg.SubscriptionReconcileInterval < minInterval || cfg.SubscriptionReconcileInterval > maxInterval {
		errs = append(errs, field.Invalid(root.Child("subscriptionReconcileInterval"), cfg.SubscriptionReconcileInterval,
			fmt.Sprintf("must be between %s and %s", minInterval, maxInterval)))
	}

	// perBMCTimeout
	if cfg.PerBMCTimeout != 0 && (cfg.PerBMCTimeout < minPerBMCTimeout || cfg.PerBMCTimeout > maxPerBMCTimeout) {
		errs = append(errs, field.Invalid(root.Child("perBMCTimeout"), cfg.PerBMCTimeout,
			fmt.Sprintf("must be between %s and %s when set", minPerBMCTimeout, maxPerBMCTimeout)))
	}

	// metrics — no allow-list validated here. The collector publishes every
	// numeric value from every MetricReport on the BMC; metric selection
	// happens at Prometheus scrape time via metric_relabel_configs.

	// eventBasedHardware — vendor must be a canonical metal-operator
	// bmc.Manufacturer value, models non-empty (and individually
	// non-blank), no duplicate vendors, valid semver for MinFirmware.
	vendorsSeen := make(map[string]bool, len(cfg.EventBasedHardware))
	for i, hw := range cfg.EventBasedHardware {
		hwPath := root.Child("eventBasedHardware").Index(i)
		vendor := strings.TrimSpace(hw.Vendor)
		switch {
		case vendor == "":
			errs = append(errs, field.Required(hwPath.Child("vendor"), ""))
		case hw.Vendor != vendor || !isKnownVendor(vendor):
			// Reject non-canonical values up front. hw.Vendor != vendor
			// catches whitespace-padded forms (e.g. " Dell Inc. ") that
			// would pass validation but never match the exact-compare
			// runtime path in vendorMatches.
			errs = append(errs, field.NotSupported(hwPath.Child("vendor"), hw.Vendor, supportedVendorList))
		case vendorsSeen[vendor]:
			errs = append(errs, field.Duplicate(hwPath.Child("vendor"), hw.Vendor))
		default:
			vendorsSeen[vendor] = true
		}
		if len(hw.Models) == 0 {
			errs = append(errs, field.Required(hwPath.Child("models"), "at least one model or \"*\" required"))
		} else {
			for j, model := range hw.Models {
				if strings.TrimSpace(model) == "" {
					errs = append(errs, field.Invalid(hwPath.Child("models").Index(j), model,
						"model entries must not be blank"))
				}
			}
		}
		if hw.MinFirmware != "" {
			if _, err := semver.ParseTolerant(hw.MinFirmware); err != nil {
				errs = append(errs, field.Invalid(hwPath.Child("minFirmware"), hw.MinFirmware,
					fmt.Sprintf("must be a valid semver: %v", err)))
			}
		}
	}
	return errs
}
