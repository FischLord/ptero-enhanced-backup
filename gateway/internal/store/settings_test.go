package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestSettingsAndEmergencyKeys(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, _, err := st.GetSetting(ctx, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	at := time.UnixMilli(1_700_000_000_000)
	if err := st.SetSetting(ctx, "x", `{"a":1}`, at); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, "x", `{"a":2}`, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	v, got, err := st.GetSetting(ctx, "x")
	if err != nil || v != `{"a":2}` || !got.Equal(at.Add(time.Second)) {
		t.Fatalf("%q %v %v", v, got, err)
	}
	if err := st.DeleteSetting(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.GetSetting(ctx, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := st.AddEmergencyKey(ctx, "k1", "hoster", at); err != nil {
		t.Fatal(err)
	}
	m, err := st.EmergencyKeys(ctx)
	if err != nil || m["k1"].Label != "hoster" || !m["k1"].CreatedAt.Equal(at) {
		t.Fatalf("%v %v", m, err)
	}
	if err := st.DeleteEmergencyKey(ctx, "k1"); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.EmergencyKeys(ctx); len(m) != 0 {
		t.Fatalf("%v", m)
	}
}
