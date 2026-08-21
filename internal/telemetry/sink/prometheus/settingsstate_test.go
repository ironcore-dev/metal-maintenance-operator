// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package prometheus_test

import (
	"testing"

	baseboardv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/baseboard/v1alpha1"
	systemv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/system/v1alpha1"
	promsink "github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink/prometheus"
	"github.com/prometheus/client_golang/prometheus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSettingsStateCollector_BIOSSettings(t *testing.T) {
	scheme := newStateScheme(t)
	bioss := &systemv1alpha1.BIOSSettings{
		ObjectMeta: metav1.ObjectMeta{Name: "bioss-1"},
		Spec: systemv1alpha1.BIOSSettingsSpec{
			ServerRef: &corev1.LocalObjectReference{Name: "server-1"},
		},
		Status: systemv1alpha1.BIOSSettingsStatus{State: systemv1alpha1.BIOSSettingsStateApplied},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(bioss).WithStatusSubresource(bioss).Build()

	reg := prometheus.NewRegistry()
	if err := promsink.NewSettingsStateCollector(c, reg); err != nil {
		t.Fatalf("NewSettingsStateCollector: %v", err)
	}

	_, ok := gatherStateMetric(t, reg, "metal_maintenance_biossettings_info", map[string]string{
		"name":   "bioss-1",
		"server": "server-1",
		"state":  "Applied",
	})
	if !ok {
		t.Fatal("metal_maintenance_biossettings_info not found")
	}
}

func TestSettingsStateCollector_BMCSettings(t *testing.T) {
	scheme := newStateScheme(t)
	bmcs := &baseboardv1alpha1.BMCSettings{
		ObjectMeta: metav1.ObjectMeta{Name: "bmcs-1"},
		Spec: baseboardv1alpha1.BMCSettingsSpec{
			BMCRef: &corev1.LocalObjectReference{Name: "bmc-1"},
		},
		Status: baseboardv1alpha1.BMCSettingsStatus{State: baseboardv1alpha1.BMCSettingsStateFailed},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(bmcs).WithStatusSubresource(bmcs).Build()

	reg := prometheus.NewRegistry()
	if err := promsink.NewSettingsStateCollector(c, reg); err != nil {
		t.Fatalf("NewSettingsStateCollector: %v", err)
	}

	_, ok := gatherStateMetric(t, reg, "metal_maintenance_bmcsettings_info", map[string]string{
		"name":  "bmcs-1",
		"bmc":   "bmc-1",
		"state": "Failed",
	})
	if !ok {
		t.Fatal("metal_maintenance_bmcsettings_info not found")
	}
}

func TestSettingsStateCollector_NilServerRefSkipped(t *testing.T) {
	scheme := newStateScheme(t)
	bioss := &systemv1alpha1.BIOSSettings{
		ObjectMeta: metav1.ObjectMeta{Name: "bioss-noref"},
		Status:     systemv1alpha1.BIOSSettingsStatus{State: systemv1alpha1.BIOSSettingsStatePending},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(bioss).WithStatusSubresource(bioss).Build()

	reg := prometheus.NewRegistry()
	if err := promsink.NewSettingsStateCollector(c, reg); err != nil {
		t.Fatalf("NewSettingsStateCollector: %v", err)
	}

	if _, err := reg.Gather(); err != nil {
		t.Errorf("Gather returned error for nil ServerRef: %v", err)
	}
	if n := gatherStateCount(t, reg, "metal_maintenance_biossettings_info"); n != 0 {
		t.Errorf("expected series to be skipped, got %d", n)
	}
}

func TestSettingsStateCollector_EmptyCluster(t *testing.T) {
	scheme := newStateScheme(t)
	c := fake.NewClientBuilder().WithScheme(scheme).Build()

	reg := prometheus.NewRegistry()
	if err := promsink.NewSettingsStateCollector(c, reg); err != nil {
		t.Fatalf("NewSettingsStateCollector: %v", err)
	}

	for _, family := range []string{
		"metal_maintenance_biossettings_info",
		"metal_maintenance_bmcsettings_info",
	} {
		if n := gatherStateCount(t, reg, family); n != 0 {
			t.Errorf("%s: got %d series, want 0", family, n)
		}
	}
}
