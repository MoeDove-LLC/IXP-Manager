package app

import (
	"crypto/sha256"
	"fmt"
	"github.com/moedove/ixp-manager/internal/ixf"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func stableExportID(namespace, id string) int64 {
	key := namespace + "\x00" + id
	if namespace == "switch" {
		key = id
	}
	hash := sha256.Sum256([]byte(key))
	var value int64
	for _, b := range hash[:6] {
		value = value<<8 | int64(b)
	}
	return value
}
func exportIXF(s *State) (ixf.Document, error) {
	return exportIXFVersion(s, "1.0")
}
func exportIXFVersion(s *State, version string) (ixf.Document, error) {
	exchanges := map[string]*ixf.Exchange{}
	seenExchange := map[int64]bool{1: true}
	switchIDs := map[string]int64{}
	seenSwitch := map[int64]bool{}
	addDefault := len(s.Resources["infrastructures"]) == 0
	for _, kind := range []string{"vlans", "switches"} {
		for _, r := range s.Resources[kind] {
			if fabricID(r) == "" {
				addDefault = true
			}
		}
	}
	if addDefault {
		exchanges[""] = &ixf.Exchange{ID: 1, IXFID: int64(number(s.Settings, "ixfId")), Name: str(s.Settings, "name"), ShortName: str(s.Settings, "shortName"), Country: str(s.Settings, "country"), URL: str(s.Settings, "website")}
	}
	for _, r := range s.Resources["infrastructures"] {
		id := stableExportID("infrastructure", str(r, "id"))
		if id == 0 || seenExchange[id] {
			return ixf.Document{}, fmt.Errorf("infrastructure export ID collision")
		}
		seenExchange[id] = true
		exchanges[str(r, "id")] = &ixf.Exchange{ID: id, IXFID: int64(number(r, "ixfId")), Name: str(r, "name"), ShortName: str(r, "shortName"), Country: str(r, "country"), URL: str(r, "website"), Exclude: r["excludeIxf"] == true}
	}
	for _, vlan := range s.Resources["vlans"] {
		exchange := exchanges[fabricID(vlan)]
		if exchange == nil {
			return ixf.Document{}, fmt.Errorf("VLAN infrastructure missing")
		}
		networks, err := vlanPrefixes(vlan)
		if err != nil {
			return ixf.Document{}, err
		}
		prefixes := []string{}
		for _, p := range networks {
			prefixes = append(prefixes, p.String())
		}
		exchange.VLANs = append(exchange.VLANs, ixf.VLAN{ID: int64(number(vlan, "tag")), Name: str(vlan, "name"), Prefixes: prefixes, Exclude: !exportVLAN(s, vlan)})
	}
	for _, sw := range s.Resources["switches"] {
		exchange := exchanges[fabricID(sw)]
		if exchange == nil {
			return ixf.Document{}, fmt.Errorf("switch infrastructure missing")
		}
		id := stableExportID("switch", str(sw, "id"))
		if id == 0 || seenSwitch[id] {
			return ixf.Document{}, fmt.Errorf("switch export ID collision")
		}
		seenSwitch[id] = true
		switchIDs[str(sw, "id")] = id
		loc := find(s, "locations", str(sw, "locationId"))
		exchange.Switches = append(exchange.Switches, ixf.Switch{ID: id, Name: str(sw, "name"), Colo: str(loc, "name"), City: str(loc, "city"), Country: str(loc, "country")})
	}
	input := ixf.Input{Timestamp: time.Now().UTC()}
	for _, exchange := range exchanges {
		input.Exchanges = append(input.Exchanges, *exchange)
	}
	for _, m := range s.Resources["members"] {
		if number(m, "asn") == 0 || m["excludeIxf"] == true {
			continue
		}
		member := ixf.Member{ID: str(m, "id"), ASN: uint32(number(m, "asn")), Name: str(m, "name"), Website: str(m, "website"), Status: str(m, "status")}
		groups := map[string]int{}
		for _, c := range s.Resources["connections"] {
			if str(c, "memberId") != member.ID || member.Status != "active" || str(c, "status") != "active" || (str(c, "ipv4") == "" && str(c, "ipv6") == "") {
				continue
			}
			vlan := find(s, "vlans", str(c, "vlanId"))
			if !exportVLAN(s, vlan) {
				continue
			}
			exchange := exchanges[fabricID(vlan)]
			if exchange == nil {
				return ixf.Document{}, fmt.Errorf("connection infrastructure missing")
			}
			ports, err := connectionPortIDs(c)
			if err != nil {
				return ixf.Document{}, err
			}
			interfaces := []ixf.Interface{}
			for _, id := range ports {
				port := find(s, "ports", id)
				sw := find(s, "switches", str(port, "switchId"))
				if port == nil || number(port, "speed") <= 0 || fabricID(sw) != fabricID(vlan) {
					return ixf.Document{}, fmt.Errorf("connection %s requires actual port speed and matching infrastructure", str(c, "id"))
				}
				interfaces = append(interfaces, ixf.Interface{SwitchID: switchIDs[str(port, "switchId")], SpeedMbps: uint64(number(port, "speed")), Type: "physical"})
			}
			addresses := ixf.VLANConnection{VLANID: int64(number(vlan, "tag"))}
			address := func(field string) *ixf.Address {
				ip := str(c, field)
				if ip == "" {
					return nil
				}
				out := &ixf.Address{Address: ip}
				for _, p := range s.Resources["peers"] {
					router := find(s, "routers", str(p, "routerId"))
					if routerRole(router) == "route-server" && str(p, "memberId") == member.ID && str(p, "address") == ip && p["enabled"] == true && str(router, "vlanId") == str(c, "vlanId") {
						out.RouteServer = true
						n := uint32(number(p, "maxPrefixes"))
						if out.MaxPrefix == 0 || n < out.MaxPrefix {
							out.MaxPrefix = n
						}
					}
				}
				for _, mac := range s.Resources["mac-addresses"] {
					if str(mac, "connectionId") == str(c, "id") {
						out.MACAddresses = append(out.MACAddresses, str(mac, "address"))
					}
				}
				return out
			}
			addresses.IPv4 = address("ipv4")
			addresses.IPv6 = address("ipv6")
			key := strconv.FormatInt(exchange.ID, 10) + ":" + strings.Join(ports, "\x00")
			if index, exists := groups[key]; exists {
				member.Connections[index].VLANs = append(member.Connections[index].VLANs, addresses)
			} else {
				groups[key] = len(member.Connections)
				member.Connections = append(member.Connections, ixf.Connection{ExchangeID: exchange.ID, State: "active", Interfaces: interfaces, VLANs: []ixf.VLANConnection{addresses}})
			}
		}
		input.Members = append(input.Members, member)
	}
	return ixf.BuildVersion(input, version)
}

// ixfRoute keeps the legacy aliases on exactly the same publication gate.
// Query parameters never grant access or override registry identities.
func ixfRoute(path string) (version string, public, matched bool) {
	if path == "/participants.json" {
		return "1.0", true, true
	}
	for _, route := range []struct {
		base, versionPrefix string
		public              bool
	}{
		{"/api/ixf", "/api/ixf/", false},
		{"/api/public/ixf", "/api/public/ixf/", true},
		{"/api/v4/member-export/ixf", "/api/v4/member-export/ixf/", true},
		{"/apiv1/member-list/list", "/apiv1/member-list/list/version/", true},
	} {
		if path == route.base {
			return "1.0", route.public, true
		}
		if strings.HasPrefix(path, route.versionPrefix) {
			return strings.TrimPrefix(path, route.versionPrefix), route.public, true
		}
	}
	return "", false, false
}

func (a *App) ixf(w http.ResponseWriter, r *http.Request, public bool, version string) {
	if r.Method != "GET" {
		problem(w, 405, "method not allowed")
		return
	}
	s := a.store.Snapshot()
	if public && s.Settings["publishIxf"] != true {
		problem(w, 404, "public IX-F feed is disabled")
		return
	}
	if !ixf.SupportedVersion(version) {
		problem(w, 400, "unsupported IX-F version; choose 0.6, 0.7 or 1.0")
		return
	}
	doc, err := exportIXFVersion(&s, version)
	if err != nil {
		if public {
			problem(w, 503, "IX-F feed unavailable; contact the exchange operator")
		} else {
			problem(w, 422, err.Error())
		}
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="ixf-member-export.json"`)
	respond(w, 200, doc)
}
