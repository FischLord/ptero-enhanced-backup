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
