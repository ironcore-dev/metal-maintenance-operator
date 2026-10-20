// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package hwmgr

import (
	"github.com/go-logr/logr"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

// FujitsuClient is a placeholder implementation of ClientInterface for Fujitsu
// servers. Fujitsu does not have a central vendor management console (unlike
// Dell OME, HPE OneView, or Lenovo XClarity), so all operations are no-ops.
type FujitsuClient struct {
	log logr.Logger
}

// NewFujitsuClient creates a no-op client for Fujitsu servers.
func NewFujitsuClient(_ ClientOptions) (*FujitsuClient, error) {
	return &FujitsuClient{
		log: logr.Discard(),
	}, nil
}

// NoConsole signals that Fujitsu has no central management console,
// so the controller will skip import scheduling for Fujitsu servers.
func (c *FujitsuClient) NoConsole() {}

// ListServers returns an empty list. Fujitsu has no central vendor management
// console to query for managed servers.
func (c *FujitsuClient) ListServers() ([]Device, error) {
	c.log.Info("ListServers is a no-op for Fujitsu: no central vendor management console")
	return []Device{}, nil
}

// ImportServer is a no-op. Fujitsu has no central vendor management console to
// import servers into.
func (c *FujitsuClient) ImportServer(
	hostname string,
	_ metalv1alpha1.IP,
	_ string,
	_ string,
) error {
	c.log.Info("ImportServer is a no-op for Fujitsu: no central vendor management console", "hostname", hostname)
	return nil
}

// RemoveServer is a no-op. Fujitsu has no central vendor management console to
// remove servers from.
func (c *FujitsuClient) RemoveServer(
	hostname string,
	_ metalv1alpha1.IP,
) error {
	c.log.Info("RemoveServer is a no-op for Fujitsu: no central vendor management console", "hostname", hostname)
	return nil
}

// GetAuthToken is a no-op. Fujitsu has no central vendor management console
// requiring authentication.
func (c *FujitsuClient) GetAuthToken() (string, error) {
	return "", nil
}

// ImportServerAsync is a no-op wrapper.
func (c *FujitsuClient) ImportServerAsync(
	hostname string,
	ip metalv1alpha1.IP,
	_ string,
	_ string,
) (string, error) {
	return "", c.ImportServer(hostname, ip, "", "")
}

// RemoveServerAsync is a no-op wrapper.
func (c *FujitsuClient) RemoveServerAsync(
	hostname string,
	ip metalv1alpha1.IP,
) (string, error) {
	return "", c.RemoveServer(hostname, ip)
}

// GetJobStatus returns a completed status. Fujitsu operations are synchronous
// no-ops so no real job tracking is needed.
func (c *FujitsuClient) GetJobStatus(jobID string) (*JobInfo, error) {
	return &JobInfo{
		JobID:    jobID,
		Status:   jobStatusCompleted,
		Progress: 100,
	}, nil
}

// IsJobComplete always returns true for Fujitsu no-op operations.
func (c *FujitsuClient) IsJobComplete(_ *JobInfo) bool {
	return true
}

// IsJobSuccessful always returns true for Fujitsu no-op operations.
func (c *FujitsuClient) IsJobSuccessful(_ *JobInfo) bool {
	return true
}
