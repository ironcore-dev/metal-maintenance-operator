// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Metadata is a pure data object holding the hardware inventory discovered
// during the discovery boot of a Server. By convention a Metadata object is
// named exactly like the Server it describes.
// The payload fields mirror what the metalprobe agent posts to the registry.
//
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"
type Metadata struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Timestamp is when the discovery data has been collected.
	// +optional
	Timestamp *metav1.Time `json:"timestamp,omitempty"`
	// SystemInfo holds DMI/SMBIOS information about the system.
	// +optional
	SystemInfo DMI `json:"systemInfo,omitempty"`
	// CPU holds the discovered CPU information.
	// +optional
	CPU []CPUInfo `json:"cpu,omitempty"`
	// NetworkInterfaces holds the discovered network interfaces.
	// +optional
	NetworkInterfaces []NetworkInterface `json:"networkInterfaces,omitempty"`
	// LLDP holds the discovered LLDP interface information.
	// +optional
	LLDP []LLDPInterface `json:"lldp,omitempty"`
	// Storage holds the discovered block devices.
	// +optional
	Storage []BlockDevice `json:"storage,omitempty"`
	// Memory holds the discovered memory devices.
	// +optional
	Memory []MemoryDevice `json:"memory,omitempty"`
	// NICs holds the discovered NIC details.
	// +optional
	NICs []NIC `json:"nics,omitempty"`
	// PCIDevices holds the discovered PCI devices.
	// +optional
	PCIDevices []PCIDevice `json:"pciDevices,omitempty"`
}

// +kubebuilder:object:root=true

// MetadataList contains a list of Metadata.
type MetadataList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Metadata `json:"items"`
}

// DMI holds DMI/SMBIOS information of a system.
type DMI struct {
	// +optional
	BIOSInformation BIOSInformation `json:"biosInformation,omitempty"`
	// +optional
	SystemInformation SystemInformation `json:"systemInformation,omitempty"`
	// +optional
	BoardInformation BoardInformation `json:"boardInformation,omitempty"`
}

// BIOSInformation holds DMI BIOS information.
type BIOSInformation struct {
	// +optional
	Vendor string `json:"vendor,omitempty"`
	// +optional
	Version string `json:"version,omitempty"`
	// +optional
	Date string `json:"date,omitempty"`
}

// SystemInformation holds DMI system information.
type SystemInformation struct {
	// +optional
	Manufacturer string `json:"manufacturer,omitempty"`
	// +optional
	ProductName string `json:"productName,omitempty"`
	// +optional
	Version string `json:"version,omitempty"`
	// +optional
	SerialNumber string `json:"serialNumber,omitempty"`
	// +optional
	UUID string `json:"uuid,omitempty"`
	// +optional
	SKUNumber string `json:"skuNumber,omitempty"`
	// +optional
	Family string `json:"family,omitempty"`
}

// BoardInformation holds DMI baseboard information.
type BoardInformation struct {
	// +optional
	Manufacturer string `json:"manufacturer,omitempty"`
	// +optional
	Product string `json:"product,omitempty"`
	// +optional
	Version string `json:"version,omitempty"`
	// +optional
	SerialNumber string `json:"serialNumber,omitempty"`
	// +optional
	AssetTag string `json:"assetTag,omitempty"`
}

// CPUInfo holds information about a single CPU.
type CPUInfo struct {
	// +optional
	ID int32 `json:"id,omitempty"`
	// +optional
	TotalCores int32 `json:"totalCores,omitempty"`
	// +optional
	TotalHardwareThreads int32 `json:"totalHardwareThreads,omitempty"`
	// +optional
	Vendor string `json:"vendor,omitempty"`
	// +optional
	Model string `json:"model,omitempty"`
	// +optional
	Capabilities []string `json:"capabilities,omitempty"`
}

// NetworkInterface describes a discovered network interface.
type NetworkInterface struct {
	// Name is the name of the network interface.
	// +required
	Name string `json:"name"`
	// IPAddresses is the list of IP addresses assigned to the interface.
	// +optional
	IPAddresses []string `json:"ipAddresses,omitempty"`
	// MACAddress is the MAC address of the network interface.
	// +required
	MACAddress string `json:"macAddress"`
	// CarrierStatus is the operational carrier status of the interface.
	// +optional
	CarrierStatus string `json:"carrierStatus,omitempty"`
}

