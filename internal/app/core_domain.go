package app

import (
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/core"
)

func coreBundleByID(s *State, id string) *core.Bundle {
	for i := range s.CoreBundles {
		if s.CoreBundles[i].ID == id {
			return &s.CoreBundles[i]
		}
	}
	return nil
}

func corePorts(b core.Bundle) []string {
	out := make([]string, 0, len(b.Links)*2)
	for _, l := range b.Links {
		out = append(out, l.SideA.PortID, l.SideB.PortID)
	}
	return out
}

func coreNormalize(b core.Bundle) core.Bundle {
	if b.ID == "" {
		b.ID = token(12)
	}
	b.Name = strings.TrimSpace(b.Name)
	b.GraphTitle = strings.TrimSpace(b.GraphTitle)
	if b.GraphTitle == "" {
		b.GraphTitle = b.Name
	}
	for i := range b.Links {
		l := &b.Links[i]
		if l.ID == "" {
			l.ID = token(12)
		}
		if l.SideA.ID == "" {
			l.SideA.ID = token(12)
		}
		if l.SideB.ID == "" {
			l.SideB.ID = token(12)
		}
	}
	return b
}

func coreMetadata(b core.Bundle) error {
	if !credentialRevision.MatchString(b.BindingRevision) {
		return fmt.Errorf("core bundle has invalid binding revision")
	}
	created, e := time.Parse(time.RFC3339Nano, b.CreatedAt)
	updated, e2 := time.Parse(time.RFC3339Nano, b.UpdatedAt)
	if e != nil || e2 != nil || updated.Before(created) {
		return fmt.Errorf("core bundle has invalid metadata timestamps")
	}
	return nil
}

func validateCore(s *State) error {
	if len(s.CoreBundles) > core.MaxBundles {
		return fmt.Errorf("core bundle quota exceeded")
	}
	used := map[string]string{}
	ids := map[string]bool{}
	groups := map[string]bool{}
	type subnetUse struct {
		p        netip.Prefix
		switches []string
		fabrics  []string
	}
	subnets := []subnetUse{}
	totalLinks := 0
	for _, b := range s.CoreBundles {
		totalLinks += len(b.Links)
		if totalLinks > core.MaxTotalLinks {
			return fmt.Errorf("total core link quota exceeded")
		}
		if err := core.Validate(b); err != nil {
			return fmt.Errorf("core bundle %s: %w", b.ID, err)
		}
		if err := coreMetadata(b); err != nil {
			return err
		}
		for _, id := range append([]string{b.ID}, coreObjectIDs(b)...) {
			if ids[id] {
				return fmt.Errorf("duplicate core object ID")
			}
			ids[id] = true
		}
		for _, side := range []core.Side{b.SideA, b.SideB} {
			sw := find(s, "switches", side.SwitchID)
			if sw == nil {
				return fmt.Errorf("core bundle switch does not exist")
			}
			if side.OwnerMemberID != "" && find(s, "members", side.OwnerMemberID) == nil {
				return fmt.Errorf("core side owner does not exist")
			}
			if b.Type != core.ECMP {
				key := fmt.Sprintf("%s/%d", side.SwitchID, *side.ChannelGroup)
				if groups[key] {
					return fmt.Errorf("core channelGroup already used on switch")
				}
				groups[key] = true
				nameKey := side.SwitchID + "/name/" + side.InterfaceName
				if groups[nameKey] {
					return fmt.Errorf("core aggregate interfaceName already used on switch")
				}
				groups[nameKey] = true
			}
		}
		swA := find(s, "switches", b.SideA.SwitchID)
		swB := find(s, "switches", b.SideB.SwitchID)
		if b.Type == core.L2LAG && fabricID(swA) != fabricID(swB) {
			return fmt.Errorf("L2 core bundle cannot bridge different infrastructures")
		}
		for _, l := range b.Links {
			for _, pair := range []struct {
				ep   core.Endpoint
				side core.Side
			}{{l.SideA, b.SideA}, {l.SideB, b.SideB}} {
				p := find(s, "ports", pair.ep.PortID)
				if p == nil || str(p, "switchId") != pair.side.SwitchID {
					return fmt.Errorf("core endpoint must reference a port on its declared switch")
				}
				if p["coreReserved"] != true {
					return fmt.Errorf("core bundle endpoint must retain its core-reserved port purpose")
				}
				if used[pair.ep.PortID] != "" {
					return fmt.Errorf("physical port belongs to another core link")
				}
				used[pair.ep.PortID] = b.ID
				if str(p, "memberId") != "" {
					return fmt.Errorf("member-owned physical port cannot be a core endpoint")
				}
			}
		}
		for _, d := range core.Addresses(b) {
			for _, ap := range []*core.AddressPair{d.IPv4, d.IPv6} {
				if ap == nil {
					continue
				}
				p, _ := netip.ParsePrefix(ap.Subnet)
				use := subnetUse{p, []string{b.SideA.SwitchID, b.SideB.SwitchID}, []string{fabricID(swA), fabricID(swB)}}
				for _, old := range subnets {
					if p.Overlaps(old.p) && (overlapStrings(use.switches, old.switches) || overlapStrings(use.fabrics, old.fabrics)) {
						return fmt.Errorf("core subnets overlap within a shared switch or infrastructure")
					}
				}
				for _, v := range s.Resources["vlans"] {
					if !contains(use.fabrics, fabricID(v)) {
						continue
					}
					ps, err := vlanPrefixes(v)
					if err != nil {
						return err
					}
					for _, vp := range ps {
						if p.Overlaps(vp) {
							return fmt.Errorf("core subnet overlaps a peering VLAN prefix")
						}
					}
				}
				for _, sw := range s.Resources["switches"] {
					if !contains(use.fabrics, fabricID(sw)) {
						continue
					}
					for _, key := range []string{"loopbackIpv4", "loopbackIpv6"} {
						if addr, err := netip.ParseAddr(str(sw, key)); err == nil && p.Contains(addr) {
							return fmt.Errorf("core subnet overlaps a switch loopback")
						}
					}
				}
				subnets = append(subnets, use)
			}
		}
	}
	for _, l := range s.Resources["core-links"] {
		a, b := str(l, "portAId"), str(l, "portBId")
		if a == "" || b == "" || a == b {
			return fmt.Errorf("unbundled core link needs distinct physical endpoints")
		}
		for _, id := range []string{a, b} {
			if find(s, "ports", id) == nil {
				return fmt.Errorf("unbundled core endpoint missing")
			}
			if used[id] != "" {
				return fmt.Errorf("physical port belongs to multiple core links")
			}
			used[id] = "unbundled:" + str(l, "id")
		}
	}
	for _, p := range s.Resources["ports"] {
		reserved, _ := p["coreReserved"].(bool)
		if (reserved || used[str(p, "id")] != "") && str(p, "memberId") != "" {
			return fmt.Errorf("core-reserved port cannot belong to a member")
		}
	}
	for _, c := range s.Resources["connections"] {
		ps, err := connectionPortIDs(c)
		if err != nil {
			return err
		}
		for _, id := range ps {
			p := find(s, "ports", id)
			reserved, _ := p["coreReserved"].(bool)
			if reserved || used[id] != "" {
				return fmt.Errorf("member connection cannot occupy a core-reserved or core-linked port")
			}
		}
	}
	// A patch cable may describe a core path. An allocated member circuit may not.
	for _, p := range s.Resources["patch-ports"] {
		id := str(p, "switchPortId")
		if id == "" || str(p, "memberId") == "" {
			continue
		}
		port := find(s, "ports", id)
		reserved, _ := port["coreReserved"].(bool)
		if reserved || used[id] != "" {
			return fmt.Errorf("member patch allocation conflicts with a core port")
		}
	}
	return nil
}

