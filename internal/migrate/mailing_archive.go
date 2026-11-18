package migrate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
)

const MailingArchiveKind = "ixp-manager-mailing-sources"
const MailingArchiveLimit int64 = 16 << 20
const MailingArchiveRecordLimit = 50000

// Only selected identity labels and mailing choices belong here. Passwords,
// security settings and other preference branches never enter this artifact.
type MailingArchiveUser struct {
	LegacyID string           `json:"legacyId"`
	Facts    NoteArchiveFacts `json:"facts"`
}
type MailingArchiveChoice struct {
	ID           string           `json:"id"`
	SourceUserID string           `json:"sourceUserId"`
	SourceKey    string           `json:"sourceKey"`
	Format       string           `json:"format"`
	Value        json.RawMessage  `json:"value"`
	Facts        NoteArchiveFacts `json:"facts,omitempty"`
}
type MailingArchive struct {
	Version  int                    `json:"version"`
	Kind     string                 `json:"kind"`
	Source   string                 `json:"source"`
	Access   string                 `json:"access"`
	Checksum string                 `json:"checksum"`
	Users    []MailingArchiveUser   `json:"users"`
	Choices  []MailingArchiveChoice `json:"choices"`
}

var legacyMailingKey = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func MailingArchiveKey(key string) bool { return legacyMailingKey.MatchString(key) }
func MailingArchiveUserID(id string) bool {
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == id
}

// Old profile forms stored strings, while MailingList::init stored integers.
// Other PHP truthy values are deliberately never interpreted as consent.
func MailingArchiveDesired(raw json.RawMessage) string {
	value, err := noteArchiveScalar(raw, 4096)
	if err != nil {
		return "unknown"
	}
	var text string
	switch v := value.(type) {
	case string:
		text = v
	case json.Number:
		text = string(v)
	}
	switch text {
	case "1":
		return "subscribed"
	case "0":
		return "unsubscribed"
	default:
		return "unknown"
	}
}

// Duplicate source rows remain visible, but are ambiguous even if their values
// agree. An operator must resolve that ambiguity in the original source export.
func MailingArchiveSelection(a *MailingArchive, user, key string) (json.RawMessage, string) {
	var value json.RawMessage
	count := 0
	for _, choice := range a.Choices {
		if choice.SourceUserID == user && choice.SourceKey == key {
			count++
			value = choice.Value
		}
	}
	if count != 1 {
		return value, "unknown"
	}
	return value, MailingArchiveDesired(value)
}

func validateMailingArchive(a *MailingArchive, checksum bool) error {
	if a == nil || a.Version != 1 || a.Kind != MailingArchiveKind || a.Source != "ixp-manager-legacy" || a.Access != "admin" || len(a.Choices) == 0 || len(a.Users)+len(a.Choices) > MailingArchiveRecordLimit {
		return errors.New("invalid bounded mailing source envelope")
	}
	users, ids := map[string]bool{}, map[string]bool{}
	for _, user := range a.Users {
		if !MailingArchiveUserID(user.LegacyID) || users[user.LegacyID] || strconv.FormatInt(NoteArchiveSourceID(user.Facts["id"]), 10) != user.LegacyID || noteArchiveFacts(user.Facts, "id username name email disabled created created_at") != nil {
			return errors.New("invalid selected mailing source user")
		}
		users[user.LegacyID] = true
	}
	for _, choice := range a.Choices {
		if !MailingArchiveUserID(choice.SourceUserID) || !MailingArchiveKey(choice.SourceKey) || len(choice.ID) > 256 || ids[choice.ID] {
			return errors.New("invalid mailing source choice identity")
		}
		ids[choice.ID] = true
		if _, err := noteArchiveScalar(choice.Value, 4096); err != nil {
			return errors.New("mailing choice must be a bounded scalar; complex values require source correction")
		}
		switch choice.Format {
		case "user.prefs":
			if !users[choice.SourceUserID] || choice.ID != "user-"+choice.SourceUserID+"-"+choice.SourceKey || len(choice.Facts) != 0 {
				return errors.New("invalid modern mailing source choice")
			}
		case "user_pref":
			id := NoteArchiveSourceID(choice.Facts["id"])
			var attribute string
			if id == 0 || choice.ID != "pref-"+strconv.FormatInt(id, 10) || strconv.FormatInt(NoteArchiveSourceID(choice.Facts["user_id"]), 10) != choice.SourceUserID || json.Unmarshal(choice.Facts["attribute"], &attribute) != nil || attribute != "mailinglist."+choice.SourceKey || noteArchiveFacts(choice.Facts, "id user_id attribute ix op value expire") != nil {
				return errors.New("invalid historical mailing source choice")
			}
			var left, right bytes.Buffer
			if json.Compact(&left, choice.Value) != nil || json.Compact(&right, choice.Facts["value"]) != nil || left.String() != right.String() {
				return errors.New("mailing source value disagrees with source evidence")
			}
		default:
			return errors.New("unsupported mailing source format")
		}
	}
	raw, err := json.Marshal(a)
	if err != nil || int64(len(raw))+1 > MailingArchiveLimit {
		return errors.New("mailing archive exceeds 16 MiB")
	}
	if checksum && (!patchHex.MatchString(a.Checksum) || a.Checksum != MailingArchiveChecksum(*a)) {
		return errors.New("mailing archive checksum mismatch")
	}
	return nil
}
func ValidateMailingArchive(a *MailingArchive) error { return validateMailingArchive(a, true) }
func MailingArchiveChecksum(a MailingArchive) string {
	a.Checksum = ""
	raw, err := json.Marshal(a)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func EncodeMailingArchive(a MailingArchive) ([]byte, error) {
	if err := validateMailingArchive(&a, false); err != nil {
		return nil, err
	}
	a.Checksum = MailingArchiveChecksum(a)
	raw, err := json.Marshal(a)
	if err != nil || int64(len(raw))+1 > MailingArchiveLimit {
		return nil, errors.New("mailing archive exceeds 16 MiB")
	}
	return append(raw, '\n'), nil
}
func ReadMailingArchive(reader io.Reader) (*MailingArchive, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, MailingArchiveLimit+1))
	if err != nil || int64(len(raw)) > MailingArchiveLimit {
		return nil, errors.New("could not read bounded mailing source archive")
	}
	if err := validateNoteJSON(raw, MailingArchiveLimit, 12, 1000000, 256); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var archive MailingArchive
	if err := decoder.Decode(&archive); err != nil {
		return nil, errors.New("invalid mailing source archive fields")
	}
	if err := ValidateMailingArchive(&archive); err != nil {
		return nil, err
	}
	return &archive, nil
}
