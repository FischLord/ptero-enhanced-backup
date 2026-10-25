package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type captured struct {
	method, path, query, auth, title, tags, prio, ctype string
	body                                                string
}

func capture(t *testing.T) (*httptest.Server, chan captured) {
	t.Helper()
	ch := make(chan captured, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ch <- captured{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("Title"),
			r.Header.Get("Tags"), r.Header.Get("Priority"), r.Header.Get("Content-Type"), string(b)}
	}))
	t.Cleanup(srv.Close)
	return srv, ch
}

var failedEv = Event{Key: EventBackupFailed, Params: Params{"backup": "b1", "server": "s1", "timed_out": true}}

func TestNtfyPostsAndKeepsHeaders(t *testing.T) {
	srv, ch := capture(t)
	n := &Notifier{Lang: "de", Channels: []Channel{&Ntfy{URL: srv.URL + "/peb", Token: "tk_123"}}}
	n.Send(failedEv)
	n.Wait()
	g := <-ch
	if g.method != "POST" || g.path != "/peb" || g.auth != "Bearer tk_123" || g.title != "PEB Backup fehlgeschlagen" ||
		g.body != "Backup b1 von Server s1 fehlgeschlagen (Zeitüberschreitung). Details im Gateway-Log." ||
		g.prio != "high" || g.tags != "warning" {
		t.Fatalf("request: %+v", g)
	}
}

func TestNtfyTitleIsASCII(t *testing.T) {
	if got := asciiFold("Größe – Übung… ok"); got != "Groesse - Uebung... ok" {
		t.Fatalf("%q", got)
	}
	srv, ch := capture(t)
	c := &Ntfy{URL: srv.URL}
	if err := c.Send(context.Background(), Message{Title: "Zeitüberschreitung ✓", Body: "x", Severity: SevInfo}); err != nil {
		t.Fatal(err)
	}
	g := <-ch
	if g.title != "Zeitueberschreitung" || g.prio != "" {
		t.Fatalf("%+v", g)
	}
}

func TestNtfyWithoutToken(t *testing.T) {
	srv, ch := capture(t)
	if err := (&Ntfy{URL: srv.URL}).Send(context.Background(), Message{Title: "t"}); err != nil {
		t.Fatal(err)
	}
	if g := <-ch; g.auth != "" {
		t.Fatalf("auth header %q", g.auth)
	}
}

func TestLanguageSelection(t *testing.T) {
	for _, k := range []string{EventBackupFailed, EventBackupAbandoned, EventMaintenanceFailed, EventPruneSkipped,
		EventTrashWarning, EventServerDeleted, EventOffsiteFailed, EventTest} {
		en, _ := Render("en", Event{Key: k})
		de, _ := Render("de", Event{Key: k})
		def, _ := Render("", Event{Key: k})
		odd, _ := Render("fr", Event{Key: k})
		if en == de || def != en || odd != en {
			t.Fatalf("%s: en=%q de=%q def=%q", k, en, de, def)
		}
		if SeverityOf(k) == "" {
			t.Fatal(k)
		}
	}
	title, body := Render("en", failedEv)
	if title != "PEB backup failed" || !strings.Contains(body, "failed (timeout)") {
		t.Fatalf("%q %q", title, body)
	}
	_, body = Render("de", Event{Key: EventTrashWarning, Params: Params{"count": 2, "gb": 1.5}})
	if !strings.Contains(body, "2 Backups (1.5 GB Originaldaten)") {
		t.Fatal(body)
	}
	if title, _ := Render("en", Event{Key: "nope"}); title == "" {
		t.Fatal("unknown key")
	}
	var nilN *Notifier
	if nilN.Language() != "en" {
		t.Fatal("nil language")
	}
}

