package migrate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const NoteArchiveKind = "ixp-manager-note-sources"
const NoteArchiveLimit int64 = 32 << 20
const NoteArchiveSourceLimit int64 = 128 << 20
const NoteArchiveRecordLimit = 100000
const NoteArchiveBodyLimit = 1 << 20
const NoteArchiveWatchLimit = 10000

// Raw scalar facts preserve null versus missing, number versus string, source
// whitespace and date precision. They are provenance, never live permissions.
type NoteArchiveFacts map[string]json.RawMessage
type NoteArchiveNote struct {
	ID           string           `json:"id"`
	LegacyID     int64            `json:"legacyId"`
	Facts        NoteArchiveFacts `json:"facts"`
	Visibility   string           `json:"visibility"`
	PrivacyKnown bool             `json:"privacyKnown"`
}
type NoteArchiveUser struct {
	ID       string           `json:"id"`
	LegacyID int64            `json:"legacyId"`
	Facts    NoteArchiveFacts `json:"facts"`
}
type NoteArchivePreference struct {
	ID           string           `json:"id"`
	Format       string           `json:"format"` // user.prefs or user_pref
	LegacyID     int64            `json:"legacyId"`
	LegacyUserID int64            `json:"legacyUserId"` // zero means source association unresolved
	PrefsState   string           `json:"prefsState"`   // modern column: missing, null, empty-array, object, json-string
	NotesState   string           `json:"notesState"`   // modern: missing, null, empty-array, object
	Notes        NoteArchiveFacts `json:"notes,omitempty"`
	Facts        NoteArchiveFacts `json:"facts"`
}
type NoteArchive struct {
	Version        int                     `json:"version"`
	Kind           string                  `json:"kind"`
	Source         string                  `json:"source"`
	Access         string                  `json:"access"`
	SourceTimezone string                  `json:"sourceTimezone"`
	Checksum       string                  `json:"checksum"`
	Notes          []NoteArchiveNote       `json:"notes"`
	Users          []NoteArchiveUser       `json:"users"`
	Preferences    []NoteArchivePreference `json:"preferences"`
	Warnings       []string                `json:"warnings"`
}

func NoteArchiveID(kind string, id int64) string { return fmt.Sprintf("legacy-note-%s-%d", kind, id) }

func noteArchiveScalar(raw json.RawMessage, limit int) (any, error) {
	// JSON may spell one source byte as a six-byte escape (for example '<').
	// Bound both the encoded representation and the decoded original scalar.
	if len(raw) == 0 || len(raw) > limit*6+2 || !utf8.Valid(raw) || !noteJSONSurrogatesValid(raw) {
		return nil, errors.New("invalid bounded note source scalar")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("one note source scalar required")
	}
	switch v := value.(type) {
	case nil, bool:
		return value, nil
	case string:
		if len(v) <= limit {
			return value, nil
		}
	case json.Number:
		if len(v) <= limit {
			return value, nil
		}
	default:
		return nil, errors.New("note source field must remain scalar")
	}
	return nil, errors.New("note source scalar exceeds decoded byte limit")
}

