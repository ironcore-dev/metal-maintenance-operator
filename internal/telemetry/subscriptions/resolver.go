// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package subscriptions

import (
	"context"
	"fmt"

	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Resolved holds the live BMC object and the credentials needed to talk to it.
type Resolved struct {
	BMC      *metalv1alpha1.BMC
	Username string
	Password string
}

// Resolver fetches the live BMC object and its credentials. The cache-backed
// production implementation does this through the controller-runtime cache so
// the call is in-memory; tests inject a fake Resolver that returns canned data.
type Resolver interface {
	Resolve(ctx context.Context, name string) (*Resolved, error)
}

// CacheResolver reads the live BMC from a controller-runtime client (typically
// backed by the cache) and pulls credentials from the referenced BMCSecret.
type CacheResolver struct {
	// Reader is the controller-runtime client.
	Reader client.Reader
}

var _ Resolver = (*CacheResolver)(nil)

// Resolve fetches the BMC by name, then the referenced BMCSecret, and returns
// both with the username/password decoded.
func (r *CacheResolver) Resolve(ctx context.Context, name string) (*Resolved, error) {
	bmcObj := &metalv1alpha1.BMC{}
	if err := r.Reader.Get(ctx, client.ObjectKey{Name: name}, bmcObj); err != nil {
		return nil, fmt.Errorf("get BMC %q: %w", name, err)
	}
	if bmcObj.Spec.BMCSecretRef.Name == "" {
		return nil, fmt.Errorf("BMC %q has no BMCSecretRef", name)
	}
	secret := &metalv1alpha1.BMCSecret{}
	// BMCSecret is cluster-scoped (per metal-operator's bmcsecret_types.go
	// +kubebuilder:resource:scope=Cluster); pass only the name.
	secretKey := client.ObjectKey{Name: bmcObj.Spec.BMCSecretRef.Name}
	if err := r.Reader.Get(ctx, secretKey, secret); err != nil {
		return nil, fmt.Errorf("get BMCSecret %q for BMC %q: %w", secretKey.Name, name, err)
	}
	user, pass := stringOr(secret.Data, secret.StringData, "username"),
		stringOr(secret.Data, secret.StringData, "password")
	if user == "" || pass == "" {
		return nil, fmt.Errorf("BMCSecret %q for BMC %q is missing username or password", secretKey.Name, name)
	}
	return &Resolved{BMC: bmcObj, Username: user, Password: pass}, nil
}

// stringOr returns the value at key from data if present, else from stringData.
func stringOr(data map[string][]byte, stringData map[string]string, key string) string {
	if v, ok := data[key]; ok && len(v) > 0 {
		return string(v)
	}
	return stringData[key]
}
