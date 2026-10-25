package offsite

import (
	"context"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ptero-enhanced-backup/gateway/internal/gate"
	"ptero-enhanced-backup/gateway/internal/restic"
	"ptero-enhanced-backup/gateway/internal/store"
)

// Two real local repositories: backup, copy, deletion in the primary, nightly mirror.
func TestIntegrationCopyAndMirror(t *testing.T) {
	bin, err := exec.LookPath("restic")
	if err != nil {
		t.Skip("restic binary not found, skipping integration test")
	}
	dir := t.TempDir()
	ctx := context.Background()
	primary := &restic.Client{Bin: bin, Repo: filepath.Join(dir, "primary"), Password: "primary-pw", CacheDir: filepath.Join(dir, "cache")}
	if err := primary.Init(ctx); err != nil {
		t.Fatal(err)
	}
	secondary := &restic.Client{
		Bin: bin, Repo: filepath.Join(dir, "secondary"), Password: "secondary-pw", CacheDir: filepath.Join(dir, "cache"),
		FromRepo: primary.Repo, FromPassword: "primary-pw",
	}
	st, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := &Service{
		Store: st, Primary: primary, Secondary: secondary, Gate: &gate.Gate{}, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Cfg: Config{Repo: secondary.Repo, PruneEnable: true, ForgetGrace: -1, Secrets: []string{"primary-pw", "secondary-pw"}},
	}
	defer svc.Wait()

	backup := func(content string) string {
		sum, err := primary.Backup(ctx, strings.NewReader(content), restic.BackupOptions{Filename: "backup.tar.gz", Host: "peb", Tags: []string{"server:s1"}})
		if err != nil {
			t.Fatal(err)
		}
		return sum.SnapshotID
	}
	s1 := backup(strings.Repeat("first backup ", 5000))
	s2 := backup(strings.Repeat("second backup ", 5000))

	if err := svc.Setup(ctx); err != nil { // creates the secondary with the chunker params of the primary
		t.Fatal(err)
	}
	pp, _ := primary.ChunkerPolynomial(ctx)
	sp, _ := secondary.ChunkerPolynomial(ctx)
	if pp == "" || pp != sp {
		t.Fatalf("chunker params differ: %q %q", pp, sp)
	}
	if err := svc.Setup(ctx); err != nil { // second call: repository exists and matches
		t.Fatal(err)
	}

	svc.Nightly(ctx)
	sec, _ := secondary.Snapshots(ctx)
	have := covered(sec)
	if len(sec) != 2 || !have[s1] || !have[s2] {
		t.Fatalf("after copy: %+v", sec)
	}
	if sec[0].Original == "" {
		t.Fatal("copied snapshots must carry their original id")
	}
	if status, _ := svc.Status(ctx); status.Lag == nil || *status.Lag != 0 || status.LastError != "" {
		t.Fatalf("%+v", status)
	}

	// A third backup is only missing on the secondary until the next copy.
	s3 := backup(strings.Repeat("third backup ", 5000))
	svc.Nightly(ctx)
	sec, _ = secondary.Snapshots(ctx)
	if len(sec) != 3 || !covered(sec)[s3] {
		t.Fatalf("incremental copy: %+v", sec)
	}

	// Deleted in the primary: the nightly run mirrors it.
	if err := primary.Forget(ctx, s1); err != nil {
		t.Fatal(err)
	}
	svc.Nightly(ctx)
	sec, _ = secondary.Snapshots(ctx)
	have = covered(sec)
	if len(sec) != 2 || have[s1] || !have[s2] || !have[s3] {
		t.Fatalf("after mirror: %+v", sec)
	}
	if err := secondary.Check(ctx, ""); err != nil {
		t.Fatal(err)
	}
	js, _ := st.Jobs(ctx, 50)
	for _, j := range js {
		if j.Result != store.JobOK {
			t.Fatalf("job %s: %s %s", j.Type, j.Result, j.Message)
		}
	}
	if status, _ := svc.Status(ctx); status.StoredBytes == nil || *status.StoredBytes <= 0 {
		t.Fatalf("stored bytes missing: %+v", status)
	}

	// The secondary restores on its own, with its own password.
	for _, sn := range sec {
		rc, err := secondary.Dump(ctx, sn.ID, "/backup.tar.gz")
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil || len(data) == 0 {
			t.Fatalf("restore from secondary: %d bytes, %v", len(data), err)
		}
	}
}
