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

// TypeMetricLabel returns the actual Prometheus label name for the server chassis
// type field. Returns "" when not configured — callers should skip type-related
// columns/labels when the returned value is empty.
func TypeMetricLabel(cfg DashboardConfig) string {
	return cfg.Topology.Type.LabelKey
}

// TypeDisplayName returns the column header for the server type field.
// Falls back to "Server Type" when not configured.
func TypeDisplayName(cfg DashboardConfig) string {
	if cfg.Topology.Type.DisplayName != "" {
		return cfg.Topology.Type.DisplayName
	}
	return "Server Type"
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

// withComplianceLabels adds a synthetic "compliance" label derived from "state":
// all series start as Non-Compliant; those with state="Completed" become Compliant.
func withComplianceLabels(expr string) string {
	return `label_replace(label_replace(` + expr +
		`, "compliance", "Non-Compliant", "state", ".+"),` +
		` "compliance", "Compliant", "state", "Completed")`
}

// BMCInfoCompliantCount counts BMCs that have a firmware CR matching compliantExpr
// and NO series matching nonCompliantExpr. Non-compliant takes precedence: a BMC
// with both completed and non-completed change requests (e.g. multi-indexed BIOS CRs)
// is excluded from the compliant count.
// kube_ironcore_bmc_info is on the left so region/bb/manufacturer filters apply correctly.
// compliantExpr should include state="Completed"; nonCompliantExpr should match the same
// base metric with state!="Completed". Neither should include a region filter (firmware
// metrics carry no region label).
// group by(name) deduplicates both sides so at most one series per BMC name participates
// in the unless and the join, regardless of how many indexed systems exist per BMC.
func BMCInfoCompliantCount(cfg DashboardConfig, compliantExpr, nonCompliantExpr, joinKey string) string {
	bb, _, _ := TopologyMetricLabels(cfg)
	base := `kube_ironcore_bmc_info{region=~"$region",` + bb + `=~"$bb",manufacturer=~"$manufacturer"}`
	compliantRHS := `group by(name)(label_replace(` + compliantExpr + `, "name", "$1", "` + joinKey + `", "(.*)"))`
	nonCompliantRHS := `group by(name)(label_replace(` + nonCompliantExpr + `, "name", "$1", "` + joinKey + `", "(.*)"))`
	return `count(` + base + ` * on(name) group_left() ` + compliantRHS + ` unless on(name) ` + nonCompliantRHS + `) or vector(0)`
}

// BMCInfoNonCompliantCount counts BMCs that are non-compliant:
// servers with a non-Completed firmware CR (nonCompliantExpr) plus servers with
// no firmware CR at all (detected via anyFirmwareBaseExpr, which should be the
// bare metric with no filters so the unless check is exhaustive).
// group by(name) deduplicates the managed-non-compliant RHS: if any indexed system
// for a BMC is non-compliant, that BMC is counted once.
func BMCInfoNonCompliantCount(cfg DashboardConfig, nonCompliantExpr, anyFirmwareBaseExpr, joinKey string) string {
	bb, _, _ := TopologyMetricLabels(cfg)
	base := `kube_ironcore_bmc_info{region=~"$region",` + bb + `=~"$bb",manufacturer=~"$manufacturer"}`
	managedNonCompliantRHS := `group by(name)(label_replace(` + nonCompliantExpr + `, "name", "$1", "` + joinKey + `", "(.*)"))`
	managedNonCompliant := base + ` * on(name) group_left() ` + managedNonCompliantRHS
	unmanaged := base + ` unless on(name) label_replace(` + anyFirmwareBaseExpr + `, "name", "$1", "` + joinKey + `", "(.*)")`
	return `count(` + managedNonCompliant + ` or ` + unmanaged + `) or vector(0)`
}

// BMCInfoFullTableJoin returns a PromQL expression for a detail table showing ALL
// BMCs — both managed (have a firmware CR) and unmanaged (no CR). Managed rows carry
// desired_version, observed_version, state and a compliance label derived from state.
// Unmanaged rows carry only topology/hardware labels and compliance="Non-Compliant".
// firmwareExpr: firmware metric with manufacturer/model filters (no region — the join
//   with kube_ironcore_bmc_info handles region and bb scoping).
// anyFirmwareBaseExpr: bare metric name (no filters) for the `unless` membership check.
// joinKey: label on the firmware metric that holds the BMC resource name.
func BMCInfoFullTableJoin(cfg DashboardConfig, firmwareExpr, anyFirmwareBaseExpr, joinKey, inventoryExtraFilters string) string {
	bb, nodename, zone := TopologyMetricLabels(cfg)

	// Managed rows: firmware metric enriched with topology from kube_ironcore_bmc_info.
	// region is included in group_left so the Region table column populates.
	ksmJoin := `label_replace(kube_ironcore_bmc_info{region=~"$region",` + bb + `=~"$bb",manufacturer=~"$manufacturer"}, "` + joinKey + `", "$1", "name", "(.*)")`
	managed := firmwareExpr + ` * on(` + joinKey + `) group_left(region, ` + bb + `, ` + nodename + `, ` + zone + `) ` + ksmJoin
	managedNormalized := NormaliseTopologyLabels(cfg, withComplianceLabels(managed))

	// Unmanaged rows: in kube_ironcore_bmc_info but absent from any firmware CR.
	inventoryFilters := `region=~"$region",` + bb + `=~"$bb",manufacturer=~"$manufacturer"`
	if inventoryExtraFilters != "" {
		inventoryFilters += "," + inventoryExtraFilters
	}
	base := `kube_ironcore_bmc_info{` + inventoryFilters + `}`
	unmanaged := base + ` unless on(name) label_replace(` + anyFirmwareBaseExpr + `, "name", "$1", "` + joinKey + `", "(.*)")`
	unmanagedNormalized := NormaliseTopologyLabels(cfg, `label_replace(`+unmanaged+`, "compliance", "Non-Compliant", "name", ".+")`)

	return managedNormalized + ` or ` + unmanagedNormalized
}

// entries matching the standard dashboard filters plus any extraFilters (e.g.
// `manufacturer=~"$manufacturer",model=~"$bios_model"`). This gives the total number
// of BMC resources in the fleet and is not limited by firmware CR coverage, so it
// is the correct metric for "Total Tracked Servers" panels.
func BMCInfoDirectCount(cfg DashboardConfig, extraFilters string) string {
	bb, _, _ := TopologyMetricLabels(cfg)
	filters := `region=~"$region",` + bb + `=~"$bb"`
	if extraFilters != "" {
		filters += "," + extraFilters
	}
	return `count(kube_ironcore_bmc_info{` + filters + `}) or vector(0)`
}

// KsmBMCInfoExpr builds the right-hand side of a PromQL binary join against
// kube_ironcore_bmc_info, filtered by $bb. joinKey is the canonical label to
// create on the right side matching the join label on the left side.
// kube_ironcore_bmc_info covers every BMC regardless of firmware management
// enrollment and is the preferred join target for fleet-wide counts.
func KsmBMCInfoExpr(cfg DashboardConfig, joinKey string) string {
	bb, _, _ := TopologyMetricLabels(cfg)
	filter := `kube_ironcore_bmc_info{` + bb + `=~"$bb"}`
	return `label_replace(` + filter + `, "` + joinKey + `", "$1", "name", "(.*)")`
}

// BMCInfoCountJoin wraps expr in a count() applying the $bb join via
// kube_ironcore_bmc_info. Prefer this over CountJoin to avoid under-counting
// when kube_ironcore_info has incomplete fleet coverage.
func BMCInfoCountJoin(cfg DashboardConfig, expr, joinKey string) string {
	return `count(` + expr + ` * on(` + joinKey + `) group_left() ` + KsmBMCInfoExpr(cfg, joinKey) + `) or vector(0)`
}

// BMCInfoTopologyJoin enriches a metric with topology labels for table display
// by joining against kube_ironcore_bmc_info instead of kube_ironcore_info.
// Requires that the joinKey label on the left side matches the BMC resource name.
func BMCInfoTopologyJoin(cfg DashboardConfig, expr, joinKey string) string {
	bb, nodename, zone := TopologyMetricLabels(cfg)
	joined := expr +
		` * on(` + joinKey + `) group_left(` + bb + `, ` + nodename + `, ` + zone + `)` +
		` ` + KsmBMCInfoExpr(cfg, joinKey)
	return NormaliseTopologyLabels(cfg, joined)
}

// BBJoin enforces $bb/$node/$manufacturer/$model filtering by joining expr with
// kube_ironcore_bmc_info (topology + hardware identity).
// redfish_monitor_reading carries no topology labels; all filtering is done via the join.
// kube_ironcore_bmc_info covers every BMC regardless of firmware management enrollment.
func BBJoin(cfg DashboardConfig, expr string) string {
	bb, _, _ := TopologyMetricLabels(cfg)
	// Filter on "name" (BMC resource name, e.g. "node001-bb098") not the short nodename label,
	// because $node is always populated from the "name" label on kube_ironcore_bmc_info.
	ksmJoin := `label_replace(kube_ironcore_bmc_info{region=~"$region",` +
		bb + `=~"$bb",name=~"$node",` +
		`manufacturer=~"$manufacturer",model=~"$model"}, "hostname", "$1", "name", "(.*)")`
	return expr + ` * on(hostname) group_left() ` + ksmJoin
}

// BBJoinNode is like BBJoin but for single-node detail dashboards that have no
// $manufacturer or $model variables. Filters only by $bb and $node.
func BBJoinNode(cfg DashboardConfig, expr string) string {
	bb, _, _ := TopologyMetricLabels(cfg)
	ksmJoin := `label_replace(kube_ironcore_bmc_info{` +
		bb + `=~"$bb",name=~"$node"}, "hostname", "$1", "name", "(.*)")`
	return expr + ` * on(hostname) group_left() ` + ksmJoin
}