func NoteArchiveSourceID(raw json.RawMessage) int64 {
	value, err := noteArchiveScalar(raw, 128)
	if err != nil {
		return 0
	}
	var text string
	switch v := value.(type) {
	case json.Number:
		text = string(v)
	case string:
		text = v
	default:
		return 0
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != text {
		return 0
	}
	return n
}

// Only explicit zero/false may be member-visible. Unknown and missing source
// flags stay internal, even though every archive itself is staff-only.
func NoteArchivePrivacy(raw json.RawMessage) (visibility string, known bool) {
	value, err := noteArchiveScalar(raw, 128)
	if err != nil {
		return "internal", false
	}
	switch v := value.(type) {
	case bool:
		if !v {
			return "member", true
		}
		return "internal", true
	case json.Number:
		if v == "0" {
			return "member", true
		}
		return "internal", v == "1"
	case string:
		if v == "0" || v == "false" {
			return "member", true
		}
		return "internal", v == "1" || v == "true"
	default:
		return "internal", false
	}
}

func noteArchiveFacts(facts NoteArchiveFacts, allowed string) error {
	if facts == nil {
		return errors.New("note source facts required")
	}
	for key, value := range facts {
		if !patchFactKey.MatchString(key) || !strings.Contains(" "+allowed+" ", " "+key+" ") {
			return errors.New("unknown note source fact")
		}
		limit := 8192
		if key == "note" {
			limit = NoteArchiveBodyLimit
		}
		if key == "value" {
			limit = 65536
		}
		if _, err := noteArchiveScalar(value, limit); err != nil {
			return err
		}
	}
	return nil
}

var legacyNoteAttribute = regexp.MustCompile(`^customer-notes\.(?:notify|read_upto|[1-9][0-9]*\.(?:notify|last_read)|watching\.[1-9][0-9]*)$`)

func NoteArchiveAttribute(value string) bool {
	if len(value) > 255 || !legacyNoteAttribute.MatchString(value) {
		return false
	}
	for _, part := range strings.Split(value, ".") {
		if part != "" && part[0] >= '0' && part[0] <= '9' {
			n, err := strconv.ParseInt(part, 10, 64)
			if err != nil || n <= 0 {
				return false
			}
		}
	}
	return true
}

func noteArchiveModern(fields NoteArchiveFacts) error {
	for key, raw := range fields {
		switch key {
		case "global_notifs", "read_upto":
			if _, err := noteArchiveScalar(raw, 8192); err != nil {
				return err
			}
		case "customer_watching", "note_watching", "last_read":
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				continue
			}
			if int64(len(raw)) > NoteArchiveLimit || strictNoteJSON(raw) != nil {
				return errors.New("invalid bounded note source watch map")
			}
			var compact bytes.Buffer
			if json.Compact(&compact, raw) == nil && compact.String() == "[]" {
				continue // PHP serializes an emptied associative array as [].
			}
			var entries NoteArchiveFacts
			if json.Unmarshal(raw, &entries) != nil || entries == nil || len(entries) > NoteArchiveWatchLimit {
				return errors.New("note source watch map must be a bounded object or null")
			}
			for id, stamp := range entries {
				if n, e := strconv.ParseInt(id, 10, 64); e != nil || n < 1 || strconv.FormatInt(n, 10) != id {
					return errors.New("invalid note source watch identity")
				}
				if _, err := noteArchiveScalar(stamp, 8192); err != nil {
					return err
				}
			}
		default:
			return errors.New("unknown note preference source field")
		}
	}
	return nil
}

