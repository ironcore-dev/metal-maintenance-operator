// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// Package cmd implements the kernel CLI.
package cmd

import (
	"kernel/internal/debian"
	"kernel/internal/kernel"
	"kernel/internal/local"
	"net/http"

	"github.com/spf13/cobra"
)

func NewRootCmd() *cobra.Command {
	var (
		debianArchiveBaseURL = debian.DefaultArchiveBaseURL
		dir                  string
	)

	root := &cobra.Command{
		Use:           "kernel",
		Short:         "Fetch and inspect Linux kernel images from the Debian archive",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&debianArchiveBaseURL, "debian-base-url", debianArchiveBaseURL, "Debian base URL")
	root.PersistentFlags().StringVarP(&dir, "dir", "d", ".", "Directory for local storage.")

	newLocalRepo := func() *local.Repository {
		return local.NewRepository(dir)
	}

	newRemoteRepo := func() kernel.Reader {
		return debian.NewRepository(debianArchiveBaseURL, http.DefaultClient)
	}

	root.AddCommand(
		newUseCmd(newLocalRepo, newRemoteRepo),
		newListCmd(newLocalRepo, newRemoteRepo),
		newInspectCmd(newLocalRepo, newRemoteRepo),
	)
	return root
}
