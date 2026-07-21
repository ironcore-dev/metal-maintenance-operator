// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package subscriptions_test

// Shared test constants — pulled out to satisfy goconst across the
// package's _test.go files.
const (
	testBMCName        = "bmc-1"
	testBMCCredsName   = "bmc-1-creds"
	vendorDellInc      = "Dell Inc."
	vendorHPE          = "HPE"
	modelR650          = "R650"
	modelPowerEdgeR750 = "PowerEdge R750"
	firmware5_10_0     = "5.10.0"
	subsFinalizer      = "telemetry.metal.ironcore.dev/subscriptions"
	secretUsernameKey  = "username"
	testUsername       = "admin"

	// reconciler-specific:
	testReceiverURL         = "http://recv:9092"
	metricReportDest        = "http://recv:9092/serverevents/metricsreport/bmc-1"
	alertsDest              = "http://recv:9092/serverevents/alerts/bmc-1"
	eventFormatMetricReport = "MetricReport"
	eventFormatEvent        = "Event"
	subURIMetric            = "/redfish/v1/EventService/Subscriptions/m"
	subURIAlert             = "/redfish/v1/EventService/Subscriptions/a"
)
