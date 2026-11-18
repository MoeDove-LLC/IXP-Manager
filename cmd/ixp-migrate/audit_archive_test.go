package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/migrate"
)

func TestLegacyAuditArchiveLimitsOutputToHistoricalMetadata(t *testing.T) {
	raw := `{"tables":{"log":[
		{"id":2,"user_id":null,"model_id":null,"model":"hidden-model-field-MUST-NOT-LEAK","action":"CUSTOM","message":"fixture-message-key-MUST-NOT-LEAK","models":{"old":{"secret":"fixture-old-key-MUST-NOT-LEAK"}},"created_at":null},
		{"id":18446744073709551615,"user_id":9007199254740993,"model_id":99,"model":"Router","action":"UPDATED","message":"fixture-router-key-MUST-NOT-LEAK","models":{"new":{"password":"fixture-new-key-MUST-NOT-LEAK"}},"created_at":"2021-03-13 01:20:00"}
	]}}`
	path := filepath.Join(t.TempDir(), "audit.json")
	var stderr bytes.Buffer
	args := []string{"--audit-archive", "--audit-source", "old-database", "--output", path}
	if err := run(args, strings.NewReader(raw), io.Discard, &stderr); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded)+stderr.String(), "MUST-NOT-LEAK") {
		t.Fatal("legacy model data or free text reached output")
	}
	var archive migrate.LegacyAuditArchive
	if err := json.Unmarshal(encoded, &archive); err != nil || migrate.ValidateLegacyAuditArchive(archive) != nil {
		t.Fatal("invalid archive", err)
	}
	if len(archive.Entries) != 2 || archive.Entries[0].ID != "18446744073709551615" || archive.Entries[0].UserID != "9007199254740993" || archive.Entries[0].CreatedAt != "2021-03-13 01:20:00" || archive.SourceTimezone != "unknown" || archive.Entries[1].Model != "Unknown" {
		t.Fatal("source metadata lost", archive)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("archive output is not private", err)
	}
	if err := run(args, strings.NewReader(raw), io.Discard, &stderr); err == nil {
		t.Fatal("archive overwrote an existing output")
	}
}
