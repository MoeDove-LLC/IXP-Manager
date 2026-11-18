package core

import (
	"fmt"
	"net/netip"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

// AddressEndpoints preserves the historical IPv4 endpoint convention, with
// bounds checks, and uses the equivalent explicit convention for IPv6.
func AddressEndpoints(raw string, family int) (*AddressPair, error) {
	if raw == "" {
		return nil, nil
	}
	p, err := netip.ParsePrefix(raw)
	if err != nil || p != p.Masked() || p.String() != raw || p.Addr().Is4In6() || p.Addr().Zone() != "" || (family == 4) != p.Addr().Is4() {
		return nil, fmt.Errorf("IPv%d subnet must be a canonical CIDR", family)
	}
	bits := p.Addr().BitLen()
	if p.Bits() >= bits {
		return nil, fmt.Errorf("subnet needs two usable endpoint addresses")
	}
	a := p.Addr()
	if p.Bits() != bits-1 {
		a = a.Next()
	}
	b := a.Next()
	if !p.Contains(a) || !p.Contains(b) || !a.IsGlobalUnicast() || !b.IsGlobalUnicast() {
		return nil, fmt.Errorf("subnet must contain two unicast endpoints")
	}
	if family == 4 && p.Bits() != 31 && !p.Contains(b.Next()) {
		return nil, fmt.Errorf("endpoint cannot be an IPv4 broadcast address")
	}
	return &AddressPair{Subnet: raw, SideA: a.String(), SideB: b.String()}, nil
}

func safeText(s string, limit int, multiline bool) bool {
	if !utf8.ValidString(s) || len(s) > limit {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\t')) {
			return false
		}
	}
	return true
}

// Validate checks a desired topology only. Reference ownership, fabric and
// address conflicts are checked transactionally by the inventory application.
func Validate(b Bundle) error {
	if !identifier.MatchString(b.ID) || strings.TrimSpace(b.Name) == "" || !safeText(b.Name, 255, false) || !safeText(b.GraphTitle, 255, false) {
		return fmt.Errorf("core bundle requires a valid ID, name and graph title")
	}
	if b.Type != ECMP && b.Type != L2LAG && b.Type != L3LAG {
		return fmt.Errorf("core bundle type must be ecmp, l2-lag or l3-lag")
	}
	if len(b.Links) < 1 || len(b.Links) > MaxLinks {
		return fmt.Errorf("core bundle needs 1..%d links", MaxLinks)
	}
	if b.SideA.SwitchID == b.SideB.SwitchID {
		return fmt.Errorf("core bundle endpoints must be on different switches")
	}
	for _, side := range []Side{b.SideA, b.SideB} {
		if !identifier.MatchString(side.SwitchID) || (side.OwnerMemberID != "" && !identifier.MatchString(side.OwnerMemberID)) {
			return fmt.Errorf("invalid core side reference")
		}
		if !safeText(side.InterfaceName, 255, false) || !safeText(side.Description, 4096, true) || side.MTU > 64000 {
			return fmt.Errorf("invalid core interface name, description or MTU (0..64000)")
		}
		if b.Type != ECMP && (strings.TrimSpace(side.InterfaceName) == "" || side.ChannelGroup == nil) {
			return fmt.Errorf("LAG sides require interfaceName and channelGroup (zero is permitted)")
		}
	}
	if b.Type != L2LAG && b.STP {
		return fmt.Errorf("STP belongs only to an L2 LAG")
	}
	if b.Type != L3LAG && (b.BFD || b.IPv4Subnet != "" || b.IPv6Subnet != "") {
		return fmt.Errorf("bundle subnets and BFD belong only to L3 LAG; ECMP uses link fields")
	}
	if b.Type == L3LAG && b.IPv4Subnet == "" && b.IPv6Subnet == "" {
		return fmt.Errorf("L3 LAG requires at least one subnet")
	}
	if _, err := AddressEndpoints(b.IPv4Subnet, 4); err != nil {
		return err
	}
	if _, err := AddressEndpoints(b.IPv6Subnet, 6); err != nil {
		return err
	}
	ids := map[string]bool{b.ID: true}
	ports := map[string]bool{}
	for _, l := range b.Links {
		if !identifier.MatchString(l.ID) || ids[l.ID] {
			return fmt.Errorf("core link IDs must be distinct")
		}
		ids[l.ID] = true
		if b.Type != ECMP && (l.BFD || l.IPv4Subnet != "" || l.IPv6Subnet != "") {
			return fmt.Errorf("link subnets and BFD belong only to ECMP")
		}
		if b.Type == ECMP && l.IPv4Subnet == "" && l.IPv6Subnet == "" {
			return fmt.Errorf("each ECMP link requires at least one subnet")
		}
		if _, err := AddressEndpoints(l.IPv4Subnet, 4); err != nil {
			return err
		}
		if _, err := AddressEndpoints(l.IPv6Subnet, 6); err != nil {
			return err
		}
		for _, ep := range []Endpoint{l.SideA, l.SideB} {
			if !identifier.MatchString(ep.ID) || ids[ep.ID] || !identifier.MatchString(ep.PortID) || ports[ep.PortID] {
				return fmt.Errorf("core endpoints require distinct IDs and physical ports")
			}
			ids[ep.ID] = true
			ports[ep.PortID] = true
			if ep.SpeedMbps != nil && (*ep.SpeedMbps == 0 || *ep.SpeedMbps > 10000000) {
				return fmt.Errorf("endpoint speedMbps must be 1..10000000, or omitted when unknown")
			}
			if ep.RateLimitMbps != nil && *ep.RateLimitMbps > 10000000 {
				return fmt.Errorf("endpoint rateLimitMbps exceeds supported range")
			}
			if ep.Duplex != "full" && ep.Duplex != "half" {
				return fmt.Errorf("endpoint duplex must be full or half")
			}
			if !safeText(ep.Notes, 4096, true) {
				return fmt.Errorf("invalid endpoint notes")
			}
			switch ep.Status {
			case "", "connected", "disabled", "not-connected", "awaiting-cross-connect", "quarantine":
			default:
				return fmt.Errorf("invalid desired endpoint status")
			}
		}
	}
	return nil
}

func Addresses(b Bundle) []DerivedLink {
	out := []DerivedLink{}
	if b.Type == L2LAG {
		return out
	}
	if b.Type == L3LAG {
		a, _ := AddressEndpoints(b.IPv4Subnet, 4)
		z, _ := AddressEndpoints(b.IPv6Subnet, 6)
		enabled := false
		for _, l := range b.Links {
			enabled = enabled || l.Enabled
		}
		return append(out, DerivedLink{IPv4: a, IPv6: z, BFD: b.BFD, Enabled: b.Enabled && enabled})
	}
	for _, l := range b.Links {
		a, _ := AddressEndpoints(l.IPv4Subnet, 4)
		z, _ := AddressEndpoints(l.IPv6Subnet, 6)
		out = append(out, DerivedLink{LinkID: l.ID, IPv4: a, IPv6: z, BFD: l.BFD, Enabled: b.Enabled && l.Enabled})
	}
	return out
}
