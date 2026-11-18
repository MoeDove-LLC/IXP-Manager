package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/ixf"
)

func TestIXFVersionedRoutesAndPublicationBoundary(t *testing.T) {
	c, _ := setupClient(t)
	anon := &client{a: c.a, t: t}
	fixture := networkFixture()
	fixture.Resources["connections"] = []Record{lagConnection()}
	fixture.Resources["members"][0]["email"] = "private-contact@example.net"
	fixture.Resources["members"][0]["notes"] = "private-member-note"
	if err := c.a.store.Update(func(s *State) error {
		for kind, rows := range fixture.Resources {
			s.Resources[kind] = rows
		}
		s.Settings = fixture.Settings
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	publicBases := []string{"/api/public/ixf", "/api/v4/member-export/ixf", "/apiv1/member-list/list"}
	for _, base := range append(append([]string(nil), publicBases...), "/participants.json") {
		if w := anon.request("GET", base, nil); w.Code != 404 {
			t.Fatalf("unversioned publication bypass: %s: %d", base, w.Code)
		}
	}
	for _, version := range []string{"0.6", "0.7", "1.0"} {
		for _, base := range publicBases {
			path := base + "/" + version
			if strings.HasPrefix(base, "/apiv1/") {
				path = base + "/version/" + version
			}
			if w := anon.request("GET", path+"?access_key=old-key&withtags=1", nil); w.Code != 404 {
				t.Fatalf("versioned publication bypass: %s: %d", path, w.Code)
			}
		}
		private := "/api/ixf/" + version
		if w := anon.request("GET", private, nil); w.Code != 401 {
			t.Fatal("private versioned export is anonymous")
		}
		if got := c.ok("GET", private, nil)["version"]; got != version {
			t.Fatalf("wrong private version: %v", got)
		}
	}
	c.ok("PUT", "/api/settings", Record{"name": "Test IXP", "asn": 64500, "ixfId": 42, "shortName": "TIX", "publishIxf": true})
	if got := anon.ok("GET", "/participants.json", nil)["version"]; got != "1.0" {
		t.Fatal("well-known legacy participant endpoint did not return the public 1.0 feed")
	}
	for _, base := range publicBases {
		if got := anon.ok("GET", base, nil)["version"]; got != "1.0" {
			t.Fatal("default version changed")
		}
		for _, version := range []string{"0.6", "0.7", "1.0"} {
			path := base + "/" + version
			if strings.HasPrefix(base, "/apiv1/") {
				path = base + "/version/" + version
			}
			w := anon.request("GET", path+"?withtags=1&ixfid_1=999&version=unknown", nil)
			if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("public alias failed: %s: %d", path, w.Code)
			}
			var doc ixf.Document
			if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
				t.Fatal(err)
			}
			if doc.Version != version || doc.IXPList[0].IXFID != 42 || len(doc.MemberList) != 1 || len(doc.MemberList[0].Connections[0].Interfaces) != 2 {
				t.Fatalf("alias changed version, identities, or LAG: %s", path)
			}
			for _, secret := range []string{"private-contact", "private-member-note", "admin@example.net", "192.0.2.250", "passwordHash"} {
				if strings.Contains(w.Body.String(), secret) {
					t.Fatalf("private field leaked: %s", secret)
				}
			}
			if w := anon.request("POST", path, nil); w.Code != 405 {
				t.Fatal("public export accepted a mutation")
			}
		}
	}
	for _, path := range []string{"/api/public/ixf/1.1", "/api/v4/member-export/ixf/0.5", "/apiv1/member-list/list/version/latest"} {
		if w := anon.request("GET", path, nil); w.Code != 400 {
			t.Fatalf("unknown version silently changed: %s: %d", path, w.Code)
		}
	}
	if err := c.a.store.Update(func(s *State) error { s.Resources["ports"][0]["speed"] = 0; return nil }); err != nil {
		t.Fatal(err)
	}
	if w := anon.request("GET", "/api/public/ixf/0.6", nil); w.Code != 503 || strings.Contains(w.Body.String(), "c1") {
		t.Fatal("invalid inventory produced a partial feed or exposed details")
	}
	if w := c.request("GET", "/api/ixf/0.6", nil); w.Code != 422 {
		t.Fatal("staff cannot diagnose invalid inventory")
	}
}

func TestIXFVersionedTokenScopeAndRegistryID(t *testing.T) {
	c, _ := setupClient(t)
	c.ok("PUT", "/api/settings", Record{"name": "Test IXP", "asn": 64500, "ixfId": 0, "shortName": "TIX"})
	exports := c.ok("POST", "/api/account/tokens", Record{"name": "IX-F export", "currentPassword": "very-long-test-password", "scopes": []string{"exports:read"}})
	read := c.ok("POST", "/api/account/tokens", Record{"name": "Inventory only", "currentPassword": "very-long-test-password", "scopes": []string{"resources:read"}})
	if w := bearerRequest(c.a, str(exports, "token"), "GET", "/api/ixf/0.6", nil); w.Code != 200 || strings.Contains(w.Body.String(), `"ixf_id"`) {
		t.Fatalf("0.6 optional registry ID failed: %d %s", w.Code, w.Body.String())
	}
	for _, version := range []string{"0.6", "0.7", "1.0"} {
		path := "/api/ixf/" + version
		if w := bearerRequest(c.a, str(read, "token"), "GET", path, nil); w.Code != 403 {
			t.Fatal("versioned export bypassed token scope")
		}
		if version != "0.6" {
			if w := c.request("GET", path+"?ignore_missing_ixfid=1&ixfid_1=777", nil); w.Code != 422 {
				t.Fatal("query invented required registry ID")
			}
		}
	}
	member := c.create("members", Record{"name": "Member", "asn": 64505, "status": "active"})
	c.create("users", Record{"name": "Member user", "email": "ixf-member@example.net", "password": "member-long-password", "role": "member", "memberId": str(member, "id")})
	m := loginClient(t, c.a, Record{"email": "ixf-member@example.net", "password": "member-long-password"})
	for _, version := range []string{"0.6", "0.7", "1.0"} {
		if w := m.request("GET", "/api/ixf/"+version, nil); w.Code != 403 {
			t.Fatal("member accessed staff export")
		}
	}
}
