package notify

import (
	"fmt"
	"log/slog"
	"net/mail"
	"net/url"
	"strings"
)

// Config is the raw notification setup from the environment.
type Config struct {
	Lang          string // PEB_NOTIFY_LANG: en (default) | de
	NtfyURL       string // PEB_NTFY_URL
	NtfyToken     string // PEB_NTFY_TOKEN
	WebhookURLs   []string
	WebhookFormat string // json (default) | discord | slack | gotify

	SMTPHost     string
	SMTPPort     int // default 587
	SMTPUser     string
	SMTPPassword string
	SMTPFrom     string
	SMTPTo       []string
	SMTPTLS      string // starttls (default) | tls | none
}

// SplitList splits a comma-separated list and drops empty entries.
func SplitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Normalize fills defaults and validates. Errors never contain URLs or secrets.
func (c *Config) Normalize() error {
	c.Lang = strings.ToLower(strings.TrimSpace(c.Lang))
	switch c.Lang {
	case "":
		c.Lang = LangEN
	case LangEN, LangDE:
	default:
		return fmt.Errorf("PEB_NOTIFY_LANG: must be en or de")
	}
	c.WebhookFormat = strings.ToLower(strings.TrimSpace(c.WebhookFormat))
	switch c.WebhookFormat {
	case "":
		c.WebhookFormat = FormatJSON
	case FormatJSON, FormatDiscord, FormatSlack, FormatGotify:
	default:
		return fmt.Errorf("PEB_WEBHOOK_FORMAT: must be json, discord, slack or gotify")
	}
	for i, w := range c.WebhookURLs {
		u, err := url.Parse(w)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("PEB_WEBHOOK_URLS: entry %d must be an http(s) URL", i+1)
		}
	}
	c.SMTPTLS = strings.ToLower(strings.TrimSpace(c.SMTPTLS))
	switch c.SMTPTLS {
	case "":
		c.SMTPTLS = TLSStart
	case TLSStart, TLSImpl, TLSNone:
	default:
		return fmt.Errorf("PEB_SMTP_TLS: must be starttls, tls or none")
	}
	if c.SMTPPort == 0 {
		c.SMTPPort = 587
	}
	if c.SMTPPort < 1 || c.SMTPPort > 65535 {
		return fmt.Errorf("PEB_SMTP_PORT: must be 1-65535")
	}
	if c.SMTPHost != "" || len(c.SMTPTo) > 0 {
		if c.SMTPHost == "" || c.SMTPFrom == "" || len(c.SMTPTo) == 0 {
			return fmt.Errorf("PEB_SMTP_HOST, PEB_SMTP_FROM and PEB_SMTP_TO must be set together")
		}
		if _, err := mail.ParseAddress(c.SMTPFrom); err != nil {
			return fmt.Errorf("PEB_SMTP_FROM: not a valid address")
		}
		for i, t := range c.SMTPTo {
			if _, err := mail.ParseAddress(t); err != nil {
				return fmt.Errorf("PEB_SMTP_TO: entry %d is not a valid address", i+1)
			}
		}
	}
	return nil
}

// New builds the notifier. The config must be normalized.
func New(c Config, log *slog.Logger) *Notifier {
	n := &Notifier{Lang: c.Lang, Log: log}
	if c.NtfyURL != "" {
		n.Channels = append(n.Channels, &Ntfy{URL: c.NtfyURL, Token: c.NtfyToken})
	}
	for _, u := range c.WebhookURLs {
		n.Channels = append(n.Channels, &Webhook{URL: u, Fmt: c.WebhookFormat})
	}
	if c.SMTPHost != "" {
		n.Channels = append(n.Channels, &SMTP{
			Host: c.SMTPHost, Port: c.SMTPPort, User: c.SMTPUser, Password: c.SMTPPassword,
			From: c.SMTPFrom, To: c.SMTPTo, TLS: c.SMTPTLS,
		})
	}
	return n
}
