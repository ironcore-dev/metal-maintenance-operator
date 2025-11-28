package ov

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

// Session defines the structure of a session object from the OV API.
type Session struct {
	ClientHost      string       `json:"clientHost"`
	LoggingID       string       `json:"loggingID"`
	LoginDomain     string       `json:"loginDomain"`
	Permissions     []Permission `json:"permissions"`
	ServerHost      string       `json:"serverHost"`
	SessionID       string       `json:"sessionID"`
	UserDefinedData string       `json:"userDefinedData"`
	Username        string       `json:"username"`
}

// Permission defines the structure of a permission object within a session.
type Permission struct {
	RoleName string  `json:"roleName"`
	ScopeURI *string `json:"scopeUri"`
	Active   bool    `json:"active"`
}

type ServerPowerControl string

const (
	// ColdBoot Reset the server using the cold boot control.
	// This is a hard reset that immediately removes power from the server hardware and
	// restarts the server after approximately eight seconds.
	ColdBoot ServerPowerControl = "ColdBoot"
	// MomentaryPress Power on or a normal (soft) power off, depending on powerState.
	MomentaryPress ServerPowerControl = "MomentaryPress"
	// PressAndHold Immediately power off the server using the press and hold control.
	PressAndHold ServerPowerControl = "PressAndHold"
	// Reset Reset the server using the reset control.
	// This will perform an immediate warm reboot of the server without removing power.
	Reset ServerPowerControl = "Reset"
)

type serverPowerState string

const (
	// ServerPowerStateOff Power off
	ServerPowerStateOff serverPowerState = "Off"
	// ServerPowerStateOn Power on
	ServerPowerStateOn serverPowerState = "On"
	// ServerPowerStatePoweringOff Powering off
	ServerPowerStatePoweringOff serverPowerState = "PoweringOff"
	// ServerPowerStatePoweringOn Powering on
	ServerPowerStatePoweringOn serverPowerState = "PoweringOn"
	// ServerPowerStateResetting Resetting
	ServerPowerStateResetting serverPowerState = "Resetting"
	// ServerPowerStateUnknown Unable to determine server power state.
	ServerPowerStateUnknown serverPowerState = "Unknown"
)

// HPEServer defines the structure of a server hardware object from the OV API.
type HPEServer struct {
	Type                  string           `json:"type"`
	Name                  string           `json:"name"`
	State                 string           `json:"state"`
	StateReason           string           `json:"stateReason"`
	AssetTag              string           `json:"assetTag"`
	Category              string           `json:"category"`
	Created               string           `json:"created"`
	Description           string           `json:"description"`
	ETag                  string           `json:"eTag"`
	FormFactor            string           `json:"formFactor"`
	LicensingIntent       string           `json:"licensingIntent"`
	LocationURI           string           `json:"locationUri"`
	MemoryMb              int              `json:"memoryMb"`
	Model                 string           `json:"model"`
	Modified              string           `json:"modified"`
	MpDNSName             string           `json:"mpDnsName"`
	MpFirmwareVersion     string           `json:"mpFirmwareVersion"`
	MpIPAddress           string           `json:"mpIpAddress"`
	MpModel               string           `json:"mpModel"`
	PartNumber            string           `json:"partNumber"`
	PortMap               PortMap          `json:"portMap"`
	Position              int              `json:"position"`
	PowerLock             bool             `json:"powerLock"`
	PowerState            serverPowerState `json:"powerState"`
	ProcessorCoreCount    int              `json:"processorCoreCount"`
	ProcessorCount        int              `json:"processorCount"`
	ProcessorSpeedMhz     int              `json:"processorSpeedMhz"`
	ProcessorType         string           `json:"processorType"`
	RefreshState          string           `json:"refreshState"`
	RomVersion            string           `json:"romVersion"`
	SerialNumber          string           `json:"serialNumber"`
	ServerGroupURI        string           `json:"serverGroupUri"`
	ServerHardwareTypeURI string           `json:"serverHardwareTypeUri"`
	ServerProfileURI      string           `json:"serverProfileUri"`
	ShortModel            string           `json:"shortModel"`
	Signature             any              `json:"signature"`
	Status                string           `json:"status"`
	URI                   string           `json:"uri"`
	UUID                  string           `json:"uuid"`
	VirtualSerialNumber   string           `json:"virtualSerialNumber"`
	VirtualUUID           string           `json:"virtualUuid"`
}

// PortMap defines the structure of the port map within a server hardware object.
type PortMap struct {
	DeviceSlots []DeviceSlot `json:"deviceSlots"`
}

