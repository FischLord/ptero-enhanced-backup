// Package store keeps uploads, backups and the file index in SQLite.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	StatusPending  = "pending"
	StatusComplete = "complete"
	StatusFailed   = "failed"
	StatusDeleted  = "deleted"
	// StatusTrashed: deleted in the panel (or its server was), still in the repository until purge_after.
	StatusTrashed = "trashed"
	// StatusPurging: a trashed backup whose snapshot is being forgotten right now (restore is no longer possible).
	StatusPurging = "purging"
)

var ErrNotFound = errors.New("not found")

const schema = `
CREATE TABLE IF NOT EXISTS uploads (
	id          TEXT PRIMARY KEY,
	key         TEXT NOT NULL,
	backup_id   INTEGER NOT NULL,
	status      TEXT NOT NULL,           -- open, complete, aborted, failed
	created_at  INTEGER NOT NULL,
	complete_etag TEXT NOT NULL DEFAULT '',
	parts_sig     TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS parts (
	upload_id TEXT NOT NULL,
	number    INTEGER NOT NULL,
	size      INTEGER NOT NULL,
	etag      TEXT NOT NULL,
	PRIMARY KEY (upload_id, number)
);
CREATE TABLE IF NOT EXISTS backups (
	id                    INTEGER PRIMARY KEY AUTOINCREMENT,
	key                   TEXT NOT NULL,
	server_uuid           TEXT NOT NULL,
	backup_uuid           TEXT NOT NULL,
	upload_id             TEXT NOT NULL,
	snapshot_id           TEXT NOT NULL DEFAULT '',
	status                TEXT NOT NULL,
	compressed_bytes      INTEGER NOT NULL DEFAULT 0,
	uncompressed_bytes    INTEGER NOT NULL DEFAULT 0,
	data_added            INTEGER NOT NULL DEFAULT 0,
	total_bytes_processed INTEGER NOT NULL DEFAULT 0,
	file_count            INTEGER NOT NULL DEFAULT 0,
	index_truncated       INTEGER NOT NULL DEFAULT 0,
	started_at            INTEGER NOT NULL,
	finished_at           INTEGER,
	duration_ms           INTEGER NOT NULL DEFAULT 0,
	error                 TEXT NOT NULL DEFAULT '',
	trashed_at            INTEGER,             -- unix ms, set while status is trashed
	purge_after           INTEGER,             -- unix ms, the nightly window forgets the snapshot after this
	trash_reason          TEXT NOT NULL DEFAULT ''  -- panel_delete, server_deleted
);
CREATE INDEX IF NOT EXISTS backups_key ON backups(key);
CREATE TABLE IF NOT EXISTS files (
	backup_id INTEGER NOT NULL REFERENCES backups(id),
	path      TEXT NOT NULL,
	type      TEXT NOT NULL,
	size      INTEGER NOT NULL,
	mode      INTEGER NOT NULL,
	mtime     INTEGER NOT NULL,
	sha256    BLOB
);
DROP INDEX IF EXISTS files_backup;
CREATE INDEX IF NOT EXISTS files_backup_path ON files(backup_id, path);
CREATE INDEX IF NOT EXISTS files_backup_type_path ON files(backup_id, type, path);
CREATE INDEX IF NOT EXISTS backups_uuid ON backups(backup_uuid);
CREATE TABLE IF NOT EXISTS maintenance_jobs (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	type        TEXT NOT NULL,           -- prune, check, stats, trash-purge, copy, forget-secondary, prune-secondary, check-secondary
	trigger     TEXT NOT NULL,           -- schedule, manual
	started_at  INTEGER NOT NULL,
	finished_at INTEGER,
	result      TEXT NOT NULL,           -- queued, running, ok, failed, skipped
	message     TEXT NOT NULL DEFAULT '',
	bytes_freed INTEGER
);
CREATE TABLE IF NOT EXISTS repo_stats (
	id                      INTEGER PRIMARY KEY AUTOINCREMENT,
	ts                      INTEGER NOT NULL,
	total_size              INTEGER NOT NULL,
	total_uncompressed_size INTEGER NOT NULL,
	snapshots_count         INTEGER NOT NULL,
	compression_ratio       REAL NOT NULL DEFAULT 0,
	disk_bytes              INTEGER,         -- real size on disk, NULL if not measurable
	target_total_bytes      INTEGER,         -- backup target size (statfs), NULL if unknown
	target_free_bytes       INTEGER          -- available space on the target (statfs), NULL if unknown
);
CREATE INDEX IF NOT EXISTS repo_stats_ts ON repo_stats(ts);
CREATE TABLE IF NOT EXISTS offsite_state (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS offsite_copies (
	snapshot_id   TEXT PRIMARY KEY,      -- snapshot ID on the secondary
	original_id   TEXT NOT NULL,         -- snapshot ID in the primary it was copied from
	copied_at     INTEGER NOT NULL,      -- unix ms
	missing_since INTEGER,               -- unix ms, since when the original is gone from the primary (NULL = present)
	forgotten_at  INTEGER                -- unix ms, NULL = still on the secondary
);
CREATE TABLE IF NOT EXISTS settings (
	key        TEXT PRIMARY KEY,
	value      TEXT NOT NULL,            -- JSON
	updated_at INTEGER NOT NULL          -- unix ms
);
CREATE TABLE IF NOT EXISTS emergency_keys (
	key_id     TEXT PRIMARY KEY,         -- restic key ID (never the password)
	label      TEXT NOT NULL,
	created_at INTEGER NOT NULL          -- unix ms
);
CREATE TABLE IF NOT EXISTS deleted_servers (
	server_uuid TEXT PRIMARY KEY,
	deleted_at  INTEGER NOT NULL,        -- unix ms, when the gateway learned that the panel deleted the server
	purge_after INTEGER NOT NULL         -- unix ms, deleted_at + orphan retention
);
CREATE TABLE IF NOT EXISTS server_stats (
	server_uuid TEXT PRIMARY KEY,
	stored_bytes INTEGER NOT NULL,       -- restic raw-data size of this server's snapshots
	measured_at  INTEGER NOT NULL        -- unix ms
);
`

