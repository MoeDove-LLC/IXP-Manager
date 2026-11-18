package app

import (
	"fmt"
	"net/netip"
	"strings"
)

func init() {
	for name, field := range (Schema{
		"registeredName": textField(false), "jurisdiction": textField(false), "registrationNotes": {Type: "longtext"},
		"billingCity": textField(false), "billingPostalCode": textField(false), "billingCountry": textField(false), "billingPhone": textField(false),
		"billingNotes": {Type: "longtext"}, "vatRate": {Type: "decimal", Min: 0, Max: 100}, "purchaseOrderRequired": {Type: "boolean"},
		"purchaseOrderNumber": textField(false), "invoiceMethod": enum("email", "post"), "invoiceEmail": {Type: "email"},
		"billingFrequency": enum("monthly", "2monthly", "quarterly", "halfyearly", "annually", "none"),
		"shortName":        textField(false), "abbreviatedName": textField(false), "noc24hPhone": textField(false), "nocEmail": {Type: "email"}, "nocHours": textField(false), "nocWebsite": {Type: "url"},
		"peeringPolicy": enum("open", "selective", "restrictive", "unknown"), "irrASSet": textField(false), "irrASSet6": textField(false),
		"resellerId": ref("members", false), "isReseller": {Type: "boolean"}, "excludeIxf": {Type: "boolean"},
	}) {
		Schemas["members"][name] = field
	}
	Schemas["members"]["asn"] = num(1, 4294967294, false)
	Schemas["members"]["notes"] = Field{Type: "longtext"}
	Schemas["notes"]["body"] = Field{Type: "longtext", Required: true}
	Schemas["tags"]["internal"] = Field{Type: "boolean"}
	Schemas["tags"]["displayAs"] = textField(false)
	for name, field := range (Schema{"mobile": textField(false), "position": textField(false), "notes": {Type: "longtext"}}) {
		Schemas["contacts"][name] = field
	}
	for name, field := range (Schema{"type": textField(false), "active": {Type: "boolean"}, "limitedTo": num(0, 3, false)}) {
		Schemas["contact-groups"][name] = field
	}
	Schemas["vendors"]["shortName"] = textField(false)
	Schemas["vendors"]["bundleName"] = textField(false)
	Schemas["equipment"]["memberId"] = ref("members", false)
	Schemas["equipment"]["unitStart"] = num(1, 100, false)
	Schemas["equipment"]["unitHeight"] = num(1, 100, false)
	Schemas["switches"]["unitStart"] = num(1, 100, false)
	Schemas["switches"]["unitHeight"] = num(1, 100, false)
	Schemas["switches"]["serialNumber"] = textField(false)
	Schemas["switches"]["assetTag"] = textField(false)
}
func validateExtended(s *State, kind string, r Record) error {
	if kind == "members" {
		if number(r, "asn") == 0 && str(r, "type") != "associate" && str(r, "type") != "internal" {
			return fmt.Errorf("peering members require an ASN; only associate/internal organizations may omit it")
		}
		if parent := str(r, "resellerId"); parent != "" {
			if parent == str(r, "id") {
				return fmt.Errorf("member cannot resell to itself")
			}
			visited := map[string]bool{str(r, "id"): true}
			for parent != "" {
				if visited[parent] {
					return fmt.Errorf("reseller relationship contains a cycle")
				}
				visited[parent] = true
				owner := find(s, "members", parent)
				if owner == nil || owner["isReseller"] != true {
					return fmt.Errorf("resellerId must refer to a designated reseller")
				}
				parent = str(owner, "resellerId")
			}
		}
	}
	if kind == "connections" || kind == "peers" {
		m := find(s, "members", str(r, "memberId"))
		if number(m, "asn") == 0 {
			return fmt.Errorf("network connections require a member with an ASN")
		}
	}
	if kind == "equipment" || kind == "switches" {
		start, height := number(r, "unitStart"), number(r, "unitHeight")
		if (start > 0) != (height > 0) {
			return fmt.Errorf("rack placement requires both unitStart and unitHeight")
		}
		if start > 0 {
			rack := find(s, "racks", str(r, "rackId"))
			if rack == nil {
				return fmt.Errorf("rack placement requires a rack")
			}
			units := number(rack, "units")
			if units == 0 || start+height-1 > units {
				return fmt.Errorf("equipment exceeds rack height")
			}
			for _, otherKind := range []string{"equipment", "switches"} {
				for _, other := range s.Resources[otherKind] {
					if otherKind == kind && str(other, "id") == str(r, "id") {
						continue
					}
					if str(other, "rackId") == str(r, "rackId") && number(other, "unitStart") > 0 && start < number(other, "unitStart")+number(other, "unitHeight") && number(other, "unitStart") < start+height {
						return fmt.Errorf("rack unit range overlaps existing equipment")
					}
				}
			}
		}
	}
	if kind == "ip-addresses" || kind == "connections" || kind == "routers" {
		return validateAddressOwnership(s)
	}
	return nil
}
func validateAddressOwnership(s *State) error {
	// Address-pool records and active configuration share one ownership boundary;
	// an unassigned pool address is not permission to overwrite a live endpoint.
	pools := map[string]Record{}
	for _, p := range s.Resources["ip-addresses"] {
		pools[str(p, "vlanId")+"\x00"+str(p, "address")] = p
	}
	for _, c := range s.Resources["connections"] {
		for _, field := range []string{"ipv4", "ipv6"} {
			ip := str(c, field)
			if ip == "" {
				continue
			}
			if pool := pools[str(c, "vlanId")+"\x00"+ip]; pool != nil && str(pool, "memberId") != "" && str(pool, "memberId") != str(c, "memberId") {
				return fmt.Errorf("connection address is reserved for another member")
			}
		}
	}
	for _, router := range s.Resources["routers"] {
		for _, field := range []string{"localIpv4", "localIpv6"} {
			if ip := str(router, field); ip != "" {
				if pool := pools[str(router, "vlanId")+"\x00"+ip]; pool != nil && str(pool, "memberId") != "" {
					return fmt.Errorf("router address is reserved for a member")
				}
			}
		}
	}
	return nil
}
func normalizedMAC(s string) string { return strings.ToLower(s) }
func usablePoolAddress(address netip.Addr, prefix netip.Prefix) bool {
	if !address.IsGlobalUnicast() || !prefix.Contains(address) {
		return false
	}
	if address.Is6() && prefix.Bits() < 127 && address == prefix.Masked().Addr() {
		return false
	}
	if address.Is4() && prefix.Bits() < 31 {
		if address == prefix.Masked().Addr() {
			return false
		}
		b := address.As4()
		network := prefix.Masked().Addr().As4()
		v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
		n := uint32(network[0])<<24 | uint32(network[1])<<16 | uint32(network[2])<<8 | uint32(network[3])
		if v == n|(^uint32(0)>>prefix.Bits()) {
			return false
		}
	}
	return true
}
