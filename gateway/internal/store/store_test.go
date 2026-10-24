package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestBackupLifecycle(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()

	id, err := st.CreateUpload(ctx, "up1", "s/b.tar.gz", "s", "b", now)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := st.BackupByKey(ctx, "s/b.tar.gz"); b == nil || b.Status != StatusPending || b.ID != id {
		t.Fatalf("pending: %+v", b)
	}
	files := []FileEntry{{Path: "a/b", Type: "f", Size: 3, Mode: 0o644, MTime: now}}
	err = st.CompleteBackup(ctx, id, Finish{SnapshotID: "abc", DataAdded: 5, FileCount: 1, Duration: time.Second, IndexTruncated: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InsertFiles(ctx, id, files); err != nil {
		t.Fatal(err)
	}
	b, _ := st.BackupByKey(ctx, "s/b.tar.gz")
	if b.Status != StatusComplete || b.SnapshotID != "abc" || b.DataAdded != 5 || b.Duration != time.Second || b.FinishedAt.IsZero() || !b.IndexTruncated {
		t.Fatalf("complete: %+v", b)
	}
	if got, _ := st.Files(ctx, id); len(got) != 1 || got[0].Path != "a/b" || got[0].Size != 3 {
		t.Fatalf("files: %+v", got)
	}
	if err := st.MarkDeleted(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BackupByKey(ctx, "s/b.tar.gz"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted still visible: %v", err)
	}
}

func TestFailStale(t *testing.T) {
	ctx := context.Background()
	st, _ := Open(filepath.Join(t.TempDir(), "db"))
	defer st.Close()
	done, _ := st.CreateUpload(ctx, "u1", "s/a.tar.gz", "s", "a", time.Now())
	st.CompleteBackup(ctx, done, Finish{SnapshotID: "x"}, time.Now())
	st.SetUploadStatus(ctx, "u1", "complete")
	st.CreateUpload(ctx, "u2", "s/b.tar.gz", "s", "b", time.Now())

	n, err := st.FailStale(ctx, time.Now())
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if b, _ := st.BackupByKey(ctx, "s/a.tar.gz"); b.Status != StatusComplete {
		t.Fatalf("finished backup touched: %+v", b)
	}
	if b, _ := st.BackupByKey(ctx, "s/b.tar.gz"); b.Status != StatusFailed {
		t.Fatalf("stale backup: %+v", b)
	}
	if s, _ := st.UploadStatus(ctx, "u1"); s != "complete" {
		t.Fatalf("finished upload status: %s", s)
	}
}

func TestInsertFilesBatches(t *testing.T) {
	ctx := context.Background()
	st, _ := Open(filepath.Join(t.TempDir(), "db"))
	defer st.Close()
	id, _ := st.CreateUpload(ctx, "u", "s/a.tar.gz", "s", "a", time.Now())
	var files []FileEntry
	for i := 0; i < fileBatch*2+17; i++ {
		files = append(files, FileEntry{Path: fmt.Sprint("f", i), Type: "f"})
	}
	if err := st.InsertFiles(ctx, id, files); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Files(ctx, id); len(got) != len(files) {
		t.Fatalf("got %d files", len(got))
	}
}

func TestUploadInfo(t *testing.T) {
	ctx := context.Background()
	st, _ := Open(filepath.Join(t.TempDir(), "db"))
	defer st.Close()
	st.CreateUpload(ctx, "u", "s/a.tar.gz", "s", "a", time.Now())
	st.SetUploadComplete(ctx, "u", "etag-3", "sig")
	i, err := st.UploadInfo(ctx, "u")
	if err != nil || i.Status != "complete" || i.ETag != "etag-3" || i.PartsSig != "sig" || i.Key != "s/a.tar.gz" {
		t.Fatalf("%+v %v", i, err)
	}
	if _, err := st.UploadInfo(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestChildrenSkipsSubtrees(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	id, _ := st.CreateUpload(ctx, "u", "s/b.tar.gz", "s", "b", now)
	var files []FileEntry
	add := func(p, typ string) {
		files = append(files, FileEntry{Path: p, Type: typ, Size: 1, Mode: 0o644, MTime: now})
	}
	// Siblings that sort between the directory entry and its subtree must not be lost.
	add("d-x", "f")
	add("d.c", "f")
	add("d/", "d")
	for i := 0; i < 1000; i++ {
		add(fmt.Sprintf("d/sub%03d/file", i), "f")
	}
	add("d0", "f")
	add("e/deep/er/f", "f")
	if err := st.InsertFiles(ctx, id, files); err != nil {
		t.Fatal(err)
	}
	got, total, err := st.Children(ctx, id, "", 100, 0)
	if err != nil || total != 5 {
		t.Fatalf("%v total=%d %+v", err, total, got)
	}
	var names []string
	for _, e := range got {
		names = append(names, e.Name)
	}
	if fmt.Sprint(names) != "[d e d-x d.c d0]" || !got[0].IsDir || !got[1].IsDir {
		t.Fatalf("root: %v %+v", names, got)
	}
	sub, total, _ := st.Children(ctx, id, "d", 10, 5)
	if total != 1000 || len(sub) != 10 || sub[0].Name != "sub005" {
		t.Fatalf("d: total=%d %+v", total, sub)
	}
}

func TestMigrateAddsTargetColumnsToOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE repo_stats (id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL, total_size INTEGER NOT NULL,
		total_uncompressed_size INTEGER NOT NULL, snapshots_count INTEGER NOT NULL, compression_ratio REAL NOT NULL DEFAULT 0, disk_bytes INTEGER);
		INSERT INTO repo_stats (ts,total_size,total_uncompressed_size,snapshots_count,disk_bytes) VALUES (1,10,20,1,10)`); err != nil {
		t.Fatal(err)
	}
	old.Close()
	for i := 0; i < 2; i++ { // opening twice must be idempotent
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.LatestRepoStat(context.Background())
		if err != nil || (i == 0 && (got.TotalSize != 10 || got.TargetTotal != nil)) {
			t.Fatalf("%+v %v", got, err)
		}
		tt, tf := int64(100), int64(40)
		if err := s.InsertRepoStat(context.Background(), RepoStat{Time: time.Now(), TargetTotal: &tt, TargetFree: &tf}); err != nil {
			t.Fatal(err)
		}
		got, _ = s.LatestRepoStat(context.Background())
		if got.TargetTotal == nil || *got.TargetTotal != 100 || *got.TargetFree != 40 {
			t.Fatalf("%+v", got)
		}
		s.Close()
	}
}
