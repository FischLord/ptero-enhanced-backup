package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"ptero-enhanced-backup/gateway/internal/store"
)

const settingPasswordAck = "password-ack"

type passwordAck struct {
	Acknowledged   bool    `json:"acknowledged"`
	AcknowledgedAt *string `json:"acknowledged_at"`
	By             string  `json:"by"`
}

func (s *Server) loadPasswordAck(r *http.Request) (passwordAck, error) {
	v, at, err := s.Store.GetSetting(r.Context(), settingPasswordAck)
	if errors.Is(err, store.ErrNotFound) {
		return passwordAck{}, nil
	}
	if err != nil {
		return passwordAck{}, err
	}
	var stored struct {
		By string `json:"by"`
	}
	_ = json.Unmarshal([]byte(v), &stored)
	return passwordAck{Acknowledged: true, AcknowledgedAt: tstr(at), By: stored.By}, nil
}

// getPasswordAck: GET /settings/password-ack.
func (s *Server) getPasswordAck(w http.ResponseWriter, r *http.Request) {
	out, err := s.loadPasswordAck(r)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, 200, out)
}

// putPasswordAck: PUT /settings/password-ack {"acknowledged": bool, "by": "name"}.
// The state belongs to the gateway, not to a browser or panel user.
func (s *Server) putPasswordAck(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Acknowledged *bool  `json:"acknowledged"`
		By           string `json:"by"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil || in.Acknowledged == nil {
		writeErr(w, 400, "bad_request", `body must be {"acknowledged": true|false, "by": "name"}`)
		return
	}
	by := strings.Map(func(r rune) rune {
		if r < 32 {
			return -1
		}
		return r
	}, in.By)
	if rs := []rune(by); len(rs) > 100 {
		by = string(rs[:100])
	}
	if *in.Acknowledged {
		b, _ := json.Marshal(map[string]string{"by": by})
		if err := s.Store.SetSetting(r.Context(), settingPasswordAck, string(b), s.now()); err != nil {
			s.internal(w, err)
			return
		}
	} else if err := s.Store.DeleteSetting(r.Context(), settingPasswordAck); err != nil {
		s.internal(w, err)
		return
	}
	s.getPasswordAck(w, r)
}
