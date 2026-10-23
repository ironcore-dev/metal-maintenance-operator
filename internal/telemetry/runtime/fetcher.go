// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/subscriptions"
)

// BMCMetricFetcher fetches a full Redfish MetricReport body from a BMC via
// authenticated GET. Used when a MetricReport POST notification arrives with
// empty MetricValues but a non-empty @odata.id (Dell 17G iDRAC 10 behaviour).
type BMCMetricFetcher struct {
	Resolver subscriptions.Resolver
	Log      logr.Logger
	client   *http.Client
}

// NewBMCMetricFetcher constructs a BMCMetricFetcher with a shared http.Client
// whose transport is cloned from http.DefaultTransport so idle-connection
// pooling and timeouts are retained.
func NewBMCMetricFetcher(resolver subscriptions.Resolver, insecureTLS bool, log logr.Logger) *BMCMetricFetcher {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: insecureTLS} //nolint:gosec
	return &BMCMetricFetcher{
		Resolver: resolver,
		Log:      log,
		client: &http.Client{
			Transport:     tr,
			Timeout:       30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (f *BMCMetricFetcher) FetchMetricReport(ctx context.Context, bmcName, odataID string) ([]byte, error) {
	r, err := f.Resolver.Resolve(ctx, bmcName)
	if err != nil {
		return nil, fmt.Errorf("resolve BMC %q: %w", bmcName, err)
	}
	rawURL, err := resolveODataURL(odataID, subscriptions.BMCEndpoint(r.BMC))
	if err != nil {
		return nil, fmt.Errorf("validate odataID for BMC %q: %w", bmcName, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(r.Username, r.Password)

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", rawURL, resp.StatusCode)
	}
	const maxFetchBytes = 1 << 20 // 1 MiB
	buf, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes+1))
	if err != nil {
		return nil, err
	}
	if len(buf) > maxFetchBytes {
		return nil, fmt.Errorf("GET %s: response exceeds %d bytes", rawURL, maxFetchBytes)
	}
	return buf, nil
}

// resolveODataURL validates odataID and resolves it against the BMC endpoint.
// The check against metricReportsPath is applied to abs.Path after URL
// normalisation so that dot-segment traversal (e.g. MetricReports/../../Systems/1)
// cannot bypass it. The resolved URL must also be same-origin with the BMC and
// carry no userinfo.
func resolveODataURL(odataID, bmcEndpoint string) (string, error) {
	const metricReportsPath = "/redfish/v1/TelemetryService/MetricReports/"
	parsed, err := url.Parse(odataID)
	if err != nil {
		return "", fmt.Errorf("parse odataID: %w", err)
	}
	origin, err := url.Parse(bmcEndpoint)
	if err != nil {
		return "", fmt.Errorf("parse BMC endpoint: %w", err)
	}
	abs := origin.ResolveReference(parsed)
	if !strings.HasPrefix(abs.Path, metricReportsPath) {
		return "", fmt.Errorf("odataID %q is not under %s", odataID, metricReportsPath)
	}
	if abs.Scheme != origin.Scheme || abs.Host != origin.Host {
		return "", fmt.Errorf("odataID %q resolves outside BMC origin %s", odataID, origin.Host)
	}
	if abs.User != nil {
		return "", fmt.Errorf("odataID %q contains userinfo", odataID)
	}
	return abs.String(), nil
}
