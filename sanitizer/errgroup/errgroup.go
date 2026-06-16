// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company and IronCore contributors
// SPDX-License-Identifier: Apache-2.0

package errgroup

import (
	"context"
	"fmt"
	"sync"
)

type Group struct {
	wg     sync.WaitGroup
	cancel context.CancelCauseFunc
	sem    chan struct{}

	errOnce sync.Once
	err     error
}

func New(ctx context.Context) (*Group, context.Context) {
	ctx, cancel := context.WithCancelCause(ctx)
	return &Group{cancel: cancel}, ctx
}

func (g *Group) done() {
	if g.sem != nil {
		<-g.sem
	}
	g.wg.Done()
}

func (g *Group) Wait() error {
	g.wg.Wait()
	g.cancel(g.err)
	return g.err
}

func (g *Group) SetLimit(n int) {
	if n < 0 {
		g.sem = nil
		return
	}
	if active := len(g.sem); active != 0 {
		panic(fmt.Errorf("errgroup: modify limit while %d goroutines in the group are still active", n))
	}
	g.sem = make(chan struct{}, n)
}

func (g *Group) run(f func() error) {
	g.wg.Add(1)
	go func() {
		defer g.done()

		if err := f(); err != nil {
			g.errOnce.Do(func() {
				g.err = err
				g.cancel(g.err)
			})
		}
	}()
}

func (g *Group) Go(f func() error) {
	if g.sem != nil {
		g.sem <- struct{}{}
	}

	g.run(f)
}

func (g *Group) GoContext(ctx context.Context, f func() error) bool {
	if g.sem != nil {
		select {
		case g.sem <- struct{}{}:
		case <-ctx.Done():
			return false
		}
	}

	g.run(f)
	return true
}
