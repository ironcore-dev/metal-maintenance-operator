// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
)

// subcommands maps each known subcommand to its handler. Add new cleaning
// operations (e.g. tpm, firmware, logs) by registering them here and dropping
// a sibling file with the runFoo(args) implementation.
var subcommands = map[string]func(args []string) int{
	"disks": runDisks,
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	switch cmd {
	case "-h", "--help", "help":
		usage()
		return
	}

	handler, ok := subcommands[cmd]
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown subcommand: %q\n\n", cmd)
		usage()
		os.Exit(1)
	}

	os.Exit(handler(os.Args[2:]))
}

func usage() {
	fmt.Fprintf(os.Stderr, `metal-sanitizer cleans server hardware between tenants.

Usage:
  metal-sanitizer <command> [flags]

Commands:
  disks    Wipe block devices (quick or secure mode)

Run 'metal-sanitizer <command> --help' for command-specific flags.
`)
}
