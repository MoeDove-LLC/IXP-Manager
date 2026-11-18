package main

import (
	"net/netip"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Numeric values come from app/Models/Router.php and the actual `routers`
// schema. In particular SOFTWARE_BIRD=1 means BIRD 1, never BIRD 2.
var legacyRouterPlatforms = map[int64]string{1: "bird1", 6: "bird2", 7: "bird3", 2: "quagga", 3: "frr", 4: "openbgpd", 5: "cisco"}

func (c *converter) routerUsed(r row, field string) {
	c.used["routers"][strconv.FormatInt(idOf(r), 10)+":"+field] = true
}
func (c *converter) routerInteger(r row, field string) (int64, bool) {
	c.routerUsed(r, field)
	if _, invalid := r[field].(bool); invalid {
		return 0, false
	}
	n, err := integer(r[field])
	return n, err == nil
}
func (c *converter) routerText(r row, field string) (string, bool) {
	c.routerUsed(r, field)
	v, ok := r[field].(string)
	return v, ok && len(v) <= 10000 && utf8.ValidString(v) && !strings.ContainsAny(v, "\x00\r") && strings.TrimSpace(v) != ""
}
func (c *converter) routerFlag(r row, field string) (value, present, valid bool) {
	c.routerUsed(r, field)
	v, exists := r[field]
	if !exists || v == nil {
		return false, false, true
	}
	if b, ok := v.(bool); ok {
		return b, true, true
	}
	n, err := integer(v)
	return n == 1, true, err == nil && (n == 0 || n == 1)
}
func routerVLANContains(vlan row, address netip.Addr) (contains, declared bool) {
	prefixes := []string{}
	for _, field := range []string{"ipv4Prefix", "ipv6Prefix"} {
		if value, ok := vlan[field].(string); ok {
			prefixes = append(prefixes, value)
		}
	}
	if extra, ok := vlan["prefixes"].([]string); ok {
		prefixes = append(prefixes, extra...)
	}
	for _, value := range prefixes {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.Addr().Is4() != address.Is4() {
			continue
		}
		declared = true
		contains = contains || prefix.Contains(address)
	}
	return contains, declared
}
func routerSourceAddress(raw string, ipv4 bool) (netip.Addr, bool) {
	address, err := netip.ParseAddr(raw)
	return address, err == nil && address.Zone() == "" && !address.Is4In6() && address.Is4() == ipv4 && address.IsGlobalUnicast()
}

// routers migrates independently declared instances as inert inventory. It does
// not derive IDs/addresses from a paired instance, a member, settings or a name;
// it never creates peers, approval records, passwords, commands or deployments.
func (c *converter) routers() {
	type candidate struct {
		source, out row
		address     string
		identifier  string
	}
	candidates := []candidate{}
	claims := map[string]int{}
	identifiers := map[string]int{}
	// Even an otherwise unsupported/paused row can declare ownership of the
	// same address. Do not choose an apparently supported row over that evidence.
	for _, r := range c.source.Tables["routers"] {
		vlanID, validVLAN := c.routerInteger(r, "vlan_id")
		peering, validPeering := c.routerText(r, "peering_ip")
		address, err := netip.ParseAddr(peering)
		if validVLAN && vlanID > 0 && validPeering && err == nil && address.Zone() == "" && !address.Is4In6() && address.IsGlobalUnicast() {
			claims[legacyID("vlans", vlanID)+"/"+address.String()]++
		}
	}
	for _, r := range c.source.Tables["routers"] {
		skip := func(reason string) { c.warn("routers", r, reason+"; router not converted") }
		software, validSoftware := c.routerInteger(r, "software")
		platform := legacyRouterPlatforms[software]
		if !validSoftware || platform == "" {
			skip("unsupported software declaration (Other is never guessed as Junos or RouterOS)")
			continue
		}
		kind, validKind := c.routerInteger(r, "type")
		role := map[int64]string{1: "route-server", 2: "route-collector"}[kind]
		if !validKind || role == "" {
			if kind == 3 {
				skip("AS112 requires separately reviewed service selection, family/DNS node inputs and ASN 112")
			} else {
				skip("unsupported router type; Other is not coerced to route-server")
			}
			continue
		}
		if role == "route-collector" && (platform == "quagga" || platform == "cisco") {
			skip("declared " + platform + " collector is not supported by the current inventory policy; role is not changed")
			continue
		}
		protocol, validProtocol := c.routerInteger(r, "protocol")
		if !validProtocol || protocol != 4 && protocol != 6 {
			skip("protocol must explicitly declare IPv4 or IPv6")
			continue
		}
		asn, validASN := c.routerInteger(r, "asn")
		if !validASN || asn < 1 || asn > 4294967294 || asn == 23456 || asn == 65535 {
			skip("asn must be an explicit supported nonreserved ASN; no member or settings fallback")
			continue
		}
		name, validName := c.routerText(r, "name")
		if !validName {
			skip("missing or invalid source name")
			continue
		}
		rid, validRID := c.routerText(r, "router_id")
		routerID, parsedRID := routerSourceAddress(rid, true)
		if !validRID || !parsedRID {
			skip("router_id requires a separate explicit unicast IPv4 value even for IPv6; no paired/address fallback")
			continue
		}
		peering, validPeering := c.routerText(r, "peering_ip")
		address, parsedPeering := routerSourceAddress(peering, protocol == 4)
		if !validPeering || !parsedPeering {
			skip("peering_ip is missing, invalid or does not match declared protocol")
			continue
		}
		vlanID, validVLAN := c.routerInteger(r, "vlan_id")
		vlan := c.convertedRecord("vlans", legacyID("vlans", vlanID))
		if !validVLAN || vlanID < 1 || vlan == nil {
			skip("vlan_id must resolve to a successfully converted VLAN")
			continue
		}
		inside, declared := routerVLANContains(vlan, address)
		if !declared {
			skip("converted VLAN has no declared prefix for this address family")
			continue
		}
		if !inside {
			skip("peering_ip is outside the converted VLAN prefixes")
			continue
		}
		flagsOK := true
		for _, field := range []string{"quarantine", "rpki", "bgp_lc"} {
			value, present, valid := c.routerFlag(r, field)
			if !valid || !present {
				skip(field + " safety evidence is absent or invalid; it is not assumed disabled")
				flagsOK = false
				break
			}
			if value {
				skip(field + " is enabled and needs separately reviewed policy inputs; it is not silently disabled")
				flagsOK = false
				break
			}
		}
		if !flagsOK {
			continue
		}
		out := row{"id": legacyID("routers", idOf(r)), "name": name, "platform": platform, "role": role, "asn": asn, "routerId": routerID.String(), "vlanId": legacyID("vlans", vlanID), "rpkiPolicy": "off"}
		if protocol == 4 {
			out["localIpv4"] = address.String()
		} else {
			out["localIpv6"] = address.String()
		}
		rfc, present, valid := c.routerFlag(r, "rfc1997_passthru")
		if !valid {
			skip("rfc1997_passthru is not a declared boolean/0/1 value")
			continue
		}
		if present {
			if platform == "bird2" || platform == "bird3" || platform == "openbgpd" {
				out["rfc1997Passthrough"] = rfc
			} else if rfc {
				skip("explicit RFC1997 passthrough has no supported control on " + platform)
				continue
			} else {
				c.warn("routers", r, "source RFC1997 passthrough is explicitly disabled; no unsupported override added for "+platform)
			}
		} else {
			c.warn("routers", r, "RFC1997 passthrough evidence is absent; no false value fabricated; review native defaults before generating configuration")
		}
		for _, field := range []string{"skip_md5", "pause_updates"} {
			value, present, valid := c.routerFlag(r, field)
			if !valid {
				skip(field + " is not a declared boolean/0/1 value")
				flagsOK = false
				break
			}
			if present {
				c.warn("routers", r, field+"="+strconv.FormatBool(value)+" is source-only; no sessions, credentials or automatic update authority are imported")
			}
		}
		if !flagsOK {
			continue
		}
		key := out["vlanId"].(string) + "/" + address.String()
		fabric, _ := vlan["infrastructureId"].(string)
		routerKey := fabric + "/" + routerID.String()
		identifiers[routerKey]++
		candidates = append(candidates, candidate{r, out, key, routerKey})
	}
	for _, candidate := range candidates {
		if claims[candidate.address] != 1 {
			c.warn("routers", candidate.source, "duplicate peering address in the same VLAN is ambiguous; all conflicting router rows were omitted")
			continue
		}
		if identifiers[candidate.identifier] != 1 {
			c.warn("routers", candidate.source, "duplicate router_id within the same converted infrastructure (including default or paired cross-AF rows); all conflicting candidates omitted without renumbering")
			continue
		}
		c.add("routers", candidate.source, candidate.out)
		platform := candidate.out["platform"].(string)
		if platform == "cisco" || platform == "quagga" {
			c.warn("routers", candidate.source, platform+" route-server identity retained as inert inventory; platform/version-specific rendering and release require separate current capability validation")
		}
		c.warn("routers", candidate.source, "inventory identity only: review old templates, pairing, authentication and routing policy separately; no peers, approval or apply created")
	}
}
