package app

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/routing"
)

func bird1ControllerFixture(t *testing.T, family int) (*client, string, Record, Record) {
	t.Helper()
	c, dir := setupClient(t)
	loc := c.create("locations", Record{"name": "BIRD1 native fixture"})
	sw := c.create("switches", Record{"name": "fixture-switch", "locationId": loc["id"]})
	port := c.create("ports", Record{"name": "fixture-port", "switchId": sw["id"], "status": "active"})
	member := c.create("members", Record{"name": "Synthetic BIRD1 member", "asn": 64512, "status": "active"})
	vlan := c.create("vlans", Record{"name": "Single family fixture", "tag": 112, "ipv4Prefix": "192.0.2.0/24", "ipv6Prefix": "2001:db8::/64"})
	connection := Record{"name": "fixture-connection", "memberId": member["id"], "portId": port["id"], "vlanId": vlan["id"], "status": "active"}
	router := Record{"name": "BIRD1 dedicated instance", "platform": "bird1", "asn": 64500, "routerId": "192.0.2.1", "vlanId": vlan["id"]}
	address, prefix := "192.0.2.10", "198.51.100.0/24"
	if family == 4 {
		connection["ipv4"] = address
		router["localIpv4"] = "192.0.2.1"
	} else {
		address, prefix = "2001:db8::10", "2001:db8:1::/48"
		connection["ipv6"] = address
		router["localIpv6"] = "2001:db8::1"
	}
	c.create("connections", connection)
	router = c.create("routers", router)
	peer := c.create("peers", Record{"name": "Synthetic BIRD1 peer", "routerId": router["id"], "memberId": member["id"], "address": address, "asn": 64512, "enabled": true, "maxPrefixes": 20, "allowedPrefixes": []string{prefix}})
	meta := c.ok("GET", "/api/peer-credentials?peerId="+str(peer, "id"), nil)
	change := credentialChange(meta)
	change["secret"] = "Synthetic_BIRD1_Controller_Key"
	c.ok("PUT", "/api/peer-credentials/"+str(peer, "id"), change)
	return c, dir, router, peer
}
func TestBIRD1ControllerSelectsOnlyArtifactFamilyAndRedacts(t *testing.T) {
	for _, family := range []int{4, 6} {
		for _, role := range []string{routing.RoleRouteServer, routing.RoleRouteCollector} {
			t.Run(string(rune('0'+family))+"/"+role, func(t *testing.T) {
				c, _, router, _ := bird1ControllerFixture(t, family)
				if role == routing.RoleRouteCollector {
					edit := editableCredentialPeer(router)
					edit["role"] = role
					edit["collectorPolicy"] = routing.CollectorObserve
					router = c.ok("PUT", "/api/resources/routers/"+str(router, "id"), edit)
				}
				revision := c.ok("POST", "/api/deployments", Record{"routerId": router["id"]})
				path := "/api/deployments/" + str(revision, "id")
				preview := c.ok("GET", path, nil)
				if preview["redacted"] != true || strings.Contains(str(preview, "config"), "Synthetic_BIRD1_Controller_Key") || !strings.Contains(str(preview, "config"), routing.RedactedBGPPassword) {
					t.Fatal("BIRD1 preview leaked authentication")
				}
				state := c.a.store.Snapshot()
				deployment := *deploymentByID(&state, str(revision, "id"))
				c.a.bird1Binary = "/not-installed/bird1"
				c.a.bird1IPv6Binary = "/not-installed/bird6"
				if r := readinessResponse(t, c, deployment); r.AddressFamily != family || r.NativeCheckerAvailable || r.ActivationMode != "bird1-confirmed-configure" || r.Actions["validate"].Allowed {
					t.Fatal("missing/family checker misreported", r)
				}
				if w := c.request("POST", path+"/validate", nil); w.Code != 422 {
					t.Fatal("missing checker accepted", w.Code)
				}
				actual4, actual6 := os.Getenv("BIRD1_BIN"), os.Getenv("BIRD1_IPV6_BIN")
				if actual4 == "" || actual6 == "" {
					return
				}
				// Even an executable with the correct major must match this exact family.
				if family == 4 {
					c.a.bird1Binary = actual6
				} else {
					c.a.bird1IPv6Binary = actual4
				}
				if w := c.request("POST", path+"/validate", nil); w.Code != 422 {
					t.Fatal("opposite actual BIRD1 family validated", w.Code)
				}
				if family == 4 {
					c.a.bird1Binary = actual4
				} else {
					c.a.bird1IPv6Binary = actual6
				}
				caps := c.ok("GET", "/api/router-platforms", nil)["items"].([]any)
				for _, raw := range caps {
					cap := raw.(map[string]any)
					if cap["platform"] == "bird1" {
						families := cap["nativeCheckerFamilies"].(map[string]any)
						key, other := "ipv4", "ipv6"
						if family == 6 {
							key, other = other, key
						}
						if cap["singleAddressFamily"] != true || cap["nativeCheckerAvailable"] != false || families[key] != true || families[other] != false || cap["fetch"] != true || cap["apply"] != true || cap["rollback"] != true {
							t.Fatal("partial checker or live capability overstated", cap)
						}
					}
				}
				if r := readinessResponse(t, c, deployment); r.AddressFamily != family || !r.NativeCheckerAvailable || !r.Actions["validate"].Allowed {
					t.Fatal("matching family was blocked by missing other checker", r)
				}
				c.ok("POST", path+"/validate", nil)
				c.ok("POST", path+"/approve", Record{"checksum": revision["checksum"]})
				downloaded := c.request("GET", path+"/config", nil)
				if downloaded.Code != 200 || downloaded.Header().Get("X-Config-Platform") != "bird1" || downloaded.Header().Get("X-Config-SHA256") != routing.Checksum(downloaded.Body.String()) {
					t.Fatal("approved BIRD1 download lost binding")
				}
				bound, e := routing.BIRD1Family(downloaded.Body.String())
				if e != nil || bound != family {
					t.Fatal("download family differs", e)
				}
			})
		}
	}
}
func TestBIRD1ControllerRejectsNewMixedInputWithoutBlockingLegacyBoot(t *testing.T) {
	c, dir := setupClient(t)
	vlan := c.create("vlans", Record{"name": "Legacy repair fixture", "tag": 112, "ipv4Prefix": "192.0.2.0/24", "ipv6Prefix": "2001:db8::/64"})
	body := Record{"name": "Legacy dual family inventory", "platform": "bird1", "asn": 64500, "routerId": "192.0.2.1", "localIpv4": "192.0.2.1", "localIpv6": "2001:db8::1", "vlanId": vlan["id"]}
	if w := c.request("POST", "/api/resources/routers", body); w.Code != 400 {
		t.Fatal("new mixed-family BIRD1 row accepted", w.Code)
	}
	body["platform"] = "bird2"
	router := c.create("routers", body)
	// This simulates an inventory row persisted before BIRD1 rendering existed.
	if e := c.a.store.Update(func(s *State) error { find(s, "routers", str(router, "id"))["platform"] = "bird1"; return nil }); e != nil {
		t.Fatal(e)
	}
	if e := c.a.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := New(Config{DataDir: dir})
	if e != nil {
		t.Fatal("old repairable BIRD1 inventory blocked startup", e)
	}
	t.Cleanup(func() { reopened.Close() })
	c.a = reopened
	c.cookie = nil
	c.csrf = ""
	response := c.request("POST", "/api/login", Record{"email": "admin@example.net", "password": "very-long-test-password"})
	if response.Code != 200 {
		t.Fatal("legacy repair login", response.Code)
	}
	c.cookie = response.Result().Cookies()[0]
	var login Record
	json.Unmarshal(response.Body.Bytes(), &login)
	c.csrf = str(login, "csrfToken")
	if w := c.request("POST", "/api/deployments", Record{"routerId": router["id"]}); w.Code != 400 {
		t.Fatal("legacy mixed-family inventory generated unsafe config", w.Code)
	}
	// Unrelated work remains available while the old row is being repaired.
	c.create("locations", Record{"name": "Unrelated work remains possible"})
	current := c.ok("GET", "/api/resources/routers/"+str(router, "id"), nil)
	edit := editableCredentialPeer(current)
	delete(edit, "localIpv6")
	c.ok("PUT", "/api/resources/routers/"+str(router, "id"), edit)
	c.ok("POST", "/api/deployments", Record{"routerId": router["id"]})
}
func TestBIRD1PeerEditCanDisableLegacyMismatch(t *testing.T) {
	state := State{Resources: map[string][]Record{"routers": {{"id": "legacy", "platform": "bird1", "localIpv4": "192.0.2.1", "localIpv6": "2001:db8::1"}}}}
	peer := Record{"routerId": "legacy", "address": "2001:db8::10", "enabled": true}
	if e := guardBIRD1ResourceMutation(&state, "peers", "PUT", peer); e == nil {
		t.Fatal("enabled legacy mixed-family peer accepted")
	}
	peer["enabled"] = false
	if e := guardBIRD1ResourceMutation(&state, "peers", "PUT", peer); e != nil {
		t.Fatal("could not disable unsafe old peer for repair", e)
	}
}

