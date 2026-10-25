package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"ptero-enhanced-backup/gateway/internal/store"
)

func hsum(s string) []byte { h := sha256.Sum256([]byte(s)); return h[:] }

func TestFilesShowSHA256(t *testing.T) {
	f := newFixture(t)
	id := f.seed(t, "srv1", "bk1", base, 1, 1, 1, time.Second)
	f.st.InsertFiles(context.Background(), id, []store.FileEntry{
		{Path: "a.txt", Type: "f", Size: 1, MTime: base, SHA256: hsum("a")},
		{Path: "old.txt", Type: "f", Size: 1, MTime: base},
	})
	m := decode(t, f.get(t, "/backups/bk1/files"), 200)
	got := map[string]any{}
	for _, it := range m["items"].([]any) {
		x := it.(map[string]any)
		got[x["name"].(string)] = x["sha256"]
	}
	if got["a.txt"] != hex.EncodeToString(hsum("a")) {
		t.Fatalf("a.txt: %v", got["a.txt"])
	}
	if v, ok := got["old.txt"]; !ok || v != nil {
		t.Fatalf("old.txt must have sha256 null, got %v (present %v)", v, ok)
	}
}

func TestDiff(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	oldID := f.seed(t, "srv1", "bk-old", base.Add(-time.Hour), 1, 1, 1, time.Second)
	newID := f.seed(t, "srv1", "bk-new", base, 1, 1, 1, time.Second)
	other := f.seed(t, "srv2", "bk-other", base, 1, 1, 1, time.Second)
	f.seedFailed(t, "srv1", "bk-bad", "boom", base)
	f.st.InsertFiles(ctx, oldID, []store.FileEntry{
		{Path: "same", Type: "f", Size: 1, SHA256: hsum("same")},
		{Path: "mod", Type: "f", Size: 1, SHA256: hsum("v1")},
		{Path: "gone", Type: "f", Size: 5, SHA256: hsum("gone")},
		{Path: "nohash-same", Type: "f", Size: 4},
		{Path: "nohash-grown", Type: "f", Size: 4},
		{Path: "d/", Type: "d"},
	})
	f.st.InsertFiles(ctx, newID, []store.FileEntry{
		{Path: "same", Type: "f", Size: 1, SHA256: hsum("same")},
		{Path: "mod", Type: "f", Size: 1, SHA256: hsum("v2")}, // same size, other content
		{Path: "fresh", Type: "f", Size: 9, SHA256: hsum("fresh")},
		{Path: "nohash-same", Type: "f", Size: 4, SHA256: hsum("n")},
		{Path: "nohash-grown", Type: "f", Size: 8, SHA256: hsum("m")},
		{Path: "d/", Type: "d"},
	})
	f.st.InsertFiles(ctx, other, []store.FileEntry{{Path: "x", Type: "f"}})

	m := decode(t, f.get(t, "/backups/bk-new/diff?against=bk-old"), 200)
	paths := func(k string) []string {
		var out []string
		for _, it := range m[k].([]any) {
			out = append(out, it.(map[string]any)["path"].(string))
		}
		return out
	}
	eq := func(got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("got %v want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("got %v want %v", got, want)
			}
		}
	}
	eq(paths("added"), "fresh")
	eq(paths("removed"), "gone")
	eq(paths("changed"), "mod", "nohash-grown")
	if num(t, m, "unchanged_count") != 2 || num(t, m, "changed_count") != 2 || num(t, m, "added_count") != 1 || num(t, m, "removed_count") != 1 {
		t.Fatalf("counts: %v", m)
	}
	ch := m["changed"].([]any)[0].(map[string]any)
	if ch["sha256"] != hex.EncodeToString(hsum("v2")) || ch["old_sha256"] != hex.EncodeToString(hsum("v1")) {
		t.Fatalf("changed hashes: %v", ch)
	}

	// Reverse direction swaps added and removed.
	m = decode(t, f.get(t, "/backups/bk-old/diff?against=bk-new"), 200)
	eq(paths("added"), "gone")
	eq(paths("removed"), "fresh")

	// Paging applies per list; counts cover everything.
	m = decode(t, f.get(t, "/backups/bk-new/diff?against=bk-old&limit=1&offset=1"), 200)
	eq(paths("changed"), "nohash-grown")
	if num(t, m, "changed_count") != 2 {
		t.Fatalf("count must cover all: %v", m)
	}

	decode(t, f.get(t, "/backups/bk-new/diff"), 400)
	decode(t, f.get(t, "/backups/bk-new/diff?against=bk-other"), 400)
	decode(t, f.get(t, "/backups/bk-new/diff?against=nope"), 404)
	decode(t, f.get(t, "/backups/nope/diff?against=bk-old"), 404)
	decode(t, f.get(t, "/backups/bk-new/diff?against=bk-bad"), 409)
	decode(t, f.get(t, "/backups/bk-new/diff?against=bk-old&limit=x"), 400)
}

func TestDiffTimeoutAndDuplicatePaths(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	oldID := f.seed(t, "srv1", "bk-o", base.Add(-time.Hour), 1, 1, 1, time.Second)
	newID := f.seed(t, "srv1", "bk-n", base, 1, 1, 1, time.Second)
	f.st.InsertFiles(ctx, oldID, []store.FileEntry{{Path: "dup", Type: "f", Size: 1, SHA256: hsum("b")}})
	// the last member wins: same content as the baseline -> unchanged
	f.st.InsertFiles(ctx, newID, []store.FileEntry{
		{Path: "dup", Type: "f", Size: 1, SHA256: hsum("a")},
		{Path: "dup", Type: "f", Size: 1, SHA256: hsum("b")},
	})
	d, err := f.st.DiffFiles(ctx, newID, oldID, 10, 0)
	if err != nil || d.UnchangedCount != 1 || d.ChangedCount != 0 || d.AddedCount != 0 {
		t.Fatalf("%+v %v", d, err)
	}
	old := diffTimeout
	diffTimeout = time.Nanosecond
	defer func() { diffTimeout = old }()
	decode(t, f.get(t, "/backups/bk-n/diff?against=bk-o"), 503)
}
