// Package ingest turns the parts of an S3 multipart upload into a restic
// snapshot: parts are spooled to disk, consumed strictly in order, gunzipped,
// indexed (tar headers) and streamed into `restic backup --stdin`.
package ingest

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const MaxPartNumber = 10000

var (
	ErrNoSuchUpload   = errors.New("no such upload")
	ErrInvalidPart    = errors.New("invalid part")
	ErrInvalidOrder   = errors.New("parts must be listed in ascending order")
	ErrPartConflict   = errors.New("part was already consumed with different content")
	ErrBadPartNumber  = errors.New("part number must be between 1 and 10000")
	ErrPipelineFailed = errors.New("ingest pipeline failed")
	ErrSpoolFull      = errors.New("spool quota exceeded")
	ErrSpoolTimeout   = errors.New("timed out waiting for free spool space")
	ErrSpoolIO        = errors.New("writing the part to the spool failed")
	ErrBodyRead       = errors.New("request body was not received completely")
	ErrBadSHA256      = errors.New("x-amz-content-sha256 does not match the body")
	ErrBadDigest      = errors.New("Content-MD5 does not match the body")
)

// Quota limits the total bytes spooled to disk across all uploads. Writers
// that do not fit wait for space instead of failing (see Upload.reserve).
type Quota struct {
	Max  int64 // <= 0 means unlimited
	used atomic.Int64

	mu   sync.Mutex
	wake chan struct{} // closed (and replaced) whenever space is released
}

func (q *Quota) tryReserve(n int64) bool {
	if q == nil {
		return true
	}
	for {
		cur := q.used.Load()
		if q.Max > 0 && cur+n > q.Max {
			return false
		}
		if q.used.CompareAndSwap(cur, cur+n) {
			return true
		}
	}
}

func (q *Quota) add(n int64) {
	if q == nil {
		return
	}
	q.used.Add(n)
	if n < 0 {
		q.mu.Lock()
		if q.wake != nil {
			close(q.wake)
			q.wake = nil
		}
		q.mu.Unlock()
	}
}

// waiter returns a channel that is closed the next time space is released.
// Fetch it before the reservation attempt so no release is missed.
func (q *Quota) waiter() <-chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.wake == nil {
		q.wake = make(chan struct{})
	}
	return q.wake
}

// Used returns the currently spooled bytes.
func (q *Quota) Used() int64 {
	if q == nil {
		return 0
	}
	return q.used.Load()
}

// PutOptions are optional checks and hints for PutPartOpts.
type PutOptions struct {
	Size   int64  // declared Content-Length, -1 if unknown (used to reserve spool quota)
	SHA256 string // expected hex SHA-256 of the body, if signed
	MD5    string // expected base64 MD5 (Content-MD5 header)
	// WaitTimeout bounds how long a part waits for free spool space. <= 0: do not
	// wait, fail at once with ErrSpoolFull. Ctx (optional) cancels the wait.
	WaitTimeout time.Duration
	Ctx         context.Context
}

type partState struct {
	size    int64
	etag    string // hex MD5, unquoted
	started bool   // reader opened (or finished) this part; content is frozen
	modTime time.Time
}

// PartInfo describes a received part.
type PartInfo struct {
	Number       int
	Size         int64
	ETag         string
	LastModified time.Time
}

// PartRef is one entry of a CompleteMultipartUpload request.
type PartRef struct {
	Number int
	ETag   string
}

// Upload is the in-memory state of one multipart upload.
type Upload struct {
	ID, Key, ServerUUID, BackupUUID string
	BackupID                        int64

	dir string

	mu        sync.Mutex
	parts     map[int]*partState
	next      int // part number the reader opens next
	total     int // set by Complete
	completed bool
	aborted   bool
	failure   error
	notify    chan struct{}
	lastSeen  time.Time

	quota *Quota
	held  int64 // spooled bytes of this upload that are charged to quota (under mu)
	etag  string
	sig   string // signature of the part list given to Complete

	consumed     atomic.Int64 // compressed bytes handed to the pipeline
	hasSlot      atomic.Bool  // the pipeline holds an ingest slot (set by Manager.run)
	timedOut     atomic.Bool
	deadlineOnce sync.Once
	timer        *time.Timer
	cancel       context.CancelFunc
	done         chan struct{}
}

