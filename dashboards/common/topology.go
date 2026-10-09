// Copyright 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package common

import (
	_ "embed"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// TopologyField holds display metadata and the source Kubernetes label key for
// one canonical topology label.
type TopologyField struct {
	DisplayName string `yaml:"displayName"`
	// LabelKey is the Kubernetes metadata label key on BMC/Server CRs that carries
	// this topology value. It must match kubeStateMetrics.topology.* in values.yaml
	// so that KSM maps the right CR label to the canonical Prometheus metric label.
	LabelKey string `yaml:"labelKey"`
}

// TopologyConfig maps each canonical metric label name to per-field settings.
// The canonical label names (bb, nodename, zone) are fixed — they are the
// contract between the dashboard PromQL and the KSM CustomResourceState config.
type TopologyConfig struct {
	BB       TopologyField `yaml:"bb"`
	Nodename TopologyField `yaml:"nodename"`
	Zone     TopologyField `yaml:"zone"`
}

// DashboardConfig is the root of the config.yaml schema shared across MMO dashboards.
type DashboardConfig struct {
	Topology TopologyConfig `yaml:"topology"`
}

//go:embed config.yaml
var defaultConfigBytes []byte

// LoadConfig returns a DashboardConfig starting from the embedded defaults in
// config.yaml and overlaying the file at path (if non-empty). Callers that want
// only the defaults may pass an empty string.
func LoadConfig(path string) (DashboardConfig, error) {
	var cfg DashboardConfig
	if err := yaml.Unmarshal(defaultConfigBytes, &cfg); err != nil {
		return cfg, fmt.Errorf("parse embedded config: %w", err)
	}
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read config file: %w", err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config file: %w", err)
	}
	return cfg, nil
}
