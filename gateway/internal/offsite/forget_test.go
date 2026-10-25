package offsite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"ptero-enhanced-backup/gateway/internal/restic"
	"ptero-enhanced-backup/gateway/internal/store"
)

func names(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("p%03d", i)
	}
	return out
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// withClock gives the service a fake clock and the default grace period.
func (e *env) withClock() *clock {
	c := &clock{t: time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC)}
	e.svc.Now = c.now
	e.svc.Cfg.ForgetGrace = 0 // default: 48h
	return c
}

func (e *env) nightly() { e.svc.Nightly(context.Background()) }

func (e *env) forgetJob(t *testing.T) (result, msg string) {
	t.Helper()
	js, _ := e.st.Jobs(context.Background(), 50)
	for _, j := range js {
		if j.Type == JobForgetSecond {
			return j.Result, j.Message
		}
	}
	t.Fatal("no forget job")
	return "", ""
}

func TestForgetLimit(t *testing.T) {
	for n, want := range map[int]int{0: 1, 5: 1, 19: 1, 20: 2, 100: 10, 200: 20, 5000: 20} {
		if got := ForgetLimit(n); got != want {
			t.Errorf("ForgetLimit(%d) = %d, want %d", n, got, want)
		}
	}
}

func TestForgetCapPercent(t *testing.T) {
	e := newEnv(t)
	e.prim.set(names(30)...)
	e.nightly() // 30 copies
	e.prim.set(names(26)...)
	e.nightly() // 4 missing, limit is 3
	if len(e.sec.forgotten) != 0 || e.sec.count("forget") != 0 {
		t.Fatalf("forgot despite the limit: %v", e.sec.forgotten)
	}
	res, msg := e.forgetJob(t)
	st, _ := e.svc.Status(context.Background())
	if res != store.JobFailed || !strings.Contains(msg, "limit 3") || !st.ForgetBlocked || !strings.Contains(st.LastError, "limit 3") || e.nt.count() == 0 {
		t.Fatalf("res=%s msg=%q st=%+v notify=%d", res, msg, st, e.nt.count())
	}
	if !strings.Contains(msg, "PEB_SECONDARY_FORGET_CONFIRM=sec-id") {
		t.Fatalf("message lacks the way out: %q", msg)
	}
	// The stop survives the next (debounced) copy run, which clears ordinary errors.
	id, _ := e.st.InsertJob(context.Background(), JobCopy, "backup", store.JobQueued, time.Now())
	e.svc.copyRun(context.Background(), id)
	if st, _ = e.svc.Status(context.Background()); !st.ForgetBlocked || st.LastError == "" {
		t.Fatalf("stop was forgotten: %+v", st)
	}
	// Within the limit: allowed again and the stop is lifted.
	e.prim.set(names(28)...)
	e.nightly()
	if len(e.sec.forgotten) != 2 {
		t.Fatalf("forgotten %v", e.sec.forgotten)
	}
	if st, _ = e.svc.Status(context.Background()); st.ForgetBlocked || st.LastError != "" {
		t.Fatalf("stop not lifted: %+v", st)
	}
}

func TestForgetCapAbsolute(t *testing.T) {
	e := newEnv(t)
	e.prim.set(names(300)...)
	e.nightly()
	e.prim.set(names(279)...) // 21 missing: 10% would allow 30, the absolute cap is 20
	e.nightly()
	if e.sec.count("forget") != 0 {
		t.Fatal("forgot 21")
	}
	e.prim.set(names(280)...) // exactly 20
	e.nightly()
	if len(e.sec.forgotten) != 20 {
		t.Fatalf("forgotten %d", len(e.sec.forgotten))
	}
}

func TestForgetConfirmEnv(t *testing.T) {
	e := newEnv(t)
	e.prim.set(names(30)...)
	e.nightly()
	e.prim.set(names(10)...) // 20 missing, limit 3
	e.svc.Cfg.ForgetConfirm = "some-other-id"
	e.nightly()
	if e.sec.count("forget") != 0 {
		t.Fatal("wrong confirmation accepted")
	}
	e.svc.Cfg.ForgetConfirm = "sec-id"
	e.nightly()
	if len(e.sec.forgotten) != 20 {
		t.Fatalf("right confirmation ignored: %d", len(e.sec.forgotten))
	}
	_, msg := e.forgetJob(t)
	if !strings.Contains(msg, "remove PEB_SECONDARY_FORGET_CONFIRM") {
		t.Fatalf("no hint to remove the variable: %q", msg)
	}
	if st, _ := e.svc.Status(context.Background()); st.ForgetBlocked {
		t.Fatal("still blocked")
	}
}

func TestForgetConfirmAPIIsUsedOnce(t *testing.T) {
	e := newEnv(t)
	e.prim.set(names(30)...)
	e.nightly()
	e.prim.set(names(10)...)
	e.nightly()
	if e.sec.count("forget") != 0 {
		t.Fatal("forgot over the limit")
	}
	if err := e.svc.ConfirmForget(context.Background()); err != nil {
		t.Fatal(err)
	}
	e.nightly()
	if len(e.sec.forgotten) != 20 {
		t.Fatalf("forgotten %d", len(e.sec.forgotten))
	}
	// Used up: the next excess is stopped again.
	e.prim.set(names(2)...) // 8 more missing, 10 left on the secondary, limit 1
	e.nightly()
	if len(e.sec.forgotten) != 20 {
		t.Fatalf("approval was reused: %d", len(e.sec.forgotten))
	}
}

