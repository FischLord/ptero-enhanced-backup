package notify

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSendPostsMessage(t *testing.T) {
	type got struct{ method, path, auth, title, body string }
	ch := make(chan got, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ch <- got{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Title"), string(b)}
	}))
	defer srv.Close()

	n := &Notifier{URL: srv.URL + "/peb", Token: "tk_123"}
	n.Send("PEB Backup fehlgeschlagen", "Backup x fehlgeschlagen")
	n.Wait()
	g := <-ch
	if g.method != "POST" || g.path != "/peb" || g.auth != "Bearer tk_123" || g.title != "PEB Backup fehlgeschlagen" ||
		g.body != "Backup x fehlgeschlagen" {
		t.Fatalf("request: %+v", g)
	}
}

func TestSendWithoutTokenAndNil(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { auth = r.Header.Get("Authorization") }))
	defer srv.Close()
	(&Notifier{URL: srv.URL}).post("t", "b")
	if auth != "" {
		t.Fatalf("auth header %q", auth)
	}
	var nilN *Notifier
	nilN.Send("t", "b") // must not panic
	nilN.Wait()
	(&Notifier{}).Send("t", "b")
}

func TestSendFailuresAreSwallowed(t *testing.T) {
	// Server error and unreachable server: Send returns immediately, post reports without the URL.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	n := &Notifier{URL: bad.URL + "/secret-topic"}
	if err := n.post("t", "b"); err == nil {
		t.Fatal("expected error for 500")
	}
	bad.Close()
	err := n.post("t", "b")
	if err == nil || len(err.Error()) > 40 {
		t.Fatalf("error should be short and URL-free: %v", err)
	}
	n.Send("t", "b")
	n.Wait()
}

func TestSendTimeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)
	n := &Notifier{URL: srv.URL, Timeout: 100 * time.Millisecond}
	start := time.Now()
	if err := n.post("t", "b"); err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("timeout not applied: %v", err)
	}
}
