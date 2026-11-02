// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// Package configloader provides a generic, live-reloading ConfigMap-backed
// configuration loader for controllers. It is modeled on the telemetry
// pipeline's bespoke ConfigLoader (internal/telemetry/runtime/config_loader.go)
// but is parameterized over the config type so any controller can adopt the
// same watch/atomic-swap/keep-last-good-on-failure behavior without
// duplicating it.
//
// Usage: instantiate a Loader[T] with a Parse function that decodes and
// validates the ConfigMap's data, register it on the manager via mgr.Add,
// and read the current config at use-site via Config(). Config() returns
// nil until the first successful parse (e.g. the ConfigMap does not exist
// yet) — callers must handle nil by falling back to their own defaults.
package configloader

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DefaultKey is the well-known ConfigMap data key loaders read by default.
const DefaultKey = "config.yaml"

// Loader watches a single ConfigMap and atomic-swaps a *T pointer on every
// add/update event. The most-recently successfully-parsed config is always
// available via Config(); parse/validate failures keep the last-good config
// in place and log a warning instead of tearing anything down.
//
// Loader implements manager.Runnable, so register it via mgr.Add(loader).
type Loader[T any] struct {
	Cache     cache.Cache   // controller-runtime cache (manager.GetCache())
	Client    client.Client // cache-backed reader, for the initial Get and reload reads
	Namespace string
	Name      string
	// Key is the ConfigMap data key holding the YAML payload. Defaults to
	// DefaultKey when empty.
	Key string
	// Parse decodes and validates raw YAML into a *T. Implementations should
	// use strict decoding (reject unknown fields) and run full semantic
	// validation before returning, mirroring subscriptions.Parse.
	Parse func(raw []byte) (*T, error)
	Log   logr.Logger

	current atomic.Pointer[T]
}

// Config returns the most recently successfully-parsed config, or nil if the
// ConfigMap has never been successfully loaded (e.g. it doesn't exist yet).
func (l *Loader[T]) Config() *T {
	return l.current.Load()
}

// Set atomic-swaps the current config directly, bypassing the ConfigMap
// watch. This is intended for preloading a static config in tests (where
// standing up a real ConfigMap + cache watch is unnecessary); production
// code should rely on Start's watch instead.
func (l *Loader[T]) Set(cfg *T) {
	l.current.Store(cfg)
}

// Start installs the watch and blocks until ctx is cancelled. Implements
// manager.Runnable.
func (l *Loader[T]) Start(ctx context.Context) error {
	if l.Cache == nil {
		return errors.New("configloader.Loader: Cache is required")
	}
	if l.Client == nil {
		return errors.New("configloader.Loader: Client is required")
	}
	if l.Namespace == "" || l.Name == "" {
		return errors.New("configloader.Loader: Namespace and Name are required")
	}
	if l.Parse == nil {
		return errors.New("configloader.Loader: Parse is required")
	}
	key := l.Key
	if key == "" {
		key = DefaultKey
	}

	// Wait for the cache to be populated before the initial read; without
	// this the cache-backed client returns "not ready" and the load is
	// silently deferred until the first watch event.
	if !l.Cache.WaitForCacheSync(ctx) {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fmt.Errorf("configloader.Loader: cache sync timed out or context cancelled")
	}

	if err := l.reload(ctx, key); err != nil {
		l.Log.Info("Initial ConfigMap load deferred to watch", "name", l.Name, "namespace", l.Namespace, "reason", err.Error())
	}

	// The cache is expected to be scoped to l.Namespace/l.Name via
	// cache.ByObject by the caller (see cmd/main.go's configMapCacheByObject).
	inf, err := l.Cache.GetInformer(ctx, &corev1.ConfigMap{})
	if err != nil {
		return fmt.Errorf("configloader.Loader: get ConfigMap informer: %w", err)
	}
	reg, err := inf.AddEventHandler(l.handler(ctx, key))
	if err != nil {
		return fmt.Errorf("configloader.Loader: add event handler: %w", err)
	}
	defer func() { _ = inf.RemoveEventHandler(reg) }()

	<-ctx.Done()
	l.Log.Info("Config loader stopped", "name", l.Name, "namespace", l.Namespace)
	return nil
}

func (l *Loader[T]) handler(ctx context.Context, key string) toolscache.ResourceEventHandler {
	onChange := func(obj any) {
		if _, ok := obj.(*corev1.ConfigMap); !ok {
			return
		}
		if err := l.reload(ctx, key); err != nil {
			l.Log.Error(err, "ConfigMap reload failed; keeping last-good config", "name", l.Name, "namespace", l.Namespace)
		}
	}
	return toolscache.ResourceEventHandlerFuncs{
		AddFunc:    onChange,
		UpdateFunc: func(_, newObj any) { onChange(newObj) },
		// Deletes leave the last-good config in place.
	}
}

// reload fetches the ConfigMap, parses+validates the data[key] field via
// l.Parse, and atomic-swaps l.current on success.
func (l *Loader[T]) reload(ctx context.Context, key string) error {
	cm := &corev1.ConfigMap{}
	if err := l.Client.Get(ctx, types.NamespacedName{Name: l.Name, Namespace: l.Namespace}, cm); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("ConfigMap %s/%s not found", l.Namespace, l.Name)
		}
		return fmt.Errorf("get ConfigMap %s/%s: %w", l.Namespace, l.Name, err)
	}
	raw, ok := cm.Data[key]
	if !ok {
		return fmt.Errorf("ConfigMap %s/%s is missing key %q", l.Namespace, l.Name, key)
	}
	cfg, err := l.Parse([]byte(raw))
	if err != nil {
		return fmt.Errorf("invalid config in ConfigMap %s/%s: %w", l.Namespace, l.Name, err)
	}
	previous := l.current.Swap(cfg)
	if previous == nil {
		l.Log.Info("Initial config loaded", "name", l.Name, "namespace", l.Namespace)
	} else {
		l.Log.V(1).Info("Config reloaded", "name", l.Name, "namespace", l.Namespace)
	}
	return nil
}
