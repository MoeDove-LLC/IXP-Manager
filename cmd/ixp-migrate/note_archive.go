package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/moedove/ixp-manager/internal/migrate"
)

// This path deliberately bypasses convert(): ordinary notes resources trim
// text and omit empty/orphan rows, while this archive preserves source values.
func buildNoteArchive(source legacyExport, timezone string) (*migrate.NoteArchive, error) {
	if source.Tables == nil {
		return nil, errors.New("required object tables is missing")
	}
	a := migrate.NoteArchive{Version: 1, Kind: migrate.NoteArchiveKind, Source: "ixp-manager-legacy", Access: "staff", SourceTimezone: timezone, Notes: []migrate.NoteArchiveNote{}, Users: []migrate.NoteArchiveUser{}, Preferences: []migrate.NoteArchivePreference{}, Warnings: []string{}}
	if err := migrate.ValidatePatchTimezone(timezone); err != nil {
		return nil, err
	}
	if timezone == "" || timezone == "unknown" {
		a.Warnings = append(a.Warnings, "note archive: source timezone is unknown; original date values are retained without inventing timestamps")
	}
	for _, record := range source.Tables["cust_notes"] {
		facts, err := noteArchiveRawFacts(record, "id customer_id title note private created updated created_at updated_at")
		if err != nil {
			return nil, fmt.Errorf("cust_notes: %w", err)
		}
		id := migrate.NoteArchiveSourceID(facts["id"])
		if id == 0 {
			return nil, errors.New("cust_notes: source id must be a positive signed-64-bit integer")
		}
		visibility, known := migrate.NoteArchivePrivacy(facts["private"])
		a.Notes = append(a.Notes, migrate.NoteArchiveNote{ID: migrate.NoteArchiveID("source", id), LegacyID: id, Facts: facts, Visibility: visibility, PrivacyKnown: known})
		if !known {
			a.Warnings = append(a.Warnings, fmt.Sprintf("cust_notes id %d: missing or unknown source privacy remains internal", id))
		}
		if migrate.NoteArchiveSourceID(facts["customer_id"]) == 0 {
			a.Warnings = append(a.Warnings, fmt.Sprintf("cust_notes id %d: source member reference is unresolved; no current member grant is created", id))
		}
	}
	for _, record := range source.Tables["user"] {
		facts, err := noteArchiveRawFacts(record, "id username name email disabled created created_at")
		if err != nil {
			return nil, fmt.Errorf("user source label: %w", err)
		}
		id := migrate.NoteArchiveSourceID(facts["id"])
		if id == 0 {
			return nil, errors.New("user: source id must be a positive signed-64-bit integer")
		}
		a.Users = append(a.Users, migrate.NoteArchiveUser{ID: migrate.NoteArchiveID("user", id), LegacyID: id, Facts: facts})
		p := migrate.NoteArchivePreference{ID: migrate.NoteArchiveID("modern-pref", id), Format: "user.prefs", LegacyID: id, LegacyUserID: id, NotesState: "missing", PrefsState: "missing", Facts: migrate.NoteArchiveFacts{}}
		prefs, present := record["prefs"]
		if present {
			p.PrefsState = "object"
			if prefs == nil {
				p.PrefsState = "null"
			} else {
				var raw []byte
				if text, ok := prefs.(string); ok {
					p.PrefsState, raw = "json-string", []byte(text)
				} else {
					raw, err = json.Marshal(prefs)
					if err != nil {
						return nil, errors.New("user.prefs could not be represented as JSON")
					}
				}
				object, err := noteArchiveObject(raw)
				if noteArchiveEmptyArray(raw) {
					object, err = map[string]json.RawMessage{}, nil
					if p.PrefsState == "object" {
						p.PrefsState = "empty-array"
					}
				}
				if err != nil {
					return nil, fmt.Errorf("user id %d: malformed prefs JSON cannot be safely filtered", id)
				}
				if notes, found := object["notes"]; found {
					if bytes.Equal(bytes.TrimSpace(notes), []byte("null")) {
						p.NotesState = "null"
					} else if noteArchiveEmptyArray(notes) {
						p.NotesState = "empty-array"
					} else {
						original, err := noteArchiveObject(notes)
						if err != nil || original == nil {
							return nil, fmt.Errorf("user id %d: notes preferences must be an object or null", id)
						}
						p.NotesState, p.Notes = "object", migrate.NoteArchiveFacts{}
						for key, value := range original {
							if strings.Contains(" global_notifs customer_watching note_watching last_read read_upto ", " "+key+" ") {
								p.Notes[key] = append(json.RawMessage(nil), value...)
							} else {
								a.Warnings = append(a.Warnings, fmt.Sprintf("user id %d: unrecognized notes preference field omitted; no other preference branch is exported", id))
							}
						}
					}
				}
			}
		}
		var mode string
		_ = json.Unmarshal(p.Notes["global_notifs"], &mode)
		if mode != "none" && mode != "watched" && mode != "all" {
			a.Warnings = append(a.Warnings, fmt.Sprintf("user id %d: missing/default/unknown notification mode remains source-only; old UI and delivery defaults differed", id))
		}
		a.Preferences = append(a.Preferences, p)
	}
	for _, record := range source.Tables["user_pref"] {
		attribute, ok := record["attribute"].(string)
		if !ok || !migrate.NoteArchiveAttribute(attribute) {
			if ok && strings.HasPrefix(attribute, "customer-notes.") {
				a.Warnings = append(a.Warnings, "user_pref: unrecognized notes attribute omitted; review the original source without treating it as authorization")
			}
			continue // A strict whitelist prevents other account preferences leaking.
		}
		facts, err := noteArchiveRawFacts(record, "id user_id attribute ix op value expire")
		if err != nil {
			return nil, fmt.Errorf("user_pref notes source: %w", err)
		}
		id := migrate.NoteArchiveSourceID(facts["id"])
		if id == 0 {
			return nil, errors.New("user_pref: source id must be a positive signed-64-bit integer")
		}
		user := migrate.NoteArchiveSourceID(facts["user_id"])
		a.Preferences = append(a.Preferences, migrate.NoteArchivePreference{ID: migrate.NoteArchiveID("legacy-pref", id), Format: "user_pref", LegacyID: id, LegacyUserID: user, Facts: facts})
		if user == 0 {
			a.Warnings = append(a.Warnings, fmt.Sprintf("user_pref id %d: source user association unresolved; no current account is inferred", id))
		}
	}
	if len(a.Preferences) > 0 {
		a.Warnings = append(a.Warnings, "note subscriptions and read cursors are immutable source records only; old account IDs, email addresses and timestamps do not authorize current subscriptions or mark current notes read")
	}
	if len(a.Notes)+len(a.Users)+len(a.Preferences) == 0 {
		return nil, errors.New("note archive requires original notes or notes preference source records")
	}
	sort.Slice(a.Notes, func(i, j int) bool { return a.Notes[i].LegacyID < a.Notes[j].LegacyID })
	sort.Slice(a.Users, func(i, j int) bool { return a.Users[i].LegacyID < a.Users[j].LegacyID })
	sort.Slice(a.Preferences, func(i, j int) bool { return a.Preferences[i].ID < a.Preferences[j].ID })
	sort.Strings(a.Warnings)
	raw, err := migrate.EncodeNoteArchive(a)
	if err != nil {
		return nil, err
	}
	return migrate.ReadNoteArchive(bytes.NewReader(raw))
}

