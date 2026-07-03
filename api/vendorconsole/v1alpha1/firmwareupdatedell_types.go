// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
//
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

type FirmwareUpdateState string

const (
	// FirmwareUpdateStatePending specifies that the BMC upgrade maintenance is waiting
	FirmwareUpdateStatePending FirmwareUpdateState = "Pending"
	// FirmwareUpdateStateInProgress specifies that upgrading BMC is in progress.
	FirmwareUpdateStateInProgress FirmwareUpdateState = "InProgress"
	// FirmwareUpdateStateCompleted specifies that the BMC upgrade maintenance has been completed.
	FirmwareUpdateStateCompleted FirmwareUpdateState = "Completed"
	// FirmwareUpdateStateFailed specifies that the BMC upgrade maintenance has failed.
	FirmwareUpdateStateFailed FirmwareUpdateState = "Failed"
)

type StageFirmwareUpdate string

const (
	// StagingFirmwareStaged specifies that no staging will be performed.
	// Starts the Firmware update on Reboot.
	StagingFirmwareStaged StageFirmwareUpdate = "StagedFirmwareUpdate"
	// NoStagingFirmwareUpdate specifies that the Firmware will be staged.
	// Starts the Firmware update immediately.
	NoStagingFirmwareUpdate StageFirmwareUpdate = "NoStagingFirmwareUpdate"
)

type SignVerifyFirmwareUpdate string

const (
	// SignVerify specifies that no staging will be performed.
	// DUP signature will be verified
	SignVerify SignVerifyFirmwareUpdate = "SignVerify"
	// SkipSignVerify specifies that the Firmware will be staged.
	// DUP signature will be verified skipped.
	SkipSignVerify SignVerifyFirmwareUpdate = "SkipSignVerify"
)

type ComplianceFirmwareUpdate string

const (
	// ComplianceUpdate specifies that firmware update needs to match compliance
	ComplianceUpdate ComplianceFirmwareUpdate = "ComplianceUpdate"
	// NoComplianceUpdate specifies that firmware update needs not match compliance
	NoComplianceUpdate ComplianceFirmwareUpdate = "NoComplianceUpdate"
)

type BaselineDowngradeType string

const (
	// DowngradableUpdate specifies that downgrade is allowed for baseline update
	DowngradableUpdate BaselineDowngradeType = "DowngradableUpdate"
	// NotDowngradableUpdate specifies that downgrade is not allowed for baseline update
	NotDowngradableUpdate BaselineDowngradeType = "NotDowngradableUpdate"
)

type BaselineBitType string

const (
	// BitType64 specifies baseline type is 64Bit
	BitType64 BaselineBitType = "64BitType"
)

type CheckCertificateCatalog string

const (
	// CheckCertificate specifies that certificate check must be done for HTTPS
	CheckCertificateHTTPS CheckCertificateCatalog = "CheckCertificateHTTPS"
	// NoCheckCertificate specifies that certificate check must not be done for HTTPS
	NoCheckCertificateHTTPS CheckCertificateCatalog = "NoCheckCertificateHTTPS"
)

