// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"kernel/internal/kernel"
	"kernel/internal/local"
	"os"
	"runtime"

	"github.com/spf13/cobra"
)

type InspectOptions struct {
	Architecture  string
	InstalledOnly bool
	Format        string
}

func (o *InspectOptions) AddFlags(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&o.Architecture, "arch", "a", o.Architecture, "Architecture of the kernel")
	cmd.Flags().StringVarP(&o.Format, "format", "f", o.Format, "Format to print.")
	cmd.Flags().BoolVarP(&o.InstalledOnly, "installed-only", "i", o.InstalledOnly, "Only look at installed kernels.")
}

func newInspectCmd(
	newLocalRepo func() *local.Repository,
	newRemoteRepo func() kernel.Reader,
) *cobra.Command {
	var (
		opts = UseOptions{
			Architecture: runtime.GOARCH,
			Format:       FormatTable,
		}
	)
	cmd := &cobra.Command{
		Use:   "inspect VERSION",
		Short: "Inspect a kernel",
		Long:  "Inspect a kernel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			localRepo := newLocalRepo()
			remoteRepo := newRemoteRepo()
			version := args[0]
			return runInspect(ctx, localRepo, remoteRepo, version, opts)
		},
	}
	opts.AddFlags(cmd)
	return cmd
}

func runInspect(ctx context.Context, localRepo *local.Repository, repo kernel.Reader, version string, opts UseOptions) error {
	key := kernel.Key{Architecture: opts.Architecture, Version: version}

	rd := kernel.Reader(localRepo)
	exists, err := localRepo.Exists(ctx, key)
	if err != nil {
		return fmt.Errorf("check existence: %w", err)
	}
	if !exists {
		rd = repo
	}

	k, err := rd.Inspect(ctx, key)
	if err != nil {
		return fmt.Errorf("inspect: %w", err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(k)
	return nil
}
