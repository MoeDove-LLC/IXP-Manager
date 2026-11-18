package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLegacyCustomerSourceEvidenceUsesExplicitSelectorColumns(t *testing.T) {
	for _, spec := range []struct {
		name                     string
		rsClient, enabled4       any
		wantClient4, wantClient6 bool
	}{
		{"explicit dual-stack", "1", true, true, true},
		{"one enabled family", true, false, false, true},
		{"non-RS interface", false, true, false, false},
		{"no RS evidence", nil, true, false, false},
		{"malformed RS evidence", "yes", true, false, false},
	} {
		t.Run(spec.name, func(t *testing.T) {
			source := decodeSample(t, sample)
			vli := source.Tables["vlaninterface"][0]
			vli["rsclient"], vli["ipv4enabled"] = spec.rsClient, spec.enabled4
			source.Tables["routers"] = []row{
				{"id": json.Number("1"), "vlan_id": "1", "protocol": json.Number("4"), "software": "unknown"},
				{"id": json.Number("2"), "vlan_id": "100", "protocol": json.Number("6")}, // same as the VLAN tag
				{"id": json.Number("3"), "vlan_id": "1", "protocol": true},
			}
			export, err := convert(source)
			if err != nil {
				t.Fatal(err)
			}
			vlans, connections := export.Resources["vlans"], export.Resources["connections"]
			if len(vlans) != 1 || len(connections) != 1 {
				t.Fatal("expected retained VLAN and connection")
			}
			if vlans[0]["legacyRouterIPv4"] != true || vlans[0]["legacyRouterIPv6"] != nil || len(export.Resources["routers"]) != 0 {
				t.Fatalf("router family evidence or executable isolation: %v", vlans[0])
			}
			connection := connections[0]
			if (connection["legacyRSClientIPv4"] == true) != spec.wantClient4 || (connection["legacyRSClientIPv6"] == true) != spec.wantClient6 {
				t.Fatalf("wrong explicit client evidence: %v", connection)
			}
			if spec.rsClient == "yes" && !strings.Contains(strings.Join(export.Warnings, "\n"), "rsclient is not an explicit boolean") {
				t.Fatal("invalid selector evidence was silently dropped")
			}
		})
	}
}
