package policy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Client interface {
	Query(context.Context, string) (string, error)
}
type WhoisClient struct {
	Address          string
	Sources          []string
	Timeout          time.Duration
	MaxResponseBytes int64
}

func (c WhoisClient) Source() string {
	source := "whois://" + c.Address
	if len(c.Sources) > 0 {
		source += "?sources=" + strings.Join(c.Sources, ",")
	}
	return source
}
func (c WhoisClient) Query(ctx context.Context, query string) (string, error) {
	if strings.ContainsAny(query, "\r\n\x00") || len(query) > 1024 {
		return "", errors.New("invalid WHOIS query")
	}
	host, port, e := net.SplitHostPort(c.Address)
	if e != nil || host == "" {
		return "", errors.New("explicit WHOIS server host:port required")
	}
	n, e := strconv.Atoi(port)
	if e != nil || n < 1 || n > 65535 {
		return "", errors.New("invalid WHOIS port")
	}
	if len(c.Sources) > 32 {
		return "", errors.New("at most32 IRR registry sources allowed")
	}
	for _, source := range c.Sources {
		if !regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`).MatchString(source) {
			return "", errors.New("invalid IRR source name")
		}
	}
	if len(c.Sources) > 0 {
		query = "-s " + strings.Join(c.Sources, ",") + " " + query
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if timeout > 60*time.Second {
		return "", errors.New("WHOIS timeout exceeds 60 seconds")
	}
	limit := c.MaxResponseBytes
	if limit <= 0 {
		limit = 8 << 20
	}
	if limit > 32<<20 {
		return "", errors.New("WHOIS response limit exceeds 32 MiB")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(ctx, "tcp", c.Address)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err = io.WriteString(conn, query+"\r\n"); err != nil {
		return "", err
	}
	b, err := io.ReadAll(io.LimitReader(conn, limit+1))
	if err != nil {
		return "", err
	}
	if int64(len(b)) > limit {
		return "", errors.New("WHOIS response exceeded configured byte limit")
	}
	return string(b), nil
}

type Limits struct {
	MaxDepth, MaxQueries, MaxASNs, MaxRoutes int
	MaxResponseBytes, MaxTotalBytes          int64
}

func DefaultLimits() Limits {
	return Limits{MaxDepth: 8, MaxQueries: 1024, MaxASNs: 512, MaxRoutes: MaxIRRRoutes, MaxResponseBytes: 8 << 20, MaxTotalBytes: 64 << 20}
}

var asNumber = regexp.MustCompile(`^AS([0-9]+)$`)
var asSetPart = regexp.MustCompile(`^AS-[A-Z0-9][A-Z0-9_-]{0,63}$`)

func normalizeASSet(raw string) (string, error) {
	name := strings.ToUpper(strings.TrimSpace(raw))
	if name == "" || len(name) > 256 {
		return "", errors.New("invalid AS-SET name")
	}
	if asNumber.MatchString(name) {
		if _, err := parseASN(name); err != nil {
			return "", err
		}
		return name, nil
	}
	hasSet := false
	for _, part := range strings.Split(name, ":") {
		if asSetPart.MatchString(part) {
			hasSet = true
			continue
		}
		if asNumber.MatchString(part) {
			if _, err := parseASN(part); err != nil {
				return "", err
			}
			continue
		}
		return "", errors.New("invalid AS-SET component")
	}
	if !hasSet {
		return "", errors.New("AS-SET name requires an AS- component")
	}
	return name, nil
}
func parseASN(raw string) (uint32, error) {
	match := asNumber.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(raw)))
	if match == nil {
		return 0, errors.New("ASN must use AS<number> notation")
	}
	n, err := strconv.ParseUint(match[1], 10, 32)
	if err != nil || !validOrigin(uint32(n)) {
		return 0, errors.New("invalid origin ASN")
	}
	return uint32(n), nil
}

type rpslObject map[string][]string

func parseRPSL(input string) ([]rpslObject, error) {
	scanner := bufio.NewScanner(strings.NewReader(input))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	objects := []rpslObject{}
	current := rpslObject{}
	last := ""
	flush := func() {
		if len(current) > 0 {
			objects = append(objects, current)
		}
		current = rpslObject{}
		last = ""
	}
	for scanner.Scan() {
		raw := strings.TrimRight(scanner.Text(), "\r")
		line := strings.TrimSpace(raw)
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "%") {
			lower := strings.ToLower(line)
			if strings.Contains(lower, "error") && !strings.Contains(lower, "no entries found") && !strings.Contains(lower, "no match") {
				return nil, errors.New("IRR server reported an error")
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		if at := strings.Index(line, "#"); at >= 0 {
			line = strings.TrimSpace(line[:at])
		}
		if strings.HasPrefix(raw, " ") || strings.HasPrefix(raw, "\t") || strings.HasPrefix(raw, "+") {
			if last == "" {
				return nil, errors.New("RPSL continuation without an attribute")
			}
			value := strings.TrimSpace(strings.TrimPrefix(line, "+"))
			values := current[last]
			values[len(values)-1] += " " + value
			current[last] = values
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		field = strings.ToLower(strings.TrimSpace(field))
		if !ok || !regexp.MustCompile(`^[a-z][a-z0-9-]*$`).MatchString(field) {
			return nil, errors.New("invalid RPSL attribute")
		}
		current[field] = append(current[field], strings.TrimSpace(value))
		last = field
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	flush()
	return objects, nil
}
func one(obj rpslObject, field string) (string, error) {
	values := obj[field]
	if len(values) != 1 {
		return "", fmt.Errorf("RPSL %s must occur exactly once", field)
	}
	return values[0], nil
}
func ParseRoutes(input string) ([]Route, error) {
	objects, err := parseRPSL(input)
	if err != nil {
		return nil, err
	}
	routes := []Route{}
	seen := map[string]bool{}
	for _, obj := range objects {
		family := 4
		field := "route"
		if len(obj["route6"]) > 0 {
			family = 6
			field = "route6"
		}
		if len(obj["route"]) == 0 && len(obj["route6"]) == 0 {
			continue
		}
		if len(obj["route"]) > 0 && len(obj["route6"]) > 0 {
			return nil, errors.New("RPSL object mixes route and route6")
		}
		raw, e := one(obj, field)
		if e != nil {
			return nil, e
		}
		p, e := prefix(raw)
		if e != nil || (p.Addr().Is4() != (family == 4)) {
			return nil, errors.New("RPSL route prefix/family invalid")
		}
		origin, e := one(obj, "origin")
		if e != nil {
			return nil, e
		}
		asn, e := parseASN(origin)
		if e != nil {
			return nil, e
		}
		key := p.String() + "/" + strconv.FormatUint(uint64(asn), 10)
		if !seen[key] {
			routes = append(routes, Route{Prefix: p.String(), OriginASN: asn})
			seen[key] = true
		}
	}
	sortRoutes(routes)
	return routes, nil
}
func sortRoutes(routes []Route) {
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Prefix == routes[j].Prefix {
			return routes[i].OriginASN < routes[j].OriginASN
		}
		return routes[i].Prefix < routes[j].Prefix
	})
}
func listValues(values []string) []string {
	result := []string{}
	for _, line := range values {
		result = append(result, strings.Fields(strings.ReplaceAll(line, ",", " "))...)
	}
	return result
}

func CollectIRR(ctx context.Context, client Client, asSet string, ttl time.Duration) (IRRSnapshot, error) {
	return CollectIRRWithLimits(ctx, client, asSet, ttl, DefaultLimits())
}
func CollectIRRWithLimits(ctx context.Context, client Client, asSet string, ttl time.Duration, limits Limits) (IRRSnapshot, error) {
	var empty IRRSnapshot
	if client == nil || ttl <= 0 || ttl > 24*time.Hour {
		return empty, errors.New("collector client and TTL within 24 hours required")
	}
	if limits.MaxDepth < 1 || limits.MaxDepth > 32 || limits.MaxQueries < 1 || limits.MaxQueries > 10000 || limits.MaxASNs < 1 || limits.MaxASNs > 4096 || limits.MaxRoutes < 1 || limits.MaxRoutes > MaxIRRRoutes || limits.MaxResponseBytes < 1 || limits.MaxResponseBytes > 32<<20 || limits.MaxTotalBytes < 1 || limits.MaxTotalBytes > 256<<20 {
		return empty, errors.New("invalid IRR collection bounds")
	}
	root, err := normalizeASSet(asSet)
	if err != nil {
		return empty, err
	}
	queries := 0
	total := int64(0)
	query := func(q string) ([]rpslObject, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		queries++
		if queries > limits.MaxQueries {
			return nil, errors.New("IRR query limit exceeded")
		}
		raw, err := client.Query(ctx, q)
		if err != nil {
			return nil, err
		}
		total += int64(len(raw))
		if int64(len(raw)) > limits.MaxResponseBytes || total > limits.MaxTotalBytes {
			return nil, errors.New("IRR response byte limit exceeded")
		}
		return parseRPSL(raw)
	}
	active := map[string]bool{}
	done := map[string]bool{}
	asns := map[uint32]bool{}
	var expand func(string, int) error
	expand = func(raw string, depth int) error {
		name, err := normalizeASSet(raw)
		if err != nil {
			return err
		}
		if asNumber.MatchString(name) {
			asn, _ := parseASN(name)
			asns[asn] = true
			if len(asns) > limits.MaxASNs {
				return errors.New("IRR AS expansion limit exceeded")
			}
			return nil
		}
		if depth > limits.MaxDepth {
			return errors.New("IRR AS-SET depth limit exceeded")
		}
		if active[name] {
			return fmt.Errorf("IRR AS-SET cycle at %s; no partial snapshot produced", name)
		}
		if done[name] {
			return nil
		}
		active[name] = true
		defer delete(active, name)
		objects, err := query("-r -T as-set " + name)
		if err != nil {
			return err
		}
		members := []string{}
		refs := map[string]bool{}
		found := false
		authoritativeSource := ""
		for _, obj := range objects {
			if len(obj["as-set"]) == 0 {
				continue
			}
			key, e := one(obj, "as-set")
			if e != nil {
				return e
			}
			if strings.ToUpper(key) != name {
				continue
			}
			if found {
				return fmt.Errorf("AS-SET %s is ambiguous across returned objects; select a single authoritative IRR source", name)
			}
			if values := obj["source"]; len(values) > 0 {
				value, e := one(obj, "source")
				if e != nil {
					return e
				}
				authoritativeSource = strings.ToUpper(value)
			}
			found = true
			members = append(members, listValues(obj["members"])...)
			members = append(members, listValues(obj["mp-members"])...)
			for _, ref := range listValues(obj["mbrs-by-ref"]) {
				refs[strings.ToUpper(ref)] = true
			}
		}
		if !found {
			return fmt.Errorf("AS-SET %s not found", name)
		}
		// Inverse membership is accepted only for the named set and authorized
		// maintainer (or explicit ANY), rather than trusting every member-of result.
		if len(refs) > 0 {
			indirect, e := query("-r -T aut-num,as-set -i member-of " + name)
			if e != nil {
				return e
			}
			for _, obj := range indirect {
				if authoritativeSource != "" {
					value, e := one(obj, "source")
					if e != nil || !strings.EqualFold(value, authoritativeSource) {
						continue
					}
				}

				belongs := false
				for _, v := range listValues(obj["member-of"]) {
					if strings.EqualFold(v, name) {
						belongs = true
					}
				}
				permitted := refs["ANY"]
				for _, mnt := range listValues(obj["mnt-by"]) {
					if refs[strings.ToUpper(mnt)] {
						permitted = true
					}
				}
				if !belongs || !permitted {
					continue
				}
				field := "as-set"
				if len(obj["aut-num"]) > 0 {
					field = "aut-num"
				}
				member, e := one(obj, field)
				if e != nil {
					return e
				}
				members = append(members, member)
			}
		}
		for _, member := range members {
			if err = expand(member, depth+1); err != nil {
				return err
			}
		}
		done[name] = true
		return nil
	}
	if err = expand(root, 0); err != nil {
		return empty, err
	}
	numbers := make([]uint32, 0, len(asns))
	for asn := range asns {
		numbers = append(numbers, asn)
	}
	sort.Slice(numbers, func(i, j int) bool { return numbers[i] < numbers[j] })
	routes := []Route{}
	seen := map[string]bool{}
	for _, asn := range numbers {
		objects, e := query("-r -T route,route6 -i origin AS" + strconv.FormatUint(uint64(asn), 10))
		if e != nil {
			return empty, e
		}
		for _, obj := range objects {
			if len(obj["route"]) == 0 && len(obj["route6"]) == 0 {
				continue
			}
			// Reuse the same strict parser on serialized relevant object fields.
			var block strings.Builder
			for _, field := range []string{"route", "route6", "origin"} {
				for _, value := range obj[field] {
					fmt.Fprintf(&block, "%s: %s\n", field, value)
				}
			}
			parsed, e := ParseRoutes(block.String())
			if e != nil {
				return empty, e
			}
			for _, route := range parsed {
				if route.OriginASN != asn {
					return empty, errors.New("IRR origin query returned an unrelated origin ASN")
				}
				key := fmt.Sprintf("%s/%d", route.Prefix, asn)
				if !seen[key] {
					routes = append(routes, route)
					seen[key] = true
				}
				if len(routes) > limits.MaxRoutes {
					return empty, errors.New("IRR route limit exceeded")
				}
			}
		}
	}
	sortRoutes(routes)
	now := time.Now().UTC()
	source := "operator-provided IRR client"
	if labelled, ok := client.(interface{ Source() string }); ok {
		source = labelled.Source()
	}
	result := IRRSnapshot{Version: 1, FetchedAt: now, ExpiresAt: now.Add(ttl), Source: source, ASSet: root, ASNs: numbers, Routes: routes}
	if err = ValidateIRRSnapshot(result, now); err != nil {
		return empty, err
	}
	return result, nil
}
