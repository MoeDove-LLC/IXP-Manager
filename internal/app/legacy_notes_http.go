package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/migrate"
)

func (a *App) legacyNotesActor(s *State, user Record, session Session, manage bool) (Record, error) {
	if session.API {
		return nil, errors.New("legacy note migration requires a browser session")
	}
	roles := []string{"admin", "operator", "viewer"}
	if manage {
		roles = []string{"admin"}
	}
	actor, err := a.atlasActor(s, user, session, roles...)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, current := range a.sessions {
		if current.ID == session.ID && current.UserID == session.UserID && current.Generation == session.Generation && current.CSRF == session.CSRF && current.MemberID == session.MemberID && time.Now().Before(current.Expires) {
			return actor, nil
		}
	}
	return nil, errors.New("legacy note migration session changed; sign in again")
}

func (a *App) legacyNotesRoutes(w http.ResponseWriter, r *http.Request, user Record, session Session) bool {
	path := r.URL.Path
	if path != "/api/import/note-archive" && path != "/api/notes/legacy" && !strings.HasPrefix(path, "/api/notes/legacy/") && path != "/api/account/note-migrations" && !strings.HasPrefix(path, "/api/account/note-migrations/") {
		return false
	}
	if session.API || !noteStaff(user) {
		problem(w, 403, "staff browser session required")
		return true
	}
	if path == "/api/import/note-archive" {
		a.importLegacyNoteArchive(w, r, user, session)
		return true
	}
	if path == "/api/account/note-migrations" || strings.HasPrefix(path, "/api/account/note-migrations/") {
		a.legacyNoteMigrations(w, r, user, session)
		return true
	}
	if path == "/api/notes/legacy/mappings" && r.Method == "POST" {
		a.createLegacyNoteMapping(w, r, user, session)
		return true
	}
	if strings.HasPrefix(path, "/api/notes/legacy/mappings/") {
		parts := strings.Split(strings.TrimPrefix(path, "/api/notes/legacy/mappings/"), "/")
		if len(parts) != 2 || !identifier.MatchString(parts[0]) || parts[1] != "cancel" || r.Method != "POST" {
			problem(w, 404, "legacy note mapping action not found")
			return true
		}
		a.cancelLegacyNoteMapping(w, r, user, session, parts[0])
		return true
	}
	if r.Method != "GET" {
		problem(w, 405, "method not allowed")
		return true
	}
	s := a.store.Snapshot()
	actor, err := a.legacyNotesActor(&s, user, session, false)
	if err != nil {
		problem(w, 401, "legacy note authority changed")
		return true
	}
	if path == "/api/notes/legacy" {
		batches, mappings := []LegacyNoteBatch{}, []Record{}
		if s.LegacyNotes != nil {
			for _, entry := range s.LegacyNotes.Archives {
				batches = append(batches, entry.Batch)
			}
			for i := range s.LegacyNotes.Mappings {
				mappings = append(mappings, legacyNoteMappingView(&s, &s.LegacyNotes.Mappings[i]))
			}
		}
		respond(w, 200, Record{"batches": batches, "mappings": mappings, "canManage": str(actor, "role") == "admin", "readOnlySources": true})
		return true
	}
	checksum := strings.TrimPrefix(path, "/api/notes/legacy/archives/")
	if !strings.HasPrefix(path, "/api/notes/legacy/archives/") || !digestPattern.MatchString(checksum) {
		problem(w, 404, "legacy note archive not found")
		return true
	}
	entry := legacyNoteArchive(&s, checksum)
	if entry == nil {
		problem(w, 404, "legacy note archive not found")
		return true
	}
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", `attachment; filename="note-sources-`+checksum+`.json"`)
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		respond(w, 200, entry.Archive)
		return true
	}
	respond(w, 200, Record{"batch": entry.Batch, "archive": entry.Archive, "readOnly": true})
	return true
}

