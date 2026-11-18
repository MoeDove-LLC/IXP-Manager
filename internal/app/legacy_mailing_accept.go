package app

import (
	"crypto/hmac"
	"errors"
	"net/http"
	"strings"
	"time"
)

func legacyMailingOwnMapping(s *State, user Record, id string) *LegacyMailingMapping {
	m := legacyMailingMapping(s, id)
	if m == nil || m.TargetUserID != str(user, "id") || m.TargetUserCreatedAt != str(user, "createdAt") || legacyMailingCurrentUser(s, m) == nil {
		return nil
	}
	return m
}
func (a *App) legacyMailingSessionBinding(session Session) string {
	return a.coreHMAC("legacy-mailing-session", Record{"id": session.ID, "user": session.UserID, "generation": session.Generation, "csrf": session.CSRF, "member": session.MemberID, "expires": session.Expires})
}
func (a *App) legacyMailingTokenHash(m *LegacyMailingMapping, value string) string {
	return a.coreHMAC("legacy-mailing-token", Record{"id": m.ID, "revision": m.Revision, "archiveChecksum": m.ArchiveChecksum, "token": value})
}
func (a *App) legacyMailingMigrations(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if r.URL.Path == "/api/account/mailing-migrations" && r.Method == "GET" {
		a.store.mu.RLock()
		defer a.store.mu.RUnlock()
		s := &a.store.state
		actor, err := a.legacyMailingActor(s, user, session, true)
		if err != nil || a.store.closed {
			problem(w, 403, "mailing migration authority changed")
			return
		}
		items := []Record{}
		if s.LegacyMailing != nil {
			for i := range s.LegacyMailing.Mappings {
				m := &s.LegacyMailing.Mappings[i]
				if m.Status == "pending" && legacyMailingOwnMapping(s, actor, m.ID) != nil {
					items = append(items, legacyMailingSourceView(s, m, actor))
				}
			}
		}
		email, _ := mailingEmail(str(actor, "email"))
		respond(w, 200, Record{"email": email, "items": items})
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/account/mailing-migrations/"), "/")
	if len(parts) != 2 || !identifier.MatchString(parts[0]) || r.Method != "POST" || (parts[1] != "review" && parts[1] != "accept") {
		problem(w, 404, "mailing migration action not found")
		return
	}
	if parts[1] == "review" {
		a.reviewLegacyMailing(w, r, user, session, parts[0])
	} else {
		a.acceptLegacyMailing(w, r, user, session, parts[0])
	}
}
func (a *App) reviewLegacyMailing(w http.ResponseWriter, r *http.Request, user Record, session Session, id string) {
	var body struct {
		Revision *uint64 `json:"revision"`
		Email    string  `json:"email"`
		Choices  []struct {
			SourceKey          string  `json:"sourceKey"`
			ListRevision       *uint64 `json:"listRevision"`
			PreferenceRevision *uint64 `json:"preferenceRevision"`
		} `json:"choices"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Revision == nil || *body.Revision == 0 || len(body.Choices) == 0 || len(body.Choices) > maxMailingLists {
		problem(w, 400, "current mapping revision, email and selected source choices are required")
		return
	}
	for _, choice := range body.Choices {
		if choice.ListRevision == nil || choice.PreferenceRevision == nil {
			problem(w, 400, "current list and preference revisions required")
			return
		}
	}
	status := 409
	var result Record
	err := a.store.Update(func(s *State) error {
		actor, err := a.legacyMailingActor(s, user, session, true)
		if err != nil {
			status = 403
			return err
		}
		m := legacyMailingOwnMapping(s, actor, id)
		if m == nil {
			status = 404
			return errors.New("mailing migration not found")
		}
		email, ok := mailingEmail(str(actor, "email"))
		if m.Status != "pending" || m.Revision != *body.Revision || m.Revision >= maxMailingRevision || !ok || email != body.Email {
			return errors.New("mapping or current email changed; reload before reviewing")
		}
		choices, views := []LegacyMailingSelection{}, []Record{}
		for _, choice := range body.Choices {
			binding := legacyMailingBinding(m, choice.SourceKey)
			if binding == nil {
				return errors.New("source list is not explicitly mapped")
			}
			view, available := legacyMailingChoice(s, m, actor, binding)
			if !available || view["listRevision"] != *choice.ListRevision || view["preferenceRevision"] != *choice.PreferenceRevision {
				return errors.New("selected source, list or preference is unavailable or changed; reload before reviewing")
			}
			choices = append(choices, LegacyMailingSelection{SourceKey: choice.SourceKey, ListRevision: *choice.ListRevision, PreferenceRevision: *choice.PreferenceRevision, Subscribed: view["desired"] == "subscribed"})
			views = append(views, view)
		}
		archive := legacyMailingArchive(s, m.ArchiveChecksum)
		if !legacyMailingSelectionsValid(&archive.Archive, m, choices) {
			return errors.New("selected choices must be unique and recognized")
		}
		legacyMailingSortChoices(choices)
		m.Revision++
		now := time.Now().UTC()
		m.UpdatedAt = now.Format(time.RFC3339Nano)
		tokenValue := token(32)
		m.Review = &LegacyMailingReview{MappingRevision: m.Revision, TokenHash: a.legacyMailingTokenHash(m, tokenValue), SessionBinding: a.legacyMailingSessionBinding(session), Email: email, Choices: choices, CreatedAt: m.UpdatedAt, ExpiresAt: now.Add(legacyMailingReviewLifetime).Format(time.RFC3339Nano)}
		if err := validateLegacyMailing(s); err != nil {
			return err
		}
		result = Record{"mapping": legacyMailingMappingView(s, m), "email": email, "choices": views, "reviewToken": tokenValue, "expiresAt": m.Review.ExpiresAt, "externalApplied": false}
		return nil
	})
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	respond(w, 200, result)
}
func (a *App) acceptLegacyMailing(w http.ResponseWriter, r *http.Request, user Record, session Session, id string) {
	var body struct {
		Revision    *uint64 `json:"revision"`
		ReviewToken string  `json:"reviewToken"`
		Acknowledge bool    `json:"acknowledge"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Revision == nil || *body.Revision == 0 || len(body.ReviewToken) != 43 || !identifier.MatchString(body.ReviewToken) || !body.Acknowledge {
		problem(w, 400, "current review token, revision and explicit acknowledgement required")
		return
	}
	status := 409
	var result Record
	err := a.store.Update(func(s *State) error {
		actor, err := a.legacyMailingActor(s, user, session, true)
		if err != nil {
			status = 403
			return err
		}
		m := legacyMailingOwnMapping(s, actor, id)
		if m == nil {
			status = 404
			return errors.New("mailing migration not found")
		}
		r := m.Review
		if m.Status != "pending" || m.Revision != *body.Revision || m.Revision >= maxMailingRevision || r == nil || r.MappingRevision != m.Revision || !hmac.Equal([]byte(r.TokenHash), []byte(a.legacyMailingTokenHash(m, body.ReviewToken))) {
			return errors.New("review changed or was consumed; review again")
		}
		email, ok := mailingEmail(str(actor, "email"))
		created, e1 := time.Parse(time.RFC3339Nano, r.CreatedAt)
		expires, e2 := time.Parse(time.RFC3339Nano, r.ExpiresAt)
		now := time.Now().UTC()
		if !ok || r.Email != email || r.SessionBinding != a.legacyMailingSessionBinding(session) || e1 != nil || e2 != nil || now.Before(created) || !now.Before(expires) {
			return errors.New("review expired or account/session changed; review again")
		}
		archive := legacyMailingArchive(s, m.ArchiveChecksum)
		if archive == nil || !legacyMailingSelectionsValid(&archive.Archive, m, r.Choices) {
			return errors.New("review source choices changed")
		}
		for _, choice := range r.Choices {
			b := legacyMailingBinding(m, choice.SourceKey)
			view, available := legacyMailingChoice(s, m, actor, b)
			if !available || view["listRevision"] != choice.ListRevision || view["preferenceRevision"] != choice.PreferenceRevision {
				return errors.New("current list or preference changed; migration cannot overwrite it")
			}
			list := legacyMailingCurrentList(s, b)
			if err := saveMailingPreference(s, *list, actor, choice.Subscribed, choice.PreferenceRevision); err != nil {
				return err
			}
		}
		m.Status = "accepted"
		m.AcceptedChoices = append([]LegacyMailingSelection(nil), r.Choices...)
		m.Review = nil
		m.Revision++
		m.AcceptedAt = now.Format(time.RFC3339Nano)
		m.UpdatedAt = m.AcceptedAt
		if err := validateLegacyMailing(s); err != nil {
			return err
		}
		audit(s, str(actor, "id"), "accept-mailing-migration", "users", str(actor, "id"))
		result = Record{"mapping": legacyMailingMappingView(s, m), "preferences": mailingSelfView(s, actor), "externalApplied": false}
		return nil
	})
	if err != nil {
		problem(w, status, err.Error())
		return
	}
	respond(w, 200, result)
}
