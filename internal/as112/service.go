// Package as112 defines the fixed AS112 service contract from RFC 7534/7535,
// RFC 8375 and RFC 9665. It does not publish routes or configure host interfaces.
package as112

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const ASN uint32 = 112
const DirectDelegation = "direct-delegation"
const DNAME = "dname"
const Both = "both"

type Service struct {
	Mode string `json:"service"`
	IPv4 bool   `json:"ipv4"`
	IPv6 bool   `json:"ipv6"`
}
type Identity struct {
	ServerName         string `json:"serverName"`
	ResponsibleMailbox string `json:"responsibleMailbox"`
	Facility           string `json:"facility"`
	Location           string `json:"location"`
	UniqueIP           string `json:"uniqueIP"`
	LOC                string `json:"loc"`
}

func (s Service) Validate() error {
	if s.Mode != DirectDelegation && s.Mode != DNAME && s.Mode != Both {
		return errors.New("AS112 service must be direct-delegation, dname or both")
	}
	if !s.IPv4 && !s.IPv6 {
		return errors.New("AS112 requires at least one explicit address family")
	}
	return nil
}
func (s Service) Families() []int {
	out := []int{}
	if s.IPv4 {
		out = append(out, 4)
	}
	if s.IPv6 {
		out = append(out, 6)
	}
	return out
}
func (s Service) Prefixes() []string {
	out := []string{}
	if s.Mode == DirectDelegation || s.Mode == Both {
		if s.IPv4 {
			out = append(out, "192.175.48.0/24")
		}
		if s.IPv6 {
			out = append(out, "2620:4f:8000::/48")
		}
	}
	if s.Mode == DNAME || s.Mode == Both {
		if s.IPv4 {
			out = append(out, "192.31.196.0/24")
		}
		if s.IPv6 {
			out = append(out, "2001:4:112::/48")
		}
	}
	return out
}
func (s Service) Addresses() []string {
	out := []string{}
	if s.Mode == DirectDelegation || s.Mode == Both {
		if s.IPv4 {
			out = append(out, "192.175.48.1", "192.175.48.6", "192.175.48.42")
		}
		if s.IPv6 {
			out = append(out, "2620:4f:8000::1", "2620:4f:8000::6", "2620:4f:8000::42")
		}
	}
	if s.Mode == DNAME || s.Mode == Both {
		if s.IPv4 {
			out = append(out, "192.31.196.1")
		}
		if s.IPv6 {
			out = append(out, "2001:4:112::1")
		}
	}
	return out
}
func (s Service) Zones() []string {
	out := []string{}
	if s.Mode == DirectDelegation || s.Mode == Both {
		out = append(out, "10.in-addr.arpa.")
		for i := 16; i <= 31; i++ {
			out = append(out, fmt.Sprintf("%d.172.in-addr.arpa.", i))
		}
		out = append(out, "168.192.in-addr.arpa.", "254.169.in-addr.arpa.", "home.arpa.", "service.arpa.")
	}
	if s.Mode == DNAME || s.Mode == Both {
		out = append(out, "empty.as112.arpa.")
	}
	return append(out, "hostname.as112.net.", "hostname.as112.arpa.")
}

var label = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func fqdn(value string) (string, error) {
	value = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	if len(value) < 3 || len(value) > 253 || !strings.Contains(value, ".") {
		return "", errors.New("AS112 DNS names must be fully qualified")
	}
	for _, part := range strings.Split(value, ".") {
		if !label.MatchString(part) {
			return "", errors.New("AS112 DNS name is invalid")
		}
	}
	return value + ".", nil
}

var locNumber = regexp.MustCompile(`^-?[0-9]+(?:\.[0-9]{1,3})?m?$`)

func normalizeLOC(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(value) > 180 || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("invalid AS112 LOC")
	}
	parts := strings.Fields(value)
	if len(parts) < 9 || len(parts) > 12 {
		return "", errors.New("LOC requires latitude/longitude degrees minutes seconds hemisphere and altitude")
	}
	for _, start := range []int{0, 4} {
		max := 90
		if start == 4 {
			max = 180
		}
		degree, e1 := strconv.Atoi(parts[start])
		minute, e2 := strconv.Atoi(parts[start+1])
		second, e3 := strconv.ParseFloat(parts[start+2], 64)
		if e1 != nil || e2 != nil || e3 != nil || !locNumber.MatchString(parts[start+2]) || degree < 0 || degree > max || minute < 0 || minute > 59 || second < 0 || second >= 60 || degree == max && (minute != 0 || second != 0) {
			return "", errors.New("invalid AS112 LOC coordinates")
		}
	}
	if parts[3] != "N" && parts[3] != "S" || parts[7] != "E" && parts[7] != "W" {
		return "", errors.New("invalid AS112 LOC hemisphere")
	}
	for i := 8; i < len(parts); i++ {
		if !locNumber.MatchString(parts[i]) {
			return "", errors.New("invalid AS112 LOC measurement")
		}
		n, err := strconv.ParseFloat(strings.TrimSuffix(parts[i], "m"), 64)
		if err != nil || i == 8 && (n < -100000 || n > 42849672.95) || i > 8 && (n < 0 || n > 90000000) {
			return "", errors.New("AS112 LOC measurement is out of range")
		}
	}
	return strings.Join(parts, " "), nil
}
func NormalizeIdentity(in Identity) (Identity, error) {
	var err error
	if in.ServerName, err = fqdn(in.ServerName); err != nil {
		return in, err
	}
	if in.ResponsibleMailbox, err = fqdn(in.ResponsibleMailbox); err != nil {
		return in, err
	}
	for _, v := range []string{in.Facility, in.Location} {
		if len(v) < 1 || len(v) > 200 || !utf8.ValidString(v) {
			return in, errors.New("AS112 facility/location must be 1..200 UTF-8 bytes")
		}
		for _, r := range v {
			if unicode.IsControl(r) {
				return in, errors.New("AS112 identity text cannot contain controls")
			}
		}
	}
	ip, err := netip.ParseAddr(in.UniqueIP)
	if err != nil || ip.Zone() != "" || ip.Is4In6() || !ip.IsGlobalUnicast() {
		return in, errors.New("AS112 identity requires one unicast node IP")
	}
	for _, prefix := range (Service{Both, true, true}).Prefixes() {
		p, _ := netip.ParsePrefix(prefix)
		if p.Contains(ip) {
			return in, errors.New("AS112 unique node IP cannot be an anycast service address")
		}
	}
	in.UniqueIP = ip.String()
	in.LOC, err = normalizeLOC(in.LOC)
	return in, err
}
