package ingest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"ptero-enhanced-backup/gateway/internal/restic"
	"ptero-enhanced-backup/gateway/internal/store"
)

func makeTarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"world/", "world/level.dat", "server.properties", "link"} {
		var h tar.Header
		switch name {
		case "world/":
			h = tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o755}
		case "link":
			h = tar.Header{Name: name, Typeflag: tar.TypeSymlink, Linkname: "server.properties"}
		default:
			data := files[name]
			h = tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(data)), ModTime: time.Unix(1700000000, 0)}
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write(files[name])
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestIndexTar(t *testing.T) {
	data := map[string][]byte{"world/level.dat": []byte("0123456789"), "server.properties": []byte("a=b")}
	gz, _ := gzip.NewReader(bytes.NewReader(makeTarGz(t, data)))
	entries, _, err := indexTar(gz, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("entries: %+v", entries)
	}
	byPath := map[string]store.FileEntry{}
	for _, e := range entries {
		byPath[e.Path] = e
	}
	if e := byPath["world/level.dat"]; e.Type != "f" || e.Size != 10 || e.Mode != 0o644 || e.MTime.Unix() != 1700000000 {
		t.Fatalf("level.dat: %+v", e)
	}
	if byPath["world/"].Type != "d" || byPath["link"].Type != "l" {
		t.Fatalf("types: %+v", byPath)
	}
}

func TestIndexTarRejectsGarbage(t *testing.T) {
	if _, _, err := indexTar(bytes.NewReader(bytes.Repeat([]byte("not a tar header "), 100)), 0); err == nil {
		t.Fatal("expected error")
	}
}

func etagOf(b string) string { s := md5.Sum([]byte(b)); return hex.EncodeToString(s[:]) }

