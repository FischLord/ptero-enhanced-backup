package admin

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"ptero-enhanced-backup/gateway/internal/notify"
)

// NotifyAdmin describes the notification channels and can test them.
type NotifyAdmin interface {
	Language() string
	Describe() []notify.Info
	Test(ctx context.Context) []notify.Result
}

// notifyTestInterval is the minimum gap between two test sends.
const notifyTestInterval = 10 * time.Second

func (s *Server) notifyInfo(w http.ResponseWriter, r *http.Request) {
	lang, ch := notify.LangEN, []notify.Info{}
	if s.NotifyAdmin != nil {
		lang, ch = s.NotifyAdmin.Language(), s.NotifyAdmin.Describe()
	}
	writeJSON(w, 200, map[string]any{"lang": lang, "channels": ch})
}

func (s *Server) notifyTest(w http.ResponseWriter, r *http.Request) {
	s.testMu.Lock()
	now := s.now()
	if wait := notifyTestInterval - now.Sub(s.testLast); !s.testLast.IsZero() && wait > 0 {
		s.testMu.Unlock()
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeErr(w, http.StatusTooManyRequests, "rate_limited", "test notifications can be sent once every 10 seconds")
		return
	}
	s.testLast = now
	s.testMu.Unlock()
	res := []notify.Result{}
	if s.NotifyAdmin != nil {
		res = s.NotifyAdmin.Test(r.Context())
	}
	writeJSON(w, 200, map[string]any{"results": res})
}
