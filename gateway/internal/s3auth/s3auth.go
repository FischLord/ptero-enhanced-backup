// Package s3auth verifies AWS Signature Version 4 requests, both header
// based (Authorization header) and presigned (query string).
package s3auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	algorithm      = "AWS4-HMAC-SHA256"
	timeFormat     = "20060102T150405Z"
	dateFormat     = "20060102"
	unsignedHash   = "UNSIGNED-PAYLOAD"
	maxPresignedTT = 7 * 24 * time.Hour
)

// Credentials is the single access key pair the gateway accepts.
type Credentials struct {
	AccessKey string
	SecretKey string
	Region    string
}

// Error carries an S3 error code (e.g. SignatureDoesNotMatch).
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errf(code, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...)}
}

// Result describes a successfully authenticated request.
type Result struct {
	Presigned bool
	// PayloadHash is the declared x-amz-content-sha256 value. It is either
	// UNSIGNED-PAYLOAD or a hex SHA-256 the caller may verify against the body.
	PayloadHash string
}

// Verifier checks request signatures.
type Verifier struct {
	Creds   Credentials
	MaxSkew time.Duration    // default 15 minutes
	Now     func() time.Time // default time.Now
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

func (v *Verifier) skew() time.Duration {
	if v.MaxSkew > 0 {
		return v.MaxSkew
	}
	return 15 * time.Minute
}

// Verify authenticates r. The returned error is always an *Error.
func (v *Verifier) Verify(r *http.Request) (*Result, error) {
	q := r.URL.Query()
	switch {
	case q.Has("X-Amz-Algorithm"):
		return v.verifyPresigned(r, q)
	case r.Header.Get("Authorization") != "":
		return v.verifyHeader(r)
	default:
		return nil, errf("AccessDenied", "Anonymous access is forbidden for this operation")
	}
}

type parsed struct {
	accessKey     string
	date, region  string
	service       string
	signedHeaders []string
	signature     string
	amzDate       time.Time
}

func (v *Verifier) verifyHeader(r *http.Request) (*Result, error) {
	auth := r.Header.Get("Authorization")
	rest, ok := strings.CutPrefix(auth, algorithm+" ")
	if !ok {
		return nil, errf("AuthorizationHeaderMalformed", "Unsupported authorization type")
	}
	fields := map[string]string{}
	for _, part := range strings.Split(rest, ",") {
		k, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			fields[k] = val
		}
	}
	p, perr := parseCommon(fields["Credential"], fields["SignedHeaders"], fields["Signature"], "AuthorizationHeaderMalformed")
	if perr != nil {
		return nil, perr
	}
	var err error
	ds := r.Header.Get("X-Amz-Date")
	if ds == "" {
		return nil, errf("AccessDenied", "AWS authentication requires a valid Date or x-amz-date header")
	}
	p.amzDate, err = time.Parse(timeFormat, ds)
	if err != nil {
		return nil, errf("AuthorizationHeaderMalformed", "Invalid x-amz-date")
	}
	if d := v.now().Sub(p.amzDate); d > v.skew() || d < -v.skew() {
		return nil, errf("RequestTimeTooSkewed", "The difference between the request time and the current time is too large.")
	}
	payload := r.Header.Get("X-Amz-Content-Sha256")
	if payload == "" {
		return nil, errf("InvalidRequest", "Missing required header for this request: x-amz-content-sha256")
	}
	if cerr := v.check(r, p, payload, canonicalQuery(r.URL.RawQuery, false), ds, false); cerr != nil {
		return nil, cerr
	}
	return &Result{PayloadHash: payload}, nil
}

func (v *Verifier) verifyPresigned(r *http.Request, q url.Values) (*Result, error) {
	if q.Get("X-Amz-Algorithm") != algorithm {
		return nil, errf("AuthorizationQueryParametersError", "X-Amz-Algorithm only supports %q", algorithm)
	}
	p, cerr := parseCommon(q.Get("X-Amz-Credential"), q.Get("X-Amz-SignedHeaders"), q.Get("X-Amz-Signature"), "AuthorizationQueryParametersError")
	if cerr != nil {
		return nil, cerr
	}
	ds := q.Get("X-Amz-Date")
	t, perr := time.Parse(timeFormat, ds)
	if perr != nil {
		return nil, errf("AuthorizationQueryParametersError", "X-Amz-Date must be in the ISO8601 Long Format \"yyyyMMdd'T'HHmmss'Z'\"")
	}
	p.amzDate = t
	secs, perr := strconv.Atoi(q.Get("X-Amz-Expires"))
	if perr != nil || secs < 1 || time.Duration(secs)*time.Second > maxPresignedTT {
		return nil, errf("AuthorizationQueryParametersError", "X-Amz-Expires must be a number between 1 and 604800")
	}
	now := v.now()
	if t.Sub(now) > v.skew() {
		return nil, errf("AccessDenied", "Request is not yet valid")
	}
	if now.After(t.Add(time.Duration(secs) * time.Second)) {
		return nil, errf("AccessDenied", "Request has expired")
	}
	payload := unsignedHash
	if h := q.Get("X-Amz-Content-Sha256"); h != "" {
		payload = h
	}
	if cerr := v.check(r, p, payload, canonicalQuery(r.URL.RawQuery, true), ds, true); cerr != nil {
		return nil, cerr
	}
	return &Result{Presigned: true, PayloadHash: payload}, nil
}

