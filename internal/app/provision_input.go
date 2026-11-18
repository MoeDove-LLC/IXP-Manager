package app

import (
	"fmt"
	"math"
	"net/netip"
	"sort"
	"strings"

	"github.com/moedove/ixp-manager/internal/provision"
)

func init() {
	for _, kind := range []string{"connections", "routers"} {
		Schemas[kind]["dnsName4"] = textField(false)
		Schemas[kind]["dnsName6"] = textField(false)
	}
	Schemas["connections"]["canPing4"] = Field{Type: "boolean"}
	Schemas["connections"]["canPing6"] = Field{Type: "boolean"}
	Schemas["connections"]["monitorBusy"] = Field{Type: "boolean"}
	Schemas["switches"]["dnsName"] = textField(false)
	Schemas["ip-addresses"]["dnsName"] = textField(false)
	for _, kind := range []string{"switches", "routers", "peers"} {
		Schemas[kind]["monitor"] = Field{Type: "boolean"}
	}
}

func provisionBool(r Record, key string, fallback bool) (bool, error) {
	value, exists := r[key]
	if !exists || value == nil || value == "" {
		return fallback, nil
	}
	flag, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%s must be boolean", key)
	}
	return flag, nil
}

func provisionDNSName(r Record, key string) (string, error) {
	value, exists := r[key]
	if !exists || value == nil || value == "" {
		return "", nil
	}
	name, ok := value.(string)
	if !ok || strings.TrimSpace(name) != name {
		return "", fmt.Errorf("%s must be a DNS hostname", key)
	}
	canonical, err := provision.SafeFQDN(name)
	if err != nil {
		return "", fmt.Errorf("%s must be a safe fully qualified DNS hostname", key)
	}
	return canonical, nil
}

