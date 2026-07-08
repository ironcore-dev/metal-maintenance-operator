// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package hwmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	neturl "net/url"
	"reflect"
	"strings"

	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// DellManagedState represents the management state of a Dell device.
type DellManagedState int

const (
	DellManagedStateManaged          DellManagedState = 3000
	DellManagedStateManagedWithAlert DellManagedState = 6000
)

// DellDeviceStatusCode represents the overall health status of a Dell device.
type DellDeviceStatusCode int

const (
	DellDeviceStatusNormal   DellDeviceStatusCode = 1000
	DellDeviceStatusUnknown  DellDeviceStatusCode = 2000
	DellDeviceStatusWarning  DellDeviceStatusCode = 3000
	DellDeviceStatusCritical DellDeviceStatusCode = 4000
	DellDeviceStatusNoStatus DellDeviceStatusCode = 5000
)

// DellAccessState represents the connectivity state of a Dell device.
type DellAccessState bool

const (
	DellAccessStateConnected    DellAccessState = true
	DellAccessStateDisconnected DellAccessState = false
)

var JobStatusMap = map[int]string{
	2020: "Scheduled",
	2030: "Queued",
	2040: "Starting",
	2050: "Running",
	2060: "Completed",
	2070: "Failed",
	2090: "Warning",
	2080: "New",
	2100: "Aborted",
	2101: "Paused",
	2102: "Stopped",
	2103: "Canceled",
}

var JobStatusFailed = []int{2070, 2090, 2100, 2101, 2102, 2103}
var JobStatusSuccess = 2060

var DeviceStatusMap = map[int]string{
	1000: "NORMAL",
	2000: "UNKNOWN",
	3000: "WARNING",
	4000: "CRITICAL",
	5000: "NOSTATUS",
}

var PowerStateMap = map[int]string{
	1:  "UNKNOWN",
	17: "ON",
	18: "OFF",
}

var DeviceTypeMap = map[int]string{
	1000: "SERVER",
	2000: "CHASSIS",
	9000: "NETWORK_CONTROLLER",
	4000: "NETWORK_IOM",
	3000: "STORAGE",
	8000: "STORAGE_IOM",
}

var JobTypeMap = map[string]int{
	"DeviceAction_Task":     3,
	"Update_Task":           5,
	"Inventory_Task":        8,
	"RollbackSoftware_Task": 15,
	"DebugLogs_Task":        18,
	"Restore_Task":          20,
	"Backup_Task":           21,
	"ChassisProfile_Task":   22,
	"SettingsUpdate_Task":   25,
	"Device_Config_Task":    50,
	"MCMOnBoarding_Task":    37,
	"MCMOffBoarding_Task":   38,
	"MCMGroupCreation_Task": 39,
	"ProfileUpdate_Task":    41,
	"QuickDeploy_Task":      42,
}

var JobURL = "/api/JobService/Jobs"
var JonTypeURL = "/api/JobService/JobTypes"
var BaselineURL = "/api/UpdateService/Baselines"
var CatalogURL = "/api/UpdateService/Catalogs"
var ComplianceReportURL = "/api/UpdateService/Baselines(%s)/DeviceComplianceReports"
var SessionURL = "/api/SessionService/Sessions"
var RefreshCatalogURL = "/api/UpdateService/Actions/UpdateService.RefreshCatalogs"
var DeviceURL = "/api/DeviceService/Devices"
var DeviceTypeURL = "/api/DeviceService/DeviceTypes"
var RefreshComplianceData = "/api/JobService/Actions/JobService.RunJobs"

// ---- DellClient ----
type DellClient struct {
	Client *client
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
	return &DellClient{Client: client}, nil
}

