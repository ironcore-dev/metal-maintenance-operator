// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package sanitizer

import (
	"testing"
)

func TestValidateDevicePath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{name: "invalid format - no /dev/ prefix", path: "/sys/block/sda", wantErr: true},
		{name: "invalid format - relative path", path: "sda", wantErr: true},
		{name: "invalid format - shell injection", path: "/dev/sda; rm -rf /", wantErr: true},
		{name: "invalid format - empty", path: "", wantErr: true},
		// Paths with valid format but non-existent on this host will fail stat.
		{name: "non-existent device", path: "/dev/sdnonexistent999", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDevicePath(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateDevicePath(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestIsRotationalUnknownDevice(t *testing.T) {
	// An unknown device name that has no sysfs entry should default to rotational=true.
	if !isRotational("sdnonexistent999") {
		t.Error("isRotational: expected true (safe default) for unknown device")
	}
}
