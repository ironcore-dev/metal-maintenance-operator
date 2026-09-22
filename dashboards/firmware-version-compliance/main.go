// Copyright 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"fmt"
	"os"

	sdk "github.com/perses/perses/go-sdk"
	"github.com/perses/perses/go-sdk/dashboard"
	"github.com/perses/perses/go-sdk/panel"
	panelgroup "github.com/perses/perses/go-sdk/panel-group"
	listvariable "github.com/perses/perses/go-sdk/variable/list-variable"

	datasourcevariable "github.com/perses/plugins/datasourcevariable/sdk/go"
	promDs "github.com/perses/plugins/prometheus/sdk/go/datasource"
	labelvalues "github.com/perses/plugins/prometheus/sdk/go/variable/label-values"
	stat "github.com/perses/plugins/statchart/sdk/go"
	table "github.com/perses/plugins/table/sdk/go"

	mmo "github.com/ironcore-dev/metal-maintenance-operator/dashboards/common"
)

// stateCellSettings colours state cells: green for "Completed", red for anything
// else. Specific value check must come before the catch-all regex (first match wins).
var stateCellSettings = []table.CellSettings{
	{
		Condition: table.Condition{
			Kind: table.ValueConditionKind,
			Spec: &table.ValueConditionSpec{Value: "Completed"},
		},
		TextColor: "#73BF69",
	},
	{
		Condition: table.Condition{
			Kind: table.RegexConditionKind,
			Spec: &table.RegexConditionSpec{Expr: ".+"},
		},
		TextColor: "#F2495C",
	},
}

// complianceCellSettings colours compliance cells: green background+text for
// "Compliant", red background+text for anything else. Specific value check must
// come before the catch-all regex (first match wins).
var complianceCellSettings = []table.CellSettings{
	{
		Condition: table.Condition{
			Kind: table.ValueConditionKind,
			Spec: &table.ValueConditionSpec{Value: "Compliant"},
		},
		TextColor:       "#73BF69",
		BackgroundColor: "#122115",
	},
	{
		Condition: table.Condition{
			Kind: table.RegexConditionKind,
			Spec: &table.RegexConditionSpec{Expr: ".+"},
		},
		TextColor:       "#F2495C",
		BackgroundColor: "#2C1117",
	},
}

// stateColumn returns a ColumnSettings entry for the "state" label with
// sorting enabled but no default sort direction (compliance column handles that).
func stateColumn() table.ColumnSettings {
	return table.ColumnSettings{
		Name:          "state",
		EnableSorting: true,
		Hide:          false,
		CellSettings:  stateCellSettings,
	}
}

// complianceColumn returns a ColumnSettings entry for the synthetic "compliance"
// label. Sorted descending by default: "Non-Compliant" > "Compliant" alphabetically,
// so non-compliant rows appear first.
func complianceColumn() table.ColumnSettings {
	return table.ColumnSettings{
		Name:          "compliance",
		Header:        "Compliance",
		EnableSorting: true,
		Sort:          table.DescSort,
		Hide:          false,
		CellSettings:  complianceCellSettings,
	}
}

// biosVersionColumns returns the columns shown in BIOS firmware tables.
// Topology labels (Node, Building Block, Zone) come from the kube_ironcore_info
// join using canonical metric label names (bb, nodename, zone).
func biosVersionColumns(cfg mmo.DashboardConfig) []table.ColumnSettings {
	return []table.ColumnSettings{
		{Name: "nodename", Header: cfg.Topology.Nodename.DisplayName, EnableSorting: true, Hide: false},
		{Name: "bb", Header: cfg.Topology.BB.DisplayName, EnableSorting: true, Hide: false},
		{Name: "region", Header: "Region", EnableSorting: true, Hide: false},
		{Name: "zone", Header: cfg.Topology.Zone.DisplayName, EnableSorting: true, Hide: false},
		complianceColumn(),
		stateColumn(),
		{Name: "manufacturer", EnableSorting: true, Hide: false},
		{Name: "model", EnableSorting: true, Hide: false},
		{Name: "observed_version", EnableSorting: true, Hide: false},
		{Name: "desired_version", EnableSorting: true, Hide: false},
	}
}

