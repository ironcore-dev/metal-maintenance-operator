// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package probe

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/registry"
	"github.com/jaypipes/ghw"
	"github.com/jaypipes/ghw/pkg/block"
)

func collectStorageInfoData() ([]registry.BlockDevice, error) {
	blockDevices := make([]registry.BlockDevice, 0)
	blockStorage, err := ghw.Block()
	if err != nil {
		return blockDevices, fmt.Errorf("failed to get block devices: %w", err)
	}
	return collectStorageDevices(blockStorage.Disks, "/sys/class/block")
}

func collectStorageDevices(disks []*block.Disk, sysfsRoot string) ([]registry.BlockDevice, error) {
	blockDevices := make([]registry.BlockDevice, 0, len(disks))
	var errs []error
	for _, b := range disks {
		rota, err := ToBool(filepath.Join(sysfsRoot, b.Name, "queue/rotational"))
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to read rotational state for %s: %w", b.Name, err))
			continue
		}
		ro, err := ToBool(filepath.Join(sysfsRoot, b.Name, "ro"))
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to read readonly state for %s: %w", b.Name, err))
			continue
		}
		lbsz, err := ToInt(filepath.Join(sysfsRoot, b.Name, "queue/logical_block_size"))
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to read logical block size for %s: %w", b.Name, err))
			continue
		}
		secsz, err := ToInt(filepath.Join(sysfsRoot, b.Name, "queue/hw_sector_size"))
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to read hardware sector size for %s: %w", b.Name, err))
			continue
		}
		blockDevices = append(blockDevices, registry.BlockDevice{
			Path:              b.BusPath,
			Name:              b.Name,
			Rotational:        rota,
			Removable:         b.IsRemovable,
			ReadOnly:          ro,
			Vendor:            b.Vendor,
			Model:             b.Model,
			Serial:            b.SerialNumber,
			WWID:              b.WWN,
			PhysicalBlockSize: int64(b.PhysicalBlockSizeBytes),
			LogicalBlockSize:  int64(lbsz),
			HWSectorSize:      int64(secsz),
			SizeBytes:         int64(b.SizeBytes),
			NUMANodeID:        int32(b.NUMANodeID),
		})
	}
	return blockDevices, errors.Join(errs...)
}

func ToString(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("unable to read file %s: %w", path, err)
	}

	contentsString := string(contents)
	trimmedString := strings.TrimSpace(contentsString)

	return trimmedString, nil
}

func ToInt(path string) (int, error) {
	fileString, err := ToString(path)
	if err != nil {
		return 0, fmt.Errorf("unable to read string from file %s: %w", path, err)
	}

	num, err := strconv.Atoi(fileString)
	if err != nil {
		return 0, fmt.Errorf("unable to convert %s file to int: %w", fileString, err)
	}

	return num, nil
}

func ToBool(path string) (bool, error) {
	num, err := ToInt(path)
	if err != nil {
		return false, fmt.Errorf("unable to read int from file %s: %w", path, err)
	}

	return num == 1, nil
}
