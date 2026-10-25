package admin

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ptero-enhanced-backup/gateway/internal/maintenance"
	"ptero-enhanced-backup/gateway/internal/offsite"
	"ptero-enhanced-backup/gateway/internal/store"
)

const token = "s3cret-admin-token"

var (
	utc  = time.UTC
	base = time.Date(2026, 10, 24, 12, 0, 0, 0, utc)
)

type fakeRestic struct {
	tar      []byte
	dumps    int
	reachErr error
}

func (f *fakeRestic) Dump(ctx context.Context, snap, path string) (io.ReadCloser, error) {
	f.dumps++
	return io.NopCloser(bytes.NewReader(f.tar)), nil
}
func (f *fakeRestic) Reachable(ctx context.Context) error { return f.reachErr }

type fakeMaint struct {
	mu      sync.Mutex
	status  maintenance.Status
	busy    bool
	trigger [][]string
}

func (m *fakeMaint) Status(ctx context.Context) (maintenance.Status, error) { return m.status, nil }
func (m *fakeMaint) Trigger(jobs []string) ([]int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy {
		return nil, maintenance.ErrBusy
	}
	m.trigger = append(m.trigger, jobs)
	ids := make([]int64, len(jobs))
	for i := range ids {
		ids[i] = int64(40 + i)
	}
	return ids, nil
}

type fakeDumps struct{ err error }

func (d fakeDumps) AcquireDump(ctx context.Context) (func(), error) { return func() {}, d.err }
func (d fakeDumps) ActiveDumps() int                                { return 1 }

type fakeIngest struct{}

func (fakeIngest) Load() (int, int) { return 2, 3 }

type fixture struct {
	srv   *Server
	st    *store.Store
	rs    *fakeRestic
	maint *fakeMaint
	dumps *fakeDumps
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &fixture{st: st, rs: &fakeRestic{}, maint: &fakeMaint{}, dumps: &fakeDumps{}}
	f.maint.status = maintenance.Status{PruneEnabled: true, NextWindow: base.Add(14 * time.Hour)}
	f.srv = &Server{
		Token: token, Store: st, Restic: f.rs, Maint: f.maint, Dumps: f.dumps, Ingest: fakeIngest{},
		SpoolUsed: func() int64 { return 123 }, SpoolLimit: 1000, Version: "test", Repo: "rest:https://user:pw@nas.example:8000/peb",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return base }, Loc: utc,
		Started: base.Add(-90 * time.Second),
	}
	return f
}

