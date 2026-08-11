// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package subscriptions

import "time"

// APIVersion is the expected value of Config.APIVersion.
// Hard-reject ConfigMaps that carry a different value.
const APIVersion = "telemetry.metal.ironcore.dev/v1alpha1"

// Config is the schema for the operator's event-push telemetry
// configuration. Breaking changes must bump APIVersion.
//
// The ConfigMap exists to tell the operator which BMCs to subscribe for
// event-format pushes (eventBasedHardware) and how the subscription
// manager paces its reconciles (subscriptionReconcileInterval,
// perBMCTimeout). BMCs not matched by an eventBasedHardware row are
// left alone — sensor metric scraping for those is the responsibility
// of an external redfish-exporter; nothing in this schema concerns
// metric collection.
type Config struct {
	APIVersion string `yaml:"apiVersion"`

	// SubscriptionReconcileInterval is how often the subscription manager
	// re-verifies each event-eligible BMC's subscription against our
	// receiver. Defends against BMC reboots that silently dropped the
	// subscription on their side. Required (must be non-zero); valid
	// range is enforced by Validate.
	SubscriptionReconcileInterval time.Duration `yaml:"subscriptionReconcileInterval"`

	// PerBMCTimeout caps each Redfish operation the subscription manager
	// performs (subscription create/delete, list). Defaults to 30s when
	// zero. Operators with slow or flaky BMCs may raise this so a
	// single slow BMC doesn't fail-fast through the reconcile loop.
	PerBMCTimeout time.Duration `yaml:"perBMCTimeout"`

	// TestEventInterval controls how often the operator fires a
	// SubmitTestEvent on each subscribed BMC to verify the full
	// push pipeline. Zero (the default) disables the health check.
	// When enabled, the test fires at most once per interval per BMC,
	// piggybacking on the normal subscription reconcile loop.
	TestEventInterval time.Duration `yaml:"testEventInterval,omitempty"`

	// TestEventTimeout is how long the operator waits for the test
	// event to arrive at the receiver before recording a failure.
	// Defaults to 30s when zero (and TestEventInterval > 0).
	TestEventTimeout time.Duration `yaml:"testEventTimeout,omitempty"`

	EventBasedHardware []HardwareMatch `yaml:"eventBasedHardware,omitempty"`
}

// HardwareMatch opts a vendor/model combination into event-based delivery.
// Default-deny: anything not matched here is left alone.
type HardwareMatch struct {
	// Vendor must be one of metal-operator's canonical bmc.Manufacturer
	// values: "Dell Inc.", "HPE", "Lenovo", or "Supermicro". These are
	// the exact strings metal-operator writes to BMC.Status.Manufacturer,
	// so the runtime match is plain equality — short forms like "Dell"
	// or "dell" are rejected by Validate (they'd silently never match).
	Vendor string `yaml:"vendor"`
	// Models is the list of model strings to match; ["*"] matches all models.
	Models []string `yaml:"models"`
	// MinFirmware is the minimum firmware version required for event-based
	// delivery. Must be a semver string if set. BMCs below this version
	// are not subscribed.
	MinFirmware string `yaml:"minFirmware,omitempty"`
	// TestMessageId is the Redfish registry-format MessageId used when
	// firing SubmitTestEvent health checks (e.g. "iDRAC.2.9.RAC0182" for
	// Dell, "SYS.1.0.TestEvent" for others). Must follow the pattern
	// <Registry>.<Major>.<Minor>.<Key>. Required for each vendor row when
	// testEventInterval is non-zero; ignored when health checks are disabled.
	TestMessageId string `yaml:"testMessageId,omitempty"`
}
