package app

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/moedove/ixp-manager/internal/migrate"
)

func logoPNG(t *testing.T, n uint8) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 8, 4))
	img.Set(1, 1, color.NRGBA{R: n, A: 255})
	var b bytes.Buffer
	if e := png.Encode(&b, img); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
func logoMultipart(c *client, path string, fields map[string]string, fileField string, data []byte, hook func()) *httptest.ResponseRecorder {
	c.t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	p, _ := mw.CreateFormFile(fileField, "synthetic.png")
	_, _ = p.Write(data)
	_ = mw.Close()
	var reader io.Reader = bytes.NewReader(b.Bytes())
	if hook != nil {
		reader = &legacyPatchReadHook{Reader: reader, hook: hook}
	}
	r := httptest.NewRequest("POST", path, reader)
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
func logoUpload(c *client, id, revision string, data []byte, hook func()) *httptest.ResponseRecorder {
	return logoMultipart(c, "/api/members/"+id+"/logo", map[string]string{"expectedRevision": revision}, "file", data, hook)
}
func logoRevisionFor(c *client, id string) string {
	return str(c.ok("GET", "/api/members/"+id+"/logo", nil), "revision")
}
func logoLogin(t *testing.T, a *App, email, password string) *client {
	return docLoginClient(t, a, email, password)
}

func TestMemberLogoLifecyclePrivacyCASAndCleanup(t *testing.T) {
	c, _ := setupClient(t)
	m := c.create("members", Record{"name": "Logo member", "asn": 64501, "status": "active"})
	id := str(m, "id")
	path := "/api/members/" + id + "/logo"
	public := "/api/public/members/" + id + "/logo"
	base := logoRevisionFor(c, id)
	first := responseRecord(t, logoUpload(c, id, base, logoPNG(t, 10), nil))
	if first["published"] != false || first["configured"] != true {
		t.Fatal(first)
	}
	old := c.a.store.Snapshot().MemberLogos[id]
	if w := c.request("GET", public, nil); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := logoUpload(c, id, base, logoPNG(t, 11), nil); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	current := c.ok("PUT", path, Record{"expectedRevision": first["revision"], "published": true})
	if current["publiclyAvailable"] != true {
		t.Fatal(current)
	}
	if w := c.request("GET", public, nil); w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(w.Code, w.Header())
	}
	if w := c.request("HEAD", public, nil); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatal("HEAD", w.Code)
	}
	if w := c.request("DELETE", "/api/resources/members/"+id, nil); w.Code < 400 {
		t.Fatal("member delete silently cascaded logo")
	}
	second := responseRecord(t, logoUpload(c, id, str(current, "revision"), logoPNG(t, 12), nil))
	if second["published"] != true {
		t.Fatal("replacement lost reviewed publication")
	}
	if _, e := os.Stat(filepath.Join(c.a.blobDir(), old.BlobID)); !os.IsNotExist(e) {
		t.Fatal("obsolete logo not cleaned", e)
	}
	removed := c.ok("DELETE", path, Record{"expectedRevision": second["revision"]})
	if removed["configured"] != false || removed["revision"] == base {
		t.Fatal("missing deletion tombstone")
	}
	if w := c.request("GET", public, nil); w.Code != 404 {
		t.Fatal("deleted public content accessible")
	}
	if w := logoUpload(c, id, str(second, "revision"), logoPNG(t, 9), nil); w.Code != 409 {
		t.Fatal("stale recreate accepted", w.Code)
	}
	if w := logoUpload(c, id, str(removed, "revision"), []byte("<svg/>"), nil); w.Code != 400 {
		t.Fatal("SVG accepted")
	}
	if n := logoUsage(ptrState(c.a.store.Snapshot())); n != 0 {
		t.Fatal("deleted logo still counted", n)
	}
}

