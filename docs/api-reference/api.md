# API Reference

## Packages
- [maintenance.metal.ironcore.dev/v1alpha1](#maintenancemetalironcoredevv1alpha1)
- [readiness.metal.ironcore.dev/v1alpha1](#readinessmetalironcoredevv1alpha1)
- [vendorconsole.metal.ironcore.dev/v1alpha1](#vendorconsolemetalironcoredevv1alpha1)


## maintenance.metal.ironcore.dev/v1alpha1

Package v1alpha1 contains API Schema definitions for the maintenance.metal.ironcore.dev v1alpha1 API group.

### Resource Types
- [MaintenancePlan](#maintenanceplan)
- [MaintenancePlanRun](#maintenanceplanrun)



#### MaintenancePlan



MaintenancePlan is the Schema for the maintenanceplans API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `maintenance.metal.ironcore.dev/v1alpha1` | | |
| `kind` _string_ | `MaintenancePlan` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[MaintenancePlanSpec](#maintenanceplanspec)_ |  |  |  |
| `status` _[MaintenancePlanStatus](#maintenanceplanstatus)_ |  |  |  |


#### MaintenancePlanPhase

_Underlying type:_ _string_

MaintenancePlanPhase represents the overall lifecycle state of a MaintenancePlan.

_Validation:_
- Enum: [Pending Active Completed Failed]

_Appears in:_
- [MaintenancePlanStatus](#maintenanceplanstatus)

| Field | Description |
| --- | --- |
| `Pending` | MaintenancePlanPhasePending means the plan has been created but no runs have started.<br /> |
| `Active` | MaintenancePlanPhaseActive means at least one run is in progress.<br /> |
| `Completed` | MaintenancePlanPhaseCompleted means all servers have been processed successfully.<br /> |
| `Failed` | MaintenancePlanPhaseFailed means one or more runs failed.<br /> |


#### MaintenancePlanRun



MaintenancePlanRun is the Schema for the maintenanceplanruns API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `maintenance.metal.ironcore.dev/v1alpha1` | | |
| `kind` _string_ | `MaintenancePlanRun` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[MaintenancePlanRunSpec](#maintenanceplanrunspec)_ |  |  |  |
| `status` _[MaintenancePlanRunStatus](#maintenanceplanrunstatus)_ |  |  |  |


#### MaintenancePlanRunPhase

_Underlying type:_ _string_

MaintenancePlanRunPhase represents the lifecycle state of a single MaintenancePlanRun.

_Validation:_
- Enum: [Pending Running Succeeded Failed]

_Appears in:_
- [MaintenancePlanRunStatus](#maintenanceplanrunstatus)

| Field | Description |
| --- | --- |
| `Pending` | MaintenancePlanRunPhasePending means the run has been created and is waiting to start.<br /> |
| `Running` | MaintenancePlanRunPhaseRunning means the run is actively executing stages.<br /> |
| `Succeeded` | MaintenancePlanRunPhaseSucceeded means all stages completed (or were skipped).<br /> |
| `Failed` | MaintenancePlanRunPhaseFailed means a stage failed and the run has halted.<br /> |


#### MaintenancePlanRunSpec



MaintenancePlanRunSpec defines the input for a single BMC's maintenance run.



_Appears in:_
- [MaintenancePlanRun](#maintenanceplanrun)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `planRef` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core)_ | PlanRef is the MaintenancePlan that generated this run. |  |  |
| `bmcRef` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core)_ | BMCRef is the BMC object that this run targets.<br />One run is created per unique BMC matched by the plan's serverSelector. |  |  |
| `serverRefs` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core) array_ | ServerRefs are all Server objects that share this BMC.<br />BMC-scoped stages (BMCSettings/BMCVersion) execute once for the BMC.<br />Server-scoped stages (BIOSSettings/BIOSVersion) fan out one child CR per server. |  | MinItems: 1 <br /> |
| `baselineBMCVersion` _string_ | BaselineBMCVersion is the BMC firmware version observed at run creation time.<br />Used for per-stage version-aware skip evaluation for BMC-scoped stages. |  |  |
| `baselineBIOSVersions` _object (keys:string, values:string)_ | BaselineBIOSVersions maps each server name to its BIOS firmware version<br />observed at run creation time. Used for per-server, per-stage version-aware<br />skip evaluation for Server-scoped stages. |  |  |
| `trigger` _[RunTrigger](#runtrigger)_ | Trigger records why this run was created. | Initial | Enum: [Initial] <br /> |
| `stages` _[PlanStage](#planstage) array_ | Stages is a snapshot of the plan's stage list at run creation time.<br />Immutable after creation. |  | MinItems: 1 <br /> |


#### MaintenancePlanRunStatus



MaintenancePlanRunStatus defines the observed state of MaintenancePlanRun.



_Appears in:_
- [MaintenancePlanRun](#maintenanceplanrun)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `phase` _[MaintenancePlanRunPhase](#maintenanceplanrunphase)_ | Phase is the high-level state of this run. |  | Enum: [Pending Running Succeeded Failed] <br /> |
| `currentStageIndex` _integer_ | CurrentStageIndex is the zero-based index of the stage currently being executed. |  |  |
| `stageStatuses` _[StageStatus](#stagestatus) array_ | StageStatuses holds per-stage execution state, indexed in the same order as Spec.Stages. |  |  |
| `startTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | StartTime is when the run transitioned from Pending to Running. |  |  |
| `completionTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | CompletionTime is when the run reached a terminal phase. |  |  |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed by the controller. |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#condition-v1-meta) array_ | Conditions represent the latest available observations of the run's state. |  |  |


#### MaintenancePlanSpec



MaintenancePlanSpec defines the desired maintenance pipeline for a fleet of servers.



_Appears in:_
- [MaintenancePlan](#maintenanceplan)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `serverSelector` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#labelselector-v1-meta)_ | ServerSelector selects the Server objects this plan applies to. |  |  |
| `maxConcurrent` _integer_ | MaxConcurrent is the maximum number of MaintenancePlanRuns that may be<br />in a non-terminal phase at the same time. | 1 | Minimum: 1 <br /> |
| `stages` _[PlanStage](#planstage) array_ | Stages is the ordered list of maintenance steps to execute. |  | MinItems: 1 <br /> |


#### MaintenancePlanStatus



MaintenancePlanStatus defines the observed state of MaintenancePlan.



_Appears in:_
- [MaintenancePlan](#maintenanceplan)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `phase` _[MaintenancePlanPhase](#maintenanceplanphase)_ | Phase is the high-level state of this plan. |  | Enum: [Pending Active Completed Failed] <br /> |
| `totalRuns` _integer_ | TotalRuns is the total number of MaintenancePlanRuns created for this plan. |  |  |
| `activeRuns` _integer_ | ActiveRuns is the number of runs currently in a non-terminal phase. |  |  |
| `succeededRuns` _integer_ | SucceededRuns is the number of runs that completed successfully. |  |  |
| `failedRuns` _integer_ | FailedRuns is the number of runs that failed. |  |  |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed by the controller. |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#condition-v1-meta) array_ | Conditions represent the latest available observations of the plan's state. |  |  |


#### PlanBMCSettingsTemplate



PlanBMCSettingsTemplate is a plan-level copy of BMCSettingsTemplate that omits
the Variables field. Variables contain an O(n²) CEL uniqueness rule that exceeds
Kubernetes' CRD validation cost budget when nested inside MaintenancePlan.
Variables can be added directly to the BMCSettings child CR if needed.



_Appears in:_
- [StageTemplate](#stagetemplate)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `version` _string_ | Version specifies the BMC firmware version for which the settings apply. |  |  |
| `settings` _object (keys:string, values:string)_ | Settings contains BMC settings as a key/value map. |  |  |
| `retryPolicy` _[RetryPolicy](#retrypolicy)_ | RetryPolicy defines automatic retry behaviour on transient failures. |  |  |
| `serverMaintenancePolicy` _[ServerMaintenancePolicy](#servermaintenancepolicy)_ | ServerMaintenancePolicy is the maintenance policy to apply on affected servers. |  |  |


#### PlanStage



PlanStage defines a single ordered step in the maintenance pipeline.



_Appears in:_
- [MaintenancePlanRunSpec](#maintenanceplanrunspec)
- [MaintenancePlanSpec](#maintenanceplanspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name is the unique identifier for this stage within the plan. |  | MaxLength: 253 <br />MinLength: 1 <br /> |
| `kind` _[StageKind](#stagekind)_ | Kind is the type of metal-operator child CRD to create for this stage. |  | Enum: [BMCSettings BMCVersion BIOSSettings BIOSVersion] <br /> |
| `template` _[StageTemplate](#stagetemplate)_ | Template contains the spec payload for the child CR. |  |  |


#### RunTrigger

_Underlying type:_ _string_

RunTrigger indicates why a MaintenancePlanRun was created.

_Validation:_
- Enum: [Initial]

_Appears in:_
- [MaintenancePlanRunSpec](#maintenanceplanrunspec)

| Field | Description |
| --- | --- |
| `Initial` | RunTriggerInitial means this run was created as part of the first-time<br />maintenance pipeline for the BMC.<br /> |


#### ServerStageStatus



ServerStageStatus captures the execution state for one server within a Server-scoped stage.



_Appears in:_
- [StageStatus](#stagestatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `serverRef` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core)_ | ServerRef identifies the server this status entry belongs to. |  |  |
| `phase` _[StagePhase](#stagephase)_ | Phase is the current execution phase for this server's child CR. |  | Enum: [Pending Running Skipped Succeeded Failed] <br /> |
| `childRef` _[ObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#objectreference-v1-core)_ | ChildRef is the object reference to the child CR created for this server (if any). |  |  |
| `startTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | StartTime is when the child CR was created for this server. |  |  |
| `completionTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | CompletionTime is when this server's child CR reached a terminal phase. |  |  |
| `message` _string_ | Message is a human-readable description of the current state for this server. |  |  |


#### StageKind

_Underlying type:_ _string_

StageKind identifies which metal-operator child CRD a stage targets.

_Validation:_
- Enum: [BMCSettings BMCVersion BIOSSettings BIOSVersion]

_Appears in:_
- [PlanStage](#planstage)

| Field | Description |
| --- | --- |
| `BMCSettings` |  |
| `BMCVersion` |  |
| `BIOSSettings` |  |
| `BIOSVersion` |  |


#### StagePhase

_Underlying type:_ _string_

StagePhase is the execution state of a single stage within a run.

_Validation:_
- Enum: [Pending Running Skipped Succeeded Failed]

_Appears in:_
- [ServerStageStatus](#serverstagestatus)
- [StageStatus](#stagestatus)

| Field | Description |
| --- | --- |
| `Pending` | StagePhasePending means the stage has not started yet.<br /> |
| `Running` | StagePhaseRunning means the child CR has been created and is being watched.<br /> |
| `Skipped` | StagePhaseSkipped means the stage was skipped because the target version is already met.<br /> |
| `Succeeded` | StagePhaseSucceeded means the child CR reached a terminal success state.<br /> |
| `Failed` | StagePhaseFailed means the child CR reached a terminal failure state.<br /> |


#### StageStatus



StageStatus captures the observed state of one stage in a run.



_Appears in:_
- [MaintenancePlanRunStatus](#maintenanceplanrunstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `name` _string_ | Name matches the stage name from MaintenancePlanSpec.Stages. |  |  |
| `phase` _[StagePhase](#stagephase)_ | Phase is the current execution phase of this stage. |  | Enum: [Pending Running Skipped Succeeded Failed] <br /> |
| `childRef` _[ObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#objectreference-v1-core)_ | ChildRef is the object reference to the child CR for BMC-scoped stages (BMCSettings/BMCVersion).<br />Empty for Server-scoped stages; see ServerStatuses instead. |  |  |
| `serverStatuses` _[ServerStageStatus](#serverstagestatus) array_ | ServerStatuses holds per-server execution state for Server-scoped stages<br />(BIOSSettings/BIOSVersion). Empty for BMC-scoped stages. |  |  |
| `startTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | StartTime is when this stage began executing. |  |  |
| `completionTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | CompletionTime is when this stage reached a terminal phase. |  |  |
| `message` _string_ | Message is a human-readable description of the current stage state. |  |  |
| `appliedSpec` _[RawExtension](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#rawextension-runtime-pkg)_ | AppliedSpec is a snapshot of the child CR's spec at the time it completed.<br />Populated before intermediate-hop CRs are deleted so the run retains a full audit record. |  |  |


#### StageTemplate



StageTemplate holds the spec payload for a single stage. Exactly one field
must be set and it must match the stage's Kind.



_Appears in:_
- [PlanStage](#planstage)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `bmcSettings` _[PlanBMCSettingsTemplate](#planbmcsettingstemplate)_ | BMCSettings is the template used when Kind is BMCSettings. |  |  |
| `bmcVersion` _[BMCVersionTemplate](#bmcversiontemplate)_ | BMCVersion is the template used when Kind is BMCVersion. |  |  |
| `biosSettings` _[BIOSSettingsTemplate](#biossettingstemplate)_ | BIOSSettings is the template used when Kind is BIOSSettings. |  |  |
| `biosVersion` _[BIOSVersionTemplate](#biosversiontemplate)_ | BIOSVersion is the template used when Kind is BIOSVersion. |  |  |



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


