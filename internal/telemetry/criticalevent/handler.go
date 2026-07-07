// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// Package criticalevent translates Critical-severity Redfish events into
// declarative Server-readiness state.
package criticalevent

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/sink"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// BMCRefField is the field-indexer key for Server lookup
	BMCRefField = "spec.bmcRef.name"

	// CriticalEventConditionType is the condition Type
	CriticalEventConditionType = "CriticalEventReceived"
)

// ConditionHandler sets CriticalEventReceived on every Server whose
// spec.bmcRef.name matches bmcName.
type ConditionHandler struct {
	// Client must be cache-backed so MatchingFields works against the
	// BMCRefField indexer.
	Client client.Client
	Log    logr.Logger
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
		Type:    CriticalEventConditionType,
		Status:  metav1.ConditionTrue,
		Reason:  fmt.Sprintf("CriticalEvent%s", sanitizeEventID(event.EventID)),
		Message: fmt.Sprintf("Critical Redfish event: %s (component: %s)", event.Message, event.OriginOfCondition),
	}

	for i := range serverList.Items {
		server := &serverList.Items[i]
		if err := h.patchCondition(ctx, server, condition); err != nil {
			h.Log.Error(err, "Failed to patch Server condition",
				"server", server.Name, "bmc", bmcName, "eventID", event.EventID)
			continue
		}
		h.Log.V(1).Info("Critical event condition applied",
			"server", server.Name, "bmc", bmcName, "eventID", event.EventID)
	}
	return nil
}

func (h *ConditionHandler) patchCondition(ctx context.Context, server *metalv1alpha1.Server, condition metav1.Condition) error {
	condition.ObservedGeneration = server.Generation

	base := server.DeepCopy()
	if !apimeta.SetStatusCondition(&server.Status.Conditions, condition) {
		return nil
	}

	if err := h.Client.Status().Patch(ctx, server, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("patch server %s status: %w", server.Name, err)
	}
	return nil
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
