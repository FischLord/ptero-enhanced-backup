// Package config reads the gateway configuration from environment variables
// (they become egg variables when running as a Pterodactyl server).
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Listen       string
	AccessKey    string
	SecretKey    string
	Region       string
	Bucket       string
	DataDir      string
	ResticRepo   string
	ResticBin    string
	ResticPass   string // RESTIC_PASSWORD, may be empty if PassFile is set
	ResticPassFn string // RESTIC_PASSWORD_FILE
	LogLevel     slog.Level

	PublicURL            string
	MaxPartBytes         int64
	MaxSpoolBytes        int64
	MaxConcurrentIngests int
	MaxConcurrentDumps   int
	MaxIndexFiles        int
	IngestTimeout        time.Duration

	AdminToken          string // empty disables the admin API
	MaintenanceHour     int
	MaintenanceMinute   int
	PruneEnabled        bool
	CheckInterval       time.Duration // 0 disables scheduled checks
	CheckReadDataSubset string
	RepoCapacityBytes   int64 // 0 = unknown
	NtfyURL             string
	NtfyToken           string
	MaintenanceTimeout  time.Duration // deadline per maintenance job
	MaintenanceBusyWait time.Duration // how long maintenance waits for running ingests/restores
	DownloadTimeout     time.Duration // deadline per restore/download
}

var (
	timeRe   = regexp.MustCompile(`^([01]?\d|2[0-3]):([0-5]\d)$`)
	subsetRe = regexp.MustCompile(`^(\d+(\.\d+)?%|\d+/\d+|\d+[KMGT]?)$`)
)

func (c *Config) SpoolDir() string { return filepath.Join(c.DataDir, "spool") }
func (c *Config) CacheDir() string { return filepath.Join(c.DataDir, "restic-cache") }
func (c *Config) DBPath() string   { return filepath.Join(c.DataDir, "peb.db") }

