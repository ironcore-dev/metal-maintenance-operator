// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package hwmgr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

type DellClient struct {
	client client
}

// AuthRequest is used to get the X-Auth-Token
type AuthRequest struct {
	UserName string `json:"UserName"`
	Password string `json:"Password"`
}

// AuthResponse contains the session token
type AuthResponse struct {
	Token string `json:"Token"`
}

// DevicesResponse is the top-level structure for the GET /Devices endpoint
type DevicesResponse struct {
	ODataContext string   `json:"@odata.context"`
	ODataCount   int      `json:"@odata.count"`
	Value        []Device `json:"value"`
}

// Credential is for the iDRAC login details
type Credential struct {
	CredentialType int    `json:"CredentialType"` // 1 for username/password
	UserName       string `json:"UserName"`
	Password       string `json:"Password"`
}

// Address is for the iDRAC IP
type Address struct {
	AddressType int    `json:"AddressType"` // 1 for IPv4
	Address     string `json:"Address"`
}

// TargetType specifies the device type (iDRAC) and addresses
type TargetType struct {
	TargetTypeID int       `json:"TargetTypeID"` // 1 for iDRAC
	Addresses    []Address `json:"Addresses"`
}

// DiscoveryJobRequest is the main payload to start the discovery job
type DiscoveryJobRequest struct {
	JobName            string       `json:"JobName"`
	ConnectionProfiles []Credential `json:"ConnectionProfiles"`
	TargetTypes        []TargetType `json:"TargetTypes"`
	JobType            int          `json:"JobType"` // 1 for immediate discovery
}

type RemoveDeviceRequest struct {
	DeviceIDs []int `json:"DeviceIDs"`
}

// DiscoveryJob represents a single discovery job in OpenManage Enterprise.
type DiscoveryJob struct {
	// OData Fields
	ODataType string `json:"@odata.type"`

	// Job Identification and Metadata
	Id          int    `json:"Id"`
	Name        string `json:"Name"`
	Description string `json:"Description"`

	// Status and Progress
	StartTime string `json:"StartTime"` // ISO 8601 format date/time
	EndTime   string `json:"EndTime"`   // Null if job is running
	Status    int    `json:"Status"`    // Status code (e.g., 3001 = Completed, 3002 = Running)
	Progress  int    `json:"Progress"`  // Percentage of completion (0-100)
	State     string `json:"State"`     // A human-readable state string

	// Device Counts
	DeviceCount           int `json:"DeviceCount"`           // Total number of target addresses/devices
	DiscoveredDeviceCount int `json:"DiscoveredDeviceCount"` // Number of devices successfully discovered

	// Configuration Group Details
	ConfigGroup struct {
		Id   int    `json:"Id"`
		Name string `json:"Name"`
	} `json:"ConfigGroup"`
}

// DiscoveryJobsResponse represents the top-level response for a collection of discovery jobs.
type DiscoveryJobsResponse struct {
	ODataContext string         `json:"@odata.context"`
	ODataCount   int            `json:"@odata.count"`
	Value        []DiscoveryJob `json:"value"`
}

func NewDellClient(options ClientOptions) (*DellClient, error) {
	client, err := NewClient(options)
	if err != nil {
		return nil, err
	}
	return &DellClient{client: *client}, nil
}

func (c *DellClient) ImportServer(hostname string, IP metalv1alpha1.IP, bmcUser, bmcPassword string) error {
	discoveryURL := c.client.parsedURL.JoinPath("/api/DiscoveryService/DiscoveryJobs")
	discoveryPayload := DiscoveryJobRequest{
		JobName: "ImportServer-" + hostname,
		ConnectionProfiles: []Credential{
			{
				CredentialType: 1,
				UserName:       c.client.username,
				Password:       c.client.password,
			},
		},
		TargetTypes: []TargetType{
			{
				TargetTypeID: 1,
				Addresses: []Address{
					{
						AddressType: 1,
						Address:     IP.String(),
					},
				},
			},
		},
		JobType: 1,
	}
	payloadBytes, err := json.Marshal(discoveryPayload)
	if err != nil {
		return fmt.Errorf("error marshalling discovery payload: %w", err)
	}
	req, err := http.NewRequest("POST", discoveryURL.String(), bytes.NewBuffer(payloadBytes))
	if err != nil {
		return fmt.Errorf("error creating discovery request: %w", err)
	}
	body, err := c.client.DoRequest(req, []int{http.StatusCreated})
	if err != nil {
		return fmt.Errorf("error executing discovery request: %w", err)
	}

	var discoveryResp DiscoveryJobsResponse
	if err := json.Unmarshal(body, &discoveryResp); err != nil {
		return fmt.Errorf("error parsing discovery response: %w", err)
	}
	// Optionally, one could check the status of the discovery job here using discoveryResp.Value[0].Id

	return nil
}

