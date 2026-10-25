package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"ptero-enhanced-backup/gateway/internal/store"
)

// MetricsPath is the Prometheus endpoint.
const MetricsPath = "/metrics"

const metricsCacheTTL = 30 * time.Second

// metricsData is what the store queries yield; cached for metricsCacheTTL.
type metricsData struct {
	counts, counts24h map[string]int64
	totals            store.Totals
	lastPerServer     []store.LastServerBackup
	serverStats       map[string]store.ServerStat
	repoStat          *store.RepoStat // nil = not measured yet
	lastOK, lastAny   map[string]*store.Job
}

var metricJobTypes = []string{"prune", "check", "stats"}

func (s *Server) metricsAuthorized(r *http.Request) bool {
	want := s.MetricsToken
	if want == "" {
		want = s.Token
	}
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || want == "" {
		return false
	}
	a, b := sha256.Sum256([]byte(tok)), sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

// ServeMetrics serves GET /metrics in the Prometheus text format. It never
// answers without a token; with neither PEB_METRICS_TOKEN nor PEB_ADMIN_TOKEN
// set the endpoint does not exist (404).
func (s *Server) ServeMetrics(w http.ResponseWriter, r *http.Request) {
	if s.MetricsToken == "" && s.Token == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.metricsAuthorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="peb-metrics"`)
		http.Error(w, "missing or invalid bearer token", http.StatusUnauthorized)
		return
	}
	body, err := s.renderMetrics(r.Context())
	if err != nil {
		s.Log.Error("metrics failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}

func (s *Server) loadMetricsData(ctx context.Context) (*metricsData, error) {
	s.metricsMu.Lock()
	defer s.metricsMu.Unlock()
	if s.metricsCache != nil && s.now().Sub(s.metricsAt) < metricsCacheTTL {
		return s.metricsCache, nil
	}
	d := &metricsData{lastOK: map[string]*store.Job{}, lastAny: map[string]*store.Job{}}
	var err error
	if d.counts, err = s.Store.BackupCountsByStatus(ctx); err != nil {
		return nil, err
	}
	if d.counts24h, err = s.Store.BackupCountsSince(ctx, s.now().Add(-24*time.Hour)); err != nil {
		return nil, err
	}
	if d.totals, err = s.Store.Totals(ctx, s.now()); err != nil {
		return nil, err
	}
	if d.lastPerServer, err = s.Store.LastBackupPerServer(ctx); err != nil {
		return nil, err
	}
	if d.serverStats, err = s.Store.ServerStats(ctx); err != nil {
		return nil, err
	}
	switch st, err := s.Store.LatestRepoStat(ctx); {
	case err == nil:
		d.repoStat = st
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	}
	for _, typ := range metricJobTypes {
		if j, err := s.Store.LastOKJob(ctx, typ); err == nil {
			d.lastOK[typ] = j
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if j, err := s.Store.LastFinishedJob(ctx, typ, false); err == nil {
			d.lastAny[typ] = j
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	s.metricsCache, s.metricsAt = d, s.now()
	return d, nil
}

// metricsProbeWait bounds how long a scrape waits for the repository probe, so a
// scrape never runs into Prometheus' scrape_timeout because the repo hangs.
const metricsProbeWait = 5 * time.Second

// reachableQuick returns the cached probe result or waits at most metricsProbeWait
// for a fresh one; on expiry it reports false. The probe keeps running in the
// background and fills the cache for the next scrape.
func (s *Server) reachableQuick(ctx context.Context) bool {
	res := make(chan bool, 1)
	go func() {
		ok, _ := s.reachable(context.WithoutCancel(ctx))
		res <- ok
	}()
	t := time.NewTimer(metricsProbeWait)
	defer t.Stop()
	select {
	case ok := <-res:
		return ok
	case <-t.C:
		return false
	case <-ctx.Done():
		return false
	}
}

func (s *Server) renderMetrics(ctx context.Context) ([]byte, error) {
	d, err := s.loadMetricsData(ctx)
	if err != nil {
		return nil, err
	}
	var m metricsWriter

	// Build and process.
	m.gauge("peb_build_info", "Gateway version (value is always 1).", sample{lbl("version", s.Version), 1})
	m.gauge("peb_uptime_seconds", "Seconds since the gateway started.", val(s.now().Sub(s.Started).Seconds()))

	// Backups.
	m.gauge("peb_backups_total", "Backups in the database by status.", statusSamples(d.counts)...)
	m.gauge("peb_backups_last_24h", "Backups started in the last 24 hours by status.", statusSamples(d.counts24h)...)
	// Sums over the database can shrink when backups are deleted, so they are typed gauge.
	m.gauge("peb_backup_received_bytes_total", "Compressed bytes received for completed backups (sum over the database).", val(d.totals.ReceivedBytes))
	m.gauge("peb_backup_data_added_bytes_total", "New repository data added by completed backups (sum over the database).", val(d.totals.DataAdded))
	var dur, added, ts []sample
	for _, l := range d.lastPerServer {
		lb := lbl("server", l.ServerUUID)
		dur = append(dur, sample{lb, l.Duration.Seconds()})
		added = append(added, sample{lb, float64(l.DataAdded)})
		ts = append(ts, sample{lb, float64(l.FinishedAt.Unix())})
	}
	m.gauge("peb_backup_last_duration_seconds", "Duration of the last completed backup per server.", dur...)
	m.gauge("peb_backup_last_data_added_bytes", "Data added to the repository by the last completed backup per server.", added...)
	m.gauge("peb_backup_last_timestamp_seconds", "Finish time of the last completed backup per server (Unix).", ts...)

	// Repository.
	ok := s.reachableQuick(ctx)
	m.gauge("peb_repo_reachable", "1 if the restic repository answered the last probe.", val(b2f(ok)))
	if d.repoStat != nil {
		m.gauge("peb_repo_size_bytes", "Repository size from the latest statistics (on disk if measured, else referenced data).", val(d.repoStat.Stored()))
	}
	if sp := s.space(ctx); sp != nil && sp.total > 0 {
		m.gauge("peb_repo_capacity_bytes", "Capacity of the backup target (filesystem size or PEB_REPO_CAPACITY_BYTES).", val(sp.total))
	}
	var stored []sample
	for _, u := range sortedKeys(d.serverStats) {
		stored = append(stored, sample{lbl("server", u), float64(d.serverStats[u].StoredBytes)})
	}
	m.gauge("peb_server_stored_bytes", "Repository space occupied by the snapshots of a server (last measurement).", stored...)

	// Load.
	running, queued := s.Ingest.Load()
	m.gauge("peb_spool_used_bytes", "Spool space in use.", val(s.SpoolUsed()))
	m.gauge("peb_spool_limit_bytes", "Configured spool limit.", val(s.SpoolLimit))
	m.gauge("peb_ingest_running", "Backups currently being ingested.", val(running))
	m.gauge("peb_ingest_queued", "Backups waiting for an ingest slot.", val(queued))
	m.gauge("peb_dumps_active", "Restores and downloads currently streaming.", val(s.Dumps.ActiveDumps()))

	// Maintenance.
	var lastOK, lastRes []sample
	for _, typ := range metricJobTypes {
		if j := d.lastOK[typ]; j != nil && !j.FinishedAt.IsZero() {
			lastOK = append(lastOK, sample{lbl("type", typ), float64(j.FinishedAt.Unix())})
		}
		if j := d.lastAny[typ]; j != nil {
			lastRes = append(lastRes, sample{lbl("type", typ), b2f(j.Result == store.JobOK)})
		}
	}
	m.gauge("peb_maintenance_last_success_timestamp_seconds", "Finish time of the last successful maintenance job (Unix).", lastOK...)
	m.gauge("peb_maintenance_last_result", "Result of the last finished maintenance job: 1 ok, 0 failed.", lastRes...)
	ms, err := s.Maint.Status(ctx)
	if err != nil {
		return nil, err
	}
	m.gauge("peb_maintenance_job_running", "1 while a maintenance job runs.", val(b2f(ms.Running != nil)))
	if j := d.lastOK["prune"]; j != nil && j.BytesFreed != nil {
		m.gauge("peb_maintenance_bytes_freed_last", "Bytes freed by the last successful prune.", val(*j.BytesFreed))
	}

	// Offsite copy.
	m.gauge("peb_secondary_configured", "1 if an offsite copy (secondary repository) is configured.", val(b2f(s.Offsite != nil)))
	if s.Offsite != nil {
		st, err := s.Offsite.Status(ctx)
		if err != nil {
			return nil, err
		}
		if st.Lag != nil {
			m.gauge("peb_secondary_pending_snapshots", "Snapshots not yet copied to the secondary repository.", val(*st.Lag))
			lag := 0.0
			if *st.Lag > 0 && !st.LagSince.IsZero() {
				lag = max(0, s.now().Sub(st.LagSince).Seconds())
			}
			m.gauge("peb_secondary_lag_seconds", "Age of the oldest snapshot not yet copied (0 if up to date).", val(lag))
		}
		if st.LastCopy != nil && st.LastCopy.Result == store.JobOK && !st.LastCopy.FinishedAt.IsZero() {
			m.gauge("peb_secondary_last_copy_timestamp_seconds", "Finish time of the last successful offsite copy (Unix).", val(st.LastCopy.FinishedAt.Unix()))
		}
		bad := st.LastError != "" || (st.LastCopy != nil && st.LastCopy.Result == store.JobFailed)
		m.gauge("peb_secondary_last_error", "1 if the last offsite copy failed.", val(b2f(bad)))
		if st.StoredBytes != nil {
			m.gauge("peb_secondary_stored_bytes", "Size of the secondary repository (last measurement).", val(*st.StoredBytes))
		}
	}
	return m.buf.Bytes(), nil
}

// ---- minimal Prometheus text writer (format 0.0.4) ----

type sample struct {
	labels string // rendered: {a="b"} or ""
	v      float64
}

func val[T ~int | ~int64 | ~float64](v T) sample { return sample{v: float64(v)} }

func lbl(kv ...string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i+1 < len(kv); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(kv[i])
		b.WriteString(`="`)
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(kv[i+1]))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

type metricsWriter struct{ buf bytes.Buffer }

// gauge writes a metric family. A family without samples is left out entirely:
// a value that is not measured yet is absent rather than 0 or NaN, so a missing
// series cannot be mistaken for a real zero in graphs and alerts.
func (m *metricsWriter) gauge(name, help string, ss ...sample) {
	if len(ss) == 0 {
		return
	}
	help = strings.NewReplacer(`\`, `\\`, "\n", `\n`).Replace(help)
	fmt.Fprintf(&m.buf, "# HELP %s %s\n# TYPE %s gauge\n", name, help, name)
	for _, s := range ss {
		fmt.Fprintf(&m.buf, "%s%s %s\n", name, s.labels, strconv.FormatFloat(s.v, 'g', -1, 64))
	}
}

func statusSamples(c map[string]int64) []sample {
	var out []sample
	for _, k := range sortedKeys(c) {
		out = append(out, sample{lbl("status", k), float64(c[k])})
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
