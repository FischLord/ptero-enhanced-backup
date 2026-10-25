package offsite

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// A backup that finishes while a copy run is in progress must lead to another
// copy afterwards (the running copy may have listed the primary before it).
func TestBackupDuringCopyIsNotLost(t *testing.T) {
	e := newEnv(t)
	e.prim.set("a")
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.sec.copyHook = func(ctx context.Context) error {
		once.Do(func() { close(started) })
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}
	if _, err := e.svc.TriggerCopy(); err != nil {
		t.Fatal(err)
	}
	<-started
	e.prim.set("a", "b")
	e.svc.BackupDone() // arrives while the run holds the slot
	close(release)
	waitFor(t, "second copy after the first run", func() bool { return e.sec.count("copy") >= 2 })
	waitFor(t, "lag cleared", func() bool {
		st, _ := e.svc.Status(context.Background())
		return st.Lag != nil && *st.Lag == 0 && !st.Pending
	})
}

// fire after Close does nothing and starts no run that Wait would miss.
func TestFireAfterCloseIsNoop(t *testing.T) {
	e := newEnv(t)
	e.prim.set("a")
	e.svc.BackupDone()
	e.svc.Close()
	e.svc.fire()
	e.svc.Wait()
	time.Sleep(3 * e.svc.Cfg.Delay)
	if n := e.sec.count("copy"); n != 0 {
		t.Fatalf("copy ran after Close: %d", n)
	}
	if _, err := e.svc.TriggerCopy(); !errors.Is(err, ErrClosed) {
		t.Fatalf("TriggerCopy after Close: %v", err)
	}
	e.svc.Startup()
	e.svc.Nightly(context.Background())
	e.svc.Wait()
	if n := e.sec.count("copy"); n != 0 {
		t.Fatalf("work after Close: %d", n)
	}
}

// Run with -race: backups, timers, Close and Wait at the same time.
func TestBackupDoneCloseWaitRace(t *testing.T) {
	for i := 0; i < 20; i++ {
		e := newEnv(t)
		e.svc.Cfg.Delay = time.Millisecond
		e.prim.set("a")
		var wg sync.WaitGroup
		for g := 0; g < 4; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 25; j++ {
					e.svc.BackupDone()
					time.Sleep(200 * time.Microsecond)
				}
			}()
		}
		time.Sleep(3 * time.Millisecond)
		e.svc.Close()
		e.svc.Wait()
		wg.Wait()
		e.svc.Wait()
		n := e.sec.count("copy")
		time.Sleep(10 * time.Millisecond)
		if m := e.sec.count("copy"); m != n {
			t.Fatalf("a copy started after Close and Wait (%d -> %d)", n, m)
		}
	}
}
