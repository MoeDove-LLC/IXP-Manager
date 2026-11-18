package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/migrate"
)

func legacyPatchTestBundle(t *testing.T, change func(map[string][]Record)) []byte {
	t.Helper()
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "old-disk")
	tables := map[string][]Record{
		"cust":     {{"id": 1, "name": "Current member A", "autsys": 64501, "status": 1}, {"id": 2, "name": "Current member B", "autsys": 64502, "status": 1}},
		"location": {{"id": 1, "name": "Location"}}, "cabinet": {{"id": 1, "name": "Rack", "locationid": 1}},
		"patch_panel":              {{"id": 1, "cabinet_id": 1, "name": "Legacy panel", "port_prefix": "F", "location_notes": "STAFF PANEL NOTE", "created_at": "2015-01-01 12:00:00"}},
		"patch_panel_port":         {{"id": 1, "patch_panel_id": 1, "number": 1, "state": 7, "customer_id": 1, "private_notes": "STAFF CURRENT NOTE", "assigned_at": "2020-01-02", "loa_code": "OLD-LOA-AUTH-MUST-NOT-IMPORT"}, {"id": 2, "patch_panel_id": 1, "number": 2, "state": 1, "duplex_master_id": 1}},
		"patch_panel_port_history": {{"id": 7, "patch_panel_port_id": 1, "number": 1, "state": 3, "cust_id": 999, "customer": "Deleted historical tenant", "switchport": "retired-switch::xe99", "private_notes": "STAFF HISTORICAL NOTE", "ceased_at": "2019-01-01"}},
	}
	content := map[string]string{}
	addFile := func(table string, id int, parent string, parentID int, name, body string, private bool, key string) {
		if key == "" {
			sum := sha256.Sum256([]byte("old-port-before-move-99-" + name))
			key = hex.EncodeToString(sum[:])
		}
		content[key] = body
		tables[table] = append(tables[table], Record{"id": id, parent: parentID, "name": name, "type": "text/plain", "uploaded_at": "2020-01-02 03:04:05", "uploaded_by": "old-operator-display-only", "size": len(body), "is_private": private, "storage_location": key})
	}
	addFile("patch_panel_port_file", 1, "patch_panel_port_id", 1, "active-current.txt", "CURRENT MEMBER CONTENT", false, "")
	addFile("patch_panel_port_file", 2, "patch_panel_port_id", 1, "staff-physical.txt", "STAFF PHYSICAL CONTENT", true, "")
	addFile("patch_panel_port_file", 4, "patch_panel_port_id", 2, "slave-unowned.txt", "NO RAW CUSTOMER GRANT", false, "")
	shared := str(tables["patch_panel_port_file"][0], "storage_location")
	addFile("patch_panel_port_history_file", 3, "patch_panel_port_history_id", 7, "active-current.txt", "CURRENT MEMBER CONTENT", false, shared)
	if change != nil {
		change(tables)
	}
	for key, body := range content {
		rel, e := migrate.PatchStoragePath(key)
		if e != nil {
			t.Fatal(e)
		}
		p := filepath.Join(sourceRoot, filepath.FromSlash(rel))
		if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(p, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
	}
	input := filepath.Join(root, "source.json")
	raw, e := json.Marshal(Record{"tables": tables})
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(input, raw, 0600); e != nil {
		t.Fatal(e)
	}
	output := filepath.Join(root, "patch.tar.gz")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "run", "./cmd/ixp-migrate", "--input", input, "--patch-bundle", "--patch-files-root", sourceRoot, "--source-timezone", "UTC", "--output", output)
	command.Dir = "../.."
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("synthetic converter: %v: %s", err, out)
	}
	b, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func (c *client) importLegacyPatchBytes(data []byte, extra bool, firstRead func()) *httptest.ResponseRecorder {
	c.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("bundle", "patch.tar.gz")
	if err != nil {
		c.t.Fatal(err)
	}
	part.Write(data)
	if extra {
		mw.WriteField("unexpected", "no")
	}
	mw.Close()
	var reader io.Reader = bytes.NewReader(body.Bytes())
	if firstRead != nil {
		reader = &legacyPatchReadHook{Reader: reader, hook: firstRead}
	}
	r := httptest.NewRequest("POST", "/api/import/patching", reader)
	r.RemoteAddr = "127.0.0.1:10000"
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("X-CSRF-Token", c.csrf)
	if c.cookie != nil {
		r.AddCookie(c.cookie)
	}
	w := httptest.NewRecorder()
	c.a.ServeHTTP(w, r)
	return w
}

type legacyPatchReadHook struct {
	io.Reader
	hook func()
}

func (r *legacyPatchReadHook) Read(b []byte) (int, error) {
	if r.hook != nil {
		f := r.hook
		r.hook = nil
		f()
	}
	return r.Reader.Read(b)
}

