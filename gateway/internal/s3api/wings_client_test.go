package s3api_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"ptero-enhanced-backup/gateway/internal/ingest"
	"ptero-enhanced-backup/gateway/internal/restic"
	"ptero-enhanced-backup/gateway/internal/store"
)

// ---- Wings-style uploader ---------------------------------------------------
//
// Mirrors server/backup/backup_s3.go of Wings 1.13: one request per part, a
// body that is a LimitReader over the single archive stream, and on 5xx (or a
// transport error) a retry of the SAME request, i.e. with the same, partly
// consumed reader. Only 4xx is final.

type wingsReader struct{ io.Reader }

func (wingsReader) Close() error { return nil }

type wingsStats struct {
	parts, retries, status5xx, status4xx atomic.Int64
	slowParts                            atomic.Int64 // parts that took > 100ms to be acknowledged
	maxPart                              atomic.Int64 // ns
}

// wingsUpload sends src (total bytes, a file in real life) in parts of
// partBytes and returns the ETags. presign gives the URL of a part.
func wingsUpload(src io.Reader, total, partBytes int64, presign func(n int32) string, st *wingsStats) ([]s3types.CompletedPart, error) {
	client := &http.Client{Timeout: 2 * time.Hour}
	var done []s3types.CompletedPart
	for n, off := int32(1), int64(0); off < total; n, off = n+1, off+partBytes {
		size := min(partBytes, total-off)
		rest := src
		req, _ := http.NewRequest(http.MethodPut, presign(n), nil)
		req.ContentLength = size
		req.Header.Add("Content-Length", strconv.FormatInt(size, 10))
		req.Header.Add("Content-Type", "application/x-gzip")
		req.Body = wingsReader{io.LimitReader(rest, size)}

		start := time.Now()
		deadline := start.Add(time.Minute)
		backoff := 500 * time.Millisecond
		var etag string
		for {
			res, err := client.Do(req)
			if err == nil {
				body, _ := io.ReadAll(res.Body)
				res.Body.Close()
				switch {
				case res.StatusCode == 200:
					etag = res.Header.Get("ETag")
				case res.StatusCode >= 500:
					st.status5xx.Add(1)
					err = fmt.Errorf("part %d: HTTP %d %s", n, res.StatusCode, body)
				default:
					st.status4xx.Add(1)
					return done, fmt.Errorf("part %d: HTTP %d %s", n, res.StatusCode, body)
				}
			}
			if err == nil {
				break
			}
			st.retries.Add(1)
			if time.Now().Add(backoff).After(deadline) {
				return done, fmt.Errorf("giving up: %w", err)
			}
			time.Sleep(backoff)
			backoff *= 2
		}
		d := time.Since(start)
		if d > 100*time.Millisecond {
			st.slowParts.Add(1)
		}
		if int64(d) > st.maxPart.Load() {
			st.maxPart.Store(int64(d))
		}
		st.parts.Add(1)
		done = append(done, s3types.CompletedPart{ETag: &etag, PartNumber: &n})
	}
	return done, nil
}

// ---- slow, hashing backend ---------------------------------------------------

type slowBackend struct {
	inner ingest.Backend
	bps   int64 // bytes per second the "restic" consumes, <= 0 unlimited

	mu     sync.Mutex
	hash   hash.Hash
	bytes  int64
	failed bool
}

func newSlowBackend(inner ingest.Backend, bps int64) *slowBackend {
	return &slowBackend{inner: inner, bps: bps}
}

type throttled struct {
	r     io.Reader
	bps   int64
	start time.Time
	n     int64
	h     hash.Hash
}

func (t *throttled) Read(p []byte) (int, error) {
	if len(p) > 256<<10 {
		p = p[:256<<10]
	}
	n, err := t.r.Read(p)
	if n > 0 {
		t.h.Write(p[:n])
		t.n += int64(n)
		if t.bps > 0 {
			if ahead := time.Duration(float64(t.n)/float64(t.bps)*float64(time.Second)) - time.Since(t.start); ahead > 0 {
				time.Sleep(ahead)
			}
		}
	}
	return n, err
}

func (s *slowBackend) Backup(ctx context.Context, r io.Reader, o restic.BackupOptions) (restic.Summary, error) {
	t := &throttled{r: r, bps: s.bps, start: time.Now(), h: sha256.New()}
	sum, err := s.inner.Backup(ctx, t, o)
	s.mu.Lock()
	s.hash, s.bytes, s.failed = t.h, t.n, err != nil
	s.mu.Unlock()
	return sum, err
}

