// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package system

import (
	"context"
	"fmt"

	systemv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/system/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/ironcore-dev/metal-operator/bmc"
)

type otbHandler struct{}

func (h *otbHandler) handlePending(_ context.Context, _ *systemv1alpha1.FirmwareUpdate, _ bmc.BMC, _ *FirmwareUpdateReconciler, _ *metalv1alpha1.Server) (bool, error) {
	return false, fmt.Errorf("HPE/Lenovo one-time-boot firmware update not yet implemented")
}

func (h *otbHandler) handleInProgress(_ context.Context, _ *systemv1alpha1.FirmwareUpdate, _ bmc.BMC, _ *FirmwareUpdateReconciler, _ *metalv1alpha1.Server) (bool, error) {
	return false, fmt.Errorf("HPE/Lenovo one-time-boot firmware update not yet implemented")
}

func (h *otbHandler) handleCompleted(_ context.Context, _ *systemv1alpha1.FirmwareUpdate, _ bmc.BMC, _ *FirmwareUpdateReconciler, _ *metalv1alpha1.Server) (bool, error) {
	return false, nil
}
