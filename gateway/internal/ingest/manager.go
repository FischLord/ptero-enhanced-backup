package ingest

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"ptero-enhanced-backup/gateway/internal/gate"
	"ptero-enhanced-backup/gateway/internal/restic"
	"ptero-enhanced-backup/gateway/internal/store"
)

// Backend is the part of restic the pipeline needs.
type Backend interface {
	Backup(ctx context.Context, r io.Reader, o restic.BackupOptions) (restic.Summary, error)
	Forget(ctx context.Context, snapshotID string) error
}

// Limits bounds the resources the pipelines may use.
type Limits struct {
	MaxSpoolBytes int64         // total spooled bytes, <= 0 unlimited
	MaxIngests    int           // concurrent restic backups
	MaxIndexFiles int           // tar members recorded per backup, <= 0 unlimited
	NoIndexHash   bool          // do not compute the SHA-256 of regular files for the index
	IngestTimeout time.Duration // deadline for the pipeline after Complete, <= 0 none
}

// DefaultLimits are used unless Configure is called.
var DefaultLimits = Limits{MaxSpoolBytes: 8 << 30, MaxIngests: 2, MaxIndexFiles: 500000, IngestTimeout: 6 * time.Hour}

// Manager owns all running uploads.
type Manager struct {
	Store     *store.Store
	Backend   Backend
	SpoolDir  string
	Log       *slog.Logger
	IdleLimit time.Duration // uploads without activity for this long are aborted
	Quota     *Quota
	// Gate lets maintenance hold back new pipelines (they queue, uploads keep spooling).
	Gate *gate.Gate
	// OnDone, if set, is called after every pipeline that was not aborted. It must not block.
	OnDone func(DoneEvent)
	// OnAbandoned, if set, is called once per backup whose upload never finished
	// (found pending at start, or dropped for inactivity). It must not block.
	OnAbandoned func(AbandonedEvent)

	queued, running atomic.Int64
	limits          Limits
	slots           chan struct{}

	baseCtx context.Context
	stop    context.CancelFunc
	mu      sync.Mutex
	uploads map[string]*Upload
	wg      sync.WaitGroup
}

func NewManager(st *store.Store, be Backend, spoolDir string, log *slog.Logger) *Manager {
	ctx, stop := context.WithCancel(context.Background())
	return &Manager{
		Store: st, Backend: be, SpoolDir: spoolDir, Log: log, IdleLimit: 6 * time.Hour,
		baseCtx: ctx, stop: stop, uploads: map[string]*Upload{},
		Quota: &Quota{Max: DefaultLimits.MaxSpoolBytes}, limits: DefaultLimits,
		slots: make(chan struct{}, DefaultLimits.MaxIngests),
		Gate:  &gate.Gate{},
	}
}

// DoneEvent describes a finished pipeline.
type DoneEvent struct {
	ServerUUID, BackupUUID string
	OK                     bool
	TimedOut               bool
}

// AbandonedEvent describes an upload that was dropped without being completed.
type AbandonedEvent struct {
	ServerUUID, BackupUUID string
	Reason                 string
}

// Load returns the pipelines that currently run restic and those waiting for a slot.
func (m *Manager) Load() (running, queued int) {
	return int(m.running.Load()), int(m.queued.Load())
}