func TestLegacyPatchBundleCurrentAndHistoricalPermissions(t *testing.T) {
	bundle := legacyPatchTestBundle(t, nil)
	c, _ := setupClient(t)
	result := responseRecord(t, c.importLegacyPatchBytes(bundle, false, nil))
	if result["loaAuthorizationImported"] != false {
		t.Fatal(result)
	}
	s := c.a.store.Snapshot()
	if len(s.LOAs) != 0 || s.LegacyPatching == nil || len(s.LegacyPatching.Histories) != 1 || len(s.LegacyPatching.Attachments) != 4 {
		t.Fatal("source snapshots/files or authorization boundary lost")
	}
	if strings.Contains(string(mustJSON(s.LegacyPatching)), "OLD-LOA-AUTH") || len(s.Resources["members"]) != 2 {
		t.Fatal("old authorization or historical tenant was manufactured")
	}
	if s.LegacyPatching.Histories[0].Source.State != 3 || s.LegacyPatching.Histories[0].Source.Facts["cust_id"] != "999" {
		t.Fatal("historical snapshot was rewritten")
	}
	currentID := migrate.PatchFileID("current", 1)
	historyID := migrate.PatchFileID("history", 3)
	privateID := migrate.PatchFileID("current", 2)
	slaveID := migrate.PatchFileID("current", 4)
	if documentByID(&s, currentID).BlobID == documentByID(&s, historyID).BlobID {
		t.Fatal("different permission identities shared a blob ID")
	}
	if documentByID(&s, currentID).Checksum == s.LegacyPatching.Attachments[0].Source.StorageKey {
		t.Fatal("old filename-derived key used as content SHA")
	}
	for _, event := range s.PatchHistory {
		if event.ActorID != accountID(c.a) || event.Action != "inventory-post" {
			t.Fatal("old events or old actor invented")
		}
	}
	c.create("users", Record{"name": "A", "email": "patch-a@example.test", "password": "patch-member-password", "role": "member", "memberId": "legacy-members-1"})
	c.create("users", Record{"name": "B", "email": "patch-b@example.test", "password": "patch-member-password", "role": "member", "memberId": "legacy-members-2"})
	member := docLoginClient(t, c.a, "patch-a@example.test", "patch-member-password")
	other := docLoginClient(t, c.a, "patch-b@example.test", "patch-member-password")
	anonymous := &client{a: c.a, t: t}
	listing := member.ok("GET", "/api/documents", nil)
	if len(listing["items"].([]any)) != 1 || len(listing["directories"].([]any)) != 1 {
		t.Fatalf("member scope widened: %v", listing)
	}
	for _, id := range []string{historyID, privateID, slaveID} {
		for _, method := range []string{"GET", "HEAD"} {
			if w := member.request(method, "/api/documents/"+id+"/download", nil); w.Code != 404 {
				t.Fatalf("protected attachment %s disclosed: %d", id, w.Code)
			}
		}
	}
	if w := member.request("GET", "/api/documents/"+currentID+"/download", nil); w.Code != 200 || w.Body.String() != "CURRENT MEMBER CONTENT" {
		t.Fatal(w.Body.String())
	}
	if w := member.request("GET", "/api/documents/"+currentID+"/text", nil); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := other.request("GET", "/api/documents/"+currentID+"/download", nil); w.Code != 404 {
		t.Fatal("other tenant read current attachment")
	}
	if w := anonymous.request("GET", "/api/public/documents/"+currentID+"/download", nil); w.Code != 404 {
		t.Fatal("old public meant Internet-public")
	}
	if w := member.request("GET", "/api/patching/legacy", nil); w.Code != 403 {
		t.Fatal("member read staff snapshots")
	}
	if w := c.request("PUT", "/api/documents/"+historyID, Record{"name": "x", "visibility": "public"}); w.Code != 409 {
		t.Fatal("archive permissions mutable")
	}
	dir := "dir-" + currentID
	if w := c.request("PUT", "/api/directories/"+dir, Record{"name": "x", "visibility": "public"}); w.Code < 400 {
		t.Fatal("folder cascade widened archive permission")
	}
	before := member.ok("GET", "/api/search?q=active-current.txt&types=documents", nil)
	if number(before, "total") == 0 {
		t.Fatal("current authorized file absent from search")
	}
	port := "legacy-patch-ports-1"
	patchAction(c, port, "release", nil)
	if len(member.ok("GET", "/api/documents", nil)["items"].([]any)) != 0 || len(member.ok("GET", "/api/documents", nil)["directories"].([]any)) != 0 {
		t.Fatal("released attachment/managed directory still exposed")
	}
	for _, suffix := range []string{"", "/download", "/text"} {
		if w := member.request("GET", "/api/documents/"+currentID+suffix, nil); w.Code != 404 {
			t.Fatalf("released file route %s disclosed: %d", suffix, w.Code)
		}
	}
	if number(member.ok("GET", "/api/search?q=active-current.txt&types=documents", nil), "total") != 0 {
		t.Fatal("released attachment leaked through cached search")
	}
	view := currentPatch(c, port)
	found := false
	for _, v := range view["attachments"].([]any) {
		found = found || str(Record(v.(map[string]any)), "id") == privateID
	}
	if !found {
		t.Fatal("private physical attachment lost on release")
	}
	patchAction(c, port, "reserve", Record{"memberId": "legacy-members-2"})
	if w := other.request("GET", "/api/documents/"+currentID+"/download", nil); w.Code != 404 {
		t.Fatal("old member content granted to new allocation")
	}
	patchAction(c, port, "release", nil)
	patchAction(c, port, "reserve", Record{"memberId": "legacy-members-1"})
	if w := member.request("GET", "/api/documents/"+currentID+"/download", nil); w.Code != 404 {
		t.Fatal("A-B-A resurrected old attachment")
	}
	if w := c.importLegacyPatchBytes(bundle, false, nil); w.Code < 400 {
		t.Fatal("duplicate import overwritten")
	}
}

