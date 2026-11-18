package core

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// These are projections of managed inventory, never SNMP health or secrets.
type SwitchInventory struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	InfrastructureID string `json:"infrastructureId"`
	Active           bool   `json:"active"`
	ASN              uint32 `json:"asn"`
	LoopbackIPv4     string `json:"loopbackIpv4,omitempty"`
	LoopbackIPv6     string `json:"loopbackIpv6,omitempty"`
	LoopbackName     string `json:"loopbackName,omitempty"`
}
type PortInventory struct {
	ID        string  `json:"id"`
	SwitchID  string  `json:"switchId"`
	Name      string  `json:"name"`
	SpeedMbps *uint64 `json:"speedMbps,omitempty"`
}
type PlanInventory struct {
	Switches []SwitchInventory `json:"switches"`
	Ports    []PortInventory   `json:"ports"`
}

type InterfacePlan struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Kind          string   `json:"kind"`
	BundleID      string   `json:"bundleId,omitempty"`
	LinkID        string   `json:"linkId,omitempty"`
	PortID        string   `json:"portId,omitempty"`
	MemberPortIDs []string `json:"memberPortIds,omitempty"`
	ChannelGroup  *uint32  `json:"channelGroup,omitempty"`
	MTU           uint32   `json:"mtu"`
	Trunk         bool     `json:"trunk"`
	FastLACP      bool     `json:"fastLacp"`
	LAGFraming    bool     `json:"lagFraming"`
	STP           bool     `json:"stp"`
	Cost          *uint32  `json:"cost,omitempty"`
	IPv4          string   `json:"ipv4,omitempty"`
	IPv6          string   `json:"ipv6,omitempty"`
	BFD           bool     `json:"bfd"`
	Shutdown      bool     `json:"shutdown"`
}
type PhysicalLinkPlan struct {
	BundleID           string  `json:"bundleId"`
	LinkID             string  `json:"linkId"`
	EndpointID         string  `json:"endpointId"`
	PortID             string  `json:"portId"`
	Name               string  `json:"name"`
	PeerSwitchID       string  `json:"peerSwitchId"`
	PeerPortID         string  `json:"peerPortId"`
	PeerName           string  `json:"peerName"`
	AggregateName      string  `json:"aggregateName,omitempty"`
	SpeedMbps          *uint64 `json:"speedMbps,omitempty"`
	InventorySpeedMbps *uint64 `json:"inventorySpeedMbps,omitempty"`
	Duplex             string  `json:"duplex"`
	Autoneg            bool    `json:"autoneg"`
	DesiredStatus      string  `json:"desiredStatus,omitempty"`
	RateLimitMbps      *uint64 `json:"rateLimitMbps,omitempty"`
	Notes              string  `json:"notes,omitempty"`
	Shutdown           bool    `json:"shutdown"`
}
type NeighborPlan struct {
	BundleID      string  `json:"bundleId"`
	LinkID        string  `json:"linkId,omitempty"`
	PeerSwitchID  string  `json:"peerSwitchId"`
	Description   string  `json:"description"`
	InterfaceName string  `json:"interfaceName"`
	Family        string  `json:"family"`
	LocalAddress  string  `json:"localAddress"`
	RemoteAddress string  `json:"remoteAddress"`
	RemoteASN     uint32  `json:"remoteAsn"`
	Cost          *uint32 `json:"cost,omitempty"`
	Preference    *uint32 `json:"preference,omitempty"`
	BFD           bool    `json:"bfd"`
	Shutdown      bool    `json:"shutdown"`
}
type BGPPlan struct {
	LocalASN  uint32         `json:"localAsn"`
	RouterID  string         `json:"routerId"`
	Neighbors []NeighborPlan `json:"neighbors"`
}
type SwitchPlan struct {
	Version               int                `json:"version"`
	Kind                  string             `json:"kind"`
	SwitchID              string             `json:"switchId"`
	Name                  string             `json:"name"`
	InfrastructureID      string             `json:"infrastructureId"`
	Active                bool               `json:"active"`
	InventoryOnly         bool               `json:"inventoryOnly"`
	NativeValidated       bool               `json:"nativeValidated"`
	RequiresTargetAdapter bool               `json:"requiresTargetAdapter"`
	Interfaces            []InterfacePlan    `json:"interfaces"`
	PhysicalLinks         []PhysicalLinkPlan `json:"physicalLinks"`
	BGP                   *BGPPlan           `json:"bgp,omitempty"`
	Floodlist             []string           `json:"floodlist"`
	FloodlistIPv6         []string           `json:"floodlistIpv6"`
	Notices               []Warning          `json:"notices"`
}

