package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/app"
	"github.com/moedove/ixp-manager/internal/core"
)

func coreFixture(t *testing.T) legacyExport {
	t.Helper()
	raw, err := os.ReadFile("testdata/core-topology.json")
	if err != nil {
		t.Fatal(err)
	}
	return decodeSample(t, string(raw))
}
func coreRow(t *testing.T, s *legacyExport, table string, id int64) row {
	t.Helper()
	for _, r := range s.Tables[table] {
		if idOf(r) == id {
			return r
		}
	}
	t.Fatalf("missing fixture %s %d", table, id)
	return nil
}
func copyCoreRow(r row) row {
	out := row{}
	for k, v := range r {
		out[k] = v
	}
	return out
}
func TestCoreMigrationPreservesAllModesAndBothEndpointAttributes(t *testing.T) {
	source := coreFixture(t)
	before, _ := json.Marshal(source)
	result, err := convert(source)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(source)
	if !bytes.Equal(before, after) {
		t.Fatal("source mutated")
	}
	if len(result.Warnings) != 0 {
		t.Fatal("supported core fixture warned", result.Warnings)
	}
	if len(result.CoreBundles) != 3 || len(result.Resources["connections"]) != 1 {
		t.Fatal("core topology flattened into member connections")
	}
	for i, want := range []string{core.ECMP, core.L2LAG, core.L3LAG} {
		b := result.CoreBundles[i]
		if b.Type != want || len(b.Links) != 2 || b.SideA.SwitchID != "legacy-switches-1" || b.SideB.SwitchID != "legacy-switches-2" || b.SideA.OwnerMemberID != "legacy-members-2" || b.SideB.OwnerMemberID != "legacy-members-2" {
			t.Fatal("bundle sides or type lost", b)
		}
		if b.BindingRevision != "" || b.CreatedAt != "" || b.UpdatedAt != "" {
			t.Fatal("old observation lineage imported")
		}
		if err := core.Validate(b); err != nil {
			t.Fatal(err)
		}
	}
	ecmp, l2, l3 := result.CoreBundles[0], result.CoreBundles[1], result.CoreBundles[2]
	if ecmp.Cost == nil || *ecmp.Cost != 0 || ecmp.Preference == nil || *ecmp.Preference != 0 || ecmp.SideA.MTU != 64000 || ecmp.SideA.ChannelGroup != nil {
		t.Fatal("nil/zero or legacy MTU semantics lost")
	}
	if l2.Cost != nil || l2.SideA.ChannelGroup == nil || *l2.SideA.ChannelGroup != 0 || !l2.STP || !l2.SideA.LAGFraming || l3.SideA.LAGFraming || !l3.BFD || l3.Enabled || l3.IPv4Subnet != "10.0.2.0/31" || l3.IPv6Subnet != "fd00:30::/127" {
		t.Fatal("LAG intent was guessed or flattened")
	}
	if !ecmp.Links[0].Enabled || ecmp.Links[1].Enabled || !ecmp.Links[0].BFD || ecmp.Links[0].IPv4Subnet != "10.0.0.0/31" || ecmp.Links[1].IPv6Subnet != "fd00:10::2/127" {
		t.Fatal("per-link ECMP fields lost")
	}
	a, z := ecmp.Links[0].SideA, ecmp.Links[0].SideB
	if a.ID != "legacy-core-interfaces-1000" || a.PortID != "legacy-ports-10" || a.SpeedMbps == nil || *a.SpeedMbps != 40000 || a.Autoneg || !z.Autoneg || a.Status != "connected" || z.Status != "disabled" || a.RateLimitMbps == nil || *a.RateLimitMbps != 0 || z.RateLimitMbps == nil || *z.RateLimitMbps != 5000 || a.Notes != "  retained indentation\nsecond line\n" {
		t.Fatal("physical endpoint attributes changed", a, z)
	}
	if l2.Links[0].SideA.Duplex != "half" || l2.SideA.Description != "Core side 0\nretained description" || l2.SideA.MTU == l2.SideB.MTU {
		t.Fatal("independent side metadata lost")
	}
	for _, p := range result.Resources["ports"] {
		if p["id"] == "legacy-ports-1" {
			if p["memberId"] != "legacy-members-1" {
				t.Fatal("normal member access changed")
			}
			continue
		}
		if p["coreReserved"] != true || p["memberId"] != nil {
			t.Fatal("core port became member-owned or available", p)
		}
		if p["id"] == "legacy-ports-10" && p["speed"] != int64(10000) {
			t.Fatal("desired speed overwrote observed physical capacity")
		}
	}
	if result.Resources["switches"][1]["active"] != false || result.Resources["switches"][0]["asn"] != int64(64510) || result.Resources["switches"][0]["loopbackIpv4"] != "10.255.0.1" || result.Resources["switches"][1]["loopbackName"] != "lo0" {
		t.Fatal("switch active/ASN/loopback values lost")
	}
	var out, log bytes.Buffer
	if err := run([]string{"-strict"}, bytes.NewReader(before), &out, &log); err != nil || out.Len() == 0 || log.Len() != 0 {
		t.Fatal("complete typed migration did not pass strict", err, log.String())
	}
	if bytes.Contains(out.Bytes(), []byte(`"backup"`)) {
		t.Fatal("backup role fabricated from cost/preference")
	}
}
func TestUnsafeCoreOwnershipNeverProducesImportOutput(t *testing.T) {
	cases := map[string]func(*legacyExport){
		"missing-side": func(s *legacyExport) {
			coreRow(t, s, "corelinks", 100)["core_interface_sidea_id"] = json.Number("9999")
		},
		"same-endpoint-both-sides": func(s *legacyExport) {
			l := coreRow(t, s, "corelinks", 100)
			l["core_interface_sideb_id"] = l["core_interface_sidea_id"]
		},
		"cross-side-reuse": func(s *legacyExport) {
			coreRow(t, s, "corelinks", 101)["core_interface_sidea_id"] = json.Number("1001")
		},
		"duplicate-physical-binding": func(s *legacyExport) {
			coreRow(t, s, "coreinterfaces", 1001)["physical_interface_id"] = json.Number("100")
		},
		"duplicate-physical-port": func(s *legacyExport) {
			p := copyCoreRow(coreRow(t, s, "physicalinterface", 100))
			p["id"] = json.Number("999")
			s.Tables["physicalinterface"] = append(s.Tables["physicalinterface"], p)
		},
		"occupied-reserved-port-no-core-interface": func(s *legacyExport) {
			s.Tables["physicalinterface"] = append(s.Tables["physicalinterface"], row{"id": json.Number("999"), "switchportid": json.Number("99"), "virtualinterfaceid": json.Number("1")})
		},
		"orphan-core-interface": func(s *legacyExport) { s.Tables["corelinks"] = s.Tables["corelinks"][1:] },
		"missing-core-bundle":   func(s *legacyExport) { s.Tables["corebundles"] = s.Tables["corebundles"][1:] },
		"core-vi-member-vlan": func(s *legacyExport) {
			s.Tables["vlaninterface"] = append(s.Tables["vlaninterface"], row{"id": json.Number("99"), "virtualinterfaceid": json.Number("10"), "vlanid": json.Number("1"), "ipv4enabled": false, "ipv6enabled": false})
		},
		"mixed-core-and-member-pi": func(s *legacyExport) { coreRow(t, s, "physicalinterface", 1)["virtualinterfaceid"] = json.Number("10") },
		"explicit-member-purpose":  func(s *legacyExport) { coreRow(t, s, "switchport", 10)["type"] = json.Number("1") },
		"boolean-reference":        func(s *legacyExport) { coreRow(t, s, "coreinterfaces", 1000)["physical_interface_id"] = true },
		"outbound-fanout": func(s *legacyExport) {
			coreRow(t, s, "physicalinterface", 100)["fanout_physical_interface_id"] = json.Number("1")
		},
		"inbound-fanout": func(s *legacyExport) {
			coreRow(t, s, "physicalinterface", 1)["fanout_physical_interface_id"] = json.Number("100")
		},
		"missing-exported-switch":  func(s *legacyExport) { coreRow(t, s, "switch", 2)["cabinetid"] = json.Number("999") },
		"missing-owner":            func(s *legacyExport) { coreRow(t, s, "virtualinterface", 10)["custid"] = json.Number("999") },
		"missing-lag-channel":      func(s *legacyExport) { coreRow(t, s, "virtualinterface", 20)["channelgroup"] = nil },
		"unknown-physical-status":  func(s *legacyExport) { coreRow(t, s, "physicalinterface", 100)["status"] = json.Number("99") },
		"missing-autoneg":          func(s *legacyExport) { delete(coreRow(t, s, "physicalinterface", 100), "autoneg") },
		"invalid-enabled":          func(s *legacyExport) { coreRow(t, s, "corebundles", 10)["enabled"] = json.Number("2") },
		"invalid-core-port-active": func(s *legacyExport) { coreRow(t, s, "switchport", 10)["active"] = json.Number("2") },
		"missing-ecmp-subnets": func(s *legacyExport) {
			l := coreRow(t, s, "corelinks", 100)
			l["ipv4_subnet"], l["ipv6_subnet"] = nil, nil
		},
		"host-bits-subnet": func(s *legacyExport) { coreRow(t, s, "corelinks", 100)["ipv4_subnet"] = "10.0.0.1/31" },
		"side-multiple-vis": func(s *legacyExport) {
			v := copyCoreRow(coreRow(t, s, "virtualinterface", 10))
			v["id"] = json.Number("90")
			s.Tables["virtualinterface"] = append(s.Tables["virtualinterface"], v)
			coreRow(t, s, "physicalinterface", 102)["virtualinterfaceid"] = json.Number("90")
		},
		"side-multiple-switches": func(s *legacyExport) {
			s.Tables["switch"] = append(s.Tables["switch"], row{"id": json.Number("3"), "name": "third", "cabinetid": json.Number("1")})
			coreRow(t, s, "switchport", 12)["switchid"] = json.Number("3")
		},
		"core-group-collision": func(s *legacyExport) { coreRow(t, s, "virtualinterface", 30)["channelgroup"] = json.Number("0") },
		"member-group-collision": func(s *legacyExport) {
			v := coreRow(t, s, "virtualinterface", 1)
			v["lag_framing"] = true
			v["channelgroup"] = json.Number("0")
		},
		"overlapping-core-subnet": func(s *legacyExport) { coreRow(t, s, "corelinks", 101)["ipv4_subnet"] = "10.0.0.0/31" },
		"peering-subnet-overlap":  func(s *legacyExport) { coreRow(t, s, "corelinks", 100)["ipv4_subnet"] = "192.0.2.0/31" },
		"loopback-overlap":        func(s *legacyExport) { coreRow(t, s, "switch", 1)["loopback_ip"] = "10.0.0.0" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := coreFixture(t)
			mutate(&s)
			raw, _ := json.Marshal(s)
			for _, args := range [][]string{nil, {"-strict"}} {
				var out, log bytes.Buffer
				if err := run(args, bytes.NewReader(raw), &out, &log); err == nil || out.Len() != 0 {
					t.Fatalf("unsafe topology published (%v): %v", args, err)
				}
			}
			output := filepath.Join(t.TempDir(), "must-not-exist.json")
			var out, log bytes.Buffer
			if err := run([]string{"-output", output}, bytes.NewReader(raw), &out, &log); err == nil {
				t.Fatal("unsafe output file accepted")
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatal("unsafe import file was created")
			}
		})
	}
}
func TestCoreReservedUnusedPortAndInputOrdering(t *testing.T) {
	s := coreFixture(t)
	s.Tables["corebundles"], s.Tables["corelinks"], s.Tables["coreinterfaces"] = nil, nil, nil
	s.Tables["physicalinterface"] = s.Tables["physicalinterface"][:1]
	s.Tables["virtualinterface"] = s.Tables["virtualinterface"][:1]
	result, err := convert(s)
	if err != nil {
		t.Fatal("unused reservations should remain valid", err)
	}
	if len(result.CoreBundles) != 0 {
		t.Fatal("invented a bundle")
	}
	for _, p := range result.Resources["ports"] {
		if p["id"] != "legacy-ports-1" && p["coreReserved"] != true {
			t.Fatal("unused core reservation lost")
		}
	}
	a := coreFixture(t)
	before, err := convert(a)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"corebundles", "corelinks", "coreinterfaces"} {
		rows := a.Tables[table]
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	}
	after, err := convert(a)
	if err != nil || !reflect.DeepEqual(before.CoreBundles, after.CoreBundles) {
		t.Fatal("core identity/order depends on export row ordering", err)
	}
}
func TestCoreConversionImportsThroughRealAPIWithFreshLineage(t *testing.T) {
	raw, _ := json.Marshal(coreFixture(t))
	var output, log bytes.Buffer
	if err := run([]string{"-strict"}, bytes.NewReader(raw), &output, &log); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	a, err := app.New(app.Config{DataDir: dir, BirdBinary: "/nonexistent/bird"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	c := &migrationDNSClient{t: t, a: a}
	c.call("POST", "/api/setup", app.Record{"name": "Migration admin", "email": "core@example.net", "password": "migration-core-password", "ixpName": "Core Fixture IXP", "asn": 64500, "setupToken": a.BootstrapToken()})
	payload := app.Record{}
	if err = json.Unmarshal(output.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	c.call("POST", "/api/import", payload)
	stored, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state app.State
	if err = json.Unmarshal(stored, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.CoreBundles) != 3 || len(state.Resources["connections"]) != 1 {
		t.Fatal("typed import lost core/member separation")
	}
	for _, b := range state.CoreBundles {
		if b.BindingRevision == "" || b.CreatedAt == "" || b.UpdatedAt == "" || strings.HasPrefix(b.CreatedAt, "2018-") {
			t.Fatal("import reused legacy lineage")
		}
	}
	exported := c.call("GET", "/api/export", nil)
	if len(exported["coreBundles"].([]any)) != 3 {
		t.Fatal("typed topology absent from export")
	}
	if ports := state.Resources["ports"]; len(ports) != 14 {
		t.Fatal("core reserved port inventory lost", len(ports))
	}
}

func TestCoreMigrationPreservesIPv6LoopbackAndCanonicalSubnet(t *testing.T) {
	s := coreFixture(t)
	coreRow(t, &s, "switch", 2)["loopback_ip"] = "FD00:FFFF::2"
	coreRow(t, &s, "corelinks", 100)["ipv6_subnet"] = "FD00:0010::/127"
	result, err := convert(s)
	if err != nil {
		t.Fatal(err)
	}
	sw := result.Resources["switches"][1]
	if sw["loopbackIpv6"] != "fd00:ffff::2" || sw["loopbackIpv4"] != nil || result.CoreBundles[0].Links[0].IPv6Subnet != "fd00:10::/127" {
		t.Fatal("IPv6 loopback/subnet coerced into IPv4 or lost")
	}
}
