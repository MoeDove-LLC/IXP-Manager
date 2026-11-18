package lookingglass

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/telemetry"
)

var errIdentity = errors.New("native routing identity does not match")

func frrArgs(c Config, args []string) []string {
	if c.VtySocket != "" {
		return append([]string{"--vty_socket", c.VtySocket}, args...)
	}
	return args
}
func frrBinary(c Config) string {
	if c.Vtysh != "" {
		return c.Vtysh
	}
	return "vtysh"
}
func openArgs(c Config, args []string) []string {
	socket := c.BgpSocket
	if socket == "" {
		socket = "/run/moedove-openbgpd/bgpd.sock"
	}
	return append([]string{"-s", socket}, args...)
}
func openBinary(c Config) string {
	if c.Bgpctl != "" {
		return c.Bgpctl
	}
	return "bgpctl"
}

func readIdentity(ctx context.Context, c Config, q Query, selector string) (nativeIdentity, error) {
	switch c.Platform {
	case "bird2", "bird3":
		major := 2
		if c.Platform == "bird3" {
			major = 3
		}
		actualID := ""
		if q.ExpectedRouterID != "" {
			raw, e := nativeCommand(ctx, birdBinary(c), birdArgs(c, "show", "status")...)
			if e != nil {
				return nativeIdentity{}, e
			}
			status, e := decodeBIRD(raw, major, false)
			if e != nil {
				return nativeIdentity{}, e
			}
			if !strings.Contains(string(raw), "0013 Daemon is up and running") {
				return nativeIdentity{}, ErrUnsupported
			}
			for _, line := range strings.Split(status.text, "\n") {
				if strings.HasPrefix(line, "Router ID is ") {
					actualID = strings.TrimPrefix(line, "Router ID is ")
				}
			}
			if actualID != q.ExpectedRouterID {
				return nativeIdentity{}, errIdentity
			}
		}
		if q.View != ViewTable {
			raw, e := nativeCommand(ctx, birdBinary(c), birdArgs(c, "show", "protocols", "all", selector)...)
			if e != nil {
				return nativeIdentity{}, e
			}
			identity, e := parseBIRDIdentity(raw, q, selector)
			if e != nil {
				return nativeIdentity{}, e
			}
			identity.RouterID = actualID
			if q.ExpectedRouterASN != 0 {
				if identity.localAS == 0 {
					return nativeIdentity{}, ErrUnsupported
				}
				if identity.localAS != q.ExpectedRouterASN {
					return nativeIdentity{}, errIdentity
				}
				identity.RouterASN = identity.localAS
			}
			return identity, nil
		}
		var raw []byte
		var e error
		raw, e = nativeCommand(ctx, birdBinary(c), birdArgs(c, "show", "protocols", "all")...)
		if e != nil {
			return nativeIdentity{}, e
		}
		protocols, e := decodeBIRD(raw, major, true)
		if e != nil {
			return nativeIdentity{}, e
		}
		count := 0
		for _, line := range strings.Split(protocols.text, "\n") {
			trim := strings.TrimSpace(line)
			if strings.HasPrefix(trim, "Local AS:") {
				n, e := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(trim, "Local AS:")), 10, 32)
				if e != nil || uint32(n) != q.ExpectedRouterASN {
					return nativeIdentity{}, errIdentity
				}
				count++
			}
		}
		if count == 0 {
			return nativeIdentity{}, ErrUnsupported
		}
		return nativeIdentity{Identity: Identity{RouterID: actualID, RouterASN: q.ExpectedRouterASN}, table: fmt.Sprintf("master%d", q.Family)}, nil
	case "frr":
		af := "ipv4"
		key := "ipv4Unicast"
		if q.Family == 6 {
			af = "ipv6"
			key = "ipv6Unicast"
		}
		raw, e := nativeCommand(ctx, frrBinary(c), frrArgs(c, []string{"-d", "bgpd", "-c", "show bgp view MOEDOVE " + af + " unicast summary json"})...)
		if e != nil {
			return nativeIdentity{}, e
		}
		root, e := jsonObject(raw)
		if e != nil {
			return nativeIdentity{}, e
		}
		section := root
		if nested, ok := root[key]; ok {
			section, e = jsonObject(nested)
			if e != nil {
				return nativeIdentity{}, e
			}
		}
		routerID, known := textField(section, "routerId")
		asn, asKnown := uintField(section, "as")
		view, viewKnown := textField(section, "vrfName")
		if !known || !asKnown || !viewKnown {
			return nativeIdentity{}, ErrUnsupported
		}
		if view != "MOEDOVE" || (q.ExpectedRouterID != "" && routerID != q.ExpectedRouterID) || (q.ExpectedRouterASN != 0 && asn != q.ExpectedRouterASN) {
			return nativeIdentity{}, errIdentity
		}
		if q.View == ViewTable {
			return nativeIdentity{Identity: Identity{RouterID: routerID, RouterASN: asn}, table: "MOEDOVE"}, nil
		}
		sessions, e := telemetry.ParseFRRSummary(raw)
		if e != nil {
			return nativeIdentity{}, ErrFormat
		}
		peer, ok := sessions[q.NeighborAddress]
		if !ok || peer.NeighborAddress != q.NeighborAddress || peer.NeighborASN != q.NeighborASN {
			return nativeIdentity{}, errIdentity
		}
		identity := Identity{Address: peer.NeighborAddress, ASN: peer.NeighborASN, State: peer.State}
		if q.ExpectedRouterID != "" {
			identity.RouterID = routerID
		}
		if q.ExpectedRouterASN != 0 {
			identity.RouterASN = asn
		}
		return nativeIdentity{Identity: identity, table: "MOEDOVE"}, nil
	case "openbgpd":
		if q.View == ViewTable {
			return nativeIdentity{}, ErrUnsupported
		}
		raw, e := nativeCommand(ctx, openBinary(c), openArgs(c, []string{"-j", "show", "neighbor", q.NeighborAddress})...)
		if e != nil {
			return nativeIdentity{}, e
		}
		peers, e := telemetry.ParseOpenBGPDNeighbors(raw)
		if e != nil {
			return nativeIdentity{}, ErrFormat
		}
		peer, ok := peers[q.NeighborAddress]
		if !ok || peer.NeighborASN != q.NeighborASN {
			return nativeIdentity{}, errIdentity
		}
		return nativeIdentity{Identity: Identity{Address: peer.NeighborAddress, ASN: peer.NeighborASN, State: peer.State}}, nil
	}
	return nativeIdentity{}, ErrUnsupported
}

