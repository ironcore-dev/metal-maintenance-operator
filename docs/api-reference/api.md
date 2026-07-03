# API Reference

## Packages
- [maintenance.metal.ironcore.dev/v1alpha1](#maintenancemetalironcoredevv1alpha1)
- [readiness.metal.ironcore.dev/v1alpha1](#readinessmetalironcoredevv1alpha1)


## maintenance.metal.ironcore.dev/v1alpha1

Package v1alpha1 contains API Schema definitions for the maintenance.metal.ironcore.dev v1alpha1 API group.

### Resource Types
- [Console](#console)



#### Console



Console is the Schema for the consoles API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `maintenance.metal.ironcore.dev/v1alpha1` | | |
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


