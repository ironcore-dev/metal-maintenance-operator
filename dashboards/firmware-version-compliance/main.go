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
					labelvalues.Datasource("$datasource"),
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
					bb, _, _ := mmo.TopologyMetricLabels(cfg)
					return bb
				}(),
					labelvalues.Datasource("$datasource"),
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
		dashboard.AddVariable("manufacturer",
			listvariable.List(
				labelvalues.PrometheusLabelValues("manufacturer",
					labelvalues.Datasource("$datasource"),
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
					labelvalues.Datasource("$datasource"),
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
					labelvalues.Datasource("$datasource"),
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
				panel.Description("Servers whose observed BIOS version matches the desired version in the upgrade policy. When this count is nonzero and equals Total Tracked Servers, the upgrade campaign for the selected hardware is complete."),
				panel.AddQuery(
					mmo.PromQL(
						mmo.CountJoin(cfg, `metal_maintenance_biosversion_info{state="Completed",region=~"$region",manufacturer=~"$manufacturer",model=~"$bios_model"}`, "Server", "server"),
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("Non-Compliant (not on target version)",
				stat.Chart(),
				panel.Description("Servers where the observed BIOS version does not yet match the desired version. These servers are pending, in-progress, or failed. Use the detail table below to identify which servers are affected and what state they are in."),
				panel.AddQuery(
					mmo.PromQL(
						mmo.CountJoin(cfg, `metal_maintenance_biosversion_info{state!="Completed",region=~"$region",manufacturer=~"$manufacturer",model=~"$bios_model"}`, "Server", "server"),
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("Total Tracked Servers",
				stat.Chart(),
				panel.Description("Total servers for which a BIOS firmware target is defined. Use this as the denominator when interpreting the Compliant and Non-Compliant counts. A count lower than expected means some servers have not yet had a BIOSVersion CR created."),
				panel.AddQuery(
					mmo.PromQL(
						mmo.CountJoin(cfg, `metal_maintenance_biosversion_info{region=~"$region",manufacturer=~"$manufacturer",model=~"$bios_model"}`, "Server", "server"),
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
				panel.Description("Per-server BIOS compliance detail showing observed and desired firmware versions alongside topology (building block, zone) and hardware identity (manufacturer, model). Non-Compliant rows sort to the top. Use the manufacturer and model filters above to focus on a specific hardware family, or the building block filter to scope to a deployment zone."),
				panel.AddQuery(
					mmo.PromQL(
						withComplianceLabel(mmo.TopologyJoin(cfg, `metal_maintenance_biosversion_info{region=~"$region",manufacturer=~"$manufacturer",model=~"$bios_model"}`, "Server", "server")),
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
				panel.Description("Servers whose observed BMC firmware version matches the desired version in the upgrade policy. BMC upgrades require a BMC reboot, so a rising compliant count during an upgrade campaign indicates the operator is successfully cycling through the fleet."),
				panel.AddQuery(
					mmo.PromQL(
						mmo.CountJoin(cfg, `metal_maintenance_bmcversion_info{state="Completed",region=~"$region",manufacturer=~"$manufacturer",model=~"$bmc_model"}`, "BMC", "bmc"),
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("Non-Compliant (not on target version)",
				stat.Chart(),
				panel.Description("Servers where the observed BMC firmware version does not yet match the desired version. Use the detail table below to distinguish servers that are pending, actively upgrading, or in a failed state — each requires a different response."),
				panel.AddQuery(
					mmo.PromQL(
						mmo.CountJoin(cfg, `metal_maintenance_bmcversion_info{state!="Completed",region=~"$region",manufacturer=~"$manufacturer",model=~"$bmc_model"}`, "BMC", "bmc"),
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("Total Tracked Servers",
				stat.Chart(),
				panel.Description("Total servers for which a BMC firmware target is defined. Compare against the building block's total BMC count to confirm all servers are enrolled in firmware management."),
				panel.AddQuery(
					mmo.PromQL(
						mmo.CountJoin(cfg, `metal_maintenance_bmcversion_info{region=~"$region",manufacturer=~"$manufacturer",model=~"$bmc_model"}`, "BMC", "bmc"),
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
				panel.Description("Per-server BMC compliance detail showing observed and desired firmware versions alongside topology and hardware identity. Non-Compliant rows sort to the top. A server stuck in Non-Compliant with state InProgress for more than the expected upgrade duration (typically 10–20 min for a BMC reboot cycle) may require manual intervention."),
				panel.AddQuery(
					mmo.PromQL(
						withComplianceLabel(mmo.TopologyJoin(cfg, `metal_maintenance_bmcversion_info{region=~"$region",manufacturer=~"$manufacturer",model=~"$bmc_model"}`, "BMC", "bmc")),
						"$datasource",
					),
				),
			),
		),
	)

	exec.BuildDashboard(builder, buildErr)
}
