// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"context"
	"fmt"
	"kernel/internal/kernel"
	"kernel/internal/local"
	"slices"

	"github.com/spf13/cobra"
)

type ListOptions struct {
	Architecture  string
	InstalledOnly bool
}

func (o *ListOptions) AddFlags(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&o.Architecture, "arch", "a", o.Architecture, "Filter kernel architecture.")
	cmd.Flags().BoolVarP(&o.InstalledOnly, "installed-only", "i", o.InstalledOnly, "Only look at installed kernels.")
}

func newListCmd(
	newLocalRepo func() *local.Repository,
	newRemoteRepo func() kernel.Reader,
) *cobra.Command {
	var (
		opts ListOptions
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List kernels available in the Debian archive",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			localRepo := newLocalRepo()
			remoteRepo := newRemoteRepo()

			return RunList(ctx, localRepo, remoteRepo, opts)
		},
	}
	opts.AddFlags(cmd)
	return cmd
}

func RunList(ctx context.Context, localRepo *local.Repository, remoteRepo kernel.Reader, opts ListOptions) error {
	var (
		kernelKeyToInstalled = make(map[kernel.Key]bool)
		kernelKeys           []kernel.Key
	)

	if !opts.InstalledOnly {
		remoteKernels, err := remoteRepo.List(ctx, kernel.ListOptions{Architecture: opts.Architecture})
		if err != nil {
			return fmt.Errorf("listing remote kernels: %w", err)
		}

		for _, key := range remoteKernels {
			kernelKeyToInstalled[key] = false
			kernelKeys = append(kernelKeys, key)
		}
	}

	localKernels, err := localRepo.List(ctx, kernel.ListOptions{Architecture: opts.Architecture})
	if err != nil {
		return fmt.Errorf("listing local kernels: %w", err)
	}

	for _, key := range localKernels {
		_, present := kernelKeyToInstalled[key]
		kernelKeyToInstalled[key] = true
		if !present {
			kernelKeys = append(kernelKeys, key)
		}
	}

	slices.SortFunc(kernelKeys, func(a, b kernel.Key) int {
		return -kernel.CompareKeys(a, b)
	})
	PrintTable(func(yield func(KernelInfo) bool) {
		for _, key := range kernelKeys {
			if !yield(KernelInfo{Key: key, Installed: kernelKeyToInstalled[key]}) {
				return
			}
		}
	})
	return nil
}
