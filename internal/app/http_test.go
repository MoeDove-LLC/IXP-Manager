package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/moedove/ixp-manager/internal/routing"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type client struct {
	a      *App
	cookie *http.Cookie
	csrf   string
	t      *testing.T
}

func (c *client) request(method, path string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	// Domain fixtures describe an immediate administrative update. They read a
	// fresh revision explicitly; concurrency tests use requestResourceRevision
	// with a captured token instead of this fixture convenience.
	resourceKind, resourceID := fixtureResourcePath(method, path)
	if resourceKind != "" {
		if row, ok := body.(Record); ok {
			copy := Record{}
			for key, value := range row {
				if key != "_etag" {
					copy[key] = value
				}
			}
			body = copy
		}
		if row, ok := body.(map[string]any); ok {
			copy := Record{}
			for key, value := range row {
				if key != "_etag" {
					copy[key] = value
				}
			}
			body = copy
		}
	}
	var b bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&b).Encode(body); err != nil {
			c.t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, &b)
	r.RemoteAddr = "127.0.0.1:10000"
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != nil {
		r.AddCookie(c.cookie)
	}
	r.Header.Set("X-CSRF-Token", c.csrf)
	if resourceKind != "" && resourceID != "" && (method == "PUT" || method == "DELETE") {
		s := c.a.store.Snapshot()
		if record := find(&s, resourceKind, resourceID); record != nil {
			r.Header.Set("If-Match", fmt.Sprintf("%q", c.a.resourceETag(resourceKind, record)))
		}
	}
	w := httptest.NewRecorder()
	c.a.ServeHTTP(w, r)
	return w
}