// DeviceSlot defines the structure of a device slot within a port map.
type DeviceSlot struct {
	DeviceName    string         `json:"deviceName"`
	Location      string         `json:"location"`
	OASlotNumber  int            `json:"oaSlotNumber"`
	PhysicalPorts []PhysicalPort `json:"physicalPorts"`
	SlotNumber    int            `json:"slotNumber"`
}

// PhysicalPort defines the structure of a physical port within a device slot.
type PhysicalPort struct {
	InterconnectPort int     `json:"interconnectPort"`
	InterconnectURI  *string `json:"interconnectUri"`
	MAC              string  `json:"mac"`
	PortNumber       int     `json:"portNumber"`
	Type             string  `json:"type"`
	VirtualPorts     []any   `json:"virtualPorts"`
}

// HPEServerProfile defines the structure of a server profile object from the OV API.
type HPEServerProfile struct {
	Type                       string          `json:"type"`
	URI                        string          `json:"uri"`
	ProfileUUID                string          `json:"profileUUID"`
	Name                       string          `json:"name"`
	Description                string          `json:"description"`
	SerialNumber               string          `json:"serialNumber"`
	UUID                       string          `json:"uuid"`
	ServerProfileTemplateURI   string          `json:"serverProfileTemplateUri"`
	ServerHardwareURI          string          `json:"serverHardwareUri"`
	ServerHardwareTypeURI      string          `json:"serverHardwareTypeUri"`
	EnclosureGroupURI          *string         `json:"enclosureGroupUri"`
	EnclosureURI               *string         `json:"enclosureUri"`
	EnclosureBay               any             `json:"enclosureBay"`
	Affinity                   any             `json:"affinity"`
	AssociatedServer           string          `json:"associatedServer"`
	Firmware                   FirmwareProfile `json:"firmware"`
	MACType                    string          `json:"macType"`
	WWNType                    string          `json:"wwnType"`
	SerialNumberType           string          `json:"serialNumberType"`
	Category                   string          `json:"category"`
	Created                    string          `json:"created"`
	Modified                   string          `json:"modified"`
	Status                     string          `json:"status"`
	State                      string          `json:"state"`
	InProgress                 bool            `json:"inProgress"`
	TaskURI                    string          `json:"taskUri"`
	Boot                       any             `json:"boot"`
	BIOS                       BIOSProfile     `json:"bios"`
	LocalStorage               any             `json:"localStorage"`
	SANStorage                 any             `json:"sanStorage"`
	OSDeploymentSettings       any             `json:"osDeploymentSettings"`
	ScopesURI                  string          `json:"scopesUri"`
	ETag                       string          `json:"eTag"`
	RefreshState               string          `json:"refreshState"`
	ServerHardwareReapplyState string          `json:"serverHardwareReapplyState"`
}

type FirmwareInstallAction string

const (
	// Activate initiates the installation and activation process for the components that were previously staged.
	FirmwareInstallActionActivate FirmwareInstallAction = "Activate"
	// Stage stages the components on the iLO for activation later.
	FirmwareInstallActionStage FirmwareInstallAction = "Stage"
	// Update initiates both staging and activation of components as a single operation.
	// This is the default option and behavior.
	FirmwareInstallActionUpdate FirmwareInstallAction = "Update"
)

type ConsistencyState string

const (
	// Consistent Component consistency state is Consistent.
	ConsistencyStateConsistent ConsistencyState = "Consistent"
	// Inconsistent Component consistency state is Inconsistent.
	ConsistencyStateInconsistent ConsistencyState = "Inconsistent"
	// Unknown consistency state of the component is Unknown.
	ConsistencyStateUnknown ConsistencyState = "Unknown"
)

// FirmwareProfile defines the structure of the firmware object within a server profile.
type FirmwareProfile struct {
	// Identifies the firmware baseline to be applied to the server hardware.
	FirmwareBaselineURI string `json:"firmwareBaselineUri"`
	// Indicates that the server firmware should be configured on the server profiles
	// created from the template. Value can be 'true' or 'false'.
	ManageFirmware bool `json:"manageFirmware"`
	// Force installation of firmware even if same or newer version is installed.
	// Downgrading the firmware can result in the installation of unsupported firmware
	// which can cause the hardware to cease operating. Value can be 'true' or 'false'.
	ForceInstallFirmware bool `json:"forceInstallFirmware"`
	// Specifies the way a firmware baseline is installed. This field is used if the 'manageFirmware' field is true.
	FirmwareInstallType FirmwareInstall `json:"firmwareInstallType"`
	// Identifies the date and time the firmware baseline will be activated.
	FirmwareScheduleDateTime string `json:"firmwareScheduleDateTime"`
	// Specifies when the applied firmware baseline will be activated.
	FirmwareActivationType FirmwareActivation `json:"firmwareActivationType"`
	// readonly value, indicates if the settings are being applied or pending application.
	ReapplyState string `json:"reapplyState"`
	//  Consistency state of the firmware component.
	ConsistencyState      ConsistencyState `json:"consistencyState"`
	SDFlexIoFwBaselineURI string           `json:"sdFlexIoFwBaselineUri"`
	// Specifies if the firmware update operation should initiate the staging and
	// activation of components as one operation or allows to perform staging and activation as separate steps.
	// Performing staging and activation as separate steps is only supported for online firmware updates.
	FirmwareInstallAction FirmwareInstallAction `json:"firmwareInstallAction"`
	InstallationPolicy    string                `json:"installationPolicy"`
	SkipAutoRetry         bool                  `json:"skipAutoRetry"`
	PatchLevel            any                   `json:"patchLevel"`
	UpdateSppID           any                   `json:"updateSppId"`
}

