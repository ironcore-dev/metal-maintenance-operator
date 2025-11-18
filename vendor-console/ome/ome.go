// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
//
// SPDX-License-Identifier: Apache-2.0

package ome

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	neturl "net/url"
	"reflect"
	"strings"

	"github.com/ironcore-dev/maintenance-operator/vendor-console/client"
	ctrl "sigs.k8s.io/controller-runtime"
)

// OME defines an interface for interacting with a Open Manager Enterprise (OME) for dell servers.
type OME struct {
	Client *client.ManagerClient
}

// Common struct for OData list responses
type ODataList[T any] struct {
	Value []T `json:"value"`
}

// Example usage:
// jobs := &ODataList[DellJobStatus]{}

// type DellJobStatusList struct {
// 	StatusCodeList []DellJobStatus `json:"value"`
// }

type DellJobStatus struct {
	JobStatusID int    `json:"Id"`
	JobStatus   string `json:"Name"`
}

// Example usage:
// jobsType := &ODataList[DellJobType]{}

// type DellJobTypeList struct {
// 	JobTypeList []DellJobType `json:"value"`
// }

type DellJobType struct {
	JobTypeID int    `json:"Id"`
	JobType   string `json:"Name"`
	Internal  bool   `json:"Internal,omitempty"`
}

// Example usage:
// task := &ODataList[DellTasksDetails]{}
// type DellTasksList struct {
// 	TasksList []DellTasksDetails `json:"value"`
// }

type DellTasksDetails struct {
	TaskName   string `json:"TaskName"`
	Complete   string `json:"EndTime"`
	Status     int    `json:"TaskStatusId"`
	Percentage string `json:"Progress"`
	TasksType  int    `json:"TaskType"`
	Target     string `json:"Key"`
}

// Example usage:
// device := &ODataList[DellDeviceData]{}
// type DellDeviceList struct {
// 	NodeList []DellDeviceData `json:"value"`
// }

type DellDeviceData struct {
	Id                 int                  `json:"Id"`
	Name               string               `json:"DeviceName"`
	SKU                string               `json:"Identifier"`
	ManagedState       DellManagedState     `json:"ManagedState"`
	OverallHealthState DellDeviceStatusCode `json:"Status"`
	AccessState        DellAccessState      `json:"ConnectionState"`
	Type               int                  `json:"Type"`
}

// Example usage:
// Alert := &ODataList[DellAlert]{}
// type DellAlertList struct {
// 	DellAlertList []DellAlert `json:"value"`
// }

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

func (d *OME) CloseSession(ctx context.Context) error {
	url := d.Client.Config.URL.JoinPath("api", "SessionService", fmt.Sprintf("Sessions('%s')", d.Client.Auth.SessionId))
	req, err := d.Client.CreateRequestWithAuth(url, http.MethodDelete, nil, nil)
	if err != nil {
		return err
	}
	_, err = d.Client.DoRequest(ctx, req, []int{http.StatusNoContent})
	if err != nil {
		return err
	}
	return nil
}

func (d *OME) GetSession(ctx context.Context) (*Session, error) {
	url := d.Client.Config.URL.JoinPath("api", "SessionService", "Sessions")
	sessionlist := &ODataList[Session]{}
	err := d.Client.Get(ctx, url, sessionlist, []int{http.StatusOK})
	if err != nil {
		return nil, err
	}
	return &sessionlist.Value[0], nil
}

