package app

import (
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"sort"
	"time"
)

func init() {
	for key, field := range (Schema{"shortName": textField(false), "country": textField(false), "website": {Type: "url"}, "ixfId": num(1, 9007199254740991, false), "peeringdbId": num(1, 9007199254740991, false), "isPrimary": {Type: "boolean"}, "excludeIxf": {Type: "boolean"}}) {
		Schemas["infrastructures"][key] = field
	}
	Schemas["switches"]["infrastructureId"] = ref("infrastructures", false)
	Schemas["vlans"]["prefixes"] = Field{Type: "prefixes"}
	Schemas["vlans"]["excludeIxf"] = Field{Type: "boolean"}
	Schemas["vlans"]["private"] = Field{Type: "boolean"}
	Schemas["vlans"]["notes"] = Field{Type: "longtext"}
	Schemas["vlans"]["peeringManager"] = Field{Type: "boolean"}
	Schemas["vlans"]["peeringMatrix"] = Field{Type: "boolean"}
	Schemas["members"]["peeringdbId"] = num(1, 9007199254740991, false)
	Schemas["connections"]["peeringdbNetixlanId"] = num(1, 9007199254740991, false)
	Schemas["ports"]["snmpIfIndex"] = num(1, 2147483647, false)
	Schemas["ports"]["snmpIfName"] = textField(false)
	Schemas["ports"]["snmpIfType"] = num(0, 4294967295, false)
	Schemas["ports"]["snmpPhysicalAddress"] = textField(false)
	Schemas["ports"]["snmpEngineIdentity"] = textField(false)
}
func fabricID(r Record) string { return str(r, "infrastructureId") }
func fabricName(s *State, id string) string {
	if id == "" {
		return "默认交换网络"
	}
	return str(find(s, "infrastructures", id), "name")
}
func vlanPrefixes(vlan Record) ([]netip.Prefix, error) {
	raw := []string{}
	for _, key := range []string{"ipv4Prefix", "ipv6Prefix"} {
		if value := str(vlan, key); value != "" {
			raw = append(raw, value)
		}
	}
	if value, ok := vlan["prefixes"]; ok && value != nil {
		values, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("VLAN prefixes must be a CIDR array")
		}
		if len(values) > 64 {
			return nil, fmt.Errorf("VLAN supports at most64 additional prefixes")
		}
		for _, v := range values {
			t, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("VLAN prefix must be text")
			}
			raw = append(raw, t)
		}
	}
	out := []netip.Prefix{}
	seen := map[netip.Prefix]bool{}
	for _, text := range raw {
		p, err := netip.ParsePrefix(text)
		if err != nil || p != p.Masked() || p.Addr().Is4In6() {
			return nil, fmt.Errorf("VLAN requires canonical CIDRs")
		}
		if !seen[p] {
			out = append(out, p)
			seen[p] = true
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Addr() != out[j].Addr() {
			return out[i].Addr().Less(out[j].Addr())
		}
		return out[i].Bits() < out[j].Bits()
	})
	return out, nil
}
func vlanContains(vlan Record, ip netip.Addr) bool {
	prefixes, err := vlanPrefixes(vlan)
	if err != nil {
		return false
	}
	for _, prefix := range prefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}
