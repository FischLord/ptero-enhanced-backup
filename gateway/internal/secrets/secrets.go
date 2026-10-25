// Package secrets removes credentials from text that is logged, stored or sent
// (error messages of restic and rclone, job messages, notifications).
package secrets

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

const mask = "***"

var (
	// scheme://user[:password]@host  ->  scheme://***@host
	urlUserInfo = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.\-]*://)[^\s/]*@`)
	// AWS_SECRET_ACCESS_KEY=..., RESTIC_PASSWORD=..., any NAME_SECRET/PASSWORD/TOKEN/..._KEY style variable.
	envSecret = regexp.MustCompile(`\b((?:AWS_SECRET_ACCESS_KEY|AWS_SESSION_TOKEN|RESTIC_PASSWORD|[A-Z][A-Z0-9_]*(?:SECRET|PASS|TOKEN|ACCOUNT_KEY|APPLICATION_KEY|SAS)[A-Z0-9_]*))\s*=\s*(?:"[^"]*"|'[^']*'|[^\s"']+)`)
	// password=..., token=..., X-Amz-Signature=... in query strings and key=value text.
	paramSecret = regexp.MustCompile(`(?i)((?:^|[?&;,_\-\s"'(]))((?:password|passwd|pass|pwd|secret|secret_key|token|access_token|api_key|apikey|signature|x-amz-signature|x-amz-credential|x-amz-security-token))=[^&\s"')]*`)
	bearer      = regexp.MustCompile(`(?i)(\bbearer\s+)[A-Za-z0-9._~+/=\-]+`)
)

// Scrub replaces credentials in s with ***: user and password of URLs, secret
// environment variables (AWS_SECRET_ACCESS_KEY=..., RESTIC_PASSWORD=...),
// secret query parameters (password=..., token=...) and bearer tokens.
func Scrub(s string) string {
	if s == "" {
		return s
	}
	s = urlUserInfo.ReplaceAllString(s, "${1}"+mask+"@")
	s = envSecret.ReplaceAllString(s, "${1}="+mask)
	s = paramSecret.ReplaceAllString(s, "${1}${2}="+mask)
	s = bearer.ReplaceAllString(s, "${1}"+mask)
	return s
}

// ScrubWith first replaces the known literal secrets (passwords, repository
// addresses) with [redacted], then applies Scrub.
func ScrubWith(s string, known ...string) string {
	for _, k := range known {
		if k != "" {
			s = strings.ReplaceAll(s, k, "[redacted]")
		}
	}
	return Scrub(s)
}

// Handler wraps a slog handler and scrubs the message and every string or
// error attribute before it is written.
type Handler struct {
	Next  slog.Handler
	known []string
}

// NewHandler returns h with scrubbing. Known literal secrets (repository
// passwords and addresses) are removed wherever they appear (see ScrubWith).
func NewHandler(h slog.Handler, known ...string) *Handler { return &Handler{Next: h, known: known} }

func (h *Handler) scrub(s string) string { return ScrubWith(s, h.known...) }

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool { return h.Next.Enabled(ctx, l) }

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	n := slog.NewRecord(r.Time, r.Level, h.scrub(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		n.AddAttrs(h.scrubAttr(a))
		return true
	})
	return h.Next.Handle(ctx, n)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = h.scrubAttr(a)
	}
	return &Handler{Next: h.Next.WithAttrs(out), known: h.known}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{Next: h.Next.WithGroup(name), known: h.known}
}

func (h *Handler) scrubAttr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, h.scrub(v.String()))
	case slog.KindGroup:
		g := v.Group()
		out := make([]slog.Attr, len(g))
		for i, x := range g {
			out[i] = h.scrubAttr(x)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	case slog.KindAny:
		x := v.Any()
		if x == nil {
			return a
		}
		return slog.String(a.Key, h.scrub(safeString(x)))
	}
	return a
}

// safeString renders any value as text; a panicking String or Error method
// (typed nil pointer) yields a placeholder instead of crashing the logger.
func safeString(x any) (out string) {
	defer func() {
		if recover() != nil {
			out = "<unprintable>"
		}
	}()
	switch t := x.(type) {
	case error:
		return t.Error()
	case fmt.Stringer:
		return t.String()
	}
	return fmt.Sprint(x)
}
