package as112

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"reflect"
	"sort"
	"strings"
	"time"
)

const MaxBundleBytes = 1 << 20

type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}
type HealthCheck struct {
	Name               string `json:"name"`
	Type               string `json:"type"`
	RCode              string `json:"rcode"`
	Authoritative      bool   `json:"authoritative"`
	RecursionAvailable bool   `json:"recursionAvailable"`
}
type HealthPlan struct {
	Executed              bool          `json:"executed"`
	RequireLocalAddresses bool          `json:"requireLocalAddresses"`
	Transports            []string      `json:"transports"`
	Checks                []HealthCheck `json:"checks"`
	Requirements          []string      `json:"requirements"`
}
type Bundle struct {
	NodeAddresses          []string   `json:"nodeAddresses"`
	RequiredLocalAddresses []string   `json:"requiredLocalAddresses"`
	RouterCreatedAt        string     `json:"routerCreatedAt,omitempty"`
	Version                int        `json:"version"`
	Kind                   string     `json:"kind"`
	RouterID               string     `json:"routerId"`
	Service                string     `json:"service"`
	Families               []int      `json:"families"`
	ASN                    uint32     `json:"asn"`
	ListenAddresses        []string   `json:"listenAddresses"`
	AdvertisePrefixes      []string   `json:"advertisePrefixes"`
	Zones                  []string   `json:"zones"`
	Identity               Identity   `json:"identity"`
	Files                  []File     `json:"files"`
	HealthPlan             HealthPlan `json:"healthPlan"`
}

