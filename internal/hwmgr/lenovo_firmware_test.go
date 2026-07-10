// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package hwmgr

import "testing"

func TestClassifyLXCAStatus(t *testing.T) {
	cases := []struct {
		in   string
		want FirmwareJobStatus
	}{
		{"Complete", FirmwareJobStatusSuccess},
		{"Succeeded", FirmwareJobStatusSuccess},
		{"Failed", FirmwareJobStatusFailed},
		{"Aborted", FirmwareJobStatusFailed},
		{"Cancelled", FirmwareJobStatusFailed},
		{"Stopped", FirmwareJobStatusFailed},
		{"Warning", FirmwareJobStatusFailed},
		{"Running", FirmwareJobStatusInProgress},
		{"Pending", FirmwareJobStatusInProgress},
		{"", FirmwareJobStatusInProgress},
	}
	for _, tc := range cases {
		if got := ClassifyLXCAStatus(tc.in); got != tc.want {
			t.Errorf("ClassifyLXCAStatus(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