func (d *OME) GetTasksStatusMap(ctx context.Context) (statuses map[int]string, err error) {
	url := d.Client.Config.URL.JoinPath("api", "JobService", "JobStatuses")
	statuslist := &ODataList[DellJobStatus]{}
	err = d.Client.Get(ctx, url, statuslist, []int{http.StatusOK})
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

func (d *OME) GetTaskTypeMap(ctx context.Context) (tasksTypes map[int]string, err error) {
	url := d.Client.Config.URL.JoinPath("api", "JobService", "JobTypes")
	JobTypeslist := &ODataList[DellJobType]{}
	err = d.Client.Get(ctx, url, JobTypeslist, []int{http.StatusOK})
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

func (d *OME) GetAllData(ctx context.Context, url *neturl.URL, returnData any, okCodes []int) ([]any, error) {
	nextURL := url
	log := ctrl.LoggerFrom(ctx)

	var allValues []any
	targetType := reflect.TypeOf(returnData)

	for nextURL != nil {
		resBody, err := d.Client.GetResponseBody(ctx, nextURL, okCodes)
		if err != nil {
			return nil, err
		}
		// Use a temporary structure to unmarshal both the values and the nextLink
		var pageData struct {
			Value    json.RawMessage `json:"value"`
			NextLink string          `json:"@odata.nextLink"`
		}

		if err = json.Unmarshal(resBody, &pageData); err != nil {
			log.V(1).Info("Failed to unmarshal page data, trying single object", "err", err, "resBody", string(resBody))
			// Handle cases where the response is a single object, not a list
			if errSignle := json.Unmarshal(resBody, &returnData); errSignle == nil {
				log.V(1).Info("Fetched single", "url", nextURL.String(), "resBody", string(resBody))
				allValues = append(allValues, returnData)
				nextURL = nil // Assume single object means no pagination
				break
			}
			return nil, fmt.Errorf("failed to decode response from: %v. \nresponse: %v \twith error: %v",
				nextURL.String(), string(resBody), err)
		}

		// Create a slice of the target type
		sliceType := reflect.SliceOf(targetType)
		pageValues := reflect.New(sliceType).Interface()

		if err = json.Unmarshal(pageData.Value, pageValues); err != nil {
			return nil, fmt.Errorf("failed to decode 'value' field from: %v. \nresponse: %v \twith error: %v",
				nextURL.String(), string(pageData.Value), err)
		}

		// Append the unmarshalled values to our combined list
		s := reflect.ValueOf(pageValues).Elem()
		for i := 0; i < s.Len(); i++ {
			allValues = append(allValues, s.Index(i).Interface())
		}

		// Prepare the URL for the next page
		if pageData.NextLink != "" {
			nextURL, err = neturl.Parse(url.Scheme + "://" + url.Host + pageData.NextLink)
			if err != nil {
				return nil, fmt.Errorf("failed to parse nextLink URL: %v", err)
			}
		} else {
			nextURL = nil // No next link, stop the loop
		}
	}
	return allValues, nil
}

func (d *OME) GetDevicesFromSKU(ctx context.Context, listSKU []string) ([]DellDeviceData, error) {
	url := d.Client.Config.URL.JoinPath("api", "DeviceService", "Devices")
	query := url.Query()
	for _, sku := range listSKU {
		query.Add("Identifier", sku)
	}
	url.RawQuery = query.Encode()
	if devicesDetails, err := d.GetAllData(ctx, url, DellDeviceData{}, []int{http.StatusOK}); err != nil {
		return nil, fmt.Errorf("failed to fetch Device details from url: %v,\n with error %w", url, err)
	} else {
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
}
func (d *OME) RefreshCatalog(ctx context.Context, catalogIds []int) error {
	url := d.Client.Config.URL.JoinPath("api", "UpdateService", "Actions", "UpdateService.RefreshCatalogs")

	bodyMap := map[string]any{
		"CatalogIds":  catalogIds,
		"AllCatalogs": len(catalogIds) == 0,
	}
	bodyString, err := json.Marshal(bodyMap)
	if err != nil {
		err = fmt.Errorf("failed stringify RefreshCatalog body with error: %v", err)
		return err
	}

	_, err = d.Client.PostWithResponse(ctx, url, strings.NewReader(string(bodyString)), []int{http.StatusNoContent})
	if err != nil {
		return err
	}
	// figure out what to do with the response
	return nil
}

func (d *OME) GetCatalog(ctx context.Context, catalogId int) (*DellCatalogDetails, error) {
	url := d.Client.Config.URL.JoinPath("api", "UpdateService", fmt.Sprintf("Catalogs(%d)", catalogId))
	catalogDetail := &DellCatalogDetails{}
	if err := d.Client.Get(ctx, url, catalogDetail, []int{http.StatusOK}); err != nil {
		return nil, fmt.Errorf("failed to fetch catalog details for %v: from url: %v,\n with error %w", catalogId, url, err)
	}
	return catalogDetail, nil
}

func (d *OME) GetAllCatalogs(ctx context.Context) ([]DellCatalogDetails, error) {
	url := d.Client.Config.URL.JoinPath("api", "UpdateService", "Catalogs")
	if catalogDetailsFetched, err := d.GetAllData(ctx, url, DellCatalogDetails{}, []int{http.StatusOK}); err != nil {
		return nil, fmt.Errorf("failed to fetch catalog details from url: %v,\n with error %w", url, err)
	} else {
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
}

func (d *OME) CreateCatalog(ctx context.Context, payload *DellCatalogDetails) (*DellCatalogDetails, error) {
	url := d.Client.Config.URL.JoinPath("api", "UpdateService", "Catalogs")

	payloadBody, err := json.Marshal(payload)
	if err != nil {
		err = fmt.Errorf("failed stringify create catalog body with error: %v", err)
		return nil, err
	}
	err = d.Client.Post(ctx, url, strings.NewReader(string(payloadBody)), payload, []int{http.StatusCreated})
	if err != nil {
		return nil, fmt.Errorf("failed to create catalog with error %w", err)
	}
	return payload, nil
}

func (d *OME) GetJobDetails(ctx context.Context, JobId int) (*DellJob, error) {
	url := d.Client.Config.URL.JoinPath("api", "JobService", fmt.Sprintf("Jobs(%d)", JobId))

	jobDetail := &DellJob{}
	if err := d.Client.Get(ctx, url, jobDetail, []int{http.StatusOK}); err != nil {
		return nil, fmt.Errorf("failed to fetch Job details for %v: from url: %v,\n with error %w", JobId, url, err)
	}
	return jobDetail, nil
}

func (d *OME) GetAllJobDetails(ctx context.Context) ([]DellJob, error) {
	url := d.Client.Config.URL.JoinPath("api", "JobService", "Jobs")

	if jobDetailsFetched, err := d.GetAllData(ctx, url, DellJob{}, []int{http.StatusOK}); err != nil {
		return nil, fmt.Errorf("failed to fetch Job details from url: %v,\n with error %w", url, err)
	} else {
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
}

func (d *OME) GetJobHistory(ctx context.Context, JobId int) ([]DellJobHistory, error) {
	jobDetails, err := d.GetJobDetails(ctx, JobId)
	if err != nil {
		return nil, err
	}

	url, err := d.Client.Config.URL.Parse(jobDetails.Histories)
	if err != nil {
		return nil, fmt.Errorf("failed to parse job history URL %q: %w", jobDetails.Histories, err)
	}

	if jobHistoryFetched, err := d.GetAllData(ctx, url, DellJobHistory{}, []int{http.StatusOK}); err != nil {
		return nil, fmt.Errorf("failed to fetch Job History details from url: %v,\n with error %w", url, err)
	} else {
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
}

func (d *OME) CreateBaseline(ctx context.Context, payload *DellBaseline) (*DellBaseline, error) {
	url := d.Client.Config.URL.JoinPath("api", "UpdateService", "Baselines")

	payloadBody, err := json.Marshal(payload)
	if err != nil {
		err = fmt.Errorf("failed stringify CreateBaseline body with error: %v", err)
		return nil, err
	}
	err = d.Client.Post(ctx, url, strings.NewReader(string(payloadBody)), payload, []int{http.StatusCreated})
	if err != nil {
		return nil, fmt.Errorf("failed to create Baselines with error %w", err)
	}
	return payload, nil
}

func (d *OME) GetAllBaseline(ctx context.Context) ([]DellBaseline, error) {
	url := d.Client.Config.URL.JoinPath("api", "UpdateService", "Baselines")

	if baselinesDetails, err := d.GetAllData(ctx, url, DellBaseline{}, []int{http.StatusOK}); err != nil {
		return nil, fmt.Errorf("failed to fetch Baseline details from url: %v,\n with error %w", url, err)
	} else {
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
}

func (d *OME) UpdateBaseline(ctx context.Context, baselineId int, payload *DellBaseline) (*DellBaseline, error) {
	url := d.Client.Config.URL.JoinPath("api", "UpdateService", fmt.Sprintf("Baselines(%d)", baselineId))
	payloadBody, err := json.Marshal(payload)
	if err != nil {
		err = fmt.Errorf("failed stringify CreateBaseline body with error: %v", err)
		return nil, err
	}
	if _, err := d.Client.PutWithResponse(
		ctx,
		url,
		strings.NewReader(string(payloadBody)),
		[]int{http.StatusOK},
	); err != nil {
		return nil, err
	}
	baselineDetails := &DellBaseline{}
	if err := d.Client.Get(ctx, url, baselineDetails, []int{http.StatusOK}); err != nil {
		return nil, fmt.Errorf("failed to fetch Baseline details for %v: from url: %v,\n with error %w", baselineId, url, err)
	}
	return baselineDetails, nil
}

func (d *OME) GetComplianceReportForBaseline(
	ctx context.Context,
	baselineID int,
) ([]DellDeviceComplianceReport, error) {
	url := d.Client.Config.URL.JoinPath(
		"api",
		"UpdateService",
		fmt.Sprintf("Baselines(%d)", baselineID),
		"DeviceComplianceReports",
	)

	if complianceReports, err := d.GetAllData(ctx, url, DellDeviceComplianceReport{}, []int{http.StatusOK}); err != nil {
		return nil, fmt.Errorf("failed to fetch Device Compliance Report details from url: %v,\n with error %w", url, err)
	} else {
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
}

func (d *OME) CreateFirmwareUpdateJob(ctx context.Context, payload *DellFirmwareUpdatePayload) (*DellJob, error) {
	url := d.Client.Config.URL.JoinPath("api", "JobService", "Jobs")
	payloadBody, err := json.Marshal(payload)
	if err != nil {
		err = fmt.Errorf("failed stringify CreateFirmwareUpdateJob body with error: %v", err)
		return nil, err
	}
	jobDetail := &DellJob{}
	err = d.Client.Post(ctx, url, strings.NewReader(string(payloadBody)), jobDetail, []int{http.StatusCreated})
	if err != nil {
		return nil, fmt.Errorf("failed to create Firmware Update Job with error %w", err)
	}
	return jobDetail, nil
}

func (d *OME) RunJobNow(ctx context.Context, jobIDs []int) error {
	url := d.Client.Config.URL.JoinPath("api", "JobService", "Actions", "JobService.RunJobs")

	bodyMap := map[string]any{
		"JobIds":  jobIDs,
		"AllJobs": len(jobIDs) == 0,
	}
	bodyString, err := json.Marshal(bodyMap)
	if err != nil {
		err = fmt.Errorf("failed stringify RefreshCatalog body with error: %v", err)
		return err
	}
	_, err = d.Client.PostWithResponse(ctx, url, strings.NewReader(string(bodyString)), []int{http.StatusNoContent})
	if err != nil {
		return err
	}
	return nil
}