func (a *App) importLegacyNoteArchive(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if r.Method != "POST" {
		problem(w, 405, "method not allowed")
		return
	}
	if str(user, "role") != "admin" {
		problem(w, 403, "administrator browser session required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, migrate.NoteArchiveLimit)
	archive, err := migrate.ReadNoteArchive(r.Body)
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	raw, err := json.Marshal(archive)
	if err != nil || int64(len(raw)) > migrate.NoteArchiveLimit {
		problem(w, 400, "legacy note archive exceeds canonical byte limit")
		return
	}
	var batch LegacyNoteBatch
	status := 409
	err = a.store.Update(func(s *State) error {
		actor, err := a.legacyNotesActor(s, user, session, true)
		if err != nil {
			status = 403
			return err
		}
		if legacyNoteArchive(s, archive.Checksum) != nil {
			return errors.New("this note source archive was already imported")
		}
		batch = LegacyNoteBatch{Checksum: archive.Checksum, ImportedAt: time.Now().UTC().Format(time.RFC3339Nano), ImportedBy: str(actor, "id"), SourceTimezone: archive.SourceTimezone, Notes: len(archive.Notes), Users: len(archive.Users), Preferences: len(archive.Preferences), Bytes: int64(len(raw))}
		l := legacyNotesState(s)
		l.Archives = append(l.Archives, LegacyNoteArchive{Batch: batch, Archive: *archive})
		if err := validateLegacyNotes(s); err != nil {
			return err
		}
		audit(s, str(actor, "id"), "import-note-sources", "notes", archive.Checksum)
		return nil
	})
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	respond(w, 200, Record{"batch": batch, "imported": true, "livePreferencesChanged": false, "mailQueued": false})
}

func (a *App) createLegacyNoteMapping(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if str(user, "role") != "admin" {
		problem(w, 403, "administrator browser session required")
		return
	}
	var body struct {
		ArchiveChecksum     string          `json:"archiveChecksum"`
		SourceUserID        json.RawMessage `json:"sourceUserId"`
		TargetUserID        string          `json:"targetUserId"`
		TargetUserCreatedAt string          `json:"targetUserCreatedAt"`
	}
	if !decode(w, r, &body) {
		return
	}
	sourceID := migrate.NoteArchiveSourceID(body.SourceUserID)
	if !digestPattern.MatchString(body.ArchiveChecksum) || sourceID == 0 || !identifier.MatchString(body.TargetUserID) || !noteTimestamp(body.TargetUserCreatedAt) {
		problem(w, 400, "explicit source and current user entity are required")
		return
	}
	status := 409
	var result Record
	err := a.store.Update(func(s *State) error {
		actor, err := a.legacyNotesActor(s, user, session, true)
		if err != nil {
			status = 403
			return err
		}
		source := legacyNoteArchive(s, body.ArchiveChecksum)
		target := find(s, "users", body.TargetUserID)
		if source == nil || legacyNoteUser(&source.Archive, sourceID) == nil || !noteStaff(target) || str(target, "createdAt") != body.TargetUserCreatedAt {
			return errors.New("source user or current target staff entity is unavailable")
		}
		l := legacyNotesState(s)
		for _, m := range l.Mappings {
			if m.ArchiveChecksum == body.ArchiveChecksum && m.SourceUserID == sourceID && m.Status != "cancelled" {
				return errors.New("source user already has a pending or accepted mapping")
			}
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		mapping := LegacyNoteMapping{ID: token(18), ArchiveChecksum: body.ArchiveChecksum, SourceUserID: sourceID, TargetUserID: body.TargetUserID, TargetUserCreatedAt: body.TargetUserCreatedAt, Status: "pending", Revision: 1, CreatedAt: now, CreatedBy: str(actor, "id"), UpdatedAt: now}
		l.Mappings = append(l.Mappings, mapping)
		if err := validateLegacyNotes(s); err != nil {
			return err
		}
		audit(s, str(actor, "id"), "map-note-source-user", "users", body.TargetUserID)
		result = legacyNoteMappingView(s, &mapping)
		return nil
	})
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	respond(w, 201, Record{"mapping": result})
}

func (a *App) cancelLegacyNoteMapping(w http.ResponseWriter, r *http.Request, user Record, session Session, id string) {
	if str(user, "role") != "admin" {
		problem(w, 403, "administrator browser session required")
		return
	}
	var body struct {
		Revision *uint64 `json:"revision"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Revision == nil || *body.Revision == 0 {
		problem(w, 400, "a mapping revision is required")
		return
	}
	status := 409
	var result Record
	err := a.store.Update(func(s *State) error {
		actor, err := a.legacyNotesActor(s, user, session, true)
		if err != nil {
			status = 403
			return err
		}
		m := legacyNoteMapping(s, id)
		if m == nil || m.Status != "pending" || m.Revision != *body.Revision || m.Revision >= maxNoteSequence {
			return errors.New("pending mapping changed; reload before cancelling")
		}
		m.Status, m.Review = "cancelled", nil
		m.Revision++
		m.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := validateLegacyNotes(s); err != nil {
			return err
		}
		audit(s, str(actor, "id"), "cancel-note-source-mapping", "users", m.TargetUserID)
		result = legacyNoteMappingView(s, m)
		return nil
	})
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	respond(w, 200, Record{"mapping": result})
}
