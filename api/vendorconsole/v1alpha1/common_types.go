// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

// Secret key names used by vendor console CRDs referencing a credential Secret.
// Controllers read/write these keys from/to the referenced Secret's Data map.
const (
	// SecretUsernameKeyName is the Secret data key holding the console username.
	SecretUsernameKeyName = "username"
	// SecretPasswordKeyName is the Secret data key holding the console password.
	SecretPasswordKeyName = "password"
	// SecretTokenKeyName is the Secret data key holding the cached session token
	// (Lenovo XClarity CSRF, Dell OME token, etc.).
	SecretTokenKeyName = "token"
	// SecretSessionIDKeyName is the Secret data key holding the cached session identifier
	// (used by vendors that require an explicit session teardown at close).
	SecretSessionIDKeyName = "sessionID"
)

// FirmwareUpdateState is the top-level lifecycle state of a firmware update CR.
// Shared across per-vendor firmware update kinds so cross-vendor tooling can
// reason about the state uniformly.
type FirmwareUpdateState string

const (
	// FirmwareUpdateStatePending indicates the CR has been observed but no
	// vendor-side work has started yet.
	FirmwareUpdateStatePending FirmwareUpdateState = "Pending"
	// FirmwareUpdateStateInProgress indicates the vendor is actively running
	// the firmware update job.
	FirmwareUpdateStateInProgress FirmwareUpdateState = "InProgress"
	// FirmwareUpdateStateCompleted indicates the vendor firmware update job
	// finished successfully and no further work is pending.
	FirmwareUpdateStateCompleted FirmwareUpdateState = "Completed"
	// FirmwareUpdateStateFailed indicates the vendor job failed or a
	// precondition (wrong manufacturer, maintenance timeout) blocked progress.
	FirmwareUpdateStateFailed FirmwareUpdateState = "Failed"
)

// Condition types used by firmware update CRs.
const (
	// FirmwareUpgradeCompletedCondition is set to True when the vendor job
	// completed successfully.
	FirmwareUpgradeCompletedCondition = "UpdateCompleted"
)
