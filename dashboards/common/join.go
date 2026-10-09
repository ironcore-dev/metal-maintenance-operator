// Copyright 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package common

// TopologyMetricLabels returns the actual Prometheus label names on kube_ironcore_info
// for each canonical topology field. When a labelKey is configured it is used
// directly; when empty the canonical name is used (bb, nodename, zone).
func TopologyMetricLabels(cfg DashboardConfig) (bb, nodename, zone string) {
	bb = "bb"
	if cfg.Topology.BB.LabelKey != "" {
		bb = cfg.Topology.BB.LabelKey
	}
	nodename = "nodename"
	if cfg.Topology.Nodename.LabelKey != "" {
		nodename = cfg.Topology.Nodename.LabelKey
	}
	zone = "zone"
	if cfg.Topology.Zone.LabelKey != "" {
		zone = cfg.Topology.Zone.LabelKey
	}
	return
}

// NormaliseTopologyLabels wraps expr with label_replace calls that rename the
// actual metric label names to the canonical names (bb, nodename, zone).
// No-ops when the labelKey already equals the canonical name.
func NormaliseTopologyLabels(cfg DashboardConfig, expr string) string {
	bb, nodename, zone := TopologyMetricLabels(cfg)
	if bb != "bb" {
		expr = `label_replace(` + expr + `, "bb", "$1", "` + bb + `", "(.*)")`
	}
	if nodename != "nodename" {
		expr = `label_replace(` + expr + `, "nodename", "$1", "` + nodename + `", "(.*)")`
	}
	if zone != "zone" {
		expr = `label_replace(` + expr + `, "zone", "$1", "` + zone + `", "(.*)")`
	}
	return expr
}

// KsmJoinExpr builds the right-hand side of a PromQL binary join against
// kube_ironcore_info for a given customresource_kind. joinKey is the canonical
// label to create on the right side matching the join label on the left side.
func KsmJoinExpr(cfg DashboardConfig, kind, joinKey string) string {
	bb, _, _ := TopologyMetricLabels(cfg)
	filter := `kube_ironcore_info{customresource_kind="` + kind + `",` + bb + `=~"$bb"}`
	return `label_replace(` + filter + `, "` + joinKey + `", "$1", "name", "(.*)")`
}

// KsmBMCJoinExpr builds the right-hand side of a PromQL binary join for BMC
// telemetry metrics that carry a "hostname" label equal to the BMC resource name.
// bb and nodename filtering are applied on the KSM side; the join then restricts
// the metric to only the matching hostnames without needing a separate hostname filter.
func KsmBMCJoinExpr(cfg DashboardConfig) string {
	bb, nodename, _ := TopologyMetricLabels(cfg)
	filter := `kube_ironcore_bmc_info{` + bb + `=~"$bb",` + nodename + `=~"$node"}`
	return `label_replace(` + filter + `, "hostname", "$1", "name", "(.*)")`
}

// TopologyJoin enriches a metric with topology labels for table display.
// kind is the customresource_kind value ("BMC" or "Server"); joinKey is the
// label linking the metric to kube_ironcore_info.
func TopologyJoin(cfg DashboardConfig, expr, kind, joinKey string) string {
	bb, nodename, zone := TopologyMetricLabels(cfg)
	joined := expr +
		` * on(` + joinKey + `) group_left(` + bb + `, ` + nodename + `, ` + zone + `)` +
		` ` + KsmJoinExpr(cfg, kind, joinKey)
	return NormaliseTopologyLabels(cfg, joined)
}

// CountJoin wraps expr in a count() applying the $bb join for filtering.
func CountJoin(cfg DashboardConfig, expr, kind, joinKey string) string {
	return `count(` + expr + ` * on(` + joinKey + `) group_left() ` + KsmJoinExpr(cfg, kind, joinKey) + `) or vector(0)`
}
