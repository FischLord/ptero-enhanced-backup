package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"ptero-enhanced-backup/gateway/internal/secrets"
)

// ---- maintenance jobs and repository stats ----

const (
	JobQueued  = "queued"
	JobRunning = "running"
	JobOK      = "ok"
	JobFailed  = "failed"
	JobSkipped = "skipped"
)

type Job struct {
	ID         int64
	Type       string
	Trigger    string
	StartedAt  time.Time
	FinishedAt time.Time // zero while not finished
	Result     string
	Message    string
	BytesFreed *int64
}

// InsertJobs creates one row per type in a single transaction and returns the ids in order.
func (s *Store) InsertJobs(ctx context.Context, types []string, trigger, result string, now time.Time) ([]int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ids := make([]int64, 0, len(types))
	for _, typ := range types {
		res, err := tx.ExecContext(ctx, `INSERT INTO maintenance_jobs (type, trigger, started_at, result) VALUES (?,?,?,?)`,
			typ, trigger, now.UnixMilli(), result)
		if err != nil {
			return nil, err
		}
		id, _ := res.LastInsertId()
		ids = append(ids, id)
	}
	return ids, tx.Commit()
}

func (s *Store) InsertJob(ctx context.Context, typ, trigger, result string, now time.Time) (int64, error) {
	ids, err := s.InsertJobs(ctx, []string{typ}, trigger, result, now)
	if err != nil {
		return 0, err
	}
	return ids[0], nil
}

// StartJob marks a queued job as running now.
func (s *Store) StartJob(ctx context.Context, id int64, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE maintenance_jobs SET result=?, started_at=? WHERE id=?`, JobRunning, now.UnixMilli(), id)
	return err
}

func (s *Store) FinishJob(ctx context.Context, id int64, result, msg string, freed *int64, now time.Time) error {
	msg = secrets.Scrub(msg) // job messages are shown in the admin API and the panel
	_, err := s.db.ExecContext(ctx, `UPDATE maintenance_jobs SET result=?, message=?, bytes_freed=?, finished_at=? WHERE id=?`,
		result, msg, freed, now.UnixMilli(), id)
	return err
}

const jobCols = `id, type, trigger, started_at, finished_at, result, message, bytes_freed`

func scanJob(sc interface{ Scan(...any) error }) (*Job, error) {
	var j Job
	var started int64
	var fin, freed sql.NullInt64
	if err := sc.Scan(&j.ID, &j.Type, &j.Trigger, &started, &fin, &j.Result, &j.Message, &freed); err != nil {
		return nil, err
	}
	j.StartedAt = time.UnixMilli(started)
	if fin.Valid {
		j.FinishedAt = time.UnixMilli(fin.Int64)
	}
	if freed.Valid {
		j.BytesFreed = &freed.Int64
	}
	return &j, nil
}

func (s *Store) Jobs(ctx context.Context, limit int) ([]*Job, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+jobCols+` FROM maintenance_jobs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// LastFinishedJob returns the newest finished job of a type (ok, failed and, if
// withSkipped, skipped), or ErrNotFound.
func (s *Store) LastFinishedJob(ctx context.Context, typ string, withSkipped bool) (*Job, error) {
	results := "'ok','failed'"
	if withSkipped {
		results += ",'skipped'"
	}
	j, err := scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobCols+` FROM maintenance_jobs
		WHERE type=? AND result IN (`+results+`) ORDER BY id DESC LIMIT 1`, typ))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return j, err
}

// ConsecutiveSkipped counts the newest finished jobs of a type that were skipped
// with exactly this message, uninterrupted (stops at the first job with any
// other outcome). Counts at most 100.
func (s *Store) ConsecutiveSkipped(ctx context.Context, typ, message string) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT result, message FROM maintenance_jobs
		WHERE type=? AND result IN ('ok','failed','skipped') ORDER BY id DESC LIMIT 100`, typ)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var res, msg string
		if err := rows.Scan(&res, &msg); err != nil {
			return 0, err
		}
		if res != JobSkipped || msg != message {
			break
		}
		n++
	}
	return n, rows.Err()
}

// FailStaleJobs marks jobs left over from a previous run as failed.
func (s *Store) FailStaleJobs(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE maintenance_jobs SET result=?, message='gateway restarted', finished_at=?
		WHERE result IN ('queued','running')`, JobFailed, now.UnixMilli())
	return err
}

