package offsite

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"ptero-enhanced-backup/gateway/internal/restic"
	"ptero-enhanced-backup/gateway/internal/store"
)

// Mirroring deletions to the secondary is the only destructive step of the
// offsite copy, so it is fenced in several ways:
//
//   - only copies that this gateway made itself (table offsite_copies) are forgotten,
//   - a snapshot must be missing in the primary for the grace period (default 48h),
//   - one run forgets at most 10% of the secondary's snapshots and never more than 20,
//   - a changed repository ID (primary or secondary) stops the step,
//   - an empty or unreadable primary list stops the step.
//
// A stopped step is released by hand: PEB_SECONDARY_FORGET_CONFIRM=<secondary
// repository ID> or the admin API (Service.ConfirmForget).

const (
	stateForgetBlock   = "forget_block"
	statePrimaryID     = "primary_repo_id"
	stateSecondaryID   = "secondary_repo_id"
	stateForgetConfirm = "forget_confirm"
	stateAdopted       = "copies_adopted"

	defaultForgetGrace = 48 * time.Hour
	forgetMaxAbs       = 20
)

// ForgetLimit is the most snapshots one run may forget: 10% of the snapshots
// on the secondary, at least 1 and at most 20.
func ForgetLimit(secondarySnapshots int) int {
	l := secondarySnapshots / 10
	if l < 1 {
		l = 1
	}
	if l > forgetMaxAbs {
		l = forgetMaxAbs
	}
	return l
}

func (s *Service) forgetGrace() time.Duration {
	switch {
	case s.Cfg.ForgetGrace < 0:
		return 0
	case s.Cfg.ForgetGrace == 0:
		return defaultForgetGrace
	}
	return s.Cfg.ForgetGrace
}

// ForgetPlan returns the secondary snapshots that are candidates for forgetting:
// copies made by this gateway (own) whose original no longer exists in the
// primary. Everything else on the secondary is never touched. An empty primary
// list while the secondary holds own copies is refused (the list may be wrong).
func ForgetPlan(prim, sec []restic.Snapshot, own map[string]store.OwnCopy) ([]string, error) {
	have := make(map[string]bool, len(prim))
	for _, p := range prim {
		have[p.ID] = true
	}
	var out []string
	copies := 0
	for _, sn := range sec {
		c, ok := own[sn.ID]
		if !ok {
			continue
		}
		copies++
		if !have[c.OriginalID] {
			out = append(out, sn.ID)
		}
	}
	if len(prim) == 0 && copies > 0 {
		return nil, fmt.Errorf("%w: the primary snapshot list is empty while the secondary holds %d copies", ErrUnsafe, copies)
	}
	return out, nil
}

// adoptLegacy takes over copies from before the bookkeeping existed: once, a
// secondary snapshot counts as ours if its original is a backup this gateway knows.
func (s *Service) adoptLegacy(ctx context.Context, sec []restic.Snapshot) {
	if v, _ := s.Store.State(ctx, stateAdopted); v != "" {
		return
	}
	own, err := s.Store.OwnCopies(ctx)
	if err != nil {
		s.Log.Warn("offsite: reading copy records failed", "err", s.scrub(err))
		return
	}
	n := 0
	for _, sn := range sec {
		if sn.Original == "" || own[sn.ID].SnapshotID != "" {
			continue
		}
		known, err := s.Store.HasSnapshot(ctx, sn.Original)
		if err != nil {
			s.Log.Warn("offsite: adopting copies failed", "err", s.scrub(err))
			return
		}
		if known {
			if err := s.Store.RecordCopy(ctx, sn.ID, sn.Original, s.now()); err != nil {
				s.Log.Warn("offsite: adopting copies failed", "err", s.scrub(err))
				return
			}
			n++
		}
	}
	s.save(stateAdopted, "1")
	s.Log.Info("offsite: took over earlier copies", "adopted", n)
}

