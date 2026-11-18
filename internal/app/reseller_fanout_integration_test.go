package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResellerFanoutInventoryLifecycleAndCoreCandidates(t *testing.T) {
	staff, _ := setupClient(t)
	reseller := staff.create("members", Record{"name": "Fixture reseller", "asn": 64540, "type": "reseller", "isReseller": true, "status": "active"})
	member := staff.create("members", Record{"name": "Resold member", "asn": 64541, "resellerId": reseller["id"], "status": "active"})
	location := staff.create("locations", Record{"name": "Fixture datacenter"})
	peeringSwitch := staff.create("switches", Record{"name": "Access switch", "locationId": location["id"]})
	resellerSwitch := staff.create("switches", Record{"name": "Reseller switch", "locationId": location["id"]})
	peering := staff.create("ports", Record{"name": "access-1", "switchId": peeringSwitch["id"], "status": "active"})
	passive := staff.create("ports", Record{"name": "fanout-1", "switchId": resellerSwitch["id"], "status": "active"})
	replacement := staff.create("ports", Record{"name": "fanout-2", "switchId": resellerSwitch["id"], "status": "active"})
	lanA := staff.create("vlans", Record{"name": "Fixture LAN A", "tag": 141, "ipv4Prefix": "192.0.2.0/24"})
	lanB := staff.create("vlans", Record{"name": "Fixture LAN B", "tag": 142, "ipv4Prefix": "198.51.100.0/24"})
	connection := staff.create("connections", Record{"name": "Resold A", "memberId": member["id"], "portId": peering["id"], "vlanId": lanA["id"], "ipv4": "192.0.2.10", "status": "active"})
	otherVLAN := staff.create("connections", Record{"name": "Resold B", "memberId": member["id"], "portId": peering["id"], "vlanId": lanB["id"], "ipv4": "198.51.100.10", "status": "active"})
	portView := func(id string) Record {
		t.Helper()
		for _, raw := range staff.ok(http.MethodGet, "/api/core/inventory", nil)["ports"].([]any) {
			port := Record(raw.(map[string]any))
			if str(port, "id") == id {
				return port
			}
		}
		t.Fatal("fixture port missing from core inventory", id)
		return nil
	}
	passiveID, replacementID := str(passive, "id"), str(replacement, "id")
	before := portView(passiveID)
	if before["available"] != true {
		t.Fatal("unassigned passive port should initially be available", before)
	}
	allocation := Record{"name": "Reseller handoff", "memberId": member["id"], "resellerId": reseller["id"], "peeringPortId": peering["id"], "fanoutPortId": passive["id"]}
	pair := staff.create(resellerFanoutKind, allocation)
	path := "/api/resources/" + resellerFanoutKind + "/" + str(pair, "id")
	if actual := portView(passiveID); actual["available"] != false || !strings.Contains(str(actual, "blockedReason"), "reseller fanout") || actual["_etag"] == before["_etag"] {
		t.Fatal("core previews did not notice reseller port reservation", actual)
	}
	memberPassword := "resold-member-fixture-password"
	staff.create("users", Record{"name": "Member operator", "email": "resold-fixture@example.test", "role": "member", "memberId": member["id"], "password": memberPassword})
	memberClient := docLoginClient(t, staff.a, "resold-fixture@example.test", memberPassword)
	if response := memberClient.request(http.MethodGet, path, nil); response.Code != http.StatusForbidden && response.Code != http.StatusNotFound {
		t.Fatal("member accessed staff physical pairing", response.Code)
	}
	if response := staff.request(http.MethodPost, "/api/resources/connections", Record{"name": "Conflicting access", "memberId": reseller["id"], "portId": passive["id"], "vlanId": lanA["id"], "ipv4": "192.0.2.11", "status": "active"}); response.Code < 400 || !strings.Contains(response.Body.String(), "fanout") {
		t.Fatal("a passive fanout became a peering connection", response.Code, response.Body.String())
	}
	if response := staff.request(http.MethodPut, "/api/resources/members/"+str(member, "id"), Record{"name": member["name"], "asn": 64541, "status": "active"}); response.Code < 400 || !strings.Contains(response.Body.String(), "fanout") {
		t.Fatal("a member lost its reseller with a physical allocation in use", response.Code, response.Body.String())
	}
	staff.ok(http.MethodDelete, "/api/resources/connections/"+str(otherVLAN, "id"), nil)
	last := "/api/resources/connections/" + str(connection, "id")
	if response := staff.request(http.MethodDelete, last, nil); response.Code < 400 || !strings.Contains(response.Body.String(), "fanout") {
		t.Fatal("deleting the last peering connection orphaned a fanout", response.Code, response.Body.String())
	}
	originalTag := staff.request(http.MethodGet, path, nil).Header().Get("ETag")
	allocation["fanoutPortId"] = replacement["id"]
	staff.ok(http.MethodPut, path, allocation)
	if portView(passiveID)["available"] != true || portView(replacementID)["available"] != false {
		t.Fatal("replacing a fanout did not move the reservation atomically")
	}
	staleDelete := httptest.NewRequest(http.MethodDelete, path, nil)
	staleDelete.AddCookie(staff.cookie)
	staleDelete.Header.Set("X-CSRF-Token", staff.csrf)
	staleDelete.Header.Set("If-Match", originalTag)
	stale := httptest.NewRecorder()
	staff.a.ServeHTTP(stale, staleDelete)
	if stale.Code != http.StatusPreconditionFailed {
		t.Fatal("stale delete released a moved allocation", stale.Code)
	}
	staff.ok(http.MethodDelete, path, nil)
	staff.ok(http.MethodDelete, last, nil)
	if portView(replacementID)["available"] != true {
		t.Fatal("releasing the relation left the passive port reserved")
	}
}
