package app

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/moedove/ixp-manager/internal/migrate"
)

const legacyNoteArchiveCount = 64
const legacyNoteMappingCount = 4096
const legacyNoteStateBytes = 64 << 20
const legacyNoteReviewLifetime = 5 * time.Minute

type LegacyNoteBatch struct {
	Checksum       string `json:"checksum"`
	ImportedAt     string `json:"importedAt"`
	ImportedBy     string `json:"importedBy"`
	SourceTimezone string `json:"sourceTimezone"`
	Notes          int    `json:"notes"`
	Users          int    `json:"users"`
	Preferences    int    `json:"preferences"`
	Bytes          int64  `json:"bytes"`
}
type LegacyNoteArchive struct {
	Batch   LegacyNoteBatch     `json:"batch"`
	Archive migrate.NoteArchive `json:"archive"`
}
type LegacyNoteSelection struct {
	Mode          string              `json:"mode"`
	MemberWatches []NoteEntityBinding `json:"memberWatches"`
	NoteWatches   []NoteEntityBinding `json:"noteWatches"`
}
type LegacyNoteReview struct {
	MappingRevision    uint64              `json:"mappingRevision"`
	TokenHash          string              `json:"tokenHash"`
	SessionBinding     string              `json:"sessionBinding"`
	Generation         uint64              `json:"generation"`
	Email              string              `json:"email"`
	PreferenceRevision uint64              `json:"preferenceRevision"`
	PreferenceDigest   string              `json:"preferenceDigest"`
	Selection          LegacyNoteSelection `json:"selection"`
	CreatedAt          string              `json:"createdAt"`
	ExpiresAt          string              `json:"expiresAt"`
}
type LegacyNoteMapping struct {
	ID                         string               `json:"id"`
	ArchiveChecksum            string               `json:"archiveChecksum"`
	SourceUserID               int64                `json:"sourceUserId"`
	TargetUserID               string               `json:"targetUserId"`
	TargetUserCreatedAt        string               `json:"targetUserCreatedAt"`
	Status                     string               `json:"status"`
	Revision                   uint64               `json:"revision"`
	CreatedAt                  string               `json:"createdAt"`
	CreatedBy                  string               `json:"createdBy"`
	UpdatedAt                  string               `json:"updatedAt"`
	AcceptedAt                 string               `json:"acceptedAt,omitempty"`
	AcceptedPreferenceRevision uint64               `json:"acceptedPreferenceRevision,omitempty"`
	AcceptedSelection          *LegacyNoteSelection `json:"acceptedSelection,omitempty"`
	Review                     *LegacyNoteReview    `json:"review,omitempty"`
}
type LegacyNotesState struct {
	Version  int                 `json:"version"`
	Archives []LegacyNoteArchive `json:"archives"`
	Mappings []LegacyNoteMapping `json:"mappings"`
}

func legacyNotesState(s *State) *LegacyNotesState {
	if s.LegacyNotes == nil {
		s.LegacyNotes = &LegacyNotesState{Version: 1, Archives: []LegacyNoteArchive{}, Mappings: []LegacyNoteMapping{}}
	}
	return s.LegacyNotes
}
func legacyNoteArchive(s *State, checksum string) *LegacyNoteArchive {
	if s.LegacyNotes != nil {
		for i := range s.LegacyNotes.Archives {
			if s.LegacyNotes.Archives[i].Batch.Checksum == checksum {
				return &s.LegacyNotes.Archives[i]
			}
		}
	}
	return nil
}
func legacyNoteMapping(s *State, id string) *LegacyNoteMapping {
	if s.LegacyNotes != nil {
		for i := range s.LegacyNotes.Mappings {
			if s.LegacyNotes.Mappings[i].ID == id {
				return &s.LegacyNotes.Mappings[i]
			}
		}
	}
	return nil
}
func legacyNoteUser(a *migrate.NoteArchive, id int64) *migrate.NoteArchiveUser {
	for i := range a.Users {
		if a.Users[i].LegacyID == id {
			return &a.Users[i]
		}
	}
	return nil
}
func legacyNoteTargetCurrent(s *State, m *LegacyNoteMapping) bool {
	u := find(s, "users", m.TargetUserID)
	return noteStaff(u) && str(u, "createdAt") == m.TargetUserCreatedAt
}
func legacyNoteMappingView(s *State, m *LegacyNoteMapping) Record {
	current := legacyNoteTargetCurrent(s, m)
	name := m.TargetUserID
	if current {
		name = str(find(s, "users", m.TargetUserID), "name")
	}
	return Record{"id": m.ID, "archiveChecksum": m.ArchiveChecksum, "sourceUserId": strconv.FormatInt(m.SourceUserID, 10), "targetUserId": m.TargetUserID, "targetUserCreatedAt": m.TargetUserCreatedAt, "targetName": name, "targetCurrent": current, "status": m.Status, "revision": m.Revision, "createdAt": m.CreatedAt, "createdBy": m.CreatedBy, "updatedAt": m.UpdatedAt, "acceptedAt": m.AcceptedAt, "acceptedPreferenceRevision": m.AcceptedPreferenceRevision}
}
func legacyNoteSelectionValid(selection LegacyNoteSelection) bool {
	if !noteMode(selection.Mode) || len(selection.MemberWatches) > maxNoteWatches || len(selection.NoteWatches) > maxNoteWatches {
		return false
	}
	for _, list := range [][]NoteEntityBinding{selection.MemberWatches, selection.NoteWatches} {
		seen := map[string]bool{}
		for _, binding := range list {
			if !validNoteEntity(binding) || seen[binding.ID] {
				return false
			}
			seen[binding.ID] = true
		}
	}
	return true
}
func legacyNoteSelectionCurrent(s *State, selection LegacyNoteSelection) bool {
	if !legacyNoteSelectionValid(selection) {
		return false
	}
	for _, binding := range selection.MemberWatches {
		if !noteEntityCurrent(s, "members", binding) {
			return false
		}
	}
	for _, binding := range selection.NoteWatches {
		if !noteEntityCurrent(s, "notes", binding) {
			return false
		}
	}
	return true
}
func legacyNotePreferenceDigest(p NotePreference) string {
	return patchHash(Record{"userId": p.UserID, "userCreatedAt": p.UserCreatedAt, "revision": p.Revision, "mode": p.Mode, "memberWatches": p.MemberWatches, "noteWatches": p.NoteWatches})
}