// BIOSProfile defines the structure of the BIOS object within a server profile.
type BIOSProfile struct {
	ManageBIOS         bool                `json:"manageBios"`
	OverriddenSettings []OverriddenSetting `json:"overriddenSettings"`
}

// OverriddenSetting defines the structure of an overridden setting within a BIOS profile.
type OverriddenSetting struct {
	ID    string `json:"id"`
	Value string `json:"value"`
}

// HPEServerProfileTemplate defines the structure of a server profile template object from the OV API.
type HPEServerProfileTemplate struct {
	Type                      string           `json:"type"`
	URI                       string           `json:"uri"`
	Name                      string           `json:"name"`
	Description               string           `json:"description"`
	ServerProfileDescription  string           `json:"serverProfileDescription"`
	ServerHardwareTypeURI     string           `json:"serverHardwareTypeUri"`
	EnclosureGroupURI         string           `json:"enclosureGroupUri"`
	MACType                   string           `json:"macType"`
	WWNType                   string           `json:"wwnType"`
	SerialNumberType          string           `json:"serialNumberType"`
	IscsiInitiatorNameType    string           `json:"iscsiInitiatorNameType"`
	HostNVMeQualifiedNameType string           `json:"hostNVMeQualifiedNameType"`
	Firmware                  FirmwareTemplate `json:"firmware"`
	Category                  string           `json:"category"`
	Created                   string           `json:"created"`
	Modified                  string           `json:"modified"`
	Status                    string           `json:"status"`
	ScopesURI                 string           `json:"scopesUri"`
	ETag                      string           `json:"eTag"`
	RefreshState              string           `json:"refreshState"`
}

type ComplianceControl string

const (
	// The server profile settings must exactly match the server profile template settings.
	// "Checked" is equivalent to the "Exact Match" option in the OneView user interface.
	ComplianceControlChecked ComplianceControl = "Checked"
	// This compliance option is only applicable to collections of resources,
	// e.g. connections and volume attachments.
	// A section is considered compliant if each resource in the server profile template
	// collection has an exact match in the server profile collection.
	// Extra resources in the server profile collection are not considered when evaluating compliance.
	// "CheckedMinimum" is equivalent to the "Minimum Match" option in the OneView user interface.
	ComplianceControlUnChecked ComplianceControl = "Unchecked"
	// Differences between the server profile template and server profile are not evaluated.
	// "Unchecked" is equivalent to the "Not Checked" option in the OneView user interface.
	ComplianceControlCheckedMinimum ComplianceControl = "CheckedMinimum"
)

type FirmwareActivation string

const (
	// The selected firmware baseline will be immediately activated.
	FirmwareActivationeImmediate FirmwareActivation = "Immediate"
	// The selected firmware baseline will be activated at the specified time.
	FirmwareActivationScheduled FirmwareActivation = "Scheduled"
	// The selected firmware baseline will not be activated.
	FirmwareActivationNotScheduled FirmwareActivation = "NotScheduled"
)

// FirmwareInstall specifies the way a firmware baseline is installed.
// This field is used if the 'manageFirmware' field is true.
type FirmwareInstall string

const (
	// FirmwareInstallTypeFirmwareOnly updates the firmware without powering down
	// the server hardware using using Smart Update Tool.
	FirmwareInstallTypeFirmwareOnly FirmwareInstall = "FirmwareOnly"
	// FirmwareInstallTypeFirmwareAndOSDrivers updates the firmware and OS drivers
	// without powering down the server hardware using Smart Update Tool.
	FirmwareInstallTypeFirmwareAndOSDrivers FirmwareInstall = "FirmwareAndOSDrivers"
	// FirmwareInstallTypeFirmwareOnlyOfflineMode manages the firmware through HPE OneView.
	// Selecting this option requires the server hardware to be powered down.
	FirmwareInstallTypeFirmwareOnlyOfflineMode FirmwareInstall = "FirmwareOnlyOfflineMode"
)

