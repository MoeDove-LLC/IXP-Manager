package backup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/app"
	"github.com/moedove/ixp-manager/internal/migrate"
)

func TestLegacyMailingBackupPreservesEvidenceReceiptsAndRejectsOldSession(t *testing.T) {
	directory := t.TempDir()
	a, err := app.New(app.Config{DataDir: directory, SetupToken: "mailing-source-backup"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	c := &provisionBackupClient{t: t, a: a}
	c.call("POST", "/api/setup", app.Record{"name": "Migration admin", "email": "migration-backup@example.test", "password": provisionBackupPassword, "ixpName": "Mailing migration backup", "asn": 64500, "setupToken": "mailing-source-backup"})
	list := c.call("POST", "/api/mailing-lists", app.Record{"key": "members", "name": "Members", "email": "members@lists.example.test", "enabled": true})
	other := c.call("POST", "/api/mailing-lists", app.Record{"key": "operations", "name": "Operations", "email": "ops@lists.example.test", "enabled": true})
	source := migrate.MailingArchive{Version: 1, Kind: migrate.MailingArchiveKind, Source: "ixp-manager-legacy", Access: "admin", Users: []migrate.MailingArchiveUser{{LegacyID: "9007199254740993", Facts: migrate.NoteArchiveFacts{"id": json.RawMessage("9007199254740993"), "email": json.RawMessage(`"source-only@example.test"`)}}}, Choices: []migrate.MailingArchiveChoice{
		{ID: "user-9007199254740993-members", SourceUserID: "9007199254740993", SourceKey: "members", Format: "user.prefs", Value: json.RawMessage(`"0"`)},
		{ID: "user-9007199254740993-operations", SourceUserID: "9007199254740993", SourceKey: "operations", Format: "user.prefs", Value: json.RawMessage(`"1"`)},
	}}
	raw, err := migrate.EncodeMailingArchive(source)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := migrate.ReadMailingArchive(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	c.call("POST", "/api/import/mailing-archive", json.RawMessage(raw))
	user := c.call("GET", "/api/resources/users", nil)["items"].([]any)[0].(map[string]any)
	mapList := func(l map[string]any) map[string]any {
		return c.call("POST", "/api/mailing-lists/legacy/mappings", app.Record{"archiveChecksum": archive.Checksum, "sourceUserId": "9007199254740993", "targetUserId": user["id"], "targetUserCreatedAt": user["createdAt"], "lists": []app.Record{{"sourceKey": l["key"], "listId": l["id"], "listCreatedAt": l["createdAt"], "listRevision": l["revision"]}}})["mapping"].(map[string]any)
	}
	review := func(m, l map[string]any) map[string]any {
		return c.call("POST", "/api/account/mailing-migrations/"+m["id"].(string)+"/review", app.Record{"revision": m["revision"], "email": "migration-backup@example.test", "choices": []app.Record{{"sourceKey": l["key"], "listRevision": l["revision"], "preferenceRevision": 0}}})
	}
	accepted := mapList(list)
	first := review(accepted, list)
	c.call("POST", "/api/account/mailing-migrations/"+accepted["id"].(string)+"/accept", app.Record{"revision": first["mapping"].(map[string]any)["revision"], "reviewToken": first["reviewToken"], "acknowledge": true})
	pending := mapList(other)
	pendingReview := review(pending, other)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	before := noteBackupState(t, directory)
	if before.LegacyMailing == nil || len(before.LegacyMailing.Archives) != 1 || len(before.LegacyMailing.Mappings) != 2 || before.LegacyMailing.Mappings[0].Status != "accepted" || before.LegacyMailing.Mappings[1].Review == nil || len(before.MailingLists.Preferences) != 1 || len(before.Outbox.Entries) != 0 {
		t.Fatal("fixture missing accepted source receipt or pending review")
	}
	backup := filepath.Join(t.TempDir(), "mailing-migration.tar.gz")
	if err := Create(directory, backup); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if err := Restore(backup, target); err != nil {
		t.Fatal(err)
	}
	after := noteBackupState(t, target)
	if !reflect.DeepEqual(before.LegacyMailing, after.LegacyMailing) || !reflect.DeepEqual(before.MailingLists, after.MailingLists) || !reflect.DeepEqual(before.Outbox, after.Outbox) {
		t.Fatal("backup changed private evidence, receipt, review or explicit choices")
	}
	restored, err := app.New(app.Config{DataDir: target})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	path := "/api/account/mailing-migrations/" + pending["id"].(string) + "/accept"
	body := app.Record{"revision": pendingReview["mapping"].(map[string]any)["revision"], "reviewToken": pendingReview["reviewToken"], "acknowledge": true}
	old := &provisionBackupClient{t: t, a: restored, cookie: c.cookie, csrf: c.csrf}
	if w := old.request("POST", path, body); w.Code != 401 {
		t.Fatal("old browser session survived restore", w.Code)
	}
	current := &provisionBackupClient{t: t, a: restored}
	current.call("POST", "/api/login", app.Record{"email": "migration-backup@example.test", "password": provisionBackupPassword})
	if w := current.request("POST", path, body); w.Code != 409 || !strings.Contains(w.Body.String(), "account/session changed") {
		t.Fatal("new login reused old pending review", w.Code, w.Body.String())
	}
	copyResponse := current.request("GET", "/api/mailing-lists/legacy/archives/"+archive.Checksum+"?download=1", nil)
	copy, err := migrate.ReadMailingArchive(bytes.NewReader(copyResponse.Body.Bytes()))
	if err != nil || !reflect.DeepEqual(copy, archive) {
		t.Fatal("restored archive no longer matches selected evidence", err)
	}
	items := current.call("GET", "/api/account/mailing-migrations", nil)["items"].([]any)
	if len(items) != 1 {
		t.Fatal("restore lost pending mapping or resurfaced accepted mapping")
	}
	item := items[0].(map[string]any)
	mapping := item["mapping"].(map[string]any)
	fresh := current.call("POST", "/api/account/mailing-migrations/"+pending["id"].(string)+"/review", app.Record{"revision": mapping["revision"], "email": "migration-backup@example.test", "choices": []app.Record{{"sourceKey": "operations", "listRevision": other["revision"], "preferenceRevision": 0}}})
	current.call("POST", path, app.Record{"revision": fresh["mapping"].(map[string]any)["revision"], "reviewToken": fresh["reviewToken"], "acknowledge": true})
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}
	final := noteBackupState(t, target)
	if len(final.MailingLists.Preferences) != 2 || len(final.Outbox.Entries) != 0 || !reflect.DeepEqual(before.LegacyMailing.Archives, final.LegacyMailing.Archives) || !reflect.DeepEqual(before.LegacyMailing.Mappings[0], final.LegacyMailing.Mappings[0]) {
		t.Fatal("fresh review altered source evidence or previous receipt")
	}
	final.LegacyMailing.Archives[0].Archive.Choices[0].Value = json.RawMessage(`"1"`)
	corrupt, _ := json.Marshal(final)
	if err := os.WriteFile(filepath.Join(target, "state.json"), corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if store, err := app.OpenStore(target); err == nil {
		store.Close()
		t.Fatal("corrupt private archive checksum accepted")
	}
	if err := Create(target, filepath.Join(t.TempDir(), "corrupt.tar.gz")); err == nil {
		t.Fatal("invalid source archive accepted as valid backup")
	}
}
