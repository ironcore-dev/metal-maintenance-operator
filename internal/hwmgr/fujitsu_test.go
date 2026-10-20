// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package hwmgr

import (
	"net/netip"
	"testing"

	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

func TestNewFujitsuClient(t *testing.T) {
	c, err := NewFujitsuClient(ClientOptions{
		Endpoint: "https://irmc.example.com",
		Username: "admin",
		Password: "password",
	})
	if err != nil {
		t.Fatalf("NewFujitsuClient() returned error: %v", err)
	}

	if c == nil {
		t.Fatal("expected non-nil client")
	}
}

func TestFujitsuListServers(t *testing.T) {
	c, _ := NewFujitsuClient(ClientOptions{})

	devices, err := c.ListServers()
	if err != nil {
		t.Fatalf("ListServers() returned error: %v", err)
	}

	if len(devices) != 0 {
		t.Errorf("expected empty device list, got %d devices", len(devices))
	}
}

func TestFujitsuImportServer(t *testing.T) {
	c, _ := NewFujitsuClient(ClientOptions{})

	ip := metalv1alpha1.IP{Addr: netip.MustParseAddr("192.168.1.100")}

	err := c.ImportServer("FujitsuServer", ip, "admin", "password")
	if err != nil {
		t.Fatalf("ImportServer() returned error: %v", err)
	}
}

func TestFujitsuRemoveServer(t *testing.T) {
	c, _ := NewFujitsuClient(ClientOptions{})

	ip := metalv1alpha1.IP{Addr: netip.MustParseAddr("192.168.1.100")}

	err := c.RemoveServer("FujitsuServer", ip)
	if err != nil {
		t.Fatalf("RemoveServer() returned error: %v", err)
	}
}

func TestFujitsuRemoveServerAsync(t *testing.T) {
	c, _ := NewFujitsuClient(ClientOptions{})

	ip := metalv1alpha1.IP{Addr: netip.MustParseAddr("192.168.1.100")}

	jobID, err := c.RemoveServerAsync("FujitsuServer", ip)
	if err != nil {
		t.Fatalf("RemoveServerAsync() returned error: %v", err)
	}

	if jobID != "" {
		t.Errorf("expected empty job ID, got %q", jobID)
	}
}

func TestFujitsuGetJobStatus(t *testing.T) {
	c, _ := NewFujitsuClient(ClientOptions{})

	jobInfo, err := c.GetJobStatus("job-123")
	if err != nil {
		t.Fatalf("GetJobStatus() returned error: %v", err)
	}

	if jobInfo == nil {
		t.Fatal("expected JobInfo, got nil")
	}

	if jobInfo.JobID != "job-123" {
		t.Errorf("expected job ID job-123, got %q", jobInfo.JobID)
	}

	if jobInfo.Status != jobStatusCompleted {
		t.Errorf("expected status %q, got %q", jobStatusCompleted, jobInfo.Status)
	}

	if jobInfo.Progress != 100 {
		t.Errorf("expected progress 100, got %d", jobInfo.Progress)
	}
}

func TestFujitsuHasNoConsole(t *testing.T) {
	c, _ := NewFujitsuClient(ClientOptions{})

	wrapped := &Client{ClientInterface: c}
	if wrapped.HasConsole() {
		t.Error("expected HasConsole to return false for Fujitsu")
	}
}
