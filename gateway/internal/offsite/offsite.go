// Package offsite keeps a second restic repository (the offsite copy) in sync
// with the primary one. New snapshots are copied a few minutes after the last
// backup (debounced) and in the nightly window; snapshots that were deleted in
// the primary are forgotten on the secondary only in the nightly run.
//
// Copy runs hold the ingest gate like any other restic job that reads the
// primary: ingests and restores continue, but prune and check of the primary
// wait until the copy has finished. Copy runs never overlap.
package offsite

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"ptero-enhanced-backup/gateway/internal/notify"
	"ptero-enhanced-backup/gateway/internal/restic"
	"ptero-enhanced-backup/gateway/internal/secrets"
	"ptero-enhanced-backup/gateway/internal/store"
)

// Job types recorded in the maintenance job table.
const (
	JobCopy          = "copy"
	JobForgetSecond  = "forget-secondary"
	JobPruneSecond   = "prune-secondary"
	JobCheckSecond   = "check-secondary"
	stateLag         = "lag"
	stateLagSince    = "lag_since"
	stateStored      = "stored_bytes"
	stateStoredAt    = "stored_at"
	stateLastError   = "last_error"
	notifyThrottle   = 12 * time.Hour
	defaultCopyLimit = 6 * time.Hour
	defaultJobLimit  = 3 * time.Hour
)

// ErrBusy is returned when a copy run is already in progress.
var ErrBusy = errors.New("offsite copy is already running")

// ErrClosed is returned for work requested after Close.
var ErrClosed = errors.New("offsite copy is shut down")

// ErrUnsafe is the reason when the forget step refuses to run.
var ErrUnsafe = errors.New("refusing to forget on the secondary")

// Secondary is the restic client of the offsite repository.
type Secondary interface {
	Exists(ctx context.Context) (bool, error)
	InitCopy(ctx context.Context) error
	ChunkerPolynomial(ctx context.Context) (string, error)
	Copy(ctx context.Context) error
	Snapshots(ctx context.Context) ([]restic.Snapshot, error)
	ForgetMany(ctx context.Context, ids []string) error
	Unlock(ctx context.Context) error
	Prune(ctx context.Context, maxUnused string) (restic.PruneResult, error)
	Check(ctx context.Context, readDataSubset string) error
	RawStats(ctx context.Context) (restic.RepoStats, error)
	RepoID(ctx context.Context) (string, error)
}

// Primary is what the offsite copy reads from the main repository.
type Primary interface {
	ChunkerPolynomial(ctx context.Context) (string, error)
	Snapshots(ctx context.Context) ([]restic.Snapshot, error)
	RepoID(ctx context.Context) (string, error)
}

// Gate is the ingest gate (see package gate).
type Gate interface {
	Enter(ctx context.Context) error
	Leave()
}

// Notifier receives failure messages (rendered per channel and language).
type Notifier interface {
	Send(ev notify.Event)
}

type Config struct {
	Repo        string        // secondary address, used to scrub messages
	Delay       time.Duration // quiet time after the last backup (default 5m)
	CopyTimeout time.Duration // deadline for one copy run (default 6h)
	JobTimeout  time.Duration // deadline for forget, prune and check (default 3h)
	PruneEnable bool          // prune the secondary in the nightly run
	MaxUnused   string        // restic prune --max-unused (default 5%)
	Secrets     []string      // never shown in messages or logs (passwords, primary address)
	// ForgetGrace is how long a snapshot must be missing in the primary before
	// the secondary forgets its copy (0 = 48h, negative = no grace).
	ForgetGrace time.Duration
	// ForgetConfirm releases a stopped forget step: it must equal the secondary's repository ID.
	ForgetConfirm string
}

