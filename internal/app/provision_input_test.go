package app

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/provision"
)

func provisionProjectionFixture() State {
	s := networkFixture()
	s.Resources["infrastructures"] = []Record{{"id": "fabric-a", "name": "交换网络甲"}, {"id": "fabric-b", "name": "交换网络乙"}}
	s.Resources["locations"] = append(s.Resources["locations"], Record{"id": "l2", "name": "第二机房"})
	s.Resources["racks"] = []Record{{"id": "rack-a", "name": "A1", "locationId": "l1"}, {"id": "rack-b", "name": "B1", "locationId": "l2"}}
	s.Resources["members"][0]["name"] = "会员甲 & Network"
	s.Resources["switches"][0]["infrastructureId"] = "fabric-a"
	s.Resources["switches"][0]["rackId"] = "rack-a"
	s.Resources["switches"][0]["dnsName"] = "SW-A.Example.NET"
	s.Resources["switches"][1]["infrastructureId"] = "fabric-a"
	s.Resources["switches"][1]["rackId"] = "rack-b"
	s.Resources["switches"][1]["locationId"] = "l2"
	s.Resources["switches"][1]["managementIp"] = "2001:db8:ffff::2"
	s.Resources["switches"][1]["monitor"] = false
	s.Resources["switches"] = append(s.Resources["switches"], Record{"id": "sw3", "name": "Other fabric switch", "locationId": "l1", "infrastructureId": "fabric-b", "managementIp": "192.0.2.251"})
	s.Resources["ports"] = append(s.Resources["ports"], Record{"id": "p5", "name": "Other fabric port", "switchId": "sw3"})
	s.Resources["vlans"][0]["infrastructureId"] = "fabric-a"
	s.Resources["vlans"][1]["infrastructureId"] = "fabric-b"
	s.Resources["vlans"][1]["ipv4Prefix"] = "192.0.2.0/24"
	connection := lagConnection()
	connection["dnsName4"] = "MEMBER4.Example.NET."
	connection["dnsName6"] = "member6.example.net"
	connection["canPing4"] = false
	connection["monitorBusy"] = true
	s.Resources["connections"] = []Record{connection, {"id": "c2", "name": "other fabric connection", "memberId": "m2", "portId": "p5", "vlanId": "v2", "ipv4": "192.0.2.10", "status": "active"}}
	s.Resources["routers"] = []Record{
		{"id": "r1", "name": "Route Server", "asn": float64(64500), "routerId": "192.0.2.1", "vlanId": "v1", "localIpv4": "192.0.2.1", "localIpv6": "2001:db8::1", "dnsName4": "RS4.Example.NET", "monitor": false},
		{"id": "r2", "name": "Route Collector", "asn": float64(64500), "routerId": "192.0.2.1", "vlanId": "v2", "localIpv4": "192.0.2.1", "role": "route-collector"},
	}
	s.Resources["peers"] = []Record{
		{"id": "actual-peer-v6", "name": "V6 session", "memberId": "m1", "routerId": "r1", "address": "2001:0DB8:0:0:0:0:0:10", "enabled": true},
		{"id": "actual-peer-other-fabric", "name": "V4 session", "memberId": "m2", "routerId": "r2", "address": "192.0.2.10", "enabled": true, "monitor": false},
		{"id": "unmatched-peer", "name": "Awaiting connection", "memberId": "m2", "routerId": "r1", "address": "192.0.2.11", "enabled": false},
	}
	s.Resources["core-links"] = []Record{{"id": "actual-physical-link", "portAId": "p1", "portBId": "p5", "type": "layer3"}}
	return s
}

func projectionTarget(t *testing.T, in provision.Input, id string) provision.Target {
	t.Helper()
	for _, target := range in.Targets {
		if target.ID == id {
			return target
		}
	}
	t.Fatal("missing projected target", id)
	return provision.Target{}
}
func projectionSession(t *testing.T, in provision.Input, id string) provision.Session {
	t.Helper()
	for _, session := range in.Sessions {
		if session.ID == id {
			return session
		}
	}
	t.Fatal("missing projected session", id)
	return provision.Session{}
}