// FirmwareTemplate defines the structure of the firmware object within a server profile template.
type FirmwareTemplate struct {
	// Defines the compliance type of template's firmware settings with the corresponding profile's firmware settings.
	// Valid values are "Checked" and "Unchecked".
	ComplianceControl ComplianceControl `json:"complianceControl"`
	// Indicates that the server firmware should be configured on the server profiles created from the template.
	// Value can be 'true' or 'false'.
	ManageFirmware bool `json:"manageFirmware"`
	// Specifies the way a firmware baseline is installed. This field is used if the 'manageFirmware' field is true.
	FirmwareInstallType FirmwareInstall `json:"firmwareInstallType"`
	// Force installation of firmware even if same or newer version is installed.
	// Downgrading the firmware can result in the installation of unsupported firmware
	// which can cause the hardware to cease operating.
	// Value can be 'true' or 'false'.
	ForceInstallFirmware bool `json:"forceInstallFirmware"`
	// Identifies the firmware baseline to be applied to the server hardware.
	FirmwareBaselineURI string `json:"firmwareBaselineUri"`
	// Specifies when the applied firmware baseline will be activated.
	FirmwareActivationType FirmwareActivation `json:"firmwareActivationType"`
	InstallationPolicy     string             `json:"installationPolicy"`
}

// HPEFirmwareComplianceReport defines the structure of the firmware compliance check response.
type HPEFirmwareComplianceReport struct {
	ComponentMappingList         []ComponentMapping `json:"componentMappingList"`
	ServerFirmwareUpdateRequired bool               `json:"serverFirmwareUpdateRequired"`
}

// ComponentMapping defines the structure of a component mapping within the compliance response.
type ComponentMapping struct {
	ComponentKey                    *string `json:"componentKey"`
	ComponentLocation               string  `json:"componentLocation"`
	ComponentName                   string  `json:"componentName"`
	ComponentType                   string  `json:"componentType"`
	InstalledVersion                string  `json:"installedVersion"`
	BaselineVersion                 string  `json:"baselineVersion"`
	ComponentFirmwareUpdateRequired bool    `json:"componentFirmwareUpdateRequired"`
	HpsumManaged                    bool    `json:"hpsumManaged"`
}

// FirmwareBundleDetails defines the structure of a firmware bundle object from the OV API.
type FirmwareBundleDetails struct {
	ResourceID         string              `json:"resourceId"`
	UUID               string              `json:"uuid"`
	XMLKeyName         string              `json:"xmlKeyName"`
	ISOFileName        string              `json:"isoFileName"`
	BaselineShortName  string              `json:"baselineShortName"`
	BundleSize         int64               `json:"bundleSize"`
	Version            string              `json:"version"`
	ReleaseDate        string              `json:"releaseDate"`
	SupportedOSList    []string            `json:"supportedOSList"`
	SupportedLanguages string              `json:"supportedLanguages"`
	FWComponents       []FirmwareComponent `json:"fwComponents"`
	SWPackagesFullPath string              `json:"swPackagesFullPath"`
	State              string              `json:"state"`
	LastTaskURI        string              `json:"lastTaskUri"`
	URI                string              `json:"uri"`
	Category           string              `json:"category"`
	ETag               string              `json:"eTag"`
	Created            string              `json:"created"`
	Modified           string              `json:"modified"`
	ResourceState      string              `json:"resourceState"`
	Description        string              `json:"description"`
	Name               string              `json:"name"`
	Status             string              `json:"status"`
}

// FirmwareComponent defines the structure of a firmware component within a bundle.
type FirmwareComponent struct {
	Name             string   `json:"name"`
	ComponentVersion string   `json:"componentVersion"`
	FileName         string   `json:"fileName"`
	SWKeyNameList    []string `json:"swKeyNameList"`
}

type TaskState string

const (
	// Cancelled Task execution was cancelled.
	TaskStateCancelled TaskState = "Cancelled"
	// Cancelling Task is marked for Cancellation by user.
	TaskStateCancelling TaskState = "Cancelling"
	// Completed Task execution has completed successfully.
	TaskStateCompleted TaskState = "Completed"
	// Error Task execution ended with an error.
	TaskStateError TaskState = "Error"
	// Interrupted Task execution has been interrupted.
	TaskStateInterrupted TaskState = "Interrupted"
	// Killed Task was non-gracefully cancelled by the user.
	TaskStateKilled TaskState = "Killed"
	// New Task is new.
	TaskStateNew TaskState = "New"
	// Pending Task is queued for later execution.
	TaskStatePending TaskState = "Pending"
	// Running Task is executing.
	TaskStateRunning TaskState = "Running"
	// Starting Task parameters are validated.
	TaskStateStarting TaskState = "Starting"
	// Stopping Task is in the process of shutting-down.
	TaskStateStopping TaskState = "Stopping"
	// Suspended Task is in an idle state.
	TaskStateSuspended TaskState = "Suspended"
	// Terminated Task was gracefully cancelled by the user.
	TaskStateTerminated TaskState = "Terminated"
	// Unknown State of task is unknown.
	TaskStateUnknown TaskState = "Unknown"
	// Warning Task execution completed successfully, but with warnings.
	TaskStateWarning TaskState = "Warning"
)

