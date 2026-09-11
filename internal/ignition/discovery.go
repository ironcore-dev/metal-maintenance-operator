// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package ignition

import (
	"bytes"
	"context"
	"fmt"

	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

// DiscoveryProvider renders the ignition config that boots the metalprobe
// agent on a Server during discovery.
type DiscoveryProvider struct {
	ProbeImage  string
	RegistryURL string
}

// Ignition renders the discovery ignition for the given server.
func (p *DiscoveryProvider) Ignition(_ context.Context, server *metalv1alpha1.Server) ([]byte, error) {
	var buf bytes.Buffer
	if err := discoveryIgnitionYAMLTemplate.Execute(&buf, struct {
		Image string
		Flags string
	}{
		Image: p.ProbeImage,
		Flags: fmt.Sprintf("--registry-url=%s --server-uuid=%s", p.RegistryURL, server.Spec.SystemUUID),
	}); err != nil {
		return nil, fmt.Errorf("render discovery ignition: %w", err)
	}
	return buf.Bytes(), nil
}
