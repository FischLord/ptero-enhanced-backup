package notify

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendScrubsCredentials(t *testing.T) {
	ch := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ch <- r.Header.Get("Title") + "|" + string(b)
	}))
	defer srv.Close()
	n := &Notifier{URL: srv.URL}
	n.Send("PEB fail", "copy to sftp://bob:pw1@host/x failed, RESTIC_PASSWORD=pw2")
	n.Wait()
	got := <-ch
	if strings.Contains(got, "pw1") || strings.Contains(got, "pw2") || !strings.Contains(got, "sftp://***@host/x") {
		t.Fatalf("%q", got)
	}
}
