package app

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestMembershipSwitchAndPinnedAPIToken(t *testing.T) {
	admin, _ := setupClient(t)
	one := admin.create("members", Record{"name": "One", "asn": 64501})
	two := admin.create("members", Record{"name": "Two", "asn": 64502})
	other := admin.create("members", Record{"name": "Other", "asn": 64503})
	user := admin.create("users", Record{"name": "Multi", "email": "multi@example.net", "role": "member", "memberIds": []string{str(one, "id"), str(two, "id")}, "memberAdminIds": []string{str(one, "id")}, "password": "membership-password"})
	member := docLoginClient(t, admin.a, "multi@example.net", "membership-password")
	session := member.ok("GET", "/api/session", nil)["user"].(map[string]any)
	if session["memberId"] != one["id"] || session["memberRole"] != "member-admin" {
		t.Fatal(session)
	}
	if w := member.request("POST", "/api/memberships/select", Record{"memberId": other["id"]}); w.Code != 403 {
		t.Fatal("unassigned membership accepted")
	}
	token := member.ok("POST", "/api/account/tokens", Record{"currentPassword": "membership-password", "name": "pinned", "scopes": []string{"resources:read"}})["token"].(string)
	contact := member.ok("POST", "/api/member/contacts", Record{"name": "NOC", "email": "noc@example.net"})
	if contact["memberId"] != one["id"] {
		t.Fatal("selfservice contact not bound")
	}
	member.ok("PUT", "/api/member/profile", Record{"name": "One updated", "phone": "+1 234"})
	if w := member.request("PUT", "/api/member/profile", Record{"asn": 123}); w.Code != 400 {
		t.Fatal("member changed staff-managed ASN")
	}
	switched := member.ok("POST", "/api/memberships/select", Record{"memberId": two["id"]})["user"].(map[string]any)
	if switched["memberRole"] != "member" {
		t.Fatal("admin role carried across tenants")
	}
	if w := member.request("POST", "/api/member/contacts", Record{"name": "bad", "email": "bad@example.net"}); w.Code != 403 {
		t.Fatal("read member writes contacts")
	}
	if n := len(member.ok("GET", "/api/member/contacts", nil)["items"].([]any)); n != 0 {
		t.Fatal("contacts leaked across switch")
	}
	req := httptest.NewRequest("GET", "/api/resources/members", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	admin.a.ServeHTTP(w, req)
	var body Record
	json.Unmarshal(w.Body.Bytes(), &body)
	items := body["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != one["id"] {
		t.Fatal("token tenant changed with browser")
	}
	admin.ok("PUT", "/api/resources/users/"+str(user, "id"), Record{"name": "Multi", "email": "multi@example.net", "role": "member", "memberIds": []string{str(two, "id")}})
	w = httptest.NewRecorder()
	admin.a.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal("membership edit did not revoke old token")
	}
	if w = member.request("GET", "/api/session", nil); w.Code != 401 {
		t.Fatal("membership edit did not revoke browser")
	}
}
func TestMemberLifecycleDisablesDesiredStateOnly(t *testing.T) {
	c, _ := setupClient(t)
	location := c.create("locations", Record{"name": "DC"})
	sw := c.create("switches", Record{"name": "SW", "locationId": location["id"]})
	port := c.create("ports", Record{"name": "Eth1", "switchId": sw["id"]})
	member := c.create("members", Record{"name": "Network", "asn": 64501, "status": "active"})
	vlan := c.create("vlans", Record{"name": "LAN", "tag": 10, "ipv4Prefix": "192.0.2.0/24"})
	conn := c.create("connections", Record{"name": "Link", "memberId": member["id"], "portId": port["id"], "vlanId": vlan["id"], "ipv4": "192.0.2.2", "status": "active"})
	router := c.create("routers", Record{"name": "RS", "asn": 64500, "routerId": "192.0.2.1", "localIpv4": "192.0.2.1", "vlanId": vlan["id"]})
	peer := c.create("peers", Record{"name": "Peer", "memberId": member["id"], "routerId": router["id"], "asn": 64501, "address": "192.0.2.2", "enabled": true, "maxPrefixes": 10, "allowedPrefixes": []string{"198.51.100.0/24"}})
	if w := c.request("PUT", "/api/resources/members/"+str(member, "id"), Record{"name": "Network", "asn": 64501, "status": "suspended"}); w.Code != 400 {
		t.Fatal("generic suspend bypass")
	}
	result := c.ok("POST", "/api/members/"+str(member, "id")+"/suspend", Record{"reason": "Maintenance"})
	if result["disabledConnections"] != float64(1) || result["disabledPeers"] != float64(1) || result["deploymentRequired"] != true {
		t.Fatal(result)
	}
	s := c.a.store.Snapshot()
	if find(&s, "peers", str(peer, "id"))["enabled"] != false || str(find(&s, "connections", str(conn, "id")), "status") != "disabled" {
		t.Fatal("lifecycle changes not atomic")
	}
	c.ok("POST", "/api/members/"+str(member, "id")+"/activate", Record{"reason": "Maintenance finished"})
	s = c.a.store.Snapshot()
	if find(&s, "peers", str(peer, "id"))["enabled"] != false {
		t.Fatal("activation silently reenabled routing")
	}
}

func TestCollectorTokenRequiresExplicitRouterBinding(t *testing.T) {
	c, _ := setupClient(t)
	if w := c.request("POST", "/api/account/tokens", Record{"currentPassword": "very-long-test-password", "name": "unbound", "scopes": []string{"monitoring:write"}}); w.Code != 403 {
		t.Fatal("unbound collector token created")
	}
	if w := c.request("POST", "/api/account/tokens", Record{"currentPassword": "very-long-test-password", "name": "unknown-router", "scopes": []string{"monitoring:write"}, "routerIds": []string{"missing"}}); w.Code != 403 {
		t.Fatal("unknown router binding accepted")
	}
}