// bmcVersionColumns returns the columns shown in BMC firmware tables.
// Topology labels come from the kube_ironcore_info join.
func bmcVersionColumns(cfg mmo.DashboardConfig) []table.ColumnSettings {
	return []table.ColumnSettings{
		{Name: "nodename", Header: cfg.Topology.Nodename.DisplayName, EnableSorting: true, Hide: false},
		{Name: "bb", Header: cfg.Topology.BB.DisplayName, EnableSorting: true, Hide: false},
		{Name: "region", Header: "Region", EnableSorting: true, Hide: false},
		{Name: "zone", Header: cfg.Topology.Zone.DisplayName, EnableSorting: true, Hide: false},
		complianceColumn(),
		stateColumn(),
		{Name: "manufacturer", EnableSorting: true, Hide: false},
		{Name: "model", EnableSorting: true, Hide: false},
		{Name: "observed_version", EnableSorting: true, Hide: false},
		{Name: "desired_version", EnableSorting: true, Hide: false},
	}
}

// topologyMetricLabels returns the actual Prometheus label names on kube_ironcore_info
// for each canonical topology field. When a labelKey is configured it is used
// directly (the existing Greenhouse/SAP KSM emits long underscore-separated names
// like kubernetes_metal_cloud_sap_bb); when empty the canonical name is used
// (new deployments using the MMO chart's CustomResourceState config emit bb,
// nodename, zone directly).
func topologyMetricLabels(cfg mmo.DashboardConfig) (bb, nodename, zone string) {
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

// normaliseTopologyLabels wraps expr with label_replace calls that rename the
// actual metric label names to the canonical names (bb, nodename, zone) expected
// by column definitions. No-ops when the labelKey already equals the canonical name.
func normaliseTopologyLabels(cfg mmo.DashboardConfig, expr string) string {
	bb, nodename, zone := topologyMetricLabels(cfg)
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

// ksmJoinExpr builds the right-hand side of a PromQL binary join against
// kube_ironcore_info. joinKey is the canonical label name to create on the
// right side (matching the join label on the left side). kind is the
// customresource_kind selector ("BMC" or "Server").
func ksmJoinExpr(cfg mmo.DashboardConfig, kind, joinKey string) string {
	bb, _, _ := topologyMetricLabels(cfg)
	filter := `kube_ironcore_info{customresource_kind="` + kind + `",` + bb + `=~"$bb"}`
	return `label_replace(` + filter + `, "` + joinKey + `", "$1", "name", "(.*)")`
}

// topologyJoin enriches a firmware metric with topology labels for table display.
// kind is the customresource_kind value ("BMC" or "Server"); joinKey is the
// label that links the firmware metric to the kube_ironcore_info series.
func topologyJoin(cfg mmo.DashboardConfig, expr, kind, joinKey string) string {
	bb, nodename, zone := topologyMetricLabels(cfg)
	joined := expr +
		` * on(` + joinKey + `) group_left(` + bb + `, ` + nodename + `, ` + zone + `)` +
		` ` + ksmJoinExpr(cfg, kind, joinKey)
	return normaliseTopologyLabels(cfg, joined)
}

// countJoin wraps a firmware metric in a count(), applying the $bb join for filtering.
func countJoin(cfg mmo.DashboardConfig, expr, kind, joinKey string) string {
	return `count(` + expr + ` * on(` + joinKey + `) group_left() ` + ksmJoinExpr(cfg, kind, joinKey) + `) or vector(0)`
}


// withComplianceLabel adds a synthetic "compliance" label to every series:
// "Non-Compliant" for state != "Completed", "Compliant" for state = "Completed".
// The inner replace sets all non-empty states to Non-Compliant; the outer
// overrides only Completed to Compliant. Uses RE2-compatible regexes.
func withComplianceLabel(expr string) string {
	return `label_replace(label_replace(` + expr +
		`, "compliance", "Non-Compliant", "state", ".+"),` +
		` "compliance", "Compliant", "state", "Completed")`
}

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "", "path to topology config YAML override (default: embedded config.yaml)")
	flag.Parse()

	cfg, err := mmo.LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	exec := sdk.NewExec()
	builder, buildErr := dashboard.New("firmware-version-compliance",
		dashboard.Name("Firmware Version Compliance"),
		dashboard.ProjectName("metal-maintenance-operator"),

		dashboard.AddVariable("datasource",
			listvariable.List(
				datasourcevariable.Datasource(promDs.PluginKind),
			),
		),
		dashboard.AddVariable("region",
			listvariable.List(
				labelvalues.PrometheusLabelValues("region",
					labelvalues.Matchers(
						"metal_maintenance_biosversion_info",
						"metal_maintenance_bmcversion_info",
					),
				),
				listvariable.AllowAllValue(true),
				listvariable.AllowMultiple(true),
				listvariable.DefaultValue("$__all"),
				listvariable.DisplayName("Region"),
			),
		),
		dashboard.AddVariable("bb",
			listvariable.List(
				labelvalues.PrometheusLabelValues(func() string {
					bb, _, _ := topologyMetricLabels(cfg)
					return bb
				}(),
					labelvalues.Matchers(
						`kube_ironcore_info{customresource_kind="BMC"}`,
						`kube_ironcore_info{customresource_kind="Server"}`,
					),
				),
				listvariable.AllowAllValue(true),
				listvariable.AllowMultiple(true),
				listvariable.DefaultValue("$__all"),
				listvariable.DisplayName(cfg.Topology.BB.DisplayName),
			),
		),
		dashboard.AddVariable("state",
			listvariable.List(
				labelvalues.PrometheusLabelValues("state",
					labelvalues.Matchers(
						`metal_maintenance_biosversion_info{region=~"$region",manufacturer=~"$manufacturer"}`,
						`metal_maintenance_bmcversion_info{region=~"$region",manufacturer=~"$manufacturer"}`,
					),
				),
				listvariable.AllowAllValue(true),
				listvariable.AllowMultiple(true),
				listvariable.DefaultValue("$__all"),
				listvariable.DisplayName("State"),
			),
		),
		dashboard.AddVariable("manufacturer",
			listvariable.List(
				labelvalues.PrometheusLabelValues("manufacturer",
					labelvalues.Matchers(
						`metal_maintenance_biosversion_info{region=~"$region"}`,
						`metal_maintenance_bmcversion_info{region=~"$region"}`,
					),
				),
				listvariable.AllowAllValue(true),
				listvariable.AllowMultiple(true),
				listvariable.DefaultValue("$__all"),
				listvariable.DisplayName("Manufacturer"),
			),
		),
		dashboard.AddVariable("bios_model",
			listvariable.List(
				labelvalues.PrometheusLabelValues("model",
					labelvalues.Matchers(`metal_maintenance_biosversion_info{region=~"$region",manufacturer=~"$manufacturer"}`),
				),
				listvariable.AllowAllValue(true),
				listvariable.AllowMultiple(true),
				listvariable.DefaultValue("$__all"),
				listvariable.DisplayName("Model (BIOS)"),
			),
		),
		dashboard.AddVariable("bmc_model",
			listvariable.List(
				labelvalues.PrometheusLabelValues("model",
					labelvalues.Matchers(`metal_maintenance_bmcversion_info{region=~"$region",manufacturer=~"$manufacturer"}`),
				),
				listvariable.AllowAllValue(true),
				listvariable.AllowMultiple(true),
				listvariable.DefaultValue("$__all"),
				listvariable.DisplayName("Model (BMC)"),
			),
		),

		// ── BIOS ─────────────────────────────────────────────────────────────────

		dashboard.AddPanelGroup("BIOS Version Compliance",
			panelgroup.PanelsPerLine(3),
			panelgroup.PanelHeight(4),
			panelgroup.AddPanel("Compliant (on target version)",
				stat.Chart(),
				panel.AddQuery(
					mmo.PromQL(
						countJoin(cfg, `metal_maintenance_biosversion_info{state="Completed",region=~"$region",manufacturer=~"$manufacturer",model=~"$bios_model"}`, "Server", "server"),
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("Non-Compliant (not on target version)",
				stat.Chart(),
				panel.AddQuery(
					mmo.PromQL(
						countJoin(cfg, `metal_maintenance_biosversion_info{state!="Completed",region=~"$region",manufacturer=~"$manufacturer",model=~"$bios_model"}`, "Server", "server"),
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("Total Tracked Servers",
				stat.Chart(),
				panel.AddQuery(
					mmo.PromQL(
						countJoin(cfg, `metal_maintenance_biosversion_info{region=~"$region",manufacturer=~"$manufacturer",model=~"$bios_model"}`, "Server", "server"),
						"$datasource",
					),
				),
			),
		),

		dashboard.AddPanelGroup("BIOS Version Detail",
			panelgroup.PanelsPerLine(1),
			panelgroup.PanelHeight(10),
			panelgroup.AddPanel("All Servers — Desired vs Observed BIOS Version",
				table.Table(
					table.WithDefaultColumnHidden(true),
					table.WithColumnSettings(biosVersionColumns(cfg)),
					table.WithEnableFiltering(true),
				),
				panel.AddQuery(
					mmo.PromQL(
						withComplianceLabel(topologyJoin(cfg, `metal_maintenance_biosversion_info{region=~"$region",manufacturer=~"$manufacturer",model=~"$bios_model",state=~"$state"}`, "Server", "server")),
						"$datasource",
					),
				),
			),
		),

		// ── BMC ──────────────────────────────────────────────────────────────────

		dashboard.AddPanelGroup("BMC Version Compliance",
			panelgroup.PanelsPerLine(3),
			panelgroup.PanelHeight(4),
			panelgroup.AddPanel("Compliant (on target version)",
				stat.Chart(),
				panel.AddQuery(
					mmo.PromQL(
						countJoin(cfg, `metal_maintenance_bmcversion_info{state="Completed",region=~"$region",manufacturer=~"$manufacturer",model=~"$bmc_model"}`, "BMC", "bmc"),
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("Non-Compliant (not on target version)",
				stat.Chart(),
				panel.AddQuery(
					mmo.PromQL(
						countJoin(cfg, `metal_maintenance_bmcversion_info{state!="Completed",region=~"$region",manufacturer=~"$manufacturer",model=~"$bmc_model"}`, "BMC", "bmc"),
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("Total Tracked Servers",
				stat.Chart(),
				panel.AddQuery(
					mmo.PromQL(
						countJoin(cfg, `metal_maintenance_bmcversion_info{region=~"$region",manufacturer=~"$manufacturer",model=~"$bmc_model"}`, "BMC", "bmc"),
						"$datasource",
					),
				),
			),
		),

		dashboard.AddPanelGroup("BMC Version Detail",
			panelgroup.PanelsPerLine(1),
			panelgroup.PanelHeight(10),
			panelgroup.AddPanel("All Servers — Desired vs Observed BMC Version",
				table.Table(
					table.WithDefaultColumnHidden(true),
					table.WithColumnSettings(bmcVersionColumns(cfg)),
					table.WithEnableFiltering(true),
				),
				panel.AddQuery(
					mmo.PromQL(
						withComplianceLabel(topologyJoin(cfg, `metal_maintenance_bmcversion_info{region=~"$region",manufacturer=~"$manufacturer",model=~"$bmc_model",state=~"$state"}`, "BMC", "bmc")),
						"$datasource",
					),
				),
			),
		),
	)

	exec.BuildDashboard(builder, buildErr)
}