func newUpload(id, key, serverUUID, backupUUID, spoolDir string, quota *Quota) *Upload {
	return &Upload{
		quota: quota,
		ID:    id, Key: key, ServerUUID: serverUUID, BackupUUID: backupUUID,
		dir:      filepath.Join(spoolDir, id),
		parts:    map[int]*partState{},
		next:     1,
		notify:   make(chan struct{}),
		lastSeen: time.Now(),
		done:     make(chan struct{}),
	}
}

// broadcast wakes everyone waiting on state changes. Caller holds u.mu.
func (u *Upload) broadcast() {
	close(u.notify)
	u.notify = make(chan struct{})
	u.lastSeen = time.Now()
}

func (u *Upload) closedErr() error {
	switch {
	case u.aborted:
		return ErrNoSuchUpload
	case u.failure != nil:
		return fmt.Errorf("%w: %v", ErrPipelineFailed, u.failure)
	}
	return nil
}

// PutPart spools a part and returns its (unquoted, hex) MD5 ETag.
func (u *Upload) PutPart(n int, body io.Reader) (string, error) {
	return u.PutPartOpts(n, body, PutOptions{Size: -1})
}

// PutPartOpts is PutPart with spool reservation and body verification.
func (u *Upload) PutPartOpts(n int, body io.Reader, o PutOptions) (string, error) {
	if n < 1 || n > MaxPartNumber {
		return "", ErrBadPartNumber
	}
	u.mu.Lock()
	err := u.closedErr()
	if err == nil && u.completed {
		err = ErrNoSuchUpload
	}
	u.mu.Unlock()
	if err != nil {
		return "", err
	}

	var held int64
	if o.Size > 0 {
		if err := u.reserve(n, o); err != nil {
			return "", err
		}
		held = o.Size
	}
	committed := false
	defer func() {
		if !committed {
			u.quota.add(-held)
		}
	}()

	tmp, err := os.CreateTemp(u.dir, "part-*.tmp")
	if err != nil {
		u.mu.Lock()
		cerr := u.closedErr() // the spool dir vanishes when the pipeline ends
		u.mu.Unlock()
		if cerr != nil {
			return "", cerr
		}
		return "", fmt.Errorf("%w: %v", ErrSpoolIO, err)
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	md := md5.New()
	w := []io.Writer{tmp, md}
	var sh hash.Hash
	if o.SHA256 != "" {
		sh = sha256.New()
		w = append(w, sh)
	}
	br := &errReader{r: body}
	size, err := io.Copy(io.MultiWriter(w...), br)
	if cerr := tmp.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("%w: %v", ErrSpoolIO, cerr)
	}
	u.quota.add(size - held)
	held = size
	if err != nil {
		if br.err != nil && !errors.Is(err, ErrSpoolIO) {
			return "", errors.Join(ErrBodyRead, err) // client side: aborted or short body
		}
		if !errors.Is(err, ErrSpoolIO) {
			err = fmt.Errorf("%w: %v", ErrSpoolIO, err)
		}
		return "", err
	}
	// Wings sends no Content-MD5, so the declared length is the only integrity
	// check on the wire: a part is only ever acknowledged if it is complete.
	if o.Size >= 0 && size != o.Size {
		return "", fmt.Errorf("%w: received %d of %d bytes", ErrBodyRead, size, o.Size)
	}
	etag := hex.EncodeToString(md.Sum(nil))
	if sh != nil && hex.EncodeToString(sh.Sum(nil)) != strings.ToLower(o.SHA256) {
		return "", ErrBadSHA256
	}
	if o.MD5 != "" {
		if want, derr := base64.StdEncoding.DecodeString(o.MD5); derr != nil || hex.EncodeToString(want) != etag {
			return "", ErrBadDigest
		}
	}

	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.closedErr(); err != nil {
		return "", err
	}
	if u.completed {
		return "", ErrNoSuchUpload
	}
	old, ok := u.parts[n]
	if ok && old.started {
		if old.etag == etag {
			return etag, nil // retry of an already consumed part
		}
		return "", ErrPartConflict
	}
	if err := os.Rename(tmp.Name(), u.partPath(n)); err != nil {
		return "", err
	}
	if ok {
		u.quota.add(-old.size)
		u.held -= old.size
	}
	u.held += size
	committed = true
	u.parts[n] = &partState{size: size, etag: etag, modTime: time.Now()}
	u.broadcast()
	return etag, nil
}

