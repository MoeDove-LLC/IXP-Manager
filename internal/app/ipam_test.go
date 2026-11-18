package app

import (
	"strings"
	"sync"
	"testing"
)

func TestIPAMReservationsAndAssignedReleaseGuard(t *testing.T) {
	c, _ := setupClient(t)
	m := c.create("members", Record{"name": "Member", "asn": 64501})
	v := c.create("vlans", Record{"name": "LAN", "tag": 10, "ipv4Prefix": "192.0.2.0/29", "ipv6Prefix": "2001:db8::/64"})
	c.create("routers", Record{"name": "RS", "asn": 64500, "routerId": "192.0.2.1", "localIpv4": "192.0.2.1", "vlanId": v["id"]})
	result := c.ok("POST", "/api/ipam/allocate", Record{"vlanId": v["id"], "memberId": m["id"], "family": 4, "count": 2})["items"].([]any)
	first := result[0].(map[string]any)
	if first["address"] != "192.0.2.2" || result[1].(map[string]any)["address"] != "192.0.2.3" {
		t.Fatal("allocated reserved or nonhost address")
	}
	if w := c.request("POST", "/api/ipam/allocate", Record{"vlanId": v["id"], "memberId": m["id"], "family": 4, "address": "192.0.2.7"}); w.Code != 409 {
		t.Fatal("broadcast allocated")
	}
	location := c.create("locations", Record{"name": "DC"})
	sw := c.create("switches", Record{"name": "SW", "locationId": location["id"]})
	port := c.create("ports", Record{"name": "Eth1", "switchId": sw["id"]})
	c.create("connections", Record{"name": "Circuit", "memberId": m["id"], "portId": port["id"], "vlanId": v["id"], "ipv4": first["address"]})
	if w := c.request("POST", "/api/ipam/release", Record{"id": first["id"], "expectedMemberId": m["id"]}); w.Code != 409 {
		t.Fatal("assigned address released")
	}
	second := result[1].(map[string]any)
	if w := c.request("POST", "/api/ipam/release", Record{"id": second["id"], "expectedMemberId": "other"}); w.Code != 409 {
		t.Fatal("stale owner release accepted")
	}
	c.ok("POST", "/api/ipam/release", Record{"id": second["id"], "expectedMemberId": m["id"]})
	list := c.ok("GET", "/api/ipam?vlanId="+str(v, "id"), nil)
	prefixes := list["prefixes"].([]any)
	if prefixes[1].(map[string]any)["usable"] != "18446744073709551615" {
		t.Fatal("IPv6 count lost integer precision")
	}
}
func TestConcurrentIPAMAndCanonicalAddresses(t *testing.T) {
	c, _ := setupClient(t)
	m := c.create("members", Record{"name": "Member", "asn": 64501})
	v := c.create("vlans", Record{"name": "LAN", "tag": 11, "ipv6Prefix": "2001:DB8::/120"})
	c.create("ip-addresses", Record{"name": "Reserved", "address": "2001:0db8:0000:0000::1", "vlanId": v["id"], "memberId": m["id"]})
	if w := c.request("POST", "/api/ipam/allocate", Record{"vlanId": v["id"], "memberId": m["id"], "family": 6, "address": "2001:db8::1"}); w.Code != 409 {
		t.Fatal("alias bypassed reservation")
	}
	var wg sync.WaitGroup
	errors := make(chan string, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := c.request("POST", "/api/ipam/allocate", Record{"vlanId": v["id"], "memberId": m["id"], "family": 6})
			if w.Code != 201 {
				errors <- w.Body.String()
			}
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	state := c.a.store.Snapshot()
	seen := map[string]bool{}
	for _, p := range state.Resources["ip-addresses"] {
		ip := str(p, "address")
		if seen[ip] || !strings.HasPrefix(ip, "2001:db8::") {
			t.Fatal("duplicate or noncanonical allocation")
		}
		seen[ip] = true
	}
	if len(seen) != 5 {
		t.Fatal("reservation transaction lost")
	}
}
func TestAssetUnitCollisionsAndNonPeeringMember(t *testing.T) {
	c, _ := setupClient(t)
	associate := c.create("members", Record{"name": "Partner", "type": "associate", "status": "active"})
	if _, ok := associate["asn"]; ok {
		t.Fatal("fabricated ASN")
	}
	if w := c.request("POST", "/api/resources/members", Record{"name": "Peer", "type": "full"}); w.Code != 400 {
		t.Fatal("peering member missing ASN")
	}
	loc := c.create("locations", Record{"name": "DC"})
	rack := c.create("racks", Record{"name": "Rack", "locationId": loc["id"], "units": 42})
	c.create("equipment", Record{"name": "IXP appliance", "rackId": rack["id"], "unitStart": 10, "unitHeight": 2})
	if w := c.request("POST", "/api/resources/switches", Record{"name": "Overlap", "locationId": loc["id"], "rackId": rack["id"], "unitStart": 11, "unitHeight": 1}); w.Code != 400 {
		t.Fatal("rack overlap allowed")
	}
}
