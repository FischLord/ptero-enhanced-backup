package admin

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"ptero-enhanced-backup/gateway/internal/restic"
	"ptero-enhanced-backup/gateway/internal/secrets"
)

// KeyManager manages restic keys (second passwords) of the repository.
type KeyManager interface {
	KeyAdd(ctx context.Context, newPassword, host, user string) (string, error)
	KeyList(ctx context.Context) ([]restic.Key, error)
	KeyRemove(ctx context.Context, id string) error
}

const (
	emergencyUser    = "peb-emergency"
	defaultKeyLabel  = "emergency"
	maxEmergencyKeys = 10
	keyPasswordLen   = 40
	keyAlphabet      = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789" // no look-alike characters
)

// keyOpTimeout bounds every key operation including the wait for the lock.
const keyOpTimeout = 5 * time.Minute // restic waits up to 2m for the lock, twice (unlock and retry)

// keysLock allows one key operation at a time; waiting ends with the context.
var keysLock = make(chan struct{}, 1)

// lockKeys acquires the key lock or fails with the context error. The returned
// func releases it.
func lockKeys(ctx context.Context) (func(), error) {
	select {
	case keysLock <- struct{}{}:
		return func() { <-keysLock }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// keyCtx derives the bounded context and takes the lock; on failure it answers 503.
func (s *Server) keyCtx(w http.ResponseWriter, r *http.Request) (context.Context, func(), bool) {
	ctx, cancel := context.WithTimeout(r.Context(), keyOpTimeout)
	unlock, err := lockKeys(ctx)
	if err != nil {
		cancel()
		writeErr(w, http.StatusServiceUnavailable, "busy", "another key operation is running or timed out")
		return nil, nil, false
	}
	return ctx, func() { unlock(); cancel() }, true
}

func newKeyPassword() (string, error) {
	b := make([]byte, keyPasswordLen)
	max := big.NewInt(int64(len(keyAlphabet)))
	for i := range b {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b[i] = keyAlphabet[n.Int64()]
	}
	return string(b), nil
}

type keyJSON struct {
	ID        string  `json:"id"`
	ShortID   string  `json:"short_id"`
	CreatedAt *string `json:"created_at"`
	Host      string  `json:"host"`
	User      string  `json:"user"`
	Current   bool    `json:"current"`
	Emergency bool    `json:"emergency"`
	Label     string  `json:"label"`
}

func (s *Server) keysUnavailable(w http.ResponseWriter) bool {
	if s.Keys == nil {
		writeErr(w, http.StatusNotImplemented, "not_supported", "key management is not available")
		return true
	}
	return false
}

func shortID(id string) string { return id[:min(8, len(id))] }

// listKeys: GET /keys.
func (s *Server) listKeys(w http.ResponseWriter, r *http.Request) {
	if s.keysUnavailable(w) {
		return
	}
	ctx, done, ok := s.keyCtx(w, r)
	if !ok {
		return
	}
	defer done()
	keys, err := s.Keys.KeyList(ctx)
	if err != nil {
		s.internal(w, err)
		return
	}
	known, err := s.Store.EmergencyKeys(ctx)
	if err != nil {
		s.internal(w, err)
		return
	}
	out := make([]keyJSON, 0, len(keys))
	for _, k := range keys {
		j := keyJSON{ID: k.ID, ShortID: shortID(k.ID), CreatedAt: tstr(k.Created), Host: k.Host, User: k.User, Current: k.Current}
		if e, ok := known[k.ID]; ok {
			j.Emergency, j.Label = true, e.Label
		}
		out = append(out, j)
	}
	writeJSON(w, 200, map[string]any{"keys": out})
}

// createKey: POST /keys {"label": "..."}. The new password is returned exactly once and stored nowhere.
func (s *Server) createKey(w http.ResponseWriter, r *http.Request) {
	if s.keysUnavailable(w) {
		return
	}
	var in struct {
		Label string `json:"label"`
	}
	if body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096)); len(strings.TrimSpace(string(body))) > 0 {
		if json.Unmarshal(body, &in) != nil {
			writeErr(w, 400, "bad_request", `body must be {"label": "name"} or empty`)
			return
		}
	}
	label := strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, in.Label))
	if rs := []rune(label); len(rs) > 64 {
		label = string(rs[:64])
	}
	if label == "" {
		label = defaultKeyLabel
	}
	ctx, done, ok := s.keyCtx(w, r)
	if !ok {
		return
	}
	defer done()
	known, err := s.Store.EmergencyKeys(ctx)
	if err != nil {
		s.internal(w, err)
		return
	}
	if len(known) >= maxEmergencyKeys {
		writeErr(w, http.StatusConflict, "too_many_keys", "delete an existing emergency key first")
		return
	}
	pw, err := newKeyPassword()
	if err != nil {
		s.internal(w, errors.New("random source failed"))
		return
	}
	id, err := s.Keys.KeyAdd(ctx, pw, label, emergencyUser)
	if err != nil {
		s.Log.Error("restic key add failed", "err", secrets.ScrubWith(err.Error(), pw))
		writeErr(w, http.StatusBadGateway, "restic_failed", "restic could not add the key")
		return
	}
	now := s.now()
	if err := s.Store.AddEmergencyKey(ctx, id, label, now); err != nil {
		// The password would be lost with this error, so take the key out of the repository again.
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), keyOpTimeout)
		rerr := s.Keys.KeyRemove(rctx, id)
		rcancel()
		if rerr != nil {
			s.Log.Error("rollback of restic key failed; key remains in the repository", "key", shortID(id), "err", secrets.Scrub(rerr.Error()))
		}
		s.internal(w, err)
		return
	}
	s.Log.Info("emergency key created", "key", shortID(id), "label", label)
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": id, "short_id": shortID(id), "label": label, "host": label, "user": emergencyUser,
		"created_at": tstr(now), "password": pw,
		"notice": "This password is shown only once and is not stored by the gateway.",
	})
}