func coreObjectIDs(b core.Bundle) []string {
	out := []string{}
	for _, l := range b.Links {
		out = append(out, l.ID, l.SideA.ID, l.SideB.ID)
	}
	return out
}
func overlapStrings(a, b []string) bool {
	for _, x := range a {
		if contains(b, x) {
			return true
		}
	}
	return false
}

func coreDerived(s *State, b core.Bundle) (core.Derived, []core.Warning) {
	d := core.Derived{Addresses: core.Addresses(b), InventoryOnly: true, CapacityKnown: true}
	warnings := []core.Warning{}
	var capacity uint64
	enabled := 0
	for _, l := range b.Links {
		if l.Enabled {
			enabled++
		}
		a := number(find(s, "ports", l.SideA.PortID), "speed")
		z := number(find(s, "ports", l.SideB.PortID), "speed")
		if (l.SideA.SpeedMbps != nil && a > 0 && float64(*l.SideA.SpeedMbps) != a) || (l.SideB.SpeedMbps != nil && z > 0 && float64(*l.SideB.SpeedMbps) != z) {
			warnings = append(warnings, core.Warning{Code: "configured-speed-mismatch", Message: "A desired endpoint speed differs from physical inventory; both values are preserved and device configuration requires review."})
		}
		if a <= 0 || z <= 0 || a != z {
			d.CapacityKnown = false
		} else {
			capacity += uint64(a)
		}
	}
	if d.CapacityKnown {
		d.CapacityMbps = &capacity
	} else {
		warnings = append(warnings, core.Warning{Code: "capacity-unknown", Message: "Physical speeds are missing or differ across a link; no aggregate capacity is asserted."})
	}
	if enabled == 0 {
		warnings = append(warnings, core.Warning{Code: "no-enabled-links", Message: "No links are enabled; this topology cannot imply healthy connectivity."})
	}
	if b.Type != core.L2LAG {
		a := find(s, "switches", b.SideA.SwitchID)
		z := find(s, "switches", b.SideB.SwitchID)
		d.LocalASNA = uint32(number(a, "asn"))
		d.LocalASNB = uint32(number(z, "asn"))
		d.RouterIDA = str(a, "loopbackIpv4")
		d.RouterIDB = str(z, "loopbackIpv4")
		d.BGPReady = core.ValidASN(d.LocalASNA) && core.ValidASN(d.LocalASNB) && d.RouterIDA != "" && d.RouterIDB != "" && str(a, "loopbackName") != "" && str(z, "loopbackName") != ""
		if !d.BGPReady {
			warnings = append(warnings, core.Warning{Code: "bgp-incomplete", Message: "Both switches require usable ASNs (not reserved or AS_TRANS), IPv4 router IDs and loopback names before generating a BGP configuration."})
		}
		v6 := b.IPv6Subnet != ""
		for _, l := range b.Links {
			v6 = v6 || l.IPv6Subnet != ""
		}
		if v6 && (b.SideA.MTU < 1280 || b.SideB.MTU < 1280) {
			warnings = append(warnings, core.Warning{Code: "ipv6-mtu-incomplete", Message: "IPv6 requires a reviewed MTU of at least 1280 on each side before device configuration; zero is unknown."})
		}
	}
	warnings = append(warnings, core.Warning{Code: "inventory-only", Message: "This is desired inventory, not device configuration or proof of LACP, BGP or BFD health."})
	return d, warnings
}