func TestMemberLogoRoleAndWorkspaceRevocationDuringUpload(t *testing.T) {
	c, _ := setupClient(t)
	a := c.create("members", Record{"name": "A", "asn": 64501, "status": "active"})
	b := c.create("members", Record{"name": "B", "asn": 64502, "status": "active"})
	aid, bid := str(a, "id"), str(b, "id")
	u := c.create("users", Record{"name": "Member admin", "email": "logo-admin@example.test", "password": "logo-member-password", "role": "member", "memberIds": []string{aid, bid}, "memberAdminIds": []string{aid, bid}})
	member := logoLogin(t, c.a, "logo-admin@example.test", "logo-member-password")
	selected := member.ok("POST", "/api/memberships/select", Record{"memberId": aid})
	member.csrf = str(selected, "csrfToken")
	base := logoRevisionFor(member, aid)
	if w := member.request("GET", "/api/members/"+bid+"/logo", nil); w.Code != 404 {
		t.Fatal("foreign selected membership visible", w.Code)
	}
	responseRecord(t, logoUpload(member, aid, base, logoPNG(t, 20), nil))
	base = logoRevisionFor(member, aid)
	w := logoUpload(member, aid, base, logoPNG(t, 21), func() {
		res := member.ok("POST", "/api/memberships/select", Record{"memberId": bid})
		member.csrf = str(res, "csrfToken")
	})
	if w.Code != 403 {
		t.Fatal("in-flight previous workspace committed", w.Code, w.Body.String())
	}
	res := member.ok("POST", "/api/memberships/select", Record{"memberId": aid})
	member.csrf = str(res, "csrfToken")
	w = logoUpload(member, aid, base, logoPNG(t, 22), func() {
		if e := c.a.store.Update(func(s *State) error { find(s, "users", str(u, "id"))["memberAdminIds"] = []any{}; return nil }); e != nil {
			t.Fatal(e)
		}
	})
	if w.Code != 403 {
		t.Fatal("revoked member admin committed", w.Code, w.Body.String())
	}
	if w = member.request("GET", "/api/members/"+aid+"/logo/content", nil); w.Code != 200 {
		t.Fatal("ordinary member lost own read", w.Code)
	}
	if w = member.request("DELETE", "/api/members/"+aid+"/logo", Record{"expectedRevision": base}); w.Code != 403 {
		t.Fatal("ordinary member wrote logo")
	}
	entries, e := os.ReadDir(c.a.blobDir())
	if e != nil {
		t.Fatal(e)
	}
	if len(entries) != 1 {
		t.Fatal("rejected uploads leaked blobs", len(entries))
	}
}

func TestMemberLogoConcurrentReplaceAndSharedQuota(t *testing.T) {
	c, _ := setupClient(t)
	m := c.create("members", Record{"name": "Race", "asn": 64501, "status": "active"})
	id := str(m, "id")
	base := logoRevisionFor(c, id)
	data := logoPNG(t, 30)
	c.a.documentQuota = int64(len(data) - 1)
	if w := logoUpload(c, id, base, data, nil); w.Code != 413 {
		t.Fatal("quota", w.Code)
	}
	c.a.documentQuota = 1 << 20
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); codes <- logoUpload(c, id, base, data, nil).Code }()
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for n := range codes {
		counts[n]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal(counts)
	}
	entries, _ := os.ReadDir(c.a.blobDir())
	if len(entries) != 1 {
		t.Fatal("CAS loser left content", len(entries))
	}
}

