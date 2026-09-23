// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package testutils

import (
	"context"

	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/maintenance/v1alpha1"
	controllerutils "github.com/ironcore-dev/metal-maintenance-operator/internal/utils"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/ironcore-dev/metal-operator/bmc"
	bmcutils "github.com/ironcore-dev/metal-operator/pkg/bmcutils"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
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

// LiveServerPowerState queries the BMC directly for the given Server's current
// power state, mirroring controllerutils.GetServerPowerState. Tests must use
// this - not Server.Status.PowerState - to assert on power state while a
// Server may be Parked: metal-operator (and simcontrollers.ServerReconciler,
// its test stand-in) stop refreshing Server.Status while parked, so
// Status.PowerState can be arbitrarily stale for the entire duration of a
// maintenance window. Intended to be polled from within an Eventually, e.g.:
//
//	Eventually(func(g Gomega) metalv1alpha1.ServerPowerState {
//		state, err := testutils.LiveServerPowerState(ctx, k8sClient, server, protocol, skipCertValidation, bmcOptions)
//		g.Expect(err).NotTo(HaveOccurred())
//		return state
//	}).Should(Equal(metalv1alpha1.ServerOnPowerState))
func LiveServerPowerState(
	ctx context.Context,
	cl client.Client,
	server *metalv1alpha1.Server,
	protocol metalv1alpha1.ProtocolScheme,
	skipCertValidation bool,
	bmcOptions bmc.Options,
) (metalv1alpha1.ServerPowerState, error) {
	bmcClient, err := bmcutils.GetBMCClientForServer(ctx, cl, server, protocol, skipCertValidation, bmcOptions)
	if err != nil {
		return "", err
	}
	defer bmcClient.Logout()

	return controllerutils.GetServerPowerState(ctx, bmcClient, server)
}