type Store struct{ db *sql.DB }

// Open opens (and migrates) the database at path.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection serialises writers; the workload is tiny.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// migrate adds columns that databases created by older versions lack.
func migrate(db *sql.DB) error {
	have := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM pragma_table_info('repo_stats')`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		have[n] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, col := range []string{"target_total_bytes", "target_free_bytes"} {
		if !have[col] {
			if _, err := db.Exec(`ALTER TABLE repo_stats ADD COLUMN ` + col + ` INTEGER`); err != nil {
				return err
			}
		}
	}
	// files.sha256: additive, old rows keep NULL, older gateways ignore the column.
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('files') WHERE name='sha256'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		// A second gateway process may have added the column in the meantime.
		if _, err := db.Exec(`ALTER TABLE files ADD COLUMN sha256 BLOB`); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}
	return migrateBackups(db)
}

// migrateBackups adds the trash columns to databases created before the trash existed.
func migrateBackups(db *sql.DB) error {
	have := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM pragma_table_info('backups')`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		have[n] = true
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, c := range [][2]string{
		{"trashed_at", "INTEGER"}, {"purge_after", "INTEGER"}, {"trash_reason", "TEXT NOT NULL DEFAULT ''"},
	} {
		if !have[c[0]] {
			if _, err := db.Exec(`ALTER TABLE backups ADD COLUMN ` + c[0] + ` ` + c[1]); err != nil {
				return err
			}
		}
	}
	return nil
}

// nullable maps an empty hash to SQL NULL.
func nullable(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func (s *Store) Close() error { return s.db.Close() }

type Backup struct {
	ID                  int64
	Key                 string
	ServerUUID          string
	BackupUUID          string
	UploadID            string
	SnapshotID          string
	Status              string
	CompressedBytes     int64
	UncompressedBytes   int64
	DataAdded           int64
	TotalBytesProcessed int64
	FileCount           int64
	IndexTruncated      bool
	StartedAt           time.Time
	FinishedAt          time.Time
	Duration            time.Duration
	Error               string
}

type FileEntry struct {
	Path  string
	Type  string // "f" file, "d" directory, "l" symlink, "o" other
	Size  int64
	Mode  int64
	MTime time.Time
	// SHA256 is the content hash of a regular file (32 bytes); nil for other
	// entries, entries of older backups and when hashing is switched off.
	SHA256 []byte
}

// CreateUpload records a new upload together with its pending backup row.
func (s *Store) CreateUpload(ctx context.Context, uploadID, key, serverUUID, backupUUID string, now time.Time) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx,
		`INSERT INTO backups (key, server_uuid, backup_uuid, upload_id, status, started_at) VALUES (?,?,?,?,?,?)`,
		key, serverUUID, backupUUID, uploadID, StatusPending, now.UnixMilli())
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.ExecContext(ctx, `INSERT INTO uploads (id, key, backup_id, status, created_at) VALUES (?,?,?,?,?)`,
		uploadID, key, id, "open", now.UnixMilli()); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// SetUploadComplete marks an upload completed and remembers the answer for retries.
