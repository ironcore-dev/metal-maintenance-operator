// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package probe

import (
	"fmt"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/registry"
	"github.com/jaypipes/ghw"
)

func collectPCIDevicesInfoData() ([]registry.PCIDevice, error) {
	pci, err := ghw.PCI()
	if err != nil {
		return []registry.PCIDevice{}, fmt.Errorf("could not get PCI info: %w", err)
	}

	pciDevs := make([]registry.PCIDevice, 0)
	for _, p := range pci.Devices {
		nid := int32(-1)
		if p.Node != nil {
			nid = int32(p.Node.ID)
		}
		pciDevs = append(pciDevs, registry.PCIDevice{
			Address:    p.Address,
			Vendor:     p.Vendor.Name,
			VendorID:   p.Vendor.ID,
			Product:    p.Product.Name,
			ProductID:  p.Product.ID,
			NumaNodeID: nid,
		})
	}
	return pciDevs, nil
}