func TestBIRD1PrivateRuntimeDigestNeverEntersPreviewOrDiff(t *testing.T) {
	c, _, router, _ := bird1ControllerFixture(t, 4)
	revision := c.ok("POST", "/api/deployments", Record{"routerId": router["id"]})
	id := str(revision, "id")
	snapshot := c.a.store.Snapshot()
	stored := deploymentByID(&snapshot, id)
	raw, e := c.a.readDeploymentArtifact(*stored)
	if e != nil {
		t.Fatal(e)
	}
	fingerprint := ""
	for _, line := range strings.Split(raw, "\n") {
		if strings.HasPrefix(line, "define md_managed_checksum = ") {
			fingerprint = strings.TrimSuffix(strings.TrimPrefix(line, "define md_managed_checksum = \""), "\";")
		}
	}
	if len(fingerprint) != 64 {
		t.Fatal("v2 runtime digest missing from private full artifact")
	}
	for _, path := range []string{"/api/deployments/" + id, "/api/deployments/" + id + "/diff"} {
		response := c.request("GET", path, nil)
		if response.Code != 200 || strings.Contains(response.Body.String(), fingerprint) || strings.Contains(response.Body.String(), stored.Checksum) || strings.Contains(response.Body.String(), "Synthetic_BIRD1_Controller_Key") {
			t.Fatal("private BIRD1 execution digest escaped public projection")
		}
	}
}