type RepoStat struct {
	Time                  time.Time
	TotalSize             int64
	TotalUncompressedSize int64
	SnapshotsCount        int64
	CompressionRatio      float64
	DiskBytes             *int64 // real size on disk (local repositories only)
	TargetTotal           *int64 // size of the backup target filesystem (statfs), nil if unknown
	TargetFree            *int64 // space available on the target, nil if unknown
}

// Stored is the best available repository size: on disk if measured, else referenced data.
func (r RepoStat) Stored() int64 {
	if r.DiskBytes != nil {
		return *r.DiskBytes
	}
	return r.TotalSize
}

func (s *Store) InsertRepoStat(ctx context.Context, r RepoStat) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO repo_stats (ts, total_size, total_uncompressed_size, snapshots_count, compression_ratio, disk_bytes, target_total_bytes, target_free_bytes)
		VALUES (?,?,?,?,?,?,?,?)`, r.Time.UnixMilli(), r.TotalSize, r.TotalUncompressedSize, r.SnapshotsCount, r.CompressionRatio, r.DiskBytes, r.TargetTotal, r.TargetFree)
	return err
}

func scanStat(sc interface{ Scan(...any) error }) (*RepoStat, error) {
	var r RepoStat
	var ts int64
	var disk, tt, tf sql.NullInt64
	if err := sc.Scan(&ts, &r.TotalSize, &r.TotalUncompressedSize, &r.SnapshotsCount, &r.CompressionRatio, &disk, &tt, &tf); err != nil {
		return nil, err
	}
	if disk.Valid {
		r.DiskBytes = &disk.Int64
	}
	if tt.Valid && tf.Valid {
		r.TargetTotal, r.TargetFree = &tt.Int64, &tf.Int64
	}
	r.Time = time.UnixMilli(ts)
	return &r, nil
}

const statCols = `ts, total_size, total_uncompressed_size, snapshots_count, compression_ratio, disk_bytes, target_total_bytes, target_free_bytes`

// LatestRepoStat returns the newest snapshot, or ErrNotFound.
func (s *Store) LatestRepoStat(ctx context.Context) (*RepoStat, error) {
	r, err := scanStat(s.db.QueryRowContext(ctx, `SELECT `+statCols+` FROM repo_stats ORDER BY id DESC LIMIT 1`))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// RepoStatsSince returns the snapshots from since on, oldest first.
func (s *Store) RepoStatsSince(ctx context.Context, since time.Time) ([]RepoStat, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+statCols+` FROM repo_stats WHERE ts>=? ORDER BY id`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RepoStat
	for rows.Next() {
		r, err := scanStat(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// RepoStatBefore returns the newest snapshot older than t, or ErrNotFound.
func (s *Store) RepoStatBefore(ctx context.Context, t time.Time) (*RepoStat, error) {
	r, err := scanStat(s.db.QueryRowContext(ctx, `SELECT `+statCols+` FROM repo_stats WHERE ts<? ORDER BY id DESC LIMIT 1`, t.UnixMilli()))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return r, err
}

// ---- backup queries ----

// BackupByUUID returns the newest backup with this uuid that is neither deleted nor trashed.
func (s *Store) BackupByUUID(ctx context.Context, uuid string) (*Backup, error) {
	return scanBackup(s.db.QueryRowContext(ctx, `SELECT `+backupCols+` FROM backups WHERE backup_uuid=? AND status NOT IN (?,?,?) ORDER BY id DESC LIMIT 1`,
		uuid, StatusDeleted, StatusTrashed, StatusPurging))
}

type BackupFilter struct {
	Server, Status string
	Limit, Offset  int
}

// ListBackups returns backups newest first (deleted ones only if asked for) and the total count.
func (s *Store) ListBackups(ctx context.Context, f BackupFilter) ([]*Backup, int, error) {
	where, args := []string{"1=1"}, []any{}
	if f.Server != "" {
		where, args = append(where, "server_uuid=?"), append(args, f.Server)
	}
	if f.Status != "" {
		where, args = append(where, "status=?"), append(args, f.Status)
	} else {
		where, args = append(where, "status NOT IN (?,?,?)"), append(args, StatusDeleted, StatusTrashed, StatusPurging)
	}
	cond := strings.Join(where, " AND ")
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM backups WHERE `+cond, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+backupCols+` FROM backups WHERE `+cond+` ORDER BY id DESC LIMIT ? OFFSET ?`,
		append(args, f.Limit, f.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []*Backup
	for rows.Next() {
		b, err := scanBackup(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, b)
	}
	return out, total, rows.Err()
}

