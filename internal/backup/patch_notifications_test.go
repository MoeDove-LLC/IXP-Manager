package backup

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/app"
	"github.com/moedove/ixp-manager/internal/notify"
)

func TestPatchNotificationBackupPreservesEncryptedMIMEAndRevocation(t *testing.T) {
	dir := t.TempDir()
	smtpPath := filepath.Join(t.TempDir(), "smtp.json")
	config := notify.Config{Enabled: true, Host: "127.0.0.1", Port: 2525, Mode: "plain", AllowLoopbackPlain: true, From: "ixp@example.test", BaseURL: "https://patch-backup.example.test"}
	data, _ := json.Marshal(config)
	if err := os.WriteFile(smtpPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	server, err := app.New(app.Config{DataDir: dir, SMTPConfig: smtpPath, SetupToken: "patch-backup-setup"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	// App.New does not start background delivery. No SMTP socket is opened.
	c := &provisionBackupClient{t: t, a: server}
	c.call("POST", "/api/setup", app.Record{"name": "Patch backup admin", "email": "patch-admin@example.test", "password": provisionBackupPassword, "ixpName": "Patch backup fixture", "asn": 64500, "setupToken": "patch-backup-setup"})
	member := c.call("POST", "/api/resources/members", app.Record{"name": "Current member", "asn": 64501, "status": "active", "nocEmail": "noc@example.test"})
	location := c.call("POST", "/api/resources/locations", app.Record{"name": "Fixture location"})
	rack := c.call("POST", "/api/resources/racks", app.Record{"name": "Fixture rack", "locationId": location["id"]})
	panel := c.call("POST", "/api/resources/patch-panels", app.Record{"name": "Fixture panel", "rackId": rack["id"], "portCount": 8})
	port := c.call("POST", "/api/resources/patch-ports", app.Record{"name": "Fixture port", "panelId": panel["id"], "number": 1, "status": "available"})
	var upload bytes.Buffer
	writer := multipart.NewWriter(&upload)
	part, _ := writer.CreateFormFile("file", "work-order.txt")
	part.Write([]byte("backup-original-member-attachment"))
	writer.WriteField("visibility", "member")
	writer.WriteField("memberId", member["id"].(string))
	writer.Close()
	request := httptest.NewRequest("POST", "/api/documents", &upload)
	request.AddCookie(c.cookie)
	request.Header.Set("X-CSRF-Token", c.csrf)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != 201 {
		t.Fatal("document fixture failed", response.Code, response.Body.String())
	}
	var doc app.Record
	if err = json.Unmarshal(response.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	p := port["id"].(string)
	base := "/api/patching/ports/" + p
	current := func() app.Record {
		for _, raw := range c.call("GET", "/api/patching/ports", nil)["items"].([]any) {
			v := app.Record(raw.(map[string]any))
			if v["id"] == p {
				return v
			}
		}
		t.Fatal("port missing")
		return nil
	}
	c.call("POST", base+"/transition", app.Record{"action": "reserve", "revision": current()["revision"], "reason": "explicit current fixture allocation", "memberId": member["id"], "documentIds": []string{doc["id"].(string)}})
	letter := c.call("POST", base+"/loas", app.Record{"revision": current()["revision"], "reason": "current authorization fixture", "expiresAt": time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)})
	compose := c.call("GET", base+"/notifications?type=info", nil)
	draft := app.Record(compose["draft"].(map[string]any))
	draft["loaId"], draft["documentIds"] = letter["id"], []string{doc["id"].(string)}
	draft["cc"], draft["bcc"] = []string{"backup-cc@example.test"}, []string{"backup-private-bcc@example.test"}
	draft["text"] = "backup-private-notification-body"
	review := c.call("POST", base+"/notifications/review", draft)
	queue := app.Record(review["draft"].(map[string]any))
	queue["reviewToken"], queue["expiresAt"], queue["acknowledge"] = review["reviewToken"], review["expiresAt"], true
	c.call("POST", base+"/notifications/queue", queue)
	history := c.call("GET", "/api/patching/history?portId="+p, nil)
	if err = server.Close(); err != nil {
		t.Fatal(err)
	}
	before := noteBackupState(t, dir)
	if before.PatchNotifications == nil || len(before.PatchNotifications.Receipts) != 1 || len(before.Outbox.Entries) != 1 {
		t.Fatal("explicit queue did not produce one durable bound snapshot")
	}
	key, err := os.ReadFile(filepath.Join(dir, "security.key"))
	if err != nil {
		t.Fatal(err)
	}
	message, err := notify.OpenMessage(key, before.Outbox.Entries[0])
	if err != nil || len(message.Attachments) != 2 || len(message.Cc) != 1 || len(message.Bcc) != 1 {
		t.Fatal("multipart fixture missing", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{message.Text, message.Bcc[0], message.Cc[0], "backup-original-member-attachment"} {
		if bytes.Contains(raw, []byte(private)) {
			t.Fatal("private mail plaintext persisted outside ciphertext")
		}
	}
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err = Create(dir, archive); err != nil {
		t.Fatal(err)
	}
	restoredDir := filepath.Join(t.TempDir(), "restored")
	if err = Restore(archive, restoredDir); err != nil {
		t.Fatal(err)
	}
	after := noteBackupState(t, restoredDir)
	if !reflect.DeepEqual(before.PatchNotifications, after.PatchNotifications) || !reflect.DeepEqual(before.Outbox, after.Outbox) || !reflect.DeepEqual(before.LOAs, after.LOAs) || !reflect.DeepEqual(before.Documents, after.Documents) {
		t.Fatal("backup changed notification binding, encryption or attachment provenance")
	}
	restoredKey, err := os.ReadFile(filepath.Join(restoredDir, "security.key"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := notify.OpenMessage(restoredKey, after.Outbox.Entries[0])
	if err != nil || !reflect.DeepEqual(message, again) {
		t.Fatal("restored encrypted recipients/PDF/document bytes changed", err)
	}
	restored, err := app.New(app.Config{DataDir: restoredDir, SMTPConfig: smtpPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	admin := &provisionBackupClient{t: t, a: restored}
	admin.call("POST", "/api/login", app.Record{"email": "patch-admin@example.test", "password": provisionBackupPassword})
	if got := admin.call("GET", "/api/patching/history?portId="+p, nil); !reflect.DeepEqual(history, got) {
		t.Fatal("restore replayed historical patch events")
	}
	if w := admin.request("POST", base+"/notifications/queue", queue); w.Code != 409 {
		t.Fatal("new login reused an old session review token", w.Code)
	}
	member = admin.call("GET", "/api/resources/members/"+member["id"].(string), nil)
	admin.call("PUT", "/api/resources/members/"+member["id"].(string), app.Record{"name": member["name"], "asn": member["asn"], "status": member["status"], "nocEmail": "changed-noc@example.test"})
	if err = restored.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := app.OpenStore(restoredDir)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Update(func(s *app.State) error { s.Outbox.Entries[0].Status = "failed"; return nil }); err != nil {
		t.Fatal(err)
	}
	store.Close()
	restored, err = app.New(app.Config{DataDir: restoredDir, SMTPConfig: smtpPath})
	if err != nil {
		t.Fatal(err)
	}
	admin = &provisionBackupClient{t: t, a: restored}
	admin.call("POST", "/api/login", app.Record{"email": "patch-admin@example.test", "password": provisionBackupPassword})
	if w := admin.request("POST", "/api/notifications/"+after.Outbox.Entries[0].ID+"/retry", nil); w.Code != 409 || strings.Contains(w.Body.String(), message.Bcc[0]) {
		t.Fatal("restored source change did not block retry")
	}
	restored.Close()
	last := noteBackupState(t, restoredDir)
	if len(last.Outbox.Entries) != 1 || len(last.PatchNotifications.Receipts) != 1 || last.Outbox.Entries[0].Status != "failed" {
		t.Fatal("read-only restore/retry created or changed delivery")
	}
}
