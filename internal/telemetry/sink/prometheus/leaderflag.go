// SPDX-FileCopyrightText: SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package prometheus

import (
	"context"
	"sync"
)

// LeaderFlag gates metrics emission to the leader pod and serialises leadership
// handoff with in-flight scrapes. It implements manager.Runnable with
// NeedLeaderElection returning true, so the manager only calls Start while this
// pod holds the lease.
//
// Internally a sync.RWMutex is used: Collect holds a read lock for its entire
// duration; Start acquires the write lock before toggling active, so leadership
// activation and deactivation wait for any concurrent Collect to complete.
type LeaderFlag struct {
	mu     sync.RWMutex
	active bool
}

// IsLeader reports whether this pod currently holds the leader lease.
func (f *LeaderFlag) IsLeader() bool {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.active
}

// AcquireCollect takes a shared read lock and returns true if this pod is the
// leader. The caller MUST call ReleaseCollect once collection is complete.
// Holding the read lock prevents leadership from being released mid-scrape:
// Start blocks on the write lock until all AcquireCollect holders release.
func (f *LeaderFlag) AcquireCollect() bool {
	f.mu.RLock()
	if !f.active {
		f.mu.RUnlock()
		return false
	}
	return true // caller owns the read lock
}

// ReleaseCollect releases the read lock acquired by AcquireCollect.
func (f *LeaderFlag) ReleaseCollect() {
	f.mu.RUnlock()
}

// Activate sets the flag to active and returns a deactivate function.
// Used in tests and in single-replica deployments where leader election is disabled.
func (f *LeaderFlag) Activate() func() {
	f.mu.Lock()
	f.active = true
	f.mu.Unlock()
	return func() {
		f.mu.Lock()
		f.active = false
		f.mu.Unlock()
	}
}

// Start implements manager.Runnable. It is only invoked while this pod holds
// the leader lease; it activates the flag, then on context cancellation waits
// for any in-flight Collect to finish before deactivating.
func (f *LeaderFlag) Start(ctx context.Context) error {
	f.mu.Lock()
	f.active = true
	f.mu.Unlock()

	<-ctx.Done()

	// Acquiring the write lock here drains any concurrent Collect that passed
	// AcquireCollect before the context was cancelled.
	f.mu.Lock()
	f.active = false
	f.mu.Unlock()
	return nil
}

// NeedLeaderElection implements manager.LeaderElectionRunnable, ensuring the
// manager only calls Start on the elected leader pod.
func (f *LeaderFlag) NeedLeaderElection() bool { return true }
