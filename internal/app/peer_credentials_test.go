package app

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func credentialFixture(t *testing.T) (*client, string, Record) {
	t.Helper()
	c, dir := setupClient(t)
	loc := c.create("locations", Record{"name": "DC"})
	sw := c.create("switches", Record{"name": "sw1", "locationId": loc["id"]})
	port := c.create("ports", Record{"name": "eth1", "switchId": sw["id"], "status": "active"})
	m := c.create("members", Record{"name": "Example", "asn": 64501, "status": "active"})
	v := c.create("vlans", Record{"name": "Peering", "tag": 100, "ipv4Prefix": "192.0.2.0/24"})
	c.create("connections", Record{"name": "Uplink", "memberId": m["id"], "portId": port["id"], "vlanId": v["id"], "ipv4": "192.0.2.10", "status": "active"})
	router := c.create("routers", Record{"name": "rs1", "platform": "junos", "asn": 64500, "routerId": "192.0.2.1", "localIpv4": "192.0.2.1", "vlanId": v["id"]})
	p := c.create("peers", Record{"name": "Peer", "routerId": router["id"], "memberId": m["id"], "address": "192.0.2.10", "asn": 64501, "enabled": true, "maxPrefixes": 10, "allowedPrefixes": []string{"198.51.100.0/24"}})
	return c, dir, p
}

func credentialChange(meta Record) Record {
	return Record{"expectedRevision": meta["revision"], "expectedPeerChecksum": meta["peerChecksum"], "currentPassword": "very-long-test-password"}
}

func editableCredentialPeer(p Record) Record {
	b, _ := json.Marshal(p)
	var result Record
	_ = json.Unmarshal(b, &result)
	for _, k := range []string{"id", "createdAt", "updatedAt"} {
		delete(result, k)
	}
	return result
}

