// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package subscriptions

import "time"

// APIVersion is the expected value of Config.APIVersion.
// Hard-reject ConfigMaps that carry a different value.
const APIVersion = "telemetry.metal.ironcore.dev/v1alpha1"

// Config is the schema for the operator's event-push telemetry
// configuration. Breaking changes must bump APIVersion.
//
// Every BMC whose EventService is reachable and enabled is subscribed for
// event-format pushes by default. Use ExcludeHardware to suppress specific
// vendor/model ranges (e.g. known-broken firmware). TestEventOverrides
// provides per-vendor test-event parameters; when absent the operator
// auto-discovers a suitable MessageId from the BMC's event registry.
type Config struct {
	APIVersion string `yaml:"apiVersion"`

	// SubscriptionReconcileInterval is how often the subscription manager
	// re-verifies each event-eligible BMC's subscription against our
	// receiver. Defends against BMC reboots that silently dropped the
	// subscription on their side. Required (must be non-zero); valid
	// range is enforced by Validate.
	SubscriptionReconcileInterval time.Duration `yaml:"subscriptionReconcileInterval"`

	// PerBMCTimeout caps each Redfish operation the subscription manager
	// performs (subscription create/delete, list, probe). Defaults to 30s
	// when zero.
	PerBMCTimeout time.Duration `yaml:"perBMCTimeout"`

	// TestEventInterval controls how often the operator fires a
	// SubmitTestEvent on each subscribed BMC to verify the full
	// push pipeline. Zero (the default) disables the health check.
	TestEventInterval time.Duration `yaml:"testEventInterval,omitempty"`

	// TestEventTimeout is how long the operator waits for the test
	// event to arrive at the receiver before recording a failure.
	// Defaults to 30s when zero (and TestEventInterval > 0).
	TestEventTimeout time.Duration `yaml:"testEventTimeout,omitempty"`

	// ExcludeHardware is an optional opt-out list. BMCs matching any row
	// are never subscribed regardless of EventService capability. Rows that
	// are absent (or an empty list) mean all BMCs are eligible.
	ExcludeHardware []ExcludeRule `yaml:"excludeHardware,omitempty"`

	// TestEventOverrides provides per-vendor test-event parameters. When a
	// BMC matches an override row the configured MessageId/Severity/
	// OriginOfCondition are used directly, skipping auto-discovery. When no
	// row matches, the operator walks the BMC's Redfish event registries to
	// find a suitable MessageId at runtime.
	TestEventOverrides []TestOverride `yaml:"testEventOverrides,omitempty"`
}

// ExcludeRule opts a vendor/model combination OUT of event-based delivery.
// BMCs matching this rule are never subscribed; the external redfish-exporter
// scrapes them via the discovery endpoint instead.
type ExcludeRule struct {
	// Vendor must be one of metal-operator's canonical bmc.Manufacturer
	// values: "Dell Inc.", "HPE", "Lenovo", or "Supermicro".
	Vendor string `yaml:"vendor"`
	// Models is the list of model strings to exclude; ["*"] excludes all
	// models for this vendor.
	Models []string `yaml:"models"`
	// MinFirmware is optional. When set, only firmware versions below this
	// semver are excluded (i.e. "exclude this vendor/model on old firmware
	// where the EventService is broken").
	MinFirmware string `yaml:"minFirmware,omitempty"`
}

// TestOverride carries the per-vendor SubmitTestEvent parameters used by the
// pipeline health check. When a BMC matches a TestOverride row the configured
// fields are used directly. When no row matches, the operator auto-discovers
// a suitable MessageId from the BMC's Redfish event registry.
type TestOverride struct {
	// Vendor must be one of metal-operator's canonical bmc.Manufacturer values.
	Vendor string `yaml:"vendor"`
	// Models is the list of model strings to match; ["*"] matches all models.
	Models []string `yaml:"models"`

	// TestMessageId is the Redfish MessageId sent in SubmitTestEvent.
	// When empty, the operator auto-discovers a MessageId at runtime.
	TestMessageId string `yaml:"testMessageId,omitempty"`
	// TestSeverity is the Severity value sent in SubmitTestEvent. Passed
	// through as-is; empty omits the field from the request entirely.
	TestSeverity string `yaml:"testSeverity,omitempty"`
	// TestOriginOfCondition is the OriginOfCondition URI sent in
	// SubmitTestEvent, as a plain string. When empty the field is omitted.
	TestOriginOfCondition string `yaml:"testOriginOfCondition,omitempty"`
}