func TestWebhookFormats(t *testing.T) {
	m := Message{Event: "backup_failed", Severity: SevError, Title: "T", Body: "B", Time: time.Date(2026, 10, 25, 1, 2, 3, 0, time.UTC)}
	cases := map[string]func(map[string]any) bool{
		FormatJSON: func(v map[string]any) bool {
			return v["event"] == "backup_failed" && v["title"] == "T" && v["message"] == "B" && v["severity"] == "error" && v["time"] == "2026-10-25T01:02:03Z"
		},
		FormatDiscord: func(v map[string]any) bool {
			e := v["embeds"].([]any)[0].(map[string]any)
			return e["title"] == "T" && e["description"] == "B" && e["color"].(float64) != 0
		},
		FormatSlack:  func(v map[string]any) bool { return v["text"] == "*T*\nB" },
		FormatGotify: func(v map[string]any) bool { return v["title"] == "T" && v["message"] == "B" && v["priority"] == 8.0 },
	}
	for f, check := range cases {
		srv, ch := capture(t)
		w := &Webhook{URL: srv.URL + "/hook?token=abc", Fmt: f}
		if err := w.Send(context.Background(), m); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		g := <-ch
		var v map[string]any
		if err := json.Unmarshal([]byte(g.body), &v); err != nil || !check(v) {
			t.Fatalf("%s payload: %s", f, g.body)
		}
		if g.method != "POST" || g.ctype != "application/json" || g.path != "/hook" || g.query != "token=abc" {
			t.Fatalf("%s request: %+v", f, g)
		}
	}
	if _, err := Payload("bogus", m); err == nil {
		t.Fatal("unknown format accepted")
	}
	if (&Webhook{}).Format() != "json" {
		t.Fatal("default format")
	}
	long, _ := Payload(FormatDiscord, Message{Title: strings.Repeat("x", 500), Body: strings.Repeat("y", 9000)})
	if len(long) > 5000 {
		t.Fatalf("discord not truncated: %d", len(long))
	}
}

func TestTargetRedaction(t *testing.T) {
	cases := map[string]string{
		"https://discord.com/api/webhooks/123/SECRET": "https://discord.com",
		"https://user:pw@ntfy.example:8443/topic?x=1": "https://ntfy.example:8443",
		"::bad":      "invalid URL",
		"/just/path": "invalid URL",
	}
	for in, want := range cases {
		if got := (&Webhook{URL: in}).Target(); got != want {
			t.Errorf("%q -> %q", in, got)
		}
		if got := (&Ntfy{URL: in}).Target(); got != want {
			t.Errorf("ntfy %q -> %q", in, got)
		}
	}
	s := &SMTP{Host: "mail.example", Port: 465, Password: "pw", To: []string{"a@x.de", "b@x.de"}}
	if got := s.Target(); got != "mail.example:465 → 2 recipients" {
		t.Fatal(got)
	}
	s.To, s.Port = s.To[:1], 0
	if got := s.Target(); got != "mail.example:587 → 1 recipient" {
		t.Fatal(got)
	}
}

func TestErrorsDoNotLeakURL(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	w := &Webhook{URL: bad.URL + "/secret-hook"}
	err := w.Send(context.Background(), Message{})
	if err == nil || strings.Contains(err.Error(), "secret-hook") {
		t.Fatalf("%v", err)
	}
	bad.Close()
	err = w.Send(context.Background(), Message{})
	if err == nil || strings.Contains(err.Error(), "secret-hook") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("%v", err)
	}
}

func TestSendScrubsCredentials(t *testing.T) {
	srv, ch := capture(t)
	n := &Notifier{Channels: []Channel{&Webhook{URL: srv.URL}}}
	n.Send(Event{Key: EventOffsiteFailed, Params: Params{"detail": "copy to sftp://bob:pw1@host/x failed, RESTIC_PASSWORD=pw2"}})
	n.Wait()
	got := (<-ch).body
	if strings.Contains(got, "pw1") || strings.Contains(got, "pw2") || !strings.Contains(got, "sftp://***@host/x") {
		t.Fatalf("%q", got)
	}
}

