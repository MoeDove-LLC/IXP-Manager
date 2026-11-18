// Package policy evaluates typed, expiring IRR/RPKI snapshots. Collecting or
// evaluating policy never approves a router revision or applies configuration.
package policy

import (
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"time"
)

const MaxROAs = 2000000
const MaxIRRRoutes = 250000

type Route struct {
	Prefix    string   `json:"prefix"`
	OriginASN uint32   `json:"originAsn"`
	ASPath    []uint32 `json:"asPath,omitempty"`
}
type ROA struct {
	Prefix    string `json:"prefix"`
	MaxLength int    `json:"maxLength"`
	ASN       uint32 `json:"asn"`
}
type ROASnapshot struct {
	Version   int       `json:"version"`
	FetchedAt time.Time `json:"fetchedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Source    string    `json:"source"`
	ROAs      []ROA     `json:"roas"`
}
type IRRSnapshot struct {
	Version   int       `json:"version"`
	FetchedAt time.Time `json:"fetchedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Source    string    `json:"source"`
	ASSet     string    `json:"asSet"`
	ASNs      []uint32  `json:"asns"`
	Routes    []Route   `json:"routes"`
}
type Validation string

const (
	Valid       Validation = "valid"
	Invalid     Validation = "invalid"
	NotFound    Validation = "not-found"
	Unavailable Validation = "unavailable"
)

func fresh(version int, fetched, expires, now time.Time, source string) error {
	if version != 1 || fetched.IsZero() || expires.IsZero() || source == "" || len(source) > 2048 {
		return errors.New("snapshot metadata is invalid")
	}
	if !expires.After(fetched) || expires.Sub(fetched) > 24*time.Hour {
		return errors.New("snapshot lifetime must be positive and at most 24 hours")
	}
	if fetched.After(now.Add(5 * time.Minute)) {
		return errors.New("snapshot was fetched in the future")
	}
	if !now.Before(expires) {
		return errors.New("policy snapshot is stale; refresh and review before use")
	}
	return nil
}
func prefix(raw string) (netip.Prefix, error) {
	p, e := netip.ParsePrefix(raw)
	if e != nil || p.Addr().Is4In6() || p != p.Masked() {
		return netip.Prefix{}, fmt.Errorf("prefix %q must be canonical IPv4 or IPv6 CIDR", raw)
	}
	return p, nil
}
func validOrigin(asn uint32) bool {
	return asn != 0 && asn != 23456 && asn != 65535 && asn != 4294967295
}
func validateOriginInput(r Route) error {
	if _, err := prefix(r.Prefix); err != nil {
		return err
	}
	if !validOrigin(r.OriginASN) {
		return errors.New("invalid route origin ASN")
	}
	return nil
}
func ValidateRoute(r Route) error {
	if err := validateOriginInput(r); err != nil {
		return err
	}
	if len(r.ASPath) > 64 {
		return errors.New("AS path exceeds 64 hops")
	}
	for _, asn := range r.ASPath {
		if !validOrigin(asn) {
			return errors.New("AS path contains an invalid ASN")
		}
	}
	return nil
}

type ROAIndex struct {
	entries map[netip.Prefix][]ROA
	expires time.Time
}

