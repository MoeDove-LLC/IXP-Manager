package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/notify"
)

const maxNotePreferences = 1000
const maxNoteWatches = 256
const maxNoteReadMarks = 50000
const maxNoteVersions = 10000
const maxNoteSequence uint64 = 9007199254740991

type NoteEntityBinding struct {
	ID        string `json:"id"`
	CreatedAt string `json:"createdAt"`
}
type NoteReadMark struct {
	NoteEntityBinding
	Revision uint64 `json:"revision"`
	Checksum string `json:"checksum"`
}
type NotePreference struct {
	UserID        string              `json:"userId"`
	UserCreatedAt string              `json:"userCreatedAt"`
	Revision      uint64              `json:"revision"`
	Mode          string              `json:"mode"`
	MemberWatches []NoteEntityBinding `json:"memberWatches"`
	NoteWatches   []NoteEntityBinding `json:"noteWatches"`
	Reads         []NoteReadMark      `json:"reads,omitempty"`
}
type NoteVersion struct {
	CreatedAt string `json:"createdAt"`
	Revision  uint64 `json:"revision"`
}
type NoteMailBinding struct {
	OutboxID           string              `json:"outboxId"`
	Key                string              `json:"key"`
	UserID             string              `json:"userId"`
	UserCreatedAt      string              `json:"userCreatedAt"`
	Generation         uint64              `json:"generation"`
	PreferenceRevision uint64              `json:"preferenceRevision"`
	Email              string              `json:"email"`
	Note               NoteEntityBinding   `json:"note"`
	Member             NoteEntityBinding   `json:"member"`
	WatchMembers       []NoteEntityBinding `json:"watchMembers"`
	Deleted            bool                `json:"deleted"`
}
type NoteNotificationState struct {
	Version     int                    `json:"version"`
	Sequence    uint64                 `json:"sequence"`
	Preferences []NotePreference       `json:"preferences,omitempty"`
	Notes       map[string]NoteVersion `json:"notes,omitempty"`
	Mail        []NoteMailBinding      `json:"mail,omitempty"`
}

