package s3api_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/klauspost/pgzip"

	"ptero-enhanced-backup/gateway/internal/gate"
	"ptero-enhanced-backup/gateway/internal/ingest"
	"ptero-enhanced-backup/gateway/internal/restic"
	"ptero-enhanced-backup/gateway/internal/s3api"
	"ptero-enhanced-backup/gateway/internal/s3auth"
	"ptero-enhanced-backup/gateway/internal/store"
)

const (
	accessKey = "AKIAPEBTEST"
	secretKey = "peb-test-secret"
	bucket    = "backups"
	partSize  = 1 << 20
)

type env struct {
	api   *s3api.Server
	mgr   *ingest.Manager
	t     *testing.T
	srv   *httptest.Server
	panel *s3.Client // acts like Pterodactyl Panel (aws-sdk)
	pre   *s3.PresignClient
	rc    *restic.Client
	st    *store.Store
}

func newEnv(t *testing.T) *env { return newEnvWith(t, nil, 0) }

// newEnvWith lets tests wrap the ingest backend (e.g. to slow it down) and
// shrink the spool (0 keeps the default).
func newEnvWith(t *testing.T, wrap func(ingest.Backend) ingest.Backend, spoolMax int64) *env {
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
	rc := &restic.Client{Bin: path, Repo: filepath.Join(dir, "repo"), Password: "pw", CacheDir: filepath.Join(dir, "cache")}
	if err := rc.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "peb.db"))
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	gt := &gate.Gate{}
	var be ingest.Backend = &restic.Gated{Client: rc, Gate: gt}
	if wrap != nil {
		be = wrap(be)
	}
	mgr := ingest.NewManager(st, be, filepath.Join(dir, "spool"), log)
	mgr.Gate = gt
	if spoolMax > 0 {
		mgr.Quota.Max = spoolMax
	}
	if err := mgr.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	api := &s3api.Server{
		Bucket:   bucket,
		Verifier: &s3auth.Verifier{Creds: s3auth.Credentials{AccessKey: accessKey, SecretKey: secretKey, Region: "us-east-1"}},
		Ingest:   mgr, Store: st, Restic: &restic.Gated{Client: rc, Gate: mgr.Gate}, Gate: mgr.Gate, Log: log, WaitTimeout: time.Minute,
	}
	srv := httptest.NewServer(api)
	e := &env{t: t, srv: srv, rc: rc, st: st, api: api, mgr: mgr}
	e.panel = e.client(secretKey)
	e.pre = s3.NewPresignClient(e.panel)
	t.Cleanup(func() {
		srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		mgr.Shutdown(ctx)
		st.Close()
	})
	return e
}

