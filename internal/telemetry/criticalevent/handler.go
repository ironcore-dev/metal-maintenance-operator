// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// Package criticalevent translates Critical-severity Redfish events into
// declarative Server-readiness state.
package criticalevent

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/prometheus/client_golang/prometheus"
)

const (
	// BMCRefField is the field-indexer key for Server lookup
	BMCRefField = "spec.bmcRef.name"

	// CriticalEventConditionType is the condition Type
	CriticalEventConditionType = "CriticalEventReceived"

	// AlertGaugeMetricName is the Prometheus metric name for the
	// per-Server critical-alert gauge.
	AlertGaugeMetricName = "redfish_server_critical_alert"
)

// NewCriticalAlertGauge creates the redfish_server_critical_alert GaugeVec.
// It must be registered with a prometheus.Registerer before use.
func NewCriticalAlertGauge() *prometheus.GaugeVec {
	return prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: AlertGaugeMetricName,
		Help: "1 if the Server has an active CriticalEventReceived condition, 0 otherwise. " +
			"Derived from Server.status.conditions and survives operator restarts.",
	}, []string{"server", "namespace"})
}

// ConditionHandler sets CriticalEventReceived on every Server whose
// spec.bmcRef.name matches bmcName.
type ConditionHandler struct {
	// Client must be cache-backed so MatchingFields works against the
	// BMCRefField indexer.
	Client client.Client
	Log    logr.Logger
	// EventRecorder is optional. When set, a Kubernetes Event is emitted
	// for each Server after the CriticalEventReceived condition is patched.
	EventRecorder record.EventRecorder
	// AlertGauge is optional. When set, it is set to 1 for each Server
	// after the CriticalEventReceived condition is patched.
	AlertGauge *prometheus.GaugeVec
}

// HandleCritical lists Servers indexed by bmcName, sets the condition on each
func (h *ConditionHandler) HandleCritical(ctx context.Context, bmcName string, event sink.Event) error {
	if bmcName == "" {
		// Empty bmcName from the wrapper would match every Server
		// without a BMC ref. Skip.
		return nil
	}

	serverList := &metalv1alpha1.ServerList{}
	if err := h.Client.List(ctx, serverList, client.MatchingFields{BMCRefField: bmcName}); err != nil {
		return fmt.Errorf("list Servers by %s=%s: %w", BMCRefField, bmcName, err)
	}

	if len(serverList.Items) == 0 {
		h.Log.V(2).Info("No Servers matched the critical event",
			"bmc", bmcName, "eventID", event.EventID)
		return nil
	}

	condition := metav1.Condition{
		Type:   CriticalEventConditionType,
		Status: metav1.ConditionTrue,
		Reason: fmt.Sprintf("CriticalEvent%s", sanitizeEventID(event.EventID)),
		Message: fmt.Sprintf("Critical Redfish event [%s]: %s (component: %s, at: %s)",
			event.MessageID, event.Message, event.OriginOfCondition, event.EventTimestamp),
	}

	var failed []error
	for i := range serverList.Items {
		server := &serverList.Items[i]
		if err := h.patchCondition(ctx, server, condition); err != nil {
			h.Log.Error(err, "Failed to patch Server condition",
				"server", server.Name, "bmc", bmcName, "eventID", event.EventID)
			failed = append(failed, err)
			continue
		}
		h.Log.V(1).Info("Critical event condition applied",
			"server", server.Name, "bmc", bmcName, "eventID", event.EventID)
		if h.AlertGauge != nil {
			h.AlertGauge.With(prometheus.Labels{
				"server":    server.Name,
				"namespace": server.Namespace,
			}).Set(1)
		}
		if h.EventRecorder != nil {
			h.EventRecorder.Eventf(server, corev1.EventTypeWarning, "HardwareAlert",
				"Critical Redfish event [%s]: %s (component: %s, at: %s)",
				event.MessageID, event.Message, event.OriginOfCondition, event.EventTimestamp)
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("failed to patch %d/%d servers: %w", len(failed), len(serverList.Items), errors.Join(failed...))
	}
	return nil
}

func (h *ConditionHandler) patchCondition(ctx context.Context, server *metalv1alpha1.Server, condition metav1.Condition) error {
	condition.ObservedGeneration = server.Generation

	base := server.DeepCopy()
	if !apimeta.SetStatusCondition(&server.Status.Conditions, condition) {
		return nil
	}

	if err := h.Client.Status().Patch(ctx, server,
		client.MergeFromWithOptions(base, client.MergeFromWithOptimisticLock{})); err != nil {
		return fmt.Errorf("patch server %s status: %w", server.Name, err)
	}
	return nil
}

// SetAlertGauge sets the alert gauge to the given value (0 or 1) for a
// Server. Used for startup pre-warming and condition clearing.
func (h *ConditionHandler) SetAlertGauge(serverName, namespace string, value float64) {
	if h.AlertGauge == nil {
		return
	}
	h.AlertGauge.With(prometheus.Labels{
		"server":    serverName,
		"namespace": namespace,
	}).Set(value)
}

// sanitizeEventID strips characters apiserver's Reason validation
// rejects (regex `^([A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?)?$`).
func sanitizeEventID(id string) string {
	if id == "" {
		return "Unknown"
	}
	out := make([]byte, 0, len(id))
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'A' && c <= 'Z',
			c >= 'a' && c <= 'z',
			c >= '0' && c <= '9',
			c == '_':
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return "Unknown"
	}
	return string(out)
}
