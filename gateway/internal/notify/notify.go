// Package notify sends short messages to an ntfy topic.
package notify

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"ptero-enhanced-backup/gateway/internal/secrets"
)

// Notifier posts to one ntfy topic URL. A nil Notifier does nothing.
type Notifier struct {
	URL   string // full topic URL
	Token string // optional bearer token
	Log   *slog.Logger

	Client  *http.Client
	Timeout time.Duration
	wg      sync.WaitGroup
}

// Send posts the message in the background; failures are only logged.
// title must be ASCII (it becomes an HTTP header), the body may be any text.
func (n *Notifier) Send(title, body string) {
	if n == nil || n.URL == "" {
		return
	}
	title, body = secrets.Scrub(title), secrets.Scrub(body)
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		if err := n.post(title, body); err != nil && n.Log != nil {
			n.Log.Warn("ntfy failed", "err", err)
		}
	}()
}

// Wait blocks until all pending messages are sent (for tests and shutdown).
func (n *Notifier) Wait() {
	if n != nil {
		n.wg.Wait()
	}
}

func (n *Notifier) post(title, body string) error {
	d := n.Timeout
	if d <= 0 {
		d = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Title", title)
	req.Header.Set("Tags", "warning")
	req.Header.Set("Priority", "high")
	if n.Token != "" {
		req.Header.Set("Authorization", "Bearer "+n.Token)
	}
	c := n.Client
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		// The URL may carry a secret topic; keep only the cause.
		return errString("request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return errString("unexpected status " + resp.Status)
	}
	return nil
}

type errString string

func (e errString) Error() string { return string(e) }