type Service struct {
	Store     *store.Store
	Primary   Primary
	Secondary Secondary
	Gate      Gate
	Notify    Notifier
	Log       *slog.Logger
	Cfg       Config
	// BaseCtx is the parent of background runs (default Background).
	BaseCtx context.Context
	// NextWindow gives the next nightly window (for next_copy_at).
	NextWindow func() time.Time
	Now        func() time.Time

	sem chan struct{}
	wg  sync.WaitGroup

	mu         sync.Mutex
	timer      *time.Timer
	due        time.Time // when the debounced copy starts, zero = none pending
	closed     bool
	loadOnce   sync.Once
	ready      bool
	lag        int64 // -1 = unknown
	lagSince   time.Time
	stored     int64 // -1 = unknown
	storedAt   time.Time
	lastErr    string
	lastNotify time.Time
	current    *store.Job
	// forgetBlock is why the forget step is stopped ("" = not stopped); kept until a forget run passes.
	forgetBlock string
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) baseCtx() context.Context {
	if s.BaseCtx != nil {
		return s.BaseCtx
	}
	return context.Background()
}

func (s *Service) semaphore() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sem == nil {
		s.sem = make(chan struct{}, 1)
	}
	return s.sem
}

func (s *Service) acquire(ctx context.Context) error {
	select {
	case s.semaphore() <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) tryAcquire() bool {
	select {
	case s.semaphore() <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Service) release() { <-s.semaphore() }

// Wait blocks until background runs have ended (shutdown, tests). Call it after Close.
func (s *Service) Wait() { s.wg.Wait() }

// begin registers a background run with Wait. It fails after Close, so
// that wg.Add never races with Wait. The caller calls s.wg.Done when finished.
func (s *Service) begin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.wg.Add(1)
	return true
}

// Close stops the pending debounce timer; running copies end with BaseCtx.
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	if s.timer != nil {
		s.timer.Stop()
	}
	s.mu.Unlock()
}

// ---- state ----

// load reads the persisted state once.
func (s *Service) load(ctx context.Context) {
	s.loadOnce.Do(func() {
		get := func(k string) string { v, _ := s.Store.State(ctx, k); return v }
		lag, stored := int64(-1), int64(-1)
		if n, err := strconv.ParseInt(get(stateLag), 10, 64); err == nil {
			lag = n
		}
		if n, err := strconv.ParseInt(get(stateStored), 10, 64); err == nil {
			stored = n
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		s.lag, s.stored = lag, stored
		s.lagSince = parseMilli(get(stateLagSince))
		s.storedAt = parseMilli(get(stateStoredAt))
		s.lastErr = get(stateLastError)
		s.forgetBlock = get(stateForgetBlock)
	})
}

func parseMilli(v string) time.Time {
	if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
		return time.UnixMilli(n)
	}
	return time.Time{}
}

func milli(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return strconv.FormatInt(t.UnixMilli(), 10)
}

func (s *Service) save(key, val string) {
	if err := s.Store.SetState(context.Background(), key, val); err != nil {
		s.Log.Warn("store offsite state failed", "key", key, "err", err)
	}
}

// setLag records the exact lag and tracks since when it has been above zero.
func (s *Service) setLag(n int64) {
	s.load(context.Background())
	s.mu.Lock()
	s.lag = n
	if n > 0 && s.lagSince.IsZero() {
		s.lagSince = s.now()
	}
	if n == 0 {
		s.lagSince = time.Time{}
	}
	since := s.lagSince
	s.mu.Unlock()
	s.save(stateLag, strconv.FormatInt(n, 10))
	s.save(stateLagSince, milli(since))
}

func (s *Service) setError(msg string) {
	s.load(context.Background())
	msg = secrets.Scrub(msg)
	s.mu.Lock()
	s.lastErr = msg
	s.mu.Unlock()
	s.save(stateLastError, msg)
}

func (s *Service) clearError() {
	s.mu.Lock()
	had := s.lastErr != ""
	s.lastErr = ""
	s.lastNotify = time.Time{}
	s.mu.Unlock()
	if had {
		s.save(stateLastError, "")
	}
}

// scrub removes secrets and the repository address and shortens restic's error to one line.
func (s *Service) scrub(err error) string {
	return s.scrubString(err.Error())
}

// scrubString is scrub for plain text.
func (s *Service) scrubString(text string) string {
	m := secrets.ScrubWith(restic.Short(text), append([]string{s.Cfg.Repo}, s.Cfg.Secrets...)...)
	if r := []rune(m); len(r) > 600 {
		m = string(r[:600]) + "..."
	}
	return m
}

