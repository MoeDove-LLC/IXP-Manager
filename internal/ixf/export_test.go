package ixf

import (
	"encoding/json"
	"fmt"
	"math"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func sample() Input {
	return Input{Timestamp: time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("test", 3600)), Exchanges: []Exchange{{ID: 1, IXFID: 42, Name: "Example Internet Exchange", ShortName: "EX-IX", URL: "https://ix.example.net", VLANs: []VLAN{{ID: 100, Name: "Peering", IPv4Prefix: "192.0.2.0/24", IPv6Prefix: "2001:db8::/64"}}, Switches: []Switch{{ID: 11, Name: "sw1"}, {ID: 12, Name: "sw2"}}}}, Members: []Member{{ID: "m1", ASN: 64496, Name: "Example Network", Status: "active", Connections: []Connection{{ExchangeID: 1, State: "active", Interfaces: []Interface{{SwitchID: 11, SpeedMbps: 10000}, {SwitchID: 12, SpeedMbps: 10000}}, VLANs: []VLANConnection{{VLANID: 100, IPv4: &Address{Address: "192.0.2.10", RouteServer: true, MaxPrefix: 100, MACAddresses: []string{"AA-BB-CC-DD-EE-FF"}}, IPv6: &Address{Address: "2001:db8::10", RouteServer: false, MaxPrefix: 200}}}}}}}}
}

func TestDualStackLAGAndSchema(t *testing.T) {
	input := sample()
	doc, err := Build(input)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Timestamp != "2026-01-02T02:04:05Z" {
		t.Fatalf("timestamp is not UTC: %s", doc.Timestamp)
	}
	conn := doc.MemberList[0].Connections[0]
	if len(conn.Interfaces) != 2 || conn.Interfaces[0].SpeedMbps != 10000 || conn.Interfaces[1].SpeedMbps != 10000 {
		t.Fatal("physical LAG members or Mbps units lost")
	}
	if conn.VLANs[0].IPv4.MACAddresses[0] != "aa:bb:cc:dd:ee:ff" {
		t.Fatal("MAC not normalized")
	}
	if !conn.VLANs[0].IPv4.RouteServer || conn.VLANs[0].IPv6.RouteServer {
		t.Fatal("address family route-server flags conflated")
	}
	if input.Members[0].Connections[0].VLANs[0].IPv4.MACAddresses[0] != "AA-BB-CC-DD-EE-FF" {
		t.Fatal("input mutated")
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	validateOfficialSchema(t, data)
	if strings.Contains(string(data), "contact_email") || strings.Contains(string(data), "management") {
		t.Fatal("private fields exported")
	}
}

func TestInactivePrivateAndEmptyConnectionsOmitted(t *testing.T) {
	for _, state := range []string{"pending", "suspended", "disabled", "quarantine", ""} {
		t.Run("member_"+state, func(t *testing.T) {
			in := sample()
			in.Members[0].Status = state
			doc, err := Build(in)
			if err != nil || len(doc.MemberList) != 0 {
				t.Fatalf("inactive member exported: %v", err)
			}
		})
		t.Run("connection_"+state, func(t *testing.T) {
			in := sample()
			in.Members[0].Connections[0].State = state
			doc, err := Build(in)
			if err != nil || len(doc.MemberList) != 0 {
				t.Fatalf("inactive connection exported: %v", err)
			}
		})
	}
	in := sample()
	in.Exchanges[0].VLANs[0].Exclude = true
	doc, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.MemberList) != 0 || len(doc.IXPList[0].VLANs) != 0 {
		t.Fatal("excluded VLAN leaked")
	}
	b, _ := json.Marshal(doc)
	validateOfficialSchema(t, b)
	if !strings.Contains(string(b), `"member_list":[]`) {
		t.Fatal("empty member list must be JSON array")
	}
	in = sample()
	in.Members[0].Connections[0].VLANs[0].IPv4 = nil
	in.Members[0].Connections[0].VLANs[0].IPv6 = nil
	doc, err = Build(in)
	if err != nil || len(doc.MemberList) != 0 {
		t.Fatal("addressless connection exported")
	}
}

