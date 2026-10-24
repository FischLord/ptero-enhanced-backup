package s3api

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"ptero-enhanced-backup/gateway/internal/gate"
)

func TestLocation(t *testing.T) {
	r := httptest.NewRequest("POST", "http://gw.local:9000/bkt/s/b.tar.gz?uploadId=x", nil)
	if got := (&Server{}).location(r); got != "http://gw.local:9000/bkt/s/b.tar.gz" {
		t.Fatal(got)
	}
	r.Header.Set("X-Forwarded-Proto", "https")
	r.Header.Set("X-Forwarded-Host", "backup.example.org")
	if got := (&Server{}).location(r); got != "https://backup.example.org/bkt/s/b.tar.gz" {
		t.Fatal(got)
	}
	if got := (&Server{PublicURL: "https://s3.example.org/"}).location(r); got != "https://s3.example.org/bkt/s/b.tar.gz" {
		t.Fatal(got)
	}
	r2 := httptest.NewRequest("POST", "https://gw.local/bkt/k", nil)
	r2.TLS = &tls.ConnectionState{}
	if got := (&Server{}).location(r2); got != "https://gw.local/bkt/k" {
		t.Fatal(got)
	}
}

func TestAcquireDumpRespectsGateAndSlots(t *testing.T) {
	g := &gate.Gate{}
	s := &Server{MaxDumps: 1, DumpWait: 100 * time.Millisecond, Gate: g}
	ctx := context.Background()

	release, err := s.AcquireDump(ctx)
	if err != nil || s.ActiveDumps() != 1 || g.Active() != 1 {
		t.Fatalf("acquire: %v active=%d gate=%d", err, s.ActiveDumps(), g.Active())
	}
	if _, err := s.AcquireDump(ctx); !errors.Is(err, ErrDumpBusy) {
		t.Fatalf("second slot: %v", err)
	}
	release()
	if s.ActiveDumps() != 0 || g.Active() != 0 {
		t.Fatalf("after release: %d %d", s.ActiveDumps(), g.Active())
	}

	// While maintenance holds the repository, restores wait and then give up with "busy".
	g.Pause()
	if _, err := s.AcquireDump(ctx); !errors.Is(err, ErrDumpBusy) {
		t.Fatalf("paused gate: %v", err)
	}
	if s.ActiveDumps() != 0 {
		t.Fatal("slot leaked while paused")
	}
	g.Resume()
	release, err = s.AcquireDump(ctx)
	if err != nil {
		t.Fatal(err)
	}
	release()
}
