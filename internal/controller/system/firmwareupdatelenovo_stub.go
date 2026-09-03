// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package system

import (
	"context"
	"fmt"

	systemv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/system/v1alpha1"
)

// stubLenovoRepositoryUpdater is a placeholder implementation of lenovoRepositoryUpdater used only
// so this scaffold compiles and its state machine can be exercised without a live XCC or a
// metal-operator Lenovo BMC client.
//
// TODO(lenovo): DELETE this file once metal-operator exposes a real Lenovo repository updater on
// its BMC client (mirroring bmc.FirmwareUpdaterDell). The real implementation must issue:
//
//	POST /redfish/v1/UpdateService/Actions/Oem/LenovoUpdateService.GetRepoUpdateDetail  (dry-run)
//	POST /redfish/v1/UpdateService/Actions/Oem/LenovoUpdateService.UpdateFromRepository (apply)
//
// and poll the returned Redfish Task. See the design doc for the confirmed payload and behaviour:
// https://github.com/shyamsundart14/metal-maintenance-operator/blob/main/docs/lenovo-redfish-updatefromrepository.md
type stubLenovoRepositoryUpdater struct{}

func newLenovoRepositoryUpdater() lenovoRepositoryUpdater {
	return &stubLenovoRepositoryUpdater{}
}

func (s *stubLenovoRepositoryUpdater) GetRepoUpdateDetail(_ context.Context, _ string, _ lenovoRepositoryParameters) (bool, string, bool, error) {
	return false, "", true, fmt.Errorf("GetRepoUpdateDetail not implemented: Lenovo BMC client support pending")
}

func (s *stubLenovoRepositoryUpdater) UpdateFromRepository(_ context.Context, _ string, _ lenovoRepositoryParameters) (string, bool, error) {
	return "", true, fmt.Errorf("UpdateFromRepository not implemented: Lenovo BMC client support pending")
}

func (s *stubLenovoRepositoryUpdater) GetTask(_ context.Context, _, _ string) (*systemv1alpha1.RepositoryTask, bool, bool, error) {
	return nil, false, false, fmt.Errorf("GetTask not implemented: Lenovo BMC client support pending")
}
