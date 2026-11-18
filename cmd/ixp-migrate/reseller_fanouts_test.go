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
)

func legacyFanoutFixture(t *testing.T) legacyExport {
	t.Helper()
	raw, err := os.ReadFile("../../tools/migrate/example-legacy-fanouts.json")
	if err != nil {
		t.Fatal(err)
	}
	return decodeSample(t, string(raw))
}
func fanoutSourceRow(t *testing.T, s *legacyExport, table string, id int64) row {
	t.Helper()
	for _, r := range s.Tables[table] {
		if idOf(r) == id {
			return r
		}
	}
	t.Fatal("missing fixture row", table, id)
	return nil
}
func fanoutOutputRow(t *testing.T, result export, kind, id string) row {
	t.Helper()
	for _, r := range result.Resources[kind] {
		if r["id"] == id {
			return r
		}
	}
	t.Fatal("missing converted record", kind, id)
	return nil
}

func TestLegacyFanoutPreservesPhysicalPairsAcrossLAGAndVLANs(t *testing.T) {
	s := legacyFanoutFixture(t)
	before, _ := json.Marshal(s)
	result, err := convert(s)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(s)
	if !bytes.Equal(before, after) {
		t.Fatal("converter mutated legacy evidence")
	}
	if len(result.Resources["reseller-fanouts"]) != 2 || len(result.Resources["connections"]) != 2 || len(result.Resources["ports"]) != 4 {
		t.Fatal("fanout duplicated per VLAN or logical access manufactured", result.Warnings)
	}
	for index, relation := range result.Resources["reseller-fanouts"] {
		want := []string{"1", "2"}[index]
		if relation["id"] != "legacy-reseller-fanouts-"+want || relation["memberId"] != "legacy-members-1" || relation["resellerId"] != "legacy-members-2" || relation["peeringPortId"] != "legacy-ports-"+want || relation["fanoutPortId"] != []string{"legacy-ports-3", "legacy-ports-4"}[index] {
			t.Fatal("physical pair/owner changed", relation)
		}
		if len(relation) != 6 {
			t.Fatal("invented relation fields", relation)
		}
	}
	for _, id := range []string{"legacy-ports-3", "legacy-ports-4"} {
		if fanoutOutputRow(t, result, "ports", id)["memberId"] != "legacy-members-2" {
			t.Fatal("passive port lost direct reseller reservation")
		}
	}
	for _, connection := range result.Resources["connections"] {
		if connection["memberId"] != "legacy-members-1" || !reflect.DeepEqual(connection["portIds"], []string{"legacy-ports-1", "legacy-ports-2"}) {
			t.Fatal("fanout mixed into logical member access")
		}
	}
	if len(result.Warnings) != 0 {
		t.Fatal("proven exact source was not fully resolved", result.Warnings)
	}
	for _, table := range []string{"physicalinterface", "virtualinterface", "switchport"} {
		rows := s.Tables[table]
		for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
			rows[left], rows[right] = rows[right], rows[left]
		}
	}
	reordered, err := convert(s)
	if err != nil || !reflect.DeepEqual(result.Resources["reseller-fanouts"], reordered.Resources["reseller-fanouts"]) {
		t.Fatal("physical pair order unstable", err)
	}
}

