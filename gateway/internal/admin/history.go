package admin

import (
	"context"
	"errors"
	"net/http"
	"time"

	"ptero-enhanced-backup/gateway/internal/store"
)

type dayJSON struct {
	Date         string `json:"date"`
	BackupsOK    int64  `json:"backups_ok"`
	BackupsFail  int64  `json:"backups_failed"`
	LogicalBytes int64  `json:"logical_bytes"`
	DataAdded    int64  `json:"data_added"`
	StoredBytes  *int64 `json:"stored_bytes"`
}

func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	days, err := intParam(r.URL.Query(), "days", 30, 365)
	if err != nil || days < 1 {
		writeErr(w, 400, "bad_request", "days must be between 1 and 365")
		return
	}
	ctx, loc, now := r.Context(), s.loc(), s.now()
	today := now.In(loc)
	first := time.Date(today.Year(), today.Month(), today.Day()-(days-1), 0, 0, 0, 0, loc)
	series := make([]dayJSON, days)
	index := map[string]int{}
	for i := range series {
		d := time.Date(first.Year(), first.Month(), first.Day()+i, 0, 0, 0, 0, loc).Format("2006-01-02")
		series[i].Date = d
		index[d] = i
	}

	backups, err := s.Store.BackupsSince(ctx, first)
	if err != nil {
		s.internal(w, err)
		return
	}
	for _, b := range backups {
		t := b.FinishedAt
		if t.IsZero() {
			t = b.StartedAt
		}
		i, ok := index[t.In(loc).Format("2006-01-02")]
		if !ok {
			continue
		}
		if b.Status == store.StatusComplete {
			series[i].BackupsOK++
			series[i].LogicalBytes += b.UncompressedBytes
			series[i].DataAdded += b.DataAdded
		} else {
			series[i].BackupsFail++
		}
	}

	// stored_bytes: last snapshot of the day; days without a snapshot carry the previous value.
	stats, err := s.Store.RepoStatsSince(ctx, first)
	if err != nil {
		s.internal(w, err)
		return
	}
	measured := map[int]int64{}
	for _, st := range stats {
		if i, ok := index[st.Time.In(loc).Format("2006-01-02")]; ok {
			measured[i] = st.Stored()
		}
	}
	var carry *int64
	if prev, err := s.Store.RepoStatBefore(ctx, first); err == nil {
		v := prev.Stored()
		carry = &v
	} else if !errors.Is(err, store.ErrNotFound) {
		s.internal(w, err)
		return
	}
	for i := range series {
		if v, ok := measured[i]; ok {
			carry = &v
		}
		if carry != nil {
			v := *carry
			series[i].StoredBytes = &v
		}
	}

	out := map[string]any{"days": days, "series": series}
	if sp := s.space(ctx); sp != nil {
		out["forecast"] = s.forecast(now, measured, sp.total, sp.free)
	}
	writeJSON(w, 200, out)
}

// targetSpace is the room on the backup target: measured with statfs (exact) or
// derived from the configured capacity (estimate).
type targetSpace struct {
	total, free int64
	exact       bool
	stored      int64
}

// space returns the room on the backup target, nil when it cannot be told.
func (s *Server) space(ctx context.Context) *targetSpace {
	st, err := s.Store.LatestRepoStat(ctx)
	if err != nil {
		if s.Capacity > 0 {
			return &targetSpace{total: s.Capacity, free: s.Capacity}
		}
		return nil
	}
	if st.TargetTotal != nil && st.TargetFree != nil {
		return &targetSpace{total: *st.TargetTotal, free: *st.TargetFree, exact: true, stored: st.Stored()}
	}
	if s.Capacity > 0 {
		free := s.Capacity - st.Stored()
		if free < 0 {
			free = 0
		}
		return &targetSpace{total: s.Capacity, free: free, stored: st.Stored()}
	}
	return nil
}

// forecast fits a line through the measured daily sizes and projects the day the
// target runs out of space. Fields are null when the trend cannot tell.
func (s *Server) forecast(now time.Time, measured map[int]int64, capacity, free int64) map[string]any {
	f := map[string]any{"capacity_bytes": capacity, "free_bytes": free, "used_bytes": nil, "growth_bytes_per_day": nil, "days_until_full": nil, "full_at": nil}
	if len(measured) == 0 {
		return f
	}
	var n, sx, sy, sxx, sxy float64
	last, lastX := int64(0), -1
	for x, y := range measured {
		fx, fy := float64(x), float64(y)
		n++
		sx, sy, sxx, sxy = sx+fx, sy+fy, sxx+fx*fx, sxy+fx*fy
		if x > lastX {
			lastX, last = x, y
		}
	}
	f["used_bytes"] = last
	if free <= 0 {
		f["days_until_full"] = 0.0
		f["full_at"] = tstr(now)
	}
	den := n*sxx - sx*sx
	if n < 2 || den == 0 {
		return f
	}
	slope := (n*sxy - sx*sy) / den
	f["growth_bytes_per_day"] = slope
	if free > 0 && slope > 0 {
		d := float64(free) / slope
		f["days_until_full"] = d
		f["full_at"] = tstr(now.Add(time.Duration(d * 24 * float64(time.Hour))))
	}
	return f
}

// dailyStored returns the last stored size of each of the last days days (index 0 =
// oldest), only for days with a snapshot.
func (s *Server) dailyStored(ctx context.Context, days int) (map[int]int64, error) {
	loc, now := s.loc(), s.now()
	today := now.In(loc)
	first := time.Date(today.Year(), today.Month(), today.Day()-(days-1), 0, 0, 0, 0, loc)
	stats, err := s.Store.RepoStatsSince(ctx, first)
	if err != nil {
		return nil, err
	}
	out := map[int]int64{}
	for _, st := range stats {
		d := st.Time.In(loc)
		i := int(time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, loc).Sub(first).Round(time.Hour*24) / (time.Hour * 24))
		if i >= 0 && i < days {
			out[i] = st.Stored()
		}
	}
	return out, nil
}