func (e *env) client(secret string) *s3.Client {
	return s3.New(s3.Options{
		Region:                     "us-east-1",
		BaseEndpoint:               aws.String(e.srv.URL),
		UsePathStyle:               true,
		Credentials:                credentials.NewStaticCredentialsProvider(accessKey, secret, ""),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
}

// ---- test data ----

type tree map[string][]byte

func randBytes(seed int64, n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func baseTree() tree {
	return tree{
		"server.properties":      []byte("motd=hello\nmax-players=20\n"),
		"world/level.dat":        randBytes(1, 3<<20),
		"world/region/r.0.0.mca": randBytes(2, 9<<20),
		"world/region/r.0.1.mca": randBytes(3, 4<<20),
		"plugins/big.jar":        randBytes(4, 2<<20),
		"logs/latest.log":        bytes.Repeat([]byte("[INFO] tick\n"), 20000),
	}
}

func (tr tree) clone() tree {
	c := tree{}
	for k, v := range tr {
		c[k] = append([]byte(nil), v...)
	}
	return c
}

type gzipWriter interface {
	io.WriteCloser
}

// makeArchive builds a tar.gz like wings does: pgzip with NoCompression, or
// plain gzip BestSpeed.
func makeArchive(t *testing.T, tr tree, usePgzip bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	var gz gzipWriter
	if usePgzip {
		w, err := pgzip.NewWriterLevel(&buf, pgzip.NoCompression)
		if err != nil {
			t.Fatal(err)
		}
		gz = w
	} else {
		w, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
		gz = w
	}
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(tr))
	for n := range tr {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		h := &tar.Header{Name: n, Mode: 0o644, Size: int64(len(tr[n])), ModTime: time.Unix(1_700_000_000, 0)}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		tw.Write(tr[n])
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func untar(t *testing.T, gzData io.Reader) tree {
	t.Helper()
	gz, err := gzip.NewReader(gzData)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	tr := tar.NewReader(gz)
	out := tree{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("untar: %v", err)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("untar read: %v", err)
		}
		out[h.Name] = b
	}
}

func equalTrees(t *testing.T, want, got tree) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("file count: want %d got %d", len(want), len(got))
	}
	for n, w := range want {
		if g, ok := got[n]; !ok || !bytes.Equal(w, g) {
			t.Fatalf("file %s differs (present=%v)", n, ok)
		}
	}
}

// ---- actors ----

func (e *env) create(key string) string {
	e.t.Helper()
	out, err := e.panel.CreateMultipartUpload(context.Background(), &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(key), ContentType: aws.String("application/x-gzip"),
	})
	if err != nil {
		e.t.Fatalf("CreateMultipartUpload: %v", err)
	}
	return *out.UploadId
}

// wingsPut is what Wings does: PUT a part to a presigned URL.
func wingsPut(url string, data []byte) (*http.Response, error) {
	req, _ := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	req.ContentLength = int64(len(data))
	req.Header.Set("Content-Type", "application/x-gzip")
	return http.DefaultClient.Do(req)
}

// upload runs the panel/wings handshake: presign every part, upload them
// sequentially, then CompleteMultipartUpload. It returns the part ETags.
func (e *env) upload(key, uploadID string, archive []byte, parts int) []s3types.CompletedPart {
	e.t.Helper()
	ctx := context.Background()
	var done []s3types.CompletedPart
	for i := 0; i*partSize < len(archive); i++ {
		chunk := archive[i*partSize : min((i+1)*partSize, len(archive))]
		n := int32(i + 1)
		ps, err := e.pre.PresignUploadPart(ctx, &s3.UploadPartInput{
			Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID), PartNumber: aws.Int32(n),
		}, s3.WithPresignExpires(time.Hour))
		if err != nil {
			e.t.Fatal(err)
		}
		resp, err := wingsPut(ps.URL, chunk)
		if err != nil {
			e.t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		etag := resp.Header.Get("ETag")
		if resp.StatusCode != 200 || etag == "" {
			e.t.Fatalf("part %d: status %d etag %q", n, resp.StatusCode, etag)
		}
		done = append(done, s3types.CompletedPart{ETag: aws.String(etag), PartNumber: aws.Int32(n)})
	}
	return done
}

func (e *env) complete(key, uploadID string, parts []s3types.CompletedPart) {
	e.t.Helper()
	_, err := e.panel.CompleteMultipartUpload(context.Background(), &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
		MultipartUpload: &s3types.CompletedMultipartUpload{Parts: parts},
	})
	if err != nil {
		e.t.Fatalf("CompleteMultipartUpload: %v", err)
	}
}

func (e *env) backup(key string, archive []byte) *store.Backup {
	e.t.Helper()
	id := e.create(key)
	parts := e.upload(key, id, archive, 0)
	e.complete(key, id, parts)
	return e.waitStatus(key, store.StatusComplete)
}

func (e *env) waitStatus(key, want string) *store.Backup {
	e.t.Helper()
	return e.waitStatusFor(key, want, 2*time.Minute)
}

