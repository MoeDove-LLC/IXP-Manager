package migrate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/mail"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const AccountArchiveKind = "ixp-manager-account-sources"
const AccountArchiveLimit int64 = 32 << 20
const AccountArchiveRecordLimit = 100000

// These are selected, private source facts, not current accounts or grants.
// Source IDs are strings even inside Facts so browser JSON cannot round them.
type AccountArchiveUser struct {
	LegacyID string           `json:"legacyId"`
	Facts    NoteArchiveFacts `json:"facts"`
}
type AccountArchiveMembership struct {
	LegacyID         string           `json:"legacyId"`
	SourceUserID     string           `json:"sourceUserId"`
	SourceCustomerID string           `json:"sourceCustomerId"`
	Facts            NoteArchiveFacts `json:"facts"`
}
type AccountArchive struct {
	Version        int                        `json:"version"`
	Kind           string                     `json:"kind"`
	Source         string                     `json:"source"`
	Access         string                     `json:"access"`
	SourceTimezone string                     `json:"sourceTimezone"`
	Checksum       string                     `json:"checksum"`
	Users          []AccountArchiveUser       `json:"users"`
	Memberships    []AccountArchiveMembership `json:"memberships"`
}

func accountFactString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

// AccountArchiveID accepts only exact positive signed-64-bit decimal IDs.
func AccountArchiveID(id string) bool {
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == id
}

func accountFacts(facts NoteArchiveFacts, allowed string) error {
	if facts == nil {
		return errors.New("account source facts are required")
	}
	keys := map[string]bool{}
	for _, key := range strings.Fields(allowed) {
		keys[key] = true
	}
	for key, raw := range facts {
		if !keys[key] {
			return errors.New("unrecognized account source fact")
		}
		if _, err := noteArchiveScalar(raw, 4096); err != nil {
			return errors.New("account source facts must be bounded scalars")
		}
	}
	return nil
}

func validateAccountArchive(a *AccountArchive, checksum bool) error {
	if a == nil || a.Version != 1 || a.Kind != AccountArchiveKind || a.Source != "ixp-manager-legacy" || a.Access != "admin" || a.SourceTimezone == "" || a.Users == nil || a.Memberships == nil || len(a.Users)+len(a.Memberships) == 0 || len(a.Users)+len(a.Memberships) > AccountArchiveRecordLimit {
		return errors.New("invalid bounded account source envelope")
	}
	if err := ValidatePatchTimezone(a.SourceTimezone); err != nil {
		return err
	}
	users, memberships := map[string]bool{}, map[string]bool{}
	for _, u := range a.Users {
		if !AccountArchiveID(u.LegacyID) || users[u.LegacyID] || accountFactString(u.Facts["id"]) != u.LegacyID || accountFacts(u.Facts, "id username name email disabled created created_at custid") != nil {
			return errors.New("invalid or duplicate source user identity or facts")
		}
		users[u.LegacyID] = true
		// An optional legacy default customer never substitutes for c2u rows.
		if raw, ok := u.Facts["custid"]; ok && NoteArchiveSourceID(raw) != 0 && !AccountArchiveID(accountFactString(raw)) {
			return errors.New("source default customer ID must be an exact string")
		}
	}
	for _, m := range a.Memberships {
		if !AccountArchiveID(m.LegacyID) || memberships[m.LegacyID] || !AccountArchiveID(m.SourceUserID) || !AccountArchiveID(m.SourceCustomerID) || accountFacts(m.Facts, "id user_id customer_id privs created_at") != nil || accountFactString(m.Facts["id"]) != m.LegacyID || accountFactString(m.Facts["user_id"]) != m.SourceUserID || accountFactString(m.Facts["customer_id"]) != m.SourceCustomerID {
			return errors.New("invalid or duplicate source membership identity or facts")
		}
		memberships[m.LegacyID] = true
	}
	raw, err := json.Marshal(a)
	if err != nil || int64(len(raw))+1 > AccountArchiveLimit {
		return errors.New("account source archive exceeds 32 MiB")
	}
	if checksum && (!patchHex.MatchString(a.Checksum) || a.Checksum != AccountArchiveChecksum(*a)) {
		return errors.New("account source archive checksum mismatch")
	}
	return nil
}

