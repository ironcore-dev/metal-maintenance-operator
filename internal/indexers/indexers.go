// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

// Package indexers centrally registers all controller-runtime field indexers
// used across the operator's reconcilers.
//
// Registering every indexer here, unconditionally, from a single call in
// cmd/main.go avoids indexes being tied to one particular controller or
// feature (e.g. only registered when telemetry is enabled) while being
// relied upon by another, unrelated reconciler — which previously caused
// "Index with name field:... does not exist" errors at runtime.
package indexers

import (
	"context"

	baseboardv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/baseboard/v1alpha1"
	maintenancev1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/maintenance/v1alpha1"
	systemv1alpha1 "github.com/ironcore-dev/metal-maintenance-operator/api/system/v1alpha1"
	"github.com/ironcore-dev/metal-maintenance-operator/internal/constants"
	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// RegisterAll registers every field indexer shared across controllers. It
// must be called exactly once against the manager's field indexer, before
// any controller that depends on these indexes is started.
func RegisterAll(ctx context.Context, indexer client.FieldIndexer) error {
	if err := indexer.IndexField(
		ctx,
		&maintenancev1alpha1.ServerMaintenance{},
		constants.ServerRefField,
		func(rawObj client.Object) []string {
			m, ok := rawObj.(*maintenancev1alpha1.ServerMaintenance)
			if !ok {
				return nil
			}
			if m.Spec.ServerRef != nil && m.Spec.ServerRef.Name != "" {
				return []string{m.Spec.ServerRef.Name}
			}
			return nil
		}); err != nil {
		return err
	}

	if err := indexer.IndexField(
		ctx,
		&systemv1alpha1.BIOSSettings{},
		constants.ServerRefField,
		func(rawObj client.Object) []string {
			s, ok := rawObj.(*systemv1alpha1.BIOSSettings)
			if !ok {
				return nil
			}
			if s.Spec.ServerRef != nil && s.Spec.ServerRef.Name != "" {
				return []string{s.Spec.ServerRef.Name}
			}
			return nil
		}); err != nil {
		return err
	}

	if err := indexer.IndexField(
		ctx,
		&baseboardv1alpha1.BMCSettings{},
		constants.BMCRefField,
		func(rawObj client.Object) []string {
			s, ok := rawObj.(*baseboardv1alpha1.BMCSettings)
			if !ok {
				return nil
			}
			if s.Spec.BMCRef != nil && s.Spec.BMCRef.Name != "" {
				return []string{s.Spec.BMCRef.Name}
			}
			return nil
		}); err != nil {
		return err
	}

	// Consumed by BIOSSettingsReconciler.enqueueBiosSettingsByBMC as well as
	// the telemetry critical-event/subscription pipeline (when enabled) —
	// registered here unconditionally so it always exists regardless of
	// which of those features is active.
	if err := indexer.IndexField(
		ctx,
		&metalv1alpha1.Server{},
		constants.BMCRefField,
		func(rawObj client.Object) []string {
			s, ok := rawObj.(*metalv1alpha1.Server)
			if !ok {
				return nil
			}
			if s.Spec.BMCRef != nil && s.Spec.BMCRef.Name != "" {
				return []string{s.Spec.BMCRef.Name}
			}
			return nil
		}); err != nil {
		return err
	}

	return nil
}
