// Package gate lets maintenance stop new restic work (ingests, dumps) and
// wait until the running work has finished.
package gate

import (
	"context"
	"sync"
	"time"
)

// Gate counts active work items and can hold back new ones.
type Gate struct {
	mu     sync.Mutex
	paused bool
	resume chan struct{} // closed on Resume
	active int
}

// Enter blocks while the gate is paused, then counts one active item.
// Every successful Enter must be paired with Leave.
func (g *Gate) Enter(ctx context.Context) error {
	for {
		g.mu.Lock()
		if !g.paused {
			g.active++
			g.mu.Unlock()
			return nil
		}
		ch := g.resume
		g.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// TryEnter is Enter without waiting; it reports whether the gate was open.
func (g *Gate) TryEnter() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.paused {
		return false
	}
	g.active++
	return true
}

// WaitOpen blocks until the gate is not paused.
func (g *Gate) WaitOpen(ctx context.Context) error {
	g.mu.Lock()
	if !g.paused {
		g.mu.Unlock()
		return nil
	}
	ch := g.resume
	g.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *Gate) Leave() {
	g.mu.Lock()
	g.active--
	g.mu.Unlock()
}

// Pause holds back new Enter calls. Running items are not affected.
func (g *Gate) Pause() {
	g.mu.Lock()
	if !g.paused {
		g.paused = true
		g.resume = make(chan struct{})
	}
	g.mu.Unlock()
}

func (g *Gate) Resume() {
	g.mu.Lock()
	if g.paused {
		g.paused = false
		close(g.resume)
	}
	g.mu.Unlock()
}

func (g *Gate) Paused() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.paused
}

// Active returns the number of items between Enter and Leave.
func (g *Gate) Active() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active
}

// WaitIdle returns nil once no item is active, or ctx.Err().
func (g *Gate) WaitIdle(ctx context.Context) error {
	t := time.NewTicker(50 * time.Millisecond)
	defer t.Stop()
	for g.Active() > 0 {
		select {
		case <-t.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
