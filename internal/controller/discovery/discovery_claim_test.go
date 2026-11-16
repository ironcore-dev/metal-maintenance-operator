// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package discovery

import (
	"context"
	"testing"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/constants"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/ignition"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestDiscoveryClaimConfiguration(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := metalv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	server := &metalv1alpha1.Server{ObjectMeta: metav1.ObjectMeta{Name: "server", UID: "server-uid"}}
	provider := &ignition.DiscoveryProvider{ProbeImage: "probe", RegistryURL: "http://old-registry"}
	r := &ServerDiscoveryReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).Build(), Scheme: scheme,
		DiscoveryNamespace: "discovery", ProbeOSImage: "old-os", IgnitionProvider: provider.Ignition,
	}
	if _, err := r.getOrCreateDiscoveryClaim(ctx, server); err != nil {
		t.Fatal(err)
	}
	key := client.ObjectKey{Namespace: "discovery", Name: "server"}
	secret := &corev1.Secret{}
	if err := r.Get(ctx, key, secret); err != nil {
		t.Fatal(err)
	}
	if !metav1.IsControlledBy(secret, server) {
		t.Fatal("new Secret must be owned before claim creation")
	}
	provider.RegistryURL = "http://new-registry"
	server.Spec.SystemUUID = "new-uuid"
	r.ProbeOSImage = "new-os"
	claim, err := r.getOrCreateDiscoveryClaim(ctx, server)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Spec.Image != "new-os" || !metav1.IsControlledBy(claim, server) {
		t.Fatal("existing claim did not retain ownership and update its image")
	}
	if err := r.Get(ctx, key, secret); err != nil {
		t.Fatal(err)
	}
	want, err := provider.Ignition(ctx, server)
	if err != nil {
		t.Fatal(err)
	}
	if string(secret.Data["ignition"]) != string(want) {
		t.Fatal("existing Secret did not update its registry URL and UUID")
	}
	version := secret.ResourceVersion
	if _, err := r.getOrCreateDiscoveryClaim(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, key, secret); err != nil {
		t.Fatal(err)
	}
	if secret.ResourceVersion != version {
		t.Fatal("unchanged ignition should not update the Secret")
	}

	secret.Labels[constants.DiscoveryForUIDLabel] = "another-server"
	if err := r.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
	if _, err := r.getOrCreateDiscoveryClaim(ctx, server); err == nil {
		t.Fatal("must reject a Secret belonging to another Server")
	}
	if err := r.Get(ctx, key, claim); err != nil {
		t.Fatal(err)
	}
	claim.OwnerReferences = nil
	if err := r.Update(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if _, err := r.getOrCreateDiscoveryClaim(ctx, server); err == nil {
		t.Fatal("must reject an unowned claim")
	}
}
