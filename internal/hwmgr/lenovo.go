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

type ServerSecurityDescriptor struct {
	ManagedAuthEnabled   bool `json:"managedAuthEnabled"`
	ManagedAuthSupported bool `json:"managedAuthSupported"`
}

// ServerDiscoveryRequest (redefined here for completeness)
type ServerManageRequest struct {
	IPAddresses        []string                 `json:"ipAddresses"`
	Username           string                   `json:"username"`
	Password           string                   `json:"password"`
	Type               string                   `json:"type"`
	SecurityDescriptor ServerSecurityDescriptor `json:"securityDescriptor"`
}

type ServerUnmanageRequest struct {
	IPAddresses []string `json:"ipAddresses"`
	Type        string   `json:"type"`
	UUID        string   `json:"uuid"`
}

// NodeListResponse mirrors the expected LXCA JSON response for GET /nodes
type NodeListResponse struct {
	NodeList []ServerNode `json:"nodeList"`
}

// ServerNode represents a managed server/node object (simplified structure)
type ServerNode struct {
	UUID        string `json:"uuid"`
	Name        string `json:"name"`
	Type        string `json:"type"` // e.g., "RackServer", "ComputeNode"
	HostName    string `json:"hostName"`
	HealthState string `json:"healthState"` // e.g., "Normal", "Warning", "Critical"
}

type SessionResponse struct {
	Response struct {
		Session struct {
			ID                string `json:"id"`
			CSRF              string `json:"csrf"`
			UserId            string `json:"UserId"`
			InactivityTimeout string `json:"inactivityTimeout"`
		} `json:"session"`
	} `json:"response"`
	Result   string `json:"result"`
	Messages []struct {
		Explanation string `json:"explanation"`
		ID          string `json:"id"`
		Recovery    struct {
			Text string `json:"text"`
			URL  string `json:"URL"`
		} `json:"recovery"`
		Text string `json:"text"`
	} `json:"messages"`
}

type LenovoClient struct {
	client *client
}

func NewLenovoClient(options ClientOptions) (c *LenovoClient, err error) {
	c = &LenovoClient{}
	c.client, err = NewClient(options)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (c *LenovoClient) ImportServer(hostname string, IP metalv1alpha1.IP, bmcUser, bmcPassword string) error {
	discoveryURL := c.client.parsedURL.JoinPath("/manageRequest?discovery=true")
	discoveryPayload := ServerManageRequest{
		IPAddresses: []string{IP.String()},
		Username:    bmcUser,
		Password:    bmcPassword,
		Type:        "Rack-Tower Server",
		SecurityDescriptor: ServerSecurityDescriptor{
			ManagedAuthEnabled:   false,
			ManagedAuthSupported: false,
		},
	}
	payloadBytes, err := json.Marshal(discoveryPayload)
	if err != nil {
		return fmt.Errorf("error marshalling discovery payload: %w", err)
	}
	req, err := http.NewRequest("POST", discoveryURL.String(), bytes.NewBuffer(payloadBytes))
	if err != nil {
		return fmt.Errorf("error creating discovery request: %w", err)
	}
	body, err := c.client.DoRequest(req, []int{202})
	if err != nil {
		return err
	}
	_ = body
	return nil
}

func (c *LenovoClient) RemoveServer(hostname string, ip metalv1alpha1.IP) error {
	servers, err := c.ListServers()
	if err != nil {
		return fmt.Errorf("error listing servers: %w", err)
	}
	serverUUID := ""
	for _, server := range servers {
		if server.Hostname == hostname {
			// For Lenovo, we need the actual UUID from the node
			// Since Device.UUID field exists, we can use it
			serverUUID = server.UUID
			break
		}
	}
	if serverUUID == "" {
		return fmt.Errorf("server with hostname %s not found", hostname)
	}
	url := c.client.parsedURL.JoinPath("/unmanageRequest")
	payload := ServerUnmanageRequest{
		IPAddresses: []string{ip.String()},
		Type:        "Rack-Tower Server",
		UUID:        serverUUID,
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("error marshalling unmanage payload: %w", err)
	}
	req, err := http.NewRequest("POST", url.String(), bytes.NewBuffer(payloadBytes))
	if err != nil {
		return fmt.Errorf("error creating unmanage request: %w", err)
	}
	_, err = c.client.DoRequest(req, []int{202})
	if err != nil {
		return fmt.Errorf("error executing unmanage request: %w", err)
	}

	return nil
}

func (c *LenovoClient) ListServers() ([]Device, error) {
	serversURL := c.client.parsedURL.JoinPath("/nodes?status=managed&includeAttributes=uuid,fqdn")

	req, err := http.NewRequest("GET", serversURL.String(), nil)
	if err != nil {
		return []Device{}, fmt.Errorf("error creating list servers request: %w", err)
	}

	body, err := c.client.DoRequest(req, []int{http.StatusOK})
	if err != nil {
		return []Device{}, fmt.Errorf("error executing list servers request: %w", err)
	}

	var nodeListResp NodeListResponse
	if err := json.Unmarshal(body, &nodeListResp); err != nil {
		return []Device{}, fmt.Errorf("error parsing list servers response: %w", err)
	}
	devices := make([]Device, 0, len(nodeListResp.NodeList))
	for _, node := range nodeListResp.NodeList {
		device := Device{
			UUID:     node.UUID,
			Name:     node.Name,
			Hostname: node.HostName,
			Model:    node.Type,
			// HealthStatus mapping can be added based on HealthState
		}
		devices = append(devices, device)
	}

	return devices, nil
}

// LoginRequest defines the JSON structure for the login payload.
type LoginRequest struct {
	UserID   string `json:"userID"`
	Password string `json:"password"`
}

func (c *LenovoClient) GetAuthToken() (string, error) {
	url := c.client.parsedURL.JoinPath("/sessions")
	if c.client.token != "" {
		// check token still valid
		req, err := http.NewRequest("GET", url.String(), nil)
		if err != nil {
			return "", fmt.Errorf("error creating login validation request: %w", err)
		}
		_, err = c.client.DoRequest(req, []int{http.StatusOK})
		if err != nil {
			return c.createToken()
		}
		return c.client.token, nil
	}
	return c.createToken()
}

func (c *LenovoClient) createToken() (string, error) {
	url := c.client.parsedURL.JoinPath("/sessions")
	loginPayload := LoginRequest{
		UserID:   c.client.username,
		Password: c.client.password,
	}
	payloadBytes, err := json.Marshal(loginPayload)
	if err != nil {
		return "", fmt.Errorf("error marshalling login payload: %w", err)
	}
	req, err := http.NewRequest("POST", url.String(), bytes.NewBuffer(payloadBytes))
	if err != nil {
		return "", fmt.Errorf("error creating login request: %w", err)
	}
	body, err := c.client.DoRequest(req, []int{http.StatusOK})
	if err != nil {
		return "", fmt.Errorf("error executing login request: %w", err)
	}
	var resp SessionResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("error parsing login response: %w", err)
	}
	c.client.token = resp.Response.Session.CSRF

	return c.client.token, nil
}

