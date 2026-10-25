package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// BackupCountsByStatus counts all backups per status (including deleted ones).
func (s *Store) BackupCountsByStatus(ctx context.Context) (map[string]int64, error) {
	return s.countByStatus(ctx, `SELECT status, COUNT(*) FROM backups GROUP BY status`)
}

// BackupCountsSince counts the backups started at or after since per status.
func (s *Store) BackupCountsSince(ctx context.Context, since time.Time) (map[string]int64, error) {
	return s.countByStatus(ctx, `SELECT status, COUNT(*) FROM backups WHERE started_at>=? GROUP BY status`, since.UnixMilli())
}

func (s *Store) countByStatus(ctx context.Context, q string, args ...any) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// LastServerBackup is the newest complete backup of one server.
type LastServerBackup struct {
	ServerUUID string
	FinishedAt time.Time
	Duration   time.Duration
	DataAdded  int64
}

// LastBackupPerServer returns the newest complete backup of every server, sorted by UUID.
func (s *Store) LastBackupPerServer(ctx context.Context) ([]LastServerBackup, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT server_uuid, COALESCE(finished_at, started_at), duration_ms, data_added
		FROM backups WHERE id IN (SELECT MAX(id) FROM backups WHERE status='complete' GROUP BY server_uuid)
		ORDER BY server_uuid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LastServerBackup
	for rows.Next() {
		var l LastServerBackup
		var fin, dur int64
		if err := rows.Scan(&l.ServerUUID, &fin, &dur, &l.DataAdded); err != nil {
			return nil, err
		}
		l.FinishedAt, l.Duration = time.UnixMilli(fin), time.Duration(dur)*time.Millisecond
		out = append(out, l)
	}
	return out, rows.Err()
}

// LastOKJob returns the newest successfully finished job of a type, or ErrNotFound.
func (s *Store) LastOKJob(ctx context.Context, typ string) (*Job, error) {
	j, err := scanJob(s.db.QueryRowContext(ctx, `SELECT `+jobCols+` FROM maintenance_jobs
		WHERE type=? AND result='ok' ORDER BY id DESC LIMIT 1`, typ))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return j, err
}
