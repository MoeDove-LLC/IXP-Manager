package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
)

func noteWatchViews(s *State, kind string, watches []NoteEntityBinding) []Record {
	items := []Record{}
	for _, binding := range watches {
		name := binding.ID
		current := noteEntityCurrent(s, kind, binding)
		if current {
			name = str(find(s, kind, binding.ID), "name")
		}
		items = append(items, Record{"id": binding.ID, "createdAt": binding.CreatedAt, "name": name, "stale": !current})
	}
	return items
}
func (a *App) notePreferenceView(s *State, p NotePreference) Record {
	return Record{"revision": p.Revision, "mode": p.Mode, "memberWatches": noteWatchViews(s, "members", p.MemberWatches), "noteWatches": noteWatchViews(s, "notes", p.NoteWatches), "deliveryEnabled": a.mailConfig.Enabled, "legacyPreferencesImported": false}
}
func noteSummary(s *State, actor Record) (Record, error) {
	if len(s.Resources["notes"]) > maxNoteVersions {
		return nil, errors.New("note summary capacity reached")
	}
	p := notePreferences(s, actor)
	reads := map[string]NoteReadMark{}
	for _, m := range p.Reads {
		reads[m.ID] = m
	}
	memberRecords := map[string]Record{}
	for _, m := range s.Resources["members"] {
		memberRecords[str(m, "id")] = m
	}
	items, members := []Record{}, []Record{}
	counts := map[string]int{}
	total := 0
	tuples := []any{str(actor, "id"), str(actor, "createdAt")}
	for _, n := range noteSortedRecords(s, "notes") {
		id, memberID := str(n, "id"), str(n, "memberId")
		v, checksum := noteVersion(s, n), noteChecksum(n)
		read, hasRead := reads[id]
		unread := !hasRead || read.CreatedAt != str(n, "createdAt") || read.Revision != v || read.Checksum != checksum
		if unread {
			total++
			counts[memberID]++
		}
		member := memberRecords[memberID]
		items = append(items, Record{"id": id, "name": str(n, "name"), "memberId": memberID, "memberName": str(member, "name"), "createdAt": str(n, "createdAt"), "updatedAt": str(n, "updatedAt"), "visibility": str(n, "visibility"), "revision": v, "unread": unread, "watched": noteWatched(p, noteEntity(n), []NoteEntityBinding{noteEntity(member)})})
		tuples = append(tuples, []any{id, str(n, "createdAt"), memberID, str(member, "createdAt"), v, checksum})
	}
	for _, m := range noteSortedRecords(s, "members") {
		members = append(members, Record{"id": str(m, "id"), "createdAt": str(m, "createdAt"), "name": str(m, "name"), "unreadCount": counts[str(m, "id")]})
		tuples = append(tuples, []string{str(m, "id"), str(m, "createdAt")})
	}
	raw, _ := json.Marshal(tuples)
	return Record{"revision": digest(string(raw)), "unreadCount": total, "items": items, "members": members, "legacyPreferencesImported": false}, nil
}