func TestForgetPrimaryIDChange(t *testing.T) {
	e := newEnv(t)
	e.prim.set("a", "b", "c")
	e.nightly() // first start: IDs are stored, no error
	if v, _ := e.st.State(context.Background(), statePrimaryID); v != "prim-id" {
		t.Fatalf("primary ID not stored: %q", v)
	}
	if v, _ := e.st.State(context.Background(), stateSecondaryID); v != "sec-id" {
		t.Fatalf("secondary ID not stored: %q", v)
	}
	if res, _ := e.forgetJob(t); res != store.JobOK {
		t.Fatalf("first start: %s", res)
	}
	e.prim.mu.Lock()
	e.prim.id = "new-prim-id"
	e.prim.mu.Unlock()
	e.prim.set("a", "c") // would be a normal deletion
	e.nightly()
	res, msg := e.forgetJob(t)
	if res != store.JobFailed || e.sec.count("forget") != 0 || !strings.Contains(msg, "prim-id") || !strings.Contains(msg, "new-prim-id") {
		t.Fatalf("res=%s msg=%q", res, msg)
	}
	if e.nt.count() == 0 {
		t.Fatal("no notification")
	}
	// A wrong release does not help, the right one does and stores the new ID.
	e.svc.Cfg.ForgetConfirm = "prim-id"
	e.nightly()
	if e.sec.count("forget") != 0 {
		t.Fatal("wrong release accepted")
	}
	e.svc.Cfg.ForgetConfirm = "sec-id"
	e.nightly()
	if len(e.sec.forgotten) != 1 {
		t.Fatalf("forgotten %v", e.sec.forgotten)
	}
	if v, _ := e.st.State(context.Background(), statePrimaryID); v != "new-prim-id" {
		t.Fatalf("new ID not stored: %q", v)
	}
}

func TestForgetSecondaryIDChange(t *testing.T) {
	e := newEnv(t)
	e.prim.set("a", "b", "c")
	e.nightly()
	e.sec.mu.Lock()
	e.sec.id = "other-sec"
	e.sec.mu.Unlock()
	e.prim.set("a", "c")
	e.nightly()
	res, msg := e.forgetJob(t)
	if res != store.JobFailed || e.sec.count("forget") != 0 || !strings.Contains(msg, "secondary repository ID changed") {
		t.Fatalf("res=%s msg=%q", res, msg)
	}
}

func TestForgetForeignSnapshotsStay(t *testing.T) {
	e := newEnv(t)
	e.sec.snaps = []restic.Snapshot{{ID: "own"}, {ID: "foreign-copy", Original: "zzz"}} // not copied by this gateway
	e.prim.set("a", "b", "c")
	e.nightly()
	e.prim.set("a", "c")
	e.nightly()
	if len(e.sec.forgotten) != 1 || e.sec.forgotten[0] != "c-b" {
		t.Fatalf("forgotten %v", e.sec.forgotten)
	}
	left, _ := e.sec.Snapshots(context.Background())
	have := map[string]bool{}
	for _, sn := range left {
		have[sn.ID] = true
	}
	if !have["own"] || !have["foreign-copy"] {
		t.Fatalf("foreign snapshots touched: %v", left)
	}
}