func (e *env) waitStatusFor(key, want string, limit time.Duration) *store.Backup {
	e.t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		b, err := e.st.BackupByKey(context.Background(), key)
		if err == nil && b.Status == want {
			return b
		}
		if err == nil && b.Status == store.StatusFailed && want != store.StatusFailed {
			e.t.Fatalf("backup failed: %s", b.Error)
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("timeout waiting for status %s", want)
	return nil
}

// download is the restore: presigned GET, as used by wings and browsers.
func (e *env) download(key string) (*http.Response, []byte) {
	e.t.Helper()
	ps, err := e.pre.PresignGetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key),
	}, s3.WithPresignExpires(time.Hour))
	if err != nil {
		e.t.Fatal(err)
	}
	resp, err := http.Get(ps.URL)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		e.t.Fatalf("download: %v", err)
	}
	return resp, body
}

func (e *env) snapshots() []restic.Snapshot {
	e.t.Helper()
	s, err := e.rc.Snapshots(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func apiCode(err error) string {
	var s = fmt.Sprint(err)
	for _, c := range []string{"SignatureDoesNotMatch", "NoSuchUpload", "NoSuchKey", "InvalidPart", "AccessDenied"} {
		if strings.Contains(s, c) {
			return c
		}
	}
	return s
}

// ---- tests ----

func TestBackupRestoreDeleteAndDedup(t *testing.T) {
	for _, variant := range []struct {
		name  string
		pgzip bool
	}{{"pgzip-nocompression", true}, {"gzip-bestspeed", false}} {
		t.Run(variant.name, func(t *testing.T) {
			e := newEnv(t)
			ctx := context.Background()
			srv, bk1, bk2, bk3 := "11111111-aaaa", "22222222-bbbb", "33333333-cccc", "44444444-dddd"
			key1, key2, key3 := srv+"/"+bk1+".tar.gz", srv+"/"+bk2+".tar.gz", srv+"/"+bk3+".tar.gz"

			data1 := baseTree()
			arch1 := makeArchive(t, data1, variant.pgzip)
			b1 := e.backup(key1, arch1)

			// Restore must be byte-identical in content.
			resp, body := e.download(key1)
			if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/x-gzip" ||
				!strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
				t.Fatalf("download headers: %d %v", resp.StatusCode, resp.Header)
			}
			equalTrees(t, data1, untar(t, bytes.NewReader(body)))

			// Metrics and file index.
			if b1.SnapshotID == "" || b1.DataAdded == 0 || b1.TotalBytesProcessed == 0 ||
				b1.CompressedBytes != int64(len(arch1)) || b1.UncompressedBytes < b1.CompressedBytes/2 || b1.FileCount != int64(len(data1)) {
				t.Fatalf("metrics: %+v", b1)
			}
			files, _ := e.st.Files(ctx, b1.ID)
			if len(files) != len(data1) {
				t.Fatalf("index: %d entries", len(files))
			}
			for _, f := range files {
				if int(f.Size) != len(data1[f.Path]) || f.Mode != 0o644 {
					t.Fatalf("index entry %+v", f)
				}
			}
			snaps := e.snapshots()
			if len(snaps) != 1 || !contains(snaps[0].Tags, "server:"+srv) || !contains(snaps[0].Tags, "backup:"+bk1) {
				t.Fatalf("snapshots: %+v", snaps)
			}

			// Second backup, slightly modified: dedup must kick in.
			data2 := data1.clone()
			copy(data2["world/region/r.0.0.mca"][4<<20:], []byte("MODIFIED-BLOCK"))
			data2["world/new.txt"] = []byte("new file")
			b2 := e.backup(key2, makeArchive(t, data2, variant.pgzip))
			t.Logf("%s: data_added first=%d second=%d (processed %d / %d)", variant.name, b1.DataAdded, b2.DataAdded,
				b1.TotalBytesProcessed, b2.TotalBytesProcessed)
			// How much a modified file adds depends on where the repository's random chunker
			// polynomial puts the chunk boundaries (2.5-9.8 MB of 19 MB observed, restic chunks
			// are up to 8 MiB), so a fixed fraction flakes. What is certain: the change touches
			// at most two chunks (<= 16 MiB of 19 MB), so the second backup must add less than
			// the first. The strict dedup check is the identical re-backup below.
			if b2.DataAdded >= b1.DataAdded {
				t.Fatalf("no dedup: first %d second %d", b1.DataAdded, b2.DataAdded)
			}
			// Identical content again: every chunk exists, almost nothing may be added.
			b3 := e.backup(key3, makeArchive(t, data1, variant.pgzip))
			if b3.DataAdded*20 > b1.DataAdded {
				t.Fatalf("no dedup for identical content: first %d third %d", b1.DataAdded, b3.DataAdded)
			}
			_, body2 := e.download(key2)
			equalTrees(t, data2, untar(t, bytes.NewReader(body2)))

			// Delete via the panel; snapshot disappears, deleting again is fine.
			for _, key := range []string{key1, key2, key3} {
				for range 2 {
					out, err := e.panel.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
					if err != nil {
						t.Fatalf("DeleteObject: %v", err)
					}
					_ = out
				}
			}
			if s := e.snapshots(); len(s) != 0 {
				t.Fatalf("snapshots after delete: %+v", s)
			}
			resp, _ = e.download(key1)
			if resp.StatusCode != 404 {
				t.Fatalf("download after delete: %d", resp.StatusCode)
			}
		})
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestListPartsAndRetry(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	key := "srv/bk.tar.gz"
	arch := makeArchive(t, baseTree(), true)
	id := e.create(key)
	parts := e.upload(key, id, arch, 0)

	lp, err := e.panel.ListParts(ctx, &s3.ListPartsInput{Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(id)})
	if err != nil {
		t.Fatal(err)
	}
	if len(lp.Parts) != len(parts) {
		t.Fatalf("ListParts: %d parts, want %d", len(lp.Parts), len(parts))
	}
	for i, p := range lp.Parts {
		if *p.ETag != *parts[i].ETag || *p.PartNumber != *parts[i].PartNumber {
			t.Fatalf("part %d: %v vs %v", i, p, parts[i])
		}
	}
	// Wings retrying the first (already consumed) part gets the same ETag.
	ps, _ := e.pre.PresignUploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(id), PartNumber: aws.Int32(1),
	})
	waitConsumed(t, e, key)
	resp, err := wingsPut(ps.URL, arch[:partSize])
	if err != nil || resp.StatusCode != 200 || resp.Header.Get("ETag") != *parts[0].ETag {
		t.Fatalf("retry of consumed part: %v %v", err, resp)
	}
	// ...but different content for a consumed part is refused.
	resp, _ = wingsPut(ps.URL, append([]byte("x"), arch[:partSize]...))
	if resp.StatusCode == 200 {
		t.Fatal("conflicting retry accepted")
	}
	// A wrong ETag in Complete is rejected, the right one accepted.
	_, err = e.panel.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(id),
		MultipartUpload: &s3types.CompletedMultipartUpload{Parts: []s3types.CompletedPart{{ETag: aws.String(`"deadbeef"`), PartNumber: aws.Int32(1)}}},
	})
	if apiCode(err) != "InvalidPart" {
		t.Fatalf("complete with bad etag: %v", err)
	}
	e.complete(key, id, parts)
	e.waitStatus(key, store.StatusComplete)
}

