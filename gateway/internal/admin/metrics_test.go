package admin

import (
	"context"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"ptero-enhanced-backup/gateway/internal/maintenance"
	"ptero-enhanced-backup/gateway/internal/offsite"
	"ptero-enhanced-backup/gateway/internal/store"
)

const metricsTok = "metrics-token-0123456789abcdef0123456789"

func (f *fixture) metrics(tok string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", MetricsPath, nil)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	rec := httptest.NewRecorder()
	f.srv.ServeMetrics(rec, req)
	return rec
}

var sampleRe = regexp.MustCompile(`^([a-z_:][a-z0-9_:]*)(\{[^}]*\})? (\S+)$`)

// parseMetrics checks the text format and returns "name{labels}" -> value.
// Every family must have HELP and TYPE before its first sample.
func parseMetrics(t *testing.T, body string) map[string]float64 {
	t.Helper()
	out := map[string]float64{}
	help, typ := map[string]bool{}, map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "# HELP "):
			help[strings.Fields(line)[2]] = true
		case strings.HasPrefix(line, "# TYPE "):
			typ[strings.Fields(line)[2]] = true
		default:
			m := sampleRe.FindStringSubmatch(line)
			if m == nil {
				t.Fatalf("bad line %q", line)
			}
			if !help[m[1]] || !typ[m[1]] {
				t.Fatalf("%s without HELP/TYPE", m[1])
			}
			v, err := strconv.ParseFloat(m[3], 64)
			if err != nil {
				t.Fatalf("value in %q: %v", line, err)
			}
			if _, dup := out[m[1]+m[2]]; dup {
				t.Fatalf("duplicate series %s%s", m[1], m[2])
			}
			out[m[1]+m[2]] = v
		}
	}
	return out
}

func checkMetrics(t *testing.T, got, want map[string]float64) {
	t.Helper()
	for k, v := range want {
		if g, ok := got[k]; !ok || g != v {
			t.Errorf("%s = %v (present %v), want %v", k, g, ok, v)
		}
	}
}

func TestMetricsAuth(t *testing.T) {
	f := newFixture(t)
	for name, tok := range map[string]string{"missing": "", "wrong": "nope"} {
		rec := f.metrics(tok)
		if rec.Code != 401 || rec.Header().Get("WWW-Authenticate") == "" {
			t.Fatalf("%s: %d", name, rec.Code)
		}
	}
	rec := f.metrics(token)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain; version=0.0.4") {
		t.Fatalf("admin token: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	// With its own token only that one counts.
	f.srv.MetricsToken = metricsTok
	if f.metrics(token).Code != 401 || f.metrics(metricsTok).Code != 200 {
		t.Fatal("own metrics token not exclusive")
	}
	// Never without a token.
	f.srv.MetricsToken, f.srv.Token = "", ""
	if f.metrics("").Code != 404 || f.metrics("x").Code != 404 {
		t.Fatal("metrics reachable without any token")
	}
	// Only the metrics token set: works without the admin API.
	f.srv.MetricsToken = metricsTok
	if f.metrics(metricsTok).Code != 200 || f.metrics("").Code != 401 {
		t.Fatal("metrics token alone")
	}
}

func TestMetricsContent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.seed(t, "srv-a", "b1", base.Add(-2*time.Hour), 1000, 400, 100, 30*time.Second)
	f.seed(t, "srv-a", "b2", base.Add(-time.Hour), 1000, 500, 50, 20*time.Second)
	f.seed(t, "srv-b", "b3", base.Add(-48*time.Hour), 2000, 800, 700, 90*time.Second)
	f.seedFailed(t, "srv-b", "b4", "boom", base.Add(-30*time.Minute))
	if err := f.st.SetServerStat(ctx, "srv-a", 4242, base); err != nil {
		t.Fatal(err)
	}
	got := parseMetrics(t, f.metrics(token).Body.String())
	checkMetrics(t, got, map[string]float64{
		`peb_backups_total{status="complete"}`:              3,
		`peb_backups_total{status="failed"}`:                1,
		`peb_backups_last_24h{status="complete"}`:           2,
		`peb_backups_last_24h{status="failed"}`:             1,
		`peb_backup_received_bytes_total`:                   1700,
		`peb_backup_data_added_bytes_total`:                 850,
		`peb_backup_last_duration_seconds{server="srv-a"}`:  20,
		`peb_backup_last_data_added_bytes{server="srv-a"}`:  50,
		`peb_backup_last_timestamp_seconds{server="srv-a"}`: float64(base.Add(-time.Hour).Unix()),
		`peb_backup_last_duration_seconds{server="srv-b"}`:  90,
		`peb_server_stored_bytes{server="srv-a"}`:           4242,
		`peb_repo_reachable`:                                1,
		`peb_spool_used_bytes`:                              123,
		`peb_spool_limit_bytes`:                             1000,
		`peb_ingest_running`:                                2,
		`peb_ingest_queued`:                                 3,
		`peb_dumps_active`:                                  1,
		`peb_maintenance_job_running`:                       0,
		`peb_secondary_configured`:                          0,
		`peb_uptime_seconds`:                                90,
		`peb_build_info{version="test"}`:                    1,
	})
	// Not measured yet: absent, not zero.
	for _, k := range []string{"peb_repo_size_bytes", "peb_repo_capacity_bytes", "peb_maintenance_bytes_freed_last", "peb_secondary_lag_seconds"} {
		if _, ok := got[k]; ok {
			t.Errorf("%s should be absent", k)
		}
	}
	for k := range got {
		if strings.HasPrefix(k, "peb_maintenance_last_") {
			t.Errorf("unexpected %s", k)
		}
	}
}

