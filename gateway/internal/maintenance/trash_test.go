package maintenance

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ptero-enhanced-backup/gateway/internal/store"
)

// trashed seeds a complete backup and moves it into the trash until purgeAfter.
func (f *fixture) trashed(t *testing.T, server, backup string, logical int64, purgeAfter time.Time) {
	t.Helper()
	ctx := context.Background()
	id, err := f.st.CreateUpload(ctx, "up-"+backup, server+"/"+backup+".tar.gz", server, backup, *f.clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.CompleteBackup(ctx, id, store.Finish{SnapshotID: "snap-" + backup, UncompressedBytes: logical}, *f.clock); err != nil {
		t.Fatal(err)
	}
	if err := f.st.TrashBackup(ctx, id, *f.clock, purgeAfter, store.ReasonPanelDelete); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) runJobs(t *testing.T, jobs ...string) {
	t.Helper()
	ids, err := f.svc.begin(jobs, "schedule")
	if err != nil {
		t.Fatal(err)
	}
	f.svc.execute(context.Background(), jobs, ids)
}

func TestTrashPurgeForgetsOnlyDueBackups(t *testing.T) {
	f := newFixture(t)
	f.trashed(t, "s1", "due", 100, f.clock.Add(-time.Minute))
	f.trashed(t, "s1", "later", 200, f.clock.Add(48*time.Hour))
	f.runJobs(t, JobTrashPurge, JobPrune)

	if c := strings.Join(f.be.list(), ","); c != "unlock,forget snap-due,unlock,prune 5%" {
		t.Fatalf("calls (the purge must come before the prune): %s", c)
	}
	jobs := f.jobs(t)
	if jobs[0].Type != JobTrashPurge || jobs[0].Result != store.JobOK || !strings.Contains(jobs[0].Message, "1 backups purged") {
		t.Fatalf("job: %+v", jobs[0])
	}
	left, _ := f.st.Trash(context.Background())
	if len(left) != 1 || left[0].BackupUUID != "later" {
		t.Fatalf("trash: %+v", left)
	}
	// The clock moves on: the second backup is due now.
	*f.clock = f.clock.Add(49 * time.Hour)
	f.runJobs(t, JobTrashPurge)
	if left, _ := f.st.Trash(context.Background()); len(left) != 0 {
		t.Fatalf("trash after expiry: %+v", left)
	}
}

func TestTrashPurgeNothingDueTouchesNoRepository(t *testing.T) {
	f := newFixture(t)
	f.trashed(t, "s1", "later", 200, f.clock.Add(time.Hour))
	f.runJobs(t, JobTrashPurge)
	if c := f.be.list(); len(c) != 0 {
		t.Fatalf("calls: %v", c)
	}
	if j := f.jobs(t)[0]; j.Result != store.JobOK || j.Message != "nothing to purge" {
		t.Fatalf("job: %+v", j)
	}
}

func TestTrashPurgeFailureKeepsBackupInTrash(t *testing.T) {
	f := newFixture(t)
	f.be.forgetErr = map[string]error{"snap-bad": errors.New("boom")}
	f.trashed(t, "s1", "bad", 100, f.clock.Add(-time.Hour))
	f.trashed(t, "s1", "good", 100, f.clock.Add(-time.Hour))
	f.runJobs(t, JobTrashPurge)
	if j := f.jobs(t)[0]; j.Result != store.JobFailed || !strings.Contains(j.Message, "1 purged, 1 failed") {
		t.Fatalf("job: %+v", j)
	}
	left, _ := f.st.Trash(context.Background())
	if len(left) != 1 || left[0].BackupUUID != "bad" {
		t.Fatalf("trash: %+v", left)
	}
	if len(f.notif.msgs) != 1 {
		t.Fatalf("failure notification: %v", f.notif.msgs)
	}
}

func TestTrashWarningOnlyAboveThreshold(t *testing.T) {
	f := newFixture(t)
	*f.clock = f.clock.Add(time.Hour + 30*time.Second) // the nightly window has just started
	f.svc.Cfg.TrashWarnBytes = 10 << 30
	// Due before the next window (02:00 tomorrow), 4 GB + 7 GB.
	f.trashed(t, "s1", "a", 4<<30, f.clock.Add(10*time.Hour))
	f.trashed(t, "s1", "b", 7<<30, f.clock.Add(20*time.Hour))
	f.trashed(t, "s1", "c", 50<<30, f.clock.Add(72*time.Hour)) // not part of the next window
	f.runJobs(t, JobTrashPurge)
	if len(f.notif.msgs) != 1 || !strings.Contains(f.notif.msgs[0], "2 Backups (11.0 GB") {
		t.Fatalf("messages: %v", f.notif.msgs)
	}

	f2 := newFixture(t)
	*f2.clock = f2.clock.Add(time.Hour + 30*time.Second)
	f2.svc.Cfg.TrashWarnBytes = 10 << 30
	f2.trashed(t, "s1", "a", 4<<30, f2.clock.Add(10*time.Hour))
	f2.runJobs(t, JobTrashPurge)
	if len(f2.notif.msgs) != 0 {
		t.Fatalf("below the threshold: %v", f2.notif.msgs)
	}
}

// An admin restores a due backup while the purge waits for the repository: it must survive.
func TestTrashPurgeSparesBackupRestoredWhileWaiting(t *testing.T) {
	f := newFixture(t)
	f.trashed(t, "s1", "saved", 100, f.clock.Add(-time.Hour))
	f.trashed(t, "s1", "gone", 100, f.clock.Add(-time.Hour))
	ctx := context.Background()
	f.be.onUnlock = func() {
		it, err := f.st.TrashByUUID(ctx, "saved")
		if err != nil {
			t.Error(err)
			return
		}
		if err := f.st.RestoreFromTrash(ctx, it.ID); err != nil {
			t.Error(err)
		}
	}
	f.runJobs(t, JobTrashPurge)
	if c := strings.Join(f.be.list(), ","); c != "unlock,forget snap-gone" {
		t.Fatalf("calls: %s", c)
	}
	b, err := f.st.BackupByUUID(ctx, "saved")
	if err != nil || b.Status != store.StatusComplete {
		t.Fatalf("restored backup: %+v %v", b, err)
	}
	if _, err := f.st.BackupByUUID(ctx, "gone"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("purged backup is still visible: %v", err)
	}
}

func TestTrashPurgeFailureReturnsToTrashAndStaysRestorable(t *testing.T) {
	f := newFixture(t)
	f.be.forgetErr = map[string]error{"snap-bad": errors.New("boom")}
	f.trashed(t, "s1", "bad", 100, f.clock.Add(-time.Hour))
	f.runJobs(t, JobTrashPurge)
	ctx := context.Background()
	it, err := f.st.TrashByUUID(ctx, "bad")
	if err != nil {
		t.Fatalf("status must be trashed again: %v", err)
	}
	if err := f.st.RestoreFromTrash(ctx, it.ID); err != nil {
		t.Fatal(err)
	}
}
