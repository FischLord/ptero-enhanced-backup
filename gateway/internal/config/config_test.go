package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadDefaults(t *testing.T) {
	c, err := Load(env(map[string]string{
		"PEB_ACCESS_KEY": "a", "PEB_SECRET_KEY": "s", "PEB_BUCKET": "b", "RESTIC_PASSWORD": "p", "SERVER_PORT": "25570",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "0.0.0.0:25570" || c.Region != "us-east-1" || c.DataDir != "/home/container/data" || c.ResticBin != "restic" {
		t.Fatalf("%+v", c)
	}
	if c.MaxPartBytes != 1<<30 || c.MaxSpoolBytes != 8<<30 || c.MaxConcurrentIngests != 2 || c.MaxConcurrentDumps != 2 ||
		c.MaxIndexFiles != 500000 || c.IngestTimeout.Hours() != 6 {
		t.Fatalf("limit defaults: %+v", c)
	}
	if c.ResticRepo != "/home/container/data/repo" {
		t.Fatalf("repo %q", c.ResticRepo)
	}
}

func TestLoadFallbackPortAndMissing(t *testing.T) {
	c, err := Load(env(map[string]string{
		"PEB_ACCESS_KEY": "a", "PEB_SECRET_KEY": "s", "PEB_BUCKET": "b", "RESTIC_PASSWORD_FILE": "/x",
	}))
	if err != nil || c.Listen != ":9000" {
		t.Fatalf("%v %+v", err, c)
	}
	if _, err := Load(env(nil)); err == nil {
		t.Fatal("expected error for empty config")
	}
}

func TestLoadLimits(t *testing.T) {
	base := map[string]string{"PEB_ACCESS_KEY": "a", "PEB_SECRET_KEY": "s", "PEB_BUCKET": "b", "RESTIC_PASSWORD": "p"}
	base["PEB_MAX_PART_BYTES"] = "1024"
	base["PEB_MAX_CONCURRENT_INGESTS"] = "5"
	base["PEB_PUBLIC_URL"] = "https://s3.example.org/"
	c, err := Load(env(base))
	if err != nil || c.MaxPartBytes != 1024 || c.MaxConcurrentIngests != 5 || c.PublicURL != "https://s3.example.org" {
		t.Fatalf("%v %+v", err, c)
	}
	base["PEB_MAX_SPOOL_BYTES"] = "lots"
	if _, err := Load(env(base)); err == nil {
		t.Fatal("expected error for non-numeric limit")
	}
}

func TestLoadMaintenanceDefaultsAndOverrides(t *testing.T) {
	base := map[string]string{"PEB_ACCESS_KEY": "a", "PEB_SECRET_KEY": "s", "PEB_BUCKET": "b", "RESTIC_PASSWORD": "p"}
	c, err := Load(env(base))
	if err != nil {
		t.Fatal(err)
	}
	if c.AdminToken != "" || c.MaintenanceHour != 2 || c.MaintenanceMinute != 0 || !c.PruneEnabled ||
		c.MaintenanceTimeout.Hours() != 3 || c.MaintenanceBusyWait.Minutes() != 15 || c.DownloadTimeout.Hours() != 2 ||
		c.CheckInterval.Hours() != 168 || c.CheckReadDataSubset != "" || c.RepoCapacityBytes != 0 {
		t.Fatalf("defaults: %+v", c)
	}
	base["PEB_MAINTENANCE_TIME"] = "3:45"
	base["PEB_PRUNE_ENABLED"] = "false"
	base["PEB_CHECK_INTERVAL"] = "24h"
	base["PEB_CHECK_READ_DATA_SUBSET"] = "5%"
	base["PEB_ADMIN_TOKEN"] = strings.Repeat("t", 32)
	base["PEB_REPO_CAPACITY_BYTES"] = "1000"
	c, err = Load(env(base))
	if err != nil || c.MaintenanceHour != 3 || c.MaintenanceMinute != 45 || c.PruneEnabled ||
		c.CheckInterval.Hours() != 24 || c.CheckReadDataSubset != "5%" || c.AdminToken != strings.Repeat("t", 32) || c.RepoCapacityBytes != 1000 {
		t.Fatalf("%v %+v", err, c)
	}
	for k, v := range map[string]string{
		"PEB_MAINTENANCE_TIME": "25:00", "PEB_PRUNE_ENABLED": "maybe", "PEB_CHECK_INTERVAL": "weekly", "PEB_CHECK_READ_DATA_SUBSET": "lots",
		"PEB_ADMIN_TOKEN": "short", "PEB_BUCKET": "admin", "PEB_MAINTENANCE_TIMEOUT": "0", "PEB_DOWNLOAD_TIMEOUT": "soon",
	} {
		bad := map[string]string{"PEB_ACCESS_KEY": "a", "PEB_SECRET_KEY": "s", "PEB_BUCKET": "b", "RESTIC_PASSWORD": "p", k: v}
		if _, err := Load(env(bad)); err == nil {
			t.Fatalf("%s=%s accepted", k, v)
		}
	}
}

func TestLoadSecondary(t *testing.T) {
	base := map[string]string{"PEB_ACCESS_KEY": "a", "PEB_SECRET_KEY": "s", "PEB_BUCKET": "b", "RESTIC_PASSWORD": "p"}
	c, err := Load(env(base))
	if err != nil || c.SecondaryEnabled() || c.SecondaryDelay.Minutes() != 5 || c.SecondaryTimeout.Hours() != 6 {
		t.Fatalf("defaults: %v %+v", err, c)
	}
	base["PEB_SECONDARY_REPO"] = "rclone:gdrive:peb"
	base["PEB_SECONDARY_DELAY"] = "90s"
	base["RCLONE_CONFIG"] = "/home/container/rclone.conf"
	c, err = Load(env(base))
	if err != nil || !c.SecondaryEnabled() || c.SecondaryDelay.Seconds() != 90 || c.RcloneConfig != "/home/container/rclone.conf" || c.SecondaryPass != "" {
		t.Fatalf("%v %+v", err, c)
	}
	for k, v := range map[string]string{"PEB_SECONDARY_DELAY": "soon", "PEB_SECONDARY_TIMEOUT": "0", "PEB_SECONDARY_REPO": "/data/repo"} {
		bad := map[string]string{"PEB_ACCESS_KEY": "a", "PEB_SECRET_KEY": "s", "PEB_BUCKET": "b", "RESTIC_PASSWORD": "p",
			"PEB_SECONDARY_REPO": "rclone:x:y", "RESTIC_REPOSITORY": "/data/repo"}
		bad[k] = v
		if _, err := Load(env(bad)); err == nil {
			t.Fatalf("%s=%s accepted", k, v)
		}
	}
	if _, err := Load(env(map[string]string{"PEB_ACCESS_KEY": "a", "PEB_SECRET_KEY": "s", "PEB_BUCKET": "b", "RESTIC_PASSWORD": "p", "PEB_SECONDARY_PASSWORD": "x"})); err == nil {
		t.Fatal("password without repo accepted")
	}
}

func TestSpoolWaitTimeout(t *testing.T) {
	base := map[string]string{"PEB_ACCESS_KEY": "a", "PEB_SECRET_KEY": "s", "PEB_BUCKET": "b", "RESTIC_PASSWORD": "p"}
	c, err := Load(env(base))
	if err != nil || c.SpoolWaitTimeout.Minutes() != 60 {
		t.Fatalf("default: %v %v", err, c.SpoolWaitTimeout)
	}
	base["PEB_SPOOL_WAIT_TIMEOUT"] = "90m"
	if c, err = Load(env(base)); err != nil || c.SpoolWaitTimeout.Minutes() != 90 {
		t.Fatalf("90m: %v %v", err, c.SpoolWaitTimeout)
	}
	base["PEB_SPOOL_WAIT_TIMEOUT"] = "0"
	if c, err = Load(env(base)); err != nil || c.SpoolWaitTimeout >= 0 {
		t.Fatalf("0 must mean no waiting: %v %v", err, c.SpoolWaitTimeout)
	}
	// Wings gives an UploadPart request 2h in total; waiting longer cannot work.
	base["PEB_SPOOL_WAIT_TIMEOUT"] = "3h"
	if _, err = Load(env(base)); err == nil || !strings.Contains(err.Error(), "PEB_SPOOL_WAIT_TIMEOUT") {
		t.Fatalf("3h accepted: %v", err)
	}
}

func TestLoadMetricsToken(t *testing.T) {
	long := strings.Repeat("m", 32)
	admin := strings.Repeat("a", 32)
	base := func(extra map[string]string) func(string) string {
		m := map[string]string{"PEB_ACCESS_KEY": "a", "PEB_SECRET_KEY": strings.Repeat("s", 32), "PEB_BUCKET": "b", "RESTIC_PASSWORD": "p"}
		for k, v := range extra {
			m[k] = v
		}
		return env(m)
	}
	if _, err := Load(base(map[string]string{"PEB_METRICS_TOKEN": long, "PEB_ADMIN_TOKEN": admin})); err != nil {
		t.Fatal(err)
	}
	for name, extra := range map[string]map[string]string{
		"short":  {"PEB_METRICS_TOKEN": "short"},
		"admin":  {"PEB_METRICS_TOKEN": admin, "PEB_ADMIN_TOKEN": admin},
		"secret": {"PEB_METRICS_TOKEN": strings.Repeat("s", 32)},
	} {
		if _, err := Load(base(extra)); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