func TestFailingChannelDoesNotBlockOthers(t *testing.T) {
	good, ch := capture(t)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()
	block := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer hang.Close()
	defer close(block)
	n := &Notifier{Timeout: 200 * time.Millisecond, Channels: []Channel{
		&Webhook{URL: bad.URL}, &Webhook{URL: hang.URL}, &Ntfy{URL: good.URL + "/t"}, &Webhook{URL: good.URL + "/w", Fmt: FormatSlack},
	}}
	start := time.Now()
	n.Send(failedEv)
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("Send blocked")
	}
	n.Wait()
	if len(ch) != 2 {
		t.Fatalf("good channels got %d messages", len(ch))
	}
	res := n.Test(context.Background())
	if len(res) != 4 || res[0].OK || res[1].OK || !res[2].OK || !res[3].OK {
		t.Fatalf("%+v", res)
	}
	if res[1].Error != "timeout" || !strings.Contains(res[0].Error, "500") {
		t.Fatalf("%+v", res)
	}
}

type panicChan struct{}

func (panicChan) Type() string                        { return "webhook" }
func (panicChan) Target() string                      { return "x" }
func (panicChan) Format() string                      { return "" }
func (panicChan) Send(context.Context, Message) error { panic("boom") }

func TestPanicInChannelIsContained(t *testing.T) {
	good, ch := capture(t)
	n := &Notifier{Channels: []Channel{panicChan{}, &Webhook{URL: good.URL}}}
	n.Send(failedEv)
	n.Wait()
	if len(ch) != 1 {
		t.Fatal("other channel not served")
	}
}

func TestNilAndEmpty(t *testing.T) {
	var n *Notifier
	n.Send(failedEv)
	n.Wait()
	if len(n.Test(context.Background())) != 0 || len(n.Describe()) != 0 {
		t.Fatal("nil")
	}
	e := &Notifier{}
	e.Send(failedEv)
	if e.Describe() == nil || len(e.Test(context.Background())) != 0 {
		t.Fatal("empty")
	}
}

func TestTestMessageIsInfo(t *testing.T) {
	srv, ch := capture(t)
	n := &Notifier{Channels: []Channel{&Webhook{URL: srv.URL}}}
	n.Test(context.Background())
	var v map[string]string
	_ = json.Unmarshal([]byte((<-ch).body), &v)
	if v["event"] != "test" || v["severity"] != "info" {
		t.Fatalf("%v", v)
	}
}

func TestConfig(t *testing.T) {
	c := Config{WebhookURLs: SplitList(" https://a.example/x , ,https://b.example/y"), SMTPHost: "m", SMTPFrom: "PEB <p@x.de>", SMTPTo: []string{"a@x.de"}}
	if err := c.Normalize(); err != nil || c.Lang != "en" || c.WebhookFormat != "json" || c.SMTPPort != 587 || c.SMTPTLS != "starttls" {
		t.Fatalf("%v %+v", err, c)
	}
	n := New(c, nil)
	if len(n.Channels) != 3 || n.Language() != "en" {
		t.Fatalf("%d", len(n.Channels))
	}
	n = New(Config{NtfyURL: "https://ntfy.example/t"}, nil)
	if len(n.Channels) != 1 || n.Channels[0].Type() != "ntfy" {
		t.Fatal("ntfy only")
	}
	bad := []Config{
		{Lang: "fr"}, {WebhookFormat: "teams"}, {WebhookURLs: []string{"ftp://x"}}, {SMTPTLS: "ssl"},
		{SMTPPort: 70000}, {SMTPHost: "m"}, {SMTPHost: "m", SMTPFrom: "nope", SMTPTo: []string{"a@x.de"}},
		{SMTPHost: "m", SMTPFrom: "a@x.de", SMTPTo: []string{"nope"}},
	}
	for i, b := range bad {
		if err := b.Normalize(); err == nil {
			t.Errorf("case %d accepted", i)
		} else if strings.Contains(err.Error(), "ftp://x") {
			t.Errorf("error leaks URL: %v", err)
		}
	}
	d := Config{Lang: "DE"}
	if d.Normalize() != nil || d.Lang != "de" {
		t.Fatal("lang case")
	}
}
