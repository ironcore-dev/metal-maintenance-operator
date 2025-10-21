// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package servermanagement

import (
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/ironcore-dev/metal-operator/bmc"
)

// Device represents a single device returned from the API
type Device struct {
	ID           int    `json:"Id"`
	Name         string `json:"Name"`
	Hostname     string `json:"Hostname"`
	Model        string `json:"Model"`
	HealthStatus int    `json:"HealthStatus"` // 4000 = OK, 4002 = Warning, etc.
}

type ServerManagementConsoleInterface interface {
	ImportServer(hostname string, IP metalv1alpha1.IP) error
	RemoveServer(hostname string) error
	ListServers() ([]Device, error)
	GetAuthToken() (string, error)
}

type ServerManagementConsole struct {
	Manufacturer string
	ServerManagementConsoleInterface
}

func New(manufacturer string, options ClientOptions) (console *ServerManagementConsole, err error) {
	console = &ServerManagementConsole{}
	console.Manufacturer = manufacturer
	switch manufacturer {
	case string(bmc.ManufacturerDell):
		console.ServerManagementConsoleInterface, err = NewDellClient(options)
		return
	case string(bmc.ManufacturerLenovo):
		console.ServerManagementConsoleInterface, err = NewLenovoClient(options)
		return
	case string(bmc.ManufacturerHPE):
		console.ServerManagementConsoleInterface, err = NewHPEClient(options)
		return
	}
	return
}