// seed stores one finished backup. data is the logical (uncompressed) size.
func (f *fixture) seed(t *testing.T, server, backup string, at time.Time, logical, received, added int64, dur time.Duration) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := f.st.CreateUpload(ctx, "up-"+backup, server+"/"+backup+".tar.gz", server, backup, at.Add(-dur))
	if err != nil {
		t.Fatal(err)
	}
	err = f.st.CompleteBackup(ctx, id, store.Finish{SnapshotID: "snap-" + backup, CompressedBytes: received, UncompressedBytes: logical,
		DataAdded: added, TotalBytesProcessed: logical, FileCount: 2, Duration: dur}, at)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *fixture) seedFailed(t *testing.T, server, backup, msg string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	id, err := f.st.CreateUpload(ctx, "up-"+backup, server+"/"+backup+".tar.gz", server, backup, at.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.FailBackup(ctx, id, msg, time.Minute, at); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	return f.do(t, "GET", path, "", token)
}

func (f *fixture) do(t *testing.T, method, path, body, tok string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, Prefix+path, strings.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder, wantCode int) map[string]any {
	t.Helper()
	if rec.Code != wantCode {
		t.Fatalf("status %d (want %d): %s", rec.Code, wantCode, rec.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("json: %v: %s", err, rec.Body.String())
	}
	return m
}

func num(t *testing.T, m map[string]any, key string) float64 {
	t.Helper()
	v, ok := m[key].(float64)
	if !ok {
		t.Fatalf("%s is not a number: %v (%v)", key, m[key], m)
	}
	return v
}

// ---- auth ----

func TestAuth(t *testing.T) {
	f := newFixture(t)
	for name, tok := range map[string]string{"missing": "", "wrong": "nope", "prefix": token[:5], "longer": token + "x"} {
		rec := f.do(t, "GET", "/status", "", tok)
		if rec.Code != 401 || rec.Header().Get("WWW-Authenticate") == "" || strings.Contains(rec.Body.String(), token) {
			t.Fatalf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	req := httptest.NewRequest("GET", Prefix+"/status", nil)
	req.Header.Set("Authorization", "Basic "+token)
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("basic auth: %d", rec.Code)
	}
	if rec := f.get(t, "/status"); rec.Code != 200 {
		t.Fatalf("right token: %d", rec.Code)
	}
	// Unknown paths need the token too, so they cannot be probed.
	if rec := f.do(t, "GET", "/nothing", "", ""); rec.Code != 401 {
		t.Fatalf("unauthenticated unknown path: %d", rec.Code)
	}
	if rec := f.get(t, "/nothing"); rec.Code != 404 {
		t.Fatalf("unknown path: %d", rec.Code)
	}
}

func TestDisabledWithoutToken(t *testing.T) {
	f := newFixture(t)
	f.srv.Token = ""
	for _, tok := range []string{"", token, "x"} {
		if rec := f.do(t, "GET", "/status", "", tok); rec.Code != 404 {
			t.Fatalf("token %q: %d", tok, rec.Code)
		}
	}
}

// ---- status / summary ----

func TestStatus(t *testing.T) {
	f := newFixture(t)
	f.maint.status.LastPrune = &store.Job{ID: 7, Type: "prune", Trigger: "schedule", StartedAt: base.Add(-10 * time.Hour),
		FinishedAt: base.Add(-9 * time.Hour), Result: "ok", Message: "prune finished"}
	f.maint.status.Running = &store.Job{ID: 8, Type: "check", Result: "running", StartedAt: base}
	rec := f.get(t, "/status")
	body := rec.Body.String()
	if strings.Contains(body, "pw") || strings.Contains(body, "user:") {
		t.Fatalf("credentials leaked: %s", body)
	}
	m := decode(t, rec, 200)
	if m["version"] != "test" || num(t, m, "uptime_seconds") != 90 {
		t.Fatalf("basics: %v", m)
	}
	repo := m["repo"].(map[string]any)
	if repo["type"] != "rest" || repo["location"] != "https://nas.example:8000/peb" || repo["reachable"] != true {
		t.Fatalf("repo: %v", repo)
	}
	ing := m["ingest"].(map[string]any)
	if ing["running"] != 2.0 || ing["queued"] != 3.0 || m["dumps"].(map[string]any)["active"] != 1.0 {
		t.Fatalf("load: %v", m)
	}
	sp := m["spool"].(map[string]any)
	if sp["used_bytes"] != 123.0 || sp["limit_bytes"] != 1000.0 {
		t.Fatalf("spool: %v", sp)
	}
	mt := m["maintenance"].(map[string]any)
	lp := mt["last_prune"].(map[string]any)
	if lp["result"] != "ok" || lp["finished_at"] != "2026-10-24T03:00:00Z" || mt["last_check"] != nil ||
		mt["next_window"] != "2026-10-25T02:00:00Z" || mt["running_job"].(map[string]any)["type"] != "check" {
		t.Fatalf("maintenance: %v", mt)
	}

	f.rs.reachErr = errors.New("down")
	f.srv.ReachableTTL = -1
	f.srv.reachAt = time.Time{}
	m = decode(t, f.get(t, "/status"), 200)
	if m["repo"].(map[string]any)["reachable"] != false {
		t.Fatalf("unreachable repo: %v", m["repo"])
	}
}

func TestReachableIsCached(t *testing.T) {
	f := newFixture(t)
	calls := 0
	f.srv.Restic = countingRestic{f.rs, &calls}
	f.get(t, "/status")
	f.get(t, "/status")
	if calls != 1 {
		t.Fatalf("reachable checks: %d", calls)
	}
}

type countingRestic struct {
	*fakeRestic
	n *int
}

func (c countingRestic) Reachable(ctx context.Context) error { *c.n++; return nil }

func TestRepoInfo(t *testing.T) {
	for in, want := range map[string][2]string{
		"/data/repo":                    {"local", "/data/repo"},
		"rest:http://u:p@host:8000/r":   {"rest", "http://host:8000/r"},
		"sftp:backup@host:/srv/restic":  {"sftp", "host:/srv/restic"},
		"s3:https://k:s@s3.example/b/p": {"s3", "https://s3.example/b/p"},
		"rclone:gdrive:peb":             {"rclone", "gdrive:peb"},
	} {
		if typ, loc := repoInfo(in); typ != want[0] || loc != want[1] {
			t.Errorf("%s: %s %s", in, typ, loc)
		}
	}
}

func TestSummary(t *testing.T) {
	f := newFixture(t)
	if m := decode(t, f.get(t, "/summary"), 200); m["backups_count"] != 0.0 || m["stored_bytes"] != nil || m["factor"] != nil ||
		m["success_rate_30d"] != nil || m["last_backup_at"] != nil {
		t.Fatalf("empty: %v", m)
	}

	f.seed(t, "srv1", "b1", base.Add(-48*time.Hour), 1000, 400, 300, 10*time.Second)
	f.seed(t, "srv1", "b2", base.Add(-24*time.Hour), 1000, 400, 20, 5*time.Second)
	f.seed(t, "srv2", "b3", base.Add(-time.Hour), 2000, 800, 500, 20*time.Second)
	f.seedFailed(t, "srv2", "b4", "restic backup: boom /internal/path", base.Add(-30*time.Minute))
	f.seedFailed(t, "srv2", "b5", "aborted", base.Add(-20*time.Minute)) // not a failure
	del := f.seed(t, "srv2", "b6", base.Add(-10*time.Minute), 9999, 9999, 9999, time.Second)
	if err := f.st.MarkDeleted(context.Background(), del); err != nil {
		t.Fatal(err)
	}
	if err := f.st.InsertRepoStat(context.Background(), store.RepoStat{Time: base.Add(-time.Hour), TotalSize: 500, TotalUncompressedSize: 900, SnapshotsCount: 3}); err != nil {
		t.Fatal(err)
	}

	m := decode(t, f.get(t, "/summary"), 200)
	want := map[string]float64{
		"backups_count": 3, "logical_bytes": 4000, "received_bytes": 1600, "data_added_total": 820, "upload_saved_bytes": 3180,
		"stored_bytes": 500, "saved_bytes": 3500, "saved_percent": 87.5, "factor": 8, "failures_30d": 1,
	}
	for k, v := range want {
		if got := num(t, m, k); got != v {
			t.Errorf("%s = %v, want %v", k, got, v)
		}
	}
	if got := num(t, m, "success_rate_30d"); got != 0.75 {
		t.Errorf("success_rate_30d = %v", got)
	}
	if m["stored_bytes_exact"] != false || m["referenced_bytes"] != 500.0 {
		t.Errorf("exactness: %v", m)
	}
	if m["last_backup_at"] != "2026-10-24T11:00:00Z" || m["stored_at"] != "2026-10-24T11:00:00Z" {
		t.Errorf("times: %v", m)
	}
}

// ---- servers / backups ----

func TestServers(t *testing.T) {
	f := newFixture(t)
	f.seed(t, "srv1", "b1", base.Add(-48*time.Hour), 1000, 400, 300, 10*time.Second)
	f.seed(t, "srv1", "b2", base.Add(-24*time.Hour), 1000, 400, 20, 20*time.Second)
	f.seed(t, "srv2", "b3", base.Add(-3*time.Hour), 2000, 800, 500, 30*time.Second)
	f.seedFailed(t, "srv2", "b4", "ingest timeout exceeded", base.Add(-time.Hour))
	f.seedFailed(t, "srv3", "b5", "restic backup: exit status 1: /home/container/data/x", base.Add(-time.Hour))

	rec := f.get(t, "/servers")
	if strings.Contains(rec.Body.String(), "/home/container") || strings.Contains(rec.Body.String(), "exit status") {
		t.Fatalf("internals leaked: %s", rec.Body.String())
	}
	m := decode(t, rec, 200)
	items := m["items"].([]any)
	if len(items) != 3 {
		t.Fatalf("items: %v", items)
	}
	s1, s2, s3 := items[0].(map[string]any), items[1].(map[string]any), items[2].(map[string]any)
	if s1["server_uuid"] != "srv1" || s1["backup_count"] != 2.0 || s1["last_status"] != "complete" || s1["last_error"] != "" ||
		s1["last_success_at"] != "2026-10-23T12:00:00Z" || s1["logical_bytes"] != 2000.0 || s1["data_added_total"] != 320.0 ||
		s1["avg_duration_ms"] != 15000.0 || s1["last_duration_ms"] != 20000.0 ||
		s1["last_backup_bytes"] != 1000.0 || s1["last_data_added"] != 20.0 {
		t.Fatalf("srv1: %v", s1)
	}
	if s2["last_status"] != "failed" || s2["last_error"] != "timeout" || s2["backup_count"] != 1.0 || s2["last_success_at"] != "2026-10-24T09:00:00Z" ||
		s2["last_backup_bytes"] != 2000.0 || s2["last_data_added"] != 500.0 {
		t.Fatalf("srv2: %v", s2)
	}
	if s3["last_status"] != "failed" || s3["last_error"] != "ingest failed" || s3["backup_count"] != 0.0 || s3["last_success_at"] != nil ||
		s3["last_backup_bytes"] != 0.0 || s3["last_data_added"] != 0.0 {
		t.Fatalf("srv3: %v", s3)
	}
}

func TestServersStoredBytes(t *testing.T) {
	f := newFixture(t)
	f.seed(t, "srv1", "b1", base.Add(-time.Hour), 1000, 400, 300, time.Second)
	f.seed(t, "srv2", "b2", base.Add(-time.Hour), 1000, 400, 300, time.Second)
	if err := f.st.SetServerStat(context.Background(), "srv1", 4096, base.Add(-30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	items := decode(t, f.get(t, "/servers"), 200)["items"].([]any)
	s1, s2 := items[0].(map[string]any), items[1].(map[string]any)
	if s1["stored_bytes"] != 4096.0 || s1["stored_at"] != "2026-10-24T11:30:00Z" {
		t.Fatalf("srv1: %v", s1)
	}
	if v, ok := s2["stored_bytes"]; !ok || v != nil {
		t.Fatalf("srv2 stored_bytes must be present and null: %v", s2)
	}
	if v, ok := s2["stored_at"]; !ok || v != nil {
		t.Fatalf("srv2 stored_at must be present and null: %v", s2)
	}
}

func TestBackups(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < 5; i++ {
		f.seed(t, "srv1", "ok"+string(rune('a'+i)), base.Add(time.Duration(-10+i)*time.Hour), 1000, 400, 100, time.Second)
	}
	f.seedFailed(t, "srv2", "bad", "restic: secret /path", base.Add(-time.Minute))
	del := f.seed(t, "srv1", "gone", base, 1, 1, 1, time.Second)
	f.st.MarkDeleted(context.Background(), del)

	m := decode(t, f.get(t, "/backups"), 200)
	if m["total"] != 6.0 || m["limit"] != 50.0 || len(m["items"].([]any)) != 6 {
		t.Fatalf("all: %v", m)
	}
	first := m["items"].([]any)[0].(map[string]any)
	if first["backup_uuid"] != "bad" || first["status"] != "failed" || first["error"] != "ingest failed" {
		t.Fatalf("newest first: %v", first)
	}

	m = decode(t, f.get(t, "/backups?server=srv1&status=complete&limit=2&offset=1"), 200)
	items := m["items"].([]any)
	if m["total"] != 5.0 || len(items) != 2 {
		t.Fatalf("filtered: %v", m)
	}
	it := items[0].(map[string]any)
	if it["backup_uuid"] != "okd" || it["server_uuid"] != "srv1" || it["status"] != "complete" || it["snapshot_id"] != "snap-okd" ||
		it["received_bytes"] != 400.0 || it["logical_bytes"] != 1000.0 || it["data_added"] != 100.0 || it["total_bytes_processed"] != 1000.0 ||
		it["file_count"] != 2.0 || it["index_truncated"] != false || it["duration_ms"] != 1000.0 ||
		it["started_at"] != "2026-10-24T04:59:59Z" || it["finished_at"] != "2026-10-24T05:00:00Z" {
		t.Fatalf("item: %v", it)
	}

	if m := decode(t, f.get(t, "/backups?status=deleted"), 200); m["total"] != 1.0 {
		t.Fatalf("deleted: %v", m)
	}
	for _, q := range []string{"limit=x", "offset=-1", "status=weird"} {
		if rec := f.get(t, "/backups?"+q); rec.Code != 400 {
			t.Fatalf("%s: %d", q, rec.Code)
		}
	}
	if m := decode(t, f.get(t, "/backups?limit=100000"), 200); m["limit"] != 500.0 {
		t.Fatalf("limit clamp: %v", m["limit"])
	}
	if m := decode(t, f.get(t, "/backups?server=none"), 200); m["total"] != 0.0 || len(m["items"].([]any)) != 0 {
		t.Fatalf("empty: %v", m)
	}
}

// ---- history ----

func TestHistory(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.seed(t, "srv1", "b1", base.Add(-48*time.Hour), 1000, 400, 300, time.Second) // 2026-10-22
	f.seed(t, "srv1", "b2", base.Add(-47*time.Hour), 500, 200, 100, time.Second)  // 2026-10-22
	f.seed(t, "srv1", "b3", base, 2000, 800, 50, time.Second)                     // today
	f.seedFailed(t, "srv1", "b4", "x", base.Add(-24*time.Hour))                   // 2026-10-23
	f.seedFailed(t, "srv1", "b5", "aborted", base.Add(-24*time.Hour))             // ignored
	f.seed(t, "srv1", "old", base.Add(-20*24*time.Hour), 1, 1, 1, time.Second)    // outside the 3 day window
	stat := func(at time.Time, size int64) {
		f.st.InsertRepoStat(ctx, store.RepoStat{Time: at, TotalSize: size, TotalUncompressedSize: size * 2, SnapshotsCount: 1})
	}
	stat(base.Add(-30*24*time.Hour), 100) // before the window: carried forward
	stat(base.Add(-47*time.Hour), 150)
	stat(base.Add(-46*time.Hour), 200) // last of 10-22
	stat(base.Add(-time.Hour), 400)

	m := decode(t, f.get(t, "/history?days=3"), 200)
	if m["days"] != 3.0 || m["forecast"] != nil {
		t.Fatalf("history: %v", m)
	}
	s := m["series"].([]any)
	if len(s) != 3 {
		t.Fatalf("series: %v", s)
	}
	d0, d1, d2 := s[0].(map[string]any), s[1].(map[string]any), s[2].(map[string]any)
	if d0["date"] != "2026-10-22" || d0["backups_ok"] != 2.0 || d0["backups_failed"] != 0.0 || d0["logical_bytes"] != 1500.0 ||
		d0["data_added"] != 400.0 || d0["stored_bytes"] != 200.0 {
		t.Fatalf("day 0: %v", d0)
	}
	if d1["date"] != "2026-10-23" || d1["backups_ok"] != 0.0 || d1["backups_failed"] != 1.0 || d1["stored_bytes"] != 200.0 {
		t.Fatalf("day 1 (carry forward): %v", d1)
	}
	if d2["date"] != "2026-10-24" || d2["backups_ok"] != 1.0 || d2["logical_bytes"] != 2000.0 || d2["stored_bytes"] != 400.0 {
		t.Fatalf("day 2: %v", d2)
	}
	if rec := f.get(t, "/history?days=0"); rec.Code != 400 {
		t.Fatalf("days=0: %d", rec.Code)
	}
	if m := decode(t, f.get(t, "/history"), 200); m["days"] != 30.0 || len(m["series"].([]any)) != 30 {
		t.Fatalf("default days: %v", m["days"])
	}
}

func TestHistoryForecast(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.srv.Capacity = 1000
	// 100 bytes per day for 4 days, last value 400 -> 600 left -> 6 days.
	for i := 0; i < 4; i++ {
		f.st.InsertRepoStat(ctx, store.RepoStat{Time: base.Add(time.Duration(-(3-i)*24) * time.Hour), TotalSize: int64(100 * (i + 1))})
	}
	m := decode(t, f.get(t, "/history?days=5"), 200)
	fc := m["forecast"].(map[string]any)
	if fc["capacity_bytes"] != 1000.0 || fc["used_bytes"] != 400.0 || fc["growth_bytes_per_day"] != 100.0 {
		t.Fatalf("forecast: %v", fc)
	}
	if d := num(t, fc, "days_until_full"); d < 5.99 || d > 6.01 || fc["full_at"] != "2026-10-30T12:00:00Z" {
		t.Fatalf("forecast: %v", fc)
	}

	// Shrinking repository: no date. Full repository: zero days.
	f2 := newFixture(t)
	f2.srv.Capacity = 1000
	f2.st.InsertRepoStat(ctx, store.RepoStat{Time: base.Add(-48 * time.Hour), TotalSize: 500})
	f2.st.InsertRepoStat(ctx, store.RepoStat{Time: base, TotalSize: 300})
	fc = decode(t, f2.get(t, "/history?days=5"), 200)["forecast"].(map[string]any)
	if fc["days_until_full"] != nil || fc["full_at"] != nil || num(t, fc, "growth_bytes_per_day") >= 0 {
		t.Fatalf("shrinking: %v", fc)
	}
	f3 := newFixture(t)
	f3.srv.Capacity = 1000
	f3.st.InsertRepoStat(ctx, store.RepoStat{Time: base, TotalSize: 1200})
	fc = decode(t, f3.get(t, "/history"), 200)["forecast"].(map[string]any)
	if fc["days_until_full"] != 0.0 || fc["growth_bytes_per_day"] != nil {
		t.Fatalf("full: %v", fc)
	}
}

// ---- maintenance ----

func TestMaintenanceJobs(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for i, typ := range []string{"prune", "check", "stats"} {
		id, _ := f.st.InsertJob(ctx, typ, "schedule", store.JobQueued, base.Add(time.Duration(i)*time.Minute))
		freed := int64(2048)
		if typ == "prune" {
			f.st.FinishJob(ctx, id, store.JobOK, "prune finished", &freed, base.Add(time.Hour))
		} else {
			f.st.FinishJob(ctx, id, store.JobSkipped, "skipped: busy", nil, base.Add(time.Hour))
		}
	}
	m := decode(t, f.get(t, "/maintenance?limit=2"), 200)
	items := m["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items: %v", items)
	}
	j := items[0].(map[string]any)
	if j["type"] != "stats" || j["result"] != "skipped" || j["message"] != "skipped: busy" || j["bytes_freed"] != nil || j["trigger"] != "schedule" ||
		j["started_at"] != "2026-10-24T12:02:00Z" || j["finished_at"] != "2026-10-24T13:00:00Z" {
		t.Fatalf("job: %v", j)
	}
	all := decode(t, f.get(t, "/maintenance"), 200)["items"].([]any)
	if last := all[2].(map[string]any); last["type"] != "prune" || last["bytes_freed"] != 2048.0 {
		t.Fatalf("prune job: %v", last)
	}
}

func TestMaintenanceRun(t *testing.T) {
	f := newFixture(t)
	m := decode(t, f.do(t, "POST", "/maintenance/run", `{"jobs":["stats","prune","prune"]}`, token), 202)
	if m["id"] != 40.0 || len(m["job_ids"].([]any)) != 2 {
		t.Fatalf("response: %v", m)
	}
	if got := strings.Join(f.maint.trigger[0], ","); got != "prune,stats" {
		t.Fatalf("jobs passed on: %s", got)
	}

	f.maint.busy = true
	m = decode(t, f.do(t, "POST", "/maintenance/run", `{"jobs":["check"]}`, token), 409)
	if m["error"] != "busy" {
		t.Fatalf("busy: %v", m)
	}
	f.maint.busy = false
	for _, body := range []string{``, `{}`, `{"jobs":[]}`, `{"jobs":["format"]}`, `[1]`, `{"jobs":"prune"}`} {
		if rec := f.do(t, "POST", "/maintenance/run", body, token); rec.Code != 400 {
			t.Fatalf("body %q: %d", body, rec.Code)
		}
	}
	if rec := f.do(t, "POST", "/maintenance/run", `{"jobs":["prune"]}`, ""); rec.Code != 401 {
		t.Fatalf("no token: %d", rec.Code)
	}
	if rec := f.do(t, "GET", "/maintenance/run", "", token); rec.Code != 405 {
		t.Fatalf("GET run: %d", rec.Code)
	}
}

// ---- files ----

func tarOf(t *testing.T, files map[string]string, order []string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, n := range order {
		if strings.HasSuffix(n, "/") {
			tw.WriteHeader(&tar.Header{Name: n, Typeflag: tar.TypeDir, Mode: 0o755, ModTime: base})
			continue
		}
		tw.WriteHeader(&tar.Header{Name: n, Typeflag: tar.TypeReg, Mode: 0o640, Size: int64(len(files[n])), ModTime: base})
		tw.Write([]byte(files[n]))
	}
	tw.Close()
	return buf.Bytes()
}

func seedIndex(t *testing.T, f *fixture) int64 {
	t.Helper()
	id := f.seed(t, "srv1", "bk1", base, 100, 50, 10, time.Second)
	idx := []store.FileEntry{
		{Path: "world/", Type: "d", Mode: 0o755, MTime: base},
		{Path: "world/level.dat", Type: "f", Size: 12, Mode: 0o644, MTime: base},
		{Path: "world/region/r.0.0.mca", Type: "f", Size: 99, Mode: 0o644, MTime: base},
		{Path: "world/region/r.0.1.mca", Type: "f", Size: 1, Mode: 0o644, MTime: base},
		{Path: "world/data/deep/er/x.txt", Type: "f", Size: 3, Mode: 0o600, MTime: base},
		{Path: "world2/other.txt", Type: "f", Size: 5, Mode: 0o644, MTime: base}, // sibling with a shared prefix
		{Path: "server.properties", Type: "f", Size: 7, Mode: 0o644, MTime: base},
		{Path: "link", Type: "l", Mode: 0o777, MTime: base},
		{Path: "cache", Type: "d", Mode: 0o755, MTime: base},
	}
	if err := f.st.InsertFiles(context.Background(), id, idx); err != nil {
		t.Fatal(err)
	}
	return id
}

type entry struct {
	Name, Path, Type, Mode string
	Size                   int64
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func listing(t *testing.T, f *fixture, q string) (map[string]any, []entry) {
	t.Helper()
	m := decode(t, f.get(t, "/backups/bk1/files"+q), 200)
	var out []entry
	for _, it := range m["items"].([]any) {
		x := it.(map[string]any)
		out = append(out, entry{x["name"].(string), x["path"].(string), x["type"].(string), str(x["mode"]), int64(x["size"].(float64))})
	}
	return m, out
}

func TestFilesBrowseDerivesDirectories(t *testing.T) {
	f := newFixture(t)
	seedIndex(t, f)

	m, root := listing(t, f, "")
	if m["total"] != 5.0 || m["path"] != "" || m["backup_uuid"] != "bk1" || m["index_truncated"] != false {
		t.Fatalf("root meta: %v", m)
	}
	var names []string
	for _, e := range root {
		names = append(names, e.Name+":"+e.Type)
	}
	// Directories first (also those only implied by deeper paths), then files, each sorted.
	want := "cache:dir,world:dir,world2:dir,link:symlink,server.properties:file"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("root:\n got %s\nwant %s", got, want)
	}
	if root[1].Mode != "0755" || root[4].Size != 7 || root[4].Mode != "0644" || root[4].Path != "server.properties" {
		t.Fatalf("root details: %+v", root)
	}

	_, world := listing(t, f, "?path=world")
	names = nil
	for _, e := range world {
		names = append(names, e.Path+":"+e.Type)
	}
	if got := strings.Join(names, ","); got != "world/data:dir,world/region:dir,world/level.dat:file" {
		t.Fatalf("world: %s", got)
	}
	// The same listing, whatever the slashes around the path.
	for _, q := range []string{"?path=/world/", "?path=./world", "?path=world/"} {
		if _, l := listing(t, f, q); len(l) != 3 {
			t.Fatalf("%s: %+v", q, l)
		}
	}
	_, region := listing(t, f, "?path=world/region")
	if len(region) != 2 || region[0].Name != "r.0.0.mca" || region[0].Size != 99 {
		t.Fatalf("region: %+v", region)
	}
	if _, deep := listing(t, f, "?path=world/data/deep"); len(deep) != 1 || deep[0].Name != "er" || deep[0].Type != "dir" {
		t.Fatalf("deep: %+v", deep)
	}
	if _, none := listing(t, f, "?path=nothing"); len(none) != 0 {
		t.Fatalf("missing dir: %+v", none)
	}

	m, page := listing(t, f, "?limit=2&offset=1")
	if m["total"] != 5.0 || len(page) != 2 || page[0].Name != "world" || page[1].Name != "world2" {
		t.Fatalf("paging: %v %+v", m, page)
	}
	for _, q := range []string{"?path=../etc", "?path=a/../b", "?limit=x"} {
		if rec := f.get(t, "/backups/bk1/files"+q); rec.Code != 400 {
			t.Fatalf("%s: %d", q, rec.Code)
		}
	}
	if rec := f.get(t, "/backups/unknown/files"); rec.Code != 404 {
		t.Fatalf("unknown backup: %d", rec.Code)
	}
}

func TestFilesPendingBackup(t *testing.T) {
	f := newFixture(t)
	f.st.CreateUpload(context.Background(), "u", "s/p.tar.gz", "s", "pending1", base)
	if rec := f.get(t, "/backups/pending1/files"); rec.Code != 409 {
		t.Fatalf("pending: %d", rec.Code)
	}
}

func TestFileDownload(t *testing.T) {
	f := newFixture(t)
	seedIndex(t, f)
	content := map[string]string{"world/level.dat": "level-bytes", "server.properties": "motd=hi", "world/region/r.0.0.mca": strings.Repeat("R", 99)}
	f.rs.tar = tarOf(t, content, []string{"world/", "server.properties", "world/level.dat", "world/region/r.0.0.mca"})

	rec := f.get(t, "/backups/bk1/file?path=world/level.dat")
	if rec.Code != 200 || rec.Body.String() != "level-bytes" || rec.Header().Get("Content-Length") != "11" ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), `filename=level.dat`) ||
		rec.Header().Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("download: %d %v %q", rec.Code, rec.Header(), rec.Body.String())
	}
	if rec := f.get(t, "/backups/bk1/file?path=/server.properties"); rec.Body.String() != "motd=hi" {
		t.Fatalf("leading slash: %q", rec.Body.String())
	}
	// In the index but missing in the archive -> 404.
	if rec := f.get(t, "/backups/bk1/file?path=world/region/r.0.1.mca"); rec.Code != 404 {
		t.Fatalf("missing in archive: %d", rec.Code)
	}
	// Not in the index and not in the archive -> 404 after a scan.
	dumps := f.rs.dumps
	if rec := f.get(t, "/backups/bk1/file?path=nope.txt"); rec.Code != 404 || f.rs.dumps != dumps+1 {
		t.Fatalf("not in index: %d dumps=%d", rec.Code, f.rs.dumps)
	}
	for path, code := range map[string]int{"": 400, "../x": 400, "world": 400, "link": 400, "cache": 400} {
		if rec := f.get(t, "/backups/bk1/file?path="+path); rec.Code != code {
			t.Fatalf("path %q: %d (want %d)", path, rec.Code, code)
		}
	}
	if rec := f.get(t, "/backups/zzz/file?path=a"); rec.Code != 404 {
		t.Fatalf("unknown backup: %d", rec.Code)
	}

	f.dumps.err = errors.New("busy")
	if rec := f.get(t, "/backups/bk1/file?path=world/level.dat"); rec.Code != 503 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("dump slots exhausted: %d", rec.Code)
	}
}

