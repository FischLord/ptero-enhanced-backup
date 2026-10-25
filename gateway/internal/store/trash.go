package store

import (
	"context"
	"database/sql"
	"strconv"
	"time"
)

// Reasons why a backup is in the trash.
const (
	ReasonPanelDelete   = "panel_delete"
	ReasonServerDeleted = "server_deleted"
)

// TrashItem is a trashed backup.
type TrashItem struct {
	Backup
	TrashedAt  time.Time
	PurgeAfter time.Time
	Reason     string
}

const trashCols = backupCols + `, COALESCE(trashed_at,0), COALESCE(purge_after,0), trash_reason`

func scanTrash(sc interface{ Scan(...any) error }) (*TrashItem, error) {
	var b Backup
	var started, durMS, trashed, purge int64
	var finished sql.NullInt64
	var trunc int
	var reason string
	err := sc.Scan(&b.ID, &b.Key, &b.ServerUUID, &b.BackupUUID, &b.UploadID, &b.SnapshotID, &b.Status,
		&b.CompressedBytes, &b.UncompressedBytes, &b.DataAdded, &b.TotalBytesProcessed, &b.FileCount, &trunc,
		&started, &finished, &durMS, &b.Error, &trashed, &purge, &reason)
	if err != nil {
		return nil, err
	}
	b.IndexTruncated = trunc != 0
	b.StartedAt = time.UnixMilli(started)
	if finished.Valid {
		b.FinishedAt = time.UnixMilli(finished.Int64)
	}
	b.Duration = time.Duration(durMS) * time.Millisecond
	return &TrashItem{Backup: b, TrashedAt: time.UnixMilli(trashed), PurgeAfter: time.UnixMilli(purge), Reason: reason}, nil
}

