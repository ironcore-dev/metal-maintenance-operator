// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// verify-test-events fires a Redfish SubmitTestEvent against every server
// in a list, using the operator's real telemetry Config (vendor/model ->
// testMessageId/testSeverity/testOriginOfCondition matching) and the exact
// SubmitTestEvent code path used by the operator's health check
// (internal/telemetry/runtime.NewTestEventClient). Runs entirely locally
// against real BMCs directly by IP/hostname — no Kubernetes cluster
// involved. It does nothing beyond connecting and calling SubmitTestEvent:
// no subscriptions are created, no inventory is read.
//
// Usage:
//
//	go run ./hack/verify-test-events \
//	  -config config/telemetry/configmap.yaml \
//	  -servers hack/verify-test-events/servers.example.yaml
//
// -config accepts either a raw telemetry Config YAML (apiVersion:
// telemetry.metal.ironcore.dev/v1alpha1 ...) or a full Kubernetes
// ConfigMap manifest, in which case data["config.yaml"] is extracted.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/runtime"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/subscriptions"
	metalbmc "github.com/ironcore-dev/metal-operator/bmc"
	goyaml "go.yaml.in/yaml/v3"
	corev1 "k8s.io/api/core/v1"
	sigsyaml "sigs.k8s.io/yaml"
)

// serverEntry is one row in the -servers YAML file. Vendor/Model must
// match a HardwareMatch row's vendor/models in the -config file exactly
// as the operator matches them (see subscriptions.SubscribeToBMC) — there
// is no auto-detection here, this tool only connects and calls
// SubmitTestEvent.
type serverEntry struct {
	Endpoint string `yaml:"endpoint"`
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Vendor   string `yaml:"vendor"`
	Model    string `yaml:"model"`
	// Firmware is only needed if the matched HardwareMatch row sets
	// minFirmware.
	Firmware string `yaml:"firmware,omitempty"`
}

type serverList struct {
	// Username/Password are fleet-wide defaults; per-server values
	// override them.
	Username string        `yaml:"username,omitempty"`
	Password string        `yaml:"password,omitempty"`
	Servers  []serverEntry `yaml:"servers"`
}

func main() {
	configPath := flag.String(
		"config",
		"",
		strings.Join([]string{
			"path to the telemetry Config YAML (raw, or a ConfigMap",
			"manifest containing it under data.config.yaml)",
		}, " "),
	)
	serversPath := flag.String("servers", "", "path to the server list YAML")
	perServerTimeout := flag.Duration("timeout", 30*time.Second, "per-server connect+submit timeout")
	insecure := flag.Bool("insecure", true, "skip TLS verification")
	probe := flag.Bool("probe", false,
		"auto-discover a TestMessageId via the BMC's Redfish EventService registries "+
			"when no testEventOverrides row provides one")
	flag.Parse()

	if *configPath == "" || *serversPath == "" {
		fmt.Fprintln(os.Stderr, "usage: verify-test-events -config <path> -servers <path>")
		os.Exit(2)
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}
	list, err := loadServers(*serversPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load servers: %v\n", err)
		os.Exit(1)
	}

	var okCount, skipCount, failCount int
	for _, s := range list.Servers {
		username := firstNonEmpty(s.Username, list.Username)
		password := firstNonEmpty(s.Password, list.Password)
		result := runOne(s, username, password, cfg, *perServerTimeout, *insecure, *probe)
		fmt.Println(result.String())
		switch result.outcome {
		case outcomeOK:
			okCount++
		case outcomeSkip:
			skipCount++
		case outcomeFail:
			failCount++
		}
	}

	fmt.Printf("\n%d server(s): %d ok, %d skipped, %d failed\n", len(list.Servers), okCount, skipCount, failCount)
	if failCount > 0 {
		os.Exit(1)
	}
}

type outcome int

const (
	outcomeOK outcome = iota
	outcomeSkip
	outcomeFail
)

type result struct {
	endpoint string
	outcome  outcome
	detail   string
}

func (r result) String() string {
	var tag string
	switch r.outcome {
	case outcomeOK:
		tag = "OK"
	case outcomeSkip:
		tag = "SKIP"
	case outcomeFail:
		tag = "FAIL"
	}
	return fmt.Sprintf("[%s] %s: %s", tag, r.endpoint, r.detail)
}

