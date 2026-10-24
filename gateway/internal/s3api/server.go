// Package s3api implements the slice of the S3 API that Pterodactyl uses for
// backups (multipart upload, GetObject, DeleteObject), path-style only.
package s3api

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"bufio"

	"ptero-enhanced-backup/gateway/internal/gate"
	"ptero-enhanced-backup/gateway/internal/ingest"
	"ptero-enhanced-backup/gateway/internal/s3auth"
	"ptero-enhanced-backup/gateway/internal/store"
)

// Restic is what the API needs from the restic wrapper.
type Restic interface {
	Dump(ctx context.Context, snapshotID, path string) (io.ReadCloser, error)
	Forget(ctx context.Context, snapshotID string) error
}

type Server struct {
	Bucket   string
	Verifier *s3auth.Verifier
	Ingest   *ingest.Manager
	Store    *store.Store
	Restic   Restic
	Log      *slog.Logger
	// WaitTimeout bounds how long GetObject waits for a running ingest.
	WaitTimeout time.Duration
	// PublicURL, if set, is used for the Location of CompleteMultipartUpload results.
	PublicURL string
	// MaxPartBytes caps a single UploadPart body (default 1 GiB).
	MaxPartBytes int64
	// MaxDumps limits concurrent restores (default 2); DumpWait bounds the wait for a slot.
	MaxDumps int
	DumpWait time.Duration
	// DeleteWait bounds how long DeleteObject waits for a running ingest.
	DeleteWait time.Duration
	// DownloadTimeout bounds a whole restore/download (default 2h).
	DownloadTimeout time.Duration
	// Gate, if set, holds back new restores while maintenance needs the repository.
	Gate *gate.Gate

	dumpOnce sync.Once
	dumpSem  chan struct{}
}

var keyRe = regexp.MustCompile(`^([A-Za-z0-9_-]{1,64})/([A-Za-z0-9_-]{1,64})\.tar\.gz$`)

func (s *Server) maxPart() int64 {
	if s.MaxPartBytes > 0 {
		return s.MaxPartBytes
	}
	return 1 << 30
}

func (s *Server) dumpSlots() chan struct{} {
	s.dumpOnce.Do(func() {
		n := s.MaxDumps
		if n < 1 {
			n = 2
		}
		s.dumpSem = make(chan struct{}, n)
	})
	return s.dumpSem
}

// ErrDumpBusy means no restore slot became free in time.
var ErrDumpBusy = errors.New("too many restores are running")

// AcquireDump reserves a restore slot (and passes the maintenance gate). The
// returned function releases it. Shared by S3 downloads and the admin API.
func (s *Server) AcquireDump(ctx context.Context) (func(), error) {
	wait := s.DumpWait
	if wait <= 0 {
		wait = 2 * time.Minute
	}
	slots := s.dumpSlots()
	wctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	select {
	case slots <- struct{}{}:
	case <-wctx.Done():
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrDumpBusy
	}
	if s.Gate != nil {
		if err := s.Gate.Enter(wctx); err != nil {
			<-slots
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, ErrDumpBusy
		}
	}
	return func() {
		if s.Gate != nil {
			s.Gate.Leave()
		}
		<-slots
	}, nil
}

// ActiveDumps returns the number of running restores.
func (s *Server) ActiveDumps() int { return len(s.dumpSlots()) }

func (s *Server) downloadTimeout() time.Duration {
	if s.DownloadTimeout > 0 {
		return s.DownloadTimeout
	}
	return 2 * time.Hour
}