func TestProvisionInputPreservesDualStackLAGAndSessionIdentity(t *testing.T) {
	s := provisionProjectionFixture()
	in, err := provisionInput(&s, provision.Options{Kind: "nagios"})
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Targets) != 9 {
		t.Fatal("unexpected address projection count", len(in.Targets))
	}
	for _, id := range []string{"connection-c1-v4", "connection-c1-v6"} {
		target := projectionTarget(t, in, id)
		if target.MemberID != "m1" || target.ASN != 64501 || target.Name != "会员甲 & Network" || !target.Active || !target.Monitor || !target.Busy {
			t.Fatal("member identity or desired flags lost")
		}
		if !reflect.DeepEqual(target.SwitchIDs, []string{"sw1", "sw2"}) || !reflect.DeepEqual(target.RackIDs, []string{"rack-a", "rack-b"}) || !reflect.DeepEqual(target.LocationIDs, []string{"l1", "l2"}) {
			t.Fatal("LAG topology flattened")
		}
		if target.VLANID != "v1" || target.InfrastructureID != "fabric-a" {
			t.Fatal("fabric/VLAN association lost")
		}
	}
	v4 := projectionTarget(t, in, "connection-c1-v4")
	v6 := projectionTarget(t, in, "connection-c1-v6")
	if v4.CanPing || !v6.CanPing || v4.DNSName != "member4.example.net." || v6.DNSName != "member6.example.net." {
		t.Fatal("family-specific preferences lost")
	}
	ipv6Session := projectionSession(t, in, "actual-peer-v6")
	if ipv6Session.TargetID != v6.ID || ipv6Session.RouterID != "r1" || ipv6Session.RouterRole != "route-server" || !ipv6Session.Enabled || !ipv6Session.Monitor {
		t.Fatal("actual IPv6 BGP session identity lost")
	}
	other := projectionSession(t, in, "actual-peer-other-fabric")
	if other.TargetID != "connection-c2-v4" || other.RouterRole != "route-collector" || other.Monitor {
		t.Fatal("same address across fabrics matched wrong session")
	}
	if unmatched := projectionSession(t, in, "unmatched-peer"); unmatched.TargetID != "" || unmatched.Enabled {
		t.Fatal("invented a target for unmatched peer")
	}
	if len(in.CoreBundles) != 0 {
		t.Fatal("invented a core bundle from a physical core-link")
	}
}

func TestProvisionInputMonitoringChoicesDoNotInventHealth(t *testing.T) {
	s := provisionProjectionFixture()
	delete(s.Resources["ports"][0], "status")
	in, err := provisionInput(&s, provision.Options{Kind: "smokeping"})
	if err != nil {
		t.Fatal(err)
	}
	if !projectionTarget(t, in, "connection-c1-v4").Active {
		t.Fatal("unknown physical health overrode desired connection state")
	}
	sw := projectionTarget(t, in, "switch-sw2-v6")
	if sw.Monitor || !sw.Active || !sw.CanPing || sw.SwitchID != "sw2" || sw.VLANID != "" {
		t.Fatal("switch monitoring choice was conflated with health/ping/VLAN membership")
	}
	router := projectionTarget(t, in, "router-r1-v4")
	if router.Monitor || !router.Active || !router.CanPing || router.RouterID != "r1" || len(router.SwitchIDs) != 0 || len(router.RackIDs) != 0 || len(router.LocationIDs) != 0 {
		t.Fatal("router placement or health was invented")
	}
	s.Resources["connections"][0]["status"] = "pending"
	in, err = provisionInput(&s, provision.Options{Kind: "nagios"})
	if err != nil {
		t.Fatal(err)
	}
	if projectionTarget(t, in, "connection-c1-v4").Active {
		t.Fatal("pending connection marked eligible")
	}
	s.Resources["connections"][0]["status"] = "active"
	s.Resources["members"][0]["status"] = "suspended"
	in, err = provisionInput(&s, provision.Options{Kind: "nagios"})
	if err != nil {
		t.Fatal(err)
	}
	if projectionTarget(t, in, "connection-c1-v4").Active {
		t.Fatal("suspended member marked eligible")
	}
}

