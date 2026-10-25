package notify

import (
	"context"
	"net/http"
	"strings"
	"unicode"
)

// Ntfy posts to one ntfy topic URL.
type Ntfy struct {
	URL    string // full topic URL
	Token  string // optional bearer token
	Client *http.Client
}

func (n *Ntfy) Type() string   { return "ntfy" }
func (n *Ntfy) Target() string { return redactURL(n.URL) }
func (n *Ntfy) Format() string { return "" }

func (n *Ntfy) Send(ctx context.Context, m Message) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, strings.NewReader(m.Body))
	if err != nil {
		return errString("invalid URL")
	}
	// The title becomes an HTTP header and must be ASCII.
	req.Header.Set("Title", asciiFold(m.Title))
	if m.Severity == SevInfo {
		req.Header.Set("Tags", "white_check_mark")
	} else {
		req.Header.Set("Tags", "warning")
		req.Header.Set("Priority", "high")
	}
	if n.Token != "" {
		req.Header.Set("Authorization", "Bearer "+n.Token)
	}
	resp, err := client(n.Client).Do(req)
	if err != nil {
		// The URL may carry a secret topic; keep only the cause.
		return httpErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return errString("unexpected status " + resp.Status)
	}
	return nil
}

var umlauts = strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "Ä", "Ae", "Ö", "Oe", "Ü", "Ue", "ß", "ss",
	"–", "-", "—", "-", "’", "'", "‘", "'", "“", `"`, "”", `"`, "…", "...")

// asciiFold maps German umlauts and common punctuation to ASCII and drops anything else non-ASCII.
func asciiFold(s string) string {
	s = umlauts.Replace(s)
	return strings.Map(func(r rune) rune {
		if r > unicode.MaxASCII || r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
