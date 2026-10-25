package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func trashFixture(t *testing.T) (*Store, context.Context) {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, context.Background()
}

func seedComplete(t *testing.T, st *Store, server, backup string, logical int64) int64 {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	id, err := st.CreateUpload(ctx, "up-"+backup, server+"/"+backup+".tar.gz", server, backup, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteBackup(ctx, id, Finish{SnapshotID: "snap-" + backup, UncompressedBytes: logical}, now); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestTrashHidesAndRestores(t *testing.T) {
	st, ctx := trashFixture(t)
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	id := seedComplete(t, st, "s1", "b1", 100)
	if err := st.TrashBackup(ctx, id, now, now.Add(time.Hour), ReasonPanelDelete); err != nil {
		t.Fatal(err)
	}
	if err := st.TrashBackup(ctx, id, now, now, ReasonPanelDelete); err != ErrNotFound {
		t.Fatalf("second trash: %v", err)
	}
	if _, err := st.BackupByKey(ctx, "s1/b1.tar.gz"); err != ErrNotFound {
		t.Fatalf("BackupByKey: %v", err)
	}
	if _, err := st.BackupByUUID(ctx, "b1"); err != ErrNotFound {
		t.Fatalf("BackupByUUID: %v", err)
	}
	if list, total, _ := st.ListBackups(ctx, BackupFilter{Limit: 10}); total != 0 || len(list) != 0 {
		t.Fatalf("list: %d", total)
	}
	tt, err := st.TrashTotals(ctx)
	if err != nil || tt.Count != 1 || tt.LogicalBytes != 100 || !tt.NextPurge.Equal(now.Add(time.Hour)) {
		t.Fatalf("totals: %+v %v", tt, err)
	}
	if due, _ := st.DueTrash(ctx, now); len(due) != 0 {
		t.Fatal("not due yet")
	}
	if due, _ := st.DueTrash(ctx, now.Add(time.Hour)); len(due) != 1 {
		t.Fatal("due")
	}
	if err := st.RestoreFromTrash(ctx, id); err != nil {
		t.Fatal(err)
	}
	if b, err := st.BackupByUUID(ctx, "b1"); err != nil || b.Status != StatusComplete {
		t.Fatalf("restored: %+v %v", b, err)
	}
	if err := st.RestoreFromTrash(ctx, id); err != ErrNotFound {
		t.Fatalf("restore twice: %v", err)
	}
}

func TestServerDeletedAndBack(t *testing.T) {
	st, ctx := trashFixture(t)
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	seedComplete(t, st, "s1", "b1", 10)
	seedComplete(t, st, "s1", "b2", 20)
	seedComplete(t, st, "s2", "b3", 30)

	moved, fresh, d, err := st.MarkServerDeleted(ctx, "s1", now, 30*24*time.Hour)
	if err != nil || moved != 2 || !fresh || !d.PurgeAfter.Equal(now.Add(30*24*time.Hour)) {
		t.Fatalf("mark: %d %v %+v %v", moved, fresh, d, err)
	}
	// Marking again keeps the first date.
	moved, fresh, d, err = st.MarkServerDeleted(ctx, "s1", now.Add(48*time.Hour), 30*24*time.Hour)
	if err != nil || moved != 0 || fresh || !d.DeletedAt.Equal(now) {
		t.Fatalf("mark again: %d %v %+v %v", moved, fresh, d, err)
	}
	items, _ := st.Trash(ctx)
	if len(items) != 2 || items[0].Reason != ReasonServerDeleted || !items[0].PurgeAfter.Equal(now.Add(30*24*time.Hour)) {
		t.Fatalf("trash: %+v", items)
	}
	if act, _ := st.ActiveServers(ctx); len(act) != 1 || act[0] != "s2" {
		t.Fatalf("active: %v", act)
	}
	was, n, err := st.UnmarkServerDeleted(ctx, "s1")
	if err != nil || !was || n != 2 {
		t.Fatalf("unmark: %v %d %v", was, n, err)
	}
	if act, _ := st.ActiveServers(ctx); len(act) != 2 {
		t.Fatalf("active after: %v", act)
	}
	if was, n, _ := st.UnmarkServerDeleted(ctx, "s1"); was || n != 0 {
		t.Fatal("unmark twice")
	}
}

func TestMigrateOldBackupsTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a database from before the trash: drop the new columns.
	for _, c := range []string{"trashed_at", "purge_after", "trash_reason"} {
		if _, err := st.db.Exec(`ALTER TABLE backups DROP COLUMN ` + c); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st.Close()
	id := seedComplete(t, st, "s", "b", 1)
	if err := st.TrashBackup(context.Background(), id, time.Now(), time.Now(), ReasonPanelDelete); err != nil {
		t.Fatal(err)
	}
}

func seedAt(t *testing.T, st *Store, server, backup string, at time.Time) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := st.CreateUpload(ctx, "up-"+backup, server+"/"+backup+".tar.gz", server, backup, at)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteBackup(ctx, id, Finish{SnapshotID: "snap-" + backup}, at); err != nil {
		t.Fatal(err)
	}
	return id
}

func seedTrashed(t *testing.T, st *Store, server, backup string, purgeAfter time.Time) int64 {
	t.Helper()
	id := seedAt(t, st, server, backup, time.Now().Add(-72*time.Hour))
	if err := st.TrashBackup(context.Background(), id, time.Now().Add(-2*time.Hour), purgeAfter, ReasonPanelDelete); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPurgeClaimIsExclusive(t *testing.T) {
	st, ctx := trashFixture(t)
	id := seedTrashed(t, st, "s1", "b1", time.Now().Add(-time.Hour))
	due := time.Now()
	if err := st.BeginPurge(ctx, id, &due); err != nil {
		t.Fatal(err)
	}
	if err := st.BeginPurge(ctx, id, &due); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second claim: %v", err)
	}
	if err := st.RestoreFromTrash(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("restore while purging: %v", err)
	}
	if b, err := st.BackupByUUID(ctx, "b1"); err == nil {
		t.Fatalf("purging backup visible: %+v", b)
	}
	if err := st.AbortPurge(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := st.RestoreFromTrash(ctx, id); err != nil {
		t.Fatalf("restore after abort: %v", err)
	}
	// Not due: no claim.
	id2 := seedTrashed(t, st, "s1", "b2", time.Now().Add(time.Hour))
	if err := st.BeginPurge(ctx, id2, &due); !errors.Is(err, ErrNotFound) {
		t.Fatalf("claim before the date: %v", err)
	}
}

func TestPurgingResetAtStartup(t *testing.T) {
	st, ctx := trashFixture(t)
	id := seedTrashed(t, st, "s1", "b1", time.Now().Add(-time.Hour))
	if err := st.BeginPurge(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FailStale(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.TrashByUUID(ctx, "b1"); err != nil {
		t.Fatalf("must be trashed again: %v", err)
	}
}

func TestLateBackupOfMarkedServerIsTrashedAgain(t *testing.T) {
	st, ctx := trashFixture(t)
	t0 := time.Now().Add(-48 * time.Hour).Truncate(time.Millisecond)
	seedAt(t, st, "srv", "old", t0.Add(-time.Hour))
	if _, fresh, _, err := st.MarkServerDeleted(ctx, "srv", t0, time.Hour); err != nil || !fresh {
		t.Fatal(fresh, err)
	}
	// An admin restores the old backup by hand: it stays restored.
	it, _ := st.TrashByUUID(ctx, "old")
	if err := st.RestoreFromTrash(ctx, it.ID); err != nil {
		t.Fatal(err)
	}
	if n, _, _, err := st.MarkServerDeleted(ctx, "srv", time.Now(), time.Hour); err != nil || n != 0 {
		t.Fatalf("restored backup trashed again: %d %v", n, err)
	}
	// A backup that finished after the mark is trashed with a fresh date, not the expired one.
	seedAt(t, st, "srv", "late", t0.Add(time.Hour))
	now := time.Now()
	n, fresh, _, err := st.MarkServerDeleted(ctx, "srv", now, 10*time.Hour)
	if err != nil || n != 1 || fresh {
		t.Fatalf("late: %d %v %v", n, fresh, err)
	}
	late, err := st.TrashByUUID(ctx, "late")
	if err != nil || !late.PurgeAfter.After(now.Add(9*time.Hour)) {
		t.Fatalf("late backup: %+v %v", late, err)
	}
}
