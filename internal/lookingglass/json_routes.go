package lookingglass

import (
	"bytes"
	"encoding/json"
	"io"
	"net/netip"
	"sort"
	"strconv"
	"strings"
)

func jsonObject(raw []byte) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, ErrFormat
	}
	if len(raw) > MaxOutputBytes {
		return nil, ErrOutputLimit
	}
	var v map[string]json.RawMessage
	d := json.NewDecoder(bytes.NewReader(raw))
	if d.Decode(&v) != nil || d.Decode(new(any)) != io.EOF || v == nil {
		return nil, ErrFormat
	}
	for _, key := range []string{"warning", "error", "errors"} {
		if _, ok := v[key]; ok {
			return nil, ErrUnsupported
		}
	}
	if status, ok := v["status"]; ok && string(status) != `"OK"` {
		return nil, ErrUnsupported
	}
	return v, nil
}
func textField(o map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := o[key]
	if !ok {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}
func uintField(o map[string]json.RawMessage, key string) (uint32, bool) {
	raw, ok := o[key]
	if !ok {
		return 0, false
	}
	var value json.Number
	if json.Unmarshal(raw, &value) != nil {
		return 0, false
	}
	n, e := strconv.ParseUint(value.String(), 10, 32)
	return uint32(n), e == nil
}
func communityValue(raw string, large bool) ([]uint32, error) {
	known := map[string]string{"no-export": "65535:65281", "no-advertise": "65535:65282", "no-export-subconfed": "65535:65283", "local-as": "65535:65283", "no-peer": "65535:65284", "nopeer": "65535:65284", "graceful-shutdown": "65535:0", "blackhole": "65535:666", "llgr-stale": "65535:6", "no-llgr": "65535:7", "accept-own": "65535:1"}
	key := strings.ReplaceAll(strings.ToLower(raw), "_", "-")
	if value, ok := known[key]; ok && !large {
		raw = value
	}
	parts := strings.Split(raw, ":")
	want := 2
	if large {
		want = 3
	}
	if len(parts) != want {
		return nil, ErrFormat
	}
	out := []uint32{}
	for _, part := range parts {
		n, e := strconv.ParseUint(part, 10, 32)
		if e != nil || (!large && n > 65535) {
			return nil, ErrFormat
		}
		out = append(out, uint32(n))
	}
	return out, nil
}
func addCommunities(route *Route, values []string, large bool) error {
	if len(values) > 256 {
		return ErrOutputLimit
	}
	for _, text := range values {
		v, e := communityValue(text, large)
		if e != nil {
			return e
		}
		if large {
			route.LargeCommunities = append(route.LargeCommunities, [3]uint32{v[0], v[1], v[2]})
		} else {
			route.Communities = append(route.Communities, [2]uint32{v[0], v[1]})
		}
	}
	return nil
}
func frrCommunity(raw json.RawMessage) ([]string, error) {
	if raw == nil {
		return []string{}, nil
	}
	var o struct {
		String *string  `json:"string"`
		List   []string `json:"list"`
	}
	if json.Unmarshal(raw, &o) != nil {
		return nil, ErrFormat
	}
	if o.String != nil {
		return strings.Fields(*o.String), nil
	}
	if o.List != nil {
		return o.List, nil
	}
	return nil, ErrFormat
}
func routeFlags(r *Route, q Query) {
	if q.View == ViewAccepted {
		v := false
		r.Filtered = &v
	}
	if q.View == ViewFiltered {
		v := true
		r.Filtered = &v
	}
}
func appendNextHop(r *Route, value string) error {
	if value == "0.0.0.0" || value == "::" {
		return nil
	}
	ip, e := canonicalAddress(value)
	if e != nil {
		return ErrFormat
	}
	if !containsString(r.NextHops, ip) {
		r.NextHops = append(r.NextHops, ip)
	}
	return nil
}

func BuildFRRRouteCommand(q Query) ([]string, error) {
	var err error
	q, err = NormalizeQuery(q)
	if err != nil {
		return nil, err
	}
	af := "ipv4"
	if q.Family == 6 {
		af = "ipv6"
	}
	base := "show bgp view MOEDOVE " + af + " unicast "
	switch q.View {
	case ViewTable:
		if q.Prefix != "" {
			return []string{"-d", "bgpd", "-c", base + q.Prefix + " json"}, nil
		}
		return []string{"-d", "bgpd", "-c", base + "detail json"}, nil
	case ViewAccepted:
		if q.Prefix != "" {
			return []string{"-d", "bgpd", "-c", base + q.Prefix + " json"}, nil
		}
		return []string{"-d", "bgpd", "-c", base + "neighbors " + q.NeighborAddress + " routes json"}, nil
	case ViewPrePolicy, ViewExported, ViewFiltered:
		word := map[string]string{ViewPrePolicy: "received-routes", ViewExported: "advertised-routes", ViewFiltered: "filtered-routes"}[q.View]
		// FRR's detail modifier may evaluate inbound policy; raw pre-policy and
		// exported summaries are filtered by exact prefix locally after bounded read.
		suffix := " json"
		if q.View == ViewFiltered {
			if q.Prefix != "" {
				suffix = " " + q.Prefix + " json"
			} else {
				suffix = " detail json"
			}
		}
		return []string{"-d", "bgpd", "-c", base + "neighbors " + q.NeighborAddress + " " + word + suffix}, nil
	}
	return nil, ErrUnsupported
}
func parseFRRPath(raw json.RawMessage, prefix string, q Query, detailed bool) (Route, bool, error) {
	o, e := jsonObject(raw)
	if e != nil {
		return Route{}, false, e
	}
	r := Route{Prefix: prefix, NextHops: []string{}, ASPath: []PathSegment{}}
	source, sourceKnown := textField(o, "peerId")
	if p, ok := o["peer"]; ok {
		peer, e := jsonObject(p)
		if e != nil {
			return r, false, e
		}
		source, sourceKnown = textField(peer, "peerId")
	}
	if q.View == ViewAccepted || q.View == ViewFiltered || q.View == ViewPrePolicy {
		if sourceKnown {
			address, e := canonicalAddress(source)
			if e != nil || address != q.NeighborAddress {
				return r, false, nil
			}
		} else if detailed {
			return r, false, ErrFormat
		}
	}
	path, present := textField(o, "path")
	if value, ok := o["aspath"]; ok {
		a, e := jsonObject(value)
		if e != nil {
			return r, false, e
		}
		path, present = textField(a, "string")
	}
	if !present {
		return r, false, ErrFormat
	}
	r.ASPath, e = ParseASPath(path)
	if e != nil {
		return r, false, e
	}
	hopKnown := false
	for _, key := range []string{"nextHop", "nextHopGlobal"} {
		if value, ok := textField(o, key); ok {
			hopKnown = true
			if e = appendNextHop(&r, value); e != nil {
				return r, false, e
			}
		}
	}
	if raw, ok := o["nexthops"]; ok {
		var values []struct {
			IP *string `json:"ip"`
		}
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return r, false, ErrFormat
		}
		hopKnown = true
		for _, v := range values {
			if v.IP == nil {
				return r, false, ErrFormat
			}
			if e = appendNextHop(&r, *v.IP); e != nil {
				return r, false, e
			}
		}
	}
	if !hopKnown {
		return r, false, ErrFormat
	}
	if detailed {
		r.Communities = [][2]uint32{}
		r.LargeCommunities = [][3]uint32{}
		for key, large := range map[string]bool{"community": false, "largeCommunity": true} {
			values, e := frrCommunity(o[key])
			if e != nil {
				return r, false, e
			}
			if e = addCommunities(&r, values, large); e != nil {
				return r, false, e
			}
		}
	} else {
		r.UnavailableAttributes = []string{"communities", "largeCommunities"}
	}
	routeFlags(&r, q)
	return r, true, nil
}
func ParseFRRRoutes(raw []byte, q Query) ([]Route, error) {
	root, e := jsonObject(raw)
	if e != nil {
		return nil, e
	}
	detailed := q.View == ViewTable || q.View == ViewFiltered || (q.View == ViewAccepted && q.Prefix != "")
	key := "routes"
	if q.View == ViewPrePolicy || q.View == ViewFiltered {
		key = "receivedRoutes"
	}
	if q.View == ViewExported {
		key = "advertisedRoutes"
	}
	networks := map[string]json.RawMessage{}
	found := false
	if payload, ok := root[key]; ok {
		if json.Unmarshal(payload, &networks) != nil || networks == nil {
			return nil, ErrFormat
		}
		found = true
	}
	if !found && q.Prefix != "" {
		if _, ok := root["paths"]; ok {
			prefix, known := textField(root, "prefix")
			if !known {
				prefix = q.Prefix
			}
			networks[prefix] = raw
			found = true
		}
	}
	if !found {
		return nil, ErrFormat
	}
	keys := []string{}
	for p := range networks {
		keys = append(keys, p)
	}
	sort.Strings(keys)
	routes := []Route{}
	for _, original := range keys {
		p, e := netip.ParsePrefix(original)
		if e != nil || p != p.Masked() {
			return nil, ErrFormat
		}
		prefix := p.String()
		if q.Prefix != "" && prefix != q.Prefix {
			continue
		}
		if p.Addr().Is4() != (q.Family == 4) {
			return nil, ErrFormat
		}
		payload := networks[original]
		paths := []json.RawMessage{}
		if len(bytes.TrimSpace(payload)) > 0 && bytes.TrimSpace(payload)[0] == '[' {
			if json.Unmarshal(payload, &paths) != nil {
				return nil, ErrFormat
			}
		} else {
			o, e := jsonObject(payload)
			if e != nil {
				return nil, e
			}
			if nested, ok := o["paths"]; ok {
				if json.Unmarshal(nested, &paths) != nil {
					return nil, ErrFormat
				}
			} else {
				paths = []json.RawMessage{payload}
			}
		}
		for _, path := range paths {
			o, e := jsonObject(path)
			if e != nil {
				return nil, e
			}
			if _, ok := o["path"]; !ok {
				if _, ok = o["aspath"]; !ok {
					if _, header := o["prefix"]; header {
						continue
					}
					return nil, ErrFormat
				}
			}
			route, keep, e := parseFRRPath(path, prefix, q, detailed)
			if e != nil {
				return nil, e
			}
			if !keep {
				continue
			}
			if e = ValidateRoute(route, q); e != nil {
				return nil, e
			}
			routes = append(routes, route)
		}
	}
	return routes, nil
}
func BuildOpenBGPDRouteCommand(q Query) ([]string, error) {
	var err error
	q, err = NormalizeQuery(q)
	if err != nil {
		return nil, err
	}
	args := []string{"-j", "show", "rib"}
	switch q.View {
	case ViewAccepted:
		args = append(args, "table", "MOEDOVE")
	case ViewPrePolicy:
		args = append(args, "in")
	case ViewExported:
		args = append(args, "out")
	default:
		return nil, ErrUnsupported
	}
	args = append(args, "neighbor", q.NeighborAddress)
	if q.Family == 4 {
		args = append(args, "inet")
	} else {
		args = append(args, "inet6")
	}
	if q.Prefix != "" {
		args = append(args, q.Prefix)
	}
	return append(args, "detail"), nil
}
func ParseOpenBGPDRoutes(raw []byte, q Query) ([]Route, error) {
	root, e := jsonObject(raw)
	if e != nil {
		return nil, e
	}
	var entries []json.RawMessage
	if payload, ok := root["rib"]; ok {
		if json.Unmarshal(payload, &entries) != nil || entries == nil {
			return nil, ErrFormat
		}
	} else if len(root) != 0 {
		return nil, ErrFormat
	}
	routes := []Route{}
	for _, raw := range entries {
		o, e := jsonObject(raw)
		if e != nil {
			return nil, e
		}
		prefix, ok := textField(o, "prefix")
		if !ok {
			return nil, ErrFormat
		}
		p, e := netip.ParsePrefix(prefix)
		if e != nil || p != p.Masked() {
			return nil, ErrFormat
		}
		prefix = p.String()
		if q.Prefix != "" && prefix != q.Prefix {
			continue
		}
		neighbor, e := jsonObject(o["neighbor"])
		if e != nil {
			return nil, e
		}
		address, ok := textField(neighbor, "remote_addr")
		if !ok {
			return nil, ErrFormat
		}
		canonical, e := canonicalAddress(address)
		if e != nil || canonical != q.NeighborAddress {
			return nil, ErrFormat
		}
		path, ok := textField(o, "aspath")
		if !ok {
			return nil, ErrFormat
		}
		segments, e := ParseASPath(path)
		if e != nil {
			return nil, e
		}
		r := Route{Prefix: prefix, ASPath: segments, NextHops: []string{}, Communities: [][2]uint32{}, LargeCommunities: [][3]uint32{}}
		hop, ok := textField(o, "exit_nexthop")
		if !ok {
			return nil, ErrFormat
		}
		if e = appendNextHop(&r, hop); e != nil {
			return nil, e
		}
		for _, key := range []string{"communities", "large_communities"} {
			if payload, ok := o[key]; ok {
				var values []string
				if json.Unmarshal(payload, &values) != nil {
					return nil, ErrFormat
				}
				if e = addCommunities(&r, values, key == "large_communities"); e != nil {
					return nil, e
				}
			}
		}
		routeFlags(&r, q)
		if q.View == ViewAccepted {
			if v, ok := o["filtered"]; ok && string(v) == "true" {
				return nil, ErrFormat
			}
		}
		if e = ValidateRoute(r, q); e != nil {
			return nil, e
		}
		routes = append(routes, r)
	}
	return routes, nil
}