func (s *Store) trashQuery(ctx context.Context, where string, args ...any) ([]*TrashItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+trashCols+` FROM backups WHERE status=? `+where+` ORDER BY purge_after, id`,
		append([]any{StatusTrashed}, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TrashItem
	for rows.Next() {
		it, err := scanTrash(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// TrashBackup moves a complete backup into the trash until purgeAfter.
func (s *Store) TrashBackup(ctx context.Context, id int64, now, purgeAfter time.Time, reason string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE backups SET status=?, trashed_at=?, purge_after=?, trash_reason=? WHERE id=? AND status=?`,
		StatusTrashed, now.UnixMilli(), purgeAfter.UnixMilli(), reason, id, StatusComplete)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Trash lists the trashed backups, next purge first.
func (s *Store) Trash(ctx context.Context) ([]*TrashItem, error) { return s.trashQuery(ctx, "") }

// DueTrash lists the trashed backups whose purge time has come.
func (s *Store) DueTrash(ctx context.Context, now time.Time) ([]*TrashItem, error) {
	return s.trashQuery(ctx, "AND purge_after<=?", now.UnixMilli())
}

// TrashSoon lists the trashed backups that become due after from and up to until.
func (s *Store) TrashSoon(ctx context.Context, from, until time.Time) ([]*TrashItem, error) {
	return s.trashQuery(ctx, "AND purge_after>? AND purge_after<=?", from.UnixMilli(), until.UnixMilli())
}

// TrashByUUID returns the trashed backup with this uuid, or ErrNotFound.
func (s *Store) TrashByUUID(ctx context.Context, uuid string) (*TrashItem, error) {
	items, err := s.trashQuery(ctx, "AND backup_uuid=?", uuid)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrNotFound
	}
	return items[len(items)-1], nil
}

// RestoreFromTrash makes a trashed backup complete again.
func (s *Store) RestoreFromTrash(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE backups SET status=?, trashed_at=NULL, purge_after=NULL, trash_reason='' WHERE id=? AND status=?`,
		StatusComplete, id, StatusTrashed)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// BeginPurge claims a trashed backup for purging: only if it is still trashed
// and (when due is set) its purge time has come. ErrNotFound means somebody
// else got it first (restored, purged) or it is not due; the caller skips it.
func (s *Store) BeginPurge(ctx context.Context, id int64, due *time.Time) error {
	q, args := `UPDATE backups SET status=? WHERE id=? AND status=?`, []any{StatusPurging, id, StatusTrashed}
	if due != nil {
		q, args = q+` AND purge_after<=?`, append(args, due.UnixMilli())
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// FinishPurge marks a claimed backup as deleted (its snapshot is forgotten).
func (s *Store) FinishPurge(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE backups SET status=? WHERE id=? AND status=?`, StatusDeleted, id, StatusPurging)
	return err
}

// AbortPurge puts a claimed backup back into the trash (the forget failed).
func (s *Store) AbortPurge(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE backups SET status=? WHERE id=? AND status=?`, StatusTrashed, id, StatusPurging)
	return err
}

// TrashTotals counts the trashed backups and sums their logical size.
type TrashTotals struct {
	Count        int64
	LogicalBytes int64
	NextPurge    time.Time // zero if the trash is empty
}

func (s *Store) TrashTotals(ctx context.Context) (TrashTotals, error) {
	var t TrashTotals
	var next sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(uncompressed_bytes),0), MIN(purge_after) FROM backups WHERE status=?`,
		StatusTrashed).Scan(&t.Count, &t.LogicalBytes, &next)
	if next.Valid {
		t.NextPurge = time.UnixMilli(next.Int64)
	}
	return t, err
}

// ---- deleted servers ----

// DeletedServer is a server the panel no longer has.
type DeletedServer struct {
	ServerUUID string
	DeletedAt  time.Time
	PurgeAfter time.Time
}

// MarkServerDeleted records that the panel deleted the server (the first mark
// wins) and moves its complete backups into the trash until deleted_at +
// retention. Backups that are already trashed keep their own date. It returns
// the number of backups moved and whether the server was newly marked.
func (s *Store) MarkServerDeleted(ctx context.Context, uuid string, now time.Time, retention time.Duration) (moved int, fresh bool, d DeletedServer, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, d, err
	}
	defer tx.Rollback()
	d = DeletedServer{ServerUUID: uuid, DeletedAt: now, PurgeAfter: now.Add(retention)}
	res, err := tx.ExecContext(ctx, `INSERT INTO deleted_servers (server_uuid, deleted_at, purge_after) VALUES (?,?,?)
		ON CONFLICT(server_uuid) DO NOTHING`, uuid, now.UnixMilli(), d.PurgeAfter.UnixMilli())
	if err != nil {
		return 0, false, d, err
	}
	n, _ := res.RowsAffected()
	fresh = n > 0
	if !fresh {
		var del, purge int64
		if err = tx.QueryRowContext(ctx, `SELECT deleted_at, purge_after FROM deleted_servers WHERE server_uuid=?`, uuid).Scan(&del, &purge); err != nil {
			return 0, false, d, err
		}
		d.DeletedAt, d.PurgeAfter = time.UnixMilli(del), time.UnixMilli(purge)
	}
	// A server that was marked before only loses backups that finished after the mark (late arrivals);
	// backups an admin restored from the trash stay. Late ones get a fresh retention, never an expired date.
	late := ""
	if !fresh {
		d.PurgeAfter = now.Add(retention)
		late = ` AND COALESCE(finished_at, started_at)>=` + strconv.FormatInt(d.DeletedAt.UnixMilli(), 10)
	}
	res, err = tx.ExecContext(ctx, `UPDATE backups SET status=?, trashed_at=?, purge_after=?, trash_reason=?
		WHERE server_uuid=? AND status=? AND snapshot_id<>''`+late,
		StatusTrashed, now.UnixMilli(), d.PurgeAfter.UnixMilli(), ReasonServerDeleted, uuid, StatusComplete)
	if err != nil {
		return 0, false, d, err
	}
	m, _ := res.RowsAffected()
	return int(m), fresh, d, tx.Commit()
}

// UnmarkServerDeleted lifts the mark (the server is back) and restores the
// backups that were trashed because of it. It reports whether the server was
// marked and how many backups came back.
func (s *Store) UnmarkServerDeleted(ctx context.Context, uuid string) (was bool, restored int, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM deleted_servers WHERE server_uuid=?`, uuid)
	if err != nil {
		return false, 0, err
	}
	n, _ := res.RowsAffected()
	res, err = tx.ExecContext(ctx, `UPDATE backups SET status=?, trashed_at=NULL, purge_after=NULL, trash_reason=''
		WHERE server_uuid=? AND status=? AND trash_reason=?`, StatusComplete, uuid, StatusTrashed, ReasonServerDeleted)
	if err != nil {
		return false, 0, err
	}
	m, _ := res.RowsAffected()
	return n > 0, int(m), tx.Commit()
}

// DeletedServers lists the servers marked as deleted.
func (s *Store) DeletedServers(ctx context.Context) ([]DeletedServer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT server_uuid, deleted_at, purge_after FROM deleted_servers ORDER BY deleted_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeletedServer
	for rows.Next() {
		var d DeletedServer
		var a, p int64
		if err := rows.Scan(&d.ServerUUID, &a, &p); err != nil {
			return nil, err
		}
		d.DeletedAt, d.PurgeAfter = time.UnixMilli(a), time.UnixMilli(p)
		out = append(out, d)
	}
	return out, rows.Err()
}

// ActiveServers lists the servers the gateway holds complete backups for
// (not marked as deleted), sorted by UUID.
func (s *Store) ActiveServers(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT server_uuid FROM backups WHERE status=? AND snapshot_id<>''
		AND server_uuid NOT IN (SELECT server_uuid FROM deleted_servers) ORDER BY server_uuid`, StatusComplete)
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
