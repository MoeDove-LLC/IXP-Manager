package core

import (
	"testing"
)

func testBundle() Bundle {
	return Bundle{ID: "bundle", Name: "Core link", GraphTitle: "Core", Type: ECMP, SideA: Side{SwitchID: "switch-a"}, SideB: Side{SwitchID: "switch-b"}, Links: []Link{{ID: "link", IPv4Subnet: "192.0.2.0/31", IPv6Subnet: "2001:db8::/127", SideA: Endpoint{ID: "endpoint-a", PortID: "port-a", Duplex: "full"}, SideB: Endpoint{ID: "endpoint-b", PortID: "port-b", Duplex: "full"}}}}
}

func TestAddressEndpointsPreserveConventionsWithoutOverflow(t *testing.T) {
	for _, tt := range []struct {
		prefix string
		af     int
		a, b   string
	}{{"192.0.2.0/31", 4, "192.0.2.0", "192.0.2.1"}, {"192.0.2.4/30", 4, "192.0.2.5", "192.0.2.6"}, {"2001:db8::/127", 6, "2001:db8::", "2001:db8::1"}, {"2001:db8::/126", 6, "2001:db8::1", "2001:db8::2"}} {
		got, err := AddressEndpoints(tt.prefix, tt.af)
		if err != nil || got.SideA != tt.a || got.SideB != tt.b {
			t.Fatalf("%s: %+v %v", tt.prefix, got, err)
		}
	}
	for _, tt := range []struct {
		prefix string
		af     int
	}{{"192.0.2.1/31", 4}, {"192.0.2.0/32", 4}, {"2001:db8::/128", 6}, {"224.0.0.0/31", 4}, {"ff00::/127", 6}, {"::ffff:192.0.2.0/127", 6}, {"192.0.2.0/31", 6}, {"2001:0db8::/127", 6}} {
		if got, err := AddressEndpoints(tt.prefix, tt.af); err == nil {
			t.Fatalf("accepted unsafe subnet %s: %+v", tt.prefix, got)
		}
	}
}

func TestValidateCoreTopologyBoundaries(t *testing.T) {
	if err := Validate(testBundle()); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Bundle){"same-switch": func(b *Bundle) { b.SideB.SwitchID = b.SideA.SwitchID }, "reused-port": func(b *Bundle) { b.Links[0].SideB.PortID = b.Links[0].SideA.PortID }, "reused-id": func(b *Bundle) { b.Links[0].SideB.ID = b.Links[0].ID }, "no-link": func(b *Bundle) { b.Links = nil }, "wrong-bfd-layer": func(b *Bundle) { b.BFD = true }, "wrong-stp-layer": func(b *Bundle) { b.STP = true }, "missing-subnet": func(b *Bundle) { b.Links[0].IPv4Subnet = ""; b.Links[0].IPv6Subnet = "" }, "bad-mtu": func(b *Bundle) { b.SideA.MTU = 64001 }, "missing-lag-group": func(b *Bundle) { b.Type = L2LAG; b.Links[0].IPv4Subnet = ""; b.Links[0].IPv6Subnet = "" }, "unknown-duplex": func(b *Bundle) { b.Links[0].SideA.Duplex = "auto" }, "fake-status": func(b *Bundle) { b.Links[0].SideA.Status = "up" }, "control-text": func(b *Bundle) { b.Name = "core\x00secret" }} {
		t.Run(name, func(t *testing.T) {
			b := testBundle()
			mutate(&b)
			if err := Validate(b); err == nil {
				t.Fatal("invalid topology accepted")
			}
		})
	}
	group := uint32(0)
	b := testBundle()
	b.Type = L3LAG
	b.BFD = true
	b.IPv4Subnet = b.Links[0].IPv4Subnet
	b.IPv6Subnet = b.Links[0].IPv6Subnet
	b.Links[0].IPv4Subnet = ""
	b.Links[0].IPv6Subnet = ""
	b.SideA.ChannelGroup = &group
	b.SideB.ChannelGroup = &group
	b.SideA.InterfaceName = "Port-Channel0"
	b.SideB.InterfaceName = "Port-Channel0"
	b.SideA.MTU = 64000
	if err := Validate(b); err != nil {
		t.Fatal("valid zero channelGroup rejected", err)
	}
	d := Addresses(b)
	if len(d) != 1 || !d[0].BFD || d[0].Enabled {
		t.Fatalf("desired BFD/disabled state lost: %+v", d)
	}
}