func TestRejectInvalidIncludedData(t *testing.T) {
	cases := map[string]func(*Input){
		"missing IX-F ID":    func(in *Input) { in.Exchanges[0].IXFID = 0 },
		"duplicate exchange": func(in *Input) { in.Exchanges = append(in.Exchanges, in.Exchanges[0]) },
		"duplicate ASN":      func(in *Input) { in.Members = append(in.Members, in.Members[0]) },
		"bad family":         func(in *Input) { in.Members[0].Connections[0].VLANs[0].IPv4.Address = "2001:db8::10" },
		"off LAN":            func(in *Input) { in.Members[0].Connections[0].VLANs[0].IPv4.Address = "198.51.100.10" },
		"missing switch":     func(in *Input) { in.Members[0].Connections[0].Interfaces[0].SwitchID = 99 },
		"missing exchange":   func(in *Input) { in.Members[0].Connections[0].ExchangeID = 99 },
		"missing VLAN":       func(in *Input) { in.Members[0].Connections[0].VLANs[0].VLANID = 99 },
		"missing speed":      func(in *Input) { in.Members[0].Connections[0].Interfaces[0].SpeedMbps = 0 },
		"missing interface":  func(in *Input) { in.Members[0].Connections[0].Interfaces = nil },
		"bad MAC":            func(in *Input) { in.Members[0].Connections[0].VLANs[0].IPv4.MACAddresses = []string{"secret"} },
		"unsafe website":     func(in *Input) { in.Members[0].Website = "javascript:alert(1)" },
		"duplicate address": func(in *Input) {
			in.Members[0].Connections = append(in.Members[0].Connections, in.Members[0].Connections[0])
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			in := sample()
			change(&in)
			if _, err := Build(in); err == nil {
				t.Fatal("invalid included data accepted")
			}
		})
	}
}

// This bounded test-only walker covers every keyword used by the pinned
// official schema. It deliberately fails on new validation keywords rather
// than silently treating an unrecognized schema constraint as success.
func validateOfficialSchema(t *testing.T, data []byte) {
	validateVersionSchema(t, data, "1.0")
}
func validateVersionSchema(t *testing.T, data []byte, version string) {
	t.Helper()
	b, err := os.ReadFile("testdata/schema-" + version + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	var value any
	if err = json.Unmarshal(b, &schema); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	if err = walk(schema, value, "$"); err != nil {
		t.Fatal(err)
	}
}
func walk(schema map[string]any, value any, path string) error {
	for k := range schema {
		switch k {
		case "$schema", "title", "description", "type", "required", "properties", "items", "format", "enum":
		default:
			return fmt.Errorf("unsupported schema keyword %s", k)
		}
	}
	switch schema["type"] {
	case "object":
		o, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("%s must be object", path)
		}
		if required, ok := schema["required"].([]any); ok {
			for _, k := range required {
				if _, exists := o[k.(string)]; !exists {
					return fmt.Errorf("%s.%s required", path, k)
				}
			}
		}
		if props, ok := schema["properties"].(map[string]any); ok {
			for k, s := range props {
				if v, exists := o[k]; exists {
					if err := walk(s.(map[string]any), v, path+"."+k); err != nil {
						return err
					}
				}
			}
		}
	case "array":
		a, ok := value.([]any)
		if !ok {
			return fmt.Errorf("%s must be array", path)
		}
		for i, v := range a {
			if err := walk(schema["items"].(map[string]any), v, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	case "integer":
		n, ok := value.(float64)
		if !ok || math.Trunc(n) != n {
			return fmt.Errorf("%s must be integer", path)
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("%s must be boolean", path)
		}
	case "string":
		s, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be string", path)
		}
		var err error
		switch schema["format"] {
		case "date-time":
			_, err = time.Parse(time.RFC3339, s)
		case "uri":
			var u *url.URL
			u, err = url.Parse(s)
			if err == nil && u.Scheme == "" {
				err = fmt.Errorf("absolute URI required")
			}
		case "ipv4", "ipv6":
			var ip netip.Addr
			ip, err = netip.ParseAddr(s)
			if err == nil && ip.Is4() != (schema["format"] == "ipv4") {
				err = fmt.Errorf("address family mismatch")
			}
		case nil:
		default:
			return fmt.Errorf("unimplemented format %s", schema["format"])
		}
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	default:
		return fmt.Errorf("unsupported schema type %s", schema["type"])
	}
	if values, ok := schema["enum"].([]any); ok {
		for _, v := range values {
			if v == value {
				return nil
			}
		}
		return fmt.Errorf("%s invalid enum", path)
	}
	return nil
}
