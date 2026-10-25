package maintenance

import (
	"strings"
	"testing"
	"time"
)

func (f *fakeBackend) count(call string) int {
	n := 0
	for _, c := range f.list() {
		if c == call {
			n++
		}
	}
	return n
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A series of backups ends in one refresh with the final state: repository
// size once, each affected server once, nothing before the quiet period.
func TestBackupSeriesIsMeasuredAfterTheLastBackup(t *testing.T) {
	f := newFixture(t)
	f.svc.Cfg.StatsQuiet = 80 * time.Millisecond
	for _, s := range []string{"srv-a", "srv-b", "srv-c"} {
		f.seedBackup(t, s, "b-"+s)
	}
	for i := 0; i < 20; i++ {
		f.svc.BackupDone([]string{"srv-a", "srv-b"}[i%2])
		time.Sleep(5 * time.Millisecond)
	}
	if n := len(f.be.list()); n != 0 {
		t.Fatalf("measured during the series: %v", f.be.list())
	}
	waitFor(t, "refresh", func() bool { return len(f.be.tagCalls()) == 2 })
	f.svc.Wait()
	if got := strings.Join(f.be.tagCalls(), ","); got != "server:srv-a,server:srv-b" {
		t.Fatalf("calls: %s", got)
	}
	if f.be.count("stats") != 1 {
		t.Fatalf("repo stats calls: %v", f.be.list())
	}
	// A second series later is measured again (not only the first backup ever).
	f.svc.BackupDone("srv-c")
	waitFor(t, "second refresh", func() bool { return f.be.count("stats server:srv-c") == 1 })
	f.svc.Wait()
	if f.be.count("stats") != 2 || f.be.count("stats server:srv-a") != 1 {
		t.Fatalf("calls: %v", f.be.list())
	}
}

// Backups that keep coming cannot postpone the refresh beyond StatsMaxWait.
func TestBackupStatsMaxWait(t *testing.T) {
	f := newFixture(t)
	f.svc.Now = time.Now
	f.svc.Cfg.StatsQuiet = 300 * time.Millisecond
	f.svc.Cfg.StatsMaxWait = 150 * time.Millisecond
	f.seedBackup(t, "srv-a", "b1")
	stop := time.After(2 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for f.be.count("stats") == 0 {
		select {
		case <-tick.C:
			f.svc.BackupDone("srv-a")
		case <-stop:
			t.Fatal("refresh postponed by the ongoing backups")
		}
	}
	f.svc.Close()
	f.svc.Wait()
}

// While maintenance runs the refresh waits instead of being dropped.
func TestBackupStatsWaitsForMaintenance(t *testing.T) {
	f := newFixture(t)
	f.svc.Cfg.StatsQuiet = 30 * time.Millisecond
	f.seedBackup(t, "srv-a", "b1")
	f.svc.busy.Store(true)
	f.svc.BackupDone("srv-a")
	time.Sleep(150 * time.Millisecond)
	if n := len(f.be.list()); n != 0 {
		t.Fatalf("measured during maintenance: %v", f.be.list())
	}
	f.svc.busy.Store(false)
	waitFor(t, "refresh after maintenance", func() bool { return f.be.count("stats server:srv-a") == 1 })
	f.svc.Close()
	f.svc.Wait()
}
