// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package hwmgr

import (
	"encoding/json"
	"fmt"
	"net/http"

	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

// FujitsuClient talks to a Fujitsu iRMC using its Redfish API.
type FujitsuClient struct {
	client *client
}

// redfishSystem represents the part of a Redfish ComputerSystem
// response that we currently care about.
type redfishSystem struct {
	ID     string `json:"Id"`
	Name   string `json:"Name"`
	Model  string `json:"Model"`
	Serial string `json:"SerialNumber"`
}

type redfishSystemsResponse struct {
	Members []struct {
		ODataID string `json:"@odata.id"`
	} `json:"Members"`
}

// NewFujitsuClient creates a client for a Fujitsu iRMC.
func NewFujitsuClient(options ClientOptions) (*FujitsuClient, error) {
	c, err := NewClient(options)
	if err != nil {
		return nil, err
	}

	// Fujitsu iRMC uses HTTP Basic Authentication
	// for the Redfish API.
	c.basicAuth = true

	return &FujitsuClient{
		client: c,
	}, nil
}

// ListServers gets the server information from the iRMC Redfish API.
func (c *FujitsuClient) ListServers() ([]Device, error) {
	systemsURL := c.client.parsedURL.JoinPath("/redfish/v1/Systems")

	req, err := http.NewRequest(
		http.MethodGet,
		systemsURL.String(),
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("error creating Redfish systems request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	body, err := c.client.DoRequest(
		req,
		[]int{http.StatusOK},
	)
	if err != nil {
		return nil, fmt.Errorf("error getting Redfish systems: %w", err)
	}

	var systemsResponse redfishSystemsResponse
	if err := json.Unmarshal(body, &systemsResponse); err != nil {
		return nil, fmt.Errorf(
			"error parsing Redfish systems response: %w",
			err,
		)
	}

	devices := make([]Device, 0, len(systemsResponse.Members))

	for _, member := range systemsResponse.Members {
		if member.ODataID == "" {
			return nil, fmt.Errorf(
				"redfish Systems response contains member without @odata.id",
			)
		}

		system, err := c.getSystem(member.ODataID)
		if err != nil {
			return nil, err
		}

		devices = append(devices, Device{
			Name:     system.Name,
			Hostname: c.client.parsedURL.Hostname(),
			Model:    system.Model,
			Serial:   system.Serial,
		})
	}

	return devices, nil
}

// getSystem gets one individual Redfish ComputerSystem.
func (c *FujitsuClient) getSystem(systemPath string) (*redfishSystem, error) {
	systemURL := c.client.parsedURL.JoinPath(systemPath)

	req, err := http.NewRequest(
		http.MethodGet,
		systemURL.String(),
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf(
			"error creating Redfish system request: %w",
			err,
		)
	}

	req.Header.Set("Accept", "application/json")

	body, err := c.client.DoRequest(
		req,
		[]int{http.StatusOK},
	)
	if err != nil {
		return nil, fmt.Errorf(
			"error getting Redfish system %s: %w",
			systemPath,
			err,
		)
	}

	var system redfishSystem
	if err := json.Unmarshal(body, &system); err != nil {
		return nil, fmt.Errorf(
			"error parsing Redfish system response: %w",
			err,
		)
	}

	if system.ID == "" {
		return nil, fmt.Errorf(
			"redfish system %s has no Id",
			systemPath,
		)
	}

	return &system, nil
}

// ImportServer validates that the Fujitsu iRMC is reachable and
// exposes the expected Redfish Systems resource.
func (c *FujitsuClient) ImportServer(
	hostname string,
	ip metalv1alpha1.IP,
	bmcUser string,
	bmcPassword string,
) error {
	devices, err := c.ListServers()
	if err != nil {
		return fmt.Errorf(
			"error validating Fujitsu server %s: %w",
			hostname,
			err,
		)
	}

	if len(devices) == 0 {
		return fmt.Errorf(
			"no Redfish systems found on Fujitsu iRMC %s",
			ip.String(),
		)
	}

	return nil
}

// RemoveServer removes the server from the metal-api management
// workflow.
func (c *FujitsuClient) RemoveServer(
	hostname string,
	ip metalv1alpha1.IP,
) error {
	// There is no Fujitsu Redfish operation corresponding to the
	// management-appliance remove/unmanage operation implemented
	// by other vendors.
	return nil
}

// GetAuthToken is not needed because Fujitsu iRMC uses
// HTTP Basic Authentication.
func (c *FujitsuClient) GetAuthToken() (string, error) {
	return "", nil
}

// ImportServerAsync wraps the synchronous Fujitsu import operation.
func (c *FujitsuClient) ImportServerAsync(
	hostname string,
	ip metalv1alpha1.IP,
	bmcUser string,
	bmcPassword string,
) (string, error) {
	if err := c.ImportServer(
		hostname,
		ip,
		bmcUser,
		bmcPassword,
	); err != nil {
		return "", err
	}

	return "", nil
}

// RemoveServerAsync wraps the synchronous Fujitsu remove operation.
func (c *FujitsuClient) RemoveServerAsync(
	hostname string,
	ip metalv1alpha1.IP,
) (string, error) {
	if err := c.RemoveServer(hostname, ip); err != nil {
		return "", err
	}

	return "", nil
}

// GetJobStatus handles the synchronous Fujitsu operation model.
func (c *FujitsuClient) GetJobStatus(jobID string) (*JobInfo, error) {
	return &JobInfo{
		JobID:    jobID,
		Status:   jobStatusCompleted,
		Progress: 100,
		Message:  "Synchronous operation completed",
	}, nil
}

// IsJobComplete returns true for Fujitsu operations because they
// are synchronous.
func (c *FujitsuClient) IsJobComplete(jobInfo *JobInfo) bool {
	return true
}

// IsJobSuccessful returns true for a completed synchronous
// Fujitsu operation.
func (c *FujitsuClient) IsJobSuccessful(jobInfo *JobInfo) bool {
	return true
}
