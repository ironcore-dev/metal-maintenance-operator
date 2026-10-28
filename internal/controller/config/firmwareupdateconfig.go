// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/util/validation/field"
)

// FirmwareUpdateAPIVersion is the expected value of
// FirmwareUpdateConfig.APIVersion. ConfigMaps carrying a different value are
// hard-rejected. Breaking changes to the schema below must bump this.
const FirmwareUpdateAPIVersion = "firmwareupdate.metal.ironcore.dev/v1alpha1"

// FirmwareUpdateConfigKey is the ConfigMap data key holding the YAML
// payload for FirmwareUpdateConfig.
const FirmwareUpdateConfigKey = "config.yaml"

const (
	minFirmwareUpdateResyncInterval = time.Second
	maxFirmwareUpdateResyncInterval = time.Hour

	maxFirmwareUpdateDefaultFailedAutoRetryCount = 10

	minFirmwareUpdateMaxRepositoryPasses = 1
	maxFirmwareUpdateMaxRepositoryPasses = 15

	maxFirmwareUpdateMinBMCResetInterval = 7 * 24 * time.Hour

	maxFirmwareUpdateScheduledJobPowerOnTimeout = 24 * time.Hour

	maxFirmwareUpdateRepositoryCheckInterval = 7 * 24 * time.Hour
)

// Built-in defaults for FirmwareUpdateConfig's fields, used by the
// FirmwareUpdate controller whenever its ConfigLoader is nil, has not yet
// loaded a config, or the loaded config leaves a given field unset.
const (
	DefaultResyncInterval              = 2 * time.Minute
	DefaultDefaultFailedAutoRetryCount = int32(3)
	DefaultMaxRepositoryPasses         = int32(5)
	DefaultMinBMCResetInterval         = 24 * time.Hour
	DefaultScheduledJobPowerOnTimeout  = 30 * time.Minute
	DefaultRepositoryCheckInterval     = 24 * time.Hour
)

// FirmwareUpdateConfig is the optional ConfigMap-delivered schema for tuning
// the FirmwareUpdate controller. Every field is a pointer: a nil field means
// "not set in the ConfigMap", in which case the operator falls back to its
// own built-in default. This lets operators override only the knobs they
// care about without having to restate every default in the ConfigMap.
type FirmwareUpdateConfig struct {
	APIVersion string `yaml:"apiVersion"`

	// ResyncInterval is the FirmwareUpdate controller's resync interval.
	ResyncInterval *time.Duration `yaml:"resyncInterval,omitempty"`

	// DefaultFailedAutoRetryCount is the number of automatic retries before a
	// Failed FirmwareUpdate stops being retried. Valid range: 0-10.
	DefaultFailedAutoRetryCount *int32 `yaml:"defaultFailedAutoRetryCount,omitempty"`

	// MaxRepositoryPasses bounds how many times a dry-run repository check
	// may find further packages pending before the FirmwareUpdate is marked
	// Valid range: 1-15.
	MaxRepositoryPasses *int32 `yaml:"maxRepositoryPasses,omitempty"`

	// MinBMCResetInterval is the minimum time that must have passed since a
	// BMC's last completed reset before another reset may be issued.
	MinBMCResetInterval *time.Duration `yaml:"minBMCResetInterval,omitempty"`

	// ScheduledJobPowerOnTimeout bounds how long a firmware job may remain in
	// Dell's "Scheduled" state with the server powered off and no observed
	// progress, before the controller powers the server back on. A value <=
	// 0 disables this check.
	ScheduledJobPowerOnTimeout *time.Duration `yaml:"scheduledJobPowerOnTimeout,omitempty"`

	// RepositoryCheckInterval bounds how often the Dell vendor handler is
	// allowed to (re-)issue a dry-run RepositoryCheck against the BMC while a
	// FirmwareUpdate sits in the Completed state. A value <= 0 disables the
	// gate (checks are re-issued on every reconcile).
	RepositoryCheckInterval *time.Duration `yaml:"repositoryCheckInterval,omitempty"`
}

// ParseFirmwareUpdateConfig decodes raw YAML from the ConfigMap's data field
// using strict unmarshalling (unknown keys are rejected) and validates all
// set fields. It returns a config with only the fields present in raw
// populated (others left nil) - callers fall back to their own defaults for
// unset fields.
func ParseFirmwareUpdateConfig(raw []byte) (*FirmwareUpdateConfig, error) {
	var cfg FirmwareUpdateConfig
	if err := parseStrictYAML(raw, &cfg); err != nil {
		return nil, err
	}
	if errs := ValidateFirmwareUpdateConfig(&cfg); len(errs) > 0 {
		return nil, errs.ToAggregate()
	}
	return &cfg, nil
}

// ValidateFirmwareUpdateConfig checks all semantic constraints on a
// FirmwareUpdateConfig. Only fields explicitly set (non-nil) are validated;
// unset fields are left to the caller's own flag-level defaults/bounds.
func ValidateFirmwareUpdateConfig(cfg *FirmwareUpdateConfig) field.ErrorList {
	var errs field.ErrorList
	root := field.NewPath("")

	errs = validateAPIVersion(errs, root.Child("apiVersion"), cfg.APIVersion, FirmwareUpdateAPIVersion)
	errs = validateDurationRange(errs, root.Child("resyncInterval"), cfg.ResyncInterval,
		minFirmwareUpdateResyncInterval, maxFirmwareUpdateResyncInterval)
	errs = validateInt32Range(errs, root.Child("defaultFailedAutoRetryCount"), cfg.DefaultFailedAutoRetryCount,
		0, maxFirmwareUpdateDefaultFailedAutoRetryCount)
	errs = validateInt32Range(errs, root.Child("maxRepositoryPasses"), cfg.MaxRepositoryPasses,
		minFirmwareUpdateMaxRepositoryPasses, maxFirmwareUpdateMaxRepositoryPasses)
	errs = validateDurationRange(errs, root.Child("minBMCResetInterval"), cfg.MinBMCResetInterval,
		0, maxFirmwareUpdateMinBMCResetInterval)

	// ScheduledJobPowerOnTimeout and RepositoryCheckInterval only have an
	// upper bound: a value <= 0 intentionally disables the check/gate, so
	// unlike the other fields above they must NOT be rejected by
	// validateDurationRange's lower bound.
	if cfg.ScheduledJobPowerOnTimeout != nil && *cfg.ScheduledJobPowerOnTimeout > maxFirmwareUpdateScheduledJobPowerOnTimeout {
		errs = append(errs, field.Invalid(root.Child("scheduledJobPowerOnTimeout"), cfg.ScheduledJobPowerOnTimeout.String(),
			fmt.Sprintf("must be <= %s (a value <= 0 disables the check)", maxFirmwareUpdateScheduledJobPowerOnTimeout)))
	}
	if cfg.RepositoryCheckInterval != nil && *cfg.RepositoryCheckInterval > maxFirmwareUpdateRepositoryCheckInterval {
		errs = append(errs, field.Invalid(root.Child("repositoryCheckInterval"), cfg.RepositoryCheckInterval.String(),
			fmt.Sprintf("must be <= %s (a value <= 0 disables the gate)", maxFirmwareUpdateRepositoryCheckInterval)))
	}

	return errs
}
