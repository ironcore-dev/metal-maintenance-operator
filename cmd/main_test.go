// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestRegistryFallbackURL(t *testing.T) {
	const want = "http://registry.example:10000"
	const host = "registry.example"
	for _, tt := range []struct {
		name, host, bindAddress, want string
	}{
		{"default", host, ":10000", want},
		{"IPv4 bind host", host, "0.0.0.0:10000", want},
		{"IPv6 bind host", host, "[::]:10000", want},
		{"IPv6 advertised host", "2001:db8::1", ":10000", "http://[2001:db8::1]:10000"},
		{"custom port", host, "127.0.0.1:12345", "http://registry.example:12345"},
		{"missing port", host, "0.0.0.0", ""},
		{"unbracketed IPv6", host, "::1:10000", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := registryFallbackURL(tt.host, tt.bindAddress)
			if tt.want == "" {
				if err == nil {
					t.Fatal("expected invalid bind address to fail")
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

const kubeSystem = "kube-system"

func secretNamespaces(t *testing.T, m map[client.Object]cache.ByObject) []string {
	t.Helper()
	for obj, byObject := range m {
		if _, ok := obj.(*corev1.Secret); !ok {
			continue
		}
		out := make([]string, 0, len(byObject.Namespaces))
		for ns := range byObject.Namespaces {
			out = append(out, ns)
		}
		slices.Sort(out)
		return out
	}
	t.Fatal("no Secret entry in ByObject")
	return nil
}

func TestSecretCacheByObject_EmptyMeansUnrestricted(t *testing.T) {
	if got := secretCacheByObject(nil, "manager", "sanitization"); got != nil {
		t.Fatalf("expected no restriction when no namespaces are configured, got %v", got)
	}
}

func TestSecretCacheByObject_AddsOperatorNamespaces(t *testing.T) {
	got := secretNamespaces(t, secretCacheByObject([]string{kubeSystem, "manager"}, "manager", "sanitization", ""))
	want := []string{kubeSystem, "manager", "sanitization"}
	if !slices.Equal(got, want) {
		t.Fatalf("namespaces = %v, want %v", got, want)
	}
}

func TestParseSecretCacheNamespaces(t *testing.T) {
	got, err := parseSecretCacheNamespaces("")
	if err != nil || got != nil {
		t.Fatalf("empty flag must stay unrestricted, got %v, %v", got, err)
	}
	got, err = parseSecretCacheNamespaces(" a , b ")
	if err != nil || !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, in := range []string{",", " ", " , ,"} {
		if _, err := parseSecretCacheNamespaces(in); err == nil {
			t.Fatalf("expected an error for %q", in)
		}
	}
}

func TestCacheByObject_MergesAndSkipsNil(t *testing.T) {
	if got := cacheByObject(nil, nil); got != nil {
		t.Fatalf("expected nil when every input is nil, got %v", got)
	}
	merged := cacheByObject(
		configMapCacheByObject([]string{kubeSystem}),
		secretCacheByObject([]string{kubeSystem}),
	)
	if len(merged) != 2 {
		t.Fatalf("expected ConfigMap and Secret entries, got %d", len(merged))
	}
}
