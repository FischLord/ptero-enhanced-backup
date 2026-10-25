package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"ptero-enhanced-backup/gateway/internal/store"
)

// Forgetter forgets one snapshot, waiting for the maintenance gate.
type Forgetter interface {
	Forget(ctx context.Context, snapshotID string) error
}

// Notifier receives short messages (ntfy). The title is ASCII.
type Notifier interface {
	Send(title, body string)
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

const trashPath = Prefix + "/trash"

func (s *Server) trashRoutes() {
	m := s.mux
	m.HandleFunc("GET "+trashPath, s.trashList)
	m.HandleFunc("POST "+trashPath+"/{uuid}/restore", s.trashRestore)
	m.HandleFunc("DELETE "+trashPath+"/{uuid}", s.trashPurge)
	m.HandleFunc("POST "+Prefix+"/servers/reconcile", s.serversReconcile)
	m.HandleFunc("POST "+Prefix+"/servers/{uuid}/deleted", s.serverDeleted)
}

type trashJSON struct {
	BackupUUID    string  `json:"backup_uuid"`
	ServerUUID    string  `json:"server_uuid"`
	Reason        string  `json:"reason"`
	LogicalBytes  int64   `json:"logical_bytes"`
	ReceivedBytes int64   `json:"received_bytes"`
	FinishedAt    *string `json:"finished_at"`
	TrashedAt     *string `json:"trashed_at"`
	PurgeAfter    *string `json:"purge_after"`
}

func (s *Server) trashList(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.Trash(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	out := make([]trashJSON, 0, len(items))
	var bytes int64
	for _, it := range items {
		bytes += it.UncompressedBytes
		out = append(out, trashJSON{
			BackupUUID: it.BackupUUID, ServerUUID: it.ServerUUID, Reason: it.Reason,
			LogicalBytes: it.UncompressedBytes, ReceivedBytes: it.CompressedBytes,
			FinishedAt: tstr(it.FinishedAt), TrashedAt: tstr(it.TrashedAt), PurgeAfter: tstr(it.PurgeAfter),
		})
	}
	writeJSON(w, 200, map[string]any{"total": len(out), "logical_bytes": bytes, "items": out})
}

func (s *Server) trashItem(w http.ResponseWriter, r *http.Request) *store.TrashItem {
	it, err := s.Store.TrashByUUID(r.Context(), r.PathValue("uuid"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, 404, "not_found", "no such backup in the trash")
	case err != nil:
		s.internal(w, err)
	default:
		return it
	}
	return nil
}

func (s *Server) trashRestore(w http.ResponseWriter, r *http.Request) {
	it := s.trashItem(w, r)
	if it == nil {
		return
	}
	if err := s.Store.RestoreFromTrash(r.Context(), it.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, 404, "not_found", "no such backup in the trash")
			return
		}
		s.internal(w, err)
		return
	}
	s.Log.Info("backup restored from the trash", "backup", it.BackupUUID, "server", it.ServerUUID)
	writeJSON(w, 200, map[string]any{"backup_uuid": it.BackupUUID, "server_uuid": it.ServerUUID, "status": store.StatusComplete})
}

func (s *Server) trashPurge(w http.ResponseWriter, r *http.Request) {
	it := s.trashItem(w, r)
	if it == nil {
		return
	}
	ctx := r.Context()
	// Claim the item first: a restore or a second purge that comes later finds it gone (409), and the
	// night job cannot take it twice.
	if err := s.Store.BeginPurge(ctx, it.ID, nil); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, 409, "conflict", "the backup was restored or purged meanwhile")
			return
		}
		s.internal(w, err)
		return
	}
	if it.SnapshotID != "" {
		if s.Forgetter == nil {
			_ = s.Store.AbortPurge(context.Background(), it.ID)
			s.internal(w, errors.New("no forgetter configured"))
			return
		}
		// Forget waits while maintenance holds the repository; ask the client to retry then.
		fctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := s.Forgetter.Forget(fctx, it.SnapshotID)
		cancel()
		if err != nil {
			if aerr := s.Store.AbortPurge(context.Background(), it.ID); aerr != nil {
				s.Log.Error("could not put the backup back into the trash", "backup", it.BackupUUID, "err", aerr)
			}
			if ctx.Err() == nil && errors.Is(fctx.Err(), context.DeadlineExceeded) {
				w.Header().Set("Retry-After", "60")
				writeErr(w, 503, "busy", "the repository is in maintenance, retry later")
				return
			}
			s.internal(w, err)
			return
		}
	}
	if err := s.Store.FinishPurge(context.Background(), it.ID); err != nil {
		s.internal(w, err)
		return
	}
	s.Log.Info("backup purged from the trash", "backup", it.BackupUUID, "server", it.ServerUUID)
	writeJSON(w, 200, map[string]any{"backup_uuid": it.BackupUUID, "purged": true})
}

// ---- deleted servers ----

