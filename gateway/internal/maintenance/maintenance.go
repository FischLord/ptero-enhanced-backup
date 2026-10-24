// Package maintenance runs the daily prune/check/stats window and records
// every job in SQLite. Prune and check need the repository to themselves, so
// new ingests and restores are held back while they run.
package maintenance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"ptero-enhanced-backup/gateway/internal/restic"
	"ptero-enhanced-backup/gateway/internal/store"
)

const (
	JobPrune = "prune"
	JobCheck = "check"
	JobStats = "stats"
)

var (
	// ErrBusy is returned when a maintenance run is already in progress.
	ErrBusy = errors.New("maintenance is already running")

	errRepoBusy = errors.New("repository busy")
)

// Backend is the part of restic maintenance needs.
type Backend interface {
	Unlock(ctx context.Context) error
	Prune(ctx context.Context, maxUnused string) (restic.PruneResult, error)
	Check(ctx context.Context, readDataSubset string) error
	RawStats(ctx context.Context) (restic.RepoStats, error)
}

// Gate stops new restic work and waits for the running work (see package gate).
type Gate interface {
	Pause()
	Resume()
	WaitIdle(ctx context.Context) error
}

// Notifier receives failure messages (title is ASCII).
type Notifier interface {
	Send(title, body string)
}

type Config struct {
	Hour, Minute   int           // daily window, local time
	PruneEnabled   bool          // scheduled prune
	CheckInterval  time.Duration // <= 0 disables scheduled checks
	ReadDataSubset string        // passed to restic check, "" = metadata only
	BusyWait       time.Duration // how long to wait for running work before skipping (default 15m)
	JobTimeout     time.Duration // deadline for each job once it runs; restic is killed after it (default 3h)
	MaxUnused      string        // restic prune --max-unused (default 5%)
	StatsMinGap    time.Duration // minimum gap between stats refreshes after backups (default 10m)
}

