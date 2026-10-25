package offsite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ptero-enhanced-backup/gateway/internal/store"
)

// A restic error that carries credentials in several shapes reaches neither the
// stored error, the job table nor the notification.
func TestErrorPathsScrubCredentials(t *testing.T) {
	e := newEnv(t)
	e.prim.set("a")
	e.sec.copyErr = errors.New("restic copy: exit 1: Fatal: unable to open s3:https://AKIAKEY:topsecret@s3.example.org/b: " +
		"AWS_SECRET_ACCESS_KEY=abc123 RESTIC_PASSWORD=pw9 GET /x?password=pw8&a=1")
	id, _ := e.st.InsertJob(context.Background(), JobCopy, "manual", store.JobQueued, time.Now())
	e.svc.copyRun(context.Background(), id)

	st, _ := e.svc.Status(context.Background())
	js, _ := e.st.Jobs(context.Background(), 10)
	all := []string{st.LastError, e.nt.msgs[0]}
	for _, j := range js {
		all = append(all, j.Message)
	}
	stored, _ := e.st.State(context.Background(), stateLastError)
	all = append(all, stored)
	for _, text := range all {
		for _, leak := range []string{"topsecret", "AKIAKEY", "abc123", "pw9", "pw8"} {
			if strings.Contains(text, leak) {
				t.Fatalf("%q leaked in %q", leak, text)
			}
		}
	}
	if !strings.Contains(st.LastError, "s3.example.org") {
		t.Fatalf("the useful part is gone: %q", st.LastError)
	}
}

func TestFinishJobScrubsMessage(t *testing.T) {
	e := newEnv(t)
	id, _ := e.st.InsertJob(context.Background(), JobCopy, "manual", store.JobQueued, time.Now())
	_ = e.st.FinishJob(context.Background(), id, store.JobFailed, "failed: sftp://u:pw@host/x password=zzz", nil, time.Now())
	js, _ := e.st.Jobs(context.Background(), 1)
	if strings.Contains(js[0].Message, "pw@") || strings.Contains(js[0].Message, "zzz") {
		t.Fatalf("%q", js[0].Message)
	}
}
