package ixf

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestVersionedPublicContract(t *testing.T) {
	for _, version := range []string{"0.6", "0.7", "1.0"} {
		t.Run(version, func(t *testing.T) {
			in := sample()
			vlan := &in.Members[0].Connections[0].VLANs[0]
			vlan.IPv4.Services = []Service{{Type: "ixrouteserver"}, {Type: "ixroutecollector"}, {Type: "ixrouteserver"}}
			before, _ := json.Marshal(in)
			doc, err := BuildVersion(in, version)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(doc)
			validateVersionSchema(t, data, version)
			if doc.Version != version || doc.Timestamp != "2026-01-02T02:04:05Z" {
				t.Fatal("wrong version or timestamp")
			}
			conn := doc.MemberList[0].Connections[0]
			if len(conn.Interfaces) != 2 || conn.Interfaces[0].SpeedMbps != 10000 || conn.Interfaces[1].SpeedMbps != 10000 {
				t.Fatal("LAG or speed units changed")
			}
			addr := conn.VLANs[0].IPv4
			if version == "0.6" && (len(addr.Services) != 0 || len(addr.ServiceTypes) != 0) {
				t.Fatal("service fields not defined by 0.6")
			}
			if version == "0.7" && (len(addr.Services) != 0 || len(addr.ServiceTypes) != 2 || addr.ServiceTypes[0] != "ixroutecollector" || addr.ServiceTypes[1] != "ixrouteserver") {
				t.Fatal("0.7 service_type is not sorted and deduplicated")
			}
			if version == "1.0" && (len(addr.ServiceTypes) != 0 || len(addr.Services) != 2 || addr.Services[0].Type != "ixroutecollector" || addr.Services[1].Type != "ixrouteserver") {
				t.Fatal("1.0 services are not sorted and deduplicated")
			}
			if !addr.RouteServer || conn.VLANs[0].IPv6.RouteServer || len(conn.VLANs[0].IPv6.Services) != 0 || len(conn.VLANs[0].IPv6.ServiceTypes) != 0 {
				t.Fatal("client status or service metadata crossed address families")
			}
			after, _ := json.Marshal(in)
			if !bytes.Equal(before, after) {
				t.Fatal("version conversion mutated caller input")
			}
			in.Exchanges[0].VLANs[0].Exclude = true
			empty, err := BuildVersion(in, version)
			if err != nil || len(empty.MemberList) != 0 {
				t.Fatal("excluded VLAN leaked", err)
			}
			data, _ = json.Marshal(empty)
			validateVersionSchema(t, data, version)
			if !bytes.Contains(data, []byte(`"member_list":[]`)) {
				t.Fatal("empty list is not an array")
			}
		})
	}
}

func TestVersionRequiredRegistryIDAndInvalidInputs(t *testing.T) {
	in := sample()
	in.Exchanges[0].IXFID = 0
	doc, err := BuildVersion(in, "0.6")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(doc)
	validateVersionSchema(t, data, "0.6")
	if bytes.Contains(data, []byte(`"ixf_id"`)) {
		t.Fatal("missing optional registry ID was invented")
	}
	for _, version := range []string{"0.7", "1.0", "0.5", "", "latest", "1.1", "0.7/extra"} {
		if _, err := BuildVersion(in, version); err == nil {
			t.Fatalf("accepted missing required registry ID or unknown version %q", version)
		}
	}
	for _, version := range []string{"0.6", "0.7", "1.0"} {
		for _, change := range []func(*Input){
			func(in *Input) { in.Exchanges[0].IXFID = -1 },
			func(in *Input) { in.Members[0].Connections[0].VLANs[0].IPv4.Address = "198.51.100.2" },
			func(in *Input) { in.Members[0].Connections[0].VLANs[0].IPv4.Services = []Service{{Type: "unknown"}} },
			func(in *Input) { in.Members[0].Connections[0].VLANs[0].IPv4.ServiceTypes = []string{"ixrouteserver"} },
		} {
			in := sample()
			change(&in)
			if _, err := BuildVersion(in, version); err == nil {
				t.Fatalf("invalid included data accepted for %s", version)
			}
		}
	}
}
