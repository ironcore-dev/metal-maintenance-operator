// Copyright 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"fmt"
	"os"

	sdk "github.com/perses/perses/go-sdk"
	"github.com/perses/perses/go-sdk/common"
	"github.com/perses/perses/go-sdk/dashboard"
	"github.com/perses/perses/go-sdk/link"
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

func main() {
	var configPath string
	flag.StringVar(&configPath, "config", "", "path to topology config YAML override (default: embedded config.yaml)")
	flag.Parse()

	cfg, err := mmo.LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	bb, _, _ := mmo.TopologyMetricLabels(cfg)
	typeLabel := mmo.TypeMetricLabel(cfg)
	typeHeader := mmo.TypeDisplayName(cfg)

	inventoryCols := []table.ColumnSettings{
		{Name: "name", Header: "BMC Name", EnableSorting: true, Hide: false,
			DataLink: &table.DataLink{
				URL:        "/projects/metal-maintenance-operator/dashboards/bmc-detail?var-node=${__data.fields[\"name\"]}",
				Title:      "Open BMC Detail",
				OpenNewTab: false,
			}},
		{Name: "manufacturer", Header: "Manufacturer", EnableSorting: true, Hide: false},
		{Name: "model", Header: "BMC Model", EnableSorting: true, Hide: false},
	}
	if typeLabel != "" {
		inventoryCols = append(inventoryCols, table.ColumnSettings{Name: typeLabel, Header: typeHeader, EnableSorting: true, Hide: false})
	}
	inventoryCols = append(inventoryCols,
		table.ColumnSettings{Name: "firmware_version", Header: "Firmware", EnableSorting: true, Hide: false},
		table.ColumnSettings{Name: "region", Header: "Region", EnableSorting: true, Hide: false},
		table.ColumnSettings{Name: bb, Header: cfg.Topology.BB.DisplayName, EnableSorting: true, Hide: false},
	)

	exec := sdk.NewExec()
	builder, buildErr := dashboard.New("bmc-telemetry",
		dashboard.Name("BMC Fleet Overview"),
		dashboard.ProjectName("metal-maintenance-operator"),

		// ── Variables ──────────────────────────────────────────────────────────────

		dashboard.AddVariable("datasource",
			listvariable.List(
				datasourcevariable.Datasource(promDs.PluginKind),
			),
		),
		dashboard.AddVariable("region",
			listvariable.List(
				labelvalues.PrometheusLabelValues("region",
					labelvalues.Datasource("$datasource"),
					labelvalues.Matchers(`kube_ironcore_bmc_info`),
				),
				listvariable.AllowAllValue(true),
				listvariable.AllowMultiple(true),
				listvariable.DefaultValue("$__all"),
				listvariable.DisplayName("Region"),
			),
		),
		dashboard.AddVariable("bb",
			listvariable.List(
				labelvalues.PrometheusLabelValues(bb,
					labelvalues.Datasource("$datasource"),
					labelvalues.Matchers(`kube_ironcore_bmc_info{region=~"$region"}`),
				),
				listvariable.AllowAllValue(true),
				listvariable.AllowMultiple(true),
				listvariable.DefaultValue("$__all"),
				listvariable.DisplayName(cfg.Topology.BB.DisplayName),
			),
		),
		dashboard.AddVariable("node",
			listvariable.List(
				labelvalues.PrometheusLabelValues("name",
					labelvalues.Datasource("$datasource"),
					labelvalues.Matchers(`kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb"}`),
				),
				listvariable.AllowAllValue(true),
				listvariable.AllowMultiple(true),
				listvariable.DefaultValue("$__all"),
				listvariable.DisplayName("Node"),
			),
		),
		dashboard.AddVariable("manufacturer",
			listvariable.List(
				labelvalues.PrometheusLabelValues("manufacturer",
					labelvalues.Datasource("$datasource"),
					labelvalues.Matchers(`kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",name=~"$node",manufacturer!=""}`),
				),
				listvariable.AllowAllValue(true),
				listvariable.AllowMultiple(true),
				listvariable.DefaultValue("$__all"),
				listvariable.DisplayName("Manufacturer"),
			),
		),
		dashboard.AddVariable("model",
			listvariable.List(
				labelvalues.PrometheusLabelValues("model",
					labelvalues.Datasource("$datasource"),
					labelvalues.Matchers(`kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",name=~"$node",manufacturer=~"$manufacturer",model!=""}`),
				),
				listvariable.AllowAllValue(true),
				listvariable.AllowMultiple(true),
				listvariable.DefaultValue("$__all"),
				listvariable.DisplayName("Model"),
			),
		),

		// ── Fleet Summary ──────────────────────────────────────────────────────────

		dashboard.AddPanelGroup("Fleet Summary",
			panelgroup.PanelsPerLine(2),
			panelgroup.PanelHeight(4),
			panelgroup.AddPanel("BMCs Reporting Telemetry",
				stat.Chart(),
				panel.Description("Number of BMCs in the selected building block currently pushing MetricReport telemetry to the operator. A count lower than Total BMCs indicates hosts not yet enrolled in telemetry or experiencing subscription delivery failures."),
				panel.AddQuery(
					mmo.PromQL(
						`count(`+mmo.BBJoin(cfg, `count by(hostname)(redfish_monitor_reading)`)+`) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("Total BMCs in Selected Building Block",
				stat.Chart(),
				panel.Description("Total BMC resources managed in the selected region and building block. Compare with the reporting count above to identify telemetry coverage gaps."),
				panel.AddQuery(
					mmo.PromQL(
						`count(kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",name=~"$node",manufacturer=~"$manufacturer",model=~"$model"}) or vector(0)`,
						"$datasource",
					),
				),
			),
		),

		// ── Manufacturer Distribution ──────────────────────────────────────────────

		dashboard.AddPanelGroup("Manufacturer Distribution",
			panelgroup.PanelsPerLine(5),
			panelgroup.PanelHeight(4),
			panelgroup.AddPanel("Dell BMCs",
				stat.Chart(),
				panel.Description("Total Dell BMCs in the selected region and building block."),
				panel.AddQuery(
					mmo.PromQL(
						`count(kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",manufacturer=~"Dell.*"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("HPE BMCs",
				stat.Chart(),
				panel.Description("Total HPE BMCs in the selected region and building block."),
				panel.AddQuery(
					mmo.PromQL(
						`count(kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",manufacturer=~"HPE.*"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("Lenovo BMCs",
				stat.Chart(),
				panel.Description("Total Lenovo BMCs in the selected region and building block."),
				panel.AddQuery(
					mmo.PromQL(
						`count(kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",manufacturer=~"Lenovo.*"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("BULL BMCs",
				stat.Chart(),
				panel.Description("Total BULL (Atos/Eviden) BMCs in the selected region and building block."),
				panel.AddQuery(
					mmo.PromQL(
						`count(kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",manufacturer=~"BULL.*"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("Fsas BMCs",
				stat.Chart(),
				panel.Description("Total Fujitsu (Fsas) BMCs in the selected region and building block."),
				panel.AddQuery(
					mmo.PromQL(
						`count(kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",manufacturer=~"Fsas.*"}) or vector(0)`,
						"$datasource",
					),
				),
			),
		),

		// ── Model Distribution ─────────────────────────────────────────────────────

		dashboard.AddPanelGroup("Model Distribution",
			panelgroup.PanelsPerLine(3),
			panelgroup.PanelHeight(4),
			panelgroup.AddPanel("iDRAC 9 (Gen 16)",
				stat.Chart(),
				panel.Description("Dell BMCs running iDRAC 9 era firmware (16G generation). These expose telemetry via MetricReport push."),
				panel.AddQuery(
					mmo.PromQL(
						`count(kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",model=~"16G.*"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("iDRAC 10 (Gen 17)",
				stat.Chart(),
				panel.Description("Dell BMCs running iDRAC 10 era firmware (17G generation). These expose telemetry via MetricReport push."),
				panel.AddQuery(
					mmo.PromQL(
						`count(kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",model=~"17G.*"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("iLO 6",
				stat.Chart(),
				panel.Description("HPE BMCs running iLO 6 firmware (Gen10 Plus / Gen11 servers)."),
				panel.AddQuery(
					mmo.PromQL(
						`count(kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",model=~"iLO 6.*"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("iLO 7",
				stat.Chart(),
				panel.Description("HPE BMCs running iLO 7 firmware (Gen12 servers)."),
				panel.AddQuery(
					mmo.PromQL(
						`count(kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",model=~"iLO 7.*"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("iRMC S6",
				stat.Chart(),
				panel.Description("Fujitsu BMCs running iRMC S6 (integrated Remote Management Controller generation 6)."),
				panel.AddQuery(
					mmo.PromQL(
						`count(kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",model=~"iRMC S6.*"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("OpenBmc",
				stat.Chart(),
				panel.Description("BMCs running OpenBMC firmware (open-source BMC stack, used by some BULL/Atos servers)."),
				panel.AddQuery(
					mmo.PromQL(
						`count(kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",model=~"OpenBmc.*"}) or vector(0)`,
						"$datasource",
					),
				),
			),
		),

		// ── Fleet Composition ──────────────────────────────────────────────────────

		dashboard.AddPanelGroup("Fleet Composition",
			panelgroup.PanelsPerLine(1),
			panelgroup.PanelHeight(8),
			panelgroup.AddPanel("Telemetry Coverage by Manufacturer / Model",
				table.Table(
					table.WithDefaultColumnHidden(true),
					table.WithColumnSettings([]table.ColumnSettings{
						{Name: "manufacturer", Header: "Manufacturer", EnableSorting: true, Hide: false},
						{Name: "model", Header: "Model", EnableSorting: true, Hide: false},
						{Name: "value #1", Header: "Total", EnableSorting: true, Hide: false},
						{Name: "value #2", Header: "Reporting", EnableSorting: true, Hide: false},
						{Name: "value #3", Header: "Not Reporting", EnableSorting: true, Sort: table.DescSort, Hide: false,
							CellSettings: []table.CellSettings{
								// First match wins: >= 1 (some BMCs not reporting) → red
								{
									Condition: table.Condition{
										Kind: table.RangeConditionKind,
										Spec: &table.RangeConditionSpec{Min: 1},
									},
									TextColor:       "#F2495C",
									BackgroundColor: "#2C1117",
								},
								// Catch-all (0 = all reporting) → green
								{
									Condition: table.Condition{
										Kind: table.RegexConditionKind,
										Spec: &table.RegexConditionSpec{Expr: ".+"},
									},
									TextColor: "#73BF69",
								},
							},
						},
					}),
					table.WithEnableFiltering(true),
					table.Transform([]common.Transform{
						{Kind: common.MergeSeriesKind, Spec: &common.MergeSeriesSpec{}},
					}),
				),
				panel.Description("Fleet hardware composition with telemetry coverage per manufacturer and model. Rows are sorted by the Not Reporting count so models with coverage gaps appear first. Red rows indicate models where some BMCs are not pushing telemetry; green indicates full coverage."),
				// Query A: total BMC count per manufacturer/model
				panel.AddQuery(
					mmo.PromQL(
						`count by(manufacturer,model)(kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",name=~"$node",manufacturer=~"$manufacturer",model=~"$model",manufacturer!=""})`,
						"$datasource",
					),
				),
				// Query B: reporting count per manufacturer/model (0 for models with no reporting BMCs)
				panel.AddQuery(
					mmo.PromQL(
						`count by(manufacturer,model)(`+
							`count by(hostname)(redfish_monitor_reading)`+
							` * on(hostname) group_left(manufacturer,model)`+
							` label_replace(kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",name=~"$node",manufacturer=~"$manufacturer",model=~"$model",manufacturer!=""},"hostname","$1","name","(.*)")`+
							`)`+
							` or count by(manufacturer,model)(kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",name=~"$node",manufacturer=~"$manufacturer",model=~"$model",manufacturer!=""}) * 0`,
						"$datasource",
					),
				),
				// Query C: not-reporting count (total - reporting); drives sort and highlighting
				panel.AddQuery(
					mmo.PromQL(
						`count by(manufacturer,model)(kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",name=~"$node",manufacturer=~"$manufacturer",model=~"$model",manufacturer!=""})`+
							` - on(manufacturer,model) group_left()`+
							` (`+
							`count by(manufacturer,model)(`+
							`count by(hostname)(redfish_monitor_reading)`+
							` * on(hostname) group_left(manufacturer,model)`+
							` label_replace(kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",name=~"$node",manufacturer=~"$manufacturer",model=~"$model",manufacturer!=""},"hostname","$1","name","(.*)")`+
							`)`+
							` or count by(manufacturer,model)(kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",name=~"$node",manufacturer=~"$manufacturer",model=~"$model",manufacturer!=""}) * 0`+
							`)`,
						"$datasource",
					),
				),
			),
		),

		// ── Fleet Inventory ────────────────────────────────────────────────────────

		dashboard.AddPanelGroup("Fleet Inventory",
			panelgroup.PanelsPerLine(1),
			panelgroup.PanelHeight(12),
			panelgroup.AddPanel("BMC Inventory",
				table.Table(
					table.WithDefaultColumnHidden(true),
					table.WithEnableFiltering(true),
					table.WithColumnSettings(inventoryCols),
				),
				panel.Description("Per-BMC inventory showing hardware identity and topology. Select a node from the Node variable above and click the panel link icon to open the BMC Detail dashboard for that host."),
				panel.AddLink("/projects/metal-maintenance-operator/dashboards/bmc-detail",
					link.Name("BMC Detail"),
					link.RenderVariable(true),
				),
				panel.AddQuery(
					mmo.PromQL(
						`kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",name=~"$node",manufacturer=~"$manufacturer",model=~"$model"}`,
						"$datasource",
					),
				),
			),
		),
	)

	exec.BuildDashboard(builder, buildErr)
}