func testUpload(t *testing.T) *Upload {
	t.Helper()
	u := newUpload("up1", "s/b.tar.gz", "s", "b", t.TempDir(), nil)
	if err := os.MkdirAll(u.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return u
}

func TestPartRetryAndOrdering(t *testing.T) {
	u := testUpload(t)
	if _, err := u.PutPart(0, bytes.NewReader(nil)); !errors.Is(err, ErrBadPartNumber) {
		t.Fatalf("part 0: %v", err)
	}

	// Not yet consumed: a retry overwrites.
	if _, err := u.PutPart(1, bytes.NewReader([]byte("old"))); err != nil {
		t.Fatal(err)
	}
	etag, err := u.PutPart(1, bytes.NewReader([]byte("new1")))
	if err != nil || etag != etagOf("new1") {
		t.Fatalf("overwrite: %v %s", err, etag)
	}
	// Part 3 arrives before part 2.
	u.PutPart(3, bytes.NewReader([]byte("ccc")))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rd := &partReader{u: u, ctx: ctx}
	got := make([]byte, 4)
	if _, err := io.ReadFull(rd, got); err != nil || string(got) != "new1" {
		t.Fatalf("read part 1: %q %v", got, err)
	}

	// The reader must now block on the missing part 2, then continue in order.
	type res struct {
		b   []byte
		err error
	}
	out := make(chan res, 1)
	go func() { b, err := io.ReadAll(rd); out <- res{b, err} }()
	select {
	case r := <-out:
		t.Fatalf("reader ran past the gap: %q %v", r.b, r.err)
	case <-time.After(100 * time.Millisecond):
	}
	u.PutPart(2, bytes.NewReader([]byte("bb")))

	// Consumed part: identical retry is accepted, different content is refused.
	if e, err := u.PutPart(1, bytes.NewReader([]byte("new1"))); err != nil || e != etagOf("new1") {
		t.Fatalf("idempotent retry: %v %s", err, e)
	}
	if _, err := u.PutPart(1, bytes.NewReader([]byte("other"))); !errors.Is(err, ErrPartConflict) {
		t.Fatalf("conflicting retry: %v", err)
	}

	refs := []PartRef{{1, `"` + etagOf("new1") + `"`}, {2, etagOf("bb")}, {3, etagOf("ccc")}}
	mp, err := u.Complete(refs)
	if err != nil {
		t.Fatal(err)
	}
	if mp[len(mp)-2:] != "-3" {
		t.Fatalf("multipart etag %q", mp)
	}
	r := <-out
	if r.err != nil || string(r.b) != "bbccc" {
		t.Fatalf("stream tail: %q %v", r.b, r.err)
	}
	// Consumed spool files are gone.
	if ents, _ := os.ReadDir(u.dir); len(ents) != 0 {
		t.Fatalf("spool not cleaned: %v", ents)
	}
	if _, err := u.PutPart(4, bytes.NewReader([]byte("x"))); !errors.Is(err, ErrNoSuchUpload) {
		t.Fatalf("part after complete: %v", err)
	}
}

func TestCompleteValidation(t *testing.T) {
	mk := func() *Upload {
		u := testUpload(t)
		u.PutPart(1, bytes.NewReader([]byte("a")))
		u.PutPart(2, bytes.NewReader([]byte("b")))
		u.PutPart(4, bytes.NewReader([]byte("d")))
		return u
	}
	cases := []struct {
		name string
		refs []PartRef
		want error
	}{
		{"empty", nil, ErrInvalidPart},
		{"wrong etag", []PartRef{{1, etagOf("zzz")}}, ErrInvalidPart},
		{"missing part", []PartRef{{1, etagOf("a")}, {2, etagOf("b")}, {3, etagOf("c")}}, ErrInvalidPart},
		{"descending", []PartRef{{2, etagOf("b")}, {1, etagOf("a")}}, ErrInvalidOrder},
		{"gap", []PartRef{{1, etagOf("a")}, {2, etagOf("b")}, {4, etagOf("d")}}, ErrInvalidPart},
	}
	for _, c := range cases {
		if _, err := mk().Complete(c.refs); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v want %v", c.name, err, c.want)
		}
	}
	// Extra, unlisted, unconsumed parts are dropped.
	u := mk()
	if _, err := u.Complete([]PartRef{{1, etagOf("a")}, {2, etagOf("b")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(u.partPath(4)); !os.IsNotExist(err) {
		t.Fatalf("extra part still spooled: %v", err)
	}
}

// fakeBackend drains the stream and records what it saw.
type fakeBackend struct {
	mu       sync.Mutex
	got      bytes.Buffer
	opts     restic.BackupOptions
	forgot   []string
	snapshot string
	failWith error
	failFast bool // fail after the first read instead of at end of stream
	stall    bool // block after the first read until cancelled
	ctxErr   chan error
}

func (f *fakeBackend) Backup(ctx context.Context, r io.Reader, o restic.BackupOptions) (restic.Summary, error) {
	f.mu.Lock()
	f.opts = o
	f.mu.Unlock()
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		f.mu.Lock()
		f.got.Write(buf[:n])
		f.mu.Unlock()
		if n > 0 && f.stall {
			<-ctx.Done()
			return restic.Summary{}, ctx.Err()
		}
		if n > 0 && f.failFast {
			return restic.Summary{SnapshotID: f.snapshot}, f.failWith
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			if f.ctxErr != nil {
				f.ctxErr <- ctx.Err()
			}
			return restic.Summary{SnapshotID: f.snapshot}, err
		}
	}
	if f.failWith != nil {
		return restic.Summary{SnapshotID: f.snapshot}, f.failWith
	}
	return restic.Summary{SnapshotID: "snap1", DataAdded: 123, TotalBytesProcessed: int64(f.got.Len())}, nil
}

func (f *fakeBackend) Forget(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgot = append(f.forgot, id)
	return nil
}

func (f *fakeBackend) received() int { f.mu.Lock(); defer f.mu.Unlock(); return f.got.Len() }

func newTestManager(t *testing.T, be Backend) *Manager {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := NewManager(st, be, filepath.Join(dir, "spool"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := m.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Shutdown(context.Background()) })
	return m
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(42)).Read(b)
	return b
}

func split(b []byte, size int) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		n := min(size, len(b))
		out = append(out, b[:n])
		b = b[n:]
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestPipelineStreamsWhilePartsArrive(t *testing.T) {
	be := &fakeBackend{}
	m := newTestManager(t, be)
	ctx := context.Background()
	archive := makeTarGz(t, map[string][]byte{"world/level.dat": randomBytes(700 << 10), "server.properties": []byte("a=b")})
	parts := split(archive, 150<<10)
	if len(parts) < 4 {
		t.Fatalf("test needs several parts, got %d", len(parts))
	}

	u, err := m.Create(ctx, "srv/bk.tar.gz", "srv", "bk")
	if err != nil {
		t.Fatal(err)
	}
	// Upload out of order except that part 1 comes first.
	order := []int{1, 3, 2}
	for i := 4; i <= len(parts); i++ {
		order = append(order, i)
	}
	var refs []PartRef
	for i, p := range parts {
		refs = append(refs, PartRef{i + 1, etagOf(string(p))})
	}
	if _, err := u.PutPart(1, bytes.NewReader(parts[0])); err != nil {
		t.Fatal(err)
	}
	// restic already receives data although the upload is far from complete.
	waitFor(t, "early data", func() bool { return be.received() > 0 })
	for _, n := range order[1:] {
		if _, err := u.PutPart(n, bytes.NewReader(parts[n-1])); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Complete(ctx, u.ID, u.Key, refs); err != nil {
		t.Fatal(err)
	}
	<-u.done

	b, err := m.Store.BackupByID(ctx, u.BackupID)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != store.StatusComplete || b.SnapshotID != "snap1" || b.DataAdded != 123 {
		t.Fatalf("backup: %+v", b)
	}
	if b.CompressedBytes != int64(len(archive)) || b.UncompressedBytes <= 700<<10 || b.FileCount != 4 {
		t.Fatalf("metrics: %+v", b)
	}
	files, _ := m.Store.Files(ctx, b.ID)
	if len(files) != 4 {
		t.Fatalf("files: %+v", files)
	}
	// Restic saw the decompressed tar, tagged correctly.
	be.mu.Lock()
	defer be.mu.Unlock()
	tr := tar.NewReader(bytes.NewReader(be.got.Bytes()))
	h, err := tr.Next()
	if err != nil || h.Name != "world/" {
		t.Fatalf("stdin is not the tar: %v %v", h, err)
	}
	if be.opts.Filename != "srv.tar" || be.opts.Tags[0] != "server:srv" || be.opts.Tags[1] != "backup:bk" {
		t.Fatalf("opts: %+v", be.opts)
	}
	if ents, _ := os.ReadDir(m.SpoolDir); len(ents) != 0 {
		t.Fatalf("spool not empty: %v", ents)
	}
	if m.IsIngesting(u.ID) {
		t.Fatal("upload still registered")
	}
}

func TestAbortStopsPipelineAndForgetsSnapshot(t *testing.T) {
	be := &fakeBackend{ctxErr: make(chan error, 1), snapshot: "leftover"}
	m := newTestManager(t, be)
	ctx := context.Background()
	archive := makeTarGz(t, map[string][]byte{"world/level.dat": randomBytes(300 << 10)})
	parts := split(archive, 100<<10)

	u, _ := m.Create(ctx, "srv/bk.tar.gz", "srv", "bk")
	u.PutPart(1, bytes.NewReader(parts[0]))
	waitFor(t, "data", func() bool { return be.received() > 0 })
	if err := m.Abort(u.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-u.done:
	default:
		t.Fatal("abort returned before the pipeline ended")
	}
	b, _ := m.Store.BackupByID(ctx, u.BackupID)
	if b.Status != store.StatusFailed || b.Error != "aborted" || b.SnapshotID != "" {
		t.Fatalf("backup after abort: %+v", b)
	}
	be.mu.Lock()
	forgot := append([]string(nil), be.forgot...)
	be.mu.Unlock()
	if len(forgot) != 1 || forgot[0] != "leftover" {
		t.Fatalf("snapshot not forgotten: %v", forgot)
	}
	if _, err := m.Get(u.ID); !errors.Is(err, ErrNoSuchUpload) {
		t.Fatalf("upload still known: %v", err)
	}
	if err := m.Abort(u.ID); !errors.Is(err, ErrNoSuchUpload) {
		t.Fatalf("second abort: %v", err)
	}
	if ents, _ := os.ReadDir(m.SpoolDir); len(ents) != 0 {
		t.Fatalf("spool not empty: %v", ents)
	}
}

func TestBackendFailureFailsUpload(t *testing.T) {
	be := &fakeBackend{failWith: errors.New("repository is full"), snapshot: "partial", failFast: true}
	m := newTestManager(t, be)
	ctx := context.Background()
	archive := makeTarGz(t, map[string][]byte{"server.properties": []byte("a=b")})

	u, _ := m.Create(ctx, "srv/bk.tar.gz", "srv", "bk")
	u.PutPart(1, bytes.NewReader(archive))
	<-u.done
	b, _ := m.Store.BackupByID(ctx, u.BackupID)
	if b.Status != store.StatusFailed {
		t.Fatalf("backup: %+v", b)
	}
	be.mu.Lock()
	forgot := be.forgot
	be.mu.Unlock()
	if len(forgot) != 1 || forgot[0] != "partial" {
		t.Fatalf("incomplete snapshot kept: %v", forgot)
	}
	// Further parts and Complete report the failure.
	if _, err := u.PutPart(2, bytes.NewReader([]byte("x"))); !errors.Is(err, ErrPipelineFailed) {
		t.Fatalf("put after failure: %v", err)
	}
	if _, err := m.Complete(ctx, u.ID, u.Key, []PartRef{{1, etagOf(string(archive))}}); !errors.Is(err, ErrPipelineFailed) {
		t.Fatalf("complete after failure: %v", err)
	}
}

func TestCorruptGzipFails(t *testing.T) {
	be := &fakeBackend{}
	m := newTestManager(t, be)
	ctx := context.Background()
	archive := makeTarGz(t, map[string][]byte{"world/level.dat": randomBytes(50 << 10)})
	cut := archive[:len(archive)/2] // truncated upload

	u, _ := m.Create(ctx, "srv/bk.tar.gz", "srv", "bk")
	u.PutPart(1, bytes.NewReader(cut))
	if _, err := m.Complete(ctx, u.ID, u.Key, []PartRef{{1, etagOf(string(cut))}}); err != nil {
		t.Fatal(err) // Complete itself is fast and does not wait for the pipeline
	}
	<-u.done
	b, _ := m.Store.BackupByID(ctx, u.BackupID)
	if b.Status != store.StatusFailed || b.Error == "" {
		t.Fatalf("backup: %+v", b)
	}
}

func TestIndexTarCap(t *testing.T) {
	data := map[string][]byte{"world/level.dat": []byte("0123456789"), "server.properties": []byte("a=b")}
	gz, _ := gzip.NewReader(bytes.NewReader(makeTarGz(t, data)))
	entries, truncated, err := indexTar(gz, 2)
	if err != nil || !truncated || len(entries) != 2 {
		t.Fatalf("%v %v %d", err, truncated, len(entries))
	}
}

func TestPutOptionsVerification(t *testing.T) {
	u := testUpload(t)
	body := "hello"
	sha := sha256.Sum256([]byte(body))
	md := md5.Sum([]byte(body))
	good := PutOptions{Size: int64(len(body)), SHA256: hex.EncodeToString(sha[:]), MD5: base64.StdEncoding.EncodeToString(md[:])}
	if _, err := u.PutPartOpts(1, bytes.NewReader([]byte(body)), good); err != nil {
		t.Fatal(err)
	}
	bad := good
	bad.SHA256 = hex.EncodeToString(make([]byte, 32))
	if _, err := u.PutPartOpts(2, bytes.NewReader([]byte(body)), bad); !errors.Is(err, ErrBadSHA256) {
		t.Fatalf("sha: %v", err)
	}
	bad = good
	bad.MD5 = base64.StdEncoding.EncodeToString(make([]byte, 16))
	if _, err := u.PutPartOpts(3, bytes.NewReader([]byte(body)), bad); !errors.Is(err, ErrBadDigest) {
		t.Fatalf("md5: %v", err)
	}
	if got := u.Parts(); len(got) != 1 {
		t.Fatalf("rejected parts were stored: %+v", got)
	}
}

func TestSpoolQuota(t *testing.T) {
	q := &Quota{Max: 10}
	u := newUpload("up", "s/b.tar.gz", "s", "b", t.TempDir(), q)
	os.MkdirAll(u.dir, 0o755)
	if _, err := u.PutPartOpts(1, bytes.NewReader(make([]byte, 6)), PutOptions{Size: 6}); err != nil {
		t.Fatal(err)
	}
	if _, err := u.PutPartOpts(2, bytes.NewReader(make([]byte, 6)), PutOptions{Size: 6}); !errors.Is(err, ErrSpoolFull) {
		t.Fatalf("expected quota error, got %v", err)
	}
	if q.Used() != 6 {
		t.Fatalf("used %d", q.Used())
	}
	// Overwriting an unconsumed part frees the old bytes.
	if _, err := u.PutPartOpts(1, bytes.NewReader(make([]byte, 4)), PutOptions{Size: 4}); err != nil || q.Used() != 4 {
		t.Fatalf("overwrite: %v used=%d", err, q.Used())
	}
	// Consuming a part frees its quota.
	u.PutPartOpts(2, bytes.NewReader(make([]byte, 5)), PutOptions{Size: 5})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rd := &partReader{u: u, ctx: ctx}
	io.ReadFull(rd, make([]byte, 4))
	rd.closeCur()
	if q.Used() != 5 {
		t.Fatalf("after consume: %d", q.Used())
	}
	u.releaseAll()
	if q.Used() != 0 {
		t.Fatalf("after release: %d", q.Used())
	}
}

func TestCompleteIdempotent(t *testing.T) {
	be := &fakeBackend{}
	m := newTestManager(t, be)
	ctx := context.Background()
	archive := makeTarGz(t, map[string][]byte{"server.properties": []byte("a=b")})
	u, _ := m.Create(ctx, "srv/bk.tar.gz", "srv", "bk")
	u.PutPart(1, bytes.NewReader(archive))
	refs := []PartRef{{1, etagOf(string(archive))}}
	e1, err := m.Complete(ctx, u.ID, u.Key, refs)
	if err != nil {
		t.Fatal(err)
	}
	<-u.done
	waitFor(t, "unregistered", func() bool { return !m.IsIngesting(u.ID) })
	// Pipeline is gone: the answer now comes from the store.
	e2, err := m.Complete(ctx, u.ID, u.Key, refs)
	if err != nil || e2 != e1 {
		t.Fatalf("retry: %v %q vs %q", err, e2, e1)
	}
	if _, err := m.Complete(ctx, u.ID, u.Key, []PartRef{{1, etagOf("other")}}); !errors.Is(err, ErrInvalidPart) {
		t.Fatalf("different list: %v", err)
	}
	if _, err := m.Complete(ctx, u.ID, "srv/other.tar.gz", refs); !errors.Is(err, ErrNoSuchUpload) {
		t.Fatalf("wrong key: %v", err)
	}
}

func TestSlotsLimitConcurrentIngests(t *testing.T) {
	be := &fakeBackend{}
	m := newTestManager(t, be)
	m.Configure(Limits{MaxIngests: 1, IngestTimeout: time.Hour})
	ctx := context.Background()
	archive := makeTarGz(t, map[string][]byte{"world/level.dat": randomBytes(300 << 10)})
	parts := split(archive, 100<<10)

	u1, _ := m.Create(ctx, "s1/b.tar.gz", "s1", "b")
	u1.PutPart(1, bytes.NewReader(parts[0]))
	waitFor(t, "first ingest running", func() bool { return be.received() > 0 })
	u2, _ := m.Create(ctx, "s2/b.tar.gz", "s2", "b")
	// Parts of the queued upload are still accepted and spooled.
	if _, err := u2.PutPart(1, bytes.NewReader(parts[0])); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if ps := u2.Parts(); len(ps) != 1 {
		t.Fatal("part not kept")
	}
	if _, err := os.Stat(u2.partPath(1)); err != nil {
		t.Fatalf("queued part was consumed: %v", err)
	}
	for i := 1; i < len(parts); i++ {
		u1.PutPart(i+1, bytes.NewReader(parts[i]))
	}
	var refs []PartRef
	for i, p := range parts {
		refs = append(refs, PartRef{i + 1, etagOf(string(p))})
	}
	m.Complete(ctx, u1.ID, u1.Key, refs)
	<-u1.done
	for i := 1; i < len(parts); i++ {
		u2.PutPart(i+1, bytes.NewReader(parts[i]))
	}
	m.Complete(ctx, u2.ID, u2.Key, refs)
	<-u2.done
	for _, u := range []*Upload{u1, u2} {
		if b, _ := m.Store.BackupByID(ctx, u.BackupID); b.Status != store.StatusComplete {
			t.Fatalf("backup %d: %+v", u.BackupID, b)
		}
	}
}

func TestIngestTimeout(t *testing.T) {
	be := &fakeBackend{stall: true}
	m := newTestManager(t, be)
	m.Configure(Limits{MaxIngests: 1, IngestTimeout: 200 * time.Millisecond})
	ctx := context.Background()
	archive := makeTarGz(t, map[string][]byte{"server.properties": []byte("a=b")})
	u, _ := m.Create(ctx, "s/b.tar.gz", "s", "b")
	u.PutPart(1, bytes.NewReader(archive))
	if _, err := m.Complete(ctx, u.ID, u.Key, []PartRef{{1, etagOf(string(archive))}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-u.done:
	case <-time.After(10 * time.Second):
		t.Fatal("deadline did not stop the ingest")
	}
	b, _ := m.Store.BackupByID(ctx, u.BackupID)
	if b.Status != store.StatusFailed || b.Error != "ingest timeout exceeded" {
		t.Fatalf("backup: %+v", b)
	}
}

func TestGatePausesNewPipelines(t *testing.T) {
	be := &fakeBackend{}
	m := newTestManager(t, be)
	ctx := context.Background()
	var done []DoneEvent
	var dmu sync.Mutex
	m.OnDone = func(ev DoneEvent) { dmu.Lock(); done = append(done, ev); dmu.Unlock() }

	m.Gate.Pause() // maintenance wants the repository
	archive := makeTarGz(t, map[string][]byte{"world/level.dat": randomBytes(10 << 10), "server.properties": []byte("a=b")})
	u, err := m.Create(ctx, "srv/bk.tar.gz", "srv", "bk")
	if err != nil {
		t.Fatal(err)
	}
	// Uploads and Complete keep working while the pipeline waits.
	if _, err := u.PutPart(1, bytes.NewReader(archive)); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Complete(ctx, u.ID, u.Key, []PartRef{{1, etagOf(string(archive))}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "queued", func() bool { r, q := m.Load(); return r == 0 && q == 1 })
	time.Sleep(150 * time.Millisecond)
	if be.received() != 0 {
		t.Fatal("restic received data while paused")
	}
	if b, _ := m.Store.BackupByID(ctx, u.BackupID); b.Status != store.StatusPending {
		t.Fatalf("status while paused: %s", b.Status)
	}

	m.Gate.Resume()
	<-u.done
	if b, _ := m.Store.BackupByID(ctx, u.BackupID); b.Status != store.StatusComplete {
		t.Fatalf("status after resume: %s", b.Status)
	}
	if r, q := m.Load(); r != 0 || q != 0 || m.Gate.Active() != 0 {
		t.Fatalf("load after finish: %d %d %d", r, q, m.Gate.Active())
	}
	dmu.Lock()
	defer dmu.Unlock()
	if len(done) != 1 || !done[0].OK || done[0].ServerUUID != "srv" || done[0].BackupUUID != "bk" {
		t.Fatalf("OnDone: %+v", done)
	}
}

func TestOnDoneReportsFailureAndTimeout(t *testing.T) {
	be := &fakeBackend{stall: true}
	m := newTestManager(t, be)
	m.Configure(Limits{MaxSpoolBytes: 1 << 30, MaxIngests: 1, IngestTimeout: 300 * time.Millisecond})
	evs := make(chan DoneEvent, 1)
	m.OnDone = func(ev DoneEvent) { evs <- ev }
	archive := makeTarGz(t, map[string][]byte{"world/level.dat": randomBytes(10 << 10)})
	u, err := m.Create(context.Background(), "srv/bk.tar.gz", "srv", "bk")
	if err != nil {
		t.Fatal(err)
	}
	u.PutPart(1, bytes.NewReader(archive))
	if _, err := m.Complete(context.Background(), u.ID, u.Key, []PartRef{{1, etagOf(string(archive))}}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-evs:
		if ev.OK || !ev.TimedOut {
			t.Fatalf("event: %+v", ev)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no event")
	}
}
