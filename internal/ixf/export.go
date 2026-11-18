// Package ixf exports IX-F Member Export 0.6, 0.7 and 1.0 without exposing private contacts,
// management addresses, user records, credentials, or free-form metadata.
package ixf

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"time"
)

type Input struct {
	Timestamp time.Time
	Exchanges []Exchange
	Members   []Member
}
type Exchange struct {
	ID, IXFID                     int64
	Name, ShortName, Country, URL string
	Exclude                       bool
	VLANs                         []VLAN
	Switches                      []Switch
}
type VLAN struct {
	Prefixes                     []string
	ID                           int64
	Name, IPv4Prefix, IPv6Prefix string
	Exclude                      bool
}
type Switch struct {
	ID                        int64
	Name, Colo, City, Country string
}
type Member struct {
	ID                    string
	ASN                   uint32
	Name, Website, Status string
	Exclude               bool
	Connections           []Connection
}
type Connection struct {
	ExchangeID int64
	State      string
	Interfaces []Interface
	VLANs      []VLANConnection
}
type Interface struct {
	SwitchID  int64  `json:"switch_id"`
	SpeedMbps uint64 `json:"if_speed"`
	Type      string `json:"if_type,omitempty"`
}
type VLANConnection struct {
	VLANID int64    `json:"vlan_id"`
	IPv4   *Address `json:"ipv4,omitempty"`
	IPv6   *Address `json:"ipv6,omitempty"`
}
type Address struct {
	Address      string    `json:"address"`
	RouteServer  bool      `json:"routeserver"`
	MaxPrefix    uint32    `json:"max_prefix,omitempty"`
	MACAddresses []string  `json:"mac_addresses,omitempty"`
	Services     []Service `json:"services,omitempty"`
	// ServiceTypes is generated for 0.7 output; callers supply Services instead.
	ServiceTypes []string `json:"service_type,omitempty"`
}

type Service struct {
	Type string `json:"type"`
}

type Document struct {
	Version    string           `json:"version"`
	Generator  string           `json:"generator"`
	Timestamp  string           `json:"timestamp"`
	IXPList    []ExchangeExport `json:"ixp_list"`
	MemberList []MemberExport   `json:"member_list"`
}
type ExchangeExport struct {
	ID        int64          `json:"ixp_id"`
	IXFID     int64          `json:"ixf_id,omitempty"`
	ShortName string         `json:"shortname"`
	Name      string         `json:"name,omitempty"`
	Country   string         `json:"country,omitempty"`
	URL       string         `json:"url,omitempty"`
	VLANs     []VLANExport   `json:"vlan"`
	Switches  []SwitchExport `json:"switch"`
}
type VLANExport struct {
	ID   int64   `json:"id"`
	Name string  `json:"name,omitempty"`
	IPv4 *Prefix `json:"ipv4,omitempty"`
	IPv6 *Prefix `json:"ipv6,omitempty"`
}
type Prefix struct {
	Prefix     string `json:"prefix"`
	MaskLength int    `json:"mask_length"`
}
type SwitchExport struct {
	ID      int64  `json:"id"`
	Name    string `json:"name,omitempty"`
	Colo    string `json:"colo,omitempty"`
	City    string `json:"city,omitempty"`
	Country string `json:"country,omitempty"`
}
type MemberExport struct {
	ASN         uint32             `json:"asnum"`
	Name        string             `json:"name,omitempty"`
	URL         string             `json:"url,omitempty"`
	Connections []ConnectionExport `json:"connection_list"`
}
type ConnectionExport struct {
	ExchangeID int64            `json:"ixp_id"`
	State      string           `json:"state"`
	Interfaces []Interface      `json:"if_list"`
	VLANs      []VLANConnection `json:"vlan_list"`
}

// Build exports active members that have an active connection on an included
// exchange/VLAN. Invalid included records cause an error rather than a partial
// feed that could instruct downstream consumers to delete valid connectivity.
func Build(in Input) (Document, error) {
	return BuildVersion(in, "1.0")
}

// SupportedVersion is deliberately exact: an unknown format must not silently
// return a different schema to an automated consumer.
func SupportedVersion(version string) bool {
	return version == "0.6" || version == "0.7" || version == "1.0"
}

