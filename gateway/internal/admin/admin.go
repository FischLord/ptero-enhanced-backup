// Package admin implements the JSON admin API under /admin/v1 (bearer token).
// It is read-only except for triggering maintenance runs. The gateway knows
// only server and backup UUIDs; the panel extension maps them to names.
package admin

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ptero-enhanced-backup/gateway/internal/maintenance"
	"ptero-enhanced-backup/gateway/internal/offsite"
	"ptero-enhanced-backup/gateway/internal/restic"
	"ptero-enhanced-backup/gateway/internal/store"
)

// Prefix is the URL prefix of the admin API.
const Prefix = "/admin/v1"

// Restic is what the API needs from the restic wrapper.
type Restic interface {
	Dump(ctx context.Context, snapshotID, path string) (io.ReadCloser, error)
	Reachable(ctx context.Context) error
}

// Maintenance is the scheduler/runner.
type Maintenance interface {
	Status(ctx context.Context) (maintenance.Status, error)
	Trigger(jobs []string) ([]int64, error)
}

// Offsite is the offsite copy (secondary repository). A nil Offsite means "not configured".
type Offsite interface {
	Status(ctx context.Context) (offsite.Status, error)
	TriggerCopy() (int64, error)
	// ConfirmForget releases a stopped forget step once.
	ConfirmForget(ctx context.Context) error
}

// Dumps hands out restore slots shared with the S3 download path.
type Dumps interface {
	AcquireDump(ctx context.Context) (func(), error)
	ActiveDumps() int
}

// Ingests reports the load of the ingest pipelines.
type Ingests interface {
	Load() (running, queued int)
}

