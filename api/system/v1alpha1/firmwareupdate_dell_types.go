// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
)

// DellShareType is the type of network share hosting the Dell update repository/catalog.
type DellShareType string

const (
	DellShareTypeNFS   DellShareType = "NFS"
	DellShareTypeCIFS  DellShareType = "CIFS"
	DellShareTypeHTTP  DellShareType = "HTTP"
	DellShareTypeHTTPS DellShareType = "HTTPS"
)

// DellVersionApplyPolicy controls whether Dell's InstallFromRepository job applies packages
// that are already at the same version and/or older than the currently installed version.
// If unset, only genuine upgrades (newer than the installed version) are applied.
type DellVersionApplyPolicy string

const (
	// DellVersionApplyPolicyAllowSameVersion re-applies packages already at the same version.
	DellVersionApplyPolicyAllowSameVersion DellVersionApplyPolicy = "AllowSameVersion"
	// DellVersionApplyPolicyAllowDowngradeVersion allows applying packages older than the currently installed version.
	DellVersionApplyPolicyAllowDowngradeVersion DellVersionApplyPolicy = "AllowDowngradeVersion"
	// DellVersionApplyPolicyAllowSameAndDowngradeVersion allows both re-applying same-version packages and downgrades.
	DellVersionApplyPolicyAllowSameAndDowngradeVersion DellVersionApplyPolicy = "AllowSameAndDowngradeVersion"
)

// DellCertificateVerificationPolicy controls whether iDRAC verifies the TLS certificate
// presented by the repository share (HTTPS/CIFS) before connecting, or ignores certificate
// warnings (e.g. self-signed/untrusted certificates). If unset, certificates are verified.
type DellCertificateVerificationPolicy string

const (
	// DellCertificateVerificationPolicyVerify requires the share's TLS certificate to be valid
	// and trusted. This is the default behavior when the field is unset.
	DellCertificateVerificationPolicyVerify DellCertificateVerificationPolicy = "Verify"
	// DellCertificateVerificationPolicyIgnore instructs iDRAC to ignore certificate warnings
	// (e.g. self-signed or untrusted certificates) when connecting to the share.
	DellCertificateVerificationPolicyIgnore DellCertificateVerificationPolicy = "Ignore"
)

// RepositoryJob represents a Dell iDRAC job resource tracking a repository-based firmware
// operation. State is intentionally a plain string mirroring bmc.DellJob.
type RepositoryJob struct {
	// +optional
	JobID string `json:"jobID,omitempty"`
	// +optional
	Name string `json:"name,omitempty"`
	// +optional
	JobType string `json:"jobType,omitempty"`
	// +optional
	State string `json:"state,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// +optional
	PercentComplete int32 `json:"percentComplete,omitempty"`
}

// ComponentJobsSummary tallies the current pass's per-component jobs (ComponentJobs) by
// completion state, computed by the controller purely for observability (e.g. printcolumns);
// controller logic drives off ComponentJobs directly rather than this summary.
type ComponentJobsSummary struct {
	// Total is the number of component jobs discovered so far in the current pass.
	// +optional
	Total int32 `json:"total,omitempty"`

	// Completed is the number of component jobs that finished successfully.
	// +optional
	Completed int32 `json:"completed,omitempty"`

	// InProgress is the number of component jobs that have not yet reached a terminal state.
	// +optional
	InProgress int32 `json:"inProgress,omitempty"`

	// Failed is the number of component jobs that finished in a failed state.
	// +optional
	Failed int32 `json:"failed,omitempty"`
}

// DellFirmwareUpdateStatus contains status fields specific to Dell's repository-based firmware
// update mechanism (DellSoftwareInstallationService.InstallFromRepository). Keeping these fields
// vendor-namespaced (rather than flat on FirmwareUpdateStatus) mirrors DellFirmwareRepository in
// the spec and leaves room for sibling vendor-specific status structs (e.g. for Fujitsu/Lenovo
// image-based updates) to be added to FirmwareUpdateStatus without colliding field names.
type DellFirmwareUpdateStatus struct {
	// CheckJob contains the state of the dry-run catalog-check job.
	// +optional
	CheckJob *RepositoryJob `json:"checkJob,omitempty"`

	// UpdateJob contains the state of the main apply job.
	// +optional
	UpdateJob *RepositoryJob `json:"updateJob,omitempty"`

	// ComponentJobs contains the state of the per-component jobs spawned by the current pass's apply job.
	// +optional
	ComponentJobs []RepositoryJob `json:"componentJobs,omitempty"`

	// ComponentJobsSummary tallies ComponentJobs by completion state.
	// +optional
	ComponentJobsSummary *ComponentJobsSummary `json:"componentJobsSummary,omitempty"`

	// BaselineJobIDs contains the iDRAC job IDs present just before issuing the apply call for the
	// current pass, used to diff and discover newly spawned component jobs. A non-nil (possibly
	// empty) slice indicates the baseline has been captured for the current pass.
	// +optional
	BaselineJobIDs []string `json:"baselineJobIDs,omitempty"`
}

// DellFirmwareRepository describes the network share hosting Dell's update repository/catalog,
// as consumed by DellSoftwareInstallationService.InstallFromRepository.
type DellFirmwareRepository struct {
	// ShareType is the type of network share hosting the repository.
	// +kubebuilder:validation:Enum=NFS;CIFS;HTTP;HTTPS
	// +required
	ShareType DellShareType `json:"shareType"`

	// Address is the share's hostname or IP address (e.g. downloads.dell.com).
	// +optional
	Address string `json:"address,omitempty"`

	// ShareName is the network share name. Not required for HTTP/HTTPS catalogs.
	// +optional
	ShareName string `json:"shareName,omitempty"`

	// CatalogFile is the catalog file name within the share. Defaults to "Catalog.xml".
	// +optional
	CatalogFile string `json:"catalogFile,omitempty"`

	// CredentialsRef references the credentials used to authenticate against the share, if required.
	// Must not be set when ShareType is HTTP.
	// +optional
	CredentialsRef *corev1.SecretReference `json:"credentialsRef,omitempty"`

	// RebootNeeded, if true, allows the BMC to reboot the server to apply updates.
	// +optional
	RebootNeeded bool `json:"rebootNeeded,omitempty"`

	// ApplyVersionPolicy controls whether packages already at the same version and/or older
	// than the currently installed version are applied. If unset, only genuine upgrades are applied.
	// +kubebuilder:validation:Enum=AllowSameVersion;AllowDowngradeVersion;AllowSameAndDowngradeVersion
	// +optional
	ApplyVersionPolicy *DellVersionApplyPolicy `json:"applyVersionPolicy,omitempty"`

	// CertificateVerification controls whether iDRAC verifies the TLS certificate presented by
	// the repository share, or ignores certificate warnings (e.g. self-signed/untrusted
	// certificates). If unset, certificates are verified.
	// +kubebuilder:validation:Enum=Verify;Ignore
	// +optional
	CertificateVerification *DellCertificateVerificationPolicy `json:"certificateVerification,omitempty"`
}
