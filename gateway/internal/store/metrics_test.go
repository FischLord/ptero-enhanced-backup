package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestMetricsQueries(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	mk := func(up, server, backup string, at time.Time, fail bool, dur time.Duration, added int64) {
		id, err := st.CreateUpload(ctx, up, server+"/"+backup, server, backup, at.Add(-dur))
		if err != nil {
			t.Fatal(err)
		}
		if fail {
			err = st.FailBackup(ctx, id, "x", dur, at)
		} else {
			err = st.CompleteBackup(ctx, id, Finish{SnapshotID: "s" + backup, DataAdded: added, Duration: dur}, at)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	mk("u1", "a", "b1", now.Add(-72*time.Hour), false, time.Minute, 1)
	mk("u2", "a", "b2", now.Add(-time.Hour), false, 2*time.Minute, 2)
	mk("u3", "b", "b3", now.Add(-2*time.Hour), true, time.Minute, 0)
	mk("u4", "b", "b4", now.Add(-3*time.Hour), false, 5*time.Second, 9)
	if _, err := st.CreateUpload(ctx, "u5", "c/b5", "c", "b5", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}

	all, err := st.BackupCountsByStatus(ctx)
	if err != nil || all[StatusComplete] != 3 || all[StatusFailed] != 1 || all[StatusPending] != 1 {
		t.Fatalf("%v %v", all, err)
	}
	day, err := st.BackupCountsSince(ctx, now.Add(-24*time.Hour))
	if err != nil || day[StatusComplete] != 2 || day[StatusFailed] != 1 || day[StatusPending] != 1 {
		t.Fatalf("%v %v", day, err)
	}
	last, err := st.LastBackupPerServer(ctx)
	if err != nil || len(last) != 2 || last[0].ServerUUID != "a" || last[0].DataAdded != 2 || last[0].Duration != 2*time.Minute ||
		!last[0].FinishedAt.Equal(now.Add(-time.Hour)) || last[1].ServerUUID != "b" || last[1].DataAdded != 9 {
		t.Fatalf("%+v %v", last, err)
	}

	if _, err := st.LastOKJob(ctx, "prune"); err != ErrNotFound {
		t.Fatalf("no job: %v", err)
	}
	for i, res := range []string{JobOK, JobFailed} {
		id, _ := st.InsertJob(ctx, "prune", "schedule", JobRunning, now)
		if err := st.FinishJob(ctx, id, res, "", nil, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	j, err := st.LastOKJob(ctx, "prune")
	if err != nil || j.Result != JobOK || !j.FinishedAt.Equal(now) {
		t.Fatalf("%+v %v", j, err)
	}
}
