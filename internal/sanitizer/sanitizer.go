// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package sanitizer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const markerFile = "/var/run/metal-sanitizer/disk-cleaning-complete"

// Run orchestrates disk cleaning. It returns a Status on success or error.
// If the marker file already exists, it returns early with a success status.
func Run(ctx context.Context, cfg Config) (*Status, error) {
	startedAt := time.Now()

	if _, err := os.Stat(markerFile); err == nil {
		status := &Status{
			StartedAt:   startedAt,
			CompletedAt: time.Now(),
			Mode:        cfg.Mode,
			Disks:       nil,
			Result:      "skipped: already completed",
		}
		if err := writeStatus(cfg.StatusFile, status); err != nil {
			return status, fmt.Errorf("failed to write status file: %w", err)
		}
		return status, nil
	}

	results, err := cleanDisks(ctx, cfg)

	completedAt := time.Now()
	result := "success"
	if err != nil {
		result = fmt.Sprintf("error: %v", err)
	}

	status := &Status{
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		Mode:        cfg.Mode,
		Disks:       results,
		Result:      result,
	}

	if writeErr := writeStatus(cfg.StatusFile, status); writeErr != nil {
		return status, fmt.Errorf("failed to write status file: %w", writeErr)
	}

	if err != nil {
		return status, err
	}

	if markErr := writeMarker(); markErr != nil {
		return status, fmt.Errorf("failed to write marker file: %w", markErr)
	}

	return status, nil
}

func writeStatus(path string, status *Status) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("failed to create status file directory: %w", err)
	}
	data, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal status: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

func writeMarker() error {
	if err := os.MkdirAll(filepath.Dir(markerFile), 0755); err != nil {
		return fmt.Errorf("failed to create marker directory: %w", err)
	}
	content := fmt.Sprintf("Disk cleaning completed at %s\n", time.Now().Format(time.RFC3339))
	return os.WriteFile(markerFile, []byte(content), 0644)
}
