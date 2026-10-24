package gate

import (
	"context"
	"testing"
	"time"
)

func TestPauseHoldsNewWorkUntilResume(t *testing.T) {
	var g Gate
	ctx := context.Background()
	if err := g.Enter(ctx); err != nil {
		t.Fatal(err)
	}
	g.Pause()

	// Running work is still counted; a new Enter blocks.
	entered := make(chan struct{})
	go func() {
		if g.Enter(ctx) == nil {
			close(entered)
		}
	}()
	select {
	case <-entered:
		t.Fatal("Enter passed a paused gate")
	case <-time.After(100 * time.Millisecond):
	}

	wctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if err := g.WaitIdle(wctx); err == nil {
		t.Fatal("WaitIdle returned while work was active")
	}
	g.Leave()
	if err := g.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}

	g.Resume()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Enter still blocked after Resume")
	}
	if g.Active() != 1 || g.Paused() {
		t.Fatalf("active=%d paused=%v", g.Active(), g.Paused())
	}
}

func TestEnterCancelled(t *testing.T) {
	var g Gate
	g.Pause()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := g.Enter(ctx); err == nil {
		t.Fatal("expected context error")
	}
	if g.Active() != 0 {
		t.Fatal("active leaked")
	}
}
