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
	"github.com/perses/perses/go-sdk/panel"
	panelgroup "github.com/perses/perses/go-sdk/panel-group"
	listvariable "github.com/perses/perses/go-sdk/variable/list-variable"

	datasourcevariable "github.com/perses/plugins/datasourcevariable/sdk/go"
	promDs "github.com/perses/plugins/prometheus/sdk/go/datasource"
	labelvalues "github.com/perses/plugins/prometheus/sdk/go/variable/label-values"
	stat "github.com/perses/plugins/statchart/sdk/go"
	table "github.com/perses/plugins/table/sdk/go"
	timeseries "github.com/perses/plugins/timeserieschart/sdk/go"

	mmo "github.com/ironcore-dev/metal-maintenance-operator/dashboards/common"
)

// bbJoin enforces $region/$bb/$node/$manufacturer/$model filtering by joining expr with
// kube_ironcore_bmc_info (topology + hardware identity).
// Event metrics carry no topology labels; all filtering is done via the join.
// kube_ironcore_bmc_info covers every BMC regardless of firmware management enrollment.
func bbJoin(cfg mmo.DashboardConfig, expr string) string {
	bb, nodename, _ := mmo.TopologyMetricLabels(cfg)
	ksmJoin := `label_replace(kube_ironcore_bmc_info{` +
		`region=~"$region",` +
		bb + `=~"$bb",` + nodename + `=~"$node",` +
		`manufacturer=~"$manufacturer",model=~"$model"}, "hostname", "$1", "name", "(.*)")`
	return expr + ` * on(hostname) group_left() ` + ksmJoin
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

	bb, nodename, _ := mmo.TopologyMetricLabels(cfg)

	exec := sdk.NewExec()
	builder, buildErr := dashboard.New("bmc-alerts",
		dashboard.Name("BMC Alert Events"),
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
				labelvalues.PrometheusLabelValues(nodename,
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
					labelvalues.Matchers(`kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",manufacturer!=""}`),
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
					labelvalues.Matchers(`kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",manufacturer=~"$manufacturer",model!=""}`),
				),
				listvariable.AllowAllValue(true),
				listvariable.AllowMultiple(true),
				listvariable.DefaultValue("$__all"),
				listvariable.DisplayName("Model"),
			),
		),

		// ── Fleet Overview ─────────────────────────────────────────────────────────

		dashboard.AddPanelGroup("Fleet Overview",
			panelgroup.PanelsPerLine(1),
			panelgroup.PanelHeight(4),
			panelgroup.AddPanel("Total BMCs in Selected Building Block",
				stat.Chart(),
				panel.Description("Total BMC resources managed in the selected building block. Use this as the denominator when interpreting pipeline health and alert counts below."),
				panel.AddQuery(
					mmo.PromQL(
						`count(kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",`+nodename+`=~"$node",manufacturer=~"$manufacturer",model=~"$model"}) or vector(0)`,
						"$datasource",
					),
				),
			),
		),

		// ── Fleet Composition ──────────────────────────────────────────────────────

		dashboard.AddPanelGroup("Fleet Composition",
			panelgroup.PanelsPerLine(1),
			panelgroup.PanelHeight(12),
			panelgroup.AddPanel("BMC Fleet — Alert Summary",
				table.Table(
					table.WithDefaultColumnHidden(true),
					table.WithEnableFiltering(true),
					table.WithColumnSettings([]table.ColumnSettings{
						{Name: "name", Header: "BMC", EnableSorting: true, Hide: false},
						{Name: "manufacturer", Header: "Manufacturer", EnableSorting: true, Hide: false},
						{Name: "model", Header: "Model", EnableSorting: true, Hide: false},
						{Name: "firmware_version", Header: "Firmware", EnableSorting: true, Hide: false},
						{Name: "region", Header: "Region", EnableSorting: true, Hide: false},
						{Name: bb, Header: cfg.Topology.BB.DisplayName, EnableSorting: true, Hide: false},
						{Name: "value #1", Header: "Critical Alerts — Last 1 Hour", EnableSorting: true, Sort: table.DescSort, Hide: false,
							CellSettings: []table.CellSettings{
								{
									Condition: table.Condition{
										Kind: table.RangeConditionKind,
										Spec: &table.RangeConditionSpec{Min: 1},
									},
									TextColor:       "#F2495C",
									BackgroundColor: "#2C1117",
								},
								{
									Condition: table.Condition{
										Kind: table.RegexConditionKind,
										Spec: &table.RegexConditionSpec{Expr: ".+"},
									},
									TextColor: "#73BF69",
								},
							},
						},
						{Name: "value #2", Header: "Warning Alerts — Last 1 Hour", EnableSorting: true, Hide: false,
							CellSettings: []table.CellSettings{
								{
									Condition: table.Condition{
										Kind: table.RangeConditionKind,
										Spec: &table.RangeConditionSpec{Min: 1},
									},
									TextColor:       "#FF9830",
									BackgroundColor: "#2C1717",
								},
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
					table.Transform([]common.Transform{
						{Kind: common.MergeSeriesKind, Spec: &common.MergeSeriesSpec{}},
					}),
				),
				panel.Description("All BMCs in the selected building block sorted by critical alert count. Each row shows the BMC identity and how many new alert events it generated in the last hour. Red cells indicate active critical conditions; orange cells indicate warnings. Zero is the healthy steady state for both columns."),
				// Critical alerts per BMC: join hostname→name, propagate bmc_info labels; 0 for BMCs with no alerts
				panel.AddQuery(
					mmo.PromQL(
						`sum by(name, manufacturer, model, firmware_version, region, `+bb+`)(`+
							`label_replace(round(increase(redfish_event_alert_total{severity="Critical"}[1h])), "name", "$1", "hostname", "(.*)")`+
							` * on(name) group_left(manufacturer, model, firmware_version, region, `+bb+`)`+
							` kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",`+nodename+`=~"$node",manufacturer=~"$manufacturer",model=~"$model"}`+
							`)`+
							` or sum by(name, manufacturer, model, firmware_version, region, `+bb+`)(`+
							`kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",`+nodename+`=~"$node",manufacturer=~"$manufacturer",model=~"$model"}`+
							`) * 0`,
						"$datasource",
					),
				),
				// Warning alerts per BMC: same pattern for severity="Warning"
				panel.AddQuery(
					mmo.PromQL(
						`sum by(name, manufacturer, model, firmware_version, region, `+bb+`)(`+
							`label_replace(round(increase(redfish_event_alert_total{severity="Warning"}[1h])), "name", "$1", "hostname", "(.*)")`+
							` * on(name) group_left(manufacturer, model, firmware_version, region, `+bb+`)`+
							` kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",`+nodename+`=~"$node",manufacturer=~"$manufacturer",model=~"$model"}`+
							`)`+
							` or sum by(name, manufacturer, model, firmware_version, region, `+bb+`)(`+
							`kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",`+nodename+`=~"$node",manufacturer=~"$manufacturer",model=~"$model"}`+
							`) * 0`,
						"$datasource",
					),
				),
			),
		),

		// ── Pipeline Health ────────────────────────────────────────────────────────

		dashboard.AddPanelGroup("Pipeline Health",
			panelgroup.PanelsPerLine(4),
			panelgroup.PanelHeight(4),
			panelgroup.AddPanel("Healthy Hosts (age < 5 min)",
				stat.Chart(),
				panel.Description("Hosts where the last successful pipeline health-check is less than 5 minutes old. This is the expected state for all hosts. Any host not counted here needs investigation."),
				panel.AddQuery(
					mmo.PromQL(
						`count(`+
							`(time() - `+bbJoin(cfg, `redfish_event_pipeline_test_timestamp{result="success"}`)+`) < 300`+
							`) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("Stale Hosts (age > 15 min)",
				stat.Chart(),
				panel.Description("Number of hosts where the last successful health-check is more than 15 minutes old, indicating likely pipeline disruption. Zero is the healthy steady state."),
				panel.AddQuery(
					mmo.PromQL(
						`count(`+
							`label_replace(kube_ironcore_bmc_info{region=~"$region",`+bb+`=~"$bb",`+nodename+`=~"$node",manufacturer=~"$manufacturer",model=~"$model"}, "hostname", "$1", "name", "(.*)") `+
							`unless on(hostname) `+
							`(redfish_event_pipeline_test_timestamp{result="success"} > (time() - 900))`+
							`) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("Worst Pipeline Age (s)",
				stat.Chart(),
				panel.Description("Highest pipeline age across all selected hosts. If this exceeds the configured test interval the worst-performing host is not receiving health-check events. A spike here that quickly resolves indicates a transient delivery delay rather than a broken pipeline."),
				panel.AddQuery(
					mmo.PromQL(
						`max(time() - `+bbJoin(cfg, `redfish_event_pipeline_test_timestamp{result="success"}`)+`)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("Median Pipeline Age (s)",
				stat.Chart(),
				panel.Description("Median pipeline age across all selected hosts. A healthy fleet shows a value close to the test interval. A rising median means widespread degradation across the building block, not just an isolated host failure."),
				panel.AddQuery(
					mmo.PromQL(
						`quantile(0.5, time() - `+bbJoin(cfg, `redfish_event_pipeline_test_timestamp{result="success"}`)+`)`,
						"$datasource",
					),
				),
			),
		),
		dashboard.AddPanelGroup("Pipeline Age Distribution",
			panelgroup.PanelsPerLine(1),
			panelgroup.PanelHeight(8),
			panelgroup.AddPanel("Pipeline Health Age — Fleet Percentiles",
				timeseries.Chart(),
				panel.Description("Fleet-wide pipeline age percentiles over time. Max shows the worst-performing host; P90 shows the tail of the distribution; Median shows the typical host. Divergence between max and median points to isolated failures rather than widespread degradation."),
				panel.AddQuery(
					mmo.PromQL(
						`max(time() - `+bbJoin(cfg, `redfish_event_pipeline_test_timestamp{result="success"}`)+`)`,
						"$datasource",
						"max",
					),
				),
				panel.AddQuery(
					mmo.PromQL(
						`quantile(0.9, time() - `+bbJoin(cfg, `redfish_event_pipeline_test_timestamp{result="success"}`)+`)`,
						"$datasource",
						"p90",
					),
				),
				panel.AddQuery(
					mmo.PromQL(
						`quantile(0.5, time() - `+bbJoin(cfg, `redfish_event_pipeline_test_timestamp{result="success"}`)+`)`,
						"$datasource",
						"median",
					),
				),
			),
		),
		dashboard.AddPanelGroup("Per-Host Pipeline Detail",
			panelgroup.PanelsPerLine(1),
			panelgroup.PanelHeight(10),
			panelgroup.AddPanel("Pipeline Health Age by Host",
				table.Table(
					table.WithEnableFiltering(true),
				),
				panel.Description("Current pipeline age per host. Sort by value descending to surface the most degraded hosts immediately. A growing age beyond the configured test interval means health-check events are not being delivered for that host. Use the Node filter above to drill into specific hosts."),
				panel.AddQuery(
					mmo.PromQL(
						`time() - `+bbJoin(cfg, `redfish_event_pipeline_test_timestamp{result="success"}`),
						"$datasource",
						"{{hostname}}",
					),
				),
			),
		),

		// ── Alerts — Last 1 Hour ──────────────────────────────────────────────────

		dashboard.AddPanelGroup("Alerts — Last 1 Hour",
			panelgroup.PanelsPerLine(2),
			panelgroup.PanelHeight(8),
			panelgroup.AddPanel("Critical Alerts",
				stat.Chart(),
				panel.Description(`Total new Critical events across all selected hosts in the last hour. Zero is the expected steady state. Any non-zero value means a hardware component is in or repeatedly entering a critical state — investigate the 24-hour detail table below for the source.`),
				panel.AddQuery(
					mmo.PromQL(
						`round(sum(`+bbJoin(cfg, `increase(redfish_event_alert_total{severity="Critical"}[1h])`)+`)) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("Warning Alerts",
				stat.Chart(),
				panel.Description("Total Warning events in the last hour. Warnings often precede critical failures; a host accumulating warnings before going fully critical provides early indication of hardware degradation. A high warning rate from a single message_id is a reliable predictor of imminent component failure."),
				panel.AddQuery(
					mmo.PromQL(
						`round(sum(`+bbJoin(cfg, `increase(redfish_event_alert_total{severity="Warning"}[1h])`)+`)) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("Critical Alert Rate",
				timeseries.Chart(),
				panel.Description("Rate of new Critical Redfish events per second. This counter is deduplicated by EventID — a single repeated event is counted once per unique occurrence, not once per push. A non-zero sustained rate means a component is continuously transitioning to a critical state; a brief spike followed by silence indicates a transient event that resolved."),
				panel.AddQuery(
					mmo.PromQL(
						bbJoin(cfg, `rate(redfish_event_alert_total{severity="Critical"}[5m])`),
						"$datasource",
						"{{hostname}} / {{component}}",
					),
				),
			),
			panelgroup.AddPanel("Warning Alert Rate",
				timeseries.Chart(),
				panel.Description("Rate of Warning-severity Redfish events. A Warning rate that increases over days before a Critical spike is a classic degradation signature — e.g. a failing drive will accumulate write error warnings before reporting a critical health change."),
				panel.AddQuery(
					mmo.PromQL(
						bbJoin(cfg, `rate(redfish_event_alert_total{severity="Warning"}[5m])`),
						"$datasource",
						"{{hostname}} / {{component}}",
					),
				),
			),
		),

		// ── Alerts — Last 24 Hours ─────────────────────────────────────────────────

		dashboard.AddPanelGroup("Alerts — Last 24 Hours",
			panelgroup.PanelsPerLine(3),
			panelgroup.PanelHeight(4),
			panelgroup.AddPanel("Critical Alerts",
				stat.Chart(),
				panel.Description("Total new Critical events across all selected hosts in the last 24 hours."),
				panel.AddQuery(
					mmo.PromQL(
						`round(sum(`+bbJoin(cfg, `increase(redfish_event_alert_total{severity="Critical"}[24h])`)+`)) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("Warning Alerts",
				stat.Chart(),
				panel.Description("Total Warning events in the last 24 hours."),
				panel.AddQuery(
					mmo.PromQL(
						`round(sum(`+bbJoin(cfg, `increase(redfish_event_alert_total{severity="Warning"}[24h])`)+`)) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("Distinct Hosts Alerting",
				stat.Chart(),
				panel.Description("Number of distinct hosts that emitted at least one alert in the last 24 hours. A sudden increase across a building block may indicate a shared infrastructure problem (power quality, cooling, firmware version) rather than isolated component failures."),
				panel.AddQuery(
					mmo.PromQL(
						`count(count by(hostname)(`+
							bbJoin(cfg, `increase(redfish_event_alert_total[24h])`)+
							` > 0)) or vector(0)`,
						"$datasource",
					),
				),
			),
		),
		dashboard.AddPanelGroup("Alert Detail",
			panelgroup.PanelsPerLine(1),
			panelgroup.PanelHeight(10),
			panelgroup.AddPanel("Alert Count by Host / Component / Message (last 24 h)",
				table.Table(
					table.WithDefaultColumnHidden(true),
					table.WithEnableFiltering(true),
					table.WithColumnSettings([]table.ColumnSettings{
						{Name: "hostname", Header: "Host", EnableSorting: true, Hide: false},
						{Name: "severity", Header: "Severity", EnableSorting: true, Hide: false},
						{Name: "message_id", Header: "Message ID", EnableSorting: true, Hide: false},
						{Name: "component", Header: "Component", EnableSorting: true, Hide: false},
						{Name: "value", Header: "Count (24 h)", EnableSorting: true, Sort: table.DescSort, Hide: false},
					}),
				),
				panel.Description(`All alert events aggregated over the last 24 hours, broken down by host, severity, Redfish message ID, and originating component. The message_id label is a Redfish message registry key (e.g. SYS.1.0.SYS1000 on Dell, iLOEvents.1.0.FanFailed on HPE) that maps to a specific hardware event type in the vendor's documentation. The component label is the trailing segment of the Redfish OriginOfCondition URI — e.g. PowerSupplies/1 identifies PSU1. Sort by count descending to find the noisiest hardware components.`),
				panel.AddQuery(
					mmo.PromQL(
						bbJoin(cfg, `round(increase(redfish_event_alert_total[24h]))`)+` > 0`,
						"$datasource",
						"{{hostname}} {{severity}} {{message_id}} {{component}}",
					),
				),
			),
		),
	)

	exec.BuildDashboard(builder, buildErr)
}
