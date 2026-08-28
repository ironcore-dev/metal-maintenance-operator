// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package main

import (
	"context"
	"testing"
)

// When the run completes at the same moment the context is cancelled, both
// channels are ready and a bare select could pick either. finished must always
// report the completion, otherwise a successful configure is reported as a
// context error.
func TestFinishedPrefersDoneWhenBothReady(t *testing.T) {
	for range 1000 {
		done := make(chan struct{})
		close(done)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if !finished(ctx, done) {
			t.Fatal("finished = false, want true when the run completed alongside cancellation")
		}
	}
}

func TestFinishedFalseWhenOnlyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if finished(ctx, make(chan struct{})) {
		t.Fatal("finished = true, want false when only the context ended")
	}
}

func TestFinishedTrueWhenDoneAndContextLive(t *testing.T) {
	done := make(chan struct{})
	close(done)
	if !finished(context.Background(), done) {
		t.Fatal("finished = false, want true when done is closed")
	}
}
