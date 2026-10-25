package config

import (
	"testing"
	"time"
)

func TestLoadForgetSafety(t *testing.T) {
	base := map[string]string{"PEB_ACCESS_KEY": "a", "PEB_SECRET_KEY": "s", "PEB_BUCKET": "b", "RESTIC_PASSWORD": "p"}
	c, err := Load(env(base))
	if err != nil || c.SecondaryForgetGrace != 48*time.Hour || c.SecondaryForgetConfirm != "" || c.PruneSkipWarn != 2 {
		t.Fatalf("defaults: %v %+v", err, c)
	}
	base["PEB_SECONDARY_FORGET_GRACE"] = "12h"
	base["PEB_SECONDARY_FORGET_CONFIRM"] = " abc123 "
	base["PEB_PRUNE_SKIP_WARN"] = "3"
	c, err = Load(env(base))
	if err != nil || c.SecondaryForgetGrace != 12*time.Hour || c.SecondaryForgetConfirm != "abc123" || c.PruneSkipWarn != 3 {
		t.Fatalf("%v %+v", err, c)
	}
	base["PEB_SECONDARY_FORGET_GRACE"] = "0"
	if c, err = Load(env(base)); err != nil || c.SecondaryForgetGrace >= 0 {
		t.Fatalf("0 must mean no grace: %v %v", err, c.SecondaryForgetGrace)
	}
	base["PEB_SECONDARY_FORGET_GRACE"] = "soon"
	if _, err = Load(env(base)); err == nil {
		t.Fatal("bad grace accepted")
	}
}
