// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/go-logr/logr"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/telemetry/subscriptions"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// configKey is the well-known ConfigMap data key the loader reads.
const configKey = "config.yaml"

// ConfigLoader watches the telemetry ConfigMap and atomic-swaps a
// *subscriptions.Config pointer on every change. The most-recent
// successfully-parsed config is always available via Config(); validation
// failures keep the last-good config in place and log a warning.
type ConfigLoader struct {
	Cache     cache.Cache   // controller-runtime cache (manager.GetCache())
	Client    client.Client // cache-backed reader, for the initial Get and reload reads
	Namespace string
	Name      string
	Log       logr.Logger

	current atomic.Pointer[subscriptions.Config]
}

// Config returns the most recent successfully-parsed config.
func (l *ConfigLoader) Config() *subscriptions.Config {
	return l.current.Load()
}

// Start installs the watch and blocks until ctx is cancelled. Implements
// manager.Runnable.
func (l *ConfigLoader) Start(ctx context.Context) error {
	if l.Cache == nil {
		return errors.New("ConfigLoader: Cache is required")
	}
	if l.Client == nil {
		return errors.New("ConfigLoader: Client is required")
	}
	if l.Namespace == "" || l.Name == "" {
		return errors.New("ConfigLoader: Namespace and Name are required")
	}

	if err := l.reload(ctx); err != nil {
		l.Log.Info("Initial ConfigMap load deferred to watch", "reason", err.Error())
	}

	inf, err := l.Cache.GetInformer(ctx, &corev1.ConfigMap{})
	if err != nil {
		return fmt.Errorf("ConfigLoader: get ConfigMap informer: %w", err)
	}
	reg, err := inf.AddEventHandler(l.handler(ctx))
	if err != nil {
		return fmt.Errorf("ConfigLoader: add event handler: %w", err)
	}
	defer func() { _ = inf.RemoveEventHandler(reg) }()

	<-ctx.Done()
	l.Log.Info("Config loader stopped")
	return nil
}

func (l *ConfigLoader) handler(ctx context.Context) toolscache.ResourceEventHandler {
	onChange := func(obj any) {
		cm, ok := obj.(*corev1.ConfigMap)
		if !ok || cm.Namespace != l.Namespace || cm.Name != l.Name {
			return
		}
		if err := l.reload(ctx); err != nil {
			l.Log.Error(err, "ConfigMap reload failed; keeping last-good config")
		}
	}
	return toolscache.ResourceEventHandlerFuncs{
		AddFunc:    onChange,
		UpdateFunc: func(_, newObj any) { onChange(newObj) },
		// Deletes leave the last-good config in place.
	}
}

// reload fetches the ConfigMap, parses + validates the config.yaml key.
func (l *ConfigLoader) reload(ctx context.Context) error {
	cm := &corev1.ConfigMap{}
	if err := l.Client.Get(ctx, types.NamespacedName{Name: l.Name, Namespace: l.Namespace}, cm); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("ConfigMap %s/%s not found", l.Namespace, l.Name)
		}
		return fmt.Errorf("get ConfigMap %s/%s: %w", l.Namespace, l.Name, err)
	}
	raw, ok := cm.Data[configKey]
	if !ok {
		return fmt.Errorf("ConfigMap %s/%s is missing key %q", l.Namespace, l.Name, configKey)
	}
	cfg, errList := subscriptions.Parse([]byte(raw))
	if len(errList) > 0 {
		return fmt.Errorf("invalid telemetry config: %w", errors.New(errList.ToAggregate().Error()))
	}
	previous := l.current.Swap(cfg)
	if previous == nil {
		l.Log.Info("Initial telemetry config loaded",
			"subscriptionReconcileInterval", cfg.SubscriptionReconcileInterval,
			"perBMCTimeout", cfg.PerBMCTimeout,
			"eventBasedRows", len(cfg.EventBasedHardware))
	} else {
		l.Log.V(1).Info("Telemetry config reloaded")
	}
	return nil
}