func validateLegacyNotes(s *State) error {
	l := s.LegacyNotes
	if l == nil {
		return nil
	}
	if l.Version != 1 || len(l.Archives) > legacyNoteArchiveCount || len(l.Mappings) > legacyNoteMappingCount {
		return errors.New("legacy note archive state exceeds bounds")
	}
	archives := map[string]*LegacyNoteArchive{}
	var bytes int64
	records := 0
	for i := range l.Archives {
		entry := &l.Archives[i]
		b, source := entry.Batch, &entry.Archive
		if archives[b.Checksum] != nil || b.Checksum != source.Checksum || !noteTimestamp(b.ImportedAt) || !identifier.MatchString(b.ImportedBy) || b.SourceTimezone != source.SourceTimezone || b.Notes != len(source.Notes) || b.Users != len(source.Users) || b.Preferences != len(source.Preferences) {
			return errors.New("invalid legacy note import receipt")
		}
		if err := migrate.ValidateNoteArchive(source); err != nil {
			return err
		}
		raw, err := json.Marshal(source)
		if err != nil || b.Bytes != int64(len(raw)) {
			return errors.New("legacy note archive size changed")
		}
		bytes += b.Bytes
		records += b.Notes + b.Users + b.Preferences
		if bytes > migrate.NoteArchiveLimit || records > migrate.NoteArchiveRecordLimit {
			return errors.New("cumulative legacy note source limit exceeded")
		}
		archives[b.Checksum] = entry
	}
	ids, active := map[string]bool{}, map[string]bool{}
	for _, m := range l.Mappings {
		source := archives[m.ArchiveChecksum]
		if !identifier.MatchString(m.ID) || ids[m.ID] || source == nil || legacyNoteUser(&source.Archive, m.SourceUserID) == nil || !identifier.MatchString(m.TargetUserID) || !noteTimestamp(m.TargetUserCreatedAt) || !identifier.MatchString(m.CreatedBy) || !noteTimestamp(m.CreatedAt) || !noteTimestamp(m.UpdatedAt) || m.Revision == 0 || m.Revision > maxNoteSequence {
			return errors.New("invalid legacy note mapping identity")
		}
		ids[m.ID] = true
		if m.Status != "pending" && m.Status != "cancelled" && m.Status != "accepted" {
			return errors.New("invalid legacy note mapping status")
		}
		if m.Status != "cancelled" {
			key := m.ArchiveChecksum + ":" + strconv.FormatInt(m.SourceUserID, 10)
			if active[key] {
				return errors.New("legacy source user has multiple active mappings")
			}
			active[key] = true
		}
		if m.Status == "accepted" {
			if !noteTimestamp(m.AcceptedAt) || m.AcceptedSelection == nil || !legacyNoteSelectionValid(*m.AcceptedSelection) || m.AcceptedPreferenceRevision > maxNoteSequence || m.Review != nil {
				return errors.New("invalid legacy note acceptance receipt")
			}
		} else if m.AcceptedAt != "" || m.AcceptedSelection != nil || m.AcceptedPreferenceRevision != 0 {
			return errors.New("unaccepted mapping cannot contain an acceptance receipt")
		}
		if r := m.Review; r != nil {
			created, e1 := time.Parse(time.RFC3339Nano, r.CreatedAt)
			expires, e2 := time.Parse(time.RFC3339Nano, r.ExpiresAt)
			if m.Status != "pending" || r.MappingRevision != m.Revision || !digestPattern.MatchString(r.TokenHash) || !digestPattern.MatchString(r.SessionBinding) || r.Email == "" || len(r.Email) > 320 || !digestPattern.MatchString(r.PreferenceDigest) || r.PreferenceRevision > maxNoteSequence || !legacyNoteSelectionValid(r.Selection) || e1 != nil || e2 != nil || expires.Sub(created) != legacyNoteReviewLifetime {
				return errors.New("invalid legacy note review binding")
			}
		}
	}
	raw, err := json.Marshal(l)
	if err != nil || len(raw) > legacyNoteStateBytes {
		return errors.New("legacy note archive and mapping bytes exceed limit")
	}
	return nil
}
