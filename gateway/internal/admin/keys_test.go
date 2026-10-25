package admin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"ptero-enhanced-backup/gateway/internal/restic"
)

type fakeKeys struct {
	keys    []restic.Key
	n       int
	addErr  error
	added   []string // passwords received
	removed []string
}

func (f *fakeKeys) KeyAdd(ctx context.Context, pw, host, user string) (string, error) {
	f.added = append(f.added, pw)
	if f.addErr != nil {
		return "", f.addErr
	}
	f.n++
	id := fmt.Sprintf("%064x", f.n)
	f.keys = append(f.keys, restic.Key{ID: id, Host: host, User: user, Created: base})
	return id, nil
}
func (f *fakeKeys) KeyList(ctx context.Context) ([]restic.Key, error) { return f.keys, nil }
func (f *fakeKeys) KeyRemove(ctx context.Context, id string) error {
	for i, k := range f.keys {
		if k.ID == id {
			if k.Current {
				return restic.ErrCurrentKey
			}
			f.keys = append(f.keys[:i], f.keys[i+1:]...)
			f.removed = append(f.removed, id)
			return nil
		}
	}
	return errors.New("no such key")
}

func keysFixture(t *testing.T) (*fixture, *fakeKeys, *bytes.Buffer) {
	f := newFixture(t)
	fk := &fakeKeys{keys: []restic.Key{{ID: strings.Repeat("a", 64), Host: "gw", User: "root", Current: true, Created: base}}}
	f.srv.Keys = fk
	var logs bytes.Buffer
	f.srv.Log = slog.New(slog.NewTextHandler(&logs, nil))
	return f, fk, &logs
}

func TestPasswordAck(t *testing.T) {
	f := newFixture(t)
	m := decode(t, f.get(t, "/settings/password-ack"), 200)
	if m["acknowledged"] != false || m["acknowledged_at"] != nil {
		t.Fatalf("%v", m)
	}
	m = decode(t, f.do(t, "PUT", "/settings/password-ack", `{"acknowledged":true,"by":"root"}`, token), 200)
	if m["acknowledged"] != true || m["by"] != "root" || m["acknowledged_at"] != base.Format(time.RFC3339) {
		t.Fatalf("%v", m)
	}
	m = decode(t, f.get(t, "/settings/password-ack"), 200)
	if m["acknowledged"] != true {
		t.Fatalf("not persisted: %v", m)
	}
	decode(t, f.do(t, "PUT", "/settings/password-ack", `{}`, token), 400)
	decode(t, f.do(t, "PUT", "/settings/password-ack", `nope`, token), 400)
	m = decode(t, f.do(t, "PUT", "/settings/password-ack", `{"acknowledged":false}`, token), 200)
	if m["acknowledged"] != false {
		t.Fatalf("%v", m)
	}
	if rec := f.do(t, "GET", "/settings/password-ack", "", ""); rec.Code != 401 {
		t.Fatalf("auth: %d", rec.Code)
	}
}

func TestKeysCreateListDelete(t *testing.T) {
	f, fk, logs := keysFixture(t)
	m := decode(t, f.do(t, "POST", "/keys", `{"label":"Hoster"}`, token), 201)
	pw, _ := m["password"].(string)
	id, _ := m["id"].(string)
	if len(pw) < 32 || m["label"] != "Hoster" || id == "" || fk.added[0] != pw {
		t.Fatalf("%v", m)
	}
	m = decode(t, f.get(t, "/keys"), 200)
	if strings.Contains(fmt.Sprint(m), pw) {
		t.Fatal("password must not appear in the list")
	}
	list := m["keys"].([]any)
	if len(list) != 2 {
		t.Fatalf("%v", list)
	}
	k0, k1 := list[0].(map[string]any), list[1].(map[string]any)
	if k0["current"] != true || k0["emergency"] != false || k1["emergency"] != true || k1["label"] != "Hoster" {
		t.Fatalf("%v", list)
	}
	if strings.Contains(logs.String(), pw) {
		t.Fatal("password in log")
	}
	// the key in use is never removed
	decode(t, f.do(t, "DELETE", "/keys/"+strings.Repeat("a", 64), "", token), 409)
	decode(t, f.do(t, "DELETE", "/keys/"+strings.Repeat("a", 8), "", token), 409)
	decode(t, f.do(t, "DELETE", "/keys/"+strings.Repeat("f", 64), "", token), 404)
	if rec := f.do(t, "DELETE", "/keys/"+id, "", token); rec.Code != 204 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if len(fk.removed) != 1 {
		t.Fatalf("%v", fk.removed)
	}
	m = decode(t, f.get(t, "/keys"), 200)
	if len(m["keys"].([]any)) != 1 {
		t.Fatalf("%v", m)
	}
	// the record is gone, so the next create works again
	decode(t, f.do(t, "POST", "/keys", ``, token), 201)
}

func TestKeysPasswordsDiffer(t *testing.T) {
	a, _ := newKeyPassword()
	b, _ := newKeyPassword()
	if a == b || len(a) < 32 {
		t.Fatalf("%q %q", a, b)
	}
}

func TestKeysErrorsDoNotLeak(t *testing.T) {
	f, fk, logs := keysFixture(t)
	fk.addErr = errors.New("restic key add: exit status 1: RESTIC_PASSWORD=hunter2 rest:https://u:pw@h/x failed")
	rec := f.do(t, "POST", "/keys", `{}`, token)
	if rec.Code != 502 || strings.Contains(rec.Body.String(), "hunter2") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if s := logs.String(); strings.Contains(s, "hunter2") || strings.Contains(s, ":pw@") || strings.Contains(s, fk.added[0]) {
		t.Fatalf("log leaks: %s", s)
	}
}

func TestKeysLimitAndUnavailable(t *testing.T) {
	f, _, _ := keysFixture(t)
	for i := 0; i < maxEmergencyKeys; i++ {
		decode(t, f.do(t, "POST", "/keys", ``, token), 201)
	}
	decode(t, f.do(t, "POST", "/keys", ``, token), 409)

	g := newFixture(t) // no Keys configured
	decode(t, g.get(t, "/keys"), 501)
	decode(t, g.do(t, "POST", "/keys", ``, token), 501)
	decode(t, g.do(t, "DELETE", "/keys/"+strings.Repeat("a", 64), "", token), 501)
}