type Service struct {
	Store   *store.Store
	Backend Backend
	Gate    Gate
	Notify  Notifier
	Log     *slog.Logger
	Cfg     Config
	// DiskSize, if set, measures the real repository size on disk (local repositories).
	DiskSize func() (int64, error)
	// TargetSpace, if set, reports total and available bytes of the backup target.
	TargetSpace func() (total, free int64, err error)
	// BaseCtx is the parent of manually triggered runs (default Background).
	BaseCtx context.Context

	// Now and Sleep are replaced in tests.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error

	busy    atomic.Bool
	wg      sync.WaitGroup
	mu      sync.Mutex
	current *store.Job
	lastRef time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// NextWindow returns the first window start after now.
func NextWindow(now time.Time, hour, minute int) time.Time {
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
	if !next.After(now) {
		next = time.Date(now.Year(), now.Month(), now.Day()+1, hour, minute, 0, 0, now.Location())
	}
	return next
}

func (s *Service) NextWindow() time.Time {
	return NextWindow(s.now(), s.Cfg.Hour, s.Cfg.Minute)
}

// Run executes the scheduled window every day until ctx ends.
func (s *Service) Run(ctx context.Context) {
	sl := s.Sleep
	if sl == nil {
		sl = sleep
	}
	var last time.Time // start of the previous window
	for {
		target := s.NextWindow()
		if !last.IsZero() && target.Before(last.Add(time.Minute)) {
			target = last.Add(time.Minute) // never run a window twice (clock jumps)
		}
		if err := sl(ctx, target.Sub(s.now())); err != nil {
			return
		}
		if s.now().Before(target) {
			continue // woke up early
		}
		last = target
		jobs := s.scheduledJobs(ctx)
		ids, err := s.begin(jobs, "schedule")
		if err != nil {
			s.Log.Warn("maintenance window skipped", "err", err)
			continue
		}
		s.execute(ctx, jobs, ids)
	}
}

// scheduledJobs picks what the daily window does.
func (s *Service) scheduledJobs(ctx context.Context) []string {
	var jobs []string
	if s.Cfg.PruneEnabled {
		jobs = append(jobs, JobPrune)
	}
	if s.checkDue(ctx) {
		jobs = append(jobs, JobCheck)
	}
	return append(jobs, JobStats)
}

func (s *Service) checkDue(ctx context.Context) bool {
	if s.Cfg.CheckInterval <= 0 {
		return false
	}
	last, err := s.Store.LastFinishedJob(ctx, JobCheck, false)
	if errors.Is(err, store.ErrNotFound) {
		return true
	}
	return err == nil && s.now().Sub(last.StartedAt) >= s.Cfg.CheckInterval
}

// Trigger starts the given jobs in the background and returns their job ids.
// It returns ErrBusy if a run is in progress.
func (s *Service) Trigger(jobs []string) ([]int64, error) {
	ids, err := s.begin(jobs, "manual")
	if err != nil {
		return nil, err
	}
	ctx := s.BaseCtx
	if ctx == nil {
		ctx = context.Background()
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.execute(ctx, jobs, ids)
	}()
	return ids, nil
}

// Wait blocks until background work (manual runs, stats refreshes) has ended.
func (s *Service) Wait() { s.wg.Wait() }

// begin claims the single run slot and creates the job rows.
func (s *Service) begin(jobs []string, trigger string) ([]int64, error) {
	if !s.busy.CompareAndSwap(false, true) {
		return nil, ErrBusy
	}
	ids, err := s.Store.InsertJobs(context.Background(), jobs, trigger, store.JobQueued, s.now())
	if err != nil {
		s.busy.Store(false)
		return nil, err
	}
	return ids, nil
}

type outcome struct {
	result, msg string
	freed       *int64
}

func (s *Service) execute(ctx context.Context, jobs []string, ids []int64) {
	defer s.busy.Store(false)
	bg := context.Background()
	for i, typ := range jobs {
		id := ids[i]
		start := s.now()
		_ = s.Store.StartJob(bg, id, start)
		s.setCurrent(&store.Job{ID: id, Type: typ, StartedAt: start, Result: store.JobRunning})

		var o outcome
		if ctx.Err() != nil {
			o = outcome{result: store.JobSkipped, msg: "gateway shutting down"}
		} else {
			jctx, cancel := context.WithTimeout(ctx, s.jobTimeout())
			o = s.runJob(jctx, typ)
			if jctx.Err() == context.DeadlineExceeded && o.result == store.JobFailed {
				o.msg = typ + " timed out after " + s.jobTimeout().String()
			}
			cancel()
		}
		_ = s.Store.FinishJob(bg, id, o.result, o.msg, o.freed, s.now())
		s.setCurrent(nil)
		s.Log.Info("maintenance job", "id", id, "type", typ, "result", o.result, "msg", o.msg)
		if o.result == store.JobFailed && s.Notify != nil {
			s.Notify.Send("PEB Wartung fehlgeschlagen", fmt.Sprintf("Wartungsjob %q ist fehlgeschlagen. Details im Gateway-Log.", typ))
		}
	}
}

func (s *Service) jobTimeout() time.Duration {
	if s.Cfg.JobTimeout > 0 {
		return s.Cfg.JobTimeout
	}
	return 3 * time.Hour
}

func (s *Service) setCurrent(j *store.Job) {
	s.mu.Lock()
	s.current = j
	s.mu.Unlock()
}

func (s *Service) runJob(ctx context.Context, typ string) outcome {
	switch typ {
	case JobPrune:
		var res restic.PruneResult
		err := s.exclusive(ctx, func() error {
			if err := s.Backend.Unlock(ctx); err != nil {
				s.Log.Warn("restic unlock failed", "err", err)
			}
			maxUnused := s.Cfg.MaxUnused
			if maxUnused == "" {
				maxUnused = "5%"
			}
			var err error
			res, err = s.Backend.Prune(ctx, maxUnused)
			return err
		})
		if o, handled := s.failure(typ, err); handled {
			return o
		}
		o := outcome{result: store.JobOK, msg: "prune finished"}
		if res.BytesFreed >= 0 {
			o.freed = &res.BytesFreed
			o.msg = fmt.Sprintf("prune finished, %d bytes freed", res.BytesFreed)
		}
		return o
	case JobCheck:
		err := s.exclusive(ctx, func() error {
			if err := s.Backend.Unlock(ctx); err != nil {
				s.Log.Warn("restic unlock failed", "err", err)
			}
			return s.Backend.Check(ctx, s.Cfg.ReadDataSubset)
		})
		if o, handled := s.failure(typ, err); handled {
			return o
		}
		msg := "no errors found"
		if s.Cfg.ReadDataSubset != "" {
			msg += ", read " + s.Cfg.ReadDataSubset + " of data"
		}
		return outcome{result: store.JobOK, msg: msg}
	case JobStats:
		st, err := s.RecordStats(ctx)
		if o, handled := s.failure(typ, err); handled {
			return o
		}
		return outcome{result: store.JobOK, msg: fmt.Sprintf("%d snapshots, %d bytes stored", st.SnapshotsCount, st.Stored())}
	}
	return outcome{result: store.JobFailed, msg: "unknown job type"}
}

// failure turns an error into a job outcome. Messages stay generic (restic's
// error text may contain paths); the detail goes to the log.
func (s *Service) failure(typ string, err error) (outcome, bool) {
	switch {
	case err == nil:
		return outcome{}, false
	case errors.Is(err, errRepoBusy):
		return outcome{result: store.JobSkipped, msg: "skipped: busy"}, true
	}
	s.Log.Error("maintenance job failed", "type", typ, "err", err)
	return outcome{result: store.JobFailed, msg: typ + " failed, see gateway log"}, true
}

// exclusive pauses new restic work, waits for running work (bounded) and runs fn.
func (s *Service) exclusive(ctx context.Context, fn func() error) error {
	s.Gate.Pause()
	defer s.Gate.Resume()
	wait := s.Cfg.BusyWait
	if wait <= 0 {
		wait = 15 * time.Minute
	}
	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	if err := s.Gate.WaitIdle(wctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errRepoBusy
	}
	return fn()
}

// RecordStats reads the repository size and stores a snapshot.
func (s *Service) RecordStats(ctx context.Context) (store.RepoStat, error) {
	rs, err := s.Backend.RawStats(ctx)
	if err != nil {
		return store.RepoStat{}, err
	}
	st := store.RepoStat{
		Time: s.now(), TotalSize: rs.TotalSize, TotalUncompressedSize: rs.TotalUncompressedSize,
		SnapshotsCount: rs.SnapshotsCount, CompressionRatio: rs.CompressionRatio,
	}
	if s.DiskSize != nil {
		if n, err := s.DiskSize(); err == nil {
			st.DiskBytes = &n
		} else {
			s.Log.Warn("measure repo size on disk failed", "err", err)
		}
	}
	if s.TargetSpace != nil {
		if total, free, err := s.TargetSpace(); err == nil {
			st.TargetTotal, st.TargetFree = &total, &free
		} else {
			s.Log.Warn("measure backup target space failed", "err", err)
		}
	}
	return st, s.Store.InsertRepoStat(ctx, st)
}

// BackupDone refreshes the repository stats after a completed backup, at most
// every StatsMinGap and never while a maintenance run is active.
func (s *Service) BackupDone() {
	gap := s.Cfg.StatsMinGap
	if gap <= 0 {
		gap = 10 * time.Minute
	}
	s.mu.Lock()
	if s.busy.Load() || s.now().Sub(s.lastRef) < gap {
		s.mu.Unlock()
		return
	}
	s.lastRef = s.now()
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ctx := s.BaseCtx
		if ctx == nil {
			ctx = context.Background()
		}
		if _, err := s.RecordStats(ctx); err != nil {
			s.Log.Warn("repo stats refresh failed", "err", err)
		}
	}()
}

// Status is the maintenance part of the admin status.
type Status struct {
	PruneEnabled bool
	NextWindow   time.Time
	LastPrune    *store.Job // nil if never ran
	LastCheck    *store.Job
	Running      *store.Job // nil if idle
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	st := Status{PruneEnabled: s.Cfg.PruneEnabled, NextWindow: s.NextWindow()}
	for typ, dst := range map[string]**store.Job{JobPrune: &st.LastPrune, JobCheck: &st.LastCheck} {
		j, err := s.Store.LastFinishedJob(ctx, typ, true)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return st, err
		}
		if err == nil {
			*dst = j
		}
	}
	s.mu.Lock()
	if s.current != nil {
		c := *s.current
		st.Running = &c
	}
	s.mu.Unlock()
	return st, nil
}