func NewROAIndex(s ROASnapshot, now time.Time) (*ROAIndex, error) {
	if err := fresh(s.Version, s.FetchedAt, s.ExpiresAt, now, s.Source); err != nil {
		return nil, err
	}
	if s.ROAs == nil {
		return nil, errors.New("ROA snapshot requires an explicit roas array (empty is allowed)")
	}
	if len(s.ROAs) > MaxROAs {
		return nil, errors.New("ROA snapshot exceeds record limit")
	}
	index := &ROAIndex{entries: map[netip.Prefix][]ROA{}, expires: s.ExpiresAt}
	for _, roa := range s.ROAs {
		p, err := prefix(roa.Prefix)
		if err != nil {
			return nil, err
		}
		if roa.MaxLength < p.Bits() || roa.MaxLength > p.Addr().BitLen() || roa.ASN == 4294967295 {
			return nil, errors.New("ROA maxLength or ASN invalid")
		}
		index.entries[p] = append(index.entries[p], roa)
	}
	return index, nil
}
func (i *ROAIndex) Validate(route Route, now time.Time) (Validation, error) {
	if i == nil || !now.Before(i.expires) {
		return Unavailable, errors.New("RPKI cache unavailable or expired")
	}
	if err := validateOriginInput(route); err != nil {
		return Unavailable, err
	}
	p, _ := prefix(route.Prefix)
	covered := false
	// A more specific contradictory VRP must not override ANY other matching
	// authorization. Evaluate every covering prefix, not longest match only.
	for bits := 0; bits <= p.Bits(); bits++ {
		cover := netip.PrefixFrom(p.Addr(), bits).Masked()
		for _, roa := range i.entries[cover] {
			covered = true
			if roa.ASN == route.OriginASN && p.Bits() <= roa.MaxLength {
				return Valid, nil
			}
		}
	}
	if covered {
		return Invalid, nil
	}
	return NotFound, nil
}
func ValidateOrigin(s ROASnapshot, r Route, now time.Time) (Validation, error) {
	index, err := NewROAIndex(s, now)
	if err != nil {
		return Unavailable, err
	}
	return index.Validate(r, now)
}
func ValidateIRRSnapshot(s IRRSnapshot, now time.Time) error {
	if err := fresh(s.Version, s.FetchedAt, s.ExpiresAt, now, s.Source); err != nil {
		return err
	}
	if _, err := normalizeASSet(s.ASSet); err != nil {
		return err
	}
	if s.ASNs == nil || s.Routes == nil {
		return errors.New("IRR snapshot requires explicit asns and routes arrays")
	}
	if len(s.ASNs) > 4096 || len(s.Routes) > MaxIRRRoutes {
		return errors.New("IRR snapshot exceeds limits")
	}
	members := map[uint32]bool{}
	for _, asn := range s.ASNs {
		if !validOrigin(asn) || members[asn] {
			return errors.New("invalid or duplicate IRR ASN")
		}
		members[asn] = true
	}
	for _, route := range s.Routes {
		if err := ValidateRoute(route); err != nil {
			return err
		}
		if !members[route.OriginASN] || len(route.ASPath) > 0 {
			return errors.New("IRR route origin must belong to the expanded AS-SET and contain no AS path")
		}
	}
	return nil
}
func CandidatePrefixes(s IRRSnapshot, originASN uint32, now time.Time) ([]string, error) {
	if err := ValidateIRRSnapshot(s, now); err != nil {
		return nil, err
	}
	if !validOrigin(originASN) {
		return nil, errors.New("invalid requested origin ASN")
	}
	result := []string{}
	seen := map[string]bool{}
	for _, r := range s.Routes {
		if r.OriginASN == originASN && !seen[r.Prefix] {
			result = append(result, r.Prefix)
			seen[r.Prefix] = true
		}
	}
	sort.Strings(result)
	return result, nil
}

type Request struct {
	AllowedRoutes      []Route      `json:"allowedRoutes,omitempty"`
	PeerASN            uint32       `json:"peerAsn"`
	Routes             []Route      `json:"routes"`
	AllowedPrefixes    []string     `json:"allowedPrefixes"`
	IRR                *IRRSnapshot `json:"irr,omitempty"`
	RPKI               *ROASnapshot `json:"rpki,omitempty"`
	RequireIRR         bool         `json:"requireIrr"`
	RejectRPKINotFound bool         `json:"rejectRpkiNotFound"`
}
type Decision struct {
	Prefix        string     `json:"prefix"`
	OriginASN     uint32     `json:"originAsn"`
	Accepted      bool       `json:"accepted"`
	RPKI          Validation `json:"rpki"`
	Reasons       []string   `json:"reasons"`
	PathValidated bool       `json:"pathValidated"`
}

