package s3api_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ptero-enhanced-backup/gateway/internal/ingest"
	"ptero-enhanced-backup/gateway/internal/restic"
	"ptero-enhanced-backup/gateway/internal/store"
)

// A slow restic and a tiny spool: UploadPart must wait (backpressure) instead
// of answering 503, so that a Wings-style client, which cannot resend a
// consumed body, still produces a byte-identical backup.
func TestSlowIngestSmallSpoolWingsClient(t *testing.T) {
	var sb *slowBackend
	e := newEnvWith(t, func(inner ingest.Backend) ingest.Backend {
		sb = newSlowBackend(inner, 6<<20) // 6 MiB/s
		return sb
	}, 2<<20) // spool: two parts
	e.api.SpoolWait = 2 * time.Minute

	var buf bytes.Buffer
	sha, plain, err := writeTarGz(&buf, 300, 3, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	var peak atomic.Int64
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
				if u := e.mgr.Quota.Used(); u > peak.Load() {
					peak.Store(u)
				}
			}
		}
	}()

	st := runWingsBackup(t, e, sb, "srv/slow.tar.gz", "srv", bytes.NewReader(buf.Bytes()), int64(buf.Len()), sha, plain, partSize)
	if st.slowParts.Load() == 0 {
		t.Fatalf("test did not exercise backpressure (parts=%d)", st.parts.Load())
	}
	if peak.Load() > 2<<20 {
		t.Fatalf("spool quota exceeded: peak %d", peak.Load())
	}
	t.Logf("%d parts, %d waited >100ms, longest %s, spool peak %d KiB, archive %d KiB",
		st.parts.Load(), st.slowParts.Load(), time.Duration(st.maxPart.Load()).Round(time.Millisecond), peak.Load()>>10, buf.Len()>>10)
	if q := e.mgr.Quota.Used(); q != 0 {
		t.Fatalf("spool not released: %d", q)
	}
}

// blockBackend never reads the stream, so the spool can never drain.
type blockBackend struct{ ingest.Backend }

func (blockBackend) Backup(ctx context.Context, r io.Reader, o restic.BackupOptions) (restic.Summary, error) {
	<-ctx.Done()
	return restic.Summary{}, ctx.Err()
}

// When the wait runs out the upload is failed cleanly: a 4xx (no Wings retry),
// the body was read completely, no snapshot, nothing left in the spool.
func TestSpoolWaitTimeoutFailsUploadWith4xx(t *testing.T) {
	e := newEnvWith(t, func(inner ingest.Backend) ingest.Backend { return blockBackend{inner} }, 3<<19) // 1.5 MiB
	e.api.SpoolWait = 400 * time.Millisecond
	key := "srv/stuck.tar.gz"
	id := e.create(key)

	data := make([]byte, partSize)
	for i := range data {
		data[i] = byte(i)
	}
	// A valid gzip header, so that the pipeline starts (and then never reads on).
	copy(data, []byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 255})
	// The pipeline never consumes, so even part 1 stays spooled (1 MiB of 1.5).
	resp, err := wingsPut(e.presignPart(key, id, 1), data)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("part 1: %v %v", err, resp)
	}
	// Part 2 is the one the running pipeline waits for: always admitted (overbooks the quota).
	resp, err = wingsPut(e.presignPart(key, id, 2), data)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("part 2: %v %v", err, resp)
	}
	st := &wingsStats{}
	start := time.Now()
	_, err = wingsUpload(bytes.NewReader(data), int64(len(data)), int64(len(data)),
		func(n int32) string { return e.presignPart(key, id, n+2) }, st)
	if err == nil || !strings.Contains(err.Error(), "HTTP 409") {
		t.Fatalf("want a final 409, got %v", err)
	}
	if st.status5xx.Load() != 0 || st.retries.Load() != 0 {
		t.Fatalf("5xx=%d retries=%d", st.status5xx.Load(), st.retries.Load())
	}
	if d := time.Since(start); d < 300*time.Millisecond {
		t.Fatalf("did not wait: %s", d)
	}
	b := e.waitStatus(key, store.StatusFailed)
	if !strings.Contains(b.Error, "spool") {
		t.Fatalf("unclear failure reason: %q", b.Error)
	}
	if s := e.snapshots(); len(s) != 0 {
		t.Fatalf("snapshot kept: %+v", s)
	}
	deadline := time.Now().Add(5 * time.Second)
	for e.mgr.Quota.Used() != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if u := e.mgr.Quota.Used(); u != 0 {
		t.Fatalf("spool not released: %d", u)
	}
	// Further requests on the dead upload are 4xx too, never 5xx.
	resp, _ = wingsPut(e.presignPart(key, id, 4), data[:10])
	if resp.StatusCode != 409 {
		t.Fatalf("part after failure: %d", resp.StatusCode)
	}
}

// A body that is shorter than its Content-Length (connection cut) is never
// acknowledged, and a complete retry of the same part afterwards wins.
func TestCutOffPartIsNotAcknowledged(t *testing.T) {
	e := newEnv(t)
	key := "srv/cut.tar.gz"
	id := e.create(key)
	url := e.presignPart(key, id, 1)

	conn := dialRaw(t, e.srv.URL)
	conn.sendPartial(t, "PUT", url, 1000, 400) // announces 1000 bytes, sends 400, closes
	time.Sleep(200 * time.Millisecond)
	if p := e.mgrParts(id); len(p) != 0 {
		t.Fatalf("cut-off part was stored: %v", p)
	}
	if q := e.mgr.Quota.Used(); q != 0 {
		t.Fatalf("quota leaked: %d", q)
	}
	// The retry with the full body is acknowledged and is what the pipeline gets.
	resp, err := wingsPut(url, bytes.Repeat([]byte{1}, 1000))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("retry: %v %v", err, resp)
	}
}

type rawConn struct{ c net.Conn }

func dialRaw(t *testing.T, base string) *rawConn {
	t.Helper()
	u, _ := url.Parse(base)
	c, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &rawConn{c}
}

// sendPartial writes a request that promises declared body bytes, sends only
// sent of them and closes the connection.
func (r *rawConn) sendPartial(t *testing.T, method, rawURL string, declared, sent int) {
	t.Helper()
	u, _ := url.Parse(rawURL)
	fmt.Fprintf(r.c, "%s %s HTTP/1.1\r\nHost: %s\r\nContent-Length: %d\r\n\r\n", method, u.RequestURI(), u.Host, declared)
	r.c.Write(bytes.Repeat([]byte{1}, sent))
	r.c.Close()
}
