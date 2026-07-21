// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// Package subscriptions reconciles Redfish event subscriptions on
// event-eligible BMCs. It runs as a controller-runtime Reconciler
// (BMCReconciler) that watches metalv1alpha1.BMC objects and drives
// Create/Delete subscription operations so each BMC has the
// subscriptions we want pointing at our HTTP receiver.
package subscriptions

import (
	"context"
	"strconv"

	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"github.com/stmcginnis/gofish/schemas"
)

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
		host = host + ":" + strconv.Itoa(int(p))
	}
	return scheme + "://" + host
}

// Subscription is one event subscription as it exists on a BMC.
type Subscription struct {
	URI         string
	Destination string
	EventFormat string // "MetricReport" or "Event".
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
	Logout()
}

// ClientFactory builds a Client per operation; the production
// implementation wraps bmc.NewRedfishBMCClient.
type ClientFactory interface {
	NewClient(ctx context.Context, r *Resolved) (Client, error)
}
