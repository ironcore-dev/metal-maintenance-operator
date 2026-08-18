// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package prometheus_test

import (
	"testing"

	psink "github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink/prometheus"
	baseboardv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/baseboard/v1alpha1"
	systemv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/system/v1alpha1"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestFirmwareStateCollector_BIOSVersion(t *testing.T) {
	scheme := newStateScheme(t)
	server := &metalv1alpha1.Server{
		ObjectMeta: metav1.ObjectMeta{Name: "server-1"},
		Status:     metalv1alpha1.ServerStatus{BIOSVersion: "1.0.0"},
	}
	biosv := &systemv1alpha1.BIOSVersion{
		ObjectMeta: metav1.ObjectMeta{Name: "biosv-1"},
		Spec: systemv1alpha1.BIOSVersionSpec{
			BIOSVersionTemplate: systemv1alpha1.BIOSVersionTemplate{Version: "2.0.0"},
			ServerRef:           &corev1.LocalObjectReference{Name: "server-1"},
		},
		Status: systemv1alpha1.BIOSVersionStatus{State: systemv1alpha1.BIOSVersionStateInProgress},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(server, biosv).WithStatusSubresource(biosv).Build()

	reg := prometheus.NewRegistry()
	if err := psink.NewFirmwareStateCollector(c, reg); err != nil {
		t.Fatalf("NewFirmwareStateCollector: %v", err)
	}

	labels, ok := gatherStateMetric(t, reg, "metal_maintenance_biosversion_info", map[string]string{
		"name":             "biosv-1",
		"server":           "server-1",
		"desired_version":  "2.0.0",
		"observed_version": "1.0.0",
		"state":            "InProgress",
	})
	if !ok {
		t.Fatal("metal_maintenance_biosversion_info not found")
	}
	if labels["desired_version"] != "2.0.0" {
		t.Errorf("desired_version: got %q, want 2.0.0", labels["desired_version"])
	}
	if labels["observed_version"] != "1.0.0" {
		t.Errorf("observed_version: got %q, want 1.0.0", labels["observed_version"])
	}
}

func TestFirmwareStateCollector_BMCVersion(t *testing.T) {
	scheme := newStateScheme(t)
	bmc := &metalv1alpha1.BMC{
		ObjectMeta: metav1.ObjectMeta{Name: "bmc-1"},
		Status:     metalv1alpha1.BMCStatus{FirmwareVersion: "3.0.0"},
	}
	bmcv := &baseboardv1alpha1.BMCVersion{
		ObjectMeta: metav1.ObjectMeta{Name: "bmcv-1"},
		Spec: baseboardv1alpha1.BMCVersionSpec{
			BMCVersionTemplate: baseboardv1alpha1.BMCVersionTemplate{Version: "3.0.0"},
			BMCRef:             &corev1.LocalObjectReference{Name: "bmc-1"},
		},
		Status: baseboardv1alpha1.BMCVersionStatus{State: baseboardv1alpha1.BMCVersionStateCompleted},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(bmc, bmcv).WithStatusSubresource(bmcv).Build()

	reg := prometheus.NewRegistry()
	if err := psink.NewFirmwareStateCollector(c, reg); err != nil {
		t.Fatalf("NewFirmwareStateCollector: %v", err)
	}

	labels, ok := gatherStateMetric(t, reg, "metal_maintenance_bmcversion_info", map[string]string{
		"name":  "bmcv-1",
		"bmc":   "bmc-1",
		"state": "Completed",
	})
	if !ok {
		t.Fatal("metal_maintenance_bmcversion_info not found")
	}
	if labels["desired_version"] != "3.0.0" {
		t.Errorf("desired_version: got %q, want 3.0.0", labels["desired_version"])
	}
	if labels["observed_version"] != "3.0.0" {
		t.Errorf("observed_version: got %q, want 3.0.0", labels["observed_version"])
	}
}

func TestFirmwareStateCollector_EmptyCluster(t *testing.T) {
	scheme := newStateScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	reg := prometheus.NewRegistry()
	if err := psink.NewFirmwareStateCollector(c, reg); err != nil {
		t.Fatalf("NewFirmwareStateCollector: %v", err)
	}

	for _, family := range []string{
		"metal_maintenance_biosversion_info",
		"metal_maintenance_bmcversion_info",
	} {
		if n := gatherStateCount(t, reg, family); n != 0 {
			t.Errorf("%s: got %d series, want 0", family, n)
		}
	}
}

func TestFirmwareStateCollector_ObservedVersionEmptyWhenServerMissing(t *testing.T) {
	scheme := newStateScheme(t)
	biosv := &systemv1alpha1.BIOSVersion{
		ObjectMeta: metav1.ObjectMeta{Name: "biosv-orphan"},
		Spec: systemv1alpha1.BIOSVersionSpec{
			BIOSVersionTemplate: systemv1alpha1.BIOSVersionTemplate{Version: "2.0.0"},
			ServerRef:           &corev1.LocalObjectReference{Name: "server-gone"},
		},
		Status: systemv1alpha1.BIOSVersionStatus{State: systemv1alpha1.BIOSVersionStatePending},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(biosv).WithStatusSubresource(biosv).Build()

	reg := prometheus.NewRegistry()
	if err := psink.NewFirmwareStateCollector(c, reg); err != nil {
		t.Fatalf("NewFirmwareStateCollector: %v", err)
	}

	labels, ok := gatherStateMetric(t, reg, "metal_maintenance_biosversion_info", map[string]string{"name": "biosv-orphan"})
	if !ok {
		t.Fatal("metal_maintenance_biosversion_info not found")
	}
	if labels["observed_version"] != "" {
		t.Errorf("observed_version: got %q, want empty (server missing)", labels["observed_version"])
	}
}
