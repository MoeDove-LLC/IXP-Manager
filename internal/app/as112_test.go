package app

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/as112"
)

func appAS112Fixture(t *testing.T, platform string) (*client, string, Record, Record) {
	t.Helper()
	c, dir := setupClient(t)
	location := c.create("locations", Record{"name": "AS112 facility"})
	sw := c.create("switches", Record{"name": "AS112 switch", "locationId": location["id"]})
	port := c.create("ports", Record{"name": "AS112 member port", "switchId": sw["id"], "status": "active"})
	member := c.create("members", Record{"name": "Explicit AS112 peer", "asn": 64501, "status": "active"})
	vlan := c.create("vlans", Record{"name": "AS112 VLAN", "tag": 112, "ipv4Prefix": "192.0.2.0/24", "ipv6Prefix": "2001:db8::/64"})
	c.create("connections", Record{"name": "Explicit connection", "memberId": member["id"], "portId": port["id"], "vlanId": vlan["id"], "ipv4": "192.0.2.10", "ipv6": "2001:db8::10", "status": "active"})
	router := Record{"name": "AS112 node", "platform": platform, "role": "as112", "asn": 112, "routerId": "192.0.2.112", "localIpv4": "192.0.2.112", "localIpv6": "2001:db8::112", "vlanId": vlan["id"], "as112Service": "both", "as112IPv4": true, "as112IPv6": true}
	if platform == "junos" {
		router["as112NextHop4"], router["as112NextHop6"] = "192.0.2.200", "2001:db8::200"
	}
	router = c.create("routers", router)
	peer := c.create("peers", Record{"name": "Opt-in peer", "routerId": router["id"], "memberId": member["id"], "asn": 64501, "address": "192.0.2.10", "enabled": true, "maxPrefixes": 10, "allowedPrefixes": []string{}, "allowedRoutes": []any{}})
	return c, dir, router, peer
}
func appAS112Profile(c *client, router Record) Record {
	unique := str(router, "localIpv4")
	if str(router, "platform") == "junos" {
		unique = str(router, "as112NextHop4")
	}
	return c.ok("POST", "/api/resources/provisioning-profiles", Record{"name": "AS112 authority", "options": Record{"kind": "as112-dns", "routerId": router["id"], "as112": Record{"serverName": "node.example.test", "responsibleMailbox": "hostmaster.example.test", "facility": "AS112 fixture", "location": "Isolated namespace", "uniqueIP": unique, "loc": ""}}})
}

func TestAS112AppFivePlatformGenerationAndNativePermissionBoundaries(t *testing.T) {
	for _, platform := range []string{"bird2", "bird3", "frr", "openbgpd", "junos"} {
		t.Run(platform, func(t *testing.T) {
			c, _, router, peer := appAS112Fixture(t, platform)
			meta := c.ok("GET", "/api/peer-credentials/"+str(peer, "id"), nil)
			change := credentialChange(meta)
			change["secret"] = "Synthetic-AS112-Secret"
			c.ok("PUT", "/api/peer-credentials/"+str(peer, "id"), change)
			d := c.ok("POST", "/api/deployments", Record{"routerId": router["id"]})
			if d["routerRole"] != "as112" || d["as112Service"] != "both" || d["as112DNSHealthChecked"] != false {
				t.Fatal("AS112 deployment metadata lost immutable role or claimed health")
			}
			path := "/api/deployments/" + str(d, "id")
			preview := c.ok("GET", path, nil)
			if strings.Contains(str(preview, "config"), "Synthetic-AS112-Secret") || !strings.Contains(str(preview, "config"), "REDACTED-BGP-SECRET") {
				t.Fatal("AS112 config preview leaked MD5")
			}
			if platform == "junos" {
				c.ok("POST", path+"/validate", nil)
				c.ok("POST", path+"/approve", Record{"checksum": d["checksum"]})
				if w := c.request("GET", path+"/config", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "<next-hop>192.0.2.200</next-hop>") {
					t.Fatal("Junos AS112 reviewed config not downloadable")
				}
			}
			bad := editableCredentialPeer(peer)
			bad["allowedPrefixes"] = []string{"198.51.100.0/24"}
			bad["allowedRoutes"] = []any{}
			if w := c.request("PUT", "/api/resources/peers/"+str(peer, "id"), bad); w.Code != 400 {
				t.Fatal("AS112 admitted obsolete ingress allowlist")
			}
			edit := editableCredentialPeer(router)
			edit["as112Service"] = "dname"
			c.ok("PUT", "/api/resources/routers/"+str(router, "id"), edit)
			old := c.ok("GET", path, nil)
			if old["as112Service"] != "both" {
				t.Fatal("historical AS112 selection followed mutable router")
			}
			if platform == "junos" {
				if w := c.request("GET", path+"/config", nil); w.Code != 409 {
					t.Fatal("AS112 service change retained old approval")
				}
			}
		})
	}
}

