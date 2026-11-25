// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation/field"
)

// BMCUserAPIVersion is the expected value of BMCUserConfig.APIVersion.
// ConfigMaps carrying a different value are hard-rejected. Breaking changes to
// the schema below must bump this.
const BMCUserAPIVersion = "bmcuser.metal.ironcore.dev/v1alpha1"

// BMCUserConfigKey is the ConfigMap data key holding the YAML payload for
// BMCUserConfig.
const BMCUserConfigKey = "config.yaml"

const (
	// minConfigurablePasswordLength is the smallest password length the config
	// may request. bmc.GenerateSecurePassword needs at least 4 characters to
	// satisfy its complexity rule; 8 is a safe, common minimum.
	minConfigurablePasswordLength = 8
	// maxConfigurablePasswordLength bounds the configurable upper limit to a
	// value no sane BMC exceeds, guarding against typos that would reintroduce
	// the over-length credentials this config exists to prevent.
	maxConfigurablePasswordLength = 255
)

// BMCUserVendorPolicy overrides the password policy for a single manufacturer.
// The map key it is stored under (see BMCUserConfig.Vendors) is matched against
// the normalized BMC manufacturer, so use the canonical vendor string (e.g.
// "Dell Inc."). Every field is optional: an unset field falls back to the
// operator's built-in behaviour for that vendor.
type BMCUserVendorPolicy struct {
	// MaxPasswordLength caps the effective maximum password length for this
	// vendor, regardless of what the BMC's Redfish AccountService reports. This
	// exists because some BMCs (notably Dell iDRAC) over-report
	// MaxPasswordLength (e.g. 122) while the account service rejects anything
	// longer than ~20 characters. A nil value leaves the vendor uncapped (the
	// BMC-reported value is used as-is).
	MaxPasswordLength *uint `yaml:"maxPasswordLength,omitempty"`

	// SpecialChars overrides the set of special (non-alphanumeric) characters
	// permitted in a generated password for this vendor. Generated passwords are
	// filtered so that only characters in this set (plus letters and digits)
	// survive — use it to exclude characters a given firmware rejects (e.g. '/',
	// backtick, '^'). A nil value keeps the generator's vendor default; an empty
	// string is rejected (a password needs some special characters for
	// complexity).
	SpecialChars *string `yaml:"specialChars,omitempty"`
}

// BMCUserConfig is the optional ConfigMap-delivered schema for tuning BMCUser
// credential generation. Every field is optional: a nil/absent field means "not
// set in the ConfigMap", in which case the operator falls back to its own
// built-in default. This lets operators override only the knobs they care about.
type BMCUserConfig struct {
	APIVersion string `yaml:"apiVersion"`

	// DefaultPasswordLength is the password length used when the BMC reports no
	// MaxPasswordLength. Valid range: 8-255. When unset, the operator's built-in
	// default (20) applies.
	DefaultPasswordLength *uint `yaml:"defaultPasswordLength,omitempty"`

	// Vendors maps a canonical manufacturer string (e.g. "Dell Inc.") to a
	// per-vendor password policy override. A manufacturer absent from the map
	// uses the operator's built-in defaults for that vendor.
	Vendors map[string]BMCUserVendorPolicy `yaml:"vendors,omitempty"`
}

// ParseBMCUserConfig decodes raw YAML from the ConfigMap's data field using
// strict unmarshalling (unknown keys are rejected) and validates all set
// fields. It returns a config with only the fields present in raw populated
// (others left nil) - callers fall back to their own defaults for unset fields.
func ParseBMCUserConfig(raw []byte) (*BMCUserConfig, error) {
	var cfg BMCUserConfig
	if err := parseStrictYAML(raw, &cfg); err != nil {
		return nil, err
	}
	if errs := ValidateBMCUserConfig(&cfg); len(errs) > 0 {
		return nil, errs.ToAggregate()
	}
	return &cfg, nil
}

// ValidateBMCUserConfig checks all semantic constraints on a BMCUserConfig.
// Only fields explicitly set (non-nil) are validated; unset fields are left to
// the operator's own defaults.
func ValidateBMCUserConfig(cfg *BMCUserConfig) field.ErrorList {
	var errs field.ErrorList
	root := field.NewPath("")

	errs = validateAPIVersion(errs, root.Child("apiVersion"), cfg.APIVersion, BMCUserAPIVersion)
	errs = validatePasswordLengthPtr(errs, root.Child("defaultPasswordLength"), cfg.DefaultPasswordLength)

	for vendor, policy := range cfg.Vendors {
		vPath := root.Child("vendors").Key(vendor)
		if vendor == "" {
			errs = append(errs, field.Invalid(vPath, vendor, "vendor key must not be empty"))
		}
		errs = validatePasswordLengthPtr(errs, vPath.Child("maxPasswordLength"), policy.MaxPasswordLength)
		if policy.SpecialChars != nil {
			if *policy.SpecialChars == "" {
				errs = append(errs, field.Invalid(vPath.Child("specialChars"), *policy.SpecialChars,
					"must not be empty; omit the field to keep the vendor default"))
			} else if i := strings.IndexFunc(*policy.SpecialChars, isAlphanumeric); i >= 0 {
				// Letters and digits are always permitted in generated passwords, so
				// listing them here is meaningless and likely a mistake. Only the set
				// of allowed *special* characters belongs in specialChars.
				errs = append(errs, field.Invalid(vPath.Child("specialChars"), *policy.SpecialChars,
					"must contain only special (non-alphanumeric) characters"))
			}
		}
	}
	return errs
}

// isAlphanumeric reports whether r is an ASCII letter or digit.
func isAlphanumeric(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// validatePasswordLengthPtr appends an error if v is non-nil and outside the
// configurable password-length bounds.
func validatePasswordLengthPtr(errs field.ErrorList, path *field.Path, v *uint) field.ErrorList {
	if v == nil {
		return errs
	}
	if *v < minConfigurablePasswordLength || *v > maxConfigurablePasswordLength {
		errs = append(errs, field.Invalid(path, fmt.Sprintf("%d", *v),
			fmt.Sprintf("must be between %d and %d", minConfigurablePasswordLength, maxConfigurablePasswordLength)))
	}
	return errs
}