// fail records an error and notifies (at most once per notifyThrottle while the error persists).
func (s *Service) fail(what string, err error) string {
	msg := what + ": " + s.scrub(err)
	s.Log.Error("offsite copy failed", "what", what, "err", msg)
	s.setError(msg)
	s.notifyError(msg)
	return msg
}

func (s *Service) notifyError(msg string) {
	if s.Notify == nil {
		return
	}
	s.mu.Lock()
	if !s.lastNotify.IsZero() && s.now().Sub(s.lastNotify) < notifyThrottle {
		s.mu.Unlock()
		return
	}
	s.lastNotify = s.now()
	s.mu.Unlock()
	s.Notify.Send(notify.Event{Key: notify.EventOffsiteFailed, Params: notify.Params{"detail": msg}})
}

// ---- setup ----

// Setup creates the secondary repository with the primary's chunker parameters,
// or checks that an existing one is reachable and matches. The caller holds the run slot.
func (s *Service) Setup(ctx context.Context) error {
	s.load(ctx)
	exists, err := s.Secondary.Exists(ctx)
	if err != nil {
		return fmt.Errorf("secondary repository is not reachable: %w", err)
	}
	if !exists {
		if err := s.Secondary.InitCopy(ctx); err != nil {
			return fmt.Errorf("creating the secondary repository failed: %w", err)
		}
		s.Log.Info("offsite repository created")
	} else {
		want, err := s.Primary.ChunkerPolynomial(ctx)
		if err != nil {
			return fmt.Errorf("reading the primary repository config failed: %w", err)
		}
		have, err := s.Secondary.ChunkerPolynomial(ctx)
		if err != nil {
			return fmt.Errorf("reading the secondary repository config failed: %w", err)
		}
		if want != have {
			return errors.New("secondary repository has other chunker parameters than the primary (deduplication would not match); use a new, empty repository")
		}
	}
	s.baseline(ctx)
	s.mu.Lock()
	s.ready = true
	s.mu.Unlock()
	return nil
}

// Startup prepares the repository in the background and catches up if snapshots are missing.
func (s *Service) Startup() {
	if !s.begin() {
		return
	}
	go func() {
		defer s.wg.Done()
		ctx := s.baseCtx()
		if err := s.acquire(ctx); err != nil {
			return
		}
		defer s.release()
		if err := s.Setup(ctx); err != nil {
			s.fail("setup", err)
			return
		}
		s.clearError()
		if err := s.refreshLag(ctx); err != nil {
			s.Log.Warn("offsite lag refresh failed", "err", s.scrub(err))
			return
		}
		s.mu.Lock()
		behind := s.lag > 0
		s.mu.Unlock()
		if behind {
			s.arm()
		}
	}()
}

func (s *Service) ensureReady(ctx context.Context) error {
	s.mu.Lock()
	ok := s.ready
	s.mu.Unlock()
	if ok {
		return nil
	}
	return s.Setup(ctx)
}

// ---- lag ----

// refreshLag counts primary snapshots that have no copy on the secondary.
func (s *Service) refreshLag(ctx context.Context) error {
	prim, sec, err := s.snapshotLists(ctx)
	if err != nil {
		return err
	}
	s.setLag(int64(len(missing(prim, sec))))
	return nil
}

// snapshotLists reads the secondary first and the primary after it, so the
// primary list is never older than the secondary list.
func (s *Service) snapshotLists(ctx context.Context) (prim, sec []restic.Snapshot, err error) {
	sec, err = s.Secondary.Snapshots(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("listing the secondary snapshots: %w", err)
	}
	if err := s.Gate.Enter(ctx); err != nil {
		return nil, nil, err
	}
	defer s.Gate.Leave()
	prim, err = s.Primary.Snapshots(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("listing the primary snapshots: %w", err)
	}
	return prim, sec, nil
}

