package backup

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/app"
)

func TestRememberedDevicesBackupRetainsOnlyHashesAndRestoreRevokesGrants(t *testing.T) {
	dir := t.TempDir()
	a, err := app.New(app.Config{DataDir: dir, SetupToken: "remembered-backup-fixture", SecureCookie: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	c := &provisionBackupClient{t: t, a: a}
	c.call("POST", "/api/setup", app.Record{"setupToken": "remembered-backup-fixture", "name": "Remembered backup", "email": "remembered-backup@example.test", "password": provisionBackupPassword, "ixpName": "Remembered fixture", "asn": 64500})
	c.call("POST", "/api/resources/members", app.Record{"name": "Preserved member", "asn": 64505})
	response := c.request("POST", "/api/login", app.Record{"email": "remembered-backup@example.test", "password": provisionBackupPassword, "rememberDevice": true})
	if response.Code != 200 {
		t.Fatal("remember fixture login failed", response.Code)
	}
	var cookie *http.Cookie
	for _, candidate := range response.Result().Cookies() {
		if candidate.Name == "__Host-moedove_remember" {
			cookie = candidate
		}
	}
	if cookie == nil {
		t.Fatal("remember fixture did not issue a persistent cookie")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	before := noteBackupState(t, dir)
	if len(before.RememberedDevices) != 1 {
		t.Fatal("fixture grant missing")
	}
	stateRaw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	validator := strings.Split(cookie.Value, ".")[2]
	if bytes.Contains(stateRaw, []byte(cookie.Value)) || bytes.Contains(stateRaw, []byte(validator)) {
		t.Fatal("private backup input contains raw browser secret")
	}
	archive := filepath.Join(t.TempDir(), "remembered.tar.gz")
	if err := Create(dir, archive); err != nil {
		t.Fatal(err)
	}
	// Revocation after the backup must not be undone by restoring that archive.
	reopened, err := app.New(app.Config{DataDir: dir, SecureCookie: true})
	if err != nil {
		t.Fatal(err)
	}
	current := &provisionBackupClient{t: t, a: reopened}
	current.call("POST", "/api/login", app.Record{"email": "remembered-backup@example.test", "password": provisionBackupPassword})
	current.call("DELETE", "/api/account/remembered-devices", nil)
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	restoredDir := filepath.Join(t.TempDir(), "restored")
	if err := Restore(archive, restoredDir); err != nil {
		t.Fatal(err)
	}
	after := noteBackupState(t, restoredDir)
	if len(after.RememberedDevices) != 0 || !reflect.DeepEqual(before.Security, after.Security) || !reflect.DeepEqual(before.Resources, after.Resources) || !reflect.DeepEqual(before.Outbox, after.Outbox) {
		t.Fatal("restore reactivated browser grants or changed unrelated account/business state")
	}
	restored, err := app.New(app.Config{DataDir: restoredDir, SecureCookie: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restored.Close() })
	r := httptest.NewRequest("POST", "https://manager.example.test/api/session/restore", strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	restored.ServeHTTP(w, r)
	if w.Code != 401 || len(w.Result().Cookies()) != 0 {
		t.Fatal("old backup cookie restored an account or erased browser cookies", w.Code)
	}
	fresh := &provisionBackupClient{t: t, a: restored}
	fresh.call("POST", "/api/login", app.Record{"email": "remembered-backup@example.test", "password": provisionBackupPassword, "rememberDevice": true})
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}
	valid := noteBackupState(t, restoredDir)
	if len(valid.RememberedDevices) != 1 || len(valid.Outbox.Entries) != 0 {
		t.Fatal("fresh explicit login failed or sent unsolicited mail")
	}
	valid.RememberedDevices[0].ValidatorHash = "not-a-validator-hash"
	raw, _ := json.Marshal(valid)
	if err := os.WriteFile(filepath.Join(restoredDir, "state.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Create(restoredDir, filepath.Join(t.TempDir(), "bad.tar.gz")); err == nil {
		t.Fatal("malformed browser grant accepted as valid backup")
	}
}