func TestProvisionInputCompatibleDefaultsAndNoMutation(t *testing.T) {
	for _, empty := range []any{nil, ""} {
		s := provisionProjectionFixture()
		for _, key := range []string{"canPing4", "canPing6", "monitorBusy"} {
			s.Resources["connections"][0][key] = empty
		}
		s.Resources["switches"][1]["monitor"] = empty
		s.Resources["routers"][0]["monitor"] = empty
		s.Resources["peers"][0]["monitor"] = empty
		before, _ := json.Marshal(s)
		options := provision.Options{Kind: "nagios", Families: []int{6, 4}}
		in, err := provisionInput(&s, options)
		if err != nil {
			t.Fatal(err)
		}
		target := projectionTarget(t, in, "connection-c1-v4")
		if !target.CanPing || target.Busy || !projectionTarget(t, in, "switch-sw2-v6").Monitor || !projectionTarget(t, in, "router-r1-v4").Monitor || !projectionSession(t, in, "actual-peer-v6").Monitor {
			t.Fatal("legacy empty/default flags changed")
		}
		after, _ := json.Marshal(s)
		if string(before) != string(after) || !reflect.DeepEqual(options.Families, []int{6, 4}) {
			t.Fatal("projection changed inventory or caller options")
		}
		for kind := range s.Resources {
			slices.Reverse(s.Resources[kind])
		}
		again, err := provisionInput(&s, options)
		if err != nil {
			t.Fatal(err)
		}
		first, _ := json.Marshal(in)
		second, _ := json.Marshal(again)
		if string(first) != string(second) {
			t.Fatal("inventory ordering changed projection")
		}
	}
}

func TestProvisionInputRejectsBrokenBindingsAndInvalidScopes(t *testing.T) {
	for _, change := range []func(*State){
		func(s *State) { s.Resources["connections"][0]["portIds"] = []any{"missing"} },
		func(s *State) { s.Resources["switches"][0]["infrastructureId"] = "fabric-b" },
		func(s *State) { s.Resources["routers"][0]["vlanId"] = "missing" },
		func(s *State) { s.Resources["connections"][0]["memberId"] = "missing" },
		func(s *State) { s.Resources["connections"][0]["ipv4"] = "2001:db8::10" },
		func(s *State) { s.Resources["connections"][0]["canPing4"] = "false" },
		func(s *State) { s.Resources["members"][0]["asn"] = float64(-1) },
		func(s *State) {
			s.Resources["connections"] = append(s.Resources["connections"], Record{"id": "duplicate-address", "memberId": "m1", "vlanId": "v1", "portId": "p3", "ipv4": "192.0.2.10"})
		},
	} {
		s := provisionProjectionFixture()
		change(&s)
		if _, err := provisionInput(&s, provision.Options{Kind: "nagios"}); err == nil {
			t.Fatal("broken inventory accepted")
		}
	}
	s := provisionProjectionFixture()
	for _, options := range []provision.Options{{Kind: "nagios", VLANID: "missing"}, {Kind: "nagios", InfrastructureID: "missing"}, {Kind: "nagios", VLANID: "v1", InfrastructureID: "fabric-b"}} {
		if _, err := provisionInput(&s, options); err == nil {
			t.Fatal("invalid or contradictory scope accepted")
		}
	}
	// All actual inventory targets survive projection; the generator owns scope.
	in, err := provisionInput(&s, provision.Options{Kind: "nagios", VLANID: "v1", InfrastructureID: "fabric-a", Families: []int{4}})
	if err != nil || len(in.Targets) != 9 {
		t.Fatal("projection silently discarded inventory before renderer scope", err)
	}
}

