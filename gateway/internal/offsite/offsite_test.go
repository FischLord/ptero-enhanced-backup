package offsite

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ptero-enhanced-backup/gateway/internal/gate"
	"ptero-enhanced-backup/gateway/internal/notify"
	"ptero-enhanced-backup/gateway/internal/restic"
	"ptero-enhanced-backup/gateway/internal/store"
)

type fakePrimary struct {
	mu    sync.Mutex
	snaps []restic.Snapshot
	err   error
	poly  string
	id    string
}

func (f *fakePrimary) Snapshots(context.Context) ([]restic.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]restic.Snapshot(nil), f.snaps...), f.err
}
func (f *fakePrimary) ChunkerPolynomial(context.Context) (string, error) { return f.poly, nil }
func (f *fakePrimary) RepoID(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.id, nil
}
func (f *fakePrimary) set(ids ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snaps = nil
	for _, id := range ids {
		f.snaps = append(f.snaps, restic.Snapshot{ID: id})
	}
}

type fakeSec struct {
	mu        sync.Mutex
	prim      *fakePrimary
	exists    bool
	existsErr error
	poly      string
	snaps     []restic.Snapshot
	calls     []string
	copyErr   error
	copyHook  func(ctx context.Context) error
	forgotten []string
	gate      *gate.Gate
	gateSeen  int
	id        string
}

func (f *fakeSec) rec(c string) { f.mu.Lock(); f.calls = append(f.calls, c); f.mu.Unlock() }
func (f *fakeSec) count(c string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, x := range f.calls {
		if x == c {
			n++
		}
	}
	return n
}
func (f *fakeSec) Exists(context.Context) (bool, error) {
	f.rec("exists")
	return f.exists, f.existsErr
}
func (f *fakeSec) InitCopy(context.Context) error {
	f.rec("init")
	f.mu.Lock()
	f.exists, f.poly = true, f.prim.poly
	f.mu.Unlock()
	return nil
}
func (f *fakeSec) ChunkerPolynomial(context.Context) (string, error) { return f.poly, nil }
func (f *fakeSec) Copy(ctx context.Context) error {
	f.rec("copy")
	if f.gate != nil {
		f.mu.Lock()
		f.gateSeen = f.gate.Active()
		f.mu.Unlock()
	}
	if f.copyHook != nil {
		if err := f.copyHook(ctx); err != nil {
			return err
		}
	}
	if f.copyErr != nil {
		return f.copyErr
	}
	prim, _ := f.prim.Snapshots(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	have := covered(f.snaps)
	for _, p := range prim {
		if !have[p.ID] {
			f.snaps = append(f.snaps, restic.Snapshot{ID: "c-" + p.ID, Original: p.ID})
		}
	}
	return nil
}
func (f *fakeSec) Snapshots(context.Context) ([]restic.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]restic.Snapshot(nil), f.snaps...), nil
}
func (f *fakeSec) ForgetMany(_ context.Context, ids []string) error {
	f.rec("forget")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgotten = append(f.forgotten, ids...)
	var keep []restic.Snapshot
	for _, sn := range f.snaps {
		gone := false
		for _, id := range ids {
			gone = gone || sn.ID == id
		}
		if !gone {
			keep = append(keep, sn)
		}
	}
	f.snaps = keep
	return nil
}
func (f *fakeSec) RepoID(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.id, nil
}
func (f *fakeSec) Unlock(context.Context) error { f.rec("unlock"); return nil }
func (f *fakeSec) Prune(context.Context, string) (restic.PruneResult, error) {
	f.rec("prune")
	return restic.PruneResult{BytesFreed: 10}, nil
}
func (f *fakeSec) Check(context.Context, string) error { f.rec("check"); return nil }
func (f *fakeSec) RawStats(context.Context) (restic.RepoStats, error) {
	f.rec("stats")
	return restic.RepoStats{TotalSize: 4242}, nil
}

type fakeNotify struct {
	mu   sync.Mutex
	msgs []string
}

