package backup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/moedove/ixp-manager/internal/app"
	"github.com/moedove/ixp-manager/internal/migrate"
)

func TestLegacyPatchingBackupPreservesSnapshotsBlobsAndAllocationScope(t *testing.T) {
	dir := t.TempDir()
	server, e := app.New(app.Config{DataDir: dir, SetupToken: "legacy-backup-setup"})
	if e != nil {
		t.Fatal(e)
	}
	defer server.Close()
	c := &provisionBackupClient{t: t, a: server}
	c.call("POST", "/api/setup", app.Record{"name": "Backup admin", "email": "legacy-backup@example.test", "password": provisionBackupPassword, "ixpName": "Legacy backup", "asn": 64500, "setupToken": "legacy-backup-setup"})
	fileRoot := t.TempDir()
	content := "original attachment survives full restore"
	keyBytes := sha256.Sum256([]byte("old-port-name-key"))
	key := hex.EncodeToString(keyBytes[:])
	rel, _ := migrate.PatchStoragePath(key)
	name := filepath.Join(fileRoot, filepath.FromSlash(rel))
	if e = os.MkdirAll(filepath.Dir(name), 0700); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(name, []byte(content), 0600); e != nil {
		t.Fatal(e)
	}
	facts := func(id, kind, parent string) map[string]string {
		return map[string]string{"id": id, parent: "1", "name": "original.txt", "size": strconv.Itoa(len(content)), "is_private": "0", "storage_location": key, "uploaded_at": "2020-01-01 12:00:00", "uploaded_by": "old uploader"}
	}
	p := migrate.PatchingImport{Version: 1, SourceTimezone: "UTC", Panels: []migrate.PatchPanelSource{{ID: "legacy-patch-panels-1", LegacyID: 1, RackID: "legacy-racks-1", Name: "Old panel", Prefix: "P", Facts: map[string]string{"id": "1", "cabinet_id": "1", "name": "Old panel", "port_prefix": "P"}}}, Ports: []migrate.PatchPortSource{{ID: "legacy-patch-ports-1", LegacyID: 1, PanelID: "legacy-patch-panels-1", MemberID: "legacy-members-1", Number: 1, State: 7, Facts: map[string]string{"id": "1", "patch_panel_id": "1", "customer_id": "1", "number": "1", "state": "7"}}}, Histories: []migrate.PatchHistorySource{{ID: "legacy-patch-history-1", LegacyID: 1, PortID: "legacy-patch-ports-1", Number: 1, State: 3, Facts: map[string]string{"id": "1", "patch_panel_port_id": "1", "state": "3", "number": "1", "customer": "Retired source owner", "private_notes": "STAFF SNAPSHOT", "ceased_at": "2019-02-01"}}}, Files: []migrate.PatchFileSource{{ID: migrate.PatchFileID("current", 1), LegacyID: 1, Kind: "current", ParentID: "legacy-patch-ports-1", Name: "original.txt", StorageKey: key, Size: int64(len(content)), Entry: migrate.PatchFileEntry("current", 1), Facts: facts("1", "current", "patch_panel_port_id")}, {ID: migrate.PatchFileID("history", 1), LegacyID: 1, Kind: "history", ParentID: "legacy-patch-history-1", Name: "original.txt", StorageKey: key, Size: int64(len(content)), Entry: migrate.PatchFileEntry("history", 1), Facts: facts("1", "history", "patch_panel_port_history_id")}}}
	bundle := migrate.PatchBundle{Version: 1, Kind: migrate.PatchBundleKind, Source: "ixp-manager-legacy", Resources: map[string][]map[string]any{"members": {{"id": "legacy-members-1", "name": "Member", "asn": 64501, "status": "active"}}, "locations": {{"id": "legacy-locations-1", "name": "Location"}}, "racks": {{"id": "legacy-racks-1", "name": "Rack", "locationId": "legacy-locations-1"}}}, Patching: p}
	path := filepath.Join(t.TempDir(), "migration.tar.gz")
	if e = migrate.WritePatchBundle(path, fileRoot, bundle); e != nil {
		t.Fatal(e)
	}
	data, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("bundle", "migration.tar.gz")
	part.Write(data)
	mw.Close()
	r := httptest.NewRequest("POST", "/api/import/patching", &body)
	r.RemoteAddr = "127.0.0.1:10000"
	r.AddCookie(c.cookie)
	r.Header.Set("X-CSRF-Token", c.csrf)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	c.call("POST", "/api/resources/users", app.Record{"name": "Member", "email": "legacy-member@example.test", "password": provisionBackupPassword, "role": "member", "memberId": "legacy-members-1"})
	before := c.call("GET", "/api/patching/legacy", nil)
	server.Close()
	archive := filepath.Join(t.TempDir(), "complete.tar.gz")
	if e = Create(dir, archive); e != nil {
		t.Fatal(e)
	}
	target := filepath.Join(t.TempDir(), "restore")
	if e = Restore(archive, target); e != nil {
		t.Fatal(e)
	}
	restored, e := app.New(app.Config{DataDir: target})
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	admin := &provisionBackupClient{t: t, a: restored}
	admin.call("POST", "/api/login", app.Record{"email": "legacy-backup@example.test", "password": provisionBackupPassword})
	after := admin.call("GET", "/api/patching/legacy", nil)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("source snapshots or allocation/entity binding changed on restore")
	}
	member := &provisionBackupClient{t: t, a: restored}
	member.call("POST", "/api/login", app.Record{"email": "legacy-member@example.test", "password": provisionBackupPassword})
	current := migrate.PatchFileID("current", 1)
	history := migrate.PatchFileID("history", 1)
	if result := member.request("GET", "/api/documents/"+current+"/download", nil); result.Code != 200 || result.Body.String() != content {
		t.Fatal("restored current allocation access lost")
	}
	if result := member.request("GET", "/api/documents/"+history+"/download", nil); result.Code != 404 {
		t.Fatal("restore made staff history member-readable")
	}
	items := admin.call("GET", "/api/patching/ports", nil)["items"].([]any)
	port := items[0].(map[string]any)
	admin.call("POST", "/api/patching/ports/legacy-patch-ports-1/transition", app.Record{"action": "release", "revision": port["revision"], "reason": "release after restore"})
	if result := member.request("GET", "/api/documents/"+current+"/download", nil); result.Code != 404 {
		t.Fatal("restored file binding ignored release")
	}
	if result := admin.request("GET", "/api/documents/"+history+"/download", nil); result.Code != 200 {
		t.Fatal("historical content lost from complete backup")
	} else if b, e := io.ReadAll(result.Result().Body); e != nil || string(b) != content {
		t.Fatal("restored bytes changed")
	}
}
