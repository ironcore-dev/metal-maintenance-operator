// Copyright 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// Package common provides shared dashboard-as-code helpers for the metal-maintenance-operator dashboards.
package common

import (
	"github.com/perses/perses/go-sdk/query"
	"github.com/perses/spec/go/plugin"
)

// promQuerySpec mirrors the Prometheus time-series query plugin spec, but uses
// interface{} for Datasource so that a bare variable reference like "$datasource"
// serialises as a JSON string rather than an object.  The SDK's typed
// datasource.Selector always produces {"kind":"...","name":"..."} which Perses
// treats as a literal lookup rather than a variable substitution.
type promQuerySpec struct {
	Datasource       interface{} `json:"datasource,omitempty"`
	Query            string      `json:"query"`
	SeriesNameFormat string      `json:"seriesNameFormat,omitempty"`
}

// PromQL builds a Prometheus time-series query option.  datasource should be a
// variable reference like "$datasource"; seriesNameFormat is optional.
func PromQL(expr, datasource string, seriesNameFormat ...string) query.Option {
	spec := promQuerySpec{
		Datasource: datasource,
		Query:      expr,
	}
	if len(seriesNameFormat) > 0 {
		spec.SeriesNameFormat = seriesNameFormat[0]
	}
	return query.Option{
		Kind: plugin.KindTimeSeriesQuery,
		Plugin: plugin.Plugin{
			Kind: "PrometheusTimeSeriesQuery",
			Spec: spec,
		},
	}
}