func TestAS112DNSProfileReviewDownloadAndCurrentRouterBinding(t *testing.T) {
	c, dir, router, _ := appAS112Fixture(t, "bird2")
	profile := appAS112Profile(c, router)
	revision := c.ok("POST", "/api/provisioning", Record{"profileId": profile["id"]})
	path := "/api/provisioning/" + str(revision, "id")
	if revision["kind"] != "as112-dns" || revision["complete"] != true || revision["validationLevel"] != "native" || revision["routerId"] != router["id"] || revision["healthExecuted"] != false {
		t.Fatal("AS112 DNS metadata did not describe a complete unchecked service bundle")
	}
	preview := c.ok("GET", path, nil)
	bundle, err := as112.Decode(str(preview, "config"))
	if err != nil || bundle.RouterID != router["id"] || bundle.RouterCreatedAt != router["createdAt"] || len(bundle.Zones) != 24 {
		t.Fatal("AS112 preview lost the current router entity or zones", err)
	}
	if w := c.request("POST", path+"/validate", nil); w.Code != 422 {
		t.Fatal("missing native DNS tools accepted")
	}
	// Contract-only checker stubs isolate HTTP review/CAS behavior. Actual
	// PowerDNS and all-zone native validation is covered by native_test.go.
	checker := filepath.Join(dir, "check-contract-fixture")
	if err = os.WriteFile(checker, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	c.a.namedCheckzone, c.a.pdnsServer = checker, checker
	c.ok("POST", path+"/validate", nil)
	c.ok("POST", path+"/approve", Record{"checksum": revision["checksum"]})
	download := c.request("GET", path+"/config", nil)
	if download.Code != 200 || download.Header().Get("X-Provisioning-Router-ID") != router["id"] || download.Header().Get("X-Config-SHA256") != digest(download.Body.String()) {
		t.Fatal("AS112 download not bound to exact router and bytes")
	}
	stored, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil || bytes.Contains(stored, []byte("local-address-nonexist-fail=yes")) {
		t.Fatal("AS112 bundle leaked from encrypted artifact into hot state")
	}
	edit := editableCredentialPeer(router)
	edit["as112Service"] = "dname"
	c.ok("PUT", "/api/resources/routers/"+str(router, "id"), edit)
	if w := c.request("GET", path+"/config", nil); w.Code != 409 {
		t.Fatal("router service mutation did not invalidate DNS download")
	}
	current := c.ok("POST", "/api/provisioning", Record{"profileId": profile["id"]})
	currentDetail := c.ok("GET", "/api/provisioning/"+str(current, "id"), nil)
	next, err := as112.Decode(str(currentDetail, "config"))
	if err != nil || next.Service != as112.DNAME || len(next.AdvertisePrefixes) != 2 {
		t.Fatal("fresh AS112 revision retained removed DD service")
	}
}

func TestAS112ProvisioningBodiesRecheckLogoutAndScope(t *testing.T) {
	c, _, router, _ := appAS112Fixture(t, "junos")
	profile := appAS112Profile(c, router)
	viewer := noteTestUser(t, c, "viewer", "as112-viewer@example.test", nil)
	if w := viewer.request("POST", "/api/provisioning", Record{"profileId": profile["id"]}); w.Code != 403 {
		t.Fatal("viewer generated service deployment")
	}
	raw, _ := json.Marshal(Record{"profileId": profile["id"]})
	body := &legacyPatchReadHook{Reader: bytes.NewReader(raw), hook: func() { c.a.mu.Lock(); delete(c.a.sessions, c.cookie.Value); c.a.mu.Unlock() }}
	r := httptest.NewRequest("POST", "/api/provisioning", body)
	r.AddCookie(c.cookie)
	r.Header.Set("X-CSRF-Token", c.csrf)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c.a.ServeHTTP(w, r)
	if w.Code < 400 || len(c.a.store.Snapshot().ProvisionRevisions) != 0 {
		t.Fatal("revoked session generated a service artifact")
	}
}

func TestAS112JunosBackendRetargetIsCASIntentUntilCompleteBinding(t *testing.T) {
	c, dir, router, _ := appAS112Fixture(t, "junos")
	profile := appAS112Profile(c, router)
	first := c.ok("POST", "/api/provisioning", Record{"profileId": profile["id"]})
	path := "/api/provisioning/" + str(first, "id")
	checker := filepath.Join(dir, "as112-contract-checker")
	if err := os.WriteFile(checker, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	c.a.namedCheckzone, c.a.pdnsServer = checker, checker
	c.ok("POST", path+"/validate", nil)
	c.ok("POST", path+"/approve", Record{"checksum": first["checksum"]})
	// Either half can be edited as desired inventory, but no inconsistent
	// pair may generate/validate/download a service revision.
	options := Record(profile["options"].(map[string]any))
	identity := Record(options["as112"].(map[string]any))
	identity["uniqueIP"] = "192.0.2.201"
	options["as112"] = identity
	profile = c.ok("PUT", "/api/resources/provisioning-profiles/"+str(profile, "id"), Record{"name": profile["name"], "options": options, "expectedRevision": profile["revision"]})
	if w := c.request("POST", "/api/provisioning", Record{"profileId": profile["id"]}); w.Code < 400 {
		t.Fatal("future backend identity generated before router binding matched")
	}
	if w := c.request("GET", path+"/config", nil); w.Code != 409 {
		t.Fatal("retargeted definition retained old approval")
	}
	edit := editableCredentialPeer(router)
	edit["as112NextHop4"] = "192.0.2.201"
	router = c.ok("PUT", "/api/resources/routers/"+str(router, "id"), edit)
	next := c.ok("POST", "/api/provisioning", Record{"profileId": profile["id"]})
	detail := c.ok("GET", "/api/provisioning/"+str(next, "id"), nil)
	bundle, err := as112.Decode(str(detail, "config"))
	if err != nil || !contains(bundle.NodeAddresses, "192.0.2.201") || !contains(bundle.NodeAddresses, "2001:db8::200") || !contains(bundle.RequiredLocalAddresses, "2001:db8::200") {
		t.Fatal("both Junos next hops were not bound to the checked DNS node", err)
	}
	nextPath := "/api/provisioning/" + str(next, "id")
	c.ok("POST", nextPath+"/validate", nil)
	c.ok("POST", nextPath+"/approve", Record{"checksum": next["checksum"]})
	edit = editableCredentialPeer(router)
	edit["as112NextHop6"] = "2001:db8::201"
	c.ok("PUT", "/api/resources/routers/"+str(router, "id"), edit)
	if w := c.request("GET", nextPath+"/config", nil); w.Code != 409 {
		t.Fatal("second-family next-hop change remained hidden behind unchanged uniqueIP")
	}
}

func TestAS112ProfileDefinitionBodyReadRechecksLogout(t *testing.T) {
	c, _, router, _ := appAS112Fixture(t, "bird2")
	profile := appAS112Profile(c, router)
	before := len(c.a.store.Snapshot().Resources[provisionProfileKind])
	raw, _ := json.Marshal(Record{"name": "revoked profile", "options": profile["options"]})
	body := &legacyPatchReadHook{Reader: bytes.NewReader(raw), hook: func() { c.a.mu.Lock(); delete(c.a.sessions, c.cookie.Value); c.a.mu.Unlock() }}
	request := httptest.NewRequest("POST", "/api/resources/provisioning-profiles", body)
	request.AddCookie(c.cookie)
	request.Header.Set("X-CSRF-Token", c.csrf)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	c.a.ServeHTTP(response, request)
	if response.Code != 401 || len(c.a.store.Snapshot().Resources[provisionProfileKind]) != before {
		t.Fatal("profile definition committed after logout during body read")
	}
}
