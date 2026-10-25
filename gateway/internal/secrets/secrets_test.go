package secrets

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestScrub(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"nothing to hide", "nothing to hide"},
		{"s3:https://AKIAEXAMPLE:topsecret@s3.example.org/bucket", "s3:https://***@s3.example.org/bucket"},
		{"rest:http://user@host:8000/repo", "rest:http://***@host:8000/repo"},
		{"Fatal: unable to open sftp://bob:p%40ss@backup.example.org:22/x: dial failed", "Fatal: unable to open sftp://***@backup.example.org:22/x: dial failed"},
		{"https://example.org/a/b@c is fine", "https://example.org/a/b@c is fine"},
		{"user@host without scheme", "user@host without scheme"},
		{"AWS_SECRET_ACCESS_KEY=abc/def+ghi next", "AWS_SECRET_ACCESS_KEY=*** next"},
		{"env RESTIC_PASSWORD=hunter2", "env RESTIC_PASSWORD=***"},
		{"RESTIC_PASSWORD = hunter2", "RESTIC_PASSWORD=***"},
		{"B2_ACCOUNT_KEY=k1 RCLONE_CONFIG_PASS=p2", "B2_ACCOUNT_KEY=*** RCLONE_CONFIG_PASS=***"},
		{"GET /x?user=a&password=hunter2&b=1", "GET /x?user=a&password=***&b=1"},
		{"?token=abc123", "?token=***"},
		{"url ?X-Amz-Signature=deadbeef&x=1", "url ?X-Amz-Signature=***&x=1"},
		{"password=hunter2 failed", "password=*** failed"},
		{"Authorization: Bearer abc.def-123", "Authorization: Bearer ***"},
		{"nopassword=1", "nopassword=1"},
		{"restic copy: exit 1: Fatal: wrong password or no key found", "restic copy: exit 1: Fatal: wrong password or no key found"},
	} {
		if got := Scrub(c.in); got != c.want {
			t.Errorf("Scrub(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestScrubWith(t *testing.T) {
	got := ScrubWith("login hunter2 to rclone:gdrive:peb failed, url=https://u:p@h/x", "hunter2", "", "rclone:gdrive:peb")
	if strings.Contains(got, "hunter2") || strings.Contains(got, "gdrive") || strings.Contains(got, "u:p") {
		t.Fatalf("%q", got)
	}
}

func TestHandlerScrubsMessageAndAttributes(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(NewHandler(slog.NewTextHandler(&buf, nil))).With("repo", "s3:https://k:s3cret@host/b")
	log.Error("copy failed RESTIC_PASSWORD=pw1", "err", errors.New("dial https://a:b@h/x"),
		"group", slog.GroupValue(slog.String("url", "?password=pw2")), "n", 3)
	out := buf.String()
	for _, leak := range []string{"s3cret", "pw1", "pw2", "a:b"} {
		if strings.Contains(out, leak) {
			t.Fatalf("%q leaked: %s", leak, out)
		}
	}
	if !strings.Contains(out, "n=3") || !strings.Contains(out, "https://***@h/x") {
		t.Fatalf("output mangled: %s", out)
	}
}

func TestScrubQuotedAndPrefixed(t *testing.T) {
	for in, want := range map[string]string{
		`RESTIC_PASSWORD="abc def" x`: `RESTIC_PASSWORD=*** x`,
		`AWS_SECRET_ACCESS_KEY='k/1'`: `AWS_SECRET_ACCESS_KEY=***`,
		`client_secret=abc&x=1`:       `client_secret=***&x=1`,
		`api-key=abc`:                 `api-key=abc`,
		`x-api_key=abc`:               `x-api_key=***`,
	} {
		if got := Scrub(in); got != want {
			t.Errorf("Scrub(%q) = %q, want %q", in, got, want)
		}
	}
}

type panicky struct{}

func (*panicky) String() string { return "boom" + string([]byte{}[0:0]) + nilDeref() }
func nilDeref() string          { var p *struct{ s string }; return p.s }

func TestHandlerAnyValuesKnownAndTypedNil(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(NewHandler(slog.NewTextHandler(&buf, nil), "hunter2"))
	var p *panicky
	log.Info("x", "args", []string{"restic", "-r", "rest:https://u:pw@h/r"}, "pw", "my hunter2 here", "nilp", p, "nilv", nil)
	out := buf.String()
	for _, leak := range []string{"u:pw", "hunter2"} {
		if strings.Contains(out, leak) {
			t.Fatalf("%q leaked: %s", leak, out)
		}
	}
}
