// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package hwmgr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// LXCA task status strings observed on /tasks/{id}. LXCA reports these under
// the `status` field of a task record. We categorize them into "success",
// "failed", and "in progress" buckets that the controller compares against
// hwmgr.FirmwareJobStatus* constants.
const (
	lxcaTaskStatusComplete  = "Complete"
	lxcaTaskStatusSucceeded = "Succeeded"
	lxcaTaskStatusFailed    = "Failed"
	lxcaTaskStatusAborted   = "Aborted"
	lxcaTaskStatusCancelled = "Cancelled"
	lxcaTaskStatusStopped   = "Stopped"
	lxcaTaskStatusWarning   = "Warning"
)

// FirmwareJobStatus is the vendor-neutral status string the firmware
// controller consumes when polling firmware update / repository / policy jobs.
type FirmwareJobStatus string

const (
	// FirmwareJobStatusSuccess indicates the job finished successfully.
	FirmwareJobStatusSuccess FirmwareJobStatus = "Success"
	// FirmwareJobStatusFailed indicates the job finished with an error.
	FirmwareJobStatusFailed FirmwareJobStatus = "Failed"
	// FirmwareJobStatusInProgress indicates the job is still running.
	FirmwareJobStatusInProgress FirmwareJobStatus = "InProgress"
)

// ClassifyLXCAStatus maps an LXCA task `status` string to a FirmwareJobStatus.
func ClassifyLXCAStatus(status string) FirmwareJobStatus {
	switch status {
	case lxcaTaskStatusComplete, lxcaTaskStatusSucceeded:
		return FirmwareJobStatusSuccess
	case lxcaTaskStatusFailed, lxcaTaskStatusAborted,
		lxcaTaskStatusCancelled, lxcaTaskStatusStopped, lxcaTaskStatusWarning:
		return FirmwareJobStatusFailed
	default:
		return FirmwareJobStatusInProgress
	}
}

// LXCACompliancePolicy is the minimal shape the firmware controller needs
// when listing existing LXCA compliance policies.
type LXCACompliancePolicy struct {
	// ID is the policy identifier LXCA assigns.
	ID string `json:"id,omitempty"`
	// Name is the human-readable policy name; keys reuse across reconciles.
	Name string `json:"policyName,omitempty"`
	// Description is passed through verbatim.
	Description string `json:"description,omitempty"`
}

// lxcaCompliancePolicyList wraps the response of GET /compliancePolicies.
type lxcaCompliancePolicyList struct {
	PolicyList []LXCACompliancePolicy `json:"policyList,omitempty"`
	// Some LXCA versions return the list directly at the top level.
	Policies []LXCACompliancePolicy `json:"policies,omitempty"`
}

// lxcaImportRequest is the body of POST /files/updateRepositories/firmware/import.
type lxcaImportRequest struct {
	Files []lxcaImportFile `json:"files"`
}

type lxcaImportFile struct {
	URL      string `json:"url"`
	Checksum string `json:"checksum,omitempty"`
}

// lxcaRepositoryStatus is the body of GET /updateRepositories/firmware/status.
type lxcaRepositoryStatus struct {
	Status   string `json:"status,omitempty"`
	Progress int    `json:"progress,omitempty"`
	Message  string `json:"message,omitempty"`
}

// lxcaCompliancePolicyCreate is the body of POST /compliancePolicies.
type lxcaCompliancePolicyCreate struct {
	PolicyName  string `json:"policyName"`
	Description string `json:"description,omitempty"`
}

// lxcaCompliancePolicyAssign is the body of POST /compliancePolicies/compareResult.
// LXCA expects `endpoints` populated with device UUIDs.
type lxcaCompliancePolicyAssign struct {
	PolicyName string             `json:"policyName"`
	Endpoints  []lxcaEndpointUUID `json:"endpoints"`
}

type lxcaEndpointUUID struct {
	UUID string `json:"uuid"`
	Type string `json:"type,omitempty"`
}

