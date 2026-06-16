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
	"text/template"

	"github.com/spf13/cobra"
)

type UseOptions struct {
	Architecture  string
	InstalledOnly bool
	Format        string
}

func (o *UseOptions) AddFlags(cmd *cobra.Command) {
	cmd.Flags().StringVarP(&o.Architecture, "arch", "a", o.Architecture, "Architecture of the kernel")
	cmd.Flags().StringVarP(&o.Format, "format", "f", o.Format, "Format to print in.")
	cmd.Flags().BoolVarP(&o.InstalledOnly, "installed-only", "i", o.InstalledOnly, "Only look at installed kernels.")
}

func newUseCmd(
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
		Use:   "use VERSION",
		Short: "Get information for a kernel image, downloading it if necessary.",
		Long:  "Get information for a kernel image, downloading it if necessary.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			localRepo := newLocalRepo()
			remoteRepo := newRemoteRepo()
			version := args[0]
			return runUse(ctx, localRepo, remoteRepo, version, opts)
		},
	}
	opts.AddFlags(cmd)
	return cmd
}

func runUse(ctx context.Context, localRepo *local.Repository, repo kernel.Reader, version string, opts UseOptions) error {
	key := kernel.Key{Architecture: opts.Architecture, Version: version}
	exists, err := localRepo.Exists(ctx, key)
	if err != nil {
		return fmt.Errorf("check existence: %w", err)
	}
	if !exists {
		if opts.InstalledOnly {
			return fmt.Errorf("kernel %s not found", key)
		}
		if err := kernel.Copy(ctx, key, repo, localRepo); err != nil {
			return fmt.Errorf("copy kernel: %w", err)
		}
	}

	switch {
	case opts.Format == FormatTable:
		PrintTable(func(yield func(KernelInfo) bool) { yield(KernelInfo{Key: key, Installed: true}) })
	case opts.Format == FormatPath:
		p := localRepo.KernelDir(key)
		fmt.Print(p)
	case opts.Format == FormatJSON:
		k, err := localRepo.Inspect(ctx, key)
		if err != nil {
			return fmt.Errorf("inspect kernel: %w", err)
		}

		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(k)
		return nil
	case IsTemplate(opts.Format):
		t, err := template.New("").Funcs(template.FuncMap{
			"KernelPath": func() string {
				return localRepo.KernelDir(key)
			},
			"ModulePath": func(module string) (string, error) {
				return localRepo.ModuleFilename(key, module)
			},
		}).Parse(opts.Format)
		if err != nil {
			return fmt.Errorf("parse format template: %w", err)
		}

		k, err := localRepo.Inspect(ctx, key)
		if err != nil {
			return fmt.Errorf("inspect kernel: %w", err)
		}

		return t.Execute(os.Stdout, struct{ Kernel *kernel.Kernel }{
			Kernel: k,
		})
	default:
		return fmt.Errorf("unknown format: %v", opts.Format)
	}

	return nil
}