func TestProvisionInventoryDNSAndBooleanValidation(t *testing.T) {
	s := provisionProjectionFixture()
	for kind, keys := range map[string][]string{"connections": {"dnsName4", "dnsName6"}, "switches": {"dnsName"}, "routers": {"dnsName4", "dnsName6"}} {
		for _, key := range keys {
			if _, ok := Schemas[kind][key]; !ok {
				t.Fatal("inventory field not registered")
			}
			for _, bad := range []string{"a.example.\ninclude evil", "a.example.;", "a/example.net", "a\\example.net", "a..example.net", "_host.example.net", "中文.example.net", strings.Repeat("a", 64) + ".example.net", "192.0.2.1", "\nhost.example.net", "host.example.net\n"} {
				if err := validateProvisionInventory(&s, kind, Record{key: bad}); err == nil {
					t.Fatal("unsafe DNS metadata accepted", kind, key)
				}
			}
			if err := validateProvisionInventory(&s, kind, Record{key: "合法.example"}); err == nil {
				t.Fatal("non-punycode DNS name accepted")
			}
			if err := validateProvisionInventory(&s, kind, Record{key: "HOST-1.Example.NET."}); err != nil {
				t.Fatal(err)
			}
		}
	}
	for kind, keys := range map[string][]string{"connections": {"canPing4", "canPing6", "monitorBusy"}, "switches": {"monitor"}, "routers": {"monitor"}, "peers": {"monitor"}} {
		for _, key := range keys {
			if Schemas[kind][key].Type != "boolean" {
				t.Fatal("boolean field not registered")
			}
			for _, bad := range []any{"true", float64(1), []any{true}} {
				if err := validateProvisionInventory(&s, kind, Record{key: bad}); err == nil {
					t.Fatal("non-boolean preference accepted")
				}
			}
		}
	}
}

func TestProvisionInputPreservesLeadingHyphenInventoryIDs(t *testing.T) {
	s := provisionProjectionFixture()
	for kind, records := range s.Resources {
		for _, r := range records {
			for key, value := range r {
				if (key == "id" || strings.HasSuffix(key, "Id")) && !(kind == "routers" && key == "routerId") {
					if raw, ok := value.(string); ok && raw != "" {
						r[key] = "-" + raw
					}
				}
				if key == "portIds" {
					for i, raw := range value.([]any) {
						value.([]any)[i] = "-" + raw.(string)
					}
				}
			}
		}
	}
	in, err := provisionInput(&s, provision.Options{Kind: "nagios", VLANID: "-v1", InfrastructureID: "-fabric-a"})
	if err != nil {
		t.Fatal("valid base64url inventory ID rejected", err)
	}
	session := projectionSession(t, in, "-actual-peer-v6")
	if session.RouterID != "-r1" || session.TargetID != "connection--c1-v6" {
		t.Fatal("inventory IDs were rewritten")
	}
}

func TestProvisionInputReservedDNSOnlyTargetsAndNoASNMember(t *testing.T) {
	s := provisionProjectionFixture()
	s.Resources["members"] = append(s.Resources["members"], Record{"id": "partner", "name": "Associate without ASN", "type": "associate", "status": "active"})
	s.Resources["ip-addresses"] = []Record{{"id": "retained-address", "name": "Disabled family allocation", "vlanId": "v1", "memberId": "partner", "address": "2001:db8::20", "dnsName": "retained.example.net."}}
	options := provision.Options{Kind: "dns-arpa", VLANID: "v1", Families: []int{6}, DNS: provision.DNSOptions{Prefix: "2001:db8::/64", PrimaryNS: "ns1.example.net.", ResponsibleMailbox: "hostmaster.example.net.", NameServers: []string{"ns1.example.net."}, Serial: 1}}
	in, err := provisionInput(&s, options)
	if err != nil {
		t.Fatal(err)
	}
	target := projectionTarget(t, in, "reservation-retained-address-v6")
	if target.MemberID != "partner" || target.ASN != 0 || target.Name != "Associate without ASN" || target.Active || target.Monitor || target.CanPing || len(target.SwitchIDs) != 0 {
		t.Fatal("reserved DNS target fabricated ASN, topology or monitoring eligibility")
	}
	artifact, err := provision.Render(in)
	if err != nil || !strings.Contains(artifact.Content, "IN PTR retained.example.net.") {
		t.Fatal("default DNS coverage lost inactive reservation", err)
	}
	options.DNS.ActiveOnly = true
	in, err = provisionInput(&s, options)
	if err != nil {
		t.Fatal(err)
	}
	artifact, err = provision.Render(in)
	if err != nil || strings.Contains(artifact.Content, "retained.example.net") {
		t.Fatal("activeOnly did not exclude DNS-only reservation", err)
	}
	for _, kind := range []string{"nagios", "smokeping"} {
		in, err = provisionInput(&s, provision.Options{Kind: kind, VLANID: "v1", Families: []int{6}})
		if err != nil {
			t.Fatal(err)
		}
		artifact, err = provision.Render(in)
		if err != nil || strings.Contains(artifact.Content, "2001:db8::20") {
			t.Fatal("inactive retained address entered monitoring", kind, err)
		}
	}
}

