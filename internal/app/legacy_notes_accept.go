package app

import (
	"crypto/hmac"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/migrate"
)

func legacyNoteOwnMapping(s *State, actor Record, id string) *LegacyNoteMapping {
	m := legacyNoteMapping(s, id)
	if m == nil || m.TargetUserID != str(actor, "id") || m.TargetUserCreatedAt != str(actor, "createdAt") || !legacyNoteTargetCurrent(s, m) {
		return nil
	}
	return m
}
func legacyNoteSourceView(s *State, m *LegacyNoteMapping) Record {
	a := legacyNoteArchive(s, m.ArchiveChecksum)
	if a == nil {
		return nil
	}
	u := legacyNoteUser(&a.Archive, m.SourceUserID)
	preferences := []migrate.NoteArchivePreference{}
	for _, p := range a.Archive.Preferences {
		if p.LegacyUserID == m.SourceUserID {
			preferences = append(preferences, p)
		}
	}
	// The string prevents browser JSON numbers from rounding source int64 IDs.
	// It is a canonical selected-source representation, not original DB bytes.
	raw, _ := json.Marshal(Record{"user": u, "preferences": preferences, "sourceTimezone": a.Archive.SourceTimezone})
	return Record{"sourceUser": u, "sourcePreferences": preferences, "sourceTimezone": a.Archive.SourceTimezone, "sourceJSON": string(raw), "sourceJSONCanonical": true}
}
func (a *App) legacyNoteSessionBinding(session Session) string {
	return a.coreHMAC("legacy-note-review-session", Record{"id": session.ID, "user": session.UserID, "generation": session.Generation, "csrf": session.CSRF, "member": session.MemberID, "expires": session.Expires})
}
func (a *App) legacyNoteTokenHash(m *LegacyNoteMapping, value string) string {
	return a.coreHMAC("legacy-note-review-token", Record{"id": m.ID, "revision": m.Revision, "archiveChecksum": m.ArchiveChecksum, "token": value})
}
func (a *App) legacyNoteMigrations(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if r.URL.Path == "/api/account/note-migrations" && r.Method == "GET" {
		s := a.store.Snapshot()
		actor, err := a.legacyNotesActor(&s, user, session, false)
		if err != nil {
			problem(w, 401, "legacy note authority changed")
			return
		}
		items := []Record{}
		if s.LegacyNotes != nil {
			for i := range s.LegacyNotes.Mappings {
				m := &s.LegacyNotes.Mappings[i]
				if m.Status != "pending" || legacyNoteOwnMapping(&s, actor, m.ID) == nil {
					continue
				}
				view := legacyNoteSourceView(&s, m)
				if view == nil {
					problem(w, 409, "legacy note source unavailable")
					return
				}
				view["mapping"] = legacyNoteMappingView(&s, m)
				items = append(items, view)
			}
		}
		respond(w, 200, Record{"items": items, "preferences": a.notePreferenceView(&s, notePreferences(&s, actor)), "email": str(actor, "email")})
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/account/note-migrations/"), "/")
	if len(parts) != 2 || !identifier.MatchString(parts[0]) || r.Method != "POST" || (parts[1] != "review" && parts[1] != "accept") {
		problem(w, 404, "legacy note migration action not found")
		return
	}
	if parts[1] == "review" {
		a.reviewLegacyNoteMapping(w, r, user, session, parts[0])
	} else {
		a.acceptLegacyNoteMapping(w, r, user, session, parts[0])
	}
}

func (a *App) reviewLegacyNoteMapping(w http.ResponseWriter, r *http.Request, user Record, session Session, id string) {
	var body struct {
		Revision           *uint64             `json:"revision"`
		PreferenceRevision *uint64             `json:"preferenceRevision"`
		Mode               string              `json:"mode"`
		MemberWatches      []NoteEntityBinding `json:"memberWatches"`
		NoteWatches        []NoteEntityBinding `json:"noteWatches"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Mode == "" {
		body.Mode = "none"
	}
	selection := LegacyNoteSelection{Mode: body.Mode, MemberWatches: noteSortedBindings(body.MemberWatches), NoteWatches: noteSortedBindings(body.NoteWatches)}
	if body.Revision == nil || *body.Revision == 0 || body.PreferenceRevision == nil || *body.PreferenceRevision > maxNoteSequence || !legacyNoteSelectionValid(selection) {
		problem(w, 400, "current mapping and preference revisions with an explicit bounded selection are required")
		return
	}
	status := 409
	var result Record
	err := a.store.Update(func(s *State) error {
		actor, err := a.legacyNotesActor(s, user, session, false)
		if err != nil {
			status = 401
			return err
		}
		m := legacyNoteOwnMapping(s, actor, id)
		if m == nil {
			status = 404
			return errors.New("legacy note migration not found")
		}
		p := notePreferences(s, actor)
		if m.Status != "pending" || m.Revision != *body.Revision || m.Revision >= maxNoteSequence || p.Revision != *body.PreferenceRevision {
			return errors.New("mapping or current preferences changed; reload before reviewing")
		}
		if !legacyNoteSelectionCurrent(s, selection) {
			return errors.New("selected watch entity changed; reload before reviewing")
		}
		m.Revision++
		now := time.Now().UTC()
		tokenValue := token(32)
		m.UpdatedAt = now.Format(time.RFC3339Nano)
		m.Review = &LegacyNoteReview{MappingRevision: m.Revision, TokenHash: a.legacyNoteTokenHash(m, tokenValue), SessionBinding: a.legacyNoteSessionBinding(session), Generation: session.Generation, Email: str(actor, "email"), PreferenceRevision: p.Revision, PreferenceDigest: legacyNotePreferenceDigest(p), Selection: selection, CreatedAt: m.UpdatedAt, ExpiresAt: now.Add(legacyNoteReviewLifetime).Format(time.RFC3339Nano)}
		if err := validateLegacyNotes(s); err != nil {
			return err
		}
		result = legacyNoteSourceView(s, m)
		if result == nil {
			return errors.New("legacy note source unavailable")
		}
		after := p
		after.Mode, after.MemberWatches, after.NoteWatches = selection.Mode, selection.MemberWatches, selection.NoteWatches
		result["mapping"], result["reviewToken"], result["expiresAt"] = legacyNoteMappingView(s, m), tokenValue, m.Review.ExpiresAt
		result["before"], result["after"], result["deliveryEnabled"] = a.notePreferenceView(s, p), a.notePreferenceView(s, after), a.mailConfig.Enabled
		result["email"] = str(actor, "email")
		return nil
	})
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	respond(w, 200, result)
}

func (a *App) acceptLegacyNoteMapping(w http.ResponseWriter, r *http.Request, user Record, session Session, id string) {
	var body struct {
		Revision    *uint64 `json:"revision"`
		ReviewToken string  `json:"reviewToken"`
		Acknowledge bool    `json:"acknowledge"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Revision == nil || *body.Revision == 0 || len(body.ReviewToken) != 43 || !identifier.MatchString(body.ReviewToken) || !body.Acknowledge {
		problem(w, 400, "explicit acknowledgement and a current review token are required")
		return
	}
	status := 409
	var result Record
	err := a.store.Update(func(s *State) error {
		actor, err := a.legacyNotesActor(s, user, session, false)
		if err != nil {
			status = 401
			return err
		}
		m := legacyNoteOwnMapping(s, actor, id)
		if m == nil {
			status = 404
			return errors.New("legacy note migration not found")
		}
		review := m.Review
		if m.Status != "pending" || m.Revision != *body.Revision || m.Revision >= maxNoteSequence || review == nil || review.MappingRevision != m.Revision || !hmac.Equal([]byte(review.TokenHash), []byte(a.legacyNoteTokenHash(m, body.ReviewToken))) {
			return errors.New("review changed or was already consumed; review again")
		}
		created, e1 := time.Parse(time.RFC3339Nano, review.CreatedAt)
		expires, e2 := time.Parse(time.RFC3339Nano, review.ExpiresAt)
		now := time.Now().UTC()
		if e1 != nil || e2 != nil || now.Before(created) || !now.Before(expires) || review.SessionBinding != a.legacyNoteSessionBinding(session) || review.Generation != session.Generation || review.Email != str(actor, "email") {
			return errors.New("review expired or authenticated account/session changed; review again")
		}
		p := notePreferences(s, actor)
		if p.Revision != review.PreferenceRevision || legacyNotePreferenceDigest(p) != review.PreferenceDigest || !legacyNoteSelectionCurrent(s, review.Selection) {
			return errors.New("current preferences or selected watch entities changed; review again")
		}
		selection := review.Selection
		if p.Mode != selection.Mode || !slices.Equal(p.MemberWatches, selection.MemberWatches) || !slices.Equal(p.NoteWatches, selection.NoteWatches) {
			if p.Revision >= maxNoteSequence {
				return errors.New("note preference revision limit reached")
			}
			p.Mode, p.MemberWatches, p.NoteWatches = selection.Mode, selection.MemberWatches, selection.NoteWatches
			p.Revision++
			// Keep the current read marks. Legacy timestamps/cursors never become
			// current reading events, even when they occur in the source archive.
			if err := saveNotePreferences(s, p); err != nil {
				return err
			}
		}
		m.Status, m.Review = "accepted", nil
		m.Revision++
		m.AcceptedAt, m.UpdatedAt = now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)
		m.AcceptedPreferenceRevision, m.AcceptedSelection = p.Revision, &selection
		if err := validateLegacyNotes(s); err != nil {
			return err
		}
		audit(s, str(actor, "id"), "accept-note-preference-migration", "users", str(actor, "id"))
		result = Record{"mapping": legacyNoteMappingView(s, m), "preferences": a.notePreferenceView(s, p), "accepted": true, "mailQueued": false, "legacyReadCursorsApplied": false, "legacyBodiesApplied": false}
		return nil
	})
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	respond(w, 200, result)
}