// acquire waits for an ingest slot and for the gate to be open; release undoes it.
func (m *Manager) acquire(ctx context.Context) error {
	m.queued.Add(1)
	defer m.queued.Add(-1)
	for {
		select {
		case m.slots <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		// Check the gate while holding the slot, so waiting pipelines never count as running.
		if m.Gate.TryEnter() {
			m.running.Add(1)
			return nil
		}
		<-m.slots
		if err := m.Gate.WaitOpen(ctx); err != nil {
			return err
		}
	}
}

func (m *Manager) release() {
	m.running.Add(-1)
	m.Gate.Leave()
	<-m.slots
}

// Configure applies resource limits. Call it before the first Create.
func (m *Manager) Configure(l Limits) {
	if l.MaxIngests < 1 {
		l.MaxIngests = 1
	}
	m.limits = l
	m.Quota.Max = l.MaxSpoolBytes
	m.slots = make(chan struct{}, l.MaxIngests)
}

// Recover cleans up after a crash: running uploads are lost, so their rows
// are failed and the spool is emptied.
func (m *Manager) Recover(ctx context.Context) error {
	pending, err := m.Store.PendingBackups(ctx)
	if err != nil {
		return err
	}
	n, err := m.Store.FailStale(ctx, time.Now())
	if err != nil {
		return err
	}
	if m.OnAbandoned != nil {
		for _, p := range pending {
			m.OnAbandoned(AbandonedEvent{ServerUUID: p.ServerUUID, BackupUUID: p.BackupUUID,
				Reason: "upload abandoned (gateway restarted during the upload) - likely a Wings/Docker restart; Wings does not retry, the backup is missing"})
		}
	}
	if n > 0 {
		m.Log.Warn("marked interrupted backups as failed", "count", n)
	}
	if err := os.RemoveAll(m.SpoolDir); err != nil {
		return err
	}
	return os.MkdirAll(m.SpoolDir, 0o755)
}

// Create registers a new upload and starts its pipeline, which waits for part 1.
func (m *Manager) Create(ctx context.Context, key, serverUUID, backupUUID string) (*Upload, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, err
	}
	id := hex.EncodeToString(raw[:])
	u := newUpload(id, key, serverUUID, backupUUID, m.SpoolDir, m.Quota)
	if err := os.MkdirAll(u.dir, 0o755); err != nil {
		return nil, err
	}
	bid, err := m.Store.CreateUpload(ctx, id, key, serverUUID, backupUUID, time.Now())
	if err != nil {
		os.RemoveAll(u.dir)
		return nil, err
	}
	u.BackupID = bid
	pctx, cancel := context.WithCancel(m.baseCtx)
	u.cancel = cancel

	m.mu.Lock()
	m.uploads[id] = u
	m.mu.Unlock()
	m.wg.Add(1)
	go m.run(pctx, u)
	return u, nil
}

// Get returns a running upload.
func (m *Manager) Get(id string) (*Upload, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u, ok := m.uploads[id]; ok {
		return u, nil
	}
	return nil, ErrNoSuchUpload
}

// IsIngesting reports whether the upload's pipeline is still running.
func (m *Manager) IsIngesting(uploadID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.uploads[uploadID]
	return ok
}

// Complete finishes the part list and returns immediately; restic keeps
// running. A repeated Complete with the same list returns the same ETag.
func (m *Manager) Complete(ctx context.Context, id, key string, refs []PartRef) (string, error) {
	u, err := m.Get(id)
	if err != nil {
		return m.completeFinished(ctx, id, key, refs)
	}
	if u.Key != key {
		return "", ErrNoSuchUpload
	}
	etag, err := u.Complete(refs)
	if err != nil {
		return "", err
	}
	u.deadlineOnce.Do(func() {
		if err := m.Store.SetUploadComplete(ctx, id, etag, u.sig); err != nil {
			m.Log.Error("store upload status", "err", err)
		}
		if d := m.limits.IngestTimeout; d > 0 {
			t := time.AfterFunc(d, func() {
				u.timedOut.Store(true)
				u.cancel()
			})
			u.mu.Lock()
			u.timer = t
			u.mu.Unlock()
		}
	})
	return etag, nil
}

// completeFinished answers a retried Complete for an upload whose pipeline is gone.
func (m *Manager) completeFinished(ctx context.Context, id, key string, refs []PartRef) (string, error) {
	info, err := m.Store.UploadInfo(ctx, id)
	if err != nil || info.Key != key || info.Status != "complete" {
		return "", ErrNoSuchUpload
	}
	if PartsSig(refs) != info.PartsSig {
		return "", fmt.Errorf("%w: upload was already completed with a different part list", ErrInvalidPart)
	}
	return info.ETag, nil
}

// UploadStatus reports the stored status of an upload ("open", "complete", ...).
func (m *Manager) UploadStatus(ctx context.Context, id string) (string, error) {
	return m.Store.UploadStatus(ctx, id)
}

// Done returns a channel closed when the upload's pipeline has ended, or nil
// if it is not running.
func (m *Manager) Done(id string) <-chan struct{} {
	u, err := m.Get(id)
	if err != nil {
		return nil
	}
	return u.done
}

