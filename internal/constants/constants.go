// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// Package constants contains shared constants used across the maintenance-operator controllers.
package constants

const SanitizedLabel = "maintenance.metal.ironcore.dev/sanitized"

// Index field keys for controller-runtime field indexers.
const (
	ServerRefField = "spec.serverRef.name"
	BMCRefField    = "spec.bmcRef.name"
)

// Shared condition types used across baseboard and system controllers.
const (
	ConditionServerMaintenanceCreated    = "ServerMaintenanceCreated"
	ConditionServerMaintenanceDeleted    = "ServerMaintenanceDeleted"
	ConditionServerMaintenanceWaiting    = "ServerMaintenanceWaiting"
	ConditionResetIssued                 = "ResetIssued"
	ConditionVersionUpgradeIssued        = "VersionUpgradeIssued"
	ConditionVersionUpgradeCompleted     = "VersionUpgradeCompleted"
	ConditionVersionUpgradeVerification  = "VersionUpgradeVerification"
	ConditionVersionUpgradeReboot        = "VersionUpgradeReboot"
	ConditionVersionUpdatePending        = "VersionUpdatePending"
	ConditionPoweringOn                  = "PoweringOn"
	ConditionReset                       = "Reset"
	ConditionReady                       = "Ready"
	ConditionRetryOfFailedResourceIssued = "RetryOfFailedResourceIssued"
)

// Shared reason strings used across baseboard and system controllers.
const (
	ReasonUpgradeIssued               = "UpgradeIssued"
	ReasonUpgradeTaskFailed           = "UpgradeTaskFailed"
	ReasonUpgradeIssueFailed          = "UpgradeIssueFailed"
	ReasonUpgradeTaskCompleted        = "UpgradeTaskCompleted"
	ReasonVersionUpdateVerified       = "VersionUpdateVerified"
	ReasonVersionVerificationFailed   = "VersionVerificationFailed"
	ReasonVersionUpgradePending       = "VersionUpgradePending"
	ReasonResetIssued                 = "ResetIssued"
	ReasonResetRequired               = "ResetRequired"
	ReasonNoResetRequired             = "NoResetRequired"
	ReasonAuthenticationFailed        = "AuthenticationFailed"
	ReasonInternalError               = "InternalServerError"
	ReasonUnknownError                = "UnknownError"
	ReasonConnectionFailed            = "ConnectionFailed"
	ReasonUserReset                   = "UserRequested"
	ReasonAutoReset                   = "AutoResetting"
	ReasonConnected                   = "Connected"
	ReasonMaintenanceCreated          = "ServerMaintenanceHasBeenCreated"
	ReasonMaintenanceDeleted          = "ServerMaintenanceHasBeenDeleted"
	ReasonMaintenanceWaiting          = "ServerMaintenanceWaitingOnApproval"
	ReasonMaintenanceApproved         = "ServerMaintenanceApproval"
	ReasonRetryOfFailedResourceIssued = "RetryOfFailedResourceIssued"
)
