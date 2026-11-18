package app

import (
	"fmt"
	"github.com/moedove/ixp-manager/internal/policy"
	"math"
	"net/netip"
	"sort"
	"time"
)

func init() { Schemas["peers"]["allowedRoutes"] = Field{Type: "route-bindings"} }
func validateOriginBindings(r Record) error {
	raw, exists := r["allowedRoutes"]
	if !exists || raw == nil {
		return nil
	}
	items, ok := raw.([]any)
	if !ok || len(items) > 10000 {
		return fmt.Errorf("allowedRoutes must contain at most 10000 prefix-origin pairs")
	}
	if len(items) == 0 {
		return nil
	}
	address, _ := netip.ParseAddr(str(r, "address"))
	prefixes := map[string]bool{}
	for _, v := range r["allowedPrefixes"].([]any) {
		prefixes[v.(string)] = true
	}
	paired := map[string]bool{}
	seen := map[string]bool{}
	for _, v := range items {
		m, ok := v.(map[string]any)
		if !ok || len(m) != 2 {
			return fmt.Errorf("allowedRoutes entries require prefix and originAsn only")
		}
		p, ok := m["prefix"].(string)
		if !ok {
			return fmt.Errorf("prefix must be text")
		}
		asn, ok := m["originAsn"].(float64)
		if !ok || asn < 1 || asn > 4294967294 || asn != math.Trunc(asn) || asn == 23456 || asn == 65535 {
			return fmt.Errorf("invalid bound origin ASN")
		}
		prefix, err := netip.ParsePrefix(p)
		if err == nil {
			p = prefix.String()
			m["prefix"] = p
		}
		if err != nil || prefix != prefix.Masked() || prefix.Addr().Is4In6() || prefix.Addr().Is4() != address.Is4() || prefix.Bits() == 0 || !prefix.Addr().IsGlobalUnicast() || !prefixes[p] {
			return fmt.Errorf("origin binding must match the peer family and explicit allowlist")
		}
		key := fmt.Sprintf("%s/%d", p, uint32(asn))
		if seen[key] {
			return fmt.Errorf("duplicate prefix-origin pair")
		}
		seen[key] = true
		paired[p] = true
	}
	if len(paired) != len(prefixes) {
		return fmt.Errorf("every allowed prefix requires at least one bound origin")
	}
	return nil
}
func candidateBindings(snapshot policy.IRRSnapshot, peer Record, all bool) ([]policy.Route, error) {
	routes, err := policy.CandidateRoutes(snapshot, uint32(number(peer, "asn")), all, time.Now())
	if err != nil {
		return nil, err
	}
	address, err := netip.ParseAddr(str(peer, "address"))
	if err != nil {
		return nil, err
	}
	out := []policy.Route{}
	seen := map[string]bool{}
	for _, route := range routes {
		if !all && route.OriginASN != uint32(number(peer, "asn")) {
			continue
		}
		prefix, err := netip.ParsePrefix(route.Prefix)
		if err != nil {
			return nil, err
		}
		if prefix.Addr().Is4() != address.Is4() {
			continue
		}
		if prefix.Bits() == 0 || !prefix.Addr().IsGlobalUnicast() {
			return nil, fmt.Errorf("IRR candidate contains an unsuitable managed prefix")
		}
		key := fmt.Sprintf("%s/%d", route.Prefix, route.OriginASN)
		if !seen[key] {
			out = append(out, route)
			seen[key] = true
		}
		if len(out) > 10000 {
			return nil, fmt.Errorf("candidate exceeds 10000 prefix-origin pairs")
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Prefix != out[j].Prefix {
			return out[i].Prefix < out[j].Prefix
		}
		return out[i].OriginASN < out[j].OriginASN
	})
	return out, nil
}
func bindingPrefixes(routes []policy.Route) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, r := range routes {
		if !seen[r.Prefix] {
			result = append(result, r.Prefix)
			seen[r.Prefix] = true
		}
	}
	sort.Strings(result)
	return result
}
func samePrefixSet(a, b any) bool {
	left, ok := a.([]any)
	if !ok {
		return false
	}
	right, ok := b.([]any)
	if !ok {
		return false
	}
	set := map[any]bool{}
	for _, p := range left {
		if _, ok := p.(string); !ok {
			return false
		}
		parsed, err := netip.ParsePrefix(p.(string))
		if err != nil {
			return false
		}
		set[parsed.String()] = true
	}
	other := map[any]bool{}
	for _, p := range right {
		if _, ok := p.(string); !ok {
			return false
		}
		parsed, err := netip.ParsePrefix(p.(string))
		if err != nil {
			return false
		}
		other[parsed.String()] = true
	}
	if len(set) != len(other) {
		return false
	}
	for p := range set {
		if !other[p] {
			return false
		}
	}
	return true
}