func TestProvisionInputReservationDNSInheritanceAndConflict(t *testing.T) {
	s := provisionProjectionFixture()
	connection := s.Resources["connections"][0]
	delete(connection, "dnsName6")
	pool := Record{"id": "retained", "name": "Preserved reservation", "vlanId": "v1", "memberId": "m1", "address": "2001:0db8:0:0:0:0:0:10", "dnsName": "RETAINED.Example.NET"}
	s.Resources["ip-addresses"] = []Record{pool}
	in, err := provisionInput(&s, provision.Options{Kind: "nagios"})
	if err != nil {
		t.Fatal(err)
	}
	if projectionTarget(t, in, "connection-c1-v6").DNSName != "retained.example.net." {
		t.Fatal("same-owner connection failed to inherit reserved DNS")
	}
	for _, target := range in.Targets {
		if strings.HasPrefix(target.ID, "reservation-") {
			t.Fatal("bound reservation was duplicated as another target")
		}
	}
	if _, mutated := connection["dnsName6"]; mutated {
		t.Fatal("projection rewrote inventory while inheriting DNS")
	}
	connection["dnsName6"] = "other.example.net."
	if err := validateProvisionInventory(&s, "connections", connection); err == nil {
		t.Fatal("explicit connection/pool DNS conflict accepted")
	}
	if _, err := provisionInput(&s, provision.Options{Kind: "nagios"}); err == nil {
		t.Fatal("projection hid explicit DNS conflict")
	}
	connection["dnsName6"] = "retained.example.net."
	pool["memberId"] = "m2"
	if err := validateProvisionInventory(&s, "ip-addresses", pool); err == nil {
		t.Fatal("reservation hostname inherited across member ownership")
	}
	pool["memberId"] = ""
	if err := validateProvisionInventory(&s, "ip-addresses", pool); err == nil {
		t.Fatal("unowned free address retained a DNS hostname")
	}
}

func TestProvisionDNSOwnerTransitionGuardAllowsSameOwnerRenumbering(t *testing.T) {
	s := provisionProjectionFixture()
	old := Record{"memberId": "m1", "address": "2001:db8::20", "vlanId": "v1", "dnsName": "member.example.net."}
	for _, body := range []Record{{"memberId": "m2", "dnsName": "member.example.net."}, {"memberId": "", "dnsName": "member.example.net."}} {
		if err := guardProvisionDNSMutation(&s, "ip-addresses", "PUT", old, body); err == nil {
			t.Fatal("owner transition carried old DNS")
		}
	}
	renumbered := Record{"memberId": "m1", "address": "2001:db8::21", "vlanId": "v2", "dnsName": "member.example.net."}
	if err := guardProvisionDNSMutation(&s, "ip-addresses", "PUT", old, renumbered); err != nil {
		t.Fatal("same-owner renumbering unnecessarily blocked", err)
	}
	if err := guardProvisionDNSMutation(&s, "ip-addresses", "PUT", old, Record{"memberId": "m2"}); err != nil {
		t.Fatal("explicit DNS clearing blocked owner transfer", err)
	}
	if err := guardProvisionDNSMutation(&s, "connections", "PUT", Record{"memberId": "m1", "dnsName4": "old.example.net."}, Record{"memberId": "m2", "dnsName4": "old.example.net."}); err == nil {
		t.Fatal("connection carried former owner's DNS name")
	}
}
