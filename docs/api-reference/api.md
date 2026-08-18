# API Reference

## Packages
- [baseboard.metal.ironcore.dev/v1alpha1](#baseboardmetalironcoredevv1alpha1)
- [maintenance.metal.ironcore.dev/v1alpha1](#maintenancemetalironcoredevv1alpha1)
- [readiness.metal.ironcore.dev/v1alpha1](#readinessmetalironcoredevv1alpha1)
- [system.metal.ironcore.dev/v1alpha1](#systemmetalironcoredevv1alpha1)
- [vendorconsole.metal.ironcore.dev/v1alpha1](#vendorconsolemetalironcoredevv1alpha1)


## baseboard.metal.ironcore.dev/v1alpha1

Package v1alpha1 contains API Schema definitions for the baseboard.metal.ironcore.dev v1alpha1 API group.

### Resource Types
- [BMCSettings](#bmcsettings)
- [BMCSettingsSet](#bmcsettingsset)
- [BMCUser](#bmcuser)
- [BMCVersion](#bmcversion)
- [BMCVersionSet](#bmcversionset)



#### BMCSettings



BMCSettings is the Schema for the BMCSettings API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `baseboard.metal.ironcore.dev/v1alpha1` | | |
| `kind` _string_ | `BMCSettings` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[BMCSettingsSpec](#bmcsettingsspec)_ |  |  |  |
| `status` _[BMCSettingsStatus](#bmcsettingsstatus)_ |  |  |  |


#### BMCSettingsSet



BMCSettingsSet is the Schema for the bmcsettingssets API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `baseboard.metal.ironcore.dev/v1alpha1` | | |
| `kind` _string_ | `BMCSettingsSet` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[BMCSettingsSetSpec](#bmcsettingssetspec)_ |  |  |  |
| `status` _[BMCSettingsSetStatus](#bmcsettingssetstatus)_ |  |  |  |


#### BMCSettingsSetSpec



BMCSettingsSetSpec defines the desired state of BMCSettingsSet.



_Appears in:_
- [BMCSettingsSet](#bmcsettingsset)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `bmcSettingsTemplate` _[BMCSettingsTemplate](#bmcsettingstemplate)_ | BMCSettingsTemplate defines the template for the BMCSettings resource to be applied to the BMCs. |  |  |
| `bmcSelector` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#labelselector-v1-meta)_ | BMCSelector specifies a label selector to identify the BMCs to be selected. |  |  |


#### BMCSettingsSetStatus



BMCSettingsSetStatus defines the observed state of BMCSettingsSet.



_Appears in:_
- [BMCSettingsSet](#bmcsettingsset)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `fullyLabeledBMCs` _integer_ | FullyLabeledBMCs is the number of BMCs in the set. |  |  |
| `availableBMCSettings` _integer_ | AvailableBMCSettings is the number of BMCSettings currently created by the set. |  |  |
| `pendingBMCSettings` _integer_ | PendingBMCSettings is the total number of pending BMCSettings in the set. |  |  |
| `inProgressBMCSettings` _integer_ | InProgressBMCSettings is the total number of BMCSettings in the set that are currently in progress. |  |  |
| `completedBMCSettings` _integer_ | CompletedBMCSettings is the total number of completed BMCSettings in the set. |  |  |
| `failedBMCSettings` _integer_ | FailedBMCSettings is the total number of failed BMCSettings in the set. |  |  |


#### BMCSettingsSpec



BMCSettingsSpec defines the desired state of BMCSettings.



_Appears in:_
- [BMCSettings](#bmcsettings)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `version` _string_ | Version specifies the software version (e.g. BIOS, BMC) these settings apply to. |  |  |
| `settingsFlow` _[SettingsFlowItem](#settingsflowitem) array_ | SettingsFlow contains the settings sequence to apply in the given order. |  |  |
| `retryPolicy` _[RetryPolicy](#retrypolicy)_ | RetryPolicy defines the retry behavior for automatic retries on transient failures. |  |  |
| `variables` _[Variable](#variable) array_ | Variables is a list of variables that can be used in the settings for templating. |  | MaxItems: 64 <br /> |
| `serverMaintenancePolicy` _[ServerMaintenancePolicy](#servermaintenancepolicy)_ | ServerMaintenancePolicy is a maintenance policy to be applied on the server. |  |  |
| `serverMaintenanceRefs` _ServerMaintenanceRefItem array_ | ServerMaintenanceRefs are references to ServerMaintenance objects which are created by the controller for each<br />server that needs to be updated with the BMC settings. |  |  |
| `bmcRef` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core)_ | BMCRef is a reference to a specific BMC to apply settings to. |  |  |


#### BMCSettingsState

_Underlying type:_ _string_

BMCSettingsState specifies the current state of the server maintenance.



_Appears in:_
- [BMCSettingsStatus](#bmcsettingsstatus)

| Field | Description |
| --- | --- |
| `Pending` | BMCSettingsStatePending specifies that the BMC settings update is waiting.<br /> |
| `InProgress` | BMCSettingsStateInProgress specifies that the BMC settings changes are in progress.<br /> |
| `Applied` | BMCSettingsStateApplied specifies that the BMC settings have been applied.<br /> |
| `Failed` | BMCSettingsStateFailed specifies that the BMC settings update has failed.<br /> |


#### BMCSettingsStatus



BMCSettingsStatus defines the observed state of BMCSettings.



_Appears in:_
- [BMCSettings](#bmcsettings)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `state` _[BMCSettingsState](#bmcsettingsstate)_ | State represents the current state of the BMC configuration task. |  |  |
| `failedAttempts` _integer_ | FailedAttempts is the number of automatic retry attempts made after failure. |  |  |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed by the controller. |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#condition-v1-meta) array_ | Conditions represents the latest available observations of the BMC Settings Resource state. |  |  |


#### BMCSettingsTemplate



BMCSettingsTemplate defines the template for BMC settings to be applied.



_Appears in:_
- [BMCSettingsSetSpec](#bmcsettingssetspec)
- [BMCSettingsSpec](#bmcsettingsspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `version` _string_ | Version specifies the software version (e.g. BIOS, BMC) these settings apply to. |  |  |
| `settingsFlow` _[SettingsFlowItem](#settingsflowitem) array_ | SettingsFlow contains the settings sequence to apply in the given order. |  |  |
| `retryPolicy` _[RetryPolicy](#retrypolicy)_ | RetryPolicy defines the retry behavior for automatic retries on transient failures. |  |  |
| `variables` _[Variable](#variable) array_ | Variables is a list of variables that can be used in the settings for templating. |  | MaxItems: 64 <br /> |
| `serverMaintenancePolicy` _[ServerMaintenancePolicy](#servermaintenancepolicy)_ | ServerMaintenancePolicy is a maintenance policy to be applied on the server. |  |  |


#### BMCUser



BMCUser is the Schema for the bmcusers API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `baseboard.metal.ironcore.dev/v1alpha1` | | |
| `kind` _string_ | `BMCUser` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[BMCUserSpec](#bmcuserspec)_ |  |  |  |
| `status` _[BMCUserStatus](#bmcuserstatus)_ |  |  |  |


#### BMCUserSpec



BMCUserSpec defines the desired state of BMCUser.



_Appears in:_
- [BMCUser](#bmcuser)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `userName` _string_ | UserName is the username of the BMC user. |  |  |
| `roleID` _string_ | RoleID is the ID of the role to assign to the user. |  |  |
| `description` _string_ | Description is a description for the BMC user. |  |  |
| `rotationPeriod` _[Duration](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#duration-v1-meta)_ | RotationPeriod defines how often the password should be rotated.<br />If not set, the password will not be rotated. |  |  |
| `bmcSecretRef` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core)_ | BMCSecretRef references the BMCSecret containing the credentials for this user.<br />If not set, the operator will generate a secure password based on BMC manufacturer requirements. |  |  |
| `bmcRef` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core)_ | BMCRef references the BMC this user should be created on. |  |  |


#### BMCUserStatus



BMCUserStatus defines the observed state of BMCUser.



_Appears in:_
- [BMCUser](#bmcuser)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `effectiveBMCSecretRef` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core)_ | EffectiveBMCSecretRef references the BMCSecret currently used for this user.<br />This may differ from Spec.BMCSecretRef if the operator generated a password. |  |  |
| `lastRotation` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | LastRotation is the timestamp of the last password rotation. |  |  |
| `passwordExpiration` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | PasswordExpiration is the timestamp when the password will expire. |  |  |
| `id` _string_ | ID is the identifier of the user in the BMC system. |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#condition-v1-meta) array_ | Conditions reflects the current state of the BMCUser. |  |  |


#### BMCVersion



BMCVersion is the Schema for the bmcversions API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `baseboard.metal.ironcore.dev/v1alpha1` | | |
| `kind` _string_ | `BMCVersion` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[BMCVersionSpec](#bmcversionspec)_ |  |  |  |
| `status` _[BMCVersionStatus](#bmcversionstatus)_ |  |  |  |


#### BMCVersionSet



BMCVersionSet is the Schema for the bmcversionsets API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `baseboard.metal.ironcore.dev/v1alpha1` | | |
| `kind` _string_ | `BMCVersionSet` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[BMCVersionSetSpec](#bmcversionsetspec)_ |  |  |  |
| `status` _[BMCVersionSetStatus](#bmcversionsetstatus)_ |  |  |  |


#### BMCVersionSetSpec



BMCVersionSetSpec defines the desired state of BMCVersionSet.



_Appears in:_
- [BMCVersionSet](#bmcversionset)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `bmcSelector` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#labelselector-v1-meta)_ | BMCSelector specifies a label selector to identify the BMCs to be selected. |  |  |
| `bmcVersionTemplate` _[BMCVersionTemplate](#bmcversiontemplate)_ | BMCVersionTemplate defines the template for the BMCVersion resource to be applied to the BMCs. |  |  |


#### BMCVersionSetStatus



BMCVersionSetStatus defines the observed state of BMCVersionSet.



_Appears in:_
- [BMCVersionSet](#bmcversionset)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `fullyLabeledBMCs` _integer_ | FullyLabeledBMCs is the number of BMCs in the set. |  |  |
| `availableBMCVersion` _integer_ | AvailableBMCVersion is the number of BMCVersion resources currently created by the set. |  |  |
| `pendingBMCVersion` _integer_ | PendingBMCVersion is the total number of pending BMCVersion resources in the set. |  |  |
| `inProgressBMCVersion` _integer_ | InProgressBMCVersion is the total number of BMCVersion resources in the set that are currently in progress. |  |  |
| `completedBMCVersion` _integer_ | CompletedBMCVersion is the total number of completed BMCVersion resources in the set. |  |  |
| `failedBMCVersion` _integer_ | FailedBMCVersion is the total number of failed BMCVersion resources in the set. |  |  |


#### BMCVersionSpec



BMCVersionSpec defines the desired state of BMCVersion.



_Appears in:_
- [BMCVersion](#bmcversion)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `version` _string_ | Version specifies the BMC version to upgrade to. |  |  |
| `updatePolicy` _[UpdatePolicy](#updatepolicy)_ | UpdatePolicy indicates whether the server's upgrade service should bypass vendor update policies. |  |  |
| `image` _[ImageSpec](#imagespec)_ | Image specifies the image to use to upgrade to the given BMC version. |  |  |
| `retryPolicy` _[RetryPolicy](#retrypolicy)_ | RetryPolicy defines the retry behavior for automatic retries on transient failures. |  |  |
| `serverMaintenancePolicy` _[ServerMaintenancePolicy](#servermaintenancepolicy)_ | ServerMaintenancePolicy is a maintenance policy to be enforced on the server managed by referred BMC. |  |  |
| `serverMaintenanceRefs` _ObjectReference array_ | ServerMaintenanceRefs are references to ServerMaintenance objects that the controller has requested for the related servers. |  |  |
| `bmcRef` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core)_ | BMCRef is a reference to a specific BMC to apply BMC upgrade on. |  |  |


#### BMCVersionState

_Underlying type:_ _string_

BMCVersionState describes the current state of a BMCVersion.



_Appears in:_
- [BMCVersionStatus](#bmcversionstatus)

| Field | Description |
| --- | --- |
| `Pending` | BMCVersionStatePending specifies that the BMC upgrade is waiting.<br /> |
| `InProgress` | BMCVersionStateInProgress specifies that upgrading BMC is in progress.<br /> |
| `Completed` | BMCVersionStateCompleted specifies that the BMC upgrade maintenance has been completed.<br /> |
| `Failed` | BMCVersionStateFailed specifies that the BMC upgrade maintenance has failed.<br /> |


#### BMCVersionStatus



BMCVersionStatus defines the observed state of BMCVersion.



_Appears in:_
- [BMCVersion](#bmcversion)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `state` _[BMCVersionState](#bmcversionstate)_ | State represents the current state of the BMC configuration task. |  |  |
| `upgradeTask` _[Task](#task)_ | UpgradeTask contains the state of the upgrade task created by the BMC. |  |  |
| `failedAttempts` _integer_ | FailedAttempts is the number of automatic retry attempts made after failure. |  |  |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed by the controller. |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#condition-v1-meta) array_ | Conditions represents the latest available observations of the BMC version upgrade state. |  |  |


#### BMCVersionTemplate



BMCVersionTemplate defines the desired BMC firmware version and upgrade parameters.



_Appears in:_
- [BMCVersionSetSpec](#bmcversionsetspec)
- [BMCVersionSpec](#bmcversionspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `version` _string_ | Version specifies the BMC version to upgrade to. |  |  |
| `updatePolicy` _[UpdatePolicy](#updatepolicy)_ | UpdatePolicy indicates whether the server's upgrade service should bypass vendor update policies. |  |  |
| `image` _[ImageSpec](#imagespec)_ | Image specifies the image to use to upgrade to the given BMC version. |  |  |
| `retryPolicy` _[RetryPolicy](#retrypolicy)_ | RetryPolicy defines the retry behavior for automatic retries on transient failures. |  |  |
| `serverMaintenancePolicy` _[ServerMaintenancePolicy](#servermaintenancepolicy)_ | ServerMaintenancePolicy is a maintenance policy to be enforced on the server managed by referred BMC. |  |  |



## maintenance.metal.ironcore.dev/v1alpha1

Package v1alpha1 contains API Schema definitions for the maintenance.metal.ironcore.dev v1alpha1 API group.

### Resource Types
- [ServerMaintenance](#servermaintenance)



#### ServerMaintenance



ServerMaintenance is the Schema for the ServerMaintenance API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `maintenance.metal.ironcore.dev/v1alpha1` | | |
| `kind` _string_ | `ServerMaintenance` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[ServerMaintenanceSpec](#servermaintenancespec)_ |  |  |  |
| `status` _[ServerMaintenanceStatus](#servermaintenancestatus)_ |  |  |  |


#### ServerMaintenancePolicy

_Underlying type:_ _string_

ServerMaintenancePolicy specifies the maintenance policy to be enforced on the server.



_Appears in:_
- [BIOSSettingsSpec](#biossettingsspec)
- [BIOSSettingsTemplate](#biossettingstemplate)
- [BIOSVersionSpec](#biosversionspec)
- [BIOSVersionTemplate](#biosversiontemplate)
- [BMCSettingsSpec](#bmcsettingsspec)
- [BMCSettingsTemplate](#bmcsettingstemplate)
- [BMCVersionSpec](#bmcversionspec)
- [BMCVersionTemplate](#bmcversiontemplate)
- [ServerMaintenanceSpec](#servermaintenancespec)
- [SettingsTemplate](#settingstemplate)

| Field | Description |
| --- | --- |
| `OwnerApproval` | ServerMaintenancePolicyOwnerApproval specifies that the maintenance policy requires owner approval.<br /> |
| `Enforced` | ServerMaintenancePolicyEnforced specifies that the maintenance policy is enforced.<br /> |


#### ServerMaintenanceSpec



ServerMaintenanceSpec defines the desired state of a ServerMaintenance.



_Appears in:_
- [ServerMaintenance](#servermaintenance)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `policy` _[ServerMaintenancePolicy](#servermaintenancepolicy)_ | Policy specifies the maintenance policy to be enforced on the server. |  | Enum: [OwnerApproval Enforced] <br /> |
| `serverRef` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core)_ | ServerRef is a reference to the server that is to be maintained. |  |  |
| `locatorLED` _[IndicatorLED](https://github.com/ironcore-dev/metal-operator/blob/main/docs/api-reference/api.md#indicatorled)_ | LocatorLED specifies the desired state of the server's locator LED during maintenance.<br />When maintenance ends, the locator LED is turned off. |  |  |
| `priority` _integer_ | Priority determines ordering when multiple ServerMaintenance resources target the same server.<br />Higher values are processed first. If priorities are equal, older resources are processed first.<br />If omitted, priority is treated as 0. | 0 |  |


#### ServerMaintenanceState

_Underlying type:_ _string_

ServerMaintenanceState specifies the current state of the server maintenance.



_Appears in:_
- [ServerMaintenanceStatus](#servermaintenancestatus)

| Field | Description |
| --- | --- |
| `Pending` | ServerMaintenanceStatePending specifies that the server maintenance is pending.<br /> |
| `InMaintenance` | ServerMaintenanceStateInMaintenance specifies that the server is in maintenance.<br /> |
| `Failed` | ServerMaintenanceStateFailed specifies that the server maintenance has failed.<br /> |


#### ServerMaintenanceStatus



ServerMaintenanceStatus defines the observed state of a ServerMaintenance.



_Appears in:_
- [ServerMaintenance](#servermaintenance)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `state` _[ServerMaintenanceState](#servermaintenancestate)_ | State specifies the current state of the server maintenance. |  |  |



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



## system.metal.ironcore.dev/v1alpha1

Package v1alpha1 contains API Schema definitions for the system.metal.ironcore.dev v1alpha1 API group.

### Resource Types
- [BIOSSettings](#biossettings)
- [BIOSSettingsSet](#biossettingsset)
- [BIOSVersion](#biosversion)
- [BIOSVersionSet](#biosversionset)



#### BIOSSettings



BIOSSettings is the Schema for the biossettings API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `system.metal.ironcore.dev/v1alpha1` | | |
| `kind` _string_ | `BIOSSettings` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[BIOSSettingsSpec](#biossettingsspec)_ |  |  |  |
| `status` _[BIOSSettingsStatus](#biossettingsstatus)_ |  |  |  |


#### BIOSSettingsFlowState

_Underlying type:_ _string_

BIOSSettingsFlowState describes the state of a single settings-flow priority step.



_Appears in:_
- [BIOSSettingsFlowStatus](#biossettingsflowstatus)

| Field | Description |
| --- | --- |
| `Pending` | BIOSSettingsFlowStatePending specifies that the BIOS settings update for the current priority is pending.<br /> |
| `InProgress` | BIOSSettingsFlowStateInProgress specifies that the BIOS settings update for the current priority is in progress.<br /> |
| `Applied` | BIOSSettingsFlowStateApplied specifies that the BIOS settings for the current priority have been applied.<br /> |
| `Failed` | BIOSSettingsFlowStateFailed specifies that the BIOS settings update has failed.<br /> |


#### BIOSSettingsFlowStatus



BIOSSettingsFlowStatus describes the per-priority-step reconciliation state.



_Appears in:_
- [BIOSSettingsStatus](#biossettingsstatus)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `flowState` _[BIOSSettingsFlowState](#biossettingsflowstate)_ | State represents the current state of the BIOS settings update for the current priority. |  |  |
| `name` _string_ | Name identifies the current priority settings from the spec. |  |  |
| `priority` _integer_ | Priority identifies the settings priority from the spec. |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#condition-v1-meta) array_ | Conditions represents the latest available observations of the BIOSSettings's current Flowstate. |  |  |
| `lastAppliedTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | LastAppliedTime represents the timestamp when the last setting was successfully applied. |  |  |


#### BIOSSettingsSet



BIOSSettingsSet is the Schema for the biossettingssets API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `system.metal.ironcore.dev/v1alpha1` | | |
| `kind` _string_ | `BIOSSettingsSet` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[BIOSSettingsSetSpec](#biossettingssetspec)_ |  |  |  |
| `status` _[BIOSSettingsSetStatus](#biossettingssetstatus)_ |  |  |  |


#### BIOSSettingsSetSpec



BIOSSettingsSetSpec defines the desired state of BIOSSettingsSet.



_Appears in:_
- [BIOSSettingsSet](#biossettingsset)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `biosSettingsTemplate` _[BIOSSettingsTemplate](#biossettingstemplate)_ | BIOSSettingsTemplate defines the template for the BIOSSettings resource to be applied to the servers. |  |  |
| `serverSelector` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#labelselector-v1-meta)_ | ServerSelector specifies a label selector to identify the servers that are to be selected. |  |  |


#### BIOSSettingsSetStatus



BIOSSettingsSetStatus defines the observed state of BIOSSettingsSet.



_Appears in:_
- [BIOSSettingsSet](#biossettingsset)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `fullyLabeledServers` _integer_ | FullyLabeledServers is the number of servers in the set. |  |  |
| `availableBIOSSettings` _integer_ | AvailableBIOSSettings is the number of BIOSSettings currently created by the set. |  |  |
| `pendingBIOSSettings` _integer_ | PendingBIOSSettings is the total number of pending BIOSSettings in the set. |  |  |
| `inProgressBIOSSettings` _integer_ | InProgressBIOSSettings is the total number of BIOSSettings in the set that are currently in progress. |  |  |
| `completedBIOSSettings` _integer_ | CompletedBIOSSettings is the total number of completed BIOSSettings in the set. |  |  |
| `failedBIOSSettings` _integer_ | FailedBIOSSettings is the total number of failed BIOSSettings in the set. |  |  |


#### BIOSSettingsSpec



BIOSSettingsSpec defines the desired state of BIOSSettings.



_Appears in:_
- [BIOSSettings](#biossettings)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `version` _string_ | Version specifies the software version (e.g. BIOS, BMC) these settings apply to. |  |  |
| `settingsFlow` _[SettingsFlowItem](#settingsflowitem) array_ | SettingsFlow contains the settings sequence to apply in the given order. |  |  |
| `retryPolicy` _[RetryPolicy](#retrypolicy)_ | RetryPolicy defines the retry behavior for automatic retries on transient failures. |  |  |
| `variables` _[Variable](#variable) array_ | Variables is a list of variables that can be used in the settings for templating. |  | MaxItems: 64 <br /> |
| `serverMaintenancePolicy` _[ServerMaintenancePolicy](#servermaintenancepolicy)_ | ServerMaintenancePolicy is a maintenance policy to be applied on the server. |  |  |
| `serverMaintenanceRef` _[ObjectReference](#objectreference)_ | ServerMaintenanceRef is a reference to a ServerMaintenance object that BIOSSettings has requested for the referred server. |  |  |
| `serverRef` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core)_ | ServerRef is a reference to a specific server to apply the BIOS settings on. |  |  |


#### BIOSSettingsState

_Underlying type:_ _string_

BIOSSettingsState specifies the current state of the BIOS Settings update.



_Appears in:_
- [BIOSSettingsStatus](#biossettingsstatus)

| Field | Description |
| --- | --- |
| `Pending` | BIOSSettingsStatePending specifies that the BIOS settings update is waiting.<br /> |
| `InProgress` | BIOSSettingsStateInProgress specifies that the BIOS settings update is in progress.<br /> |
| `Applied` | BIOSSettingsStateApplied specifies that the BIOS settings have been applied.<br /> |
| `Failed` | BIOSSettingsStateFailed specifies that the BIOS settings update has failed.<br /> |


#### BIOSSettingsStatus



BIOSSettingsStatus defines the observed state of BIOSSettings.



_Appears in:_
- [BIOSSettings](#biossettings)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `state` _[BIOSSettingsState](#biossettingsstate)_ | State represents the current state of the BIOS settings update. |  |  |
| `flowState` _[BIOSSettingsFlowStatus](#biossettingsflowstatus) array_ | FlowState is a list of individual BIOSSettings operation flows. |  |  |
| `lastAppliedTime` _[Time](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#time-v1-meta)_ | LastAppliedTime represents the timestamp when the last setting was successfully applied. |  |  |
| `failedAttempts` _integer_ | FailedAttempts is the number of automatic retry attempts made after failure. |  |  |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed by the controller. |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#condition-v1-meta) array_ | Conditions represents the latest available observations of the BIOSSettings's current state. |  |  |


#### BIOSSettingsTemplate



BIOSSettingsTemplate defines the template for BIOS settings to be applied.



_Appears in:_
- [BIOSSettingsSetSpec](#biossettingssetspec)
- [BIOSSettingsSpec](#biossettingsspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `version` _string_ | Version specifies the software version (e.g. BIOS, BMC) these settings apply to. |  |  |
| `settingsFlow` _[SettingsFlowItem](#settingsflowitem) array_ | SettingsFlow contains the settings sequence to apply in the given order. |  |  |
| `retryPolicy` _[RetryPolicy](#retrypolicy)_ | RetryPolicy defines the retry behavior for automatic retries on transient failures. |  |  |
| `variables` _[Variable](#variable) array_ | Variables is a list of variables that can be used in the settings for templating. |  | MaxItems: 64 <br /> |
| `serverMaintenancePolicy` _[ServerMaintenancePolicy](#servermaintenancepolicy)_ | ServerMaintenancePolicy is a maintenance policy to be applied on the server. |  |  |


#### BIOSVersion



BIOSVersion is the Schema for the biosversions API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `system.metal.ironcore.dev/v1alpha1` | | |
| `kind` _string_ | `BIOSVersion` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[BIOSVersionSpec](#biosversionspec)_ |  |  |  |
| `status` _[BIOSVersionStatus](#biosversionstatus)_ |  |  |  |


#### BIOSVersionSet



BIOSVersionSet is the Schema for the biosversionsets API.





| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `apiVersion` _string_ | `system.metal.ironcore.dev/v1alpha1` | | |
| `kind` _string_ | `BIOSVersionSet` | | |
| `metadata` _[ObjectMeta](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#objectmeta-v1-meta)_ | Refer to Kubernetes API documentation for fields of `metadata`. |  |  |
| `spec` _[BIOSVersionSetSpec](#biosversionsetspec)_ |  |  |  |
| `status` _[BIOSVersionSetStatus](#biosversionsetstatus)_ |  |  |  |


#### BIOSVersionSetSpec



BIOSVersionSetSpec defines the desired state of BIOSVersionSet.



_Appears in:_
- [BIOSVersionSet](#biosversionset)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `serverSelector` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#labelselector-v1-meta)_ | ServerSelector specifies a label selector to identify the servers that are to be selected. |  |  |
| `biosVersionTemplate` _[BIOSVersionTemplate](#biosversiontemplate)_ | BIOSVersionTemplate defines the template for the BIOSVersion resource to be applied to the servers. |  |  |


#### BIOSVersionSetStatus



BIOSVersionSetStatus defines the observed state of BIOSVersionSet.



_Appears in:_
- [BIOSVersionSet](#biosversionset)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `fullyLabeledServers` _integer_ | FullyLabeledServers is the number of servers in the set. |  |  |
| `availableBIOSVersion` _integer_ | AvailableBIOSVersion is the number of BIOSVersion created by the set. |  |  |
| `pendingBIOSVersion` _integer_ | PendingBIOSVersion is the total number of pending BIOSVersion in the set. |  |  |
| `inProgressBIOSVersion` _integer_ | InProgressBIOSVersion is the total number of BIOSVersion resources in the set that are currently in progress. |  |  |
| `completedBIOSVersion` _integer_ | CompletedBIOSVersion is the total number of completed BIOSVersion in the set. |  |  |
| `failedBIOSVersion` _integer_ | FailedBIOSVersion is the total number of failed BIOSVersion in the set. |  |  |


#### BIOSVersionSpec



BIOSVersionSpec defines the desired state of BIOSVersion.



_Appears in:_
- [BIOSVersion](#biosversion)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `version` _string_ | Version specifies the BIOS version to upgrade to. |  |  |
| `updatePolicy` _[UpdatePolicy](#updatepolicy)_ | UpdatePolicy indicates whether the server's upgrade service should bypass vendor update policies. |  |  |
| `image` _[ImageSpec](#imagespec)_ | Image specifies the image to use to upgrade to the given BIOS version. |  |  |
| `serverMaintenancePolicy` _[ServerMaintenancePolicy](#servermaintenancepolicy)_ | ServerMaintenancePolicy is a maintenance policy to be enforced on the server. |  |  |
| `retryPolicy` _[RetryPolicy](#retrypolicy)_ | RetryPolicy defines the retry behavior for automatic retries on transient failures. |  |  |
| `serverMaintenanceRef` _[ObjectReference](#objectreference)_ | ServerMaintenanceRef is a reference to a ServerMaintenance object that the controller has requested for the referred server. |  |  |
| `serverRef` _[LocalObjectReference](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#localobjectreference-v1-core)_ | ServerRef is a reference to a specific server to apply the BIOS upgrade on. |  |  |


#### BIOSVersionState

_Underlying type:_ _string_

BIOSVersionState describes the current state of a BIOSVersion.



_Appears in:_
- [BIOSVersionStatus](#biosversionstatus)

| Field | Description |
| --- | --- |
| `Pending` | BIOSVersionStatePending specifies that the BIOS upgrade is waiting.<br /> |
| `InProgress` | BIOSVersionStateInProgress specifies that upgrading BIOS is in progress.<br /> |
| `Completed` | BIOSVersionStateCompleted specifies that the BIOS upgrade has been completed.<br /> |
| `Failed` | BIOSVersionStateFailed specifies that the BIOS upgrade has failed.<br /> |


#### BIOSVersionStatus



BIOSVersionStatus defines the observed state of BIOSVersion.



_Appears in:_
- [BIOSVersion](#biosversion)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `state` _[BIOSVersionState](#biosversionstate)_ | State represents the current state of the BIOS upgrade task. |  |  |
| `upgradeTask` _[Task](#task)_ | UpgradeTask contains the state of the Upgrade Task created by the BMC. |  |  |
| `failedAttempts` _integer_ | FailedAttempts is the number of automatic retry attempts made after failure. |  |  |
| `observedGeneration` _integer_ | ObservedGeneration is the most recent generation observed by the controller. |  |  |
| `conditions` _[Condition](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#condition-v1-meta) array_ | Conditions represents the latest available observations of the BIOS version upgrade state. |  |  |


#### BIOSVersionTemplate



BIOSVersionTemplate defines the desired BIOS firmware version and upgrade parameters.



_Appears in:_
- [BIOSVersionSetSpec](#biosversionsetspec)
- [BIOSVersionSpec](#biosversionspec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `version` _string_ | Version specifies the BIOS version to upgrade to. |  |  |
| `updatePolicy` _[UpdatePolicy](#updatepolicy)_ | UpdatePolicy indicates whether the server's upgrade service should bypass vendor update policies. |  |  |
| `image` _[ImageSpec](#imagespec)_ | Image specifies the image to use to upgrade to the given BIOS version. |  |  |
| `serverMaintenancePolicy` _[ServerMaintenancePolicy](#servermaintenancepolicy)_ | ServerMaintenancePolicy is a maintenance policy to be enforced on the server. |  |  |
| `retryPolicy` _[RetryPolicy](#retrypolicy)_ | RetryPolicy defines the retry behavior for automatic retries on transient failures. |  |  |



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


#### ConsoleConnection



ConsoleConnection describes how to reach the server management console.



_Appears in:_
- [ConsoleSpec](#consolespec)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `url` _string_ | URL is the URL of the server management console. |  |  |
| `insecureSkipTLSVerify` _boolean_ | InsecureSkipTLSVerify disables TLS certificate verification when<br />communicating with the management console. This should only be used for<br />consoles that present self-signed or otherwise untrusted certificates. | false |  |


#### ConsoleSpec



ConsoleSpec defines the desired state of Console.



_Appears in:_
- [Console](#console)

| Field | Description | Default | Validation |
| --- | --- | --- | --- |
| `serverSelector` _[LabelSelector](https://kubernetes.io/docs/reference/generated/kubernetes-api/v1.35/#labelselector-v1-meta)_ | ServerSelector specifies a label selector to identify the servers that are to be selected. |  |  |
| `connection` _[ConsoleConnection](#consoleconnection)_ | Connection contains the console endpoint and transport-security settings. |  |  |
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


