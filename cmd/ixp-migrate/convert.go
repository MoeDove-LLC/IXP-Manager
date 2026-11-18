package main

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"github.com/moedove/ixp-manager/internal/core"
	"github.com/moedove/ixp-manager/internal/migrate"
)

type row map[string]any
type legacyExport struct {
	Tables map[string][]row `json:"tables"`
}
type export struct {
	LogoSources    []migrate.LogoSource          `json:"-"`
	Docstore       *migrate.DocstoreImport       `json:"docstore,omitempty"`
	Version        int                           `json:"version"`
	Source         string                        `json:"source"`
	Resources      map[string][]row              `json:"resources"`
	Warnings       []string                      `json:"warnings"`
	CoreBundles    []core.Bundle                 `json:"coreBundles,omitempty"`
	SFlowReceivers []migrate.SFlowReceiverImport `json:"sflowReceivers,omitempty"`
	Patching       *migrate.PatchingImport       `json:"patching,omitempty"`
}

type converter struct {
	source                 legacyExport
	result                 export
	index                  map[string]map[string]row
	used                   map[string]map[string]bool
	exported               map[string]map[string]bool
	resolved               map[string]map[int64]bool
	addressClaims          map[string]legacyAddressClaim
	addressReferences      map[string][]row
	unsafeAddressOwnership bool
	coreGraph              *legacyCoreGraph
	unsafeCoreOwnership    bool
	peerOptions            peerDraftOptions
}

func convert(source legacyExport) (export, error) {
	return convertWithPeerDrafts(source, peerDraftOptions{})
}