func (s *Server) waitTimeout() time.Duration {
	if s.WaitTimeout > 0 {
		return s.WaitTimeout
	}
	return 10 * time.Minute
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok\n")
		return
	}
	reqID := newRequestID()
	sw := &statusWriter{ResponseWriter: w, status: 200}
	start := time.Now()
	defer func() {
		s.Log.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"bytes", sw.n, "dur", time.Since(start).Round(time.Millisecond), "id", reqID)
	}()

	if err := s.handle(sw, r, reqID); err != nil {
		var ae *apiError
		if !errors.As(err, &ae) {
			s.Log.Error("internal error", "err", err, "id", reqID)
			ae = newErr(500, "InternalError", "We encountered an internal error. Please try again.")
		}
		writeError(sw, r, reqID, ae)
	}
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request, reqID string) error {
	w.Header().Set("x-amz-request-id", reqID)
	w.Header().Set("Server", "peb-gateway")

	auth, err := s.Verifier.Verify(r)
	if err != nil {
		var ae *s3auth.Error
		if errors.As(err, &ae) {
			s.Log.Warn("auth rejected", "code", ae.Code, "path", r.URL.Path, "method", r.Method)
			return newErr(statusFor(ae.Code), ae.Code, ae.Message)
		}
		return err
	}
	if strings.HasPrefix(auth.PayloadHash, "STREAMING-") {
		return newErr(501, "NotImplemented", "aws-chunked uploads are not supported")
	}

	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if bucket != s.Bucket {
		return newErr(404, "NoSuchBucket", "The specified bucket does not exist")
	}
	if key == "" {
		return newErr(501, "NotImplemented", "Bucket operations are not supported")
	}
	q := r.URL.Query()
	uploadID := q.Get("uploadId")

	switch {
	case r.Method == http.MethodPost && q.Has("uploads"):
		return s.createMultipart(w, r, key)
	case r.Method == http.MethodPut && q.Has("partNumber") && uploadID != "":
		return s.uploadPart(w, r, key, uploadID, q.Get("partNumber"), auth)
	case r.Method == http.MethodGet && uploadID != "":
		return s.listParts(w, r, key, uploadID)
	case r.Method == http.MethodPost && uploadID != "":
		return s.completeMultipart(w, r, key, uploadID, auth.PayloadHash)
	case r.Method == http.MethodDelete && uploadID != "":
		return s.abortMultipart(w, key, uploadID)
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		return s.getObject(w, r, key)
	case r.Method == http.MethodDelete:
		return s.deleteObject(w, r, key)
	}
	return newErr(405, "MethodNotAllowed", "The specified method is not allowed against this resource.")
}

func parseKey(key string) (server, backup string, ok bool) {
	m := keyRe.FindStringSubmatch(key)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

func (s *Server) upload(key, id string) (*ingest.Upload, error) {
	u, err := s.Ingest.Get(id)
	if err != nil || u.Key != key {
		return nil, newErr(404, "NoSuchUpload", "The specified upload does not exist.")
	}
	return u, nil
}

// uploadErr translates ingest errors. Internal details are logged, never sent.
func (s *Server) uploadErr(err error) error {
	switch {
	case errors.Is(err, ingest.ErrNoSuchUpload):
		return newErr(404, "NoSuchUpload", "The specified upload does not exist or was already completed or aborted.")
	case errors.Is(err, ingest.ErrBadPartNumber):
		return newErr(400, "InvalidArgument", err.Error())
	case errors.Is(err, ingest.ErrInvalidOrder):
		return newErr(400, "InvalidPartOrder", err.Error())
	case errors.Is(err, ingest.ErrInvalidPart):
		return newErr(400, "InvalidPart", err.Error())
	case errors.Is(err, ingest.ErrPartConflict):
		return newErr(409, "OperationAborted", err.Error())
	case errors.Is(err, ingest.ErrSpoolFull):
		return newErr(503, "SlowDown", "The gateway spool is full. Please retry later.")
	case errors.Is(err, ingest.ErrBadSHA256):
		return newErr(400, "XAmzContentSHA256Mismatch", "The provided 'x-amz-content-sha256' header does not match what was computed.")
	case errors.Is(err, ingest.ErrBadDigest):
		return newErr(400, "BadDigest", "The Content-MD5 you specified did not match what we received.")
	case errors.Is(err, ingest.ErrPipelineFailed):
		s.Log.Error("upload pipeline failed", "err", err)
		return newErr(500, "InternalError", "Processing of this upload failed. See the gateway log.")
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return newErr(400, "EntityTooLarge", "Your proposed upload exceeds the maximum allowed size")
	}
	return err
}

type initiateResult struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	Bucket   string
	Key      string
	UploadID string `xml:"UploadId"`
}

