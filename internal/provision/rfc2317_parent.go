package provision

import (
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxRFC2317ParentBytes = 1 << 20
const maxRFC2317DumpBytes = 8 << 20

type parentToken struct {
	raw        string
	start, end int
	quoted     bool
}
type parentStatement struct {
	tokens       []parentToken
	omittedOwner bool
}
type parentRR struct {
	owner, kind string
	ttl         uint32
	data        []parentToken
}
type parentZone struct {
	records                []parentRR
	serial                 uint32
	serialStart, serialEnd int
}

// Tokenize only a deliberately bounded master-file subset. Active directives
// are inspected before any native parser sees the file. Escaped names, includes,
// generators, nested parentheses and multiline quoted strings are not accepted.
func parentStatements(raw string, limit int) ([]parentStatement, error) {
	if len(raw) == 0 || len(raw) > limit || !utf8.ValidString(raw) {
		return nil, errors.New("parent zone exceeds size/UTF-8 bounds")
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c == 127 || c < 32 && c != '\t' && c != '\r' && c != '\n' {
			return nil, errors.New("unsupported control in parent zone")
		}
	}
	out := []parentStatement{}
	current := parentStatement{}
	depth := 0
	lineStart, indented := true, false
	total := 0
	finish := func() error {
		if len(current.tokens) > 0 {
			out = append(out, current)
			current = parentStatement{}
			if len(out) > 10002 {
				return errors.New("parent zone exceeds record bound")
			}
		}
		return nil
	}
	for i := 0; i < len(raw); {
		c := raw[i]
		if c == ' ' || c == '\t' {
			if lineStart {
				indented = true
			}
			i++
			continue
		}
		if c == '\r' || c == '\n' {
			if c == '\r' {
				if i+1 >= len(raw) || raw[i+1] != '\n' {
					return nil, errors.New("bare CR in parent zone")
				}
				i++
			}
			i++
			if depth == 0 {
				if err := finish(); err != nil {
					return nil, err
				}
			}
			lineStart, indented = true, false
			continue
		}
		if c == ';' {
			for i < len(raw) && raw[i] != '\n' && raw[i] != '\r' {
				i++
			}
			continue
		}
		if c < 32 || c == 127 {
			return nil, errors.New("unsupported control in parent zone")
		}
		if c == '(' {
			if depth != 0 || len(current.tokens) == 0 {
				return nil, errors.New("unsupported parenthesis structure")
			}
			depth = 1
			lineStart = false
			i++
			continue
		}
		if c == ')' {
			if depth != 1 {
				return nil, errors.New("unbalanced parenthesis")
			}
			depth = 0
			lineStart = false
			i++
			continue
		}
		if len(current.tokens) == 0 {
			current.omittedOwner = indented
		}
		lineStart = false
		start := i
		quoted := c == '"'
		if quoted {
			i++
			closed := false
			for i < len(raw) {
				if raw[i] == '\n' || raw[i] == '\r' || raw[i] == 0 {
					return nil, errors.New("multiline/control quoted parent text unsupported")
				}
				if raw[i] == '\\' {
					i++
					if i >= len(raw) || raw[i] == '\n' || raw[i] == '\r' {
						return nil, errors.New("quoted continuation unsupported")
					}
					i++
					continue
				}
				if raw[i] == '"' {
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, errors.New("unterminated parent text")
			}
			if i < len(raw) && !strings.ContainsRune(" \t\r\n;()", rune(raw[i])) {
				return nil, errors.New("adjacent quoted/unquoted tokens unsupported")
			}
		} else {
			for i < len(raw) && !strings.ContainsRune(" \t\r\n;()", rune(raw[i])) {
				if raw[i] == '\\' || raw[i] == '"' || raw[i] < 32 || raw[i] == 127 {
					return nil, errors.New("escaped names or mixed quoted tokens unsupported")
				}
				i++
			}
		}
		if i-start > 65536 {
			return nil, errors.New("parent token exceeds bound")
		}
		current.tokens = append(current.tokens, parentToken{raw[start:i], start, i, quoted})
		total++
		if total > 100000 {
			return nil, errors.New("parent token count exceeds bound")
		}
	}
	if depth != 0 {
		return nil, errors.New("unterminated parenthesis")
	}
	if err := finish(); err != nil {
		return nil, err
	}
	return out, nil
}
func dnsTTL(value string) (uint32, bool) {
	if value == "" {
		return 0, false
	}
	var total uint64
	for i := 0; i < len(value); {
		start := i
		for i < len(value) && value[i] >= '0' && value[i] <= '9' {
			i++
		}
		if start == i {
			return 0, false
		}
		n, err := strconv.ParseUint(value[start:i], 10, 32)
		if err != nil {
			return 0, false
		}
		mult := uint64(1)
		if i < len(value) {
			switch value[i] {
			case 'w', 'W':
				mult = 604800
			case 'd', 'D':
				mult = 86400
			case 'h', 'H':
				mult = 3600
			case 'm', 'M':
				mult = 60
			case 's', 'S':
			default:
				return 0, false
			}
			i++
		}
		total += n * mult
		if total > uint64(^uint32(0)) {
			return 0, false
		}
	}
	return uint32(total), true
}
func decimalSerial(raw string) (uint32, bool) {
	if raw == "" {
		return 0, false
	}
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, e := strconv.ParseUint(raw, 10, 32)
	return uint32(n), e == nil
}

// Existing unrelated DNS owner labels may contain '_' or the RFC2317 slash
// convention. These are names, never paths. Wildcards and escaped labels are
// refused so their effects on the claimed owner set cannot be hidden.
func parentName(token parentToken, origin string) (string, error) {
	if token.quoted {
		return "", errors.New("quoted DNS names unsupported")
	}
	value := strings.ToLower(token.raw)
	if value == "@" {
		return origin, nil
	}
	if value == "." {
		return value, nil
	}
	if !strings.HasSuffix(value, ".") {
		value += "." + origin
	}
	if len(value) > 254 {
		return "", errors.New("DNS name exceeds bound")
	}
	for _, label := range strings.Split(strings.TrimSuffix(value, "."), ".") {
		if label == "" || len(label) > 63 {
			return "", errors.New("invalid parent DNS label")
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '/') {
				return "", errors.New("wildcard, escaped or unsupported parent DNS name")
			}
		}
	}
	return value, nil
}
func normalizeParentData(kind string, data []parentToken, origin string) ([]parentToken, error) {
	out := append([]parentToken(nil), data...)
	nameAt := func(i int) error {
		v, e := parentName(out[i], origin)
		if e == nil {
			out[i].raw = v
		}
		return e
	}
	switch kind {
	case "SOA":
		if len(out) != 7 {
			return nil, errors.New("SOA requires exactly seven fields")
		}
		if e := nameAt(0); e != nil {
			return nil, e
		}
		if e := nameAt(1); e != nil {
			return nil, e
		}
		for i := 2; i < 7; i++ {
			if out[i].quoted {
				return nil, errors.New("quoted SOA numbers unsupported")
			}
			var n uint32
			var ok bool
			if i == 2 {
				n, ok = decimalSerial(out[i].raw)
			} else {
				n, ok = dnsTTL(out[i].raw)
			}
			if !ok {
				return nil, errors.New("invalid SOA numeric field")
			}
			out[i].raw = strconv.FormatUint(uint64(n), 10)
		}
	case "NS", "PTR", "CNAME":
		if len(out) != 1 {
			return nil, errors.New("name record requires one target")
		}
		if e := nameAt(0); e != nil {
			return nil, e
		}
	case "A", "AAAA":
		if len(out) != 1 || out[0].quoted {
			return nil, errors.New("address record requires one literal")
		}
		a, e := netip.ParseAddr(out[0].raw)
		if e != nil || a.Zone() != "" || a.Is4() != (kind == "A") {
			return nil, errors.New("invalid parent address record")
		}
		out[0].raw = a.String()
	case "MX":
		if len(out) != 2 || out[0].quoted {
			return nil, errors.New("invalid MX shape")
		}
		n, e := strconv.ParseUint(out[0].raw, 10, 16)
		if e != nil {
			return nil, errors.New("invalid MX preference")
		}
		out[0].raw = strconv.FormatUint(n, 10)
		if e := nameAt(1); e != nil {
			return nil, e
		}
	case "TXT":
		if len(out) == 0 {
			return nil, errors.New("empty TXT shape")
		}
		for _, t := range out {
			if !t.quoted {
				return nil, errors.New("TXT data must use explicit quoted strings")
			}
		}
	default:
		return nil, errors.New("unsupported parent RR type; DNSSEC, DNAME and unknown structures require a separate workflow")
	}
	return out, nil
}

func parseRFC2317Parent(raw, zone string, limit int) (parentZone, error) {
	var model parentZone
	statements, err := parentStatements(raw, limit)
	if err != nil {
		return model, err
	}
	origin, lastOwner := zone, ""
	var defaultTTL *uint32
	originSeen, soaSeen, apexNS := false, false, 0
	explicitSOA := false
	for _, statement := range statements {
		tokens := statement.tokens
		for i, t := range tokens {
			if !t.quoted && strings.HasPrefix(t.raw, "$") {
				key := strings.ToUpper(t.raw)
				if i != 0 || len(tokens) != 2 || (key != "$ORIGIN" && key != "$TTL") {
					return model, errors.New("active $INCLUDE, $GENERATE or unsupported zone directive refused before native parsing")
				}
			}
		}
		if strings.HasPrefix(tokens[0].raw, "$") && !tokens[0].quoted {
			switch strings.ToUpper(tokens[0].raw) {
			case "$ORIGIN":
				value, e := SafeFQDN(tokens[1].raw)
				if e != nil || tokens[1].quoted || value != zone || !strings.HasSuffix(tokens[1].raw, ".") || originSeen || len(model.records) > 0 {
					return model, errors.New("parent must declare only the exact absolute origin before its records")
				}
				origin = value
				originSeen = true
			case "$TTL":
				n, ok := dnsTTL(tokens[1].raw)
				if !ok || tokens[1].quoted || defaultTTL != nil || len(model.records) > 0 {
					return model, errors.New("parent default TTL must be explicit once before records")
				}
				defaultTTL = &n
			}
			continue
		}
		at := 0
		owner := lastOwner
		if !statement.omittedOwner {
			owner, err = parentName(tokens[0], origin)
			if err != nil {
				return model, err
			}
			at++
			lastOwner = owner
		}
		if owner == "" || owner != zone && !strings.HasSuffix(owner, "."+zone) {
			return model, errors.New("parent RR owner is missing or outside the reviewed zone")
		}
		var ttl *uint32
		class := false
		for at < len(tokens) {
			if tokens[at].quoted {
				break
			}
			value := strings.ToUpper(tokens[at].raw)
			if value == "IN" {
				if class {
					return model, errors.New("duplicate RR class")
				}
				class = true
				at++
				continue
			}
			if value == "CH" || value == "HS" || value == "ANY" || value == "NONE" {
				return model, errors.New("parent records must be IN class")
			}
			if n, ok := dnsTTL(tokens[at].raw); ok {
				if ttl != nil {
					return model, errors.New("duplicate RR TTL")
				}
				ttl = &n
				at++
				continue
			}
			break
		}
		if ttl == nil {
			ttl = defaultTTL
		}
		if ttl == nil || at >= len(tokens) || tokens[at].quoted {
			return model, errors.New("parent RR requires explicit TTL/default and supported type")
		}
		kind := strings.ToUpper(tokens[at].raw)
		at++
		data, e := normalizeParentData(kind, tokens[at:], origin)
		if e != nil {
			return model, e
		}
		if kind == "SOA" {
			if soaSeen || owner != zone {
				return model, errors.New("parent needs exactly one apex SOA")
			}
			soaSeen = true
			model.serial, _ = decimalSerial(data[2].raw)
			model.serialStart, model.serialEnd = data[2].start, data[2].end
			explicitSOA = !statement.omittedOwner && strings.HasSuffix(tokens[0].raw, ".") && owner == zone
		}
		if owner == zone && kind == "NS" {
			apexNS++
		}
		model.records = append(model.records, parentRR{owner, kind, *ttl, data})
	}
	if !soaSeen || apexNS == 0 || !originSeen && !explicitSOA {
		return model, errors.New("complete parent requires an explicit matching zone identity, one apex SOA and apex NS")
	}
	return model, nil
}

func parentRRKey(rr parentRR, serialOverride *uint32) string {
	parts := []string{rr.owner, strconv.FormatUint(uint64(rr.ttl), 10), "IN", rr.kind}
	for i, t := range rr.data {
		v := t.raw
		if rr.kind == "SOA" && i == 2 && serialOverride != nil {
			v = strconv.FormatUint(uint64(*serialOverride), 10)
		}
		parts = append(parts, v)
	}
	return strings.Join(parts, " ")
}
func sameNSRecords(records []parentRR, names []string) bool {
	if len(records) != len(names) {
		return false
	}
	seen := map[string]bool{}
	for _, rr := range records {
		if rr.kind != "NS" || len(rr.data) != 1 || seen[rr.data[0].raw] {
			return false
		}
		seen[rr.data[0].raw] = true
	}
	for _, name := range names {
		if !seen[name] {
			return false
		}
	}
	return true
}
func parentLabelRange(label string) (uint16, uint16, bool) {
	if pieces := strings.Split(label, "-"); len(pieces) == 2 {
		a, e1 := strconv.ParseUint(pieces[0], 10, 8)
		z, e2 := strconv.ParseUint(pieces[1], 10, 8)
		return uint16(a), uint16(z), e1 == nil && e2 == nil && a <= z
	}
	if pieces := strings.Split(label, "/"); len(pieces) == 2 {
		a, e1 := strconv.ParseUint(pieces[0], 10, 8)
		bits, e2 := strconv.ParseUint(pieces[1], 10, 8)
		if e1 == nil && e2 == nil && bits >= 25 && bits <= 32 {
			size := uint64(1) << uint(32-bits)
			if a%size == 0 {
				return uint16(a), uint16(a + size - 1), true
			}
		}
	}
	return 0, 0, false
}

func checkParentSnapshot(in RFC2317Input, model parentZone) ([]parentRR, error) {
	if model.serial != *in.Parent.ObservedSerial {
		return nil, errors.New("actual parent SOA does not match the reviewed observed serial")
	}
	byOwner := map[string][]parentRR{}
	for _, rr := range model.records {
		byOwner[rr.owner] = append(byOwner[rr.owner], rr)
	}
	// A newly delegated child must not hide records formerly served by the
	// parent. Nor may a CNAME be added over an existing deeper namespace.
	for owner := range byOwner {
		if strings.HasSuffix(owner, "."+in.ChildZone) {
			return nil, errors.New("parent has data below the child delegation; implicit occlusion refused")
		}
		for _, claim := range in.Parent.Owners {
			if strings.HasSuffix(owner, "."+claim.Owner) {
				return nil, errors.New("parent has a deeper namespace below a claimed CNAME owner")
			}
		}
	}
	for owner, records := range byOwner {
		if owner == in.ChildZone || owner == in.Parent.Zone {
			continue
		}
		label := strings.TrimSuffix(owner, "."+in.Parent.Zone)
		if strings.Contains(label, ".") {
			continue
		}
		for _, rr := range records {
			if rr.kind != "NS" {
				continue
			}
			first, last, ok := parentLabelRange(label)
			if ok && first <= *in.Last && last >= *in.First {
				return nil, errors.New("existing classless delegation overlaps the reviewed range")
			}
		}
	}
	additions := []parentRR{}
	delegation := byOwner[in.ChildZone]
	if in.Parent.Delegation.State == "absent" {
		if len(delegation) != 0 {
			return nil, errors.New("delegation declared absent but actual parent contains data at that owner")
		}
		for _, ns := range in.DNS.NameServers {
			additions = append(additions, parentRR{owner: in.ChildZone, kind: "NS", ttl: in.Parent.TTL, data: []parentToken{{raw: ns}}})
		}
	} else if !sameNSRecords(delegation, in.DNS.NameServers) {
		return nil, errors.New("actual parent delegation differs from reviewed NS ownership")
	}
	for _, claim := range in.Parent.Owners {
		records := byOwner[claim.Owner]
		if claim.State == "absent" {
			if len(records) != 0 {
				return nil, errors.New("CNAME owner declared absent but actual parent contains data")
			}
			label := strings.TrimSuffix(claim.Owner, "."+in.Parent.Zone)
			additions = append(additions, parentRR{owner: claim.Owner, kind: "CNAME", ttl: in.Parent.TTL, data: []parentToken{{raw: label + "." + in.ChildZone}}})
		} else if len(records) != 1 || records[0].kind != "CNAME" || records[0].data[0].raw != claim.Target {
			return nil, errors.New("actual CNAME owner/target differs from the reviewed exact claim")
		}
	}
	return additions, nil
}

func parentAdditionsText(records []parentRR, newline string) string {
	if len(records) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(newline + "; RFC2317 reviewed additions." + newline)
	for _, rr := range records {
		fmt.Fprintf(&b, "%s %d IN %s %s%s", rr.owner, rr.ttl, rr.kind, rr.data[0].raw, newline)
	}
	return b.String()
}