func TestMemberLogoImportReviewPrivateAndEntityBinding(t *testing.T) {
	c, _ := setupClient(t)
	c.ok("POST", "/api/import", Record{"version": 1, "resources": Record{"members": []Record{{"id": "legacy-members-1", "name": "Legacy logo", "asn": 64501, "status": "active"}}}})
	data, e := migrate.EncodeLogoBundle([]migrate.LogoItem{{Source: migrate.LogoSource{ID: "1", MemberID: "legacy-members-1", StoredName: "0123456789abcdef0123456789abcdef01234567.png", OriginalName: "legacy.png", UploadedBy: "SOURCE NAME ONLY", UploadedAt: "2010-01-01"}, PNG: logoPNG(t, 40)}})
	if e != nil {
		t.Fatal(e)
	}
	preview := func() Record {
		return responseRecord(t, logoMultipart(c, "/api/import/member-logos/preview", nil, "bundle", data, nil))
	}
	review := preview()
	if e = c.a.store.Update(func(s *State) error {
		find(s, "members", "legacy-members-1")["name"] = "Changed identity label"
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	w := logoMultipart(c, "/api/import/member-logos", map[string]string{"reviewToken": str(review, "reviewToken")}, "bundle", data, nil)
	if w.Code != 409 {
		t.Fatal("stale import review accepted", w.Code)
	}
	review = preview()
	responseRecord(t, logoMultipart(c, "/api/import/member-logos", map[string]string{"reviewToken": str(review, "reviewToken")}, "bundle", data, nil))
	meta := c.ok("GET", "/api/members/legacy-members-1/logo", nil)
	if meta["published"] != false || meta["imported"] != true {
		t.Fatal(meta)
	}
	if w = c.request("GET", "/api/public/members/legacy-members-1/logo", nil); w.Code != 404 {
		t.Fatal("legacy public permission imported")
	}
	state := c.a.store.Snapshot()
	for _, a := range state.Audit {
		if a.Action == "legacy-logo-import" && a.Actor == "SOURCE NAME ONLY" {
			t.Fatal("fabricated actor")
		}
	}
	if e = c.a.store.Update(func(s *State) error {
		find(s, "members", "legacy-members-1")["createdAt"] = "2020-01-01T00:00:00Z"
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if w = c.request("GET", "/api/members/legacy-members-1/logo/content", nil); w.Code != 404 {
		t.Fatal("new member entity inherited logo", w.Code)
	}
}

func TestMemberLogoSharedQuotaAndIndependentIdenticalBlobs(t *testing.T) {
	c, _ := setupClient(t)
	one := c.create("members", Record{"name": "One logo", "asn": 64501})
	two := c.create("members", Record{"name": "Two logo", "asn": 64502})
	a, b := str(one, "id"), str(two, "id")
	data := logoPNG(t, 50)
	responseRecord(t, logoUpload(c, a, logoRevisionFor(c, a), data, nil))
	responseRecord(t, logoUpload(c, b, logoRevisionFor(c, b), data, nil))
	s := c.a.store.Snapshot()
	if s.MemberLogos[a].BlobID == s.MemberLogos[b].BlobID {
		t.Fatal("two permission identities share a blob")
	}
	current := c.ok("DELETE", "/api/members/"+a+"/logo", Record{"expectedRevision": logoRevisionFor(c, a)})
	if current["configured"] != false {
		t.Fatal(current)
	}
	if w := c.request("GET", "/api/members/"+b+"/logo/content", nil); w.Code != 200 {
		t.Fatal("deleting one member removed another's identical image")
	}
	c.a.documentQuota = int64(len(data) + 4)
	if w := c.upload("/api/documents", "quota.txt", "12345", map[string]string{"visibility": "staff"}); w.Code < 400 {
		t.Fatal("document upload ignored logo bytes")
	}
	responseRecord(t, c.upload("/api/documents", "quota.txt", "1234", map[string]string{"visibility": "staff"}))
	if w := logoUpload(c, a, logoRevisionFor(c, a), data, nil); w.Code != 413 {
		t.Fatal("logo upload ignored document bytes", w.Code)
	}
}

func TestMemberLogoUploadRejectsLogoutAndEntityReplacement(t *testing.T) {
	c, _ := setupClient(t)
	m := c.create("members", Record{"name": "Captured logo owner", "asn": 64501})
	id := str(m, "id")
	base := logoRevisionFor(c, id)
	w := logoUpload(c, id, base, logoPNG(t, 60), func() { c.ok("POST", "/api/logout", nil) })
	if w.Code != 403 {
		t.Fatal("logged-out upload committed", w.Code)
	}
	c = docLoginClient(t, c.a, "admin@example.net", "very-long-test-password")
	base = logoRevisionFor(c, id)
	w = logoUpload(c, id, base, logoPNG(t, 61), func() {
		if e := c.a.store.Update(func(s *State) error { find(s, "members", id)["createdAt"] = "2020-01-01T00:00:00Z"; return nil }); e != nil {
			t.Fatal(e)
		}
	})
	if w.Code != 409 {
		t.Fatal("new member entity inherited old upload target", w.Code)
	}
	files, _ := os.ReadDir(c.a.blobDir())
	if len(files) != 0 {
		t.Fatal("failed authority/CAS left blobs")
	}
}
