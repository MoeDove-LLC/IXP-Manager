package app

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/moedove/ixp-manager/internal/migrate"
)

const legacyMailingReviewLifetime = 5 * time.Minute
const legacyMailingMappingLimit = 4096

type LegacyMailingBatch struct {
	Checksum   string `json:"checksum"`
	ImportedAt string `json:"importedAt"`
	ImportedBy string `json:"importedBy"`
	Users      int    `json:"users"`
	Choices    int    `json:"choices"`
	Bytes      int64  `json:"bytes"`
}
type LegacyMailingArchive struct {
	Batch   LegacyMailingBatch     `json:"batch"`
	Archive migrate.MailingArchive `json:"archive"`
}
type LegacyMailingListBinding struct {
	SourceKey          string `json:"sourceKey"`
	ListID             string `json:"listId"`
	ListCreatedAt      string `json:"listCreatedAt"`
	ListKey            string `json:"listKey"`
	ListEmail          string `json:"listEmail"`
	PreferenceRevision uint64 `json:"preferenceRevision"`
	PreferenceDigest   string `json:"preferenceDigest"`
}
type LegacyMailingSelection struct {
	SourceKey          string `json:"sourceKey"`
	ListRevision       uint64 `json:"listRevision"`
	PreferenceRevision uint64 `json:"preferenceRevision"`
	Subscribed         bool   `json:"subscribed"`
}
type LegacyMailingReview struct {
	MappingRevision uint64                   `json:"mappingRevision"`
	TokenHash       string                   `json:"tokenHash"`
	SessionBinding  string                   `json:"sessionBinding"`
	Email           string                   `json:"email"`
	Choices         []LegacyMailingSelection `json:"choices"`
	CreatedAt       string                   `json:"createdAt"`
	ExpiresAt       string                   `json:"expiresAt"`
}
type LegacyMailingMapping struct {
	ID                  string                     `json:"id"`
	ArchiveChecksum     string                     `json:"archiveChecksum"`
	SourceUserID        string                     `json:"sourceUserId"`
	TargetUserID        string                     `json:"targetUserId"`
	TargetUserCreatedAt string                     `json:"targetUserCreatedAt"`
	Lists               []LegacyMailingListBinding `json:"lists"`
	Status              string                     `json:"status"`
	Revision            uint64                     `json:"revision"`
	CreatedAt           string                     `json:"createdAt"`
	CreatedBy           string                     `json:"createdBy"`
	UpdatedAt           string                     `json:"updatedAt"`
	AcceptedAt          string                     `json:"acceptedAt,omitempty"`
	AcceptedChoices     []LegacyMailingSelection   `json:"acceptedChoices,omitempty"`
	Review              *LegacyMailingReview       `json:"review,omitempty"`
}
type LegacyMailingState struct {
	Version  int                    `json:"version"`
	Archives []LegacyMailingArchive `json:"archives"`
	Mappings []LegacyMailingMapping `json:"mappings"`
}