func noteArchiveRawFacts(record row, fields string) (migrate.NoteArchiveFacts, error) {
	facts := migrate.NoteArchiveFacts{}
	for _, key := range strings.Fields(fields) {
		if value, present := record[key]; present {
			raw, err := json.Marshal(value)
			if err != nil {
				return nil, errors.New("source scalar could not be represented")
			}
			facts[key] = raw
		}
	}
	return facts, nil
}

func noteArchiveEmptyArray(raw []byte) bool {
	if int64(len(raw)) > migrate.NoteArchiveLimit {
		return false
	}
	var compact bytes.Buffer
	return json.Compact(&compact, raw) == nil && compact.String() == "[]"
}

// Token decoding catches duplicate keys in JSON stored inside the original
// prefs string before a map could silently discard a conflicting value.
func noteArchiveObject(raw []byte) (map[string]json.RawMessage, error) {
	if len(raw) == 0 || int64(len(raw)) > migrate.NoteArchiveLimit {
		return nil, errors.New("invalid bounded source preference object")
	}
	if err := migrate.ValidateNoteArchiveSourceJSON(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if start == nil {
		if _, err = decoder.Token(); err == io.EOF {
			return nil, nil
		}
		return nil, errors.New("trailing source preference JSON")
	}
	if start != json.Delim('{') {
		return nil, errors.New("source preference object required")
	}
	out := map[string]json.RawMessage{}
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok || len(name) > 256 || out[name] != nil {
			return nil, errors.New("invalid or duplicate source preference key")
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, errors.New("invalid source preference field")
		}
		out[name] = value
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return nil, errors.New("incomplete source preference object")
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, errors.New("trailing source preference JSON")
	}
	return out, nil
}
