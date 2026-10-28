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
	timeseries "github.com/perses/plugins/timeserieschart/sdk/go"

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

	exec := sdk.NewExec()
	builder, buildErr := dashboard.New("bmc-detail",
		dashboard.Name("BMC Detail"),
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
				listvariable.DisplayName("Node"),
			),
		),

		// ── BMC Info ───────────────────────────────────────────────────────────────

		dashboard.AddPanelGroup("BMC Info",
			panelgroup.PanelsPerLine(1),
			panelgroup.PanelHeight(3),
			panelgroup.AddPanel("BMC Info",
				table.Table(
					table.WithDefaultColumnHidden(true),
					table.WithColumnSettings([]table.ColumnSettings{
						{Name: "name", Header: "BMC", EnableSorting: true, Hide: false},
						{Name: "manufacturer", Header: "Manufacturer", EnableSorting: true, Hide: false},
						{Name: "model", Header: "BMC Model", EnableSorting: true, Hide: false},
						{Name: "firmware_version", Header: "Firmware", EnableSorting: true, Hide: false},
						{Name: "region", Header: "Region", EnableSorting: true, Hide: false},
						{Name: bb, Header: cfg.Topology.BB.DisplayName, EnableSorting: true, Hide: false},
					}),
				),
				panel.Description("Hardware identity and topology for the selected BMC."),
				panel.AddQuery(
					mmo.PromQL(
						`kube_ironcore_bmc_info{name=~"$node"}`,
						"$datasource",
					),
				),
			),
		),

		// ── System Power ───────────────────────────────────────────────────────────

		dashboard.AddPanelGroup("System Power",
			panelgroup.PanelsPerLine(3),
			panelgroup.PanelHeight(4),
			panelgroup.AddPanel("Average System Power (W)",
				stat.Chart(),
				panel.Description("Current average power draw per selected host. High sustained values relative to the server's rated TDP may indicate thermal events forcing fan power overhead, or unexpectedly compute-intensive workloads.\n\n**Vendor support:** Dell, HPE (metrics: AverageConsumedWatts, SystemInputPower, SystemPowerConsumption). Lenovo not observed in current fleet."),
				panel.AddQuery(
					mmo.PromQL(
						`avg by(hostname)(`+mmo.BBJoinNode(cfg, `redfish_canonical:power_consumed_watts`)+`)`,
						"$datasource",
						"{{hostname}}",
					),
				),
			),
			panelgroup.AddPanel("Peak System Power (W)",
				stat.Chart(),
				panel.Description("Highest instantaneous power recorded. Useful for confirming servers remain within PSU headroom and for sizing future capacity in the same building block.\n\n**Vendor support:** HPE iLO only (metric: MaxConsumedWatts). Dell and Lenovo do not report peak power via MetricReports."),
				panel.AddQuery(
					mmo.PromQL(
						`max by(hostname)(`+mmo.BBJoinNode(cfg, `redfish_canonical:power_max_watts`)+`)`,
						"$datasource",
						"{{hostname}}",
					),
				),
			),
			panelgroup.AddPanel("Minimum System Power (W)",
				stat.Chart(),
				panel.Description("Lowest power observed. A large gap between minimum and average suggests bursty workloads; compare with CPU utilisation panels to distinguish idle vs. low-frequency states.\n\n**Vendor support:** HPE iLO only (metric: MinConsumedWatts). Dell and Lenovo do not report minimum power via MetricReports."),
				panel.AddQuery(
					mmo.PromQL(
						`min by(hostname)(`+mmo.BBJoinNode(cfg, `redfish_canonical:power_min_watts`)+`)`,
						"$datasource",
						"{{hostname}}",
					),
				),
			),
		),
		dashboard.AddPanelGroup("System Power Over Time",
			panelgroup.PanelsPerLine(1),
			panelgroup.PanelHeight(8),
			panelgroup.AddPanel("System Power Over Time (W)",
				timeseries.Chart(),
				panel.Description("Power consumption trajectory per host. Correlate with CPU utilisation and frequency panels to identify whether a rise in power is workload-driven or thermally induced.\n\n**Vendor support:** Dell, HPE (metrics: AverageConsumedWatts, SystemInputPower, SystemPowerConsumption). Multiple series may appear for the same host when more than one metric ID matches."),
				panel.AddQuery(
					mmo.PromQL(
						mmo.BBJoinNode(cfg, `redfish_canonical:power_consumed_watts`),
						"$datasource",
						"{{hostname}} ({{metric_id}})",
					),
				),
			),
		),

		// ── PSU Power ──────────────────────────────────────────────────────────────

		dashboard.AddPanelGroup("PSU Power",
			panelgroup.PanelsPerLine(2),
			panelgroup.PanelHeight(8),
			panelgroup.AddPanel("PSU Input Power (W)",
				timeseries.Chart(),
				panel.Description("AC input watts drawn per power supply unit. Persistent imbalance between PSU0 and PSU1 on the same host indicates uneven load distribution. A sudden drop to zero on one PSU is a strong indicator of PSU failure — cross-reference with alert panels.\n\n**Vendor support:** Lenovo only (metric: AveragePowerSupplyInput). Dell and HPE do not expose per-PSU wattage via MetricReports."),
				panel.AddQuery(
					mmo.PromQL(
						`label_replace(`+
							mmo.BBJoinNode(cfg, `redfish_canonical:psu_input_power_watts`)+`,`+
							`"psu","$1","origin_context",".*/PowerSupplies/([^/]+)/.*")`,
						"$datasource",
						"{{hostname}} PSU{{psu}}",
					),
				),
			),
			panelgroup.AddPanel("PSU Output Power (W)",
				timeseries.Chart(),
				panel.Description("DC output watts delivered per PSU. Subtracting output from input gives the heat generated by the PSU itself. A PSU delivering significantly less than it receives (low efficiency) may be operating outside its optimal load range or approaching end-of-life.\n\n**Vendor support:** Lenovo only (metric: AveragePowerSupplyOutput). Dell and HPE do not expose per-PSU wattage via MetricReports."),
				panel.AddQuery(
					mmo.PromQL(
						`label_replace(`+
							mmo.BBJoinNode(cfg, `redfish_canonical:psu_output_power_watts`)+`,`+
							`"psu","$1","origin_context",".*/PowerSupplies/([^/]+)/.*")`,
						"$datasource",
						"{{hostname}} PSU{{psu}}",
					),
				),
			),
		),

		// ── Component Power ────────────────────────────────────────────────────────

		dashboard.AddPanelGroup("Component Power",
			panelgroup.PanelsPerLine(2),
			panelgroup.PanelHeight(8),
			panelgroup.AddPanel("Per-CPU Socket Power (W)",
				timeseries.Chart(),
				panel.Description("Power draw per CPU socket. Sustained imbalance between sockets on the same host with symmetric workloads may indicate NUMA-skewed scheduling. A socket reading zero may be halted or in a deep power-save state — verify CPU state with frequency panels.\n\n**Vendor support:** HPE iLO only (metrics: CPU0Power, CPU1Power). Dell and Lenovo do not report per-socket CPU power via MetricReports."),
				panel.AddQuery(
					mmo.PromQL(
						mmo.BBJoinNode(cfg, `redfish_canonical:cpu0_power_watts`),
						"$datasource",
						"{{hostname}} cpu0",
					),
				),
				panel.AddQuery(
					mmo.PromQL(
						mmo.BBJoinNode(cfg, `redfish_canonical:cpu1_power_watts`),
						"$datasource",
						"{{hostname}} cpu1",
					),
				),
			),
			panelgroup.AddPanel("Component Power Breakdown (W)",
				timeseries.Chart(),
				panel.Description("Power split across CPU, DIMM, and GPU subsystems. Memory-intensive workloads show elevated DimmWatts. A host running GPU workloads will show GpuWatts; its absence on a GPU node indicates the accelerator is idle or not yet under load.\n\n**Vendor support:** CPU and memory power available on Dell and HPE. GPU power (GpuWatts) is HPE only."),
				panel.AddQuery(
					mmo.PromQL(
						mmo.BBJoinNode(cfg, `redfish_canonical:cpu_total_power_watts`),
						"$datasource",
						"{{hostname}} CPU",
					),
				),
				panel.AddQuery(
					mmo.PromQL(
						mmo.BBJoinNode(cfg, `redfish_canonical:memory_total_power_watts`),
						"$datasource",
						"{{hostname}} Memory",
					),
				),
				panel.AddQuery(
					mmo.PromQL(
						mmo.BBJoinNode(cfg, `redfish_canonical:gpu_power_watts`),
						"$datasource",
						"{{hostname}} GPU",
					),
				),
			),
		),

		// ── Power Limits ───────────────────────────────────────────────────────────

		dashboard.AddPanelGroup("Power Limits",
			panelgroup.PanelsPerLine(4),
			panelgroup.PanelHeight(4),
			panelgroup.AddPanel("Power Cap (W)",
				stat.Chart(),
				panel.Description("Configured chassis power cap. If actual consumption in the System Power panel approaches this value, the BMC will begin CPU throttling, increasing request latency. A cap below expected peak TDP indicates a deliberate energy budget constraint for this building block.\n\n**Vendor support:** HPE iLO only (metric: Cap). Dell and Lenovo do not expose a configurable power cap via MetricReports."),
				panel.AddQuery(
					mmo.PromQL(
						`max by(hostname)(`+mmo.BBJoinNode(cfg, `redfish_canonical:power_cap_watts`)+`)`,
						"$datasource",
						"{{hostname}}",
					),
				),
			),
			panelgroup.AddPanel("CPU Max TDP (W)",
				stat.Chart(),
				panel.Description("CPU thermal design power ceiling. Sustained operation at this level means the CPU is at its thermal or power limit; compare with frequency panels — throttling will show as a frequency drop.\n\n**Vendor support:** HPE iLO only (metric: CpuMax)."),
				panel.AddQuery(
					mmo.PromQL(
						`max by(hostname)(`+mmo.BBJoinNode(cfg, `redfish_monitor_reading{metric_id="CpuMax"}`)+`)`,
						"$datasource",
						"{{hostname}}",
					),
				),
			),
			panelgroup.AddPanel("CPU Cap Limit (W)",
				stat.Chart(),
				panel.Description("Active CPU power cap enforced by the BMC power policy. A value below CpuMax means a power-capping policy is reducing CPU performance; this is intentional in environments with strict per-rack power budgets.\n\n**Vendor support:** HPE iLO only (metric: CpuCapLim)."),
				panel.AddQuery(
					mmo.PromQL(
						`max by(hostname)(`+mmo.BBJoinNode(cfg, `redfish_monitor_reading{metric_id="CpuCapLim"}`)+`)`,
						"$datasource",
						"{{hostname}}",
					),
				),
			),
			panelgroup.AddPanel("CPU Power Save Limit (W)",
				stat.Chart(),
				panel.Description("Power target when the CPU power-save policy is active. Servers operating at this level trade throughput for energy efficiency; this is expected during off-peak hours but may cause latency issues for interactive workloads.\n\n**Vendor support:** HPE iLO only (metric: CpuPwrSavLim)."),
				panel.AddQuery(
					mmo.PromQL(
						`max by(hostname)(`+mmo.BBJoinNode(cfg, `redfish_monitor_reading{metric_id="CpuPwrSavLim"}`)+`)`,
						"$datasource",
						"{{hostname}}",
					),
				),
			),
		),

		// ── CPU Utilization ────────────────────────────────────────────────────────

		dashboard.AddPanelGroup("CPU Utilization",
			panelgroup.PanelsPerLine(3),
			panelgroup.PanelHeight(8),
			panelgroup.AddPanel("Average CPU Utilization (%)",
				timeseries.Chart(),
				panel.Description("BMC-reported average utilisation across all cores. Complements OS-level CPU metrics — disagreement between the two can surface hyperthreading, frequency scaling, or SMT effects invisible to the OS scheduler.\n\n**Vendor support:** Dell, HPE, Lenovo (metrics: CPUUtil, CPUUsage, AverageCPUUtilization)."),
				panel.AddQuery(
					mmo.PromQL(
						mmo.BBJoinNode(cfg, `redfish_canonical:cpu_utilization_pct`),
						"$datasource",
						"{{hostname}}",
					),
				),
			),
			panelgroup.AddPanel("CPU Interconnect Utilization (%)",
				timeseries.Chart(),
				panel.Description("Utilisation of the inter-socket interconnect (UPI/QPI). High values on multi-socket hosts indicate workloads with heavy cross-NUMA memory traffic; consider NUMA-aware placement or binding workloads to a single socket.\n\n**Vendor support:** HPE iLO only (metric: CPUICUtil)."),
				panel.AddQuery(
					mmo.PromQL(
						mmo.BBJoinNode(cfg, `redfish_canonical:cpu_interconnect_util_pct`),
						"$datasource",
						"{{hostname}}",
					),
				),
			),
			panelgroup.AddPanel("CPU CUPS Utilization (%)",
				timeseries.Chart(),
				panel.Description("Aggregate CPU utilisation via the CUPS (CPU Utilization, Power, and Scalability) metric.\n\n**Vendor support:** Dell iDRAC10, HPE iLO 7 (metrics: AggregateUsage, AggregateCUPSUsage). Not available on Dell iDRAC9 (16G) or Lenovo."),
				panel.AddQuery(
					mmo.PromQL(
						mmo.BBJoinNode(cfg, `redfish_canonical:cpu_cups_util_pct`),
						"$datasource",
						"{{hostname}}",
					),
				),
			),
		),

		// ── CPU Frequency ──────────────────────────────────────────────────────────

		dashboard.AddPanelGroup("CPU Frequency",
			panelgroup.PanelsPerLine(1),
			panelgroup.PanelHeight(8),
			panelgroup.AddPanel("Per-Socket CPU Frequency (MHz)",
				timeseries.Chart(),
				panel.Description("Average operating frequency per CPU socket. Sustained frequency below the rated base clock indicates active thermal throttling or power capping in effect. Frequency divergence between sockets on identical workloads points to a hardware asymmetry or an uneven cooling situation within the chassis.\n\n**Vendor support:** HPE iLO (metrics: AvgCPU0Freq–AvgCPU3Freq), Dell iDRAC (metric: AvgFrequencyAcrossCores — single aggregate, not per-socket). Not available on Lenovo."),
				panel.AddQuery(
					mmo.PromQL(
						mmo.BBJoinNode(cfg, `redfish_canonical:cpu_avg_freq_mhz`),
						"$datasource",
						"{{hostname}} {{metric_id}}",
					),
				),
			),
		),

		// ── Memory ─────────────────────────────────────────────────────────────────

		dashboard.AddPanelGroup("Memory",
			panelgroup.PanelsPerLine(2),
			panelgroup.PanelHeight(8),
			panelgroup.AddPanel("Average Memory Utilization (%)",
				timeseries.Chart(),
				panel.Description("DRAM capacity used as a percentage. Sustained near-100% utilisation increases DRAM thermal throttling risk; correlate with Memory Bus Utilization to distinguish capacity pressure from bandwidth pressure.\n\n**Vendor support:** Dell, Lenovo (metrics: MemoryUsage, AverageMemoryUtilization). HPE iLO does not report memory utilization via MetricReports."),
				panel.AddQuery(
					mmo.PromQL(
						mmo.BBJoinNode(cfg, `redfish_canonical:memory_util_pct`),
						"$datasource",
						"{{hostname}}",
					),
				),
			),
			panelgroup.AddPanel("Memory Bus Utilization (%)",
				timeseries.Chart(),
				panel.Description("Memory bandwidth utilisation on the memory bus. High bus utilisation with moderate CPU utilisation is characteristic of memory-bandwidth-bound workloads. Combine with CPU Interconnect Utilization to distinguish local-socket vs. cross-socket memory pressure.\n\n**Vendor support:** HPE iLO only (metric: MemoryBusUtil)."),
				panel.AddQuery(
					mmo.PromQL(
						mmo.BBJoinNode(cfg, `redfish_canonical:memory_bus_util_pct`),
						"$datasource",
						"{{hostname}}",
					),
				),
			),
		),

		// ── Thermal ────────────────────────────────────────────────────────────────

		dashboard.AddPanelGroup("Thermal",
			panelgroup.PanelsPerLine(1),
			panelgroup.PanelHeight(8),
			panelgroup.AddPanel("Ambient Temperature (°C)",
				timeseries.Chart(),
				panel.Description("Chassis intake ambient temperature. A rising trend narrows the thermal headroom available before CPU or DIMM throttling activates. Correlate with power and fan panels (when available) to assess whether cooling is keeping pace with thermal load.\n\n**Vendor support:** HPE iLO only (metric: AmbTemp). Dell reports per-sensor temperatures via the Per-Sensor Temperature panel instead."),
				panel.AddQuery(
					mmo.PromQL(
						mmo.BBJoinNode(cfg, `redfish_canonical:inlet_temperature_celsius`),
						"$datasource",
						"{{hostname}}",
					),
				),
			),
			panelgroup.AddPanel("Per-Sensor Temperature (°C)",
				timeseries.Chart(),
				panel.Description("Individual temperature sensor readings (CPU die, DIMM, inlet, exhaust). Only visible when the BMC's Thermal MetricReportDefinition is active. A sensor approaching its UpperCaution threshold is an early warning of a thermal event; a reading already above UpperCritical will trigger BMC-side CPU throttling.\n\n**Vendor support:** Dell iDRAC only (metric: TemperatureReading). HPE iLO reports only the ambient inlet temperature, not individual sensor readings, via MetricReports."),
				panel.AddQuery(
					mmo.PromQL(
						`label_replace(`+
							mmo.BBJoinNode(cfg, `redfish_canonical:temperature_reading_celsius`)+`,`+
							`"sensor","$1","sensor_id","([^#/]+).*")`,
						"$datasource",
						"{{hostname}} {{sensor}}",
					),
				),
			),
			panelgroup.AddPanel("Fan Speed (RPM)",
				timeseries.Chart(),
				panel.Description("Fan RPM per fan unit. Elevated RPM across all fans indicates the BMC is reacting to a thermal event. A single fan at 0 RPM that should be running signals a fan failure and will cause the BMC to compensate by spinning remaining fans harder. Only visible when the BMC's Fan MetricReportDefinition is enabled.\n\n**Vendor support:** Dell iDRAC only (metric: RPMReading). HPE iLO does not report fan speeds via MetricReports."),
				panel.AddQuery(
					mmo.PromQL(
						mmo.BBJoinNode(cfg, `redfish_canonical:fan_speed_rpm`),
						"$datasource",
						"{{hostname}} {{origin_context}}",
					),
				),
			),
		),
	)

	exec.BuildDashboard(builder, buildErr)
}
