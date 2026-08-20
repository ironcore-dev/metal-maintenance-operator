// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package testutils

import (
	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/maintenance/v1alpha1"
	controllerutils "github.com/ironcore-dev/metal-maintenance-operator/internal/utils"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
)

// ServerParkedFor matches a Server that is Parked and owned by the given
// ServerMaintenance, mirroring controllerutils.IsServerParkedForOwner. It
// replaces the old Spec.ServerMaintenanceRef/Status.State == Maintenance
// assertions that no longer apply now that Server maintenance state is
// driven by the Parked-state mechanism (see
// simcontrollers.ServerReconciler.syncParkedState). Shared by the
// maintenance, baseboard, and system controller test suites so Parked-state
// assertions stay consistent across all of them.
func ServerParkedFor(maintenance *maintenancev1alpha1.ServerMaintenance) types.GomegaMatcher {
	return gomega.SatisfyAll(
		gomega.HaveField("Status.State", metalv1alpha1.ServerStateParked),
		gomega.HaveField("Annotations", gomega.HaveKeyWithValue(
			controllerutils.ServerMaintenanceOwnerAnnotation,
			controllerutils.ServerMaintenanceOwnerKey(maintenance.Namespace, maintenance.Name),
		)),
	)
}

// ServerNotParked matches a Server that is not (or no longer) Parked for any
// maintenance.
var ServerNotParked = gomega.SatisfyAll(
	gomega.HaveField("Status.State", gomega.Not(gomega.Equal(metalv1alpha1.ServerStateParked))),
	gomega.HaveField("Annotations", gomega.Not(gomega.HaveKey(controllerutils.ServerMaintenanceOwnerAnnotation))),
)
