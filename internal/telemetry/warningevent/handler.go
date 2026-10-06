// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// Package warningevent emits Kubernetes Events for Warning-severity Redfish
// alerts. It mirrors the criticalevent package but does not patch Server
// conditions — a Kubernetes Event is sufficient for non-critical alerts.
package warningevent

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// BMCRefField is the field-indexer key for Server lookup.
	BMCRefField = "spec.bmcRef.name"
)

// Handler emits a Kubernetes Event for every Warning-severity Redfish alert
// on each Server whose spec.bmcRef.name matches the BMC name.
type Handler struct {
	// Client must be cache-backed so MatchingFields works against the
	// BMCRefField indexer.
	Client        client.Client
	Log           logr.Logger
	EventRecorder record.EventRecorder
}

// HandleWarning lists Servers indexed by bmcName and emits a Kubernetes Event
// for each one.
func (h *Handler) HandleWarning(ctx context.Context, bmcName string, event sink.Event) error {
	if bmcName == "" {
		return nil
	}

	serverList := &metalv1alpha1.ServerList{}
	if err := h.Client.List(ctx, serverList, client.MatchingFields{BMCRefField: bmcName}); err != nil {
		return fmt.Errorf("list Servers by %s=%s: %w", BMCRefField, bmcName, err)
	}

	if len(serverList.Items) == 0 {
		h.Log.V(2).Info("No Servers matched the warning event",
			"bmc", bmcName, "eventID", event.EventID)
		return nil
	}

	if h.EventRecorder == nil {
		return nil
	}

	for i := range serverList.Items {
		server := &serverList.Items[i]
		h.EventRecorder.Eventf(server, corev1.EventTypeWarning, "HardwareAlert",
			"Warning Redfish event [%s]: %s (component: %s, at: %s)",
			event.MessageID, event.Message, event.OriginOfCondition, event.EventTimestamp)
		h.Log.V(1).Info("Warning event emitted",
			"server", server.Name, "bmc", bmcName, "eventID", event.EventID)
	}
	return nil
}