func (c *DellClient) ImportServer(hostname string, IP metalv1alpha1.IP, bmcUser, bmcPassword string) error {
	discoveryURL := c.Client.parsedURL.JoinPath("/api/DiscoveryConfigService/DiscoveryConfigGroups")

	// Create ConnectionProfile as JSON string
	connectionProfile := map[string]any{
		"profileName":        "",
		"profileDescription": "",
		"type":               "DISCOVERY",
		"credentials": []map[string]any{
			{
				"type":     "WSMAN",
				"authType": "Basic",
				"modified": false,
				"credentials": map[string]string{
					"username": bmcUser,
					"password": bmcPassword,
				},
			},
		},
	}
	connectionProfileJSON, err := json.Marshal(connectionProfile)
	if err != nil {
		return fmt.Errorf("error marshalling connection profile: %w", err)
	}

	discoveryPayload := map[string]any{
		"DiscoveryConfigGroupName": "ImportServer-" + hostname,
		"DiscoveryConfigModels": []map[string]any{
			{
				"DiscoveryConfigTargets": []map[string]any{
					{
						"NetworkAddressDetail": IP.String(),
					},
				},
				"ConnectionProfile": string(connectionProfileJSON),
				"DeviceType":        []int{1000}, // Server device type
			},
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
	_, err = c.Client.DoRequest(req, []int{http.StatusCreated, http.StatusOK})
	if err != nil {
		return fmt.Errorf("error executing discovery request: %w", err)
	}

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
	removeURL := c.Client.parsedURL.JoinPath("/api/DeviceService/Actions/DeviceService.RemoveDevices")
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
	_, err = c.Client.DoRequest(req, []int{http.StatusNoContent})
	if err != nil {
		return fmt.Errorf("error executing remove request: %w", err)
	}
	return nil
}

func (c *DellClient) ListServers() ([]Device, error) {
	serversURL := c.Client.parsedURL.JoinPath("/api/DeviceService/Devices")

	req, err := http.NewRequest("GET", serversURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("error creating get servers request: %w", err)
	}
	body, err := c.Client.DoRequest(req, []int{http.StatusOK})
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
	authURL := c.Client.parsedURL.String() + "/api/SessionService/Sessions"
	if c.Client.token != "" {
		// check token still valid
		req, err := http.NewRequest("GET", authURL, nil)
		if err != nil {
			return "", fmt.Errorf("error creating auth validation request: %w", err)
		}
		_, err = c.Client.DoRequest(req, []int{http.StatusOK})
		if err != nil {
			return c.createToken()
		}
		return c.Client.token, nil
	}
	return c.createToken()
}

func (c *DellClient) createToken() (string, error) {
	authURL := c.Client.parsedURL.String() + "/api/SessionService/Sessions"
	authPayload := AuthRequest{
		UserName: c.Client.username,
		Password: c.Client.password,
	}
	payloadBytes, err := json.Marshal(authPayload)
	if err != nil {
		return "", fmt.Errorf("error marshalling auth payload: %w", err)
	}

	req, err := http.NewRequest("POST", authURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return "", fmt.Errorf("error creating auth request: %w", err)
	}

	respBody, err := c.Client.DoRequest(req, []int{http.StatusCreated})
	if err != nil {
		return "", fmt.Errorf("error executing auth request: %w", err)
	}

	var authResp AuthResponse
	if err := json.Unmarshal(respBody, &authResp); err != nil {
		return "", fmt.Errorf("error parsing auth response: %w", err)
	}
	c.Client.token = authResp.Token
	return authResp.Token, nil
}

// ImportServerAsync initiates an asynchronous import and returns the job ID.
func (c *DellClient) ImportServerAsync(hostname string, IP metalv1alpha1.IP, bmcUser, bmcPassword string) (string, error) {
	discoveryURL := c.Client.parsedURL.JoinPath("/api/DiscoveryConfigService/DiscoveryConfigGroups")

	connectionProfile := map[string]any{
		"profileName":        "",
		"profileDescription": "",
		"type":               "DISCOVERY",
		"credentials": []map[string]any{
			{
				"type":     "WSMAN",
				"authType": "Basic",
				"modified": false,
				"credentials": map[string]string{
					"username": bmcUser,
					"password": bmcPassword,
				},
			},
		},
	}
	connectionProfileJSON, err := json.Marshal(connectionProfile)
	if err != nil {
		return "", fmt.Errorf("error marshalling connection profile: %w", err)
	}

	discoveryPayload := map[string]any{
		"DiscoveryConfigGroupName": "ImportServer-" + hostname,
		"DiscoveryConfigModels": []map[string]any{
			{
				"DiscoveryConfigTargets": []map[string]any{
					{
						"NetworkAddressDetail": IP.String(),
					},
				},
				"ConnectionProfile": string(connectionProfileJSON),
				"DeviceType":        []int{1000},
			},
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

	respBody, err := c.Client.DoRequest(req, []int{http.StatusCreated, http.StatusOK})
	if err != nil {
		return "", fmt.Errorf("error executing discovery request: %w", err)
	}

	// Parse response to extract DiscoveryConfigGroupId
	var response map[string]any
	if err := json.Unmarshal(respBody, &response); err != nil {
		return "", fmt.Errorf("error parsing discovery response: %w", err)
	}

	// Extract job ID from response
	if groupID, ok := response["DiscoveryConfigGroupId"].(float64); ok {
		return fmt.Sprintf("%d", int(groupID)), nil
	}

	return "", fmt.Errorf("no DiscoveryConfigGroupId in response")
}

// RemoveServerAsync initiates an asynchronous remove operation.
func (c *DellClient) RemoveServerAsync(hostname string, ip metalv1alpha1.IP) (string, error) {
	// Dell's RemoveDevices is synchronous, return empty job ID
	err := c.RemoveServer(hostname, ip)
	return "", err
}

// GetJobStatus retrieves the status of a Dell discovery job.
func (c *DellClient) GetJobStatus(jobID string) (*JobInfo, error) {
	// Query the discovery jobs endpoint filtering by config group ID
	jobsURL := c.Client.parsedURL.JoinPath("/api/DiscoveryConfigService/Jobs")

	req, err := http.NewRequest("GET", jobsURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("error creating job status request: %w", err)
	}

	respBody, err := c.Client.DoRequest(req, []int{http.StatusOK})
	if err != nil {
		return nil, fmt.Errorf("error executing job status request: %w", err)
	}

	var jobsResp DiscoveryJobsResponse
	if err := json.Unmarshal(respBody, &jobsResp); err != nil {
		return nil, fmt.Errorf("error parsing jobs response: %w", err)
	}

	// Find the job with matching config group ID
	for _, job := range jobsResp.Value {
		if fmt.Sprintf("%d", job.ConfigGroup.Id) == jobID {
			return &JobInfo{
				JobID:    jobID,
				Status:   fmt.Sprintf("%d", job.Status),
				Progress: job.Progress,
				Message:  job.State,
			}, nil
		}
	}

	return nil, fmt.Errorf("job %s not found", jobID)
}

// IsJobComplete returns true if the Dell job is no longer running.
func (c *DellClient) IsJobComplete(jobInfo *JobInfo) bool {
	// Status 3002 = Running, anything else means complete
	return jobInfo.Status != "3002"
}

// IsJobSuccessful returns true if the Dell job completed successfully.
func (c *DellClient) IsJobSuccessful(jobInfo *JobInfo) bool {
	// Status 3001 = Completed successfully
	return jobInfo.Status == "3001"
}

// ODataList is a generic wrapper for OData paginated list responses.
type ODataList[T any] struct {
	Value []T `json:"value"`
}

type DellJobStatus struct {
	JobStatusID int    `json:"Id"`
	JobStatus   string `json:"Name"`
}

type DellJobType struct {
	JobTypeID int    `json:"Id"`
	JobType   string `json:"Name"`
	Internal  bool   `json:"Internal,omitempty"`
}

type DellTasksDetails struct {
	TaskName   string `json:"TaskName"`
	Complete   string `json:"EndTime"`
	Status     int    `json:"TaskStatusId"`
	Percentage string `json:"Progress"`
	TasksType  int    `json:"TaskType"`
	Target     string `json:"Key"`
}

type DellDeviceData struct {
	Id                 int                  `json:"Id"`
	Name               string               `json:"DeviceName"`
	SKU                string               `json:"Identifier"`
	ManagedState       DellManagedState     `json:"ManagedState"`
	OverallHealthState DellDeviceStatusCode `json:"Status"`
	AccessState        DellAccessState      `json:"ConnectionState"`
	Type               int                  `json:"Type"`
}

type DellAlert struct {
	SeverityName string `json:"SeverityName"`
	Message      string `json:"Message"`
	TimeStamp    string `json:"TimeStamp"`
	AlertID      string `json:"AlertMessageId"`
}

type DellCatalogDetails struct {
	Id                 int                   `json:"Id"`
	Filename           string                `json:"Filename"`
	SourcePath         string                `json:"SourcePath"`
	Status             string                `json:"Status"`
	TaskId             int                   `json:"TaskId"`
	BaseLocation       string                `json:"BaseLocation"`
	ManifestIdentifier string                `json:"ManifestIdentifier"`
	ReleaseIdentifier  string                `json:"ReleaseIdentifier"`
	ManifestVersion    string                `json:"ManifestVersion"`
	CreatedDate        string                `json:"CreatedDate"`
	LastUpdated        string                `json:"LastUpdated"`
	BundlesCount       int                   `json:"BundlesCount"`
	Repository         DellCatalogRepository `json:"Repository"`
}

type DellCatalogRepository struct {
	Id               int    `json:"Id,omitempty"`
	Name             string `json:"Name"`
	Description      string `json:"Description"`
	Source           string `json:"Source"`
	DomainName       string `json:"DomainName"`
	Username         string `json:"Username"`
	Password         string `json:"Password"`
	CheckCertificate bool   `json:"CheckCertificate"`
	RepositoryType   string `json:"RepositoryType"`
}

type DellDeviceComplianceReport struct {
	Id                              int                             `json:"Id"`
	DeviceId                        int                             `json:"DeviceId"`
	ServiceTag                      string                          `json:"ServiceTag"`
	DeviceModel                     string                          `json:"DeviceModel"`
	DeviceTypeName                  string                          `json:"DeviceTypeName"`
	DeviceName                      string                          `json:"DeviceName"`
	FirmwareStatus                  string                          `json:"FirmwareStatus"`
	ComplianceStatus                string                          `json:"ComplianceStatus"`
	DeviceTypeId                    int                             `json:"DeviceTypeId"`
	RebootRequired                  bool                            `json:"RebootRequired"`
	DeviceFirmwareUpdateCapable     bool                            `json:"DeviceFirmwareUpdateCapable"`
	DeviceUserFirmwareUpdateCapable bool                            `json:"DeviceUserFirmwareUpdateCapable"`
	ComponentComplianceReports      []DellComponentComplianceReport `json:"ComponentComplianceReports"`
}

type DellComponentComplianceReport struct {
	Id                        int    `json:"Id"`
	Version                   string `json:"Version"`
	CurrentVersion            string `json:"CurrentVersion"`
	Path                      string `json:"Path"`
	Name                      string `json:"Name"`
	Criticality               string `json:"Criticality"`
	UniqueIdentifier          string `json:"UniqueIdentifier"`
	TargetIdentifier          string `json:"TargetIdentifier"`
	UpdateAction              string `json:"UpdateAction"`
	SourceName                string `json:"SourceName"`
	PrerequisiteInfo          string `json:"PrerequisiteInfo"`
	ImpactAssessment          string `json:"ImpactAssessment"`
	Uri                       string `json:"Uri"`
	RebootRequired            bool   `json:"RebootRequired"`
	ComplianceStatus          string `json:"ComplianceStatus"`
	ComplianceDependencies    []any  `json:"ComplianceDependencies"`
	ComponentType             string `json:"ComponentType"`
	DependencyUpgradeRequired bool   `json:"DependencyUpgradeRequired"`
}

type DellBaseline struct {
	Id               int          `json:"Id,omitempty"`
	Name             string       `json:"Name"`
	Description      string       `json:"Description"`
	CatalogId        int          `json:"CatalogId"`
	RepositoryId     int          `json:"RepositoryId"`
	TaskId           int          `json:"TaskId,omitempty"`
	DowngradeEnabled bool         `json:"DowngradeEnabled"`
	Is64Bit          bool         `json:"Is64Bit"`
	Targets          []DellTarget `json:"Targets"`
}

type DellTarget struct {
	Id         int             `json:"Id"`
	JobId      int             `json:"JobId,omitempty"`
	Type       *DellTargetType `json:"Type,omitempty"`
	TargetType *DellTargetType `json:"TargetType,omitempty"`
	Data       string          `json:"Data,omitempty"`
}

type DellTargetType struct {
	Id   int    `json:"Id"`
	Name string `json:"Name"`
}

type DellFirmwareUpdatePayload struct {
	JobName        string       `json:"JobName"`
	JobDescription string       `json:"JobDescription"`
	Schedule       string       `json:"Schedule"`
	State          string       `json:"State"`
	JobType        DellJobType  `json:"JobType"`
	Params         []DellParams `json:"Params"`
	Targets        []DellTarget `json:"Targets"`
}

type DellParams struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

type DellJob struct {
	Id             int           `json:"Id"`
	JobName        string        `json:"JobName"`
	JobDescription string        `json:"JobDescription"`
	NextRun        string        `json:"NextRun"`
	LastRun        string        `json:"LastRun"`
	StartTime      string        `json:"StartTime"`
	EndTime        string        `json:"EndTime"`
	Schedule       string        `json:"Schedule"`
	State          string        `json:"State"`
	CreatedBy      string        `json:"CreatedBy"`
	UpdatedBy      string        `json:"UpdatedBy"`
	Visible        bool          `json:"Visible"`
	Editable       bool          `json:"Editable"`
	Builtin        bool          `json:"Builtin"`
	Histories      string        `json:"ExecutionHistories@odata.navigationLink"`
	Status         DellJobStatus `json:"LastRunStatus"`
	JobType        DellJobType   `json:"JobType"`
	Targets        []DellTarget  `json:"Targets"`
	Params         []DellParams  `json:"Params"`
}

type DellJobHistory struct {
	Id             int           `json:"Id"`
	JobName        string        `json:"JobName"`
	Progress       string        `json:"Progress"`
	StartTime      string        `json:"StartTime"`
	EndTime        string        `json:"EndTime"`
	LastUpdateTime string        `json:"LastUpdateTime"`
	ExecutedBy     string        `json:"ExecutedBy"`
	JobId          int           `json:"JobId"`
	JobStatus      DellJobStatus `json:"JobStatus"`
	HistoryDetails string        `json:"ExecutionHistoryDetails@odata.navigationLink"`
}

type Session struct {
	ODataType             string   `json:"@odata.type"`
	ODataID               string   `json:"@odata.id"`
	Id                    string   `json:"Id"`
	Description           string   `json:"Description"`
	Name                  string   `json:"Name"`
	UserName              string   `json:"UserName"`
	UserId                int      `json:"UserId"`
	Password              *string  `json:"Password"`
	Roles                 []string `json:"Roles"`
	IpAddress             string   `json:"IpAddress"`
	StartTimeStamp        string   `json:"StartTimeStamp"`
	LastAccessedTimeStamp string   `json:"LastAccessedTimeStamp"`
	DirectoryGroup        []string `json:"DirectoryGroup"`
}

func (c *DellClient) CloseSession(ctx context.Context) error {
	url := c.Client.Config.URL.JoinPath("api", "SessionService", fmt.Sprintf("Sessions('%s')", c.Client.Auth.SessionId))
	req, err := c.Client.CreateRequestWithAuth(url, http.MethodDelete, nil, nil)
	if err != nil {
		return err
	}
	_, err = c.Client.MgrDoRequest(ctx, req, []int{http.StatusNoContent})
	if err != nil {
		return err
	}
	return nil
}

func (c *DellClient) GetSession(ctx context.Context) (*Session, error) {
	url := c.Client.Config.URL.JoinPath("api", "SessionService", "Sessions")
	sessionlist := &ODataList[Session]{}
	err := c.Client.Get(ctx, url, sessionlist, []int{http.StatusOK})
	if err != nil {
		return nil, err
	}
	if len(sessionlist.Value) == 0 {
		return nil, fmt.Errorf("no active sessions found")
	}
	return &sessionlist.Value[0], nil
}

func (c *DellClient) GetTasksStatusMap(ctx context.Context) (statuses map[int]string, err error) {
	url := c.Client.Config.URL.JoinPath("api", "JobService", "JobStatuses")
	statuslist := &ODataList[DellJobStatus]{}
	err = c.Client.Get(ctx, url, statuslist, []int{http.StatusOK})
	if err != nil {
		err = fmt.Errorf("failed to get data %v", err)
		return
	}
	statuses = make(map[int]string, len(statuslist.Value))
	for i := range statuslist.Value {
		status := &statuslist.Value[i]
		statuses[status.JobStatusID] = status.JobStatus
	}
	return
}

func (c *DellClient) GetTaskTypeMap(ctx context.Context) (tasksTypes map[int]string, err error) {
	url := c.Client.Config.URL.JoinPath("api", "JobService", "JobTypes")
	JobTypeslist := &ODataList[DellJobType]{}
	err = c.Client.Get(ctx, url, JobTypeslist, []int{http.StatusOK})
	if err != nil {
		err = fmt.Errorf("failed to get data %v", err)
		return
	}
	tasksTypes = make(map[int]string)
	for _, st := range JobTypeslist.Value {
		tasksTypes[st.JobTypeID] = st.JobType
	}
	return
}

func (c *DellClient) GetAllData(ctx context.Context, url *neturl.URL, returnData any, okCodes []int) ([]any, error) {
	nextURL := url
	log := ctrl.LoggerFrom(ctx)

	var allValues []any
	targetType := reflect.TypeOf(returnData)

	for nextURL != nil {
		resBody, err := c.Client.GetResponseBody(ctx, nextURL, okCodes)
		if err != nil {
			return nil, err
		}
		var pageData struct {
			Value    json.RawMessage `json:"value"`
			NextLink string          `json:"@odata.nextLink"`
		}
		if err = json.Unmarshal(resBody, &pageData); err != nil {
			log.V(1).Info("Failed to unmarshal page data, trying single object", "err", err, "resBody", string(resBody))
			concrete := reflect.New(targetType).Interface()
			if errSingle := json.Unmarshal(resBody, concrete); errSingle == nil {
				log.V(1).Info("Fetched single", "url", nextURL.String(), "resBody", string(resBody))
				allValues = append(allValues, reflect.ValueOf(concrete).Elem().Interface())
				nextURL = nil
				break
			}
			return nil, fmt.Errorf("failed to decode response from: %v. \nresponse: %v \twith error: %v",
				nextURL.String(), string(resBody), err)
		}
		sliceType := reflect.SliceOf(targetType)
		pageValues := reflect.New(sliceType).Interface()
		if err = json.Unmarshal(pageData.Value, pageValues); err != nil {
			return nil, fmt.Errorf("failed to decode 'value' field from: %v. \nresponse: %v \twith error: %v",
				nextURL.String(), string(pageData.Value), err)
		}
		s := reflect.ValueOf(pageValues).Elem()
		for i := 0; i < s.Len(); i++ {
			allValues = append(allValues, s.Index(i).Interface())
		}
		if pageData.NextLink != "" {
			nextURL, err = neturl.Parse(url.Scheme + "://" + url.Host + pageData.NextLink)
			if err != nil {
				return nil, fmt.Errorf("failed to parse nextLink URL: %v", err)
			}
		} else {
			nextURL = nil
		}
	}
	return allValues, nil
}

func (c *DellClient) GetDevicesFromSKU(ctx context.Context, listSKU []string) ([]DellDeviceData, error) {
	url := c.Client.Config.URL.JoinPath("api", "DeviceService", "Devices")
	query := url.Query()
	for _, sku := range listSKU {
		query.Add("Identifier", sku)
	}
	url.RawQuery = query.Encode()
	devicesDetails, err := c.GetAllData(ctx, url, DellDeviceData{}, []int{http.StatusOK})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Device details from url: %v,\n with error %w", url, err)
	}
	deviceDetailsList := make([]DellDeviceData, 0, len(devicesDetails))
	for _, v := range devicesDetails {
		if item, ok := v.(DellDeviceData); ok {
			deviceDetailsList = append(deviceDetailsList, item)
		} else {
			return nil, fmt.Errorf("cannot convert type %T to DellDeviceData, data: %v", v, devicesDetails)
		}
	}
	return deviceDetailsList, nil
}

func (c *DellClient) RefreshCatalog(ctx context.Context, catalogIds []int) error {
	url := c.Client.Config.URL.JoinPath("api", "UpdateService", "Actions", "UpdateService.RefreshCatalogs")
	bodyMap := map[string]any{
		"CatalogIds":  catalogIds,
		"AllCatalogs": len(catalogIds) == 0,
	}
	bodyString, err := json.Marshal(bodyMap)
	if err != nil {
		return fmt.Errorf("failed stringify RefreshCatalog body with error: %v", err)
	}
	_, err = c.Client.PostWithResponse(ctx, url, strings.NewReader(string(bodyString)), []int{http.StatusNoContent})
	return err
}

func (c *DellClient) GetCatalog(ctx context.Context, catalogId int) (*DellCatalogDetails, error) {
	url := c.Client.Config.URL.JoinPath("api", "UpdateService", fmt.Sprintf("Catalogs(%d)", catalogId))
	catalogDetail := &DellCatalogDetails{}
	if err := c.Client.Get(ctx, url, catalogDetail, []int{http.StatusOK}); err != nil {
		return nil, fmt.Errorf("failed to fetch catalog details for %v: from url: %v,\n with error %w", catalogId, url, err)
	}
	return catalogDetail, nil
}

func (c *DellClient) GetAllCatalogs(ctx context.Context) ([]DellCatalogDetails, error) {
	url := c.Client.Config.URL.JoinPath("api", "UpdateService", "Catalogs")
	catalogDetailsFetched, err := c.GetAllData(ctx, url, DellCatalogDetails{}, []int{http.StatusOK})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch catalog details from url: %v,\n with error %w", url, err)
	}
	catalogDetails := make([]DellCatalogDetails, 0, len(catalogDetailsFetched))
	for _, v := range catalogDetailsFetched {
		if item, ok := v.(DellCatalogDetails); ok {
			catalogDetails = append(catalogDetails, item)
		} else {
			return nil, fmt.Errorf("cannot convert type %T to DellCatalogDetails, data: %v", v, catalogDetailsFetched)
		}
	}
	return catalogDetails, nil
}

func (c *DellClient) CreateCatalog(ctx context.Context, payload *DellCatalogDetails) (*DellCatalogDetails, error) {
	url := c.Client.Config.URL.JoinPath("api", "UpdateService", "Catalogs")
	payloadBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed stringify create catalog body with error: %v", err)
	}
	err = c.Client.Post(ctx, url, strings.NewReader(string(payloadBody)), payload, []int{http.StatusCreated})
	if err != nil {
		return nil, fmt.Errorf("failed to create catalog with error %w", err)
	}
	return payload, nil
}

func (c *DellClient) GetJobDetails(ctx context.Context, JobId int) (*DellJob, error) {
	url := c.Client.Config.URL.JoinPath("api", "JobService", fmt.Sprintf("Jobs(%d)", JobId))
	jobDetail := &DellJob{}
	if err := c.Client.Get(ctx, url, jobDetail, []int{http.StatusOK}); err != nil {
		return nil, fmt.Errorf("failed to fetch Job details for %v: from url: %v,\n with error %w", JobId, url, err)
	}
	return jobDetail, nil
}

func (c *DellClient) GetAllJobDetails(ctx context.Context) ([]DellJob, error) {
	url := c.Client.Config.URL.JoinPath("api", "JobService", "Jobs")
	jobDetailsFetched, err := c.GetAllData(ctx, url, DellJob{}, []int{http.StatusOK})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Job details from url: %v,\n with error %w", url, err)
	}
	jobDetails := make([]DellJob, 0, len(jobDetailsFetched))
	for _, v := range jobDetailsFetched {
		if item, ok := v.(DellJob); ok {
			jobDetails = append(jobDetails, item)
		} else {
			return nil, fmt.Errorf("cannot convert type %T to DellJob, data: %v", v, jobDetailsFetched)
		}
	}
	return jobDetails, nil
}