func (n *fakeNotify) Send(ev notify.Event) {
	_, body := notify.Render("de", ev)
	n.mu.Lock()
	n.msgs = append(n.msgs, body)
	n.mu.Unlock()
}
func (n *fakeNotify) count() int { n.mu.Lock(); defer n.mu.Unlock(); return len(n.msgs) }

type env struct {
	svc  *Service
	prim *fakePrimary
	sec  *fakeSec
	st   *store.Store
	nt   *fakeNotify
	gate *gate.Gate
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	g := &gate.Gate{}
	prim := &fakePrimary{poly: "poly1", id: "prim-id"}
	sec := &fakeSec{prim: prim, exists: true, poly: "poly1", gate: g, id: "sec-id"}
	nt := &fakeNotify{}
	svc := &Service{
		Store: st, Primary: prim, Secondary: sec, Gate: g, Notify: nt, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Cfg: Config{Repo: "rclone:gdrive:peb", Delay: 40 * time.Millisecond, PruneEnable: true, ForgetGrace: -1, Secrets: []string{"hunter2"}},
	}
	t.Cleanup(func() { svc.Close(); svc.Wait() })
	return &env{svc: svc, prim: prim, sec: sec, st: st, nt: nt, gate: g}
}

func (e *env) jobs(t *testing.T) map[string]string {
	t.Helper()
	js, err := e.st.Jobs(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for _, j := range js {
		if _, ok := m[j.Type]; !ok { // newest first
			m[j.Type] = j.Result
		}
	}
	return m
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestSetupCreatesMissingRepo(t *testing.T) {
	e := newEnv(t)
	e.sec.exists = false
	if err := e.svc.Setup(context.Background()); err != nil || e.sec.count("init") != 1 || e.sec.poly != "poly1" {
		t.Fatalf("%v %v", err, e.sec.calls)
	}
}

func TestSetupExistingChecksChunker(t *testing.T) {
	e := newEnv(t)
	if err := e.svc.Setup(context.Background()); err != nil || e.sec.count("init") != 0 {
		t.Fatalf("matching repo: %v %v", err, e.sec.calls)
	}
	e.sec.poly = "other"
	if err := e.svc.Setup(context.Background()); err == nil || !strings.Contains(err.Error(), "chunker") {
		t.Fatalf("mismatch accepted: %v", err)
	}
	e.sec.existsErr = errors.New("rclone: token expired")
	if err := e.svc.Setup(context.Background()); err == nil || e.sec.count("init") != 0 {
		t.Fatalf("unreachable repo must not be initialised: %v", err)
	}
}

func TestChunkerMismatchBlocksCopyAndSetsError(t *testing.T) {
	e := newEnv(t)
	e.sec.poly = "other"
	e.prim.set("a")
	id, _ := e.st.InsertJob(context.Background(), JobCopy, "manual", store.JobQueued, time.Now())
	e.svc.copyRun(context.Background(), id)
	st, _ := e.svc.Status(context.Background())
	if e.sec.count("copy") != 0 || !strings.Contains(st.LastError, "chunker") || e.nt.count() != 1 {
		t.Fatalf("copies=%d err=%q notify=%d", e.sec.count("copy"), st.LastError, e.nt.count())
	}
}

func TestCopyRunHoldsGateAndTracksLag(t *testing.T) {
	e := newEnv(t)
	e.prim.set("a", "b")
	id, _ := e.st.InsertJob(context.Background(), JobCopy, "manual", store.JobQueued, time.Now())
	if !e.svc.copyRun(context.Background(), id) {
		t.Fatal("copy failed")
	}
	if e.sec.gateSeen != 1 || e.gate.Active() != 0 {
		t.Fatalf("gate during copy %d, after %d", e.sec.gateSeen, e.gate.Active())
	}
	st, _ := e.svc.Status(context.Background())
	if st.Lag == nil || *st.Lag != 0 || st.LastCopy == nil || st.LastCopy.Result != store.JobOK || st.LastError != "" || !st.LagSince.IsZero() {
		t.Fatalf("%+v", st)
	}
	e.prim.set("a", "b", "c")
	e.svc.BackupDone()
	st, _ = e.svc.Status(context.Background())
	if *st.Lag != 1 || st.LagSince.IsZero() || !st.Pending {
		t.Fatalf("after backup: %+v", st)
	}
}

func TestCopyFailureSetsErrorAndNotifiesOnce(t *testing.T) {
	e := newEnv(t)
	e.prim.set("a")
	e.sec.copyErr = errors.New("restic copy: exit 1: rclone: login with hunter2 failed")
	for i := 0; i < 3; i++ {
		id, _ := e.st.InsertJob(context.Background(), JobCopy, "backup", store.JobQueued, time.Now())
		if e.svc.copyRun(context.Background(), id) {
			t.Fatal("expected failure")
		}
	}
	st, _ := e.svc.Status(context.Background())
	if st.LastCopy.Result != store.JobFailed || st.LastError == "" || e.nt.count() != 1 {
		t.Fatalf("%+v notify=%d", st, e.nt.count())
	}
	if strings.Contains(st.LastError, "hunter2") || strings.Contains(e.nt.msgs[0], "hunter2") {
		t.Fatalf("secret leaked: %q %q", st.LastError, e.nt.msgs[0])
	}
	e.sec.copyErr = nil
	id, _ := e.st.InsertJob(context.Background(), JobCopy, "backup", store.JobQueued, time.Now())
	if !e.svc.copyRun(context.Background(), id) {
		t.Fatal("recovery failed")
	}
	if st, _ = e.svc.Status(context.Background()); st.LastError != "" {
		t.Fatalf("error not cleared: %q", st.LastError)
	}
}

func TestDebounceOneCopyAfterLastBackup(t *testing.T) {
	e := newEnv(t)
	e.svc.Cfg.Delay = 120 * time.Millisecond
	e.prim.set("a")
	start := time.Now()
	for i := 0; i < 4; i++ { // backups keep arriving: the deadline keeps moving
		e.svc.BackupDone()
		time.Sleep(40 * time.Millisecond)
	}
	if n := e.sec.count("copy"); n != 0 {
		t.Fatalf("copy ran before the quiet period (%d)", n)
	}
	waitFor(t, "debounced copy", func() bool { return e.sec.count("copy") >= 1 })
	if since := time.Since(start); since < 120*time.Millisecond+3*40*time.Millisecond {
		t.Fatalf("copy after only %v", since)
	}
	time.Sleep(300 * time.Millisecond)
	if n := e.sec.count("copy"); n != 1 {
		t.Fatalf("expected exactly one copy, got %d", n)
	}
	waitFor(t, "job recorded", func() bool { return e.jobs(t)[JobCopy] == store.JobOK })
}

func TestNoParallelCopy(t *testing.T) {
	e := newEnv(t)
	e.prim.set("a")
	block, release := make(chan struct{}), make(chan struct{})
	e.sec.copyHook = func(ctx context.Context) error {
		close(block)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}
	if _, err := e.svc.TriggerCopy(); err != nil {
		t.Fatal(err)
	}
	<-block
	if _, err := e.svc.TriggerCopy(); !errors.Is(err, ErrBusy) {
		t.Fatalf("second copy: %v", err)
	}
	e.svc.BackupDone() // debounced run must wait, not overlap
	time.Sleep(150 * time.Millisecond)
	if n := e.sec.count("copy"); n != 1 {
		t.Fatalf("parallel copy: %d", n)
	}
	close(release)
	e.sec.copyHook = nil
	waitFor(t, "retry after the first run", func() bool { return e.sec.count("copy") >= 2 })
}

func TestCopyCancelledAtShutdown(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	e.svc.BaseCtx = ctx
	started := make(chan struct{})
	e.sec.copyHook = func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	if _, err := e.svc.TriggerCopy(); err != nil {
		t.Fatal(err)
	}
	<-started
	cancel()
	done := make(chan struct{})
	go func() { e.svc.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("copy did not stop")
	}
	if got := e.jobs(t)[JobCopy]; got != store.JobSkipped {
		t.Fatalf("job result %q", got)
	}
	if e.nt.count() != 0 {
		t.Fatal("shutdown must not notify")
	}
}

func TestNightlyMirrorsDeletions(t *testing.T) {
	e := newEnv(t)
	e.prim.set("a", "b", "c")
	e.sec.snaps = []restic.Snapshot{{ID: "own"}} // not made by copy: never touched
	e.svc.Nightly(context.Background())
	e.prim.set("a", "c") // b deleted in the primary
	e.svc.Nightly(context.Background())
	if len(e.sec.forgotten) != 1 || e.sec.forgotten[0] != "c-b" {
		t.Fatalf("forgotten %v", e.sec.forgotten)
	}
	js := e.jobs(t)
	for _, typ := range []string{JobCopy, JobForgetSecond, JobPruneSecond, JobCheckSecond} {
		if js[typ] != store.JobOK {
			t.Fatalf("%s: %q (%v)", typ, js[typ], js)
		}
	}
	st, _ := e.svc.Status(context.Background())
	if st.StoredBytes == nil || *st.StoredBytes != 4242 || *st.Lag != 0 || st.LastError != "" {
		t.Fatalf("%+v", st)
	}
	left, _ := e.sec.Snapshots(context.Background())
	if len(left) != 3 { // own, c-a, c-c
		t.Fatalf("left %v", left)
	}
}

func TestSafetyEmptyPrimaryNeverForgets(t *testing.T) {
	e := newEnv(t)
	e.prim.set("a", "b")
	e.svc.Nightly(context.Background())
	e.prim.set() // empty list, as after a broken listing
	e.svc.Nightly(context.Background())
	if len(e.sec.forgotten) != 0 || e.sec.count("forget") != 0 || e.sec.count("prune") != 1 {
		t.Fatalf("forgot=%v calls=%v", e.sec.forgotten, e.sec.calls)
	}
	js := e.jobs(t)
	st, _ := e.svc.Status(context.Background())
	if js[JobForgetSecond] != store.JobFailed || js[JobPruneSecond] != store.JobSkipped || st.LastError == "" || e.nt.count() == 0 {
		t.Fatalf("jobs=%v err=%q notify=%d", js, st.LastError, e.nt.count())
	}
}

func TestSafetyPrimaryListErrorNeverForgets(t *testing.T) {
	e := newEnv(t)
	e.prim.set("a")
	e.svc.Nightly(context.Background())
	e.prim.mu.Lock()
	e.prim.err = errors.New("repository locked")
	e.prim.mu.Unlock()
	e.svc.Nightly(context.Background())
	if e.sec.count("forget") != 0 {
		t.Fatal("forgot with unreadable primary")
	}
	if e.jobs(t)[JobForgetSecond] != store.JobFailed || e.nt.count() == 0 {
		t.Fatalf("%v", e.jobs(t))
	}
}

func TestForgetPlan(t *testing.T) {
	sec := []restic.Snapshot{{ID: "x1", Original: "a"}, {ID: "x2", Original: "b"}, {ID: "x3", Original: "c"}, {ID: "own"}}
	own := map[string]store.OwnCopy{"x1": {SnapshotID: "x1", OriginalID: "a"}, "x2": {SnapshotID: "x2", OriginalID: "b"}}
	ids, err := ForgetPlan([]restic.Snapshot{{ID: "a"}}, sec, own)
	if err != nil || len(ids) != 1 || ids[0] != "x2" { // x3 is a foreign copy, own has no original
		t.Fatalf("%v %v", ids, err)
	}
	if _, err := ForgetPlan(nil, sec, own); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("empty primary: %v", err)
	}
	if ids, err := ForgetPlan(nil, []restic.Snapshot{{ID: "own"}}, nil); err != nil || len(ids) != 0 {
		t.Fatalf("both empty: %v %v", ids, err)
	}
}

func TestStatusNextCopy(t *testing.T) {
	e := newEnv(t)
	win := time.Now().Add(5 * time.Hour)
	e.svc.NextWindow = func() time.Time { return win }
	st, _ := e.svc.Status(context.Background())
	if !st.NextCopy.Equal(win) || st.Pending || st.Lag != nil {
		t.Fatalf("%+v", st)
	}
}
