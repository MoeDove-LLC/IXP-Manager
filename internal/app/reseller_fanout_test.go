package app

import (
	"net/http"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/core"
)

type resellerFanoutFixture struct {
	client                            *client
	member, reseller, other           string
	peering, lagPort, passive, spare  string
	switchID, locationID              string
	firstVLAN, secondVLAN             string
	firstConnection, secondConnection string
}

func newResellerFanoutFixture(t *testing.T) resellerFanoutFixture {
	t.Helper()
	c, _ := setupClient(t)
	f := resellerFanoutFixture{client: c}
	f.reseller = str(c.create("members", Record{"name": "Designated reseller", "asn": 64510, "isReseller": true, "status": "active"}), "id")
	f.member = str(c.create("members", Record{"name": "Sold member", "asn": 64511, "resellerId": f.reseller, "status": "active"}), "id")
	f.other = str(c.create("members", Record{"name": "Other designated reseller", "asn": 64512, "isReseller": true, "status": "active"}), "id")
	f.locationID = str(c.create("locations", Record{"name": "Fanout site"}), "id")
	f.switchID = str(c.create("switches", Record{"name": "Peering switch", "locationId": f.locationID}), "id")
	fanoutSwitch := str(c.create("switches", Record{"name": "Fanout switch", "locationId": f.locationID}), "id")
	port := func(name, switchID string) string {
		return str(c.create("ports", Record{"name": name, "switchId": switchID, "status": "active"}), "id")
	}
	f.peering, f.lagPort = port("Peering port", f.switchID), port("LAG second port", f.switchID)
	f.passive, f.spare = port("Passive fanout port", fanoutSwitch), port("Spare physical port", fanoutSwitch)
	f.firstVLAN = str(c.create("vlans", Record{"name": "Peering 100", "tag": 100, "ipv4Prefix": "192.0.2.0/24"}), "id")
	f.secondVLAN = str(c.create("vlans", Record{"name": "Peering 200", "tag": 200, "ipv4Prefix": "198.51.100.0/24"}), "id")
	connect := func(name, vlan, address string) string {
		return str(c.create("connections", Record{"name": name, "memberId": f.member, "vlanId": vlan, "portIds": []string{f.peering, f.lagPort}, "linkMode": "lag", "status": "active", "ipv4": address}), "id")
	}
	f.firstConnection = connect("LAG on VLAN 100", f.firstVLAN, "192.0.2.10")
	f.secondConnection = connect("LAG on VLAN 200", f.secondVLAN, "198.51.100.10")
	return f
}

func (f resellerFanoutFixture) pair() Record {
	return Record{"name": "Sold member physical fanout", "memberId": f.member, "resellerId": f.reseller, "peeringPortId": f.peering, "fanoutPortId": f.passive}
}

const resellerFanoutPath = "/api/resources/" + resellerFanoutKind