// lxcaApplyRequest is the body of POST /updatableComponents.
type lxcaApplyRequest struct {
	Activation string           `json:"activation"`
	OnError    string           `json:"onError,omitempty"`
	DeviceList []lxcaApplyEntry `json:"deviceList"`
}

type lxcaApplyEntry struct {
	UUID       string `json:"uuid"`
	PolicyName string `json:"policyName"`
}

// lxcaApplyResponse is the response of POST /updatableComponents; it
// contains the LXCA task id we later poll on /tasks/{id}.
type lxcaApplyResponse struct {
	JobID  string `json:"jobID,omitempty"`
	TaskID string `json:"taskID,omitempty"`
}

// lxcaTask is the shape of GET /tasks/{id} we care about.
type lxcaTask struct {
	ID       string `json:"id,omitempty"`
	Status   string `json:"status,omitempty"`
	Progress int    `json:"progress,omitempty"`
	Message  string `json:"progressMessage,omitempty"`
}

// ImportFirmwarePayload asks LXCA to download a UXSP or firmware bundle
// from `url` and stage it in its firmware repository. Returns the task id
// LXCA returned (may be empty if LXCA responds without one — the caller
// should then poll GetRepositoryStatus).
func (c *LenovoClient) ImportFirmwarePayload(url, checksum string) (string, error) {
	if url == "" {
		return "", fmt.Errorf("firmware payload url is required")
	}
	endpoint := c.client.parsedURL.JoinPath("/files/updateRepositories/firmware/import")
	body := lxcaImportRequest{Files: []lxcaImportFile{{URL: url, Checksum: checksum}}}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", fmt.Errorf("error marshalling import payload: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint.String(), bytes.NewBuffer(payload))
	if err != nil {
		return "", fmt.Errorf("error creating import request: %w", err)
	}
	respBody, err := c.client.DoRequest(req, []int{http.StatusOK, http.StatusAccepted})
	if err != nil {
		return "", fmt.Errorf("error executing import request: %w", err)
	}
	var resp map[string]any
	_ = json.Unmarshal(respBody, &resp)
	if id, ok := resp["jobID"].(string); ok && id != "" {
		return id, nil
	}
	if id, ok := resp["taskID"].(string); ok && id != "" {
		return id, nil
	}
	return "", nil
}

// GetRepositoryStatus polls LXCA for the firmware repository import status.
func (c *LenovoClient) GetRepositoryStatus() (*JobInfo, error) {
	endpoint := c.client.parsedURL.JoinPath("/updateRepositories/firmware/status")
	req, err := http.NewRequest(http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("error creating repository status request: %w", err)
	}
	body, err := c.client.DoRequest(req, []int{http.StatusOK})
	if err != nil {
		return nil, fmt.Errorf("error executing repository status request: %w", err)
	}
	var status lxcaRepositoryStatus
	if err := json.Unmarshal(body, &status); err != nil {
		return nil, fmt.Errorf("error parsing repository status: %w", err)
	}
	return &JobInfo{
		Status:   string(ClassifyLXCAStatus(status.Status)),
		Progress: status.Progress,
		Message:  status.Message,
	}, nil
}

// ListCompliancePolicies fetches LXCA's compliance policies. Callers should
// key on Name to reuse an existing policy.
func (c *LenovoClient) ListCompliancePolicies() ([]LXCACompliancePolicy, error) {
	endpoint := c.client.parsedURL.JoinPath("/compliancePolicies")
	req, err := http.NewRequest(http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("error creating list policies request: %w", err)
	}
	body, err := c.client.DoRequest(req, []int{http.StatusOK})
	if err != nil {
		return nil, fmt.Errorf("error executing list policies request: %w", err)
	}
	var list lxcaCompliancePolicyList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("error parsing policies response: %w", err)
	}
	if len(list.PolicyList) > 0 {
		return list.PolicyList, nil
	}
	return list.Policies, nil
}