func BuildVersion(in Input, version string) (Document, error) {
	doc := Document{Version: version, Generator: "MoeDove IXP Manager", IXPList: []ExchangeExport{}, MemberList: []MemberExport{}}
	if !SupportedVersion(version) {
		return doc, fmt.Errorf("unsupported IX-F version; choose 0.6, 0.7 or 1.0")
	}
	if in.Timestamp.IsZero() {
		return doc, fmt.Errorf("export timestamp is required")
	}
	doc.Timestamp = in.Timestamp.UTC().Format(time.RFC3339)
	exchanges := map[int64]Exchange{}
	vlans := map[int64]map[int64]VLAN{}
	switches := map[int64]map[int64]bool{}
	excluded := map[int64]bool{}
	for _, e := range in.Exchanges {
		if !validID(e.ID) {
			return doc, fmt.Errorf("exchange ID must be a positive exact JSON integer")
		}
		if _, ok := exchanges[e.ID]; ok || excluded[e.ID] {
			return doc, fmt.Errorf("duplicate exchange ID %d", e.ID)
		}
		if e.Exclude {
			excluded[e.ID] = true
			continue
		}
		if (!validID(e.IXFID) && !(version == "0.6" && e.IXFID == 0)) || strings.TrimSpace(e.ShortName) == "" {
			return doc, fmt.Errorf("exchange %d requires IX-F ID and shortname", e.ID)
		}
		if err := website(e.URL); err != nil {
			return doc, fmt.Errorf("exchange %d: %w", e.ID, err)
		}
		x := ExchangeExport{ID: e.ID, IXFID: e.IXFID, ShortName: e.ShortName, Name: e.Name, Country: e.Country, URL: e.URL, VLANs: []VLANExport{}, Switches: []SwitchExport{}}
		exchanges[e.ID] = e
		vlans[e.ID] = map[int64]VLAN{}
		switches[e.ID] = map[int64]bool{}
		for _, v := range e.VLANs {
			if !validID(v.ID) {
				return doc, fmt.Errorf("invalid VLAN ID on exchange %d", e.ID)
			}
			if _, ok := vlans[e.ID][v.ID]; ok {
				return doc, fmt.Errorf("duplicate VLAN ID %d", v.ID)
			}
			vlans[e.ID][v.ID] = v
			if v.Exclude {
				continue
			}
			networks, err := vlanNetworks(v)
			if err != nil {
				return doc, err
			}
			p4, err := prefix(singleNetwork(networks, true), true)
			if err != nil {
				return doc, fmt.Errorf("VLAN %d: %w", v.ID, err)
			}
			p6, err := prefix(singleNetwork(networks, false), false)
			if err != nil {
				return doc, fmt.Errorf("VLAN %d: %w", v.ID, err)
			}
			x.VLANs = append(x.VLANs, VLANExport{ID: v.ID, Name: v.Name, IPv4: p4, IPv6: p6})
		}
		for _, sw := range e.Switches {
			if !validID(sw.ID) || switches[e.ID][sw.ID] {
				return doc, fmt.Errorf("invalid or duplicate switch ID on exchange %d", e.ID)
			}
			switches[e.ID][sw.ID] = true
			x.Switches = append(x.Switches, SwitchExport{ID: sw.ID, Name: sw.Name, Colo: sw.Colo, City: sw.City, Country: sw.Country})
		}
		sort.Slice(x.VLANs, func(i, j int) bool { return x.VLANs[i].ID < x.VLANs[j].ID })
		sort.Slice(x.Switches, func(i, j int) bool { return x.Switches[i].ID < x.Switches[j].ID })
		doc.IXPList = append(doc.IXPList, x)
	}
	if len(doc.IXPList) == 0 {
		return doc, fmt.Errorf("at least one included exchange is required")
	}
	seenASN := map[uint32]bool{}
	seenAddress := map[string]bool{}
	for _, m := range in.Members {
		if m.Exclude || m.Status != "active" {
			continue
		}
		if m.ASN == 0 || m.ASN == 4294967295 || seenASN[m.ASN] {
			return doc, fmt.Errorf("invalid or duplicate ASN for member %s", m.ID)
		}
		seenASN[m.ASN] = true
		if err := website(m.Website); err != nil {
			return doc, fmt.Errorf("member %s: %w", m.ID, err)
		}
		member := MemberExport{ASN: m.ASN, Name: m.Name, URL: m.Website, Connections: []ConnectionExport{}}
		for _, c := range m.Connections {
			if c.State != "active" || excluded[c.ExchangeID] {
				continue
			}
			if _, ok := exchanges[c.ExchangeID]; !ok {
				return doc, fmt.Errorf("member %s references missing exchange", m.ID)
			}
			conn := ConnectionExport{ExchangeID: c.ExchangeID, State: "active", Interfaces: append([]Interface(nil), c.Interfaces...), VLANs: []VLANConnection{}}
			seenVLAN := map[int64]bool{}
			for _, v := range c.VLANs {
				configured, ok := vlans[c.ExchangeID][v.VLANID]
				if !ok {
					return doc, fmt.Errorf("member %s references missing VLAN", m.ID)
				}
				if configured.Exclude {
					continue
				}
				if seenVLAN[v.VLANID] {
					return doc, fmt.Errorf("duplicate VLAN on connection for member %s", m.ID)
				}
				seenVLAN[v.VLANID] = true
				if v.IPv4 == nil && v.IPv6 == nil {
					continue
				}
				networks, err := vlanNetworks(configured)
				if err != nil {
					return doc, err
				}
				v4, err := address(v.IPv4, networks, true, version)
				if err != nil {
					return doc, fmt.Errorf("member %s IPv4: %w", m.ID, err)
				}
				v6, err := address(v.IPv6, networks, false, version)
				if err != nil {
					return doc, fmt.Errorf("member %s IPv6: %w", m.ID, err)
				}
				for _, addr := range []*Address{v4, v6} {
					if addr == nil {
						continue
					}
					key := fmt.Sprintf("%d/%d/%s", c.ExchangeID, v.VLANID, addr.Address)
					if seenAddress[key] {
						return doc, fmt.Errorf("duplicate exported address on VLAN %d", v.VLANID)
					}
					seenAddress[key] = true
				}
				conn.VLANs = append(conn.VLANs, VLANConnection{VLANID: v.VLANID, IPv4: v4, IPv6: v6})
			}
			if len(conn.VLANs) == 0 {
				continue
			}
			if len(conn.Interfaces) == 0 {
				return doc, fmt.Errorf("member %s active connection requires a physical interface", m.ID)
			}
			for _, iface := range conn.Interfaces {
				if !switches[c.ExchangeID][iface.SwitchID] {
					return doc, fmt.Errorf("member %s references missing switch", m.ID)
				}
				if iface.SpeedMbps == 0 || iface.SpeedMbps > 10000000 {
					return doc, fmt.Errorf("member %s interface speed must be positive Mbps", m.ID)
				}
			}
			sort.Slice(conn.VLANs, func(i, j int) bool { return conn.VLANs[i].VLANID < conn.VLANs[j].VLANID })
			member.Connections = append(member.Connections, conn)
		}
		if len(member.Connections) > 0 {
			doc.MemberList = append(doc.MemberList, member)
		}
	}
	sort.Slice(doc.IXPList, func(i, j int) bool { return doc.IXPList[i].ID < doc.IXPList[j].ID })
	sort.Slice(doc.MemberList, func(i, j int) bool { return doc.MemberList[i].ASN < doc.MemberList[j].ASN })
	return doc, nil
}

