// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package config

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/vincent-petithory/dataurl"
)

type Config struct {
	ReportURL string `json:"reportURL"`
}

func FetchViaIgnition(ctx context.Context, ignitionURL string) (*Config, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ignitionURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(res.Body)
		return nil, fmt.Errorf("unexpected status code: %d: %s", res.StatusCode, string(data))
	}

	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	return decodeIgnitionConfig(data)
}

type IgnitionShim struct {
	Storage IgnitionShimStorage `json:"storage"`
}

type IgnitionShimStorage struct {
	Files []IgnitionShimFile
}

type IgnitionShimFile struct {
	Path     string               `json:"path"`
	Contents IgnitionShimContents `json:"contents"`
}

type IgnitionShimContents struct {
	Source string `json:"source"`
}

func decodeIgnitionConfig(ignitionData []byte) (*Config, error) {
	ignition := &IgnitionShim{}
	if err := json.Unmarshal(ignitionData, ignition); err != nil {
		return nil, fmt.Errorf("unmarshalling json: %w", err)
	}

	for _, file := range ignition.Storage.Files {
		if file.Path != "/sanitizer/config" {
			continue
		}

		dataURL, err := dataurl.DecodeString(file.Contents.Source)
		if err != nil {
			return nil, fmt.Errorf("decoding data url: %w", err)
		}

		cfg := &Config{}
		if err := json.Unmarshal(dataURL.Data, cfg); err != nil {
			return nil, fmt.Errorf("unmarshalling config json: %w", err)
		}
		return cfg, nil
	}
	return nil, fmt.Errorf("no config file found in ignition")
}
