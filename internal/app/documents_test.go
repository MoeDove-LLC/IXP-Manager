package app

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func (c *client) upload(path, name, content string, fields map[string]string) *httptest.ResponseRecorder {
	c.t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	f, err := mw.CreateFormFile("file", name)
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err = io.WriteString(f, content); err != nil {
		c.t.Fatal(err)
	}
	for k, v := range fields {
		if err = mw.WriteField(k, v); err != nil {
			c.t.Fatal(err)
		}
	}
	mw.Close()
	r := httptest.NewRequest("POST", path, &b)
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
func responseRecord(t *testing.T, w *httptest.ResponseRecorder) Record {
	t.Helper()
	if w.Code < 200 || w.Code >= 300 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var r Record
	if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	return r
}
func docLoginClient(t *testing.T, a *App, email, password string) *client {
	t.Helper()
	c := &client{a: a, t: t}
	w := c.request("POST", "/api/login", Record{"email": email, "password": password})
	data := responseRecord(t, w)
	c.cookie = w.Result().Cookies()[0]
	c.csrf = str(data, "csrfToken")
	return c
}
func TestDocumentsTenantAndPublicBoundaries(t *testing.T) {
	c, _ := setupClient(t)
	one := c.create("members", Record{"name": "One", "asn": 64501})
	two := c.create("members", Record{"name": "Two", "asn": 64502})
	c.create("users", Record{"name": "Member", "email": "member@example.net", "role": "member", "memberId": one["id"], "password": "member-long-password"})
	member := docLoginClient(t, c.a, "member@example.net", "member-long-password")
	anon := &client{a: c.a, t: t}
	own := responseRecord(t, c.upload("/api/documents", "own.md", "member one private", map[string]string{"memberId": str(one, "id"), "visibility": "member"}))
	other := responseRecord(t, c.upload("/api/documents", "other.md", "member two private", map[string]string{"memberId": str(two, "id"), "visibility": "member"}))
	pub := responseRecord(t, c.upload("/api/documents", "public.html", "<script>bad()</script>", map[string]string{"visibility": "public"}))
	shared := responseRecord(t, c.upload("/api/documents", "shared.txt", "members only", map[string]string{"visibility": "authenticated"}))
	staff := responseRecord(t, c.upload("/api/documents", "internal.txt", "staff only", nil))
	visible := member.ok("GET", "/api/documents", nil)["items"].([]any)
	if len(visible) != 3 {
		t.Fatalf("member listing: %v", visible)
	}
	for _, id := range []string{str(other, "id"), str(staff, "id")} {
		if w := member.request("GET", "/api/documents/"+id+"/download", nil); w.Code != 404 {
			t.Fatal("member read another scope")
		}
	}
	if w := member.request("GET", "/api/documents/"+str(own, "id")+"/download", nil); w.Code != 200 || w.Body.String() != "member one private" {
		t.Fatal("own download failed")
	}
	if w := member.upload("/api/documents", "x.txt", "bad", nil); w.Code != 403 {
		t.Fatal("member can upload")
	}
	if w := anon.request("GET", "/api/public/documents/"+str(shared, "id")+"/download", nil); w.Code != 404 {
		t.Fatal("anonymous read shared member file")
	}
	w := anon.request("GET", "/api/public/documents/"+str(pub, "id")+"/download", nil)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") || w.Header().Get("Content-Type") != "application/octet-stream" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("public active content not forced attachment")
	}
	if w := c.request("DELETE", "/api/resources/members/"+str(two, "id"), nil); w.Code != 400 {
		t.Fatalf("member with documents deleted: %d", w.Code)
	}
	if strings.Contains(anon.request("GET", "/api/public/documents", nil).Body.String(), "member two") {
		t.Fatal("public listing disclosed private data")
	}
}
func TestDocumentTreePermissionsAndCycles(t *testing.T) {
	c, _ := setupClient(t)
	member := c.create("members", Record{"name": "Network", "asn": 64501})
	root := c.ok("POST", "/api/directories", Record{"name": "Root", "visibility": "staff"})
	child := c.ok("POST", "/api/directories", Record{"name": "Child", "parentId": root["id"]})
	doc := responseRecord(t, c.upload("/api/documents", "notes.md", "draft", map[string]string{"directoryId": str(child, "id")}))
	if w := c.request("DELETE", "/api/directories/"+str(root, "id"), nil); w.Code != 409 {
		t.Fatal("nonempty directory removed")
	}
	if w := c.request("PUT", "/api/directories/"+str(root, "id"), Record{"name": "Root", "parentId": child["id"], "visibility": "staff"}); w.Code != 409 {
		t.Fatal("directory cycle accepted")
	}
	c.ok("PUT", "/api/directories/"+str(root, "id"), Record{"name": "Root", "visibility": "member", "memberId": member["id"]})
	s := c.a.store.Snapshot()
	changed := documentByID(&s, str(doc, "id"))
	if changed.Visibility != "member" || changed.MemberID != str(member, "id") {
		t.Fatal("folder permissions not cascaded atomically")
	}
	if w := c.upload("/api/documents", "bad.md", "x", map[string]string{"directoryId": str(child, "id"), "visibility": "public"}); w.Code != 409 {
		t.Fatal("child bypassed directory permissions")
	}
	c.ok("DELETE", "/api/documents/"+str(doc, "id"), nil)
	c.ok("DELETE", "/api/directories/"+str(child, "id"), nil)
	c.ok("DELETE", "/api/directories/"+str(root, "id"), nil)
}
func TestDocumentsIntegrityPathsAndOptimisticEditing(t *testing.T) {
	c, _ := setupClient(t)
	d := responseRecord(t, c.upload("/api/documents", "example.md", "original", nil))
	id := str(d, "id")
	edited := c.ok("PUT", "/api/documents/"+id+"/text", Record{"content": "updated", "checksum": d["checksum"]})
	if w := c.request("PUT", "/api/documents/"+id+"/text", Record{"content": "stale overwrite", "checksum": d["checksum"]}); w.Code != 409 {
		t.Fatal("stale write overwrote current data")
	}
	if w := c.upload("/api/documents/"+id+"/content", "new.md", "new content", nil); w.Code != 409 {
		t.Fatal("file replacement without checksum allowed")
	}
	replaced := responseRecord(t, c.upload("/api/documents/"+id+"/content", "new.md", "replacement", map[string]string{"checksum": str(edited, "checksum")}))
	if replaced["version"] != float64(3) {
		t.Fatal("version history lost")
	}
	if w := c.upload("/api/documents", "a.md", "x", map[string]string{"name": "../../state.json"}); w.Code != 409 {
		t.Fatal("path-like logical name accepted")
	}
	s := c.a.store.Snapshot()
	blob := documentByID(&s, id).BlobID
	path := filepath.Join(c.a.blobDir(), blob)
	if err := os.WriteFile(path, []byte("corrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	if w := c.request("GET", "/api/documents/"+id+"/download", nil); w.Code != 500 {
		t.Fatal("corrupt content served")
	}
	secret := filepath.Join(t.TempDir(), "unrelated")
	os.WriteFile(secret, []byte("never served"), 0600)
	os.Remove(path)
	if err := os.Symlink(secret, path); err != nil {
		t.Fatal(err)
	}
	if w := c.request("GET", "/api/documents/"+id+"/download", nil); w.Code != 500 || strings.Contains(w.Body.String(), "never served") {
		t.Fatal("symlink followed")
	}
}
func TestDocumentQuotaFailureCleansUnpublishedBlobs(t *testing.T) {
	c, _ := setupClient(t)
	c.a.documentQuota = 5
	responseRecord(t, c.upload("/api/documents", "one.txt", "12345", nil))
	if w := c.upload("/api/documents", "two.txt", "6", nil); w.Code != 409 {
		t.Fatal("quota ignored")
	}
	entries, err := os.ReadDir(c.a.blobDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("failed upload leaked content: %v", entries)
	}
	saved := c.a.store.Snapshot()
	if len(saved.Documents) != 1 {
		t.Fatal("failed quota check committed metadata")
	}
}
func TestDocumentsRestartRetainsContentAndRemovesOnlyOrphans(t *testing.T) {
	c, dir := setupClient(t)
	d := responseRecord(t, c.upload("/api/documents", "public.txt", "retained", map[string]string{"visibility": "public"}))
	orphan, err := c.a.writeBlob(strings.NewReader("unpublished"))
	if err != nil {
		t.Fatal(err)
	}
	c.a.Close()
	a, err := New(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	anon := &client{a: a, t: t}
	w := anon.request("GET", "/api/public/documents/"+str(d, "id")+"/download", nil)
	if w.Code != 200 || w.Body.String() != "retained" {
		t.Fatal("restart lost saved content")
	}
	if _, err = os.Stat(filepath.Join(a.blobDir(), orphan.BlobID)); !os.IsNotExist(err) {
		t.Fatal("orphan not reclaimed")
	}
}