func noteWatchesAllowed(s *State, kind string, old, next []NoteEntityBinding) error {
	if len(next) > maxNoteWatches {
		return errors.New("at most 256 watches per category are allowed")
	}
	seen := map[string]bool{}
	for _, w := range next {
		if !validNoteEntity(w) || seen[w.ID] {
			return errors.New("watches must contain distinct valid entity bindings")
		}
		seen[w.ID] = true
		// Keeping an existing stale watch is allowed; it never grants a watch of
		// a newly created object with the same ID. This permits disabling mail
		// without forcing the user to clean every historical watch first.
		if !noteEntityCurrent(s, kind, w) && !slices.Contains(old, w) {
			return noteRevisionConflict()
		}
	}
	return nil
}
func (a *App) noteNotificationsRoutes(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if session.API || !noteStaff(user) {
		problem(w, 403, "staff browser session required")
		return
	}
	path := r.URL.Path
	if path != "/api/account/note-preferences" && path != "/api/account/note-summary" && path != "/api/account/note-read" {
		problem(w, 404, "note action not found")
		return
	}
	if r.Method == "GET" && (path == "/api/account/note-preferences" || path == "/api/account/note-summary") {
		s := a.store.Snapshot()
		actor, err := a.atlasActor(&s, user, session, "admin", "operator", "viewer")
		if err != nil {
			problem(w, 401, "note authority changed; sign in again")
			return
		}
		if path == "/api/account/note-preferences" {
			respond(w, 200, a.notePreferenceView(&s, notePreferences(&s, actor)))
			return
		}
		result, err := noteSummary(&s, actor)
		if err != nil {
			problem(w, 409, err.Error())
			return
		}
		respond(w, 200, result)
		return
	}
	if path == "/api/account/note-preferences" && r.Method == "PUT" {
		var body struct {
			Revision      *uint64             `json:"revision"`
			Mode          string              `json:"mode"`
			MemberWatches []NoteEntityBinding `json:"memberWatches"`
			NoteWatches   []NoteEntityBinding `json:"noteWatches"`
		}
		if !decode(w, r, &body) {
			return
		}
		if body.Revision == nil || *body.Revision > maxNoteSequence || !noteMode(body.Mode) || len(body.MemberWatches) > maxNoteWatches || len(body.NoteWatches) > maxNoteWatches {
			problem(w, 400, "valid preference revision, mode and bounded watches are required")
			return
		}
		var result Record
		status := 409
		err := a.store.Update(func(s *State) error {
			actor, err := a.atlasActor(s, user, session, "admin", "operator", "viewer")
			if err != nil {
				status = 401
				return err
			}
			pruneNoteState(s)
			p := notePreferences(s, actor)
			if p.Revision != *body.Revision {
				return noteRevisionConflict()
			}
			if err = noteWatchesAllowed(s, "members", p.MemberWatches, body.MemberWatches); err != nil {
				return err
			}
			if err = noteWatchesAllowed(s, "notes", p.NoteWatches, body.NoteWatches); err != nil {
				return err
			}
			nextMembers, nextNotes := noteSortedBindings(body.MemberWatches), noteSortedBindings(body.NoteWatches)
			if p.Mode != body.Mode || !slices.Equal(p.MemberWatches, nextMembers) || !slices.Equal(p.NoteWatches, nextNotes) {
				if p.Revision >= maxNoteSequence {
					return errors.New("note preference revision limit reached")
				}
				p.Mode, p.MemberWatches, p.NoteWatches = body.Mode, nextMembers, nextNotes
				p.Revision++
				if err = saveNotePreferences(s, p); err != nil {
					return err
				}
				audit(s, str(actor, "id"), "update-note-preferences", "users", str(actor, "id"))
			}
			result = a.notePreferenceView(s, p)
			return nil
		})
		if err != nil {
			problem(w, status, err.Error())
			return
		}
		respond(w, 200, result)
		return
	}
	if path == "/api/account/note-read" && r.Method == "POST" {
		var body struct {
			Revision string `json:"revision"`
			MemberID string `json:"memberId"`
		}
		if !decode(w, r, &body) {
			return
		}
		if !digestPattern.MatchString(body.Revision) || (body.MemberID != "" && !identifier.MatchString(body.MemberID)) {
			problem(w, 400, "a current note summary revision is required")
			return
		}
		var result Record
		status := 409
		err := a.store.Update(func(s *State) error {
			actor, err := a.atlasActor(s, user, session, "admin", "operator", "viewer")
			if err != nil {
				status = 401
				return err
			}
			pruneNoteState(s)
			current, err := noteSummary(s, actor)
			if err != nil {
				return err
			}
			if str(current, "revision") != body.Revision {
				return noteRevisionConflict()
			}
			if body.MemberID != "" && find(s, "members", body.MemberID) == nil {
				return errors.New("member no longer exists")
			}
			p := notePreferences(s, actor)
			marks := map[string]NoteReadMark{}
			for _, mark := range p.Reads {
				marks[mark.ID] = mark
			}
			for _, n := range s.Resources["notes"] {
				if body.MemberID == "" || str(n, "memberId") == body.MemberID {
					if !validNoteEntity(noteEntity(n)) {
						return errors.New("note entity identity unavailable")
					}
					marks[str(n, "id")] = NoteReadMark{NoteEntityBinding: noteEntity(n), Revision: noteVersion(s, n), Checksum: noteChecksum(n)}
				}
			}
			p.Reads = nil
			for _, n := range noteSortedRecords(s, "notes") {
				if mark, ok := marks[str(n, "id")]; ok {
					p.Reads = append(p.Reads, mark)
				}
			}
			if err = saveNotePreferences(s, p); err != nil {
				return err
			}
			audit(s, str(actor, "id"), "read-notes", "notes", body.MemberID)
			result, err = noteSummary(s, actor)
			return err
		})
		if err != nil {
			problem(w, status, err.Error())
			return
		}
		respond(w, 200, result)
		return
	}
	problem(w, 405, "method not allowed")
}
