package app

import (
	"fmt"
	"math"
	"net"
	"net/mail"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Field struct {
	Type     string   `json:"type"`
	Required bool     `json:"required,omitempty"`
	Ref      string   `json:"ref,omitempty"`
	Options  []string `json:"options,omitempty"`
	Min      float64  `json:"min,omitempty"`
	Max      float64  `json:"max,omitempty"`
}
type Schema map[string]Field

func textField(required bool) Field { return Field{Type: "string", Required: required} }
func ref(kind string, required bool) Field {
	return Field{Type: "string", Ref: kind, Required: required}
}
func enum(options ...string) Field { return Field{Type: "string", Options: options} }
func num(min, max float64, required bool) Field {
	return Field{Type: "number", Min: min, Max: max, Required: required}
}

var Schemas = map[string]Schema{
	"members":   {"name": textField(true), "asn": num(1, 4294967294, true), "status": enum("active", "pending", "suspended"), "email": {Type: "email"}, "website": {Type: "url"}, "notes": textField(false)},
	"locations": {"name": textField(true), "city": textField(false), "country": textField(false), "address": textField(false)},
	"racks":     {"name": textField(true), "locationId": ref("locations", true), "units": num(1, 100, false)},
	"switches":  {"name": textField(true), "locationId": ref("locations", true), "rackId": ref("racks", false), "managementIp": {Type: "ip"}, "model": textField(false), "vendorId": ref("vendors", false)},
	"ports":     {"name": textField(true), "switchId": ref("switches", true), "speed": num(1, 10000000, false), "status": enum("active", "inactive"), "memberId": ref("members", false)},
	"vlans": {
		"name": textField(true), "tag": num(1, 4094, true), "ipv4Prefix": {Type: "prefix4"}, "ipv6Prefix": {Type: "prefix6"}, "infrastructureId": ref("infrastructures", false),
		"legacyRouterIPv4": {Type: "boolean"}, "legacyRouterIPv6": {Type: "boolean"},
	},
	"connections": {
		"name": textField(true), "memberId": ref("members", true), "portId": ref("ports", false), "portIds": {Type: "references", Ref: "ports"},
		"linkMode": enum("single", "lag"), "mtu": num(1280, 9216, false), "vlanId": ref("vlans", true), "ipv4": {Type: "ipv4"}, "ipv6": {Type: "ipv6"},
		"speed": num(1, 640000000, false), "status": enum("active", "pending", "disabled", "quarantine"),
		"legacyRSClientIPv4": {Type: "boolean"}, "legacyRSClientIPv6": {Type: "boolean"},
	},
	"routers":             {"name": textField(true), "platform": enum("bird2", "frr", "routeros7", "junos", "bird3", "openbgpd", "cisco", "quagga", "bird1"), "asn": num(1, 4294967294, true), "routerId": {Type: "ipv4", Required: true}, "localIpv4": {Type: "ipv4"}, "localIpv6": {Type: "ipv6"}, "vlanId": ref("vlans", true)},
	"peers":               {"name": textField(true), "routerId": ref("routers", true), "memberId": ref("members", true), "address": {Type: "ip", Required: true}, "asn": num(1, 4294967294, true), "enabled": {Type: "boolean"}, "maxPrefixes": num(1, 1000000, true), "allowedPrefixes": {Type: "prefixes", Required: true}},
	"contacts":            {"name": textField(true), "email": {Type: "email", Required: true}, "memberId": ref("members", true), "phone": textField(false), "role": textField(false)},
	"users":               {"name": textField(true), "email": {Type: "email", Required: true}, "role": enum("admin", "operator", "viewer", "member"), "memberId": ref("members", false), "password": {Type: "password"}},
	"vendors":             {"name": textField(true), "website": {Type: "url"}},
	"infrastructures":     {"name": textField(true), "notes": textField(false)},
	"mac-addresses":       {"name": textField(true), "connectionId": ref("connections", true), "address": {Type: "mac", Required: true}},
	"ip-addresses":        {"name": textField(true), "vlanId": ref("vlans", true), "address": {Type: "ip", Required: true}, "memberId": ref("members", false)},
	"tags":                {"name": textField(true), "description": textField(false)},
	"member-tags":         {"name": textField(true), "memberId": ref("members", true), "tagId": ref("tags", true)},
	"notes":               {"name": textField(true), "memberId": ref("members", true), "body": textField(true), "visibility": enum("internal", "member")},
	"equipment":           {"name": textField(true), "memberId": ref("members", true), "rackId": ref("racks", false), "description": textField(false)},
	"console-servers":     {"name": textField(true), "locationId": ref("locations", true), "managementIp": {Type: "ip", Required: true}},
	"console-connections": {"name": textField(true), "consoleServerId": ref("console-servers", true), "memberId": ref("members", true), "port": num(1, 65535, true)},
	"patch-panels":        {"name": textField(true), "rackId": ref("racks", true), "portCount": num(1, 1024, true)},
	"patch-ports":         {"name": textField(true), "panelId": ref("patch-panels", true), "number": num(1, 1024, true), "memberId": ref("members", false), "status": enum("available", "reserved", "connected"), "circuitId": textField(false), "notes": textField(false)},
	"core-links":          {"name": textField(true), "portAId": ref("ports", true), "portBId": ref("ports", true), "type": enum("layer2", "layer3"), "notes": textField(false)},
	"irr-sources":         {"name": textField(true), "host": textField(true), "port": num(1, 65535, true)},
}
var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

func validate(s *State, kind string, r Record) error {
	if err := validateOwnershipTime(kind, r); err != nil {
		return err
	}
	if err := validateLegacyCustomerEvidence(kind, r); err != nil {
		return err
	}
	schema, ok := Schemas[kind]
	if !ok {
		return fmt.Errorf("unknown resource %s", kind)
	}
	for k := range r {
		if kind == "users" && k == membershipDetachedField {
			continue
		}
		if k == "id" || k == "createdAt" || k == "updatedAt" || k == "passwordHash" || (k == "ownershipSince" && ownsObservations(kind)) || (k == "bindingSince" && kind == "connections") {
			continue
		}
		if _, ok := schema[k]; !ok {
			return fmt.Errorf("unknown field %s", k)
		}
	}
	for k, f := range schema {
		v, exists := r[k]
		if !exists || v == nil || v == "" {
			if f.Required {
				return fmt.Errorf("%s is required", k)
			}
			continue
		}
		var err error
		switch f.Type {
		case "provision-options":
			switch v.(type) {
			case map[string]any, Record:
			default:
				err = fmt.Errorf("must be a typed configuration options object")
			}
		case "decimal":
			n, ok := v.(float64)
			if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n < f.Min || n > f.Max {
				err = fmt.Errorf("must be a number within allowed range")
			}
		case "number":
			n, ok := v.(float64)
			if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n != math.Trunc(n) || n < f.Min || n > f.Max {
				err = fmt.Errorf("must be an integer between %.0f and %.0f", f.Min, f.Max)
			}
		case "document-refs":
			values, ok := v.([]any)
			if !ok || len(values) > 32 {
				err = fmt.Errorf("must contain at most 32 document IDs")
				break
			}
			seen := map[string]bool{}
			for _, raw := range values {
				id, ok := raw.(string)
				if !ok || seen[id] || documentByID(s, id) == nil {
					err = fmt.Errorf("must reference distinct existing documents")
					break
				}
				seen[id] = true
			}
		case "route-bindings":
			if _, ok := v.([]any); !ok {
				err = fmt.Errorf("must be an array of prefix-origin pairs")
			}
		case "boolean":
			if _, ok := v.(bool); !ok {
				err = fmt.Errorf("must be boolean")
			}
		case "references":
			values, ok := v.([]any)
			if !ok || len(values) > 64 || (f.Required && len(values) == 0) {
				err = fmt.Errorf("must contain valid references (maximum64)")
				break
			}
			seen := map[string]bool{}
			for _, raw := range values {
				id, ok := raw.(string)
				if !ok || !identifier.MatchString(id) || seen[id] || find(s, f.Ref, id) == nil {
					err = fmt.Errorf("must contain distinct existing %s IDs", f.Ref)
					break
				}
				seen[id] = true
			}
		case "prefixes":
			a, ok := v.([]any)
			if !ok || len(a) > 10000 {
				err = fmt.Errorf("must be an array of at most 10000 prefixes")
				break
			}
			for i, p := range a {
				t, ok := p.(string)
				if !ok {
					err = fmt.Errorf("invalid prefix")
					break
				}
				prefix, e := netip.ParsePrefix(t)
				if e != nil || prefix != prefix.Masked() {
					err = fmt.Errorf("prefix must be canonical CIDR")
					break
				}
				a[i] = prefix.String()
			}
		default:
			t, ok := v.(string)
			maxLength := 10000
			if f.Type == "longtext" {
				maxLength = 65536
			}
			if !ok || len(t) > maxLength || strings.ContainsAny(t, "\x00\r") {
				err = fmt.Errorf("invalid text")
				break
			}
			if f.Required && strings.TrimSpace(t) == "" {
				err = fmt.Errorf("cannot be blank")
			}
			switch f.Type {
			case "date":
				if _, e := time.Parse("2006-01-02", t); e != nil {
					err = fmt.Errorf("must be YYYY-MM-DD")
				}
			case "email":
				a, e := mail.ParseAddress(t)
				if e != nil || a.Address != t {
					err = fmt.Errorf("invalid email")
				}
			case "url":
				u, e := url.Parse(t)
				if e != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
					err = fmt.Errorf("must be an HTTP(S) URL")
				}
			case "password":
				if len(t) < 12 || len(t) > 128 {
					err = fmt.Errorf("must contain 12 to 128 bytes")
				}
			case "ip", "ipv4", "ipv6":
				a, e := netip.ParseAddr(t)
				if e != nil || a.Zone() != "" || a.Is4In6() || a.IsUnspecified() || a.IsMulticast() || (f.Type == "ipv4" && !a.Is4()) || (f.Type == "ipv6" && !a.Is6()) {
					err = fmt.Errorf("invalid %s address", f.Type)
				} else {
					r[k] = a.String()
				}
			case "prefix4", "prefix6":
				p, e := netip.ParsePrefix(t)
				if e != nil || p != p.Masked() || p.Addr().Is4In6() || (f.Type == "prefix4" && !p.Addr().Is4()) || (f.Type == "prefix6" && !p.Addr().Is6()) {
					err = fmt.Errorf("invalid canonical %s CIDR", f.Type)
				} else {
					r[k] = p.String()
				}
			case "mac":
				m, e := net.ParseMAC(t)
				if e != nil || len(m) != 6 || m[0]&1 != 0 || m.String() == "00:00:00:00:00:00" {
					err = fmt.Errorf("registered MAC must be a nonzero unicast EUI-48 address")
				} else {
					r[k] = m.String()
				}
			}
			if f.Ref != "" && find(s, f.Ref, t) == nil {
				err = fmt.Errorf("references missing %s", f.Ref)
			}
			if len(f.Options) > 0 {
				found := false
				for _, o := range f.Options {
					if t == o {
						found = true
					}
				}
				if !found {
					err = fmt.Errorf("must be one of %s", strings.Join(f.Options, ", "))
				}
			}
		}
		if err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
	}
	for _, old := range s.Resources[kind] {
		if str(old, "id") == str(r, "id") {
			continue
		}
		unique := []string{}
		switch kind {
		case "members":
			unique = []string{"asn"}
		case "users":
			unique = []string{"email"}
		case "vlans":
			unique = nil
		case "routers":
			unique = nil
		}
		for _, k := range unique {
			if r[k] != nil && strings.EqualFold(fmt.Sprint(old[k]), fmt.Sprint(r[k])) {
				return fmt.Errorf("%s already exists", k)
			}
		}
		if kind == "ports" && str(old, "switchId") == str(r, "switchId") && str(old, "name") == str(r, "name") {
			return fmt.Errorf("port name already exists on switch")
		}
		if kind == "peers" && str(old, "routerId") == str(r, "routerId") && str(old, "address") == str(r, "address") {
			return fmt.Errorf("peer address already exists on router")
		}
		if kind == "patch-ports" && str(old, "panelId") == str(r, "panelId") && number(old, "number") == number(r, "number") {
			return fmt.Errorf("patch panel port number already exists")
		}
		if kind == "ip-addresses" && str(old, "vlanId") == str(r, "vlanId") && str(old, "address") == str(r, "address") {
			return fmt.Errorf("address already exists in VLAN pool")
		}
		if kind == "mac-addresses" && canonicalDeviceMAC(str(old, "address")) == canonicalDeviceMAC(str(r, "address")) {
			left := find(s, "connections", str(old, "connectionId"))
			right := find(s, "connections", str(r, "connectionId"))
			if str(left, "vlanId") == str(right, "vlanId") {
				return fmt.Errorf("MAC already registered on this VLAN; resolve the existing registration before assigning it elsewhere")
			}
		}
		if kind == "member-tags" && str(old, "memberId") == str(r, "memberId") && str(old, "tagId") == str(r, "tagId") {
			return fmt.Errorf("tag already assigned to member")
		}
	}
	if kind == "connections" {
		if err := validateNetworkConnection(s, r); err != nil {
			return err
		}
	}

	if kind == "routers" {
		if err := validateRouterRole(r); err != nil {
			return err
		}
		vlan := find(s, "vlans", str(r, "vlanId"))
		if str(r, "localIpv4") == "" && str(r, "localIpv6") == "" {
			return fmt.Errorf("at least one local router address is required")
		}
		for _, pair := range [][2]string{{"localIpv4", "ipv4Prefix"}, {"localIpv6", "ipv6Prefix"}} {
			if str(r, pair[0]) == "" {
				continue
			}
			a, _ := netip.ParseAddr(str(r, pair[0]))
			if !vlanContains(vlan, a) {
				return fmt.Errorf("%s must be inside the router VLAN prefix", pair[0])
			}
			for _, other := range s.Resources["routers"] {
				if str(other, "id") != str(r, "id") && str(other, "vlanId") == str(r, "vlanId") && str(other, pair[0]) == str(r, pair[0]) {
					return fmt.Errorf("router local address is already allocated")
				}
			}
		}
	}
	if kind == "peers" {
		m := find(s, "members", str(r, "memberId"))
		if number(m, "asn") != number(r, "asn") {
			return fmt.Errorf("peer ASN must match member")
		}
		if r["enabled"] == true {
			if str(m, "status") != "active" {
				return fmt.Errorf("enabled peer requires active member")
			}
			p, _ := r["allowedPrefixes"].([]any)
			if routerRole(find(s, "routers", str(r, "routerId"))) == "as112" {
				origins, _ := r["allowedRoutes"].([]any)
				if len(p)+len(origins) > 0 {
					return fmt.Errorf("AS112 peers reject all inbound routes; explicitly clear previous allowlists before enabling")
				}
			}
			if len(p) == 0 && !peerCanObserveWithoutAllowlist(s, r) {
				return fmt.Errorf("enabled peer requires an explicit prefix allowlist")
			}
		}
	}
	if kind == "users" {
		if str(r, "role") == "" {
			return fmt.Errorf("role is required")
		}
	}
	if kind == "core-links" && str(r, "portAId") == str(r, "portBId") {
		return fmt.Errorf("core link endpoints must differ")
	}
	if kind == "switches" && str(r, "rackId") != "" {
		rack := find(s, "racks", str(r, "rackId"))
		if str(rack, "locationId") != str(r, "locationId") {
			return fmt.Errorf("rack and switch must belong to the same location")
		}
	}
	if kind == "patch-ports" {
		panel := find(s, "patch-panels", str(r, "panelId"))
		if number(r, "number") > number(panel, "portCount") {
			return fmt.Errorf("port number exceeds patch panel capacity")
		}
		if str(r, "status") == "connected" && str(r, "memberId") == "" {
			return fmt.Errorf("connected patch port requires a member")
		}
	}
	if kind == "ip-addresses" {
		a, _ := netip.ParseAddr(str(r, "address"))
		field := "ipv6Prefix"
		if a.Is4() {
			field = "ipv4Prefix"
		}
		vlan := find(s, "vlans", str(r, "vlanId"))
		_ = field
		if !vlanContains(vlan, a) {
			return fmt.Errorf("address must be inside VLAN prefix")
		}
	}
	if err := validateBusiness(s, kind, r); err != nil {
		return err
	}
	if err := validateConsoleRecord(s, kind, r); err != nil {
		return err
	}
	if err := validateExtended(s, kind, r); err != nil {
		return err
	}
	if err := validateProvisionInventory(s, kind, r); err != nil {
		return err
	}
	if kind == provisionProfileKind {
		if err := validateProvisionProfile(s, r); err != nil {
			return err
		}
	}
	return validateFabrics(s, kind, r)
}
func validateAll(s *State) error {
	for kind, items := range s.Resources {
		if _, ok := Schemas[kind]; !ok {
			return fmt.Errorf("unknown resource %s", kind)
		}
		ids := map[string]bool{}
		for _, r := range items {
			id := str(r, "id")
			if !identifier.MatchString(id) || ids[id] {
				return fmt.Errorf("invalid or duplicate resource ID in %s", kind)
			}
			ids[id] = true
			if err := validate(s, kind, r); err != nil {
				return fmt.Errorf("%s/%s: %w", kind, str(r, "id"), err)
			}
		}
	}
	if err := validateCoreInventoryFlags(s); err != nil {
		return err
	}
	if err := validateTrafficInventory(s); err != nil {
		return err
	}
	if err := validateSFlowInventory(s); err != nil {
		return err
	}
	if err := validateSFlowReceivers(s); err != nil {
		return err
	}
	if err := validateLegacyApprovals(s); err != nil {
		return err
	}
	return validateCore(s)
}