// waitConsumed gives the pipeline time to pick up part 1 (it starts reading it immediately).
func waitConsumed(t *testing.T, e *env, key string) {
	time.Sleep(500 * time.Millisecond)
}

func TestAbort(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	key := "srv/aborted.tar.gz"
	arch := makeArchive(t, baseTree(), true)
	id := e.create(key)
	ps, _ := e.pre.PresignUploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(id), PartNumber: aws.Int32(1),
	})
	if resp, err := wingsPut(ps.URL, arch[:partSize]); err != nil || resp.StatusCode != 200 {
		t.Fatalf("part: %v %v", err, resp)
	}
	if _, err := e.panel.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(id),
	}); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if s := e.snapshots(); len(s) != 0 {
		t.Fatalf("snapshot left behind: %+v", s)
	}
	b := e.waitStatus(key, store.StatusFailed)
	if b.Error != "aborted" {
		t.Fatalf("backup: %+v", b)
	}
	_, err := e.panel.ListParts(ctx, &s3.ListPartsInput{Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(id)})
	if apiCode(err) != "NoSuchUpload" {
		t.Fatalf("ListParts after abort: %v", err)
	}
	if resp, _ := e.download(key); resp.StatusCode != 404 {
		t.Fatalf("GET after abort: %d", resp.StatusCode)
	}
}

