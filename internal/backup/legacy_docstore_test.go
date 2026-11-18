package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/app"
	"github.com/moedove/ixp-manager/internal/migrate"
)

func TestLegacyDocstoreBackupPreservesBytesACLAndSourceLogs(t *testing.T) {
	dir := t.TempDir()
	a, err := app.New(app.Config{DataDir: dir, SetupToken: "docstore-backup-setup"})
	if err != nil {
		t.Fatal(err)
	}
	c := &provisionBackupClient{t: t, a: a}
	c.call("POST", "/api/setup", app.Record{"name": "Admin", "email": "docstore-admin@example.test", "password": provisionBackupPassword, "ixpName": "Backup", "asn": 64500, "setupToken": "docstore-backup-setup"})
	a.Close()
	store, err := app.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	stamp, batch := "2026-01-01T00:00:00Z", strings.Repeat("1", 64)
	content := []byte("restored document bytes")
	sum := sha256.Sum256(content)
	checksum := hex.EncodeToString(sum[:])
	err = store.Update(func(s *app.State) error {
		s.Resources["members"] = append(s.Resources["members"], app.Record{"id": "legacy-members-1", "name": "Member", "asn": float64(64501), "status": "active", "createdAt": stamp})
		l := &app.LegacyDocstoreState{Batches: []app.LegacyDocstoreBatch{{ID: batch, ImportedAt: stamp, ImportedBy: "source-importer", SourceTimezone: "UTC", Files: 2, Logs: 1}}}
		for i, kind := range []string{"shared", "customer"} {
			id := migrate.DocstoreID(kind, "file", 1)
			blob := "blob-" + strings.Repeat(string(rune('a'+i)), 24)
			facts := map[string]string{"id": "1", "name": "source.txt", "disk": "local", "path": kind + "/source.txt", "min_privs": "0", "created_by": "former-actor"}
			f := migrate.DocstoreFileSource{ID: id, LegacyID: 1, Kind: kind, Name: "source.txt", StorageKey: facts["path"], Size: int64(len(content)), Checksum: checksum, Entry: migrate.DocstoreFileEntry(kind, 1), Facts: facts}
			binding := app.LegacyDocstoreFile{Source: f, BatchID: batch, DocumentID: id, DirectoryID: "dir-" + id}
			if kind == "customer" {
				binding.Source.MemberID, binding.Source.MinPrivileges, binding.MemberCreatedAt = "legacy-members-1", 2, stamp
				facts["cust_id"], facts["min_privs"] = "1", "2"
			}
			l.Files = append(l.Files, binding)
			s.Directories = append(s.Directories, app.Directory{ID: binding.DirectoryID, Name: "Source " + kind, Visibility: "staff", CreatedAt: stamp, UpdatedAt: stamp})
			s.Documents = append(s.Documents, app.Document{ID: id, Name: "source.txt", DirectoryID: binding.DirectoryID, Visibility: "staff", Version: 1, BlobID: blob, Size: int64(len(content)), Checksum: checksum, MediaType: "text/plain", CreatedAt: stamp, UpdatedAt: stamp})
			if e := os.WriteFile(filepath.Join(dir, "blobs", blob), content, 0600); e != nil {
				return e
			}
		}
		log := migrate.DocstoreLogSource{ID: migrate.DocstoreID("shared", "log", 1), LegacyID: 1, FileID: migrate.DocstoreID("shared", "file", 1), CreatedAt: "2020-01-01 12:00:00", Facts: map[string]string{"id": "1", "docstore_file_id": "1", "created_at": "2020-01-01 12:00:00"}}
		l.Logs = []app.LegacyDocstoreLog{{Source: log, BatchID: batch}}
		s.LegacyDocstore = l
		return nil
	})
	before := store.Snapshot().LegacyDocstore
	store.Close()
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.tar.gz")
	if err = Create(dir, archive); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "restored")
	if err = Restore(archive, target); err != nil {
		t.Fatal(err)
	}
	store, err = app.OpenStore(target)
	if err != nil {
		t.Fatal(err)
	}
	after := store.Snapshot().LegacyDocstore
	store.Close()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("backup changed source access records or per-file member bindings")
	}
	a, err = app.New(app.Config{DataDir: target})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	c = &provisionBackupClient{t: t, a: a}
	c.call("POST", "/api/login", app.Record{"email": "docstore-admin@example.test", "password": provisionBackupPassword})
	c.call("POST", "/api/resources/users", app.Record{"name": "Ordinary member", "email": "docstore-member@example.test", "password": provisionBackupPassword, "role": "member", "memberId": "legacy-members-1"})
	c.call("POST", "/api/resources/users", app.Record{"name": "Member administrator", "email": "docstore-member-admin@example.test", "password": provisionBackupPassword, "role": "member", "memberId": "legacy-members-1", "memberAdminIds": []string{"legacy-members-1"}})
	member := &provisionBackupClient{t: t, a: a}
	member.call("POST", "/api/login", app.Record{"email": "docstore-member@example.test", "password": provisionBackupPassword})
	admin := &provisionBackupClient{t: t, a: a}
	admin.call("POST", "/api/login", app.Record{"email": "docstore-member-admin@example.test", "password": provisionBackupPassword})
	private := "/api/documents/" + migrate.DocstoreID("customer", "file", 1) + "/download"
	if w := member.request("GET", private, nil); w.Code != 404 {
		t.Fatal("restore broadened member-admin permission", w.Code)
	}
	if w := admin.request("GET", private, nil); w.Code != 200 || w.Body.String() != string(content) {
		t.Fatal("restore lost administrator access or content", w.Code)
	}
	anonymous := &provisionBackupClient{t: t, a: a}
	if w := anonymous.request("GET", "/api/public/documents/"+migrate.DocstoreID("shared", "file", 1)+"/download", nil); w.Code != 200 || w.Body.String() != string(content) {
		t.Fatal("restore lost explicitly public source file", w.Code)
	}
}
