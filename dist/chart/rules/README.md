# Redfish Canonical Recording Rules

Cross-vendor Redfish metric normalization. These rules materialize stable
`redfish_canonical:*` time series from vendor-specific `metric_id` label values,
so dashboards and alerts work identically regardless of BMC manufacturer.

## Canonical Metric Mapping

| Canonical name | HPE iLO (`metric_id`) | Dell iDRAC (`metric_id`) | Lenovo XCC (`metric_id`) |
|---|---|---|---|
| `cpu_utilization_pct` | `CPUUtil` | `CPUUsage`, `CPUUsagePctReading` | `AverageCPUUtilization` |
| `memory_util_pct` | — | `MemoryUsage`, `MemoryUsagePctReading` | `AverageMemoryUtilization` |
| `memory_bus_util_pct` | `MemoryBusUtil` | — | — |
| `io_bus_util_pct` | `IOBusUtil` | `IOUsage`, `IOUsagePctReading` | — |
| `cpu_interconnect_util_pct` | `CPUICUtil` *(Gen11)* | — | — |
| `cpu_avg_freq_mhz` | `AvgCPU0Freq`, `AvgCPU1Freq` | — | — |
| `cpu_cups_util_pct` | `AggregateCUPSUsage` *(iLO7)* | `AggregateUsage` | — |
| `power_consumed_watts` | `AverageConsumedWatts` | `SystemPowerConsumption`, `SystemInputPower` | — ¹ |
| `power_min_watts` | `MinConsumedWatts` | — | — |
| `power_max_watts` | `MaxConsumedWatts` | — | — |
| `power_cap_watts` | `Cap` | — | — |
| `psu_input_power_watts` | — | — | `AveragePowerSupplyInput` |
| `psu_output_power_watts` | — | — | `AveragePowerSupplyOutput` |
| `cpu_total_power_watts` | `CpuWatts` | `TotalCPUPower` | — ¹ |
| `memory_total_power_watts` | `DimmWatts` | `TotalMemoryPower` | — |
| `gpu_power_watts` | `GpuWatts` | `TotalGPUPower` | — |
| `cpu0_power_watts` | `CPU0Power` | `CPUPower` | — ¹ |
| `cpu1_power_watts` | `CPU1Power` | — | — |
| `fan_total_power_watts` | — | `TotalFanPower` | — |
| `inlet_temperature_celsius` | `AmbTemp` ² | `SystemAvgIntelTempHour` ² | — ¹ |
| `temperature_reading_celsius` | — ³ | `TemperatureReading` | — ³ |
| `fan_speed_rpm` | — ³ | `RPMReading` | — ³ |

¹ Lenovo gap: the relevant MetricReportDefinition groups (`PowerMetrics`,
`CPUPowerMetrics`, `InletAirTemp`, `CPUTemp`) have no `MetricId` field — MMO
drops these silently at `metricsink.go:86-88`. Fixing requires a future
`toSamples()` change to fall back to `MetricProperty`.

² Semantics differ: HPE `AmbTemp` is an instantaneous reading; Dell
`SystemAvgIntelTempHour` is a 1-hour rolling average.

³ Available via Redfish polling only for HPE and Lenovo; Dell pushes these via
TelemetryService.

## Adding Custom Rules

To deploy additional rules without modifying the chart, use the top-level
`extraObjects` value to inject a PrometheusRule CR directly:

```yaml
extraObjects:
  - apiVersion: monitoring.coreos.com/v1
    kind: PrometheusRule
    metadata:
      name: mmo-redfish-custom
      labels:
        prometheus: my-prometheus-instance  # must match prometheusRules.labels.prometheus
    spec:
      groups:
        - name: my_custom_rules
          rules:
            - record: redfish_canonical:my_metric
              expr: redfish_monitor_reading{metric_id="MyVendorMetricId"}
```

## Adding New Vendors or Firmware

When the `RedfishUnmappedMetricId` alert fires:

1. Identify the `metric_id` value and which vendor it came from
2. Add it to the matching `metric_id=~"..."` regex in `redfish-canonical.rules`
3. Add the mapping to the table above
4. Dashboard panels never change — they query stable canonical names
