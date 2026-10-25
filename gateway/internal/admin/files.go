package admin

import (
	"archive/tar"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"ptero-enhanced-backup/gateway/internal/store"
)

// cleanPath normalises a path inside a backup: no leading/trailing slash or "./".
func cleanPath(p string) (string, bool) {
	p = strings.Trim(strings.TrimPrefix(p, "./"), "/")
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", false
		}
	}
	return p, true
}

// backupFor loads the backup of the URL and writes the error response itself.
func (s *Server) backupFor(w http.ResponseWriter, r *http.Request) *store.Backup {
	b, err := s.Store.BackupByUUID(r.Context(), r.PathValue("uuid"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, 404, "not_found", "backup not found")
	case err != nil:
		s.internal(w, err)
	case b.Status != store.StatusComplete:
		writeErr(w, 409, "not_complete", "backup is not complete")
	default:
		return b
	}
	return nil
}

func kind(e store.DirEntry) string {
	switch {
	case e.IsDir:
		return "dir"
	case e.Type == "f":
		return "file"
	case e.Type == "l":
		return "symlink"
	}
	return "other"
}

func (s *Server) files(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	dir, ok := cleanPath(q.Get("path"))
	limit, err1 := intParam(q, "limit", 200, 1000)
	offset, err2 := intParam(q, "offset", 0, 0)
	if err := errors.Join(err1, err2); err != nil || !ok {
		writeErr(w, 400, "bad_request", "invalid path, limit or offset")
		return
	}
	b := s.backupFor(w, r)
	if b == nil {
		return
	}
	entries, total, err := s.Store.Children(r.Context(), b.ID, dir, limit, offset)
	if err != nil {
		s.internal(w, err)
		return
	}
	type item struct {
		Name  string  `json:"name"`
		Path  string  `json:"path"`
		Type  string  `json:"type"`
		Size  int64   `json:"size"`
		Mode  *string `json:"mode"`
		MTime *string `json:"mtime"`
		// SHA256 is the hex content hash of a file, null if unknown.
		SHA256 *string `json:"sha256"`
	}
	items := make([]item, 0, len(entries))
	for _, e := range entries {
		// Directories derived from deeper paths have no tar entry of their own.
		var mt, mode *string
		if e.MTime.Unix() > 0 {
			mt = tstr(e.MTime)
			m := "0" + strconv.FormatInt(e.Mode&0o7777, 8)
			mode = &m
		}
		var sum *string
		if len(e.SHA256) > 0 && e.Type == "f" {
			h := hex.EncodeToString(e.SHA256)
			sum = &h
		}
		items = append(items, item{Name: e.Name, Path: path.Join(dir, e.Name), Type: kind(e), Size: e.Size, Mode: mode, MTime: mt, SHA256: sum})
	}
	writeJSON(w, 200, map[string]any{
		"backup_uuid": b.BackupUUID, "path": dir, "index_truncated": b.IndexTruncated,
		"total": total, "limit": limit, "offset": offset, "items": items,
	})
}

// file streams one regular file out of the backup's tar (restic dump + archive/tar).
//
// The index only guards obvious mistakes (directories, symlinks). A path that is
// missing from the index, or any path of a backup with a truncated index, is
// looked up by scanning the archive. Hard links are served with the content of
// their target. If a name occurs several times in the tar, the first entry wins.
func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	p, ok := cleanPath(r.URL.Query().Get("path"))
	if !ok || p == "" {
		writeErr(w, 400, "bad_request", "path is required")
		return
	}
	b := s.backupFor(w, r)
	if b == nil {
		return
	}
	ctx := r.Context()
	fe, err := s.Store.FileByPath(ctx, b.ID, p)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.internal(w, err)
		return
	}
	if err == nil && (fe.Type == "d" || fe.Type == "l") {
		writeErr(w, 400, "not_a_file", "path is not a regular file")
		return
	}

	release, err := s.Dumps.AcquireDump(ctx)
	if err != nil {
		if ctx.Err() == nil {
			w.Header().Set("Retry-After", "30")
			writeErr(w, 503, "busy", "too many restores are running, retry later")
		}
		return
	}
	defer release()
	timeout := s.DownloadTimeout
	if timeout <= 0 {
		timeout = 2 * time.Hour
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(timeout))

	target := p
	for hops := 0; hops < 5; hops++ { // a hard link may point to a hard link
		rc, err := s.Restic.Dump(ctx, b.SnapshotID, "/"+b.ServerUUID+".tar")
		if err != nil {
			s.internal(w, err)
			return
		}
		tr := tar.NewReader(rc)
		h, err := findEntry(tr, target)
		switch {
		case errors.Is(err, io.EOF):
			rc.Close()
			writeErr(w, 404, "not_found", "file not found in the archive")
			return
		case err != nil:
			rc.Close()
			s.internal(w, err)
			return
		case h.Typeflag == tar.TypeLink:
			rc.Close()
			next, ok := cleanPath(h.Linkname)
			if !ok || next == "" {
				writeErr(w, 404, "not_found", "invalid hard link in the archive")
				return
			}
			target = next
			continue
		}
		defer rc.Close()
		hd := w.Header()
		hd.Set("Content-Type", "application/octet-stream")
		hd.Set("Content-Length", strconv.FormatInt(h.Size, 10))
		hd.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(p)}))
		w.WriteHeader(http.StatusOK)
		if _, err := io.Copy(w, tr); err != nil {
			s.Log.Error("file download failed", "backup", b.BackupUUID, "err", err)
			panic(http.ErrAbortHandler) // truncated, not "complete"
		}
		return
	}
	writeErr(w, 404, "not_found", "too many hard link levels")
}

// findEntry advances tr to the first regular file or hard link called name.
func findEntry(tr *tar.Reader, name string) (*tar.Header, error) {
	for {
		h, err := tr.Next()
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA && h.Typeflag != tar.TypeLink {
			continue
		}
		if n, _ := cleanPath(h.Name); n == name {
			return h, nil
		}
	}
}
