// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package probe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jaypipes/ghw/pkg/block"
)

func TestCollectStorageDevicesKeepsReadableDisks(t *testing.T) {
	files := map[string]string{
		"queue/rotational": "1", "ro": "0",
		"queue/logical_block_size": "512", "queue/hw_sector_size": "4096",
	}
	for missing := range files {
		t.Run(missing, func(t *testing.T) {
			root := t.TempDir()
			disks := []*block.Disk{{Name: "first"}, {Name: "unreadable"}, {Name: "last"}}
			for _, disk := range disks {
				for name, value := range files {
					if disk.Name == "unreadable" && name == missing {
						continue
					}
					path := filepath.Join(root, disk.Name, name)
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := collectStorageDevices(disks, root)
			if err == nil || !strings.Contains(err.Error(), "unreadable") {
				t.Fatalf("expected unreadable disk error, got %v", err)
			}
			if len(got) != 2 || got[0].Name != "first" || got[1].Name != "last" {
				t.Fatalf("expected first and last disks, got %+v", got)
			}
			for _, disk := range got {
				if !disk.Rotational || disk.ReadOnly || disk.LogicalBlockSize != 512 || disk.HWSectorSize != 4096 {
					t.Fatalf("incorrect disk data: %+v", disk)
				}
			}
		})
	}
}
