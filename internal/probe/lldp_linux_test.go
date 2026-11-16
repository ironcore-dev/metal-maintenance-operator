// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package probe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCollectLLDPInfoPolling(t *testing.T) {
	for _, tt := range []struct {
		name, firstOutput string
		populated         bool
	}{
		{"empty then populated", `printf '%s\n' '{"lldp":{}}'`, true},
		{"command failure then populated", "exit 1", true},
		{"parse failure then populated", "printf '%s\\n' 'invalid JSON'", true},
		{"empty until timeout", `printf '%s\n' '{"lldp":{}}'`, false},
		{"command failure until timeout", "exit 1", false},
		{"parse failure until timeout", "printf '%s\\n' 'invalid JSON'", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("PATH", dir)
			t.Setenv("LLDP_TEST_MARKER", filepath.Join(dir, "attempted"))
			script := "#!/bin/sh\n" + tt.firstOutput + "\n"
			if tt.populated {
				script = `#!/bin/sh
if [ ! -f "$LLDP_TEST_MARKER" ]; then
  : > "$LLDP_TEST_MARKER"
  ` + tt.firstOutput + `
else
  printf '%s\n' '{"lldp":{"interface":{"eth0":{"chassis":{"switch":{"id":{"value":"switch-id"}}}}}}}'
fi
`
			}
			if err := os.WriteFile(filepath.Join(dir, "lldpctl"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			lldp, err := collectLLDPInfo(context.Background(), 10*time.Millisecond, time.Second)
			if tt.populated {
				if err != nil || len(lldp.Interfaces) != 1 || lldp.Interfaces[0].Name != "eth0" {
					t.Fatalf("expected populated LLDP after retry, got %+v, %v", lldp, err)
				}
			} else if !errors.Is(err, context.DeadlineExceeded) || len(lldp.Interfaces) != 0 {
				t.Fatalf("expected empty LLDP and timeout error, got %+v, %v", lldp, err)
			}
		})
	}
}