func (c *DellClient) GetJobHistory(ctx context.Context, JobId int) ([]DellJobHistory, error) {
	jobDetails, err := c.GetJobDetails(ctx, JobId)
	if err != nil {
		return nil, err
	}
	url, err := c.Client.Config.URL.Parse(jobDetails.Histories)
	if err != nil {
		return nil, fmt.Errorf("failed to parse job history URL %q: %w", jobDetails.Histories, err)
	}
	jobHistoryFetched, err := c.GetAllData(ctx, url, DellJobHistory{}, []int{http.StatusOK})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Job History details from url: %v,\n with error %w", url, err)
	}
	jobHistoryDetails := make([]DellJobHistory, 0, len(jobHistoryFetched))
	for _, v := range jobHistoryFetched {
		if item, ok := v.(DellJobHistory); ok {
			jobHistoryDetails = append(jobHistoryDetails, item)
		} else {
			return nil, fmt.Errorf("cannot convert type %T to DellJobHistory, data: %v", v, jobHistoryFetched)
		}
	}
	return jobHistoryDetails, nil
}

func (c *DellClient) CreateBaseline(ctx context.Context, payload *DellBaseline) (*DellBaseline, error) {
	url := c.Client.Config.URL.JoinPath("api", "UpdateService", "Baselines")
	payloadBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed stringify CreateBaseline body with error: %v", err)
	}
	err = c.Client.Post(ctx, url, strings.NewReader(string(payloadBody)), payload, []int{http.StatusCreated})
	if err != nil {
		return nil, fmt.Errorf("failed to create Baselines with error %w", err)
	}
	return payload, nil
}

