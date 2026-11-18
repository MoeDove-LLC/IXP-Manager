package backup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/app"
	"github.com/moedove/ixp-manager/internal/migrate"
	"github.com/moedove/ixp-manager/internal/notify"
)

func TestLegacyNotesBackupKeepsReceiptsAndRequiresFreshReview(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "smtp.json")
	config, err := json.Marshal(notify.Config{Enabled: true, Host: "127.0.0.1", Port: 2525, Mode: "plain", AllowLoopbackPlain: true, From: "ixp@example.test", BaseURL: "https://legacy-notes.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, config, 0600); err != nil {
		t.Fatal(err)
	}
	// Configuration permits durable enqueue, so an empty queue is meaningful.
	// No mail worker or SMTP listener/connection is ever started by this test.
	server, err := app.New(app.Config{DataDir: directory, SMTPConfig: configPath, SetupToken: "legacy-note-backup-setup"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	admin := &provisionBackupClient{t: t, a: server}
	admin.call("POST", "/api/setup", app.Record{"name": "Source backup admin", "email": "source-admin@example.test", "password": provisionBackupPassword, "ixpName": "Legacy notes backup", "asn": 64500, "setupToken": "legacy-note-backup-setup"})
	member := admin.call("POST", "/api/resources/members", app.Record{"name": "Current member", "asn": 64501})
	otherMember := admin.call("POST", "/api/resources/members", app.Record{"name": "Other current member", "asn": 64502})
	note := admin.call("POST", "/api/resources/notes", app.Record{"name": "Current note", "body": "CURRENT CONTENT, NOT LEGACY BODY", "memberId": member["id"], "visibility": "internal"})
	admin.call("POST", "/api/resources/notes", app.Record{"name": "Still unread current note", "body": "OTHER CURRENT CONTENT", "memberId": otherMember["id"], "visibility": "internal"})
	viewer := admin.call("POST", "/api/resources/users", app.Record{"name": "Current staff", "email": "source-reader@example.test", "role": "viewer", "password": provisionBackupPassword})
	admin.call("POST", "/api/resources/users", app.Record{"name": "Ordinary member", "email": "source-member@example.test", "role": "member", "memberId": member["id"], "password": provisionBackupPassword})
	reader := &provisionBackupClient{t: t, a: server}
	reader.call("POST", "/api/login", app.Record{"email": "source-reader@example.test", "password": provisionBackupPassword})
	summary := reader.call("GET", "/api/account/note-summary", nil)
	readSummary := reader.call("POST", "/api/account/note-read", app.Record{"revision": summary["revision"], "memberId": member["id"]})
	if readSummary["unreadCount"] != float64(1) {
		t.Fatal("fixture did not retain a current unread note")
	}
	liveNotesBefore := admin.call("GET", "/api/resources/notes", nil)

	facts := func(values map[string]any) migrate.NoteArchiveFacts {
		out := migrate.NoteArchiveFacts{}
		for key, value := range values {
			out[key], _ = json.Marshal(value)
		}
		return out
	}
	const sourceOne int64 = 9007199254740993
	const sourceTwo int64 = 9007199254740994
	archive := migrate.NoteArchive{Version: 1, Kind: migrate.NoteArchiveKind, Source: "ixp-manager-legacy", Access: "staff", SourceTimezone: "unknown", Warnings: []string{"Original preferences and read timestamps are provenance, not current permissions."}}
	archive.Notes = []migrate.NoteArchiveNote{{ID: migrate.NoteArchiveID("source", 1), LegacyID: 1, Visibility: "internal", PrivacyKnown: false, Facts: facts(map[string]any{"id": 1, "customer_id": 41, "title": "  Source title  ", "note": " OLD PRIVATE\r\nBODY ", "private": "unknown", "created": "2010-01-02"})}}
	for _, id := range []int64{sourceOne, sourceTwo} {
		archive.Users = append(archive.Users, migrate.NoteArchiveUser{ID: migrate.NoteArchiveID("user", id), LegacyID: id, Facts: facts(map[string]any{"id": id, "username": "historical-user-" + strconv.FormatInt(id, 10), "email": "source-reader@example.test", "disabled": true})})
		archive.Preferences = append(archive.Preferences, migrate.NoteArchivePreference{ID: migrate.NoteArchiveID("modern-pref", id), Format: "user.prefs", LegacyID: id, LegacyUserID: id, PrefsState: "object", NotesState: "object", Facts: migrate.NoteArchiveFacts{}, Notes: facts(map[string]any{"global_notifs": "all", "customer_watching": map[string]any{"41": "2010-01-02"}, "note_watching": map[string]any{"1": "2010-01-02"}, "read_upto": "2099-01-01", "last_read": map[string]any{"41": "2099-01-01"}})})
	}
	raw, err := migrate.EncodeNoteArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	source, err := migrate.ReadNoteArchive(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	admin.call("POST", "/api/import/note-archive", json.RawMessage(raw))
	mapSource := func(id int64) map[string]any {
		return admin.call("POST", "/api/notes/legacy/mappings", app.Record{"archiveChecksum": source.Checksum, "sourceUserId": strconv.FormatInt(id, 10), "targetUserId": viewer["id"], "targetUserCreatedAt": viewer["createdAt"]})["mapping"].(map[string]any)
	}
	members := []app.Record{{"id": member["id"], "createdAt": member["createdAt"]}}
	notes := []app.Record{{"id": note["id"], "createdAt": note["createdAt"]}}
	acceptedMapping := mapSource(sourceOne)
	firstReview := reader.call("POST", "/api/account/note-migrations/"+acceptedMapping["id"].(string)+"/review", app.Record{"revision": acceptedMapping["revision"], "preferenceRevision": 0, "mode": "watched", "memberWatches": members, "noteWatches": notes})
	accepted := reader.call("POST", "/api/account/note-migrations/"+acceptedMapping["id"].(string)+"/accept", app.Record{"revision": firstReview["mapping"].(map[string]any)["revision"], "reviewToken": firstReview["reviewToken"], "acknowledge": true})
	if accepted["mailQueued"] != false || accepted["legacyReadCursorsApplied"] != false || accepted["legacyBodiesApplied"] != false {
		t.Fatal("first acceptance applied source content or historical reads")
	}
	pending := mapSource(sourceTwo)
	preferencesBefore := reader.call("GET", "/api/account/note-preferences", nil)
	pendingReview := reader.call("POST", "/api/account/note-migrations/"+pending["id"].(string)+"/review", app.Record{"revision": pending["revision"], "preferenceRevision": preferencesBefore["revision"], "mode": "all"})
	summaryBefore := reader.call("GET", "/api/account/note-summary", nil)
	if summaryBefore["unreadCount"] != float64(1) || preferencesBefore["mode"] != "watched" || !reflect.DeepEqual(liveNotesBefore, admin.call("GET", "/api/resources/notes", nil)) {
		t.Fatal("fixture did not retain current read state and accepted selection")
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	before := noteBackupState(t, directory)
	if before.LegacyNotes == nil || len(before.LegacyNotes.Archives) != 1 || len(before.LegacyNotes.Mappings) != 2 || len(before.Outbox.Entries) != 0 {
		t.Fatal("fixture source/mapping/outbox counts differ")
	}
	if before.LegacyNotes.Mappings[0].Status != "accepted" || before.LegacyNotes.Mappings[0].AcceptedSelection == nil || before.LegacyNotes.Mappings[0].Review != nil || before.LegacyNotes.Mappings[1].Status != "pending" || before.LegacyNotes.Mappings[1].Review == nil {
		t.Fatal("fixture lacks accepted receipt and pending review")
	}
	var originalReads []app.NoteReadMark
	for _, preference := range before.NoteNotifications.Preferences {
		if preference.UserID == viewer["id"] {
			originalReads = preference.Reads
		}
	}
	if len(originalReads) != 1 || originalReads[0].ID != note["id"] {
		t.Fatal("fixture current read cursor is absent")
	}
	keyBefore, err := os.ReadFile(filepath.Join(directory, "security.key"))
	if err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(t.TempDir(), "legacy-notes.tar.gz")
	if err := Create(directory, backupPath); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if err := Restore(backupPath, target); err != nil {
		t.Fatal(err)
	}
	after := noteBackupState(t, target)
	keyAfter, err := os.ReadFile(filepath.Join(target, "security.key"))
	if err != nil || !bytes.Equal(keyBefore, keyAfter) || !reflect.DeepEqual(before.LegacyNotes, after.LegacyNotes) || !reflect.DeepEqual(before.NoteNotifications, after.NoteNotifications) || !reflect.DeepEqual(before.Outbox, after.Outbox) || !reflect.DeepEqual(before.Resources["notes"], after.Resources["notes"]) {
		t.Fatal("complete backup changed source bytes/checksum, receipts, review binding, preferences or live notes")
	}
	if err := migrate.ValidateNoteArchive(&after.LegacyNotes.Archives[0].Archive); err != nil || !reflect.DeepEqual(*source, after.LegacyNotes.Archives[0].Archive) {
		t.Fatal("restored archive did not preserve exact source values", err)
	}
	restored, err := app.New(app.Config{DataDir: target, SMTPConfig: configPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	acceptPath := "/api/account/note-migrations/" + pending["id"].(string) + "/accept"
	oldBody := app.Record{"revision": pendingReview["mapping"].(map[string]any)["revision"], "reviewToken": pendingReview["reviewToken"], "acknowledge": true}
	oldSession := &provisionBackupClient{t: t, a: restored, cookie: reader.cookie, csrf: reader.csrf}
	if response := oldSession.request("POST", acceptPath, oldBody); response.Code != 401 {
		t.Fatal("restore recovered an old browser session", response.Code)
	}
	newSession := &provisionBackupClient{t: t, a: restored}
	newSession.call("POST", "/api/login", app.Record{"email": "source-reader@example.test", "password": provisionBackupPassword})
	if !reflect.DeepEqual(preferencesBefore, newSession.call("GET", "/api/account/note-preferences", nil)) || !reflect.DeepEqual(summaryBefore, newSession.call("GET", "/api/account/note-summary", nil)) {
		t.Fatal("restored current account's preferences or unread summary changed")
	}
	expires, err := time.Parse(time.RFC3339Nano, after.LegacyNotes.Mappings[1].Review.ExpiresAt)
	if err != nil || !time.Now().Before(expires) {
		t.Fatal("fixture review expired before the new-session rejection check")
	}
	if response := newSession.request("POST", acceptPath, oldBody); response.Code != 409 || !strings.Contains(response.Body.String(), "account/session changed") {
		t.Fatal("new login reused a restored old-session review token", response.Code)
	}
	items := newSession.call("GET", "/api/account/note-migrations", nil)["items"].([]any)
	if len(items) != 1 {
		t.Fatal("restore lost pending mapping or resurfaced accepted mapping")
	}
	currentMapping := items[0].(map[string]any)["mapping"].(map[string]any)
	if currentMapping["sourceUserId"] != strconv.FormatInt(sourceTwo, 10) || !strings.Contains(items[0].(map[string]any)["sourceJSON"].(string), strconv.FormatInt(sourceTwo, 10)) {
		t.Fatal("restored source account identity lost precision")
	}
	download := newSession.request("GET", "/api/notes/legacy/archives/"+source.Checksum+"?download=1", nil)
	copy, err := migrate.ReadNoteArchive(bytes.NewReader(download.Body.Bytes()))
	if download.Code != 200 || err != nil || !reflect.DeepEqual(source, copy) {
		t.Fatal("restored archive download changed the canonical source", err)
	}
	ordinary := &provisionBackupClient{t: t, a: restored}
	ordinary.call("POST", "/api/login", app.Record{"email": "source-member@example.test", "password": provisionBackupPassword})
	if response := ordinary.request("GET", "/api/notes/legacy/archives/"+source.Checksum+"?download=1", nil); response.Code != 403 {
		t.Fatal("restored source archive became member-readable")
	}
	// A fresh review defaults to none; it does not reuse the source's old all.
	newReview := newSession.call("POST", "/api/account/note-migrations/"+pending["id"].(string)+"/review", app.Record{"revision": currentMapping["revision"], "preferenceRevision": preferencesBefore["revision"]})
	if newReview["after"].(map[string]any)["mode"] != "none" {
		t.Fatal("fresh review silently adopted the archived all preference")
	}
	newSession.call("POST", acceptPath, app.Record{"revision": newReview["mapping"].(map[string]any)["revision"], "reviewToken": newReview["reviewToken"], "acknowledge": true})
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}
	final := noteBackupState(t, target)
	if !reflect.DeepEqual(before.LegacyNotes.Archives, final.LegacyNotes.Archives) || !reflect.DeepEqual(before.LegacyNotes.Mappings[0], final.LegacyNotes.Mappings[0]) || final.LegacyNotes.Mappings[1].Status != "accepted" || final.LegacyNotes.Mappings[1].Review != nil || !reflect.DeepEqual(before.Resources["notes"], final.Resources["notes"]) || !reflect.DeepEqual(before.Outbox, final.Outbox) {
		t.Fatal("fresh acceptance changed prior sources/receipt/live notes/mail or failed to consume the review")
	}
	for _, preference := range final.NoteNotifications.Preferences {
		if preference.UserID == viewer["id"] && (preference.Mode != "none" || !reflect.DeepEqual(originalReads, preference.Reads)) {
			t.Fatal("fresh acceptance imported historical reads or ignored the explicit current choice")
		}
	}
}
