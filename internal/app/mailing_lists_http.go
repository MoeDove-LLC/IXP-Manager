package app

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

func mailingExportRoute(path string) (id, legacyKey, selection string, plain bool, ok bool) {
	const prefix = "/api/mailing-lists/"
	if strings.HasPrefix(path, prefix) {
		parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
		if len(parts) == 2 && identifier.MatchString(parts[0]) && parts[1] == "export" {
			return parts[0], "", "", false, true
		}
	}
	const legacy = "/api/v4/mailing-list/"
	if strings.HasPrefix(path, legacy) {
		parts := strings.Split(strings.TrimPrefix(path, legacy), "/")
		if len(parts) >= 2 && (parts[0] == "subscribers" || parts[0] == "unsubscribed") {
			if len(parts) == 2 && mailingKey.MatchString(parts[1]) {
				return "", parts[1], parts[0], true, true
			}
			if len(parts) == 3 && parts[1] == "json" && mailingKey.MatchString(parts[2]) {
				return "", parts[2], parts[0], false, true
			}
		}
	}
	return "", "", "", false, false
}
func mailingAPIPath(path string) bool {
	return path == "/api/mailing-lists" || strings.HasPrefix(path, "/api/mailing-lists/") || path == "/api/account/mailing-lists" || strings.HasPrefix(path, "/api/account/mailing-lists/") || strings.HasPrefix(path, "/api/v4/mailing-list/")
}
func (a *App) mailingActor(s *State, user Record, session Session, self bool) (Record, error) {
	roles := []string{"admin"}
	if self {
		roles = []string{"admin", "operator", "viewer", "member"}
	}
	actor, err := a.atlasActor(s, user, session, roles...)
	if err != nil || str(actor, "createdAt") != str(user, "createdAt") || session.API && (self || !contains(session.Scopes, "mailing-lists:read")) {
		return nil, errors.New("mailing list authority changed; sign in again")
	}
	return actor, nil
}
func (a *App) mailingListsRoutes(w http.ResponseWriter, r *http.Request, user Record, session Session) bool {
	if !mailingAPIPath(r.URL.Path) {
		return false
	}
	self := strings.HasPrefix(r.URL.Path, "/api/account/mailing-lists")
	id, key, selection, plain, export := mailingExportRoute(r.URL.Path)
	if session.API && (!export || r.Method != "GET") || !self && str(user, "role") != "admin" {
		problem(w, 403, "mailing list configuration and export require an administrator; tokens can only export with mailing-lists:read")
		return true
	}
	if r.Method == "GET" {
		a.store.mu.RLock()
		defer a.store.mu.RUnlock()
		s := &a.store.state
		actor, err := a.mailingActor(s, user, session, self)
		if err != nil || a.store.closed {
			problem(w, 403, "mailing list access changed")
			return true
		}
		if self && r.URL.Path == "/api/account/mailing-lists" {
			respond(w, 200, mailingSelfView(s, actor))
			return true
		}
		if !self && r.URL.Path == "/api/mailing-lists" {
			items := []MailingList{}
			if s.MailingLists != nil {
				items = s.MailingLists.Lists
			}
			respond(w, 200, Record{"items": items})
			return true
		}
		if export {
			if key != "" && s.MailingLists != nil {
				for _, list := range s.MailingLists.Lists {
					if list.Key == key {
						id = list.ID
					}
				}
			}
			list := mailingList(s, id)
			if list == nil {
				problem(w, 404, "mailing list not found")
				return true
			}
			result, err := exportMailingList(s, *list, time.Now())
			if err != nil {
				problem(w, 409, err.Error())
				return true
			}
			if selection != "" {
				addresses := result.Subscribed
				if selection == "unsubscribed" {
					addresses = result.Unsubscribed
				}
				if plain {
					w.Header().Set("Content-Type", "text/plain; charset=utf-8")
					_, _ = w.Write([]byte(strings.Join(addresses, "\n") + "\n"))
				} else {
					respond(w, 200, addresses)
				}
				return true
			}
			respond(w, 200, result)
			return true
		}
		problem(w, 404, "mailing list action not found")
		return true
	}
	if self && r.Method == "PUT" {
		a.saveMailingChoice(w, r, user, session)
		return true
	}
	if !self && (r.Method == "POST" && r.URL.Path == "/api/mailing-lists" || r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/api/mailing-lists/")) {
		a.saveMailingDefinition(w, r, user, session)
		return true
	}
	problem(w, 405, "method not allowed")
	return true
}
func (a *App) saveMailingChoice(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	id := strings.TrimPrefix(r.URL.Path, "/api/account/mailing-lists/")
	if !identifier.MatchString(id) {
		problem(w, 404, "mailing list not found")
		return
	}
	var body struct {
		ListRevision       *uint64 `json:"listRevision"`
		PreferenceRevision *uint64 `json:"preferenceRevision"`
		Email              string  `json:"email"`
		Subscribed         *bool   `json:"subscribed"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.ListRevision == nil || body.PreferenceRevision == nil || body.Subscribed == nil {
		problem(w, 400, "current list and preference revisions, account email and explicit subscription choice are required")
		return
	}
	var result Record
	status := 400
	err := a.store.Update(func(s *State) error {
		actor, err := a.mailingActor(s, user, session, true)
		if err != nil {
			status = 403
			return err
		}
		list := mailingList(s, id)
		if list == nil {
			status = 404
			return errors.New("mailing list not found")
		}
		email, ok := mailingEmail(str(actor, "email"))
		if list.Revision != *body.ListRevision || !ok || body.Email != email {
			return errMailingConflict
		}
		before, _ := mailingPreference(s, id, actor)
		if err = saveMailingPreference(s, *list, actor, *body.Subscribed, *body.PreferenceRevision); err != nil {
			return err
		}
		after, _ := mailingPreference(s, id, actor)
		if after.Revision != before.Revision {
			audit(s, str(actor, "id"), "update-mailing-preference", "mailing-lists", id)
		}
		result = mailingSelfView(s, actor)
		return nil
	})
	if err != nil {
		if errors.Is(err, errMailingConflict) {
			status = 409
		}
		problem(w, status, err.Error())
		return
	}
	respond(w, 200, result)
}
func (a *App) saveMailingDefinition(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	var body struct {
		Revision    *uint64 `json:"revision"`
		Key         *string `json:"key"`
		Email       *string `json:"email"`
		Name        string  `json:"name"`
		Description string  `json:"description"`
		ArchiveURL  string  `json:"archiveUrl"`
		Enabled     *bool   `json:"enabled"`
	}
	if !decode(w, r, &body) {
		return
	}
	create := r.Method == "POST"
	if body.Enabled == nil || create && (body.Key == nil || body.Email == nil || body.Revision != nil) || !create && (body.Key != nil || body.Email != nil || body.Revision == nil) {
		problem(w, 400, "create requires key/email; updates require revision and cannot change the list key or email")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/mailing-lists/")
	if !create && !identifier.MatchString(id) {
		problem(w, 404, "mailing list not found")
		return
	}
	status := 400
	var result MailingList
	err := a.store.Update(func(s *State) error {
		actor, err := a.mailingActor(s, user, session, false)
		if err != nil {
			status = 403
			return err
		}
		m := mailingState(s)
		list := mailingList(s, id)
		if create {
			if len(m.Lists) >= maxMailingLists {
				return errors.New("mailing list capacity reached")
			}
			email, _ := mailingEmail(*body.Email)
			result = MailingList{ID: token(16), Key: *body.Key, Email: email, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
		} else {
			if list == nil {
				status = 404
				return errors.New("mailing list not found")
			}
			if list.Revision != *body.Revision {
				return errMailingConflict
			}
			result = *list
		}
		result.Name, result.Description, result.ArchiveURL, result.Enabled = strings.TrimSpace(body.Name), strings.TrimSpace(body.Description), strings.TrimSpace(body.ArchiveURL), *body.Enabled
		if err = validMailingDefinition(result); err != nil {
			return err
		}
		for _, other := range m.Lists {
			if other.ID != result.ID && (other.Key == result.Key || other.Email == result.Email) {
				return errors.New("mailing list key and address must each be unique, including disabled lists")
			}
		}
		if !create && result == *list {
			return nil
		}
		result.Revision, err = nextMailingRevision(m)
		if err != nil {
			return err
		}
		if create {
			m.Lists = append(m.Lists, result)
		} else {
			*list = result
		}
		audit(s, str(actor, "id"), "configure-mailing-list", "mailing-lists", result.ID)
		return validateMailingState(s)
	})
	if err != nil {
		if errors.Is(err, errMailingConflict) {
			status = 409
		}
		problem(w, status, err.Error())
		return
	}
	if create {
		respond(w, 201, result)
	} else {
		respond(w, 200, result)
	}
}
