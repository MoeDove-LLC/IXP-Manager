package backup

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/moedove/ixp-manager/internal/app"
)

func TestMailingPreferencesBackupRestoreAndInvalidState(t *testing.T) {
	directory := t.TempDir()
	a, err := app.New(app.Config{DataDir: directory, SetupToken: "mailing-backup"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	c := &provisionBackupClient{t: t, a: a}
	c.call("POST", "/api/setup", app.Record{"name": "Mailing administrator", "email": "mailing-backup@example.test", "password": provisionBackupPassword, "ixpName": "Mailing backup", "asn": 64500, "setupToken": "mailing-backup"})
	list := c.call("POST", "/api/mailing-lists", app.Record{"key": "members", "name": "Members", "email": "members@lists.example.test", "enabled": true})
	id := list["id"].(string)
	view := c.call("GET", "/api/account/mailing-lists", nil)
	item := view["items"].([]any)[0].(map[string]any)
	c.call("PUT", "/api/account/mailing-lists/"+id, app.Record{"listRevision": item["listRevision"], "preferenceRevision": item["preferenceRevision"], "email": view["email"], "subscribed": true})
	beforeExport := c.call("GET", "/api/mailing-lists/"+id+"/export", nil)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	before := noteBackupState(t, directory)
	archive := filepath.Join(t.TempDir(), "mailing.tar.gz")
	if err := Create(directory, archive); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err := Restore(archive, restored); err != nil {
		t.Fatal(err)
	}
	after := noteBackupState(t, restored)
	if !reflect.DeepEqual(before.MailingLists, after.MailingLists) || after.MailingLists == nil || len(after.MailingLists.Preferences) != 1 || len(after.Outbox.Entries) != 0 {
		t.Fatal("backup changed explicit preferences or queued unsolicited mail")
	}
	reopened, err := app.New(app.Config{DataDir: restored})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	r := &provisionBackupClient{t: t, a: reopened}
	r.call("POST", "/api/login", app.Record{"email": "mailing-backup@example.test", "password": provisionBackupPassword})
	reloaded := r.call("GET", "/api/account/mailing-lists", nil)
	if reloaded["synchronization"] != "external-not-observed" || reloaded["items"].([]any)[0].(map[string]any)["desired"] != "subscribed" {
		t.Fatal("restore lost choice or invented external sync success")
	}
	exported := r.call("GET", "/api/mailing-lists/"+id+"/export", nil)
	if exported["checksum"] != beforeExport["checksum"] {
		t.Fatal("restore changed the same logical desired membership")
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	after.MailingLists.Preferences = append(after.MailingLists.Preferences, after.MailingLists.Preferences[0])
	raw, err := json.Marshal(after)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(restored, "state.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := app.OpenStore(restored); err == nil {
		s.Close()
		t.Fatal("duplicate persisted consent was accepted")
	}
	if err := Create(restored, filepath.Join(t.TempDir(), "invalid.tar.gz")); err == nil {
		t.Fatal("invalid preference state was accepted as a valid backup")
	}
}
