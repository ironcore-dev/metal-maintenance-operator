// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package hwmgr

import (
	"fmt"
	"strings"

	"github.com/HewlettPackard/oneview-golang/ov"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

const jobStatusCompleted = "completed"

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
	rackServer := ov.ServerHardware{
		Hostname:           IP.String(),
		Username:           bmcUser,
		Password:           bmcPassword,
		Force:              false,
		LicensingIntent:    "OneView",
		ConfigurationState: "Managed",
	}
	_, err := c.client.AddRackServer(rackServer)
	if err != nil && strings.Contains(err.Error(), "has already been added") {
		return nil
	}
	return err
}

func (c *HPEClient) RemoveServer(hostname string, ip metalv1alpha1.IP) error {
	server, err := c.client.GetServerHardwareByName(hostname)
	if err != nil {
		return err
	}
	if err := c.client.DeleteServerHardware(server.URI); err != nil {
		if strings.Contains(err.Error(), "has an active profile") {
			return ErrServerHasActiveProfile
		}
		return err
	}
	return nil
}

func (c *HPEClient) ListServers() ([]Device, error) {
	var devices []Device
	start := 0
	pageSize := 100
	for {
		hpeServers, err := c.client.GetServerHardwareList([]string{""}, "", fmt.Sprintf("%d", start), fmt.Sprintf("%d", pageSize), "")
		if err != nil {
			return nil, err
		}
		for _, srv := range hpeServers.Members {
			devices = append(devices, Device{
				Hostname: srv.Name,
				Name:     srv.Name,
				Model:    srv.Model,
			})
		}
		// use Total if available, otherwise fall back to member count heuristic
		if hpeServers.Total > 0 {
			if len(devices) >= hpeServers.Total {
				break
			}
		} else if len(hpeServers.Members) < pageSize {
			break
		}
		start += pageSize
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
		Status:   jobStatusCompleted,
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