func TestExplicitEmptyAdminMembershipRevokesAuthority(t *testing.T) {
	c, _ := setupClient(t)
	m := c.create("members", Record{"name": "Member", "asn": 64501})
	u := c.create("users", Record{"name": "TenantAdmin", "email": "tenantadmin@example.net", "role": "member", "memberIds": []string{str(m, "id")}, "memberAdminIds": []string{str(m, "id")}, "password": "tenant-admin-password"})
	c.ok("PUT", "/api/resources/users/"+str(u, "id"), Record{"name": "TenantAdmin", "email": "tenantadmin@example.net", "role": "member", "memberIds": []string{str(m, "id")}, "memberAdminIds": []string{}})
	s := c.a.store.Snapshot()
	if memberAdministrator(find(&s, "users", str(u, "id")), str(m, "id")) {
		t.Fatal("empty admin selection retained privileges")
	}
	if ids := membershipIDs(Record{"memberId": str(m, "id"), "memberIds": []any{}}); len(ids) != 0 {
		t.Fatal("explicit empty list fell back to legacy membership")
	}
	if w := c.request("PUT", "/api/resources/users/"+str(u, "id"), Record{"name": "TenantAdmin", "email": "tenantadmin@example.net", "role": "member", "memberId": m["id"], "memberIds": []string{}}); w.Code != 400 {
		t.Fatal("member without effective membership accepted")
	}
}

func TestEquivalentPeerIPv6SpellingPreservesOwnership(t *testing.T) {
	c, _ := setupClient(t)
	m := c.create("members", Record{"name": "Member", "asn": 64501})
	v := c.create("vlans", Record{"name": "V6", "tag": 10, "ipv6Prefix": "2001:db8::/64"})
	router := c.create("routers", Record{"name": "RS", "asn": 64500, "routerId": "192.0.2.1", "localIpv6": "2001:db8::1", "vlanId": v["id"]})
	p := c.create("peers", Record{"name": "Peer", "memberId": m["id"], "routerId": router["id"], "asn": 64501, "address": "2001:db8::2", "enabled": false, "maxPrefixes": 100, "allowedPrefixes": []string{"2001:db8:1::/48"}})
	state := c.a.store.Snapshot()
	before := str(find(&state, "peers", str(p, "id")), "ownershipSince")
	id := str(p, "id")
	delete(p, "id")
	delete(p, "createdAt")
	delete(p, "updatedAt")
	p["address"] = "2001:0DB8:0000:0000:0000:0000:0000:0002"
	c.ok("PUT", "/api/resources/peers/"+id, p)
	state = c.a.store.Snapshot()
	if str(find(&state, "peers", id), "ownershipSince") != before {
		t.Fatal("canonical alias reset ownership")
	}
	p["address"] = "2001:db8::3"
	c.ok("PUT", "/api/resources/peers/"+id, p)
	state = c.a.store.Snapshot()
	if str(find(&state, "peers", id), "ownershipSince") == before {
		t.Fatal("real identity change did not reset ownership")
	}
}
func TestMembershipSwitchInvalidatesOldTabMutation(t *testing.T) {
	c, _ := setupClient(t)
	a := c.create("members", Record{"name": "A", "asn": 64501})
	b := c.create("members", Record{"name": "B", "asn": 64502})
	c.create("users", Record{"name": "Both", "email": "tabs@example.net", "role": "member", "memberIds": []string{str(a, "id"), str(b, "id")}, "memberAdminIds": []string{str(a, "id"), str(b, "id")}, "password": "both-tenants-password"})
	member := docLoginClient(t, c.a, "tabs@example.net", "both-tenants-password")
	old := member.csrf
	changed := member.ok("POST", "/api/memberships/select", Record{"memberId": b["id"]})
	if str(changed, "csrfToken") == old {
		t.Fatal("tenant switch did not rotate CSRF")
	}
	if w := member.request("POST", "/api/member/contacts", Record{"name": "Old A form", "email": "old-a@example.net"}); w.Code != 403 {
		t.Fatal("stale tab wrote to switched tenant")
	}
	member.csrf = str(changed, "csrfToken")
	created := member.ok("POST", "/api/member/contacts", Record{"name": "B form", "email": "b@example.net"})
	if created["memberId"] != b["id"] {
		t.Fatal("current context write failed")
	}
}

func TestPeerBindingEpochTracksConnectionAddressRoundTrip(t *testing.T) {
	f := monitoringFixture(t)
	state := f.c.a.store.Snapshot()
	before := monitoringBindingRevision(&state, "peer", str(f.peer, "id"), str(f.router, "id"))
	portEpoch := monitoringBindingRevision(&state, "port", str(f.port, "id"), str(f.router, "id"))
	conn := clean(find(&state, "connections", str(f.connection, "id")))
	delete(conn, "id")
	delete(conn, "createdAt")
	delete(conn, "updatedAt")
	conn["ipv4"] = "192.0.2.12"
	f.c.ok("PUT", "/api/resources/connections/"+str(f.connection, "id"), conn)
	conn["ipv4"] = "192.0.2.10"
	f.c.ok("PUT", "/api/resources/connections/"+str(f.connection, "id"), conn)
	state = f.c.a.store.Snapshot()
	if monitoringBindingRevision(&state, "peer", str(f.peer, "id"), str(f.router, "id")) == before {
		t.Fatal("old peer lease survived network unbind/rebind")
	}
	if monitoringBindingRevision(&state, "port", str(f.port, "id"), str(f.router, "id")) != portEpoch {
		t.Fatal("same-owner physical traffic history invalidated by IP-only change")
	}
}