func TestMetricsMaintenanceRepoAndSecondary(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	freed := int64(999)
	add := func(typ, result string, at time.Time, freed *int64) {
		id, err := f.st.InsertJob(ctx, typ, "schedule", store.JobRunning, at.Add(-time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.st.FinishJob(ctx, id, result, "", freed, at); err != nil {
			t.Fatal(err)
		}
	}
	add("prune", store.JobOK, base.Add(-5*time.Hour), &freed)
	add("check", store.JobOK, base.Add(-4*time.Hour), nil)
	add("check", store.JobFailed, base.Add(-3*time.Hour), nil)
	disk := int64(5000)
	tt, tf := int64(10000), int64(6000)
	if err := f.st.InsertRepoStat(ctx, store.RepoStat{Time: base, TotalSize: 4000, DiskBytes: &disk, TargetTotal: &tt, TargetFree: &tf}); err != nil {
		t.Fatal(err)
	}
	f.maint.status = maintenance.Status{Running: &store.Job{Type: "check"}}
	f.rs.reachErr = context.DeadlineExceeded
	lag, stored := int64(3), int64(7000)
	f.srv.Offsite = &fakeOffsite{st: offsite.Status{
		LastCopy: &store.Job{Result: store.JobOK, FinishedAt: base.Add(-time.Hour)},
		Lag:      &lag, LagSince: base.Add(-2 * time.Hour), StoredBytes: &stored,
	}}
	got := parseMetrics(t, f.metrics(token).Body.String())
	checkMetrics(t, got, map[string]float64{
		`peb_maintenance_last_success_timestamp_seconds{type="prune"}`: float64(base.Add(-5 * time.Hour).Unix()),
		`peb_maintenance_last_success_timestamp_seconds{type="check"}`: float64(base.Add(-4 * time.Hour).Unix()),
		`peb_maintenance_last_result{type="prune"}`:                    1,
		`peb_maintenance_last_result{type="check"}`:                    0,
		`peb_maintenance_job_running`:                                  1,
		`peb_maintenance_bytes_freed_last`:                             999,
		`peb_repo_size_bytes`:                                          5000,
		`peb_repo_capacity_bytes`:                                      10000,
		`peb_repo_reachable`:                                           0,
		`peb_secondary_configured`:                                     1,
		`peb_secondary_pending_snapshots`:                              3,
		`peb_secondary_lag_seconds`:                                    7200,
		`peb_secondary_last_copy_timestamp_seconds`:                    float64(base.Add(-time.Hour).Unix()),
		`peb_secondary_last_error`:                                     0,
		`peb_secondary_stored_bytes`:                                   7000,
	})
	if _, ok := got[`peb_maintenance_last_result{type="stats"}`]; ok {
		t.Error("stats never ran, must be absent")
	}
}

func TestMetricsCache(t *testing.T) {
	f := newFixture(t)
	f.seed(t, "srv-a", "b1", base.Add(-time.Hour), 10, 5, 1, time.Second)
	if got := parseMetrics(t, f.metrics(token).Body.String()); got[`peb_backups_total{status="complete"}`] != 1 {
		t.Fatal("first")
	}
	f.seed(t, "srv-a", "b2", base.Add(-time.Minute), 10, 5, 1, time.Second)
	if got := parseMetrics(t, f.metrics(token).Body.String()); got[`peb_backups_total{status="complete"}`] != 1 {
		t.Fatal("cache not used")
	}
	f.srv.Now = func() time.Time { return base.Add(31 * time.Second) }
	if got := parseMetrics(t, f.metrics(token).Body.String()); got[`peb_backups_total{status="complete"}`] != 2 {
		t.Fatal("cache not refreshed after 30s")
	}
}

func TestMetricsLabelEscaping(t *testing.T) {
	if got := lbl("server", "a\"b\\c\nd"); got != `{server="a\"b\\c\nd"}` {
		t.Fatal(got)
	}
}

func TestMetricsMethods(t *testing.T) {
	f := newFixture(t)
	req := httptest.NewRequest("HEAD", MetricsPath, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	f.srv.ServeMetrics(rec, req)
	if rec.Code != 200 {
		t.Fatalf("HEAD: %d", rec.Code)
	}
	req = httptest.NewRequest("POST", MetricsPath, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	f.srv.ServeMetrics(rec, req)
	if rec.Code != 405 || rec.Header().Get("Allow") == "" {
		t.Fatalf("POST: %d", rec.Code)
	}
}