func (s *Store) SetUploadComplete(ctx context.Context, uploadID, etag, sig string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE uploads SET status='complete', complete_etag=?, parts_sig=? WHERE id=?`, etag, sig, uploadID)
	return err
}

type UploadInfo struct {
	Key, Status, ETag, PartsSig string
}

func (s *Store) UploadInfo(ctx context.Context, uploadID string) (*UploadInfo, error) {
	var u UploadInfo
	err := s.db.QueryRowContext(ctx, `SELECT key, status, complete_etag, parts_sig FROM uploads WHERE id=?`, uploadID).
		Scan(&u.Key, &u.Status, &u.ETag, &u.PartsSig)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &u, err
}

func (s *Store) SetUploadStatus(ctx context.Context, uploadID, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE uploads SET status=? WHERE id=?`, status, uploadID)
	return err
}

// UploadStatus returns the upload status ("open", "complete", ...).
func (s *Store) UploadStatus(ctx context.Context, uploadID string) (string, error) {
	var st string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM uploads WHERE id=?`, uploadID).Scan(&st)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return st, err
}

func (s *Store) UpsertPart(ctx context.Context, uploadID string, number int, size int64, etag string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO parts (upload_id, number, size, etag) VALUES (?,?,?,?)
		 ON CONFLICT(upload_id, number) DO UPDATE SET size=excluded.size, etag=excluded.etag`,
		uploadID, number, size, etag)
	return err
}

type Finish struct {
	SnapshotID          string
	CompressedBytes     int64
	UncompressedBytes   int64
	DataAdded           int64
	TotalBytesProcessed int64
	FileCount           int64
	IndexTruncated      bool
	Duration            time.Duration
}

// CompleteBackup marks the backup complete. The file index is stored
// separately (InsertFiles) so that an index problem never costs the snapshot.
func (s *Store) CompleteBackup(ctx context.Context, backupID int64, f Finish, now time.Time) error {
	trunc := 0
	if f.IndexTruncated {
		trunc = 1
	}
	_, err := s.db.ExecContext(ctx, `UPDATE backups SET status=?, snapshot_id=?, compressed_bytes=?, uncompressed_bytes=?,
		data_added=?, total_bytes_processed=?, file_count=?, index_truncated=?, finished_at=?, duration_ms=?, error='' WHERE id=?`,
		StatusComplete, f.SnapshotID, f.CompressedBytes, f.UncompressedBytes, f.DataAdded, f.TotalBytesProcessed,
		f.FileCount, trunc, now.UnixMilli(), f.Duration.Milliseconds(), backupID)
	return err
}

const fileBatch = 5000

// InsertFiles stores the file index in batches so other queries are not blocked for long.
func (s *Store) InsertFiles(ctx context.Context, backupID int64, files []FileEntry) error {
	for len(files) > 0 {
		n := min(fileBatch, len(files))
		if err := s.insertBatch(ctx, backupID, files[:n]); err != nil {
			return err
		}
		files = files[n:]
	}
	return nil
}

