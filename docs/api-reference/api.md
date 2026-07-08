# API Reference

## Packages
- [readiness.metal.ironcore.dev/v1alpha1](#readinessmetalironcoredevv1alpha1)
- [vendorconsole.metal.ironcore.dev/v1alpha1](#vendorconsolemetalironcoredevv1alpha1)


## readiness.metal.ironcore.dev/v1alpha1

Package v1alpha1 contains API Schema definitions for the readiness.metal.ironcore.dev v1alpha1 API group.

### Resource Types
- [ServerWiring](#serverwiring)



#### ExpectedInterface



ExpectedInterface defines the expected state of a server network interface.



_Appears in:_
- [ExpectedNetworkSpec](#expectednetworkspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `macAddress` _string_ | MACAddress is the MAC address of the interface and acts as the primary key. |  |  |
| `carrierStatus` _string_ | CarrierStatus is the expected operational carrier status (e.g. "up").<br />If omitted, carrier status is not checked. |  |  |
| `neighbors` _[ExpectedNeighbor](#expectedneighbor) array_ | Neighbors lists the LLDP neighbors that must all be present on this interface.<br />If omitted or empty, neighbor presence is not checked. |  |  |


#### ExpectedNeighbor



ExpectedNeighbor defines an expected LLDP neighbor on a network interface.



_Appears in:_
- [ExpectedInterface](#expectedinterface)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `systemName` _string_ | SystemName is the LLDP system name of the expected neighbor (e.g. switch hostname). |  |  |
| `portID` _string_ | PortID is the LLDP port identifier of the expected neighbor. |  |  |


#### ExpectedNetworkSpec



ExpectedNetworkSpec defines the expected network wiring for a server.



_Appears in:_
- [ServerWiringSpec](#serverwiringspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `interfaces` _[ExpectedInterface](#expectedinterface) array_ | Interfaces is the list of expected network interfaces, keyed by MAC address. |  |  |


#### InterfaceMismatch



InterfaceMismatch describes a single wiring validation failure on a network interface.



_Appears in:_
- [ServerWiringStatus](#serverwiringstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `macAddress` _string_ | MACAddress is the MAC address of the interface that failed validation. |  |  |
| `reason` _string_ | Reason is a machine-readable token identifying the failure type. |  |  |
| `message` _string_ | Message describes the mismatch. |  |  |


#### ServerWiring



ServerWiring is the Schema for the serverwirings API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `readiness.metal.ironcore.dev/v1alpha1` | | |
| `kind` _string_ | `ServerWiring` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[ServerWiringSpec](#serverwiringspec)_ |  |  |  |
| `status` _[ServerWiringStatus](#serverwiringstatus)_ |  |  |  |


#### ServerWiringSpec



ServerWiringSpec defines the desired state of ServerWiring.



_Appears in:_
- [ServerWiring](#serverwiring)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `serverRef` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core)_ | ServerRef references the cluster-scoped Server to validate. |  |  |
| `network` _[ExpectedNetworkSpec](#expectednetworkspec)_ | Network defines the expected network wiring for the server. |  |  |


#### ServerWiringStatus



ServerWiringStatus defines the observed state of ServerWiring.



_Appears in:_
- [ServerWiring](#serverwiring)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `ready` _boolean_ | Ready is true when all expected interfaces and neighbors were found. | false |  |
| `mismatches` _[InterfaceMismatch](#interfacemismatch) array_ | Mismatches lists validation failures for the server. |  |  |



## vendorconsole.metal.ironcore.dev/v1alpha1

Package v1alpha1 contains API Schema definitions for the vendorconsole.metal.ironcore.dev v1alpha1 API group.

### Resource Types
- [Console](#console)
- [FirmwareUpdateDELL](#firmwareupdatedell)



#### BaselineBitType

_Underlying type:_ _string_





_Appears in:_
- [BaselinesConfig](#baselinesconfig)

| Field | Description |
| --- | --- |
| `64BitType` | BitType64 specifies baseline type is 64Bit<br /> |


#### BaselineDowngradeType

_Underlying type:_ _string_





_Appears in:_
- [BaselinesConfig](#baselinesconfig)

| Field | Description |
| --- | --- |
| `DowngradableUpdate` | DowngradableUpdate specifies that downgrade is allowed for baseline update<br /> |
| `NotDowngradableUpdate` | NotDowngradableUpdate specifies that downgrade is not allowed for baseline update<br /> |


#### BaselinesConfig







_Appears in:_
- [FirmwareUpdateDELLSpec](#firmwareupdatedellspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the baseline to be used for the firmware update. |  | Required: \{\} <br /> |
| `description` _string_ | Description is a brief description of the baseline. |  |  |
| `downgradeEnabled` _[BaselineDowngradeType](#baselinedowngradetype)_ | DowngradeEnabled specifies whether downgrade is enabled for the baseline update. | DowngradableUpdate |  |
| `bitType` _[BaselineBitType](#baselinebittype)_ | Is64Bit specifies whether the baseline update is for 64-bit systems. | BitType64 |  |


#### CheckCertificateCatalog

_Underlying type:_ _string_





_Appears in:_
- [Repository](#repository)

| Field | Description |
| --- | --- |
| `CheckCertificateHTTPS` | CheckCertificate specifies that certificate check must be done for HTTPS<br /> |
| `NoCheckCertificateHTTPS` | NoCheckCertificate specifies that certificate check must not be done for HTTPS<br /> |


#### ComplianceFirmwareUpdate

_Underlying type:_ _string_





_Appears in:_
- [FirmwareUpgradeConfig](#firmwareupgradeconfig)

| Field | Description |
| --- | --- |
| `ComplianceUpdate` | ComplianceUpdate specifies that firmware update needs to match compliance<br /> |
| `NoComplianceUpdate` | NoComplianceUpdate specifies that firmware update needs not match compliance<br /> |


#### Console



Console is the Schema for the consoles API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `vendorconsole.metal.ironcore.dev/v1alpha1` | | |
| `kind` _string_ | `Console` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[ConsoleSpec](#consolespec)_ |  |  |  |
| `status` _[ConsoleStatus](#consolestatus)_ |  |  |  |


#### ConsoleSpec



ConsoleSpec defines the desired state of Console.



_Appears in:_
- [Console](#console)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `serverSelector` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#labelselector-v1-meta)_ | ServerSelector specifies a label selector to identify the servers that are to be selected. |  |  |
| `consoleURL` _string_ | ConsoleURL is the URL of the server management console. |  |  |
| `manufacturer` _[Manufacturer](#manufacturer)_ | Manufacturer is the manufacturer of the server management console (e.g., "Dell", "HPE", "Lenovo"). |  |  |
| `bmcCredentialSecretRef` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core)_ | BMCCredentialSecretRef references the secret containing BMC credentials. |  |  |


#### ConsoleStatus



ConsoleStatus defines the observed state of Console.



_Appears in:_
- [Console](#console)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `managedServers` _integer_ | ManagedServers number of managed servers. |  |  |
| `unmanagedServers` _integer_ | UnmanagedServers number of unmanaged servers. |  |  |
| `totalServers` _integer_ | TotalServers total number of servers. |  |  |
| `pendingOperations` _[PendingOperation](#pendingoperation) array_ | PendingOperations tracks in-flight vendor operations. |  |  |


#### CreateCatalog



note: Uniqueness constraints:
CreateCatalog.FileName
CreateCatalog.CatalogPath
CreateCatalog.Repository.Name
If all these are same as an existing catalog,
then it is considered duplicate and will not be created again.



_Appears in:_
- [FirmwareUpdateDELLSpec](#firmwareupdatedellspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `fileName` _string_ | FileName is the name of the catalog file to be created. |  | MaxLength: 253 <br />MinLength: 1 <br />Required: \{\} <br /> |
| `sourcePath` _string_ | SourcePath is the path to the catalog file on the OME server.<br />This is the path where the catalog will be created. with IP or FQDN of the repo server. |  | MaxLength: 1024 <br />MinLength: 1 <br />Type: string <br /> |
| `repository` _[Repository](#repository)_ | Repository contains the details of the repository from which the catalog will be created. |  | Required: \{\} <br /> |


#### DellBaseline







_Appears in:_
- [FirmwareUpdateDELLStatus](#firmwareupdatedellstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `id` _integer_ | Id is the unique identifier for the baseline created in OME. |  |  |


#### DellCatalog







_Appears in:_
- [FirmwareUpdateDELLStatus](#firmwareupdatedellstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `id` _integer_ | Id is the unique identifier for the catalog created in OME. |  |  |


#### DellJob







_Appears in:_
- [FirmwareUpdateDELLStatus](#firmwareupdatedellstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `jobId` _integer_ | Id is the unique identifier for the job created in OME. |  |  |
| `name` _string_ | Name is the name of the job. |  |  |


#### FirmwareUpdateDELL



FirmwareUpdateDELL is the Schema for the FirmwareUpdateDELLs API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `vendorconsole.metal.ironcore.dev/v1alpha1` | | |
| `kind` _string_ | `FirmwareUpdateDELL` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[FirmwareUpdateDELLSpec](#firmwareupdatedellspec)_ |  |  |  |
| `status` _[FirmwareUpdateDELLStatus](#firmwareupdatedellstatus)_ |  |  |  |


#### FirmwareUpdateDELLSpec



FirmwareUpdateDELLSpec defines the desired state of FirmwareUpdateDELL.



_Appears in:_
- [FirmwareUpdateDELL](#firmwareupdatedell)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `omeURL` _string_ | OMEURL is the URL of the Dell OpenManage Enterprise (OME) instance. |  | Pattern: `^https?://[a-zA-Z0-9.-]+(:[0-9]+)?(/.*)?$` <br /> |
| `secretRef` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core)_ | secretRef is a reference to the Kubernetes Secret (of type SecretTypeBasicAuth) object that contains the credentials<br />to access the Dell OpenManage Enterprise (OME). This secret includes sensitive information such as usernames and passwords. |  |  |
| `createCatalog` _[CreateCatalog](#createcatalog)_ | CreateCatalog is the fields required to create catalog through the Dell OpenManage Enterprise (OME). |  |  |
| `catalogName` _string_ | CatalogRepositoryName is the name of the catalog to be used for the firmware update.<br />The operator will use the catalog name and ignore CreateCatalog field. |  |  |
| `firmwareUpgradeConfig` _[FirmwareUpgradeConfig](#firmwareupgradeconfig)_ | FirmwareUpgradeConfig contains configuration options for the firmware upgrade process. |  |  |
| `baselineConfig` _[BaselinesConfig](#baselinesconfig)_ | BaselineConfig contains configuration options for the baseline to be used for the firmware update. |  |  |
| `serverSelector` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#labelselector-v1-meta)_ | ServerSelector specifies a label selector to identify the servers that are to be selected. |  |  |
| `serverMaintenancePolicy` _[ServerMaintenancePolicy](#servermaintenancepolicy)_ | ServerMaintenancePolicy is a maintenance policy to be enforced on the server managed by referred BMC. |  |  |
| `serverMaintenanceRefs` _ServerMaintenanceRefItem array_ | ServerMaintenanceRefs are references to a ServerMaintenance objects that Controller has requested for the each of the related server. |  |  |


#### FirmwareUpdateDELLStatus



FirmwareUpdateDELLStatus defines the observed state of FirmwareUpdateDELL.



_Appears in:_
- [FirmwareUpdateDELL](#firmwareupdatedell)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `state` _[FirmwareUpdateState](#firmwareupdatestate)_ | State represents the current state of the bios configuration task. |  |  |
| `updateTask` _[DellJob](#delljob)_ | UpdateTask contains the state of the Update Task created by the OME for the firmware upgrade. |  |  |
| `catalog` _[DellCatalog](#dellcatalog)_ | Catalog contains the details of the Catalog created by the OME for the firmware upgrade. |  |  |
| `baseline` _[DellBaseline](#dellbaseline)_ | Baseline contains the details of the Baseline created by the OME for the firmware upgrade. |  |  |
| `serverCount` _integer_ | ServerCount is the total number of servers selected by the ServerSelector. |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#condition-v1-meta) array_ | Conditions represents the latest available observations of the Bios version upgrade state. |  |  |


#### FirmwareUpdateState

_Underlying type:_ _string_





_Appears in:_
- [FirmwareUpdateDELLStatus](#firmwareupdatedellstatus)

| Field | Description |
| --- | --- |
| `Pending` | FirmwareUpdateStatePending specifies that the BMC upgrade maintenance is waiting<br /> |
| `InProgress` | FirmwareUpdateStateInProgress specifies that upgrading BMC is in progress.<br /> |
| `Completed` | FirmwareUpdateStateCompleted specifies that the BMC upgrade maintenance has been completed.<br /> |
| `Failed` | FirmwareUpdateStateFailed specifies that the BMC upgrade maintenance has failed.<br /> |


#### FirmwareUpgradeConfig



FirmwareUpgradeConfig contains configuration options for the firmware upgrade process.



_Appears in:_
- [FirmwareUpdateDELLSpec](#firmwareupdatedellspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `signVerify` _[SignVerifyFirmwareUpdate](#signverifyfirmwareupdate)_ | SignVerify specifies whether to verify the signature of the firmware before upgrade. | SignVerify |  |
| `stagingValue` _[StageFirmwareUpdate](#stagefirmwareupdate)_ | StagingValue is the value used for staging the firmware before upgrade. | NoStagingFirmwareUpdate |  |
| `complianceUpdate` _[ComplianceFirmwareUpdate](#compliancefirmwareupdate)_ | ComplianceUpdate specifies whether to perform a compliance update during the firmware upgrade. | ComplianceUpdate |  |
| `operationName` _string_ | OperationName specifies the name of the operation to be performed for the firmware update.<br />refer to Dell OME API documentation for possible values. | INSTALL_FIRMWARE | MinLength: 1 <br /> |
| `jobTypeName` _string_ | JobTypeName specifies the type of job to be created for the firmware update.<br />refer to Dell OME API documentation for possible values. | Update_Task |  |


#### JobStatus

_Underlying type:_ _string_

JobStatus defines the status of a vendor operation.



_Appears in:_
- [PendingOperation](#pendingoperation)

| Field | Description |
| --- | --- |
| `Pending` | JobStatusPending indicates the operation has been queued but not started.<br /> |
| `Running` | JobStatusRunning indicates the operation is in progress.<br /> |
| `Completed` | JobStatusCompleted indicates the operation completed successfully.<br /> |
| `Failed` | JobStatusFailed indicates the operation failed.<br /> |
| `TimedOut` | JobStatusTimedOut indicates the operation exceeded the timeout period.<br /> |


#### OperationType

_Underlying type:_ _string_

OperationType defines the type of vendor operation.



_Appears in:_
- [PendingOperation](#pendingoperation)

| Field | Description |
| --- | --- |
| `Import` | OperationTypeImport represents importing a server into the console.<br /> |
| `Remove` | OperationTypeRemove represents removing a server from the console.<br /> |


#### PendingOperation



PendingOperation tracks an in-flight vendor operation.



_Appears in:_
- [ConsoleStatus](#consolestatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `serverName` _string_ | ServerName is the name of the Server resource. |  |  |
| `hostname` _string_ | Hostname is the DNS name used for the server in the vendor console. |  |  |
| `ip` _string_ | IP is the BMC IP address of the server. |  |  |
| `operationType` _[OperationType](#operationtype)_ | OperationType is the type of operation (Import or Remove). |  |  |
| `jobId` _string_ | JobID is the vendor-specific job identifier for tracking. |  |  |
| `status` _[JobStatus](#jobstatus)_ | Status is the current status of the operation. |  |  |
| `startTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | StartTime is when the operation was initiated. |  |  |
| `lastChecked` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | LastChecked is when the job status was last polled. |  |  |
| `retryCount` _integer_ | RetryCount tracks how many times the operation has been retried. |  |  |
| `message` _string_ | Message provides human-readable status information. |  |  |


#### Repository







_Appears in:_
- [CreateCatalog](#createcatalog)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the name of the repository. |  | MaxLength: 253 <br />MinLength: 1 <br />Type: string <br /> |
| `description` _string_ | Description is a brief description of the repository. |  | MaxLength: 1024 <br /> |
| `repositoryType` _string_ | RepositoryType is the type of the repository (e.g., "CIFS", "NFS", "HTTPS", "DELL_ONLINE"). |  | Enum: [CIFS NFS HTTPS HTTP DELL_ONLINE] <br />MinLength: 1 <br />Required: \{\} <br /> |
| `source` _string_ | Source is the source URL/IP of the repository. |  | MaxLength: 253 <br />MinLength: 1 <br />Required: \{\} <br />Type: string <br /> |
| `domainName` _string_ | DomainName is the domain name for authentication, if required. |  |  |
| `username` _string_ | Username is the username for authentication to the repository. |  |  |
| `password` _string_ | Password is the password for authentication to the repository. |  |  |
| `checkCertificate` _[CheckCertificateCatalog](#checkcertificatecatalog)_ | CheckCertificate indicates whether to check the SSL certificate of the repository. | NoCheckCertificateHTTPS |  |


#### SignVerifyFirmwareUpdate

_Underlying type:_ _string_





_Appears in:_
- [FirmwareUpgradeConfig](#firmwareupgradeconfig)

| Field | Description |
| --- | --- |
| `SignVerify` | SignVerify specifies that no staging will be performed.<br />DUP signature will be verified<br /> |
| `SkipSignVerify` | SkipSignVerify specifies that the Firmware will be staged.<br />DUP signature will be verified skipped.<br /> |


#### StageFirmwareUpdate

_Underlying type:_ _string_





_Appears in:_
- [FirmwareUpgradeConfig](#firmwareupgradeconfig)

| Field | Description |
| --- | --- |
| `StagedFirmwareUpdate` | StagingFirmwareStaged specifies that no staging will be performed.<br />Starts the Firmware update on Reboot.<br /> |
| `NoStagingFirmwareUpdate` | NoStagingFirmwareUpdate specifies that the Firmware will be staged.<br />Starts the Firmware update immediately.<br /> |


