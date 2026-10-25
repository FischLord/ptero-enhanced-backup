package ingest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"ptero-enhanced-backup/gateway/internal/store"
)

// putAsync starts PutPartOpts in the background.
func putAsync(u *Upload, n int, data []byte, o PutOptions) <-chan error {
	ch := make(chan error, 1)
	go func() {
		o.Size = int64(len(data))
		_, err := u.PutPartOpts(n, bytes.NewReader(data), o)
		ch <- err
	}()
	return ch
}

func quotaUpload(t *testing.T, max int64) (*Upload, *Quota) {
	t.Helper()
	q := &Quota{Max: max}
	u := newUpload("up", "s/b.tar.gz", "s", "b", t.TempDir(), q)
	if err := os.MkdirAll(u.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return u, q
}

func TestPartWaitsForSpoolSpace(t *testing.T) {
	u, q := quotaUpload(t, 10)
	if _, err := u.PutPartOpts(1, bytes.NewReader(make([]byte, 6)), PutOptions{Size: 6}); err != nil {
		t.Fatal(err)
	}
	done := putAsync(u, 2, bytes.Repeat([]byte{7}, 6), PutOptions{WaitTimeout: 10 * time.Second})
	select {
	case err := <-done:
		t.Fatalf("must block while the spool is full, got %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	// The pipeline consumes part 1; that frees space and releases the writer.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rd := &partReader{u: u, ctx: ctx}
	if _, err := io.ReadFull(rd, make([]byte, 6)); err != nil {
		t.Fatal(err)
	}
	rd.closeCur()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("waiting part failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("writer was not woken when space was freed")
	}
	if q.Used() != 6 {
		t.Fatalf("used %d", q.Used())
	}
}

func TestPartWaitTimesOut(t *testing.T) {
	u, q := quotaUpload(t, 10)
	u.PutPartOpts(1, bytes.NewReader(make([]byte, 8)), PutOptions{Size: 8})
	start := time.Now()
	_, err := u.PutPartOpts(2, bytes.NewReader(make([]byte, 8)), PutOptions{Size: 8, WaitTimeout: 200 * time.Millisecond})
	if !errors.Is(err, ErrSpoolTimeout) {
		t.Fatalf("want ErrSpoolTimeout, got %v", err)
	}
	if time.Since(start) < 150*time.Millisecond {
		t.Fatal("returned before the timeout")
	}
	if q.Used() != 8 || len(u.Parts()) != 1 {
		t.Fatalf("failed wait left state behind: used=%d parts=%d", q.Used(), len(u.Parts()))
	}
}

func TestPartWaitCancelledByContext(t *testing.T) {
	u, _ := quotaUpload(t, 10)
	u.PutPartOpts(1, bytes.NewReader(make([]byte, 8)), PutOptions{Size: 8})
	ctx, cancel := context.WithCancel(context.Background())
	done := putAsync(u, 2, make([]byte, 8), PutOptions{WaitTimeout: time.Minute, Ctx: ctx})
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrBodyRead) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not end the wait")
	}
}

func TestPartLargerThanSpoolFailsAtOnce(t *testing.T) {
	u, _ := quotaUpload(t, 10)
	start := time.Now()
	_, err := u.PutPartOpts(1, bytes.NewReader(make([]byte, 11)), PutOptions{Size: 11, WaitTimeout: time.Minute})
	if !errors.Is(err, ErrSpoolFull) || time.Since(start) > time.Second {
		t.Fatalf("%v after %s", err, time.Since(start))
	}
	// Without a wait budget a full spool is reported immediately too.
	u.PutPartOpts(1, bytes.NewReader(make([]byte, 8)), PutOptions{Size: 8})
	if _, err := u.PutPartOpts(2, bytes.NewReader(make([]byte, 8)), PutOptions{Size: 8}); !errors.Is(err, ErrSpoolFull) {
		t.Fatalf("%v", err)
	}
}

func TestWaitingPartEndsWhenUploadFails(t *testing.T) {
	be := &fakeBackend{}
	m := newTestManager(t, be)
	m.Quota.Max = 10
	ctx := context.Background()
	u, err := m.Create(ctx, "srv/bk.tar.gz", "srv", "bk")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.PutPartOpts(2, bytes.NewReader(make([]byte, 8)), PutOptions{Size: 8}); err != nil {
		t.Fatal(err) // part 1 is missing, so nothing is consumed and the spool stays full
	}
	done := putAsync(u, 3, make([]byte, 8), PutOptions{WaitTimeout: time.Minute})
	time.Sleep(100 * time.Millisecond)
	m.Fail(u.ID, errors.New("spool wait gave up"))
	select {
	case err := <-done:
		if !errors.Is(err, ErrPipelineFailed) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waiting part did not notice the failure")
	}
	<-u.done
	b, _ := m.Store.BackupByID(ctx, u.BackupID)
	if b.Status != store.StatusFailed || !strings.Contains(b.Error, "spool wait gave up") {
		t.Fatalf("backup: %+v", b)
	}
	if m.Quota.Used() != 0 {
		t.Fatalf("spool not released: %d", m.Quota.Used())
	}
	if _, err := os.Stat(u.dir); !os.IsNotExist(err) {
		t.Fatalf("spool dir kept: %v", err)
	}
}

type failingReader struct {
	data []byte
	err  error
}

func (f *failingReader) Read(p []byte) (int, error) {
	if len(f.data) == 0 {
		return 0, f.err
	}
	n := copy(p, f.data)
	f.data = f.data[n:]
	return n, nil
}

func TestPartLengthMustMatch(t *testing.T) {
	u, q := quotaUpload(t, 100)
	// Body ends early with a clean EOF: never acknowledged.
	_, err := u.PutPartOpts(1, bytes.NewReader(make([]byte, 5)), PutOptions{Size: 10})
	if !errors.Is(err, ErrBodyRead) {
		t.Fatalf("short body: %v", err)
	}
	// Connection dies in the middle of the body.
	_, err = u.PutPartOpts(1, &failingReader{data: make([]byte, 5), err: io.ErrUnexpectedEOF}, PutOptions{Size: 10})
	if !errors.Is(err, ErrBodyRead) {
		t.Fatalf("broken body: %v", err)
	}
	if len(u.Parts()) != 0 || q.Used() != 0 {
		t.Fatalf("incomplete part was kept: parts=%d used=%d", len(u.Parts()), q.Used())
	}
	if _, err := u.PutPartOpts(1, bytes.NewReader(make([]byte, 10)), PutOptions{Size: 10}); err != nil {
		t.Fatal(err)
	}
}

func TestDuplicatePartLastCompleteVersionWins(t *testing.T) {
	u, q := quotaUpload(t, 100)
	a, b := bytes.Repeat([]byte("a"), 10), bytes.Repeat([]byte("b"), 10)
	ea, _ := u.PutPartOpts(1, bytes.NewReader(a), PutOptions{Size: 10})
	// A failed re-upload (cut off) must not replace the good one.
	if _, err := u.PutPartOpts(1, bytes.NewReader(b[:4]), PutOptions{Size: 10}); err == nil {
		t.Fatal("cut-off retry accepted")
	}
	if p := u.Parts(); len(p) != 1 || p[0].ETag != ea {
		t.Fatalf("good part lost: %+v", p)
	}
	// A complete re-upload replaces it.
	eb, err := u.PutPartOpts(1, bytes.NewReader(b), PutOptions{Size: 10})
	if err != nil || eb == ea {
		t.Fatalf("%v %s", err, eb)
	}
	if q.Used() != 10 {
		t.Fatalf("quota after replace: %d", q.Used())
	}
	if _, err := u.Complete([]PartRef{{1, `"` + ea + `"`}}); !errors.Is(err, ErrInvalidPart) {
		t.Fatalf("stale ETag accepted: %v", err)
	}
	if _, err := u.Complete([]PartRef{{1, `"` + eb + `"`}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, _ := io.ReadAll(&partReader{u: u, ctx: ctx})
	if !bytes.Equal(got, b) {
		t.Fatalf("stream has %q, want the last version", got)
	}
}

func TestCompleteWithGapsAndWrongOrder(t *testing.T) {
	u, _ := quotaUpload(t, 100)
	e1, _ := u.PutPartOpts(1, bytes.NewReader([]byte("one")), PutOptions{Size: 3})
	e3, _ := u.PutPartOpts(3, bytes.NewReader([]byte("three")), PutOptions{Size: 5})
	if _, err := u.Complete([]PartRef{{1, e1}, {3, e3}}); !errors.Is(err, ErrInvalidPart) {
		t.Fatalf("gap: %v", err)
	}
	if _, err := u.Complete([]PartRef{{3, e3}, {1, e1}}); !errors.Is(err, ErrInvalidOrder) {
		t.Fatalf("order: %v", err)
	}
	if u.isCompleted() {
		t.Fatal("invalid Complete must not complete the upload")
	}
}

func tarGzOf(t *testing.T, build func(tw *tar.Writer), raw func(b []byte) []byte) []byte {
	t.Helper()
	var tb bytes.Buffer
	tw := tar.NewWriter(&tb)
	build(tw)
	tw.Close()
	body := tb.Bytes()
	if raw != nil {
		body = raw(body)
	}
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	gz.Write(body)
	gz.Close()
	return out.Bytes()
}

func runBroken(t *testing.T, archive []byte) (*fakeBackend, *store.Backup) {
	t.Helper()
	be := &fakeBackend{snapshot: "partial"}
	m := newTestManager(t, be)
	ctx := context.Background()
	u, _ := m.Create(ctx, "srv/bk.tar.gz", "srv", "bk")
	if _, err := u.PutPart(1, bytes.NewReader(archive)); err != nil {
		t.Fatal(err)
	}
	// A broken archive can fail the pipeline before Complete arrives; that is fine here.
	if _, err := m.Complete(ctx, u.ID, u.Key, []PartRef{{1, etagOf(string(archive))}}); err != nil && !errors.Is(err, ErrPipelineFailed) {
		t.Fatal(err)
	}
	<-u.done
	b, _ := m.Store.BackupByID(ctx, u.BackupID)
	return be, b
}

func TestTarErrorsFailTheBackup(t *testing.T) {
	file := func(tw *tar.Writer) {
		tw.WriteHeader(&tar.Header{Name: "a.bin", Typeflag: tar.TypeReg, Mode: 0o644, Size: 4096})
		tw.Write(make([]byte, 4096))
	}
	cases := map[string][]byte{
		// gzip is fine, the tar inside is cut in the middle of a member
		"truncated member": tarGzOf(t, file, func(b []byte) []byte { return b[:512+1000] }),
		// gzip is fine, but there is no tar header at all
		"bad header": tarGzOf(t, func(*tar.Writer) {}, func([]byte) []byte { return bytes.Repeat([]byte("this is not a tar header "), 100) }),
		// header block with a broken checksum
		"bad checksum": tarGzOf(t, file, func(b []byte) []byte { b[148] = 'x'; return b }),
	}
	for name, arch := range cases {
		t.Run(name, func(t *testing.T) {
			be, b := runBroken(t, arch)
			if b.Status != store.StatusFailed || b.Error == "" || b.SnapshotID != "" {
				t.Fatalf("backup must be failed without snapshot: %+v", b)
			}
			be.mu.Lock()
			defer be.mu.Unlock()
			if len(be.forgot) != 1 || be.forgot[0] != "partial" {
				t.Fatalf("snapshot of the broken stream not forgotten: %v", be.forgot)
			}
		})
	}
}

// Queued uploads fill the spool while the upload holding the only ingest slot
// waits for its next part. That part must be admitted at once; without the
// exception both sides wait for each other until the timeout.
func TestSlotHolderPartAdmittedWhenQueuedUploadsFillSpool(t *testing.T) {
	be := &fakeBackend{}
	m := newTestManager(t, be)
	m.Configure(Limits{MaxSpoolBytes: 10, MaxIngests: 1, MaxIndexFiles: 100, IngestTimeout: time.Minute})
	ctx := context.Background()
	a, err := m.Create(ctx, "s/a.tar.gz", "s", "a") // gets the slot, waits for part 1
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "slot", func() bool { return a.hasSlot.Load() })
	b, err := m.Create(ctx, "s/b.tar.gz", "s", "b") // queued behind A
	if err != nil {
		t.Fatal(err)
	}
	// B fills the whole spool while it waits for the slot.
	if _, err := b.PutPartOpts(1, bytes.NewReader(make([]byte, 10)), PutOptions{Size: 10}); err != nil {
		t.Fatal(err)
	}
	// A's first part would never fit by quota; it is the one the pipeline waits for.
	// (A's data is no valid gzip, the pipeline fails afterwards; only admission is tested.)
	start := time.Now()
	_, err = a.PutPartOpts(1, bytes.NewReader(make([]byte, 6)), PutOptions{Size: 6, WaitTimeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("part the pipeline waits for was not admitted: %v (after %s)", err, time.Since(start))
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("admission took %s", d)
	}
	// A part the pipeline does not wait for yet still waits for space.
	_, err = a.PutPartOpts(3, bytes.NewReader(make([]byte, 6)), PutOptions{Size: 6, WaitTimeout: 150 * time.Millisecond})
	if !errors.Is(err, ErrSpoolTimeout) && !errors.Is(err, ErrPipelineFailed) {
		t.Fatalf("part 3: %v", err)
	}
}
