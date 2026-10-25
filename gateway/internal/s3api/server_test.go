package s3api

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"ptero-enhanced-backup/gateway/internal/gate"
	"ptero-enhanced-backup/gateway/internal/ingest"
	"ptero-enhanced-backup/gateway/internal/s3auth"
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

// countBody is an endless body that counts what the server reads from it.
type countBody struct{ n int64 }

func (c *countBody) Read(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }
func (c *countBody) Close() error               { return nil }

// A request that fails authentication must not get the full part drain: only a
// bounded read, then the connection is closed.
func TestUnauthenticatedPartBodyIsDrainedBoundedAndClosed(t *testing.T) {
	s := &Server{Bucket: "b", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Verifier:     &s3auth.Verifier{Creds: s3auth.Credentials{AccessKey: "a", SecretKey: "s", Region: "us-east-1"}},
		MaxPartBytes: 1 << 30}
	body := &countBody{}
	r := httptest.NewRequest("PUT", "http://gw.local/b/srv/x.tar.gz?partNumber=1&uploadId=u", body)
	r.ContentLength = 1 << 30
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code < 400 || w.Code >= 500 {
		t.Fatalf("status %d", w.Code)
	}
	if w.Header().Get("Connection") != "close" {
		t.Fatalf("Connection header %q", w.Header().Get("Connection"))
	}
	if body.n > unauthDrainMax+64<<10 {
		t.Fatalf("read %d bytes of an unauthenticated body", body.n)
	}
}

// A failed lookup of the upload is answered 409 and must not fail the upload
// (the nil Ingest would panic in Fail).
func TestUploadPartLookupErrorDoesNotFailUpload(t *testing.T) {
	s := &Server{Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		getUpload: func(string) (*ingest.Upload, error) { return nil, errors.New("database is locked") }}
	r := httptest.NewRequest("PUT", "http://gw.local/b/srv/x.tar.gz?partNumber=1&uploadId=u", nil)
	err := s.uploadPart(httptest.NewRecorder(), r, "srv/x.tar.gz", "u", "1", &s3auth.Result{})
	var ae *apiError
	if !errors.As(err, &ae) || ae.Status != 409 {
		t.Fatalf("got %v", err)
	}
}
