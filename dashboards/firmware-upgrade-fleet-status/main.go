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
					labelvalues.Datasource("$datasource"),
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
					labelvalues.Datasource("$datasource"),
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
				panel.Description("Servers queued for a BIOS firmware upgrade that have not yet started. A count that does not decrease over time may indicate that upgrades are not progressing. Check whether new servers are entering Pending and review the Controller Health panels below."),
				panel.AddQuery(
					mmoquery.PromQL(
						`count(metal_maintenance_biosversion_info{state="Pending",manufacturer=~"$manufacturer",model=~"$model"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("BIOS In Progress",
				stat.Chart(),
				panel.Description("Servers currently undergoing a BIOS firmware upgrade. This count should be small and transient. A server remaining In Progress beyond the expected upgrade duration (typically a few minutes for BIOS) indicates a stuck upgrade."),
				panel.AddQuery(
					mmoquery.PromQL(
						`count(metal_maintenance_biosversion_info{state="InProgress",manufacturer=~"$manufacturer",model=~"$model"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("BIOS Completed",
				stat.Chart(),
				panel.Description("Servers that have successfully reached the target BIOS version. During an upgrade campaign this count should grow steadily. When the Pending and In Progress counts reach zero the campaign is complete."),
				panel.AddQuery(
					mmoquery.PromQL(
						`count(metal_maintenance_biosversion_info{state="Completed",manufacturer=~"$manufacturer",model=~"$model"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("BIOS Failed",
				stat.Chart(),
				panel.Description("Servers where the BIOS upgrade has failed. Any non-zero count requires investigation — a single model-specific failure often predicts failures on other servers of the same model. See the Failed Upgrades table below for details."),
				panel.AddQuery(
					mmoquery.PromQL(
						`count(metal_maintenance_biosversion_info{state="Failed",manufacturer=~"$manufacturer",model=~"$model"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("BIOS Unknown",
				stat.Chart(),
				panel.Description("Servers whose BIOS upgrade state is reported as Unknown. Check the BIOSVersion resource and controller logs to determine why the state is missing or explicitly Unknown."),
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
				panel.Description("Servers queued for a BMC firmware upgrade that have not yet started. BMC upgrades involve a BMC reboot. Use this count to monitor work waiting to start."),
				panel.AddQuery(
					mmoquery.PromQL(
						`count(metal_maintenance_bmcversion_info{state="Pending",manufacturer=~"$manufacturer",model=~"$model"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("BMC In Progress",
				stat.Chart(),
				panel.Description("Servers currently undergoing a BMC firmware upgrade. BMC upgrades require a BMC reboot cycle (typically 10–20 minutes), so a small In Progress count is expected and normal during a campaign. A server stuck here beyond 30 minutes warrants investigation."),
				panel.AddQuery(
					mmoquery.PromQL(
						`count(metal_maintenance_bmcversion_info{state="InProgress",manufacturer=~"$manufacturer",model=~"$model"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("BMC Completed",
				stat.Chart(),
				panel.Description("Servers that have successfully completed the BMC firmware upgrade and are running the target version. The rising slope of this count over the campaign duration indicates the upgrade throughput of the operator."),
				panel.AddQuery(
					mmoquery.PromQL(
						`count(metal_maintenance_bmcversion_info{state="Completed",manufacturer=~"$manufacturer",model=~"$model"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("BMC Failed",
				stat.Chart(),
				panel.Description("Servers where the BMC upgrade has failed. BMC upgrade failures are more disruptive than BIOS failures because they can leave the BMC in a degraded state. See the Failed Upgrades table below and cross-reference with the BMC Alert Events dashboard for hardware-level signals."),
				panel.AddQuery(
					mmoquery.PromQL(
						`count(metal_maintenance_bmcversion_info{state="Failed",manufacturer=~"$manufacturer",model=~"$model"}) or vector(0)`,
						"$datasource",
					),
				),
			),
			panelgroup.AddPanel("BMC Unknown",
				stat.Chart(),
				panel.Description("Servers whose BMC upgrade state is reported as Unknown. Check the BMCVersion resource and controller logs to determine why the state is missing or explicitly Unknown."),
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
				panel.Description("Servers currently undergoing a BIOS firmware upgrade, showing the CRD resource name, observed version, desired version, and state. Use this to monitor active upgrade progress. A server present here longer than the expected upgrade window needs investigation."),
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
				panel.Description("Servers currently undergoing a BMC firmware upgrade. BMC upgrades are longer than BIOS upgrades due to the BMC reboot cycle. A server present here for more than 30 minutes is likely stuck and may need the upgrade to be retried or manually completed."),
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
				panel.Description("Servers where the BIOS firmware upgrade has failed, showing the observed and desired versions. If multiple servers of the same model are failing, the issue is likely with the firmware image or compatibility rather than an individual server fault."),
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
				panel.Description("Servers where the BMC firmware upgrade has failed. A failed BMC upgrade may leave the BMC temporarily unreachable; cross-reference with the BMC Alert Events dashboard and check BMC connectivity before retrying. Repeated failure on the same server may indicate a hardware fault."),
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
				panel.Description("Rate of reconciliation errors per controller per second. Sustained non-zero values mean the controller is repeatedly failing to process work items — check operator logs for the underlying cause. A spike during an upgrade campaign followed by recovery is normal (e.g. BMC temporarily unreachable during reboot)."),
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
				panel.Description("Number of items waiting to be processed by each controller. A growing queue that does not drain means the controller cannot keep up with incoming work. Combined with a non-zero error rate, a growing queue depth indicates a systemic problem requiring operator intervention."),
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
				panel.Description("99th percentile time to complete a single reconciliation loop. An active BMC upgrade campaign does not by itself explain elevated per-reconciliation latency; elevated latency on either controller warrants investigation."),
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
