package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Webhook formats.
const (
	FormatJSON    = "json"
	FormatDiscord = "discord"
	FormatSlack   = "slack"
	FormatGotify  = "gotify"
)

// Webhook posts a JSON payload to one URL. The URL usually carries a secret.
type Webhook struct {
	URL    string
	Fmt    string // json (default), discord, slack, gotify
	Client *http.Client
}

func (w *Webhook) Type() string   { return "webhook" }
func (w *Webhook) Target() string { return redactURL(w.URL) }
func (w *Webhook) Format() string {
	if w.Fmt == "" {
		return FormatJSON
	}
	return w.Fmt
}

func trunc(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-3]) + "..."
	}
	return s
}

// Payload renders the request body for a format.
func Payload(format string, m Message) ([]byte, error) {
	ts := m.Time.UTC().Format(time.RFC3339)
	var v any
	switch format {
	case "", FormatJSON:
		v = map[string]string{"event": m.Event, "title": m.Title, "message": m.Body, "severity": m.Severity, "time": ts}
	case FormatDiscord:
		color := map[string]int{SevError: 0xE74C3C, SevWarning: 0xF1C40F}[m.Severity]
		if color == 0 {
			color = 0x2ECC71
		}
		v = map[string]any{"embeds": []map[string]any{{
			"title": trunc(m.Title, 256), "description": trunc(m.Body, 4000), "color": color, "timestamp": ts,
		}}}
	case FormatSlack:
		v = map[string]string{"text": "*" + m.Title + "*\n" + m.Body}
	case FormatGotify:
		prio := map[string]int{SevError: 8, SevWarning: 5}[m.Severity]
		if prio == 0 {
			prio = 3
		}
		v = map[string]any{"title": m.Title, "message": m.Body, "priority": prio}
	default:
		return nil, errString("unknown webhook format")
	}
	return json.Marshal(v)
}

func (w *Webhook) Send(ctx context.Context, m Message) error {
	body, err := Payload(w.Fmt, m)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return errString("invalid URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "peb-gateway")
	resp, err := client(w.Client).Do(req)
	if err != nil {
		return httpErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return errString("unexpected status " + resp.Status)
	}
	return nil
}
