package ingest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"math/rand"
	"strconv"
	"strings"
	"testing"
	"time"

	"ptero-enhanced-backup/gateway/internal/store"
)

func tarBytes(t *testing.T, build func(tw *tar.Writer)) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	build(tw)
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func addFile(t *testing.T, tw *tar.Writer, name string, data []byte, format tar.Format) {
	t.Helper()
	h := &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(data)), ModTime: time.Unix(1700000000, 0), Format: format}
	if err := tw.WriteHeader(h); err != nil {
		t.Fatal(err)
	}
	tw.Write(data)
}

func TestIndexTarHashKnownValue(t *testing.T) {
	data := tarBytes(t, func(tw *tar.Writer) {
		addFile(t, tw, "abc.txt", []byte("abc"), tar.FormatUSTAR)
		addFile(t, tw, "empty", nil, tar.FormatUSTAR)
		tw.WriteHeader(&tar.Header{Name: "dir/", Typeflag: tar.TypeDir, Mode: 0o755})
		tw.WriteHeader(&tar.Header{Name: "ln", Typeflag: tar.TypeSymlink, Linkname: "abc.txt"})
	})
	entries, _, err := indexTar(bytes.NewReader(data), 0)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]store.FileEntry{}
	for _, e := range entries {
		by[e.Path] = e
	}
	// sha256("abc"), sha256("")
	if got := hex.EncodeToString(by["abc.txt"].SHA256); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("abc: %s", got)
	}
	if got := hex.EncodeToString(by["empty"].SHA256); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty: %s", got)
	}
	if by["dir/"].SHA256 != nil || by["ln"].SHA256 != nil {
		t.Fatalf("non-files must have no hash: %+v %+v", by["dir/"], by["ln"])
	}
}

func TestIndexTarHashOff(t *testing.T) {
	data := tarBytes(t, func(tw *tar.Writer) { addFile(t, tw, "a", []byte("abc"), tar.FormatUSTAR) })
	entries, _, err := indexTarHash(bytes.NewReader(data), 0, false)
	if err != nil || len(entries) != 1 || entries[0].SHA256 != nil || entries[0].Size != 3 {
		t.Fatalf("%+v %v", entries, err)
	}
}

func TestIndexTarHashLongAndPAXPaths(t *testing.T) {
	long := strings.Repeat("deep/", 60) + "file-ünï.txt" // > 100 bytes, non-ASCII: PAX
	gnu := strings.Repeat("g", 150) + "/x"
	data := tarBytes(t, func(tw *tar.Writer) {
		addFile(t, tw, "before", []byte("1"), tar.FormatUSTAR)
		addFile(t, tw, long, []byte("pax content"), tar.FormatPAX)
		addFile(t, tw, gnu, []byte("gnu content"), tar.FormatGNU)
		addFile(t, tw, "after", []byte("2"), tar.FormatUSTAR)
	})
	entries, _, err := indexTar(bytes.NewReader(data), 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"before": "1", long: "pax content", gnu: "gnu content", "after": "2"}
	if len(entries) != len(want) {
		t.Fatalf("entries: %d", len(entries))
	}
	for _, e := range entries {
		c, ok := want[e.Path]
		if !ok {
			t.Fatalf("unexpected path %q", e.Path)
		}
		s := sha256.Sum256([]byte(c))
		if !bytes.Equal(e.SHA256, s[:]) {
			t.Fatalf("%q: wrong hash", e.Path)
		}
	}
}

func TestIndexTarHashTruncatedStream(t *testing.T) {
	data := tarBytes(t, func(tw *tar.Writer) {
		addFile(t, tw, "whole", []byte("complete"), tar.FormatUSTAR)
		addFile(t, tw, "cut", bytes.Repeat([]byte("z"), 5000), tar.FormatUSTAR)
	})
	// Cut in the middle of the body of "cut" (header 512 + padded body 512 for "whole", header 512 for "cut").
	cut := data[:512+512+512+2000]
	entries, _, err := indexTar(bytes.NewReader(cut), 0)
	if err == nil {
		t.Fatal("expected an error for a truncated stream")
	}
	for _, e := range entries {
		if e.Path == "cut" {
			t.Fatalf("truncated file must not be recorded: %+v", e)
		}
	}
	if len(entries) != 1 || entries[0].SHA256 == nil {
		t.Fatalf("entries before the cut stay intact: %+v", entries)
	}
}

func TestIndexTarHashBeyondLimitIsSkipped(t *testing.T) {
	data := tarBytes(t, func(tw *tar.Writer) {
		for _, n := range []string{"a", "b", "c"} {
			addFile(t, tw, n, []byte(n), tar.FormatUSTAR)
		}
	})
	entries, truncated, err := indexTar(bytes.NewReader(data), 2)
	if err != nil || !truncated || len(entries) != 2 || entries[0].SHA256 == nil {
		t.Fatalf("%+v %v %v", entries, truncated, err)
	}
}

// synthTar builds a tar of about total bytes with mixed file sizes (many small, some large).
func synthTar(total int64) []byte {
	rng := rand.New(rand.NewSource(1))
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	sizes := []int{200, 4 << 10, 64 << 10, 1 << 20, 8 << 20}
	payload := make([]byte, 8<<20)
	rng.Read(payload)
	var written int64
	for i := 0; written < total; i++ {
		sz := sizes[rng.Intn(len(sizes))]
		if i%3 == 0 {
			sz = sizes[rng.Intn(3)]
		}
		tw.WriteHeader(&tar.Header{Name: "dir" + strconv.Itoa(i%97) + "/f" + strconv.Itoa(i), Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(sz), ModTime: time.Unix(1700000000, 0)})
		tw.Write(payload[:sz])
		written += int64(sz)
	}
	tw.Close()
	return buf.Bytes()
}

// BenchmarkIngestPath measures gunzip -> tee(tar index, pipe to a draining consumer),
// (256 MB), i.e. the ingest path without restic (which is far slower than this).
func BenchmarkIngestPath(b *testing.B) {
	raw := synthTar(256 << 20)
	var zbuf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&zbuf, gzip.BestSpeed)
	zw.Write(raw)
	zw.Close()
	gzData := zbuf.Bytes()
	for _, hash := range []bool{false, true} {
		name := "nohash"
		if hash {
			name = "sha256"
		}
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(raw)))
			for i := 0; i < b.N; i++ {
				gz, err := gzip.NewReader(bytes.NewReader(gzData))
				if err != nil {
					b.Fatal(err)
				}
				pr, pw := io.Pipe()
				done := make(chan struct{})
				go func() { io.Copy(io.Discard, pr); close(done) }()
				tee := io.TeeReader(gz, pw)
				if _, _, err := indexTarHash(tee, 0, hash); err != nil {
					b.Fatal(err)
				}
				io.Copy(io.Discard, tee)
				pw.Close()
				<-done
			}
		})
	}
}

// BenchmarkIndexTar measures the index path (tar parse + optional SHA-256) on ~1 GB of mixed files.
func BenchmarkIndexTar(b *testing.B) {
	data := synthTar(1 << 30)
	for _, hash := range []bool{false, true} {
		name := "nohash"
		if hash {
			name = "sha256"
		}
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(data)))
			for i := 0; i < b.N; i++ {
				// LimitReader hides bytes.Reader's WriteTo/Seek, like the real tee does.
				if _, _, err := indexTarHash(io.LimitReader(bytes.NewReader(data), int64(len(data))), 0, hash); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