func zoneQuote(value string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, v := range []byte(value) {
		if v < 32 || v > 126 || v == '"' || v == '\\' {
			fmt.Fprintf(&b, "\\%03d", v)
		} else {
			b.WriteByte(v)
		}
	}
	b.WriteByte('"')
	return b.String()
}
func serviceForBundle(b Bundle) (Service, error) {
	s := Service{Mode: b.Service}
	for _, f := range b.Families {
		switch f {
		case 4:
			if s.IPv4 {
				return s, errors.New("repeated AS112 family")
			}
			s.IPv4 = true
		case 6:
			if s.IPv6 {
				return s, errors.New("repeated AS112 family")
			}
			s.IPv6 = true
		default:
			return s, errors.New("invalid AS112 family")
		}
	}
	return s, s.Validate()
}
func Render(routerID string, s Service, identity Identity) (Bundle, error) {
	return RenderBound(routerID, "", s, identity)
}
func RenderBound(routerID, createdAt string, s Service, identity Identity) (Bundle, error) {
	return RenderNode(routerID, createdAt, s, identity, nil)
}
func RenderNode(routerID, createdAt string, s Service, identity Identity, nodeAddresses []string) (Bundle, error) {
	if createdAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, createdAt); err != nil {
			return Bundle{}, errors.New("invalid AS112 router entity timestamp")
		}
	}
	if len(routerID) < 1 || len(routerID) > 100 {
		return Bundle{}, errors.New("AS112 router binding is required")
	}
	for _, r := range routerID {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return Bundle{}, errors.New("invalid AS112 router binding")
		}
	}
	if err := s.Validate(); err != nil {
		return Bundle{}, err
	}
	id, err := NormalizeIdentity(identity)
	if err != nil {
		return Bundle{}, err
	}
	if len(nodeAddresses) == 0 {
		nodeAddresses = []string{id.UniqueIP}
	}
	if len(nodeAddresses) > 2 {
		return Bundle{}, errors.New("AS112 supports one DNS node with at most one transport address per family")
	}
	nodes := []string{}
	families := map[bool]bool{}
	identityFound := false
	for _, raw := range nodeAddresses {
		ip, e := netip.ParseAddr(raw)
		if e != nil || ip.Zone() != "" || ip.Is4In6() || !ip.IsGlobalUnicast() || families[ip.Is4()] {
			return Bundle{}, errors.New("invalid or repeated AS112 DNS node address family")
		}
		for _, prefix := range (Service{Both, true, true}).Prefixes() {
			p, _ := netip.ParsePrefix(prefix)
			if p.Contains(ip) {
				return Bundle{}, errors.New("AS112 DNS node transport address cannot be anycast")
			}
		}
		families[ip.Is4()] = true
		nodes = append(nodes, ip.String())
		identityFound = identityFound || ip.String() == id.UniqueIP
	}
	if !identityFound || createdAt != "" && (s.IPv4 != families[true] || s.IPv6 != families[false]) {
		return Bundle{}, errors.New("bound AS112 DNS node must contain uniqueIP and every enabled transport family")
	}
	sort.Strings(nodes)
	b := Bundle{Version: 1, Kind: "as112-dns", RouterID: routerID, Service: s.Mode, Families: s.Families(), ASN: ASN, ListenAddresses: s.Addresses(), AdvertisePrefixes: s.Prefixes(), Zones: s.Zones(), Identity: id, Files: []File{}, HealthPlan: HealthPlan{RequireLocalAddresses: true, Transports: []string{"udp", "tcp"}, Checks: []HealthCheck{}, Requirements: []string{"Configure every selected anycast address locally before starting DNS.", "Run PowerDNS Authoritative with this directory as its WorkingDirectory.", "Require successful DNS health before advertising AS112 prefixes.", "Withdraw the service prefixes whenever DNS or address readiness is lost; this bundle does not automate routing withdrawal.", "Provide a separately reviewed host return path; AS112 BGP rejects all received routes."}}}
	b.RouterCreatedAt = createdAt
	b.NodeAddresses = nodes
	b.RequiredLocalAddresses = append(append([]string{}, b.ListenAddresses...), nodes...)
	config := "# Generated AS112 PowerDNS Authoritative configuration; no activation performed.\nserver-id=" + strings.TrimSuffix(id.ServerName, ".") + "\nversion-string=anonymous\nlocal-address=" + strings.Join(b.ListenAddresses, ",") + "\nlocal-address-nonexist-fail=yes\nlocal-port=53\nlaunch=bind\nbind-config=named.conf\nbind-check-interval=0\napi=no\nwebserver=no\ndisable-axfr=yes\nallow-axfr-ips=\nallow-notify-from=\nallow-unsigned-autoprimary=no\nallow-unsigned-notify=no\ndnsupdate=no\nforward-dnsupdate=no\nprimary=no\nsecondary=no\nautosecondary=no\ndname-processing=no\nexpand-alias=no\nresolver=no\nsecurity-poll-suffix=\n"
	b.Files = append(b.Files, File{"pdns.conf", config})
	var named strings.Builder
	named.WriteString("// AS112 authoritative zones; run with the bundle directory as WorkingDirectory.\n")
	for i, zone := range b.Zones {
		file := fmt.Sprintf("zones/zone-%02d.zone", i)
		fmt.Fprintf(&named, "zone %q { type master; file %q; };\n", strings.TrimSuffix(zone, "."), file)
		mname, rname := "prisoner.iana.org.", "hostmaster.root-servers.org."
		names := []string{"blackhole-1.iana.org.", "blackhole-2.iana.org."}
		if zone == "empty.as112.arpa." || zone == "hostname.as112.arpa." {
			mname, rname = "blackhole.as112.arpa.", "noc.dns.icann.org."
			names = []string{"blackhole.as112.arpa."}
		}
		identityZone := strings.HasPrefix(zone, "hostname.")
		if identityZone {
			mname, rname = id.ServerName, id.ResponsibleMailbox
		}
		text := fmt.Sprintf("; Generated AS112 authoritative zone; not activated.\n$ORIGIN %s\n$TTL 604800\n@ IN SOA %s %s ( 1 604800 60 604800 604800 )\n", zone, mname, rname)
		for _, name := range names {
			text += "@ IN NS " + name + "\n"
		}
		if identityZone {
			text += "@ IN TXT " + zoneQuote(id.Facility) + " " + zoneQuote(id.Location) + "\n@ IN TXT \"See https://www.as112.net/ for more information.\"\n@ IN TXT " + zoneQuote("Unique IP: "+id.UniqueIP) + "\n"
			if id.LOC != "" {
				text += "@ IN LOC " + id.LOC + "\n"
			}
			b.HealthPlan.Checks = append(b.HealthPlan.Checks, HealthCheck{zone, "TXT", "NOERROR", true, false})
		} else {
			b.HealthPlan.Checks = append(b.HealthPlan.Checks, HealthCheck{"health-check-nonexistent." + zone, "PTR", "NXDOMAIN", true, false})
		}
		b.Files = append(b.Files, File{file, text})
	}
	b.Files = append(b.Files, File{"named.conf", named.String()})
	return b, nil
}
func Encode(b Bundle) (string, error) {
	if err := Validate(b); err != nil {
		return "", err
	}
	raw, err := json.MarshalIndent(b, "", "  ")
	if len(raw) > MaxBundleBytes {
		return "", errors.New("AS112 bundle exceeds bound")
	}
	return string(raw) + "\n", err
}
func Decode(raw string) (Bundle, error) {
	var b Bundle
	if len(raw) == 0 || len(raw) > MaxBundleBytes {
		return b, errors.New("invalid AS112 bundle size")
	}
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&b) != nil || d.Decode(new(any)) != io.EOF {
		return b, errors.New("invalid typed AS112 bundle")
	}
	if err := Validate(b); err != nil {
		return b, err
	}
	canonical, err := json.MarshalIndent(b, "", "  ")
	if err != nil || raw != string(canonical)+"\n" {
		return b, errors.New("AS112 bundle must retain its exact canonical JSON encoding")
	}
	return b, nil
}
func Validate(b Bundle) error {
	s, err := serviceForBundle(b)
	if err != nil {
		return err
	}
	wanted, err := RenderNode(b.RouterID, b.RouterCreatedAt, s, b.Identity, b.NodeAddresses)
	if err != nil || !reflect.DeepEqual(b, wanted) {
		return errors.New("AS112 bundle is not the exact generated fixed-service contract")
	}
	return nil
}
