package backup

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/moedove/ixp-manager/internal/app"
	"github.com/moedove/ixp-manager/internal/notify"
)

func TestMemberWelcomeMailBackupRestoresEncryptedMessageAndReceipt(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "smtp.json")
	config := notify.Config{Enabled: true, Host: "127.0.0.1", Port: 2525, Mode: "plain", AllowLoopbackPlain: true, From: "ixp@example.test", BaseURL: "https://welcome-backup.example.test"}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	server, err := app.New(app.Config{DataDir: dir, SMTPConfig: configPath, SetupToken: "welcome-backup-setup"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	admin := &provisionBackupClient{t: t, a: server}
	admin.call("POST", "/api/setup", app.Record{"name": "Welcome admin", "email": "welcome-admin@example.test", "password": provisionBackupPassword, "ixpName": "Welcome backup", "asn": 64500, "setupToken": "welcome-backup-setup"})
	member := admin.call("POST", "/api/resources/members", app.Record{
		"name": "Welcome backup member", "asn": 64501, "status": "active", "email": "welcome-backup-to@example.test", "nocEmail": "welcome-backup-hidden@example.test",
	})
	path := "/api/members/" + member["id"].(string) + "/welcome-email"
	context := admin.call("GET", path, nil)
	draft := app.Record(context["draft"].(map[string]any))
	draft["to"], draft["cc"], draft["bcc"] = []string{"welcome-backup-to@example.test"}, []string{}, []string{"welcome-backup-hidden@example.test"}
	draft["text"] = "Welcome backup private body contents"
	review := admin.call("POST", path+"/review", draft)
	queue := app.Record(review["draft"].(map[string]any))
	queue["reviewToken"], queue["expiresAt"], queue["acknowledge"] = review["reviewToken"], review["expiresAt"], true
	admin.call("POST", path+"/queue", queue)
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	before := noteBackupState(t, dir)
	if len(before.WelcomeMail) != 1 || len(before.Outbox.Entries) != 1 || before.Outbox.Entries[0].Status != "pending" {
		t.Fatal("review did not queue exactly one bound welcome message")
	}
	key, err := os.ReadFile(filepath.Join(dir, "security.key"))
	if err != nil {
		t.Fatal(err)
	}
	message, err := notify.OpenMessage(key, before.Outbox.Entries[0])
	if err != nil || message.Text != draft["text"] || len(message.Bcc) != 1 {
		t.Fatal("captured body or BCC differed after initial reopen", err)
	}
	archive := filepath.Join(t.TempDir(), "welcome.tar.gz")
	if err := Create(dir, archive); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err := Restore(archive, restored); err != nil {
		t.Fatal(err)
	}
	after := noteBackupState(t, restored)
	if !reflect.DeepEqual(before.WelcomeMail, after.WelcomeMail) || !reflect.DeepEqual(before.Outbox, after.Outbox) {
		t.Fatal("restore changed reviewed welcome metadata or ciphertext")
	}
	restoredKey, err := os.ReadFile(filepath.Join(restored, "security.key"))
	if err != nil {
		t.Fatal(err)
	}
	restoredMessage, err := notify.OpenMessage(restoredKey, after.Outbox.Entries[0])
	if err != nil || !reflect.DeepEqual(message, restoredMessage) {
		t.Fatal("restored encrypted welcome message did not match the reviewed snapshot", err)
	}
	metadata, err := json.Marshal(app.Record{"receipt": after.WelcomeMail[0], "outbox": after.Outbox.Entries[0]})
	if err != nil || bytes.Contains(metadata, []byte(message.Text)) || bytes.Contains(metadata, []byte(message.Bcc[0])) || bytes.Contains(metadata, []byte(review["reviewToken"].(string))) {
		t.Fatal("restored welcome receipt or outbox metadata disclosed review content")
	}
}
