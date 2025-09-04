package ome

import (
	"encoding/json"
	"fmt"
	"net/http"
	neturl "net/url"
	"reflect"
	"strings"

	"github.com/ironcore-dev/maintenance-operator/ManagerConsole/client"
)

// OME defines an interface for interacting with a Open Manager Enterprice (OME) for dell servers.
type OME struct {
	Client     *client.ManagerClient
	AuthConfig *client.AuthToken
	Config     *client.Config
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
	DeviceID           int                  `json:"Id"`
	Name               string               `json:"DeviceName"`
	UUID               string               `json:"Identifier"`
	ManagedState       DellManagedState     `json:"ManagedState"`
	OverallHealthState DellDeviceStatusCode `json:"Status"`
	AccessState        DellAccessState      `json:"ConnectionState"`
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
	Id    int            `json:"Id"`
	JobId int            `json:"JobId,omitempty"`
	Type  DellTargetType `json:"Type"`
	Data  string         `json:"Data,omitempty"`
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

type Dell struct {
	DeviceID           int
	UUID               string
	RemoteBoardDNSName string
}

func (d *OME) GetTasksStatusMap() (statuses map[int]string, err error) {
	url := d.Config.URL.JoinPath("api", "JobService", "JobStatuses")
	statuslist := &ODataList[DellJobStatus]{}
	err = d.Client.Get(url, statuslist, []int{http.StatusOK})
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

func (d *OME) GetTaskTypeMap() (tasksTypes map[int]string, err error) {
	url := d.Config.URL.JoinPath("api", "JobService", "JobTypes")
	JobTypeslist := &ODataList[DellJobType]{}
	err = d.Client.Get(url, JobTypeslist, []int{http.StatusOK})
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

func (d *OME) GetAllData(url *neturl.URL, returnData any, okCodes []int) (*ODataList[any], error) {

	next_url := url

	returnDataList := &ODataList[any]{}
	for next_url != nil {
		resBody, err := d.Client.GetResponseBody(url, okCodes)
		if err != nil {
			return nil, fmt.Errorf("failed to read response from: %v. \n with error: %w", url.String(), err)
		}

		data := make(map[string]any)
		if err = json.Unmarshal(resBody, &data); err != nil {
			return nil, fmt.Errorf("failed to decode response from: %v. \nresponse: %v \twith error: %v", url.String(), string(resBody), err)
		}
		if nextURL, ok := data["@odata.nextLink"]; ok {
			nextURLStr, ok := nextURL.(string)
			if !ok {
				return nil, fmt.Errorf("nextLink is not a string: %v", nextURL)
			}
			next_url, err = neturl.Parse(url.Scheme + "://" + url.Host + nextURLStr)
			if err != nil {
				return nil, fmt.Errorf("failed to parse nextLink URL: %v", err)
			}
		} else {
			next_url = nil // No next link, stop the loop
		}

		if err = json.Unmarshal(resBody, returnData); err != nil {
			return nil, fmt.Errorf("failed to decode response from: %v. \nresponse: %v \twith error: %v", url.String(), string(resBody), err)
		}
		returnDataList.Value = append(returnDataList.Value, reflect.ValueOf(returnData).Elem())
	}
	return returnDataList, nil
}

func (d *OME) RefreshCatalog(CatalogIds []int) error {
	url := d.Config.URL.JoinPath("api", "UpdateService", "Actions", "UpdateService.RefreshCatalogs")

	bodyMap := map[string]any{
		"CatalogIds":  CatalogIds,
		"AllCatalogs": len(CatalogIds) != 0,
	}
	bodyString, err := json.Marshal(bodyMap)
	if err != nil {
		err = fmt.Errorf("failed stringify RefreshCatalog body with error: %v", err)
		return err
	}

	_, err = d.Client.PostWithResponse(url, strings.NewReader(string(bodyString)), []int{http.StatusOK})
	if err != nil {
		return fmt.Errorf("failed to read response body from: %v. \n with error: %w", url.String(), err)
	}
	// figure out what to do with the response
	return nil
}

func (d *OME) GetCatalog(catalogId int) (*DellCatalogDetails, error) {
	url := d.Config.URL.JoinPath("api", "UpdateService", fmt.Sprintf("Catalogs(%d)", catalogId))
	catalogDetail := &DellCatalogDetails{}
	if err := d.Client.Get(url, catalogDetail, []int{http.StatusOK}); err != nil {
		return nil, fmt.Errorf("failed to fetch catalog details for %v: from url: %v,\n with error %w", catalogId, url, err)
	}
	return catalogDetail, nil
}

func (d *OME) GetAllCatalogs() (*ODataList[DellCatalogDetails], error) {
	url := d.Config.URL.JoinPath("api", "UpdateService", "Catalogs")
	if catalogDetailsFetched, err := d.GetAllData(url, DellCatalogDetails{}, []int{http.StatusOK}); err != nil {
		return nil, fmt.Errorf("failed to fetch catalog details from url: %v,\n with error %w", url, err)
	} else {
		catalogDetails := &ODataList[DellCatalogDetails]{}
		for _, v := range catalogDetailsFetched.Value {
			switch v := v.(type) {
			case DellCatalogDetails:
				catalogDetails.Value = append(catalogDetails.Value, v)
			default:
				return nil, fmt.Errorf("cannot convert type %T to DellDeviceData, data: %v", v, catalogDetailsFetched)
			}
		}
		return catalogDetails, nil
	}
}

func (d *OME) CreateCatalog(payload DellCatalogDetails) (*DellCatalogDetails, error) {
	url := d.Config.URL.JoinPath("api", "UpdateService", "Catalogs")

	payloadBody, err := json.Marshal(payload)
	if err != nil {
		err = fmt.Errorf("failed stringify CreateCatalog body with error: %v", err)
		return nil, err
	}
	err = d.Client.Post(url, strings.NewReader(string(payloadBody)), payload, []int{http.StatusCreated})
	if err != nil {
		return nil, fmt.Errorf("failed to create catalog with error %w", err)
	}
	return &payload, nil
}

func (d *OME) GetJobDetails(JobId int) (*DellJob, error) {
	url := d.Config.URL.JoinPath("api", "JobService", fmt.Sprintf("Jobs(%d)", JobId))

	jobDetail := &DellJob{}
	if err := d.Client.Get(url, jobDetail, []int{http.StatusOK}); err != nil {
		return nil, fmt.Errorf("failed to fetch Job details for %v: from url: %v,\n with error %w", JobId, url, err)
	}
	return jobDetail, nil
}

func (d *OME) GetAllJobDetails() (*ODataList[DellJob], error) {
	url := d.Config.URL.JoinPath("api", "JobService", "Jobs")

	if jobDetailsFetched, err := d.GetAllData(url, DellJob{}, []int{http.StatusOK}); err != nil {
		return nil, fmt.Errorf("failed to fetch Job details from url: %v,\n with error %w", url, err)
	} else {
		jobDetails := &ODataList[DellJob]{}
		for _, v := range jobDetailsFetched.Value {
			switch v := v.(type) {
			case DellJob:
				jobDetails.Value = append(jobDetails.Value, v)
			default:
				return nil, fmt.Errorf("cannot convert type %T to DellJob, data: %v", v, jobDetailsFetched)
			}
		}
		return jobDetails, nil
	}
}

func (d *OME) GetJobHistory(JobId int) (*ODataList[DellJobHistory], error) {
	jobDetails, err := d.GetJobDetails(JobId)

	if err != nil {
		return nil, err
	}
	url := d.Config.URL.JoinPath(jobDetails.Histories)

	if jobHistoryFetched, err := d.GetAllData(url, DellJobHistory{}, []int{http.StatusOK}); err != nil {
		return nil, fmt.Errorf("failed to fetch Job History details from url: %v,\n with error %w", url, err)
	} else {
		jobHistoryDetails := &ODataList[DellJobHistory]{}
		for _, v := range jobHistoryFetched.Value {
			switch v := v.(type) {
			case DellJobHistory:
				jobHistoryDetails.Value = append(jobHistoryDetails.Value, v)
			default:
				return nil, fmt.Errorf("cannot convert type %T to DellJobHistory, data: %v", v, jobHistoryFetched)
			}
		}
		return jobHistoryDetails, nil
	}
}

func (d *OME) CreateBaseline(payload DellBaseline) (*DellBaseline, error) {
	url := d.Config.URL.JoinPath("api", "UpdateService", "Baselines")

	payloadBody, err := json.Marshal(payload)
	if err != nil {
		err = fmt.Errorf("failed stringify CreateBaseline body with error: %v", err)
		return nil, err
	}
	err = d.Client.Post(url, strings.NewReader(string(payloadBody)), payload, []int{http.StatusCreated})
	if err != nil {
		return nil, fmt.Errorf("failed to create Baselines with error %w", err)
	}
	return &payload, nil
}

func (d *OME) GetComplianceReportForBaseline(baselineID string) (*ODataList[DellDeviceComplianceReport], error) {
	url := d.Config.URL.JoinPath("api", "UpdateService", fmt.Sprintf("Baselines(%s)", baselineID), "DeviceComplianceReports")

	if complianceReports, err := d.GetAllData(url, DellDeviceComplianceReport{}, []int{http.StatusOK}); err != nil {
		return nil, fmt.Errorf("failed to fetch Device Compliance Report details from url: %v,\n with error %w", url, err)
	} else {
		complianceReport := &ODataList[DellDeviceComplianceReport]{}
		for _, v := range complianceReports.Value {
			switch v := v.(type) {
			case DellDeviceComplianceReport:
				complianceReport.Value = append(complianceReport.Value, v)
			default:
				return nil, fmt.Errorf("cannot convert type %T to DellJobHistory, data: %v", v, complianceReports)
			}
		}
		return complianceReport, nil
	}
}

func (d *OME) CreateFirmwareUpdateJob(payload DellFirmwareUpdatePayload) (*DellJob, error) {
	url := d.Config.URL.JoinPath("api", "JobService", "Jobs")

	payloadBody, err := json.Marshal(payload)
	if err != nil {
		err = fmt.Errorf("failed stringify CreateFirmwareUpdateJob body with error: %v", err)
		return nil, err
	}
	jobDetail := &DellJob{}
	err = d.Client.Post(url, strings.NewReader(string(payloadBody)), jobDetail, []int{http.StatusCreated})
	if err != nil {
		return nil, fmt.Errorf("failed to create Firmware Update Job with error %w", err)
	}
	return jobDetail, nil
}

func (d *OME) RunJobNow(jobIDs []int) error {
	url := d.Config.URL.JoinPath("api", "JobService", "Actions", "JobService.RunJobs")

	bodyMap := map[string]any{
		"JobIds":  jobIDs,
		"AllJobs": len(jobIDs) == 0,
	}
	bodyString, err := json.Marshal(bodyMap)
	if err != nil {
		err = fmt.Errorf("failed stringify RefreshCatalog body with error: %v", err)
		return err
	}
	_, err = d.Client.PostWithResponse(url, strings.NewReader(string(bodyString)), []int{http.StatusNoContent})
	if err != nil {
		return fmt.Errorf("failed to create Job at url %v, payload %v with error %w", url, bodyMap, err)
	}
	return nil
}