func TestTruncatedUploadLeavesNoSnapshot(t *testing.T) {
	e := newEnv(t)
	key := "srv/truncated.tar.gz"
	arch := makeArchive(t, baseTree(), false)
	id := e.create(key)
	parts := e.upload(key, id, arch[:len(arch)/2], 0)
	e.complete(key, id, parts) // returns fast even though the stream is broken
	b := e.waitStatus(key, store.StatusFailed)
	t.Logf("failure recorded: %.120s", b.Error)
	if s := e.snapshots(); len(s) != 0 {
		t.Fatalf("incomplete snapshot kept: %+v", s)
	}
}

func TestConcurrentBackups(t *testing.T) {
	e := newEnv(t)
	var wg sync.WaitGroup
	trees := map[string]tree{}
	for i := range 3 {
		tr := baseTree()
		tr["server-id"] = []byte(fmt.Sprint("server ", i))
		tr["world/level.dat"] = randBytes(int64(100+i), 3<<20)
		trees[fmt.Sprintf("srv%d/bk.tar.gz", i)] = tr
	}
	archives := map[string][]byte{}
	for k, tr := range trees {
		archives[k] = makeArchive(t, tr, true)
	}
	for k, a := range archives {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.backup(k, a)
		}()
	}
	wg.Wait()
	for k, tr := range trees {
		_, body := e.download(k)
		equalTrees(t, tr, untar(t, bytes.NewReader(body)))
	}
	if n := len(e.snapshots()); n != 3 {
		t.Fatalf("snapshots: %d", n)
	}
}

func TestAuthRejected(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	bad := e.client("wrong-secret")
	_, err := bad.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String("a/b.tar.gz")})
	if apiCode(err) != "SignatureDoesNotMatch" {
		t.Fatalf("bad secret: %v", err)
	}
	// Presigned URL signed with the wrong secret, and a tampered one.
	key := "a/b.tar.gz"
	id := e.create(key)
	badPS, _ := s3.NewPresignClient(bad).PresignUploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(id), PartNumber: aws.Int32(1),
	})
	if resp, _ := wingsPut(badPS.URL, []byte("x")); resp.StatusCode != 403 {
		t.Fatalf("bad presign: %d", resp.StatusCode)
	}
	goodPS, _ := e.pre.PresignUploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(id), PartNumber: aws.Int32(1),
	})
	u, _ := url.Parse(goodPS.URL)
	q := u.Query()
	q.Set("partNumber", "2")
	u.RawQuery = q.Encode()
	if resp, _ := wingsPut(u.String(), []byte("x")); resp.StatusCode != 403 {
		t.Fatalf("tampered presign: %d", resp.StatusCode)
	}
	// Anonymous request, wrong bucket, healthz without auth.
	if resp, _ := http.Get(e.srv.URL + "/" + bucket + "/" + key); resp.StatusCode != 403 {
		t.Fatalf("anonymous: %d", resp.StatusCode)
	}
	if resp, _ := http.Get(e.srv.URL + "/healthz"); resp.StatusCode != 200 {
		t.Fatalf("healthz: %d", resp.StatusCode)
	}
	_, err = e.panel.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String("other"), Key: aws.String(key)})
	if err == nil {
		t.Fatal("wrong bucket accepted")
	}
}