func TestBGPCredentialLifecycleAndDeploymentRedaction(t *testing.T) {
	c, dir, peer := credentialFixture(t)
	id := str(peer, "id")
	path := "/api/peer-credentials/" + id
	meta := c.ok("GET", "/api/peer-credentials?peerId="+id, nil)
	body := credentialChange(meta)
	body["secret"] = "TestSecretAlpha_123+/="
	secret := body["secret"].(string)
	configured := c.ok("PUT", path, body)
	if configured["configured"] != true || configured["revision"] == meta["revision"] || configured["generatedSecret"] != nil {
		t.Fatal("credential metadata or one-time secret behavior incorrect")
	}
	if w := c.request("PUT", path, body); w.Code != 409 {
		t.Fatal("stale credential update accepted", w.Code)
	}
	edit := editableCredentialPeer(peer)
	edit["address"] = "192.0.2.11"
	if w := c.request("PUT", "/api/resources/peers/"+id, edit); w.Code != 400 {
		t.Fatal("credential moved to a different peer identity", w.Code)
	}
	edit["address"] = peer["address"]
	edit["name"] = "Renamed"
	c.ok("PUT", "/api/resources/peers/"+id, edit)
	d := c.ok("POST", "/api/deployments", Record{"routerId": peer["routerId"]})
	dpath := "/api/deployments/" + str(d, "id")
	for _, route := range []string{"/api/deployments", dpath, dpath + "/diff", "/api/export", "/api/audit", "/api/resources/peers", path} {
		w := c.request("GET", route, nil)
		if w.Code != 200 || strings.Contains(w.Body.String(), secret) || strings.Contains(w.Body.String(), "ciphertext") {
			t.Fatalf("redacted route %s returned sensitive data or failed: %d", route, w.Code)
		}
	}
	c.ok("POST", dpath+"/validate", nil)
	c.ok("POST", dpath+"/approve", Record{"checksum": d["checksum"]})
	w := c.request("GET", dpath+"/config", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), secret) {
		t.Fatal("approved private artifact did not contain the required credential")
	}
	rotation := credentialChange(configured)
	rotation["generate"] = true
	rotated := c.ok("PUT", path, rotation)
	generated := str(rotated, "generatedSecret")
	if len(generated) < 32 || generated == secret {
		t.Fatal("generated credential has insufficient entropy length")
	}
	if w = c.request("GET", dpath+"/config", nil); w.Code != 409 {
		t.Fatal("old approved credential remained downloadable after rotation")
	}
	for _, value := range []string{secret, generated} {
		err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err == nil && bytes.Contains(data, []byte(value)) {
				t.Errorf("secret persisted as plaintext in %s", filepath.Base(path))
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	next := c.ok("POST", "/api/deployments", Record{"routerId": peer["routerId"]})
	if next["checksum"] == d["checksum"] {
		t.Fatal("credential rotation did not change approval checksum")
	}
	diff := c.request("GET", "/api/deployments/"+str(next, "id")+"/diff", nil)
	revisionMarker := hex.EncodeToString([]byte(str(rotated, "revision")))
	if strings.Contains(diff.Body.String(), generated) || strings.Contains(diff.Body.String(), secret) || !strings.Contains(diff.Body.String(), revisionMarker) {
		t.Fatal("rotation diff leaked a secret or did not expose its opaque revision")
	}
	removed := c.ok("DELETE", path, credentialChange(rotated))
	if removed["configured"] != false || removed["revision"] == "" || removed["revision"] == rotated["revision"] {
		t.Fatal("removal did not retain a new tombstone revision")
	}
	if w = c.request("PUT", path, body); w.Code != 409 {
		t.Fatal("credential ABA allowed the original empty revision to be reused")
	}
}

func TestBGPCredentialPermissionsMFAAndEncryptionKey(t *testing.T) {
	c, dir, peer := credentialFixture(t)
	id := str(peer, "id")
	path := "/api/peer-credentials/" + id
	meta := c.ok("GET", path, nil)
	change := credentialChange(meta)
	change["generate"] = true
	change["currentPassword"] = "wrong-password"
	if w := c.request("PUT", path, change); w.Code != 403 {
		t.Fatal("wrong current password accepted")
	}
	_, recovery := enableMFA(t, c)
	change["currentPassword"] = "very-long-test-password"
	if w := c.request("PUT", path, change); w.Code != 403 {
		t.Fatal("MFA requirement bypassed")
	}
	change["code"] = recovery[0]
	configured := c.ok("PUT", path, change)
	change = credentialChange(configured)
	change["code"] = recovery[0]
	change["generate"] = true
	if w := c.request("PUT", path, change); w.Code != 403 {
		t.Fatal("MFA recovery code reused")
	}
	c.create("users", Record{"name": "Viewer", "email": "viewer@example.net", "role": "viewer", "password": "viewer-long-password"})
	viewer := docLoginClient(t, c.a, "viewer@example.net", "viewer-long-password")
	if w := viewer.request("GET", path, nil); w.Code != 403 {
		t.Fatal("viewer accessed credential management")
	}
	d := c.ok("POST", "/api/deployments", Record{"routerId": peer["routerId"]})
	dpath := "/api/deployments/" + str(d, "id")
	c.ok("POST", dpath+"/validate", nil)
	c.ok("POST", dpath+"/approve", Record{"checksum": d["checksum"]})
	if w := viewer.request("GET", dpath+"/config", nil); w.Code != 403 {
		t.Fatal("viewer downloaded secret configuration")
	}
	adminID := accountID(c.a)
	tokens := []struct {
		id, raw string
		scopes  []string
		routers []string
	}{
		{"read", "mdt.read.opaque", []string{"deployments:read"}, nil},
		{"pull", "mdt.pull.opaque", []string{"deployments:pull"}, []string{str(peer, "routerId")}},
		{"other", "mdt.other.opaque", []string{"deployments:pull"}, []string{"other-router"}},
		{"unbound", "mdt.unbound.opaque", []string{"deployments:pull"}, nil},
	}
	if err := c.a.store.Update(func(s *State) error {
		u := s.Security[adminID]
		for _, token := range tokens {
			u.Tokens = append(u.Tokens, APIToken{ID: token.id, Hash: digest(token.raw), Scopes: token.scopes, RouterIDs: token.routers, ExpiresAt: time.Now().Add(time.Hour)})
		}
		s.Security[adminID] = u
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, token := range tokens {
		want := 403
		if token.id == "pull" {
			want = 200
		}
		if w := bearerRequest(c.a, token.raw, "GET", dpath+"/config", nil); w.Code != want {
			t.Errorf("%s token config status %d want %d", token.id, w.Code, want)
		}
		if w := bearerRequest(c.a, token.raw, "GET", path, nil); w.Code != 403 {
			t.Error("API token accessed credential management")
		}
	}
	// Isolate BGP credentials as the only encrypted state before restart checks.
	if err := c.a.store.Update(func(s *State) error {
		s.Security[adminID] = SecurityUser{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c.a.Close()
	keyPath := filepath.Join(dir, "security.key")
	key, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if a, err := New(Config{DataDir: dir}); err == nil {
		a.Close()
		t.Fatal("missing BGP encryption key accepted")
	}
	if err = os.WriteFile(keyPath, bytes.Repeat([]byte{9}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if a, err := New(Config{DataDir: dir}); err == nil {
		a.Close()
		t.Fatal("wrong BGP encryption key accepted")
	}
	if err = os.WriteFile(keyPath, key, 0600); err != nil {
		t.Fatal(err)
	}
	a, err := New(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	a.Close()
}

func TestBGPCredentialChangedPeerAndTokenBindings(t *testing.T) {
	c, peer := policyPeer(t)
	id := str(peer, "id")
	meta := c.ok("GET", "/api/peer-credentials/"+id, nil)
	edit := editableCredentialPeer(peer)
	edit["address"] = "192.0.2.3"
	c.ok("PUT", "/api/resources/peers/"+id, edit)
	body := credentialChange(meta)
	body["generate"] = true
	if w := c.request("PUT", "/api/peer-credentials/"+id, body); w.Code != 409 {
		t.Fatal("credential installed after peer target changed")
	}
	token := Record{"name": "Deployment pull", "currentPassword": "very-long-test-password", "scopes": []string{"deployments:pull"}}
	if w := c.request("POST", "/api/account/tokens", token); w.Code != 403 {
		t.Fatal("unbound deployment token issued")
	}
	token["routerIds"] = []string{str(peer, "routerId")}
	created := c.ok("POST", "/api/account/tokens", token)
	if str(created, "token") == "" {
		t.Fatal("bound deployment token not issued")
	}
}
