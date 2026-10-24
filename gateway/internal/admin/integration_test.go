package admin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"log/slog"
	"math/rand"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ptero-enhanced-backup/gateway/internal/ingest"
	"ptero-enhanced-backup/gateway/internal/maintenance"
	"ptero-enhanced-backup/gateway/internal/restic"
	"ptero-enhanced-backup/gateway/internal/store"
)

type realEnv struct {
	srv  *Server
	mgr  *ingest.Manager
	rc   *restic.Client
	st   *store.Store
	svc  *maintenance.Service
	data map[string][]byte
}

type semDumps struct{ n int }

func (d *semDumps) AcquireDump(ctx context.Context) (func(), error) {
	d.n++
	return func() { d.n-- }, nil
}
func (d *semDumps) ActiveDumps() int { return d.n }

func newRealEnv(t *testing.T) *realEnv {
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
	mgr := ingest.NewManager(st, rc, filepath.Join(dir, "spool"), log)
	if err := mgr.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		mgr.Shutdown(ctx)
		st.Close()
	})
	svc := &maintenance.Service{
		Store: st, Backend: rc, Gate: mgr.Gate, Log: log,
		Cfg: maintenance.Config{Hour: 2, PruneEnabled: true, CheckInterval: time.Hour, ReadDataSubset: "100%", BusyWait: 30 * time.Second},
	}
	srv := &Server{
		Token: token, Store: st, Restic: rc, Maint: svc, Dumps: &semDumps{}, Ingest: mgr,
		SpoolUsed: mgr.Quota.Used, Log: log, Repo: rc.Repo, Started: time.Now(),
	}
	return &realEnv{srv: srv, mgr: mgr, rc: rc, st: st, svc: svc}
}

// backupTar stores a tar.gz of files through the real ingest pipeline.
func (e *realEnv) backupTar(t *testing.T, server, backup string, files map[string][]byte) {
	t.Helper()
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sortStrings(names)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, n := range names {
		tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(files[n])), ModTime: time.Unix(1_700_000_000, 0)})
		tw.Write(files[n])
	}
	tw.Close()
	gz.Close()
	archive := buf.Bytes()

	ctx := context.Background()
	u, err := e.mgr.Create(ctx, server+"/"+backup+".tar.gz", server, backup)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.PutPart(1, bytes.NewReader(archive)); err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum(archive)
	if _, err := e.mgr.Complete(ctx, u.ID, u.Key, []ingest.PartRef{{Number: 1, ETag: hex.EncodeToString(sum[:])}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.mgr.Done(u.ID):
	case <-time.After(2 * time.Minute):
		t.Fatal("ingest timeout")
	}
	if b, err := e.st.BackupByID(ctx, u.BackupID); err != nil || b.Status != store.StatusComplete {
		t.Fatalf("backup: %+v %v", b, err)
	}
}

func sortStrings(s []string) {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

func (e *realEnv) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", Prefix+path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	e.srv.ServeHTTP(rec, req)
	return rec
}

func rnd(seed int64, n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func TestRealRestic_SingleFileDownload(t *testing.T) {
	e := newRealEnv(t)
	files := map[string][]byte{
		"server.properties":      []byte("motd=hello\n"),
		"world/level.dat":        rnd(1, 3<<20),
		"world/region/r.0.0.mca": rnd(2, 5<<20),
		"plugins/zz.jar":         rnd(3, 1<<20),
	}
	e.backupTar(t, "srv-1", "bk-1", files)

	for name, want := range files {
		rec := e.get(t, "/backups/bk-1/file?path="+name)
		if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), want) {
			t.Fatalf("%s: status %d, %d bytes (want %d)", name, rec.Code, rec.Body.Len(), len(want))
		}
	}
	if rec := e.get(t, "/backups/bk-1/file?path=world/missing.dat"); rec.Code != 404 {
		t.Fatalf("missing file: %d", rec.Code)
	}
	if rec := e.get(t, "/backups/bk-1/file?path=world"); rec.Code != 404 && rec.Code != 400 {
		t.Fatalf("dir: %d", rec.Code)
	}
	// The index written by the real pipeline drives the browser.
	m := decode(t, e.get(t, "/backups/bk-1/files?path=world"), 200)
	if m["total"] != 2.0 {
		t.Fatalf("world listing: %v", m)
	}
}

func TestRealRestic_MaintenanceRun(t *testing.T) {
	e := newRealEnv(t)
	ctx := context.Background()
	e.backupTar(t, "srv-1", "bk-1", map[string][]byte{"a.bin": rnd(1, 2<<20), "b.txt": []byte("one")})
	e.backupTar(t, "srv-1", "bk-2", map[string][]byte{"a.bin": rnd(5, 2<<20), "b.txt": []byte("two")})
	// Deleting a backup frees nothing until prune runs.
	b1, err := e.st.BackupByUUID(ctx, "bk-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.rc.Forget(ctx, b1.SnapshotID); err != nil {
		t.Fatal(err)
	}
	before, err := e.rc.RawStats(ctx)
	if err != nil {
		t.Fatal(err)
	}

	ids, err := e.svc.Trigger([]string{maintenance.JobPrune, maintenance.JobCheck, maintenance.JobStats})
	if err != nil || len(ids) != 3 {
		t.Fatal(ids, err)
	}
	e.svc.Wait()

	jobs, err := e.st.Jobs(ctx, 10)
	if err != nil || len(jobs) != 3 {
		t.Fatalf("jobs: %v %v", jobs, err)
	}
	for _, j := range jobs {
		if j.Result != store.JobOK || j.FinishedAt.IsZero() {
			t.Fatalf("job %s: %+v", j.Type, j)
		}
	}
	prune := jobs[2]
	if prune.Type != "prune" || prune.BytesFreed == nil || *prune.BytesFreed <= 0 {
		t.Fatalf("prune job should report freed bytes: %+v", prune)
	}
	if !strings.Contains(jobs[1].Message, "100%") {
		t.Fatalf("check message: %q", jobs[1].Message)
	}
	rs, err := e.st.LatestRepoStat(ctx)
	if err != nil || rs.SnapshotsCount != 1 || rs.TotalSize <= 0 {
		t.Fatalf("repo stats after prune: %+v (before %+v) %v", rs, before, err)
	}
	if e.mgr.Gate.Paused() {
		t.Fatal("gate left paused")
	}

	// And through the API.
	m := decode(t, e.get(t, "/maintenance"), 200)
	if len(m["items"].([]any)) != 3 {
		t.Fatalf("api jobs: %v", m)
	}
	sum := decode(t, e.get(t, "/summary"), 200)
	if sum["stored_bytes"] != float64(rs.TotalSize) {
		t.Fatalf("summary: %v", sum)
	}
}