func planAddress(value string, family int) error {
	if value == "" {
		return nil
	}
	a, err := netip.ParseAddr(value)
	if err != nil || a.String() != value || a.Is4In6() || a.Zone() != "" || !a.IsGlobalUnicast() || (family == 4) != a.Is4() {
		return fmt.Errorf("switch loopback must be a canonical unicast IPv%d address", family)
	}
	return nil
}

// ValidASN matches the routing configuration boundary: AS_TRANS and reserved
// boundary values are inventory data, never usable local/remote AS numbers.
func ValidASN(asn uint32) bool {
	return asn != 0 && asn != 23456 && asn != 65535 && asn != 4294967295
}
func planReady(sw SwitchInventory) bool {
	return ValidASN(sw.ASN) && sw.LoopbackIPv4 != "" && strings.TrimSpace(sw.LoopbackName) != ""
}

// BuildSwitchPlan returns typed JSON input for a reviewed target adapter. It
// deliberately produces no native CLI, executable template, or live operation.
// Missing data needed for routed configuration is an error, including on a
// disabled link: a plan must not guess an ASN, router ID, or interface name.
func BuildSwitchPlan(switchID string, bundles []Bundle, inventory PlanInventory) (SwitchPlan, error) {
	empty := SwitchPlan{}
	if len(bundles) > MaxBundles || len(inventory.Switches) > 10000 || len(inventory.Ports) > 100000 {
		return empty, fmt.Errorf("switch plan inventory limit exceeded")
	}
	switches := map[string]SwitchInventory{}
	ports := map[string]PortInventory{}
	for _, sw := range inventory.Switches {
		if !identifier.MatchString(sw.ID) || switches[sw.ID].ID != "" || strings.TrimSpace(sw.Name) == "" || !safeText(sw.Name, 255, false) || !safeText(sw.LoopbackName, 255, false) || sw.ASN == 4294967295 {
			return empty, fmt.Errorf("invalid or duplicate switch inventory")
		}
		if sw.InfrastructureID != "" && !identifier.MatchString(sw.InfrastructureID) {
			return empty, fmt.Errorf("invalid infrastructure ID")
		}
		if err := planAddress(sw.LoopbackIPv4, 4); err != nil {
			return empty, err
		}
		if err := planAddress(sw.LoopbackIPv6, 6); err != nil {
			return empty, err
		}
		switches[sw.ID] = sw
	}
	target, ok := switches[switchID]
	if !ok {
		return empty, fmt.Errorf("target switch does not exist")
	}
	for _, p := range inventory.Ports {
		if !identifier.MatchString(p.ID) || ports[p.ID].ID != "" || switches[p.SwitchID].ID == "" || strings.TrimSpace(p.Name) == "" || !safeText(p.Name, 255, false) || (p.SpeedMbps != nil && (*p.SpeedMbps == 0 || *p.SpeedMbps > 10000000)) {
			return empty, fmt.Errorf("invalid or duplicate physical port inventory")
		}
		ports[p.ID] = p
	}
	plan := SwitchPlan{Version: 1, Kind: "switch-core", SwitchID: target.ID, Name: target.Name, InfrastructureID: target.InfrastructureID, Active: target.Active, InventoryOnly: true, RequiresTargetAdapter: true, Interfaces: []InterfacePlan{}, PhysicalLinks: []PhysicalLinkPlan{}, Floodlist: []string{}, FloodlistIPv6: []string{}, Notices: []Warning{{Code: "target-adapter-required", Message: "Typed desired JSON only; validate a target-specific adapter and native configuration before installation. No LACP, BGP or BFD health is asserted."}}}
	ordered := append([]Bundle(nil), bundles...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	usedPorts := map[string]bool{}
	usedIDs := map[string]bool{}
	interfaceNames := map[string]bool{}
	localSubnets := []netip.Prefix{}
	totalLinks := 0
	addInterface := func(in InterfacePlan) error {
		if strings.TrimSpace(in.Name) == "" || interfaceNames[in.Name] {
			return fmt.Errorf("target interface names must be distinct and nonempty")
		}
		interfaceNames[in.Name] = true
		plan.Interfaces = append(plan.Interfaces, in)
		return nil
	}
	for _, b := range ordered {
		if err := Validate(b); err != nil {
			return empty, err
		}
		if usedIDs[b.ID] {
			return empty, fmt.Errorf("duplicate core bundle ID")
		}
		usedIDs[b.ID] = true
		totalLinks += len(b.Links)
		if totalLinks > MaxTotalLinks {
			return empty, fmt.Errorf("total core link limit exceeded")
		}
		for _, l := range b.Links {
			for _, id := range []string{l.ID, l.SideA.ID, l.SideB.ID} {
				if usedIDs[id] {
					return empty, fmt.Errorf("duplicate core object ID")
				}
				usedIDs[id] = true
			}
			for _, ep := range []Endpoint{l.SideA, l.SideB} {
				if usedPorts[ep.PortID] {
					return empty, fmt.Errorf("physical port reused across core links")
				}
				usedPorts[ep.PortID] = true
			}
		}
		if b.SideA.SwitchID != switchID && b.SideB.SwitchID != switchID {
			continue
		}
		local, remote := b.SideA, b.SideB
		localIsA := true
		if b.SideB.SwitchID == switchID {
			local, remote = b.SideB, b.SideA
			localIsA = false
		}
		peer, exists := switches[remote.SwitchID]
		if !exists {
			return empty, fmt.Errorf("peer switch missing")
		}
		if b.Type == L2LAG && target.InfrastructureID != peer.InfrastructureID {
			return empty, fmt.Errorf("L2 core bundle cannot cross infrastructures")
		}
		if b.Type != L2LAG {
			if !planReady(target) || !planReady(peer) {
				return empty, fmt.Errorf("routed core plans require each switch ASN, IPv4 router ID and loopback name")
			}
			if plan.BGP == nil {
				plan.BGP = &BGPPlan{LocalASN: target.ASN, RouterID: target.LoopbackIPv4, Neighbors: []NeighborPlan{}}
			}
		}
		links := append([]Link(nil), b.Links...)
		sort.Slice(links, func(i, j int) bool { return links[i].ID < links[j].ID })
		memberIDs := []string{}
		anyEnabled := false
		linkNames := map[string]string{}
		for _, l := range links {
			ep, rep := l.SideA, l.SideB
			if !localIsA {
				ep, rep = l.SideB, l.SideA
			}
			p, rp := ports[ep.PortID], ports[rep.PortID]
			if p.ID == "" || rp.ID == "" || p.SwitchID != target.ID || rp.SwitchID != peer.ID {
				return empty, fmt.Errorf("core endpoint port missing or on the wrong switch")
			}
			shutdown := !target.Active || !b.Enabled || !l.Enabled
			physical := PhysicalLinkPlan{BundleID: b.ID, LinkID: l.ID, EndpointID: ep.ID, PortID: p.ID, Name: p.Name, PeerSwitchID: peer.ID, PeerPortID: rp.ID, PeerName: rp.Name, SpeedMbps: ep.SpeedMbps, InventorySpeedMbps: p.SpeedMbps, Duplex: ep.Duplex, Autoneg: ep.Autoneg, DesiredStatus: ep.Status, RateLimitMbps: ep.RateLimitMbps, Notes: ep.Notes, Shutdown: shutdown}
			if b.Type != ECMP {
				physical.AggregateName = local.InterfaceName
			}
			plan.PhysicalLinks = append(plan.PhysicalLinks, physical)
			memberIDs = append(memberIDs, p.ID)
			linkNames[l.ID] = p.Name
			anyEnabled = anyEnabled || l.Enabled
			if ep.SpeedMbps != nil && p.SpeedMbps != nil && *ep.SpeedMbps != *p.SpeedMbps {
				plan.Notices = append(plan.Notices, Warning{Code: "configured-speed-mismatch", Message: "Configured endpoint speed differs from physical inventory; the target adapter must review both values."})
			}
		}
		for _, d := range Addresses(b) {
			name := local.InterfaceName
			kind := "aggregate-layer3"
			portID := ""
			if b.Type == ECMP {
				name = linkNames[d.LinkID]
				kind = "routed-physical"
				for _, l := range links {
					if l.ID == d.LinkID {
						if localIsA {
							portID = l.SideA.PortID
						} else {
							portID = l.SideB.PortID
						}
					}
				}
			}
			in := InterfacePlan{Name: name, Description: b.Name, Kind: kind, BundleID: b.ID, LinkID: d.LinkID, PortID: portID, MTU: local.MTU, Trunk: local.Trunk, FastLACP: local.FastLACP, LAGFraming: local.LAGFraming, BFD: d.BFD, Shutdown: !target.Active || !d.Enabled}
			if b.Type != ECMP {
				in.ChannelGroup = local.ChannelGroup
				in.MemberPortIDs = append([]string(nil), memberIDs...)
			}
			for _, af := range []struct {
				name string
				ap   *AddressPair
			}{{"ipv4", d.IPv4}, {"ipv6", d.IPv6}} {
				if af.ap == nil {
					continue
				}
				p, _ := netip.ParsePrefix(af.ap.Subnet)
				for _, prior := range localSubnets {
					if p.Overlaps(prior) {
						return empty, fmt.Errorf("routed core subnets overlap on target switch")
					}
				}
				localSubnets = append(localSubnets, p)
				addr, other := af.ap.SideA, af.ap.SideB
				if !localIsA {
					addr, other = other, addr
				}
				cidr := fmt.Sprintf("%s/%d", addr, p.Bits())
				if af.name == "ipv4" {
					in.IPv4 = cidr
				} else {
					in.IPv6 = cidr
					if local.MTU < 1280 {
						return empty, fmt.Errorf("IPv6 switch plans require an explicitly reviewed MTU of at least 1280")
					}
				}
				plan.BGP.Neighbors = append(plan.BGP.Neighbors, NeighborPlan{BundleID: b.ID, LinkID: d.LinkID, PeerSwitchID: peer.ID, Description: peer.Name, InterfaceName: name, Family: af.name, LocalAddress: addr, RemoteAddress: other, RemoteASN: peer.ASN, Cost: b.Cost, Preference: b.Preference, BFD: d.BFD, Shutdown: in.Shutdown || !peer.Active})
			}
			if err := addInterface(in); err != nil {
				return empty, err
			}
		}
		if b.Type == L2LAG {
			if err := addInterface(InterfacePlan{Name: local.InterfaceName, Description: b.Name, Kind: "aggregate-layer2", BundleID: b.ID, MemberPortIDs: memberIDs, ChannelGroup: local.ChannelGroup, MTU: local.MTU, Trunk: local.Trunk, FastLACP: local.FastLACP, LAGFraming: local.LAGFraming, STP: b.STP, Cost: b.Cost, Shutdown: !target.Active || !b.Enabled || !anyEnabled}); err != nil {
				return empty, err
			}
		}
	}
	if target.LoopbackIPv4 != "" || target.LoopbackIPv6 != "" {
		if strings.TrimSpace(target.LoopbackName) == "" {
			return empty, fmt.Errorf("loopback addresses require an interface name")
		}
		in := InterfacePlan{Name: target.LoopbackName, Kind: "loopback", Description: "Loopback interface", Shutdown: !target.Active}
		if target.LoopbackIPv4 != "" {
			in.IPv4 = target.LoopbackIPv4 + "/32"
		}
		if target.LoopbackIPv6 != "" {
			in.IPv6 = target.LoopbackIPv6 + "/128"
		}
		if err := addInterface(in); err != nil {
			return empty, err
		}
	}
	seenFlood := map[string]bool{}
	for _, sw := range inventory.Switches {
		if sw.ID == target.ID || !sw.Active || sw.InfrastructureID != target.InfrastructureID {
			continue
		}
		for _, af := range []struct {
			addr   string
			family int
		}{{sw.LoopbackIPv4, 4}, {sw.LoopbackIPv6, 6}} {
			if af.addr == "" {
				continue
			}
			if af.addr == target.LoopbackIPv4 || af.addr == target.LoopbackIPv6 || seenFlood[af.addr] {
				return empty, fmt.Errorf("duplicate switch loopback in infrastructure")
			}
			seenFlood[af.addr] = true
			if af.family == 4 {
				plan.Floodlist = append(plan.Floodlist, af.addr)
			} else {
				plan.FloodlistIPv6 = append(plan.FloodlistIPv6, af.addr)
			}
		}
	}
	sort.Strings(plan.Floodlist)
	sort.Strings(plan.FloodlistIPv6)
	if err := ValidateSwitchPlan(plan); err != nil {
		return empty, err
	}
	return plan, nil
}

// ValidateSwitchPlan checks bounded typed structure and internal consistency.
// It cannot attest that the described inventory exists on a device or that a
// target adapter accepts it. Those limitations are mandatory metadata.
func ValidateSwitchPlan(p SwitchPlan) error {
	if p.Version != 1 || p.Kind != "switch-core" || !p.InventoryOnly || p.NativeValidated || !p.RequiresTargetAdapter || !identifier.MatchString(p.SwitchID) || strings.TrimSpace(p.Name) == "" || !safeText(p.Name, 255, false) || (p.InfrastructureID != "" && !identifier.MatchString(p.InfrastructureID)) {
		return fmt.Errorf("invalid switch plan identity or validation claims")
	}
	if len(p.Interfaces) > MaxTotalLinks+MaxBundles+1 || len(p.PhysicalLinks) > MaxTotalLinks || len(p.Floodlist) > 10000 || len(p.FloodlistIPv6) > 10000 || len(p.Notices) > MaxTotalLinks+1 {
		return fmt.Errorf("switch plan limit exceeded")
	}
	for _, n := range p.Notices {
		if !identifier.MatchString(n.Code) || !safeText(n.Message, 4096, false) {
			return fmt.Errorf("invalid switch plan notice")
		}
	}
	physical := map[string]PhysicalLinkPlan{}
	physicalNames := map[string]bool{}
	objectIDs := map[string]bool{}
	for _, link := range p.PhysicalLinks {
		for _, id := range []string{link.BundleID, link.LinkID, link.EndpointID, link.PortID, link.PeerSwitchID, link.PeerPortID} {
			if !identifier.MatchString(id) {
				return fmt.Errorf("invalid physical link identity")
			}
		}
		if link.PeerSwitchID == p.SwitchID || link.PortID == link.PeerPortID || physical[link.PortID].PortID != "" || physicalNames[link.Name] || objectIDs[link.EndpointID] || objectIDs[link.LinkID] {
			return fmt.Errorf("physical links must have distinct endpoint, link, port and interface identities")
		}
		objectIDs[link.EndpointID] = true
		objectIDs[link.LinkID] = true
		physicalNames[link.Name] = true
		physical[link.PortID] = link
		if strings.TrimSpace(link.Name) == "" || strings.TrimSpace(link.PeerName) == "" || !safeText(link.Name, 255, false) || !safeText(link.PeerName, 255, false) || !safeText(link.AggregateName, 255, false) || !safeText(link.Notes, 4096, true) || (!p.Active && !link.Shutdown) {
			return fmt.Errorf("invalid physical link attributes")
		}
		if link.Duplex != "full" && link.Duplex != "half" {
			return fmt.Errorf("invalid physical duplex")
		}
		switch link.DesiredStatus {
		case "", "connected", "disabled", "not-connected", "awaiting-cross-connect", "quarantine":
		default:
			return fmt.Errorf("invalid desired physical status")
		}
		for _, speed := range []*uint64{link.SpeedMbps, link.InventorySpeedMbps} {
			if speed != nil && (*speed == 0 || *speed > 10000000) {
				return fmt.Errorf("invalid physical link speed")
			}
		}
		if link.RateLimitMbps != nil && *link.RateLimitMbps > 10000000 {
			return fmt.Errorf("invalid physical rate limit")
		}
	}
	type routed struct {
		in                  InterfacePlan
		family              string
		local, remote, peer string
	}
	routes := map[string]routed{}
	usedPhysical := map[string]bool{}
	names := map[string]bool{}
	subnets := []netip.Prefix{}
	loopback4, loopback6 := "", ""
	groups := map[uint32]bool{}
	for _, in := range p.Interfaces {
		if in.Kind != "routed-physical" && physicalNames[in.Name] {
			return fmt.Errorf("aggregate or loopback name collides with a physical interface")
		}
		if strings.TrimSpace(in.Name) == "" || !safeText(in.Name, 255, false) || !safeText(in.Description, 4096, true) || names[in.Name] || in.MTU > 64000 || (!p.Active && !in.Shutdown) {
			return fmt.Errorf("invalid or duplicate planned interface")
		}
		names[in.Name] = true
		if in.Kind == "loopback" {
			if loopback4 != "" || loopback6 != "" || in.BundleID != "" || in.LinkID != "" || in.PortID != "" || len(in.MemberPortIDs) > 0 || in.ChannelGroup != nil || in.BFD || in.STP || in.Trunk || in.FastLACP || in.LAGFraming || (in.IPv4 == "" && in.IPv6 == "") {
				return fmt.Errorf("invalid loopback interface")
			}
			for _, af := range []struct {
				raw    string
				family int
			}{{in.IPv4, 4}, {in.IPv6, 6}} {
				if af.raw == "" {
					continue
				}
				prefix, err := netip.ParsePrefix(af.raw)
				if err != nil || prefix.String() != af.raw || prefix.Bits() != prefix.Addr().BitLen() || planAddress(prefix.Addr().String(), af.family) != nil {
					return fmt.Errorf("loopback interface requires host prefixes")
				}
				if af.family == 4 {
					loopback4 = prefix.Addr().String()
				} else {
					loopback6 = prefix.Addr().String()
				}
			}
			continue
		}
		if !identifier.MatchString(in.BundleID) {
			return fmt.Errorf("planned core interface requires bundle ID")
		}
		peer := ""
		if in.Kind == "routed-physical" {
			link, exists := physical[in.PortID]
			if !exists || link.Name != in.Name || link.BundleID != in.BundleID || link.LinkID != in.LinkID || link.AggregateName != "" || link.Shutdown != in.Shutdown || in.STP || in.ChannelGroup != nil || len(in.MemberPortIDs) > 0 || usedPhysical[in.PortID] {
				return fmt.Errorf("routed physical interface does not match its link")
			}
			usedPhysical[in.PortID] = true
			peer = link.PeerSwitchID
		} else if in.Kind == "aggregate-layer2" || in.Kind == "aggregate-layer3" {
			if in.ChannelGroup == nil || groups[*in.ChannelGroup] || len(in.MemberPortIDs) == 0 || len(in.MemberPortIDs) > MaxLinks || in.LinkID != "" || in.PortID != "" {
				return fmt.Errorf("invalid aggregate interface membership or channel group")
			}
			groups[*in.ChannelGroup] = true
			allShutdown := true
			for _, id := range in.MemberPortIDs {
				link, exists := physical[id]
				if !exists || usedPhysical[id] || link.BundleID != in.BundleID || link.AggregateName != in.Name || (peer != "" && peer != link.PeerSwitchID) {
					return fmt.Errorf("aggregate member missing, reused or linked to another switch")
				}
				usedPhysical[id] = true
				peer = link.PeerSwitchID
				allShutdown = allShutdown && link.Shutdown
			}
			if allShutdown && !in.Shutdown {
				return fmt.Errorf("aggregate cannot be enabled with all members shut down")
			}
			if in.Kind == "aggregate-layer2" {
				if in.IPv4 != "" || in.IPv6 != "" || in.BFD {
					return fmt.Errorf("L2 aggregate cannot declare routed addresses or BFD")
				}
				continue
			}
			if in.STP {
				return fmt.Errorf("L3 aggregate cannot declare STP")
			}
		} else {
			return fmt.Errorf("unsupported planned interface kind")
		}
		if in.IPv4 == "" && in.IPv6 == "" {
			return fmt.Errorf("routed interface requires an address family")
		}
		for _, af := range []struct {
			raw, name string
			family    int
		}{{in.IPv4, "ipv4", 4}, {in.IPv6, "ipv6", 6}} {
			if af.raw == "" {
				continue
			}
			prefix, err := netip.ParsePrefix(af.raw)
			if err != nil || prefix.String() != af.raw {
				return fmt.Errorf("invalid routed interface address")
			}
			ap, err := AddressEndpoints(prefix.Masked().String(), af.family)
			if err != nil {
				return err
			}
			addr := prefix.Addr().String()
			remote := ap.SideA
			if addr == ap.SideA {
				remote = ap.SideB
			} else if addr != ap.SideB {
				return fmt.Errorf("routed address is not a defined subnet endpoint")
			}
			if af.family == 6 && in.MTU < 1280 {
				return fmt.Errorf("IPv6 plan requires reviewed MTU of at least 1280")
			}
			for _, old := range subnets {
				if old.Overlaps(prefix.Masked()) {
					return fmt.Errorf("planned routed subnets overlap")
				}
			}
			subnets = append(subnets, prefix.Masked())
			routes[in.Name+"/"+af.name] = routed{in, af.name, addr, remote, peer}
		}
	}
	if len(usedPhysical) != len(physical) {
		return fmt.Errorf("unattached physical links in switch plan")
	}
	for _, subnet := range subnets {
		for _, addr := range []string{loopback4, loopback6} {
			if a, err := netip.ParseAddr(addr); err == nil && subnet.Contains(a) {
				return fmt.Errorf("routed subnet overlaps target loopback")
			}
		}
	}
	if len(routes) == 0 {
		if p.BGP != nil {
			return fmt.Errorf("BGP plan without routed core interfaces")
		}
	} else {
		if p.BGP == nil || !ValidASN(p.BGP.LocalASN) || planAddress(p.BGP.RouterID, 4) != nil || p.BGP.RouterID == "" || p.BGP.RouterID != loopback4 || len(p.BGP.Neighbors) != len(routes) {
			return fmt.Errorf("routed plan requires complete BGP identity and one neighbor per interface family")
		}
		seen := map[string]bool{}
		for _, n := range p.BGP.Neighbors {
			key := n.InterfaceName + "/" + n.Family
			r, exists := routes[key]
			if !exists || seen[key] || n.BundleID != r.in.BundleID || n.LinkID != r.in.LinkID || n.PeerSwitchID != r.peer || n.LocalAddress != r.local || n.RemoteAddress != r.remote || !ValidASN(n.RemoteASN) || n.BFD != r.in.BFD || (r.in.Shutdown && !n.Shutdown) || !safeText(n.Description, 255, false) {
				return fmt.Errorf("BGP neighbor does not match its routed interface")
			}
			seen[key] = true
		}
	}
	flood := map[string]bool{}
	for _, af := range []struct {
		addresses []string
		family    int
	}{{p.Floodlist, 4}, {p.FloodlistIPv6, 6}} {
		for _, addr := range af.addresses {
			if addr == "" || addr == loopback4 || addr == loopback6 || flood[addr] || planAddress(addr, af.family) != nil {
				return fmt.Errorf("invalid duplicate or self floodlist address")
			}
			flood[addr] = true
		}
	}
	return nil
}
