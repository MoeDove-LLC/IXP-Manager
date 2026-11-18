package core

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func planFixture() (Bundle, PlanInventory) {
	b := testBundle()
	b.Enabled = true
	b.Links[0].Enabled = true
	b.Links[0].BFD = true
	b.SideA.MTU = 9000
	b.SideB.MTU = 9000
	cost, pref := uint32(30), uint32(120)
	b.Cost = &cost
	b.Preference = &pref
	speed := uint64(10000)
	b.Links[0].SideA.SpeedMbps = &speed
	b.Links[0].SideB.SpeedMbps = &speed
	in := PlanInventory{Switches: []SwitchInventory{{ID: "switch-a", Name: "交换机 A", InfrastructureID: "fabric-a", Active: true, ASN: 64500, LoopbackIPv4: "10.0.0.1", LoopbackIPv6: "2001:db8:ffff::1", LoopbackName: "lo0"}, {ID: "switch-b", Name: "switch B", InfrastructureID: "fabric-a", Active: true, ASN: 64501, LoopbackIPv4: "10.0.0.2", LoopbackIPv6: "2001:db8:ffff::2", LoopbackName: "lo0"}, {ID: "switch-c", Name: "inactive", InfrastructureID: "fabric-a", Active: false, LoopbackIPv4: "10.0.0.3"}, {ID: "switch-d", Name: "other fabric", InfrastructureID: "fabric-b", Active: true, LoopbackIPv4: "10.0.0.4"}}, Ports: []PortInventory{{ID: "port-a", SwitchID: "switch-a", Name: "Ethernet1", SpeedMbps: &speed}, {ID: "port-b", SwitchID: "switch-b", Name: "Ethernet2", SpeedMbps: &speed}}}
	return b, in
}

func TestSwitchPlanRejectsReservedRoutingASNs(t *testing.T) {
	for _, asn := range []uint32{0, 23456, 65535, 4294967295} {
		for _, side := range []int{0, 1} {
			b, in := planFixture()
			in.Switches[side].ASN = asn
			if _, err := BuildSwitchPlan("switch-a", []Bundle{b}, in); err == nil {
				t.Fatal("reserved source ASN emitted", asn, side)
			}
			b, in = planFixture()
			plan, err := BuildSwitchPlan("switch-a", []Bundle{b}, in)
			if err != nil {
				t.Fatal(err)
			}
			if side == 0 {
				plan.BGP.LocalASN = asn
			} else {
				plan.BGP.Neighbors[0].RemoteASN = asn
			}
			if ValidateSwitchPlan(plan) == nil {
				t.Fatal("reserved ASN accepted in supplied plan", asn, side)
			}
		}
	}
}

func TestSwitchPlanECMPDualStackUsesActualEndpoints(t *testing.T) {
	b, in := planFixture()
	plan, err := BuildSwitchPlan("switch-a", []Bundle{b}, in)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.InventoryOnly || plan.NativeValidated || !plan.RequiresTargetAdapter || len(plan.Interfaces) != 2 || len(plan.PhysicalLinks) != 1 || plan.BGP == nil || len(plan.BGP.Neighbors) != 2 {
		t.Fatalf("incomplete plan: %+v", plan)
	}
	if plan.Interfaces[0].Name != "Ethernet1" || plan.Interfaces[0].IPv4 != "192.0.2.0/31" || plan.Interfaces[0].IPv6 != "2001:db8::/127" || !plan.Interfaces[0].BFD {
		t.Fatalf("bad interface: %+v", plan.Interfaces[0])
	}
	if plan.BGP.LocalASN != 64500 || plan.BGP.RouterID != "10.0.0.1" || plan.BGP.Neighbors[0].RemoteASN != 64501 || plan.BGP.Neighbors[0].RemoteAddress != "192.0.2.1" || *plan.BGP.Neighbors[0].Preference != 120 {
		t.Fatalf("bad BGP: %+v", plan.BGP)
	}
	if !reflect.DeepEqual(plan.Floodlist, []string{"10.0.0.2"}) || !reflect.DeepEqual(plan.FloodlistIPv6, []string{"2001:db8:ffff::2"}) {
		t.Fatalf("inactive or foreign fabric leaked into floodlist: %+v", plan)
	}
	reverse, err := BuildSwitchPlan("switch-b", []Bundle{b}, in)
	if err != nil {
		t.Fatal(err)
	}
	if reverse.Interfaces[0].Name != "Ethernet2" || reverse.Interfaces[0].IPv4 != "192.0.2.1/31" || reverse.BGP.Neighbors[0].RemoteAddress != "192.0.2.0" || reverse.BGP.Neighbors[1].RemoteAddress != "2001:db8::" {
		t.Fatal("side B addresses not reversed correctly")
	}
	// JSON quoting remains data. No shell or CLI interpolation is performed.
	in.Ports[0].Name = `Ethernet "quoted"; $(arbitrary)`
	quoted, err := BuildSwitchPlan("switch-a", []Bundle{b}, in)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(quoted)
	var decoded SwitchPlan
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded.PhysicalLinks[0].Name != in.Ports[0].Name {
		t.Fatal("interface name not preserved as JSON data", err)
	}
}