// deleteKey: DELETE /keys/{id}. The key in use is never removed.
func (s *Server) deleteKey(w http.ResponseWriter, r *http.Request) {
	if s.keysUnavailable(w) {
		return
	}
	want := r.PathValue("id")
	ctx, done, ok := s.keyCtx(w, r)
	if !ok {
		return
	}
	defer done()
	keys, err := s.Keys.KeyList(ctx)
	if err != nil {
		s.internal(w, err)
		return
	}
	var match []restic.Key
	for _, k := range keys {
		if k.ID == want || (len(want) >= 8 && strings.HasPrefix(k.ID, want)) {
			match = append(match, k)
		}
	}
	switch {
	case len(match) == 0:
		writeErr(w, 404, "not_found", "no such key")
		return
	case len(match) > 1:
		writeErr(w, 400, "ambiguous", "key ID prefix is ambiguous")
		return
	case match[0].Current:
		writeErr(w, http.StatusConflict, "current_key", "the key in use cannot be removed")
		return
	}
	id := match[0].ID
	if err := s.Keys.KeyRemove(ctx, id); err != nil {
		if errors.Is(err, restic.ErrCurrentKey) {
			writeErr(w, http.StatusConflict, "current_key", "the key in use cannot be removed")
			return
		}
		s.Log.Error("restic key remove failed", "err", secrets.Scrub(err.Error()))
		writeErr(w, http.StatusBadGateway, "restic_failed", "restic could not remove the key")
		return
	}
	if err := s.Store.DeleteEmergencyKey(ctx, id); err != nil {
		// The key is already gone from the repository; a stale record is harmless (the list shows restic's keys).
		s.Log.Error("emergency key record could not be deleted", "key", shortID(id), "err", err)
	}
	s.Log.Info("restic key removed", "key", shortID(id))
	w.WriteHeader(http.StatusNoContent)
}
