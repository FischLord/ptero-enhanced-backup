package s3auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var fuzzNow = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

func fuzzVerifier() *Verifier {
	return &Verifier{Creds: Credentials{"AKIATESTKEY", "secret", "us-east-1"}, Now: func() time.Time { return fuzzNow }}
}

// fuzzCheck asserts the Verify contract: either a result or an *Error, never both.
func fuzzCheck(t *testing.T, res *Result, err error) {
	t.Helper()
	if err == nil {
		if res == nil {
			t.Fatal("nil result without error")
		}
		return
	}
	if _, ok := err.(*Error); !ok {
		t.Fatalf("error is %T, want *Error", err)
	}
	if res != nil {
		t.Fatal("result together with error")
	}
}

// FuzzVerifyHeader feeds arbitrary Authorization, date and payload headers
// plus path and query into the header-auth verifier.
func FuzzVerifyHeader(f *testing.F) {
	valid := "AWS4-HMAC-SHA256 Credential=AKIATESTKEY/20261025/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=" + strings.Repeat("a", 64)
	f.Add(valid, "20261025T120000Z", "UNSIGNED-PAYLOAD", "/bkt/key", "uploadId=a%2Fb&partNumber=2")
	f.Add("", "", "", "/", "")
	f.Add("AWS4-HMAC-SHA256 ", "20261025T120000Z", "x", "/", "")
	f.Add("AWS4-HMAC-SHA256 Credential=////, SignedHeaders=;;, Signature=", "x", "x", "/%zz", "a=%zz&&=&=")
	f.Add("AWS4-HMAC-SHA256 Credential=a/b/c/d/aws4_request,SignedHeaders=host,Signature=zz", "99999999T999999Z", "h", "//..//../x", "%")
	f.Add(strings.Repeat("AWS4-HMAC-SHA256 ,", 5000), "20261025T120000Z", "h", "/"+strings.Repeat("a/", 3000), strings.Repeat("a=b&", 3000))
	f.Fuzz(func(t *testing.T, auth, date, payload, path, query string) {
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		r, err := http.NewRequest("GET", "http://gw.example.test:9000/", nil)
		if err != nil {
			t.Skip()
		}
		r.URL.Path = path
		r.URL.RawQuery = query
		r.Header["Authorization"] = []string{auth}
		r.Header["X-Amz-Date"] = []string{date}
		r.Header["X-Amz-Content-Sha256"] = []string{payload}
		res, verr := fuzzVerifier().Verify(r)
		fuzzCheck(t, res, verr)
		if verr == nil {
			t.Fatal("random input must not authenticate")
		}
	})
}

// FuzzVerifyPresigned fuzzes the presign parser via X-Amz-* query parameters.
func FuzzVerifyPresigned(f *testing.F) {
	cred := "AKIATESTKEY%2F20261025%2Fus-east-1%2Fs3%2Faws4_request"
	f.Add("X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=" + cred + "&X-Amz-Date=20261025T120000Z&X-Amz-Expires=900&X-Amz-SignedHeaders=host&X-Amz-Signature=" + strings.Repeat("0", 64))
	f.Add("")
	f.Add("X-Amz-Algorithm=")
	f.Add("X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Expires=99999999999999999999")
	f.Add("X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=a%2Fb%2Fc%2Fd%2Faws4_request&X-Amz-Date=20261025T120000Z&X-Amz-Expires=-1&X-Amz-SignedHeaders=host&X-Amz-Signature=x")
	f.Add("X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Date=00010101T000000Z&X-Amz-Expires=604800&X-Amz-Credential=%zz&X-Amz-Signature=%")
	f.Add(strings.Repeat("X-Amz-Algorithm=AWS4-HMAC-SHA256&", 2000))
	f.Fuzz(func(t *testing.T, query string) {
		r := httptest.NewRequest("GET", "http://gw.example.test:9000/bkt/key", nil)
		r.URL.RawQuery = "X-Amz-Algorithm=AWS4-HMAC-SHA256&" + query
		res, err := fuzzVerifier().Verify(r)
		fuzzCheck(t, res, err)
		r.URL.RawQuery = query
		res, err = fuzzVerifier().Verify(r)
		fuzzCheck(t, res, err)
	})
}

// FuzzCanonical checks the canonicalisation helpers never panic and that
// uriEncode output contains no unsafe characters.
func FuzzCanonical(f *testing.F) {
	f.Add("/a b/c%2F", "a=b&c=%20&&=", "a b")
	f.Add("", "%", "\x00\xff")
	f.Fuzz(func(t *testing.T, path, query, s string) {
		_ = canonicalQuery(query, true)
		_ = canonicalQuery(query, false)
		enc := uriEncode(s, true)
		for i := 0; i < len(enc); i++ {
			if c := enc[i]; c == '/' || c == ' ' {
				t.Fatalf("unsafe char in %q", enc)
			}
		}
		r := httptest.NewRequest("GET", "http://h/", nil)
		r.URL.Path = path
		r.URL.RawPath = ""
		_ = canonicalPath(r.URL)
	})
}
