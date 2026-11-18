package main

import (
	"fmt"
	"math"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/moedove/ixp-manager/internal/core"
)

type legacyCoreEndpoint struct{ ci, pi, vi, port row }
type legacyCoreGraph struct {
	ports     map[int64]bool
	pis       map[int64]bool
	vis       map[int64]bool
	endpoints map[int64]legacyCoreEndpoint
	links     map[int64][]row
}

func (c *converter) coreError(table string, r row, message string) error {
	c.warn(table, r, message)
	c.unsafeCoreOwnership = true
	return fmt.Errorf("unsafe core topology: %s id %d: %s; no safe import file can be produced", table, idOf(r), message)
}
func (c *converter) coreUsed(table string, r row, field string) {
	c.used[table][strconv.FormatInt(idOf(r), 10)+":"+field] = true
}
func (c *converter) coreNumber(table string, r row, field string) (int64, bool, error) {
	c.coreUsed(table, r, field)
	value := r[field]
	if value == nil || value == "" {
		return 0, false, nil
	}
	if _, ok := value.(bool); ok {
		return 0, true, c.coreError(table, r, field+" must be an integer, not a boolean")
	}
	n, err := integer(value)
	if err != nil {
		return 0, true, c.coreError(table, r, field+" must be an exact integer")
	}
	return n, true, nil
}
func (c *converter) coreReference(table string, r row, field, target string) (row, error) {
	n, set, err := c.coreNumber(table, r, field)
	if err != nil {
		return nil, err
	}
	linked := c.index[target][strconv.FormatInt(n, 10)]
	if !set || n < 1 || linked == nil {
		return nil, c.coreError(table, r, "missing or invalid "+field+" reference")
	}
	return linked, nil
}
func (c *converter) coreBool(table string, r row, field string, required bool) (bool, error) {
	c.coreUsed(table, r, field)
	if r[field] == nil || r[field] == "" {
		if required {
			return false, c.coreError(table, r, "missing explicit "+field+" flag")
		}
		return false, nil
	}
	n, err := integer(r[field])
	if err != nil || (n != 0 && n != 1) {
		return false, c.coreError(table, r, field+" must be boolean or0/1")
	}
	return n == 1, nil
}
func (c *converter) coreText(table string, r row, field string, multiline bool) (string, error) {
	c.coreUsed(table, r, field)
	if r[field] == nil {
		return "", nil
	}
	value, ok := r[field].(string)
	if !ok {
		return "", c.coreError(table, r, field+" must be text")
	}
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	if !multiline {
		value = strings.TrimSpace(value)
	}
	return value, nil
}
func (c *converter) coreUint32(table string, r row, field string) (*uint32, error) {
	n, set, err := c.coreNumber(table, r, field)
	if err != nil {
		return nil, err
	}
	if !set {
		return nil, nil
	}
	if n < 0 || n > math.MaxUint32 {
		return nil, c.coreError(table, r, field+" outside unsigned32-bit range")
	}
	v := uint32(n)
	return &v, nil
}
func (c *converter) coreUint64(table string, r row, field string) (*uint64, error) {
	n, set, err := c.coreNumber(table, r, field)
	if err != nil {
		return nil, err
	}
	if !set {
		return nil, nil
	}
	if n < 0 {
		return nil, c.coreError(table, r, field+" cannot be negative")
	}
	v := uint64(n)
	return &v, nil
}
func (c *converter) coreResolve(table string, r row) {
	for _, field := range []string{"created_at", "updated_at"} {
		c.coreUsed(table, r, field)
	}
	c.resolve(table, r)
}