func (c *DellClient) RemoveServer(hostname string, ip metalv1alpha1.IP) error {
	servers, err := c.ListServers()
	if err != nil {
		return fmt.Errorf("error listing servers: %w", err)
	}
	serverID := 0
	for _, server := range servers {
		if server.Hostname == hostname {
			serverID = server.ID
			break
		}
	}
	if serverID == 0 {
		return fmt.Errorf("server with hostname %s not found", hostname)
	}
	removeURL := c.client.parsedURL.JoinPath("/api/DeviceService/Actions/DeviceService.RemoveDevices")
	removePayload := RemoveDeviceRequest{
		DeviceIDs: []int{serverID},
	}
	payloadBytes, err := json.Marshal(removePayload)
	if err != nil {
		return fmt.Errorf("error marshalling remove payload: %w", err)
	}
	req, err := http.NewRequest("POST", removeURL.String(), bytes.NewBuffer(payloadBytes))
	if err != nil {
		return fmt.Errorf("error creating remove request: %w", err)
	}
	_, err = c.client.DoRequest(req, []int{http.StatusNoContent})
	if err != nil {
		return fmt.Errorf("error executing remove request: %w", err)
	}
	return nil
}

func (c *DellClient) ListServers() ([]Device, error) {
	serversURL := c.client.parsedURL.JoinPath("/api/DeviceService/Devices")

	req, err := http.NewRequest("GET", serversURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("error creating get servers request: %w", err)
	}
	body, err := c.client.DoRequest(req, []int{http.StatusOK})
	if err != nil {
		return nil, fmt.Errorf("error executing get servers request: %w", err)
	}
	var devicesResp DevicesResponse
	if err := json.Unmarshal(body, &devicesResp); err != nil {
		return nil, fmt.Errorf("error parsing get servers response: %w", err)
	}
	return devicesResp.Value, nil
}

func (c *DellClient) GetAuthToken() (string, error) {
	authURL := c.client.parsedURL.String() + "/api/SessionService/Sessions"
	if c.client.token != "" {
		// check token still valid
		req, err := http.NewRequest("GET", authURL, nil)
		if err != nil {
			return "", fmt.Errorf("error creating auth validation request: %w", err)
		}
		_, err = c.client.DoRequest(req, []int{http.StatusOK})
		if err != nil {
			return c.createToken()
		}
		return c.client.token, nil
	}
	return c.createToken()
}

func (c *DellClient) createToken() (string, error) {
	authURL := c.client.parsedURL.String() + "/api/SessionService/Sessions"
	authPayload := AuthRequest{
		UserName: c.client.username,
		Password: c.client.password,
	}
	payloadBytes, err := json.Marshal(authPayload)
	if err != nil {
		return "", fmt.Errorf("error marshalling auth payload: %w", err)
	}

	req, err := http.NewRequest("POST", authURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return "", fmt.Errorf("error creating auth request: %w", err)
	}

	respBody, err := c.client.DoRequest(req, []int{http.StatusCreated})
	if err != nil {
		return "", fmt.Errorf("error executing auth request: %w", err)
	}

	var authResp AuthResponse
	if err := json.Unmarshal(respBody, &authResp); err != nil {
		return "", fmt.Errorf("error parsing auth response: %w", err)
	}
	c.client.token = authResp.Token
	return authResp.Token, nil
}
