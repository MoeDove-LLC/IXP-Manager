package lookingglass

import (
	"errors"
	"net/netip"
	"regexp"
	"strings"
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)
var revision = regexp.MustCompile(`^[a-f0-9]{64}$`)
var protocolName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
var ErrUnsupported = errors.New("requested native route view is unsupported")
var ErrFormat = errors.New("native route format is not recognized")
var ErrOutputLimit = errors.New("native route output exceeded its limit")

func canonicalAddress(raw string) (string, error) {
	a, e := netip.ParseAddr(raw)
	if e != nil || a.Zone() != "" || a.Is4In6() || a.IsUnspecified() || a.IsMulticast() {
		return "", errors.New("invalid unicast address")
	}
	return a.String(), nil
}
func NormalizeQuery(q Query) (Query, error) {
	if !identifier.MatchString(q.RouterID) || !revision.MatchString(q.OwnershipRevision) {
		return q, errors.New("query requires router and ownership identity")
	}
	switch q.Platform {
	case "bird2", "bird3", "frr", "openbgpd":
	default:
		return q, ErrUnsupported
	}
	switch q.View {
	case ViewAccepted, ViewFiltered, ViewPrePolicy, ViewExported, ViewTable:
	default:
		return q, ErrUnsupported
	}
	if q.MaxRows == 0 {
		q.MaxRows = 50
	}
	if q.MaxRows < 1 || q.MaxRows > MaxRows {
		return q, errors.New("maxRows must be 1..200")
	}
	if q.ExpectedRouterID != "" {
		a, e := netip.ParseAddr(q.ExpectedRouterID)
		if e != nil || !a.Is4() || a.IsUnspecified() || a.IsMulticast() {
			return q, errors.New("invalid expected router ID")
		}
		q.ExpectedRouterID = a.String()
	}
	if q.View == ViewTable {
		if q.PeerID != "" || q.NeighborAddress != "" || q.NeighborASN != 0 {
			return q, errors.New("table query may not select a peer")
		}
		a, e := netip.ParseAddr(q.ExpectedRouterID)
		if e != nil || !a.Is4() || a.IsUnspecified() || a.IsMulticast() || q.ExpectedRouterASN == 0 || q.ExpectedRouterASN == ^uint32(0) {
			return q, errors.New("table query requires actual router ID and ASN")
		}
		q.ExpectedRouterID = a.String()
	} else {
		if !identifier.MatchString(q.PeerID) || q.NeighborASN == 0 || q.NeighborASN == ^uint32(0) {
			return q, errors.New("query requires actual peer ID and ASN")
		}
		address, e := canonicalAddress(q.NeighborAddress)
		if e != nil {
			return q, e
		}
		q.NeighborAddress = address
		if q.Family == 0 {
			a, _ := netip.ParseAddr(address)
			q.Family = 6
			if a.Is4() {
				q.Family = 4
			}
		}
	}
	if q.Family != 4 && q.Family != 6 {
		return q, errors.New("query family must be 4 or6")
	}
	if q.Prefix != "" {
		p, e := netip.ParsePrefix(q.Prefix)
		if e != nil || p != p.Masked() || p.Addr().Is4In6() || p.Addr().Is4() != (q.Family == 4) {
			return q, errors.New("query prefix must be canonical and match the selected family")
		}
		q.Prefix = p.String()
	}
	return q, nil
}

func ValidateRoute(route Route, q Query) error {
	p, e := netip.ParsePrefix(route.Prefix)
	if e != nil || p != p.Masked() || p.Addr().Is4In6() || p.Addr().Is4() != (q.Family == 4) {
		return ErrFormat
	}
	if q.Prefix != "" && route.Prefix != q.Prefix {
		return errors.New("native result escaped the requested prefix")
	}
	if len(route.UnavailableAttributes) > 2 {
		return ErrFormat
	}
	seenMissing := map[string]bool{}
	for _, name := range route.UnavailableAttributes {
		if seenMissing[name] {
			return ErrFormat
		}
		seenMissing[name] = true
		switch name {
		case "communities":
			if route.Communities != nil {
				return ErrFormat
			}
		case "largeCommunities":
			if route.LargeCommunities != nil {
				return ErrFormat
			}
		default:
			return ErrFormat
		}
	}
	if len(route.NextHops) > 16 || len(route.ASPath) > 64 || len(route.Communities) > 256 || len(route.LargeCommunities) > 256 {
		return ErrOutputLimit
	}
	for _, ip := range route.NextHops {
		if canonical, e := canonicalAddress(ip); e != nil || canonical != ip {
			return ErrFormat
		}
	}
	count := 0
	for _, segment := range route.ASPath {
		switch segment.Type {
		case "sequence", "set", "confed-sequence", "confed-set":
		default:
			return ErrFormat
		}
		if len(segment.ASNs) == 0 {
			return ErrFormat
		}
		count += len(segment.ASNs)
		if count > 512 {
			return ErrOutputLimit
		}
	}
	for _, community := range route.Communities {
		if community[0] > 65535 || community[1] > 65535 {
			return ErrFormat
		}
	}
	if q.View == ViewAccepted && route.Filtered != nil && *route.Filtered {
		return ErrFormat
	}
	if q.View == ViewFiltered && (route.Filtered == nil || !*route.Filtered) {
		return ErrFormat
	}
	return nil
}

