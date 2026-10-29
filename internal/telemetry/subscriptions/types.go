// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// Package subscriptions reconciles Redfish event subscriptions on
// event-eligible BMCs. It runs as a controller-runtime Reconciler
// (BMCReconciler) that watches metalv1alpha1.BMC objects and drives
// Create/Delete subscription operations so each BMC has the
// subscriptions we want pointing at our HTTP receiver.
package subscriptions

import (
	"context"
	"net"
	"strconv"

	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/stmcginnis/gofish/schemas"
)

// TestEventParams holds the per-vendor parameters for a SubmitTestEvent call.
// All fields map directly to Redfish EventService.SubmitTestEvent parameters.
type TestEventParams struct {
	MessageId         string
	Severity          string // e.g. "OK", "Informational"
	OriginOfCondition string // e.g. "/redfish/v1/Systems/1"; empty = omit
}

// pathPrefix is the URL prefix every subscription destination starts
// with. We only adopt or delete subscriptions whose path begins here —
// foreign subscriptions are left alone.
const pathPrefix = "/serverevents/"

// BMCRef is the minimum information a consumer needs to reason about a BMC.
type BMCRef struct {
	Name            string
	Namespace       string
	Vendor          string
	Model           string
	FirmwareVersion string
}

// BMCEndpoint builds the Redfish base URL (scheme://host[:port]) for a BMC.
func BMCEndpoint(b *metalv1alpha1.BMC) string {
	scheme := "https"
	if s := string(b.Spec.Protocol.Scheme); s != "" {
		scheme = s
	}
	host := b.Status.IP.String()
	if p := b.Spec.Protocol.Port; p > 0 {
		host = net.JoinHostPort(host, strconv.Itoa(int(p)))
	}
	return scheme + "://" + host
}

// Subscription is one event subscription as it exists on a BMC.
type Subscription struct {
	URI         string
	Destination string
	EventFormat string // "MetricReport" or "Event".
}

// EventServiceCapabilities holds the result of a live EventService probe.
type EventServiceCapabilities struct {
	// ServiceEnabled is true when the BMC's EventService is present and enabled.
	ServiceEnabled bool
	// HasSubmitTestEvent is true when the EventService advertises a
	// SubmitTestEvent action target.
	HasSubmitTestEvent bool
	// DiscoveredTestMessageId is the first viable MessageId found by walking
	// the BMC's RegistryPrefixes. Empty when the walk was skipped (an override
	// was provided) or when no registry entry could be found.
	DiscoveredTestMessageId string
}

// Client is the reconciler's contract with a BMC over Redfish. It's the
// bmc.BMC surface for Create/Delete/Logout, augmented with the list
// operation that upstream bmc.BMC does not expose today. The production
// implementation lives in the runtime package and wraps bmc.BMC with a
// direct read of /redfish/v1/EventService/Subscriptions via the
// *gofish.APIClient accessor added by metal-operator PR #966.
type Client interface {
	CreateEventSubscription(
		ctx context.Context,
		destination string,
		eventType schemas.EventFormatType,
		protocol schemas.DeliveryRetryPolicy,
	) (string, error)
	DeleteEventSubscription(ctx context.Context, uri string) error
	ListEventSubscriptions(ctx context.Context) ([]Subscription, error)
	// SubmitTestEvent fires a Redfish SubmitTestEvent action using the
	// given params. The MessageId is used as a correlation token; the
	// caller can match it when the event arrives at the receiver to
	// confirm round-trip.
	SubmitTestEvent(ctx context.Context, params TestEventParams) error
	// ProbeEventService interrogates the BMC's EventService to determine
	// whether it supports event-push subscriptions. When needsDiscovery is
	// true and the service is capable, it also walks the BMC's Redfish
	// message registries to find a suitable SubmitTestEvent MessageId.
	ProbeEventService(ctx context.Context, needsDiscovery bool) (*EventServiceCapabilities, error)
	Logout()
}

// ClientFactory builds a Client per operation; the production
// implementation wraps bmc.NewRedfishBMCClient.
type ClientFactory interface {
	NewClient(ctx context.Context, r *Resolved) (Client, error)
}