func fixtureResourcePath(method, path string) (string, string) {
	if !strings.HasPrefix(path, "/api/resources/") {
		return "", ""
	}
	parts := strings.Split(strings.TrimPrefix(strings.SplitN(path, "?", 2)[0], "/api/resources/"), "/")
	if len(parts) < 1 || len(parts) > 2 || parts[0] == provisionProfileKind {
		return "", ""
	}
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}
func (c *client) ok(method, path string, body any) Record {
	c.t.Helper()
	w := c.request(method, path, body)
	if w.Code < 200 || w.Code >= 300 {
		c.t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
	}
	var result Record
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		c.t.Fatal(err)
	}
	return result
}
func setupClient(t *testing.T) (*client, string) {
	t.Helper()
	dir := t.TempDir()
	a, err := New(Config{DataDir: dir, BirdBinary: "/nonexistent/bird"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	c := &client{a: a, t: t}
	w := c.request("POST", "/api/setup", Record{"name": "Admin", "email": "admin@example.net", "password": "very-long-test-password", "ixpName": "Test IXP", "asn": 64500, "setupToken": a.BootstrapToken()})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	c.cookie = w.Result().Cookies()[0]
	var data struct {
		CSRF string `json:"csrfToken"`
	}
	json.Unmarshal(w.Body.Bytes(), &data)
	c.csrf = data.CSRF
	return c, dir
}
func (c *client) create(kind string, b Record) Record { return c.ok("POST", "/api/resources/"+kind, b) }
func TestAuthAndCSRF(t *testing.T) {
	c, _ := setupClient(t)
	if !c.cookie.HttpOnly || c.cookie.SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe session cookie")
	}
	if w := c.request("POST", "/api/setup", Record{"name": "X", "email": "x@example.net", "password": "very-long-test-password", "ixpName": "X", "asn": 1}); w.Code == 200 {
		t.Fatal("setup reused")
	}
	old := c.csrf
	c.csrf = "wrong"
	if w := c.request("POST", "/api/resources/locations", Record{"name": "bad"}); w.Code != 403 {
		t.Fatal(w.Code)
	}
	c.csrf = old
	r := httptest.NewRequest("POST", "/api/logout", nil)
	r.AddCookie(c.cookie)
	r.Header.Set("X-CSRF-Token", c.csrf)
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	c.a.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross origin accepted")
	}
	c.ok("POST", "/api/logout", nil)
	if w = c.request("GET", "/api/session", nil); w.Code != 401 {
		t.Fatal("session survived logout")
	}
}
func TestPersistenceAndLock(t *testing.T) {
	c, dir := setupClient(t)
	m := c.create("members", Record{"name": "Example", "asn": 64501, "status": "active"})
	if _, err := OpenStore(dir); err == nil {
		t.Fatal("second writer acquired store")
	}
	if info, err := os.Stat(filepath.Join(dir, "state.json")); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("store permissions")
	}
	c.a.Close()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	state := s.Snapshot()
	if find(&state, "members", str(m, "id")) == nil {
		t.Fatal("member not persisted")
	}
	before := s.Snapshot()
	err = s.Update(func(state *State) error { state.Settings["invalid"] = func() {}; return nil })
	if err == nil {
		t.Fatal("invalid state committed")
	}
	if len(s.Snapshot().Settings) != len(before.Settings) {
		t.Fatal("failed transaction changed state")
	}
}
func TestResourceConstraintsAndAtomicImport(t *testing.T) {
	c, _ := setupClient(t)
	loc := c.create("locations", Record{"name": "DC"})
	sw := c.create("switches", Record{"name": "sw1", "locationId": loc["id"]})
	if w := c.request("DELETE", "/api/resources/locations/"+str(loc, "id"), nil); w.Code != 400 {
		t.Fatal("referenced location removed")
	}
	if w := c.request("POST", "/api/resources/ports", Record{"name": "p1", "switchId": "missing"}); w.Code != 400 {
		t.Fatal("dangling ref accepted")
	}
	c.create("ports", Record{"name": "p1", "switchId": sw["id"]})
	if w := c.request("POST", "/api/resources/ports", Record{"name": "p1", "switchId": sw["id"]}); w.Code != 400 {
		t.Fatal("duplicate port accepted")
	}
	if w := c.request("POST", "/api/resources/members", Record{"name": "x", "asn": 1.1}); w.Code != 400 {
		t.Fatal("fractional ASN accepted")
	}
	if w := c.request("POST", "/api/resources/members", Record{"name": "x", "asn": 10, "shell": "evil"}); w.Code != 400 {
		t.Fatal("unknown field accepted")
	}
	payload := Record{"version": 1, "resources": Record{"locations": []Record{{"id": "new-location", "name": "Imported"}}, "racks": []Record{{"id": "new-rack", "name": "R", "locationId": "missing"}}}}
	if w := c.request("POST", "/api/import", payload); w.Code != 400 {
		t.Fatal(w.Body.String())
	}
	s := c.a.store.Snapshot()
	if find(&s, "locations", "new-location") != nil {
		t.Fatal("partial import committed")
	}
}
func TestTenancyAndLastAdmin(t *testing.T) {
	c, _ := setupClient(t)
	m1 := c.create("members", Record{"name": "One", "asn": 64501})
	m2 := c.create("members", Record{"name": "Two", "asn": 64502})
	c.create("contacts", Record{"name": "Private", "memberId": m2["id"], "email": "secret@example.net"})
	u := c.create("users", Record{"name": "Member", "email": "member@example.net", "role": "member", "memberId": m1["id"], "password": "member-very-long-password"})
	if _, ok := u["passwordHash"]; ok {
		t.Fatal("password leaked")
	}
	users := c.ok("GET", "/api/resources/users", nil)["items"].([]any)
	var adminID string
	for _, v := range users {
		r := v.(map[string]any)
		if r["role"] == "admin" {
			adminID = r["id"].(string)
		}
	}
	if w := c.request("DELETE", "/api/resources/users/"+adminID, nil); w.Code != 400 {
		t.Fatal("last admin removed")
	}
	member := &client{a: c.a, t: t}
	w := member.request("POST", "/api/login", Record{"email": "member@example.net", "password": "member-very-long-password"})
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	member.cookie = w.Result().Cookies()[0]
	var login Record
	json.Unmarshal(w.Body.Bytes(), &login)
	member.csrf = str(login, "csrfToken")
	items := member.ok("GET", "/api/resources/members", nil)["items"].([]any)
	if len(items) != 1 {
		t.Fatal("member sees other members")
	}
	if w = member.request("GET", "/api/resources/contacts", nil); strings.Contains(w.Body.String(), "secret@example.net") {
		t.Fatal("tenant leak")
	}
	if w = member.request("POST", "/api/resources/members", Record{"name": "bad", "asn": 10}); w.Code != 403 {
		t.Fatal("member can write")
	}
	if w = member.request("GET", "/api/audit", nil); w.Code != 403 {
		t.Fatal("member sees audit")
	}
}
func TestDeploymentFailsClosed(t *testing.T) {
	c, _ := setupClient(t)
	v := c.create("vlans", Record{"name": "Peering", "tag": 100, "ipv4Prefix": "192.0.2.0/24"})
	r := c.create("routers", Record{"name": "rs1", "asn": 64500, "routerId": "192.0.2.1", "localIpv4": "192.0.2.1", "vlanId": v["id"]})
	d := c.ok("POST", "/api/deployments", Record{"routerId": r["id"]})
	path := "/api/deployments/" + str(d, "id")
	if w := c.request("POST", path+"/approve", Record{"checksum": d["checksum"]}); w.Code != 409 {
		t.Fatal("unvalidated revision approved")
	}
	if w := c.request("POST", path+"/validate", nil); w.Code != 422 {
		t.Fatal("missing native binary accepted")
	}
	if w := c.request("GET", path+"/config", nil); w.Code != 409 {
		t.Fatal("unapproved config downloaded")
	}
}

