package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A database written by a version without files.sha256 must open, read and accept new rows.
func TestMigrateAddsFileHashColumn(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = old.Exec(`CREATE TABLE files (backup_id INTEGER NOT NULL, path TEXT NOT NULL, type TEXT NOT NULL, size INTEGER NOT NULL, mode INTEGER NOT NULL, mtime INTEGER NOT NULL);
		INSERT INTO files VALUES (7, 'old.txt', 'f', 3, 420, 1700000000);`)
	if err != nil {
		t.Fatal(err)
	}
	old.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.Files(ctx, 7)
	if err != nil || len(got) != 1 || got[0].Path != "old.txt" || got[0].SHA256 != nil {
		t.Fatalf("old row: %+v %v", got, err)
	}
	sum := sha256.Sum256([]byte("x"))
	if err := st.InsertFiles(ctx, 7, []FileEntry{{Path: "new.txt", Type: "f", Size: 1, MTime: time.Unix(1, 0), SHA256: sum[:]}}); err != nil {
		t.Fatal(err)
	}
	got, _ = st.Files(ctx, 7)
	if len(got) != 2 || !bytes.Equal(got[0].SHA256, sum[:]) || got[1].SHA256 != nil {
		t.Fatalf("new row: %+v", got)
	}
	// Opening again must be a no-op.
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
}

// Measures the SQLite size of 100000 index entries with and without hash (PEB_MEASURE=1, run with -v).
func TestIndexSizeWithAndWithoutHash(t *testing.T) {
	if os.Getenv("PEB_MEASURE") == "" {
		t.Skip("measurement only; set PEB_MEASURE=1")
	}
	const n = 100000
	size := func(hash bool) int64 {
		path := filepath.Join(t.TempDir(), "db")
		st, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		files := make([]FileEntry, n)
		for i := range files {
			files[i] = FileEntry{Path: fmt.Sprintf("world/region/dir%03d/file%06d.dat", i%500, i), Type: "f", Size: int64(i * 7), Mode: 0o644, MTime: time.Unix(1700000000+int64(i), 0)}
			if hash {
				s := sha256.Sum256([]byte(files[i].Path))
				files[i].SHA256 = s[:]
			}
		}
		if err := st.InsertFiles(context.Background(), 1, files); err != nil {
			t.Fatal(err)
		}
		if _, err := st.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			t.Fatal(err)
		}
		st.Close()
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return fi.Size()
	}
	without, with := size(false), size(true)
	t.Logf("SQLite for %d entries: without hash %d bytes, with hash %d bytes (+%d bytes, %.1f B/entry)",
		n, without, with, with-without, float64(with-without)/n)
	if with <= without {
		t.Fatalf("hash column should cost space")
	}
}
