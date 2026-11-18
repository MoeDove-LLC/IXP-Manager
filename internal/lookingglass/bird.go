package lookingglass

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"github.com/moedove/ixp-manager/internal/telemetry"
)

type birdReply struct {
	text  string
	major int
}

// birdc -v suppresses terminal0000; its successful process exit proves the CLI
// response completed (a disconnected/truncated socket exits nonzero). Numeric
// error frames still fail here, and verbose continuation padding is removed.
func decodeBIRD(raw []byte, major int, _ bool) (birdReply, error) {
	if len(raw) == 0 {
		return birdReply{}, ErrFormat
	}
	if len(raw) > MaxOutputBytes {
		return birdReply{}, ErrOutputLimit
	}
	var b strings.Builder
	version := false
	scan := bufio.NewScanner(bytes.NewReader(raw))
	scan.Buffer(make([]byte, 4096), 1<<20)
	for scan.Scan() {
		line := scan.Text()
		if strings.HasPrefix(line, "     ") {
			line = line[5:]
		} else if len(line) >= 5 && (line[4] == ' ' || line[4] == '-') {
			code, e := strconv.Atoi(line[:4])
			if e == nil {
				if code >= 8000 {
					return birdReply{}, ErrUnsupported
				}
				line = line[5:]
				if code == 1 {
					if !strings.HasPrefix(line, fmt.Sprintf("BIRD %d.", major)) {
						return birdReply{}, errors.New("native BIRD major mismatch")
					}
					version = true
				}
			}
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if scan.Err() != nil || !version {
		return birdReply{}, ErrFormat
	}
	return birdReply{b.String(), major}, nil
}
func birdArgs(c Config, words ...string) []string {
	args := []string{"-v", "-r"}
	if c.BirdSocket != "" {
		args = append(args, "-s", c.BirdSocket)
	}
	return append(args, words...)
}
func birdBinary(c Config) string {
	if c.Birdc != "" {
		return c.Birdc
	}
	return "birdc"
}

type nativeIdentity struct {
	Identity
	table    string
	filtered bool
	localAS  uint32
}

func parseBIRDIdentity(raw []byte, q Query, selector string) (nativeIdentity, error) {
	major := 2
	if q.Platform == "bird3" {
		major = 3
	}
	reply, err := decodeBIRD(raw, major, true)
	if err != nil {
		return nativeIdentity{}, err
	}
	var sessions map[string]telemetry.ObservedSession
	if major == 3 {
		sessions, err = telemetry.ParseBIRD3([]byte(reply.text))
	} else {
		sessions, err = telemetry.ParseBIRD2([]byte(reply.text))
	}
	if err != nil {
		return nativeIdentity{}, ErrFormat
	}
	observed, ok := sessions[selector]
	if !ok || observed.NeighborAddress != q.NeighborAddress || observed.NeighborASN != q.NeighborASN {
		return nativeIdentity{}, errors.New("native peer identity mismatch")
	}
	result := nativeIdentity{Identity: Identity{Address: observed.NeighborAddress, ASN: observed.NeighborASN, State: observed.State}}
	channel := ""
	wanted := fmt.Sprintf("ipv%d", q.Family)
	for _, line := range strings.Split(reply.text, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "Local AS:") {
			n, e := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(trim, "Local AS:")), 10, 32)
			if e != nil {
				return nativeIdentity{}, ErrFormat
			}
			result.localAS = uint32(n)
		}
		if strings.HasPrefix(trim, "Channel ") {
			channel = strings.TrimPrefix(trim, "Channel ")
		}
		if channel == wanted && strings.HasPrefix(trim, "Table:") {
			table := strings.TrimSpace(strings.TrimPrefix(trim, "Table:"))
			if !protocolName.MatchString(table) {
				return nativeIdentity{}, ErrFormat
			}
			result.table = table
		}
		if channel == wanted && strings.HasPrefix(trim, "Routes:") && regexp.MustCompile(`\b[0-9]+ filtered,`).MatchString(trim) {
			result.filtered = true
		}
	}
	if result.table == "" {
		return nativeIdentity{}, ErrUnsupported
	}
	return result, nil
}
func BuildBIRDRouteCommand(q Query, selector, table string) ([]string, error) {
	var err error
	q, err = NormalizeQuery(q)
	if err != nil {
		return nil, err
	}
	if !protocolName.MatchString(table) {
		return nil, ErrFormat
	}
	words := []string{"show", "route"}
	if q.Prefix != "" {
		words = append(words, q.Prefix)
	}
	if q.View == ViewTable {
		return append(words, "table", table, "all"), nil
	}
	if !protocolName.MatchString(selector) {
		return nil, ErrFormat
	}
	switch q.View {
	case ViewPrePolicy:
		words = append(words, "import", "table", selector+fmt.Sprintf(".ipv%d", q.Family))
	case ViewAccepted:
		words = append(words, "table", table, "protocol", selector)
	case ViewFiltered:
		words = append(words, "table", table, "filtered", "protocol", selector)
	case ViewExported:
		words = append(words, "table", table, "export", selector)
	default:
		return nil, ErrUnsupported
	}
	return append(words, "all"), nil
}

