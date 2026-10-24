package s3auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const (
	ak     = "AKIATESTKEY"
	sk     = "super-secret-key"
	region = "us-east-1"
	host   = "gw.example.test:9000"
)

func newVerifier(now time.Time) *Verifier {
	return &Verifier{Creds: Credentials{ak, sk, region}, Now: func() time.Time { return now }}
}

func code(t *testing.T, err error) string {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("expected *Error, got %v", err)
	}
	return e.Code
}

// signHeader signs a request like the AWS SDK does for the panel's calls.
func signHeader(t *testing.T, secret string, at time.Time, method, rawURL string, body []byte) *http.Request {
	t.Helper()
	req, _ := http.NewRequest(method, rawURL, bytes.NewReader(body))
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	req.Header.Set("X-Amz-Content-Sha256", hash)
	req.Header.Set("Content-Type", "application/x-gzip")
	err := v4.NewSigner().SignHTTP(context.Background(), aws.Credentials{AccessKeyID: ak, SecretAccessKey: secret},
		req, hash, "s3", region, at)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func presign(t *testing.T, secret string, expires time.Duration, key string) *http.Request {
	t.Helper()
	cl := s3.New(s3.Options{
		Region:       region,
		BaseEndpoint: aws.String("http://" + host),
		UsePathStyle: true,
		Credentials:  credentials.NewStaticCredentialsProvider(ak, secret, ""),
	})
	pc := s3.NewPresignClient(cl)
	ps, err := pc.PresignUploadPart(context.Background(), &s3.UploadPartInput{
		Bucket: aws.String("bkt"), Key: aws.String(key), UploadId: aws.String("up 1/+"), PartNumber: aws.Int32(3),
	}, s3.WithPresignExpires(expires))
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(ps.Method, ps.URL, nil)
	return req
}

func TestHeaderAuth(t *testing.T) {
	now := time.Now().UTC()
	body := []byte(`<CompleteMultipartUpload/>`)
	u := "http://" + host + "/bkt/srv-1/bk-1.tar.gz?uploadId=abc%2Fdef&partNumber=2"
	v := newVerifier(now)

	res, err := v.Verify(signHeader(t, sk, now, "POST", u, body))
	if err != nil || res.Presigned || len(res.PayloadHash) != 64 {
		t.Fatalf("valid request rejected: %v %+v", err, res)
	}
	if _, err := v.Verify(signHeader(t, "other-secret", now, "POST", u, body)); code(t, err) != "SignatureDoesNotMatch" {
		t.Fatalf("wrong secret: %v", err)
	}
	// clock skew in both directions
	if _, err := newVerifier(now.Add(20 * time.Minute)).Verify(signHeader(t, sk, now, "POST", u, body)); code(t, err) != "RequestTimeTooSkewed" {
		t.Fatalf("old request: %v", err)
	}
	if _, err := newVerifier(now.Add(-20 * time.Minute)).Verify(signHeader(t, sk, now, "POST", u, body)); code(t, err) != "RequestTimeTooSkewed" {
		t.Fatalf("future request: %v", err)
	}
	if _, err := newVerifier(now.Add(10 * time.Minute)).Verify(signHeader(t, sk, now, "POST", u, body)); err != nil {
		t.Fatalf("within skew: %v", err)
	}
	// tampering
	r := signHeader(t, sk, now, "POST", u, body)
	r.URL.RawQuery = "uploadId=other&partNumber=2"
	if _, err := v.Verify(r); code(t, err) != "SignatureDoesNotMatch" {
		t.Fatalf("tampered query: %v", err)
	}
	r = signHeader(t, sk, now, "POST", u, body)
	r.URL.Path = "/bkt/srv-1/other.tar.gz"
	if _, err := v.Verify(r); code(t, err) != "SignatureDoesNotMatch" {
		t.Fatalf("tampered path: %v", err)
	}
	r = signHeader(t, sk, now, "POST", u, body)
	r.Method = "DELETE"
	if _, err := v.Verify(r); code(t, err) != "SignatureDoesNotMatch" {
		t.Fatalf("tampered method: %v", err)
	}
	// unknown access key
	r = signHeader(t, sk, now, "POST", u, body)
	r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), ak, "AKIAOTHER", 1))
	if _, err := v.Verify(r); code(t, err) != "InvalidAccessKeyId" {
		t.Fatalf("access key: %v", err)
	}
	// host / date / content hash must be part of the signature
	for _, drop := range []string{"host;", "x-amz-date", "x-amz-content-sha256;"} {
		r = signHeader(t, sk, now, "POST", u, body)
		r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"), drop, "", 1))
		if _, err := v.Verify(r); err == nil {
			t.Fatalf("dropping %q from SignedHeaders accepted", drop)
		}
	}
	// anonymous
	anon, _ := http.NewRequest("GET", u, nil)
	if _, err := v.Verify(anon); code(t, err) != "AccessDenied" {
		t.Fatalf("anonymous: %v", err)
	}
}

func TestPresignedAuth(t *testing.T) {
	now := time.Now().UTC()
	key := "srv-1/bk-1.tar.gz"

	res, err := newVerifier(now).Verify(presign(t, sk, 15*time.Minute, key))
	if err != nil || !res.Presigned || res.PayloadHash != "UNSIGNED-PAYLOAD" {
		t.Fatalf("valid presigned rejected: %v %+v", err, res)
	}
	if _, err := newVerifier(now).Verify(presign(t, "nope", time.Minute, key)); code(t, err) != "SignatureDoesNotMatch" {
		t.Fatalf("wrong secret: %v", err)
	}
	if _, err := newVerifier(now.Add(16 * time.Minute)).Verify(presign(t, sk, 15*time.Minute, key)); code(t, err) != "AccessDenied" {
		t.Fatalf("expired: %v", err)
	}
	if _, err := newVerifier(now.Add(14 * time.Minute)).Verify(presign(t, sk, 15*time.Minute, key)); err != nil {
		t.Fatalf("not yet expired: %v", err)
	}
	if _, err := newVerifier(now.Add(-time.Hour)).Verify(presign(t, sk, 15*time.Minute, key)); code(t, err) != "AccessDenied" {
		t.Fatalf("future presign: %v", err)
	}
	// extending the lifetime by hand must break the signature
	r := presign(t, sk, time.Minute, key)
	q := r.URL.Query()
	q.Set("X-Amz-Expires", "3600")
	r.URL.RawQuery = q.Encode()
	if _, err := newVerifier(now).Verify(r); code(t, err) != "SignatureDoesNotMatch" {
		t.Fatalf("tampered expires: %v", err)
	}
	// a presigned URL for a part is not valid for another method
	r = presign(t, sk, time.Minute, key)
	r.Method = "GET"
	if _, err := newVerifier(now).Verify(r); code(t, err) != "SignatureDoesNotMatch" {
		t.Fatalf("method swap: %v", err)
	}
}

func TestPresignedGetThroughServer(t *testing.T) {
	// A request that went over the wire (Host header from the server side).
	now := time.Now().UTC()
	var verr error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, verr = newVerifier(now).Verify(r)
	}))
	defer srv.Close()
	cl := s3.New(s3.Options{
		Region: region, UsePathStyle: true, BaseEndpoint: aws.String(srv.URL),
		Credentials: credentials.NewStaticCredentialsProvider(ak, sk, ""),
	})
	ps, err := s3.NewPresignClient(cl).PresignGetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String("bkt"), Key: aws.String("a/b.tar.gz"),
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(ps.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if verr != nil {
		t.Fatal(verr)
	}
}