func TestFileFallbackScanAndHardlinks(t *testing.T) {
	f := newFixture(t)
	id := f.seed(t, "srv1", "bk1", base, 1, 1, 1, time.Second)
	if err := f.st.SetIndexTruncated(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	put := func(name, body string) {
		tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
		tw.Write([]byte(body))
	}
	put("data/orig.bin", "original")
	put("data/dup.txt", "first")
	put("data/dup.txt", "second")
	tw.WriteHeader(&tar.Header{Name: "data/link1", Typeflag: tar.TypeLink, Linkname: "data/orig.bin"})
	tw.WriteHeader(&tar.Header{Name: "data/link2", Typeflag: tar.TypeLink, Linkname: "data/link1"})
	tw.WriteHeader(&tar.Header{Name: "data/loop", Typeflag: tar.TypeLink, Linkname: "data/loop"})
	tw.Close()
	f.rs.tar = buf.Bytes()

	// Nothing is indexed, the archive is scanned instead.
	for path, want := range map[string]string{
		"data/orig.bin": "original", "data/link1": "original", "data/link2": "original", "data/dup.txt": "first",
	} {
		if rec := f.get(t, "/backups/bk1/file?path="+path); rec.Code != 200 || rec.Body.String() != want {
			t.Fatalf("%s: %d %q", path, rec.Code, rec.Body.String())
		}
	}
	if rec := f.get(t, "/backups/bk1/file?path=data/loop"); rec.Code != 404 {
		t.Fatalf("link loop: %d", rec.Code)
	}
	if rec := f.get(t, "/backups/bk1/file?path=data/none"); rec.Code != 404 {
		t.Fatalf("missing: %d", rec.Code)
	}
}

func TestRepoInfoNeverLeaksCredentials(t *testing.T) {
	for repo, want := range map[string]string{
		"rest:https://user:pa/ss@word#1%40x@nas.example:8000/peb": "https://nas.example:8000/peb",
		"rest:http://admin:p@ss/w0rd@host/repo":                   "http://host/repo",
		"s3:https://AKIA:sec/ret%2F+key@s3.example/bucket/pre":    "https://s3.example/bucket/pre",
		"sftp:u:pw@host:/srv":                                     "host:/srv",
	} {
		if _, loc := repoInfo(repo); loc != want {
			t.Errorf("%s: got %q want %q", repo, loc, want)
		}
	}
}

func TestSummaryPrefersDiskSize(t *testing.T) {
	f := newFixture(t)
	f.seed(t, "srv1", "b1", base.Add(-time.Hour), 1000, 400, 300, time.Second)
	disk := int64(800)
	f.st.InsertRepoStat(context.Background(), store.RepoStat{Time: base, TotalSize: 500, DiskBytes: &disk})
	m := decode(t, f.get(t, "/summary"), 200)
	if m["stored_bytes"] != 800.0 || m["referenced_bytes"] != 500.0 || m["stored_bytes_exact"] != true || m["saved_bytes"] != 200.0 {
		t.Fatalf("summary: %v", m)
	}
	h := decode(t, f.get(t, "/history?days=1"), 200)["series"].([]any)[0].(map[string]any)
	if h["stored_bytes"] != 800.0 {
		t.Fatalf("history: %v", h)
	}
}

func TestIndexWithDotSlashNames(t *testing.T) {
	// Entries are indexed with "./" stripped; the archive may still use it.
	f := newFixture(t)
	id := f.seed(t, "srv1", "bk1", base, 1, 1, 1, time.Second)
	f.st.InsertFiles(context.Background(), id, []store.FileEntry{{Path: "a.txt", Type: "f", Size: 2, Mode: 0o644, MTime: base}})
	f.rs.tar = tarOf(t, map[string]string{"./a.txt": "hi"}, []string{"./a.txt"})
	if rec := f.get(t, "/backups/bk1/file?path=a.txt"); rec.Code != 200 || rec.Body.String() != "hi" {
		t.Fatalf("%d %q", rec.Code, rec.Body.String())
	}
}

func TestSummaryTarget(t *testing.T) {
	ctx := context.Background()

	// Nothing known: null.
	f0 := newFixture(t)
	if m := decode(t, f0.get(t, "/summary"), 200); m["target"] != nil {
		t.Fatalf("target: %v", m["target"])
	}

	// statfs values win over the configured capacity; 100 bytes of growth per day, 100 added per backup.
	f := newFixture(t)
	f.srv.Capacity = 1 << 40
	for i := 0; i < 4; i++ {
		tt, tf := int64(10000), int64(9000-100*i)
		f.st.InsertRepoStat(ctx, store.RepoStat{Time: base.Add(time.Duration(-(3-i)*24) * time.Hour), TotalSize: int64(100 * (i + 1)), TargetTotal: &tt, TargetFree: &tf})
	}
	f.seed(t, "srv1", "b1", base.Add(-48*time.Hour), 1000, 400, 100, time.Second)
	f.seed(t, "srv1", "b2", base.Add(-24*time.Hour), 1000, 400, 200, time.Second)
	tg := decode(t, f.get(t, "/summary"), 200)["target"].(map[string]any)
	if tg["exact"] != true || tg["total_bytes"] != 10000.0 || tg["free_bytes"] != 8700.0 || tg["used_bytes"] != 1300.0 ||
		tg["peb_bytes"] != 400.0 || tg["other_bytes"] != 900.0 {
		t.Fatalf("target: %v", tg)
	}
	// free 8700, growth 100/day -> 87 days; average added 150 -> 58 backups.
	if d := num(t, tg, "days_until_full"); d < 86.9 || d > 87.1 {
		t.Fatalf("days_until_full: %v", tg)
	}
	if tg["avg_data_added"] != 150.0 || tg["backups_remaining"] != 58.0 {
		t.Fatalf("estimates: %v", tg)
	}
	// The history forecast uses the real free space as well.
	fc := decode(t, f.get(t, "/history?days=5"), 200)["forecast"].(map[string]any)
	if fc["capacity_bytes"] != 10000.0 || fc["free_bytes"] != 8700.0 {
		t.Fatalf("forecast: %v", fc)
	}

	// Without statfs: configured capacity minus stored, flagged as estimate; no backups -> no remaining.
	f2 := newFixture(t)
	f2.srv.Capacity = 1000
	f2.st.InsertRepoStat(ctx, store.RepoStat{Time: base, TotalSize: 300})
	tg = decode(t, f2.get(t, "/summary"), 200)["target"].(map[string]any)
	if tg["exact"] != false || tg["total_bytes"] != 1000.0 || tg["free_bytes"] != 700.0 || tg["peb_bytes"] != 300.0 ||
		tg["other_bytes"] != 0.0 || tg["backups_remaining"] != nil || tg["avg_data_added"] != nil {
		t.Fatalf("capacity fallback: %v", tg)
	}
}

type fakeOffsite struct {
	st   offsite.Status
	busy bool
	runs int

	confirmed int
}

func (o *fakeOffsite) Status(context.Context) (offsite.Status, error) { return o.st, nil }
func (o *fakeOffsite) ConfirmForget(context.Context) error            { o.confirmed++; return nil }
func (o *fakeOffsite) TriggerCopy() (int64, error) {
	if o.busy {
		return 0, offsite.ErrBusy
	}
	o.runs++
	return 77, nil
}

func TestSecondaryNotConfigured(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"/status", "/summary"} {
		sec := decode(t, f.get(t, path), 200)["secondary"].(map[string]any)
		if sec["configured"] != false || sec["repo"] != nil || sec["state"] != "off" {
			t.Fatalf("%s: %v", path, sec)
		}
	}
	if rec := f.do(t, "POST", "/maintenance/run", `{"jobs":["copy"]}`, token); rec.Code != 400 {
		t.Fatalf("copy without offsite: %d", rec.Code)
	}
}

