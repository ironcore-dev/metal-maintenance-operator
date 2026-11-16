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

	data, err := p.Ignition(context.Background(), server)
	if err != nil {
		t.Fatal(err)
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
}
