package restic

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func realClient(t *testing.T) *Client {
	t.Helper()
	bin := os.Getenv("PEB_RESTIC_BIN")
	if bin == "" {
		bin = "restic"
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		t.Skip("restic binary not found, skipping integration test")
	}
	dir := t.TempDir()
	c := &Client{Bin: path, Repo: filepath.Join(dir, "repo"), Password: "primary-pw", CacheDir: filepath.Join(dir, "cache")}
	if err := c.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestKeyAddListRemoveReal(t *testing.T) {
	c := realClient(t)
	ctx := context.Background()
	const second = "second-password-for-the-hoster-123456"
	id, err := c.KeyAdd(ctx, second, "hoster", "peb-emergency")
	if err != nil || len(id) != 64 {
		t.Fatalf("%q %v", id, err)
	}
	// no temporary password file is left behind
	if m, _ := filepath.Glob(filepath.Join(c.CacheDir, ".peb-newkey-*")); len(m) != 0 {
		t.Fatalf("temporary file left: %v", m)
	}
	// the new password opens the repository
	other := &Client{Bin: c.Bin, Repo: c.Repo, Password: second, CacheDir: c.CacheDir}
	if err := other.run(ctx, "cat", "config"); err != nil {
		t.Fatalf("second password does not work: %v", err)
	}
	keys, err := c.KeyList(ctx)
	if err != nil || len(keys) != 2 {
		t.Fatalf("%v %v", keys, err)
	}
	var cur, em *Key
	for i := range keys {
		if keys[i].Current {
			cur = &keys[i]
		}
		if keys[i].ID == id {
			em = &keys[i]
		}
	}
	if cur == nil || em == nil || em.Host != "hoster" || em.User != "peb-emergency" || em.Created.IsZero() || em.Current {
		t.Fatalf("%+v", keys)
	}
	if err := c.KeyRemove(ctx, cur.ID); err != ErrCurrentKey {
		t.Fatalf("current key removal must be refused, got %v", err)
	}
	if err := c.KeyRemove(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := other.run(ctx, "cat", "config"); err == nil {
		t.Fatal("removed key still works")
	}
	if keys, _ := c.KeyList(ctx); len(keys) != 1 {
		t.Fatalf("%v", keys)
	}
}

func TestKeyAddErrorIsScrubbed(t *testing.T) {
	c := realClient(t)
	c.Repo += "-missing"
	_, err := c.KeyAdd(context.Background(), "secret-new-password-xyz", "h", "u")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "secret-new-password-xyz") || strings.Contains(err.Error(), ".peb-newkey-") {
		t.Fatalf("leak: %v", err)
	}
}