func TestResellerFanoutInventoryCASAndLifecycle(t *testing.T) {
	f := newResellerFanoutFixture(t)
	c := f.client
	pair := c.create(resellerFanoutKind, f.pair())
	pairID := str(pair, "id")
	path := resellerFanoutPath + "/" + pairID
	read := requestResourceRevision(c, http.MethodGet, path, nil, "")
	if read.Code != http.StatusOK || read.Header().Get("ETag") == "" || pair["vlanId"] != nil || pair["ipv4"] != nil {
		t.Fatal("physical pair did not retain the normal resource contract")
	}
	update := f.pair()
	update["notes"] = "Reviewed existing cabling"
	if w := requestResourceRevision(c, http.MethodPut, path, update, ""); w.Code != http.StatusPreconditionRequired {
		t.Fatalf("fanout PUT bypassed CAS: HTTP %d", w.Code)
	}
	if w := requestResourceRevision(c, http.MethodPut, path, update, read.Header().Get("ETag")); w.Code != http.StatusOK || w.Header().Get("ETag") == read.Header().Get("ETag") {
		t.Fatalf("reviewed fanout PUT did not advance its ETag: HTTP %d", w.Code)
	}
	if w := requestResourceRevision(c, http.MethodDelete, path, nil, read.Header().Get("ETag")); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("old review released a physical reservation: HTTP %d", w.Code)
	}
	c.create("users", Record{"name": "Sold viewer", "email": "reseller-fanout-member@example.test", "password": "sold-member-long-password", "role": "member", "memberId": f.member})
	m := docLoginClient(t, c.a, "reseller-fanout-member@example.test", "sold-member-long-password")
	if got := m.ok(http.MethodGet, resellerFanoutPath, nil)["items"].([]any); len(got) != 0 {
		t.Fatal("physical reseller topology leaked to a member")
	}
	if w := m.request(http.MethodGet, path, nil); w.Code != http.StatusNotFound {
		t.Fatalf("known fanout ID disclosed staff inventory: HTTP %d", w.Code)
	}
	if w := m.request(http.MethodPost, resellerFanoutPath, f.pair()); w.Code != http.StatusForbidden {
		t.Fatalf("member mutated staff fanout inventory: HTTP %d", w.Code)
	}

	// Both VLANs use the existing LAG. Closing a member disables intent while
	// preserving the physical pairing, and any one VLAN may be removed first.
	c.ok(http.MethodPost, "/api/members/"+f.member+"/close", Record{"reason": "fixture lifecycle"})
	c.ok(http.MethodPost, "/api/members/"+f.reseller+"/suspend", Record{"reason": "fixture lifecycle"})
	c.ok(http.MethodPut, "/api/resources/ports/"+f.peering, Record{"name": "Peering port", "switchId": f.switchID, "memberId": f.member, "status": "inactive"})
	if got := c.ok(http.MethodGet, path, nil); str(got, "id") != pairID {
		t.Fatal("member or port lifecycle released the physical reservation")
	}
	c.ok(http.MethodDelete, "/api/resources/connections/"+f.firstConnection, nil)
	c.ok(http.MethodDelete, "/api/resources/vlans/"+f.firstVLAN, nil)
	last := "/api/resources/connections/" + f.secondConnection
	if w := c.request(http.MethodDelete, last, nil); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "reseller fanout peering port") {
		t.Fatalf("last access connection was orphaned: HTTP %d %s", w.Code, w.Body.String())
	}
	if got := c.ok(http.MethodGet, last, nil); got["status"] != "disabled" || got["ipv4"] != "198.51.100.10" {
		t.Fatal("physical metadata changed existing VLAN or address facts")
	}
	c.ok(http.MethodDelete, path, nil)
	c.ok(http.MethodDelete, last, nil)
	c.ok(http.MethodDelete, "/api/resources/vlans/"+f.secondVLAN, nil)
}

