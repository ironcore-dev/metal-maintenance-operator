// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"context"
	"crypto/rand"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// HMACKeyDataKey is the Secret data key holding the raw HMAC signing key bytes.
	HMACKeyDataKey = "key"
	// HMACKeyLen is the HMAC key length in bytes (256-bit).
	HMACKeyLen = 32
)

// EnsureHMACKey returns the operator's HMAC signing key, creating the backing
// Secret if it does not yet exist. The Secret is created in the given namespace
// with the given name.
func EnsureHMACKey(ctx context.Context, c client.Client, namespace, name string) ([]byte, error) {
	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: namespace, Name: name}
	if err := c.Get(ctx, key, secret); err == nil {
		if k, ok := secret.Data[HMACKeyDataKey]; ok && len(k) == HMACKeyLen {
			return k, nil
		}
		return nil, fmt.Errorf("hmac key Secret %s/%s exists but has no valid %q entry", namespace, name, HMACKeyDataKey)
	} else if !apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("failed to get hmac key Secret %s/%s: %w", namespace, name, err)
	}

	raw := make([]byte, HMACKeyLen)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("failed to generate hmac key: %w", err)
	}

	newSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Data: map[string][]byte{HMACKeyDataKey: raw},
	}
	if err := c.Create(ctx, newSecret); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Another replica created it concurrently — read and return it.
			if err2 := c.Get(ctx, key, secret); err2 != nil {
				return nil, fmt.Errorf("hmac key Secret created concurrently but unreadable: %w", err2)
			}
			if k, ok := secret.Data[HMACKeyDataKey]; ok && len(k) == HMACKeyLen {
				return k, nil
			}
			return nil, fmt.Errorf("concurrently created hmac key Secret has no valid %q entry", HMACKeyDataKey)
		}
		return nil, fmt.Errorf("failed to create hmac key Secret %s/%s: %w", namespace, name, err)
	}
	return raw, nil
}
