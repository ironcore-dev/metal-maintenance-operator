// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"iter"
	"kernel/internal/kernel"
	"os"
	"strings"
	"text/tabwriter"
)

const (
	FormatTable = "table"
	FormatJSON  = "json"
	FormatPath  = "path"
)

func IsTemplate(format string) bool {
	format = strings.TrimSpace(format)
	return strings.HasPrefix(format, "{{") &&
		strings.HasSuffix(format, "}}")
}

type Print string

const (
	PrintOverview Print = "overview"
	PrintPath     Print = "path"
)

var Prints = []Print{
	PrintOverview,
	PrintPath,
}

type KernelInfo struct {
	Key       kernel.Key
	Installed bool
}

func PrintTable(infos iter.Seq[KernelInfo]) {
	w := tabwriter.NewWriter(os.Stdout, 12, 0, 1, ' ', 0)
	_, _ = fmt.Fprintln(w, "VERSION\tARCHITECTURE\tSTATUS")
	for info := range infos {
		indicator := "available"
		if info.Installed {
			indicator = "installed"
		}

		_, _ = fmt.Fprintln(w, fmt.Sprintf("%s\t%s\t%s", info.Key.Version, info.Key.Architecture, indicator))
	}
	_ = w.Flush()
}
