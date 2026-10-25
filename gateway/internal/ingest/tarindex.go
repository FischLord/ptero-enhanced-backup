package ingest

import (
	"archive/tar"
	"crypto/sha256"
	"errors"
	"io"
	"strings"

	"ptero-enhanced-backup/gateway/internal/store"
)

// indexTar reads a tar stream to the end and returns one entry per member.
// Reading is what drives the stream, so the caller feeds it a tee. Beyond
// max entries (max > 0) members are still read but no longer recorded; the
// bool reports that.
//
// Regular files are hashed (SHA-256 of the content) while their body is read
// anyway; the hash is only recorded once the whole body has been read, so a
// cut-off stream never yields a hash.
func indexTar(r io.Reader, max int) ([]store.FileEntry, bool, error) {
	return indexTarHash(r, max, true)
}

// indexTarHash is indexTar with the content hash switchable (PEB_INDEX_HASH).
func indexTarHash(r io.Reader, max int, hash bool) ([]store.FileEntry, bool, error) {
	tr := tar.NewReader(r)
	hasher := sha256.New()
	buf := make([]byte, 128<<10)
	var out []store.FileEntry
	truncated := false
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, truncated, nil
		}
		if err != nil {
			return out, truncated, err
		}
		if h.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		if max > 0 && len(out) >= max {
			truncated = true
			continue
		}
		e := store.FileEntry{
			Path:  strings.TrimPrefix(h.Name, "./"),
			Type:  entryType(h.Typeflag),
			Size:  h.Size,
			Mode:  h.Mode,
			MTime: h.ModTime,
		}
		// Sparse entries (PAX GNU.sparse.*) are expanded by archive/tar with
		// synthesised zero holes; hashing them would read the logical size
		// instead of the bytes on the wire. Wings never writes sparse entries,
		// so they only get a size, no hash.
		if hash && e.Type == "f" && !isSparse(h) {
			hasher.Reset()
			if _, err := io.CopyBuffer(hasher, tr, buf); err != nil {
				return out, truncated, err
			}
			e.SHA256 = hasher.Sum(nil)
		}
		out = append(out, e)
	}
}

func isSparse(h *tar.Header) bool {
	if h.Typeflag == tar.TypeGNUSparse {
		return true
	}
	for k := range h.PAXRecords {
		if strings.HasPrefix(k, "GNU.sparse.") {
			return true
		}
	}
	return false
}

func entryType(t byte) string {
	switch t {
	case tar.TypeReg, tar.TypeRegA:
		return "f"
	case tar.TypeDir:
		return "d"
	case tar.TypeSymlink:
		return "l"
	}
	return "o"
}