// covered is the set of primary snapshot IDs that exist on the secondary.
func covered(sec []restic.Snapshot) map[string]bool {
	m := make(map[string]bool, len(sec))
	for _, sn := range sec {
		if sn.Original != "" {
			m[sn.Original] = true
		}
		m[sn.ID] = true
	}
	return m
}

func missing(prim, sec []restic.Snapshot) []string {
	have := covered(sec)
	var out []string
	for _, p := range prim {
		if !have[p.ID] {
			out = append(out, p.ID)
		}
	}
	return out
}

// ---- debounced copy ----

// BackupDone notes a completed backup and (re)starts the debounce timer. The
// copy starts Delay after the last backup.
func (s *Service) BackupDone() {
	s.load(context.Background())
	s.mu.Lock()
	if s.lag >= 0 { // the new snapshot is missing on the secondary
		s.lag++
		if s.lagSince.IsZero() {
			s.lagSince = s.now()
		}
	}
	lag, since := s.lag, s.lagSince
	s.mu.Unlock()
	if lag >= 0 {
		s.save(stateLag, strconv.FormatInt(lag, 10))
		s.save(stateLagSince, milli(since))
	}
	s.arm()
}

func (s *Service) delay() time.Duration {
	if s.Cfg.Delay > 0 {
		return s.Cfg.Delay
	}
	return 5 * time.Minute
}

func (s *Service) arm() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.due = s.now().Add(s.delay())
	if s.timer == nil {
		s.timer = time.AfterFunc(s.delay(), s.fire)
	} else {
		s.timer.Reset(s.delay())
	}
}

// fire is the debounce timer. The deadline is taken (due cleared) in the same
// critical section that checks it, so a BackupDone that arrives later sets a
// new deadline instead of being wiped; if the run slot is taken the deadline is
// put back, and the next quiet period tries again. After Close it does nothing.
func (s *Service) fire() {
	s.mu.Lock()
	if s.closed || s.due.IsZero() {
		s.mu.Unlock()
		return
	}
	if wait := s.due.Sub(s.now()); wait > 0 { // a later backup moved the deadline
		s.timer.Reset(wait)
		s.mu.Unlock()
		return
	}
	s.due = time.Time{}
	s.wg.Add(1) // under the lock: Close and Wait cannot slip in between
	s.mu.Unlock()
	defer s.wg.Done()
	ctx := s.baseCtx()
	if ctx.Err() != nil {
		return
	}
	if !s.tryAcquire() { // never two copy runs; try again after the next quiet period
		s.arm()
		return
	}
	defer s.release()
	id, err := s.Store.InsertJob(context.Background(), JobCopy, "backup", store.JobQueued, s.now())
	if err != nil {
		s.Log.Error("record copy job failed", "err", err)
		return
	}
	s.copyRun(ctx, id)
}

// TriggerCopy starts a copy run in the background (admin API). It returns ErrBusy
// if a copy or nightly run is in progress.
func (s *Service) TriggerCopy() (int64, error) {
	if !s.begin() {
		return 0, ErrClosed
	}
	if !s.tryAcquire() {
		s.wg.Done()
		return 0, ErrBusy
	}
	id, err := s.Store.InsertJob(context.Background(), JobCopy, "manual", store.JobQueued, s.now())
	if err != nil {
		s.release()
		s.wg.Done()
		return 0, err
	}
	go func() {
		defer s.wg.Done()
		defer s.release()
		s.copyRun(s.baseCtx(), id)
	}()
	return id, nil
}

// ---- job plumbing ----

type result struct {
	res, msg string
	freed    *int64
}