func TestSecondaryStatus(t *testing.T) {
	f := newFixture(t)
	lag, stored := int64(3), int64(5000)
	o := &fakeOffsite{st: offsite.Status{
		Repo:     "s3:https://AKIAKEY:topsecret@s3.example.org/bucket/peb",
		LastCopy: &store.Job{Result: store.JobOK, FinishedAt: base.Add(-time.Hour)},
		Lag:      &lag, LagSince: base.Add(-2 * time.Hour), StoredBytes: &stored, StoredAt: base.Add(-3 * time.Hour),
		NextCopy: base.Add(5 * time.Minute), Pending: true,
	}}
	f.srv.Offsite = o
	for _, path := range []string{"/status", "/summary"} {
		rec := f.get(t, path)
		if strings.Contains(rec.Body.String(), "topsecret") || strings.Contains(rec.Body.String(), "AKIAKEY") {
			t.Fatalf("%s leaks credentials: %s", path, rec.Body)
		}
		sec := decode(t, rec, 200)["secondary"].(map[string]any)
		if sec["configured"] != true || sec["repo"] != "s3:https://s3.example.org/bucket/peb" || sec["type"] != "s3" ||
			sec["last_copy_result"] != "ok" || sec["lag_snapshots"] != 3.0 || sec["stored_bytes"] != 5000.0 ||
			sec["state"] != "ok" || sec["last_copy_at"] != "2026-10-24T11:00:00Z" || sec["next_copy_at"] != "2026-10-24T12:05:00Z" {
			t.Fatalf("%s: %v", path, sec)
		}
	}
	o.st.LastError = "copy failed: token expired"
	o.st.LastCopy.Result = store.JobFailed
	sec := decode(t, f.get(t, "/status"), 200)["secondary"].(map[string]any)
	if sec["state"] != "error" || sec["last_error"] != "copy failed: token expired" {
		t.Fatalf("%v", sec)
	}
}