func vlanFamilyPrefixes(vlan Record, family int) ([]netip.Prefix, error) {
	all, err := vlanPrefixes(vlan)
	if err != nil {
		return nil, err
	}
	out := []netip.Prefix{}
	for _, p := range all {
		if (family == 4 && p.Addr().Is4()) || (family == 6 && p.Addr().Is6()) {
			out = append(out, p)
		}
	}
	return out, nil
}
func validateFabrics(s *State, kind string, r Record) error {
	if kind == "infrastructures" {
		for _, other := range s.Resources[kind] {
			if str(other, "id") == str(r, "id") {
				continue
			}
			if r["isPrimary"] == true && other["isPrimary"] == true {
				return fmt.Errorf("only one primary infrastructure is allowed")
			}
			if number(r, "ixfId") > 0 && number(r, "ixfId") == number(other, "ixfId") {
				return fmt.Errorf("infrastructure IX-F ID already used")
			}
		}
	}
	if kind == "vlans" {
		if _, err := vlanPrefixes(r); err != nil {
			return err
		}
		for _, other := range s.Resources["vlans"] {
			if str(other, "id") != str(r, "id") && fabricID(other) == fabricID(r) && number(other, "tag") == number(r, "tag") {
				return fmt.Errorf("VLAN tag already used in this infrastructure")
			}
		}
	}
	if kind == "ports" && number(r, "snmpIfIndex") > 0 {
		for _, other := range s.Resources["ports"] {
			if str(other, "id") != str(r, "id") && str(other, "switchId") == str(r, "switchId") && number(other, "snmpIfIndex") == number(r, "snmpIfIndex") {
				return fmt.Errorf("SNMP ifIndex already bound on switch")
			}
		}
	}
	if kind == "connections" {
		ids, err := connectionPortIDs(r)
		if err != nil {
			return err
		}
		vlan := find(s, "vlans", str(r, "vlanId"))
		for _, id := range ids {
			port := find(s, "ports", id)
			sw := find(s, "switches", str(port, "switchId"))
			if fabricID(sw) != fabricID(vlan) {
				return fmt.Errorf("connection switch and VLAN must belong to the same infrastructure")
			}
		}
	}
	if kind == "routers" {
		vlan := find(s, "vlans", str(r, "vlanId"))
		for _, other := range s.Resources["routers"] {
			if str(other, "id") == str(r, "id") {
				continue
			}
			otherVLAN := find(s, "vlans", str(other, "vlanId"))
			if fabricID(vlan) == fabricID(otherVLAN) && str(other, "routerId") == str(r, "routerId") {
				return fmt.Errorf("router identifier already used in this infrastructure")
			}
		}
	}
	if kind == "core-links" && str(r, "type") == "layer2" {
		left := find(s, "ports", str(r, "portAId"))
		right := find(s, "ports", str(r, "portBId"))
		if fabricID(find(s, "switches", str(left, "switchId"))) != fabricID(find(s, "switches", str(right, "switchId"))) {
			return fmt.Errorf("layer2 core link cannot bridge different infrastructures")
		}
	}
	return nil
}
func (a *App) fabrics(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if session.API || (str(user, "role") != "admin" && str(user, "role") != "operator") {
		problem(w, 403, "staff browser account required")
		return
	}
	if r.URL.Path != "/api/fabrics/assign" || r.Method != "POST" {
		problem(w, 404, "fabric action not found")
		return
	}
	var input struct {
		InfrastructureID string   `json:"infrastructureId"`
		VLANIDs          []string `json:"vlanIds"`
		SwitchIDs        []string `json:"switchIds"`
	}
	if !decode(w, r, &input) {
		return
	}
	if len(input.VLANIDs)+len(input.SwitchIDs) == 0 || len(input.VLANIDs)+len(input.SwitchIDs) > 1000 {
		problem(w, 400, "choose1..1000 VLANs/switches")
		return
	}
	err := a.store.Update(func(s *State) error {
		if input.InfrastructureID != "" && find(s, "infrastructures", input.InfrastructureID) == nil {
			return fmt.Errorf("target infrastructure not found")
		}
		for _, group := range []struct {
			kind string
			ids  []string
		}{{"vlans", input.VLANIDs}, {"switches", input.SwitchIDs}} {
			seen := map[string]bool{}
			for _, id := range group.ids {
				if seen[id] {
					return fmt.Errorf("duplicate assignment ID")
				}
				seen[id] = true
				record := find(s, group.kind, id)
				if record == nil {
					return fmt.Errorf("selected inventory record not found")
				}
				record["infrastructureId"] = input.InfrastructureID
				record["updatedAt"] = time.Now().UTC().Format(time.RFC3339Nano)
			}
		}
		if err := validateAll(s); err != nil {
			return err
		}
		if err := validatePatching(s); err != nil {
			return err
		}
		audit(s, str(user, "id"), "assign-fabric", "infrastructures", input.InfrastructureID)
		return nil
	})
	if err != nil {
		problem(w, 409, err.Error())
		return
	}
	respond(w, 200, Record{"infrastructureId": input.InfrastructureID, "vlanIds": input.VLANIDs, "switchIds": input.SwitchIDs, "inventoryOnly": true})
}
func exportVLAN(s *State, vlan Record) bool {
	if vlan["private"] == true || vlan["excludeIxf"] == true {
		return false
	}
	return find(s, "infrastructures", fabricID(vlan))["excludeIxf"] != true
}
func distinctStrings(input []string) []string { sort.Strings(input); return slices.Compact(input) }