func legacyMailingState(s *State) *LegacyMailingState {
	if s.LegacyMailing == nil {
		s.LegacyMailing = &LegacyMailingState{Version: 1, Archives: []LegacyMailingArchive{}, Mappings: []LegacyMailingMapping{}}
	}
	return s.LegacyMailing
}
func legacyMailingArchive(s *State, checksum string) *LegacyMailingArchive {
	if s.LegacyMailing != nil {
		for i := range s.LegacyMailing.Archives {
			if s.LegacyMailing.Archives[i].Batch.Checksum == checksum {
				return &s.LegacyMailing.Archives[i]
			}
		}
	}
	return nil
}
func legacyMailingMapping(s *State, id string) *LegacyMailingMapping {
	if s.LegacyMailing != nil {
		for i := range s.LegacyMailing.Mappings {
			if s.LegacyMailing.Mappings[i].ID == id {
				return &s.LegacyMailing.Mappings[i]
			}
		}
	}
	return nil
}
func legacyMailingUser(a *migrate.MailingArchive, id string) *migrate.MailingArchiveUser {
	for i := range a.Users {
		if a.Users[i].LegacyID == id {
			return &a.Users[i]
		}
	}
	return nil
}
func legacyMailingSourceKey(a *migrate.MailingArchive, user, key string) bool {
	for _, c := range a.Choices {
		if c.SourceUserID == user && c.SourceKey == key {
			return true
		}
	}
	return false
}
func legacyMailingBinding(m *LegacyMailingMapping, key string) *LegacyMailingListBinding {
	for i := range m.Lists {
		if m.Lists[i].SourceKey == key {
			return &m.Lists[i]
		}
	}
	return nil
}
func legacyMailingCurrentUser(s *State, m *LegacyMailingMapping) Record {
	u := find(s, "users", m.TargetUserID)
	if u == nil || str(u, "createdAt") != m.TargetUserCreatedAt || !contains([]string{"admin", "operator", "viewer", "member"}, str(u, "role")) {
		return nil
	}
	return u
}
func legacyMailingCurrentList(s *State, b *LegacyMailingListBinding) *MailingList {
	l := mailingList(s, b.ListID)
	if l == nil || l.CreatedAt != b.ListCreatedAt || l.Key != b.ListKey || l.Email != b.ListEmail {
		return nil
	}
	return l
}
func legacyMailingPreferenceDigest(p MailingPreference) string {
	raw, _ := json.Marshal(p)
	return digest(string(raw))
}
func legacyMailingMappingView(s *State, m *LegacyMailingMapping) Record {
	u := legacyMailingCurrentUser(s, m)
	return Record{"id": m.ID, "archiveChecksum": m.ArchiveChecksum, "sourceUserId": m.SourceUserID, "targetUserId": m.TargetUserID, "targetUserCreatedAt": m.TargetUserCreatedAt, "targetName": str(u, "name"), "targetCurrent": u != nil, "lists": m.Lists, "status": m.Status, "revision": m.Revision, "createdAt": m.CreatedAt, "createdBy": m.CreatedBy, "updatedAt": m.UpdatedAt, "acceptedAt": m.AcceptedAt, "acceptedChoices": m.AcceptedChoices}
}
func legacyMailingChoice(s *State, m *LegacyMailingMapping, user Record, b *LegacyMailingListBinding) (Record, bool) {
	a := legacyMailingArchive(s, m.ArchiveChecksum)
	value, desired := migrate.MailingArchiveSelection(&a.Archive, m.SourceUserID, b.SourceKey)
	list := legacyMailingCurrentList(s, b)
	p, index := mailingPreference(s, b.ListID, user)
	email, validEmail := mailingEmail(str(user, "email"))
	reason := ""
	switch {
	case desired == "unknown":
		reason = "Unknown or conflicting source value; choose directly in current mailing settings."
	case list == nil:
		reason = "Mapped list identity changed."
	case !validEmail:
		reason = "Current account needs a valid email address."
	case !list.Enabled && desired == "subscribed":
		reason = "Mapped list is disabled."
	case index >= 0 && p.Email == email:
		reason = "An explicit current-email choice already exists; migration cannot replace it."
	case p.Revision != b.PreferenceRevision || legacyMailingPreferenceDigest(p) != b.PreferenceDigest:
		reason = "Preference changed since mapping; migration cannot overwrite it."
	}
	var sourceValue any
	if desired != "unknown" {
		_ = json.Unmarshal(value, &sourceValue)
	}
	view := Record{"sourceKey": b.SourceKey, "sourceValue": sourceValue, "desired": desired, "listId": b.ListID, "listName": "", "listEmail": b.ListEmail, "listRevision": uint64(0), "preferenceRevision": p.Revision, "available": reason == "", "reason": reason}
	if list != nil {
		view["listName"], view["listRevision"] = list.Name, list.Revision
	}
	return view, reason == ""
}
func legacyMailingSourceView(s *State, m *LegacyMailingMapping, user Record) Record {
	choices := []Record{}
	for i := range m.Lists {
		v, _ := legacyMailingChoice(s, m, user, &m.Lists[i])
		choices = append(choices, v)
	}
	// Unknown source values and historical identity labels remain admin-only.
	return Record{"mapping": legacyMailingMappingView(s, m), "sourceUser": Record{"legacyId": m.SourceUserID}, "choices": choices}
}
func legacyMailingSelectionsValid(a *migrate.MailingArchive, m *LegacyMailingMapping, choices []LegacyMailingSelection) bool {
	if len(choices) == 0 || len(choices) > maxMailingLists {
		return false
	}
	seen := map[string]bool{}
	for _, c := range choices {
		_, desired := migrate.MailingArchiveSelection(a, m.SourceUserID, c.SourceKey)
		if seen[c.SourceKey] || legacyMailingBinding(m, c.SourceKey) == nil || c.ListRevision == 0 || c.ListRevision > maxMailingRevision || c.PreferenceRevision > maxMailingRevision || desired == "unknown" || c.Subscribed != (desired == "subscribed") {
			return false
		}
		seen[c.SourceKey] = true
	}
	return true
}
func validateLegacyMailing(s *State) error {
	l := s.LegacyMailing
	if l == nil {
		return nil
	}
	if l.Version != 1 || len(l.Archives) > 64 || len(l.Mappings) > legacyMailingMappingLimit {
		return errors.New("legacy mailing state exceeds bounds")
	}
	archives := map[string]*LegacyMailingArchive{}
	var size int64
	count := 0
	for i := range l.Archives {
		a := &l.Archives[i]
		b := a.Batch
		raw, err := json.Marshal(a.Archive)
		if err != nil || archives[b.Checksum] != nil || b.Checksum != a.Archive.Checksum || b.Bytes != int64(len(raw)) || b.Users != len(a.Archive.Users) || b.Choices != len(a.Archive.Choices) || !noteTimestamp(b.ImportedAt) || !identifier.MatchString(b.ImportedBy) {
			return errors.New("invalid legacy mailing import receipt")
		}
		if err := migrate.ValidateMailingArchive(&a.Archive); err != nil {
			return err
		}
		size += b.Bytes
		count += b.Users + b.Choices
		if size > migrate.MailingArchiveLimit || count > migrate.MailingArchiveRecordLimit {
			return errors.New("cumulative legacy mailing archives exceed bounds")
		}
		archives[b.Checksum] = a
	}
	ids, active := map[string]bool{}, map[string]bool{}
	for _, m := range l.Mappings {
		a := archives[m.ArchiveChecksum]
		if !identifier.MatchString(m.ID) || ids[m.ID] || a == nil || legacyMailingUser(&a.Archive, m.SourceUserID) == nil || !identifier.MatchString(m.TargetUserID) || !noteTimestamp(m.TargetUserCreatedAt) || !noteTimestamp(m.CreatedAt) || !noteTimestamp(m.UpdatedAt) || !identifier.MatchString(m.CreatedBy) || m.Revision == 0 || m.Revision > maxMailingRevision || len(m.Lists) == 0 || len(m.Lists) > maxMailingLists {
			return errors.New("invalid legacy mailing mapping identity")
		}
		ids[m.ID] = true
		if m.Status != "pending" && m.Status != "cancelled" && m.Status != "accepted" {
			return errors.New("invalid legacy mailing mapping status")
		}
		keys, lists := map[string]bool{}, map[string]bool{}
		for _, b := range m.Lists {
			if !legacyMailingSourceKey(&a.Archive, m.SourceUserID, b.SourceKey) || keys[b.SourceKey] || lists[b.ListID] || !identifier.MatchString(b.ListID) || !noteTimestamp(b.ListCreatedAt) || !mailingKey.MatchString(b.ListKey) || !digestPattern.MatchString(b.PreferenceDigest) || b.PreferenceRevision > maxMailingRevision {
				return errors.New("invalid legacy mailing list binding")
			}
			if email, ok := mailingEmail(b.ListEmail); !ok || email != b.ListEmail {
				return errors.New("invalid legacy mailing list address")
			}
			keys[b.SourceKey], lists[b.ListID] = true, true
			occupied := m.Status == "pending"
			if m.Status == "accepted" {
				for _, choice := range m.AcceptedChoices {
					occupied = occupied || choice.SourceKey == b.SourceKey
				}
			}
			if occupied {
				key := m.ArchiveChecksum + ":" + m.SourceUserID + ":" + b.SourceKey
				if active[key] {
					return errors.New("source mailing choice has multiple active mappings")
				}
				active[key] = true
			}
		}
		if m.Status == "accepted" {
			if !noteTimestamp(m.AcceptedAt) || !legacyMailingSelectionsValid(&a.Archive, &m, m.AcceptedChoices) || m.Review != nil {
				return errors.New("invalid legacy mailing acceptance receipt")
			}
		} else if m.AcceptedAt != "" || len(m.AcceptedChoices) != 0 {
			return errors.New("unaccepted mapping contains acceptance receipt")
		}
		if r := m.Review; r != nil {
			created, e1 := time.Parse(time.RFC3339Nano, r.CreatedAt)
			expires, e2 := time.Parse(time.RFC3339Nano, r.ExpiresAt)
			email, ok := mailingEmail(r.Email)
			if m.Status != "pending" || r.MappingRevision != m.Revision || !digestPattern.MatchString(r.TokenHash) || !digestPattern.MatchString(r.SessionBinding) || !ok || email != r.Email || e1 != nil || e2 != nil || expires.Sub(created) != legacyMailingReviewLifetime || !legacyMailingSelectionsValid(&a.Archive, &m, r.Choices) {
				return errors.New("invalid legacy mailing review binding")
			}
		}
	}
	raw, err := json.Marshal(l)
	if err != nil || len(raw) > 32<<20 {
		return errors.New("legacy mailing state exceeds byte bound")
	}
	return nil
}
func legacyMailingSortChoices(choices []LegacyMailingSelection) {
	sort.Slice(choices, func(i, j int) bool { return choices[i].SourceKey < choices[j].SourceKey })
}
