// Copyright 2025 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// Package common provides shared dashboard-as-code helpers for the metal-maintenance-operator dashboards.
package common

import (
	"github.com/perses/perses/go-sdk/query"
	promquery "github.com/perses/plugins/prometheus/sdk/go/query"
)

// PromQL builds a Prometheus time-series query option. datasource should be a
// variable reference like "$datasource"; seriesNameFormat is optional.
func PromQL(expr, datasource string, seriesNameFormat ...string) query.Option {
	opts := []promquery.Option{
		promquery.Datasource(datasource),
	}
	if len(seriesNameFormat) > 0 {
		opts = append(opts, promquery.SeriesNameFormat(seriesNameFormat[0]))
	}
	return promquery.PromQL(expr, opts...)
}