func (s *Server) createMultipart(w http.ResponseWriter, r *http.Request, key string) error {
	server, backup, ok := parseKey(key)
	if !ok {
		return newErr(400, "InvalidArgument", "Key must look like <server-uuid>/<backup-uuid>.tar.gz")
	}
	u, err := s.Ingest.Create(r.Context(), key, server, backup)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/xml")
	writeXML(w, initiateResult{Bucket: s.Bucket, Key: key, UploadID: u.ID})
	return nil
}

func (s *Server) uploadPart(w http.ResponseWriter, r *http.Request, key, uploadID, partNo string, auth *s3auth.Result) error {
	u, err := s.upload(key, uploadID)
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(partNo)
	if err != nil {
		return newErr(400, "InvalidArgument", "Part number must be an integer between 1 and 10000, inclusive")
	}
	if r.Header.Get("x-amz-copy-source") != "" {
		return newErr(501, "NotImplemented", "UploadPartCopy is not supported")
	}
	if r.ContentLength < 0 {
		return newErr(411, "MissingContentLength", "You must provide the Content-Length HTTP header.")
	}
	if r.ContentLength > s.maxPart() {
		return newErr(400, "EntityTooLarge", "Your proposed upload exceeds the maximum allowed size")
	}
	opts := ingest.PutOptions{Size: r.ContentLength, MD5: r.Header.Get("Content-MD5")}
	if !auth.Presigned && len(auth.PayloadHash) == 64 {
		opts.SHA256 = auth.PayloadHash
	}
	body := http.MaxBytesReader(w, r.Body, s.maxPart())
	etag, err := u.PutPartOpts(n, body, opts)
	if err != nil {
		return s.uploadErr(err)
	}
	if err := s.Store.UpsertPart(r.Context(), uploadID, n, r.ContentLength, etag); err != nil {
		s.Log.Warn("store part", "err", err)
	}
	w.Header().Set("ETag", `"`+etag+`"`)
	return nil
}

type listPartsResult struct {
	XMLName              xml.Name `xml:"ListPartsResult"`
	Bucket               string
	Key                  string
	UploadID             string `xml:"UploadId"`
	PartNumberMarker     int
	NextPartNumberMarker int
	MaxParts             int
	IsTruncated          bool
	Parts                []listPart `xml:"Part"`
}

type listPart struct {
	PartNumber   int
	LastModified string
	ETag         string
	Size         int64
}

func (s *Server) listParts(w http.ResponseWriter, r *http.Request, key, uploadID string) error {
	u, err := s.upload(key, uploadID)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	marker, _ := strconv.Atoi(q.Get("part-number-marker"))
	max, err := strconv.Atoi(q.Get("max-parts"))
	if err != nil || max < 1 || max > 1000 {
		max = 1000
	}
	res := listPartsResult{Bucket: s.Bucket, Key: key, UploadID: uploadID, PartNumberMarker: marker, MaxParts: max}
	for _, p := range u.Parts() {
		if p.Number <= marker {
			continue
		}
		if len(res.Parts) == max {
			res.IsTruncated = true
			break
		}
		res.Parts = append(res.Parts, listPart{p.Number, p.LastModified.UTC().Format("2006-01-02T15:04:05.000Z"), `"` + p.ETag + `"`, p.Size})
		res.NextPartNumberMarker = p.Number
	}
	w.Header().Set("Content-Type", "application/xml")
	writeXML(w, res)
	return nil
}

type completeRequest struct {
	XMLName xml.Name `xml:"CompleteMultipartUpload"`
	Parts   []struct {
		PartNumber int
		ETag       string
	} `xml:"Part"`
}

