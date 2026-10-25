//go:build load

package s3api_test

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ptero-enhanced-backup/gateway/internal/ingest"
)

func envInt(name string, def int64) int64 {
	if v, err := strconv.ParseInt(os.Getenv(name), 10, 64); err == nil {
		return v
	}
	return def
}

// vmHWM returns the peak resident set size of this process in KiB.
func vmHWM() int64 {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "VmHWM:") {
			fs := strings.Fields(sc.Text())
			n, _ := strconv.ParseInt(fs[1], 10, 64)
			return n
		}
	}
	return 0
}

// TestLoadWingsBackupSlowIngestSmallSpool is the live failure in miniature: a
// multi-GB archive uploaded faster than restic can take it, a spool of only two
// parts. The Wings-style client (retries 5xx with the same, consumed body) must
// get 200 for every part and the restored tar must be byte-identical.
//
// Run with `make test-load`. Sizes can be changed with PEB_LOAD_BIG_FILES,
// PEB_LOAD_BIG_MIB, PEB_LOAD_SMALL_FILES, PEB_LOAD_INGEST_MIBPS; keep the total
// at or below ~4 GB.
func TestLoadWingsBackupSlowIngestSmallSpool(t *testing.T) {
	bigFiles := int(envInt("PEB_LOAD_BIG_FILES", 5))
	bigMiB := envInt("PEB_LOAD_BIG_MIB", 560)
	smallFiles := int(envInt("PEB_LOAD_SMALL_FILES", 20000))
	mibps := envInt("PEB_LOAD_INGEST_MIBPS", 30)
	const part = 128 << 20
	const spool = 256 << 20

	arch := filepath.Join(t.TempDir(), "backup.tar.gz")
	f, err := os.Create(arch)
	if err != nil {
		t.Fatal(err)
	}
	bw := bufio.NewWriterSize(f, 1<<20)
	t0 := time.Now()
	sha, plain, err := writeTarGz(bw, smallFiles, bigFiles, bigMiB<<20)
	if err == nil {
		err = bw.Flush()
	}
	if err != nil {
		t.Fatal(err)
	}
	total, _ := f.Seek(0, 2)
	f.Seek(0, 0)
	t.Logf("archive: %d MiB tar, %d MiB gzip, %d small files, %d big files, built in %s",
		plain>>20, total>>20, smallFiles, bigFiles, time.Since(t0).Round(time.Second))

	var sb *slowBackend
	e := newEnvWith(t, func(inner ingest.Backend) ingest.Backend {
		sb = newSlowBackend(inner, mibps<<20)
		return sb
	}, spool)
	e.api.SpoolWait = 60 * time.Minute

	var peak int64
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				if u := e.mgr.Quota.Used(); u > peak {
					peak = u
				}
			}
		}
	}()

	t0 = time.Now()
	st := runWingsBackup(t, e, sb, "srv/load.tar.gz", "srv", f, total, sha, plain, part)
	t.Logf("backup ok in %s: %d parts of %d MiB, %d waited >100ms (longest %s), 5xx=%d retries=%d",
		time.Since(t0).Round(time.Second), st.parts.Load(), part>>20, st.slowParts.Load(),
		time.Duration(st.maxPart.Load()).Round(time.Second), st.status5xx.Load(), st.retries.Load())
	t.Logf("spool peak %d MiB of %d MiB; test process peak RSS %d MiB (restic runs as a child process, not included)",
		peak>>20, spool>>20, vmHWM()>>10)
	if peak > spool {
		t.Fatalf("spool quota exceeded: %d", peak)
	}
	if st.slowParts.Load() == 0 {
		t.Fatal("load test never hit a full spool; make the ingest slower or the archive bigger")
	}
}
