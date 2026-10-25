package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// SMTP TLS modes.
const (
	TLSStart = "starttls"
	TLSImpl  = "tls"
	TLSNone  = "none"
)

// SMTP sends a plain-text mail to a fixed list of recipients.
type SMTP struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
	To       []string
	TLS      string // starttls (default), tls, none

	tlsConfig *tls.Config // tests only; nil = default with ServerName=Host
}

func (s *SMTP) Type() string   { return "smtp" }
func (s *SMTP) Format() string { return "" }
func (s *SMTP) Target() string {
	n := fmt.Sprintf("%d recipients", len(s.To))
	if len(s.To) == 1 {
		n = "1 recipient"
	}
	return net.JoinHostPort(s.Host, strconv.Itoa(s.port())) + " → " + n
}

func (s *SMTP) port() int {
	if s.Port > 0 {
		return s.Port
	}
	return 587
}

// plainAuth is AUTH PLAIN without net/smtp's refusal of unencrypted links; the
// operator chose TLS=none explicitly.
type plainAuth struct{ user, pass string }

func (a plainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pass), nil
}
func (a plainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, fmt.Errorf("unexpected challenge")
	}
	return nil, nil
}

func (s *SMTP) Send(ctx context.Context, m Message) error {
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return errString("invalid sender address")
	}
	rcpts := make([]string, 0, len(s.To))
	hdrTo := make([]string, 0, len(s.To))
	for _, t := range s.To {
		a, err := mail.ParseAddress(t)
		if err != nil {
			return errString("invalid recipient address")
		}
		rcpts = append(rcpts, a.Address)
		hdrTo = append(hdrTo, a.String())
	}
	msg, err := buildMail(from, hdrTo, m)
	if err != nil {
		return err
	}

	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.port()))
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return smtpErr(ctx, "connect failed")
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := closeOnDone(ctx, conn)
	defer stop()

	cfg := s.tlsConfig
	if cfg == nil {
		cfg = &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}
	}
	if s.TLS == TLSImpl {
		tc := tls.Client(conn, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			return smtpErr(ctx, "TLS handshake failed")
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		conn.Close()
		return smtpErr(ctx, "SMTP greeting failed")
	}
	defer c.Close()
	if err := c.Hello("localhost"); err != nil {
		return smtpErr(ctx, "EHLO failed")
	}
	if s.TLS == "" || s.TLS == TLSStart {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errString("server does not offer STARTTLS")
		}
		if err := c.StartTLS(cfg); err != nil {
			return smtpErr(ctx, "STARTTLS failed")
		}
	}
	if s.User != "" {
		if ok, _ := c.Extension("AUTH"); !ok {
			return errString("server does not offer AUTH")
		}
		if err := c.Auth(plainAuth{s.User, s.Password}); err != nil {
			return errString("authentication failed")
		}
	}
	if err := c.Mail(from.Address); err != nil {
		return errString("sender rejected")
	}
	for _, r := range rcpts {
		if err := c.Rcpt(r); err != nil {
			return errString("recipient rejected")
		}
	}
	w, err := c.Data()
	if err != nil {
		return smtpErr(ctx, "DATA failed")
	}
	if _, err := w.Write(msg); err != nil {
		return smtpErr(ctx, "sending failed")
	}
	if err := w.Close(); err != nil {
		return errString("message rejected")
	}
	_ = c.Quit()
	return nil
}

// closeOnDone closes conn when ctx ends so blocked reads return.
func closeOnDone(ctx context.Context, conn net.Conn) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-done:
		}
	}()
	return func() { close(done) }
}

func smtpErr(ctx context.Context, what string) error {
	if ctx.Err() != nil {
		return errString("timeout")
	}
	return errString(what)
}

func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return ' '
		}
		return r
	}, s)
}

func buildMail(from *mail.Address, to []string, m Message) ([]byte, error) {
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, errString("internal error")
	}
	domain := "localhost"
	if i := strings.LastIndex(from.Address, "@"); i >= 0 {
		domain = from.Address[i+1:]
	}
	var b bytes.Buffer
	h := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	h("From", from.String())
	h("To", strings.Join(to, ", "))
	h("Subject", mime.QEncoding.Encode("utf-8", clean(m.Title)))
	h("Date", m.Time.Format(time.RFC1123Z))
	h("Message-ID", "<"+hex.EncodeToString(id)+"@"+domain+">")
	h("MIME-Version", "1.0")
	h("Content-Type", `text/plain; charset="utf-8"`)
	h("Content-Transfer-Encoding", "quoted-printable")
	h("X-PEB-Event", clean(m.Event))
	b.WriteString("\r\n")
	w := quotedprintable.NewWriter(&b)
	_, _ = w.Write([]byte(m.Body + "\n"))
	_ = w.Close()
	return b.Bytes(), nil
}