// Execute checks actual identity before AND after the query. Complete means a
// native listing finished within bounds, not an atomic snapshot of a live RIB.
func Execute(ctx context.Context, c Config, q Query) Result {
	normalized, e := NormalizeQuery(q)
	result := Result{Query: normalized, ObservedAt: time.Now().UTC(), Status: "error", Routes: []Route{}}
	fail := func(err error, code string) Result {
		result.Status = "error"
		result.ErrorCode = code
		result.Complete = false
		result.Truncated = false
		result.Routes = []Route{}
		result.ObservedAt = time.Now().UTC()
		if errors.Is(err, ErrUnsupported) {
			result.Status = "unsupported"
			result.ErrorCode = "unsupported-view"
		}
		if errors.Is(err, ErrOutputLimit) {
			result.ErrorCode = "output-limit"
		}
		if errors.Is(err, ErrFormat) {
			result.ErrorCode = "format-unknown"
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			result.ErrorCode = "timeout"
		}
		return result
	}
	if e != nil {
		return fail(e, "invalid-query")
	}
	q = normalized
	if e = ValidateConfig(c); e != nil {
		return fail(e, "invalid-local-config")
	}
	if c.RouterID != q.RouterID || c.Platform != q.Platform {
		return fail(errIdentity, "target-mismatch")
	}
	if !Supported(q.Platform, q.View) {
		return fail(ErrUnsupported, "unsupported-view")
	}
	if q.View == ViewTable && !c.AllowTable {
		return fail(ErrUnsupported, "table-disabled")
	}
	selector := ""
	if q.View != ViewTable {
		selector = c.Peers[q.PeerID]
		if selector == "" {
			return fail(errIdentity, "unmapped-peer")
		}
		if c.Platform != "bird2" && c.Platform != "bird3" {
			ip, e := canonicalAddress(selector)
			if e != nil || ip != q.NeighborAddress {
				return fail(errIdentity, "mapping-mismatch")
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	before, e := readIdentity(ctx, c, q, selector)
	if e != nil {
		return fail(e, "identity-mismatch")
	}
	result.Identity = before.Identity
	if (q.Platform == "bird2" || q.Platform == "bird3") && q.View == ViewFiltered && !before.filtered {
		return fail(ErrUnsupported, "retention-unavailable")
	}
	var raw []byte
	var routes []Route
	switch q.Platform {
	case "bird2", "bird3":
		var args []string
		args, e = BuildBIRDRouteCommand(q, selector, before.table)
		if e == nil {
			raw, e = nativeCommand(ctx, birdBinary(c), birdArgs(c, args...)...)
		}
		if e == nil {
			routes, e = ParseBIRDRoutes(raw, q, selector)
		}
	case "frr":
		var args []string
		args, e = BuildFRRRouteCommand(q)
		if e == nil {
			raw, e = nativeCommand(ctx, frrBinary(c), frrArgs(c, args)...)
		}
		if e == nil {
			routes, e = ParseFRRRoutes(raw, q)
		}
	case "openbgpd":
		var args []string
		args, e = BuildOpenBGPDRouteCommand(q)
		if e == nil {
			raw, e = nativeCommand(ctx, openBinary(c), openArgs(c, args)...)
		}
		if e == nil {
			routes, e = ParseOpenBGPDRoutes(raw, q)
		}
	}
	if e != nil {
		return fail(e, "native-query-failed")
	}
	after, e := readIdentity(ctx, c, q, selector)
	if e != nil {
		return fail(e, "identity-mismatch")
	}
	if before != after {
		return fail(errIdentity, "identity-changed")
	}
	sort.SliceStable(routes, func(i, j int) bool {
		a, _ := netip.ParsePrefix(routes[i].Prefix)
		b, _ := netip.ParsePrefix(routes[j].Prefix)
		if a.Addr() != b.Addr() {
			return a.Addr().Less(b.Addr())
		}
		return a.Bits() < b.Bits()
	})
	result.Status = "ok"
	result.Complete = true
	result.ObservedAt = time.Now().UTC()
	result.Routes = routes
	if len(routes) > q.MaxRows {
		result.Routes = routes[:q.MaxRows]
		result.Status = "truncated"
		result.Complete = false
		result.Truncated = true
	}
	if e = ValidateResult(result, q); e != nil {
		return fail(e, "invalid-native-result")
	}
	return result
}
