package restic

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ptero-enhanced-backup/gateway/internal/gate"
)

func TestParsePruneFreed(t *testing.T) {
	out := "to delete:             0 blobs / 0 B\ntotal prune:           12 blobs / 1.500 MiB\nremaining:   1 blobs / 2 B\n"
	if got := parsePruneFreed(out); got != 1572864 {
		t.Fatalf("got %d", got)
	}
	if got := parsePruneFreed("total prune:           1 blobs / 246 B\n"); got != 246 {
		t.Fatalf("got %d", got)
	}
	if got := parsePruneFreed("nothing useful"); got != -1 {
		t.Fatalf("got %d", got)
	}
}

func TestGatedWaitsForGate(t *testing.T) {
	g := &gate.Gate{}
	rg := &Gated{Client: &Client{Bin: "true"}, Gate: g} // "true" accepts any arguments and succeeds
	if err := rg.Forget(context.Background(), "abc"); err != nil || g.Active() != 0 {
		t.Fatalf("open gate: %v active=%d", err, g.Active())
	}
	g.Pause()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := rg.Forget(ctx, "abc"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("paused gate: %v", err)
	}
	if _, err := rg.RawStats(ctx); err == nil {
		t.Fatal("RawStats must wait for the gate too")
	}
	g.Resume()
	if err := rg.Forget(context.Background(), "abc"); err != nil {
		t.Fatal(err)
	}
}

func TestDirSizeAndRepoType(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "a/b"), 0o755)
	os.WriteFile(filepath.Join(dir, "a/x"), make([]byte, 100), 0o644)
	os.WriteFile(filepath.Join(dir, "a/b/y"), make([]byte, 23), 0o644)
	if n, err := DirSize(dir); err != nil || n != 123 {
		t.Fatalf("%d %v", n, err)
	}
	for in, want := range map[string]string{"/data/repo": "local", "rest:http://h/": "rest", "sftp:u@h:/x": "sftp", "rclone:r:p": "rclone", "relative/rest": "local"} {
		if got := RepoType(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

func TestFSSpace(t *testing.T) {
	total, free, err := FSSpace(t.TempDir())
	if err != nil || total <= 0 || free < 0 || free > total {
		t.Fatalf("total=%d free=%d err=%v", total, free, err)
	}
	if _, _, err := FSSpace(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing path must fail")
	}
}
