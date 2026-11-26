// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package ignition

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/token"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

const probePasswordLength = 16

// DiscoveryProvider renders the ignition config that boots the metalprobe
// agent on a Server during discovery.
type DiscoveryProvider struct {
	ProbeImage   string
	RegistryURL  string
	EnableSSH    bool
	EnablePasswd bool
	User         string // defaults to "metal" if empty
	SigningKey   []byte
	TokenExpiry  time.Duration
}

// Ignition renders the discovery ignition for the given server and returns
// any generated credentials. Credentials is nil when both EnableSSH and
// EnablePasswd are false.
func (p *DiscoveryProvider) Ignition(_ context.Context, server *metalv1alpha1.Server) ([]byte, *Credentials, error) {
	user := p.User
	if user == "" {
		user = "metal"
	}

	var creds *Credentials

	if p.EnableSSH || p.EnablePasswd {
		creds = &Credentials{User: user}
		if p.EnableSSH {
			pub, priv, err := GenerateSSHKeypair()
			if err != nil {
				return nil, nil, fmt.Errorf("generate ssh keypair: %w", err)
			}
			creds.SSHPublicKey = pub
			creds.SSHPrivateKey = priv
		}
		if p.EnablePasswd {
			plain, hash, err := GeneratePassword(probePasswordLength)
			if err != nil {
				return nil, nil, fmt.Errorf("generate password: %w", err)
			}
			creds.Password = plain
			creds.PasswordHash = hash
		}
	}

	sshPublicKey := ""
	passwordHash := ""
	if creds != nil {
		sshPublicKey = string(creds.SSHPublicKey)
		passwordHash = string(creds.PasswordHash)
	}

	flags := fmt.Sprintf("--registry-url=%s --server-uuid=%s", p.RegistryURL, server.Spec.SystemUUID)
	if len(p.SigningKey) > 0 {
		expiry := p.TokenExpiry
		if expiry <= 0 {
			expiry = 10 * time.Minute
		}
		tok, err := token.GenerateSignedDiscoveryToken(p.SigningKey, server.Spec.SystemUUID, expiry)
		if err != nil {
			return nil, nil, fmt.Errorf("generate discovery token: %w", err)
		}
		flags += " --registry-token=" + tok
	}

	var buf bytes.Buffer
	if err := discoveryIgnitionYAMLTemplate.Execute(&buf, struct {
		Image        string
		Flags        string
		User         string
		SSHPublicKey string
		PasswordHash string
	}{
		Image:        p.ProbeImage,
		Flags:        flags,
		User:         user,
		SSHPublicKey: sshPublicKey,
		PasswordHash: passwordHash,
	}); err != nil {
		return nil, nil, fmt.Errorf("render discovery ignition: %w", err)
	}
	return buf.Bytes(), creds, nil
}