// markServerDeleted trashes the backups of a server the panel no longer has.
func (s *Server) markServerDeleted(ctx context.Context, uuid string) (moved int, fresh bool, d store.DeletedServer, err error) {
	moved, fresh, d, err = s.Store.MarkServerDeleted(ctx, uuid, s.now(), s.OrphanRetention)
	if err != nil {
		return
	}
	if moved > 0 {
		s.Log.Info("server deleted in the panel, backups moved to the trash", "server", uuid, "backups", moved, "purge_after", d.PurgeAfter)
	}
	return
}

func (s *Server) notifyServers(moved map[string]int, retention time.Duration) {
	if s.Notify == nil || len(moved) == 0 {
		return
	}
	var servers, backups int
	var names []string
	for u, n := range moved {
		if n > 0 {
			servers++
			backups += n
			names = append(names, u)
		}
	}
	if servers == 0 {
		return
	}
	sort.Strings(names)
	if len(names) > 3 {
		names = append(names[:3], "...")
	}
	days := int(retention.Hours() / 24)
	s.Notify.Send("PEB Server geloescht", fmt.Sprintf(
		"%d Server wurden im Panel geloescht (%s). Ihre %d Backups liegen im Papierkorb und werden nach %d Tagen endgueltig entfernt.",
		servers, strings.Join(names, ", "), backups, days))
}

func (s *Server) serverDeleted(w http.ResponseWriter, r *http.Request) {
	uuid := strings.ToLower(r.PathValue("uuid"))
	if !uuidRe.MatchString(uuid) {
		writeErr(w, 400, "bad_request", "server uuid must be a UUID")
		return
	}
	moved, fresh, d, err := s.markServerDeleted(r.Context(), uuid)
	if err != nil {
		s.internal(w, err)
		return
	}
	s.notifyServers(map[string]int{uuid: moved}, s.OrphanRetention)
	writeJSON(w, 200, map[string]any{
		"server_uuid": uuid, "newly_marked": fresh, "trashed": moved,
		"deleted_at": tstr(d.DeletedAt), "purge_after": tstr(d.PurgeAfter),
	})
}

// serversReconcile compares the server UUIDs of the panel with the gateway's.
//
// Safety nets: an empty list never marks anything, and a list that shares no
// UUID with the (non-empty) gateway side is refused (wrong panel, empty
// database). Marking only moves backups into the trash, which is reversible.
func (s *Server) serversReconcile(w http.ResponseWriter, r *http.Request) {
	if s.ReconcileOff {
		writeJSON(w, 200, map[string]any{"skipped": "disabled", "marked_deleted": []string{}, "restored": []string{}, "trashed": 0})
		return
	}
	var body struct {
		ServerUUIDs []string `json:"server_uuids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&body); err != nil {
		writeErr(w, 400, "bad_request", "body must be JSON like {\"server_uuids\":[\"...\"]}")
		return
	}
	panel := map[string]bool{}
	for _, u := range body.ServerUUIDs {
		u = strings.ToLower(strings.TrimSpace(u))
		if !uuidRe.MatchString(u) {
			writeErr(w, 400, "bad_request", "server_uuids must contain UUIDs only")
			return
		}
		panel[u] = true
	}
	res := map[string]any{"skipped": nil, "marked_deleted": []string{}, "restored": []string{}, "trashed": 0}
	if len(panel) == 0 {
		res["skipped"] = "empty_list"
		writeJSON(w, 200, res)
		return
	}
	ctx := r.Context()
	active, err := s.Store.ActiveServers(ctx)
	if err != nil {
		s.internal(w, err)
		return
	}
	deleted, err := s.Store.DeletedServers(ctx)
	if err != nil {
		s.internal(w, err)
		return
	}
	var missing []string
	common := 0
	var absentMarked []string // marked before and still gone: late backups of them are trashed again
	for _, u := range active {
		if panel[u] {
			common++
		} else {
			missing = append(missing, u)
		}
	}
	for _, d := range deleted {
		if panel[d.ServerUUID] {
			common++
		} else {
			absentMarked = append(absentMarked, d.ServerUUID)
		}
	}
	if common == 0 && len(active)+len(deleted) > 0 {
		res["skipped"] = "no_overlap"
		writeJSON(w, 200, res)
		return
	}
	marked, restored := []string{}, []string{}
	movedBy := map[string]int{}
	total := 0
	for _, u := range append(missing, absentMarked...) {
		moved, fresh, _, err := s.markServerDeleted(ctx, u)
		if err != nil {
			s.internal(w, err)
			return
		}
		if !fresh && moved == 0 {
			continue
		}
		marked = append(marked, u)
		movedBy[u] = moved
		total += moved
	}
	for _, d := range deleted {
		if !panel[d.ServerUUID] {
			continue
		}
		was, n, err := s.Store.UnmarkServerDeleted(ctx, d.ServerUUID)
		if err != nil {
			s.internal(w, err)
			return
		}
		if was {
			s.Log.Info("server is back in the panel, backups restored from the trash", "server", d.ServerUUID, "backups", n)
			restored = append(restored, d.ServerUUID)
		}
	}
	s.notifyServers(movedBy, s.OrphanRetention)
	res["marked_deleted"], res["restored"], res["trashed"] = marked, restored, total
	writeJSON(w, 200, res)
}
