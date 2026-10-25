package ingest

import (
	"archive/tar"
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"
)

func fuzzTar(hs []tar.Header, bodies []string, finish bool) []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for i := range hs {
		h := hs[i]
		b := bodies[i]
		h.Size = int64(len(b))
		if h.Typeflag == tar.TypeDir || h.Typeflag == tar.TypeSymlink || h.Typeflag == tar.TypeXGlobalHeader {
			h.Size = 0
			b = ""
		}
		if tw.WriteHeader(&h) != nil {
			continue
		}
		_, _ = tw.Write([]byte(b))
	}
	if finish {
		_ = tw.Close()
	} else {
		_ = tw.Flush()
	}
	return buf.Bytes()
}

// rawHeader builds a 512-byte ustar header with a valid checksum.
func rawHeader(name string, typeflag byte, sizeOctal string) []byte {
	h := make([]byte, 512)
	copy(h, name)
	copy(h[124:], sizeOctal+"\x00")
	h[156] = typeflag
	copy(h[257:], "ustar\x0000")
	for i := 148; i < 156; i++ {
		h[i] = ' '
	}
	sum := 0
	for _, c := range h {
		sum += int(c)
	}
	copy(h[148:], fmt.Sprintf("%06o\x00 ", sum))
	return h
}

// FuzzIndexTar feeds arbitrary bytes into indexTar. Seeds cover valid
// streams, GNU long names, PAX headers, sparse files and truncations.
func FuzzIndexTar(f *testing.F) {
	mt := time.Unix(1700000000, 0)
	long := strings.Repeat("d/", 200) + "file"
	seeds := [][]byte{
		fuzzTar([]tar.Header{{Name: "./a.txt", Typeflag: tar.TypeReg, Mode: 0o644, ModTime: mt}, {Name: "dir/", Typeflag: tar.TypeDir}, {Name: "l", Typeflag: tar.TypeSymlink, Linkname: "../x"}}, []string{"hello", "", ""}, true),
		fuzzTar([]tar.Header{{Name: "../../etc/passwd", Typeflag: tar.TypeReg}}, []string{"x"}, true),
		fuzzTar([]tar.Header{{Name: long, Typeflag: tar.TypeReg, Format: tar.FormatGNU}}, []string{"x"}, true),
		fuzzTar([]tar.Header{{Name: long, Typeflag: tar.TypeReg, Format: tar.FormatPAX, PAXRecords: map[string]string{"comment": "c"}}}, []string{"x"}, true),
		fuzzTar([]tar.Header{{Name: "g", Typeflag: tar.TypeXGlobalHeader, PAXRecords: map[string]string{"k": "v"}}}, []string{""}, true),
		fuzzTar([]tar.Header{{Name: "a", Typeflag: tar.TypeReg}}, []string{"0123456789"}, false),
		{},
		bytes.Repeat([]byte{0}, 1024),
		bytes.Repeat([]byte{0xff}, 512),
		rawHeader("big", '0', "77777777777"), // ~8 GiB declared, no body
		rawHeader("big", '0', "77777777777")[:300],
		rawHeader("sp", 'S', "77777777777"),
		rawHeader("px", 'x', "00000000020"),
		append(rawHeader("px", 'x', "00000000040"), []byte("99999999999 path=x\n"+strings.Repeat("\x00", 512))...),
		append(rawHeader("ln", 'L', "77777777777"), 'a'),
		[]byte("1024 path=" + strings.Repeat("a", 10) + "\n"),
	}
	for _, s := range seeds {
		f.Add(s, 0)
		f.Add(s, 1)
	}
	f.Fuzz(func(t *testing.T, data []byte, max int) {
		if max < 0 || max > 1000 {
			max = 0
		}
		out, trunc, _ := indexTar(bytes.NewReader(data), max)
		if max > 0 && len(out) > max {
			t.Fatalf("recorded %d entries > max %d", len(out), max)
		}
		if trunc && max == 0 {
			t.Fatal("truncated without limit")
		}
		if len(out) > len(data)/512+1 {
			t.Fatalf("%d entries from %d bytes", len(out), len(data))
		}
	})
}