func ValidateAccountArchive(a *AccountArchive) error { return validateAccountArchive(a, true) }
func AccountArchiveChecksum(a AccountArchive) string {
	a.Checksum = ""
	raw, err := json.Marshal(a)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func EncodeAccountArchive(a AccountArchive) ([]byte, error) {
	if err := validateAccountArchive(&a, false); err != nil {
		return nil, err
	}
	a.Checksum = AccountArchiveChecksum(a)
	raw, err := json.Marshal(a)
	if err != nil || int64(len(raw))+1 > AccountArchiveLimit {
		return nil, errors.New("account source archive exceeds 32 MiB")
	}
	return append(raw, '\n'), nil
}
func ReadAccountArchive(reader io.Reader) (*AccountArchive, error) {
	raw, err := io.ReadAll(io.LimitReader(reader, AccountArchiveLimit+1))
	if err != nil || int64(len(raw)) > AccountArchiveLimit {
		return nil, errors.New("could not read bounded account source archive")
	}
	if err = validateNoteJSON(raw, AccountArchiveLimit, 8, 2000000, 128); err != nil {
		return nil, errors.New("account source archive must be unambiguous valid JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var archive AccountArchive
	if decoder.Decode(&archive) != nil {
		return nil, errors.New("invalid account source archive fields")
	}
	if err = ValidateAccountArchive(&archive); err != nil {
		return nil, err
	}
	// encoding/json accepts case-insensitive struct field aliases. The archive
	// contract does not: require exactly the known spellings at every level.
	var envelope map[string]json.RawMessage
	_ = json.Unmarshal(raw, &envelope)
	if !accountExactKeys(envelope, "version kind source access sourceTimezone checksum users memberships") {
		return nil, errors.New("noncanonical account source envelope fields")
	}
	for _, group := range []struct{ key, fields string }{{"users", "legacyId facts"}, {"memberships", "legacyId sourceUserId sourceCustomerId facts"}} {
		var rows []map[string]json.RawMessage
		if json.Unmarshal(envelope[group.key], &rows) != nil {
			return nil, errors.New("invalid account source rows")
		}
		for _, row := range rows {
			if !accountExactKeys(row, group.fields) {
				return nil, errors.New("noncanonical account source row fields")
			}
		}
	}
	return &archive, nil
}
func accountExactKeys(values map[string]json.RawMessage, fields string) bool {
	keys := strings.Fields(fields)
	if len(values) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, found := values[key]; !found {
			return false
		}
	}
	return true
}

// State is evidence only. Unknown and disabled must never silently activate.
func AccountArchiveUserState(u AccountArchiveUser) string {
	value, _ := noteArchiveScalar(u.Facts["disabled"], 128)
	switch v := value.(type) {
	case bool:
		if v {
			return "disabled"
		}
		return "active"
	case string:
		if v == "0" {
			return "active"
		}
		if v == "1" {
			return "disabled"
		}
	case json.Number:
		if v == "0" {
			return "active"
		}
		if v == "1" {
			return "disabled"
		}
	}
	return "unknown"
}
func AccountArchiveEmail(u AccountArchiveUser) string {
	value := strings.TrimSpace(accountFactString(u.Facts["email"]))
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || len(value) > 254 || strings.ContainsAny(value, "\r\n") {
		return ""
	}
	return strings.ToLower(value)
}
func AccountArchivePrivilege(m AccountArchiveMembership) string {
	value, _ := noteArchiveScalar(m.Facts["privs"], 128)
	var code string
	switch v := value.(type) {
	case string:
		code = v
	case json.Number:
		code = string(v)
	}
	switch code {
	case "0":
		return "public"
	case "1":
		return "member"
	case "2":
		return "member-admin"
	case "3":
		return "superuser"
	}
	return "unknown"
}

// Creation returns only an unambiguous source instant. Missing, date-only,
// conflicting columns, zero dates, unknown zones, and DST folds/gaps stay raw.
// This proves what the export says, never the identity of a current account.
func AccountArchiveCreation(facts NoteArchiveFacts, timezone string) (string, bool) {
	var result time.Time
	for _, field := range []string{"created", "created_at"} {
		raw, exists := facts[field]
		if !exists {
			continue
		}
		value := accountFactString(raw)
		parsed, ok := accountSourceTime(value, timezone)
		if !ok || (!result.IsZero() && !result.Equal(parsed)) {
			return "", false
		}
		result = parsed
	}
	if result.IsZero() {
		return "", false
	}
	return result.UTC().Format(time.RFC3339Nano), true
}

var accountRFC3339Time = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,9})?(?:Z|[+-](?:[01][0-9]|2[0-3]):[0-5][0-9])$`)
var accountSQLTime = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,6})?$`)

