package admin

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"ptero-enhanced-backup/gateway/internal/store"
)

// diffTimeout bounds one comparison: it holds the only database connection.
var diffTimeout = 10 * time.Second

func hexOrNil(b []byte) *string {
	if len(b) == 0 {
		return nil
	}
	h := hex.EncodeToString(b)
	return &h
}

// diff compares the regular files of two complete backups of the same server:
// {uuid} is the newer side, ?against= the baseline.
func (s *Server) diff(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, err1 := intParam(q, "limit", 200, 1000)
	offset, err2 := intParam(q, "offset", 0, 0)
	against := q.Get("against")
	if err := errors.Join(err1, err2); err != nil || against == "" {
		writeErr(w, 400, "bad_request", "against is required; invalid limit or offset")
		return
	}
	b := s.backupFor(w, r)
	if b == nil {
		return
	}
	old, err := s.Store.BackupByUUID(r.Context(), against)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, 404, "not_found", "backup to compare against not found")
		return
	case err != nil:
		s.internal(w, err)
		return
	case old.Status != store.StatusComplete:
		writeErr(w, 409, "not_complete", "backup to compare against is not complete")
		return
	case old.ServerUUID != b.ServerUUID:
		writeErr(w, 400, "different_server", "both backups must belong to the same server")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), diffTimeout)
	defer cancel()
	d, err := s.Store.DiffFiles(ctx, b.ID, old.ID, limit, offset)
	if errors.Is(err, context.DeadlineExceeded) && r.Context().Err() == nil {
		writeErr(w, 503, "diff_timeout", "the comparison took longer than "+diffTimeout.String()+"; try again later or compare smaller backups")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	type item struct {
		Path      string  `json:"path"`
		Size      int64   `json:"size"`
		SHA256    *string `json:"sha256"`
		OldSize   *int64  `json:"old_size,omitempty"`
		OldSHA256 *string `json:"old_sha256,omitempty"`
	}
	conv := func(in []store.DiffEntry, changed bool) []item {
		out := make([]item, 0, len(in))
		for _, e := range in {
			it := item{Path: e.Path, Size: e.Size, SHA256: hexOrNil(e.SHA256)}
			if changed {
				sz := e.OldSize
				it.OldSize, it.OldSHA256 = &sz, hexOrNil(e.OldSHA256)
			}
			out = append(out, it)
		}
		return out
	}
	writeJSON(w, 200, map[string]any{
		"backup_uuid": b.BackupUUID, "against": old.BackupUUID,
		"index_truncated": b.IndexTruncated || old.IndexTruncated,
		"limit":           limit, "offset": offset,
		"added": conv(d.Added, false), "removed": conv(d.Removed, false), "changed": conv(d.Changed, true),
		"added_count": d.AddedCount, "removed_count": d.RemovedCount, "changed_count": d.ChangedCount,
		"unchanged_count": d.UnchangedCount,
	})
}