func ValidateResult(result Result, expected Query) error {
	q, err := NormalizeQuery(result.Query)
	if err != nil {
		return err
	}
	if q != result.Query {
		return errors.New("result query is not normalized")
	}
	normalExpected, err := NormalizeQuery(expected)
	if err != nil || q != normalExpected {
		return errors.New("result does not match its leased query")
	}
	switch result.Identity.State {
	case "", "unknown", "Idle", "Connect", "Active", "OpenSent", "OpenConfirm", "Established":
	default:
		return errors.New("unknown identity state")
	}
	if result.Identity.RouterID != "" {
		address, err := netip.ParseAddr(result.Identity.RouterID)
		if err != nil || !address.Is4() || address.String() != result.Identity.RouterID {
			return errors.New("invalid observed router ID")
		}
	}
	if (result.Identity.Address != "" && result.Identity.Address != q.NeighborAddress) || (result.Identity.ASN != 0 && result.Identity.ASN != q.NeighborASN) || (result.Identity.RouterID != "" && result.Identity.RouterID != q.ExpectedRouterID) || (result.Identity.RouterASN != 0 && result.Identity.RouterASN != q.ExpectedRouterASN) {
		return errors.New("result contains identity outside its leased target")
	}
	if result.ObservedAt.IsZero() || len(result.Routes) > q.MaxRows {
		return errors.New("result timestamp or row limit is invalid")
	}
	switch result.Status {
	case "ok":
		if !result.Complete || result.Truncated || result.ErrorCode != "" {
			return errors.New("inconsistent complete result")
		}
	case "truncated":
		if result.Complete || !result.Truncated || result.ErrorCode != "" {
			return errors.New("inconsistent truncated result")
		}
	case "unsupported", "error":
		if result.Complete || result.Truncated || len(result.Routes) != 0 || !validErrorCode(result.ErrorCode) {
			return errors.New("inconsistent failed result")
		}
		return nil
	default:
		return errors.New("unknown route result status")
	}
	if q.View == ViewTable {
		if result.Identity.RouterID != q.ExpectedRouterID || result.Identity.RouterASN != q.ExpectedRouterASN {
			return errors.New("router identity did not match query")
		}
	} else if result.Identity.Address != q.NeighborAddress || result.Identity.ASN != q.NeighborASN {
		return errors.New("neighbor identity did not match query")
	}
	for _, route := range result.Routes {
		if err := ValidateRoute(route, q); err != nil {
			return err
		}
	}
	return nil
}

func validErrorCode(code string) bool {
	switch code {
	case "invalid-query", "invalid-local-config", "target-mismatch", "table-disabled", "unmapped-peer", "mapping-mismatch", "identity-mismatch", "unsupported-view", "retention-unavailable", "native-query-failed", "identity-changed", "invalid-native-result", "output-limit", "format-unknown", "timeout", "identity-unavailable", "native-view-unavailable":
		return true
	}
	return false
}

func Capabilities() []Capability {
	return []Capability{
		{"bird2", []string{ViewAccepted, ViewFiltered, ViewPrePolicy, ViewExported, ViewTable}, true, "BIRD numeric read-only control replies; filtered availability requires retained-filtered counter evidence; pre-policy requires native import table. Table requires local opt-in and matching router/AS identity."},
		{"bird3", []string{ViewAccepted, ViewFiltered, ViewPrePolicy, ViewExported, ViewTable}, true, "BIRD3 numeric read-only control replies; same runtime retention and identity requirements, verified against BIRD3 rather than BIRD2 output."},
		{"frr", []string{ViewAccepted, ViewFiltered, ViewPrePolicy, ViewExported, ViewTable}, true, "Fixed MOEDOVE view and native JSON; retained views fail when soft reconfiguration is unavailable. Table requires local opt-in and matching identity."},
		{"openbgpd", []string{ViewAccepted, ViewPrePolicy, ViewExported}, true, "Native Adj-RIB-In and outgoing/managed RIB views; filtered retention and independent table router identity cannot be proven from this native status interface."},
	}
}

func Supported(platform, view string) bool {
	for _, c := range Capabilities() {
		if c.Platform == platform {
			for _, v := range c.Views {
				if v == view {
					return true
				}
			}
		}
	}
	return false
}
func resultFailure(q Query, status, code string) Result {
	return Result{Query: q, Status: status, ErrorCode: code, Routes: []Route{}}
}
func safeToken(token string) bool {
	return token != "" && len(token) <= 256 && !strings.ContainsAny(token, "\x00\r\n")
}
