// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package probe

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/registry"
	"k8s.io/apimachinery/pkg/util/wait"
)

func collectLLDPInfo(ctx context.Context, interval, duration time.Duration) (registry.LLDP, error) {
	lldp := registry.LLDP{}
	var out bytes.Buffer
	tool, err := exec.LookPath("lldpctl")
	if err != nil {
		// TODO: exit or just skip?
		return lldp, fmt.Errorf("lldpctl is not present: %w", err)
	}

	var lastErr error
	if err := wait.PollUntilContextTimeout(ctx, interval, duration, true, func(ctx context.Context) (done bool, err error) {
		out.Reset()
		cmd := exec.CommandContext(ctx, tool, "-f", "json")
		cmd.Stdout = &out
		err = cmd.Run()
		if err != nil {
			lastErr = fmt.Errorf("running lldpctl encountered a problem: %w", err)
			return false, nil
		}
		if len(out.Bytes()) == 0 {
			return false, nil
		}
		parsed, err := registry.ParseLLDPCTL(out.Bytes())
		if err != nil {
			lastErr = fmt.Errorf("can't parse lldpctl output: %s: %w", out.String(), err)
			return false, nil
		}
		if len(parsed.Interfaces) == 0 {
			return false, nil
		}
		lldp = parsed
		return true, nil
	}); err != nil {
		return registry.LLDP{}, errors.Join(err, lastErr)
	}

	return lldp, nil
}
