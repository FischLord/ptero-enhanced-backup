package store

import (
	"bytes"
	"context"
	"database/sql"
)

// DiffEntry is one file of a comparison. For changed files Size/SHA256 are
// those of the newer backup and OldSize/OldSHA256 those of the baseline.
type DiffEntry struct {
	Path      string
	Size      int64
	SHA256    []byte
	OldSize   int64
	OldSHA256 []byte
}

// DiffResult compares the regular files of two backups. A page of each list is
// returned; the counts cover the whole comparison.
type DiffResult struct {
	Added, Removed, Changed []DiffEntry
	AddedCount              int
	RemovedCount            int
	ChangedCount            int
	UnchangedCount          int
}

// diffRow is one regular file of a backup's index.
type diffRow struct {
	path string
	size int64
	sum  []byte
}

// diffCursor streams the regular files of one backup ordered by path. A path
// that occurs several times in the tar is folded into its last row (highest
// rowid), as an extraction would let the last member win; size and hash then
// come from the same row.
type diffCursor struct {
	rows *sql.Rows
	cur  diffRow
	have bool // cur holds a row read ahead
	done bool
}

func (s *Store) openDiffCursor(ctx context.Context, tx *sql.Tx, backupID int64) (*diffCursor, error) {
	rows, err := tx.QueryContext(ctx, `SELECT path, size, sha256 FROM files WHERE backup_id=? AND type='f' ORDER BY path, rowid`, backupID)
	if err != nil {
		return nil, err
	}
	return &diffCursor{rows: rows}, nil
}

func (c *diffCursor) scan() (diffRow, bool, error) {
	if !c.rows.Next() {
		return diffRow{}, false, c.rows.Err()
	}
	var r diffRow
	err := c.rows.Scan(&r.path, &r.size, &r.sum)
	return r, err == nil, err
}

// next returns the next distinct path.
func (c *diffCursor) next() (diffRow, bool, error) {
	if c.done {
		return diffRow{}, false, nil
	}
	if !c.have {
		r, ok, err := c.scan()
		if err != nil || !ok {
			c.done = true
			return diffRow{}, false, err
		}
		c.cur = r
	}
	last := c.cur
	for {
		r, ok, err := c.scan()
		if err != nil {
			c.done = true
			return diffRow{}, false, err
		}
		if !ok {
			c.done, c.have = true, false
			return last, true, nil
		}
		if r.path != last.path {
			c.cur, c.have = r, true
			return last, true, nil
		}
		last = r // same path again: the later member wins
	}
}

func changedRow(n, o diffRow) bool {
	if n.sum != nil && o.sum != nil {
		return !bytes.Equal(n.sum, o.sum)
	}
	return n.size != o.size // one side has no hash: the size decides
}

// DiffFiles compares backup newID against the baseline oldID by path.
// limit/offset page each of the three lists. Both indexes are read once, in
// path order, inside one read transaction (merge join in Go): the counts and
// the lists therefore always belong together, and the cost is two index scans.
func (s *Store) DiffFiles(ctx context.Context, newID, oldID int64, limit, offset int) (*DiffResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	nc, err := s.openDiffCursor(ctx, tx, newID)
	if err != nil {
		return nil, err
	}
	defer nc.rows.Close()
	oc, err := s.openDiffCursor(ctx, tx, oldID)
	if err != nil {
		return nil, err
	}
	defer oc.rows.Close()

	var r DiffResult
	add := func(list *[]DiffEntry, count *int, e DiffEntry) {
		if *count >= offset && len(*list) < limit {
			*list = append(*list, e)
		}
		*count++
	}
	n, nok, err := nc.next()
	if err != nil {
		return nil, err
	}
	o, ook, err := oc.next()
	if err != nil {
		return nil, err
	}
	for nok || ook {
		switch {
		case !ook || (nok && n.path < o.path):
			add(&r.Added, &r.AddedCount, DiffEntry{Path: n.path, Size: n.size, SHA256: n.sum})
			n, nok, err = nc.next()
		case !nok || o.path < n.path:
			add(&r.Removed, &r.RemovedCount, DiffEntry{Path: o.path, Size: o.size, SHA256: o.sum})
			o, ook, err = oc.next()
		default:
			if changedRow(n, o) {
				add(&r.Changed, &r.ChangedCount, DiffEntry{Path: n.path, Size: n.size, SHA256: n.sum, OldSize: o.size, OldSHA256: o.sum})
			} else {
				r.UnchangedCount++
			}
			if n, nok, err = nc.next(); err == nil {
				o, ook, err = oc.next()
			}
		}
		if err != nil {
			return nil, err
		}
	}
	return &r, nil
}
