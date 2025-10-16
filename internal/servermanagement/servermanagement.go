// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package servermanagement

import (
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

// Device represents a single device returned from the API
type Device struct {
	ID           int    `json:"Id"`
	Name         string `json:"Name"`
	Model        string `json:"Model"`
	HealthStatus int    `json:"HealthStatus"` // 4000 = OK, 4002 = Warning, etc.
}

type ServerManagementConsoleInterface interface {
	ImportServer(hostname string, IP metalv1alpha1.IP) error
	RemoveServer() error
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
	case "Dell":
		console.ServerManagementConsoleInterface, err = NewDellClient(options)
		return
	case "Lenovo":
		// Not implemented yet
		return
	case "HPE":
		// Not implemented yet
		return
	}
	return
}
