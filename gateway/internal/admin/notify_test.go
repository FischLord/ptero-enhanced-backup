package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ptero-enhanced-backup/gateway/internal/notify"
)

func notifyFix(t *testing.T) (*fixture, *atomic.Int32) {
	t.Helper()
	hits := new(atomic.Int32)
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	t.Cleanup(ok.Close)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	t.Cleanup(bad.Close)
	f := newFixture(t)
	f.srv.NotifyAdmin = &notify.Notifier{Lang: "de", Channels: []notify.Channel{
		&notify.Webhook{URL: ok.URL + "/hook/SECRETPART?token=SECRET", Fmt: "discord"},
		&notify.Webhook{URL: bad.URL + "/x/SECRETPART"},
	}}
	return f, hits
}

func TestNotifyInfoRedacted(t *testing.T) {
	f, _ := notifyFix(t)
	rec := f.get(t, "/notify")
	m := decode(t, rec, 200)
	if m["lang"] != "de" {
		t.Fatalf("lang: %v", m)
	}
	ch := m["channels"].([]any)
	if len(ch) != 2 || ch[0].(map[string]any)["format"] != "discord" || ch[0].(map[string]any)["type"] != "webhook" {
		t.Fatalf("channels: %v", ch)
	}
	if strings.Contains(rec.Body.String(), "SECRET") || !strings.HasPrefix(ch[0].(map[string]any)["target"].(string), "http://127.0.0.1:") {
		t.Fatalf("not redacted: %s", rec.Body.String())
	}
}

func TestNotifyInfoEmpty(t *testing.T) {
	f := newFixture(t)
	rec := f.get(t, "/notify")
	if !strings.Contains(rec.Body.String(), `"channels":[]`) || !strings.Contains(rec.Body.String(), `"lang":"en"`) {
		t.Fatalf("%s", rec.Body.String())
	}
	f.srv.NotifyAdmin = &notify.Notifier{}
	if r := f.get(t, "/notify"); !strings.Contains(r.Body.String(), `"channels":[]`) {
		t.Fatalf("%s", r.Body.String())
	}
}

func TestNotifyTestResultsAndRateLimit(t *testing.T) {
	f, hits := notifyFix(t)
	now := base
	f.srv.Now = func() time.Time { return now }
	rec := f.do(t, "POST", "/notify/test", "", token)
	m := decode(t, rec, 200)
	res := m["results"].([]any)
	if len(res) != 2 || res[0].(map[string]any)["ok"] != true || res[1].(map[string]any)["ok"] != false {
		t.Fatalf("results: %v", res)
	}
	if e := res[1].(map[string]any)["error"].(string); !strings.Contains(e, "500") {
		t.Fatalf("error: %q", e)
	}
	if strings.Contains(rec.Body.String(), "SECRET") || hits.Load() != 1 {
		t.Fatalf("leak or hits %d: %s", hits.Load(), rec.Body.String())
	}
	now = base.Add(5 * time.Second)
	if r := f.do(t, "POST", "/notify/test", "", token); r.Code != 429 {
		t.Fatalf("want 429, got %d", r.Code)
	}
	now = base.Add(11 * time.Second)
	if r := f.do(t, "POST", "/notify/test", "", token); r.Code != 200 {
		t.Fatalf("want 200, got %d", r.Code)
	}
}

func TestNotifyAuth(t *testing.T) {
	f, _ := notifyFix(t)
	if r := f.do(t, "GET", "/notify", "", ""); r.Code != 401 {
		t.Fatalf("%d", r.Code)
	}
	if r := f.do(t, "POST", "/notify/test", "", "wrong"); r.Code != 401 {
		t.Fatalf("%d", r.Code)
	}
}
