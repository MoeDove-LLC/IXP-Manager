package api

import (
	"context"
	"errors"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type dictionary struct {
	path       string
	properties []string
	required   []string
	writable   map[string]string
}

func attributes(pairs ...string) map[string]string {
	out := map[string]string{}
	for i := 0; i < len(pairs); i += 2 {
		out[pairs[i]] = pairs[i+1]
	}
	return out
}

var dictionaries = map[Resource]dictionary{
	SystemResource: {path: "/system/resource", properties: []string{"version", "architecture-name", "board-name", "platform", "uptime", "cpu-count", "total-memory", "free-memory"}, required: []string{"version"}},
	SystemLicense:  {path: "/system/license", properties: []string{"system-id", "software-id", "level"}},
	RouterBoard:    {path: "/system/routerboard", properties: []string{"routerboard", "board-name", "model", "serial-number", "firmware-type", "factory-firmware", "minimum-firmware", "current-firmware", "upgrade-firmware"}},
	Identity:       {path: "/system/identity", properties: []string{"name"}, required: []string{"name"}},
	RoutingTable:   {path: "/routing/table", properties: []string{".id", "name", "fib", "disabled", "dynamic", "invalid", "used", "comment"}, required: []string{".id", "name"}, writable: attributes("name", "name", "fib", "switch-on", "disabled", "bool", "comment", "text")},
	BGPInstance:    {path: "/routing/bgp/instance", properties: []string{".id", "name", "as", "router-id", "routing-table", "vrf", "disabled", "inactive", "comment"}, required: []string{".id", "name"}, writable: attributes("name", "name", "as", "asn", "router-id", "ipv4", "routing-table", "name", "vrf", "name", "disabled", "bool", "comment", "text")},
	BGPConnection:  {path: "/routing/bgp/connection", properties: []string{".id", "name", "instance", "disabled", "dynamic", "inactive", "templates", "afi", "local.address", "local.role", "remote.address", "remote.as", "as", "routing-table", "vrf", "connect", "listen", "nexthop-choice", "multihop", "input.filter", "input.filter-nlri", "input.limit-process-routes-ipv4", "input.limit-process-routes-ipv6", "output.filter-chain", "output.filter-select", "output.network", "output.redistribute", "output.default-originate", "output.default-prepend", "output.remove-private-as", "output.as-override", "comment"}, required: []string{".id", "name"}, writable: attributes("name", "name", "as", "asn", "instance", "name", "templates", "empty", "disabled", "bool", "afi", "afi", "local.address", "ip", "local.role", "role", "remote.address", "host", "remote.as", "asn", "routing-table", "name", "vrf", "name", "connect", "bool", "listen", "bool", "nexthop-choice", "nexthop", "multihop", "bool", "input.filter", "name", "input.limit-process-routes-ipv4", "limit", "input.limit-process-routes-ipv6", "limit", "output.filter-chain", "name", "output.default-originate", "default", "output.default-prepend", "uint8", "output.remove-private-as", "bool", "output.as-override", "bool", "tcp-md5-key", "secret", "comment", "text")},
	FilterRule:     {path: "/routing/filter/rule", properties: []string{".id", "chain", "rule", "disabled", "inactive", "invalid", "comment"}, required: []string{".id", "chain", "rule"}, writable: attributes("chain", "name", "rule", "rule", "disabled", "bool", "comment", "text")},
	SafeMode:       {path: "/safe-mode", properties: []string{"enabled", "current", "user", "owner"}},
}
var objectID = regexp.MustCompile(`^\*[A-Fa-f0-9]{1,16}$`)
var objectName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.:-]{0,127}$`)

func has(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}
func (c *Client) Read(ctx context.Context, resource Resource) ([]Record, error) {
	return c.read(ctx, resource, false)
}

// ReadDetailed uses the same fixed property allowlist as Read. It asks the
// native print command to include detail; omissions still remain unknown.
func (c *Client) ReadDetailed(ctx context.Context, resource Resource) ([]Record, error) {
	return c.read(ctx, resource, true)
}

func (c *Client) read(ctx context.Context, resource Resource, detail bool) ([]Record, error) {
	definition, ok := dictionaries[resource]
	if !ok {
		return nil, &Error{Code: CodeConfig}
	}
	words := []string{definition.path + "/print", "=.proplist=" + strings.Join(definition.properties, ",")}
	if detail {
		words = append(words, "=detail=")
	}
	response, e := c.command(ctx, words, false, false)
	if e != nil {
		var failure *Error
		if errors.As(e, &failure) && failure.Code == CodeTrap && failure.Category == 0 {
			return nil, &Error{Code: CodeUnsupported, Resource: resource, Category: 0}
		}
		if errors.As(e, &failure) {
			copy := *failure
			copy.Resource = resource
			return nil, &copy
		}
		return nil, e
	}
	if len(response.done) > 0 {
		return nil, &Error{Code: CodeProtocol, Resource: resource}
	}
	rows := make([]Record, 0, len(response.rows))
	for _, raw := range response.rows {
		row := Record{}
		for _, property := range definition.properties {
			if value, exists := raw[property]; exists {
				if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
					return nil, &Error{Code: CodeProtocol, Resource: resource}
				}
				row[property] = value
			}
		}
		for _, property := range definition.required {
			if row[property] == "" {
				return nil, &Error{Code: CodeProtocol, Resource: resource}
			}
		}
		if id, exists := row[".id"]; exists && !objectID.MatchString(id) {
			return nil, &Error{Code: CodeProtocol, Resource: resource}
		}
		rows = append(rows, row)
	}
	return rows, nil
}
func validValue(kind, value string) bool {
	if !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	switch kind {
	case "name":
		return objectName.MatchString(value)
	case "empty":
		return value == ""
	case "text":
		return len(value) <= 1024
	case "rule":
		return len(value) > 0 && len(value) <= 16384
	case "secret":
		if len(value) > 80 {
			return false
		}
		for _, c := range value {
			if unicode.IsControl(c) {
				return false
			}
		}
		return true
	case "bool":
		return has([]string{"yes", "no", "true", "false"}, value)
	case "switch-on":
		return value == "yes" || value == "true"
	case "asn":
		n, e := strconv.ParseUint(value, 10, 32)
		return e == nil && n > 0 && n < 4294967295 && strconv.FormatUint(n, 10) == value
	case "uint8":
		n, e := strconv.ParseUint(value, 10, 8)
		return e == nil && strconv.FormatUint(n, 10) == value
	case "limit":
		n, e := strconv.ParseUint(value, 10, 32)
		return e == nil && n > 0 && n <= 1000000 && strconv.FormatUint(n, 10) == value
	case "ip", "ipv4", "host":
		address, e := netip.ParseAddr(value)
		if e != nil && kind == "host" {
			prefix, e := netip.ParsePrefix(value)
			if e != nil || prefix.Bits() != prefix.Addr().BitLen() {
				return false
			}
			address = prefix.Addr()
		} else if e != nil {
			return false
		}
		return address.Zone() == "" && !address.IsUnspecified() && !address.IsMulticast() && !address.Is4In6() && (kind != "ipv4" || address.Is4())
	case "afi":
		return has([]string{"ip", "ipv6", "ip,ipv6"}, value)
	case "role":
		return has([]string{"ibgp", "ibgp-rr", "ebgp", "ebgp-provider", "ebgp-rs", "ebgp-rs-client", "ebgp-customer", "ebgp-peer"}, value)
	case "nexthop":
		return has([]string{"default", "force-self", "propagate"}, value)
	case "default":
		return has([]string{"never", "always", "if-installed"}, value)
	}
	return false
}
func mutationWords(m Mutation) ([]string, error) {
	definition, ok := dictionaries[m.Resource]
	if !ok || len(definition.writable) == 0 {
		return nil, &Error{Code: CodeConfig}
	}
	if !has([]string{string(Add), string(Set), string(Enable), string(Disable), string(Remove), string(Unset)}, string(m.Action)) {
		return nil, &Error{Code: CodeConfig}
	}
	if m.Action == Add {
		if m.ID != "" {
			return nil, &Error{Code: CodeConfig}
		}
	} else if !objectID.MatchString(m.ID) {
		return nil, &Error{Code: CodeConfig}
	}
	if m.Action == Unset {
		allowed := m.Resource == RoutingTable && m.Property == "fib" || m.Resource == BGPConnection && (m.Property == "input.limit-process-routes-ipv4" || m.Property == "input.limit-process-routes-ipv6")
		if !allowed || len(m.Attributes) != 0 {
			return nil, &Error{Code: CodeConfig}
		}
		return []string{definition.path + "/unset", "=.id=" + m.ID, "=value-name=" + m.Property}, nil
	}
	if m.Property != "" {
		return nil, &Error{Code: CodeConfig}
	}
	if len(m.Attributes) > 64 || m.Action != Add && m.Action != Set && len(m.Attributes) != 0 {
		return nil, &Error{Code: CodeConfig}
	}
	if (m.Action == Add || m.Action == Set) && len(m.Attributes) == 0 {
		return nil, &Error{Code: CodeConfig}
	}
	seen := map[string]bool{}
	attrs := append([]Attribute(nil), m.Attributes...)
	for _, attribute := range attrs {
		kind, ok := definition.writable[attribute.Name]
		if !ok || seen[attribute.Name] || !validValue(kind, attribute.Value) {
			return nil, &Error{Code: CodeConfig}
		}
		seen[attribute.Name] = true
	}
	if m.Action == Add {
		required := []string{"name"}
		switch m.Resource {
		case BGPInstance:
			required = []string{"name", "as", "router-id", "routing-table"}
		case BGPConnection:
			required = []string{"name", "instance", "disabled", "afi", "local.address", "local.role", "remote.address", "remote.as", "input.filter", "output.filter-chain"}
		case FilterRule:
			required = []string{"chain", "rule"}
		}
		for _, property := range required {
			if !seen[property] {
				return nil, &Error{Code: CodeConfig}
			}
		}
	}
	sort.Slice(attrs, func(i, j int) bool { return attrs[i].Name < attrs[j].Name })
	words := []string{definition.path + "/" + string(m.Action)}
	if m.ID != "" {
		words = append(words, "=.id="+m.ID)
	}
	for _, attribute := range attrs {
		value := attribute.Value
		if definition.writable[attribute.Name] == "switch-on" {
			value = ""
		}
		words = append(words, "="+attribute.Name+"="+value)
	}
	return words, nil
}
func ValidateMutation(m Mutation) error { _, e := mutationWords(m); return e }

func (c *Client) Apply(ctx context.Context, mutation Mutation) (Result, error) {
	words, e := mutationWords(mutation)
	if e != nil {
		return Result{}, e
	}
	response, e := c.command(ctx, words, true, false)
	if e != nil {
		return Result{}, e
	}
	result := Result{Acknowledged: true}
	if mutation.Action == Add {
		ret, exists := response.done["ret"]
		if !exists || !objectID.MatchString(ret) {
			c.Close()
			return Result{}, &Error{Code: CodeProtocol, Uncertain: true, Resource: mutation.Resource, Action: mutation.Action}
		}
		result.ID = ret
	}
	return result, nil
}

// ReadProperty requests one known non-sensitive property explicitly. Missing
// print flags remain unknown; callers may use this getter instead of inventing
// defaults. The transport still preserves a missing ret as Known=false.
func (c *Client) ReadProperty(ctx context.Context, resource Resource, id, property string) (PropertyValue, error) {
	definition, ok := dictionaries[resource]
	if !ok || !has(definition.properties, property) || sensitiveName(property) || property == ".id" {
		return PropertyValue{}, &Error{Code: CodeConfig}
	}
	words := []string{definition.path + "/get"}
	if resource == RoutingTable || resource == BGPInstance || resource == BGPConnection || resource == FilterRule {
		if !objectID.MatchString(id) {
			return PropertyValue{}, &Error{Code: CodeConfig}
		}
		words = append(words, "=.id="+id)
	} else if id != "" {
		return PropertyValue{}, &Error{Code: CodeConfig}
	}
	words = append(words, "=value-name="+property)
	response, e := c.command(ctx, words, false, false)
	if e != nil {
		return PropertyValue{}, e
	}
	if len(response.rows) > 0 {
		return PropertyValue{}, &Error{Code: CodeProtocol}
	}
	value, known := response.done["ret"]
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return PropertyValue{}, &Error{Code: CodeProtocol}
	}
	return PropertyValue{Known: known, Value: value}, nil
}

func (c *Client) ReadCredential(ctx context.Context, connectionID string) (Credential, error) {
	if !objectID.MatchString(connectionID) {
		return Credential{}, &Error{Code: CodeConfig}
	}
	response, e := c.command(ctx, []string{"/routing/bgp/connection/get", "=.id=" + connectionID, "=value-name=tcp-md5-key"}, false, false)
	if e != nil {
		return Credential{}, e
	}
	if len(response.rows) != 0 {
		return Credential{}, &Error{Code: CodeProtocol}
	}
	value, present := response.done["ret"]
	if !present {
		return Credential{Known: false}, nil
	}
	if len(value) > 80 {
		return Credential{}, &Error{Code: CodeLimit}
	}
	if !validValue("secret", value) {
		return Credential{Known: false}, nil
	}
	if value != "" && (strings.Trim(value, "*") == "" || value == "<hidden>" || value == "[redacted]") {
		return Credential{Known: false}, nil
	}
	return Credential{Known: true, Value: SecretValue{value: []byte(value)}}, nil
}
func boolValue(value string) (bool, bool) {
	switch value {
	case "true", "yes":
		return true, true
	case "false", "no":
		return false, true
	}
	return false, false
}
func (c *Client) ReadSafeMode(ctx context.Context) (SafeModeState, error) {
	rows, e := c.Read(ctx, SafeMode)
	if e != nil {
		return SafeModeState{}, e
	}
	if len(rows) != 1 {
		return SafeModeState{}, &Error{Code: CodeProtocol, Resource: SafeMode}
	}
	enabled, known := boolValue(rows[0]["enabled"])
	current, currentKnown := boolValue(rows[0]["current"])
	if !known || !currentKnown || current && !enabled {
		return SafeModeState{}, &Error{Code: CodeProtocol, Resource: SafeMode}
	}
	return SafeModeState{Enabled: enabled, Current: current, User: rows[0]["user"], Owner: rows[0]["owner"]}, nil
}
func (c *Client) Inventory(ctx context.Context) (Inventory, error) {
	inventory := Inventory{Resources: []Record{}, Licenses: []Record{}, RouterBoards: []Record{}, Identities: []Record{}, Tables: []Record{}, Instances: []Record{}, Connections: []Record{}, FilterRules: []Record{}, Capabilities: map[Resource]Capability{}}
	for _, entry := range []struct {
		resource Resource
		dest     *[]Record
		optional bool
	}{{SystemResource, &inventory.Resources, false}, {SystemLicense, &inventory.Licenses, true}, {Identity, &inventory.Identities, false}, {RouterBoard, &inventory.RouterBoards, true}, {RoutingTable, &inventory.Tables, false}, {BGPInstance, &inventory.Instances, false}, {BGPConnection, &inventory.Connections, false}, {FilterRule, &inventory.FilterRules, false}} {
		rows, e := c.Read(ctx, entry.resource)
		if e != nil {
			var failure *Error
			if entry.optional && errors.As(e, &failure) && failure.Code == CodeUnsupported {
				inventory.Capabilities[entry.resource] = Unsupported
				continue
			}
			return Inventory{}, e
		}
		if (entry.resource == SystemResource || entry.resource == Identity) && len(rows) != 1 {
			return Inventory{}, &Error{Code: CodeProtocol, Resource: entry.resource}
		}
		*entry.dest = rows
		inventory.Capabilities[entry.resource] = Supported
	}
	safe, e := c.ReadSafeMode(ctx)
	if e == nil {
		inventory.SafeMode = &safe
		inventory.Capabilities[SafeMode] = Supported
	} else {
		var failure *Error
		if !errors.As(e, &failure) || c.closed.Load() {
			return Inventory{}, e
		}
		if failure.Code == CodeUnsupported {
			inventory.Capabilities[SafeMode] = Unsupported
		} else if failure.Code == CodeTrap || failure.Code == CodeProtocol {
			inventory.Capabilities[SafeMode] = Unknown
		} else {
			return Inventory{}, e
		}
	}
	return inventory, nil
}