var UnSuccessfulTaskStates = []TaskState{
	TaskStateCancelled,
	TaskStateError,
	TaskStateInterrupted,
	TaskStateKilled,
	TaskStateUnknown,
	TaskStateTerminated,
	TaskStateWarning,
}

// HPETaskDetails defines the structure of a task object from the OV API.
type HPETaskDetails struct {
	AssociatedResource      AssociatedResource `json:"associatedResource"`
	AssociatedTaskURI       *string            `json:"associatedTaskUri"`
	CompletedSteps          int                `json:"completedSteps"`
	ComputedPercentComplete int                `json:"computedPercentComplete"`
	Created                 string             `json:"created"`
	Data                    map[string]any     `json:"data"`
	ETag                    string             `json:"eTag"`
	ExpectedDuration        int                `json:"expectedDuration"`
	Hidden                  bool               `json:"hidden"`
	Modified                string             `json:"modified"`
	Name                    string             `json:"name"`
	Owner                   string             `json:"owner"`
	ParentTaskURI           string             `json:"parentTaskUri"`
	PercentComplete         int                `json:"percentComplete"`
	ProgressUpdates         []ProgressUpdate   `json:"progressUpdates"`
	StartTime               string             `json:"startTime"`
	StateReason             string             `json:"stateReason"`
	TaskErrors              []TaskError        `json:"taskErrors"`
	TaskOutput              []string           `json:"taskOutput"`
	TaskState               TaskState          `json:"taskState"`
	TaskStatus              string             `json:"taskStatus"`
	TaskType                string             `json:"taskType"`
	TotalSteps              int                `json:"totalSteps"`
	URI                     string             `json:"uri"`
	UserInitiated           bool               `json:"userInitiated"`
	Category                string             `json:"category"`
	Type                    string             `json:"type"`
	IsCancellable           bool               `json:"isCancellable"`
}

// TaskError defines the structure of an error object within a task.
type TaskError struct {
	Data               map[string]string `json:"data"`
	Details            string            `json:"details"`
	ErrorCode          string            `json:"errorCode"`
	ErrorSource        string            `json:"errorSource"`
	Message            string            `json:"message"`
	MessageParameters  []any             `json:"messageParameters"`
	NestedErrors       []TaskError       `json:"nestedErrors"`
	RecommendedActions []string          `json:"recommendedActions"`
}

// AssociatedResource defines the structure of an associated resource within a task.
type AssociatedResource struct {
	AssociationType  string `json:"associationType"`
	ResourceCategory string `json:"resourceCategory"`
	ResourceName     string `json:"resourceName"`
	ResourceURI      string `json:"resourceUri"`
}

// ProgressUpdate defines the structure of a progress update within a task.
type ProgressUpdate struct {
	ID           int    `json:"id"`
	StatusUpdate string `json:"statusUpdate"`
	Timestamp    string `json:"timestamp"`
}

// OV defines an interface for interacting with a One View (OV) for HPE servers.
type OV struct {
	Client *client.ManagerClient
}

func (h *OV) CloseSession(ctx context.Context) error {
	url := h.Client.Config.URL.JoinPath("rest", "sessions")
	req, err := h.Client.CreateRequestWithAuth(url, http.MethodDelete, nil, nil)
	if err != nil {
		return err
	}
	_, err = h.Client.DoRequest(ctx, req, []int{http.StatusNoContent})
	if err != nil {
		return err
	}
	return nil
}

func (h *OV) GetSession(ctx context.Context) (*Session, error) {
	url := h.Client.Config.URL.JoinPath("rest", "sessions")
	session := &Session{}
	err := h.Client.Get(ctx, url, session, []int{http.StatusOK})
	if err != nil {
		return nil, err
	}
	return session, nil
}