func TestForgetAdoptsEarlierCopies(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for i, snap := range []string{"known1", "known2"} { // backups this gateway knows
		id, err := e.st.CreateUpload(ctx, fmt.Sprintf("up%d", i), fmt.Sprintf("s/b%d", i), "s", fmt.Sprintf("b%d", i), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if err := e.st.CompleteBackup(ctx, id, store.Finish{SnapshotID: snap}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	// Copies from before the bookkeeping: two of known backups, one of a snapshot the gateway never saw.
	e.sec.snaps = []restic.Snapshot{{ID: "old1", Original: "known1"}, {ID: "old2", Original: "known2"}, {ID: "alien", Original: "unknown"}}
	e.prim.set("known1", "known2", "unknown")
	e.nightly()
	own, _ := e.st.OwnCopies(ctx)
	if own["old1"].OriginalID != "known1" || own["old2"].OriginalID != "known2" || own["alien"].SnapshotID != "" {
		t.Fatalf("adopted: %v", own)
	}
	e.prim.set("known2") // known1 and unknown vanish; 6 on the secondary? no: 3 old + copies
	e.nightly()
	f := map[string]bool{}
	for _, id := range e.sec.forgotten {
		f[id] = true
	}
	if !f["old1"] || f["alien"] {
		t.Fatalf("forgotten %v", e.sec.forgotten)
	}
}

func TestForgetGracePeriod(t *testing.T) {
	e := newEnv(t)
	clk := e.withClock()
	e.prim.set(names(12)...)
	e.nightly()
	e.prim.set(names(11)...) // p011 deleted
	clk.add(24 * time.Hour)
	e.nightly()
	if e.sec.count("forget") != 0 {
		t.Fatal("forgot on the first sight")
	}
	if res, msg := e.forgetJob(t); res != store.JobOK || !strings.Contains(msg, "grace") {
		t.Fatalf("res=%s msg=%q", res, msg)
	}
	clk.add(47 * time.Hour) // 47h since it went missing
	e.nightly()
	if e.sec.count("forget") != 0 {
		t.Fatal("forgot inside the grace period")
	}
	clk.add(2 * time.Hour) // 49h
	e.nightly()
	if len(e.sec.forgotten) != 1 || e.sec.forgotten[0] != "c-p011" {
		t.Fatalf("forgotten %v", e.sec.forgotten)
	}
}

func TestForgetGraceResetsWhenSnapshotReturns(t *testing.T) {
	e := newEnv(t)
	clk := e.withClock()
	e.prim.set(names(12)...)
	e.nightly()
	e.prim.set(names(11)...)
	clk.add(time.Hour)
	e.nightly()
	clk.add(40 * time.Hour)
	e.prim.set(names(12)...) // back
	e.nightly()
	e.prim.set(names(11)...) // gone again: the clock starts anew
	clk.add(time.Hour)
	e.nightly()
	clk.add(30 * time.Hour)
	e.nightly()
	if e.sec.count("forget") != 0 {
		t.Fatal("the earlier absence still counted")
	}
	clk.add(30 * time.Hour)
	e.nightly()
	if len(e.sec.forgotten) != 1 {
		t.Fatalf("forgotten %v", e.sec.forgotten)
	}
}

func TestForgetErrUnsafeIsSentinel(t *testing.T) {
	e := newEnv(t)
	e.prim.set(names(30)...)
	e.nightly()
	e.prim.set(names(10)...)
	_, _, err := e.svc.forgetStep(context.Background())
	if !errors.Is(err, ErrUnsafe) {
		t.Fatalf("%v", err)
	}
}

// The approval survives runs that forget nothing (grace period still running)
// and is used up by the run that really forgets.
func TestForgetApprovalSurvivesGracePeriod(t *testing.T) {
	e := newEnv(t)
	clk := e.withClock()
	e.prim.set(names(30)...)
	e.nightly()
	e.prim.set(names(10)...) // 20 missing, limit 3
	clk.add(24 * time.Hour)
	e.nightly() // night N: candidates wait, nothing is ready, so the cap does not block yet
	if res, msg := e.forgetJob(t); res != store.JobOK || !strings.Contains(msg, "grace") {
		t.Fatalf("res=%s msg=%q", res, msg)
	}
	if err := e.svc.ConfirmForget(context.Background()); err != nil {
		t.Fatal(err)
	}
	clk.add(24 * time.Hour)
	e.nightly() // N+1: 24h, still in grace; the approval must not be used up
	if e.sec.count("forget") != 0 {
		t.Fatal("forgot inside the grace period")
	}
	clk.add(25 * time.Hour)
	e.nightly() // N+2: due, 20 > 3, approved
	if len(e.sec.forgotten) != 20 {
		t.Fatalf("approval lost: forgotten %d", len(e.sec.forgotten))
	}
	// Used up now.
	e.prim.set(names(2)...)
	clk.add(24 * time.Hour)
	e.nightly()
	clk.add(49 * time.Hour)
	e.nightly()
	if len(e.sec.forgotten) != 20 {
		t.Fatalf("approval was reused: %d", len(e.sec.forgotten))
	}
}

// Without approval the cap stops the run as soon as the snapshots are due.
func TestForgetCapCountsOnlyDueSnapshots(t *testing.T) {
	e := newEnv(t)
	clk := e.withClock()
	e.prim.set(names(30)...)
	e.nightly()
	e.prim.set(names(10)...)
	clk.add(24 * time.Hour)
	e.nightly()
	if st, _ := e.svc.Status(context.Background()); st.ForgetBlocked {
		t.Fatal("blocked while nothing is due")
	}
	clk.add(49 * time.Hour)
	e.nightly()
	if st, _ := e.svc.Status(context.Background()); !st.ForgetBlocked || e.sec.count("forget") != 0 {
		t.Fatal("cap did not stop the due snapshots")
	}
}

// Copies that arrived in a copy run that failed afterwards are still tracked.
func TestCopiesTrackedAfterFailedCopy(t *testing.T) {
	e := newEnv(t)
	e.prim.set(names(3)...)
	e.sec.copyHook = func(context.Context) error {
		e.sec.mu.Lock()
		e.sec.snaps = append(e.sec.snaps, restic.Snapshot{ID: "c-p000", Original: "p000"})
		e.sec.mu.Unlock()
		return errors.New("connection reset")
	}
	e.nightly()
	own, err := e.st.OwnCopies(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := own["c-p000"]; !ok {
		t.Fatalf("partial copy not tracked: %v", own)
	}
}