// Load builds a Config from the given lookup function (os.Getenv in production).
func Load(get func(string) string) (*Config, error) {
	def := func(key, fallback string) string {
		if v := get(key); v != "" {
			return v
		}
		return fallback
	}
	c := &Config{
		Listen:       get("PEB_LISTEN"),
		AccessKey:    get("PEB_ACCESS_KEY"),
		SecretKey:    get("PEB_SECRET_KEY"),
		Region:       def("PEB_REGION", "us-east-1"),
		Bucket:       get("PEB_BUCKET"),
		DataDir:      def("PEB_DATA_DIR", "/home/container/data"),
		ResticBin:    def("PEB_RESTIC_BIN", "restic"),
		ResticRepo:   get("RESTIC_REPOSITORY"),
		ResticPass:   get("RESTIC_PASSWORD"),
		ResticPassFn: get("RESTIC_PASSWORD_FILE"),
	}
	if c.Listen == "" {
		if p := get("SERVER_PORT"); p != "" {
			c.Listen = "0.0.0.0:" + p
		} else {
			c.Listen = ":9000"
		}
	}
	if c.ResticRepo == "" {
		c.ResticRepo = filepath.Join(c.DataDir, "repo")
	}
	if lvl := get("PEB_LOG_LEVEL"); lvl != "" {
		if err := c.LogLevel.UnmarshalText([]byte(strings.ToUpper(lvl))); err != nil {
			return nil, fmt.Errorf("PEB_LOG_LEVEL: %w", err)
		}
	}

	var errs []error
	num := func(name string, def int64) int64 {
		v := get(name)
		if v == "" {
			return def
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			errs = append(errs, fmt.Errorf("%s: must be a non-negative integer", name))
			return def
		}
		return n
	}
	c.PublicURL = strings.TrimRight(get("PEB_PUBLIC_URL"), "/")
	c.MaxPartBytes = num("PEB_MAX_PART_BYTES", 1<<30)
	c.MaxSpoolBytes = num("PEB_MAX_SPOOL_BYTES", 8<<30)
	c.MaxConcurrentIngests = int(max(num("PEB_MAX_CONCURRENT_INGESTS", 2), 1))
	c.MaxConcurrentDumps = int(max(num("PEB_MAX_CONCURRENT_DUMPS", 2), 1))
	c.MaxIndexFiles = int(num("PEB_MAX_INDEX_FILES", 500000))
	c.IngestTimeout = 6 * time.Hour
	if v := get("PEB_INGEST_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			errs = append(errs, errors.New("PEB_INGEST_TIMEOUT: must be a duration like 6h"))
		} else {
			c.IngestTimeout = d
		}
	}
	c.AdminToken = get("PEB_ADMIN_TOKEN")
	c.NtfyURL = get("PEB_NTFY_URL")
	c.NtfyToken = get("PEB_NTFY_TOKEN")
	c.RepoCapacityBytes = num("PEB_REPO_CAPACITY_BYTES", 0)
	m := timeRe.FindStringSubmatch(def("PEB_MAINTENANCE_TIME", "02:00"))
	if m == nil {
		errs = append(errs, errors.New("PEB_MAINTENANCE_TIME: must be HH:MM"))
	} else {
		c.MaintenanceHour, _ = strconv.Atoi(m[1])
		c.MaintenanceMinute, _ = strconv.Atoi(m[2])
	}
	c.PruneEnabled = true
	if v := get("PEB_PRUNE_ENABLED"); v != "" {
		if b, err := strconv.ParseBool(v); err != nil {
			errs = append(errs, errors.New("PEB_PRUNE_ENABLED: must be true or false"))
		} else {
			c.PruneEnabled = b
		}
	}
	c.CheckInterval = 168 * time.Hour
	if v := get("PEB_CHECK_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			errs = append(errs, errors.New("PEB_CHECK_INTERVAL: must be a duration like 168h"))
		} else {
			c.CheckInterval = d
		}
	}
	dur := func(name string, def time.Duration) time.Duration {
		v := get(name)
		if v == "" {
			return def
		}
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			errs = append(errs, fmt.Errorf("%s: must be a positive duration like 3h", name))
			return def
		}
		return d
	}
	c.MaintenanceTimeout = dur("PEB_MAINTENANCE_TIMEOUT", 3*time.Hour)
	c.MaintenanceBusyWait = dur("PEB_MAINTENANCE_BUSY_WAIT", 15*time.Minute)
	c.DownloadTimeout = dur("PEB_DOWNLOAD_TIMEOUT", 2*time.Hour)
	if c.AdminToken != "" && len(c.AdminToken) < 32 {
		errs = append(errs, errors.New("PEB_ADMIN_TOKEN: must be at least 32 characters (or unset to disable the admin API)"))
	}
	if c.Bucket == "admin" {
		errs = append(errs, errors.New("PEB_BUCKET: \"admin\" is reserved for the admin API path"))
	}
	c.CheckReadDataSubset = get("PEB_CHECK_READ_DATA_SUBSET")
	if c.CheckReadDataSubset != "" && !subsetRe.MatchString(c.CheckReadDataSubset) {
		errs = append(errs, errors.New("PEB_CHECK_READ_DATA_SUBSET: must look like 5%, 1/5 or 100M"))
	}
	for _, r := range []struct{ name, val string }{
		{"PEB_ACCESS_KEY", c.AccessKey}, {"PEB_SECRET_KEY", c.SecretKey}, {"PEB_BUCKET", c.Bucket},
	} {
		if r.val == "" {
			errs = append(errs, fmt.Errorf("%s is required", r.name))
		}
	}
	if c.ResticPass == "" && c.ResticPassFn == "" {
		errs = append(errs, errors.New("RESTIC_PASSWORD or RESTIC_PASSWORD_FILE is required"))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return c, nil
}

// FromEnv loads the configuration from the process environment.
func FromEnv() (*Config, error) { return Load(os.Getenv) }
