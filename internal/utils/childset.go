// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package utils

import (
	"context"
	"errors"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	metalv1alpha1 "github.com/ironcore-dev/metal-operator/api/v1alpha1"
)

// ChildState buckets a child resource's current lifecycle state for status aggregation.
type ChildState int

const (
	// ChildPending indicates the child has not started processing yet.
	ChildPending ChildState = iota
	// ChildInProgress indicates the child is actively being processed.
	ChildInProgress
	// ChildSucceeded indicates the child reached a successful terminal state.
	ChildSucceeded
	// ChildFailed indicates the child reached a failed terminal state.
	ChildFailed
)

// ChildCounts holds the aggregated child-state counters common to every Set's status type.
type ChildCounts struct {
	Available  int32
	Pending    int32
	InProgress int32
	Succeeded  int32
	Failed     int32
}

// ChildAdapter adapts a concrete child resource type (e.g. BIOSSettings, BIOSVersion,
// FirmwareUpdate) so that ChildManager can create, prune, patch and aggregate status for
// it without per-type duplication. Implementations are typically built per-reconcile as a
// closure over the owning Set, so ApplyTemplate can read the Set's current template.
type ChildAdapter[C client.Object] struct {
	// NewChild returns a new, empty child object with only its Name set.
	NewChild func(name string) C
	// TargetName returns the name of the Server this child targets, or "" if unset.
	TargetName func(child C) string
	// SetTargetRef sets the Server reference on a newly-created child.
	SetTargetRef func(child C, serverName string)
	// ApplyTemplate copies the owning Set's template onto the child's Spec.
	ApplyTemplate func(child C)
	// IsInProgress reports whether the child is currently being processed, so it must not
	// be patched or deleted outright.
	IsInProgress func(child C) bool
	// State buckets the child's current status for aggregation.
	State func(child C) ChildState
	// MaintenanceRefs returns the ServerMaintenance references associated with the child,
	// used to check whether a maintenance window is still active before deletion.
	MaintenanceRefs func(child C) []metalv1alpha1.ObjectReference
}

// ChildManager runs the reconciliation steps shared by the "Set" fan-out controllers
// (e.g. BIOSSettingsSet, BIOSVersionSet, FirmwareUpdateSet): creating one child resource
// per selected Server, keeping it patched from a template, pruning children whose Server
// no longer matches, and aggregating child status for the Set's own status.
//
// Each concrete controller stays responsible for its own Reconcile/delete control flow,
// selector handling, finalizer handling, owned-list retrieval and status-type mapping; it
// delegates the repeated create/prune/patch/aggregate logic here via a ChildAdapter
// describing how to read/write its specific child type.
type ChildManager[C client.Object] struct {
	Client client.Client
}

// EnsureForServers creates a child (named deterministically via VersionSetChildName) for
// every server in servers that is not already represented in owned.
func (m *ChildManager[C]) EnsureForServers(
	ctx context.Context,
	adapter ChildAdapter[C],
	set client.Object,
	servers []metalv1alpha1.Server,
	owned []C,
) error {
	log := ctrl.LoggerFrom(ctx)
	withChild := make(map[string]bool, len(owned))
	for _, child := range owned {
		if name := adapter.TargetName(child); name != "" {
			withChild[name] = true
		}
	}

	var errs []error
	for _, server := range servers {
		if withChild[server.Name] {
			continue
		}
		childName := VersionSetChildName(set.GetName(), server.Name)
		child := adapter.NewChild(childName)
		child.SetNamespace(set.GetNamespace())

		opResult, err := controllerutil.CreateOrPatch(ctx, m.Client, child, func() error {
			adapter.ApplyTemplate(child)
			adapter.SetTargetRef(child, server.Name)
			return controllerutil.SetControllerReference(set, child, m.Client.Scheme())
		})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		log.V(1).Info("Created child resource", "Child", child.GetName(), "Server", server.Name, "Operation", opResult)
	}
	return errors.Join(errs...)
}

// DeleteOrphans deletes children whose target Server no longer matches servers, skipping
// any that are in-progress with an active ServerMaintenance window.
func (m *ChildManager[C]) DeleteOrphans(
	ctx context.Context,
	adapter ChildAdapter[C],
	servers []metalv1alpha1.Server,
	owned []C,
) error {
	log := ctrl.LoggerFrom(ctx)
	serverSet := make(map[string]struct{}, len(servers))
	for _, s := range servers {
		serverSet[s.Name] = struct{}{}
	}

	var errs []error
	for _, child := range owned {
		name := adapter.TargetName(child)
		if name == "" {
			continue
		}
		if _, ok := serverSet[name]; ok {
			continue
		}

		if adapter.IsInProgress(child) {
			refs := adapter.MaintenanceRefs(child)
			if len(refs) > 0 {
				active, err := IsAnyServerMaintenanceActive(ctx, m.Client, refs)
				if err != nil {
					errs = append(errs, fmt.Errorf("failed to check maintenance state for %s: %w", child.GetName(), err))
					continue
				}
				if active {
					log.V(1).Info("Waiting for maintenance to complete before deletion", "Child", child.GetName())
					continue
				}
			}
		}
		if err := m.Client.Delete(ctx, child); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// PatchFromTemplate re-applies the owning Set's template onto every non-in-progress child.
// It returns true if at least one child is currently in-progress (and therefore the Set
// should requeue rather than consider itself converged).
func (m *ChildManager[C]) PatchFromTemplate(ctx context.Context, adapter ChildAdapter[C], owned []C) (bool, error) {
	log := ctrl.LoggerFrom(ctx)
	if len(owned) == 0 {
		return false, nil
	}

	var pending bool
	var errs []error
	for _, child := range owned {
		if adapter.IsInProgress(child) {
			pending = true
			continue
		}
		opResult, err := controllerutil.CreateOrPatch(ctx, m.Client, child, func() error {
			adapter.ApplyTemplate(child)
			return nil
		})
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if opResult != controllerutil.OperationResultNone {
			log.V(1).Info("Patched child with updated spec", "Child", child.GetName(), "Operation", opResult)
		}
	}
	return pending, errors.Join(errs...)
}

// Aggregate buckets the owned children's states into ChildCounts for status reporting.
func (m *ChildManager[C]) Aggregate(adapter ChildAdapter[C], owned []C) ChildCounts {
	counts := ChildCounts{Available: int32(len(owned))}
	for _, child := range owned {
		switch adapter.State(child) {
		case ChildSucceeded:
			counts.Succeeded++
		case ChildFailed:
			counts.Failed++
		case ChildInProgress:
			counts.InProgress++
		default:
			counts.Pending++
		}
	}
	return counts
}

// DeletableCount returns how many owned children are safe to delete right now, i.e. are
// not blocked on an active ServerMaintenance window. It is used by a Set's delete path to
// decide whether its finalizer can be removed yet.
func (m *ChildManager[C]) DeletableCount(ctx context.Context, adapter ChildAdapter[C], owned []C) (int, error) {
	var errs []error
	deletable := 0
	for _, child := range owned {
		refs := adapter.MaintenanceRefs(child)
		if len(refs) == 0 {
			deletable++
			continue
		}
		active, err := IsAnyServerMaintenanceActive(ctx, m.Client, refs)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed to check maintenance state for %s: %w", child.GetName(), err))
			continue
		}
		if !active {
			deletable++
		}
	}
	return deletable, errors.Join(errs...)
}