// trackCopies records the secondary snapshots that appeared during a copy run
// (before = the list taken before the run) as copies made by this gateway. It
// also catches up with copies of earlier runs that were never recorded (a run
// that failed half way, a failed listing): a secondary snapshot counts as ours
// if its original is a backup this gateway knows. Best effort; it uses its own
// deadline when the run's context is already over.
func (s *Service) trackCopies(ctx context.Context, before []restic.Snapshot) {
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
	}
	after, err := s.Secondary.Snapshots(ctx)
	if err != nil {
		s.Log.Warn("offsite: listing the secondary after the copy failed, new copies are not tracked", "err", s.scrub(err))
		return
	}
	own, err := s.Store.OwnCopies(ctx)
	if err != nil {
		s.Log.Warn("offsite: reading copy records failed", "err", s.scrub(err))
		return
	}
	was := make(map[string]bool, len(before))
	for _, sn := range before {
		was[sn.ID] = true
	}
	for _, sn := range after {
		if sn.Original == "" || own[sn.ID].SnapshotID != "" {
			continue
		}
		if was[sn.ID] { // older than this run: ours only if the original is a known backup
			known, err := s.Store.HasSnapshot(ctx, sn.Original)
			if err != nil || !known {
				continue
			}
		}
		if err := s.Store.RecordCopy(ctx, sn.ID, sn.Original, s.now()); err != nil {
			s.Log.Warn("offsite: recording a copy failed", "err", s.scrub(err))
		}
	}
}

// baseline remembers the repository IDs on first contact (errors are not fatal here).
func (s *Service) baseline(ctx context.Context) {
	if id, err := s.Primary.RepoID(ctx); err == nil {
		if v, _ := s.Store.State(ctx, statePrimaryID); v == "" {
			s.save(statePrimaryID, id)
		}
	}
	if id, err := s.Secondary.RepoID(ctx); err == nil {
		if v, _ := s.Store.State(ctx, stateSecondaryID); v == "" {
			s.save(stateSecondaryID, id)
		}
	}
}

// ConfirmForget releases a stopped forget step once (admin API). The approval
// is bound to the current secondary repository ID and is used up by the next
// forget run that finishes.
func (s *Service) ConfirmForget(ctx context.Context) error {
	id, err := s.Secondary.RepoID(ctx)
	if err != nil {
		return fmt.Errorf("reading the secondary repository ID: %w", err)
	}
	return s.Store.SetState(ctx, stateForgetConfirm, id)
}

// confirmation tells whether the forget step is released for the secondary with ID sid.
func (s *Service) confirmation(ctx context.Context, sid string) (ok, viaEnv bool) {
	if s.Cfg.ForgetConfirm != "" && s.Cfg.ForgetConfirm == sid {
		return true, true
	}
	v, _ := s.Store.State(ctx, stateForgetConfirm)
	return v != "" && v == sid, false
}

// block records why the forget step stopped and returns the error for the job.
func (s *Service) block(format string, args ...any) error {
	msg := s.scrubString(fmt.Sprintf(format, args...))
	s.mu.Lock()
	s.forgetBlock = msg
	s.mu.Unlock()
	s.save(stateForgetBlock, msg)
	return fmt.Errorf("%w: %s", ErrUnsafe, msg)
}

func (s *Service) unblock() {
	s.mu.Lock()
	had := s.forgetBlock != ""
	s.forgetBlock = ""
	s.mu.Unlock()
	if had {
		s.save(stateForgetBlock, "")
	}
}

func approveHint(sid string) string {
	return fmt.Sprintf("to approve set PEB_SECONDARY_FORGET_CONFIRM=%s or call POST /admin/v1/offsite/forget-confirm", sid)
}