func TestResellerFanoutRejectsConflictingAssignmentsThroughGenericAndPatchAPI(t *testing.T) {
	f := newResellerFanoutFixture(t)
	c := f.client
	bad := f.pair()
	bad["resellerId"] = f.other
	if w := c.request(http.MethodPost, resellerFanoutPath, bad); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "directly designated") {
		t.Fatalf("unrelated designated reseller was accepted: HTTP %d %s", w.Code, w.Body.String())
	}
	bad = f.pair()
	bad["peeringPortId"] = f.spare
	if w := c.request(http.MethodPost, resellerFanoutPath, bad); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "access connection") {
		t.Fatalf("unanchored peering endpoint accepted: HTTP %d %s", w.Code, w.Body.String())
	}
	c.create(resellerFanoutKind, f.pair())
	if w := c.request(http.MethodPost, resellerFanoutPath, f.pair()); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "already used") {
		t.Fatalf("physical endpoint reused: HTTP %d %s", w.Code, w.Body.String())
	}
	bad = f.pair()
	bad["peeringPortId"], bad["fanoutPortId"] = f.lagPort, f.peering
	if w := c.request(http.MethodPost, resellerFanoutPath, bad); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "already used") {
		t.Fatalf("a physical endpoint became part of a fanout chain: HTTP %d %s", w.Code, w.Body.String())
	}
	if w := c.request(http.MethodPost, "/api/resources/connections", Record{"name": "Conflicting connection", "memberId": f.reseller, "portId": f.passive, "vlanId": f.firstVLAN, "status": "disabled"}); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "passive reseller fanout") {
		t.Fatalf("nonactive access connection occupied passive port: HTTP %d %s", w.Code, w.Body.String())
	}
	portPath := "/api/resources/ports/" + f.passive
	port := editableCredentialPeer(c.ok(http.MethodGet, portPath, nil))
	port["memberId"] = f.other
	if w := c.request(http.MethodPut, portPath, port); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "another member") {
		t.Fatalf("passive ownership reassignment accepted: HTTP %d %s", w.Code, w.Body.String())
	}
	port["memberId"] = f.reseller
	c.ok(http.MethodPut, portPath, port)
	port["coreReserved"] = true
	if w := c.request(http.MethodPut, portPath, port); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "reserved for core") {
		t.Fatalf("core reservation over passive port accepted: HTTP %d %s", w.Code, w.Body.String())
	}
	if w := c.request(http.MethodPost, "/api/resources/core-links", Record{"name": "Conflicting core arrow", "portAId": f.spare, "portBId": f.passive, "type": "layer2"}); w.Code != http.StatusBadRequest {
		t.Fatalf("flat core link over passive port accepted: HTTP %d %s", w.Code, w.Body.String())
	}
	delete(port, "memberId")
	delete(port, "coreReserved")
	c.ok(http.MethodPut, portPath, port)

	rack := c.create("racks", Record{"name": "Fanout rack", "locationId": f.locationID})
	panel := c.create("patch-panels", Record{"name": "Fanout pass-through", "rackId": rack["id"], "portCount": 12})
	patchPort := c.create("patch-ports", Record{"name": "Prewired fanout", "panelId": panel["id"], "number": 1, "switchPortId": f.passive, "status": "prewired"})
	var revision string
	for _, raw := range c.ok(http.MethodGet, "/api/patching/ports", nil)["items"].([]any) {
		row := Record(raw.(map[string]any))
		if row["id"] == patchPort["id"] {
			revision = str(row, "revision")
		}
	}
	if revision == "" {
		t.Fatal("prewired patch metadata did not coexist with fanout")
	}
	transitionPath := "/api/patching/ports/" + str(patchPort, "id") + "/transition"
	request := Record{"action": "reserve", "revision": revision, "memberId": f.other, "reason": "fixture"}
	if w := c.request(http.MethodPost, transitionPath, request); w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "reseller fanout endpoint") {
		t.Fatalf("patch allocation transferred fanout port ownership: HTTP %d %s", w.Code, w.Body.String())
	}
	request["memberId"] = f.reseller
	c.ok(http.MethodPost, transitionPath, request)
}

func TestResellerFanoutValidateAllChecksCoreAndPeeringBindings(t *testing.T) {
	f := newResellerFanoutFixture(t)
	f.client.create(resellerFanoutKind, f.pair())
	baseline := f.client.a.store.Snapshot()
	for _, tc := range []struct {
		name   string
		want   string
		change func(*State)
	}{
		{"identical endpoints", "distinct physical endpoints", func(s *State) { s.Resources[resellerFanoutKind][0]["fanoutPortId"] = f.peering }},
		{"peering port used by a different member", "owned by its member", func(s *State) {
			for _, c := range s.Resources["connections"] {
				c["memberId"] = f.other
			}
		}},
		{"unreserved flat port binding", "unbundled core link", func(s *State) {
			s.Resources["core-links"] = []Record{{"id": "legacy-flat", "name": "Flat", "type": "layer2", "portAId": f.passive, "portBId": f.spare}}
		}},
		{"unreserved typed port binding", "core bundle", func(s *State) {
			s.CoreBundles = []core.Bundle{{Links: []core.Link{{SideA: core.Endpoint{PortID: f.passive}, SideB: core.Endpoint{PortID: f.spare}}}}}
		}},
		{"separate VLAN owner change", "physical port already belongs to another member connection", func(s *State) {
			find(s, "connections", f.firstConnection)["memberId"] = f.other
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := cloneState(baseline)
			tc.change(&candidate)
			if err := validateAll(&candidate); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("conflicting state not rejected at the expected axis: %v", err)
			}
		})
	}
}
