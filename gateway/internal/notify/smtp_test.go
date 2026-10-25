package notify

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"math/big"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeSMTP struct {
	ln       net.Listener
	tlsCfg   *tls.Config
	implicit bool // TLS from the first byte
	offerTLS bool
	authUser string
	authPass string
	rejectTo string

	mu       sync.Mutex
	from     string
	rcpts    []string
	data     string
	authed   bool
	sawPlain bool // AUTH sent before TLS
	hang     bool
}

func selfSigned(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}, pool
}

func startSMTP(t *testing.T, f *fakeSMTP) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.ln = ln
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	if f.hang {
		time.Sleep(5 * time.Second)
		return
	}
	if f.implicit {
		c = tls.Server(c, f.tlsCfg)
	}
	r := bufio.NewReader(c)
	w := func(s string) { c.Write([]byte(s + "\r\n")) }
	w("220 fake ESMTP")
	secure := f.implicit
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		up := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(up, "EHLO"):
			c.Write([]byte("250-fake\r\n"))
			if f.offerTLS && !secure {
				c.Write([]byte("250-STARTTLS\r\n"))
			}
			if f.authUser != "" {
				c.Write([]byte("250-AUTH PLAIN\r\n"))
			}
			w("250 8BITMIME")
		case up == "STARTTLS":
			w("220 go ahead")
			c = tls.Server(c, f.tlsCfg)
			r = bufio.NewReader(c)
			w = func(s string) { c.Write([]byte(s + "\r\n")) }
			secure = true
		case strings.HasPrefix(up, "AUTH PLAIN"):
			if !secure {
				f.mu.Lock()
				f.sawPlain = true
				f.mu.Unlock()
			}
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(line[len("AUTH PLAIN"):]))
			if string(raw) == "\x00"+f.authUser+"\x00"+f.authPass {
				f.mu.Lock()
				f.authed = true
				f.mu.Unlock()
				w("235 ok")
			} else {
				w("535 bad credentials")
			}
		case strings.HasPrefix(up, "MAIL FROM:"):
			f.mu.Lock()
			f.from = strings.Trim(strings.Fields(line[len("MAIL FROM:"):])[0], "<> ")
			f.mu.Unlock()
			w("250 ok")
		case strings.HasPrefix(up, "RCPT TO:"):
			a := strings.Trim(line[len("RCPT TO:"):], "<> ")
			if f.rejectTo != "" && a == f.rejectTo {
				w("550 no such user")
				continue
			}
			f.mu.Lock()
			f.rcpts = append(f.rcpts, a)
			f.mu.Unlock()
			w("250 ok")
		case up == "DATA":
			w("354 go")
			var sb strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				sb.WriteString(l)
			}
			f.mu.Lock()
			f.data = sb.String()
			f.mu.Unlock()
			w("250 queued")
		case up == "QUIT":
			w("221 bye")
			return
		default:
			w("250 ok")
		}
	}
}

func (f *fakeSMTP) port() int {
	_, p, _ := net.SplitHostPort(f.ln.Addr().String())
	n, _ := strconv.Atoi(p)
	return n
}

var smtpMsg = Message{Event: "backup_failed", Severity: SevError, Title: "PEB Backup fehlgeschlagen – Größe",
	Body: "Zeitüberschreitung bei Backup b1\nZeile 2", Time: time.Date(2026, 10, 25, 1, 2, 3, 0, time.UTC)}

func newSMTP(f *fakeSMTP, mode string, pool *x509.CertPool) *SMTP {
	s := &SMTP{Host: "127.0.0.1", Port: f.port(), From: "PEB <peb@example.org>", To: []string{"a@example.net", "Bob <b@example.net>"}, TLS: mode}
	if pool != nil {
		s.tlsConfig = &tls.Config{RootCAs: pool, ServerName: "localhost"}
	}
	return s
}

func checkMail(t *testing.T, f *fakeSMTP) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.from != "peb@example.org" || len(f.rcpts) != 2 || f.rcpts[0] != "a@example.net" || f.rcpts[1] != "b@example.net" {
		t.Fatalf("envelope: %q %v", f.from, f.rcpts)
	}
	msg, err := mail.ReadMessage(strings.NewReader(f.data))
	if err != nil {
		t.Fatal(err)
	}
	h := msg.Header
	dec := new(mime.WordDecoder)
	subj, err := dec.DecodeHeader(h.Get("Subject"))
	if err != nil || subj != smtpMsg.Title {
		t.Fatalf("subject %q %v", subj, err)
	}
	if _, err := h.Date(); err != nil {
		t.Fatalf("date: %v", err)
	}
	if id := h.Get("Message-ID"); !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, "@example.org>") {
		t.Fatalf("message-id %q", id)
	}
	if !strings.Contains(h.Get("Content-Type"), "utf-8") || h.Get("MIME-Version") != "1.0" ||
		!strings.Contains(h.Get("To"), "a@example.net") || !strings.Contains(h.Get("From"), "peb@example.org") {
		t.Fatalf("headers: %v", h)
	}
	body := new(strings.Builder)
	buf := make([]byte, 4096)
	qr := quotedprintable.NewReader(msg.Body)
	for {
		n, err := qr.Read(buf)
		body.Write(buf[:n])
		if err != nil {
			break
		}
	}
	if !strings.Contains(body.String(), "Zeitüberschreitung bei Backup b1") || !strings.Contains(body.String(), "Zeile 2") {
		t.Fatalf("body %q", body.String())
	}
}

