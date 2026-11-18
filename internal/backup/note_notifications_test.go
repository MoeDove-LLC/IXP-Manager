package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/app"
	"github.com/moedove/ixp-manager/internal/notify"
)

func noteBackupState(t *testing.T, directory string) app.State {
	t.Helper()
	store, err := app.OpenStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	state := store.Snapshot()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return state
}

func TestNoteNotificationsBackupPreservesStateAndRevocation(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "smtp.json")
	// Enabling configuration exercises the real transaction/encryption path.
	// App.New does not start a mail worker, and this test never starts one.
	config := notify.Config{Enabled: true, Host: "127.0.0.1", Port: 2525, Mode: "plain", AllowLoopbackPlain: true, From: "ixp@example.test", BaseURL: "https://notes.example.test"}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	server, err := app.New(app.Config{DataDir: directory, SMTPConfig: configPath, SetupToken: "note-backup-setup"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	admin := &provisionBackupClient{t: t, a: server}
	admin.call("POST", "/api/setup", app.Record{"name": "Backup administrator", "email": "note-admin@example.test", "password": provisionBackupPassword, "ixpName": "Note backup fixture", "asn": 64500, "setupToken": "note-backup-setup"})
	one := admin.call("POST", "/api/resources/members", app.Record{"name": "Watched member", "asn": 64501})
	two := admin.call("POST", "/api/resources/members", app.Record{"name": "Other member", "asn": 64502})
	viewer := admin.call("POST", "/api/resources/users", app.Record{"name": "Note reader", "email": "note-reader@example.test", "password": provisionBackupPassword, "role": "viewer"})
	note := admin.call("POST", "/api/resources/notes", app.Record{"name": "Watched note", "body": "before-private-backup-snapshot", "memberId": one["id"], "visibility": "internal"})
	admin.call("POST", "/api/resources/notes", app.Record{"name": "Unread note", "body": "unrelated-private-note", "memberId": two["id"], "visibility": "internal"})
	reader := &provisionBackupClient{t: t, a: server}
	reader.call("POST", "/api/login", app.Record{"email": "note-reader@example.test", "password": provisionBackupPassword})
	members := []app.Record{{"id": one["id"], "createdAt": one["createdAt"]}}
	notes := []app.Record{{"id": note["id"], "createdAt": note["createdAt"]}}
	reader.call("PUT", "/api/account/note-preferences", app.Record{"revision": 0, "mode": "watched", "memberWatches": members, "noteWatches": notes})
	note = admin.call("PUT", "/api/resources/notes/"+note["id"].(string), app.Record{"name": note["name"], "body": "after-private-backup-snapshot", "memberId": one["id"], "visibility": "internal"})
	summary := reader.call("GET", "/api/account/note-summary", nil)
	if summary["unreadCount"] != float64(2) {
		t.Fatal("fixture did not have two unread notes")
	}
	summary = reader.call("POST", "/api/account/note-read", app.Record{"revision": summary["revision"], "memberId": one["id"]})
	if summary["unreadCount"] != float64(1) {
		t.Fatal("fixture did not retain the other member's unread note")
	}
	preferences := reader.call("GET", "/api/account/note-preferences", nil)
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	before := noteBackupState(t, directory)
	if before.NoteNotifications == nil || len(before.NoteNotifications.Notes) != 2 || len(before.NoteNotifications.Mail) != 1 || len(before.Outbox.Entries) != 1 {
		t.Fatal("fixture did not produce revisions and exactly one queued notification")
	}
	entry := before.Outbox.Entries[0]
	binding := before.NoteNotifications.Mail[0]
	if entry.Status != "pending" || entry.Attempts != 0 || binding.OutboxID != entry.ID || binding.UserID != viewer["id"] || binding.UserCreatedAt != viewer["createdAt"] || binding.Note.ID != note["id"] || binding.Note.CreatedAt != note["createdAt"] || binding.Member.ID != one["id"] || binding.Member.CreatedAt != one["createdAt"] {
		t.Fatal("fixture did not bind pending mail to the exact recipient and note/member entities")
	}
	readerCursor := false
	for _, preference := range before.NoteNotifications.Preferences {
		if preference.UserID == viewer["id"] {
			readerCursor = preference.Mode == "watched" && len(preference.MemberWatches) == 1 && len(preference.NoteWatches) == 1 && len(preference.Reads) == 1 && preference.Reads[0].ID == note["id"] && preference.Reads[0].Revision == before.NoteNotifications.Notes[note["id"].(string)].Revision && preference.Reads[0].Checksum != ""
		}
	}
	if !readerCursor {
		t.Fatal("fixture did not persist the recipient's per-note revision/checksum read mark")
	}
	key, err := os.ReadFile(filepath.Join(directory, "security.key"))
	if err != nil {
		t.Fatal(err)
	}
	message, err := notify.OpenMessage(key, entry)
	if err != nil || message.ID != entry.ID || !strings.Contains(message.Text, "before-private-backup-snapshot") || !strings.Contains(message.Text, "after-private-backup-snapshot") {
		t.Fatal("queued message was not a decryptable immutable before/after snapshot")
	}
	sealed, err := json.Marshal(before.Outbox)
	if err != nil || strings.Contains(string(sealed), "private-backup-snapshot") {
		t.Fatal("outbox exposed plaintext note content")
	}
	archive := filepath.Join(t.TempDir(), "notes.tar.gz")
	if err := Create(directory, archive); err != nil {
		t.Fatal(err)
	}

	for _, mode := range []string{"still-authorized", "unsubscribed", "role-revoked"} {
		t.Run(mode, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "restored")
			if err := Restore(archive, target); err != nil {
				t.Fatal(err)
			}
			after := noteBackupState(t, target)
			if !reflect.DeepEqual(before.NoteNotifications, after.NoteNotifications) || !reflect.DeepEqual(before.Outbox, after.Outbox) {
				t.Fatal("restore changed preferences, entity bindings, read cursors, revisions, or encrypted mail")
			}
			restoredKey, err := os.ReadFile(filepath.Join(target, "security.key"))
			if err != nil {
				t.Fatal(err)
			}
			restoredMessage, err := notify.OpenMessage(restoredKey, after.Outbox.Entries[0])
			if err != nil || !reflect.DeepEqual(message, restoredMessage) {
				t.Fatal("restored encryption key did not recover the exact original message")
			}
			// A failed-but-unexpired delivery makes retry meaningful. The positive
			// control below proves that later 409s are authorization decisions,
			// not the generic refusal to retry an already-pending message.
			store, err := app.OpenStore(target)
			if err != nil {
				t.Fatal(err)
			}
			err = store.Update(func(s *app.State) error {
				s.Outbox.Entries[0].Status = "failed"
				s.Outbox.Entries[0].Attempts = notify.MaxAttempts
				return nil
			})
			store.Close()
			if err != nil {
				t.Fatal(err)
			}
			failed := noteBackupState(t, target).Outbox
			restored, err := app.New(app.Config{DataDir: target, SMTPConfig: configPath})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = restored.Close() })
			rAdmin := &provisionBackupClient{t: t, a: restored}
			rAdmin.call("POST", "/api/login", app.Record{"email": "note-admin@example.test", "password": provisionBackupPassword})
			rReader := &provisionBackupClient{t: t, a: restored}
			rReader.call("POST", "/api/login", app.Record{"email": "note-reader@example.test", "password": provisionBackupPassword})
			if !reflect.DeepEqual(preferences, rReader.call("GET", "/api/account/note-preferences", nil)) || !reflect.DeepEqual(summary, rReader.call("GET", "/api/account/note-summary", nil)) {
				t.Fatal("restored application changed visible subscriptions or unread state")
			}
			switch mode {
			case "unsubscribed":
				rReader.call("PUT", "/api/account/note-preferences", app.Record{"revision": preferences["revision"], "mode": "none", "memberWatches": members, "noteWatches": notes})
			case "role-revoked":
				rAdmin.call("PUT", "/api/resources/users/"+viewer["id"].(string), app.Record{"name": viewer["name"], "email": viewer["email"], "role": "member", "memberId": one["id"]})
			}
			response := rAdmin.request("POST", "/api/notifications/"+entry.ID+"/retry", nil)
			if mode == "still-authorized" {
				if response.Code != 200 {
					t.Fatal("restored authorized notification could not be retried", response.Code)
				}
			} else if response.Code != 409 || !strings.Contains(response.Body.String(), "note recipient, subscription or entity changed") {
				t.Fatal("restored obsolete notification bypassed applicability checks", response.Code)
			}
			if err := restored.Close(); err != nil {
				t.Fatal(err)
			}
			final := noteBackupState(t, target)
			if mode == "still-authorized" {
				if final.Outbox.Entries[0].Status != "pending" || final.Outbox.Entries[0].Attempts != 0 || final.Outbox.Entries[0].Sealed != entry.Sealed || !reflect.DeepEqual(final.NoteNotifications.Mail, before.NoteNotifications.Mail) {
					t.Fatal("retry changed the immutable message or its recipient binding")
				}
			} else if !reflect.DeepEqual(failed, final.Outbox) {
				t.Fatal("rejected retry mutated the restored failed delivery")
			}
		})
	}
}
