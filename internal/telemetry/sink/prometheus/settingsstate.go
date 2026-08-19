// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package prometheus

import (
	"context"
	"time"

	baseboardv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/baseboard/v1alpha1"
	systemv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/system/v1alpha1"
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	biosSettingsDesc = prometheus.NewDesc(
		"metal_maintenance_biossettings_info",
		"State of BIOSSettings resources.",
		[]string{"name", "server", "state"},
		nil,
	)
	bmcSettingsDesc = prometheus.NewDesc(
		"metal_maintenance_bmcsettings_info",
		"State of BMCSettings resources.",
		[]string{"name", "bmc", "state"},
		nil,
	)
)

// SettingsStateCollector implements prometheus.Collector for BIOSSettings and BMCSettings resources.
type SettingsStateCollector struct {
	client client.Client
}

// NewSettingsStateCollector creates and registers a SettingsStateCollector with the given Prometheus registerer.
func NewSettingsStateCollector(c client.Client, reg prometheus.Registerer) error {
	return reg.Register(&SettingsStateCollector{client: c})
}

func (s *SettingsStateCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- biosSettingsDesc
	ch <- bmcSettingsDesc
}

func (s *SettingsStateCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var biossList systemv1alpha1.BIOSSettingsList
	if err := s.client.List(ctx, &biossList); err != nil {
		ch <- prometheus.NewInvalidMetric(biosSettingsDesc, err)
	} else {
		for i := range biossList.Items {
			bs := &biossList.Items[i]
			if bs.Spec.ServerRef == nil {
				continue
			}
			m, metricErr := prometheus.NewConstMetric(biosSettingsDesc, prometheus.GaugeValue, 1,
				bs.Name, bs.Spec.ServerRef.Name, stateLabel(string(bs.Status.State)))
			if metricErr != nil {
				ch <- prometheus.NewInvalidMetric(biosSettingsDesc, metricErr)
				continue
			}
			ch <- m
		}
	}

	var bmcsList baseboardv1alpha1.BMCSettingsList
	if err := s.client.List(ctx, &bmcsList); err != nil {
		ch <- prometheus.NewInvalidMetric(bmcSettingsDesc, err)
	} else {
		for i := range bmcsList.Items {
			bs := &bmcsList.Items[i]
			if bs.Spec.BMCRef == nil {
				continue
			}
			m, metricErr := prometheus.NewConstMetric(bmcSettingsDesc, prometheus.GaugeValue, 1,
				bs.Name, bs.Spec.BMCRef.Name, stateLabel(string(bs.Status.State)))
			if metricErr != nil {
				ch <- prometheus.NewInvalidMetric(bmcSettingsDesc, metricErr)
				continue
			}
			ch <- m
		}
	}
}