func parseCommon(cred, signedHeaders, sig, malformed string) (*parsed, *Error) {
	parts := strings.Split(cred, "/")
	if len(parts) != 5 || parts[4] != "aws4_request" || sig == "" || signedHeaders == "" {
		return nil, errf(malformed, "Malformed credential, signed headers or signature")
	}
	return &parsed{
		accessKey:     parts[0],
		date:          parts[1],
		region:        parts[2],
		service:       parts[3],
		signedHeaders: strings.Split(signedHeaders, ";"),
		signature:     sig,
	}, nil
}

func (v *Verifier) check(r *http.Request, p *parsed, payloadHash, query, amzDate string, presigned bool) *Error {
	if !hasString(p.signedHeaders, "host") {
		return errf("AccessDenied", "There were headers present in the request which were not signed: host")
	}
	if !presigned && !(hasString(p.signedHeaders, "x-amz-date") && hasString(p.signedHeaders, "x-amz-content-sha256")) {
		return errf("AccessDenied", "x-amz-date and x-amz-content-sha256 must be signed")
	}
	if subtle.ConstantTimeCompare([]byte(p.accessKey), []byte(v.Creds.AccessKey)) != 1 {
		return errf("InvalidAccessKeyId", "The AWS Access Key Id you provided does not exist in our records.")
	}
	if p.service != "s3" || p.region != v.Creds.Region {
		return errf("AuthorizationHeaderMalformed", "The authorization credential is wrong; expected region %q and service \"s3\"", v.Creds.Region)
	}
	if !strings.HasPrefix(amzDate, p.date) {
		return errf("AuthorizationHeaderMalformed", "Credential date does not match x-amz-date")
	}
	canonical := strings.Join([]string{
		r.Method,
		canonicalPath(r.URL),
		query,
		canonicalHeaders(r, p.signedHeaders),
		strings.Join(p.signedHeaders, ";"),
		payloadHash,
	}, "\n")
	scope := strings.Join([]string{p.date, p.region, p.service, "aws4_request"}, "/")
	sum := sha256.Sum256([]byte(canonical))
	toSign := strings.Join([]string{algorithm, amzDate, scope, hex.EncodeToString(sum[:])}, "\n")
	want := hex.EncodeToString(hmacSHA256(signingKey(v.Creds.SecretKey, p.date, p.region, p.service), toSign))
	if !hmac.Equal([]byte(want), []byte(strings.ToLower(p.signature))) {
		return errf("SignatureDoesNotMatch", "The request signature we calculated does not match the signature you provided. Check your key and signing method.")
	}
	return nil
}

func hasString(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func signingKey(secret, date, region, service string) []byte {
	k := hmacSHA256([]byte("AWS4"+secret), date)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	return hmacSHA256(k, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// canonicalPath uses the S3 rule: the decoded path is URI-encoded exactly once.
func canonicalPath(u *url.URL) string {
	p, err := url.PathUnescape(u.EscapedPath())
	if err != nil || p == "" {
		return "/"
	}
	return uriEncode(p, false)
}

func canonicalQuery(raw string, presigned bool) string {
	if raw == "" {
		return ""
	}
	type kv struct{ k, v string }
	var items []kv
	for _, pair := range strings.Split(raw, "&") {
		if pair == "" {
			continue
		}
		k, val, _ := strings.Cut(pair, "=")
		dk, err1 := url.QueryUnescape(k)
		dv, err2 := url.QueryUnescape(val)
		if err1 != nil || err2 != nil {
			dk, dv = k, val
		}
		if presigned && dk == "X-Amz-Signature" {
			continue
		}
		items = append(items, kv{uriEncode(dk, true), uriEncode(dv, true)})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].k != items[j].k {
			return items[i].k < items[j].k
		}
		return items[i].v < items[j].v
	})
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.k + "=" + it.v
	}
	return strings.Join(out, "&")
}

func canonicalHeaders(r *http.Request, names []string) string {
	var b strings.Builder
	for _, n := range names {
		var val string
		if n == "host" {
			val = r.Host
		} else if vals := r.Header.Values(http.CanonicalHeaderKey(n)); n == "content-length" && len(vals) == 0 && r.ContentLength > 0 {
			val = strconv.FormatInt(r.ContentLength, 10)
		} else {
			vals := r.Header.Values(http.CanonicalHeaderKey(n))
			for i := range vals {
				vals[i] = strings.Join(strings.Fields(vals[i]), " ")
			}
			val = strings.Join(vals, ",")
		}
		b.WriteString(n + ":" + val + "\n")
	}
	return b.String()
}

// uriEncode percent-encodes everything except unreserved characters (and
// "/" when encodeSlash is false), as required by SigV4.
func uriEncode(s string, encodeSlash bool) string {
	const hexUpper = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == '/' && !encodeSlash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexUpper[c>>4])
			b.WriteByte(hexUpper[c&15])
		}
	}
	return b.String()
}