func accountSourceTime(value, timezone string) (time.Time, bool) {
	if accountRFC3339Time.MatchString(value) {
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil && parsed.Year() >= 1 {
			return parsed, true
		}
	}
	if !accountSQLTime.MatchString(value) || timezone == "" || timezone == "unknown" || ValidatePatchTimezone(timezone) != nil {
		return time.Time{}, false
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, false
	}
	// Original SQL DATETIME precision, including optional microseconds.
	layout := "2006-01-02 15:04:05"
	wall, err := time.Parse(layout, value)
	if err != nil || wall.Year() < 1 {
		return time.Time{}, false
	}
	parsed, err := time.ParseInLocation(layout, value, location)
	if err != nil {
		return time.Time{}, false
	}
	offsets := map[int]bool{}
	_, offset := parsed.Zone()
	offsets[offset] = true
	start, end := parsed.ZoneBounds()
	for _, bound := range []time.Time{start.Add(-time.Second), end} {
		if bound.Year() > 1 && bound.Year() < 10000 {
			_, offset = bound.In(location).Zone()
			offsets[offset] = true
		}
	}
	count := 0
	var match time.Time
	for offset := range offsets {
		candidate := wall.Add(-time.Duration(offset) * time.Second).In(location)
		if candidate.Format("2006-01-02 15:04:05.999999999") == wall.Format("2006-01-02 15:04:05.999999999") {
			count++
			match = candidate
		}
	}
	return match, count == 1
}

type AccountArchiveFinding struct {
	Entity   string `json:"entity"`
	LegacyID string `json:"legacyId"`
	Code     string `json:"code"`
}

// Review reports bounded, fixed codes, never source emails or unknown values.
// Even a row with no findings still requires explicit current-entity mapping,
// administrator authority and the recipient's invitation acceptance.
func AccountArchiveReview(a *AccountArchive) []AccountArchiveFinding {
	findings := []AccountArchiveFinding{}
	if a == nil {
		return findings
	}
	add := func(entity, id, code string) { findings = append(findings, AccountArchiveFinding{entity, id, code}) }
	users, emails, pairs, counts := map[string]AccountArchiveUser{}, map[string]int{}, map[string]int{}, map[string]int{}
	for _, u := range a.Users {
		users[u.LegacyID] = u
		if email := AccountArchiveEmail(u); email != "" {
			emails[email]++
		}
	}
	for _, m := range a.Memberships {
		pairs[m.SourceUserID+":"+m.SourceCustomerID]++
		counts[m.SourceUserID]++
	}
	for _, u := range a.Users {
		if state := AccountArchiveUserState(u); state != "active" {
			add("user", u.LegacyID, "state-"+state)
		}
		email := AccountArchiveEmail(u)
		if email == "" {
			add("user", u.LegacyID, "email-unusable")
		} else if emails[email] > 1 {
			add("user", u.LegacyID, "email-collision")
		}
		if strings.TrimSpace(accountFactString(u.Facts["name"])) == "" {
			add("user", u.LegacyID, "name-unusable")
		}
		if _, ok := AccountArchiveCreation(u.Facts, a.SourceTimezone); !ok {
			add("user", u.LegacyID, "creation-unproven")
		}
		if counts[u.LegacyID] == 0 {
			add("user", u.LegacyID, "memberships-missing")
		}
		if raw, exists := u.Facts["custid"]; exists {
			id := accountFactString(raw)
			if !AccountArchiveID(id) || pairs[u.LegacyID+":"+id] == 0 {
				add("user", u.LegacyID, "default-membership-unresolved")
			}
		}
	}
	for _, m := range a.Memberships {
		u, exists := users[m.SourceUserID]
		if !exists {
			add("membership", m.LegacyID, "user-unresolved")
		} else if state := AccountArchiveUserState(u); state != "active" {
			add("membership", m.LegacyID, "user-state-"+state)
		}
		if pairs[m.SourceUserID+":"+m.SourceCustomerID] != 1 {
			add("membership", m.LegacyID, "membership-duplicate")
		}
		switch role := AccountArchivePrivilege(m); role {
		case "unknown", "superuser", "public":
			add("membership", m.LegacyID, "privilege-"+role)
		}
		if _, ok := AccountArchiveCreation(m.Facts, a.SourceTimezone); !ok {
			add("membership", m.LegacyID, "creation-unproven")
		}
	}
	sort.Slice(findings, func(i, j int) bool {
		left, right := findings[i], findings[j]
		if left.Entity != right.Entity {
			return left.Entity < right.Entity
		}
		if left.LegacyID != right.LegacyID {
			return left.LegacyID < right.LegacyID
		}
		return left.Code < right.Code
	})
	return findings
}
