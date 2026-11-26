// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package ignition

import (
	"context"
	"strings"
	"testing"

	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

func TestDiscoveryIgnition(t *testing.T) {
	p := &DiscoveryProvider{
		ProbeImage:  "metalprobe:test",
		RegistryURL: "http://registry:10000",
	}
	server := &metalv1alpha1.Server{
		Spec: metalv1alpha1.ServerSpec{SystemUUID: "some-uuid"},
	}

	data, creds, err := p.Ignition(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	if creds != nil {
		t.Fatal("expected no credentials when GenerateSSHKey=false and PasswordLength=0")
	}
	out := string(data)
	for _, want := range []string{
		"variant: fcos",
		"metalprobe.service",
		"ctr run --rm --net-host --privileged metalprobe:test metalprobe",
		"--registry-url=http://registry:10000 --server-uuid=some-uuid",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ignition does not contain %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "passwd") {
		t.Error("ignition should not contain passwd section when credentials are disabled")
	}
}

func TestDiscoveryIgnitionWithCredentials(t *testing.T) {
	p := &DiscoveryProvider{
		ProbeImage:   "metalprobe:test",
		RegistryURL:  "http://registry:10000",
		EnableSSH:    true,
		EnablePasswd: true,
	}
	server := &metalv1alpha1.Server{
		Spec: metalv1alpha1.ServerSpec{SystemUUID: "some-uuid"},
	}

	data, creds, err := p.Ignition(context.Background(), server)
	if err != nil {
		t.Fatal(err)
	}
	if creds == nil {
		t.Fatal("expected credentials when EnableSSH=true and EnablePasswd=true")
	}
	if len(creds.SSHPublicKey) == 0 || len(creds.SSHPrivateKey) == 0 {
		t.Error("expected non-empty SSH keypair")
	}
	if len(creds.Password) != 16 {
		t.Errorf("expected password length 16, got %d", len(creds.Password))
	}
	if len(creds.PasswordHash) == 0 {
		t.Error("expected non-empty password hash")
	}
	out := string(data)
	if !strings.Contains(out, "passwd") {
		t.Error("ignition should contain passwd section when credentials are enabled")
	}
	if !strings.Contains(out, "metal") {
		t.Error("ignition should contain user 'metal'")
	}
	if !strings.Contains(out, "ssh_authorized_keys") {
		t.Error("ignition should contain ssh_authorized_keys")
	}
	if !strings.Contains(out, "password_hash") {
		t.Error("ignition should contain password_hash")
	}
}