// FirmwareUpdateDELLSpec defines the desired state of FirmwareUpdateDELL.
// +kubebuilder:validation:XValidation:rule="has(self.createCatalog) || has(self.catalogName)", message="Either createCatalog or catalogName must be provided, but not both."
type FirmwareUpdateDELLSpec struct {
	// OMEURL is the URL of the Dell OpenManage Enterprise (OME) instance.
	// +required
	// +kubebuilder:validation:Pattern=`^https?://[a-zA-Z0-9.-]+(:[0-9]+)?(/.*)?$`
	OMEURL string `json:"omeURL"`

	// secretRef is a reference to the Kubernetes Secret (of type SecretTypeBasicAuth) object that contains the credentials
	// to access the Dell OpenManage Enterprise (OME). This secret includes sensitive information such as usernames and passwords.
	// +required
	SecretRef *corev1.LocalObjectReference `json:"secretRef"`

	// CreateCatalog is the fields required to create catalog through the Dell OpenManage Enterprise (OME).
	// +optional
	CreateCatalog *CreateCatalog `json:"createCatalog,omitempty"`

	// CatalogRepositoryName is the name of the catalog to be used for the firmware update.
	// The operator will use the catalog name and ignore CreateCatalog field.
	// +optional
	CatalogRepositoryName string `json:"catalogName,omitempty"`

	// FirmwareUpgradeConfig contains configuration options for the firmware upgrade process.
	// +optional
	FirmwareUpgradeConfig *FirmwareUpgradeConfig `json:"firmwareUpgradeConfig,omitempty"`

	// BaselineConfig contains configuration options for the baseline to be used for the firmware update.
	// +optional
	BaselineConfig *BaselinesConfig `json:"baselineConfig,omitempty"`

	// ServerSelector specifies a label selector to identify the servers that are to be selected.
	// +required
	ServerSelector metav1.LabelSelector `json:"serverSelector"`

	// ServerMaintenancePolicy is a maintenance policy to be enforced on the server managed by referred BMC.
	// +optional
	ServerMaintenancePolicy metalv1alpha1.ServerMaintenancePolicy `json:"serverMaintenancePolicy,omitempty"`

	// ServerMaintenanceRefs are references to a ServerMaintenance objects that Controller has requested for the each of the related server.
	// +optional
	ServerMaintenanceRefs []metalv1alpha1.ServerMaintenanceRefItem `json:"serverMaintenanceRefs,omitempty"`
}

// FirmwareUpgradeConfig contains configuration options for the firmware upgrade process.
type FirmwareUpgradeConfig struct {
	// SignVerify specifies whether to verify the signature of the firmware before upgrade.
	// +optional
	// +kubebuilder:default="SignVerify"
	SignVerify SignVerifyFirmwareUpdate `json:"signVerify,omitempty"`

	// StagingValue is the value used for staging the firmware before upgrade.
	// +optional
	// +kubebuilder:default=NoStagingFirmwareUpdate
	StagingValue StageFirmwareUpdate `json:"stagingValue,omitempty"`

	// ComplianceUpdate specifies whether to perform a compliance update during the firmware upgrade.
	// +optional
	// +kubebuilder:default=ComplianceUpdate
	ComplianceUpdate ComplianceFirmwareUpdate `json:"complianceUpdate,omitempty"`

	// OperationName specifies the name of the operation to be performed for the firmware update.
	// refer to Dell OME API documentation for possible values.
	// +required
	// +kubebuilder:default="INSTALL_FIRMWARE"
	// +kubebuilder:validation:MinLength=1
	OperationName string `json:"operationName"`

	// JobTypeName specifies the type of job to be created for the firmware update.
	// refer to Dell OME API documentation for possible values.
	// +optional
	// +kubebuilder:default="Update_Task"
	JobTypeName string `json:"jobTypeName,omitempty"`
}

type BaselinesConfig struct {
	// Name is the name of the baseline to be used for the firmware update.
	// +required
	// +kubebuilder:validation:Required
	Name string `json:"name"`
	// Description is a brief description of the baseline.
	// +optional
	Description string `json:"description,omitempty"`
	// DowngradeEnabled specifies whether downgrade is enabled for the baseline update.
	// +optional
	// +kubebuilder:default=DowngradableUpdate
	DowngradeEnabled BaselineDowngradeType `json:"downgradeEnabled,omitempty"`
	// Is64Bit specifies whether the baseline update is for 64-bit systems.
	// +optional
	// +kubebuilder:default=BitType64
	BitType BaselineBitType `json:"bitType,omitempty"`
}

// note: Uniqueness constraints:
// CreateCatalog.FileName
// CreateCatalog.CatalogPath
// CreateCatalog.Repository.Name
// If all these are same as an existing catalog,
// then it is considered duplicate and will not be created again.
type CreateCatalog struct {
	// FileName is the name of the catalog file to be created.
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:MinLength=1
	FileName string `json:"fileName"`
	// SourcePath is the path to the catalog file on the OME server.
	// This is the path where the catalog will be created. with IP or FQDN of the repo server.
	// +required
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Type=string
	SourcePath string `json:"sourcePath"`
	// Repository contains the details of the repository from which the catalog will be created.
	// +required
	// +kubebuilder:validation:Required
	Repository *Repository `json:"repository"`
}

