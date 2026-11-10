// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package prometheus

import "time"

// SetNowFn replaces the clock function used by ActiveAlertTracker.
// For tests only.
func (t *ActiveAlertTracker) SetNowFn(fn func() time.Time) { t.nowFn = fn }

// ExpireStale runs one expiry pass synchronously.
// For tests only.
func (t *ActiveAlertTracker) ExpireStale() { t.expireStale() }