// runJob executes fn as job id with a deadline and records the outcome.
// The caller holds the run slot.
func (s *Service) runJob(ctx context.Context, id int64, typ string, limit time.Duration, fn func(ctx context.Context) (string, *int64, error)) (string, error) {
	bg := context.Background()
	start := s.now()
	_ = s.Store.StartJob(bg, id, start)
	s.mu.Lock()
	s.current = &store.Job{ID: id, Type: typ, StartedAt: start, Result: store.JobRunning}
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.current = nil; s.mu.Unlock() }()

	var r result
	var runErr error
	if ctx.Err() != nil {
		r = result{res: store.JobSkipped, msg: "gateway shutting down"}
		runErr = ctx.Err()
	} else {
		jctx, cancel := context.WithTimeout(ctx, limit)
		msg, freed, err := fn(jctx)
		timedOut := jctx.Err() == context.DeadlineExceeded
		cancel()
		switch {
		case err == nil:
			r = result{res: store.JobOK, msg: msg, freed: freed}
		case ctx.Err() != nil:
			r = result{res: store.JobSkipped, msg: "gateway shutting down"}
			runErr = ctx.Err()
		case timedOut:
			r = result{res: store.JobFailed, msg: typ + " timed out after " + limit.String()}
			runErr = errors.New(r.msg)
		case errors.Is(err, ErrUnsafe):
			r = result{res: store.JobFailed, msg: s.scrub(err)}
			runErr = err
		default:
			r = result{res: store.JobFailed, msg: typ + " failed: " + s.scrub(err)}
			runErr = err
		}
	}
	_ = s.Store.FinishJob(bg, id, r.res, r.msg, r.freed, s.now())
	s.Log.Info("offsite job", "id", id, "type", typ, "result", r.res, "msg", r.msg)
	return r.res, runErr
}

func (s *Service) copyLimit() time.Duration {
	if s.Cfg.CopyTimeout > 0 {
		return s.Cfg.CopyTimeout
	}
	return defaultCopyLimit
}

func (s *Service) jobLimit() time.Duration {
	if s.Cfg.JobTimeout > 0 {
		return s.Cfg.JobTimeout
	}
	return defaultJobLimit
}

// copyRun is the copy job. The caller holds the run slot.
func (s *Service) copyRun(ctx context.Context, id int64) bool {
	res, err := s.runJob(ctx, id, JobCopy, s.copyLimit(), func(ctx context.Context) (string, *int64, error) {
		if err := s.ensureReady(ctx); err != nil {
			return "", nil, err
		}
		// Hold the gate for the whole copy: prune and check of the primary wait
		// for it, ingests and restores are not held back.
		if err := s.Gate.Enter(ctx); err != nil {
			return "", nil, err
		}
		before, listErr := s.Secondary.Snapshots(ctx)
		if listErr == nil {
			s.adoptLegacy(ctx, before)
		}
		err := s.Secondary.Copy(ctx)
		s.Gate.Leave()
		if listErr == nil {
			s.trackCopies(ctx, before) // also after a failed copy: what arrived is ours
		}
		if err != nil {
			return "", nil, err
		}
		if listErr != nil {
			s.Log.Warn("offsite: listing the secondary before the copy failed, new copies are not tracked", "err", s.scrub(listErr))
		}
		lagErr := s.refreshLag(ctx)
		if lagErr != nil {
			s.Log.Warn("offsite lag refresh failed", "err", s.scrub(lagErr))
		}
		return "copy finished", nil, nil
	})
	if err != nil && res == store.JobFailed {
		s.fail("copy", err)
		return false
	}
	if res == store.JobOK {
		s.clearError()
		return true
	}
	return false
}