func (s *slowBackend) Forget(ctx context.Context, id string) error { return s.inner.Forget(ctx, id) }

// seen returns the SHA-256 and length of the tar stream restic received.
func (s *slowBackend) seen() (string, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hash == nil {
		return "", 0
	}
	return hex.EncodeToString(s.hash.Sum(nil)), s.bytes
}

// ---- synthetic server archive ---------------------------------------------------

// writeTarGz writes gzip(tar(files)) to w and returns the SHA-256 and length of
// the plain tar stream. smallFiles files of 1-8 KiB and bigFiles files of
// bigBytes each (random, so the stream does not shrink). Memory use is a few
// MiB regardless of the size.
func writeTarGz(w io.Writer, smallFiles, bigFiles int, bigBytes int64) (string, int64, error) {
	gz, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
	h := sha256.New()
	var size atomic.Int64
	tw := tar.NewWriter(&countingWriter{w: io.MultiWriter(gz, h), n: &size})
	rng := rand.New(rand.NewSource(7))
	chunk := make([]byte, 1<<20)
	put := func(name string, size int64, compressible bool) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: size, ModTime: time.Unix(1700000000, 0)}); err != nil {
			return err
		}
		for size > 0 {
			n := min(int64(len(chunk)), size)
			if compressible {
				for i := range chunk[:n] {
					chunk[i] = byte('a' + i%7)
				}
			} else {
				rng.Read(chunk[:n])
			}
			if _, err := tw.Write(chunk[:n]); err != nil {
				return err
			}
			size -= n
		}
		return nil
	}
	var err error
	for i := 0; i < smallFiles && err == nil; i++ {
		err = put(fmt.Sprintf("plugins/cfg/%05d.yml", i), 1024+int64(rng.Intn(7*1024)), i%3 == 0)
	}
	for i := 0; i < bigFiles && err == nil; i++ {
		err = put(fmt.Sprintf("world/region/r.%d.mca", i), bigBytes, false)
		if err == nil && i%2 == 0 {
			for j := 0; j < 200 && err == nil; j++ { // small files between the big ones
				err = put(fmt.Sprintf("world/data/%d-%03d.dat", i, j), 2048, true)
			}
		}
	}
	if err == nil {
		err = tw.Close()
	}
	if err == nil {
		err = gz.Close()
	}
	return hex.EncodeToString(h.Sum(nil)), size.Load(), err
}

type countingWriter struct {
	w io.Writer
	n *atomic.Int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n.Add(int64(n))
	return n, err
}

// dumpSHA hashes /<server>.tar of a snapshot as restic restores it.
func dumpSHA(t *testing.T, e *env, snapshotID, server string) (string, int64) {
	t.Helper()
	rc, err := e.rc.Dump(context.Background(), snapshotID, "/"+server+".tar")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	h := sha256.New()
	n, err := io.Copy(h, rc)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	return hex.EncodeToString(h.Sum(nil)), n
}

// runWingsBackup drives one whole backup like Wings does (archive in a file,
// parts uploaded sequentially) and verifies the result byte for byte.
func runWingsBackup(t *testing.T, e *env, sb *slowBackend, key, server string, archive io.Reader, total int64, wantSHA string, wantLen int64, partBytes int64) *wingsStats {
	t.Helper()
	id := e.create(key)
	st := &wingsStats{}
	parts, err := wingsUpload(archive, total, partBytes, func(n int32) string { return e.presignPart(key, id, n) }, st)
	if err != nil {
		t.Fatalf("wings upload: %v (parts ok: %d, 5xx: %d)", err, st.parts.Load(), st.status5xx.Load())
	}
	e.complete(key, id, parts)
	b := e.waitStatusFor(key, store.StatusComplete, 10*time.Minute)

	gotSHA, gotLen := sb.seen()
	if gotSHA != wantSHA || gotLen != wantLen {
		t.Fatalf("restic received a different tar stream: sha %s (%d bytes), want %s (%d bytes)", gotSHA, gotLen, wantSHA, wantLen)
	}
	dsha, dlen := dumpSHA(t, e, b.SnapshotID, server)
	if dsha != wantSHA || dlen != wantLen {
		t.Fatalf("restored tar differs: sha %s (%d bytes), want %s (%d bytes)", dsha, dlen, wantSHA, wantLen)
	}
	if st.status5xx.Load() != 0 || st.retries.Load() != 0 {
		t.Fatalf("gateway answered Wings with 5xx / forced retries: 5xx=%d retries=%d", st.status5xx.Load(), st.retries.Load())
	}
	return st
}
