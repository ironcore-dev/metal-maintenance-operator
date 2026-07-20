// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package hwmgr

// AuthMethod constants for HTTP authentication header names.
type AuthMethod string

const (
	HPEToken  AuthMethod = "auth"
	BasicAuth AuthMethod = "Authorization"
	DellToken AuthMethod = "X-Auth-Token"
	None      AuthMethod = ""
)

// Manufacturer identifies the hardware vendor.
type Manufacturer string

const (
	ManufacturerDell   Manufacturer = "Dell Inc."
	ManufacturerLenovo Manufacturer = "Lenovo"
	ManufacturerHPE    Manufacturer = "HPE"
)
