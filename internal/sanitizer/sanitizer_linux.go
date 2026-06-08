// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package sanitizer

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jaypipes/ghw"
	"golang.org/x/sync/errgroup"
)

// devicePathRegex validates that device paths match expected format.
// Supports multipath/RAID devices (e.g., /dev/mapper/mpatha, /dev/cciss/c0d0).
var devicePathRegex = regexp.MustCompile(`^/dev/[a-zA-Z0-9\-_/]+$`)

// validateDevicePath ensures a device path is safe and refers to a block device.
func validateDevicePath(devicePath string) error {
	if !devicePathRegex.MatchString(devicePath) {
		return fmt.Errorf("invalid device path format: %s", devicePath)
	}

	fi, err := os.Stat(devicePath)
	if err != nil {
		return fmt.Errorf("device path does not exist: %w", err)
	}

	if fi.Mode()&os.ModeDevice == 0 {
		return fmt.Errorf("path is not a device: %s", devicePath)
	}
	if fi.Mode()&os.ModeCharDevice != 0 {
		return fmt.Errorf("path is a character device, not a block device: %s", devicePath)
	}

	return nil
}

// cleanDisks enumerates and cleans eligible disks concurrently.
func cleanDisks(ctx context.Context, cfg Config) ([]DiskResult, error) {
	log := slog.Default()

	blockStorage, err := ghw.Block()
	if err != nil {
		return nil, fmt.Errorf("failed to enumerate block devices: %w", err)
	}

	if len(blockStorage.Disks) == 0 {
		log.InfoContext(ctx, "No disks found to clean")
		return nil, nil
	}

	// Build an allowlist if specific devices were requested.
	allowed := make(map[string]bool, len(cfg.Devices))
	for _, d := range cfg.Devices {
		allowed[filepath.Base(d)] = true
	}

	var eligibleDisks []*ghw.Disk
	for _, disk := range blockStorage.Disks {
		if len(allowed) > 0 && !allowed[disk.Name] {
			continue
		}

		ro, err := isReadOnly(disk.Name)
		if err != nil {
			log.WarnContext(ctx, "Failed to check read-only status, skipping disk", "disk", disk.Name, "error", err)
			continue
		}
		if ro {
			log.InfoContext(ctx, "Skipping read-only disk", "disk", disk.Name)
			continue
		}

		if disk.IsRemovable {
			log.InfoContext(ctx, "Skipping removable disk", "disk", disk.Name)
			continue
		}

		devicePath := "/dev/" + disk.Name
		if _, err := os.Stat(devicePath); err != nil {
			log.WarnContext(ctx, "Device path does not exist, skipping", "disk", disk.Name, "path", devicePath)
			continue
		}

		eligibleDisks = append(eligibleDisks, disk)
	}

	if len(eligibleDisks) == 0 {
		log.InfoContext(ctx, "No eligible disks to clean")
		return nil, nil
	}

	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = 4
	}

	log.InfoContext(ctx, "Starting concurrent disk cleaning",
		"mode", cfg.Mode, "eligible", len(eligibleDisks), "maxConcurrent", maxConcurrent)

	results := make([]DiskResult, len(eligibleDisks))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrent)

	for i, disk := range eligibleDisks {
		g.Go(func() error {
			result := cleanOne(gctx, disk, cfg)
			results[i] = result
			if result.Err != "" {
				return fmt.Errorf("disk %s: %s", disk.Name, result.Err)
			}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return results, fmt.Errorf("disk cleaning failed: %w", err)
	}

	return results, nil
}

// cleanOne cleans a single disk, returning a DiskResult.
func cleanOne(ctx context.Context, disk *ghw.Disk, cfg Config) DiskResult {
	log := slog.Default()
	devicePath := "/dev/" + disk.Name

	result := DiskResult{
		Device:  devicePath,
		Started: time.Now(),
	}

	if cfg.DryRun {
		log.InfoContext(ctx, "Dry-run: would clean disk",
			"disk", disk.Name, "mode", cfg.Mode)
		result.Method = "dry-run"
		result.Completed = time.Now()
		return result
	}

	log.InfoContext(ctx, "Cleaning disk",
		"disk", disk.Name, "model", disk.Model, "size", disk.SizeBytes, "mode", cfg.Mode)

	var method string
	var err error

	switch cfg.Mode {
	case ModeQuick:
		method, err = quickCleanDisk(ctx, disk.Name, devicePath)
	case ModeSecure:
		method, err = secureCleanDisk(ctx, disk.Name, devicePath)
	default:
		err = fmt.Errorf("unknown mode: %s", cfg.Mode)
	}

	result.Completed = time.Now()
	result.Method = method
	if err != nil {
		result.Err = err.Error()
		log.WarnContext(ctx, "Failed to clean disk", "disk", disk.Name, "error", err, "duration", result.Completed.Sub(result.Started))
	} else {
		log.InfoContext(ctx, "Successfully cleaned disk", "disk", disk.Name, "duration", result.Completed.Sub(result.Started))
	}

	return result
}

func quickCleanDisk(ctx context.Context, diskName, devicePath string) (string, error) {
	log := slog.Default()

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	if err := validateDevicePath(devicePath); err != nil {
		return "", err
	}

	log.DebugContext(ctx, "Using wipefs to remove filesystem signatures", "disk", diskName)
	cmd := exec.CommandContext(ctx, "wipefs", "-a", devicePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return "wipefs", fmt.Errorf("wipefs failed: %w, output: %s", err, string(output))
	}

	if err := rereadPartitionTable(ctx, devicePath); err != nil {
		log.WarnContext(ctx, "Failed to re-read partition table after wipefs", "disk", diskName, "error", err)
	}

	return "wipefs", nil
}

func secureCleanDisk(ctx context.Context, diskName, devicePath string) (string, error) {
	log := slog.Default()

	ctx, cancel := context.WithTimeout(ctx, 24*time.Hour)
	defer cancel()

	if err := validateDevicePath(devicePath); err != nil {
		return "", err
	}

	// Try blkdiscard for SSDs first.
	if !isRotational(diskName) {
		log.DebugContext(ctx, "Detected non-rotational flash storage, using blkdiscard", "disk", diskName)
		if err := executeBlkDiscard(ctx, devicePath); err != nil {
			log.WarnContext(ctx, "blkdiscard failed, falling back to dd", "disk", diskName, "error", err)
		} else {
			if err := rereadPartitionTable(ctx, devicePath); err != nil {
				log.WarnContext(ctx, "Failed to re-read partition table after blkdiscard", "disk", diskName, "error", err)
			}
			return "blkdiscard", nil
		}
	}

	log.DebugContext(ctx, "Using dd for secure wipe", "disk", diskName)
	cmd := exec.CommandContext(ctx, "dd",
		"if=/dev/urandom",
		"of="+devicePath,
		"bs=1M",
		"status=progress",
		"oflag=direct")

	output, err := cmd.CombinedOutput()
	if err != nil {
		outputStr := string(output)
		// "No space left on device" is expected when dd fills the disk.
		if !strings.Contains(outputStr, "No space left on device") {
			return "dd", fmt.Errorf("dd failed: %w, output: %s", err, outputStr)
		}
		log.DebugContext(ctx, "dd completed (expected 'No space left' at end)", "disk", diskName)
	}

	if err := rereadPartitionTable(ctx, devicePath); err != nil {
		log.WarnContext(ctx, "Failed to re-read partition table after dd", "disk", diskName, "error", err)
	}

	return "dd", nil
}

// isRotational checks sysfs to determine if the drive is a spinning HDD (true) or SSD/NVMe (false).
// For multipath/dm devices, checks underlying slaves. Defaults to true (HDD) if unknown for safety.
func isRotational(diskName string) bool {
	baseName := filepath.Base(diskName)

	// Check if this is a device-mapper/multipath device.
	dmPath := fmt.Sprintf("/sys/block/%s/dm/name", baseName)
	if _, err := os.Stat(dmPath); err == nil {
		slavesDir := fmt.Sprintf("/sys/block/%s/slaves", baseName)
		entries, err := os.ReadDir(slavesDir)
		if err == nil && len(entries) > 0 {
			slaveName := entries[0].Name()
			slavePath := fmt.Sprintf("/sys/block/%s/queue/rotational", slaveName)
			if data, err := os.ReadFile(slavePath); err == nil {
				return strings.TrimSpace(string(data)) == "1"
			}
		}
	}

	path := fmt.Sprintf("/sys/block/%s/queue/rotational", baseName)
	data, err := os.ReadFile(path)
	if err != nil {
		return true // assume rotational (HDD) for safety
	}

	return strings.TrimSpace(string(data)) == "1"
}

// executeBlkDiscard issues TRIM/UNMAP commands to securely erase flash cells.
func executeBlkDiscard(ctx context.Context, devicePath string) error {
	cmd := exec.CommandContext(ctx, "blkdiscard", "--secure", devicePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("blkdiscard failed: %w, output: %s", err, string(output))
	}
	return nil
}

// isReadOnly checks if a disk is read-only (hardware write-protected).
func isReadOnly(diskName string) (bool, error) {
	baseName := filepath.Base(diskName)
	roPath := fmt.Sprintf("/sys/class/block/%s/ro", baseName)
	data, err := os.ReadFile(roPath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to read ro sysfs attribute: %w", err)
	}
	return strings.TrimSpace(string(data)) == "1", nil
}

func rereadPartitionTable(ctx context.Context, devicePath string) error {
	cmd := exec.CommandContext(ctx, "blockdev", "--rereadpt", devicePath)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to re-read partition table: %w", err)
	}
	return nil
}
