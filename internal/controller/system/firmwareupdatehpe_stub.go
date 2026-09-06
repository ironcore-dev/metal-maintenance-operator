// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package system

import (
	"context"
	"fmt"
)

// stubHPEInstallSetUpdater is a placeholder implementation of hpeInstallSetUpdater used only so
// this scaffold compiles and its state machine can be exercised without a live iLO or an SPP
// manifest reader.
//
// TODO(hpe): DELETE this file once metal-operator exposes an HPE iLO client (ComponentRepository
// AddFromUri, InstallSets create + HpeComponentInstallSet.Invoke, UpdateTaskQueue polling) and an
// SPP metadata.json reader with the Target-GUID diff. The real implementation is documented here:
// https://github.com/shyamsundart14/metal-maintenance-operator/blob/main/docs/hpe-spp-manifest.md
type stubHPEInstallSetUpdater struct{}

func newHPEInstallSetUpdater() hpeInstallSetUpdater {
	return &stubHPEInstallSetUpdater{}
}

func (s *stubHPEInstallSetUpdater) ComputeUpdateSet(_ context.Context, _, _, _, _ string) ([]hpeComponentUpdate, error) {
	return nil, fmt.Errorf("ComputeUpdateSet not implemented: HPE SPP-manifest diff support pending")
}

func (s *stubHPEInstallSetUpdater) StageComponent(_ context.Context, _, _ string) error {
	return fmt.Errorf("StageComponent not implemented: HPE iLO AddFromUri support pending")
}

func (s *stubHPEInstallSetUpdater) CreateInstallSet(_ context.Context, _, _ string, _ []hpeComponentUpdate) (string, error) {
	return "", fmt.Errorf("CreateInstallSet not implemented: HPE iLO Install Set support pending")
}

func (s *stubHPEInstallSetUpdater) InvokeInstallSet(_ context.Context, _, _ string) error {
	return fmt.Errorf("InvokeInstallSet not implemented: HPE iLO Install Set support pending")
}

func (s *stubHPEInstallSetUpdater) InstallSetState(_ context.Context, _, _ string) (bool, bool, error) {
	return false, false, fmt.Errorf("InstallSetState not implemented: HPE iLO UpdateTaskQueue support pending")
}