// LLDPInterface holds LLDP information of a single interface.
type LLDPInterface struct {
	// +optional
	Name string `json:"name,omitempty"`
	// +optional
	Neighbors []LLDPNeighbor `json:"neighbors,omitempty"`
}

// LLDPNeighbor describes a single LLDP neighbor.
type LLDPNeighbor struct {
	// +optional
	ChassisID string `json:"chassisId,omitempty"`
	// +optional
	PortID string `json:"portId,omitempty"`
	// +optional
	PortDescription string `json:"portDescription,omitempty"`
	// +optional
	SystemName string `json:"systemName,omitempty"`
	// +optional
	SystemDescription string `json:"systemDescription,omitempty"`
	// +optional
	MgmtIP string `json:"mgmtIp,omitempty"`
	// +optional
	Capabilities []string `json:"capabilities,omitempty"`
	// +optional
	VlanID string `json:"vlanId,omitempty"`
}

// BlockDevice describes a discovered block device.
type BlockDevice struct {
	// +optional
	Path string `json:"path,omitempty"`
	// +optional
	Name string `json:"name,omitempty"`
	// +optional
	Rotational bool `json:"rotational,omitempty"`
	// +optional
	Removable bool `json:"removable,omitempty"`
	// +optional
	ReadOnly bool `json:"readOnly,omitempty"`
	// +optional
	Vendor string `json:"vendor,omitempty"`
	// +optional
	Model string `json:"model,omitempty"`
	// +optional
	Serial string `json:"serial,omitempty"`
	// +optional
	WWID string `json:"wwid,omitempty"`
	// +optional
	PhysicalBlockSize int64 `json:"physicalBlockSize,omitempty"`
	// +optional
	LogicalBlockSize int64 `json:"logicalBlockSize,omitempty"`
	// +optional
	HWSectorSize int64 `json:"hWSectorSize,omitempty"`
	// +optional
	SizeBytes int64 `json:"sizeBytes,omitempty"`
	// +optional
	NUMANodeID int32 `json:"numaNodeID,omitempty"`
}

// MemoryDevice describes a discovered memory device.
type MemoryDevice struct {
	// +optional
	SizeBytes int64 `json:"size,omitempty"`
	// +optional
	DeviceSet string `json:"deviceSet,omitempty"`
	// +optional
	DeviceLocator string `json:"deviceLocator,omitempty"`
	// +optional
	BankLocator string `json:"bankLocator,omitempty"`
	// +optional
	MemoryType string `json:"memoryType,omitempty"`
	// +optional
	Speed string `json:"speed,omitempty"`
	// +optional
	Vendor string `json:"vendor,omitempty"`
	// +optional
	SerialNumber string `json:"serialNumber,omitempty"`
	// +optional
	AssetTag string `json:"assetTag,omitempty"`
	// +optional
	PartNumber string `json:"partNumber,omitempty"`
	// +optional
	ConfiguredMemorySpeed string `json:"configuredMemorySpeed,omitempty"`
	// +optional
	MinimumVoltage string `json:"minimumVoltage,omitempty"`
	// +optional
	MaximumVoltage string `json:"maximumVoltage,omitempty"`
	// +optional
	ConfiguredVoltage string `json:"configuredVoltage,omitempty"`
}

// NIC describes a discovered network interface card.
type NIC struct {
	// +optional
	Name string `json:"name,omitempty"`
	// +optional
	MAC string `json:"mac,omitempty"`
	// +optional
	PCIAddress string `json:"pciAddress,omitempty"`
	// +optional
	Speed string `json:"speed,omitempty"`
	// +optional
	LinkModes []string `json:"linkModes,omitempty"`
	// +optional
	SupportedPorts []string `json:"supportedPorts,omitempty"`
	// +optional
	FirmwareVersion string `json:"firmwareVersion,omitempty"`
}

// PCIDevice describes a discovered PCI device.
type PCIDevice struct {
	// +optional
	Address string `json:"address,omitempty"`
	// +optional
	Vendor string `json:"vendor,omitempty"`
	// +optional
	VendorID string `json:"vendorID,omitempty"`
	// +optional
	Product string `json:"product,omitempty"`
	// +optional
	ProductID string `json:"productID,omitempty"`
	// +optional
	NumaNodeID int32 `json:"numaNodeID,omitempty"`
}

func init() {
	SchemeBuilder.Register(&Metadata{}, &MetadataList{})
}
