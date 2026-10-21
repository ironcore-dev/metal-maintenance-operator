// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// Package constants contains shared constants used across the maintenance-operator controllers.
package constants

const SanitizedLabel = "maintenance.metal.ironcore.dev/sanitized"

// Index field keys for controller-runtime field indexers.
const (
	ServerRefField = "spec.serverRef.name"
	BMCRefField    = "spec.bmcRef.name"
)

// Operation annotation key and values used to drive out-of-band reconciliation
// behavior (ignore/retry, and their child-propagation variants), independent of
// metal-operator's own copy of the same convention.
const (
	// OperationAnnotation is the annotation key selecting an out-of-band operation to perform on
	// a resource, i.e. an action driven by the annotation value rather than by spec reconciliation.
	// Its value must be one of the OperationAnnotation* constants below.
	OperationAnnotation = "metal.ironcore.dev/operation"

	// OperationAnnotationIgnore makes the reconciler skip a resource entirely when set on it.
	OperationAnnotationIgnore = "ignore"

	// OperationAnnotationIgnorePropagated is set by the controller on a child resource to make the
	// child's reconciler skip it, after the parent requested ignoring its children via ignore-child
	// or ignore-child-and-self. It is controller-set state, not user-facing input.
	OperationAnnotationIgnorePropagated = "ignore-propagated"

	// OperationAnnotationIgnoreChild makes the controller skip reconciling a parent resource's
	// children while still reconciling the parent itself.
	OperationAnnotationIgnoreChild = "ignore-child"
	// OperationAnnotationIgnoreChildAndSelf makes the controller skip reconciling both a parent
	// resource's children and the parent itself.
	OperationAnnotationIgnoreChildAndSelf = "ignore-child-and-self"

	// OperationAnnotationRetry restarts a resource's reconciliation from failed state back to
	// initial state when set on it.
	OperationAnnotationRetry = "retry"

	// OperationAnnotationRetryPropagated is set by the controller on a child resource to restart
	// the child's reconciliation from failed state back to initial state, after the parent requested
	// retrying its children via retry-child or retry-child-and-self. It is controller-set state, not
	// user-facing input.
	OperationAnnotationRetryPropagated = "retry-propagated"

	// OperationAnnotationRetryChild restarts the reconciliation of a parent resource's children
	// from failed state back to initial state.
	OperationAnnotationRetryChild = "retry-child"
	// OperationAnnotationRetryChildAndSelf restarts the reconciliation of both a parent resource's
	// children and the parent itself from failed state back to initial state.
	OperationAnnotationRetryChildAndSelf = "retry-child-and-self"

	// OperationAnnotationForceUpdateOrDeleteInProgress allows a resource to be deleted even while an
	// operation is still in progress.
	OperationAnnotationForceUpdateOrDeleteInProgress = "allow-in-progress-delete"
	// OperationAnnotationForceUpdateInProgress allows a resource to be updated even while an operation
	// is still in progress.
	OperationAnnotationForceUpdateInProgress = "allow-in-progress-update"
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
	ConditionProgressDeadlineExceeded    = "ProgressDeadlineExceeded"

	// ConditionRetryResetIssued tracks a BMC reset issued specifically to clear a hung/stuck BMC
	// before retrying a Failed resource. It is intentionally distinct from ConditionResetIssued
	// (used for the BMC-stabilization reset performed as part of the normal apply flow) so a stale
	// True value left over from an earlier, unrelated reset does not short-circuit the retry reset.
	ConditionRetryResetIssued = "RetryResetIssued"
	// ConditionServerResetIssued tracks a server reboot/power-on issued before retrying a Failed
	// resource, to clear a server that may be stuck in a bad state.
	ConditionServerResetIssued = "ServerResetIssued"
	// ConditionServerResetPowerOffIssued tracks the power-off stage of ConditionServerResetIssued's
	// explicit power-off-then-power-on sequence (skipped, and marked done immediately, if the
	// server was already off when the retry reset was requested).
	ConditionServerResetPowerOffIssued = "ServerResetPowerOffIssued"
	// ConditionServerResetForcePowerOffIssued tracks whether a forced power-off was escalated to
	// after the graceful power-off requested by ConditionServerResetPowerOffIssued did not complete
	// within the configured PowerOffTimeout.
	ConditionServerResetForcePowerOffIssued = "ServerResetForcePowerOffIssued"
	// ConditionServerResetPowerOnIssued tracks the power-on stage of ConditionServerResetIssued's
	// explicit power-off-then-power-on sequence. It exists purely so its own LastTransitionTime can
	// anchor the GiveUpTimeout for this stage separately from ConditionServerResetPowerOffIssued's,
	// so time spent in the power-off stage does not eat into the power-on stage's budget.
	ConditionServerResetPowerOnIssued = "ServerResetPowerOnIssued"
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
	ReasonProgressDeadlineExceeded    = "ProgressDeadlineExceeded"
	// ReasonForcePowerOffIssued is set when a graceful power-off before a retry did not complete
	// within PowerOffTimeout, and was escalated to a forced power-off.
	ReasonForcePowerOffIssued = "ForcePowerOffIssued"
	// ReasonServerResetGaveUp is set on ConditionServerResetIssued when the server did not recover
	// within GiveUpTimeout and automatic retrying was given up on with exponential back-off.
	ReasonServerResetGaveUp = "ServerResetGaveUp"
)
