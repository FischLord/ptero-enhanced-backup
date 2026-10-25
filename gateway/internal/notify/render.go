package notify

import (
	"fmt"
	"strings"
)

// Languages.
const (
	LangEN = "en"
	LangDE = "de"
)

// Event keys.
const (
	EventBackupFailed      = "backup_failed"
	EventBackupAbandoned   = "backup_abandoned"
	EventMaintenanceFailed = "maintenance_failed"
	EventPruneSkipped      = "prune_skipped"
	EventTrashWarning      = "trash_warning"
	EventServerDeleted     = "server_deleted"
	EventOffsiteFailed     = "offsite_failed"
	EventTest              = "test"
)

// Params are the values an event text is built from.
type Params map[string]any

// Event is something worth telling the operator about. The text is built per language.
type Event struct {
	Key    string
	Params Params
}

func (p Params) s(k string) string { return fmt.Sprint(p[k]) }
func (p Params) i(k string) int {
	switch v := p[k].(type) {
	case int:
		return v
	case int64:
		return int(v)
	}
	return 0
}
func (p Params) f(k string) float64 {
	if v, ok := p[k].(float64); ok {
		return v
	}
	return 0
}
func (p Params) b(k string) bool { v, _ := p[k].(bool); return v }

type text struct {
	sev string
	en  func(p Params) (title, body string)
	de  func(p Params) (title, body string)
}

var texts = map[string]text{
	EventBackupAbandoned: {
		sev: SevWarning,
		en: func(p Params) (string, string) {
			return "PEB backup aborted", fmt.Sprintf("Backup %s of server %s: %s.", p.s("backup"), p.s("server"), p.s("reason"))
		},
		de: func(p Params) (string, string) {
			return "PEB Backup abgebrochen", fmt.Sprintf("Backup %s von Server %s: %s.", p.s("backup"), p.s("server"), p.s("reason"))
		},
	},
	EventBackupFailed: {
		sev: SevError,
		en: func(p Params) (string, string) {
			why := "processing failed"
			if p.b("timed_out") {
				why = "timeout"
			}
			return "PEB backup failed", fmt.Sprintf("Backup %s of server %s failed (%s). See the gateway log for details.", p.s("backup"), p.s("server"), why)
		},
		de: func(p Params) (string, string) {
			why := "Verarbeitung fehlgeschlagen"
			if p.b("timed_out") {
				why = "Zeitüberschreitung"
			}
			return "PEB Backup fehlgeschlagen", fmt.Sprintf("Backup %s von Server %s fehlgeschlagen (%s). Details im Gateway-Log.", p.s("backup"), p.s("server"), why)
		},
	},
	EventMaintenanceFailed: {
		sev: SevError,
		en: func(p Params) (string, string) {
			return "PEB maintenance failed", fmt.Sprintf("Maintenance job %q failed. See the gateway log for details.", p.s("job"))
		},
		de: func(p Params) (string, string) {
			return "PEB Wartung fehlgeschlagen", fmt.Sprintf("Wartungsjob %q ist fehlgeschlagen. Details im Gateway-Log.", p.s("job"))
		},
	},
	EventPruneSkipped: {
		sev: SevWarning,
		en: func(p Params) (string, string) {
			return "PEB prune keeps being skipped", fmt.Sprintf("The prune of the main repository was skipped %d times in a row because the repository was busy (long backups, restores or the offsite copy). No storage is freed without a prune.", p.i("count"))
		},
		de: func(p Params) (string, string) {
			return "PEB Prune wird uebersprungen", fmt.Sprintf("Der Prune des Hauptrepos wurde %d Mal in Folge uebersprungen, weil das Repository belegt war (lange Backups, Restores oder Offsite-Kopie). Ohne Prune wird kein Speicher freigegeben.", p.i("count"))
		},
	},
	EventTrashWarning: {
		sev: SevWarning,
		en: func(p Params) (string, string) {
			return "PEB trash will be emptied", fmt.Sprintf("At the next maintenance window %d backups (%.1f GB of original data) will be deleted for good. Until then you can restore them in the dashboard under Trash.", p.i("count"), p.f("gb"))
		},
		de: func(p Params) (string, string) {
			return "PEB Papierkorb wird geleert", fmt.Sprintf("Beim naechsten Wartungsfenster werden %d Backups (%.1f GB Originaldaten) endgueltig geloescht. Wiederherstellen geht bis dahin im Dashboard unter Papierkorb.", p.i("count"), p.f("gb"))
		},
	},
	EventServerDeleted: {
		sev: SevWarning,
		en: func(p Params) (string, string) {
			return "PEB server deleted", fmt.Sprintf("%d servers were deleted in the Panel (%s). Their %d backups are in the trash and will be removed for good after %d days.", p.i("servers"), p.s("names"), p.i("backups"), p.i("days"))
		},
		de: func(p Params) (string, string) {
			return "PEB Server geloescht", fmt.Sprintf("%d Server wurden im Panel geloescht (%s). Ihre %d Backups liegen im Papierkorb und werden nach %d Tagen endgueltig entfernt.", p.i("servers"), p.s("names"), p.i("backups"), p.i("days"))
		},
	},
	EventOffsiteFailed: {
		sev: SevError,
		en:  func(p Params) (string, string) { return "PEB offsite copy failed", p.s("detail") },
		de:  func(p Params) (string, string) { return "PEB Offsite-Kopie fehlgeschlagen", p.s("detail") },
	},
	EventTest: {
		sev: SevInfo,
		en: func(Params) (string, string) {
			return "PEB test message", "This is a test message from the PEB gateway. Notifications to this channel work."
		},
		de: func(Params) (string, string) {
			return "PEB Testnachricht", "Das ist eine Testnachricht vom PEB-Gateway. Benachrichtigungen an diesen Kanal funktionieren."
		},
	},
}

func normLang(l string) string {
	if strings.EqualFold(strings.TrimSpace(l), LangDE) {
		return LangDE
	}
	return LangEN
}

// SeverityOf returns the severity of an event key.
func SeverityOf(key string) string {
	if t, ok := texts[key]; ok {
		return t.sev
	}
	return SevWarning
}

// Render builds title and body in the given language ("de", anything else is English).
func Render(lang string, ev Event) (title, body string) {
	t, ok := texts[ev.Key]
	if !ok {
		return "PEB notification", ev.Key
	}
	p := ev.Params
	if p == nil {
		p = Params{}
	}
	if normLang(lang) == LangDE {
		return t.de(p)
	}
	return t.en(p)
}