func validID(id int64) bool { return id > 0 && id <= 9007199254740991 }
func website(s string) error {
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return fmt.Errorf("invalid public website URL")
	}
	return nil
}
func prefix(s string, ipv4 bool) (*Prefix, error) {
	if s == "" {
		return nil, nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil || p.Addr().Is4() != ipv4 || p.Addr().Is4In6() || p != p.Masked() {
		return nil, fmt.Errorf("invalid canonical prefix")
	}
	return &Prefix{Prefix: p.Addr().String(), MaskLength: p.Bits()}, nil
}
func address(a *Address, networks []netip.Prefix, ipv4 bool, version string) (*Address, error) {
	if a == nil {
		return nil, nil
	}
	ip, err := netip.ParseAddr(a.Address)
	if err != nil || ip.Is4() != ipv4 || ip.Is4In6() || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() {
		return nil, fmt.Errorf("invalid address family or unicast address")
	}
	contained := false
	for _, p := range networks {
		if p.Contains(ip) {
			contained = true
		}
	}
	if !contained {
		return nil, fmt.Errorf("address is outside VLAN prefix")
	}
	out := *a
	out.Address = ip.String()
	out.MACAddresses = nil
	out.Services = nil
	out.ServiceTypes = nil
	if len(a.ServiceTypes) != 0 {
		return nil, fmt.Errorf("service_type is an output field; supply typed services")
	}
	services := map[string]bool{}
	for _, service := range a.Services {
		if service.Type != "ixrouteserver" && service.Type != "ixroutecollector" {
			return nil, fmt.Errorf("invalid IX-F service type")
		}
		services[service.Type] = true
	}
	for _, kind := range []string{"ixroutecollector", "ixrouteserver"} {
		if !services[kind] {
			continue
		}
		switch version {
		case "0.7":
			out.ServiceTypes = append(out.ServiceTypes, kind)
		case "1.0":
			out.Services = append(out.Services, Service{Type: kind})
		}
	}
	seen := map[string]bool{}
	for _, s := range a.MACAddresses {
		mac, err := net.ParseMAC(s)
		if err != nil || len(mac) != 6 {
			return nil, fmt.Errorf("invalid Ethernet MAC address")
		}
		normal := mac.String()
		if !seen[normal] {
			out.MACAddresses = append(out.MACAddresses, normal)
			seen[normal] = true
		}
	}
	sort.Strings(out.MACAddresses)
	return &out, nil
}

func vlanNetworks(v VLAN) ([]netip.Prefix, error) {
	raw := append([]string(nil), v.Prefixes...)
	if v.IPv4Prefix != "" {
		raw = append(raw, v.IPv4Prefix)
	}
	if v.IPv6Prefix != "" {
		raw = append(raw, v.IPv6Prefix)
	}
	out := []netip.Prefix{}
	seen := map[netip.Prefix]bool{}
	for _, s := range raw {
		p, err := netip.ParsePrefix(s)
		if err != nil || p != p.Masked() || p.Addr().Is4In6() {
			return nil, fmt.Errorf("invalid VLAN prefix")
		}
		if !seen[p] {
			out = append(out, p)
			seen[p] = true
		}
	}
	return out, nil
}

// These IX-F versions have one optional prefix field per family. Multiple declared networks
// remain validated internally; omit that field instead of inventing an aggregate.
func singleNetwork(networks []netip.Prefix, ipv4 bool) string {
	value := ""
	count := 0
	for _, p := range networks {
		if p.Addr().Is4() == ipv4 {
			value = p.String()
			count++
		}
	}
	if count == 1 {
		return value
	}
	return ""
}