func TestMaintenanceRunCopy(t *testing.T) {
	f := newFixture(t)
	o := &fakeOffsite{}
	f.srv.Offsite = o
	m := decode(t, f.do(t, "POST", "/maintenance/run", `{"jobs":["copy"]}`, token), 202)
	if m["id"] != 77.0 || o.runs != 1 || len(f.maint.trigger) != 0 {
		t.Fatalf("%v runs=%d", m, o.runs)
	}
	m = decode(t, f.do(t, "POST", "/maintenance/run", `{"jobs":["copy","stats"]}`, token), 202)
	if len(m["job_ids"].([]any)) != 2 || len(f.maint.trigger) != 1 {
		t.Fatalf("%v", m)
	}
	o.busy = true
	decode(t, f.do(t, "POST", "/maintenance/run", `{"jobs":["copy"]}`, token), 409)
}

func TestStatusPruneSkipWarnings(t *testing.T) {
	f := newFixture(t)
	m := decode(t, f.get(t, "/status"), 200)["maintenance"].(map[string]any)
	if w, ok := m["warnings"].([]any); !ok || len(w) != 0 || m["prune_skipped_consecutive"] != 0.0 {
		t.Fatalf("clean: %v", m)
	}
	f.maint.status.PruneSkipped = 3
	f.maint.status.Warnings = []string{"prune was skipped as busy 3 times in a row"}
	m = decode(t, f.get(t, "/status"), 200)["maintenance"].(map[string]any)
	if w := m["warnings"].([]any); len(w) != 1 || m["prune_skipped_consecutive"] != 3.0 {
		t.Fatalf("warned: %v", m)
	}
}

func TestOffsiteForgetConfirm(t *testing.T) {
	f := newFixture(t)
	if rec := f.do(t, "POST", "/offsite/forget-confirm", ``, token); rec.Code != 400 {
		t.Fatalf("without offsite: %d", rec.Code)
	}
	o := &fakeOffsite{st: offsite.Status{Repo: "rclone:gdrive:peb", ForgetBlocked: true, LastError: "forget-secondary: too many"}}
	f.srv.Offsite = o
	if rec := f.do(t, "POST", "/offsite/forget-confirm", ``, ""); rec.Code != 401 {
		t.Fatalf("without token: %d", rec.Code)
	}
	m := decode(t, f.do(t, "POST", "/offsite/forget-confirm", ``, token), 200)
	if m["confirmed"] != true || o.confirmed != 1 {
		t.Fatalf("%v %d", m, o.confirmed)
	}
	sec := decode(t, f.get(t, "/status"), 200)["secondary"].(map[string]any)
	if sec["forget_blocked"] != true || sec["state"] != "error" {
		t.Fatalf("%v", sec)
	}
}
