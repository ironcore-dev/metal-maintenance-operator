package ome

type DellManagedState int

const (
	DellManagedStateManaged          DellManagedState = 3000
	DellManagedStateManagedWithAlert DellManagedState = 6000
)

var (
	DellAuthBody = map[string]string{
		"UserName":    "",
		"Password":    "",
		"SessionType": "API",
	}
)

type DellDeviceStatusCode int

const (
	DellDeviceStatusNormal   DellDeviceStatusCode = 1000
	DellDeviceStatusUnknown  DellDeviceStatusCode = 2000
	DellDeviceStatusWarning  DellDeviceStatusCode = 3000
	DellDeviceStatusCritical DellDeviceStatusCode = 4000
	DellDeviceStatusNoStatus DellDeviceStatusCode = 5000
)

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

var JobURL = "/api/JobService/Jobs" // (%s)
var JonTypeURL = "/api/JobService/JobTypes"
var BaselineURL = "/api/UpdateService/Baselines"
var CatalogURL = "/api/UpdateService/Catalogs"
var ComplianceReportURL = "/api/UpdateService/Baselines(%s)/DeviceComplianceReports"
var SessionURL = "/api/SessionService/Sessions"
var RefreshCatalogURL = "/api/UpdateService/Actions/UpdateService.RefreshCatalogs"
var DeviceURL = "/api/DeviceService/Devices"
var DeviceTypeURL = "/api/DeviceService/DeviceTypes"
var RefreshComplianceData = "/api/JobService/Actions/JobService.RunJobs" // figure out how to use this