func convertWithPeerDrafts(source legacyExport, peerOptions peerDraftOptions) (export, error) {
	if source.Tables == nil {
		return export{}, fmt.Errorf("required object tables is missing")
	}
	if err := peerOptions.validate(); err != nil {
		return export{}, err
	}
	if err := peerOptions.validateColumns(source); err != nil {
		return export{}, err
	}
	normalized, err := normalizePivots(source)
	if err != nil {
		return export{}, err
	}
	source = normalized
	c := &converter{source: source, result: export{Version: 1, Source: "ixp-manager-legacy", Resources: map[string][]row{}, Warnings: []string{}}, index: map[string]map[string]row{}, used: map[string]map[string]bool{}, exported: map[string]map[string]bool{}, resolved: map[string]map[int64]bool{}}
	c.peerOptions = peerOptions
	if source.Tables == nil {
		return c.result, fmt.Errorf("required object tables is missing")
	}
	// Validate IDs before conversion. All supported source tables use positive
	// integer IDs; number decoding must not pass through float64.
	for _, table := range []string{"cust", "location", "cabinet", "switch", "switchport", "vlan", "networkinfo", "routers", "contact", "virtualinterface", "physicalinterface", "vlaninterface", "ipv4address", "ipv6address", "company_billing_detail", "company_registration_detail", "cust_tag", "cust_to_cust_tag", "cust_notes", "contact_group", "contact_to_group", "vendor", "custkit", "infrastructure", "corebundles", "corelinks", "coreinterfaces", "l2address", "sflow_receiver", "patch_panel", "patch_panel_port", "patch_panel_port_history", "patch_panel_port_file", "patch_panel_port_history_file", "console_server", "consoleserverconnection", "logos", "docstore_directories", "docstore_customer_directories", "docstore_files", "docstore_customer_files", "docstore_logs"} {
		c.index[table] = map[string]row{}
		c.used[table] = map[string]bool{}
		for i, r := range source.Tables[table] {
			id, err := integer(r["id"])
			_, isBool := r["id"].(bool)
			if err != nil || id < 1 || isBool {
				return c.result, fmt.Errorf("%s row %d: id must be a positive integer", table, i+1)
			}
			key := strconv.FormatInt(id, 10)
			if c.index[table][key] != nil {
				return c.result, fmt.Errorf("%s: duplicate id %s", table, key)
			}
			c.index[table][key] = r
			c.used[table][key+":id"] = true
		}
	}
	if err := c.prepareCoreGraph(); err != nil {
		return c.result, err
	}
	for _, r := range source.Tables["cust"] {
		o := c.base("cust", "members", r)
		o["name"] = c.str("cust", r, "name")
		o["asn"] = c.num("cust", r, "autsys")
		o["email"] = c.str("cust", r, "peeringemail")
		if o["email"] == "" {
			o["email"] = c.str("cust", r, "nocemail")
		}
		o["website"] = c.str("cust", r, "corpwww")
		status := c.num("cust", r, "status")
		o["status"] = map[int64]string{1: "active", 2: "pending", 3: "suspended"}[status]
		if o["status"] == "" {
			c.warn("cust", r, "unknown status; imported as suspended")
			o["status"] = "suspended"
		}
		c.membersExtended(r, o)
		asn := o["asn"].(int64)
		allowNoASN := asn == 0 && (o["type"] == "associate" || o["type"] == "internal")
		if !c.nameOK("cust", r, o) || (!allowNoASN && (asn < 1 || asn > 4294967294)) {
			c.warn("cust", r, "missing or invalid ASN/name; member was not converted")
			continue
		}
		if allowNoASN {
			delete(o, "asn")
		}
		c.add("members", r, o)
	}
	c.extendedAfterMembers()
	for _, r := range source.Tables["location"] {
		o := c.base("location", "locations", r)
		for _, f := range []string{"name", "city", "country", "address"} {
			o[f] = c.str("location", r, f)
		}
		if c.nameOK("location", r, o) {
			c.add("locations", r, o)
		}
	}
	for _, r := range source.Tables["cabinet"] {
		o := c.base("cabinet", "racks", r)
		o["name"] = c.str("cabinet", r, "name")
		o["locationId"] = c.ref("cabinet", r, "locationid", "locations")
		if n := c.num("cabinet", r, "height"); n > 0 {
			o["units"] = n
		}
		if c.nameOK("cabinet", r, o) && c.refsOK("cabinet", r, o, "locationId") {
			c.add("racks", r, o)
		}
	}
	for _, r := range source.Tables["switch"] {
		o := c.base("switch", "switches", r)
		o["name"] = c.str("switch", r, "name")
		o["rackId"] = c.ref("switch", r, "cabinetid", "racks")
		cab := c.lookup("switch", r, "cabinetid", "cabinet")
		if cab != nil {
			o["locationId"] = c.ref("cabinet", cab, "locationid", "locations")
		}
		if raw := c.num("switch", r, "infrastructure"); raw > 0 {
			if fabric := c.ref("switch", r, "infrastructure", "infrastructures"); fabric != "" {
				o["infrastructureId"] = fabric
			} else {
				c.warn("switch", r, "infrastructure not converted; switch skipped")
				continue
			}
		}
		o["model"] = c.str("switch", r, "model")
		if hostname, ok := c.dnsHostname("switch", r, "hostname"); ok && hostname != "" {
			o["dnsName"] = hostname
		}
		if id := c.ref("switch", r, "vendorid", "vendors"); id != "" {
			o["vendorId"] = id
		}
		c.textFields("switch", r, o, map[string]string{"serialNumber": "serialNumber"})
		o["managementIp"] = c.str("switch", r, "ipv4addr")
		if o["managementIp"] == "" {
			o["managementIp"] = c.str("switch", r, "ipv6addr")
		}
		if err := c.coreSwitchFields(r, o); err != nil {
			return c.result, err
		}
		if c.nameOK("switch", r, o) && c.refsOK("switch", r, o, "rackId", "locationId") {
			c.add("switches", r, o)
		}
	}
	for _, r := range source.Tables["switchport"] {
		o := c.base("switchport", "ports", r)
		o["name"] = c.str("switchport", r, "name")
		if o["name"] == "" {
			o["name"] = c.str("switchport", r, "ifName")
		}
		o["switchId"] = c.ref("switchport", r, "switchid", "switches")
		if n := c.num("switchport", r, "ifHighSpeed"); n > 0 {
			o["speed"] = n
		}
		o["status"] = "inactive"
		if c.num("switchport", r, "active") == 1 {
			o["status"] = "active"
		}
		if c.coreGraph.ports[idOf(r)] {
			o["coreReserved"] = true
		}
		if c.nameOK("switchport", r, o) && c.refsOK("switchport", r, o, "switchId") {
			c.add("ports", r, o)
		}
	}
	for _, r := range source.Tables["vlan"] {
		o := c.base("vlan", "vlans", r)
		o["name"] = c.str("vlan", r, "name")
		o["tag"] = c.num("vlan", r, "number")
		if raw := c.num("vlan", r, "infrastructureid"); raw > 0 {
			if fabric := c.ref("vlan", r, "infrastructureid", "infrastructures"); fabric != "" {
				o["infrastructureId"] = fabric
			} else {
				c.warn("vlan", r, "infrastructure not converted; VLAN skipped")
				continue
			}
		}
		for old, key := range map[string]string{"private": "private", "peering_matrix": "peeringMatrix", "peering_manager": "peeringManager"} {
			if r[old] != nil {
				o[key] = c.num("vlan", r, old) != 0
			}
		}
		if r["export_to_ixf"] != nil {
			o["excludeIxf"] = c.num("vlan", r, "export_to_ixf") == 0
		}
		if text := c.str("vlan", r, "notes"); text != "" {
			o["notes"] = text
		}
		if o["tag"].(int64) < 1 || o["tag"].(int64) > 4094 {
			c.warn("vlan", r, "tag outside 1..4094; not converted")
			continue
		}
		for _, network := range source.Tables["networkinfo"] {
			if c.num("networkinfo", network, "vlanid") != idOf(r) {
				continue
			}
			proto := c.num("networkinfo", network, "protocol")
			field := map[int64]string{4: "ipv4Prefix", 6: "ipv6Prefix"}[proto]
			prefix, err := netip.ParsePrefix(fmt.Sprintf("%s/%d", c.str("networkinfo", network, "network"), c.num("networkinfo", network, "masklen")))
			if err != nil || field == "" || (prefix.Addr().Is4() != (proto == 4)) {
				c.warn("networkinfo", network, "invalid prefix or protocol; not converted")
				continue
			}
			canonical := prefix.Masked().String()
			if o[field] != nil {
				if o[field] != canonical {
					extra, _ := o["prefixes"].([]string)
					found := false
					for _, existing := range extra {
						if existing == canonical {
							found = true
						}
					}
					if !found {
						o["prefixes"] = append(extra, canonical)
					}
				}
			} else {
				o[field] = canonical
			}
			c.resolve("networkinfo", network)
		}
		if c.nameOK("vlan", r, o) {
			c.add("vlans", r, o)
		}
	}
	c.routers()
	for _, r := range source.Tables["contact"] {
		o := c.base("contact", "contacts", r)
		for _, f := range []string{"name", "email", "phone"} {
			o[f] = c.str("contact", r, f)
		}
		o["memberId"] = c.ref("contact", r, "custid", "members")
		c.textFields("contact", r, o, map[string]string{"mobile": "mobile", "position": "position", "notes": "notes"})
		if o["email"] == "" {
			c.warn("contact", r, "email is required; not converted")
			continue
		}
		if c.nameOK("contact", r, o) && c.refsOK("contact", r, o, "memberId") {
			c.add("contacts", r, o)
		}
	}
	if err := c.convertCoreBundles(); err != nil {
		return c.result, err
	}
	c.connections()
	if err := c.registeredMACs(); err != nil {
		return c.result, err
	}
	if err := c.sflowReceivers(); err != nil {
		return c.result, err
	}
	c.extendedAfterContacts()
	c.addressPools()
	if err := c.consoles(); err != nil {
		return c.result, err
	}
	if err := c.patching(); err != nil {
		return c.result, err
	}
	if err := c.logos(); err != nil {
		return c.result, err
	}
	if err := c.docstore(); err != nil {
		return c.result, err
	}
	if c.peerOptions.Enabled && !c.unsafeAddressOwnership && !c.unsafeCoreOwnership {
		if err := c.makePeerDrafts(); err != nil {
			c.unmapped()
			sort.Strings(c.result.Warnings)
			return c.result, err
		}
	}
	c.unmapped()
	sort.Strings(c.result.Warnings)
	if c.unsafeAddressOwnership {
		return c.result, fmt.Errorf("referenced address ownership is ambiguous or missing; fix the source ownership and retry; no safe import file can be produced")
	}
	if c.unsafeCoreOwnership {
		return c.result, fmt.Errorf("core interface ownership is ambiguous or incomplete; fix the source topology; no safe import file can be produced")
	}
	return c.result, nil
}

