// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package wiper

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
	"unsafe"

	"github.com/u-root/u-root/pkg/mount/block"
	"golang.org/x/sys/unix"
)

const (
	BLKDISCARD    = 0x1277
	BLKSECDISCARD = 0x127d
	//BLKZEROOUT    = 0x127f
)

var ErrNotSupported = errors.New("wiper: device does not support this operation")

type BlkDiscard struct {
	PreferSecure bool
}

func (b *BlkDiscard) Name() string {
	return "blkdiscard"
}

func (b *BlkDiscard) Supports(d *block.BlockDev) bool {
	f, err := os.OpenFile(d.DevicePath(), os.O_RDONLY, 0)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()

	// Zero-length probe: kernels supporting BLKDISCARD return 0 or EINVAL on a
	// zero-length range; unsupported devices return EOPNOTSUPP/ENOTTY.
	rng := [2]uint64{0, 0}
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(),
		uintptr(BLKDISCARD), uintptr(unsafe.Pointer(&rng[0])))
	return errno == 0 || errors.Is(errno, unix.EINVAL)
}

func (b *BlkDiscard) Deadline(d *block.BlockDev) (time.Duration, error) {
	sizeBytes, err := d.Size()
	if err != nil {
		return 0, err
	}

	// ~1 GB/s optimistic; bound below by 5 min
	secs := sizeBytes / (1 << 30)
	if secs < 300 {
		secs = 300
	}
	return time.Duration(secs) * time.Second, nil
}

func (b *BlkDiscard) Wipe(ctx context.Context, d *block.BlockDev) error {
	f, err := os.OpenFile(d.DevicePath(), os.O_RDWR|unix.O_EXCL, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", d.DevicePath(), err)
	}
	defer func() { _ = f.Close() }()

	sizeBytes, err := d.Size()
	if err != nil {
		return fmt.Errorf("get device size: %w", err)
	}

	ops := []struct {
		op     uintptr
		secure bool
	}{{BLKDISCARD, false}}
	if b.PreferSecure {
		ops = []struct {
			op     uintptr
			secure bool
		}{{BLKSECDISCARD, true}, {BLKDISCARD, false}}
	}

	var lastErr error
	for _, o := range ops {
		err := ioctlDiscard(f, o.op, 0, sizeBytes)
		if err == nil {
			return nil
		}
		if !errors.Is(err, unix.EOPNOTSUPP) && !errors.Is(err, unix.ENOTTY) {
			return fmt.Errorf("blkdiscard(secure=%t): %w", o.secure, err)
		}
		lastErr = err
	}
	return fmt.Errorf("blkdiscard: %w (last errno: %v)", ErrNotSupported, lastErr)
}

func ioctlDiscard(f *os.File, op uintptr, offset, length uint64) error {
	rng := [2]uint64{offset, length}

	_, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(), op,
		uintptr(unsafe.Pointer(&rng[0])))
	if errno != 0 {
		return errno
	}
	return nil
}