func TestNativeDeploymentLifecycle(t *testing.T) {
	binary := os.Getenv("BIRD_BIN")
	if binary == "" {
		t.Skip("BIRD_BIN required for native lifecycle")
	}
	c, _ := setupClient(t)
	c.a.birdBinary = binary
	loc := c.create("locations", Record{"name": "DC"})
	sw := c.create("switches", Record{"name": "sw1", "locationId": loc["id"]})
	port := c.create("ports", Record{"name": "eth1", "switchId": sw["id"], "status": "active"})
	m := c.create("members", Record{"name": "Example", "asn": 64501, "status": "active"})
	v := c.create("vlans", Record{"name": "Peering", "tag": 100, "ipv4Prefix": "192.0.2.0/24"})
	c.create("connections", Record{"name": "Uplink", "memberId": m["id"], "portId": port["id"], "vlanId": v["id"], "ipv4": "192.0.2.10", "status": "active"})
	router := c.create("routers", Record{"name": "rs1", "asn": 64500, "routerId": "192.0.2.1", "localIpv4": "192.0.2.1", "vlanId": v["id"]})
	p := c.create("peers", Record{"name": "Peer", "routerId": router["id"], "memberId": m["id"], "address": "192.0.2.10", "asn": 64501, "enabled": true, "maxPrefixes": 10, "allowedPrefixes": []string{"198.51.100.0/24"}})
	d := c.ok("POST", "/api/deployments", Record{"routerId": router["id"]})
	path := "/api/deployments/" + str(d, "id")
	c.ok("POST", path+"/validate", nil)
	if w := c.request("POST", path+"/approve", Record{"checksum": "tampered"}); w.Code != 409 {
		t.Fatal("checksum not enforced")
	}
	c.ok("POST", path+"/approve", Record{"checksum": d["checksum"]})
	w := c.request("GET", path+"/config", nil)
	expected := ""
	for _, revision := range c.a.store.Snapshot().Deployments {
		if revision.ID == str(d, "id") {
			var err error
			expected, err = c.a.readDeploymentArtifact(revision)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if w.Code != 200 || w.Body.String() != expected || w.Header().Get("X-Config-SHA256") != routing.Checksum(w.Body.String()) {
		t.Fatal("download differs from reviewed version")
	}
	newer := c.ok("POST", "/api/deployments", Record{"routerId": router["id"]})
	newPath := "/api/deployments/" + str(newer, "id")
	c.ok("POST", newPath+"/validate", nil)
	delete(p, "id")
	delete(p, "createdAt")
	delete(p, "updatedAt")
	p["maxPrefixes"] = 20
	s := c.a.store.Snapshot()
	peerID := str(s.Resources["peers"][0], "id")
	c.ok("PUT", "/api/resources/peers/"+peerID, p)
	if w := c.request("POST", newPath+"/approve", Record{"checksum": newer["checksum"]}); w.Code != 409 {
		t.Fatal("stale config approved")
	}
}
func TestImportCrossOrderReferences(t *testing.T) {
	c, _ := setupClient(t)
	fixture := Record{"version": 1, "source": "test", "resources": Record{"switches": []Record{{"id": "legacy-switches-1", "name": "sw", "locationId": "legacy-locations-1"}}, "locations": []Record{{"id": "legacy-locations-1", "name": "DC"}}}}
	result := c.ok("POST", "/api/import", fixture)
	if result["imported"] != float64(2) {
		t.Fatal(result)
	}
	if w := c.request("POST", "/api/import", fixture); w.Code != 400 {
		t.Fatal("duplicate import accepted")
	}
}

func TestNullJSONRejected(t *testing.T) {
	c, _ := setupClient(t)
	r := httptest.NewRequest("POST", "/api/resources/locations", strings.NewReader("null"))
	r.RemoteAddr = "127.0.0.1:1234"
	r.Header.Set("Content-Type", "application/json")
	r.AddCookie(c.cookie)
	r.Header.Set("X-CSRF-Token", c.csrf)
	w := httptest.NewRecorder()
	c.a.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("null body: %d", w.Code)
	}
}
func TestSetupRequiresSecretEvenFromLoopback(t *testing.T) {
	a, err := New(Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	c := &client{a: a, t: t}
	w := c.request("POST", "/api/setup", Record{"name": "Attacker", "email": "bad@example.net", "password": "long-bad-password", "ixpName": "Bad", "asn": 64500})
	if w.Code != 403 {
		t.Fatal("loopback proxy bypassed setup token")
	}
	if strings.Contains(c.request("GET", "/api/setup", nil).Body.String(), a.BootstrapToken()) {
		t.Fatal("setup token exposed")
	}
}

func TestIXFPublicOptInAndPrivacy(t *testing.T) {
	c, _ := setupClient(t)
	anon := &client{a: c.a, t: t}
	if w := anon.request("GET", "/api/public/ixf", nil); w.Code != 404 {
		t.Fatal("public feed enabled by default")
	}
	c.create("members", Record{"name": "PrivatePending", "asn": 64509, "status": "pending", "email": "private@example.net"})
	c.ok("PUT", "/api/settings", Record{"name": "Example IXP", "asn": 64500, "ixfId": 100, "shortName": "EX-IX", "publishIxf": true, "country": "CN"})
	w := anon.request("GET", "/api/public/ixf", nil)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if strings.Contains(w.Body.String(), "private@example.net") || strings.Contains(w.Body.String(), "PrivatePending") || strings.Contains(w.Body.String(), "admin@example.net") {
		t.Fatal("private data leaked")
	}
	c.ok("PUT", "/api/settings", Record{"name": "Example IXP", "asn": 64500, "ixfId": 100, "shortName": "EX-IX", "publishIxf": false})
	if w = anon.request("GET", "/api/v4/member-export/ixf", nil); w.Code != 404 {
		t.Fatal("alias bypasses public gate")
	}
}

func TestMultiPlatformPipeline(t *testing.T) {
	c, _ := setupClient(t)
	caps := c.ok("GET", "/api/router-platforms", nil)["items"].([]any)
	if len(caps) < 4 {
		t.Fatal("platform capabilities missing")
	}
	v := c.create("vlans", Record{"name": "Peering", "tag": 100, "ipv4Prefix": "192.0.2.0/24"})
	router := c.create("routers", Record{"name": "Junos", "platform": "junos", "asn": 64500, "routerId": "192.0.2.1", "localIpv4": "192.0.2.1", "vlanId": v["id"]})
	d := c.ok("POST", "/api/deployments", Record{"routerId": router["id"]})
	if str(d, "platform") != "junos" {
		t.Fatal("platform not pinned in version")
	}
	path := "/api/deployments/" + str(d, "id")
	checked := c.ok("POST", path+"/validate", nil)
	if !strings.Contains(str(checked, "validation"), "structure") || !strings.Contains(str(checked, "validation"), "mandatory") {
		t.Fatal("structural check confused with native semantic check")
	}
	c.ok("POST", path+"/approve", Record{"checksum": d["checksum"]})
	w := c.request("GET", path+"/config", nil)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Disposition"), ".xml") {
		t.Fatal("wrong platform artifact")
	}
	delete(router, "id")
	delete(router, "createdAt")
	delete(router, "updatedAt")
	router["platform"] = "routeros7"
	s := c.a.store.Snapshot()
	id := str(s.Resources["routers"][0], "id")
	c.ok("PUT", "/api/resources/routers/"+id, router)
	if w = c.request("GET", path+"/config", nil); w.Code != 409 {
		t.Fatal("stale approved artifact remains downloadable")
	}
	ros := c.ok("POST", "/api/deployments", Record{"routerId": id})
	if w = c.request("POST", "/api/deployments/"+str(ros, "id")+"/validate", nil); w.Code != 422 {
		t.Fatal("unsupported device validation reported success")
	}
}

func TestCorruptStoreRejectedBeforeServing(t *testing.T) {
	dir := t.TempDir()
	state := State{Version: 1, Settings: Record{}, Resources: map[string][]Record{"peers": {{"id": "bad-peer", "name": "Corrupt", "routerId": "missing", "allowedPrefixes": "not-array"}}}}
	b, _ := json.Marshal(state)
	if err := os.WriteFile(filepath.Join(dir, "state.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	if s, err := OpenStore(dir); err == nil {
		s.Close()
		t.Fatal("invalid stored graph accepted")
	}
}

func TestClosedStoreCannotOverwriteReplacementInstance(t *testing.T) {
	dir := t.TempDir()
	old, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = old.Update(func(s *State) error { s.Settings["name"] = "old"; return nil }); err != nil {
		t.Fatal(err)
	}
	old.Close()
	current, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	if err = current.Update(func(s *State) error { s.Settings["name"] = "new"; return nil }); err != nil {
		t.Fatal(err)
	}
	called := false
	if err = old.Update(func(s *State) error { called = true; s.Settings["name"] = "stale"; return nil }); err == nil || called {
		t.Fatal("closed store ran stale mutation")
	}
	if err = old.Close(); err != nil {
		t.Fatal("close should be idempotent")
	}
	if current.Snapshot().Settings["name"] != "new" {
		t.Fatal("replacement state changed")
	}
}

func TestStoreRejectsUnknownOrIncompleteSchemaWithoutOverwriting(t *testing.T) {
	for _, raw := range []string{`{}`, `{"version":1,"resources":{},"settings":{},"futureFeature":{"important":"keep"}}`} {
		dir := t.TempDir()
		path := filepath.Join(dir, "state.json")
		if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if store, err := OpenStore(dir); err == nil {
			store.Close()
			t.Fatal("unsafe state accepted")
		}
		after, _ := os.ReadFile(path)
		if string(after) != raw {
			t.Fatal("failed startup overwrote state")
		}
	}
}

func TestClosedInstanceCannotUnlinkRestoredArtifacts(t *testing.T) {
	c, dir := setupClient(t)
	blob, err := c.a.writeBlob(strings.NewReader("retained immutable data"))
	if err != nil {
		t.Fatal(err)
	}
	policyID := "fixture"
	policyPath := filepath.Join(c.a.policyDir(), policyID+".json")
	if err = os.WriteFile(policyPath, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	c.a.Close()
	replacement, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	c.a.discardBlob(blob.BlobID)
	c.a.removePolicyArtifact(policyID)
	if _, err = os.Stat(filepath.Join(c.a.blobDir(), blob.BlobID)); err != nil {
		t.Fatal("old blob cleanup crossed close generation")
	}
	if _, err = os.Stat(policyPath); err != nil {
		t.Fatal("old policy cleanup crossed close generation")
	}
}
