package maintenance

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

type fakeBackend struct {
	mu        sync.Mutex
	calls     []string
	gate      *gate.Gate
	paused    bool // gate state seen during prune
	active    int  // active gate items seen during prune
	pruneErr  error
	block     chan struct{} // if set, prune waits for it
	hang      bool          // prune waits for its context
	started   chan struct{}
	freed     int64
	statsSize int64
	tagSize   map[string]int64 // RawStatsTag result per tag
	tagErr    map[string]error
	forgetErr map[string]error // ForgetRaw result per snapshot
	onUnlock  func()           // runs inside Unlock (the purge waited for the repository)
}

func (f *fakeBackend) record(c string) { f.mu.Lock(); f.calls = append(f.calls, c); f.mu.Unlock() }

func (f *fakeBackend) list() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeBackend) Unlock(ctx context.Context) error {
	f.record("unlock")
	if f.onUnlock != nil {
		f.onUnlock()
	}
	return nil
}
func (f *fakeBackend) Prune(ctx context.Context, maxUnused string) (restic.PruneResult, error) {
	f.record("prune " + maxUnused)
	f.mu.Lock()
	f.paused, f.active = f.gate.Paused(), f.gate.Active()
	f.mu.Unlock()
	if f.started != nil {
		close(f.started)
	}
	if f.block != nil {
		<-f.block
	}
	if f.hang {
		<-ctx.Done()
		return restic.PruneResult{BytesFreed: -1}, ctx.Err()
	}
	return restic.PruneResult{BytesFreed: f.freed}, f.pruneErr
}
func (f *fakeBackend) ForgetRaw(ctx context.Context, id string) error {
	f.record("forget " + id)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.forgetErr[id]
}
func (f *fakeBackend) Check(ctx context.Context, subset string) error {
	f.record("check " + subset)
	return nil
}
func (f *fakeBackend) RawStats(ctx context.Context) (restic.RepoStats, error) {
	f.record("stats")
	return restic.RepoStats{TotalSize: f.statsSize, TotalUncompressedSize: 2 * f.statsSize, SnapshotsCount: 3, CompressionRatio: 2}, nil
}

func (f *fakeBackend) RawStatsTag(ctx context.Context, tag string) (restic.RepoStats, error) {
	f.record("stats " + tag)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.tagErr[tag]; err != nil {
		return restic.RepoStats{}, err
	}
	return restic.RepoStats{TotalSize: f.tagSize[tag]}, nil
}

type fakeNotify struct {
	mu   sync.Mutex
	msgs []string
}

func (n *fakeNotify) Send(ev notify.Event) {
	title, body := notify.Render("de", ev)
	n.mu.Lock()
	n.msgs = append(n.msgs, title+"|"+body)
	n.mu.Unlock()
}

type fixture struct {
	svc   *Service
	be    *fakeBackend
	gate  *gate.Gate
	st    *store.Store
	notif *fakeNotify
	clock *time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	g := &gate.Gate{}
	be := &fakeBackend{gate: g, statsSize: 1000}
	clock := time.Date(2026, 10, 24, 1, 0, 0, 0, time.FixedZone("CEST", 2*3600))
	f := &fixture{be: be, gate: g, st: st, notif: &fakeNotify{}, clock: &clock}
	f.svc = &Service{
		Store: st, Backend: be, Gate: g, Notify: f.notif, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Cfg: Config{Hour: 2, PruneEnabled: true, CheckInterval: 168 * time.Hour, BusyWait: 200 * time.Millisecond},
		Now: func() time.Time { return clock },
	}
	return f
}