// Nightly is the offsite part of the maintenance window: copy, mirror deletions,
// prune and check on the secondary, then measure its size.
func (s *Service) Nightly(ctx context.Context) {
	if !s.begin() {
		return
	}
	defer s.wg.Done()
	if err := s.acquire(ctx); err != nil {
		return
	}
	defer s.release()
	s.mu.Lock()
	s.due = time.Time{} // the nightly copy replaces a pending one
	if s.timer != nil {
		s.timer.Stop()
	}
	s.mu.Unlock()

	id, err := s.Store.InsertJob(context.Background(), JobCopy, "schedule", store.JobQueued, s.now())
	if err != nil {
		s.Log.Error("record copy job failed", "err", err)
		return
	}
	if !s.copyRun(ctx, id) {
		return // secondary unreachable or broken: nothing else makes sense
	}

	ids, err := s.Store.InsertJobs(context.Background(), []string{JobForgetSecond, JobPruneSecond, JobCheckSecond}, "schedule", store.JobQueued, s.now())
	if err != nil {
		s.Log.Error("record offsite jobs failed", "err", err)
		return
	}
	skipRest := func(from int, why string, to int) {
		for _, jid := range ids[from:to] {
			_ = s.Store.StartJob(context.Background(), jid, s.now())
			_ = s.Store.FinishJob(context.Background(), jid, store.JobSkipped, why, nil, s.now())
		}
	}

	res, err := s.runJob(ctx, ids[0], JobForgetSecond, s.jobLimit(), s.forgetStep)
	if res != store.JobOK {
		if res == store.JobFailed {
			s.fail("forget-secondary", err)
		}
		skipRest(1, "skipped: forget failed", 3)
		return
	}
	if s.Cfg.PruneEnable {
		res, err = s.runJob(ctx, ids[1], JobPruneSecond, s.jobLimit(), s.pruneStep)
		if res == store.JobFailed {
			s.fail("prune-secondary", err)
		}
	} else {
		skipRest(1, "prune disabled", 2)
	}
	if ctx.Err() != nil {
		return
	}
	res, err = s.runJob(ctx, ids[2], JobCheckSecond, s.jobLimit(), func(ctx context.Context) (string, *int64, error) {
		if err := s.Secondary.Check(ctx, ""); err != nil {
			return "", nil, err
		}
		return "no errors found", nil, nil
	})
	if res == store.JobFailed {
		s.fail("check-secondary", err)
	}
	s.measure(ctx)
}

func (s *Service) pruneStep(ctx context.Context) (string, *int64, error) {
	if err := s.Secondary.Unlock(ctx); err != nil {
		s.Log.Warn("secondary unlock failed", "err", s.scrub(err))
	}
	mu := s.Cfg.MaxUnused
	if mu == "" {
		mu = "5%"
	}
	res, err := s.Secondary.Prune(ctx, mu)
	if err != nil {
		return "", nil, err
	}
	if res.BytesFreed >= 0 {
		return fmt.Sprintf("prune finished, %d bytes freed", res.BytesFreed), &res.BytesFreed, nil
	}
	return "prune finished", nil, nil
}

// measure stores the raw size of the secondary.
func (s *Service) measure(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, s.jobLimit())
	defer cancel()
	rs, err := s.Secondary.RawStats(cctx)
	if err != nil {
		s.Log.Warn("offsite stats failed", "err", s.scrub(err))
		return
	}
	s.load(ctx)
	now := s.now()
	s.mu.Lock()
	s.stored, s.storedAt = rs.TotalSize, now
	s.mu.Unlock()
	s.save(stateStored, strconv.FormatInt(rs.TotalSize, 10))
	s.save(stateStoredAt, milli(now))
}

// ---- status ----

type Status struct {
	Repo        string
	Running     *store.Job
	LastCopy    *store.Job // last finished copy job (ok or failed), nil if none
	LastError   string
	Lag         *int64 // nil = unknown
	LagSince    time.Time
	StoredBytes *int64
	StoredAt    time.Time
	NextCopy    time.Time
	Pending     bool
	// ForgetBlocked is set while the forget step is stopped by a safety check
	// (LastError then says why, until a forget run passes).
	ForgetBlocked bool
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	s.load(ctx)
	st := Status{Repo: s.Cfg.Repo}
	j, err := s.Store.LastFinishedJob(ctx, JobCopy, false)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return st, err
	}
	if err == nil {
		st.LastCopy = j
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st.LastError = s.lastErr
	if s.forgetBlock != "" {
		st.ForgetBlocked = true
		if st.LastError == "" {
			st.LastError = "forget-secondary: " + s.forgetBlock
		}
	}
	if s.lag >= 0 {
		n := s.lag
		st.Lag = &n
	}
	st.LagSince = s.lagSince
	if s.stored >= 0 {
		n := s.stored
		st.StoredBytes = &n
	}
	st.StoredAt = s.storedAt
	if s.current != nil {
		c := *s.current
		st.Running = &c
	}
	switch {
	case !s.due.IsZero():
		st.NextCopy, st.Pending = s.due, true
	case s.NextWindow != nil:
		st.NextCopy = s.NextWindow()
	}
	return st, nil
}