func validateNoteArchive(a *NoteArchive, checksum bool) error {
	if a == nil || a.Version != 1 || a.Kind != NoteArchiveKind || a.Source != "ixp-manager-legacy" || a.Access != "staff" || len(a.Notes)+len(a.Users)+len(a.Preferences) > NoteArchiveRecordLimit || len(a.Warnings) > NoteArchiveRecordLimit {
		return errors.New("invalid bounded staff-only note archive")
	}
	if err := ValidatePatchTimezone(a.SourceTimezone); err != nil {
		return err
	}
	seen := map[string]bool{}
	users := map[int64]bool{}
	var sourceBytes int64
	countFacts := func(facts NoteArchiveFacts) bool {
		for key, raw := range facts {
			sourceBytes += int64(len(key) + len(raw))
			if sourceBytes > NoteArchiveLimit {
				return false
			}
		}
		return true
	}
	identity := func(id, kind string, legacy int64) bool {
		if legacy <= 0 || id != NoteArchiveID(kind, legacy) || seen[id] {
			return false
		}
		seen[id] = true
		return true
	}
	for _, n := range a.Notes {
		if !countFacts(n.Facts) {
			return errors.New("note archive source bytes exceed limit")
		}
		if !identity(n.ID, "source", n.LegacyID) || NoteArchiveSourceID(n.Facts["id"]) != n.LegacyID {
			return errors.New("invalid or duplicate note source identity")
		}
		if err := noteArchiveFacts(n.Facts, "id customer_id title note private created updated created_at updated_at"); err != nil {
			return err
		}
		visibility, known := NoteArchivePrivacy(n.Facts["private"])
		if n.Visibility != visibility || n.PrivacyKnown != known {
			return errors.New("note source privacy contradicts original flag")
		}
	}
	for _, u := range a.Users {
		if !countFacts(u.Facts) {
			return errors.New("note archive source bytes exceed limit")
		}
		if !identity(u.ID, "user", u.LegacyID) || NoteArchiveSourceID(u.Facts["id"]) != u.LegacyID {
			return errors.New("invalid or duplicate note source user")
		}
		if err := noteArchiveFacts(u.Facts, "id username name email disabled created created_at"); err != nil {
			return err
		}
		users[u.LegacyID] = true
	}
	for _, p := range a.Preferences {
		if !countFacts(p.Facts) || !countFacts(p.Notes) {
			return errors.New("note archive source bytes exceed limit")
		}
		switch p.Format {
		case "user.prefs":
			if !identity(p.ID, "modern-pref", p.LegacyID) || p.LegacyUserID != p.LegacyID || !users[p.LegacyUserID] || len(p.Facts) != 0 {
				return errors.New("invalid modern note source association")
			}
			if p.PrefsState != "missing" && p.PrefsState != "null" && p.PrefsState != "empty-array" && p.PrefsState != "object" && p.PrefsState != "json-string" {
				return errors.New("invalid original prefs column presence")
			}
			if (p.PrefsState == "missing" || p.PrefsState == "null" || p.PrefsState == "empty-array") && p.NotesState != "missing" {
				return errors.New("absent original prefs cannot contain notes")
			}
			if p.NotesState != "missing" && p.NotesState != "null" && p.NotesState != "empty-array" && p.NotesState != "object" || p.NotesState != "object" && len(p.Notes) != 0 {
				return errors.New("invalid original notes preference presence")
			}
			if err := noteArchiveModern(p.Notes); err != nil {
				return err
			}
		case "user_pref":
			if !identity(p.ID, "legacy-pref", p.LegacyID) || NoteArchiveSourceID(p.Facts["id"]) != p.LegacyID || p.LegacyUserID != NoteArchiveSourceID(p.Facts["user_id"]) || p.NotesState != "" || p.PrefsState != "" || len(p.Notes) != 0 {
				return errors.New("invalid legacy note preference source")
			}
			if err := noteArchiveFacts(p.Facts, "id user_id attribute ix op value expire"); err != nil {
				return err
			}
			var attribute string
			if json.Unmarshal(p.Facts["attribute"], &attribute) != nil || !NoteArchiveAttribute(attribute) {
				return errors.New("only original notes user_pref attributes are allowed")
			}
		default:
			return errors.New("unknown note preference source format")
		}
	}
	for _, warning := range a.Warnings {
		sourceBytes += int64(len(warning))
		if len(warning) > 4096 || !utf8.ValidString(warning) || sourceBytes > NoteArchiveLimit {
			return errors.New("invalid note archive warning")
		}
	}
	if checksum {
		if !patchHex.MatchString(a.Checksum) || a.Checksum != NoteArchiveChecksum(*a) {
			return errors.New("note source archive checksum mismatch")
		}
	}
	return nil
}

func ValidateNoteArchive(a *NoteArchive) error { return validateNoteArchive(a, true) }

// This checksum covers the selected, canonical source envelope. It is not a
// signature or a digest of the entire original database export.
func NoteArchiveChecksum(a NoteArchive) string {
	a.Checksum = ""
	raw, err := json.Marshal(a)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func EncodeNoteArchive(a NoteArchive) ([]byte, error) {
	if err := validateNoteArchive(&a, false); err != nil {
		return nil, err
	}
	a.Checksum = NoteArchiveChecksum(a)
	raw, err := json.Marshal(a)
	if err != nil || int64(len(raw))+1 > NoteArchiveLimit {
		return nil, errors.New("note source archive exceeds 32 MiB")
	}
	return append(raw, '\n'), nil
}

func ReadNoteArchive(reader io.Reader) (*NoteArchive, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, NoteArchiveLimit+1))
	if err != nil || int64(len(raw)) > NoteArchiveLimit {
		return nil, errors.New("could not read bounded note source archive")
	}
	if err = strictNoteJSON(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var archive NoteArchive
	if err = decoder.Decode(&archive); err != nil {
		return nil, errors.New("invalid note source archive fields")
	}
	if err = ValidateNoteArchive(&archive); err != nil {
		return nil, err
	}
	return &archive, nil
}