func (c *DellClient) GetAllBaseline(ctx context.Context) ([]DellBaseline, error) {
	url := c.Client.Config.URL.JoinPath("api", "UpdateService", "Baselines")
	baselinesDetails, err := c.GetAllData(ctx, url, DellBaseline{}, []int{http.StatusOK})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Baseline details from url: %v,\n with error %w", url, err)
	}
	baselineInfo := make([]DellBaseline, 0, len(baselinesDetails))
	for _, v := range baselinesDetails {
		if item, ok := v.(DellBaseline); ok {
			baselineInfo = append(baselineInfo, item)
		} else {
			return nil, fmt.Errorf("cannot convert type %T to DellBaseline, data: %v", v, baselinesDetails)
		}
	}
	return baselineInfo, nil
}

func (c *DellClient) UpdateBaseline(ctx context.Context, baselineId int, payload *DellBaseline) (*DellBaseline, error) {
	url := c.Client.Config.URL.JoinPath("api", "UpdateService", fmt.Sprintf("Baselines(%d)", baselineId))
	payloadBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed stringify CreateBaseline body with error: %v", err)
	}
	if _, err := c.Client.PutWithResponse(ctx, url, strings.NewReader(string(payloadBody)), []int{http.StatusOK}); err != nil {
		return nil, err
	}
	baselineDetails := &DellBaseline{}
	if err := c.Client.Get(ctx, url, baselineDetails, []int{http.StatusOK}); err != nil {
		return nil, fmt.Errorf("failed to fetch Baseline details for %v: from url: %v,\n with error %w", baselineId, url, err)
	}
	return baselineDetails, nil
}

