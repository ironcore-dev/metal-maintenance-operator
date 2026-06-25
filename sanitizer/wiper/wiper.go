// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package wiper

import (
	"context"
	"time"

	"github.com/u-root/u-root/pkg/mount/block"
)

type Wiper interface {
	Name() string
	Wipe(ctx context.Context, disk *block.BlockDev) error
	Supports(disk *block.BlockDev) bool
	Deadline(disk *block.BlockDev) (time.Duration, error)
}

func Pick(d *block.BlockDev, wipers []Wiper) Wiper {
	for _, w := range wipers {
		if w.Supports(d) {
			return w
		}
	}
	return nil
}
