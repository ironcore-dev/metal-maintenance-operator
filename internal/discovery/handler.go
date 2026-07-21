// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// Package discovery exposes the operator's BMC inventory as a Prometheus
// HTTP service-discovery endpoint. An external redfish-exporte configures
// its scrape job with http_sd_configs pointed at this endpoint and
// scrapes BMCs directly — no metric collection happens here.
//
// Endpoint:
//
//	GET /sd/bmcs  → 200 OK, application/json
//	*             → 405
package discovery

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"

	"github.com/go-logr/logr"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Path is the URL the handler serves at.
const Path = "/sd/bmcs"

// Target is one entry in the Prometheus HTTP SD response. One target
// per object so each BMC is independently relabelable.
type Target struct {
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels"`
}

// Handler serves the SD endpoint. Reads the live BMC list from the
// controller-runtime cache on each request — no in-memory cache of
// rendered output.
type Handler struct {
	Client client.Client
	Log    logr.Logger
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "Only GET is allowed", http.StatusMethodNotAllowed)
		return
	}

	list := &metalv1alpha1.BMCList{}
	if err := h.Client.List(req.Context(), list); err != nil {
		h.log().Error(err, "Failed to list BMCs for service discovery")
		http.Error(w, "Failed to list BMCs", http.StatusInternalServerError)
		return
	}

	targets := Render(list.Items)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(targets); err != nil {
		h.log().V(1).Info("Failed to encode SD response", "err", err.Error())
	}
}

// Render projects the BMC list onto Prometheus SD targets.
// Extracted from ServeHTTP so the contract is unit-testable without an
// HTTP server.
func Render(bmcs []metalv1alpha1.BMC) []Target {
	out := make([]Target, 0, len(bmcs))
	for i := range bmcs {
		b := &bmcs[i]
		if !b.DeletionTimestamp.IsZero() {
			continue
		}
		if !b.Status.IP.IsValid() {
			continue
		}
		out = append(out, Target{
			Targets: []string{hostPort(b)},
			Labels:  labelsFor(b),
		})
	}
	return out
}

func hostPort(b *metalv1alpha1.BMC) string {
	host := b.Status.IP.String()
	if p := b.Spec.Protocol.Port; p > 0 {
		return net.JoinHostPort(host, strconv.Itoa(int(p)))
	}
	return host
}

// labelsFor builds the Prometheus meta-labels for one BMC.
func labelsFor(b *metalv1alpha1.BMC) map[string]string {
	labels := map[string]string{
		"__meta_bmc_name":      b.Name,
		"__meta_bmc_namespace": b.Namespace,
	}
	if s := b.Status.Manufacturer; s != "" {
		labels["__meta_bmc_vendor"] = s
	}
	if s := b.Status.Model; s != "" {
		labels["__meta_bmc_model"] = s
	}
	if s := b.Status.SerialNumber; s != "" {
		labels["__meta_bmc_serial"] = s
	}
	if s := b.Status.FirmwareVersion; s != "" {
		labels["__meta_bmc_firmware"] = s
	}
	scheme := "https"
	if s := string(b.Spec.Protocol.Scheme); s != "" {
		scheme = s
	}
	labels["__meta_bmc_protocol"] = scheme
	if name := b.Spec.BMCSecretRef.Name; name != "" {
		labels["__meta_bmc_secret_name"] = name
		labels["__meta_bmc_secret_namespace"] = b.Namespace
	}
	return labels
}

func (h *Handler) log() logr.Logger {
	if h.Log == (logr.Logger{}) {
		return logr.Discard()
	}
	return h.Log
}

var _ http.Handler = (*Handler)(nil)
