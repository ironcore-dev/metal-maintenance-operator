/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

// DELLFirmwareUpdateOMESpec defines the desired state of DELLFirmwareUpdateOME.
type DELLFirmwareUpdateOMESpec struct {
	// OMEURL is the URL of the Dell OpenManage Enterprise (OME) instance.
	// +required
	// +kubebuilder:validation:Pattern=`^https?://[a-zA-Z0-9.-]+(:[0-9]+)?(/.*)?$`
	OMEURL string `json:"omeURL"`

	// secretRef is a reference to the Kubernetes Secret (of type SecretTypeBasicAuth) object that contains the credentials
	// to access the Dell OpenManage Enterprise (OME). This secret includes sensitive information such as usernames and passwords.
	// +required
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`

	// CreateCatalog is the fields required to create catalog through the Dell OpenManage Enterprise (OME).
	// +optional
	CreateCatalog *CreateCatalog `json:"createCatalog,omitempty"`

	// CatalogName is the name of the catalog to be used for the firmware update.
	// The operator will use the catalog name and ignore CreateCatalog field.
	// +optional
	CatalogName string `json:"catalogName,omitempty"`

	// ServerSelector specifies a label selector to identify the servers that are to be selected.
	// +required
	ServerSelector metav1.LabelSelector `json:"serverSelector"`
}

type CreateCatalog struct {
	// FileName is the name of the catalog file to be created.
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-zA-Z0-9._-]+$`
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:MinLength=1
	FileName string `json:"fileName"`
	// CatalogPath is the path to the catalog file on the OME server.
	// This is the path where the catalog will be created. with IP or FQDN of the repo server.
	// +required
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Type=string
	CatalogPath string `json:"sourcePath"`
	// Repository contains the details of the repository from which the catalog will be created.
	// +required
	// +kubebuilder:validation:Required
	Repository *Repository `json:"repository"`
}

type Repository struct {
	// Name is the name of the repository.
	// +optional
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
	// Description is a brief description of the repository.
	// +optional
	// +kubebuilder:validation:MaxLength=1024
	Description string `json:"description"`
	// RepositoryType is the type of the repository (e.g., "CIFS", "NFS", "HTTPS", "downloads.dell.com").
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=CIFS;NFS;HTTPS;HTTP;downloads.dell.com
	// +kubebuilder:validation:MinLength=1
	RepositoryType string `json:"repositoryType"`
	// Source is the source URL/IP of the repository.
	// +required
	// +kubebuilder:validation:Required
	Source string `json:"source"`
	// DomainName is the domain name for authentication, if required.
	// +optional
	DomainName string `json:"domainName"`
	// Username is the username for authentication to the repository.
	// +optional
	Username string `json:"username"`
	// Password is the password for authentication to the repository.
	// +optional
	Password string `json:"password"`
	// CheckCertificate indicates whether to check the SSL certificate of the repository.
	// +required
	// +kubebuilder:default=true
	// +kubebuilder:validation:Type=boolean
	CheckCertificate bool `json:"checkCertificate"`
}

// DELLFirmwareUpdateOMEStatus defines the observed state of DELLFirmwareUpdateOME.
type DELLFirmwareUpdateOMEStatus struct {
	// State represents the current state of the bios configuration task.
	// +optional
	State FirmwareUpdateState `json:"state,omitempty"`

	// UpdateTask contains the state of the Update Task(s) created by the OME for the firmware upgrade.
	// +optional
	UpdateTask []Task `json:"updateTask,omitempty"`

	// Conditions represents the latest available observations of the Bios version upgrade state.
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// DELLFirmwareUpdateOME is the Schema for the dellfirmwareupdateomes API.
type DELLFirmwareUpdateOME struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DELLFirmwareUpdateOMESpec   `json:"spec,omitempty"`
	Status DELLFirmwareUpdateOMEStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DELLFirmwareUpdateOMEList contains a list of DELLFirmwareUpdateOME.
type DELLFirmwareUpdateOMEList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DELLFirmwareUpdateOME `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DELLFirmwareUpdateOME{}, &DELLFirmwareUpdateOMEList{})
}