// CreateCompliancePolicy creates a new LXCA compliance policy and returns
// its ID.
func (c *LenovoClient) CreateCompliancePolicy(name, description string) (string, error) {
	endpoint := c.client.parsedURL.JoinPath("/compliancePolicies")
	payload, err := json.Marshal(lxcaCompliancePolicyCreate{
		PolicyName:  name,
		Description: description,
	})
	if err != nil {
		return "", fmt.Errorf("error marshalling policy payload: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint.String(), bytes.NewBuffer(payload))
	if err != nil {
		return "", fmt.Errorf("error creating policy request: %w", err)
	}
	body, err := c.client.DoRequest(req, []int{http.StatusOK, http.StatusCreated, http.StatusAccepted})
	if err != nil {
		return "", fmt.Errorf("error executing policy request: %w", err)
	}
	var resp LXCACompliancePolicy
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("error parsing policy response: %w", err)
	}
	if resp.ID == "" {
		// LXCA may not echo back the ID; fall back to the name (which is
		// unique on the appliance) so the controller can still reuse it.
		resp.ID = name
	}
	return resp.ID, nil
}

// AssignCompliancePolicy attaches the policy identified by `policyName` to
// the given device UUIDs.
func (c *LenovoClient) AssignCompliancePolicy(policyName string, deviceUUIDs []string) error {
	if policyName == "" {
		return fmt.Errorf("policyName is required")
	}
	if len(deviceUUIDs) == 0 {
		return nil
	}
	endpoint := c.client.parsedURL.JoinPath("/compliancePolicies/compareResult")
	endpoints := make([]lxcaEndpointUUID, 0, len(deviceUUIDs))
	for _, uuid := range deviceUUIDs {
		endpoints = append(endpoints, lxcaEndpointUUID{UUID: uuid, Type: "Server"})
	}
	payload, err := json.Marshal(lxcaCompliancePolicyAssign{
		PolicyName: policyName,
		Endpoints:  endpoints,
	})
	if err != nil {
		return fmt.Errorf("error marshalling policy assignment: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint.String(), bytes.NewBuffer(payload))
	if err != nil {
		return fmt.Errorf("error creating policy assignment request: %w", err)
	}
	_, err = c.client.DoRequest(req, []int{http.StatusOK, http.StatusAccepted, http.StatusNoContent})
	if err != nil {
		return fmt.Errorf("error executing policy assignment request: %w", err)
	}
	return nil
}

// ApplyFirmwareUpdate submits a firmware update job that targets the given
// device UUIDs under the compliance policy `policyName`. Returns the LXCA
// task id which the caller polls via GetTaskStatus.
func (c *LenovoClient) ApplyFirmwareUpdate(deviceUUIDs []string, policyName, activation string) (string, error) {
	if len(deviceUUIDs) == 0 {
		return "", fmt.Errorf("at least one device UUID is required")
	}
	if activation == "" {
		activation = "Immediate"
	}
	endpoint := c.client.parsedURL.JoinPath("/updatableComponents")
	deviceList := make([]lxcaApplyEntry, 0, len(deviceUUIDs))
	for _, uuid := range deviceUUIDs {
		deviceList = append(deviceList, lxcaApplyEntry{UUID: uuid, PolicyName: policyName})
	}
	payload, err := json.Marshal(lxcaApplyRequest{
		Activation: activation,
		OnError:    "stopOnError",
		DeviceList: deviceList,
	})
	if err != nil {
		return "", fmt.Errorf("error marshalling apply payload: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, endpoint.String(), bytes.NewBuffer(payload))
	if err != nil {
		return "", fmt.Errorf("error creating apply request: %w", err)
	}
	body, err := c.client.DoRequest(req, []int{http.StatusOK, http.StatusAccepted})
	if err != nil {
		return "", fmt.Errorf("error executing apply request: %w", err)
	}
	var resp lxcaApplyResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("error parsing apply response: %w", err)
	}
	if resp.JobID != "" {
		return resp.JobID, nil
	}
	if resp.TaskID != "" {
		return resp.TaskID, nil
	}
	// Some LXCA versions embed the id under a non-standard key; fall through
	// to a generic map decode so we don't fail on cosmetic response shape
	// changes.
	var generic map[string]any
	if err := json.Unmarshal(body, &generic); err == nil {
		for _, k := range []string{"jobId", "taskId", "id"} {
			if v, ok := generic[k].(string); ok && v != "" {
				return v, nil
			}
		}
	}
	return "", fmt.Errorf("apply firmware update: LXCA response missing task id")
}

// GetTaskStatus polls a single LXCA task by id.
func (c *LenovoClient) GetTaskStatus(taskID string) (*JobInfo, error) {
	if taskID == "" {
		return nil, fmt.Errorf("taskID is required")
	}
	endpoint := c.client.parsedURL.JoinPath("/tasks/" + taskID)
	req, err := http.NewRequest(http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("error creating task status request: %w", err)
	}
	body, err := c.client.DoRequest(req, []int{http.StatusOK})
	if err != nil {
		return nil, fmt.Errorf("error executing task status request: %w", err)
	}
	// LXCA sometimes returns a single object, sometimes wraps it in a list.
	// Try both.
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var list []lxcaTask
		if err := json.Unmarshal(body, &list); err != nil {
			return nil, fmt.Errorf("error parsing task list: %w", err)
		}
		if len(list) == 0 {
			return nil, fmt.Errorf("task %s not found", taskID)
		}
		return jobInfoFromLXCATask(list[0], taskID), nil
	}
	var t lxcaTask
	if err := json.Unmarshal(body, &t); err != nil {
		return nil, fmt.Errorf("error parsing task: %w", err)
	}
	return jobInfoFromLXCATask(t, taskID), nil
}

func jobInfoFromLXCATask(t lxcaTask, fallbackID string) *JobInfo {
	id := t.ID
	if id == "" {
		id = fallbackID
	}
	return &JobInfo{
		JobID:    id,
		Status:   string(ClassifyLXCAStatus(t.Status)),
		Progress: t.Progress,
		Message:  strings.TrimSpace(t.Message),
	}
}

// CancelTask asks LXCA to cancel a running task.
func (c *LenovoClient) CancelTask(taskID string) error {
	if taskID == "" {
		return nil
	}
	endpoint := c.client.parsedURL.JoinPath("/tasks/" + taskID)
	req, err := http.NewRequest(http.MethodDelete, endpoint.String(), nil)
	if err != nil {
		return fmt.Errorf("error creating cancel task request: %w", err)
	}
	_, err = c.client.DoRequest(req, []int{http.StatusOK, http.StatusAccepted, http.StatusNoContent})
	if err != nil {
		return fmt.Errorf("error executing cancel task request: %w", err)
	}
	return nil
}

// CloseSession tears down the LXCA session identified by the client's
// current token. Safe to call multiple times.
func (c *LenovoClient) CloseSession() error {
	if c.client == nil || c.client.token == "" {
		return nil
	}
	endpoint := c.client.parsedURL.JoinPath("/sessions")
	req, err := http.NewRequest(http.MethodDelete, endpoint.String(), nil)
	if err != nil {
		return fmt.Errorf("error creating close session request: %w", err)
	}
	if _, err := c.client.DoRequest(req, []int{
		http.StatusOK, http.StatusNoContent, http.StatusUnauthorized,
	}); err != nil {
		return fmt.Errorf("error executing close session request: %w", err)
	}
	c.client.token = ""
	return nil
}

// LookupDeviceUUID resolves a hostname to the UUID LXCA has for the node.
// Returns an empty string if the node is not currently managed.
func (c *LenovoClient) LookupDeviceUUID(hostname string) (string, error) {
	if hostname == "" {
		return "", nil
	}
	devices, err := c.ListServers()
	if err != nil {
		return "", err
	}
	for _, d := range devices {
		if strings.EqualFold(d.Hostname, hostname) || strings.EqualFold(d.Name, hostname) {
			return d.UUID, nil
		}
	}
	return "", nil
}
