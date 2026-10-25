package config

import (
	"testing"
	"time"
)

func TestLoadTrashRetention(t *testing.T) {
	base := map[string]string{"PEB_ACCESS_KEY": "a", "PEB_SECRET_KEY": "s", "PEB_BUCKET": "b", "RESTIC_PASSWORD": "p"}
	c, err := Load(env(base))
	if err != nil || c.TrashRetention != 7*24*time.Hour || c.OrphanRetention != 30*24*time.Hour || c.TrashWarnBytes != 10<<30 {
		t.Fatalf("defaults: %v %+v", err, c)
	}
	base["PEB_TRASH_RETENTION"] = "0"
	base["PEB_ORPHAN_RETENTION"] = "72h"
	base["PEB_TRASH_WARN_GB"] = "0"
	c, err = Load(env(base))
	if err != nil || c.TrashRetention != 0 || c.OrphanRetention != 72*time.Hour || c.TrashWarnBytes != 0 {
		t.Fatalf("custom: %v %+v", err, c)
	}
	base["PEB_TRASH_RETENTION"] = "7d"
	if _, err = Load(env(base)); err == nil {
		t.Fatal("a duration Go cannot parse was accepted")
	}
	base["PEB_TRASH_RETENTION"] = "-1h"
	if _, err = Load(env(base)); err == nil {
		t.Fatal("negative retention accepted")
	}
}

func TestLoadReconcile(t *testing.T) {
	base := map[string]string{"PEB_ACCESS_KEY": "a", "PEB_SECRET_KEY": "s", "PEB_BUCKET": "b", "RESTIC_PASSWORD": "p"}
	if c, err := Load(env(base)); err != nil || !c.Reconcile {
		t.Fatalf("default must be on: %v %+v", err, c)
	}
	base["PEB_RECONCILE"] = "0"
	if c, err := Load(env(base)); err != nil || c.Reconcile {
		t.Fatalf("off: %v %+v", err, c)
	}
	base["PEB_RECONCILE"] = "maybe"
	if _, err := Load(env(base)); err == nil {
		t.Fatal("garbage accepted")
	}
}
