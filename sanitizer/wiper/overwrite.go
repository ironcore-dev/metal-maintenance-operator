// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package wiper

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/u-root/u-root/pkg/mount/block"
	"golang.org/x/sys/unix"
)

type Overwrite struct{}

func (o *Overwrite) Name() string                    { return "overwrite-random" }
func (o *Overwrite) Supports(d *block.BlockDev) bool { return true }
func (o *Overwrite) Deadline(d *block.BlockDev) (time.Duration, error) {
	sizeBytes, err := d.Size()
	if err != nil {
		return 0, fmt.Errorf("get device size: %w", err)
	}

	// Assume 100 MB/s worst case + 50% slack, with a 5-minute floor.
	duration := time.Duration(sizeBytes) * 3 * time.Second / (200 << 20)
	if duration < 5*time.Minute {
		duration = 5 * time.Minute
	}
	return duration, nil
}

func (o *Overwrite) Wipe(ctx context.Context, d *block.BlockDev) error {
	f, err := os.OpenFile(d.DevicePath(), os.O_WRONLY|unix.O_EXCL|unix.O_DIRECT, 0)
	if err != nil {
		// Fall back without O_DIRECT if filesystem doesn't like it.
		f, err = os.OpenFile(d.DevicePath(), os.O_WRONLY|unix.O_EXCL, 0)
		if err != nil {
			return err
		}
	}
	defer func() { _ = f.Close() }()

	sizeBytes, err := d.Size()
	if err != nil {
		return fmt.Errorf("get device size: %w", err)
	}

	// Seed an AES-CTR keystream from crypto/rand so the disk becomes the
	// bottleneck again — crypto/rand tops out around a few hundred MB/s,
	// while AES-NI gives several GB/s.
	key := make([]byte, 32)
	iv := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return fmt.Errorf("seed key: %w", err)
	}
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return fmt.Errorf("seed iv: %w", err)
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("new cipher: %w", err)
	}
	stream := cipher.NewCTR(blk, iv)

	const bufSize = 4 << 20 // 4 MiB
	zero := make([]byte, bufSize)
	buf := make([]byte, bufSize)
	var written uint64
	for written < sizeBytes {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		n := bufSize
		if remaining := sizeBytes - written; remaining < bufSize {
			n = int(remaining)
		}
		stream.XORKeyStream(buf[:n], zero[:n])
		if _, err := f.Write(buf[:n]); err != nil {
			return err
		}
		written += uint64(n)
	}
	return f.Sync() // fsync the device
}