func TestLegacyFanoutRejectsUnsafeSourceWithoutCLIOutput(t *testing.T) {
	cases := map[string]func(*legacyExport){
		"missing-target": func(s *legacyExport) {
			fanoutSourceRow(t, s, "physicalinterface", 1)["fanout_physical_interface_id"] = json.Number("404")
		},
		"malformed-target": func(s *legacyExport) {
			fanoutSourceRow(t, s, "physicalinterface", 1)["fanout_physical_interface_id"] = "PRIVATE-SECRET"
		},
		"boolean-target": func(s *legacyExport) {
			fanoutSourceRow(t, s, "physicalinterface", 1)["fanout_physical_interface_id"] = true
		},
		"noncanonical-target": func(s *legacyExport) {
			fanoutSourceRow(t, s, "physicalinterface", 1)["fanout_physical_interface_id"] = "03"
		},
		"self-target": func(s *legacyExport) {
			fanoutSourceRow(t, s, "physicalinterface", 1)["fanout_physical_interface_id"] = json.Number("1")
		},
		"chain": func(s *legacyExport) {
			fanoutSourceRow(t, s, "physicalinterface", 3)["fanout_physical_interface_id"] = json.Number("4")
		},
		"duplicate-target": func(s *legacyExport) {
			fanoutSourceRow(t, s, "physicalinterface", 2)["fanout_physical_interface_id"] = json.Number("3")
		},
		"duplicate-passive-port": func(s *legacyExport) {
			fanoutSourceRow(t, s, "physicalinterface", 4)["switchportid"] = json.Number("3")
		},
		"same-port": func(s *legacyExport) {
			fanoutSourceRow(t, s, "physicalinterface", 3)["switchportid"] = json.Number("1")
		},
		"missing-fanout-vi":          func(s *legacyExport) { fanoutSourceRow(t, s, "physicalinterface", 3)["virtualinterfaceid"] = nil },
		"wrong-fanout-owner":         func(s *legacyExport) { fanoutSourceRow(t, s, "virtualinterface", 2)["custid"] = json.Number("1") },
		"not-resold":                 func(s *legacyExport) { delete(fanoutSourceRow(t, s, "cust", 1), "reseller") },
		"not-designated-reseller":    func(s *legacyExport) { fanoutSourceRow(t, s, "cust", 2)["isReseller"] = json.Number("0") },
		"unknown-reseller-flag":      func(s *legacyExport) { fanoutSourceRow(t, s, "cust", 2)["isReseller"] = "PRIVATE-SECRET" },
		"missing-converted-reseller": func(s *legacyExport) { fanoutSourceRow(t, s, "cust", 2)["name"] = "" },
		"missing-switch":             func(s *legacyExport) { fanoutSourceRow(t, s, "switchport", 3)["switchid"] = json.Number("404") },
		"wrong-port-type":            func(s *legacyExport) { fanoutSourceRow(t, s, "switchport", 3)["type"] = json.Number("7") },
		"unknown-port-type":          func(s *legacyExport) { fanoutSourceRow(t, s, "switchport", 3)["type"] = json.Number("0") },
		"wrong-peering-type":         func(s *legacyExport) { fanoutSourceRow(t, s, "switchport", 1)["type"] = json.Number("6") },
		"core-port":                  func(s *legacyExport) { fanoutSourceRow(t, s, "switchport", 3)["type"] = json.Number("3") },
		"shared-passive-vi": func(s *legacyExport) {
			fanoutSourceRow(t, s, "physicalinterface", 4)["virtualinterfaceid"] = json.Number("2")
		},
		"passive-lag":          func(s *legacyExport) { fanoutSourceRow(t, s, "virtualinterface", 2)["lag_framing"] = true },
		"passive-trunk":        func(s *legacyExport) { fanoutSourceRow(t, s, "virtualinterface", 2)["trunk"] = true },
		"passive-channelgroup": func(s *legacyExport) { fanoutSourceRow(t, s, "virtualinterface", 2)["channelgroup"] = json.Number("1") },
		"fanout-vlan-access": func(s *legacyExport) {
			s.Tables["vlaninterface"] = append(s.Tables["vlaninterface"], row{"id": json.Number("3"), "virtualinterfaceid": json.Number("2"), "vlanid": json.Number("1"), "ipv4enabled": json.Number("0"), "ipv6enabled": json.Number("0")})
		},
		"partial-member-vlan-conversion":      func(s *legacyExport) { fanoutSourceRow(t, s, "vlaninterface", 2)["vlanid"] = json.Number("404") },
		"missing-converted-member-connection": func(s *legacyExport) { fanoutSourceRow(t, s, "virtualinterface", 1)["mtu"] = json.Number("65000") },
		"orphan-typed-fanout": func(s *legacyExport) {
			s.Tables["switchport"] = append(s.Tables["switchport"], row{"id": json.Number("9"), "name": "unassociated fanout", "switchid": json.Number("1"), "active": json.Number("1"), "type": json.Number("6")})
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s := legacyFanoutFixture(t)
			change(&s)
			raw, _ := json.Marshal(s)
			if _, err := convert(s); err == nil {
				t.Fatal("unsafe fanout source accepted")
			}
			for _, strict := range []bool{false, true} {
				var output, diagnostics bytes.Buffer
				path := filepath.Join(t.TempDir(), "unsafe.json")
				args := []string{"--output", path}
				if strict {
					args = append(args, "--strict")
				}
				err := run(args, bytes.NewReader(raw), &output, &diagnostics)
				if err == nil || output.Len() != 0 {
					t.Fatal("unsafe conversion produced output", strict)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("unsafe conversion wrote import file")
				}
				if strings.Contains(err.Error()+diagnostics.String(), "PRIVATE-SECRET") {
					t.Fatal("source error leaked ignored scalar")
				}
			}
		})
	}
}

