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
	UUID         string `json:"Uuid"` // Used by Lenovo (string UUID)
	Name         string `json:"Name"`
	Hostname     string `json:"Hostname"`
	Model        string `json:"Model"`
	HealthStatus int    `json:"HealthStatus"` // 4000 = OK, 4002 = Warning, etc.
}

// JobInfo represents the status of a vendor async operation.
type JobInfo struct {
	// JobID is the vendor-specific job identifier.
	JobID string
	// Status is the vendor-specific status string.
	Status string
	// Progress is the completion percentage (0-100).
	Progress int
	// Message provides human-readable status information.
	Message string
}

type ClientInterface interface {
	ImportServer(hostname string, IP metalv1alpha1.IP, bmcUser, bmcPassword string) error
	RemoveServer(hostname string, IP metalv1alpha1.IP) error
	ListServers() ([]Device, error)
	GetAuthToken() (string, error)
	// ImportServerAsync initiates an asynchronous import operation and returns a job ID.
	// Returns empty string if the operation is synchronous.
	ImportServerAsync(hostname string, IP metalv1alpha1.IP, bmcUser, bmcPassword string) (jobID string, err error)
	// RemoveServerAsync initiates an asynchronous remove operation and returns a job ID.
	// Returns empty string if the operation is synchronous.
	RemoveServerAsync(hostname string, IP metalv1alpha1.IP) (jobID string, err error)
	// GetJobStatus retrieves the current status of an async operation.
	GetJobStatus(jobID string) (*JobInfo, error)
	// IsJobComplete returns true if the job is no longer running.
	IsJobComplete(jobInfo *JobInfo) bool
	// IsJobSuccessful returns true if the job completed successfully.
	IsJobSuccessful(jobInfo *JobInfo) bool
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