func (e *env) presignPart(key, uploadID string, n int32) string {
	ps, err := e.pre.PresignUploadPart(context.Background(), &s3.UploadPartInput{
		Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID), PartNumber: aws.Int32(n),
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return ps.URL
}

func errBody(resp *http.Response) string {
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return string(b)
}

func TestResourceLimits(t *testing.T) {
	e := newEnv(t)
	key := "srv/limits.tar.gz"
	id := e.create(key)
	url := e.presignPart(key, id, 1)

	// Part size cap.
	e.api.MaxPartBytes = 1000
	resp, err := wingsPut(url, make([]byte, 2000))
	if err != nil {
		t.Fatal(err)
	}
	if body := errBody(resp); resp.StatusCode != 400 || !strings.Contains(body, "EntityTooLarge") {
		t.Fatalf("oversized part: %d %s", resp.StatusCode, body)
	}
	e.api.MaxPartBytes = 0

	// Missing Content-Length (chunked).
	pr, pw := io.Pipe()
	go func() { pw.Write([]byte("abc")); pw.Close() }()
	req, _ := http.NewRequest(http.MethodPut, url, pr)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if body := errBody(resp); resp.StatusCode != 411 || !strings.Contains(body, "MissingContentLength") {
		t.Fatalf("chunked part: %d %s", resp.StatusCode, body)
	}

	// A part that can never fit into the spool: 4xx (never 5xx, Wings would retry it
	// with a half-read body) and the upload is failed.
	e.mgr.Quota.Max = 10
	resp, _ = wingsPut(url, make([]byte, 100))
	if body := errBody(resp); resp.StatusCode != 409 || !strings.Contains(body, "OperationAborted") {
		t.Fatalf("spool too small: %d %s", resp.StatusCode, body)
	}
	e.mgr.Quota.Max = 1 << 30
	resp, _ = wingsPut(url, make([]byte, 100))
	if body := errBody(resp); resp.StatusCode != 409 {
		t.Fatalf("failed upload must stay failed: %d %s", resp.StatusCode, body)
	}
	e.waitStatus(key, store.StatusFailed)
}

func TestBodyHashVerification(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	key := "srv/hash.tar.gz"
	id := e.create(key)

	// The SDK sends a signed hex payload hash over plain HTTP; this must verify.
	out, err := e.panel.UploadPart(ctx, &s3.UploadPartInput{
		Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(id), PartNumber: aws.Int32(1),
		Body: bytes.NewReader([]byte("part one")),
	})
	if err != nil || out.ETag == nil {
		t.Fatalf("SDK UploadPart: %v", err)
	}
	// A header-signed request whose declared hash does not match the body is refused.
	u := e.srv.URL + "/" + bucket + "/" + key + "?partNumber=2&uploadId=" + id
	req, _ := http.NewRequest(http.MethodPut, u, strings.NewReader("part two"))
	wrong := sha256.Sum256([]byte("something else"))
	hash := hex.EncodeToString(wrong[:])
	req.Header.Set("X-Amz-Content-Sha256", hash)
	if err := v4.NewSigner().SignHTTP(ctx, aws.Credentials{AccessKeyID: accessKey, SecretAccessKey: secretKey},
		req, hash, "s3", "us-east-1", time.Now()); err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if body := errBody(resp); resp.StatusCode != 400 || !strings.Contains(body, "XAmzContentSHA256Mismatch") {
		t.Fatalf("bad hash: %d %s", resp.StatusCode, body)
	}
	// Content-MD5 mismatch.
	req, _ = http.NewRequest(http.MethodPut, e.presignPart(key, id, 3), strings.NewReader("part three"))
	req.ContentLength = 10
	req.Header.Set("Content-MD5", "AAAAAAAAAAAAAAAAAAAAAA==")
	resp, _ = http.DefaultClient.Do(req)
	if body := errBody(resp); resp.StatusCode != 400 || !strings.Contains(body, "BadDigest") {
		t.Fatalf("bad md5: %d %s", resp.StatusCode, body)
	}
	for _, p := range e.mgrParts(id) {
		if p == 2 || p == 3 {
			t.Fatalf("rejected part %d was stored", p)
		}
	}
}

func (e *env) mgrParts(id string) []int {
	u, err := e.mgr.Get(id)
	if err != nil {
		e.t.Fatal(err)
	}
	var out []int
	for _, p := range u.Parts() {
		out = append(out, p.Number)
	}
	return out
}

func TestCompleteIdempotentAndHead(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	key := "srv/idem.tar.gz"
	arch := makeArchive(t, baseTree(), true)
	id := e.create(key)
	parts := e.upload(key, id, arch, 0)
	complete := func(ps []s3types.CompletedPart) (*s3.CompleteMultipartUploadOutput, error) {
		return e.panel.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
			Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(id),
			MultipartUpload: &s3types.CompletedMultipartUpload{Parts: ps},
		})
	}
	first, err := complete(parts)
	if err != nil {
		t.Fatal(err)
	}
	// HEAD waits for the ingest like GET does.
	ps, _ := e.pre.PresignHeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	req, _ := http.NewRequest(http.MethodHead, ps.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("HEAD: %v %v", err, resp)
	}
	if b, _ := e.st.BackupByKey(ctx, key); b.Status != store.StatusComplete {
		t.Fatalf("HEAD returned before the ingest finished: %s", b.Status)
	}
	// Same list again (the pipeline is long gone): same answer.
	again, err := complete(parts)
	if err != nil || *again.ETag != *first.ETag {
		t.Fatalf("retried Complete: %v", err)
	}
	if _, err := complete(parts[:1]); apiCode(err) != "InvalidPart" {
		t.Fatalf("different list: %v", err)
	}
}