func (s *Service) forgetStep(ctx context.Context) (string, *int64, error) {
	s.load(ctx)
	prim, sec, err := s.snapshotLists(ctx)
	if err != nil {
		if ctx.Err() != nil { // shutdown or deadline, not a safety stop
			return "", nil, err
		}
		return "", nil, s.block("%v", err)
	}
	pid, err := s.Primary.RepoID(ctx)
	if err != nil {
		return "", nil, s.block("reading the primary repository ID failed: %v", err)
	}
	sid, err := s.Secondary.RepoID(ctx)
	if err != nil {
		return "", nil, s.block("reading the secondary repository ID failed: %v", err)
	}
	confirmed, viaEnv := s.confirmation(ctx, sid)
	overridden := false // the release was needed in this run

	// A swapped repository: nothing the stored records say can be trusted.
	var changed []string
	storedP, _ := s.Store.State(ctx, statePrimaryID)
	storedS, _ := s.Store.State(ctx, stateSecondaryID)
	if storedP != "" && storedP != pid {
		changed = append(changed, fmt.Sprintf("the primary repository ID changed (stored %s, now %s)", storedP, pid))
	}
	if storedS != "" && storedS != sid {
		changed = append(changed, fmt.Sprintf("the secondary repository ID changed (stored %s, now %s)", storedS, sid))
	}
	if len(changed) > 0 {
		if !confirmed {
			return "", nil, s.block("%s; %s", strings.Join(changed, "; "), approveHint(sid))
		}
		overridden = true
	}
	s.save(statePrimaryID, pid)
	s.save(stateSecondaryID, sid)

	own, err := s.Store.OwnCopies(ctx)
	if err != nil {
		return "", nil, s.block("reading the copy records failed: %v", err)
	}
	onSec := make(map[string]bool, len(sec))
	for _, sn := range sec {
		onSec[sn.ID] = true
	}
	var gone []string // records of copies that are no longer on the secondary
	for id := range own {
		if !onSec[id] {
			gone = append(gone, id)
			delete(own, id)
		}
	}
	if len(gone) > 0 {
		_ = s.Store.MarkForgotten(ctx, gone, s.now())
	}

	cand, err := ForgetPlan(prim, sec, own)
	if err != nil {
		return "", nil, s.block("%v", strings.TrimPrefix(err.Error(), ErrUnsafe.Error()+": "))
	}

	// Track since when the originals are gone; a returning original resets the clock.
	inPrim := make(map[string]bool, len(prim))
	for _, p := range prim {
		inPrim[p.ID] = true
	}
	now := s.now()
	for id, c := range own {
		switch {
		case inPrim[c.OriginalID] && !c.MissingSince.IsZero():
			_ = s.Store.SetMissingSince(ctx, id, time.Time{})
		case !inPrim[c.OriginalID] && c.MissingSince.IsZero():
			c.MissingSince = now
			own[id] = c
			_ = s.Store.SetMissingSince(ctx, id, now)
		}
	}

	grace := s.forgetGrace()
	var ready []string // candidates whose grace period is over
	for _, id := range cand {
		if now.Sub(own[id].MissingSince) >= grace {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)

	// The cap counts what would really be forgotten in this run; candidates
	// still in their grace period do not count yet.
	if limit := ForgetLimit(len(sec)); len(ready) > limit {
		if !confirmed {
			return "", nil, s.block("%d snapshots are missing in the primary, more than one run may forget (limit %d of %d on the secondary); nothing was forgotten; %s",
				len(ready), limit, len(sec), approveHint(sid))
		}
		overridden = true
	}

	s.unblock()
	if overridden && viaEnv {
		s.Log.Warn("offsite: the release PEB_SECONDARY_FORGET_CONFIRM was used; remove the variable again, it keeps the safety limits switched off")
	}
	if len(ready) == 0 {
		if n := len(cand); n > 0 {
			return fmt.Sprintf("nothing to forget yet, %d snapshots wait for the grace period (%s)", n, grace), nil, nil
		}
		return "nothing to forget", nil, nil
	}
	if err := s.Secondary.ForgetMany(ctx, ready); err != nil {
		return "", nil, err
	}
	if v, _ := s.Store.State(ctx, stateForgetConfirm); v != "" {
		_ = s.Store.SetState(ctx, stateForgetConfirm, "") // an approval from the admin API is used up by a run that forgot something
	}
	if err := s.Store.MarkForgotten(ctx, ready, s.now()); err != nil {
		s.Log.Warn("offsite: recording forgotten copies failed", "err", s.scrub(err))
	}
	msg := fmt.Sprintf("%d snapshots forgotten", len(ready))
	if waiting := len(cand) - len(ready); waiting > 0 {
		msg += fmt.Sprintf(", %d wait for the grace period", waiting)
	}
	if overridden {
		msg += " (released by hand)"
		if viaEnv {
			msg += "; remove PEB_SECONDARY_FORGET_CONFIRM again"
		}
	}
	return msg, nil, nil
}