// Duplicate keys, hidden second documents, deep nesting and oversized node
// graphs are rejected before decoding into typed source records.
func strictNoteJSON(raw []byte) error {
	return validateNoteJSON(raw, NoteArchiveLimit, 12, 2000000, 256)
}

// ValidateNoteArchiveSourceJSON checks the complete database-export JSON before
// encoding/json can replace malformed Unicode or discard duplicate keys. It
// does not apply the smaller note-package byte or source-domain limits.
func ValidateNoteArchiveSourceJSON(raw []byte) error {
	return validateNoteJSON(raw, NoteArchiveSourceLimit, 32, 8000000, int(NoteArchiveSourceLimit))
}

func validateNoteJSON(raw []byte, byteLimit int64, depthLimit, nodeLimit, keyLimit int) error {
	if !utf8.Valid(raw) || int64(len(raw)) > byteLimit || !noteJSONSurrogatesValid(raw) {
		return errors.New("invalid note archive encoding")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	nodes := 0
	var value func(int) error
	value = func(depth int) error {
		nodes++
		if depth > depthLimit || nodes > nodeLimit {
			return errors.New("note archive JSON structure exceeds bounds")
		}
		token, err := decoder.Token()
		if err != nil {
			return errors.New("invalid note archive JSON")
		}
		if delimiter, ok := token.(json.Delim); ok {
			switch delimiter {
			case '{':
				keys := map[string]bool{}
				for decoder.More() {
					key, err := decoder.Token()
					name, ok := key.(string)
					if err != nil || !ok || len(name) > keyLimit || keys[name] {
						return errors.New("invalid or duplicate note archive key")
					}
					keys[name] = true
					if err := value(depth + 1); err != nil {
						return err
					}
				}
				closing, err := decoder.Token()
				if err != nil || closing != json.Delim('}') {
					return errors.New("incomplete note archive object")
				}
			case '[':
				for decoder.More() {
					if err := value(depth + 1); err != nil {
						return err
					}
				}
				closing, err := decoder.Token()
				if err != nil || closing != json.Delim(']') {
					return errors.New("incomplete note archive array")
				}
			default:
				return errors.New("unexpected note archive delimiter")
			}
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("exactly one note archive JSON document is required")
	}
	return nil
}

// encoding/json replaces lone UTF-16 surrogate escapes with U+FFFD. Reject
// those before decoding, without rejecting a genuine replacement character or
// a literal backslash followed by "u" in the source text.
func noteJSONSurrogatesValid(raw []byte) bool {
	hex4 := func(at int) (uint16, bool) {
		if at+4 > len(raw) {
			return 0, false
		}
		var value uint16
		for _, c := range raw[at : at+4] {
			value <<= 4
			switch {
			case c >= '0' && c <= '9':
				value |= uint16(c - '0')
			case c >= 'a' && c <= 'f':
				value |= uint16(c-'a') + 10
			case c >= 'A' && c <= 'F':
				value |= uint16(c-'A') + 10
			default:
				return 0, false
			}
		}
		return value, true
	}
	inString := false
	for i := 0; i < len(raw); i++ {
		if !inString {
			if raw[i] == '"' {
				inString = true
			}
			continue
		}
		if raw[i] == '"' {
			inString = false
			continue
		}
		if raw[i] != '\\' {
			continue
		}
		i++
		if i >= len(raw) {
			return false
		}
		if raw[i] != 'u' {
			continue // The JSON tokenizer separately validates ordinary escapes.
		}
		value, ok := hex4(i + 1)
		if !ok {
			return false
		}
		i += 4
		if value >= 0xdc00 && value <= 0xdfff {
			return false
		}
		if value >= 0xd800 && value <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			low, ok := hex4(i + 3)
			if !ok || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