func TestLegacyFanoutLargeIdentityMetadataAndUnresolvedRows(t *testing.T) {
	s := legacyFanoutFixture(t)
	fanoutSourceRow(t, &s, "physicalinterface", 1)["id"] = json.Number("9007199254740993")
	for _, port := range s.Tables["switchport"] {
		delete(port, "type")
	}
	fn := fanoutSourceRow(t, &s, "physicalinterface", 3)
	fn["notes"] = "PRIVATE-NOTE-SECRET"
	fn["password"] = "PASSWORD-SECRET"
	fn["status"] = json.Number("1")
	s.Tables["virtualinterface"] = append(s.Tables["virtualinterface"], row{"id": json.Number("20"), "custid": json.Number("2")})
	s.Tables["physicalinterface"] = append(s.Tables["physicalinterface"], row{"id": json.Number("20"), "virtualinterfaceid": json.Number("20")})
	result, err := convert(s)
	if err != nil {
		t.Fatal(err)
	}
	if fanoutOutputRow(t, result, "reseller-fanouts", "legacy-reseller-fanouts-9007199254740993")["fanoutPortId"] != "legacy-ports-3" {
		t.Fatal("large source ID rounded")
	}
	raw, _ := json.Marshal(result)
	for _, secret := range []string{"PRIVATE-NOTE-SECRET", "PASSWORD-SECRET"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("unreviewed field leaked")
		}
	}
	warnings := strings.Join(result.Warnings, "\n")
	if !strings.Contains(warnings, "physicalinterface: 1 unassigned") || !strings.Contains(warnings, "virtualinterface: 1 unassigned") || !strings.Contains(warnings, "notes") || !strings.Contains(warnings, "status") {
		t.Fatal("unrelated standalone metadata incorrectly marked migrated", warnings)
	}
}

func TestLegacyFanoutOrphanTypedPortFailsEvenWithoutLinks(t *testing.T) {
	s := legacyFanoutFixture(t)
	for _, pi := range s.Tables["physicalinterface"] {
		delete(pi, "fanout_physical_interface_id")
	}
	if _, err := convert(s); err == nil {
		t.Fatal("known fanout role became free port inventory")
	}
}

func TestLegacyFanoutConversionImportsThroughGoWithReservations(t *testing.T) {
	source := legacyFanoutFixture(t)
	raw, _ := json.Marshal(source)
	var output, diagnostics bytes.Buffer
	if err := run([]string{"--strict"}, bytes.NewReader(raw), &output, &diagnostics); err != nil {
		t.Fatal(err, diagnostics.String())
	}
	dir := t.TempDir()
	a, err := app.New(app.Config{DataDir: dir, BirdBinary: "/nonexistent/bird"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	c := &migrationDNSClient{t: t, a: a}
	c.call("POST", "/api/setup", app.Record{"name": "Fanout migration admin", "email": "fanout-migrate@example.test", "password": "fanout-migration-password", "ixpName": "Fanout fixture IXP", "asn": 64599, "setupToken": a.BootstrapToken()})
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
	if len(state.Resources["reseller-fanouts"]) != 2 || len(state.Resources["connections"]) != 2 || len(state.PeerCredentials) != 0 || len(state.Deployments) != 0 {
		t.Fatal("fanout import lost pairs or created routing authority")
	}
	for _, relation := range state.Resources["reseller-fanouts"] {
		if relation["createdAt"] == nil || relation["memberId"] != "legacy-members-1" || relation["resellerId"] != "legacy-members-2" {
			t.Fatal("fanout current lineage/owner missing")
		}
	}
	exported := c.call("GET", "/api/export", nil)["resources"].(map[string]any)
	if len(exported["reseller-fanouts"].([]any)) != 2 {
		t.Fatal("reserved relation omitted from Go inventory export")
	}
}