func TestSMTPStartTLSWithAuth(t *testing.T) {
	cfg, pool := selfSigned(t)
	f := &fakeSMTP{tlsCfg: cfg, offerTLS: true, authUser: "u", authPass: "p"}
	startSMTP(t, f)
	s := newSMTP(f, TLSStart, pool)
	s.User, s.Password = "u", "p"
	if err := s.Send(context.Background(), smtpMsg); err != nil {
		t.Fatal(err)
	}
	checkMail(t, f)
	if !f.authed || f.sawPlain {
		t.Fatalf("authed=%v plainBeforeTLS=%v", f.authed, f.sawPlain)
	}
}

func TestSMTPImplicitTLS(t *testing.T) {
	cfg, pool := selfSigned(t)
	f := &fakeSMTP{tlsCfg: cfg, implicit: true}
	startSMTP(t, f)
	if err := newSMTP(f, TLSImpl, pool).Send(context.Background(), smtpMsg); err != nil {
		t.Fatal(err)
	}
	checkMail(t, f)
}

func TestSMTPPlainNone(t *testing.T) {
	f := &fakeSMTP{authUser: "u", authPass: "p"}
	startSMTP(t, f)
	s := newSMTP(f, TLSNone, nil)
	s.User, s.Password = "u", "p"
	if err := s.Send(context.Background(), smtpMsg); err != nil {
		t.Fatal(err)
	}
	checkMail(t, f)
}

func TestSMTPFailures(t *testing.T) {
	cfg, pool := selfSigned(t)
	// STARTTLS required but not offered: never fall back to plaintext.
	f := &fakeSMTP{tlsCfg: cfg}
	startSMTP(t, f)
	if err := newSMTP(f, TLSStart, pool).Send(context.Background(), smtpMsg); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Fatalf("%v", err)
	}
	if f.data != "" {
		t.Fatal("mail sent without TLS")
	}
	// Certificate not trusted.
	f2 := &fakeSMTP{tlsCfg: cfg, offerTLS: true}
	startSMTP(t, f2)
	if err := newSMTP(f2, TLSStart, nil).Send(context.Background(), smtpMsg); err == nil {
		t.Fatal("untrusted certificate accepted")
	}
	// Wrong password; error must not echo it.
	f3 := &fakeSMTP{authUser: "u", authPass: "right"}
	startSMTP(t, f3)
	s := newSMTP(f3, TLSNone, nil)
	s.User, s.Password = "u", "wrongpw"
	if err := s.Send(context.Background(), smtpMsg); err == nil || strings.Contains(err.Error(), "wrongpw") || !strings.Contains(err.Error(), "authentication") {
		t.Fatalf("%v", err)
	}
	// Recipient rejected.
	f4 := &fakeSMTP{rejectTo: "a@example.net"}
	startSMTP(t, f4)
	if err := newSMTP(f4, TLSNone, nil).Send(context.Background(), smtpMsg); err == nil || !strings.Contains(err.Error(), "recipient") {
		t.Fatalf("%v", err)
	}
	// Connection refused.
	dead, _ := net.Listen("tcp", "127.0.0.1:0")
	port := dead.Addr().(*net.TCPAddr).Port
	dead.Close()
	if err := (&SMTP{Host: "127.0.0.1", Port: port, From: "a@x.de", To: []string{"b@x.de"}, TLS: TLSNone}).Send(context.Background(), smtpMsg); err == nil || strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("%v", err)
	}
	// Bad addresses.
	if err := (&SMTP{Host: "h", From: "nope", To: []string{"b@x.de"}}).Send(context.Background(), smtpMsg); err == nil {
		t.Fatal("bad sender accepted")
	}
}

func TestSMTPTimeout(t *testing.T) {
	f := &fakeSMTP{hang: true}
	startSMTP(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := newSMTP(f, TLSNone, nil).Send(ctx, smtpMsg)
	if err == nil || time.Since(start) > 3*time.Second || err.Error() != "timeout" {
		t.Fatalf("%v after %v", err, time.Since(start))
	}
}

func TestSMTPHeaderInjection(t *testing.T) {
	b, err := buildMail(&mail.Address{Address: "a@x.de"}, []string{"b@x.de"}, Message{Title: "x\r\nBcc: evil@x.de", Event: "e\nX: y", Time: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "\r\nBcc:") || strings.Contains(string(b), "\nX: y") {
		t.Fatalf("injection: %s", b)
	}
}

func TestSMTPViaNotifierAlongsideFailingChannel(t *testing.T) {
	f := &fakeSMTP{}
	startSMTP(t, f)
	n := &Notifier{Lang: "en", Channels: []Channel{
		&Webhook{URL: "http://127.0.0.1:1/dead"},
		newSMTP(f, TLSNone, nil),
	}}
	n.Send(Event{Key: EventTest})
	n.Wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.Contains(f.data, "PEB test message") {
		t.Fatalf("smtp not delivered: %q", f.data)
	}
}
