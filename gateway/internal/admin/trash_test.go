package admin

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"ptero-enhanced-backup/gateway/internal/notify"
	"ptero-enhanced-backup/gateway/internal/store"
)

const (
	srvA = "11111111-1111-4111-8111-111111111111"
	srvB = "22222222-2222-4222-8222-222222222222"
	bkA1 = "aaaaaaaa-0000-4000-8000-000000000001"
	bkA2 = "aaaaaaaa-0000-4000-8000-000000000002"
	bkB1 = "bbbbbbbb-0000-4000-8000-000000000001"
)

type fakeForget struct {
	mu  sync.Mutex
	ids []string
	err error
	on  func() // runs while the snapshot is being forgotten
}

func (f *fakeForget) Forget(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ids = append(f.ids, id)
	if f.on != nil {
		f.on()
	}
	return f.err
}

type fakeNotifier struct {
	mu   sync.Mutex
	msgs []string
}

func (n *fakeNotifier) Send(ev notify.Event) {
	title, body := notify.Render("de", ev)
	n.mu.Lock()
	n.msgs = append(n.msgs, title+"|"+body)
	n.mu.Unlock()
}

func trashFix(t *testing.T) (*fixture, *fakeForget, *fakeNotifier) {
	t.Helper()
	f := newFixture(t)
	fg, nt := &fakeForget{}, &fakeNotifier{}
	f.srv.Forgetter, f.srv.Notify = fg, nt
	f.srv.OrphanRetention = 30 * 24 * time.Hour
	f.seed(t, srvA, bkA1, base.Add(-48*time.Hour), 1000, 500, 100, time.Minute)
	f.seed(t, srvA, bkA2, base.Add(-24*time.Hour), 1000, 500, 100, time.Minute)
	f.seed(t, srvB, bkB1, base.Add(-24*time.Hour), 2000, 900, 100, time.Minute)
	return f, fg, nt
}

func post(f *fixture, t *testing.T, path, body string) map[string]any {
	t.Helper()
	return decode(t, f.do(t, "POST", path, body, token), 200)
}

func strs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func TestTrashListRestoreAndPurge(t *testing.T) {
	f, fg, _ := trashFix(t)
	ctx := context.Background()
	b, _ := f.st.BackupByUUID(ctx, bkA1)
	if err := f.st.TrashBackup(ctx, b.ID, base, base.Add(7*24*time.Hour), store.ReasonPanelDelete); err != nil {
		t.Fatal(err)
	}
	m := decode(t, f.get(t, "/trash"), 200)
	items := m["items"].([]any)
	if m["total"] != 1.0 || len(items) != 1 {
		t.Fatalf("trash: %v", m)
	}
	it := items[0].(map[string]any)
	if it["backup_uuid"] != bkA1 || it["server_uuid"] != srvA || it["reason"] != "panel_delete" || it["logical_bytes"] != 1000.0 ||
		it["trashed_at"] != "2026-10-24T12:00:00Z" || it["purge_after"] != "2026-10-31T12:00:00Z" {
		t.Fatalf("item: %v", it)
	}
	// Trashed backups are neither in the list nor in the totals; the summary reports the trash.
	if m := decode(t, f.get(t, "/backups"), 200); m["total"] != 2.0 {
		t.Fatalf("backups: %v", m["total"])
	}
	s := decode(t, f.get(t, "/summary"), 200)
	if s["backups_count"] != 2.0 || s["trash"].(map[string]any)["count"] != 1.0 {
		t.Fatalf("summary: %v", s)
	}
	// A trashed backup cannot be browsed.
	if rec := f.get(t, "/backups/"+bkA1+"/files"); rec.Code != 404 {
		t.Fatalf("files of a trashed backup: %d", rec.Code)
	}

	// Restore: complete again and browsable for the admin.
	r := post(f, t, "/trash/"+bkA1+"/restore", "")
	if r["status"] != "complete" {
		t.Fatalf("restore: %v", r)
	}
	if rec := f.get(t, "/backups/"+bkA1+"/files"); rec.Code != 200 {
		t.Fatalf("files after restore: %d %s", rec.Code, rec.Body.String())
	}
	if rec := f.do(t, "POST", "/trash/"+bkA1+"/restore", "", token); rec.Code != 404 {
		t.Fatalf("restore twice: %d", rec.Code)
	}

	// Purge now: forget, then gone from the trash.
	b, _ = f.st.BackupByUUID(ctx, bkA2)
	_ = f.st.TrashBackup(ctx, b.ID, base, base.Add(time.Hour), store.ReasonPanelDelete)
	if rec := f.do(t, "DELETE", "/trash/"+bkA2, "", token); rec.Code != 200 {
		t.Fatalf("purge: %d %s", rec.Code, rec.Body.String())
	}
	if len(fg.ids) != 1 || fg.ids[0] != "snap-"+bkA2 {
		t.Fatalf("forgotten: %v", fg.ids)
	}
	if left, _ := f.st.Trash(ctx); len(left) != 0 {
		t.Fatalf("trash: %v", left)
	}
	if rec := f.do(t, "DELETE", "/trash/"+bkA2, "", token); rec.Code != 404 {
		t.Fatalf("purge twice: %d", rec.Code)
	}
}