func Simulate(request Request, now time.Time) ([]Decision, error) {
	if !validOrigin(request.PeerASN) || len(request.Routes) > 1000 || len(request.AllowedPrefixes) > 10000 {
		return nil, errors.New("invalid peer or simulation exceeds limits")
	}
	allow := map[string]bool{}
	for _, raw := range request.AllowedPrefixes {
		p, err := prefix(raw)
		if err != nil {
			return nil, err
		}
		allow[p.String()] = true
	}
	if len(request.AllowedRoutes) > 10000 {
		return nil, errors.New("reviewed prefix-origin mapping exceeds limit")
	}
	paired := map[string]map[uint32]bool{}
	for _, route := range request.AllowedRoutes {
		if err := ValidateRoute(route); err != nil {
			return nil, err
		}
		if len(route.ASPath) > 0 || !allow[route.Prefix] {
			return nil, errors.New("reviewed origin mapping must match the exact prefix allowlist")
		}
		if paired[route.Prefix] == nil {
			paired[route.Prefix] = map[uint32]bool{}
		}
		paired[route.Prefix][route.OriginASN] = true
	}
	if len(request.AllowedRoutes) > 0 && len(paired) != len(allow) {
		return nil, errors.New("reviewed prefix-origin mapping is incomplete")
	}
	irr := map[string]map[uint32]bool{}
	if request.RequireIRR && request.IRR == nil {
		return nil, errors.New("required IRR snapshot is missing")
	}
	if request.IRR != nil {
		if err := ValidateIRRSnapshot(*request.IRR, now); err != nil {
			return nil, err
		}
		for _, r := range request.IRR.Routes {
			if irr[r.Prefix] == nil {
				irr[r.Prefix] = map[uint32]bool{}
			}
			irr[r.Prefix][r.OriginASN] = true
		}
	}
	var index *ROAIndex
	var err error
	if request.RejectRPKINotFound && request.RPKI == nil {
		return nil, errors.New("strict RPKI policy requires a current snapshot")
	}
	if request.RPKI != nil {
		index, err = NewROAIndex(*request.RPKI, now)
		if err != nil {
			return nil, err
		}
	}
	decisions := make([]Decision, 0, len(request.Routes))
	for _, route := range request.Routes {
		d := Decision{Prefix: route.Prefix, OriginASN: route.OriginASN, RPKI: Unavailable, Reasons: []string{}}
		if err = validateOriginInput(route); err != nil {
			d.Reasons = append(d.Reasons, err.Error())
			decisions = append(decisions, d)
			continue
		}
		if err = ValidateRoute(route); err != nil {
			d.Reasons = append(d.Reasons, err.Error())
		}
		if len(route.ASPath) == 0 {
			if route.OriginASN != request.PeerASN {
				d.Reasons = append(d.Reasons, "transit origin requires an explicit AS path")
			}
		} else {
			d.PathValidated = true
			if route.ASPath[0] != request.PeerASN {
				d.Reasons = append(d.Reasons, "first AS does not match the peer")
			}
			if route.ASPath[len(route.ASPath)-1] != route.OriginASN {
				d.Reasons = append(d.Reasons, "last AS does not match the claimed origin")
			}
		}
		p, _ := prefix(route.Prefix)
		if p.Bits() == 0 || !p.Addr().IsGlobalUnicast() {
			d.Reasons = append(d.Reasons, "default or non-unicast prefix is not allowed by managed route-server profiles")
		}
		if !allow[p.String()] {
			d.Reasons = append(d.Reasons, "prefix is absent from the exact reviewed allowlist")
		}
		if len(request.AllowedRoutes) > 0 && !paired[p.String()][route.OriginASN] {
			d.Reasons = append(d.Reasons, "prefix/origin pair is absent from the reviewed mapping")
		}
		if request.RequireIRR && !irr[p.String()][route.OriginASN] {
			d.Reasons = append(d.Reasons, "prefix/origin pair is not authorized by the IRR snapshot")
		}
		if index != nil {
			d.RPKI, err = index.Validate(route, now)
			if err != nil {
				return nil, err
			}
			if d.RPKI == Invalid || (request.RejectRPKINotFound && d.RPKI == NotFound) {
				d.Reasons = append(d.Reasons, "route rejected by explicit RPKI policy: "+string(d.RPKI))
			}
		}
		d.Accepted = len(d.Reasons) == 0
		decisions = append(decisions, d)
	}
	return decisions, nil
}

// CandidateRoutes proposes explicit prefix-origin authorizations. Full AS-SET
// expansion is opt-in and never collapses away the corresponding origin ASN.
func CandidateRoutes(s IRRSnapshot, originASN uint32, includeASSetOrigins bool, now time.Time) ([]Route, error) {
	if err := ValidateIRRSnapshot(s, now); err != nil {
		return nil, err
	}
	if !validOrigin(originASN) {
		return nil, errors.New("invalid peer ASN")
	}
	member := false
	for _, asn := range s.ASNs {
		if asn == originASN {
			member = true
		}
	}
	if includeASSetOrigins && !member {
		return nil, errors.New("peer ASN must belong to the expanded AS-SET before reviewing downstream origins")
	}
	out := []Route{}
	seen := map[string]bool{}
	for _, route := range s.Routes {
		if !includeASSetOrigins && route.OriginASN != originASN {
			continue
		}
		key := fmt.Sprintf("%s/%d", route.Prefix, route.OriginASN)
		if !seen[key] {
			out = append(out, Route{Prefix: route.Prefix, OriginASN: route.OriginASN})
			seen[key] = true
		}
	}
	sortRoutes(out)
	return out, nil
}