func TestSwitchPlanL3LAGOneLogicalInterfaceAndBundleBFD(t *testing.T) {
	b, in := planFixture()
	group := uint32(0)
	b.Type = L3LAG
	b.BFD = true
	b.IPv4Subnet = b.Links[0].IPv4Subnet
	b.IPv6Subnet = b.Links[0].IPv6Subnet
	b.SideA.ChannelGroup = &group
	b.SideB.ChannelGroup = &group
	b.SideA.InterfaceName = "PortChannel0"
	b.SideB.InterfaceName = "PortChannel0"
	b.Links[0].BFD = false
	b.Links[0].IPv4Subnet = ""
	b.Links[0].IPv6Subnet = ""
	second := b.Links[0]
	second.ID = "second-link"
	second.Enabled = false
	second.SideA.ID = "second-a"
	second.SideA.PortID = "port-a2"
	second.SideB.ID = "second-b"
	second.SideB.PortID = "port-b2"
	b.Links = append(b.Links, second)
	in.Ports = append(in.Ports, PortInventory{ID: "port-a2", SwitchID: "switch-a", Name: "Ethernet3"}, PortInventory{ID: "port-b2", SwitchID: "switch-b", Name: "Ethernet4"})
	plan, err := BuildSwitchPlan("switch-a", []Bundle{b}, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Interfaces) != 2 || len(plan.PhysicalLinks) != 2 || len(plan.BGP.Neighbors) != 2 {
		t.Fatal("LAG addresses duplicated on physical members")
	}
	lag := plan.Interfaces[0]
	if lag.Kind != "aggregate-layer3" || lag.Name != "PortChannel0" || !lag.BFD || lag.Shutdown || len(lag.MemberPortIDs) != 2 {
		t.Fatalf("bad LAG: %+v", lag)
	}
	if !plan.BGP.Neighbors[0].BFD || !plan.BGP.Neighbors[1].BFD {
		t.Fatal("legacy L3-LAG BFD bug retained")
	}
	if !plan.PhysicalLinks[1].Shutdown {
		t.Fatal("disabled member not shut down")
	}
	in.Switches[0].Active = false
	disabled, err := BuildSwitchPlan("switch-a", []Bundle{b}, in)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range disabled.Interfaces {
		if !i.Shutdown {
			t.Fatal("inactive switch enabled interface")
		}
	}
	for _, n := range disabled.BGP.Neighbors {
		if !n.Shutdown {
			t.Fatal("inactive switch enabled neighbor")
		}
	}
}

func TestSwitchPlanL2WithoutInventedBGP(t *testing.T) {
	b, in := planFixture()
	b.Type = L2LAG
	b.STP = true
	b.Links[0].BFD = false
	b.Links[0].IPv4Subnet = ""
	b.Links[0].IPv6Subnet = ""
	group := uint32(12)
	b.SideA.ChannelGroup = &group
	b.SideB.ChannelGroup = &group
	b.SideA.InterfaceName = "ae12"
	b.SideB.InterfaceName = "ae12"
	for i := range in.Switches {
		in.Switches[i].ASN = 0
		in.Switches[i].LoopbackIPv4 = ""
		in.Switches[i].LoopbackIPv6 = ""
		in.Switches[i].LoopbackName = ""
	}
	plan, err := BuildSwitchPlan("switch-a", []Bundle{b}, in)
	if err != nil {
		t.Fatal(err)
	}
	if plan.BGP != nil || len(plan.Interfaces) != 1 || !plan.Interfaces[0].STP || plan.Interfaces[0].IPv4 != "" {
		t.Fatal("L2 plan invented BGP")
	}
}

