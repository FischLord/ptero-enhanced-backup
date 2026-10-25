package restic

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A leftover lock survives --retry-lock; key remove then runs `unlock` once and retries once.
func TestKeyRemoveUnlocksStaleLockAndRetries(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "args")
	flag := filepath.Join(dir, "unlocked")
	bin := filepath.Join(dir, "restic")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\n" +
		"case \"$1\" in\n" +
		"unlock) touch " + flag + " ;;\n" +
		"key) case \"$2\" in\n" +
		"list) echo '[{\"current\":false,\"id\":\"" + strings.Repeat("b", 64) + "\",\"created\":\"2026-10-25 07:00:00\"}]' ;;\n" +
		"remove) [ -f " + flag + " ] || { echo 'unable to create lock in backend: repository is already locked by PID 377' >&2; exit 11; } ;;\n" +
		"esac ;;\n" +
		"esac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &Client{Bin: bin, Repo: dir, Password: "pw"}
	if err := c.KeyRemove(context.Background(), strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if n := strings.Count(string(b), "key remove"); n != 2 {
		t.Fatalf("remove attempts: %d\n%s", n, b)
	}
	if strings.Count(string(b), "unlock") != 1 || strings.Contains(string(b), "--remove-all") {
		t.Fatalf("unlock calls:\n%s", b)
	}
}
