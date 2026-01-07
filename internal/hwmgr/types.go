// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package hwmgr

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

type ClientInterface interface {
	ImportServer(hostname string, IP metalv1alpha1.IP, bmcUser, bmcPassword string) error
	RemoveServer(hostname string, IP metalv1alpha1.IP) error
	ListServers() ([]Device, error)
	GetAuthToken() (string, error)
}

type Client struct {
	Manufacturer bmc.Manufacturer
	ClientInterface
}

func New(manufacturer bmc.Manufacturer, options ClientOptions) (client *Client, err error) {
	client = &Client{}
	client.Manufacturer = manufacturer
	switch manufacturer {
	case bmc.ManufacturerDell:
		client.ClientInterface, err = NewDellClient(options)
		return
	case bmc.ManufacturerLenovo:
		client.ClientInterface, err = NewLenovoClient(options)
		return
	case bmc.ManufacturerHPE:
		client.ClientInterface, err = NewHPEClient(options)
		return
	}
	return
}
