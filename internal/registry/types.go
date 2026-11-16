// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	discoveryv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/discovery/v1alpha1"
)

// RegistrationPayload is the payload the metalprobe agent POSTs to /register.
type RegistrationPayload struct {
	SystemUUID string                     `json:"systemUUID"`
	Data       discoveryv1alpha1.Metadata `json:"data"`
}

// Aliases to the discovery API types so the metalprobe agent code can build
// its payload without importing the API package (and to keep the wire format
// identical to the Metadata CRD).
type (
	Server            = discoveryv1alpha1.Metadata
	DMI               = discoveryv1alpha1.DMI
	BIOSInformation   = discoveryv1alpha1.BIOSInformation
	ServerInformation = discoveryv1alpha1.SystemInformation
	BoardInformation  = discoveryv1alpha1.BoardInformation
	CPUInfo           = discoveryv1alpha1.CPUInfo
	NetworkInterface  = discoveryv1alpha1.NetworkInterface
	LLDPInterface     = discoveryv1alpha1.LLDPInterface
	Neighbor          = discoveryv1alpha1.LLDPNeighbor
	BlockDevice       = discoveryv1alpha1.BlockDevice
	MemoryDevice      = discoveryv1alpha1.MemoryDevice
	NIC               = discoveryv1alpha1.NIC
	PCIDevice         = discoveryv1alpha1.PCIDevice
)