func TestSwitchPlanRejectsIncompleteRoutedInventory(t *testing.T) {
	for name, change := range map[string]func(*Bundle, *PlanInventory){"missing-local-asn": func(_ *Bundle, in *PlanInventory) { in.Switches[0].ASN = 0 }, "missing-peer-asn": func(_ *Bundle, in *PlanInventory) { in.Switches[1].ASN = 0 }, "v6-not-router-id": func(_ *Bundle, in *PlanInventory) { in.Switches[0].LoopbackIPv4 = "" }, "missing-port": func(_ *Bundle, in *PlanInventory) { in.Ports = in.Ports[:1] }, "unknown-v6-mtu": func(b *Bundle, _ *PlanInventory) { b.SideA.MTU = 0 }, "port-switch-mismatch": func(_ *Bundle, in *PlanInventory) { in.Ports[0].SwitchID = "switch-b" }, "loopback-collision": func(_ *Bundle, in *PlanInventory) { in.Switches[1].LoopbackIPv4 = in.Switches[0].LoopbackIPv4 }, "loopback-overlap": func(_ *Bundle, in *PlanInventory) { in.Switches[0].LoopbackIPv4 = "192.0.2.0" }} {
		t.Run(name, func(t *testing.T) {
			b, in := planFixture()
			change(&b, &in)
			if p, err := BuildSwitchPlan("switch-a", []Bundle{b}, in); err == nil || p.Kind != "" {
				t.Fatalf("incomplete inventory produced plan: %+v, %v", p, err)
			}
		})
	}
}

func TestValidateSwitchPlanRejectsMissingOrContradictoryStructure(t *testing.T) {
	b, in := planFixture()
	good, err := BuildSwitchPlan("switch-a", []Bundle{b}, in)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(good)
	for name, change := range map[string]func(*SwitchPlan){"native-claim": func(p *SwitchPlan) { p.NativeValidated = true }, "missing-neighbor": func(p *SwitchPlan) { p.BGP.Neighbors = p.BGP.Neighbors[:1] }, "wrong-peer-asn": func(p *SwitchPlan) { p.BGP.Neighbors[0].RemoteASN = 0 }, "wrong-peer-address": func(p *SwitchPlan) { p.BGP.Neighbors[0].RemoteAddress = "192.0.2.9" }, "path-has-command": func(p *SwitchPlan) { p.Interfaces[0].Name = "x\nshutdown" }, "orphan-physical": func(p *SwitchPlan) { p.Interfaces = p.Interfaces[1:] }, "missing-physical": func(p *SwitchPlan) { p.PhysicalLinks = nil }, "bfd-mismatch": func(p *SwitchPlan) { p.BGP.Neighbors[0].BFD = false }, "fake-router-id": func(p *SwitchPlan) { p.BGP.RouterID = "10.0.0.99" }, "self-flood": func(p *SwitchPlan) { p.Floodlist = append(p.Floodlist, "10.0.0.1") }, "wrong-local-address": func(p *SwitchPlan) { p.Interfaces[0].IPv4 = "192.0.2.2/30" }, "unsupported-kind": func(p *SwitchPlan) { p.Interfaces[0].Kind = "shell" }, "unbounded-note": func(p *SwitchPlan) { p.Notices[0].Message = strings.Repeat("x", 4097) }} {
		t.Run(name, func(t *testing.T) {
			var p SwitchPlan
			_ = json.Unmarshal(raw, &p)
			change(&p)
			if err := ValidateSwitchPlan(p); err == nil {
				t.Fatal("malformed plan accepted")
			}
		})
	}
}
