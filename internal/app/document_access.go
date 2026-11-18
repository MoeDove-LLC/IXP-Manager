package app

import (
	"errors"
	"net/http"
)

func requireMutableDocument(s *State, id string) error {
	if legacyPatchAttachment(s, id) != nil || legacyDocstoreFile(s, id) != nil {
		return errors.New("legacy document snapshots are read-only; upload a separate new document for changes")
	}
	return nil
}

// Recheck live ownership and the authenticated principal after content I/O,
// before any bytes (including HEAD metadata) can be returned.
func (a *App) commitDocumentRead(r *http.Request, user Record, opened Document, action string, record bool) error {
	check := func(s *State) error {
		actor := user
		if str(user, "id") != "" {
			session, ok := r.Context().Value(documentSessionKey{}).(Session)
			if !ok {
				return errors.New("document authentication context unavailable")
			}
			var err error
			actor, err = a.atlasActor(s, user, session, "admin", "operator", "viewer", "member")
			if err != nil {
				return err
			}
		}
		current := documentByID(s, opened.ID)
		if current == nil || current.BlobID != opened.BlobID || current.Checksum != opened.Checksum || !documentReadable(s, *current, actor) {
			return errors.New("document permission, version or allocation changed")
		}
		if record {
			who := str(actor, "id")
			if who == "" {
				who = "public"
			}
			audit(s, who, action, "documents", opened.ID)
		}
		return nil
	}
	if record {
		return a.store.Update(check)
	}
	s := a.store.Snapshot()
	return check(&s)
}
