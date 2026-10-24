package ingest

import (
	"archive/tar"
	"errors"
	"io"
	"strings"

	"ptero-enhanced-backup/gateway/internal/store"
)

// indexTar reads a tar stream to the end and returns one entry per member.
// Reading is what drives the stream, so the caller feeds it a tee. Beyond
// max entries (max > 0) members are still read but no longer recorded; the
// bool reports that.
func indexTar(r io.Reader, max int) ([]store.FileEntry, bool, error) {
	tr := tar.NewReader(r)
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
		out = append(out, store.FileEntry{
			Path:  strings.TrimPrefix(h.Name, "./"),
			Type:  entryType(h.Typeflag),
			Size:  h.Size,
			Mode:  h.Mode,
			MTime: h.ModTime,
		})
	}
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
