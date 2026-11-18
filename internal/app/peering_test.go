package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func peeringUser(t *testing.T, c *client, name, email, memberID string, admin bool) *client {
	t.Helper()
	user := Record{"name": name, "email": email, "role": "member", "memberId": memberID, "password": "peering-member-password"}
	if admin {
		user["memberAdminIds"] = []string{memberID}
	}
	c.create("users", user)
	return loginClient(t, c.a, Record{"email": email, "password": "peering-member-password"})
}
func resourceUpdate(t *testing.T, c *client, kind, id string, fields Record) {
	t.Helper()
	current := c.ok("GET", "/api/resources/"+kind+"/"+id, nil)
	for key := range current {
		if _, known := Schemas[kind][key]; !known {
			delete(current, key)
		}
	}
	for key, value := range fields {
		current[key] = value
	}
	c.ok("PUT", "/api/resources/"+kind+"/"+id, current)
}
func TestBilateralProposalDecisionAndMatrixNeverFabricateBGP(t *testing.T) {
	f := monitoringFixture(t)
	source := peeringUser(t, f.c, "Source", "source@peering.example", str(f.member, "id"), false)
	recipient := peeringUser(t, f.c, "Recipient admin", "recipient@peering.example", str(f.otherMember, "id"), true)
	ordinaryRecipient := peeringUser(t, f.c, "Recipient reader", "reader@peering.example", str(f.otherMember, "id"), false)
	vlanID := str(f.connection, "vlanId")
	partners := source.ok("GET", "/api/peering/partners", nil)
	if len(partners["members"].([]any)) != 1 {
		t.Fatal("shared VLAN partner missing")
	}
	raw, _ := json.Marshal(partners)
	if strings.Contains(string(raw), "recipient@peering.example") || strings.Contains(string(raw), "Reader") {
		t.Fatal("partner discovery disclosed person/contact data")
	}
	request := source.ok("POST", "/api/peering/requests", Record{"toMemberId": f.otherMember["id"], "vlanId": vlanID, "message": "希望建立双边互联"})
	id := str(request, "id")
	if request["status"] != "pending" || request["topologyValid"] != true || request["canRevoke"] != true {
		t.Fatal(request)
	}
	if w := ordinaryRecipient.request("POST", "/api/peering/requests/"+id+"/decision", Record{"decision": "accept"}); w.Code != 409 {
		t.Fatal("ordinary recipient accepted on behalf of member")
	}
	if w := f.c.request("POST", "/api/peering/requests/"+id+"/decision", Record{"decision": "accept"}); w.Code != 409 {
		t.Fatal("staff fabricated member consent")
	}
	accepted := recipient.ok("POST", "/api/peering/requests/"+id+"/decision", Record{"decision": "accept", "message": "同意管理约定"})
	if accepted["status"] != "accepted" || len(accepted["events"].([]any)) != 2 {
		t.Fatal("accept event missing")
	}
	matrix := source.ok("GET", "/api/peering/matrix", nil)
	raw, _ = json.Marshal(matrix)
	if strings.Contains(string(raw), "Established") || matrix["agreementIsOperationalState"] != false {
		t.Fatal("administrative agreement fabricated BGP state")
	}
	if len(matrix["bilateral"].([]any)) != 1 || len(matrix["routeServer"].([]any)) != 1 {
		t.Fatal("bilateral and configured RS relationships not separated")
	}
	if w := source.request("POST", "/api/peering/requests/"+id+"/revoke", Record{}); w.Code != 409 {
		t.Fatal("ordinary source revoked accepted agreement without admin authority")
	}
	revoked := recipient.ok("POST", "/api/peering/requests/"+id+"/revoke", Record{"message": "撤销约定"})
	if revoked["status"] != "revoked" || len(revoked["events"].([]any)) != 3 {
		t.Fatal("immutable revoke event missing")
	}
	state := f.c.a.store.Snapshot()
	if err := validatePeering(&state); err != nil {
		t.Fatal(err)
	}
	state.PeeringRequests[0].Events[1].Kind = "proposed"
	if err := validatePeering(&state); err == nil {
		t.Fatal("invalid event history accepted")
	}
}
func TestPeeringPrivacyFlagsAreIndependentFromIXFExport(t *testing.T) {
	f := monitoringFixture(t)
	source := peeringUser(t, f.c, "Source", "privacy-source@peering.example", str(f.member, "id"), false)
	recipient := peeringUser(t, f.c, "Recipient", "privacy-target@peering.example", str(f.otherMember, "id"), true)
	vlanID := str(f.connection, "vlanId")
	resourceUpdate(t, f.c, "members", str(f.otherMember, "id"), Record{"excludeIxf": true})
	resourceUpdate(t, f.c, "vlans", vlanID, Record{"excludeIxf": true})
	if len(source.ok("GET", "/api/peering/partners", nil)["members"].([]any)) != 1 {
		t.Fatal("IX-F export opt-out incorrectly disabled authenticated peer discovery")
	}
	resourceUpdate(t, f.c, "members", str(f.otherMember, "id"), Record{"peeringManagerEnabled": false})
	if len(source.ok("GET", "/api/peering/partners", nil)["members"].([]any)) != 0 {
		t.Fatal("member manager opt-out ignored")
	}
	matrix := source.ok("GET", "/api/peering/matrix", nil)
	if len(matrix["members"].([]any)) != 2 {
		t.Fatal("matrix visibility incorrectly depended on manager toggle")
	}
	resourceUpdate(t, f.c, "members", str(f.otherMember, "id"), Record{"peeringManagerEnabled": true, "peeringMatrixEnabled": false})
	if len(source.ok("GET", "/api/peering/matrix", nil)["members"].([]any)) != 1 {
		t.Fatal("member matrix opt-out ignored")
	}
	request := source.ok("POST", "/api/peering/requests", Record{"toMemberId": f.otherMember["id"], "vlanId": vlanID})
	id := str(request, "id")
	resourceUpdate(t, f.c, "vlans", vlanID, Record{"private": true, "peeringManager": true, "peeringMatrix": true})
	if len(source.ok("GET", "/api/peering/partners", nil)["members"].([]any)) != 0 {
		t.Fatal("private VLAN disclosed by explicit manager flag")
	}
	if w := recipient.request("POST", "/api/peering/requests/"+id+"/decision", Record{"decision": "accept"}); w.Code != 409 {
		t.Fatal("decision failed to recheck changed private topology")
	}
	rows := source.ok("GET", "/api/peering/requests", nil)["items"].([]any)
	row := rows[0].(map[string]any)
	if row["topologyValid"] != false || len(row["toAddresses"].([]any)) != 0 {
		t.Fatal("stale/private topology exposed current peer addresses")
	}
}
func TestBilateralRequiresExactVLANAndActiveMembership(t *testing.T) {
	f := monitoringFixture(t)
	source := peeringUser(t, f.c, "Source", "vlan-source@peering.example", str(f.member, "id"), true)
	recipient := peeringUser(t, f.c, "Target", "vlan-target@peering.example", str(f.otherMember, "id"), true)
	fabric := f.c.create("infrastructures", Record{"name": "Another fabric"})
	otherVLAN := f.c.create("vlans", Record{"name": "Same numeric tag", "tag": 100, "ipv4Prefix": "198.51.100.0/24", "infrastructureId": fabric["id"]})
	if w := source.request("POST", "/api/peering/requests", Record{"toMemberId": f.otherMember["id"], "vlanId": otherVLAN["id"]}); w.Code != 409 {
		t.Fatal("numeric VLAN tag confused distinct fabrics")
	}
	request := source.ok("POST", "/api/peering/requests", Record{"toMemberId": f.otherMember["id"], "vlanId": f.connection["vlanId"]})
	id := str(request, "id")
	f.c.ok("POST", "/api/members/"+str(f.otherMember, "id")+"/close", Record{"reason": "离开交换中心"})
	if w := recipient.request("POST", "/api/peering/requests/"+id+"/decision", Record{"decision": "accept"}); w.Code != 409 {
		t.Fatal("closed member accepted peering")
	}
	state := f.c.a.store.Snapshot()
	if err := validatePeering(&state); err != nil {
		t.Fatal("history prevented member closure")
	}
}
func TestPeeringTenantHistoryAndExplicitEmailOnly(t *testing.T) {
	f := monitoringFixture(t)
	source := peeringUser(t, f.c, "Source", "notify-source@peering.example", str(f.member, "id"), false)
	target := peeringUser(t, f.c, "Target", "notify-target@peering.example", str(f.otherMember, "id"), true)
	if w := source.request("POST", "/api/peering/requests", Record{"toMemberId": f.otherMember["id"], "vlanId": f.connection["vlanId"], "notify": true}); w.Code != 503 {
		t.Fatal("disabled SMTP pretended to notify")
	}
	enableTestMail(f.c.a)
	request := source.ok("POST", "/api/peering/requests", Record{"toMemberId": f.otherMember["id"], "vlanId": f.connection["vlanId"], "notify": true})
	if len(f.c.a.store.Snapshot().Outbox.Entries) != 1 {
		t.Fatal("explicit notification was not queued")
	}
	target.ok("POST", "/api/peering/requests/"+str(request, "id")+"/decision", Record{"decision": "reject"})
	if len(f.c.a.store.Snapshot().Outbox.Entries) != 1 {
		t.Fatal("implicit decision email was queued")
	}
	thirdMember := f.c.create("members", Record{"name": "Unrelated", "asn": 64503, "status": "active"})
	third := peeringUser(t, f.c, "Third", "third@peering.example", str(thirdMember, "id"), true)
	if len(third.ok("GET", "/api/peering/requests", nil)["items"].([]any)) != 0 {
		t.Fatal("unrelated tenant saw peering history")
	}
	if w := third.request("POST", "/api/peering/requests/"+str(request, "id")+"/revoke", Record{}); w.Code != 409 {
		t.Fatal("unrelated tenant mutated peering")
	}
}

