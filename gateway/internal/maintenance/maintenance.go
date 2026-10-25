// Package maintenance runs the daily prune/check/stats window and records
// every job in SQLite. Prune and check need the repository to themselves, so
// new ingests and restores are held back while they run.
package maintenance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"ptero-enhanced-backup/gateway/internal/notify"
	"ptero-enhanced-backup/gateway/internal/restic"
	"ptero-enhanced-backup/gateway/internal/store"
)

// msgSkippedBusy is the message of a job that found the repository busy.
const msgSkippedBusy = "skipped: busy"

// stateSkipWarned remembers that the "prune keeps being skipped" warning was sent.
const stateSkipWarned = "prune_skip_warned"

const (
	JobPrune = "prune"
	JobCheck = "check"
	JobStats = "stats"
	// JobTrashPurge forgets the trashed backups whose time has come (before the prune, so it frees the space).
	JobTrashPurge = "trash-purge"
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
	// RawStatsTag is RawStats limited to the snapshots with the tag.
	RawStatsTag(ctx context.Context, tag string) (restic.RepoStats, error)
	// ForgetRaw forgets a snapshot without waiting for the gate (maintenance holds it).
	ForgetRaw(ctx context.Context, snapshotID string) error
}

// Gate stops new restic work and waits for the running work (see package gate).
type Gate interface {
	Pause()
	Resume()
	WaitIdle(ctx context.Context) error
}

// Notifier receives failure messages (rendered per channel and language).
type Notifier interface {
	Send(ev notify.Event)
}

type Config struct {
	Hour, Minute   int           // daily window, local time
	PruneEnabled   bool          // scheduled prune
	CheckInterval  time.Duration // <= 0 disables scheduled checks
	ReadDataSubset string        // passed to restic check, "" = metadata only
	BusyWait       time.Duration // how long to wait for running work before skipping (default 15m)
	JobTimeout     time.Duration // deadline for each job once it runs; restic is killed after it (default 3h)
	MaxUnused      string        // restic prune --max-unused (default 5%)
	StatsQuiet     time.Duration // stats refresh after backups this long after the last backup (default 2m)
	StatsMaxWait   time.Duration // ... but at most this long after the first unmeasured backup (default 10m)
	// PruneSkipWarn: warn (once) after this many prune jobs in a row were skipped as busy; 0 = never.
	PruneSkipWarn int
	// TrashWarnBytes: before the window ends, warn (ntfy) about trashed backups that
	// become due within the next 24 h if they add up to at least this many logical bytes; 0 = off.
	TrashWarnBytes int64
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
	// AfterWindow, if set, runs in the background after each scheduled window
	// (the offsite copy's nightly run).
	AfterWindow func(ctx context.Context)

	// Now and Sleep are replaced in tests.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error

	busy    atomic.Bool
	wg      sync.WaitGroup
	mu      sync.Mutex
	current *store.Job

	statsMu      sync.Mutex
	statsTimer   *time.Timer
	statsClosed  bool
	statsRunning bool
	pendingSince time.Time       // first backup not measured yet; zero = nothing pending
	pendingSrv   map[string]bool // servers to measure with the next refresh
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
		if s.AfterWindow != nil && ctx.Err() == nil {
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.AfterWindow(ctx)
			}()
		}
	}
}

// scheduledJobs picks what the daily window does.
func (s *Service) scheduledJobs(ctx context.Context) []string {
	jobs := []string{JobTrashPurge}
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
		if typ == JobPrune {
			s.pruneSkipWarning(o)
		}
		if o.result == store.JobFailed && s.Notify != nil {
			s.Notify.Send(notify.Event{Key: notify.EventMaintenanceFailed, Params: notify.Params{"job": typ}})
		}
	}
}