type Repository struct {
	// Name is the name of the repository.
	// +required
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Type=string
	Name string `json:"name"`
	// Description is a brief description of the repository.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Description string `json:"description,omitempty"`
	// RepositoryType is the type of the repository (e.g., "CIFS", "NFS", "HTTPS", "DELL_ONLINE").
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=CIFS;NFS;HTTPS;HTTP;DELL_ONLINE
	// +kubebuilder:validation:MinLength=1
	RepositoryType string `json:"repositoryType"`
	// Source is the source URL/IP of the repository.
	// +required
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Required
	Source string `json:"source"`
	// DomainName is the domain name for authentication, if required.
	// +optional
	DomainName string `json:"domainName,omitempty"`
	// Username is the username for authentication to the repository.
	// +optional
	Username string `json:"username,omitempty"`
	// Password is the password for authentication to the repository.
	// +optional
	Password string `json:"password,omitempty"`
	// CheckCertificate indicates whether to check the SSL certificate of the repository.
	// +optional
	// +kubebuilder:default=NoCheckCertificateHTTPS
	CheckCertificate CheckCertificateCatalog `json:"checkCertificate,omitempty"`
}

type DellJob struct {
	// Id is the unique identifier for the job created in OME.
	Id int `json:"jobId,omitempty"`
	// Name is the name of the job.
	Name string `json:"name,omitempty"`
}

// FirmwareUpdateDELLStatus defines the observed state of FirmwareUpdateDELL.
type FirmwareUpdateDELLStatus struct {
	// State represents the current state of the bios configuration task.
	// +optional
	State FirmwareUpdateState `json:"state,omitempty"`

	// UpdateTask contains the state of the Update Task created by the OME for the firmware upgrade.
	// +optional
	UpdateTask *DellJob `json:"updateTask,omitempty"`

	// Catalog contains the details of the Catalog created by the OME for the firmware upgrade.
	Catalog *DellCatalog `json:"catalog,omitempty"`

	// Baseline contains the details of the Baseline created by the OME for the firmware upgrade.
	// +optional
	Baseline *DellBaseline `json:"baseline,omitempty"`

	// ServerCount is the total number of servers selected by the ServerSelector.
	// +optional
	ServerCount int32 `json:"serverCount,omitempty"`

	// Conditions represents the latest available observations of the Bios version upgrade state.
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
}

type DellCatalog struct {
	// Id is the unique identifier for the catalog created in OME.
	Id int `json:"id,omitempty"`
}

type DellBaseline struct {
	// Id is the unique identifier for the baseline created in OME.
	Id int `json:"id,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="vendorURL",type=string,JSONPath=`.spec.omeURL`
// +kubebuilder:printcolumn:name="serverMaintenancePolicy",type=string,JSONPath=`.spec.serverMaintenancePolicy`
// +kubebuilder:printcolumn:name="serverCount",type=integer,JSONPath=`.status.serverCount`
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="!has(oldSelf.status) || !has(oldSelf.status.state) || oldSelf.status.state != 'InProgress' || self.spec.createCatalog == oldSelf.spec.createCatalog", message="CreateCatalog is immutable when status is InProgress"

// FirmwareUpdateDELL is the Schema for the FirmwareUpdateDELLs API.
type FirmwareUpdateDELL struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   FirmwareUpdateDELLSpec   `json:"spec,omitempty"`
	Status FirmwareUpdateDELLStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// FirmwareUpdateDELLList contains a list of FirmwareUpdateDELL.
type FirmwareUpdateDELLList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []FirmwareUpdateDELL `json:"items"`
}

func init() {
	SchemeBuilder.Register(&FirmwareUpdateDELL{}, &FirmwareUpdateDELLList{})
}