func TestLegacyPatchQuarantinesWholePanelAndPinsMemberEntity(t *testing.T) {
	t.Run("other", func(t *testing.T) {
		c, _ := setupClient(t)
		result := responseRecord(t, c.importLegacyPatchBytes(legacyPatchTestBundle(t, func(tables map[string][]Record) { tables["patch_panel_port"][0]["state"] = 999 }), false, nil))
		s := c.a.store.Snapshot()
		if len(s.Resources["patch-panels"]) != 0 || len(s.Resources["patch-ports"]) != 0 || len(s.Documents) != 4 {
			t.Fatal("uncertain panel became available or snapshots lost")
		}
		if number(Record(result["patching"].(map[string]any)), "reviewPanels") != 1 {
			t.Fatal(result)
		}
	})
	t.Run("gap", func(t *testing.T) {
		c, _ := setupClient(t)
		responseRecord(t, c.importLegacyPatchBytes(legacyPatchTestBundle(t, func(tables map[string][]Record) { tables["patch_panel_port"][1]["number"] = 3 }), false, nil))
		s := c.a.store.Snapshot()
		if len(s.Resources["patch-ports"]) != 0 {
			t.Fatal("missing old port synthesized as available")
		}
	})
	t.Run("member-instance", func(t *testing.T) {
		c, _ := setupClient(t)
		responseRecord(t, c.importLegacyPatchBytes(legacyPatchTestBundle(t, nil), false, nil))
		c.create("users", Record{"name": "A", "email": "patch-a@example.test", "password": "patch-member-password", "role": "member", "memberId": "legacy-members-1"})
		m := docLoginClient(t, c.a, "patch-a@example.test", "patch-member-password")
		if err := c.a.store.Update(func(s *State) error {
			find(s, "members", "legacy-members-1")["createdAt"] = time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if w := m.request("GET", "/api/documents/legacy-patch-current-file-1/download", nil); w.Code != 404 {
			t.Fatal("same ID different member entity inherited attachment")
		}
	})
}

func rewriteLegacyPatchBundle(t *testing.T, original []byte) []byte {
	t.Helper()
	contents := map[string][]byte{}
	bundle, e := migrate.ReadPatchBundle(bytes.NewReader(original), func(f migrate.PatchFileSource, r io.Reader) error {
		b, e := io.ReadAll(r)
		contents[f.Entry] = b
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	contents[bundle.Patching.Files[1].Entry][0] ^= 1
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	manifest, _ := json.Marshal(bundle)
	write := func(name string, b []byte) {
		if e := tw.WriteHeader(&tar.Header{Name: name, Size: int64(len(b)), Mode: 0600, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); e != nil {
			t.Fatal(e)
		}
		tw.Write(b)
	}
	write("manifest.json", manifest)
	for _, f := range bundle.Patching.Files {
		write(f.Entry, contents[f.Entry])
	}
	tw.Close()
	gz.Close()
	return out.Bytes()
}
func TestLegacyPatchImportFailureLeavesNoPartialStateOrBlobs(t *testing.T) {
	bundle := legacyPatchTestBundle(t, nil)
	for _, mode := range []string{"checksum", "extra-multipart", "quota", "revoked-during-read", "logout-during-read"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := setupClient(t)
			data := bundle
			extra := false
			var hook func()
			if mode == "checksum" {
				data = rewriteLegacyPatchBundle(t, data)
			}
			if mode == "extra-multipart" {
				extra = true
			}
			if mode == "quota" {
				c.a.documentQuota = 1
			}
			if mode == "revoked-during-read" {
				actorID := accountID(c.a)
				hook = func() {
					if e := c.a.store.Update(func(s *State) error { find(s, "users", actorID)["role"] = "viewer"; return nil }); e != nil {
						t.Fatal(e)
					}
				}
			}
			if mode == "logout-during-read" {
				hook = func() {
					if w := c.request("POST", "/api/logout", nil); w.Code != 200 {
						t.Fatal("logout fixture failed", w.Code)
					}
				}
			}
			w := c.importLegacyPatchBytes(data, extra, hook)
			if w.Code < 400 {
				t.Fatal("unsafe import committed")
			}
			s := c.a.store.Snapshot()
			if len(s.Documents) != 0 || s.LegacyPatching != nil || len(s.Resources["patch-panels"]) != 0 {
				t.Fatal("failed import changed state")
			}
			files, e := os.ReadDir(c.a.blobDir())
			if e != nil || len(files) != 0 {
				t.Fatalf("failed import left blobs: %v %v", files, e)
			}
		})
	}
}