// pruneSkipWarning sends one notification when the prune was skipped as busy
// PruneSkipWarn times in a row. A successful prune re-arms it.
func (s *Service) pruneSkipWarning(o outcome) {
	if s.Cfg.PruneSkipWarn <= 0 {
		return
	}
	ctx := context.Background()
	switch {
	case o.result == store.JobOK:
		if err := s.Store.SetState(ctx, stateSkipWarned, ""); err != nil {
			s.Log.Warn("store prune warning state failed", "err", err)
		}
	case o.result == store.JobSkipped && o.msg == msgSkippedBusy:
		n, err := s.Store.ConsecutiveSkipped(ctx, JobPrune, msgSkippedBusy)
		if err != nil || n < s.Cfg.PruneSkipWarn {
			return
		}
		if v, _ := s.Store.State(ctx, stateSkipWarned); v != "" {
			return
		}
		if err := s.Store.SetState(ctx, stateSkipWarned, "1"); err != nil {
			s.Log.Warn("store prune warning state failed", "err", err)
		}
		s.Log.Warn("prune keeps being skipped because the repository is busy", "times", n)
		if s.Notify != nil {
			s.Notify.Send(notify.Event{Key: notify.EventPruneSkipped, Params: notify.Params{"count": n}})
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
	case JobTrashPurge:
		return s.purgeTrash(ctx)
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

// purgeTrash forgets the trashed backups that are due. Only the snapshot is
// forgotten; the following prune frees the space. A snapshot that fails stays
// in the trash and is tried again the next night.
func (s *Service) purgeTrash(ctx context.Context) outcome {
	due, err := s.Store.DueTrash(ctx, s.now())
	if err != nil {
		return s.failureOutcome(JobTrashPurge, err)
	}
	if len(due) == 0 {
		s.warnTrashSoon(ctx)
		return outcome{result: store.JobOK, msg: "nothing to purge"}
	}
	var done, failed int
	var bytes int64
	err = s.exclusive(ctx, func() error {
		if err := s.Backend.Unlock(ctx); err != nil {
			s.Log.Warn("restic unlock failed", "err", err)
		}
		// Waiting for the repository can take long and an admin may restore meanwhile:
		// every item is claimed again (trashed -> purging, only if still due) before its snapshot goes.
		now := s.now()
		for _, it := range due {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := s.Store.BeginPurge(ctx, it.ID, &now); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					continue // restored, purged by hand or no longer due
				}
				return err
			}
			if it.SnapshotID != "" {
				if err := s.Backend.ForgetRaw(ctx, it.SnapshotID); err != nil {
					s.Log.Error("trash purge: forget failed", "backup", it.BackupUUID, "err", err)
					if aerr := s.Store.AbortPurge(context.Background(), it.ID); aerr != nil {
						s.Log.Error("trash purge: could not put the backup back", "backup", it.BackupUUID, "err", aerr)
					}
					failed++
					continue
				}
			}
			if err := s.Store.FinishPurge(context.Background(), it.ID); err != nil {
				return err
			}
			done++
			bytes += it.UncompressedBytes
		}
		return nil
	})
	if o, handled := s.failure(JobTrashPurge, err); handled {
		return o
	}
	s.warnTrashSoon(ctx)
	if failed > 0 {
		return outcome{result: store.JobFailed, msg: fmt.Sprintf("%d purged, %d failed, see gateway log", done, failed)}
	}
	return outcome{result: store.JobOK, msg: fmt.Sprintf("%d backups purged (%d bytes of original data)", done, bytes)}
}

// warnTrashSoon sends one ntfy message about the trash that empties in the next window.
func (s *Service) warnTrashSoon(ctx context.Context) {
	if s.Cfg.TrashWarnBytes <= 0 || s.Notify == nil {
		return
	}
	now := s.now()
	items, err := s.Store.TrashSoon(ctx, now, s.NextWindow().Add(time.Minute))
	if err != nil {
		s.Log.Warn("trash warning failed", "err", err)
		return
	}
	var sum int64
	for _, it := range items {
		sum += it.UncompressedBytes
	}
	if len(items) == 0 || sum < s.Cfg.TrashWarnBytes {
		return
	}
	s.Notify.Send(notify.Event{Key: notify.EventTrashWarning, Params: notify.Params{
		"count": len(items), "gb": float64(sum) / (1 << 30)}})
}

func (s *Service) failureOutcome(typ string, err error) outcome {
	o, _ := s.failure(typ, err)
	return o
}

// failure turns an error into a job outcome. Messages stay generic (restic's
// error text may contain paths); the detail goes to the log.
func (s *Service) failure(typ string, err error) (outcome, bool) {
	switch {
	case err == nil:
		return outcome{}, false
	case errors.Is(err, errRepoBusy):
		return outcome{result: store.JobSkipped, msg: msgSkippedBusy}, true
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

// RecordStats is the full statistics run (nightly and manual): the repository
// size, then the space of every server's snapshots. A failing server never
// fails the run.
func (s *Service) RecordStats(ctx context.Context) (store.RepoStat, error) {
	st, err := s.RecordRepoStats(ctx)
	if err != nil {
		return st, err
	}
	s.recordServerStats(ctx, nil, true)
	return st, nil
}

// RecordStartupStats takes the first repository snapshot and measures only the
// servers that have no value yet.
func (s *Service) RecordStartupStats(ctx context.Context) error {
	if _, err := s.RecordRepoStats(ctx); err != nil {
		return err
	}
	have, err := s.Store.ServerStats(ctx)
	if err != nil {
		return err
	}
	servers, err := s.Store.ServersWithSnapshots(ctx)
	if err != nil {
		return err
	}
	var missing []string
	for _, u := range servers {
		if _, ok := have[u]; !ok {
			missing = append(missing, u)
		}
	}
	s.recordServerStats(ctx, missing, false)
	return nil
}

// RecordRepoStats reads the repository size and stores a snapshot.
func (s *Service) RecordRepoStats(ctx context.Context) (store.RepoStat, error) {
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
	if err := s.Store.InsertRepoStat(ctx, st); err != nil {
		return st, err
	}
	return st, nil
}

// serverStatTimeout bounds one per-server restic call.
const serverStatTimeout = 15 * time.Minute

// recordServerStats measures the space of the given servers' snapshots (all
// servers if all is set), one restic call after the other (each waits for the
// gate). A failed call is logged and keeps the old value.
func (s *Service) recordServerStats(ctx context.Context, only []string, all bool) {
	servers := only
	if all {
		var err error
		servers, err = s.Store.ServersWithSnapshots(ctx)
		if err != nil {
			s.Log.Warn("list servers for stats failed", "err", err)
			return
		}
		if err := s.Store.DeleteServerStatsExcept(ctx, servers); err != nil {
			s.Log.Warn("clean up server stats failed", "err", err)
		}
	}
	for _, uuid := range servers {
		if ctx.Err() != nil {
			return
		}
		cctx, cancel := context.WithTimeout(ctx, serverStatTimeout)
		rs, err := s.Backend.RawStatsTag(cctx, restic.ServerTag(uuid))
		cancel()
		if err != nil {
			s.Log.Warn("server stats failed, keeping old value", "server", uuid, "err", err)
			continue
		}
		if err := s.Store.SetServerStat(ctx, uuid, rs.TotalSize, s.now()); err != nil {
			s.Log.Warn("store server stats failed", "server", uuid, "err", err)
		}
	}
}

// BackupDone notes a completed backup of serverUUID. The statistics (repository
// size, then that server's space) are refreshed once the backups stop coming:
// StatsQuiet after the last one, but at most StatsMaxWait after the first one
// that is still unmeasured. A series of backups therefore ends in one
// refresh with the final values, not in one measurement at the first backup.
// Each server is measured once per refresh, never per backup.
func (s *Service) BackupDone(serverUUID string) {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()
	if s.statsClosed {
		return
	}
	if s.pendingSince.IsZero() {
		s.pendingSince = s.now()
	}
	if serverUUID != "" {
		if s.pendingSrv == nil {
			s.pendingSrv = map[string]bool{}
		}
		s.pendingSrv[serverUUID] = true
	}
	s.armStats(s.statsQuiet())
}

func (s *Service) statsQuiet() time.Duration {
	if s.Cfg.StatsQuiet > 0 {
		return s.Cfg.StatsQuiet
	}
	return 2 * time.Minute
}

func (s *Service) statsMaxWait() time.Duration {
	if s.Cfg.StatsMaxWait > 0 {
		return s.Cfg.StatsMaxWait
	}
	return 10 * time.Minute
}

// armStats (re)starts the timer; statsMu must be held. The delay is cut so the
// refresh never lies more than StatsMaxWait after the oldest pending backup.
func (s *Service) armStats(d time.Duration) {
	if left := s.pendingSince.Add(s.statsMaxWait()).Sub(s.now()); left < d {
		d = max(left, 0)
	}
	if s.statsTimer == nil {
		s.statsTimer = time.AfterFunc(d, s.fireStats)
	} else {
		s.statsTimer.Reset(d)
	}
}

// fireStats runs the pending refresh. While a maintenance run is active or an
// earlier refresh is still running it tries again after the quiet period (the
// nightly run measures everything anyway).
func (s *Service) fireStats() {
	s.statsMu.Lock()
	if s.statsClosed || s.pendingSince.IsZero() {
		s.statsMu.Unlock()
		return
	}
	if s.busy.Load() || s.statsRunning {
		s.armStats(s.statsQuiet())
		s.statsMu.Unlock()
		return
	}
	servers := make([]string, 0, len(s.pendingSrv))
	for u := range s.pendingSrv {
		servers = append(servers, u)
	}
	sort.Strings(servers)
	s.pendingSrv, s.pendingSince = nil, time.Time{}
	s.statsRunning = true
	s.wg.Add(1) // under the lock: Close cannot slip in between
	s.statsMu.Unlock()
	defer s.wg.Done()
	defer func() {
		s.statsMu.Lock()
		s.statsRunning = false
		s.statsMu.Unlock()
	}()
	ctx := s.BaseCtx
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return
	}
	if _, err := s.RecordRepoStats(ctx); err != nil {
		s.Log.Warn("repo stats refresh failed", "err", err)
	}
	s.recordServerStats(ctx, servers, false)
}

// Close stops the pending statistics refresh; call it before Wait on shutdown.
func (s *Service) Close() {
	s.statsMu.Lock()
	s.statsClosed = true
	if s.statsTimer != nil {
		s.statsTimer.Stop()
	}
	s.statsMu.Unlock()
}

// Status is the maintenance part of the admin status.
type Status struct {
	PruneEnabled bool
	NextWindow   time.Time
	LastPrune    *store.Job // nil if never ran
	LastCheck    *store.Job
	Running      *store.Job // nil if idle
	// PruneSkipped counts the prune jobs in a row that were skipped as busy.
	PruneSkipped int
	// Warnings are short texts for things that need attention (empty = none).
	Warnings []string
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
	if n, err := s.Store.ConsecutiveSkipped(ctx, JobPrune, msgSkippedBusy); err != nil {
		return st, err
	} else {
		st.PruneSkipped = n
		if w := s.Cfg.PruneSkipWarn; w > 0 && n >= w {
			st.Warnings = append(st.Warnings, fmt.Sprintf("prune was skipped as busy %d times in a row", n))
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
