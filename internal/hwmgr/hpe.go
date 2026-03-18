// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package hwmgr

import (
	"fmt"

	"github.com/HewlettPackard/oneview-golang/ov" // This is a common SDK path
	"github.com/HewlettPackard/oneview-golang/utils"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

type HPEClient struct {
	client *ov.OVClient
}

func NewHPEClient(options ClientOptions) (c *HPEClient, err error) {
	c = &HPEClient{}
	baseClient := &ov.OVClient{}
	ovc := baseClient.NewOVClient(
		options.Username,
		options.Password,
		options.Domain,
		options.Endpoint,
		options.InsecureSkipVerify,
		0, // 0 for auto-detect API version
		"",
	)
	ovc.APIKey = options.Token
	c.client = ovc
	return
}

func (c *HPEClient) ImportServer(hostname string, IP metalv1alpha1.IP, bmcUser, bmcPassword string) error {
	scp, err := c.client.GetScopeByName("ScopeHardware")
	if err != nil {
		return fmt.Errorf("error getting scope: %w", err)
	}
	rackServer := ov.ServerHardware{
		Name:               hostname,
		Username:           bmcUser,
		Password:           bmcPassword,
		Force:              false,
		LicensingIntent:    "OneView", // OneView or OneViewNoiLO for Managed
		ConfigurationState: "Managed",
		InitialScopeUris:   []utils.Nstring{scp.URI},
	}
	_, err = c.client.AddRackServer(rackServer)
	return err
}

func (c *HPEClient) RemoveServer(hostname string, ip metalv1alpha1.IP) error {
	server, err := c.client.GetServerHardwareByName(hostname)
	if err != nil {
		return err
	}
	return c.client.DeleteServerHardware(server.URI)
}

func (c *HPEClient) ListServers() ([]Device, error) {
	filters := []string{""}
	hpeServers, err := c.client.GetServerHardwareList(filters, "", "", "", "")
	if err != nil {
		return []Device{}, err
	}
	devices := make([]Device, 0, len(hpeServers.Members))
	for _, srv := range hpeServers.Members {
		device := Device{
			// ID:       srv.UUID.String(),
			Hostname: srv.Hostname,
			Name:     srv.Name,
			Model:    srv.Model,
		}
		devices = append(devices, device)
	}
	return devices, nil
}

func (c *HPEClient) GetAuthToken() (string, error) {
	_, err := c.client.GetIdleTimeout()
	if err != nil {
		if err := c.client.RefreshLogin(); err != nil {
			return "", err
		}
	}
	return c.client.APIKey, nil
}

// ImportServerAsync initiates an import operation.
// HPE AddRackServer is synchronous, so this returns an empty job ID.
func (c *HPEClient) ImportServerAsync(hostname string, IP metalv1alpha1.IP, bmcUser, bmcPassword string) (string, error) {
	err := c.ImportServer(hostname, IP, bmcUser, bmcPassword)
	return "", err
}

// RemoveServerAsync initiates a remove operation.
// HPE DeleteServerHardware is synchronous, so this returns an empty job ID.
func (c *HPEClient) RemoveServerAsync(hostname string, ip metalv1alpha1.IP) (string, error) {
	err := c.RemoveServer(hostname, ip)
	return "", err
}

// GetJobStatus retrieves the status of an HPE operation.
// Since HPE operations are synchronous, this always returns completed.
func (c *HPEClient) GetJobStatus(jobID string) (*JobInfo, error) {
	return &JobInfo{
		JobID:    "",
		Status:   "completed",
		Progress: 100,
		Message:  "Synchronous operation completed",
	}, nil
}

// IsJobComplete returns true for HPE operations (always synchronous).
func (c *HPEClient) IsJobComplete(jobInfo *JobInfo) bool {
	return true
}

// IsJobSuccessful returns true for HPE operations (always synchronous).
func (c *HPEClient) IsJobSuccessful(jobInfo *JobInfo) bool {
	return true
}