func TestTrashPurgeFailureKeepsItem(t *testing.T) {
	f, fg, _ := trashFix(t)
	ctx := context.Background()
	b, _ := f.st.BackupByUUID(ctx, bkA1)
	_ = f.st.TrashBackup(ctx, b.ID, base, base.Add(time.Hour), store.ReasonPanelDelete)
	fg.err = errors.New("boom")
	if rec := f.do(t, "DELETE", "/trash/"+bkA1, "", token); rec.Code != 500 {
		t.Fatalf("status %d", rec.Code)
	}
	if left, _ := f.st.Trash(ctx); len(left) != 1 {
		t.Fatal("must stay in the trash")
	}
}

func TestServerDeletedMovesBackupsToTrash(t *testing.T) {
	f, _, nt := trashFix(t)
	r := post(f, t, "/servers/"+srvA+"/deleted", "")
	if r["trashed"] != 2.0 || r["newly_marked"] != true || r["purge_after"] != "2026-11-23T12:00:00Z" {
		t.Fatalf("deleted: %v", r)
	}
	items, _ := f.st.Trash(context.Background())
	if len(items) != 2 || items[0].Reason != store.ReasonServerDeleted {
		t.Fatalf("trash: %+v", items)
	}
	if len(nt.msgs) != 1 || !strings.Contains(nt.msgs[0], "2 Backups") || !strings.Contains(nt.msgs[0], "30 Tage") {
		t.Fatalf("ntfy: %v", nt.msgs)
	}
	// Idempotent: no second message.
	r = post(f, t, "/servers/"+srvA+"/deleted", "")
	if r["trashed"] != 0.0 || r["newly_marked"] != false || len(nt.msgs) != 1 {
		t.Fatalf("again: %v %v", r, nt.msgs)
	}
	if rec := f.do(t, "POST", "/servers/not-a-uuid/deleted", "", token); rec.Code != 400 {
		t.Fatalf("bad uuid: %d", rec.Code)
	}
}

func TestReconcileMarksMissingServersAndLiftsFalseAlarms(t *testing.T) {
	f, _, nt := trashFix(t)
	ctx := context.Background()

	// The panel only knows server B: A is deleted.
	r := post(f, t, "/servers/reconcile", `{"server_uuids":["`+srvB+`"]}`)
	if got := strs(r["marked_deleted"]); len(got) != 1 || got[0] != srvA || r["trashed"] != 2.0 || r["skipped"] != nil {
		t.Fatalf("reconcile: %v", r)
	}
	if left, _ := f.st.Trash(ctx); len(left) != 2 {
		t.Fatalf("trash: %d", len(left))
	}
	if len(nt.msgs) != 1 {
		t.Fatalf("ntfy: %v", nt.msgs)
	}
	// Same list again: nothing new.
	r = post(f, t, "/servers/reconcile", `{"server_uuids":["`+srvB+`"]}`)
	if len(strs(r["marked_deleted"])) != 0 || len(strs(r["restored"])) != 0 || len(nt.msgs) != 1 {
		t.Fatalf("again: %v", r)
	}
	// False alarm: A shows up again, its backups return.
	r = post(f, t, "/servers/reconcile", `{"server_uuids":["`+strings.ToUpper(srvA)+`","`+srvB+`"]}`)
	if got := strs(r["restored"]); len(got) != 1 || got[0] != srvA {
		t.Fatalf("restore: %v", r)
	}
	if left, _ := f.st.Trash(ctx); len(left) != 0 {
		t.Fatalf("trash after the false alarm: %d", len(left))
	}
	if b, err := f.st.BackupByUUID(ctx, bkA1); err != nil || b.Status != store.StatusComplete {
		t.Fatalf("backup: %+v %v", b, err)
	}
	if d, _ := f.st.DeletedServers(ctx); len(d) != 0 {
		t.Fatalf("still marked: %v", d)
	}
}