func TestDeleteOpenUploadAndMissingSnapshot(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	key := "srv/open.tar.gz"
	id := e.create(key)
	if resp, err := wingsPut(e.presignPart(key, id, 1), []byte("partial")); err != nil || resp.StatusCode != 200 {
		t.Fatalf("part: %v %v", err, resp)
	}
	if _, err := e.panel.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); err != nil {
		t.Fatalf("DeleteObject on open upload: %v", err)
	}
	if _, err := e.mgr.Get(id); err == nil {
		t.Fatal("upload still running after delete")
	}
	if _, err := e.st.BackupByKey(ctx, key); err == nil {
		t.Fatal("backup still visible")
	}
	if err := e.rc.Forget(ctx, "deadbeef"); err != nil {
		t.Fatalf("forget of unknown snapshot: %v", err)
	}
}

func TestDeleteDuringMaintenanceAsksToRetry(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	key := "srv/maint.tar.gz"
	e.backup(key, makeArchive(t, tree{"a.txt": []byte("hello")}, false))
	e.api.DumpWait = 200 * time.Millisecond

	e.mgr.Gate.Pause() // prune is running
	_, err := e.panel.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err == nil || !strings.Contains(err.Error(), "SlowDown") {
		t.Fatalf("delete during maintenance: %v", err)
	}
	if len(e.snapshots()) != 1 {
		t.Fatal("snapshot must survive the refused delete")
	}
	e.mgr.Gate.Resume()
	e.api.DumpWait = time.Minute // the real forget must fit
	if _, err := e.panel.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}); err != nil {
		t.Fatalf("delete after maintenance: %v", err)
	}
	if len(e.snapshots()) != 0 {
		t.Fatal("snapshot not forgotten")
	}
}
