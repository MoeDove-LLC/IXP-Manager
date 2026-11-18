package app

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/migrate"
)

func legacyDocstoreTestBundle(t *testing.T) ([]byte, migrate.DocstoreBundle) {
	t.Helper()
	p := migrate.DocstoreImport{Version: 1, SourceTimezone: "UTC"}
	for _, kind := range []string{"shared", "customer"} {
		facts := map[string]string{"id": "1", "name": "Mixed source folder"}
		member := ""
		if kind == "customer" {
			facts["cust_id"], member = "1", "legacy-members-1"
		}
		p.Directories = append(p.Directories, migrate.DocstoreDirectorySource{ID: migrate.DocstoreID(kind, "directory", 1), LegacyID: 1, Kind: kind, MemberID: member, Name: facts["name"], Facts: facts})
		for privilege := -1; privilege <= 3; privilege++ {
			id := int64(privilege + 2)
			name, raw := "file-"+strconv.Itoa(privilege)+".txt", strconv.Itoa(privilege)
			body := []byte(kind + "-" + raw + " PRIVATE FILE BYTES")
			sum := sha256.Sum256(body)
			facts := map[string]string{"id": strconv.FormatInt(id, 10), "name": name, "min_privs": raw, "path": kind + "/" + name, "disk": "local", "created_by": "OLD-ACTOR-ONLY", "created_at": "2020-01-01 12:00:00"}
			key := "docstore_directory_id"
			if kind == "customer" {
				facts["cust_id"], key = "1", "docstore_customer_directory_id"
			}
			facts[key] = "1"
			p.Files = append(p.Files, migrate.DocstoreFileSource{ID: migrate.DocstoreID(kind, "file", id), LegacyID: id, Kind: kind, DirectoryID: migrate.DocstoreID(kind, "directory", 1), MemberID: member, Name: name, MinPrivileges: privilege, StorageKey: facts["path"], Size: int64(len(body)), Checksum: hex.EncodeToString(sum[:]), Entry: migrate.DocstoreFileEntry(kind, id), Facts: facts})
		}
	}
	p.Logs = []migrate.DocstoreLogSource{{ID: migrate.DocstoreID("shared", "log", 1), LegacyID: 1, FileID: migrate.DocstoreID("shared", "file", 2), CreatedAt: "2020-01-01 12:00:00", Facts: map[string]string{"id": "1", "docstore_file_id": "2", "created_at": "2020-01-01 12:00:00"}}}
	bundle := migrate.DocstoreBundle{Version: 1, Kind: migrate.DocstoreBundleKind, Source: "ixp-manager-legacy", Docstore: p}
	if err := migrate.ValidateDocstore(&p, true); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	entry := func(name string, data []byte) {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600, Size: int64(len(data)), Format: tar.FormatUSTAR}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	entry("manifest.json", raw)
	for _, f := range p.Files {
		entry(f.Entry, []byte(f.Kind+"-"+strconv.Itoa(f.MinPrivileges)+" PRIVATE FILE BYTES"))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes(), bundle
}

func (c *client) importLegacyDocstoreBytes(data []byte, hook func()) *httptest.ResponseRecorder {
	c.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("bundle", "documents.tar.gz")
	if err != nil {
		c.t.Fatal(err)
	}
	part.Write(data)
	mw.Close()
	var reader io.Reader = bytes.NewReader(body.Bytes())
	if hook != nil {
		reader = &legacyPatchReadHook{Reader: reader, hook: hook}
	}
	r := httptest.NewRequest("POST", "/api/import/docstore", reader)
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

func TestLegacyDocstoreImportAndHTTPPermissions(t *testing.T) {
	c, _ := setupClient(t)
	if err := c.a.store.Update(func(s *State) error {
		s.Resources["members"] = append(s.Resources["members"], Record{"id": "legacy-members-1", "name": "Old member", "asn": float64(64501), "createdAt": "2026-01-01T00:00:00Z"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	data, _ := legacyDocstoreTestBundle(t)
	responseRecord(t, c.importLegacyDocstoreBytes(data, nil))
	c.create("users", Record{"name": "Member", "email": "old-doc-member@example.test", "role": "member", "memberId": "legacy-members-1", "password": "member-long-password"})
	c.create("users", Record{"name": "Member admin", "email": "old-doc-admin@example.test", "role": "member", "memberId": "legacy-members-1", "memberAdminIds": []string{"legacy-members-1"}, "password": "member-long-password"})
	member := docLoginClient(t, c.a, "old-doc-member@example.test", "member-long-password")
	admin := docLoginClient(t, c.a, "old-doc-admin@example.test", "member-long-password")
	anon := &client{a: c.a, t: t}
	for _, test := range []struct {
		c     *client
		path  string
		count int
	}{{member, "/api/documents", 4}, {admin, "/api/documents", 6}, {anon, "/api/public/documents", 1}, {c, "/api/documents", 10}} {
		listing := test.c.ok("GET", test.path, nil)
		if len(listing["items"].([]any)) != test.count || len(listing["directories"].([]any)) != test.count {
			t.Fatal("mixed source-folder permissions changed", test.path, listing)
		}
	}
	for _, actor := range []*client{member, anon} {
		if w := actor.request("GET", "/api/documents/legacy", nil); w.Code != 403 && w.Code != 401 {
			t.Fatal("source access archive exposed to member/public", w.Code)
		}
	}
	archive := c.ok("GET", "/api/documents/legacy", nil)
	if archive["accessRecordsAreSuccessfulDownloads"] != false || len(archive["archive"].(map[string]any)["logs"].([]any)) != 1 {
		t.Fatal("legacy access archive was not readable or misrepresented old logs")
	}
	id := migrate.DocstoreID("customer", "file", 4) // min_privs=2
	if w := member.request("GET", "/api/documents/"+id+"/download", nil); w.Code != 404 {
		t.Fatal("ordinary member read member-admin file")
	}
	if w := admin.request("GET", "/api/documents/"+id+"/download", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "customer-2") {
		t.Fatal("member administrator could not read file", w.Code)
	}
	for _, method := range []string{"PUT", "DELETE"} {
		if w := c.request(method, "/api/documents/"+id, Record{"name": "replacement.txt"}); w.Code != 409 {
			t.Fatal("legacy document became writable", method, w.Code)
		}
	}
	before := c.a.store.Snapshot()
	if w := c.importLegacyDocstoreBytes(data, nil); w.Code != 409 {
		t.Fatal("duplicate archive accepted", w.Code)
	}
	after := c.a.store.Snapshot()
	if len(after.Documents) != len(before.Documents) || len(after.Audit) != len(before.Audit) {
		t.Fatal("failed duplicate import mutated state")
	}
	for _, event := range after.Audit {
		if event.Actor == "OLD-ACTOR-ONLY" {
			t.Fatal("historical actor became a current audit identity")
		}
	}
	if err := c.a.store.Update(func(s *State) error { s.Resources["members"][0]["createdAt"] = "2026-02-01T00:00:00Z"; return nil }); err != nil {
		t.Fatal(err)
	}
	if w := admin.request("HEAD", "/api/documents/"+id+"/download", nil); w.Code != 404 {
		t.Fatal("recreated member retained archived file access")
	}
}

func TestLegacyDocstoreFailedImportDiscardsProvisionalBlobs(t *testing.T) {
	for _, mode := range []string{"trailing", "authority", "logout", "quota"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := setupClient(t)
			data, _ := legacyDocstoreTestBundle(t)
			var hook func()
			switch mode {
			case "trailing":
				data = append(data, []byte("extra")...)
			case "authority":
				hook = func() {
					c.a.store.Update(func(s *State) error { s.Resources["users"][0]["role"] = "viewer"; return nil })
				}
			case "quota":
				c.a.documentQuota = 1
			case "logout":
				hook = func() {
					if w := c.request("POST", "/api/logout", nil); w.Code != 200 {
						t.Fatal("logout fixture failed", w.Code)
					}
				}
			}
			if w := c.importLegacyDocstoreBytes(data, hook); w.Code < 400 {
				t.Fatal("invalid import accepted", mode)
			}
			s := c.a.store.Snapshot()
			if s.LegacyDocstore != nil || len(s.Documents) != 0 {
				t.Fatal("failed import left committed documents")
			}
			files, err := os.ReadDir(c.a.blobDir())
			if err != nil || len(files) != 0 {
				t.Fatal("failed import left provisional blobs", err, len(files))
			}
		})
	}
}

func TestLegacyDocstoreConcurrentImportCannotBeReplaced(t *testing.T) {
	c, _ := setupClient(t)
	data, bundle := legacyDocstoreTestBundle(t)
	f := bundle.Docstore.Files[1]
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "replacement.txt")
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(part, "ATTEMPTED REPLACEMENT")
	mw.WriteField("checksum", f.Checksum)
	mw.Close()
	reader := &legacyPatchReadHook{Reader: bytes.NewReader(body.Bytes()), hook: func() {
		responseRecord(t, c.importLegacyDocstoreBytes(data, nil))
	}}
	r := httptest.NewRequest("POST", "/api/documents/"+f.ID+"/content", reader)
	r.RemoteAddr = "127.0.0.1:10000"
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("X-CSRF-Token", c.csrf)
	r.AddCookie(c.cookie)
	w := httptest.NewRecorder()
	c.a.ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatal("in-flight replacement bypassed newly imported read-only binding", w.Code, w.Body.String())
	}
	s := c.a.store.Snapshot()
	d := documentByID(&s, f.ID)
	if d == nil || d.Version != 1 || d.Checksum != f.Checksum || len(d.Revisions) != 0 {
		t.Fatal("concurrent upload changed immutable content")
	}
	files, err := os.ReadDir(c.a.blobDir())
	if err != nil || len(files) != len(bundle.Docstore.Files) {
		t.Fatal("rejected replacement left unreferenced content", err)
	}
}

func TestLegacyDocstorePerFilePrivileges(t *testing.T) {
	s := State{Resources: map[string][]Record{"members": {{"id": "one", "createdAt": "original"}, {"id": "two", "createdAt": "original"}}}}
	actors := []Record{nil, {"role": "member", "memberId": "one", "memberRole": "member"}, {"role": "member", "memberId": "one", "memberRole": "member-admin"}, {"role": "member", "memberId": "two", "memberRole": "member-admin"}, {"role": "viewer"}, {"role": "operator"}, {"role": "admin"}}
	for _, kind := range []string{"shared", "customer"} {
		for _, privilege := range []int{-1, 0, 1, 2, 3, 4} {
			f := LegacyDocstoreFile{Source: migrate.DocstoreFileSource{Kind: kind, MinPrivileges: privilege}}
			if kind == "customer" {
				f.Source.MemberID, f.MemberCreatedAt = "one", "original"
			}
			for i, actor := range actors {
				want := i >= 4
				if privilege >= 0 && privilege <= 2 {
					if kind == "shared" {
						want = want || privilege == 0 || privilege == 1 && i >= 1 || privilege == 2 && (i == 2 || i == 3)
					} else {
						want = want || (i == 1 || i == 2) && (privilege <= 1 || i == 2)
					}
				}
				if got := legacyDocstoreVisible(&s, &f, actor); got != want {
					t.Fatalf("kind=%s privilege=%d actor=%d: got %t, want %t", kind, privilege, i, got, want)
				}
			}
		}
	}
}

func TestLegacyDocstoreEntityAndReviewRemainClosed(t *testing.T) {
	s := State{Resources: map[string][]Record{"members": {{"id": "one", "createdAt": "original"}}}}
	actor := Record{"role": "member", "memberId": "one", "memberRole": "member-admin"}
	f := LegacyDocstoreFile{Source: migrate.DocstoreFileSource{Kind: "customer", MemberID: "one", MinPrivileges: 0}, MemberCreatedAt: "original"}
	if !legacyDocstoreVisible(&s, &f, actor) {
		t.Fatal("original member cannot read its imported file")
	}
	s.Resources["members"][0]["createdAt"] = "replacement"
	if legacyDocstoreVisible(&s, &f, actor) {
		t.Fatal("reused member ID regained former entity access")
	}
	if legacyDocstoreScope(&s, &f) != "staff" {
		t.Fatal("recreated member was still presented as an authorized owner")
	}
	s.Resources["members"] = nil
	if legacyDocstoreVisible(&s, &f, actor) {
		t.Fatal("deleted member retained access")
	}
	s.Resources["members"] = []Record{{"id": "one", "createdAt": "original"}}
	f.Review = "missing source association"
	if legacyDocstoreVisible(&s, &f, actor) {
		t.Fatal("unresolved import became readable")
	}
	f.Source.Kind, f.Source.MemberID = "shared", ""
	if legacyDocstoreVisible(&s, &f, nil) {
		t.Fatal("unresolved import became public")
	}
}

func TestLegacyDocstoreProjectionDoesNotExposeSourceFacts(t *testing.T) {
	f := LegacyDocstoreFile{Source: migrate.DocstoreFileSource{ID: "legacy-shared-file-1", Kind: "shared", Name: "member-guide.txt", MinPrivileges: 2, Facts: map[string]string{"path": "PRIVATE-STORAGE-PATH", "created_by": "PRIVATE-OLD-ACTOR"}}, DocumentID: "legacy-shared-file-1", DirectoryID: "dir-legacy-shared-file-1"}
	s := State{LegacyDocstore: &LegacyDocstoreState{Files: []LegacyDocstoreFile{f}}}
	d := Document{ID: f.DocumentID, Name: f.Source.Name, Visibility: "staff", DirectoryID: f.DirectoryID}
	view := legacyDocumentView(&s, d)
	if view["readOnly"] != true || view["legacyDocstore"] != true || view["visibility"] != "authenticated-admin" {
		t.Fatal("document did not expose truthful read-only access level")
	}
	b, err := json.Marshal(view)
	if err != nil || strings.Contains(string(b), "PRIVATE-") {
		t.Fatal("source path or historical actor leaked into member listing")
	}
	member := Record{"role": "member", "memberRole": "member"}
	admin := Record{"role": "member", "memberRole": "member-admin"}
	dir := Directory{ID: f.DirectoryID, Visibility: "staff"}
	if documentReadable(&s, d, member) || directoryReadable(&s, dir, member) || !documentReadable(&s, d, admin) || !directoryReadable(&s, dir, admin) {
		t.Fatal("document and directory visibility disagree with per-file privilege")
	}
}
