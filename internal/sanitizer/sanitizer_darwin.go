// SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package sanitizer

import (
	"context"
	"errors"
)

func cleanDisks(_ context.Context, _ Config) ([]DiskResult, error) {
	return nil, errors.New("disk cleaning is only supported on Linux")
}