func (s *Store) insertBatch(ctx context.Context, backupID int64, files []FileEntry) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO files (backup_id, path, type, size, mode, mtime, sha256) VALUES (?,?,?,?,?,?,?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range files {
		if _, err := stmt.ExecContext(ctx, backupID, e.Path, e.Type, e.Size, e.Mode, e.MTime.Unix(), nullable(e.SHA256)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetIndexTruncated flags a backup whose file index is missing or partial.
func (s *Store) SetIndexTruncated(ctx context.Context, backupID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE backups SET index_truncated=1 WHERE id=?`, backupID)
	return err
}

func (s *Store) FailBackup(ctx context.Context, backupID int64, msg string, dur time.Duration, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE backups SET status=?, error=?, finished_at=?, duration_ms=? WHERE id=?`,
		StatusFailed, msg, now.UnixMilli(), dur.Milliseconds(), backupID)
	return err
}

func (s *Store) MarkDeleted(ctx context.Context, backupID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE backups SET status=? WHERE id=?`, StatusDeleted, backupID)
	return err
}

const backupCols = `id, key, server_uuid, backup_uuid, upload_id, snapshot_id, status, compressed_bytes, uncompressed_bytes,
	data_added, total_bytes_processed, file_count, index_truncated, started_at, finished_at, duration_ms, error`

// BackupByKey returns the newest backup for key that is neither deleted nor
// in the trash (the panel no longer knows trashed backups).
func (s *Store) BackupByKey(ctx context.Context, key string) (*Backup, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+backupCols+` FROM backups WHERE key=? AND status NOT IN (?,?,?) ORDER BY id DESC LIMIT 1`,
		key, StatusDeleted, StatusTrashed, StatusPurging)
	return scanBackup(row)
}

func (s *Store) BackupByID(ctx context.Context, id int64) (*Backup, error) {
	return scanBackup(s.db.QueryRowContext(ctx, `SELECT `+backupCols+` FROM backups WHERE id=?`, id))
}

func scanBackup(row interface{ Scan(...any) error }) (*Backup, error) {
	var b Backup
	var started int64
	var finished sql.NullInt64
	var durMS int64
	var trunc int
	err := row.Scan(&b.ID, &b.Key, &b.ServerUUID, &b.BackupUUID, &b.UploadID, &b.SnapshotID, &b.Status,
		&b.CompressedBytes, &b.UncompressedBytes, &b.DataAdded, &b.TotalBytesProcessed, &b.FileCount, &trunc,
		&started, &finished, &durMS, &b.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	b.IndexTruncated = trunc != 0
	b.StartedAt = time.UnixMilli(started)
	if finished.Valid {
		b.FinishedAt = time.UnixMilli(finished.Int64)
	}
	b.Duration = time.Duration(durMS) * time.Millisecond
	return &b, nil
}

// Files returns the indexed entries of a backup ordered by path.
func (s *Store) Files(ctx context.Context, backupID int64) ([]FileEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path, type, size, mode, mtime, sha256 FROM files WHERE backup_id=? ORDER BY path`, backupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FileEntry
	for rows.Next() {
		var e FileEntry
		var mt int64
		if err := rows.Scan(&e.Path, &e.Type, &e.Size, &e.Mode, &mt, &e.SHA256); err != nil {
			return nil, err
		}
		e.MTime = time.Unix(mt, 0)
		out = append(out, e)
	}
	return out, rows.Err()
}

// PendingBackup identifies a backup whose upload has not finished.
type PendingBackup struct {
	ID                     int64
	ServerUUID, BackupUUID string
}

// PendingBackups lists backups that are still pending (call before FailStale to learn which ones it will fail).
func (s *Store) PendingBackups(ctx context.Context) ([]PendingBackup, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, server_uuid, backup_uuid FROM backups WHERE status=? ORDER BY id`, StatusPending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingBackup
	for rows.Next() {
		var p PendingBackup
		if err := rows.Scan(&p.ID, &p.ServerUUID, &p.BackupUUID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// FailStale marks everything that was still running when the gateway stopped as failed.
func (s *Store) FailStale(ctx context.Context, now time.Time) (int64, error) {
	if _, err := s.db.ExecContext(ctx, `UPDATE uploads SET status='failed' WHERE backup_id IN (SELECT id FROM backups WHERE status='pending')`); err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE backups SET status=?, error='gateway restarted during ingest', finished_at=? WHERE status=?`,
		StatusFailed, now.UnixMilli(), StatusPending)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return n, err
	}
	// A purge that was interrupted by a stop is tried again (forget of a gone snapshot counts as success).
	if _, err := s.db.ExecContext(ctx, `UPDATE backups SET status=? WHERE status=?`, StatusTrashed, StatusPurging); err != nil {
		return n, err
	}
	return n, nil
}