func TestReconcileSafetyNets(t *testing.T) {
	f, _, nt := trashFix(t)
	ctx := context.Background()

	// An empty list marks nothing.
	r := post(f, t, "/servers/reconcile", `{"server_uuids":[]}`)
	if r["skipped"] != "empty_list" {
		t.Fatalf("empty: %v", r)
	}
	// A list without any known server (wrong panel, broken query) marks nothing either.
	r = post(f, t, "/servers/reconcile", `{"server_uuids":["33333333-3333-4333-8333-333333333333"]}`)
	if r["skipped"] != "no_overlap" {
		t.Fatalf("no overlap: %v", r)
	}
	if left, _ := f.st.Trash(ctx); len(left) != 0 || len(nt.msgs) != 0 {
		t.Fatalf("trash: %d, ntfy: %v", len(left), nt.msgs)
	}
	for _, body := range []string{`{"server_uuids":["x"]}`, `nope`} {
		if rec := f.do(t, "POST", "/servers/reconcile", body, token); rec.Code != 400 {
			t.Fatalf("%q: %d", body, rec.Code)
		}
	}
}

func TestPurgeBlocksRestoreAndFailureReturnsToTrash(t *testing.T) {
	f, fg, _ := trashFix(t)
	ctx := context.Background()
	b, _ := f.st.BackupByUUID(ctx, bkA1)
	_ = f.st.TrashBackup(ctx, b.ID, base, base.Add(time.Hour), store.ReasonPanelDelete)
	// A restore that arrives while the snapshot is being forgotten is refused; the backup ends deleted.
	fg.on = func() {
		if rec := f.do(t, "POST", "/trash/"+bkA1+"/restore", "", token); rec.Code != 404 {
			t.Errorf("restore while purging: %d", rec.Code)
		}
	}
	if rec := f.do(t, "DELETE", "/trash/"+bkA1, "", token); rec.Code != 200 {
		t.Fatalf("purge: %d", rec.Code)
	}
	if _, err := f.st.BackupByUUID(ctx, bkA1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("still visible: %v", err)
	}
	// A failing forget puts the backup back so that it can be restored.
	b, _ = f.st.BackupByUUID(ctx, bkA2)
	_ = f.st.TrashBackup(ctx, b.ID, base, base.Add(time.Hour), store.ReasonPanelDelete)
	fg.on, fg.err = nil, errors.New("boom")
	if rec := f.do(t, "DELETE", "/trash/"+bkA2, "", token); rec.Code != 500 {
		t.Fatalf("failing purge: %d", rec.Code)
	}
	if rec := f.do(t, "POST", "/trash/"+bkA2+"/restore", "", token); rec.Code != 200 {
		t.Fatalf("restore after the failed purge: %d", rec.Code)
	}
}

func TestReconcileDisabledAndLateBackups(t *testing.T) {
	f, _, _ := trashFix(t)
	ctx := context.Background()
	f.srv.ReconcileOff = true
	if r := post(f, t, "/servers/reconcile", `{"server_uuids":["`+srvB+`"]}`); r["skipped"] != "disabled" {
		t.Fatalf("disabled: %v", r)
	}
	if left, _ := f.st.Trash(ctx); len(left) != 0 {
		t.Fatalf("trash: %d", len(left))
	}
	f.srv.ReconcileOff = false
	post(f, t, "/servers/reconcile", `{"server_uuids":["`+srvB+`"]}`) // A is marked
	// A backup of the marked server that finishes later is trashed again at the next reconcile.
	f.seed(t, srvA, "aaaaaaaa-0000-4000-8000-000000000009", base.Add(time.Hour), 10, 5, 1, time.Minute)
	r := post(f, t, "/servers/reconcile", `{"server_uuids":["`+srvB+`"]}`)
	if r["trashed"] != 1.0 {
		t.Fatalf("late backup: %v", r)
	}
	late, err := f.st.TrashByUUID(ctx, "aaaaaaaa-0000-4000-8000-000000000009")
	if err != nil || !late.PurgeAfter.Equal(base.Add(30*24*time.Hour)) {
		t.Fatalf("late backup: %+v %v", late, err)
	}
}