func (h *OV) GetAllData(ctx context.Context, url *neturl.URL, returnData any, okCodes []int) ([]any, error) {
	nextURL := url
	log := ctrl.LoggerFrom(ctx)

	var allValues []any
	targetType := reflect.TypeOf(returnData)

	for nextURL != nil {
		resBody, err := h.Client.GetResponseBody(ctx, nextURL, okCodes)
		if err != nil {
			return nil, err
		}
		// Use a temporary structure to unmarshal both the values and the nextLink
		var pageData struct {
			Members  json.RawMessage `json:"members"`
			NextLink string          `json:"nextPageUri"`
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

		if err = json.Unmarshal(pageData.Members, pageValues); err != nil {
			return nil, fmt.Errorf("failed to decode 'value' field from: %v. \nresponse: %v \twith error: %v",
				nextURL.String(), string(pageData.Members), err)
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

func (h *OV) buildFilterParams(filterParams []string) string {
	regexFilter := "("
	for idx, sn := range filterParams {
		if len(filterParams) == idx+1 {
			regexFilter += fmt.Sprintf("^%s$", sn)
			break
		}
		regexFilter += fmt.Sprintf("^%s$|", sn)
	}
	regexFilter += ")"
	return regexFilter
}

func (h *OV) GetServersFromSerialNumber( // nolint:dupl
	ctx context.Context,
	serialNumbers []string,
) ([]HPEServer, error) {
	url := h.Client.Config.URL.JoinPath("rest", "server-hardware")
	// build regex filter as the query is not supported atm for this URI

	if len(serialNumbers) == 0 {
		return []HPEServer{}, nil
	}
	serialNumbersMap := make(map[string]struct{})
	if len(serialNumbers) < 15 {
		filterParams := h.buildFilterParams(serialNumbers)
		url.RawQuery = fmt.Sprintf("filter=\"'serialNumber'regex'%s'\"", filterParams)
	}

	for _, sn := range serialNumbers {
		serialNumbersMap[sn] = struct{}{}
	}

	if serverDetails, err := h.GetAllData(ctx, url, HPEServer{}, []int{http.StatusOK}); err != nil {
		return nil, fmt.Errorf("failed to fetch Device details from url: %v,\n with error %w", url, err)
	} else {
		serverDetailsList := make([]HPEServer, 0, len(serverDetails))
		for _, v := range serverDetails {
			if item, ok := v.(HPEServer); ok {
				if len(serialNumbers) != len(serverDetails) {
					// filter the results as we could not use query param for filtering
					if _, exists := serialNumbersMap[item.SerialNumber]; exists {
						serverDetailsList = append(serverDetailsList, item)
					}
				} else {
					serverDetailsList = append(serverDetailsList, item)
				}
			} else {
				return nil, fmt.Errorf("cannot convert type %T to HPEServer, data: %v", v, serverDetails)
			}
		}
		return serverDetailsList, nil
	}
}

func (h *OV) GetServerProfilesFromSerialNumber( // nolint:dupl
	ctx context.Context,
	serialNumbers []string,
) ([]HPEServerProfile, error) {
	url := h.Client.Config.URL.JoinPath("rest", "server-profiles")
	if len(serialNumbers) == 0 {
		return []HPEServerProfile{}, nil
	}
	serialNumbersMap := make(map[string]struct{})
	if len(serialNumbers) < 15 {
		filterParams := h.buildFilterParams(serialNumbers)
		url.RawQuery = fmt.Sprintf(`filter="'serialNumber'regex'%s'"`, filterParams)
	}
	for _, sn := range serialNumbers {
		serialNumbersMap[sn] = struct{}{}
	}

	if serverProfileDetails, err := h.GetAllData(ctx, url, HPEServerProfile{}, []int{http.StatusOK}); err != nil {
		return nil, fmt.Errorf("failed to fetch Device details from url: %v,\n with error %w", url, err)
	} else {
		serverProfileDetailsList := make([]HPEServerProfile, 0, len(serverProfileDetails))
		for _, v := range serverProfileDetails {
			if item, ok := v.(HPEServerProfile); ok {
				if len(serialNumbers) != len(serverProfileDetails) {
					// filter the results as we could not use query param for filtering
					if _, exists := serialNumbersMap[item.SerialNumber]; exists {
						serverProfileDetailsList = append(serverProfileDetailsList, item)
					}
				} else {
					serverProfileDetailsList = append(serverProfileDetailsList, item)
				}
			} else {
				return nil, fmt.Errorf("cannot convert type %T to HPEServerProfile, data: %v", v, serverProfileDetails)
			}
		}
		return serverProfileDetailsList, nil
	}
}

func (h *OV) GetServerProfilesTemplatesFromURIs(
	ctx context.Context,
	uris []string,
) ([]HPEServerProfileTemplate, error) {
	url := h.Client.Config.URL.JoinPath("rest", "server-profile-templates")
	if len(uris) == 0 {
		return []HPEServerProfileTemplate{}, nil
	}
	serverProfileTemplateList, err := h.GetAllData(ctx, url, HPEServerProfileTemplate{}, []int{http.StatusOK})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Device details from url: %v,\n with error %w", url, err)
	}

	uriMap := make(map[string]struct{})
	for _, uri := range uris {
		uriMap[uri] = struct{}{}
	}

	serverProfileTemplates := make([]HPEServerProfileTemplate, 0, len(serverProfileTemplateList))
	for _, v := range serverProfileTemplateList {
		if item, ok := v.(HPEServerProfileTemplate); ok {
			if _, ok := uriMap[item.URI]; ok {
				serverProfileTemplates = append(serverProfileTemplates, item)
			}
		} else {
			return nil, fmt.Errorf("cannot convert type %T to HPEServerProfileTemplate, data: %v", v, serverProfileTemplateList)
		}
	}
	return serverProfileTemplates, nil
}

func (h *OV) RefreshServerProfile(ctx context.Context, serverProfileUUID string) (string, error) {
	url := h.Client.Config.URL.JoinPath("rest", "server-profiles", serverProfileUUID)
	refreshBody := []map[string]string{
		{
			"op":    "replace",
			"path":  "/refreshState",
			"value": "RefreshPending",
		},
	}
	return patchCall(ctx, h, url, refreshBody)
}

func (h *OV) ServerProfileTemplateUpgradeFirmware(ctx context.Context, serverProfileUUID string) (string, error) {
	url := h.Client.Config.URL.JoinPath("rest", "server-profiles", serverProfileUUID)
	templateUpdateBody := []map[string]string{
		{
			"op":    "replace",
			"path":  "/templateCompliance",
			"value": "PendingCompliance",
		},
	}
	return patchCall(ctx, h, url, templateUpdateBody)
}

func (h *OV) ServerProfileUpgradeFirmware(ctx context.Context, serverProfileUUID string) (string, error) {
	url := h.Client.Config.URL.JoinPath("rest", "server-profiles", serverProfileUUID)
	firmwareUpdateBody := []map[string]string{
		{
			"op":    "replace",
			"path":  "/firmware/reapplyState",
			"value": "ApplyPending",
		},
	}
	return patchCall(ctx, h, url, firmwareUpdateBody)
}

func (h *OV) GetFirmwareComplianceReport(
	ctx context.Context,
	serverHardwareUUID,
	firmwareBaselineId string,
) (*HPEFirmwareComplianceReport, error) {
	url := h.Client.Config.URL.JoinPath("rest", "server-hardware", "firmware-compliance")
	firmwareComplainceBody := map[string]string{
		"firmwareBaselineId": firmwareBaselineId,
		"serverUUID":         serverHardwareUUID,
	}
	payloadBody, err := json.Marshal(firmwareComplainceBody)
	if err != nil {
		err = fmt.Errorf("failed stringify firmwareComplainceBody for server-hardware with error: %v", err)
		return nil, err
	}

	reportData := &HPEFirmwareComplianceReport{}
	err = h.Client.Post(ctx, url, strings.NewReader(string(payloadBody)), reportData, []int{http.StatusOK})
	if err != nil {
		return nil, err
	}
	return reportData, nil
}

func (h *OV) GetFirmwareBundleDetails(
	ctx context.Context,
	name, version, firmwareBaselineId string,
) (*FirmwareBundleDetails, error) {
	url := h.Client.Config.URL.JoinPath("rest", "firmware-drivers")

	if firmwareBundleDetails, err := h.GetAllData(ctx, url, FirmwareBundleDetails{}, []int{http.StatusOK}); err != nil {
		return nil, fmt.Errorf("failed to fetch FirmwareBundle details from url: %v,\n with error %w", url, err)
	} else {
		for _, v := range firmwareBundleDetails {
			if item, ok := v.(FirmwareBundleDetails); ok {
				if firmwareBaselineId != "" && item.UUID == firmwareBaselineId {
					return &item, nil
				} else if name != "" && version != "" && item.Name == name && item.Version == version {
					return &item, nil
				}
			} else {
				return nil, fmt.Errorf("cannot convert type %T to FirmwareBundleDetails, data: %v", v, firmwareBundleDetails)
			}
		}
		return nil, fmt.Errorf("no firmware bundle found matching the given criteria %v", firmwareBundleDetails)
	}
}

func (h *OV) UpdateServerProfileFirmware(
	ctx context.Context,
	serverProfileUUID string,
	firmware FirmwareProfile,
) (string, error) {
	url := h.Client.Config.URL.JoinPath("rest", "server-profiles", serverProfileUUID)
	firmwarePatchBody := map[string]map[string]any{
		"firmware": {
			"manageFirmware":         firmware.ManageFirmware,
			"firmwareBaselineUri":    firmware.FirmwareBaselineURI,
			"forceInstallFirmware":   firmware.ForceInstallFirmware,
			"firmwareInstallType":    firmware.FirmwareInstallType,
			"firmwareInstallAction":  FirmwareInstallActionUpdate,
			"firmwareActivationType": FirmwareActivationeImmediate,
		},
	}
	payloadBody, err := json.Marshal(firmwarePatchBody)
	if err != nil {
		err = fmt.Errorf("failed stringify firmwarePatchBody for server-profiles with error: %v", err)
		return "", err
	}

	req, err := h.Client.CreateRequestWithAuth(
		url,
		http.MethodPut,
		strings.NewReader(string(payloadBody)),
		nil,
	)
	if err != nil {
		return "", fmt.Errorf("failed to create request %v \twith error: %v", url.String(), err)
	}
	resp, err := h.Client.DoRequest(ctx, req, []int{http.StatusOK, http.StatusAccepted})
	if err != nil {
		return "", err
	}
	return resp.Header.Get("Location"), nil
}

func (h *OV) UpdateServerProfileTemplateFirmware(
	ctx context.Context,
	serverProfileTemplateUUID string,
	firmware FirmwareTemplate,
) (string, error) {
	url := h.Client.Config.URL.JoinPath("rest", "server-profile-templates", serverProfileTemplateUUID)
	firmwarePatchBody := map[string]map[string]any{
		"firmware": {
			"manageFirmware":         firmware.ManageFirmware,
			"complianceControl":      ComplianceControlChecked,
			"firmwareBaselineUri":    firmware.FirmwareBaselineURI,
			"firmwareInstallType":    FirmwareInstallActionUpdate,
			"forceInstallFirmware":   firmware.ForceInstallFirmware,
			"firmwareActivationType": FirmwareActivationeImmediate,
		},
	}
	payloadBody, err := json.Marshal(firmwarePatchBody)
	if err != nil {
		err = fmt.Errorf("failed stringify firmwarePatchBody for server-profile-templates with error: %v", err)
		return "", err
	}

	req, err := h.Client.CreateRequestWithAuth(
		url,
		http.MethodPut,
		strings.NewReader(string(payloadBody)),
		nil,
	)
	if err != nil {
		return "", fmt.Errorf("failed to create request %v \twith error: %v", url.String(), err)
	}
	resp, err := h.Client.DoRequest(ctx, req, []int{http.StatusOK, http.StatusAccepted})
	if err != nil {
		return "", err
	}
	return resp.Header.Get("Location"), nil
}

func (h *OV) GetTask(ctx context.Context, taskURI string) (*HPETaskDetails, error) {
	url := h.Client.Config.URL.JoinPath(taskURI)
	taskDetails := &HPETaskDetails{}
	err := h.Client.Get(ctx, url, taskDetails, []int{http.StatusOK})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch Task details from url: %v,\n with error %w", url, err)
	}
	return taskDetails, nil
}

func (h *OV) ServerPowerOff(
	ctx context.Context,
	serverHardwareUUID string,
	controlType ServerPowerControl,
) (string, error) {
	url := h.Client.Config.URL.JoinPath("rest", "server-hardware", serverHardwareUUID, "powerState")

	serverPowerBody := map[string]string{
		"powerState":   string(ServerPowerStateOff),
		"powerControl": string(controlType),
	}
	payloadBody, err := json.Marshal(serverPowerBody)
	if err != nil {
		err = fmt.Errorf("failed stringify Server-Hardware powerState body with error: %v", err)
		return "", err
	}

	req, err := h.Client.CreateRequestWithAuth(
		url,
		http.MethodPut,
		strings.NewReader(string(payloadBody)),
		nil,
	)
	if err != nil {
		return "", fmt.Errorf("failed to create request %v \twith error: %v", url.String(), err)
	}
	resp, err := h.Client.DoRequest(ctx, req, []int{http.StatusOK, http.StatusAccepted})
	if err != nil {
		return "", err
	}
	return resp.Header.Get("Location"), nil

}

func patchCall(ctx context.Context, h *OV, url *neturl.URL, firmwareUpdateBody []map[string]string) (string, error) {
	payloadBody, err := json.Marshal(firmwareUpdateBody)
	if err != nil {
		err = fmt.Errorf("failed stringify body %v for server-profiles with error: %v", firmwareUpdateBody, err)
		return "", err
	}

	req, err := h.Client.CreateRequestWithAuth(
		url,
		http.MethodPatch,
		strings.NewReader(string(payloadBody)),
		nil,
	)
	if err != nil {
		return "", fmt.Errorf("failed to create request %v \twith error: %v", url.String(), err)
	}
	resp, err := h.Client.DoRequest(ctx, req, []int{http.StatusOK, http.StatusAccepted})
	if err != nil {
		return "", err
	}
	return resp.Header.Get("Location"), nil
}