func (f *fixture) jobs(t *testing.T) []*store.Job {
	t.Helper()
	jobs, err := f.st.Jobs(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	for i, j := 0, len(jobs)-1; i < j; i, j = i+1, j-1 { // oldest first
		jobs[i], jobs[j] = jobs[j], jobs[i]
	}
	return jobs
}

func TestNextWindow(t *testing.T) {
	loc := time.FixedZone("x", 3600)
	at := func(m, d, h, min int) time.Time { return time.Date(2026, time.Month(m), d, h, min, 0, 0, loc) }
	for _, c := range []struct{ now, want time.Time }{
		{at(10, 24, 1, 0), at(10, 24, 2, 0)},
		{at(10, 24, 2, 0), at(10, 25, 2, 0)}, // exactly at the window: next one
		{at(10, 24, 23, 59), at(10, 25, 2, 0)},
		{at(10, 31, 12, 0), at(11, 1, 2, 0)},
	} {
		if got := NextWindow(c.now, 2, 0); !got.Equal(c.want) {
			t.Errorf("now %v: got %v want %v", c.now, got, c.want)
		}
	}
}

func TestSchedulerRunsEveryDay(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var sleeps []time.Duration
	f.svc.Sleep = func(ctx context.Context, d time.Duration) error {
		sleeps = append(sleeps, d)
		if len(sleeps) == 3 {
			cancel()
			return ctx.Err()
		}
		*f.clock = f.clock.Add(d)
		return nil
	}
	f.svc.Run(ctx)

	if len(sleeps) != 3 || sleeps[0] != time.Hour || sleeps[1] != 24*time.Hour || sleeps[2] != 24*time.Hour {
		t.Fatalf("sleeps: %v", sleeps)
	}
	var got []string
	for _, j := range f.jobs(t) {
		if j.Result != store.JobOK || j.Trigger != "schedule" {
			t.Fatalf("job %+v", j)
		}
		got = append(got, j.Type)
	}
	// Day 1: first check ever is due; day 2: not yet (interval is a week).
	if strings.Join(got, ",") != "trash-purge,prune,check,stats,trash-purge,prune,stats" {
		t.Fatalf("jobs: %v", got)
	}
	if st, err := f.st.LatestRepoStat(context.Background()); err != nil || st.TotalSize != 1000 || st.SnapshotsCount != 3 {
		t.Fatalf("repo stats: %+v %v", st, err)
	}
	if c := f.be.list(); c[0] != "unlock" || c[1] != "prune 5%" {
		t.Fatalf("calls: %v", c)
	}
}

func TestPruneDisabledAndCheckSubset(t *testing.T) {
	f := newFixture(t)
	f.svc.Cfg.PruneEnabled = false
	f.svc.Cfg.ReadDataSubset = "5%"
	jobs := f.svc.scheduledJobs(context.Background())
	if strings.Join(jobs, ",") != "trash-purge,check,stats" {
		t.Fatalf("jobs: %v", jobs)
	}
	ids, err := f.svc.begin(jobs, "schedule")
	if err != nil {
		t.Fatal(err)
	}
	f.svc.execute(context.Background(), jobs, ids)
	if c := strings.Join(f.be.list(), ","); c != "unlock,check 5%,stats" {
		t.Fatalf("calls: %s", c)
	}
	if j := f.jobs(t)[1]; !strings.Contains(j.Message, "5%") {
		t.Fatalf("message: %q", j.Message)
	}
}

func TestCheckDue(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ran := func(ago time.Duration, result string) {
		id, _ := f.st.InsertJob(ctx, JobCheck, "schedule", store.JobQueued, f.svc.now().Add(-ago))
		_ = f.st.FinishJob(ctx, id, result, "", nil, f.svc.now())
	}
	ran(200*time.Hour, store.JobOK)
	if !f.svc.checkDue(ctx) {
		t.Fatal("old check should be due")
	}
	ran(2*time.Hour, store.JobSkipped) // skipped runs do not count
	if !f.svc.checkDue(ctx) {
		t.Fatal("skipped check should not postpone the next one")
	}
	ran(2*time.Hour, store.JobOK)
	if f.svc.checkDue(ctx) {
		t.Fatal("recent check should not be due")
	}
	f.svc.Cfg.CheckInterval = 0
	if f.svc.checkDue(ctx) {
		t.Fatal("interval 0 disables checks")
	}
}

func TestJobTimeoutFailsJobAndResumesGate(t *testing.T) {
	f := newFixture(t)
	f.be.hang = true
	f.svc.Cfg.JobTimeout = 150 * time.Millisecond
	if _, err := f.svc.Trigger([]string{JobPrune, JobStats}); err != nil {
		t.Fatal(err)
	}
	f.svc.Wait()
	jobs := f.jobs(t)
	if jobs[0].Result != store.JobFailed || !strings.Contains(jobs[0].Message, "timed out") {
		t.Fatalf("prune: %+v", jobs[0])
	}
	if jobs[1].Result != store.JobOK {
		t.Fatalf("next job must still run: %+v", jobs[1])
	}
	if f.gate.Paused() {
		t.Fatal("gate stays paused after a timeout")
	}
	if len(f.notif.msgs) != 1 {
		t.Fatalf("notifications: %v", f.notif.msgs)
	}
}

func TestSchedulerIgnoresEarlyWakeup(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := 0
	f.svc.Sleep = func(ctx context.Context, d time.Duration) error {
		n++
		if n == 3 {
			cancel()
			return ctx.Err()
		}
		return nil // returns without the clock moving: woke up too early
	}
	f.svc.Run(ctx)
	if jobs := f.jobs(t); len(jobs) != 0 {
		t.Fatalf("jobs ran before the window: %+v", jobs)
	}
}

func TestStatsRecordDiskSize(t *testing.T) {
	f := newFixture(t)
	f.svc.DiskSize = func() (int64, error) { return 4242, nil }
	st, err := f.svc.RecordStats(context.Background())
	if err != nil || st.DiskBytes == nil || *st.DiskBytes != 4242 {
		t.Fatalf("%+v %v", st, err)
	}
	got, _ := f.st.LatestRepoStat(context.Background())
	if got.DiskBytes == nil || *got.DiskBytes != 4242 || got.TotalSize != 1000 || got.Stored() != 4242 {
		t.Fatalf("stored: %+v", got)
	}
	f.svc.DiskSize = func() (int64, error) { return 0, errors.New("boom") }
	st, _ = f.svc.RecordStats(context.Background())
	if st.DiskBytes != nil || st.Stored() != 1000 {
		t.Fatalf("fallback: %+v", st)
	}
}

func TestPruneRunsExclusivelyAndRecordsFreedBytes(t *testing.T) {
	f := newFixture(t)
	f.be.freed = 4096
	ids, err := f.svc.Trigger([]string{JobPrune})
	if err != nil || len(ids) != 1 {
		t.Fatal(ids, err)
	}
	f.svc.Wait()

	if !f.be.paused || f.be.active != 0 {
		t.Fatalf("prune must run with a paused, idle gate: paused=%v active=%d", f.be.paused, f.be.active)
	}
	if f.gate.Paused() {
		t.Fatal("gate not resumed after prune")
	}
	j := f.jobs(t)[0]
	if j.Result != store.JobOK || j.Trigger != "manual" || j.BytesFreed == nil || *j.BytesFreed != 4096 || j.FinishedAt.IsZero() {
		t.Fatalf("job: %+v", j)
	}
}

func TestPruneWaitsForRunningWork(t *testing.T) {
	f := newFixture(t)
	f.svc.Cfg.BusyWait = 5 * time.Second
	if err := f.gate.Enter(context.Background()); err != nil { // a running ingest
		t.Fatal(err)
	}
	if _, err := f.svc.Trigger([]string{JobPrune}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if len(f.be.list()) != 0 || !f.gate.Paused() {
		t.Fatalf("prune must wait while work runs; calls=%v", f.be.list())
	}
	// New work is held back while prune waits.
	entered := make(chan struct{})
	go func() {
		if f.gate.Enter(context.Background()) == nil {
			close(entered)
		}
	}()
	f.gate.Leave() // the ingest finishes
	f.svc.Wait()
	select {
	case <-entered: // resumed afterwards
	case <-time.After(2 * time.Second):
		t.Fatal("held-back work never resumed")
	}
	if j := f.jobs(t)[0]; j.Result != store.JobOK {
		t.Fatalf("job: %+v", j)
	}
}

func TestPruneAndCheckSkippedWhenBusy(t *testing.T) {
	f := newFixture(t)
	if err := f.gate.Enter(context.Background()); err != nil { // never finishes
		t.Fatal(err)
	}
	if _, err := f.svc.Trigger([]string{JobPrune, JobCheck, JobStats}); err != nil {
		t.Fatal(err)
	}
	f.svc.Wait()
	jobs := f.jobs(t)
	if len(jobs) != 3 {
		t.Fatalf("jobs: %+v", jobs)
	}
	for _, j := range jobs[:2] {
		if j.Result != store.JobSkipped || j.Message != "skipped: busy" {
			t.Fatalf("job: %+v", j)
		}
	}
	if jobs[2].Result != store.JobOK {
		t.Fatalf("stats should still run: %+v", jobs[2])
	}
	if c := strings.Join(f.be.list(), ","); c != "stats" {
		t.Fatalf("restic must not be touched: %s", c)
	}
	if f.gate.Paused() {
		t.Fatal("gate must be resumed after skipping")
	}
	if len(f.notif.msgs) != 0 {
		t.Fatal("skipping is not a failure")
	}
}

func TestOnlyOneRunAtATime(t *testing.T) {
	f := newFixture(t)
	f.be.block, f.be.started = make(chan struct{}), make(chan struct{})
	if _, err := f.svc.Trigger([]string{JobPrune}); err != nil {
		t.Fatal(err)
	}
	<-f.be.started
	if _, err := f.svc.Trigger([]string{JobStats}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second trigger: %v", err)
	}
	st, err := f.svc.Status(context.Background())
	if err != nil || st.Running == nil || st.Running.Type != JobPrune || st.Running.Result != store.JobRunning {
		t.Fatalf("status: %+v %v", st, err)
	}
	close(f.be.block)
	f.svc.Wait()
	if st, _ := f.svc.Status(context.Background()); st.Running != nil || st.LastPrune == nil || st.LastPrune.Result != store.JobOK {
		t.Fatalf("status after: %+v", st)
	}
	if _, err := f.svc.Trigger([]string{JobStats}); err != nil {
		t.Fatalf("trigger after finish: %v", err)
	}
	f.svc.Wait()
}

func TestFailureIsRecordedGenericAndNotified(t *testing.T) {
	f := newFixture(t)
	f.be.pruneErr = errors.New("restic prune: exit status 1: /secret/path locked")
	if _, err := f.svc.Trigger([]string{JobPrune}); err != nil {
		t.Fatal(err)
	}
	f.svc.Wait()
	j := f.jobs(t)[0]
	if j.Result != store.JobFailed || strings.Contains(j.Message, "secret") || j.Message == "" {
		t.Fatalf("job: %+v", j)
	}
	if len(f.notif.msgs) != 1 || strings.Contains(f.notif.msgs[0], "secret") || !strings.Contains(f.notif.msgs[0], "prune") {
		t.Fatalf("notifications: %v", f.notif.msgs)
	}
	if f.gate.Paused() {
		t.Fatal("gate must be resumed after a failure")
	}
}

func TestStaleJobsFailedOnStart(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id, _ := f.st.InsertJob(ctx, JobPrune, "schedule", store.JobRunning, f.svc.now())
	if err := f.st.FailStaleJobs(ctx, f.svc.now()); err != nil {
		t.Fatal(err)
	}
	if j := f.jobs(t)[0]; j.ID != id || j.Result != store.JobFailed {
		t.Fatalf("job: %+v", j)
	}
}

func TestStatsRecordTargetSpace(t *testing.T) {
	f := newFixture(t)
	f.svc.TargetSpace = func() (int64, int64, error) { return 1000, 400, nil }
	if _, err := f.svc.RecordStats(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := f.st.LatestRepoStat(context.Background())
	if got.TargetTotal == nil || *got.TargetTotal != 1000 || got.TargetFree == nil || *got.TargetFree != 400 {
		t.Fatalf("target: %+v", got)
	}
	f.svc.TargetSpace = func() (int64, int64, error) { return 0, 0, errors.New("boom") }
	f.svc.RecordStats(context.Background())
	got, _ = f.st.LatestRepoStat(context.Background())
	if got.TargetTotal != nil || got.TargetFree != nil {
		t.Fatalf("unknown target must stay nil: %+v", got)
	}
}

func (f *fixture) seedBackup(t *testing.T, server, backup string) {
	t.Helper()
	ctx := context.Background()
	id, err := f.st.CreateUpload(ctx, "up-"+backup, server+"/"+backup+".tar.gz", server, backup, f.svc.now())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.CompleteBackup(ctx, id, store.Finish{SnapshotID: "snap-" + backup}, f.svc.now()); err != nil {
		t.Fatal(err)
	}
}

func TestStatsJobMeasuresEachServer(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.seedBackup(t, "srv-a", "b1")
	f.seedBackup(t, "srv-a", "b2")
	f.seedBackup(t, "srv-b", "b3")
	f.be.tagSize = map[string]int64{"server:srv-a": 111, "server:srv-b": 222}
	if _, err := f.svc.RecordStats(ctx); err != nil {
		t.Fatal(err)
	}
	var tagged []string
	for _, c := range f.be.list() {
		if strings.HasPrefix(c, "stats server:") {
			tagged = append(tagged, c)
		}
	}
	if strings.Join(tagged, ",") != "stats server:srv-a,stats server:srv-b" {
		t.Fatalf("calls (one per server, in order): %v", tagged)
	}
	got, _ := f.st.ServerStats(ctx)
	if got["srv-a"].StoredBytes != 111 || got["srv-b"].StoredBytes != 222 || !got["srv-a"].MeasuredAt.Equal(*f.clock) {
		t.Fatalf("stored: %+v", got)
	}
}

func TestServerStatsFailureKeepsOldValue(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.seedBackup(t, "srv-a", "b1")
	f.seedBackup(t, "srv-b", "b2")
	f.be.tagSize = map[string]int64{"server:srv-a": 100, "server:srv-b": 200}
	f.svc.RecordStats(ctx)
	*f.clock = f.clock.Add(time.Hour)
	f.be.tagErr = map[string]error{"server:srv-a": errors.New("boom")}
	f.be.tagSize = map[string]int64{"server:srv-a": 999, "server:srv-b": 250}
	if _, err := f.svc.RecordStats(ctx); err != nil {
		t.Fatalf("a failing server must not fail the job: %v", err)
	}
	got, _ := f.st.ServerStats(ctx)
	if got["srv-a"].StoredBytes != 100 || got["srv-a"].MeasuredAt.Equal(*f.clock) {
		t.Fatalf("old value must stay: %+v", got["srv-a"])
	}
	if got["srv-b"].StoredBytes != 250 {
		t.Fatalf("other servers continue: %+v", got["srv-b"])
	}
}

func TestServerStatsDroppedWithoutBackups(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.seedBackup(t, "srv-a", "b1")
	f.st.SetServerStat(ctx, "gone", 5, f.svc.now())
	f.svc.RecordStats(ctx)
	got, _ := f.st.ServerStats(ctx)
	if _, ok := got["gone"]; ok || len(got) != 1 {
		t.Fatalf("stale entry kept: %+v", got)
	}
}

func (f *fakeBackend) tagCalls() []string {
	var out []string
	for _, c := range f.list() {
		if strings.HasPrefix(c, "stats server:") {
			out = append(out, strings.TrimPrefix(c, "stats "))
		}
	}
	return out
}

func TestStartupMeasuresOnlyServersWithoutValue(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.seedBackup(t, "srv-a", "b1")
	f.seedBackup(t, "srv-b", "b2")
	f.be.tagSize = map[string]int64{"server:srv-a": 1, "server:srv-b": 2}
	old := f.clock.Add(-24 * time.Hour)
	f.st.SetServerStat(ctx, "srv-a", 77, old)
	if err := f.svc.RecordStartupStats(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(f.be.tagCalls(), ","); got != "server:srv-b" {
		t.Fatalf("calls: %s", got)
	}
	got, _ := f.st.ServerStats(ctx)
	if got["srv-a"].StoredBytes != 77 || got["srv-b"].StoredBytes != 2 {
		t.Fatalf("stats: %+v", got)
	}
}

func TestPruneSkippedWarnsOnceAndRearms(t *testing.T) {
	f := newFixture(t)
	f.svc.Cfg.PruneSkipWarn = 2
	run := func() {
		t.Helper()
		if _, err := f.svc.Trigger([]string{JobPrune}); err != nil {
			t.Fatal(err)
		}
		f.svc.Wait()
	}
	warnings := func() int {
		n := 0
		for _, m := range f.notif.msgs {
			if strings.Contains(m, "uebersprungen") {
				n++
			}
		}
		return n
	}
	if err := f.gate.Enter(context.Background()); err != nil { // keeps the repository busy
		t.Fatal(err)
	}
	run()
	if warnings() != 0 {
		t.Fatal("warned after a single skip")
	}
	if st, _ := f.svc.Status(context.Background()); st.PruneSkipped != 1 || len(st.Warnings) != 0 {
		t.Fatalf("%+v", st)
	}
	run()
	if warnings() != 1 {
		t.Fatalf("no warning after the second skip: %v", f.notif.msgs)
	}
	if st, _ := f.svc.Status(context.Background()); st.PruneSkipped != 2 || len(st.Warnings) != 1 {
		t.Fatalf("%+v", st)
	}
	run()
	run()
	if warnings() != 1 {
		t.Fatalf("warned again while the skips continue: %d", warnings())
	}
	f.gate.Leave() // free again: the next prune runs and re-arms the warning
	run()
	if st, _ := f.svc.Status(context.Background()); st.PruneSkipped != 0 || len(st.Warnings) != 0 {
		t.Fatalf("not cleared: %+v", st)
	}
	if err := f.gate.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	run()
	run()
	if warnings() != 2 {
		t.Fatalf("warning not re-armed: %d", warnings())
	}
}

func TestPruneSkipWarnDisabled(t *testing.T) {
	f := newFixture(t) // PruneSkipWarn 0 = off
	if err := f.gate.Enter(context.Background()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := f.svc.Trigger([]string{JobPrune}); err != nil {
			t.Fatal(err)
		}
		f.svc.Wait()
	}
	if len(f.notif.msgs) != 0 {
		t.Fatalf("notified: %v", f.notif.msgs)
	}
	if st, _ := f.svc.Status(context.Background()); len(st.Warnings) != 0 || st.PruneSkipped != 3 {
		t.Fatalf("%+v", st)
	}
}