// runOne connects to one BMC and calls SubmitTestEvent. It performs no
// subscription management. When probe is true and no testEventOverrides row
// provides a MessageId, it walks the BMC's Redfish event registries to
// auto-discover one.
func runOne(s serverEntry, username, password string,
	cfg *subscriptions.Config, timeout time.Duration, insecure, probe bool,
) result {
	ref := subscriptions.BMCRef{
		Name: s.Endpoint, Vendor: s.Vendor, Model: s.Model, FirmwareVersion: s.Firmware,
	}

	if subscriptions.IsExcluded(ref, cfg) {
		return result{endpoint: s.Endpoint, outcome: outcomeSkip,
			detail: fmt.Sprintf(
				"vendor=%q model=%q matched an excludeHardware rule",
				s.Vendor, s.Model,
			)}
	}

	override := subscriptions.TestOverrideFor(ref, cfg)
	msgId := ""
	severity := ""
	origin := ""
	if override != nil {
		msgId = override.TestMessageId
		severity = override.TestSeverity
		origin = override.TestOriginOfCondition
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	base, err := metalbmc.NewRedfishBMCClient(ctx, metalbmc.Options{
		Endpoint:    s.Endpoint,
		Username:    username,
		Password:    password,
		BasicAuth:   true,
		InsecureTLS: insecure,
	})
	if err != nil {
		return result{endpoint: s.Endpoint, outcome: outcomeFail, detail: fmt.Sprintf("connect: %v", err)}
	}
	defer base.Logout()

	client, err := runtime.NewTestEventClient(base)
	if err != nil {
		return result{endpoint: s.Endpoint, outcome: outcomeFail, detail: fmt.Sprintf("wrap client: %v", err)}
	}

	// Auto-discover a MessageId when probe is requested and none was configured.
	if msgId == "" && probe {
		caps, err := client.ProbeEventService(ctx, true)
		if err != nil {
			return result{endpoint: s.Endpoint, outcome: outcomeFail,
				detail: fmt.Sprintf("probe EventService: %v", err)}
		}
		if !caps.ServiceEnabled || !caps.HasSubmitTestEvent {
			return result{endpoint: s.Endpoint, outcome: outcomeSkip,
				detail: "EventService not enabled or SubmitTestEvent not supported"}
		}
		if caps.DiscoveredTestMessageId == "" {
			return result{endpoint: s.Endpoint, outcome: outcomeSkip,
				detail: "EventService enabled but no MessageId could be discovered from registries"}
		}
		fmt.Printf("  [discovered] %s → %s\n", s.Endpoint, caps.DiscoveredTestMessageId)
		msgId = caps.DiscoveredTestMessageId
	}

	if msgId == "" {
		return result{endpoint: s.Endpoint, outcome: outcomeSkip,
			detail: fmt.Sprintf(
				"no testEventOverrides row for vendor=%q model=%q and --probe not set",
				s.Vendor, s.Model,
			)}
	}

	params := subscriptions.TestEventParams{
		MessageId:         msgId,
		Severity:          severity,
		OriginOfCondition: origin,
	}
	if err := client.SubmitTestEvent(ctx, params); err != nil {
		return result{endpoint: s.Endpoint, outcome: outcomeFail,
			detail: fmt.Sprintf(
				"SubmitTestEvent(vendor=%q model=%q messageId=%q): %v",
				s.Vendor,
				s.Model,
				msgId,
				err,
			)}
	}
	return result{endpoint: s.Endpoint, outcome: outcomeOK,
		detail: fmt.Sprintf(
			"SubmitTestEvent accepted (vendor=%q model=%q messageId=%q)",
			s.Vendor,
			s.Model,
			msgId,
		)}
}

// loadConfig accepts either a raw telemetry Config YAML document, or a
// full Kubernetes ConfigMap manifest (decoded into the same corev1.ConfigMap
// type and read via runtime.ConfigKey that ConfigLoader.reload uses in
// production), in which case data[runtime.ConfigKey] is extracted.
func loadConfig(path string) (*subscriptions.Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cm corev1.ConfigMap
	if err := sigsyaml.Unmarshal(raw, &cm); err == nil && cm.Kind == "ConfigMap" {
		data, ok := cm.Data[runtime.ConfigKey]
		if !ok {
			return nil, fmt.Errorf("%s: ConfigMap has no data[%q] key", path, runtime.ConfigKey)
		}
		raw = []byte(data)
	}

	cfg, errs := subscriptions.Parse(raw)
	if len(errs) > 0 {
		return nil, fmt.Errorf("invalid config: %v", errs.ToAggregate())
	}
	return cfg, nil
}

func loadServers(path string) (*serverList, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var list serverList
	if err := goyaml.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(list.Servers) == 0 {
		return nil, fmt.Errorf("%s: no servers listed", path)
	}
	for i, s := range list.Servers {
		if s.Endpoint == "" {
			return nil, fmt.Errorf("%s: servers[%d] has no endpoint", path, i)
		}
		if s.Vendor == "" {
			return nil, fmt.Errorf("%s: servers[%d] (%s) has no vendor", path, i, s.Endpoint)
		}
	}
	return &list, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
