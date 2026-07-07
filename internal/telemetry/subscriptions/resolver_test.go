// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package subscriptions_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/subscriptions"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newReader(objs ...client.Object) client.Reader {
	s := runtime.NewScheme()
	_ = metalv1alpha1.AddToScheme(s)
	return fake.NewClientBuilder().WithScheme(s).WithObjects(objs...).Build()
}

func TestCacheResolver_Resolve_HappyPath(t *testing.T) {
	bmcObj := &metalv1alpha1.BMC{
		// BMC is cluster-scoped (per metal-operator's bmc_types.go), so
		// ObjectMeta has no Namespace; BMCSecret lives in a real namespace.
		ObjectMeta: metav1.ObjectMeta{Name: testBMCName},
		Spec: metalv1alpha1.BMCSpec{
			BMCSecretRef: corev1.LocalObjectReference{Name: testBMCCredsName},
		},
	}
	secret := &metalv1alpha1.BMCSecret{
		ObjectMeta: metav1.ObjectMeta{Name: testBMCCredsName},
		Data: map[string][]byte{
			secretUsernameKey: []byte(testUsername),
			"password":        []byte("hunter2"),
		},
	}
	r := &subscriptions.CacheResolver{Reader: newReader(bmcObj, secret)}

	got, err := r.Resolve(context.Background(), testBMCName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Username != testUsername || got.Password != "hunter2" {
		t.Errorf("credentials mismatch: username=%q, password=<redacted, length=%d>", got.Username, len(got.Password))
	}
	if got.BMC.Name != testBMCName {
		t.Errorf("BMC name: got %q", got.BMC.Name)
	}
}

func TestCacheResolver_Resolve_BMCNotFound(t *testing.T) {
	r := &subscriptions.CacheResolver{Reader: newReader()}
	_, err := r.Resolve(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error")
	}
	if !apierrors.IsNotFound(errors.Unwrap(err)) && !apierrors.IsNotFound(err) {
		t.Errorf("expected NotFound, got %v", err)
	}
}

func TestCacheResolver_Resolve_MissingSecretRef(t *testing.T) {
	bmcObj := &metalv1alpha1.BMC{
		ObjectMeta: metav1.ObjectMeta{Name: testBMCName},
	}
	r := &subscriptions.CacheResolver{Reader: newReader(bmcObj)}
	_, err := r.Resolve(context.Background(), testBMCName)
	if err == nil {
		t.Fatal("expected error for missing BMCSecretRef")
	}
}

func TestCacheResolver_Resolve_MissingCredentialFields(t *testing.T) {
	bmcObj := &metalv1alpha1.BMC{
		ObjectMeta: metav1.ObjectMeta{Name: testBMCName},
		Spec: metalv1alpha1.BMCSpec{
			BMCSecretRef: corev1.LocalObjectReference{Name: testBMCCredsName},
		},
	}
	// Username present but no password.
	secret := &metalv1alpha1.BMCSecret{
		ObjectMeta: metav1.ObjectMeta{Name: testBMCCredsName},
		Data:       map[string][]byte{secretUsernameKey: []byte(testUsername)},
	}
	r := &subscriptions.CacheResolver{Reader: newReader(bmcObj, secret)}
	_, err := r.Resolve(context.Background(), testBMCName)
	if err == nil {
		t.Fatal("expected error for missing password")
	}
}

func TestCacheResolver_Resolve_StringDataFallback(t *testing.T) {
	bmcObj := &metalv1alpha1.BMC{
		ObjectMeta: metav1.ObjectMeta{Name: testBMCName},
		Spec: metalv1alpha1.BMCSpec{
			BMCSecretRef: corev1.LocalObjectReference{Name: testBMCCredsName},
		},
	}
	// Credentials only in StringData — resolver must fall back from Data.
	secret := &metalv1alpha1.BMCSecret{
		ObjectMeta: metav1.ObjectMeta{Name: testBMCCredsName},
		StringData: map[string]string{
			secretUsernameKey: testUsername,
			"password":        "p",
		},
	}
	r := &subscriptions.CacheResolver{Reader: newReader(bmcObj, secret)}
	got, err := r.Resolve(context.Background(), testBMCName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Username != testUsername || got.Password != "p" {
		t.Errorf("credentials mismatch: username=%q, password=<redacted, length=%d>", got.Username, len(got.Password))
	}
}

func TestCacheResolver_Resolve_SecretNotFound(t *testing.T) {
	bmcObj := &metalv1alpha1.BMC{
		ObjectMeta: metav1.ObjectMeta{Name: testBMCName},
		Spec: metalv1alpha1.BMCSpec{
			BMCSecretRef: corev1.LocalObjectReference{Name: "missing-secret"},
		},
	}
	r := &subscriptions.CacheResolver{Reader: newReader(bmcObj)}
	_, err := r.Resolve(context.Background(), testBMCName)
	if err == nil {
		t.Fatal("expected error")
	}
	// Sanity: this should be a wrapped NotFound — the poller relies on
	// apierrors.IsNotFound walking the chain.
	if !apierrors.IsNotFound(err) {
		// Synthesise a clearer failure than just "got %v"
		gr := schema.GroupResource{Group: "metal.ironcore.dev", Resource: "bmcsecrets"}
		t.Errorf("expected IsNotFound to recognise wrapped %s/missing-secret error; got %v", gr, err)
	}
}
