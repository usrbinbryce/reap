package engine

import (
	"context"
	"sync"
)

// A gate pauses workers without tearing anything down
type gate struct {
	mu     sync.Mutex
	paused bool
	open   chan struct{} // closed when running
}

func newGate() *gate {
	ch := make(chan struct{})
	close(ch)
	return &gate{open: ch}
}

func (g *gate) Pause() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.paused {
		g.paused = true
		g.open = make(chan struct{})
	}
}

func (g *gate) Resume() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.paused {
		g.paused = false
		close(g.open)
	}
}

func (g *gate) Paused() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.paused
}

func (g *gate) Wait(ctx context.Context) {
	g.mu.Lock()
	ch := g.open
	g.mu.Unlock()
	select {
	case <-ch:
	case <-ctx.Done():
	}
}
