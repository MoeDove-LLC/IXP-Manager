package routing

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

type PrefixOrigin struct {
	Prefix    string `json:"prefix"`
	OriginASN uint32 `json:"originAsn"`
}
type originGroup struct {
	ASN      uint32
	Prefixes []string
	Name     string
}

func validatePrefixOrigins(peer Peer, ipv4 bool) error {
	if len(peer.AllowedRoutes) == 0 {
		return nil
	}
	if len(peer.AllowedRoutes) > 10000 {
		return fmt.Errorf("peer %s: at most10000 prefix-origin pairs allowed", peer.ID)
	}
	union := map[string]bool{}
	allowed := map[string]bool{}
	for _, raw := range peer.AllowedPrefixes {
		p, _ := netip.ParsePrefix(raw)
		allowed[p.String()] = true
	}
	for _, route := range peer.AllowedRoutes {
		p, err := netip.ParsePrefix(route.Prefix)
		if err != nil || p != p.Masked() || p.Addr().Is4() != ipv4 || p.Addr().Is4In6() || !validASN(route.OriginASN) || !allowed[p.String()] {
			return fmt.Errorf("peer %s: prefix-origin mapping must use canonical allowed prefixes and valid origin ASNs", peer.ID)
		}
		union[p.String()] = true
	}
	if len(union) != len(allowed) {
		return fmt.Errorf("peer %s: prefix-origin mapping must cover the entire exact allowlist", peer.ID)
	}
	return nil
}
func originGroups(peer Peer) []originGroup {
	if len(peer.AllowedRoutes) == 0 {
		return []originGroup{{Prefixes: canonicalPrefixes(peer), Name: peerName(peer)}}
	}
	groups := map[uint32]map[string]bool{}
	for _, route := range peer.AllowedRoutes {
		if groups[route.OriginASN] == nil {
			groups[route.OriginASN] = map[string]bool{}
		}
		p, _ := netip.ParsePrefix(route.Prefix)
		groups[route.OriginASN][p.String()] = true
	}
	out := make([]originGroup, 0, len(groups))
	for asn, prefixes := range groups {
		g := originGroup{ASN: asn, Name: fmt.Sprintf("%s_o%d", peerName(peer), asn)}
		for prefix := range prefixes {
			g.Prefixes = append(g.Prefixes, prefix)
		}
		sort.Strings(g.Prefixes)
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ASN < out[j].ASN })
	return out
}
func birdOriginGuard(peer Peer) string {
	if len(peer.AllowedRoutes) == 0 {
		return ""
	}
	parts := []string{}
	for _, g := range originGroups(peer) {
		parts = append(parts, fmt.Sprintf("(bgp_path.last = %d && net ~ [ %s ])", g.ASN, strings.Join(g.Prefixes, ", ")))
	}
	return "      if !(" + strings.Join(parts, " || ") + ") then reject;\n"
}
func frrOriginPattern(peerASN, originASN uint32) string {
	pattern := fmt.Sprintf("^%d(_[0-9]+){0,62}_%d$", peerASN, originASN)
	if peerASN == originASN {
		pattern = fmt.Sprintf("^(%d|%d(_[0-9]+){0,62}_%d)$", peerASN, peerASN, originASN)
	}
	return pattern
}
func junosOriginPattern(peerASN, originASN uint32) string {
	if originASN == 0 {
		return fmt.Sprintf("%d .{0,63}", peerASN)
	}
	pattern := fmt.Sprintf("%d .{0,62} %d", peerASN, originASN)
	if peerASN == originASN {
		return fmt.Sprintf("(%d|%s)", peerASN, pattern)
	}
	return pattern
}
