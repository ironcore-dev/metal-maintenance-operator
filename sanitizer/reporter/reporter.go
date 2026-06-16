// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package reporter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

type Reporter struct {
	reportURL string
}

func New(reportURL string) *Reporter {
	return &Reporter{
		reportURL: reportURL,
	}
}

type Result struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func (r *Reporter) ReportResult(ctx context.Context, err error) error {
	var (
		status = "Success"
		errMsg string
	)
	if err != nil {
		status = "Failure"
		errMsg = err.Error()
	}

	data, err := json.Marshal(&Result{Status: status, Error: errMsg})
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.reportURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("creating report request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("sending request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	data, _ = io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("report request failed: %d (%s)", resp.StatusCode, string(data))
	}
	return nil
}
