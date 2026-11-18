package app

import (
	"strings"
	"testing"
)

func TestFabricIsolationAndAtomicAssignment(t *testing.T) {
	c, _ := setupClient(t)
	location := c.create("locations", Record{"name": "DC"})
	a := c.create("infrastructures", Record{"name": "FabricA", "shortName": "A", "ixfId": 101, "isPrimary": true, "excludeIxf": false})
	b := c.create("infrastructures", Record{"name": "FabricB", "shortName": "B", "ixfId": 102, "excludeIxf": false})
	swA := c.create("switches", Record{"name": "A", "locationId": location["id"], "infrastructureId": a["id"]})
	swB := c.create("switches", Record{"name": "B", "locationId": location["id"], "infrastructureId": b["id"]})
	pA := c.create("ports", Record{"name": "eth1", "switchId": swA["id"], "speed": 10000})
	pB := c.create("ports", Record{"name": "eth1", "switchId": swB["id"], "speed": 25000})
	vA := c.create("vlans", Record{"name": "A", "tag": 100, "infrastructureId": a["id"], "ipv4Prefix": "192.0.2.0/24"})
	vB := c.create("vlans", Record{"name": "B", "tag": 100, "infrastructureId": b["id"], "ipv4Prefix": "192.0.2.0/24"})
	member := c.create("members", Record{"name": "Member", "asn": 64501, "status": "active"})
	if w := c.request("POST", "/api/resources/connections", Record{"name": "Wrong", "memberId": member["id"], "portId": pA["id"], "vlanId": vB["id"], "ipv4": "192.0.2.2", "status": "active"}); w.Code != 400 {
		t.Fatal("crossfabric link accepted")
	}
	c.create("connections", Record{"name": "A", "memberId": member["id"], "portId": pA["id"], "vlanId": vA["id"], "ipv4": "192.0.2.2", "status": "active"})
	c.create("connections", Record{"name": "B", "memberId": member["id"], "portId": pB["id"], "vlanId": vB["id"], "ipv4": "192.0.2.2", "status": "active"})
	if w := c.request("POST", "/api/fabrics/assign", Record{"infrastructureId": b["id"], "switchIds": []string{str(swA, "id")}}); w.Code != 409 {
		t.Fatal("partial topology moved")
	}
	third := c.create("infrastructures", Record{"name": "C", "shortName": "C", "ixfId": 103, "excludeIxf": false})
	c.ok("POST", "/api/fabrics/assign", Record{"infrastructureId": third["id"], "switchIds": []string{str(swA, "id")}, "vlanIds": []string{str(vA, "id")}})
	s := c.a.store.Snapshot()
	if fabricID(find(&s, "switches", str(swA, "id"))) != str(third, "id") {
		t.Fatal("atomicmove not committed")
	}
	doc, err := exportIXF(&s)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.IXPList) != 3 || len(doc.MemberList) != 1 || len(doc.MemberList[0].Connections) != 2 {
		t.Fatal("IXF flattened fabrics")
	}
	// An entire infrastructure opts out without exposing its membership or switch.
	c.ok("PUT", "/api/resources/infrastructures/"+str(b, "id"), Record{"name": "B", "shortName": "B", "ixfId": 102, "excludeIxf": true})
	s = c.a.store.Snapshot()
	doc, err = exportIXF(&s)
	if err != nil || len(doc.MemberList[0].Connections) != 1 {
		t.Fatal("excluded fabric leaked")
	}
}
func TestMultiPrefixVLANUsesRealRanges(t *testing.T) {
	c, _ := setupClient(t)
	v := c.create("vlans", Record{"name": "Multi", "tag": 10, "ipv4Prefix": "192.0.2.0/24", "prefixes": []string{"198.51.100.0/24", "2001:db8::/64", "2001:db8:1::/64"}})
	member := c.create("members", Record{"name": "Member", "asn": 64501, "status": "active"})
	loc := c.create("locations", Record{"name": "DC"})
	sw := c.create("switches", Record{"name": "SW", "locationId": loc["id"]})
	port := c.create("ports", Record{"name": "eth1", "switchId": sw["id"], "speed": 10000})
	c.create("connections", Record{"name": "Second subnet", "memberId": member["id"], "portId": port["id"], "vlanId": v["id"], "ipv4": "198.51.100.2", "ipv6": "2001:db8:1::2", "status": "active"})
	c.create("routers", Record{"name": "RS", "asn": 64500, "routerId": "192.0.2.1", "localIpv4": "198.51.100.1", "localIpv6": "2001:db8:1::1", "vlanId": v["id"]})
	if w := c.request("POST", "/api/ipam/allocate", Record{"vlanId": v["id"], "memberId": member["id"], "family": 4}); w.Code != 409 {
		t.Fatal("ambiguous allocation silently chose firstprefix")
	}
	got := c.ok("POST", "/api/ipam/allocate", Record{"vlanId": v["id"], "memberId": member["id"], "family": 4, "prefix": "198.51.100.0/24"})
	if got["items"].([]any)[0].(map[string]any)["address"] != "198.51.100.3" {
		t.Fatal("allocation didn't use selected range")
	}
	if n := len(c.ok("GET", "/api/ipam?vlanId="+str(v, "id"), nil)["prefixes"].([]any)); n != 4 {
		t.Fatal("prefix inventory incomplete")
	}
	c.ok("PUT", "/api/settings", Record{"name": "IXP", "asn": 64500, "ixfId": 1, "shortName": "IXP"})
	s := c.a.store.Snapshot()
	doc, err := exportIXF(&s)
	if err != nil {
		t.Fatal(err)
	}
	if doc.IXPList[0].VLANs[0].IPv4 != nil || doc.IXPList[0].VLANs[0].IPv6 != nil {
		t.Fatal("multiple ranges became invented IXFaggregate")
	}
	if !strings.HasPrefix(doc.MemberList[0].Connections[0].VLANs[0].IPv4.Address, "198.51.100.") {
		t.Fatal("secondary address lost")
	}
}

func TestPrimaryFabricSwitchIsAtomic(t *testing.T) {
	c, _ := setupClient(t)
	a := c.create("infrastructures", Record{"name": "A", "isPrimary": true})
	b := c.create("infrastructures", Record{"name": "B"})
	c.ok("PUT", "/api/resources/infrastructures/"+str(b, "id"), Record{"name": "B", "isPrimary": true})
	s := c.a.store.Snapshot()
	if find(&s, "infrastructures", str(a, "id"))["isPrimary"] == true || find(&s, "infrastructures", str(b, "id"))["isPrimary"] != true {
		t.Fatal("primary not switched atomically")
	}
	if w := c.request("POST", "/api/resources/infrastructures", Record{"name": "", "isPrimary": true}); w.Code != 400 {
		t.Fatal("invalid infrastructure accepted")
	}
	s = c.a.store.Snapshot()
	if find(&s, "infrastructures", str(b, "id"))["isPrimary"] != true {
		t.Fatal("failed create cleared primary")
	}
}
