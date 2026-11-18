package app

import (
	"strconv"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/migrate"
)

func TestLegacyAuditArchiveIsPrivateHistoricalAndPersistent(t *testing.T) {
	c, dir := setupClient(t)
	archive := migrate.LegacyAuditArchive{
		Version: 1, Kind: migrate.LegacyAuditArchiveKind, Source: "previous-exchange", SourceTimezone: "unknown",
	}
	for id := 51; id > 0; id-- {
		archive.Entries = append(archive.Entries, migrate.LegacyAuditEntry{
			ID: strconv.Itoa(id), UserID: "9007199254740993", Model: "Router", ModelID: "10",
			Action: "UPDATED", CreatedAt: "2021-03-13 01:20:00",
		})
	}
	raw, err := migrate.EncodeLegacyAuditArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	before := c.a.store.Snapshot()
	upload := "/api/import/audit-archive"
	w := legacyNotesBodyRequest(c, "POST", upload, raw, nil)
	if w.Code != 201 {
		t.Fatalf("archive import failed: %d %s", w.Code, w.Body.String())
	}
	imported := responseRecord(t, w)["batch"].(map[string]any)
	checksum := imported["checksum"].(string)
	path := "/api/audit/legacy/" + checksum
	first := c.ok("GET", path, nil)
	last := c.ok("GET", path+"?offset=50", nil)
	if first["nextOffset"] != float64(50) || len(first["entries"].([]any)) != 50 || len(last["entries"].([]any)) != 1 || last["nextOffset"] != nil {
		t.Fatal("archive pagination mismatch")
	}
	entry := last["entries"].([]any)[0].(map[string]any)
	if entry["id"] != "1" || entry["userId"] != "9007199254740993" || entry["createdAt"] != "2021-03-13 01:20:00" {
		t.Fatal("archive rewrote source metadata", entry)
	}
	if got := c.a.store.Snapshot().Audit; len(got) != len(before.Audit)+1 || got[len(got)-1].Action != "import-legacy-audit" {
		t.Fatal("legacy entries were confused with the live audit stream")
	}
	if w := legacyNotesBodyRequest(c, "POST", upload, raw, nil); w.Code != 409 {
		t.Fatal("identical archive was imported again", w.Code)
	}
	archive.Entries = archive.Entries[50:]
	archive.Entries[0].Action = "DELETED"
	overlap, err := migrate.EncodeLegacyAuditArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	if w := legacyNotesBodyRequest(c, "POST", upload, overlap, nil); w.Code != 409 {
		t.Fatal("overlapping source ID could be imported in a new batch", w.Code)
	}
	malformed := []byte(strings.Replace(string(raw), `"model": "Router"`, `"model": "Router", "message": "untrusted detail"`, 1))
	if w := legacyNotesBodyRequest(c, "POST", upload, malformed, nil); w.Code != 400 {
		t.Fatal("archive accepted raw log details", w.Code)
	}
	viewer := noteTestUser(t, c, "viewer", "legacy-audit-viewer@example.test", nil)
	for _, url := range []string{"/api/audit/legacy", path} {
		if w := viewer.request("GET", url, nil); w.Code != 403 {
			t.Fatalf("non-admin read private historical data: %d", w.Code)
		}
	}
	if w := c.request("GET", "/api/export", nil); w.Code != 200 || strings.Contains(w.Body.String(), "9007199254740993") {
		t.Fatal("historical data entered resource export", w.Code)
	}
	if err := c.a.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	saved := store.Snapshot().LegacyAudit
	if len(saved) != 1 || saved[0].Archive.Checksum != checksum {
		t.Fatal("legacy audit history was not preserved across restart")
	}
}
