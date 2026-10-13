package engine

import (
	"context"
	"testing"
	"time"
)

func TestGate(t *testing.T) {
	g := newGate()
	if g.Paused() {
		t.Fatal("new gate is open")
	}
	g.Wait(context.Background()) // returns at once

	g.Pause()
	g.Pause() // idempotent
	if !g.Paused() {
		t.Fatal("Pause")
	}
	released := make(chan struct{})
	go func() { g.Wait(context.Background()); close(released) }()
	select {
	case <-released:
		t.Fatal("Wait returned while paused")
	case <-time.After(30 * time.Millisecond):
	}
	g.Resume()
	g.Resume() // idempotent
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("Resume didn't release waiters")
	}

	g.Pause()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g.Wait(ctx) // a done context releases a paused wait
}
