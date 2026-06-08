// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/sanitizer"
)

// runDisks executes the "disks" subcommand: wipe block devices using the
// metal-sanitizer disk-cleaning logic. Returns a process exit code.
func runDisks(args []string) int {
	fs := flag.NewFlagSet("disks", flag.ContinueOnError)

	var (
		mode          string
		statusFile    string
		maxConcurrent int
		dryRun        bool
		devices       stringSlice
	)

	fs.StringVar(&mode, "mode", "", "Cleaning mode: quick or secure (required)")
	fs.StringVar(&statusFile, "status-file", "", "Path to write status JSON (optional; empty disables)")
	fs.IntVar(&maxConcurrent, "max-concurrent", 4, "Maximum concurrent disk operations")
	fs.BoolVar(&dryRun, "dry-run", false, "Log what would happen without executing")
	fs.Var(&devices, "device", "Restrict to this device (repeatable; default: all eligible)")

	if err := fs.Parse(args); err != nil {
		// flag.ErrHelp is signalled when -h/--help is passed; that's a successful exit.
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		// flag already prints usage on parse errors
		return 1
	}

	log := slog.Default()

	if err := validateDisksFlags(mode); err != nil {
		fs.Usage()
		fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
		return 1
	}

	if err := checkDisksTools(); err != nil {
		log.Error("Required tools not found", "error", err)
		return 1
	}

	cfg := sanitizer.Config{
		Mode:          sanitizer.Mode(mode),
		StatusFile:    statusFile,
		Devices:       []string(devices),
		MaxConcurrent: maxConcurrent,
		DryRun:        dryRun,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info("Starting disk sanitization", "mode", mode, "dryRun", dryRun, "maxConcurrent", maxConcurrent)

	status, err := sanitizer.Run(ctx, cfg)
	if err != nil {
		log.Error("Disk sanitization failed", "error", err)
		if hasPartialFailures(status) {
			return 2
		}
		return 1
	}

	if status != nil {
		log.Info("Disk sanitization complete", "result", status.Result)
	}
	return 0
}

func validateDisksFlags(mode string) error {
	if mode == "" {
		return fmt.Errorf("--mode is required")
	}
	if mode != string(sanitizer.ModeQuick) && mode != string(sanitizer.ModeSecure) {
		return fmt.Errorf("--mode must be 'quick' or 'secure', got %q", mode)
	}
	return nil
}

func checkDisksTools() error {
	tools := []string{"wipefs", "blkdiscard", "dd", "blockdev"}
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("required tool %q not found in PATH: %w", tool, err)
		}
	}
	return nil
}

func hasPartialFailures(status *sanitizer.Status) bool {
	if status == nil {
		return false
	}
	for _, d := range status.Disks {
		if d.Err != "" {
			return true
		}
	}
	return false
}

// stringSlice is a flag.Value that collects repeated --device flags.
type stringSlice []string

func (s *stringSlice) String() string {
	return fmt.Sprintf("%v", []string(*s))
}

func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}
