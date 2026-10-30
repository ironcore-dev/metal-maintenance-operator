// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// Package config holds the optional ConfigMap-delivered schemas for tuning
// individual controllers (one file per controller, e.g.
// firmwareupdateconfig.go for FirmwareUpdate). Each schema is decoupled from
// its controller/reconciler package so it can be parsed, validated, and
// tested in isolation - mirroring the internal/telemetry/subscriptions vs
// internal/telemetry/runtime split. This file holds the small amount of
// strict-YAML-decode and bounds-validation logic shared across those
// per-controller schemas.
package config

import (
	"fmt"
	"strings"
	"time"

	goyaml "go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// parseStrictYAML decodes raw YAML into out using strict unmarshalling
// (unknown keys are rejected).
func parseStrictYAML(raw []byte, out any) error {
	dec := goyaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("YAML parse error: %w", err)
	}
	return nil
}

// validateAPIVersion appends an error to errs if got != want.
func validateAPIVersion(errs field.ErrorList, path *field.Path, got, want string) field.ErrorList {
	if got != want {
		errs = append(errs, field.Invalid(path, got, fmt.Sprintf("must be %q", want)))
	}
	return errs
}

// validateDurationRange appends an error to errs if v is non-nil and *v
// falls outside [min, max].
func validateDurationRange(errs field.ErrorList, path *field.Path, v *time.Duration, min, max time.Duration) field.ErrorList {
	if v == nil {
		return errs
	}
	if *v < min || *v > max {
		errs = append(errs, field.Invalid(path, v.String(), fmt.Sprintf("must be between %s and %s", min, max)))
	}
	return errs
}

// validateInt32Range appends an error to errs if v is non-nil and *v falls
// outside [min, max].
func validateInt32Range(errs field.ErrorList, path *field.Path, v *int32, min, max int32) field.ErrorList {
	if v == nil {
		return errs
	}
	if *v < min || *v > max {
		errs = append(errs, field.Invalid(path, *v, fmt.Sprintf("must be between %d and %d", min, max)))
	}
	return errs
}
