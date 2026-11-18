package app

import (
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const maxMailingLists = 64
const maxMailingPreferences = 50000
const maxMailingRetired = 100000
const maxMailingRevision uint64 = 9007199254740991

var mailingKey = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var errMailingConflict = errors.New("mailing list, account email or preference changed; reload and review before saving")

type MailingList struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Email       string `json:"email"`
	ArchiveURL  string `json:"archiveUrl"`
	Enabled     bool   `json:"enabled"`
	Revision    uint64 `json:"revision"`
	CreatedAt   string `json:"createdAt"`
}
type MailingPreference struct {
	ListID        string `json:"listId"`
	UserID        string `json:"userId"`
	UserCreatedAt string `json:"userCreatedAt"`
	Email         string `json:"email"`
	Subscribed    bool   `json:"subscribed"`
	Revision      uint64 `json:"revision"`
}
type MailingRetiredAddress struct {
	ListID string `json:"listId"`
	Email  string `json:"email"`
}
type MailingState struct {
	Version     int                     `json:"version"`
	Sequence    uint64                  `json:"sequence"`
	Lists       []MailingList           `json:"lists"`
	Preferences []MailingPreference     `json:"preferences,omitempty"`
	Retired     []MailingRetiredAddress `json:"retired,omitempty"`
}

func mailingState(s *State) *MailingState {
	if s.MailingLists == nil {
		s.MailingLists = &MailingState{Version: 1, Lists: []MailingList{}}
	}
	return s.MailingLists
}
func mailingEmail(raw string) (string, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if len(value) > 254 || !mailAddress(value) {
		return "", false
	}
	for _, c := range value {
		if c <= 32 || c >= 127 {
			return "", false
		}
	}
	return value, true
}
func validMailingDefinition(list MailingList) error {
	email, ok := mailingEmail(list.Email)
	if !mailingKey.MatchString(list.Key) || strings.TrimSpace(list.Name) != list.Name || list.Name == "" || len(list.Name) > 160 || strings.ContainsAny(list.Name, "\r\n\x00") || len(list.Description) > 4096 || strings.ContainsRune(list.Description, '\x00') || !ok || email != list.Email {
		return errors.New("mailing list requires a lowercase key, name and plain ASCII email address within size limits")
	}
	if list.ArchiveURL != "" {
		u, err := url.Parse(list.ArchiveURL)
		if err != nil || len(list.ArchiveURL) > 2048 || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.Scheme != "https" && u.Scheme != "http" || strings.ContainsAny(list.ArchiveURL, "\r\n\x00") {
			return errors.New("archive URL must be an absolute HTTP or HTTPS address without credentials")
		}
	}
	return nil
}
func validateMailingState(s *State) error {
	m := s.MailingLists
	if m == nil {
		return nil
	}
	if m.Version != 1 || m.Sequence > maxMailingRevision || len(m.Lists) > maxMailingLists || len(m.Preferences) > maxMailingPreferences || len(m.Retired) > maxMailingRetired {
		return errors.New("invalid mailing list state or capacity")
	}
	ids, keys, emails := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, list := range m.Lists {
		if !identifier.MatchString(list.ID) || ids[list.ID] || keys[list.Key] || emails[list.Email] || !noteTimestamp(list.CreatedAt) || list.Revision == 0 || list.Revision > m.Sequence || validMailingDefinition(list) != nil {
			return errors.New("invalid persisted mailing list definition")
		}
		ids[list.ID], keys[list.Key], emails[list.Email] = true, true, true
	}
	seen := map[string]bool{}
	for _, p := range m.Preferences {
		key := p.ListID + "\x00" + p.UserID + "\x00" + p.UserCreatedAt
		email, ok := mailingEmail(p.Email)
		if !ids[p.ListID] || !identifier.MatchString(p.UserID) || !noteTimestamp(p.UserCreatedAt) || !ok || p.Email != email || p.Revision == 0 || p.Revision > m.Sequence || seen[key] {
			return errors.New("invalid persisted mailing preference")
		}
		seen[key] = true
	}
	seen = map[string]bool{}
	for _, p := range m.Retired {
		key := p.ListID + "\x00" + p.Email
		email, ok := mailingEmail(p.Email)
		if !ids[p.ListID] || !ok || p.Email != email || seen[key] {
			return errors.New("invalid retired mailing address")
		}
		seen[key] = true
	}
	return nil
}
func nextMailingRevision(m *MailingState) (uint64, error) {
	if m.Sequence >= maxMailingRevision {
		return 0, errors.New("mailing list revision capacity reached")
	}
	m.Sequence++
	return m.Sequence, nil
}
func mailingList(s *State, id string) *MailingList {
	if s.MailingLists != nil {
		for i := range s.MailingLists.Lists {
			if s.MailingLists.Lists[i].ID == id {
				return &s.MailingLists.Lists[i]
			}
		}
	}
	return nil
}
func mailingPreference(s *State, listID string, user Record) (MailingPreference, int) {
	if s.MailingLists != nil {
		for i, p := range s.MailingLists.Preferences {
			if p.ListID == listID && p.UserID == str(user, "id") && p.UserCreatedAt == str(user, "createdAt") {
				return p, i
			}
		}
	}
	return MailingPreference{}, -1
}
func mailingSelfView(s *State, user Record) Record {
	items := []Record{}
	email, _ := mailingEmail(str(user, "email"))
	if s.MailingLists != nil {
		for _, list := range s.MailingLists.Lists {
			p, index := mailingPreference(s, list.ID, user)
			if !list.Enabled && index < 0 {
				continue
			}
			desired := "unset"
			if index >= 0 && p.Email == email {
				desired = "unsubscribed"
				if p.Subscribed {
					desired = "subscribed"
				}
			}
			items = append(items, Record{"id": list.ID, "key": list.Key, "name": list.Name, "description": list.Description, "email": list.Email, "archiveUrl": list.ArchiveURL, "enabled": list.Enabled, "revision": list.Revision, "listRevision": list.Revision, "preferenceRevision": p.Revision, "desired": desired, "staleEmail": index >= 0 && p.Email != email})
		}
	}
	return Record{"email": email, "items": items, "synchronization": "external-not-observed"}
}
func saveMailingPreference(s *State, list MailingList, user Record, subscribed bool, expected uint64) error {
	p, index := mailingPreference(s, list.ID, user)
	if p.Revision != expected {
		return errMailingConflict
	}
	email, ok := mailingEmail(str(user, "email"))
	if !ok {
		return errors.New("account requires a plain ASCII email address before choosing a subscription")
	}
	if subscribed && !list.Enabled {
		return errors.New("mailing list is disabled; only withdrawal of a preference is available")
	}
	if index >= 0 && p.Email == email && p.Subscribed == subscribed {
		return nil
	}
	m := mailingState(s)
	if index < 0 && len(m.Preferences) >= maxMailingPreferences {
		return errors.New("mailing preference capacity reached")
	}
	if index >= 0 && p.Email != email {
		retired := MailingRetiredAddress{ListID: list.ID, Email: p.Email}
		found := false
		for _, r := range m.Retired {
			found = found || r == retired
		}
		if !found {
			if len(m.Retired) >= maxMailingRetired {
				return errors.New("retired mailing address capacity reached")
			}
			m.Retired = append(m.Retired, retired)
		}
	}
	revision, err := nextMailingRevision(m)
	if err != nil {
		return err
	}
	p = MailingPreference{ListID: list.ID, UserID: str(user, "id"), UserCreatedAt: str(user, "createdAt"), Email: email, Subscribed: subscribed, Revision: revision}
	if index >= 0 {
		m.Preferences[index] = p
	} else {
		m.Preferences = append(m.Preferences, p)
	}
	return validateMailingState(s)
}

