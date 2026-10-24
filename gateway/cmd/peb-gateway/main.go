// Command peb-gateway is an S3-compatible endpoint that stores Pterodactyl
// backups deduplicated in a restic repository.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // TZ=Europe/Berlin must work in the slim image

	"ptero-enhanced-backup/gateway/internal/admin"
	"ptero-enhanced-backup/gateway/internal/config"
	"ptero-enhanced-backup/gateway/internal/gate"
	"ptero-enhanced-backup/gateway/internal/ingest"
	"ptero-enhanced-backup/gateway/internal/maintenance"
	"ptero-enhanced-backup/gateway/internal/notify"
	"ptero-enhanced-backup/gateway/internal/restic"
	"ptero-enhanced-backup/gateway/internal/s3api"
	"ptero-enhanced-backup/gateway/internal/s3auth"
	"ptero-enhanced-backup/gateway/internal/store"
)

// version is set at build time (-ldflags "-X main.version=...").
var version = "dev"

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(log)

	for _, d := range []string{cfg.DataDir, cfg.CacheDir()} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()

	rc := &restic.Client{
		Bin: cfg.ResticBin, Repo: cfg.ResticRepo,
		Password: cfg.ResticPass, PasswordFile: cfg.ResticPassFn, CacheDir: cfg.CacheDir(),
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := rc.Init(ctx); err != nil {
		return err
	}

	// Everything that touches the repository besides backup/dump goes through the gate.
	rg := &restic.Gated{Client: rc, Gate: &gate.Gate{}}
	mgr := ingest.NewManager(st, rg, cfg.SpoolDir(), log)
	mgr.Gate = rg.Gate
	mgr.Configure(ingest.Limits{
		MaxSpoolBytes: cfg.MaxSpoolBytes, MaxIngests: cfg.MaxConcurrentIngests,
		MaxIndexFiles: cfg.MaxIndexFiles, IngestTimeout: cfg.IngestTimeout,
	})
	if err := mgr.Recover(ctx); err != nil {
		return err
	}
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				mgr.Reap()
			case <-ctx.Done():
				return
			}
		}
	}()

	var nt *notify.Notifier
	if cfg.NtfyURL != "" {
		nt = &notify.Notifier{URL: cfg.NtfyURL, Token: cfg.NtfyToken, Log: log}
	}
	maint := &maintenance.Service{
		Store: st, Backend: rg, Gate: mgr.Gate, Notify: nt, Log: log, BaseCtx: ctx,
		Cfg: maintenance.Config{
			Hour: cfg.MaintenanceHour, Minute: cfg.MaintenanceMinute, PruneEnabled: cfg.PruneEnabled,
			CheckInterval: cfg.CheckInterval, ReadDataSubset: cfg.CheckReadDataSubset,
			BusyWait: cfg.MaintenanceBusyWait, JobTimeout: cfg.MaintenanceTimeout,
		},
	}
	if restic.RepoType(cfg.ResticRepo) == "local" {
		maint.DiskSize = func() (int64, error) { return restic.DirSize(cfg.ResticRepo) }
		maint.TargetSpace = func() (int64, int64, error) { return restic.FSSpace(cfg.ResticRepo) }
	}
	if err := st.FailStaleJobs(ctx, time.Now()); err != nil {
		return err
	}
	mgr.OnDone = func(ev ingest.DoneEvent) {
		if ev.OK {
			maint.BackupDone()
			return
		}
		why := "Verarbeitung fehlgeschlagen"
		if ev.TimedOut {
			why = "Zeitüberschreitung"
		}
		nt.Send("PEB Backup fehlgeschlagen", fmt.Sprintf("Backup %s von Server %s fehlgeschlagen (%s). Details im Gateway-Log.", ev.BackupUUID, ev.ServerUUID, why))
	}
	go maint.Run(ctx)
	go func() { // first size snapshot, so the dashboard has a value right after start
		if _, err := maint.RecordStats(ctx); err != nil {
			log.Warn("initial repo stats failed", "err", err)
		}
	}()

	api := &s3api.Server{
		Bucket:   cfg.Bucket,
		Verifier: &s3auth.Verifier{Creds: s3auth.Credentials{AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey, Region: cfg.Region}},
		Ingest:   mgr, Store: st, Restic: rg, Log: log,
		PublicURL: cfg.PublicURL, MaxPartBytes: cfg.MaxPartBytes, MaxDumps: cfg.MaxConcurrentDumps,
		Gate: mgr.Gate, DownloadTimeout: cfg.DownloadTimeout,
	}
	adm := &admin.Server{
		Token: cfg.AdminToken, Store: st, Restic: rc, Maint: maint, Dumps: api, Ingest: mgr,
		SpoolUsed: mgr.Quota.Used, SpoolLimit: cfg.MaxSpoolBytes, Version: version, Repo: cfg.ResticRepo,
		Capacity: cfg.RepoCapacityBytes, Log: log, Started: time.Now(), DownloadTimeout: cfg.DownloadTimeout,
	}
	if cfg.AdminToken == "" {
		log.Info("admin API disabled (PEB_ADMIN_TOKEN not set)")
	}
	// No ServeMux here: it would redirect unclean S3 paths.
	root := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, admin.Prefix+"/") {
			adm.ServeHTTP(w, r)
			return
		}
		api.ServeHTTP(w, r)
	})
	srv := &http.Server{
		Addr: cfg.Listen, Handler: root,
		ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 64 << 10,
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "addr", cfg.Listen, "bucket", cfg.Bucket, "repo", cfg.ResticRepo, "data", cfg.DataDir)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		log.Warn("http shutdown", "err", err)
	}
	mgr.Shutdown(sctx)
	maint.Wait()
	nt.Wait()
	return nil
}