// Fail ends an upload that is still receiving parts: the pipeline is stopped,
// the backup row becomes "failed" with reason, the spool is released and
// further parts and Complete are answered with ErrPipelineFailed.
func (m *Manager) Fail(id string, reason error) {
	u, err := m.Get(id)
	if err != nil {
		return
	}
	u.mu.Lock()
	if u.completed || u.aborted || u.failure != nil {
		u.mu.Unlock()
		return
	}
	u.failure = reason
	u.broadcast()
	u.mu.Unlock()
	u.cancel()
}

// Abort stops the pipeline, removes the spool and forgets any snapshot.
func (m *Manager) Abort(id string) error {
	u, err := m.Get(id)
	if err != nil {
		return err
	}
	u.mu.Lock()
	if u.completed {
		u.mu.Unlock()
		return ErrNoSuchUpload
	}
	u.aborted = true
	u.broadcast()
	u.mu.Unlock()
	u.cancel()
	select {
	case <-u.done:
	case <-time.After(30 * time.Second):
		m.Log.Warn("abort: pipeline still running", "upload", id)
	}
	m.remove(id)
	return nil
}

func (m *Manager) remove(id string) {
	m.mu.Lock()
	delete(m.uploads, id)
	m.mu.Unlock()
}

// Reap aborts uploads that have been idle for IdleLimit; run it periodically.
func (m *Manager) Reap() {
	m.mu.Lock()
	stale := map[string]*Upload{}
	for id, u := range m.uploads {
		if !u.isCompleted() && time.Since(u.idleSince()) > m.IdleLimit {
			stale[id] = u
		}
	}
	m.mu.Unlock()
	for id, u := range stale {
		m.Log.Warn("aborting idle upload", "upload", id)
		if m.Abort(id) == nil && m.OnAbandoned != nil {
			m.OnAbandoned(AbandonedEvent{ServerUUID: u.ServerUUID, BackupUUID: u.BackupUUID,
				Reason: "upload abandoned (no activity for " + m.IdleLimit.Round(time.Minute).String() + ") - likely a Wings/Docker restart; Wings does not retry, the backup is missing"})
		}
	}
}

// Shutdown aborts uploads that are still receiving parts, gives finished
// ones until ctx expires to reach restic, then cancels everything.
func (m *Manager) Shutdown(ctx context.Context) {
	m.mu.Lock()
	var open []string
	for id, u := range m.uploads {
		if !u.isCompleted() {
			open = append(open, id)
		}
	}
	m.mu.Unlock()
	for _, id := range open {
		_ = m.Abort(id)
	}
	waited := make(chan struct{})
	go func() { m.wg.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-ctx.Done():
		m.Log.Warn("shutdown: cancelling running ingests")
		m.stop()
		<-waited
	}
	m.stop()
}

