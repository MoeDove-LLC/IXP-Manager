package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strconv"

	"github.com/moedove/ixp-manager/internal/migrate"
)

// Only the actual PHP user and customer_to_users tables establish source
// identities and relationships. Neither legacy user.custid nor email equality
// can invent a membership. All other tables and account secrets are omitted.
func buildAccountArchive(source legacyExport, timezone string) (*migrate.AccountArchive, error) {
	users, usersPresent := source.Tables["user"]
	memberships, membershipsPresent := source.Tables["customer_to_users"]
	if !usersPresent || !membershipsPresent || users == nil || memberships == nil {
		return nil, errors.New("account source requires explicit user and customer_to_users arrays, including empty arrays")
	}
	for _, alias := range []string{"users", "customer_to_user", "customerToUsers"} {
		if _, present := source.Tables[alias]; present {
			return nil, errors.New("unrecognized account table alias; supply the original user and customer_to_users schema")
		}
	}
	if len(users)+len(memberships) > migrate.AccountArchiveRecordLimit {
		return nil, errors.New("account source record capacity exceeded")
	}
	if timezone == "" {
		timezone = "unknown"
	}
	a := migrate.AccountArchive{Version: 1, Kind: migrate.AccountArchiveKind, Source: "ixp-manager-legacy", Access: "admin", SourceTimezone: timezone, Users: []migrate.AccountArchiveUser{}, Memberships: []migrate.AccountArchiveMembership{}}
	for _, record := range users {
		facts, err := noteArchiveRawFacts(record, "id username name email disabled created created_at custid")
		if err != nil {
			return nil, errors.New("invalid selected source user facts")
		}
		id, err := accountSourceID(facts, "id")
		if err != nil {
			return nil, errors.New("user requires an exact positive source ID")
		}
		if migrate.NoteArchiveSourceID(facts["custid"]) != 0 {
			_, _ = accountSourceID(facts, "custid")
		}
		a.Users = append(a.Users, migrate.AccountArchiveUser{LegacyID: id, Facts: facts})
	}
	for _, record := range memberships {
		facts, err := noteArchiveRawFacts(record, "id user_id customer_id privs created_at")
		if err != nil {
			return nil, errors.New("invalid selected source membership facts")
		}
		id, e1 := accountSourceID(facts, "id")
		user, e2 := accountSourceID(facts, "user_id")
		customer, e3 := accountSourceID(facts, "customer_id")
		if e1 != nil || e2 != nil || e3 != nil {
			return nil, errors.New("customer_to_users requires exact positive row, user and customer source IDs")
		}
		a.Memberships = append(a.Memberships, migrate.AccountArchiveMembership{LegacyID: id, SourceUserID: user, SourceCustomerID: customer, Facts: facts})
	}
	sort.Slice(a.Users, func(i, j int) bool { return a.Users[i].LegacyID < a.Users[j].LegacyID })
	sort.Slice(a.Memberships, func(i, j int) bool { return a.Memberships[i].LegacyID < a.Memberships[j].LegacyID })
	raw, err := migrate.EncodeAccountArchive(a)
	if err != nil {
		return nil, err
	}
	return migrate.ReadAccountArchive(bytes.NewReader(raw))
}

func accountSourceID(facts migrate.NoteArchiveFacts, key string) (string, error) {
	id := migrate.NoteArchiveSourceID(facts[key])
	if id == 0 {
		return "", errors.New("exact positive source ID required")
	}
	text := strconv.FormatInt(id, 10)
	facts[key], _ = json.Marshal(text)
	return text, nil
}
