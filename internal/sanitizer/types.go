// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package sanitizer

import "time"

// Mode defines the disk cleaning strategy.
type Mode string

const (
	ModeQuick  Mode = "quick"
	ModeSecure Mode = "secure"
)

// Config holds configuration for a sanitizer run.
type Config struct {
	Mode          Mode
	StatusFile    string
	Devices       []string
	MaxConcurrent int
	DryRun        bool
}

// DiskResult captures the outcome of cleaning a single disk.
type DiskResult struct {
	Device    string    `json:"device"`
	Started   time.Time `json:"started"`
	Completed time.Time `json:"completed"`
	Method    string    `json:"method"`
	Err       string    `json:"error,omitempty"`
}

// Status is the top-level status document written to the status file.
type Status struct {
	StartedAt   time.Time    `json:"startedAt"`
	CompletedAt time.Time    `json:"completedAt"`
	Mode        Mode         `json:"mode"`
	Disks       []DiskResult `json:"disks"`
	Result      string       `json:"result"`
}
