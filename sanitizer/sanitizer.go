// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package sanitizer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/ironcore-dev/sanitizer/errgroup"
	"github.com/ironcore-dev/sanitizer/wiper"
	"github.com/u-root/u-root/pkg/mount/block"
)

type Sanitizer struct {
	wipers      []wiper.Wiper
	parallelism *int
}

func New(wipers []wiper.Wiper) *Sanitizer {
	return &Sanitizer{wipers: wipers}
}

type Transport int

const (
	TransportUnknown Transport = iota
	TransportNVMe
	TransportSATA
	TransportVirtio
	TransportMMC
)

func isPartition(bd *block.BlockDev) bool {
	_, err := os.Stat("/sys/class/block/" + bd.Name + "/partition")
	return err == nil
}

func isMounted(disk *block.BlockDev) (bool, error) {
	_, err := block.GetMountpointByDevice(disk.DevicePath())
	if err != nil {
		if !strings.Contains(err.Error(), "mountpoint not found") {
			return false, fmt.Errorf("checking whether %s is mounted: %w", disk.DevicePath(), err)
		}
		return false, nil
	}
	return true, nil
}

func classify(name string) Transport {
	switch {
	case strings.HasPrefix(name, "nvme"):
		return TransportNVMe
	case strings.HasPrefix(name, "sd"):
		return TransportSATA
	case strings.HasPrefix(name, "vd"):
		return TransportVirtio
	case strings.HasPrefix(name, "mmcblk"):
		return TransportMMC
	}
	return TransportUnknown
}

func (s *Sanitizer) enumerate() (block.BlockDevices, error) {
	disks, err := block.GetBlockDevices()
	if err != nil {
		return nil, fmt.Errorf("getting block devices: %w", err)
	}

	disks = disks.FilterZeroSize()

	var filtered block.BlockDevices
	for _, disk := range disks {
		mounted, err := isMounted(disk)
		if err != nil {
			return nil, err
		}
		if mounted {
			continue
		}

		if isPartition(disk) {
			continue
		}

		if classify(disk.Name) == TransportUnknown {
			continue
		}

		filtered = append(filtered, disk)
	}
	return filtered, nil
}

func (s *Sanitizer) Sanitize(ctx context.Context) error {
	slog.Info("Enumerating block devices")
	disks, err := s.enumerate()
	if err != nil {
		return fmt.Errorf("enumerating block devices: %w", err)
	}

	grp, grpCtx := errgroup.New(ctx)
	if s.parallelism != nil {
		grp.SetLimit(*s.parallelism)
	}

	err = func() error {
		for _, disk := range disks {
			w := wiper.Pick(disk, s.wipers)
			if w == nil {
				return fmt.Errorf("[disk %s] no supported wiper found", disk)
			}

			deadline, err := w.Deadline(disk)
			if err != nil {
				return fmt.Errorf("[wiper %s disk %s] deadline for : %w", w.Name(), disk, err)
			}

			if !grp.GoContext(grpCtx, func() error {
				wipeCtx, cancel := context.WithTimeout(grpCtx, deadline)
				defer cancel()
				slog.Info("Starting wipe", "Wiper", w.Name(), "Disk", disk, "Timeout", deadline)
				if err := w.Wipe(wipeCtx, disk); err != nil {
					return fmt.Errorf("[wiper %s disk %s] wipe: %w", w.Name(), disk, err)
				}
				return nil
			}) {
				return nil
			}
		}
		return nil
	}()
	return errors.Join(err, grp.Wait())
}