// validateProvisionInventory is called by the common inventory validator. It
// never rewrites the inventory while validating a generated configuration.
func validateProvisionInventory(s *State, kind string, r Record) error {
	var names, flags []string
	switch kind {
	case "connections":
		names = []string{"dnsName4", "dnsName6"}
		flags = []string{"canPing4", "canPing6", "monitorBusy"}
	case "switches":
		names = []string{"dnsName"}
		flags = []string{"monitor"}
	case "ip-addresses":
		names = []string{"dnsName"}
	case "routers":
		names = []string{"dnsName4", "dnsName6"}
		flags = []string{"monitor"}
	case "peers":
		flags = []string{"monitor"}
	}
	for _, key := range names {
		if _, err := provisionDNSName(r, key); err != nil {
			return err
		}
	}
	for _, key := range flags {
		if _, err := provisionBool(r, key, true); err != nil {
			return err
		}
	}
	if kind == "ip-addresses" && str(r, "dnsName") != "" {
		if s == nil || str(r, "memberId") == "" || find(s, "members", str(r, "memberId")) == nil {
			return fmt.Errorf("a reserved DNS hostname requires a real member owner")
		}
		for _, connection := range s.Resources["connections"] {
			for _, family := range []int{4, 6} {
				if _, err := reservationDNS(connection, r, family); err != nil {
					return err
				}
			}
		}
	}
	if kind == "connections" && s != nil {
		for _, pool := range s.Resources["ip-addresses"] {
			for _, family := range []int{4, 6} {
				if _, err := reservationDNS(r, pool, family); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// Ownership transitions cannot carry the previous tenant's DNS preferences.
// Same-owner renumbering is allowed; it invalidates generated snapshots normally.
func guardProvisionDNSMutation(_ *State, kind, method string, old, body Record) error {
	if method != "PUT" || old == nil || str(old, "memberId") == str(body, "memberId") {
		return nil
	}
	fields := []string{}
	if kind == "ip-addresses" {
		fields = []string{"dnsName"}
	}
	if kind == "connections" {
		fields = []string{"dnsName4", "dnsName6"}
	}
	for _, field := range fields {
		if str(body, field) != "" {
			return fmt.Errorf("clear DNS hostnames before changing the member owner")
		}
	}
	return nil
}

func provisionVLANAddress(vlan, raw string) string {
	address, err := netip.ParseAddr(raw)
	if err != nil {
		return ""
	}
	return vlan + "\x00" + address.String()
}

func reservationDNS(connection, pool Record, family int) (string, error) {
	key := provisionVLANAddress(str(connection, "vlanId"), str(connection, fmt.Sprintf("ipv%d", family)))
	if key == "" || key != provisionVLANAddress(str(pool, "vlanId"), str(pool, "address")) {
		return "", nil
	}
	name, err := provisionDNSName(pool, "dnsName")
	if err != nil {
		return "", err
	}
	if name == "" {
		return "", nil
	}
	if str(pool, "memberId") == "" || str(pool, "memberId") != str(connection, "memberId") {
		return "", fmt.Errorf("reserved DNS hostname belongs to a different address owner")
	}
	explicit, err := provisionDNSName(connection, fmt.Sprintf("dnsName%d", family))
	if err != nil {
		return "", err
	}
	if explicit != "" && explicit != name {
		return "", fmt.Errorf("connection and reservation DNS hostnames conflict; reconcile the recorded names first")
	}
	return name, nil
}

type provisionInventory map[string]map[string]Record

func provisionIndex(s *State) (provisionInventory, error) {
	index := provisionInventory{}
	for _, kind := range []string{"connections", "members", "ports", "switches", "racks", "locations", "vlans", "routers", "peers", "infrastructures", "ip-addresses"} {
		index[kind] = map[string]Record{}
		for _, r := range s.Resources[kind] {
			id := str(r, "id")
			if !identifier.MatchString(id) || index[kind][id] != nil {
				return nil, fmt.Errorf("invalid or duplicate %s inventory identifier", kind)
			}
			if err := validateProvisionInventory(s, kind, r); err != nil {
				return nil, err
			}
			index[kind][id] = r
		}
	}
	return index, nil
}

func (index provisionInventory) required(kind, id string) (Record, error) {
	r := index[kind][id]
	if r == nil {
		return nil, fmt.Errorf("provisioning inventory references missing %s record", kind)
	}
	return r, nil
}

func provisionASN(r Record) (uint32, error) {
	v, ok := r["asn"].(float64)
	if !ok || math.IsNaN(v) || math.IsInf(v, 0) || v < 1 || v > 4294967294 || math.Trunc(v) != v {
		return 0, fmt.Errorf("provisioning inventory requires a valid ASN")
	}
	return uint32(v), nil
}

func provisionAddress(raw string, family int) (netip.Addr, error) {
	a, err := netip.ParseAddr(raw)
	if err != nil || a.Zone() != "" || a.Is4In6() || a.IsUnspecified() || a.IsMulticast() || (family == 4 && !a.Is4()) || (family == 6 && !a.Is6()) {
		return netip.Addr{}, fmt.Errorf("provisioning inventory has an invalid address or address family")
	}
	return a, nil
}

func provisionSorted(set map[string]bool) []string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func (index provisionInventory) placement(switchIDs []string) ([]string, []string, []string, error) {
	switches, racks, locations := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, id := range switchIDs {
		sw, err := index.required("switches", id)
		if err != nil {
			return nil, nil, nil, err
		}
		switches[id] = true
		location := str(sw, "locationId")
		if _, err = index.required("locations", location); err != nil {
			return nil, nil, nil, err
		}
		locations[location] = true
		if rackID := str(sw, "rackId"); rackID != "" {
			rack, err := index.required("racks", rackID)
			if err != nil {
				return nil, nil, nil, err
			}
			// Keep both actual inventory locations if they differ. Projection must
			// not silently invent consistency or discard a recorded rack location.
			rackLocation := str(rack, "locationId")
			if _, err = index.required("locations", rackLocation); err != nil {
				return nil, nil, nil, err
			}
			racks[rackID] = true
			locations[rackLocation] = true
		}
	}
	return provisionSorted(switches), provisionSorted(racks), provisionSorted(locations), nil
}

// provisionInput is a deterministic projection of configured inventory, not
// telemetry. Active expresses desired eligibility, never observed availability.
// Every address remains present for DNS's optional inactive-assignment coverage;
// the generator owns VLAN/infrastructure/family and monitoring selection.
func provisionInput(s *State, options provision.Options) (provision.Input, error) {
	if options.Kind == "as112-dns" {
		return as112ProvisionInput(s, options)
	}
	if options.Kind == "switch-core" {
		return coreProvisionInput(s, options)
	}
	input := provision.Input{Targets: []provision.Target{}, Sessions: []provision.Session{}, CoreBundles: []provision.CoreBundle{}}
	if s == nil {
		return input, fmt.Errorf("provisioning inventory is unavailable")
	}
	options.Families = append([]int(nil), options.Families...)
	normalized, err := provision.NormalizeOptions(options)
	if err != nil {
		return input, err
	}
	input.Options = normalized
	index, err := provisionIndex(s)
	if err != nil {
		return input, err
	}
	if options.InfrastructureID != "" {
		if _, err = index.required("infrastructures", options.InfrastructureID); err != nil {
			return input, err
		}
	}
	if options.VLANID != "" {
		vlan, err := index.required("vlans", options.VLANID)
		if err != nil {
			return input, err
		}
		if options.InfrastructureID != "" && fabricID(vlan) != options.InfrastructureID {
			return input, fmt.Errorf("selected VLAN is not in the selected infrastructure")
		}
	}
	memberTargets := map[string]string{}
	poolByAddress := map[string]Record{}
	boundAddresses := map[string]bool{}
	for _, pool := range s.Resources["ip-addresses"] {
		address, err := provisionAddress(str(pool, "address"), 0)
		if err != nil {
			return input, err
		}
		if _, err = index.required("vlans", str(pool, "vlanId")); err != nil {
			return input, err
		}
		key := provisionVLANAddress(str(pool, "vlanId"), address.String())
		if poolByAddress[key] != nil {
			return input, fmt.Errorf("duplicate address reservation in one VLAN")
		}
		poolByAddress[key] = pool
	}
	memberKey := func(member, vlan, address string) string { return member + "\x00" + vlan + "\x00" + address }
	for _, connection := range s.Resources["connections"] {
		member, err := index.required("members", str(connection, "memberId"))
		if err != nil {
			return input, err
		}
		vlan, err := index.required("vlans", str(connection, "vlanId"))
		if err != nil {
			return input, err
		}
		asn, err := provisionASN(member)
		if err != nil {
			return input, err
		}
		ports, err := connectionPortIDs(connection)
		if err != nil {
			return input, err
		}
		switchIDs := []string{}
		for _, id := range ports {
			port, err := index.required("ports", id)
			if err != nil {
				return input, err
			}
			sw, err := index.required("switches", str(port, "switchId"))
			if err != nil {
				return input, err
			}
			if fabricID(sw) != fabricID(vlan) {
				return input, fmt.Errorf("connection switch and VLAN belong to different infrastructures")
			}
			switchIDs = append(switchIDs, str(sw, "id"))
		}
		switchIDs, rackIDs, locationIDs, err := index.placement(switchIDs)
		if err != nil {
			return input, err
		}
		busy, _ := provisionBool(connection, "monitorBusy", false)
		for _, family := range []int{4, 6} {
			field := fmt.Sprintf("ipv%d", family)
			raw := str(connection, field)
			if raw == "" {
				continue
			}
			address, err := provisionAddress(raw, family)
			if err != nil {
				return input, err
			}
			dnsName, err := provisionDNSName(connection, fmt.Sprintf("dnsName%d", family))
			if err != nil {
				return input, err
			}
			addressKey := provisionVLANAddress(str(vlan, "id"), address.String())
			if pool := poolByAddress[addressKey]; pool != nil {
				inherited, err := reservationDNS(connection, pool, family)
				if err != nil {
					return input, err
				}
				if dnsName == "" {
					dnsName = inherited
				}
			}
			boundAddresses[addressKey] = true
			canPing, _ := provisionBool(connection, fmt.Sprintf("canPing%d", family), true)
			target := provision.Target{ID: fmt.Sprintf("connection-%s-v%d", str(connection, "id"), family), Kind: "member", Name: str(member, "name"), Address: address.String(), DNSName: dnsName, MemberID: str(member, "id"), ASN: asn, Active: str(connection, "status") == "active" && str(member, "status") == "active", Monitor: true, CanPing: canPing, Busy: busy, VLANID: str(vlan, "id"), VLANName: str(vlan, "name"), InfrastructureID: fabricID(vlan), SwitchIDs: switchIDs, RackIDs: rackIDs, LocationIDs: locationIDs}
			key := memberKey(target.MemberID, target.VLANID, target.Address)
			if memberTargets[key] != "" {
				return input, fmt.Errorf("multiple connections claim the same member address on one VLAN")
			}
			memberTargets[key] = target.ID
			input.Targets = append(input.Targets, target)
		}
	}
	for _, sw := range s.Resources["switches"] {
		if str(sw, "managementIp") == "" {
			continue
		}
		address, err := provisionAddress(str(sw, "managementIp"), 0)
		if err != nil {
			return input, err
		}
		family := 6
		if address.Is4() {
			family = 4
		}
		switchIDs, rackIDs, locationIDs, err := index.placement([]string{str(sw, "id")})
		if err != nil {
			return input, err
		}
		dnsName, err := provisionDNSName(sw, "dnsName")
		if err != nil {
			return input, err
		}
		monitor, _ := provisionBool(sw, "monitor", true)
		input.Targets = append(input.Targets, provision.Target{ID: fmt.Sprintf("switch-%s-v%d", str(sw, "id"), family), Kind: "switch", Name: str(sw, "name"), Address: address.String(), DNSName: dnsName, Active: sw["active"] != false, Monitor: monitor, CanPing: true, InfrastructureID: fabricID(sw), SwitchID: str(sw, "id"), SwitchIDs: switchIDs, RackIDs: rackIDs, LocationIDs: locationIDs, VendorID: str(sw, "vendorId"), Model: str(sw, "model")})
	}
	for _, router := range s.Resources["routers"] {
		vlan, err := index.required("vlans", str(router, "vlanId"))
		if err != nil {
			return input, err
		}
		asn, err := provisionASN(router)
		if err != nil {
			return input, err
		}
		monitor, _ := provisionBool(router, "monitor", true)
		for _, family := range []int{4, 6} {
			raw := str(router, fmt.Sprintf("localIpv%d", family))
			if raw == "" {
				continue
			}
			address, err := provisionAddress(raw, family)
			if err != nil {
				return input, err
			}
			addressKey := provisionVLANAddress(str(vlan, "id"), address.String())
			if pool := poolByAddress[addressKey]; pool != nil && str(pool, "memberId") != "" {
				return input, fmt.Errorf("router address is reserved for a member")
			}
			boundAddresses[addressKey] = true
			dnsName, err := provisionDNSName(router, fmt.Sprintf("dnsName%d", family))
			if err != nil {
				return input, err
			}
			input.Targets = append(input.Targets, provision.Target{ID: fmt.Sprintf("router-%s-v%d", str(router, "id"), family), Kind: "router", Name: str(router, "name"), Address: address.String(), DNSName: dnsName, ASN: asn, Active: true, Monitor: monitor, CanPing: true, VLANID: str(vlan, "id"), VLANName: str(vlan, "name"), InfrastructureID: fabricID(vlan), RouterID: str(router, "id")})
		}
	}
	for _, pool := range s.Resources["ip-addresses"] {
		name, err := provisionDNSName(pool, "dnsName")
		if err != nil {
			return input, err
		}
		if name == "" || str(pool, "memberId") == "" || boundAddresses[provisionVLANAddress(str(pool, "vlanId"), str(pool, "address"))] {
			continue
		}
		member, err := index.required("members", str(pool, "memberId"))
		if err != nil {
			return input, err
		}
		vlan, err := index.required("vlans", str(pool, "vlanId"))
		if err != nil {
			return input, err
		}
		address, err := provisionAddress(str(pool, "address"), 0)
		if err != nil {
			return input, err
		}
		family := 6
		if address.Is4() {
			family = 4
		}
		asn := uint32(0)
		if raw, exists := member["asn"]; exists && raw != nil && raw != "" {
			asn, err = provisionASN(member)
			if err != nil {
				return input, err
			}
		}
		input.Targets = append(input.Targets, provision.Target{ID: fmt.Sprintf("reservation-%s-v%d", str(pool, "id"), family), Kind: "member", Name: str(member, "name"), Address: address.String(), DNSName: name, MemberID: str(member, "id"), ASN: asn, Active: false, Monitor: false, CanPing: false, VLANID: str(vlan, "id"), VLANName: str(vlan, "name"), InfrastructureID: fabricID(vlan)})
	}
	for _, peer := range s.Resources["peers"] {
		router, err := index.required("routers", str(peer, "routerId"))
		if err != nil {
			return input, err
		}
		if _, err = index.required("members", str(peer, "memberId")); err != nil {
			return input, err
		}
		address, err := provisionAddress(str(peer, "address"), 0)
		if err != nil {
			return input, err
		}
		monitor, _ := provisionBool(peer, "monitor", true)
		enabled, _ := peer["enabled"].(bool)
		input.Sessions = append(input.Sessions, provision.Session{ID: str(peer, "id"), TargetID: memberTargets[memberKey(str(peer, "memberId"), str(router, "vlanId"), address.String())], RouterID: str(router, "id"), RouterRole: routerRole(router), Enabled: enabled, Monitor: monitor})
	}
	sort.Slice(input.Targets, func(i, j int) bool { return input.Targets[i].ID < input.Targets[j].ID })
	sort.Slice(input.Sessions, func(i, j int) bool { return input.Sessions[i].ID < input.Sessions[j].ID })
	coreMonitoringInput(s, &input)
	if err := provision.ValidateInput(input); err != nil {
		return input, err
	}
	// Only explicit typed bundles are projected. Unbundled physical links never
	// become inferred core monitoring objects.
	return input, nil
}
