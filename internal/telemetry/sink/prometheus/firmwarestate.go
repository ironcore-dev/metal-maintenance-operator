// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package prometheus

import (
	"context"
	"time"

	baseboardv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/baseboard/v1alpha1"
	systemv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/system/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/prometheus/client_golang/prometheus"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	biosFirmwareDesc = prometheus.NewDesc(
		"metal_maintenance_biosversion_info",
		"State of BIOS firmware upgrade resources.",
		[]string{"name", "server", "manufacturer", "model", "desired_version", "observed_version", "state"},
		nil,
	)
	bmcFirmwareDesc = prometheus.NewDesc(
		"metal_maintenance_bmcversion_info",
		"State of BMC firmware upgrade resources.",
		[]string{"name", "bmc", "manufacturer", "model", "desired_version", "observed_version", "state"},
		nil,
	)
)

// FirmwareStateCollector implements prometheus.Collector for BIOSVersion and BMCVersion resources.
type FirmwareStateCollector struct {
	client client.Client
}

// NewFirmwareStateCollector creates and registers a FirmwareStateCollector with the given Prometheus registerer.
func NewFirmwareStateCollector(c client.Client, reg prometheus.Registerer) error {
	return reg.Register(&FirmwareStateCollector{client: c})
}

func (v *FirmwareStateCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- biosFirmwareDesc
	ch <- bmcFirmwareDesc
}

func stateLabel(s string) string {
	if s == "" {
		return "Unknown"
	}
	return s
}

func (v *FirmwareStateCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var biosvList systemv1alpha1.BIOSVersionList
	if err := v.client.List(ctx, &biosvList); err != nil {
		ch <- prometheus.NewInvalidMetric(biosFirmwareDesc, err)
	} else {
		for i := range biosvList.Items {
			bv := &biosvList.Items[i]
			if bv.Spec.ServerRef == nil {
				continue
			}
			serverName := bv.Spec.ServerRef.Name
			observedVersion := ""
			var srv metalv1alpha1.Server
			if getErr := v.client.Get(ctx, client.ObjectKey{Name: serverName}, &srv); getErr == nil {
				observedVersion = srv.Status.BIOSVersion
			} else if !apierrors.IsNotFound(getErr) {
				continue
			}
			m, metricErr := prometheus.NewConstMetric(biosFirmwareDesc, prometheus.GaugeValue, 1,
				bv.Name, serverName, srv.Status.Manufacturer, srv.Status.Model, bv.Spec.Version, observedVersion, stateLabel(string(bv.Status.State)))
			if metricErr != nil {
				ch <- prometheus.NewInvalidMetric(biosFirmwareDesc, metricErr)
				continue
			}
			ch <- m
		}
	}

	var bmcvList baseboardv1alpha1.BMCVersionList
	if err := v.client.List(ctx, &bmcvList); err != nil {
		ch <- prometheus.NewInvalidMetric(bmcFirmwareDesc, err)
	} else {
		for i := range bmcvList.Items {
			bv := &bmcvList.Items[i]
			if bv.Spec.BMCRef == nil {
				continue
			}
			bmcName := bv.Spec.BMCRef.Name
			observedVersion := ""
			var bmc metalv1alpha1.BMC
			if getErr := v.client.Get(ctx, client.ObjectKey{Name: bmcName}, &bmc); getErr == nil {
				observedVersion = bmc.Status.FirmwareVersion
			} else if !apierrors.IsNotFound(getErr) {
				continue
			}
			m, metricErr := prometheus.NewConstMetric(bmcFirmwareDesc, prometheus.GaugeValue, 1,
				bv.Name, bmcName, bmc.Status.Manufacturer, bmc.Status.Model, bv.Spec.Version, observedVersion, stateLabel(string(bv.Status.State)))
			if metricErr != nil {
				ch <- prometheus.NewInvalidMetric(bmcFirmwareDesc, metricErr)
				continue
			}
			ch <- m
		}
	}
}