var birdRouteStart = regexp.MustCompile(`^([0-9a-fA-F:./]+)?\s+(unicast|unreachable|blackhole|prohibit|recursive|none) \[([A-Za-z_][A-Za-z0-9_]*) `)
var birdAttribute = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_. ]*:`)
var birdPair = regexp.MustCompile(`\(([0-9]+),\s*([0-9]+)\)`)
var birdTriple = regexp.MustCompile(`\(([0-9]+),\s*([0-9]+),\s*([0-9]+)\)`)

func ParseBIRDRoutes(raw []byte, q Query, selector string) ([]Route, error) {
	major := 2
	if q.Platform == "bird3" {
		major = 3
	}
	reply, err := decodeBIRD(raw, major, true)
	if err != nil {
		return nil, err
	}
	routes := []Route{}
	var route *Route
	pathSeen := false
	lastPrefix := ""
	continuation := ""
	finish := func() error {
		if route == nil {
			return nil
		}
		if !pathSeen {
			return ErrFormat
		}
		if err := ValidateRoute(*route, q); err != nil {
			return err
		}
		routes = append(routes, *route)
		return nil
	}
	for _, line := range strings.Split(reply.text, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "BIRD ") || strings.HasPrefix(trim, "Table ") {
			continue
		}
		if match := birdRouteStart.FindStringSubmatch(line); match != nil {
			if err := finish(); err != nil {
				return nil, err
			}
			if match[1] != "" {
				lastPrefix = match[1]
			}
			p, e := netip.ParsePrefix(lastPrefix)
			if e != nil {
				return nil, ErrFormat
			}
			if q.View != ViewExported && q.View != ViewTable && match[3] != selector {
				return nil, errors.New("route belongs to another native peer")
			}
			route = &Route{Prefix: p.String(), NextHops: []string{}, ASPath: []PathSegment{}, Communities: [][2]uint32{}, LargeCommunities: [][3]uint32{}}
			pathSeen = false
			continuation = ""
			if q.View == ViewAccepted {
				v := false
				route.Filtered = &v
			}
			if q.View == ViewFiltered {
				v := true
				route.Filtered = &v
			}
			continue
		}
		if route == nil {
			return nil, ErrFormat
		}
		if strings.HasPrefix(trim, "BGP.as_path:") || strings.HasPrefix(trim, "bgp_path:") {
			if pathSeen {
				return nil, ErrFormat
			}
			pathSeen = true
			route.ASPath, err = ParseASPath(strings.TrimSpace(strings.SplitN(trim, ":", 2)[1]))
			if err != nil {
				return nil, err
			}
			continuation = ""
			continue
		}
		if strings.HasPrefix(trim, "BGP.next_hop:") || strings.HasPrefix(trim, "bgp_next_hop:") {
			for _, rawIP := range strings.Fields(strings.SplitN(trim, ":", 2)[1]) {
				ip, e := canonicalAddress(rawIP)
				if e != nil {
					return nil, ErrFormat
				}
				if !containsString(route.NextHops, ip) {
					route.NextHops = append(route.NextHops, ip)
				}
			}
			continuation = ""
			continue
		}
		community := ""
		if strings.HasPrefix(trim, "BGP.community:") || strings.HasPrefix(trim, "bgp_community:") {
			continuation = "community"
			community = strings.TrimSpace(strings.SplitN(trim, ":", 2)[1])
		}
		if strings.HasPrefix(trim, "BGP.large_community:") || strings.HasPrefix(trim, "bgp_large_community:") {
			continuation = "large"
			community = strings.TrimSpace(strings.SplitN(trim, ":", 2)[1])
		}
		if strings.HasPrefix(line, "\t\t") && continuation != "" {
			community = trim
		}
		if community != "" {
			pattern := birdPair
			if continuation == "large" {
				pattern = birdTriple
			}
			matches := pattern.FindAllStringSubmatch(community, -1)
			if strings.TrimSpace(pattern.ReplaceAllString(community, "")) != "" {
				return nil, ErrFormat
			}
			for _, m := range matches {
				values := []uint32{}
				for _, text := range m[1:] {
					value, e := strconv.ParseUint(text, 10, 32)
					if e != nil {
						return nil, ErrFormat
					}
					values = append(values, uint32(value))
				}
				if len(values) == 2 {
					route.Communities = append(route.Communities, [2]uint32{values[0], values[1]})
				} else {
					route.LargeCommunities = append(route.LargeCommunities, [3]uint32{values[0], values[1], values[2]})
				}
			}
			continue
		}
		continuation = ""
		// Only known attribute-shaped data and native nexthop display lines may be
		// ignored; none is copied into the result. A new outer format fails closed.
		if line != "" && (line[0] == ' ' || line[0] == '\t') && (birdAttribute.MatchString(trim) || strings.HasPrefix(trim, "via ") || strings.HasPrefix(trim, "dev ")) {
			continue
		}
		return nil, ErrFormat
	}
	if err := finish(); err != nil {
		return nil, err
	}
	return routes, nil
}
func containsString(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