func TestPeeringQueuedMailRechecksRecipientAuthority(t *testing.T) {
	f := monitoringFixture(t)
	source := peeringUser(t, f.c, "Source", "queue-source@peering.example", str(f.member, "id"), false)
	target := peeringUser(t, f.c, "Target", "queue-target@peering.example", str(f.otherMember, "id"), true)
	_ = target
	sender := enableTestMail(f.c.a)
	source.ok("POST", "/api/peering/requests", Record{"toMemberId": f.otherMember["id"], "vlanId": f.connection["vlanId"], "notify": true})
	state := f.c.a.store.Snapshot()
	userID := str(emailUser(&state, "queue-target@peering.example"), "id")
	resourceUpdate(t, f.c, "users", userID, Record{"memberAdminIds": nil})
	if err := f.c.a.deliverOne(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.messages) != 0 || f.c.a.store.Snapshot().Outbox.Entries[0].Status != "expired" {
		t.Fatal("notification sent after scoped recipient authority was removed")
	}
}

func TestBilateralASNSnapshotMustStillMatchAtDecision(t *testing.T) {
	f := monitoringFixture(t)
	source := peeringUser(t, f.c, "Source", "asn-source@peering.example", str(f.member, "id"), false)
	target := peeringUser(t, f.c, "Target", "asn-target@peering.example", str(f.otherMember, "id"), true)
	request := source.ok("POST", "/api/peering/requests", Record{"toMemberId": f.otherMember["id"], "vlanId": f.connection["vlanId"]})
	if request["fromASN"] != float64(64501) || request["toASN"] != float64(64502) {
		t.Fatal("agreement did not snapshot network identity")
	}
	resourceUpdate(t, f.c, "members", str(f.otherMember, "id"), Record{"asn": 64509})
	if w := target.request("POST", "/api/peering/requests/"+str(request, "id")+"/decision", Record{"decision": "accept"}); w.Code != 409 {
		t.Fatal("recipient accepted agreement for a changed ASN")
	}
	rows := source.ok("GET", "/api/peering/requests", nil)["items"].([]any)
	row := rows[0].(map[string]any)
	if row["toASN"] != float64(64502) || row["topologyValid"] != false {
		t.Fatal("historical ASN was rewritten or treated as current")
	}
}