func (c *DellClient) GetComplianceReportForBaseline(ctx context.Context, baselineID int) ([]DellDeviceComplianceReport, error) {
	url := c.Client.Config.URL.JoinPath(
		"api", "UpdateService",
		fmt.Sprintf("Baselines(%d)", baselineID),
		"DeviceComplianceReports",
	)
	complianceReports, err := c.GetAllData(ctx, url, DellDeviceComplianceReport{}, []int{http.StatusOK})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Device Compliance Report details from url: %v,\n with error %w", url, err)
	}
	complianceReport := make([]DellDeviceComplianceReport, 0, len(complianceReports))
	for _, v := range complianceReports {
		if item, ok := v.(DellDeviceComplianceReport); ok {
			complianceReport = append(complianceReport, item)
		} else {
			return nil, fmt.Errorf("cannot convert type %T to DellDeviceComplianceReport, data: %v", v, complianceReports)
		}
	}
	return complianceReport, nil
}

func (c *DellClient) CreateFirmwareUpdateJob(ctx context.Context, payload *DellFirmwareUpdatePayload) (*DellJob, error) {
	url := c.Client.Config.URL.JoinPath("api", "JobService", "Jobs")
	payloadBody, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed stringify CreateFirmwareUpdateJob body with error: %v", err)
	}
	jobDetail := &DellJob{}
	err = c.Client.Post(ctx, url, strings.NewReader(string(payloadBody)), jobDetail, []int{http.StatusCreated})
	if err != nil {
		return nil, fmt.Errorf("failed to create Firmware Update Job with error %w", err)
	}
	return jobDetail, nil
}

