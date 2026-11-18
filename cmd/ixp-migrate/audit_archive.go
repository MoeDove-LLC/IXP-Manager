package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/moedove/ixp-manager/internal/migrate"
)

func legacyAuditID(value any, optional bool) (string, bool) {
	if value == nil && optional {
		return "", true
	}
	var raw string
	switch value := value.(type) {
	case string:
		raw = value
	case json.Number:
		raw = value.String()
	default:
		return "", false
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	return raw, err == nil && n > 0 && strconv.FormatUint(n, 10) == raw
}

func legacyAuditTimestamp(value any) (string, bool) {
	if value == nil {
		return "", true
	}
	s, ok := value.(string)
	return s, ok && s != ""
}

func buildLegacyAuditArchive(source legacyExport, origin, timezone string) ([]byte, error) {
	rows := source.Tables["log"]
	if len(rows) == 0 || len(rows) > migrate.MaxLegacyAuditArchiveRows {
		return nil, errors.New("audit archive requires 1 to 10000 rows from the legacy log table")
	}
	archive := migrate.LegacyAuditArchive{
		Version: 1, Kind: migrate.LegacyAuditArchiveKind, Source: origin, SourceTimezone: timezone,
		Entries: make([]migrate.LegacyAuditEntry, 0, len(rows)),
	}
	for i, row := range rows {
		id, idOK := legacyAuditID(row["id"], false)
		user, userOK := legacyAuditID(row["user_id"], true)
		modelID, modelOK := legacyAuditID(row["model_id"], true)
		created, createdOK := legacyAuditTimestamp(row["created_at"])
		updated, updatedOK := legacyAuditTimestamp(row["updated_at"])
		if !idOK || !userOK || !modelOK || !createdOK || !updatedOK {
			return nil, fmt.Errorf("invalid identity or timestamp in log row at position %d", i+1)
		}
		model, _ := row["model"].(string)
		action, _ := row["action"].(string)
		archive.Entries = append(archive.Entries, migrate.LegacyAuditEntry{
			ID: id, UserID: user, ModelID: modelID, Model: migrate.LegacyAuditModel(model),
			Action: migrate.LegacyAuditAction(action), CreatedAt: created, UpdatedAt: updated,
		})
	}
	sort.Slice(archive.Entries, func(i, j int) bool {
		a, b := archive.Entries[i].ID, archive.Entries[j].ID
		if len(a) != len(b) {
			return len(a) > len(b)
		}
		return a > b
	})
	return migrate.EncodeLegacyAuditArchive(archive)
}
