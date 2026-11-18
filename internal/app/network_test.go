package app

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func networkFixture() State {
	return State{Version: 1, Settings: Record{"name": "Test IXP", "ixfId": float64(42), "shortName": "TIX"}, Resources: map[string][]Record{
		"members":   {{"id": "m1", "name": "First member", "asn": float64(64501), "status": "active"}, {"id": "m2", "name": "Second member", "asn": float64(64502), "status": "active"}},
		"locations": {{"id": "l1", "name": "DC", "country": "DE"}},
		"switches":  {{"id": "sw1", "name": "sw-a", "locationId": "l1", "managementIp": "192.0.2.250"}, {"id": "sw2", "name": "sw-b", "locationId": "l1"}},
		"ports":     {{"id": "p1", "name": "xe-1", "switchId": "sw1", "speed": float64(10000), "status": "active"}, {"id": "p2", "name": "xe-2", "switchId": "sw2", "speed": float64(25000), "status": "active"}, {"id": "p3", "name": "unknown-speed", "switchId": "sw1"}, {"id": "p4", "name": "disabled-port", "switchId": "sw1", "speed": float64(10000), "status": "inactive"}},
		"vlans":     {{"id": "v1", "name": "LAN1", "tag": float64(100), "ipv4Prefix": "192.0.2.0/24", "ipv6Prefix": "2001:db8::/64"}, {"id": "v2", "name": "LAN2", "tag": float64(200), "ipv4Prefix": "198.51.100.0/24"}},
	}}
}
func lagConnection() Record {
	return Record{"id": "c1", "name": "LAG", "memberId": "m1", "vlanId": "v1", "portIds": []any{"p1", "p2"}, "ipv4": "192.0.2.10", "ipv6": "2001:db8::10", "status": "active", "mtu": float64(9000)}
}
func TestNetworkConnectionConstraints(t *testing.T) {
	cases := []struct {
		name   string
		change func(*State, Record)
	}{
		{"both port forms", func(s *State, r Record) { r["portId"] = "p1" }},
		{"duplicate ports", func(s *State, r Record) { r["portIds"] = []any{"p1", "p1"} }},
		{"missing reference", func(s *State, r Record) { r["portIds"] = []any{"p1", "missing"} }},
		{"no ports", func(s *State, r Record) { r["portIds"] = []any{} }},
		{"single with multiple ports", func(s *State, r Record) { r["linkMode"] = "single" }},
		{"wrong owner", func(s *State, r Record) { s.Resources["ports"][0]["memberId"] = "m2" }},
		{"inactive physical", func(s *State, r Record) { r["portIds"] = []any{"p1", "p4"} }},
		{"invented aggregate", func(s *State, r Record) { r["speed"] = float64(70000) }},
		{"unknown aggregate assertion", func(s *State, r Record) { r["portIds"] = []any{"p1", "p3"}; r["speed"] = float64(10000) }},
		{"invalid MTU", func(s *State, r Record) { r["mtu"] = float64(9217) }},
		{"same VLAN overlap", func(s *State, r Record) {
			s.Resources["connections"] = []Record{{"id": "other", "memberId": "m1", "vlanId": "v1", "portId": "p1"}}
		}},
		{"different owner other VLAN", func(s *State, r Record) {
			s.Resources["connections"] = []Record{{"id": "other", "memberId": "m2", "vlanId": "v2", "portIds": []any{"p1", "p2"}}}
		}},
		{"partial bundle overlap", func(s *State, r Record) {
			s.Resources["connections"] = []Record{{"id": "other", "memberId": "m1", "vlanId": "v2", "portId": "p1"}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := networkFixture()
			r := lagConnection()
			tc.change(&s, r)
			if err := validate(&s, "connections", r); err == nil {
				t.Fatal("unsafe connection accepted")
			}
		})
	}
}
func TestNetworkSingleCompatibilityAndDerivedCapacity(t *testing.T) {
	s := networkFixture()
	single := lagConnection()
	delete(single, "portIds")
	single["portId"] = "p1"
	if err := validate(&s, "connections", single); err != nil {
		t.Fatal(err)
	}
	if str(single, "linkMode") != "single" {
		t.Fatal("single mode not inferred")
	}
	single["linkMode"] = "lag"
	if err := validate(&s, "connections", single); err != nil {
		t.Fatal("one-member LAG rejected")
	}
	r := lagConnection()
	r["speed"] = float64(35000)
	if err := validate(&s, "connections", r); err != nil {
		t.Fatal(err)
	}
	if str(r, "linkMode") != "lag" {
		t.Fatal("LAG mode not inferred")
	}
	projection := decorateConnection(&s, r)
	if projection["capacityKnown"] != true || number(projection, "capacityMbps") != 35000 {
		t.Fatal("wrong aggregate")
	}
	if _, mutated := r["capacityKnown"]; mutated {
		t.Fatal("response projection mutated stored record")
	}
	unknown := lagConnection()
	unknown["portIds"] = []any{"p1", "p3"}
	if err := validate(&s, "connections", unknown); err != nil {
		t.Fatal("unset physical status broke compatibility")
	}
	projection = decorateConnection(&s, unknown)
	if projection["capacityKnown"] != false {
		t.Fatal("unknown speed represented as known")
	}
	if _, has := projection["capacityMbps"]; has {
		t.Fatal("partial sum fabricated")
	}
}
func TestNetworkLAGMultiVLANIXFPhysicalSpeeds(t *testing.T) {
	s := networkFixture()
	first := lagConnection()
	second := lagConnection()
	second["id"] = "c2"
	second["vlanId"] = "v2"
	second["ipv4"] = "198.51.100.10"
	delete(second, "ipv6")
	second["portIds"] = []any{"p2", "p1"}
	s.Resources["connections"] = []Record{first, second}
	if err := validateAll(&s); err != nil {
		t.Fatal(err)
	}
	doc, err := exportIXF(&s)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.MemberList) != 1 || len(doc.MemberList[0].Connections) != 1 {
		t.Fatal("same LAG duplicated instead of grouping VLANs")
	}
	c := doc.MemberList[0].Connections[0]
	if len(c.Interfaces) != 2 || c.Interfaces[0].SpeedMbps != 10000 || c.Interfaces[1].SpeedMbps != 25000 || len(c.VLANs) != 2 {
		t.Fatalf("incorrect physical export: %+v", c)
	}
	delete(s.Resources["ports"][1], "speed")
	s.Resources["connections"][0]["speed"] = float64(35000)
	if _, err = exportIXF(&s); err == nil {
		t.Fatal("unknown physical speed filled from logical aggregate")
	}
}
func TestLAGAPIReferenceAndResponseProjection(t *testing.T) {
	c, _ := setupClient(t)
	s := networkFixture()
	c.ok("POST", "/api/import", Record{"version": 1, "resources": s.Resources})
	body := lagConnection()
	delete(body, "id")
	result := c.create("connections", body)
	if result["capacityKnown"] != true || number(result, "capacityMbps") != 35000 {
		t.Fatalf("API lacks derived capacity: %v", result)
	}
	details := result["portDetails"].([]any)
	if len(details) != 2 || str(details[0].(map[string]any), "switchName") != "sw-a" {
		t.Fatal("safe physical labels missing")
	}
	for _, key := range []string{"capacityKnown", "capacityMbps", "portDetails"} {
		bad := clean(body)
		bad[key] = result[key]
		if w := c.request("POST", "/api/resources/connections", bad); w.Code != 400 {
			t.Fatalf("projection field %s accepted on write", key)
		}
	}
	if w := c.request("DELETE", "/api/resources/ports/p2", nil); w.Code != 400 {
		t.Fatal("deleted referenced LAG member port")
	}
	if w := c.request("PUT", "/api/resources/ports/p1", Record{"name": "xe-1", "switchId": "sw1", "speed": 10000, "memberId": "m2"}); w.Code != 400 {
		t.Fatal("reassigned an occupied physical port")
	}
	state := c.a.store.Snapshot()
	own := visible(&state, "connections", Record{"role": "member", "memberId": "m1"})
	other := visible(&state, "connections", Record{"role": "member", "memberId": "m2"})
	if len(own) != 1 || len(other) != 0 || len(visible(&state, "ports", Record{"role": "member", "memberId": "m1"})) != 0 {
		t.Fatal("connection projection widened member inventory access")
	}
	raw, _ := json.Marshal(own)
	if strings.Contains(string(raw), "managementIp") || strings.Contains(string(raw), "192.0.2.250") || strings.Contains(string(raw), "unknown-speed") {
		t.Fatal("projection exposed unrelated inventory")
	}
}
func TestLegacyLAGConverterAPIIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "run", "./cmd/ixp-migrate", "-strict", "-input", "tools/migrate/example-legacy-lag.json")
	cmd.Dir = "../.."
	output, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			t.Fatalf("converter: %v: %s", err, ee.Stderr)
		}
		t.Fatal(err)
	}
	var payload Record
	if err = json.Unmarshal(output, &payload); err != nil {
		t.Fatal(err)
	}
	c, _ := setupClient(t)
	result := c.ok("POST", "/api/import", payload)
	c.ok("PUT", "/api/settings", Record{"name": "Test IXP", "asn": 64500, "ixfId": 42, "shortName": "TIX"})
	if number(result, "imported") != 12 {
		t.Fatalf("lost fixture resources: %v", result)
	}
	state := c.a.store.Snapshot()
	first := find(&state, "connections", "legacy-connections-1")
	ids, err := connectionPortIDs(first)
	if err != nil || len(ids) != 2 || str(first, "linkMode") != "lag" || number(first, "mtu") != 9000 {
		t.Fatal("LAG mapping lost")
	}
	doc, err := exportIXF(&state)
	if err != nil {
		t.Fatal(err)
	}
	interfaces := doc.MemberList[0].Connections[0].Interfaces
	if len(interfaces) != 2 || interfaces[0].SpeedMbps != 10000 || interfaces[1].SpeedMbps != 25000 || len(doc.MemberList[0].Connections[0].VLANs) != 2 {
		t.Fatal("CLI-to-API-to-IXF flattened bundle")
	}
}
