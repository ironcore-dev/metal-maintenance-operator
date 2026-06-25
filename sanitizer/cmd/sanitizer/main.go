// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/ironcore-dev/sanitizer"
	"github.com/ironcore-dev/sanitizer/config"
	"github.com/ironcore-dev/sanitizer/reporter"
	"github.com/ironcore-dev/sanitizer/wiper"
	"github.com/u-root/u-root/pkg/cmdline"
	"github.com/u-root/u-root/pkg/dhclient"
)

type Options struct {
	ReportURL   string
	IgnitionURL string
}

func dhConfigure(ctx context.Context) error {
	ifs, err := dhclient.Interfaces("^e.*")
	if err != nil {
		allIfaces, _ := dhclient.Interfaces(".*")
		allIfaceNames := make([]string, len(allIfaces))
		for _, iface := range allIfaces {
			allIfaceNames = append(allIfaceNames, fmt.Sprintf("%#+v", iface.Attrs()))
		}
		return fmt.Errorf("getting interfaces: %w (available interfaces %v)", err, allIfaceNames)
	}

	var (
		errs       []error
		done       = make(chan struct{})
		closeOnce  sync.Once
		notifyDone = func() {
			closeOnce.Do(func() { close(done) })
		}
	)
	go func() {
		defer notifyDone()

		for res := range dhclient.SendRequests(ctx, ifs, true, true, dhclient.Config{
			Timeout: 15 * time.Second,
			Retries: 5,
			V4ServerAddr: &net.UDPAddr{
				IP:   net.IPv4bcast,
				Port: dhcpv4.ServerPort,
			},
			V6ServerAddr: &net.UDPAddr{
				IP:   net.ParseIP("ff02::1:2"),
				Port: dhcpv6.DefaultServerPort,
			},
		}, 30*time.Second) {
			if res.Err != nil {
				errs = append(errs, res.Err)
				continue
			}

			if err := res.Lease.Configure(); err != nil {
				errs = append(errs, err)
				continue
			}

			notifyDone()
		}
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return errors.Join(errs...)
	}
}

func run(ctx context.Context, opts Options) error {
	if opts.IgnitionURL != "" || opts.ReportURL != "" {
		if err := dhConfigure(ctx); err != nil {
			return fmt.Errorf("dhconfigure: %w", err)
		}
	}

	reportURL := opts.ReportURL

	if opts.IgnitionURL != "" {
		cfg, err := config.FetchViaIgnition(ctx, opts.IgnitionURL)
		if err != nil {
			return fmt.Errorf("fetch config via ignition url %s: %w", opts.IgnitionURL, err)
		}

		reportURL = cfg.ReportURL
	}

	s := sanitizer.New([]wiper.Wiper{
		&wiper.BlkDiscard{PreferSecure: true},
		&wiper.Overwrite{},
	})
	err := s.Sanitize(ctx)
	if reportURL != "" {
		r := reporter.New(reportURL)
		if reportErr := r.ReportResult(ctx, err); reportErr != nil {
			err = errors.Join(err, reportErr)
		}
	}
	return err
}

func main() {
	reportURL, _ := cmdline.Flag("sanitizer.report.url")
	ignitionURL, _ := cmdline.Flag("ignition.config.url")

	if err := run(context.Background(), Options{
		IgnitionURL: ignitionURL,
		ReportURL:   reportURL,
	}); err != nil {
		slog.Error("Error sanitizing", "error", err)
	} else {
		slog.Info("Sanitization successful")
	}
	park()
}

// park blocks forever as PID 1 in the sanitizer initramfs. The sanitizer has
// already POSTed its result to the manager; returning here would terminate
// PID 1 and trigger a kernel panic before the manager can act on the report.
// Bringing the server out of park (via reboot) is the controller's job.
func park() {
	select {}
}