func noteState(s *State) *NoteNotificationState {
	if s.NoteNotifications == nil {
		s.NoteNotifications = &NoteNotificationState{Version: 1, Notes: map[string]NoteVersion{}}
	}
	if s.NoteNotifications.Notes == nil {
		s.NoteNotifications.Notes = map[string]NoteVersion{}
	}
	return s.NoteNotifications
}
func noteStaff(user Record) bool {
	return contains([]string{"admin", "operator", "viewer"}, str(user, "role"))
}
func noteTimestamp(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}
func validNoteEntity(v NoteEntityBinding) bool {
	return identifier.MatchString(v.ID) && noteTimestamp(v.CreatedAt)
}
func noteEntity(record Record) NoteEntityBinding {
	return NoteEntityBinding{ID: str(record, "id"), CreatedAt: str(record, "createdAt")}
}
func noteEntityCurrent(s *State, kind string, v NoteEntityBinding) bool {
	r := find(s, kind, v.ID)
	return validNoteEntity(v) && r != nil && str(r, "createdAt") == v.CreatedAt
}
func noteMode(mode string) bool { return mode == "none" || mode == "watched" || mode == "all" }
func notePreferences(s *State, user Record) NotePreference {
	if s.NoteNotifications != nil {
		for _, p := range s.NoteNotifications.Preferences {
			if p.UserID == str(user, "id") && p.UserCreatedAt == str(user, "createdAt") {
				return p
			}
		}
	}
	return NotePreference{UserID: str(user, "id"), UserCreatedAt: str(user, "createdAt"), Mode: "none", MemberWatches: []NoteEntityBinding{}, NoteWatches: []NoteEntityBinding{}}
}
func saveNotePreferences(s *State, p NotePreference) error {
	l := noteState(s)
	for i, old := range l.Preferences {
		if old.UserID == p.UserID {
			l.Preferences[i] = p
			return validateNoteNotifications(s)
		}
	}
	if len(l.Preferences) >= maxNotePreferences {
		return errors.New("note preference capacity reached")
	}
	l.Preferences = append(l.Preferences, p)
	return validateNoteNotifications(s)
}
func noteVersion(s *State, n Record) uint64 {
	if s.NoteNotifications != nil {
		if v, ok := s.NoteNotifications.Notes[str(n, "id")]; ok && v.CreatedAt == str(n, "createdAt") {
			return v.Revision
		}
	}
	return 0 // Imported inventory has no fabricated change event.
}
func noteChecksum(n Record) string {
	raw, _ := json.Marshal([]string{str(n, "id"), str(n, "createdAt"), str(n, "memberId"), str(n, "name"), str(n, "body"), str(n, "visibility")})
	return digest(string(raw))
}
func noteSemanticEqual(old, next Record) bool {
	if old == nil || next == nil {
		return old == nil && next == nil
	}
	for _, key := range []string{"name", "body", "memberId", "visibility"} {
		if str(old, key) != str(next, key) {
			return false
		}
	}
	return true
}
func noteWatched(p NotePreference, note NoteEntityBinding, members []NoteEntityBinding) bool {
	if p.Mode == "none" {
		return false
	}
	if p.Mode == "all" {
		return true
	}
	for _, w := range p.NoteWatches {
		if w == note {
			return true
		}
	}
	for _, w := range p.MemberWatches {
		for _, member := range members {
			if w == member {
				return true
			}
		}
	}
	return false
}
func noteRead(p NotePreference, n Record, revision uint64) bool {
	for _, mark := range p.Reads {
		if mark.ID == str(n, "id") && mark.CreatedAt == str(n, "createdAt") && mark.Revision == revision && mark.Checksum == noteChecksum(n) {
			return true
		}
	}
	return false
}
func noteMailBinding(s *State, id string) *NoteMailBinding {
	if s.NoteNotifications != nil {
		for i := range s.NoteNotifications.Mail {
			if s.NoteNotifications.Mail[i].OutboxID == id {
				return &s.NoteNotifications.Mail[i]
			}
		}
	}
	return nil
}
func pruneNoteState(s *State) {
	if s.NoteNotifications == nil {
		return
	}
	l := s.NoteNotifications
	current := map[string]Record{}
	for _, n := range s.Resources["notes"] {
		current[str(n, "id")] = n
	}
	for id, v := range l.Notes {
		if n := current[id]; n == nil || str(n, "createdAt") != v.CreatedAt {
			delete(l.Notes, id)
		}
	}
	for i := range l.Preferences {
		p := &l.Preferences[i]
		out := p.Reads[:0]
		for _, r := range p.Reads {
			if n := current[r.ID]; n != nil && str(n, "createdAt") == r.CreatedAt {
				out = append(out, r)
			}
		}
		p.Reads = out
	}
	pruneNoteMailBindings(s)
}

