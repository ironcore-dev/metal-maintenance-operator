// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

// PlanBMCSettingsTemplate is a plan-level copy of BMCSettingsTemplate that omits
// the Variables field. Variables contain an O(n²) CEL uniqueness rule that exceeds
// Kubernetes' CRD validation cost budget when nested inside MaintenancePlan.
// Variables can be added directly to the BMCSettings child CR if needed.
type PlanBMCSettingsTemplate struct {
	// Version specifies the BMC firmware version for which the settings apply.
	// +required
	Version string `json:"version"`

	// Settings contains BMC settings as a key/value map.
	// +optional
	Settings map[string]string `json:"settings,omitempty"`

	// RetryPolicy defines automatic retry behaviour on transient failures.
	// +optional
	RetryPolicy *metalv1alpha1.RetryPolicy `json:"retryPolicy,omitempty"`

	// ServerMaintenancePolicy is the maintenance policy to apply on affected servers.
	// +optional
	ServerMaintenancePolicy metalv1alpha1.ServerMaintenancePolicy `json:"serverMaintenancePolicy,omitempty"`
}