type completeResult struct {
	XMLName  xml.Name `xml:"CompleteMultipartUploadResult"`
	Location string
	Bucket   string
	Key      string
	ETag     string
}

func (s *Server) completeMultipart(w http.ResponseWriter, r *http.Request, key, uploadID, payloadHash string) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return err
	}
	if len(payloadHash) == 64 {
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != strings.ToLower(payloadHash) {
			return newErr(400, "XAmzContentSHA256Mismatch", "The provided 'x-amz-content-sha256' header does not match what was computed.")
		}
	}
	var req completeRequest
	if err := xml.Unmarshal(body, &req); err != nil {
		return newErr(400, "MalformedXML", "The XML you provided was not well-formed or did not validate against our published schema.")
	}
	refs := make([]ingest.PartRef, len(req.Parts))
	for i, p := range req.Parts {
		refs[i] = ingest.PartRef{Number: p.PartNumber, ETag: p.ETag}
	}
	etag, err := s.Ingest.Complete(r.Context(), uploadID, key, refs)
	if err != nil {
		return s.uploadErr(err)
	}
	w.Header().Set("Content-Type", "application/xml")
	writeXML(w, completeResult{Location: s.location(r), Bucket: s.Bucket, Key: key, ETag: `"` + etag + `"`})
	return nil
}

