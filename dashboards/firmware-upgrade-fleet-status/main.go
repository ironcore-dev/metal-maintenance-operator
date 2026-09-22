// Copyright 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"

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
	timeseries "github.com/perses/plugins/timeserieschart/sdk/go"

	mmoquery "github.com/ironcore-dev/metal-maintenance-operator/dashboards/common"
)

const (
	biosVersionMetric = "metal_maintenance_biosversion_info"
	bmcVersionMetric  = "metal_maintenance_bmcversion_info"
)

func main() {
	flag.String("config", "", "topology config YAML path (unused by this dashboard; accepted for CLI consistency)")
	flag.Parse()

	exec := sdk.NewExec()
	builder, buildErr := dashboard.New("firmware-upgrade-fleet-status",
		dashboard.Name("Firmware Upgrade Fleet Status"),
		dashboard.ProjectName("metal-maintenance-operator"),

		// Variables
		dashboard.AddVariable("datasource",
			listvariable.List(
				datasourcevariable.Datasource(promDs.PluginKind),
			),
		),
		dashboard.AddVariable("manufacturer",
			listvariable.List(
				labelvalues.PrometheusLabelValues("manufacturer",
					labelvalues.Matchers(
						biosVersionMetric,
						bmcVersionMetric,
					),
				),
				listvariable.AllowAllValue(true),
				listvariable.AllowMultiple(true),
				listvariable.DisplayName("Manufacturer"),
			),
		),
		dashboard.AddVariable("model",
			listvariable.List(
				labelvalues.PrometheusLabelValues("model",
					labelvalues.Matchers(
						biosVersionMetric+`{manufacturer=~"$manufacturer"}`,
						bmcVersionMetric+`{manufacturer=~"$manufacturer"}`,
					),
				),
				listvariable.AllowAllValue(true),
				listvariable.AllowMultiple(true),
				listvariable.DisplayName("Model"),
			),
		),

		// BIOS state summary
		dashboard.AddPanelGroup("BIOS Firmware Status",
			panelgroup.PanelsPerLine(5),
			panelgroup.PanelHeight(4),
			panelgroup.AddPanel("BIOS Pending",
				stat.Chart(),
				panel.AddQuery(
					mmoquery.PromQL(
						`count(metal_maintenance_biosversion_info{state="Pending",manufacturer=~"$manufacturer",model=~"$model"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("BIOS In Progress",
				stat.Chart(),
				panel.AddQuery(
					mmoquery.PromQL(
						`count(metal_maintenance_biosversion_info{state="InProgress",manufacturer=~"$manufacturer",model=~"$model"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("BIOS Completed",
				stat.Chart(),
				panel.AddQuery(
					mmoquery.PromQL(
						`count(metal_maintenance_biosversion_info{state="Completed",manufacturer=~"$manufacturer",model=~"$model"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("BIOS Failed",
				stat.Chart(),
				panel.AddQuery(
					mmoquery.PromQL(
						`count(metal_maintenance_biosversion_info{state="Failed",manufacturer=~"$manufacturer",model=~"$model"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("BIOS Unknown",
				stat.Chart(),
				panel.AddQuery(
					mmoquery.PromQL(
						`count(metal_maintenance_biosversion_info{state="Unknown",manufacturer=~"$manufacturer",model=~"$model"}) or vector(0)`,
						"$datasource",
					),
				),
			),
		),

		// BMC state summary
		dashboard.AddPanelGroup("BMC Firmware Status",
			panelgroup.PanelsPerLine(5),
			panelgroup.PanelHeight(4),
			panelgroup.AddPanel("BMC Pending",
				stat.Chart(),
				panel.AddQuery(
					mmoquery.PromQL(
						`count(metal_maintenance_bmcversion_info{state="Pending",manufacturer=~"$manufacturer",model=~"$model"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("BMC In Progress",
				stat.Chart(),
				panel.AddQuery(
					mmoquery.PromQL(
						`count(metal_maintenance_bmcversion_info{state="InProgress",manufacturer=~"$manufacturer",model=~"$model"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("BMC Completed",
				stat.Chart(),
				panel.AddQuery(
					mmoquery.PromQL(
						`count(metal_maintenance_bmcversion_info{state="Completed",manufacturer=~"$manufacturer",model=~"$model"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("BMC Failed",
				stat.Chart(),
				panel.AddQuery(
					mmoquery.PromQL(
						`count(metal_maintenance_bmcversion_info{state="Failed",manufacturer=~"$manufacturer",model=~"$model"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("BMC Unknown",
				stat.Chart(),
				panel.AddQuery(
					mmoquery.PromQL(
						`count(metal_maintenance_bmcversion_info{state="Unknown",manufacturer=~"$manufacturer",model=~"$model"}) or vector(0)`,
						"$datasource",
					),
				),
			),
		),

		// In-progress and failed tables
		dashboard.AddPanelGroup("Active Upgrades",
			panelgroup.PanelHeight(8),
			panelgroup.AddPanel("BIOS Upgrades In Progress",
				table.Table(),
				panel.AddQuery(
					mmoquery.PromQL(
						`metal_maintenance_biosversion_info{state="InProgress",manufacturer=~"$manufacturer",model=~"$model"}`,
						"$datasource",
						"{{name}}",
					),
				),
			),
			panelgroup.AddPanel("BMC Upgrades In Progress",
				table.Table(),
				panel.AddQuery(
					mmoquery.PromQL(
						`metal_maintenance_bmcversion_info{state="InProgress",manufacturer=~"$manufacturer",model=~"$model"}`,
						"$datasource",
						"{{name}}",
					),
				),
			),
		),
		dashboard.AddPanelGroup("Failed Upgrades",
			panelgroup.PanelHeight(8),
			panelgroup.AddPanel("BIOS Upgrades Failed",
				table.Table(),
				panel.AddQuery(
					mmoquery.PromQL(
						`metal_maintenance_biosversion_info{state="Failed",manufacturer=~"$manufacturer",model=~"$model"}`,
						"$datasource",
						"{{name}}",
					),
				),
			),
			panelgroup.AddPanel("BMC Upgrades Failed",
				table.Table(),
				panel.AddQuery(
					mmoquery.PromQL(
						`metal_maintenance_bmcversion_info{state="Failed",manufacturer=~"$manufacturer",model=~"$model"}`,
						"$datasource",
						"{{name}}",
					),
				),
			),
		),

		// Controller health
		dashboard.AddPanelGroup("Controller Health",
			panelgroup.PanelHeight(8),
			panelgroup.AddPanel("Reconcile Error Rate",
				timeseries.Chart(),
				panel.AddQuery(
					mmoquery.PromQL(
						`sum by(controller) (rate(controller_runtime_reconcile_errors_total{controller=~"biosversion|bmcversion"}[5m]))`,
						"$datasource",
						"{{controller}}",
					),
				),
			),
			panelgroup.AddPanel("Work Queue Depth",
				timeseries.Chart(),
				panel.AddQuery(
					mmoquery.PromQL(
						`workqueue_depth{name=~"biosversion|bmcversion"}`,
						"$datasource",
						"{{name}}",
					),
				),
			),
			panelgroup.AddPanel("P99 Reconcile Latency",
				timeseries.Chart(),
				panel.AddQuery(
					mmoquery.PromQL(
						`histogram_quantile(0.99, sum by(le, controller) (rate(controller_runtime_reconcile_time_seconds_bucket{controller=~"biosversion|bmcversion"}[5m])))`,
						"$datasource",
						"{{controller}}",
					),
				),
			),
		),
	)

	exec.BuildDashboard(builder, buildErr)
}