// BackupsSince returns completed and failed (not aborted) backups finished at or after since, oldest first.
func (s *Store) BackupsSince(ctx context.Context, since time.Time) ([]*Backup, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+backupCols+` FROM backups
		WHERE status IN ('complete','failed') AND error<>'aborted' AND COALESCE(finished_at, started_at)>=? ORDER BY id`, since.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Backup
	for rows.Next() {
		b, err := scanBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// Totals aggregates the completed, non-deleted backups.
type Totals struct {
	Count          int64
	LogicalBytes   int64 // sum of uncompressed sizes
	ReceivedBytes  int64 // sum of received (compressed) sizes
	DataAdded      int64
	LastBackup     time.Time
	OK30, Failed30 int64 // finished in the last 30 days (aborted ones excluded)
}

func (s *Store) Totals(ctx context.Context, now time.Time) (Totals, error) {
	var t Totals
	var last sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(uncompressed_bytes),0), COALESCE(SUM(compressed_bytes),0),
		COALESCE(SUM(data_added),0), MAX(finished_at) FROM backups WHERE status='complete'`).
		Scan(&t.Count, &t.LogicalBytes, &t.ReceivedBytes, &t.DataAdded, &last)
	if err != nil {
		return t, err
	}
	if last.Valid {
		t.LastBackup = time.UnixMilli(last.Int64)
	}
	since := now.AddDate(0, 0, -30).UnixMilli()
	err = s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(status='complete'),0), COALESCE(SUM(status='failed'),0) FROM backups
		WHERE status IN ('complete','failed') AND error<>'aborted' AND COALESCE(finished_at, started_at)>=?`, since).
		Scan(&t.OK30, &t.Failed30)
	return t, err
}

// ServerRows returns the finished (complete or failed, not aborted) backups of all
// servers, oldest first, for per-server aggregation.
func (s *Store) ServerRows(ctx context.Context) ([]*Backup, error) {
	return s.BackupsSince(ctx, time.Time{})
}

// ---- file browsing ----

type DirEntry struct {
	Name  string
	IsDir bool
	Type  string // "f", "d", "l", "o"
	Size  int64
	Mode  int64
	MTime time.Time
	// SHA256 of a regular file, nil if unknown (see FileEntry.SHA256).
	SHA256 []byte
}

// Children lists the immediate children of dir ("" = root) derived from the
// indexed paths, directories first. total is the number of children.
//
// It skips from child to child with one index seek each instead of reading
// every descendant, so a huge subtree costs one lookup. The listing of a single
// directory is capped at maxChildren entries.
func (s *Store) Children(ctx context.Context, backupID int64, dir string, limit, offset int) ([]DirEntry, int, error) {
	const maxChildren = 200000
	prefix, hi := "", ""
	if dir != "" {
		prefix, hi = dir+"/", dir+"0" // '0' is the byte after '/'
	}
	byName := map[string]*DirEntry{}
	start := prefix // inclusive lower bound
	for len(byName) < maxChildren {
		q, args := `SELECT path, type, size, mode, mtime, sha256 FROM files WHERE backup_id=? AND path >= ?`, []any{backupID, start}
		if hi != "" {
			q, args = q+` AND path < ?`, append(args, hi)
		}
		var path, typ string
		var size, mode, mt int64
		var sum []byte
		err := s.db.QueryRowContext(ctx, q+` ORDER BY path LIMIT 1`, args...).Scan(&path, &typ, &size, &mode, &mt, &sum)
		if errors.Is(err, sql.ErrNoRows) {
			break
		}
		if err != nil {
			return nil, 0, err
		}
		rest := strings.TrimPrefix(path, prefix)
		name, tail, nested := strings.Cut(rest, "/")
		if name == "" || name == "." { // the directory itself or the archive root
			start = path + "\x00"
			continue
		}
		e := byName[name]
		if e == nil {
			e = &DirEntry{Name: name, Type: "d"}
			byName[name] = e
		}
		switch {
		case nested && tail == "": // explicit directory entry "name/"
			e.IsDir, e.Mode, e.MTime = true, mode, time.Unix(mt, 0)
		case nested: // implied by a deeper path
			e.IsDir = true
		default:
			e.Type, e.Size, e.Mode, e.MTime, e.SHA256 = typ, size, mode, time.Unix(mt, 0), sum
			e.IsDir = typ == "d"
		}
		if nested {
			start = prefix + name + "0" // jump over the whole subtree
		} else {
			start = path + "\x00"
		}
	}
	all := make([]DirEntry, 0, len(byName))
	for _, e := range byName {
		if e.IsDir {
			e.Type = "d"
		}
		all = append(all, *e)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].IsDir != all[j].IsDir {
			return all[i].IsDir
		}
		return all[i].Name < all[j].Name
	})
	total := len(all)
	if offset >= total {
		return nil, total, nil
	}
	return all[offset:min(total, offset+limit)], total, nil
}