// Reserve every core-typed port before member conversion. A free TYPE_CORE
// port remains a reservation; a physical interface on it must resolve through
// exactly one complete core interface/link/bundle chain.
func (c *converter) prepareCoreGraph() error {
	g := &legacyCoreGraph{ports: map[int64]bool{}, pis: map[int64]bool{}, vis: map[int64]bool{}, endpoints: map[int64]legacyCoreEndpoint{}, links: map[int64][]row{}}
	c.coreGraph = g
	for _, port := range c.source.Tables["switchport"] {
		kind, _ := integer(port["type"])
		if kind == 3 {
			g.ports[idOf(port)] = true
			c.coreUsed("switchport", port, "type")
		}
	}
	piByPort := map[int64][]row{}
	for _, pi := range c.source.Tables["physicalinterface"] {
		portID, _ := integer(pi["switchportid"])
		if portID > 0 {
			piByPort[portID] = append(piByPort[portID], pi)
		}
	}
	ciByPI := map[int64]int64{}
	for _, ci := range c.source.Tables["coreinterfaces"] {
		pi, err := c.coreReference("coreinterfaces", ci, "physical_interface_id", "physicalinterface")
		if err != nil {
			return err
		}
		if ciByPI[idOf(pi)] != 0 {
			return c.coreError("coreinterfaces", ci, "physical interface belongs to multiple core endpoints")
		}
		ciByPI[idOf(pi)] = idOf(ci)
		port, err := c.coreReference("physicalinterface", pi, "switchportid", "switchport")
		if err != nil {
			return err
		}
		if len(piByPort[idOf(port)]) != 1 {
			return c.coreError("physicalinterface", pi, "physical port is claimed by multiple physical interfaces")
		}
		kind, set, err := c.coreNumber("switchport", port, "type")
		if err != nil {
			return err
		}
		if set && kind != 0 && kind != 3 {
			return c.coreError("switchport", port, "core endpoint conflicts with an explicitly non-core port purpose")
		}
		vi, err := c.coreReference("physicalinterface", pi, "virtualinterfaceid", "virtualinterface")
		if err != nil {
			return err
		}
		fanout, _, err := c.coreNumber("physicalinterface", pi, "fanout_physical_interface_id")
		if err != nil {
			return err
		}
		if fanout != 0 {
			return c.coreError("physicalinterface", pi, "core endpoint cannot carry a member fanout relationship")
		}
		g.ports[idOf(port)], g.pis[idOf(pi)], g.vis[idOf(vi)] = true, true, true
		g.endpoints[idOf(ci)] = legacyCoreEndpoint{ci, pi, vi, port}
	}
	ciUse := map[int64]bool{}
	viUse := map[int64]string{}
	for _, link := range c.source.Tables["corelinks"] {
		bundle, err := c.coreReference("corelinks", link, "core_bundle_id", "corebundles")
		if err != nil {
			return err
		}
		for _, side := range []string{"a", "b"} {
			ci, err := c.coreReference("corelinks", link, "core_interface_side"+side+"_id", "coreinterfaces")
			if err != nil {
				return err
			}
			if ciUse[idOf(ci)] {
				return c.coreError("corelinks", link, "core interface is reused across link sides or links")
			}
			ciUse[idOf(ci)] = true
			ep := g.endpoints[idOf(ci)]
			key := fmt.Sprintf("%d/%s", idOf(bundle), side)
			if old := viUse[idOf(ep.vi)]; old != "" && old != key {
				return c.coreError("virtualinterface", ep.vi, "core virtual interface is shared across bundle sides or bundles")
			}
			viUse[idOf(ep.vi)] = key
		}
		g.links[idOf(bundle)] = append(g.links[idOf(bundle)], link)
	}
	for _, ci := range c.source.Tables["coreinterfaces"] {
		if !ciUse[idOf(ci)] {
			return c.coreError("coreinterfaces", ci, "orphan core interface cannot be dropped or treated as available")
		}
	}
	for _, bundle := range c.source.Tables["corebundles"] {
		if len(g.links[idOf(bundle)]) == 0 {
			return c.coreError("corebundles", bundle, "core bundle has no complete links")
		}
	}
	for _, pi := range c.source.Tables["physicalinterface"] {
		portID, _ := integer(pi["switchportid"])
		viID, _ := integer(pi["virtualinterfaceid"])
		if g.ports[portID] && !g.pis[idOf(pi)] {
			return c.coreError("physicalinterface", pi, "core-reserved port is occupied by a physical interface without a complete core binding")
		}
		if g.vis[viID] && !g.pis[idOf(pi)] {
			return c.coreError("physicalinterface", pi, "core virtual interface mixes core and non-core physical interfaces")
		}
		fanoutID, _ := integer(pi["fanout_physical_interface_id"])
		if g.pis[fanoutID] {
			return c.coreError("physicalinterface", pi, "member fanout references a core physical interface")
		}
	}
	for _, vli := range c.source.Tables["vlaninterface"] {
		viID, _ := integer(vli["virtualinterfaceid"])
		if g.vis[viID] {
			return c.coreError("vlaninterface", vli, "core virtual interface has a VLAN member-access attachment; cannot silently import or discard it")
		}
	}
	for _, port := range c.source.Tables["switchport"] {
		if g.ports[idOf(port)] {
			if _, err := c.coreBool("switchport", port, "active", false); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *converter) coreSwitchFields(r, o row) error {
	if _, exists := r["active"]; exists {
		active, err := c.coreBool("switch", r, "active", false)
		if err != nil {
			return err
		}
		o["active"] = active
	}
	if r["asn"] != nil && r["asn"] != "" {
		n, _, err := c.coreNumber("switch", r, "asn")
		if err != nil {
			return err
		}
		if n < 1 || n > 4294967294 {
			return c.coreError("switch", r, "switch ASN outside supported range")
		}
		o["asn"] = n
	}
	if r["loopback_ip"] != nil {
		raw, err := c.coreText("switch", r, "loopback_ip", false)
		if err != nil {
			return err
		}
		if raw != "" {
			ip, err := netip.ParseAddr(raw)
			if err != nil || ip.Is4In6() || ip.Zone() != "" || !ip.IsGlobalUnicast() {
				return c.coreError("switch", r, "loopback must be a unicast IP without a zone")
			}
			field := "loopbackIpv6"
			if ip.Is4() {
				field = "loopbackIpv4"
			}
			o[field] = ip.String()
		}
	}
	if r["loopback_name"] != nil {
		value, err := c.coreText("switch", r, "loopback_name", false)
		if err != nil {
			return err
		}
		if value != "" {
			o["loopbackName"] = value
		}
	}
	return nil
}
func (c *converter) convertedCoreRef(table string, r row, field, target string) (string, error) {
	linked, err := c.coreReference(table, r, field, map[string]string{"ports": "switchport", "switches": "switch", "members": "cust"}[target])
	if err != nil {
		return "", err
	}
	id := legacyID(target, idOf(linked))
	if !c.exported[target][id] {
		return "", c.coreError(table, r, "required "+target+" record was not converted; core occupancy cannot be dropped")
	}
	return id, nil
}
func (c *converter) coreSide(ep legacyCoreEndpoint) (core.Side, error) {
	var side core.Side
	var err error
	if side.SwitchID, err = c.convertedCoreRef("switchport", ep.port, "switchid", "switches"); err != nil {
		return side, err
	}
	owner, set, err := c.coreNumber("virtualinterface", ep.vi, "custid")
	if err != nil {
		return side, err
	}
	if set && owner != 0 {
		if side.OwnerMemberID, err = c.convertedCoreRef("virtualinterface", ep.vi, "custid", "members"); err != nil {
			return side, err
		}
	}
	if side.InterfaceName, err = c.coreText("virtualinterface", ep.vi, "name", false); err != nil {
		return side, err
	}
	if side.Description, err = c.coreText("virtualinterface", ep.vi, "description", true); err != nil {
		return side, err
	}
	if side.ChannelGroup, err = c.coreUint32("virtualinterface", ep.vi, "channelgroup"); err != nil {
		return side, err
	}
	mtu, err := c.coreUint32("virtualinterface", ep.vi, "mtu")
	if err != nil {
		return side, err
	}
	if mtu != nil {
		side.MTU = *mtu
	}
	if side.Trunk, err = c.coreBool("virtualinterface", ep.vi, "trunk", false); err != nil {
		return side, err
	}
	if side.FastLACP, err = c.coreBool("virtualinterface", ep.vi, "fastlacp", false); err != nil {
		return side, err
	}
	if side.LAGFraming, err = c.coreBool("virtualinterface", ep.vi, "lag_framing", false); err != nil {
		return side, err
	}
	return side, nil
}
func (c *converter) coreEndpoint(ep legacyCoreEndpoint) (core.Endpoint, error) {
	out := core.Endpoint{ID: legacyID("core-interfaces", idOf(ep.ci))}
	var err error
	if out.PortID, err = c.convertedCoreRef("physicalinterface", ep.pi, "switchportid", "ports"); err != nil {
		return out, err
	}
	if out.SpeedMbps, err = c.coreUint64("physicalinterface", ep.pi, "speed"); err != nil {
		return out, err
	}
	if out.RateLimitMbps, err = c.coreUint64("physicalinterface", ep.pi, "rate_limit"); err != nil {
		return out, err
	}
	if out.Duplex, err = c.coreText("physicalinterface", ep.pi, "duplex", false); err != nil {
		return out, err
	}
	out.Duplex = strings.ToLower(out.Duplex)
	if out.Autoneg, err = c.coreBool("physicalinterface", ep.pi, "autoneg", true); err != nil {
		return out, err
	}
	if out.Notes, err = c.coreText("physicalinterface", ep.pi, "notes", true); err != nil {
		return out, err
	}
	status, set, err := c.coreNumber("physicalinterface", ep.pi, "status")
	if err != nil {
		return out, err
	}
	if set {
		out.Status = map[int64]string{1: "connected", 2: "disabled", 3: "not-connected", 4: "awaiting-cross-connect", 5: "quarantine"}[status]
		if out.Status == "" {
			return out, c.coreError("physicalinterface", ep.pi, "unknown physical status; cannot fabricate an enabled/disabled state")
		}
	}
	return out, nil
}
func (c *converter) coreSubnet(table string, r row, field string, family int) (string, error) {
	value, err := c.coreText(table, r, field, false)
	if err != nil {
		return "", err
	}
	if value != "" {
		p, e := netip.ParsePrefix(value)
		if e == nil && p == p.Masked() {
			value = p.String()
		}
	}
	if _, err = core.AddressEndpoints(value, family); err != nil {
		return "", c.coreError(table, r, field+" does not contain a canonical subnet with two usable endpoints")
	}
	return value, nil
}

func (c *converter) convertCoreBundles() error {
	for id := range c.coreGraph.ports {
		if !c.exported["ports"][legacyID("ports", id)] {
			return c.coreError("switchport", c.index["switchport"][strconv.FormatInt(id, 10)], "core-reserved port was not converted")
		}
	}
	if len(c.source.Tables["corebundles"]) > core.MaxBundles {
		return fmt.Errorf("core bundle count exceeds supported migration limit")
	}
	if len(c.source.Tables["corelinks"]) > core.MaxTotalLinks {
		return fmt.Errorf("core link count exceeds supported migration limit")
	}
	bundles := append([]row(nil), c.source.Tables["corebundles"]...)
	sort.Slice(bundles, func(i, j int) bool { return idOf(bundles[i]) < idOf(bundles[j]) })
	for _, r := range bundles {
		b := core.Bundle{ID: legacyID("core-bundles", idOf(r)), Links: []core.Link{}}
		var err error
		if b.Name, err = c.coreText("corebundles", r, "description", false); err != nil {
			return err
		}
		if b.GraphTitle, err = c.coreText("corebundles", r, "graph_title", false); err != nil {
			return err
		}
		typeID, _, err := c.coreNumber("corebundles", r, "type")
		if err != nil {
			return err
		}
		b.Type = map[int64]string{1: core.ECMP, 2: core.L2LAG, 3: core.L3LAG}[typeID]
		if b.Enabled, err = c.coreBool("corebundles", r, "enabled", true); err != nil {
			return err
		}
		if b.BFD, err = c.coreBool("corebundles", r, "bfd", false); err != nil {
			return err
		}
		if b.STP, err = c.coreBool("corebundles", r, "stp", false); err != nil {
			return err
		}
		if b.IPv4Subnet, err = c.coreSubnet("corebundles", r, "ipv4_subnet", 4); err != nil {
			return err
		}
		if b.IPv6Subnet, err = c.coreSubnet("corebundles", r, "ipv6_subnet", 6); err != nil {
			return err
		}
		if b.Cost, err = c.coreUint32("corebundles", r, "cost"); err != nil {
			return err
		}
		if b.Preference, err = c.coreUint32("corebundles", r, "preference"); err != nil {
			return err
		}
		links := append([]row(nil), c.coreGraph.links[idOf(r)]...)
		sort.Slice(links, func(i, j int) bool { return idOf(links[i]) < idOf(links[j]) })
		viIDs := [2]int64{}
		switchIDs := [2]string{}
		for _, link := range links {
			l := core.Link{ID: legacyID("core-links", idOf(link))}
			if l.Enabled, err = c.coreBool("corelinks", link, "enabled", true); err != nil {
				return err
			}
			if l.BFD, err = c.coreBool("corelinks", link, "bfd", false); err != nil {
				return err
			}
			if l.IPv4Subnet, err = c.coreSubnet("corelinks", link, "ipv4_subnet", 4); err != nil {
				return err
			}
			if l.IPv6Subnet, err = c.coreSubnet("corelinks", link, "ipv6_subnet", 6); err != nil {
				return err
			}
			for i, side := range []string{"a", "b"} {
				ci, err := c.coreReference("corelinks", link, "core_interface_side"+side+"_id", "coreinterfaces")
				if err != nil {
					return err
				}
				ep := c.coreGraph.endpoints[idOf(ci)]
				if viIDs[i] != 0 && viIDs[i] != idOf(ep.vi) {
					return c.coreError("corelinks", link, "one bundle side spans distinct virtual interfaces; cannot collapse them")
				}
				viIDs[i] = idOf(ep.vi)
				endpoint, err := c.coreEndpoint(ep)
				if err != nil {
					return err
				}
				logical, err := c.coreSide(ep)
				if err != nil {
					return err
				}
				if switchIDs[i] != "" && switchIDs[i] != logical.SwitchID {
					return c.coreError("corelinks", link, "one core bundle side spans multiple switches; cannot collapse that topology")
				}
				switchIDs[i] = logical.SwitchID
				if i == 0 {
					l.SideA = endpoint
					b.SideA = logical
				} else {
					l.SideB = endpoint
					b.SideB = logical
				}
				c.coreResolve("coreinterfaces", ep.ci)
				c.coreResolve("physicalinterface", ep.pi)
				c.coreResolve("virtualinterface", ep.vi)
			}
			b.Links = append(b.Links, l)
			c.coreResolve("corelinks", link)
		}
		if err = core.Validate(b); err != nil {
			return c.coreError("corebundles", r, "cannot represent bundle faithfully: "+err.Error())
		}
		c.result.CoreBundles = append(c.result.CoreBundles, b)
		c.coreResolve("corebundles", r)
	}
	return c.checkConvertedCoreInventory()
}

// Catch inventory contradictions before publishing a file, rather than asking
// operators to discover them only after handing the export to the import API.
func (c *converter) checkConvertedCoreInventory() error {
	switches := map[string]row{}
	for _, sw := range c.result.Resources["switches"] {
		switches[sw["id"].(string)] = sw
	}
	text := func(r row, key string) string { v, _ := r[key].(string); return v }
	shared := func(a, b []string) bool {
		for _, x := range a {
			for _, y := range b {
				if x == y {
					return true
				}
			}
		}
		return false
	}
	type use struct {
		prefix            netip.Prefix
		switches, fabrics []string
	}
	uses := []use{}
	groups := map[string]bool{}
	for _, b := range c.result.CoreBundles {
		source := c.index["corebundles"][strings.TrimPrefix(b.ID, "legacy-core-bundles-")]
		fabricA, fabricB := text(switches[b.SideA.SwitchID], "infrastructureId"), text(switches[b.SideB.SwitchID], "infrastructureId")
		if b.Type == core.L2LAG && fabricA != fabricB {
			return c.coreError("corebundles", source, "L2 bundle crosses distinct infrastructures")
		}
		if b.Type != core.ECMP {
			for _, side := range []core.Side{b.SideA, b.SideB} {
				key := fmt.Sprintf("%s/%d", side.SwitchID, *side.ChannelGroup)
				if groups[key] {
					return c.coreError("corebundles", source, "channel group is reused on the same switch")
				}
				groups[key] = true
			}
		}
		for _, derived := range core.Addresses(b) {
			for _, pair := range []*core.AddressPair{derived.IPv4, derived.IPv6} {
				if pair == nil {
					continue
				}
				p, _ := netip.ParsePrefix(pair.Subnet)
				next := use{p, []string{b.SideA.SwitchID, b.SideB.SwitchID}, []string{fabricA, fabricB}}
				for _, old := range uses {
					if p.Overlaps(old.prefix) && (shared(next.switches, old.switches) || shared(next.fabrics, old.fabrics)) {
						return c.coreError("corebundles", source, "core subnets overlap within a shared switch or infrastructure")
					}
				}
				for _, v := range c.result.Resources["vlans"] {
					if !shared(next.fabrics, []string{text(v, "infrastructureId")}) {
						continue
					}
					prefixes := []string{text(v, "ipv4Prefix"), text(v, "ipv6Prefix")}
					if extra, ok := v["prefixes"].([]string); ok {
						prefixes = append(prefixes, extra...)
					}
					for _, raw := range prefixes {
						other, err := netip.ParsePrefix(raw)
						if err == nil && p.Overlaps(other) {
							return c.coreError("corebundles", source, "core subnet overlaps a peering VLAN prefix")
						}
					}
				}
				for _, sw := range switches {
					if !shared(next.fabrics, []string{text(sw, "infrastructureId")}) {
						continue
					}
					for _, key := range []string{"loopbackIpv4", "loopbackIpv6"} {
						ip, err := netip.ParseAddr(text(sw, key))
						if err == nil && p.Contains(ip) {
							return c.coreError("corebundles", source, "core subnet overlaps a switch loopback")
						}
					}
				}
				uses = append(uses, next)
			}
		}
	}
	// VI channel groups also have physical significance outside core bundles.
	// A member LAG must not silently share the numeric group of an imported core.
	byVI := map[int64][]row{}
	for _, pi := range c.source.Tables["physicalinterface"] {
		id, _ := integer(pi["virtualinterfaceid"])
		byVI[id] = append(byVI[id], pi)
	}
	for _, vi := range c.source.Tables["virtualinterface"] {
		if c.coreGraph.vis[idOf(vi)] {
			continue
		}
		group, err := integer(vi["channelgroup"])
		if err != nil || group < 0 {
			continue
		}
		framing, _ := integer(vi["lag_framing"])
		if len(byVI[idOf(vi)]) < 2 && framing != 1 {
			continue
		}
		for _, pi := range byVI[idOf(vi)] {
			portID, _ := integer(pi["switchportid"])
			port := c.index["switchport"][strconv.FormatInt(portID, 10)]
			switchID, _ := integer(port["switchid"])
			if groups[fmt.Sprintf("%s/%d", legacyID("switches", switchID), group)] {
				return c.coreError("virtualinterface", vi, "member LAG and core bundle claim the same channel group on a switch")
			}
		}
	}
	return nil
}