// Store.Update invokes this after every successful transaction, including
// cleanup and unrelated outbox enqueue operations that may prune old entries.
func pruneNoteMailBindings(s *State) {
	if s.NoteNotifications == nil {
		return
	}
	l := s.NoteNotifications
	entries := map[string]bool{}
	for _, e := range s.Outbox.Entries {
		entries[e.ID] = true
	}
	mail := l.Mail[:0]
	for _, b := range l.Mail {
		if entries[b.OutboxID] {
			mail = append(mail, b)
		}
	}
	l.Mail = mail
}
func validateNoteNotifications(s *State) error {
	l := s.NoteNotifications
	if l == nil {
		return nil
	}
	if l.Version != 1 || l.Sequence > maxNoteSequence || len(l.Preferences) > maxNotePreferences || len(l.Notes) > maxNoteVersions || len(l.Mail) > notify.MaxEntries {
		return errors.New("invalid bounded note notification state")
	}
	users := map[string]bool{}
	reads := 0
	checkWatches := func(values []NoteEntityBinding) bool {
		if len(values) > maxNoteWatches {
			return false
		}
		seen := map[string]bool{}
		for _, v := range values {
			if !validNoteEntity(v) || seen[v.ID] {
				return false
			}
			seen[v.ID] = true
		}
		return true
	}
	for _, p := range l.Preferences {
		if !identifier.MatchString(p.UserID) || !noteTimestamp(p.UserCreatedAt) || users[p.UserID] || !noteMode(p.Mode) || p.Revision > maxNoteSequence || !checkWatches(p.MemberWatches) || !checkWatches(p.NoteWatches) {
			return errors.New("invalid note preference or entity binding")
		}
		users[p.UserID] = true
		marks := map[string]bool{}
		for _, m := range p.Reads {
			if !validNoteEntity(m.NoteEntityBinding) || !digestPattern.MatchString(m.Checksum) || m.Revision > l.Sequence || marks[m.ID] {
				return errors.New("invalid note read cursor")
			}
			marks[m.ID] = true
			reads++
		}
	}
	if reads > maxNoteReadMarks {
		return errors.New("note read cursor capacity reached")
	}
	for id, v := range l.Notes {
		if !identifier.MatchString(id) || !noteTimestamp(v.CreatedAt) || v.Revision == 0 || v.Revision > l.Sequence {
			return errors.New("invalid note revision")
		}
	}
	outbox := map[string]notify.Entry{}
	for _, e := range s.Outbox.Entries {
		outbox[e.ID] = e
	}
	seen := map[string]bool{}
	for _, b := range l.Mail {
		e, ok := outbox[b.OutboxID]
		if !ok || seen[b.OutboxID] || e.Key != b.Key || e.To != b.Email || b.OutboxID == "" || !strings.HasPrefix(b.Key, "note:") || !identifier.MatchString(b.UserID) || !noteTimestamp(b.UserCreatedAt) || b.PreferenceRevision == 0 || b.PreferenceRevision > maxNoteSequence || !validNoteEntity(b.Note) || !validNoteEntity(b.Member) || len(b.WatchMembers) < 1 || len(b.WatchMembers) > 2 {
			return errors.New("invalid note delivery binding")
		}
		for _, m := range b.WatchMembers {
			if !validNoteEntity(m) {
				return errors.New("invalid note delivery member")
			}
		}
		seen[b.OutboxID] = true
	}
	return nil
}

// Called only inside the same Store transaction as a successful notes mutation.
func (a *App) noteChanged(s *State, user Record, session Session, old, next Record) error {
	actor, err := a.atlasActor(s, user, session, "admin", "operator")
	if err != nil {
		return err
	}
	if noteSemanticEqual(old, next) {
		return nil
	}
	l := noteState(s)
	pruneNoteState(s)
	if l.Sequence >= maxNoteSequence {
		return errors.New("note revision limit reached")
	}
	l.Sequence++
	n := next
	if n == nil {
		n = old
	}
	if !validNoteEntity(noteEntity(n)) {
		return errors.New("note entity identity is unavailable")
	}
	if next == nil {
		delete(l.Notes, str(n, "id"))
	} else {
		l.Notes[str(n, "id")] = NoteVersion{CreatedAt: str(n, "createdAt"), Revision: l.Sequence}
	}
	// Reading one's own edit does not mute that person's independent subscription.
	p := notePreferences(s, actor)
	if next != nil {
		marks := p.Reads[:0]
		for _, r := range p.Reads {
			if r.ID != str(next, "id") {
				marks = append(marks, r)
			}
		}
		p.Reads = append(marks, NoteReadMark{NoteEntityBinding: noteEntity(next), Revision: l.Sequence, Checksum: noteChecksum(next)})
		if err = saveNotePreferences(s, p); err != nil {
			return err
		}
	}
	if a.mailConfig.Enabled {
		if err = a.enqueueNoteMail(s, actor, old, next); err != nil {
			return err
		}
	}
	return validateNoteNotifications(s)
}

func noteSortedRecords(s *State, kind string) []Record {
	out := append([]Record(nil), s.Resources[kind]...)
	sort.Slice(out, func(i, j int) bool { return str(out[i], "id") < str(out[j], "id") })
	return out
}
func noteRevisionConflict() error { return fmt.Errorf("note state changed; reload before saving") }
