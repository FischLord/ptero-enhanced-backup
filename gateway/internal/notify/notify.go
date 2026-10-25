// Package notify sends short event messages to several channels (ntfy, webhooks, SMTP).
// Channels are independent: one failing channel never affects another, and sending
// is asynchronous so it never blocks a backup.
package notify

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"ptero-enhanced-backup/gateway/internal/secrets"
)

// Severity levels.
const (
	SevInfo    = "info"
	SevWarning = "warning"
	SevError   = "error"
)

// ChannelTimeout is the default deadline for one delivery.
const ChannelTimeout = 10 * time.Second

// Message is a rendered notification, already scrubbed of credentials.
type Message struct {
	Event    string
	Severity string
	Title    string
	Body     string
	Time     time.Time
}

// Channel delivers a message to one target. Errors must be short and must not
// contain URLs, tokens or passwords.
type Channel interface {
	Type() string   // "ntfy", "webhook" or "smtp"
	Target() string // redacted description, safe to show
	Format() string // webhook payload format, empty for other types
	Send(ctx context.Context, m Message) error
}

// Notifier fans events out to its channels. A nil Notifier does nothing.
type Notifier struct {
	Lang     string // "en" (default) or "de"
	Channels []Channel
	Log      *slog.Logger
	Timeout  time.Duration // per channel, default ChannelTimeout
	Now      func() time.Time

	wg sync.WaitGroup
}

// Info describes one channel for the admin API.
type Info struct {
	Type   string `json:"type"`
	Target string `json:"target"`
	Format string `json:"format,omitempty"`
}

// Result is the outcome of one test delivery.
type Result struct {
	Type   string `json:"type"`
	Target string `json:"target"`
	OK     bool   `json:"ok"`
	Error  string `json:"error,omitempty"`
}

func (n *Notifier) timeout() time.Duration {
	if n.Timeout > 0 {
		return n.Timeout
	}
	return ChannelTimeout
}

func (n *Notifier) now() time.Time {
	if n.Now != nil {
		return n.Now().UTC()
	}
	return time.Now().UTC()
}

// Language returns the effective message language.
func (n *Notifier) Language() string {
	if n == nil {
		return LangEN
	}
	return normLang(n.Lang)
}

// Describe returns redacted descriptions of all channels (never nil).
func (n *Notifier) Describe() []Info {
	out := []Info{}
	if n == nil {
		return out
	}
	for _, c := range n.Channels {
		out = append(out, Info{Type: c.Type(), Target: c.Target(), Format: c.Format()})
	}
	return out
}

func (n *Notifier) message(ev Event) Message {
	title, body := Render(n.Lang, ev)
	return Message{
		Event: ev.Key, Severity: SeverityOf(ev.Key),
		Title: secrets.Scrub(title), Body: secrets.Scrub(body), Time: n.now(),
	}
}

// Send delivers the event to every channel in the background; failures are only logged.
func (n *Notifier) Send(ev Event) {
	if n == nil || len(n.Channels) == 0 {
		return
	}
	m := n.message(ev)
	for _, c := range n.Channels {
		n.wg.Add(1)
		go func() {
			defer n.wg.Done()
			if err := n.deliver(context.Background(), c, m); err != nil && n.Log != nil {
				n.Log.Warn("notification failed", "channel", c.Type(), "target", c.Target(), "err", secrets.Scrub(err.Error()))
			}
		}()
	}
}

// Wait blocks until all pending messages are sent (for tests and shutdown).
func (n *Notifier) Wait() {
	if n != nil {
		n.wg.Wait()
	}
}

// Test sends a test message to every channel at once and waits for the outcomes.
func (n *Notifier) Test(ctx context.Context) []Result {
	if n == nil {
		return []Result{}
	}
	m := n.message(Event{Key: EventTest})
	res := make([]Result, len(n.Channels))
	var wg sync.WaitGroup
	for i, c := range n.Channels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := Result{Type: c.Type(), Target: c.Target(), OK: true}
			if err := n.deliver(ctx, c, m); err != nil {
				r.OK, r.Error = false, secrets.Scrub(err.Error())
			}
			res[i] = r
		}()
	}
	wg.Wait()
	return res
}

// deliver runs one channel with its own deadline; a panic becomes an error.
func (n *Notifier) deliver(ctx context.Context, c Channel, m Message) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errString("internal error")
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, n.timeout())
	defer cancel()
	return c.Send(ctx, m)
}

type errString string

func (e errString) Error() string { return string(e) }

// httpErr turns a transport error into a short one without the URL.
func httpErr(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return errString("timeout")
	}
	return errString("request failed")
}

func client(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return http.DefaultClient
}

// redactURL keeps scheme and host only (paths, queries and userinfo may be secrets).
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Scheme == "" {
		return "invalid URL"
	}
	return u.Scheme + "://" + u.Host
}