func (c *converter) connections() {
	for _, r := range c.source.Tables["vlaninterface"] {
		vi := c.lookup("vlaninterface", r, "virtualinterfaceid", "virtualinterface")
		if vi == nil {
			c.warn("vlaninterface", r, "missing virtual interface; not converted")
			continue
		}
		if c.coreGraph.vis[idOf(vi)] {
			c.warn("vlaninterface", r, "core virtual interface cannot be converted to member access")
			c.unsafeCoreOwnership = true
			continue
		}
		physical := []row{}
		for _, p := range c.source.Tables["physicalinterface"] {
			if c.num("physicalinterface", p, "virtualinterfaceid") == idOf(vi) {
				physical = append(physical, p)
			}
		}
		if len(physical) == 0 || len(physical) > 64 {
			c.warn("vlaninterface", r, "connection requires 1..64 physical ports; not converted")
			continue
		}
		sort.Slice(physical, func(i, j int) bool { return idOf(physical[i]) < idOf(physical[j]) })
		o := c.base("vlaninterface", "connections", r)
		o["name"] = c.str("virtualinterface", vi, "name")
		if o["name"] == "" {
			o["name"] = fmt.Sprintf("Legacy connection %d", idOf(r))
		}
		o["memberId"] = c.ref("virtualinterface", vi, "custid", "members")
		o["vlanId"] = c.ref("vlaninterface", r, "vlanid", "vlans")
		mode := "single"
		framing := c.num("virtualinterface", vi, "lag_framing")
		if len(physical) > 1 || framing == 1 {
			mode = "lag"
		}
		o["linkMode"] = mode
		valid := true
		if mtu := c.num("virtualinterface", vi, "mtu"); mtu != 0 {
			if mtu < 1280 || mtu > 9216 {
				c.warn("virtualinterface", vi, "MTU outside supported 1280..9216; connection not converted")
				valid = false
			} else {
				o["mtu"] = mtu
			}
		}
		ports := []string{}
		seen := map[string]bool{}
		portRecords := []row{}
		speeds := []int64{}
		sum := int64(0)
		known := true
		status := "active"
		rank := map[string]int{"active": 1, "pending": 2, "disabled": 3, "quarantine": 4}
		for _, p := range physical {
			id := c.ref("physicalinterface", p, "switchportid", "ports")
			if id == "" || seen[id] {
				c.warn("physicalinterface", p, "missing or duplicate converted physical port; entire connection not converted")
				valid = false
				continue
			}
			seen[id] = true
			var port row
			for _, existing := range c.result.Resources["ports"] {
				if existing["id"] == id {
					port = existing
					break
				}
			}
			if port["memberId"] != nil && port["memberId"] != o["memberId"] {
				c.warn("physicalinterface", p, "physical port already assigned to another member; entire connection not converted")
				valid = false
			}
			speed, _ := port["speed"].(int64)
			configured := c.num("physicalinterface", p, "speed")
			if speed <= 0 {
				speed = configured
			}
			if speed < 0 || speed > 10000000 {
				c.warn("physicalinterface", p, "physical speed outside supported range; connection not converted")
				valid = false
			}
			if speed > 0 {
				sum += speed
			} else {
				known = false
			}
			current := map[int64]string{1: "active", 2: "disabled", 3: "pending", 4: "pending", 5: "quarantine"}[c.num("physicalinterface", p, "status")]
			if current == "" {
				c.warn("physicalinterface", p, "unknown status; imported as disabled")
				current = "disabled"
			}
			if port["status"] == "inactive" && current == "active" {
				current = "disabled"
			}
			if rank[current] > rank[status] {
				status = current
			}
			ports = append(ports, id)
			portRecords = append(portRecords, port)
			speeds = append(speeds, speed)
		}
		if len(ports) == 1 {
			o["portId"] = ports[0]
		} else {
			o["portIds"] = ports
		}
		if known && sum > 0 {
			o["speed"] = sum
		}
		o["status"] = status
		if !c.monitorPreferences(r, o) {
			valid = false
		}
		addresses := map[string]row{}
		for _, protocol := range []string{"4", "6"} {
			family := "ipv" + protocol
			if c.num("vlaninterface", r, family+"enabled") != 1 {
				continue
			}
			a := c.lookup("vlaninterface", r, family+"addressid", family+"address")
			if a == nil {
				c.warn("vlaninterface", r, "enabled IP address is missing; connection not converted")
				valid = false
				continue
			}
			claim := c.addressClaim(family+"address", a)
			if !claim.valid || claim.memberID != o["memberId"] {
				valid = false
				continue
			}
			if c.num(family+"address", a, "vlanid") != c.num("vlaninterface", r, "vlanid") {
				c.warn("vlaninterface", r, "address belongs to a different VLAN; connection not converted")
				valid = false
				continue
			}
			address, err := netip.ParseAddr(c.str(family+"address", a, "address"))
			if err != nil || address.Is4() != (protocol == "4") {
				c.warn("vlaninterface", r, "invalid IP address; connection not converted")
				valid = false
				continue
			}
			o[family] = address.String()
			if claim.dnsName != "" {
				o["dnsName"+protocol] = claim.dnsName
			}
			addresses[family+"address"] = a
		}
		if valid && c.refsOK("vlaninterface", r, o, "memberId", "vlanId") {
			for i, port := range portRecords {
				port["memberId"] = o["memberId"]
				if speeds[i] > 0 {
					port["speed"] = speeds[i]
				}
			}
			c.add("connections", r, o)
			c.resolve("virtualinterface", vi)
			for _, p := range physical {
				c.resolve("physicalinterface", p)
			}
			for table, a := range addresses {
				c.resolve(table, a)
			}
		}
	}
}
func (c *converter) resolve(table string, r row) {
	if c.resolved[table] == nil {
		c.resolved[table] = map[int64]bool{}
	}
	c.resolved[table][idOf(r)] = true
}