// errReader remembers whether an error came from the request body.
type errReader struct {
	r   io.Reader
	err error
}

func (e *errReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && err != io.EOF {
		e.err = err
	}
	return n, err
}

// reserve claims o.Size bytes of spool quota. When the spool is full it blocks
// until consumption frees space, the upload dies, ctx ends or WaitTimeout
// passes. Blocking (instead of an error) matters: Wings retries a failed
// UploadPart with a half-read body, which can never succeed.
//
// Exception: the part the running pipeline is waiting for (hasSlot and n ==
// next) is always admitted, overbooking the quota by at most one part per
// slot. Otherwise slot holders could wait for a part that waits for space
// occupied by queued uploads, which only move once a slot is free (a cycle
// that would only end when the wait times out).
func (u *Upload) reserve(n int, o PutOptions) error {
	q := u.quota
	if q == nil || q.tryReserve(o.Size) {
		return nil
	}
	if q.Max > 0 && o.Size > q.Max {
		return fmt.Errorf("%w: a part of %d bytes can never fit into a spool of %d bytes", ErrSpoolFull, o.Size, q.Max)
	}
	if u.pipelineWaitsFor(n) {
		q.add(o.Size)
		return nil
	}
	if o.WaitTimeout <= 0 {
		return ErrSpoolFull
	}
	ctx := o.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	timer := time.NewTimer(o.WaitTimeout)
	defer timer.Stop()
	poll := time.NewTicker(time.Second) // safety net, e.g. when Max is raised at runtime
	defer poll.Stop()
	for {
		wake := q.waiter()
		if q.tryReserve(o.Size) {
			return nil
		}
		if u.pipelineWaitsFor(n) {
			q.add(o.Size)
			return nil
		}
		u.mu.Lock()
		err := u.closedErr()
		if err == nil && u.completed {
			err = ErrNoSuchUpload
		}
		un := u.notify
		u.mu.Unlock()
		if err != nil {
			return err
		}
		select {
		case <-wake:
		case <-un:
		case <-poll.C:
		case <-timer.C:
			return fmt.Errorf("%w (%s)", ErrSpoolTimeout, o.WaitTimeout)
		case <-ctx.Done():
			return errors.Join(ErrBodyRead, ctx.Err())
		}
	}
}

// pipelineWaitsFor reports whether the running pipeline needs part n next.
func (u *Upload) pipelineWaitsFor(n int) bool {
	if !u.hasSlot.Load() {
		return false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.next == n
}

// releaseConsumed returns the quota of a part that was consumed and deleted.
func (u *Upload) releaseConsumed(size int64) {
	u.mu.Lock()
	u.held -= size
	u.mu.Unlock()
	u.quota.add(-size)
}

// releaseAll returns whatever is still charged to the quota.
func (u *Upload) releaseAll() {
	u.mu.Lock()
	h := u.held
	u.held = 0
	u.mu.Unlock()
	u.quota.add(-h)
}

func (u *Upload) partPath(n int) string { return filepath.Join(u.dir, strconv.Itoa(n)) }

// Parts lists the received parts in ascending order.
func (u *Upload) Parts() []PartInfo {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([]PartInfo, 0, len(u.parts))
	for n, p := range u.parts {
		out = append(out, PartInfo{n, p.size, p.etag, p.modTime})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number < out[j].Number })
	return out
}