// ImportServerAsync initiates an asynchronous import and returns a job identifier.
func (c *LenovoClient) ImportServerAsync(hostname string, IP metalv1alpha1.IP, bmcUser, bmcPassword string) (string, error) {
	discoveryURL := c.client.parsedURL.JoinPath("/manageRequest?discovery=true")
	discoveryPayload := ServerManageRequest{
		IPAddresses: []string{IP.String()},
		Username:    bmcUser,
		Password:    bmcPassword,
		Type:        "Rack-Tower Server",
		SecurityDescriptor: ServerSecurityDescriptor{
			ManagedAuthEnabled:   false,
			ManagedAuthSupported: false,
		},
	}
	payloadBytes, err := json.Marshal(discoveryPayload)
	if err != nil {
		return "", fmt.Errorf("error marshalling discovery payload: %w", err)
	}
	req, err := http.NewRequest("POST", discoveryURL.String(), bytes.NewBuffer(payloadBytes))
	if err != nil {
		return "", fmt.Errorf("error creating discovery request: %w", err)
	}
	body, err := c.client.DoRequest(req, []int{202})
	if err != nil {
		return "", err
	}

	// Try to parse the response for a job/task ID
	var response map[string]interface{}
	if err := json.Unmarshal(body, &response); err == nil {
		// Check for common job ID fields
		if jobID, ok := response["jobId"].(string); ok && jobID != "" {
			return jobID, nil
		}
		if taskID, ok := response["taskId"].(string); ok && taskID != "" {
			return taskID, nil
		}
	}

	// If no job ID in response, use hostname as identifier for polling
	return hostname, nil
}

// RemoveServerAsync initiates an asynchronous remove operation.
func (c *LenovoClient) RemoveServerAsync(hostname string, ip metalv1alpha1.IP) (string, error) {
	// Lenovo's unmanage is synchronous, return empty job ID
	err := c.RemoveServer(hostname, ip)
	return "", err
}

// GetJobStatus retrieves the status of a Lenovo import job by polling managed nodes.
func (c *LenovoClient) GetJobStatus(jobID string) (*JobInfo, error) {
	// For Lenovo, we poll the /nodes endpoint to check if the hostname appears
	serversURL := c.client.parsedURL.JoinPath("/nodes?status=managed&includeAttributes=uuid,fqdn")

	req, err := http.NewRequest("GET", serversURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("error creating nodes request: %w", err)
	}

	body, err := c.client.DoRequest(req, []int{http.StatusOK})
	if err != nil {
		return nil, fmt.Errorf("error executing nodes request: %w", err)
	}

	var nodeListResp NodeListResponse
	if err := json.Unmarshal(body, &nodeListResp); err != nil {
		return nil, fmt.Errorf("error parsing nodes response: %w", err)
	}

	// Check if the hostname (jobID) appears in the managed nodes
	for _, node := range nodeListResp.NodeList {
		if node.HostName == jobID {
			return &JobInfo{
				JobID:    jobID,
				Status:   "completed",
				Progress: 100,
				Message:  "Server imported successfully",
			}, nil
		}
	}

	// Not found yet, still in progress
	return &JobInfo{
		JobID:    jobID,
		Status:   "running",
		Progress: 50,
		Message:  "Import in progress",
	}, nil
}

// IsJobComplete returns true if the Lenovo job is no longer running.
func (c *LenovoClient) IsJobComplete(jobInfo *JobInfo) bool {
	return jobInfo.Status == "completed"
}

// IsJobSuccessful returns true if the Lenovo job completed successfully.
func (c *LenovoClient) IsJobSuccessful(jobInfo *JobInfo) bool {
	return jobInfo.Status == "completed"
}