func (c *DellClient) RunJobNow(ctx context.Context, jobIDs []int) error {
	url := c.Client.Config.URL.JoinPath("api", "JobService", "Actions", "JobService.RunJobs")
	bodyMap := map[string]any{
		"JobIds":  jobIDs,
		"AllJobs": len(jobIDs) == 0,
	}
	bodyString, err := json.Marshal(bodyMap)
	if err != nil {
		return fmt.Errorf("failed stringify RunJobNow body with error: %v", err)
	}
	_, err = c.Client.PostWithResponse(ctx, url, strings.NewReader(string(bodyString)), []int{http.StatusNoContent})
	return err
}

// GetDellConsole builds a DellClient, validates/creates the session, and returns it.
func GetDellConsole(ctx context.Context, config *MgrConfig, auth *AuthToken) (*DellClient, error) {
	log := logf.FromContext(ctx)
	mfgConsole := &DellClient{
		Client: &client{
			httpClient: CreateManagerClient(config),
			Config:     config,
			Auth:       auth,
		},
	}
	if auth.Token != "" {
		mfgConsole.Client.Auth.Token = auth.Token
		session, err := mfgConsole.GetSession(ctx)
		if session != nil && err == nil {
			mfgConsole.Client.Auth.SessionId = session.Id
			return mfgConsole, nil
		}
		var reqErr *ResponseError
		if err != nil {
			if errors.As(err, &reqErr) && reqErr.StatusCode == http.StatusUnauthorized {
				log.V(1).Info("existing token is invalid, need to re-authorize", "status code", reqErr.StatusCode)
			} else {
				return nil, fmt.Errorf("failed to validate existing token for user %q: %w", auth.Username, err)
			}
		} else {
			return mfgConsole, nil
		}
	}
	dellAuthBody := map[string]string{
		"UserName":    auth.Username,
		"Password":    auth.Password,
		"SessionType": "API",
	}
	if err := mfgConsole.Client.CreateSession(
		ctx,
		config.URL.JoinPath(SessionURL),
		dellAuthBody, DellToken,
		[]int{http.StatusCreated, http.StatusUnauthorized},
	); err != nil {
		return nil, fmt.Errorf("failed to create session with error: %w", err)
	}
	session, err := mfgConsole.GetSession(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to validate token with error: %w", err)
	}
	if session != nil {
		mfgConsole.Client.Auth.SessionId = session.Id
	}
	return mfgConsole, nil
}
