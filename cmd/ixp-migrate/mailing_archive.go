package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/moedove/ixp-manager/internal/migrate"
)

func buildMailingArchive(source legacyExport) (*migrate.MailingArchive, error) {
	a := migrate.MailingArchive{Version: 1, Kind: migrate.MailingArchiveKind, Source: "ixp-manager-legacy", Access: "admin", Users: []migrate.MailingArchiveUser{}, Choices: []migrate.MailingArchiveChoice{}}
	if source.Tables == nil {
		return nil, errors.New("required object tables is missing")
	}
	if len(source.Tables["users"]) > 0 || len(source.Tables["user_preferences"]) > 0 {
		return nil, errors.New("unrecognized users/user_preferences schema; use the original user/user_pref tables without inferred field mappings")
	}
	if len(source.Tables["user"]) > migrate.MailingArchiveRecordLimit {
		return nil, errors.New("mailing source user capacity exceeded")
	}
	seenUsers := map[string]bool{}
	for _, record := range source.Tables["user"] {
		facts, err := noteArchiveRawFacts(record, "id username name email disabled created created_at")
		if err != nil {
			return nil, err
		}
		id := migrate.NoteArchiveSourceID(facts["id"])
		if id == 0 {
			return nil, errors.New("user source id must be a positive signed-64-bit integer")
		}
		userID := strconv.FormatInt(id, 10)
		if seenUsers[userID] {
			return nil, errors.New("duplicate mailing source user identity")
		}
		seenUsers[userID] = true
		a.Users = append(a.Users, migrate.MailingArchiveUser{LegacyID: userID, Facts: facts})
		prefs, found := record["prefs"]
		if !found || prefs == nil {
			continue
		}
		raw, err := json.Marshal(prefs)
		if value, ok := prefs.(string); ok {
			raw = []byte(value)
		}
		if err != nil {
			return nil, errors.New("could not represent source prefs")
		}
		if noteArchiveEmptyArray(raw) {
			continue
		}
		object, err := noteArchiveObject(raw)
		if err != nil {
			return nil, fmt.Errorf("user %s: malformed preferences cannot be safely filtered", userID)
		}
		branch, found := object["mailinglist"]
		if !found || bytes.Equal(bytes.TrimSpace(branch), []byte("null")) || noteArchiveEmptyArray(branch) {
			continue
		}
		choices, err := noteArchiveObject(branch)
		if err != nil || choices == nil {
			return nil, fmt.Errorf("user %s: mailinglist must be an object, null or empty array", userID)
		}
		if len(a.Choices)+len(choices)+len(a.Users) > migrate.MailingArchiveRecordLimit {
			return nil, errors.New("mailing source record capacity exceeded")
		}
		for key, value := range choices {
			a.Choices = append(a.Choices, migrate.MailingArchiveChoice{ID: "user-" + userID + "-" + key, SourceUserID: userID, SourceKey: key, Format: "user.prefs", Value: value})
		}
	}
	for _, record := range source.Tables["user_pref"] {
		attribute, ok := record["attribute"].(string)
		if !ok || !strings.HasPrefix(attribute, "mailinglist") {
			continue
		}
		key := strings.TrimPrefix(attribute, "mailinglist.")
		if attribute == key || !migrate.MailingArchiveKey(key) {
			return nil, errors.New("unrecognized mailinglist attribute requires explicit source correction")
		}
		facts, err := noteArchiveRawFacts(record, "id user_id attribute ix op value expire")
		if err != nil {
			return nil, err
		}
		id, user := migrate.NoteArchiveSourceID(facts["id"]), migrate.NoteArchiveSourceID(facts["user_id"])
		if id == 0 || user == 0 {
			return nil, errors.New("mailing user_pref requires positive source row and user IDs")
		}
		if len(a.Choices)+len(a.Users) >= migrate.MailingArchiveRecordLimit {
			return nil, errors.New("mailing source record capacity exceeded")
		}
		a.Choices = append(a.Choices, migrate.MailingArchiveChoice{ID: "pref-" + strconv.FormatInt(id, 10), SourceUserID: strconv.FormatInt(user, 10), SourceKey: key, Format: "user_pref", Value: facts["value"], Facts: facts})
	}
	usedUsers := map[string]bool{}
	for _, choice := range a.Choices {
		usedUsers[choice.SourceUserID] = true
	}
	selectedUsers := []migrate.MailingArchiveUser{}
	for _, user := range a.Users {
		if usedUsers[user.LegacyID] {
			selectedUsers = append(selectedUsers, user)
		}
	}
	a.Users = selectedUsers
	sort.Slice(a.Users, func(i, j int) bool { return a.Users[i].LegacyID < a.Users[j].LegacyID })
	sort.Slice(a.Choices, func(i, j int) bool { return a.Choices[i].ID < a.Choices[j].ID })
	raw, err := migrate.EncodeMailingArchive(a)
	if err != nil {
		return nil, err
	}
	return migrate.ReadMailingArchive(bytes.NewReader(raw))
}
