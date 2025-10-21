package servermanagement

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

// ServerDiscoveryRequest (redefined here for completeness)
type ServerDiscoveryRequest struct {
	IPAddress     string `json:"ipAddress"`
	UserID        string `json:"userID"`
	Password      string `json:"password"`
	ConnectionSet string `json:"connectionSet"`
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
	return
}

func (c *LenovoClient) ImportServer(hostname string, IP metalv1alpha1.IP) error {
	discoveryURL := c.client.parsedURL.JoinPath("/a/discover")
	discoveryPayload := ServerDiscoveryRequest{
		IPAddress:     IP.String(),
		UserID:        c.client.username,
		Password:      c.client.password,
		ConnectionSet: "Default",
	}
	payloadBytes, err := json.Marshal(discoveryPayload)
	if err != nil {
		return fmt.Errorf("error marshalling discovery payload: %w", err)
	}
	req, err := http.NewRequest("POST", discoveryURL.String(), bytes.NewBuffer(payloadBytes))
	body, err := c.client.DoRequest(req, []int{202})
	if err != nil {
		return err
	}
	_ = body
	return nil
}

func (c *LenovoClient) RemoveServer(hostname string) error {
	return nil
}

func (c *LenovoClient) ListServers() ([]Device, error) {
	serversURL := c.client.parsedURL.JoinPath("/nodes")
	var devices []Device

	req, err := http.NewRequest("GET", serversURL.String(), nil)
	if err != nil {
		return devices, fmt.Errorf("error creating list servers request: %w", err)
	}

	body, err := c.client.DoRequest(req, []int{http.StatusOK})
	if err != nil {
		return devices, fmt.Errorf("error executing list servers request: %w", err)
	}

	var nodeListResp NodeListResponse
	if err := json.Unmarshal(body, &nodeListResp); err != nil {
		return devices, fmt.Errorf("error parsing list servers response: %w", err)
	}
	for _, node := range nodeListResp.NodeList {
		device := Device{
			ID:       0, // Lenovo LXCA does not provide a numeric ID in this example
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