// location builds the object URL for Complete results: configured public URL,
// then forwarded headers, then the request itself.
func (s *Server) location(r *http.Request) string {
	if s.PublicURL != "" {
		return strings.TrimRight(s.PublicURL, "/") + r.URL.EscapedPath()
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if p := r.Header.Get("X-Forwarded-Proto"); p == "http" || p == "https" {
		scheme = p
	}
	host := r.Host
	if h := r.Header.Get("X-Forwarded-Host"); h != "" {
		host, _, _ = strings.Cut(h, ",")
		host = strings.TrimSpace(host)
	}
	return scheme + "://" + host + r.URL.EscapedPath()
}

func (s *Server) abortMultipart(w http.ResponseWriter, key, uploadID string) error {
	if _, err := s.upload(key, uploadID); err != nil {
		return err
	}
	if err := s.Ingest.Abort(uploadID); err != nil {
		return s.uploadErr(err)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// awaitIngest waits (bounded) until a backup whose upload was completed has
// been processed. Uploads that were never completed do not exist for readers.
func (s *Server) awaitIngest(ctx context.Context, b *store.Backup, limit time.Duration) (*store.Backup, error) {
	timer := time.NewTimer(limit)
	defer timer.Stop()
	for b.Status == store.StatusPending {
		up, _ := s.Store.UploadStatus(ctx, b.UploadID)
		if up != "complete" {
			return nil, newErr(404, "NoSuchKey", "The specified key does not exist (upload not completed).")
		}
		if done := s.Ingest.Done(b.UploadID); done != nil {
			select {
			case <-done:
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-timer.C:
				return nil, newErr(503, "SlowDown", "The backup is still being processed. Please retry later.")
			}
		}
		var err error
		if b, err = s.Store.BackupByID(ctx, b.ID); err != nil {
			return nil, err
		}
		if b.Status == store.StatusPending {
			select { // pipeline gone but row not final yet; brief pause, still bounded
			case <-time.After(200 * time.Millisecond):
			case <-timer.C:
				return nil, newErr(503, "SlowDown", "The backup is still being processed. Please retry later.")
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	return b, nil
}

// lookup finds the backup for key, waiting for a running ingest if needed.
func (s *Server) lookup(ctx context.Context, key string) (*store.Backup, error) {
	b, err := s.Store.BackupByKey(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return nil, newErr(404, "NoSuchKey", "The specified key does not exist.")
	}
	if err != nil {
		return nil, err
	}
	if b, err = s.awaitIngest(ctx, b, s.waitTimeout()); err != nil {
		return nil, err
	}
	if b.Status != store.StatusComplete {
		return nil, newErr(404, "NoSuchKey", "The specified key does not exist ("+b.Status+").")
	}
	return b, nil
}

func (s *Server) getObject(w http.ResponseWriter, r *http.Request, key string) error {
	if _, _, ok := parseKey(key); !ok {
		return newErr(404, "NoSuchKey", "The specified key does not exist.")
	}
	head := r.Method == http.MethodHead
	b, err := s.lookup(r.Context(), key)
	if err != nil {
		return err
	}
	setHeaders := func() {
		h := w.Header()
		h.Set("Content-Type", "application/x-gzip")
		h.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.tar.gz"`, b.BackupUUID))
		if !b.FinishedAt.IsZero() {
			h.Set("Last-Modified", b.FinishedAt.UTC().Format(http.TimeFormat))
		}
	}
	if head {
		setHeaders()
		return nil
	}

	release, err := s.AcquireDump(r.Context())
	if errors.Is(err, ErrDumpBusy) {
		return newErr(503, "SlowDown", "Too many restores are running. Please retry later.")
	}
	if err != nil {
		return err
	}
	defer release()

	dctx, cancel := context.WithTimeout(r.Context(), s.downloadTimeout())
	defer cancel()
	// A stalled client must not hold a restore slot (and block maintenance) forever.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(s.downloadTimeout()))
	rc, err := s.Restic.Dump(dctx, b.SnapshotID, "/"+b.ServerUUID+".tar")
	if err != nil {
		return err
	}
	defer rc.Close()
	br := bufio.NewReaderSize(rc, 256<<10)
	if _, err := br.Peek(1); err != nil && !errors.Is(err, io.EOF) {
		return err // nothing sent yet, a clean 500 is possible
	}
	setHeaders()
	w.WriteHeader(http.StatusOK)
	gz, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
	if _, err := io.Copy(gz, br); err != nil {
		s.Log.Error("restore stream failed", "key", key, "err", err)
		// Abort the connection so the client sees a truncated, not a "complete", archive.
		panic(http.ErrAbortHandler)
	}
	return gz.Close()
}

func (s *Server) deleteObject(w http.ResponseWriter, r *http.Request, key string) error {
	ctx := r.Context()
	b, err := s.Store.BackupByKey(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	if err != nil {
		return err
	}
	if b.Status == store.StatusPending {
		up, _ := s.Store.UploadStatus(ctx, b.UploadID)
		if up != "complete" {
			// Never completed: deleting means abandoning the upload.
			if err := s.Ingest.Abort(b.UploadID); err != nil && !errors.Is(err, ingest.ErrNoSuchUpload) {
				return err
			}
			if b, err = s.Store.BackupByID(ctx, b.ID); err != nil {
				return err
			}
		} else {
			wait := s.DeleteWait
			if wait <= 0 {
				wait = time.Minute
			}
			if b, err = s.awaitIngest(ctx, b, wait); err != nil {
				var ae *apiError
				if errors.As(err, &ae) && ae.Code == "SlowDown" {
					return newErr(409, "OperationAborted", "The backup is still being processed; delete it again later.")
				}
				return err
			}
		}
	}
	if b.SnapshotID != "" {
		// Forget waits while maintenance holds the repository; ask the client to retry then.
		wait := s.DumpWait
		if wait <= 0 {
			wait = 2 * time.Minute
		}
		fctx, cancel := context.WithTimeout(ctx, wait)
		err := s.Restic.Forget(fctx, b.SnapshotID)
		cancel()
		if err != nil {
			if ctx.Err() == nil && errors.Is(fctx.Err(), context.DeadlineExceeded) {
				return newErr(503, "SlowDown", "The repository is in maintenance. Please retry later.")
			}
			return err
		}
	}
	if err := s.Store.MarkDeleted(ctx, b.ID); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return strings.ToUpper(hex.EncodeToString(b[:]))
}

// statusWriter records status and size for the access log.
type statusWriter struct {
	http.ResponseWriter
	status int
	n      int64
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.n += int64(n)
	return n, err
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