type MailingExportList struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Email     string `json:"email"`
	Revision  uint64 `json:"revision"`
	CreatedAt string `json:"createdAt"`
}
type MailingExport struct {
	Version         int               `json:"version"`
	List            MailingExportList `json:"list"`
	Checksum        string            `json:"checksum"`
	Subscribed      []string          `json:"subscribed"`
	Unsubscribed    []string          `json:"unsubscribed"`
	GeneratedAt     string            `json:"generatedAt"`
	ExternalApplied bool              `json:"externalApplied"`
}

func exportMailingList(s *State, list MailingList, now time.Time) (MailingExport, error) {
	if err := validateMailingState(s); err != nil {
		return MailingExport{}, err
	}
	if !list.Enabled {
		return MailingExport{}, errors.New("disabled mailing list cannot be exported for synchronization")
	}
	out := MailingExport{Version: 1, List: MailingExportList{list.ID, list.Key, list.Email, list.Revision, list.CreatedAt}, Subscribed: []string{}, Unsubscribed: []string{}, GeneratedAt: now.UTC().Format(time.RFC3339Nano)}
	intent := map[string]bool{}
	users := map[string]Record{}
	for _, user := range s.Resources["users"] {
		users[str(user, "id")] = user
	}
	if s.MailingLists != nil {
		for _, retired := range s.MailingLists.Retired {
			if retired.ListID == list.ID {
				intent[retired.Email] = false
			}
		}
		for _, p := range s.MailingLists.Preferences {
			if p.ListID != list.ID {
				continue
			}
			u := users[p.UserID]
			email, ok := mailingEmail(str(u, "email"))
			current := ok && str(u, "createdAt") == p.UserCreatedAt && email == p.Email
			// A new current account's explicit consent takes precedence over an
			// old account/address tombstone. Unset preferences never add a user.
			intent[p.Email] = intent[p.Email] || current && p.Subscribed
		}
	}
	for email, subscribed := range intent {
		if subscribed {
			out.Subscribed = append(out.Subscribed, email)
		} else {
			out.Unsubscribed = append(out.Unsubscribed, email)
		}
	}
	sort.Strings(out.Subscribed)
	sort.Strings(out.Unsubscribed)
	raw, _ := json.Marshal(struct {
		Version      int               `json:"version"`
		List         MailingExportList `json:"list"`
		Subscribed   []string          `json:"subscribed"`
		Unsubscribed []string          `json:"unsubscribed"`
	}{out.Version, out.List, out.Subscribed, out.Unsubscribed})
	out.Checksum = digest(string(raw))
	return out, nil
}
