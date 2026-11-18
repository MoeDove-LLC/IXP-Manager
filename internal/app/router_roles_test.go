package app

import (
	"strings"
	"testing"
)

func TestCollectorRoleInvalidatesApprovalAndDoesNotAdvertiseRouteServer(t *testing.T) {
	c, _, peer := credentialFixture(t)
	ports := c.ok("GET", "/api/resources/ports", nil)["items"].([]any)
	port := Record(ports[0].(map[string]any))
	portEdit := editableCredentialPeer(port)
	portEdit["speed"] = 1000
	c.ok("PUT", "/api/resources/ports/"+str(port, "id"), portEdit)
	routerID := str(peer, "routerId")
	c.ok("PUT", "/api/settings", Record{"name": "Role Test IXP", "asn": 64500, "ixfId": 100, "shortName": "ROLE-IX", "country": "CN"})
	old := c.ok("POST", "/api/deployments", Record{"routerId": routerID})
	oldPath := "/api/deployments/" + str(old, "id")
	c.ok("POST", oldPath+"/validate", nil)
	c.ok("POST", oldPath+"/approve", Record{"checksum": old["checksum"]})
	if old["routerRole"] != "route-server" {
		t.Fatal("legacy router did not default to route-server")
	}
	if got := c.ok("GET", "/api/peering/matrix", nil)["routeServer"].([]any); len(got) != 1 {
		t.Fatal("fixture route-server relationship missing")
	}
	router := c.ok("GET", "/api/resources/routers/"+routerID, nil)
	edit := editableCredentialPeer(router)
	edit["role"] = "route-collector"
	edit["collectorPolicy"] = "observe"
	if w := c.request("PUT", "/api/resources/routers/"+routerID, edit); w.Code != 400 {
		t.Fatal("unsupported Junos collector accepted")
	}
	edit["platform"] = "bird2"
	c.ok("PUT", "/api/resources/routers/"+routerID, edit)
	if w := c.request("GET", oldPath+"/config", nil); w.Code != 409 {
		t.Fatal("route-server approval survived a collector role change")
	}
	peerEdit := editableCredentialPeer(peer)
	peerEdit["allowedPrefixes"] = []string{}
	c.ok("PUT", "/api/resources/peers/"+str(peer, "id"), peerEdit)
	created := c.ok("POST", "/api/deployments", Record{"routerId": routerID})
	if created["routerRole"] != "route-collector" || created["collectorPolicy"] != "observe" {
		t.Fatal("collector role was not pinned in the revision")
	}
	preview := c.ok("GET", "/api/deployments/"+str(created, "id"), nil)
	config := str(preview, "config")
	if !strings.Contains(config, "export none;") || !strings.Contains(config, "import keep filtered yes;") || strings.Contains(config, "protocol kernel") {
		t.Fatal("collector preview lacks no-export/retained-observation isolation")
	}
	if got := c.ok("GET", "/api/peering/matrix", nil)["routeServer"].([]any); len(got) != 0 {
		t.Fatal("collector appeared as a route-server peering relationship")
	}
	snapshot := c.a.store.Snapshot()
	doc, err := exportIXF(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.MemberList) != 1 || doc.MemberList[0].Connections[0].VLANs[0].IPv4.RouteServer {
		t.Fatal("collector-only client exported as an IX-F route-server client")
	}
	edit["collectorPolicy"] = "authorized-only"
	if w := c.request("PUT", "/api/resources/routers/"+routerID, edit); w.Code != 400 {
		t.Fatal("authorized-only collector accepted an enabled empty-allowlist peer")
	}
	edit["role"] = "route-server"
	delete(edit, "collectorPolicy")
	if w := c.request("PUT", "/api/resources/routers/"+routerID, edit); w.Code != 400 {
		t.Fatal("changing to server silently admitted a peer without an allowlist")
	}
	oldMetadata := c.ok("GET", oldPath, nil)
	if oldMetadata["routerRole"] != "route-server" {
		t.Fatal("historical revision role followed mutable router state")
	}
}