// FileByPath returns one indexed entry (directory entries may be stored with a trailing slash).
func (s *Store) FileByPath(ctx context.Context, backupID int64, path string) (*FileEntry, error) {
	var e FileEntry
	var mt int64
	err := s.db.QueryRowContext(ctx, `SELECT path, type, size, mode, mtime, sha256 FROM files WHERE backup_id=? AND path IN (?, ?) LIMIT 1`,
		backupID, path, path+"/").Scan(&e.Path, &e.Type, &e.Size, &e.Mode, &mt, &e.SHA256)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	e.MTime = time.Unix(mt, 0)
	return &e, err
}

// ServerStat is the measured repository space of one server's snapshots.
type ServerStat struct {
	StoredBytes int64
	MeasuredAt  time.Time
}

// ServersWithSnapshots lists the servers that have at least one complete,
// non-deleted backup with a snapshot, sorted by UUID.
func (s *Store) ServersWithSnapshots(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT server_uuid FROM backups WHERE status=? AND snapshot_id<>'' ORDER BY server_uuid`, StatusComplete)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// SetServerStat stores (or replaces) the measurement of one server.
func (s *Store) SetServerStat(ctx context.Context, serverUUID string, bytes int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO server_stats (server_uuid, stored_bytes, measured_at) VALUES (?,?,?)
		ON CONFLICT(server_uuid) DO UPDATE SET stored_bytes=excluded.stored_bytes, measured_at=excluded.measured_at`,
		serverUUID, bytes, at.UnixMilli())
	return err
}

// ServerStats returns the stored measurements by server UUID.
func (s *Store) ServerStats(ctx context.Context) (map[string]ServerStat, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT server_uuid, stored_bytes, measured_at FROM server_stats`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ServerStat{}
	for rows.Next() {
		var u string
		var st ServerStat
		var ts int64
		if err := rows.Scan(&u, &st.StoredBytes, &ts); err != nil {
			return nil, err
		}
		st.MeasuredAt = time.UnixMilli(ts)
		out[u] = st
	}
	return out, rows.Err()
}

// DeleteServerStatsExcept drops measurements of servers not in keep (their backups are gone).
func (s *Store) DeleteServerStatsExcept(ctx context.Context, keep []string) error {
	have, err := s.ServerStats(ctx)
	if err != nil {
		return err
	}
	k := make(map[string]bool, len(keep))
	for _, u := range keep {
		k[u] = true
	}
	for u := range have {
		if !k[u] {
			if _, err := s.db.ExecContext(ctx, `DELETE FROM server_stats WHERE server_uuid=?`, u); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---- offsite copy state ----

// SetState stores a small key/value pair of the offsite copy (an empty value clears it).
func (s *Store) SetState(ctx context.Context, key, value string) error {
	if value == "" {
		_, err := s.db.ExecContext(ctx, `DELETE FROM offsite_state WHERE key=?`, key)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO offsite_state (key, value) VALUES (?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// State returns the stored value, or "" if there is none.
func (s *Store) State(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM offsite_state WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}