// Complete validates the part list against what was received and tells the
// pipeline where the stream ends. It returns the S3-style multipart ETag
// (unquoted). It does not wait for restic.
func (u *Upload) Complete(refs []PartRef) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.closedErr(); err != nil {
		return "", err
	}
	if len(refs) == 0 {
		return "", fmt.Errorf("%w: no parts listed", ErrInvalidPart)
	}
	if u.completed {
		// Retried Complete: same list gets the same answer, anything else is an error.
		if PartsSig(refs) != u.sig {
			return "", fmt.Errorf("%w: upload was already completed with a different part list", ErrInvalidPart)
		}
		return u.etag, nil
	}
	for i := 1; i < len(refs); i++ {
		if refs[i].Number <= refs[i-1].Number {
			return "", ErrInvalidOrder
		}
	}
	sum := md5.New()
	for i, r := range refs {
		if r.Number != i+1 {
			return "", fmt.Errorf("%w: part %d: numbering must be contiguous from 1", ErrInvalidPart, r.Number)
		}
		p, ok := u.parts[r.Number]
		if !ok {
			return "", fmt.Errorf("%w: part %d was not uploaded", ErrInvalidPart, r.Number)
		}
		if strings.Trim(r.ETag, `"`) != p.etag {
			return "", fmt.Errorf("%w: part %d etag mismatch", ErrInvalidPart, r.Number)
		}
		raw, _ := hex.DecodeString(p.etag)
		sum.Write(raw)
	}
	for n, p := range u.parts {
		if n > len(refs) && p.started {
			return "", fmt.Errorf("%w: part %d was consumed but is not listed", ErrInvalidPart, n)
		}
	}
	for n := range u.parts {
		if n > len(refs) {
			os.Remove(u.partPath(n))
			u.quota.add(-u.parts[n].size)
			u.held -= u.parts[n].size
			delete(u.parts, n)
		}
	}
	u.total = len(refs)
	u.completed = true
	u.etag = fmt.Sprintf("%s-%d", hex.EncodeToString(sum.Sum(nil)), len(refs))
	u.sig = PartsSig(refs)
	u.broadcast()
	return u.etag, nil
}

// PartsSig identifies a CompleteMultipartUpload part list.
func PartsSig(refs []PartRef) string {
	h := sha256.New()
	for _, r := range refs {
		fmt.Fprintf(h, "%d:%s\n", r.Number, strings.Trim(r.ETag, `"`))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (u *Upload) isAborted() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.aborted
}

func (u *Upload) isCompleted() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.completed
}

func (u *Upload) idleSince() time.Time {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.lastSeen
}

func (u *Upload) fail(err error) {
	u.mu.Lock()
	u.failure = err
	u.broadcast()
	u.mu.Unlock()
}

// openNext blocks until the next part is available, opens it and marks it
// frozen. It returns io.EOF after the last part of a completed upload.
func (u *Upload) openNext(ctx context.Context) (*os.File, int64, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for {
		if u.aborted {
			return nil, 0, context.Canceled
		}
		if u.total > 0 && u.next > u.total {
			return nil, 0, io.EOF
		}
		if p, ok := u.parts[u.next]; ok {
			f, err := os.Open(u.partPath(u.next))
			if err != nil {
				return nil, 0, err
			}
			p.started = true
			u.next++
			u.broadcast() // a writer waiting for the following part may now be admitted
			return f, p.size, nil
		}
		ch := u.notify
		u.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			u.mu.Lock()
			return nil, 0, ctx.Err()
		}
		u.mu.Lock()
	}
}

// partReader concatenates the parts in order, deleting each once consumed.
type partReader struct {
	u       *Upload
	ctx     context.Context
	cur     *os.File
	curSize int64
}

func (r *partReader) Read(p []byte) (int, error) {
	for {
		if r.cur == nil {
			f, size, err := r.u.openNext(r.ctx)
			if err != nil {
				return 0, err
			}
			r.cur, r.curSize = f, size
		}
		n, err := r.cur.Read(p)
		if n > 0 {
			r.u.consumed.Add(int64(n))
			return n, nil
		}
		if err == io.EOF {
			r.closeCur()
			continue
		}
		if err != nil {
			return 0, err
		}
	}
}

func (r *partReader) closeCur() {
	if r.cur != nil {
		name := r.cur.Name()
		r.cur.Close()
		os.Remove(name)
		r.cur = nil
		r.u.releaseConsumed(r.curSize)
	}
}

func (r *partReader) Close() error { r.closeCur(); return nil }