type Server struct {
	Token      string // empty disables the API (404)
	Store      *store.Store
	Restic     Restic
	Maint      Maintenance
	Offsite    Offsite
	Keys       KeyManager // nil: the key endpoints answer 501
	Dumps      Dumps
	Ingest     Ingests
	SpoolUsed  func() int64
	SpoolLimit int64
	Version    string
	Repo       string // RESTIC_REPOSITORY, shown without credentials
	Capacity   int64  // optional repository capacity for the forecast, 0 = unknown
	Log        *slog.Logger
	Now        func() time.Time // default time.Now
	Loc        *time.Location   // day boundaries of the history, default time.Local
	Started    time.Time
	// Forgetter purges trashed snapshots on request; Notify gets the trash messages (may be nil).
	Forgetter Forgetter
	Notify    Notifier
	// OrphanRetention: how long the backups of a server deleted in the panel stay in the trash.
	OrphanRetention time.Duration
	// ReconcileOff answers POST /servers/reconcile with skipped=disabled (PEB_RECONCILE=0).
	ReconcileOff bool
	// DownloadTimeout bounds a single-file download (default 2h).
	DownloadTimeout time.Duration
	ReachableTTL    time.Duration // default 60s
	// MetricsToken, if set, is the only token accepted on /metrics; else Token is used.
	MetricsToken string

	once    sync.Once
	mux     *http.ServeMux
	reachMu sync.Mutex
	reachAt time.Time
	reachOK bool

	metricsMu    sync.Mutex
	metricsAt    time.Time
	metricsCache *metricsData
}

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Server) loc() *time.Location {
	if s.Loc != nil {
		return s.Loc
	}
	return time.Local
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.Token == "" {
		http.NotFound(w, r)
		return
	}
	if !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="peb-admin"`)
		writeErr(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
		return
	}
	s.once.Do(s.routes)
	w.Header().Set("Cache-Control", "no-store")
	s.mux.ServeHTTP(w, r)
}

func (s *Server) authorized(r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false
	}
	// Hash first so the comparison does not leak the token length.
	a, b := sha256.Sum256([]byte(tok)), sha256.Sum256([]byte(s.Token))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func (s *Server) routes() {
	m := http.NewServeMux()
	m.HandleFunc("GET "+Prefix+"/status", s.status)
	m.HandleFunc("GET "+Prefix+"/summary", s.summary)
	m.HandleFunc("GET "+Prefix+"/servers", s.servers)
	m.HandleFunc("GET "+Prefix+"/backups", s.backups)
	m.HandleFunc("GET "+Prefix+"/backups/{uuid}/files", s.files)
	m.HandleFunc("GET "+Prefix+"/backups/{uuid}/file", s.file)
	m.HandleFunc("GET "+Prefix+"/backups/{uuid}/diff", s.diff)
	m.HandleFunc("GET "+Prefix+"/history", s.history)
	m.HandleFunc("GET "+Prefix+"/maintenance", s.maintenanceJobs)
	m.HandleFunc("POST "+Prefix+"/maintenance/run", s.maintenanceRun)
	m.HandleFunc("POST "+Prefix+"/offsite/forget-confirm", s.offsiteForgetConfirm)
	m.HandleFunc("GET "+Prefix+"/settings/password-ack", s.getPasswordAck)
	m.HandleFunc("PUT "+Prefix+"/settings/password-ack", s.putPasswordAck)
	m.HandleFunc("GET "+Prefix+"/keys", s.listKeys)
	m.HandleFunc("POST "+Prefix+"/keys", s.createKey)
	m.HandleFunc("DELETE "+Prefix+"/keys/{id}", s.deleteKey)
	s.mux = m
	s.trashRoutes()
}

// ---- helpers ----

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, key, msg string) {
	writeJSON(w, code, map[string]string{"error": key, "message": msg})
}

func (s *Server) internal(w http.ResponseWriter, err error) {
	s.Log.Error("admin request failed", "err", err)
	writeErr(w, http.StatusInternalServerError, "internal", "internal error")
}

// tstr formats a time as RFC 3339 UTC; the zero time becomes null.
func tstr(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	v := t.UTC().Format(time.RFC3339)
	return &v
}

// intParam reads an optional non-negative integer query parameter, clamped to max (max > 0).
func intParam(q url.Values, name string, def, max int) (int, error) {
	v := q.Get(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, errors.New(name + " must be a non-negative integer")
	}
	if max > 0 && n > max {
		n = max
	}
	return n, nil
}

// publicError maps the stored error to a category; the stored text may contain internals.
func publicError(status, msg string) string {
	if status != store.StatusFailed {
		return ""
	}
	switch {
	case msg == "aborted":
		return "aborted"
	case strings.Contains(msg, "timeout exceeded"):
		return "timeout"
	case strings.Contains(msg, "gateway restarted"):
		return "gateway restarted"
	}
	return "ingest failed"
}

type jobJSON struct {
	ID         int64   `json:"id"`
	Type       string  `json:"type"`
	Trigger    string  `json:"trigger"`
	StartedAt  *string `json:"started_at"`
	FinishedAt *string `json:"finished_at"`
	Result     string  `json:"result"`
	Message    string  `json:"message"`
	BytesFreed *int64  `json:"bytes_freed"`
}

func jobOut(j *store.Job) *jobJSON {
	if j == nil {
		return nil
	}
	return &jobJSON{ID: j.ID, Type: j.Type, Trigger: j.Trigger, StartedAt: tstr(j.StartedAt), FinishedAt: tstr(j.FinishedAt),
		Result: j.Result, Message: j.Message, BytesFreed: j.BytesFreed}
}

// repoInfo splits a restic repository string into type and a location without credentials.
//
// Credentials may sit anywhere before the host and may contain "/", "@", "#" or
// "%" unescaped, so parsing is avoided: everything up to the last "@" is dropped.
// That can also cut an "@" inside a path, which only loses detail.
func repoInfo(repo string) (typ, loc string) {
	typ = restic.RepoType(repo)
	if typ == "local" {
		return typ, repo
	}
	rest := strings.TrimPrefix(repo, typ+":")
	scheme := ""
	if i := strings.Index(rest, "://"); i >= 0 {
		scheme, rest = rest[:i+3], rest[i+3:]
	}
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		rest = rest[i+1:]
	}
	return typ, scheme + rest
}

// offsiteBlock is the "secondary" block of status and summary. The repository
// address is shown without credentials.
func (s *Server) offsiteBlock(ctx context.Context) (map[string]any, error) {
	out := map[string]any{
		"configured": false, "repo": nil, "type": nil, "state": "off", "running": false, "last_copy_at": nil, "last_copy_result": nil,
		"last_error": "", "forget_blocked": false, "lag_snapshots": nil, "lag_since": nil, "stored_bytes": nil, "stored_at": nil, "next_copy_at": nil,
	}
	if s.Offsite == nil {
		return out, nil
	}
	st, err := s.Offsite.Status(ctx)
	if err != nil {
		return nil, err
	}
	typ, loc := repoInfo(st.Repo)
	repo := loc
	if typ != "local" {
		repo = typ + ":" + loc
	}
	out["configured"], out["repo"], out["type"] = true, repo, typ
	out["last_error"] = st.LastError
	out["forget_blocked"] = st.ForgetBlocked
	out["running"] = st.Running != nil
	if st.LastCopy != nil {
		out["last_copy_at"] = tstr(st.LastCopy.FinishedAt)
		out["last_copy_result"] = st.LastCopy.Result
	}
	if st.Lag != nil {
		out["lag_snapshots"] = *st.Lag
	}
	out["lag_since"] = tstr(st.LagSince)
	if st.StoredBytes != nil {
		out["stored_bytes"] = *st.StoredBytes
		out["stored_at"] = tstr(st.StoredAt)
	}
	out["next_copy_at"] = tstr(st.NextCopy)
	switch {
	case st.Running != nil:
		out["state"] = "copying"
	case st.LastError != "" || (st.LastCopy != nil && st.LastCopy.Result == store.JobFailed):
		out["state"] = "error"
	case st.LastCopy == nil:
		out["state"] = "pending"
	default:
		out["state"] = "ok"
	}
	return out, nil
}

func (s *Server) reachable(ctx context.Context) (bool, time.Time) {
	ttl := s.ReachableTTL
	if ttl <= 0 {
		ttl = time.Minute
	}
	s.reachMu.Lock()
	defer s.reachMu.Unlock()
	if s.reachAt.IsZero() || s.now().Sub(s.reachAt) > ttl {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		s.reachOK = s.Restic.Reachable(cctx) == nil
		cancel()
		s.reachAt = s.now()
	}
	return s.reachOK, s.reachAt
}

// ---- handlers ----

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ms, err := s.Maint.Status(ctx)
	if err != nil {
		s.internal(w, err)
		return
	}
	typ, loc := repoInfo(s.Repo)
	ok, at := s.reachable(ctx)
	running, queued := s.Ingest.Load()
	sec, err := s.offsiteBlock(ctx)
	if err != nil {
		s.internal(w, err)
		return
	}
	warnings := ms.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	writeJSON(w, 200, map[string]any{
		"secondary":      sec,
		"version":        s.Version,
		"uptime_seconds": int64(s.now().Sub(s.Started).Seconds()),
		"repo":           map[string]any{"type": typ, "location": loc, "reachable": ok, "checked_at": tstr(at)},
		"ingest":         map[string]any{"running": running, "queued": queued},
		"dumps":          map[string]any{"active": s.Dumps.ActiveDumps()},
		"spool":          map[string]any{"used_bytes": s.SpoolUsed(), "limit_bytes": s.SpoolLimit},
		"maintenance": map[string]any{
			"prune_enabled":             ms.PruneEnabled,
			"next_window":               tstr(ms.NextWindow),
			"last_prune":                jobOut(ms.LastPrune),
			"last_check":                jobOut(ms.LastCheck),
			"running_job":               jobOut(ms.Running),
			"prune_skipped_consecutive": ms.PruneSkipped,
			"warnings":                  warnings,
		},
	})
}

func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	t, err := s.Store.Totals(ctx, s.now())
	if err != nil {
		s.internal(w, err)
		return
	}
	out := map[string]any{
		"backups_count":      t.Count,
		"logical_bytes":      t.LogicalBytes,
		"received_bytes":     t.ReceivedBytes,
		"data_added_total":   t.DataAdded,
		"upload_saved_bytes": t.LogicalBytes - t.DataAdded,
		"failures_30d":       t.Failed30,
		"success_rate_30d":   nil,
		"last_backup_at":     tstr(t.LastBackup),
		"stored_bytes":       nil,
		"stored_at":          nil,
		"referenced_bytes":   nil,
		"stored_bytes_exact": nil,
		"saved_bytes":        nil,
		"saved_percent":      nil,
		"factor":             nil,
	}
	if n := t.OK30 + t.Failed30; n > 0 {
		out["success_rate_30d"] = float64(t.OK30) / float64(n)
	}
	switch st, err := s.Store.LatestRepoStat(ctx); {
	case err == nil:
		stored := st.Stored()
		out["stored_bytes"] = stored
		out["referenced_bytes"] = st.TotalSize
		out["stored_bytes_exact"] = st.DiskBytes != nil
		out["stored_at"] = tstr(st.Time)
		out["saved_bytes"] = t.LogicalBytes - stored
		if t.LogicalBytes > 0 {
			out["saved_percent"] = float64(t.LogicalBytes-stored) / float64(t.LogicalBytes) * 100
		}
		if stored > 0 {
			out["factor"] = float64(t.LogicalBytes) / float64(stored)
		}
	case !errors.Is(err, store.ErrNotFound):
		s.internal(w, err)
		return
	}
	target, err := s.target(ctx)
	if err != nil {
		s.internal(w, err)
		return
	}
	out["target"] = target
	tt, err := s.Store.TrashTotals(ctx)
	if err != nil {
		s.internal(w, err)
		return
	}
	out["trash"] = map[string]any{"count": tt.Count, "logical_bytes": tt.LogicalBytes, "next_purge_at": tstr(tt.NextPurge)}
	if out["secondary"], err = s.offsiteBlock(ctx); err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, 200, out)
}

// target describes the room on the backup target (statfs for filesystem
// repositories, else PEB_REPO_CAPACITY_BYTES minus stored; null if neither).
func (s *Server) target(ctx context.Context) (any, error) {
	sp := s.space(ctx)
	if sp == nil {
		return nil, nil
	}
	used := sp.total - sp.free
	other := used - sp.stored
	if other < 0 {
		other = 0
	}
	out := map[string]any{
		"total_bytes": sp.total, "free_bytes": sp.free, "used_bytes": used,
		"peb_bytes": sp.stored, "other_bytes": other, "exact": sp.exact,
		"days_until_full": nil, "avg_data_added": nil, "backups_remaining": nil,
	}
	measured, err := s.dailyStored(ctx, 30)
	if err != nil {
		return nil, err
	}
	if fc := s.forecast(s.now(), measured, sp.total, sp.free); fc["days_until_full"] != nil {
		out["days_until_full"] = fc["days_until_full"]
	}
	rows, err := s.Store.BackupsSince(ctx, s.now().AddDate(0, 0, -30))
	if err != nil {
		return nil, err
	}
	var sum, n int64
	for _, b := range rows {
		if b.Status == store.StatusComplete {
			sum += b.DataAdded
			n++
		}
	}
	if n > 0 {
		avg := sum / n
		out["avg_data_added"] = avg
		if avg > 0 {
			out["backups_remaining"] = sp.free / avg
		}
	}
	return out, nil
}

type serverJSON struct {
	ServerUUID     string  `json:"server_uuid"`
	BackupCount    int64   `json:"backup_count"`
	LastSuccessAt  *string `json:"last_success_at"`
	LastStatus     string  `json:"last_status"`
	LastError      string  `json:"last_error"`
	LastFinishedAt *string `json:"last_finished_at"`
	LogicalBytes   int64   `json:"logical_bytes"`
	DataAddedTotal int64   `json:"data_added_total"`
	AvgDurationMS  int64   `json:"avg_duration_ms"`
	LastDurationMS int64   `json:"last_duration_ms"`
	// Latest complete backup: uncompressed size and what it really added after dedup.
	LastBackupBytes int64 `json:"last_backup_bytes"`
	LastDataAdded   int64 `json:"last_data_added"`
	// Space the server's snapshots occupy in the repository (compressed, deduplicated);
	// null until measured.
	StoredBytes *int64  `json:"stored_bytes"`
	StoredAt    *string `json:"stored_at"`
}

func (s *Server) servers(w http.ResponseWriter, r *http.Request) {
	rows, err := s.Store.ServerRows(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	stats, err := s.Store.ServerStats(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	byUUID := map[string]*serverJSON{}
	var totalDur = map[string]int64{}
	for _, b := range rows { // oldest first, so the last row of a server is its newest
		sj := byUUID[b.ServerUUID]
		if sj == nil {
			sj = &serverJSON{ServerUUID: b.ServerUUID}
			byUUID[b.ServerUUID] = sj
		}
		sj.LastStatus = b.Status
		sj.LastError = publicError(b.Status, b.Error)
		sj.LastFinishedAt = tstr(b.FinishedAt)
		sj.LastDurationMS = b.Duration.Milliseconds()
		if b.Status == store.StatusComplete {
			sj.BackupCount++
			sj.LastSuccessAt = tstr(b.FinishedAt)
			sj.LogicalBytes += b.UncompressedBytes
			sj.DataAddedTotal += b.DataAdded
			sj.LastBackupBytes = b.UncompressedBytes
			sj.LastDataAdded = b.DataAdded
			totalDur[b.ServerUUID] += b.Duration.Milliseconds()
		}
	}
	items := make([]*serverJSON, 0, len(byUUID))
	for id, sj := range byUUID {
		if sj.BackupCount > 0 {
			sj.AvgDurationMS = totalDur[id] / sj.BackupCount
		}
		if st, ok := stats[id]; ok {
			b, at := st.StoredBytes, st.MeasuredAt.UTC().Format(time.RFC3339)
			sj.StoredBytes, sj.StoredAt = &b, &at
		}
		items = append(items, sj)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ServerUUID < items[j].ServerUUID })
	writeJSON(w, 200, map[string]any{"items": items})
}

type backupJSON struct {
	BackupUUID          string  `json:"backup_uuid"`
	ServerUUID          string  `json:"server_uuid"`
	Status              string  `json:"status"`
	Error               string  `json:"error"`
	SnapshotID          string  `json:"snapshot_id"`
	ReceivedBytes       int64   `json:"received_bytes"`
	LogicalBytes        int64   `json:"logical_bytes"`
	DataAdded           int64   `json:"data_added"`
	TotalBytesProcessed int64   `json:"total_bytes_processed"`
	FileCount           int64   `json:"file_count"`
	IndexTruncated      bool    `json:"index_truncated"`
	StartedAt           *string `json:"started_at"`
	FinishedAt          *string `json:"finished_at"`
	DurationMS          int64   `json:"duration_ms"`
}

func (s *Server) backups(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, err1 := intParam(q, "limit", 50, 500)
	offset, err2 := intParam(q, "offset", 0, 0)
	if err := errors.Join(err1, err2); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	f := store.BackupFilter{Server: q.Get("server"), Status: q.Get("status"), Limit: limit, Offset: offset}
	switch f.Status {
	case "", store.StatusComplete, store.StatusFailed, store.StatusPending, store.StatusDeleted:
	default:
		writeErr(w, 400, "bad_request", "status must be complete, failed, pending or deleted")
		return
	}
	list, total, err := s.Store.ListBackups(r.Context(), f)
	if err != nil {
		s.internal(w, err)
		return
	}
	items := make([]backupJSON, 0, len(list))
	for _, b := range list {
		items = append(items, backupJSON{
			BackupUUID: b.BackupUUID, ServerUUID: b.ServerUUID, Status: b.Status, Error: publicError(b.Status, b.Error),
			SnapshotID: b.SnapshotID, ReceivedBytes: b.CompressedBytes, LogicalBytes: b.UncompressedBytes, DataAdded: b.DataAdded,
			TotalBytesProcessed: b.TotalBytesProcessed, FileCount: b.FileCount, IndexTruncated: b.IndexTruncated,
			StartedAt: tstr(b.StartedAt), FinishedAt: tstr(b.FinishedAt), DurationMS: b.Duration.Milliseconds(),
		})
	}
	writeJSON(w, 200, map[string]any{"total": total, "limit": limit, "offset": offset, "items": items})
}

func (s *Server) maintenanceJobs(w http.ResponseWriter, r *http.Request) {
	limit, err := intParam(r.URL.Query(), "limit", 50, 500)
	if err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	jobs, err := s.Store.Jobs(r.Context(), limit)
	if err != nil {
		s.internal(w, err)
		return
	}
	items := make([]*jobJSON, 0, len(jobs))
	for _, j := range jobs {
		items = append(items, jobOut(j))
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

// offsiteForgetConfirm releases the stopped forget step of the offsite copy once.
func (s *Server) offsiteForgetConfirm(w http.ResponseWriter, r *http.Request) {
	if s.Offsite == nil {
		writeErr(w, 400, "bad_request", "needs a configured offsite copy (PEB_SECONDARY_REPO)")
		return
	}
	if err := s.Offsite.ConfirmForget(r.Context()); err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"confirmed": true})
}

func (s *Server) maintenanceRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Jobs []string `json:"jobs"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, 400, "bad_request", "body must be JSON like {\"jobs\":[\"prune\"]}")
		return
	}
	want := map[string]bool{}
	for _, j := range body.Jobs {
		switch j {
		case maintenance.JobPrune, maintenance.JobCheck, maintenance.JobStats, maintenance.JobTrashPurge:
		case offsite.JobCopy:
			if s.Offsite == nil {
				writeErr(w, 400, "bad_request", "copy needs a configured offsite copy (PEB_SECONDARY_REPO)")
				return
			}
		default:
			writeErr(w, 400, "bad_request", "jobs may contain prune, check, stats, trash-purge and copy")
			return
		}
		want[j] = true
	}
	var jobs []string
	for _, j := range []string{maintenance.JobTrashPurge, maintenance.JobPrune, maintenance.JobCheck, maintenance.JobStats} { // fixed order
		if want[j] {
			jobs = append(jobs, j)
		}
	}
	if len(jobs) == 0 && !want[offsite.JobCopy] {
		writeErr(w, 400, "bad_request", "jobs must not be empty")
		return
	}
	// Maintenance first: if it is busy nothing was started, so the 409 is true.
	var ids []int64
	if len(jobs) > 0 {
		var err error
		ids, err = s.Maint.Trigger(jobs)
		switch {
		case errors.Is(err, maintenance.ErrBusy):
			writeErr(w, 409, "busy", "a maintenance run is already in progress")
			return
		case err != nil:
			s.internal(w, err)
			return
		}
	}
	if want[offsite.JobCopy] {
		id, err := s.Offsite.TriggerCopy()
		switch {
		case errors.Is(err, offsite.ErrBusy):
			if len(ids) > 0 { // the maintenance jobs did start
				writeJSON(w, 202, map[string]any{"id": ids[0], "job_ids": ids, "copy_skipped": "busy"})
				return
			}
			writeErr(w, 409, "busy", "an offsite copy run is already in progress")
			return
		case err != nil:
			if len(ids) > 0 {
				s.Log.Error("starting the offsite copy failed", "err", err)
				writeJSON(w, 202, map[string]any{"id": ids[0], "job_ids": ids, "copy_skipped": "error"})
				return
			}
			s.internal(w, err)
			return
		}
		ids = append(ids, id)
	}
	writeJSON(w, 202, map[string]any{"id": ids[0], "job_ids": ids})
}