func (m *Manager) run(ctx context.Context, u *Upload) {
	defer m.wg.Done()
	defer close(u.done)
	defer func() {
		os.RemoveAll(u.dir)
		u.releaseAll()
		u.mu.Lock()
		if u.timer != nil {
			u.timer.Stop()
		}
		u.mu.Unlock()
	}()

	start := time.Now()
	log := m.Log.With("upload", u.ID, "server", u.ServerUUID, "backup", u.BackupUUID)
	log.Info("ingest started")

	var pr procResult
	var err error
	// Parts keep being accepted while we wait for a free slot; only consumption waits.
	if err = m.acquire(ctx); err == nil {
		u.mu.Lock()
		u.hasSlot.Store(true)
		u.broadcast() // part writers waiting for space may now be admitted
		u.mu.Unlock()
		pr, err = m.process(ctx, u)
		u.hasSlot.Store(false)
		m.release()
	}
	if err != nil && u.timedOut.Load() {
		err = errors.New("ingest timeout exceeded")
	}
	if err != nil {
		// Fail() cancelled the pipeline; report why, not "context canceled".
		u.mu.Lock()
		if u.failure != nil && !u.aborted {
			err = u.failure
		}
		u.mu.Unlock()
	}

	// Finalisation must not be cut short: a good snapshot has to be recorded.
	bg, cancel := context.WithTimeout(context.Background(), time.Hour)
	defer cancel()
	dur := time.Since(start)
	aborted := u.isAborted()
	sum := pr.sum

	// A snapshot that must not survive: aborted, or restic flagged it incomplete.
	if sum.SnapshotID != "" && (err != nil || aborted) {
		if ferr := m.Backend.Forget(bg, sum.SnapshotID); ferr != nil {
			log.Error("forget leftover snapshot", "snapshot", sum.SnapshotID, "err", ferr)
		}
		sum.SnapshotID = ""
	}

	ok := false
	switch {
	case aborted:
		_ = m.Store.SetUploadStatus(bg, u.ID, "aborted")
		_ = m.Store.FailBackup(bg, u.BackupID, "aborted", dur, time.Now())
		log.Info("ingest aborted")
	case err != nil:
		u.fail(err)
		_ = m.Store.SetUploadStatus(bg, u.ID, "failed")
		_ = m.Store.FailBackup(bg, u.BackupID, err.Error(), dur, time.Now())
		log.Error("ingest failed", "err", err)
	default:
		fin := store.Finish{
			SnapshotID: sum.SnapshotID, CompressedBytes: u.consumed.Load(), UncompressedBytes: pr.uncompressed,
			DataAdded: sum.DataAdded, TotalBytesProcessed: sum.TotalBytesProcessed,
			FileCount: int64(len(pr.files)), IndexTruncated: pr.truncated, Duration: dur,
		}
		var serr error
		for i := 0; i < 3; i++ {
			if serr = m.Store.CompleteBackup(bg, u.BackupID, fin, time.Now()); serr == nil {
				break
			}
			time.Sleep(time.Second)
		}
		if serr != nil {
			// The snapshot is good; keep it even though the row could not be updated.
			log.Error("store backup result", "snapshot", sum.SnapshotID, "err", serr)
			break
		}
		ok = true
		if ierr := m.Store.InsertFiles(bg, u.BackupID, pr.files); ierr != nil {
			log.Error("store file index", "err", ierr)
			_ = m.Store.SetIndexTruncated(bg, u.BackupID)
		}
		log.Info("ingest finished", "snapshot", sum.SnapshotID, "compressed", fin.CompressedBytes,
			"uncompressed", pr.uncompressed, "data_added", sum.DataAdded, "index_truncated", pr.truncated,
			"duration", dur.Round(time.Millisecond))
	}
	if aborted || u.isCompleted() {
		m.remove(u.ID)
	}
	if m.OnDone != nil && !aborted {
		m.OnDone(DoneEvent{ServerUUID: u.ServerUUID, BackupUUID: u.BackupUUID, OK: ok, TimedOut: u.timedOut.Load()})
	}
}

type countReader struct {
	r io.Reader
	n int64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

type procResult struct {
	sum          restic.Summary
	files        []store.FileEntry
	uncompressed int64
	truncated    bool
}

// process runs parts -> gunzip -> tee(tar index, restic stdin).
func (m *Manager) process(ctx context.Context, u *Upload) (procResult, error) {
	src := &partReader{u: u, ctx: ctx}
	defer src.Close()

	gz, err := gzip.NewReader(src)
	if err != nil {
		return procResult{}, fmt.Errorf("gzip: %w", err)
	}
	// Cancelling bctx kills restic; used when the stream turns out to be broken.
	bctx, bcancel := context.WithCancel(ctx)
	defer bcancel()
	pr, pw := io.Pipe()
	type result struct {
		sum restic.Summary
		err error
	}
	resc := make(chan result, 1)
	go func() {
		sum, err := m.Backend.Backup(bctx, pr, restic.BackupOptions{
			Filename: u.ServerUUID + ".tar",
			Host:     "peb-gateway",
			Tags:     []string{"server:" + u.ServerUUID, "backup:" + u.BackupUUID},
		})
		pr.CloseWithError(err)
		resc <- result{sum, err}
	}()

	cr := &countReader{r: gz}
	tee := io.TeeReader(cr, pw)
	files, truncated, idxErr := indexTarHash(tee, m.limits.MaxIndexFiles, !m.limits.NoIndexHash)
	if idxErr == nil {
		// Consume trailing padding so restic gets every byte and gzip verifies its checksum.
		_, idxErr = io.Copy(io.Discard, tee)
	}
	if idxErr != nil {
		bcancel() // never let restic mistake a broken stream for a finished one
		pw.CloseWithError(idxErr)
	} else {
		pw.Close()
	}
	res := <-resc
	out := procResult{sum: res.sum, files: files, uncompressed: cr.n, truncated: truncated}
	if idxErr != nil || res.err != nil {
		return out, errors.Join(idxErr, res.err)
	}
	return out, nil
}
