package restic

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRestic is a script that logs its arguments and answers key add / key list.
func fakeRestic(t *testing.T) (*Client, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "args")
	bin := filepath.Join(dir, "restic")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\n" +
		"case \"$1 $2\" in\n" +
		"\"key add\") echo saved new key with ID " + strings.Repeat("a", 64) + " ;;\n" +
		"\"key list\") echo '[{\"current\":false,\"id\":\"" + strings.Repeat("b", 64) + "\",\"created\":\"2026-10-25 07:00:00\"}]' ;;\n" +
		"esac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Client{Bin: bin, Repo: filepath.Join(dir, "repo"), Password: "pw", CacheDir: dir}, log
}

// key list must not take a lock; key add/remove must wait for one instead of failing at once.
func TestKeyCallsLockHandling(t *testing.T) {
	c, logf := fakeRestic(t)
	ctx := context.Background()
	if _, err := c.KeyList(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.KeyAdd(ctx, "x", "h", "u"); err != nil {
		t.Fatal(err)
	}
	if err := c.KeyRemove(ctx, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(logf)
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		switch {
		case strings.HasPrefix(line, "key list"):
			seen["list"] = true
			if !strings.Contains(line, "--no-lock") {
				t.Errorf("key list without --no-lock: %s", line)
			}
		case strings.HasPrefix(line, "key add"), strings.HasPrefix(line, "key remove"):
			seen[strings.Fields(line)[1]] = true
			if !strings.Contains(line, "--retry-lock "+keyLockWait) {
				t.Errorf("missing --retry-lock: %s", line)
			}
		}
	}
	if !seen["list"] || !seen["add"] || !seen["remove"] {
		t.Fatalf("calls seen: %v", seen)
	}
}

// A cancelled call must get a catchable signal so restic can remove its lock.
func TestCancelLetsResticCleanUp(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "cleaned")
	bin := filepath.Join(dir, "restic")
	script := "#!/bin/sh\ntrap 'touch " + marker + "; exit 0' TERM\nwhile true; do sleep 0.05; done\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &Client{Bin: bin, Repo: dir}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := c.output(ctx, "snapshots"); done <- err }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	<-done
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("restic was killed instead of asked to stop: %v", err)
	}
}
