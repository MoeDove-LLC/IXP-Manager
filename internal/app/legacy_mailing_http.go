package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/migrate"
)

func (a *App) legacyMailingActor(s *State, user Record, session Session, self bool) (Record, error) {
	if session.API {
		return nil, errors.New("mailing migration requires a browser session")
	}
	actor, err := a.mailingActor(s, user, session, self)
	if err != nil {
		return nil, err
	}
	// Bind the complete current browser session, including CSRF and member scope.
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, current := range a.sessions {
		if current.ID == session.ID && current.UserID == session.UserID && current.Generation == session.Generation && current.CSRF == session.CSRF && current.MemberID == session.MemberID && time.Now().Before(current.Expires) {
			return actor, nil
		}
	}
	return nil, errors.New("mailing migration session changed; sign in again")
}
func (a *App) legacyMailingRoutes(w http.ResponseWriter, r *http.Request, user Record, session Session) bool {
	path := r.URL.Path
	self := path == "/api/account/mailing-migrations" || strings.HasPrefix(path, "/api/account/mailing-migrations/")
	if !self && path != "/api/import/mailing-archive" && path != "/api/mailing-lists/legacy" && !strings.HasPrefix(path, "/api/mailing-lists/legacy/") {
		return false
	}
	if session.API || !self && str(user, "role") != "admin" {
		problem(w, 403, "administrator browser session required for source archives and mappings")
		return true
	}
	if self {
		a.legacyMailingMigrations(w, r, user, session)
		return true
	}
	if path == "/api/import/mailing-archive" {
		a.importLegacyMailingArchive(w, r, user, session)
		return true
	}
	if path == "/api/mailing-lists/legacy/mappings" && r.Method == "POST" {
		a.createLegacyMailingMapping(w, r, user, session)
		return true
	}
	if strings.HasPrefix(path, "/api/mailing-lists/legacy/mappings/") {
		parts := strings.Split(strings.TrimPrefix(path, "/api/mailing-lists/legacy/mappings/"), "/")
		if len(parts) != 2 || !identifier.MatchString(parts[0]) || parts[1] != "cancel" || r.Method != "POST" {
			problem(w, 404, "legacy mailing mapping action not found")
			return true
		}
		a.cancelLegacyMailingMapping(w, r, user, session, parts[0])
		return true
	}
	if r.Method != "GET" {
		problem(w, 405, "method not allowed")
		return true
	}
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	s := &a.store.state
	if _, err := a.legacyMailingActor(s, user, session, false); err != nil || a.store.closed {
		problem(w, 403, "legacy mailing authority changed")
		return true
	}
	if path == "/api/mailing-lists/legacy" {
		batches, mappings := []LegacyMailingBatch{}, []Record{}
		if s.LegacyMailing != nil {
			for _, entry := range s.LegacyMailing.Archives {
				batches = append(batches, entry.Batch)
			}
			for i := range s.LegacyMailing.Mappings {
				mappings = append(mappings, legacyMailingMappingView(s, &s.LegacyMailing.Mappings[i]))
			}
		}
		respond(w, 200, Record{"batches": batches, "mappings": mappings, "canManage": true})
		return true
	}
	checksum := strings.TrimPrefix(path, "/api/mailing-lists/legacy/archives/")
	entry := legacyMailingArchive(s, checksum)
	if !strings.HasPrefix(path, "/api/mailing-lists/legacy/archives/") || !digestPattern.MatchString(checksum) || entry == nil {
		problem(w, 404, "legacy mailing archive not found")
		return true
	}
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", `attachment; filename="mailing-sources-`+checksum+`.json"`)
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		respond(w, 200, entry.Archive)
	} else {
		respond(w, 200, Record{"batch": entry.Batch, "archive": entry.Archive, "readOnly": true})
	}
	return true
}
func (a *App) importLegacyMailingArchive(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if r.Method != "POST" {
		problem(w, 405, "method not allowed")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, migrate.MailingArchiveLimit)
	archive, err := migrate.ReadMailingArchive(r.Body)
	if err != nil {
		problem(w, 400, err.Error())
		return
	}
	raw, _ := json.Marshal(archive)
	var batch LegacyMailingBatch
	status := 409
	err = a.store.Update(func(s *State) error {
		actor, err := a.legacyMailingActor(s, user, session, false)
		if err != nil {
			status = 403
			return err
		}
		if legacyMailingArchive(s, archive.Checksum) != nil {
			return errors.New("mailing source archive already imported")
		}
		batch = LegacyMailingBatch{Checksum: archive.Checksum, ImportedAt: time.Now().UTC().Format(time.RFC3339Nano), ImportedBy: str(actor, "id"), Users: len(archive.Users), Choices: len(archive.Choices), Bytes: int64(len(raw))}
		state := legacyMailingState(s)
		state.Archives = append(state.Archives, LegacyMailingArchive{Batch: batch, Archive: *archive})
		if err := validateLegacyMailing(s); err != nil {
			return err
		}
		audit(s, str(actor, "id"), "import-mailing-sources", "mailing-lists", archive.Checksum)
		return nil
	})
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	respond(w, 200, Record{"batch": batch, "imported": true, "livePreferencesChanged": false, "externalApplied": false})
}
func (a *App) createLegacyMailingMapping(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	var body struct {
		ArchiveChecksum     string `json:"archiveChecksum"`
		SourceUserID        string `json:"sourceUserId"`
		TargetUserID        string `json:"targetUserId"`
		TargetUserCreatedAt string `json:"targetUserCreatedAt"`
		Lists               []struct {
			SourceKey     string `json:"sourceKey"`
			ListID        string `json:"listId"`
			ListCreatedAt string `json:"listCreatedAt"`
			ListRevision  uint64 `json:"listRevision"`
		} `json:"lists"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !digestPattern.MatchString(body.ArchiveChecksum) || !migrate.MailingArchiveUserID(body.SourceUserID) || !identifier.MatchString(body.TargetUserID) || !noteTimestamp(body.TargetUserCreatedAt) || len(body.Lists) == 0 || len(body.Lists) > maxMailingLists {
		problem(w, 400, "explicit source, current account and selected list entities are required")
		return
	}
	status := 409
	var result Record
	err := a.store.Update(func(s *State) error {
		actor, err := a.legacyMailingActor(s, user, session, false)
		if err != nil {
			status = 403
			return err
		}
		source := legacyMailingArchive(s, body.ArchiveChecksum)
		m := LegacyMailingMapping{ID: token(18), ArchiveChecksum: body.ArchiveChecksum, SourceUserID: body.SourceUserID, TargetUserID: body.TargetUserID, TargetUserCreatedAt: body.TargetUserCreatedAt, Lists: []LegacyMailingListBinding{}, Status: "pending", Revision: 1, CreatedBy: str(actor, "id"), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		m.UpdatedAt = m.CreatedAt
		target := legacyMailingCurrentUser(s, &m)
		if source == nil || legacyMailingUser(&source.Archive, body.SourceUserID) == nil || target == nil {
			return errors.New("source user or target account identity is unavailable")
		}
		for _, binding := range body.Lists {
			list := mailingList(s, binding.ListID)
			if !legacyMailingSourceKey(&source.Archive, body.SourceUserID, binding.SourceKey) || list == nil || list.CreatedAt != binding.ListCreatedAt || list.Revision != binding.ListRevision {
				return errors.New("source key or current list entity changed; reload before mapping")
			}
			p, _ := mailingPreference(s, list.ID, target)
			m.Lists = append(m.Lists, LegacyMailingListBinding{SourceKey: binding.SourceKey, ListID: list.ID, ListCreatedAt: list.CreatedAt, ListKey: list.Key, ListEmail: list.Email, PreferenceRevision: p.Revision, PreferenceDigest: legacyMailingPreferenceDigest(p)})
		}
		sort.Slice(m.Lists, func(i, j int) bool { return m.Lists[i].SourceKey < m.Lists[j].SourceKey })
		state := legacyMailingState(s)
		state.Mappings = append(state.Mappings, m)
		if err := validateLegacyMailing(s); err != nil {
			return err
		}
		audit(s, str(actor, "id"), "map-mailing-source-user", "users", m.TargetUserID)
		result = legacyMailingMappingView(s, &m)
		return nil
	})
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	respond(w, 201, Record{"mapping": result})
}
func (a *App) cancelLegacyMailingMapping(w http.ResponseWriter, r *http.Request, user Record, session Session, id string) {
	var body struct {
		Revision *uint64 `json:"revision"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Revision == nil || *body.Revision == 0 {
		problem(w, 400, "mapping revision required")
		return
	}
	status := 409
	var result Record
	err := a.store.Update(func(s *State) error {
		actor, err := a.legacyMailingActor(s, user, session, false)
		if err != nil {
			status = 403
			return err
		}
		m := legacyMailingMapping(s, id)
		if m == nil || m.Status != "pending" || m.Revision != *body.Revision || m.Revision >= maxMailingRevision {
			return errors.New("pending mapping changed; reload before cancelling")
		}
		m.Status, m.Review = "cancelled", nil
		m.Revision++
		m.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if err := validateLegacyMailing(s); err != nil {
			return err
		}
		audit(s, str(actor, "id"), "cancel-mailing-source-mapping", "users", m.TargetUserID)
		result = legacyMailingMappingView(s, m)
		return nil
	})
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	respond(w, 200, Record{"mapping": result})
}
