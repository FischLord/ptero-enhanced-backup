package store

import (
	"context"
	"database/sql"
	"time"
)

// OwnCopy is a snapshot on the secondary repository that this gateway copied itself.
type OwnCopy struct {
	SnapshotID   string // ID on the secondary
	OriginalID   string // ID in the primary
	CopiedAt     time.Time
	MissingSince time.Time // zero = the original still exists in the primary
}

func msOrNil(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UnixMilli()
}

func fromMs(n sql.NullInt64) time.Time {
	if n.Valid && n.Int64 > 0 {
		return time.UnixMilli(n.Int64)
	}
	return time.Time{}
}

// OwnCopies returns the copies that are still on the secondary (not forgotten), keyed by secondary snapshot ID.
func (s *Store) OwnCopies(ctx context.Context) (map[string]OwnCopy, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT snapshot_id, original_id, copied_at, missing_since FROM offsite_copies WHERE forgotten_at IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]OwnCopy{}
	for rows.Next() {
		var c OwnCopy
		var copied int64
		var missing sql.NullInt64
		if err := rows.Scan(&c.SnapshotID, &c.OriginalID, &copied, &missing); err != nil {
			return nil, err
		}
		c.CopiedAt, c.MissingSince = time.UnixMilli(copied), fromMs(missing)
		out[c.SnapshotID] = c
	}
	return out, rows.Err()
}

// RecordCopy notes that the gateway copied the primary snapshot originalID to the secondary as snapshotID.
func (s *Store) RecordCopy(ctx context.Context, snapshotID, originalID string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO offsite_copies (snapshot_id, original_id, copied_at) VALUES (?,?,?)`,
		snapshotID, originalID, at.UnixMilli())
	return err
}

// SetMissingSince stores when the original of a copy first went missing in the primary (zero clears it).
func (s *Store) SetMissingSince(ctx context.Context, snapshotID string, t time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE offsite_copies SET missing_since=? WHERE snapshot_id=?`, msOrNil(t), snapshotID)
	return err
}

// MarkForgotten notes that the copies are gone from the secondary.
func (s *Store) MarkForgotten(ctx context.Context, ids []string, at time.Time) error {
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `UPDATE offsite_copies SET forgotten_at=? WHERE snapshot_id=? AND forgotten_at IS NULL`, at.UnixMilli(), id); err != nil {
			return err
		}
	}
	return nil
}

// ResetCopies drops all copy records (the secondary repository was replaced).
func (s *Store) ResetCopies(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM offsite_copies`)
	return err
}

// HasSnapshot reports whether the gateway knows a backup with this primary snapshot ID.
func (s *Store) HasSnapshot(ctx context.Context, snapshotID string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM backups WHERE snapshot_id=? AND snapshot_id<>''`, snapshotID).Scan(&n)
	return n > 0, err
}