func (c *converter) base(table, kind string, r row) row {
	o := row{"id": legacyID(kind, idOf(r))}
	// Keep legacy timestamps only in the source backup; importing resources will
	// establish trustworthy new-system timestamps and an import audit entry.
	return o
}
func legacyID(kind string, id int64) string { return fmt.Sprintf("legacy-%s-%d", kind, id) }
func idOf(r row) int64                      { n, _ := integer(r["id"]); return n }
func integer(v any) (int64, error) {
	switch v := v.(type) {
	case json.Number:
		return v.Int64()
	case string:
		return strconv.ParseInt(v, 10, 64)
	case bool:
		if v {
			return 1, nil
		}
		return 0, nil
	case nil:
		return 0, fmt.Errorf("null")
	default:
		return 0, fmt.Errorf("not an integer")
	}
}
func (c *converter) str(table string, r row, field string) string {
	c.used[table][strconv.FormatInt(idOf(r), 10)+":"+field] = true
	if r[field] == nil {
		return ""
	}
	if v, ok := r[field].(string); ok {
		return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(v, "\r\n", "\n"), "\r", "\n"))
	}
	c.warn(table, r, field+" is not text; left empty")
	return ""
}
func (c *converter) num(table string, r row, field string) int64 {
	c.used[table][strconv.FormatInt(idOf(r), 10)+":"+field] = true
	if r[field] == nil {
		return 0
	}
	n, err := integer(r[field])
	if err != nil {
		c.warn(table, r, field+" is not an integer; treated as unset")
	}
	return n
}
func (c *converter) lookup(table string, r row, field, target string) row {
	return c.index[target][strconv.FormatInt(c.num(table, r, field), 10)]
}
func (c *converter) ref(table string, r row, field, target string) string {
	id := c.num(table, r, field)
	if id < 1 || !c.exported[target][legacyID(target, id)] {
		return ""
	}
	return legacyID(target, id)
}
func (c *converter) refsOK(table string, r, o row, fields ...string) bool {
	for _, field := range fields {
		if o[field] == nil || o[field] == "" {
			c.warn(table, r, "missing converted reference "+field+"; record not converted")
			return false
		}
	}
	return true
}
func (c *converter) nameOK(table string, r, o row) bool {
	if o["name"] == "" {
		c.warn(table, r, "missing name; record not converted")
		return false
	}
	return true
}
func (c *converter) add(kind string, r, o row) {
	if c.exported[kind] == nil {
		c.exported[kind] = map[string]bool{}
	}
	c.exported[kind][o["id"].(string)] = true
	c.result.Resources[kind] = append(c.result.Resources[kind], o)
}
func (c *converter) warn(table string, r row, message string) {
	c.result.Warnings = append(c.result.Warnings, fmt.Sprintf("%s id %d: %s", table, idOf(r), message))
}
func (c *converter) unmapped() {
	for table, rows := range c.source.Tables {
		if len(rows) == 0 {
			continue
		}
		if c.used[table] == nil {
			if table == "macaddress" {
				c.result.Warnings = append(c.result.Warnings, fmt.Sprintf("table macaddress: %d learned MAC observations are excluded; discovery never becomes registered MAC or receiver authorization", len(rows)))
				continue
			}
			c.result.Warnings = append(c.result.Warnings, fmt.Sprintf("table %s: %d rows are not supported; preserve source backup and migrate separately", table, len(rows)))
			continue
		}
		fields := map[string]bool{}
		for _, r := range rows {
			for field, v := range r {
				if !c.used[table][strconv.FormatInt(idOf(r), 10)+":"+field] && v != nil && v != "" {
					fields[field] = true
				}
			}
		}
		keys := make([]string, 0, len(fields))
		for f := range fields {
			keys = append(keys, f)
		}
		sort.Strings(keys)
		if len(keys) > 0 {
			c.result.Warnings = append(c.result.Warnings, fmt.Sprintf("table %s: fields not migrated: %s", table, strings.Join(keys, ", ")))
		}
		if table == "ipv4address" || table == "ipv6address" || table == "virtualinterface" || table == "physicalinterface" || table == "networkinfo" || table == "company_billing_detail" || table == "company_registration_detail" {
			remaining := 0
			for _, r := range rows {
				if !c.resolved[table][idOf(r)] {
					remaining++
				}
			}
			if remaining > 0 {
				c.result.Warnings = append(c.result.Warnings, fmt.Sprintf("table %s: %d unassigned or unresolved standalone records are not migrated; preserve source backup", table, remaining))
			}
		}
	}
}
