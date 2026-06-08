// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package sanitizer_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/sanitizer"
)

// TestModeValues verifies the Mode constants have the expected string values.
func TestModeValues(t *testing.T) {
	if sanitizer.ModeQuick != "quick" {
		t.Errorf("ModeQuick = %q, want %q", sanitizer.ModeQuick, "quick")
	}
	if sanitizer.ModeSecure != "secure" {
		t.Errorf("ModeSecure = %q, want %q", sanitizer.ModeSecure, "secure")
	}
}

// TestStatusJSONRoundTrip verifies Status serialises and deserialises correctly.
func TestStatusJSONRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Second) // truncate for JSON round-trip fidelity
	orig := sanitizer.Status{
		StartedAt:   now,
		CompletedAt: now.Add(5 * time.Second),
		Mode:        sanitizer.ModeQuick,
		Disks: []sanitizer.DiskResult{
			{
				Device:    "/dev/sda",
				Started:   now,
				Completed: now.Add(2 * time.Second),
				Method:    "wipefs",
				Err:       "",
			},
		},
		Result: "success",
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var got sanitizer.Status
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if got.Mode != orig.Mode {
		t.Errorf("Mode: got %q, want %q", got.Mode, orig.Mode)
	}
	if got.Result != orig.Result {
		t.Errorf("Result: got %q, want %q", got.Result, orig.Result)
	}
	if len(got.Disks) != 1 {
		t.Fatalf("Disks length: got %d, want 1", len(got.Disks))
	}
	if got.Disks[0].Method != "wipefs" {
		t.Errorf("Disk[0].Method: got %q, want %q", got.Disks[0].Method, "wipefs")
	}
}

// TestRunMarkerEarlyReturn checks that Run returns early when the marker file exists.
func TestRunMarkerEarlyReturn(t *testing.T) {
	dir := t.TempDir()

	// Write a fake marker file at the well-known path by patching via env is not possible
	// for a hardcoded const, so we test the observable effect: if Run is given an already-
	// completed marker we get back a skipped result. We simulate this by writing the marker
	// to the real path (only works if the test has write access, e.g. in CI as root or if
	// the marker dir can be created). If not, we skip gracefully.
	const markerDir = "/var/run/metal-sanitizer"
	markerPath := filepath.Join(markerDir, "disk-cleaning-complete")

	if err := os.MkdirAll(markerDir, 0755); err != nil {
		t.Skipf("Skipping marker test: cannot create %s: %v", markerDir, err)
	}
	if err := os.WriteFile(markerPath, []byte("test\n"), 0644); err != nil {
		t.Skipf("Skipping marker test: cannot write to %s: %v", markerPath, err)
	}
	t.Cleanup(func() { _ = os.Remove(markerPath) })

	statusFile := filepath.Join(dir, "status.json")
	cfg := sanitizer.Config{
		Mode:       sanitizer.ModeQuick,
		StatusFile: statusFile,
	}

	status, err := sanitizer.Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if status == nil {
		t.Fatal("Run returned nil status")
	}
	if status.Result != "skipped: already completed" {
		t.Errorf("Result: got %q, want %q", status.Result, "skipped: already completed")
	}

	// Status file should have been written.
	data, err := os.ReadFile(statusFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var written sanitizer.Status
	if err := json.Unmarshal(data, &written); err != nil {
		t.Fatalf("json.Unmarshal from status file: %v", err)
	}
	if written.Mode != sanitizer.ModeQuick {
		t.Errorf("Written Mode: got %q, want %q", written.Mode, sanitizer.ModeQuick)
	}
}

// TestConfigDefaults verifies that Config zero values are sensible.
func TestConfigDefaults(t *testing.T) {
	cfg := sanitizer.Config{}
	if cfg.MaxConcurrent != 0 {
		t.Errorf("expected default MaxConcurrent 0, got %d", cfg.MaxConcurrent)
	}
	if cfg.DryRun {
		t.Error("expected DryRun default false")
	}
}
