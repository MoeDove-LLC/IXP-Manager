package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func receiverFixture(t *testing.T) legacyExport {
	t.Helper()
	raw, err := os.ReadFile("../../tools/migrate/example-legacy-sflow-receivers.json")
	if err != nil {
		t.Fatal(err)
	}
	return decodeSample(t, string(raw))
}

func TestLegacyReceiversAndConfiguredMACsPreserveClosedLAGScope(t *testing.T) {
	source := receiverFixture(t)
	result, err := convert(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.SFlowReceivers) != 2 || len(result.Resources["mac-addresses"]) != 3 {
		t.Fatal("receiver/registered MAC rows lost")
	}
	for i, receiver := range result.SFlowReceivers {
		if receiver.Spec.Revision != "" || receiver.Spec.MemberID != "legacy-members-1" || len(receiver.Spec.ConnectionIDs) != 2 || len(receiver.Spec.VLANIDs) != 2 || receiver.Spec.ConnectionIDs[0] != "legacy-connections-1" || receiver.Spec.ConnectionIDs[1] != "legacy-connections-2" || receiver.Spec.Port != 6343 {
			t.Fatalf("receiver scope or authority changed: %+v", receiver)
		}
		if receiver.Spec.Enabled != (i == 0) {
			t.Fatal("standard row/explicit active extension intent changed")
		}
	}
	if result.SFlowReceivers[0].Spec.Address != "198.51.100.250" || result.SFlowReceivers[1].Spec.Address != "2001:db8:ffff::250" {
		t.Fatal("destination changed")
	}
	for _, connection := range result.Resources["connections"] {
		ports, ok := connection["portIds"].([]string)
		if !ok || len(ports) != 2 {
			t.Fatal("receiver conversion flattened a LAG")
		}
	}
	for i, mac := range result.Resources["mac-addresses"] {
		want := map[int]string{0: "02:00:00:00:00:01", 1: "02:00:00:00:00:02", 2: "02:00:00:00:00:03"}[i]
		if mac["address"] != want {
			t.Fatal("registered MAC normalization changed", mac)
		}
	}
	raw, _ := json.Marshal(result)
	if bytes.Contains(raw, []byte("0200000000ff")) || bytes.Contains(raw, []byte("02:00:00:00:00:ff")) || bytes.Contains(raw, []byte("approvedAt")) || bytes.Contains(raw, []byte("approvedChecksum")) {
		t.Fatal("learned MAC or runtime authority leaked into import")
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "learned MAC observations are excluded") {
		t.Fatal("learned observations exclusion was silent")
	}
	delete(source.Tables, "macaddress")
	result, err = convert(source)
	if err != nil || len(result.Warnings) != 0 {
		t.Fatal("supported source unexpectedly incomplete", result.Warnings, err)
	}
	encoded, _ := json.Marshal(source)
	var out, log bytes.Buffer
	if err = run([]string{"-strict"}, bytes.NewReader(encoded), &out, &log); err != nil || out.Len() == 0 || log.Len() != 0 {
		t.Fatal("complete receiver source failed strict conversion", err, log.String())
	}
}

func TestReceiverAndRegisteredMACFailuresNeverEmitPartialImport(t *testing.T) {
	changes := map[string]func(*legacyExport){
		"missing-vi":         func(s *legacyExport) { s.Tables["sflow_receiver"][0]["virtual_interface_id"] = json.Number("999") },
		"boolean-vi":         func(s *legacyExport) { s.Tables["sflow_receiver"][0]["virtual_interface_id"] = true },
		"missing-member":     func(s *legacyExport) { s.Tables["virtualinterface"][0]["custid"] = json.Number("999") },
		"missing-vlan":       func(s *legacyExport) { s.Tables["vlaninterface"][1]["vlanid"] = json.Number("999") },
		"dropped-connection": func(s *legacyExport) { s.Tables["virtualinterface"][0]["mtu"] = json.Number("65000") },
		"missing-port":       func(s *legacyExport) { s.Tables["physicalinterface"][1]["switchportid"] = json.Number("999") },
		"ambiguous-vlan": func(s *legacyExport) {
			s.Tables["vlaninterface"][1]["vlanid"] = json.Number("1")
			s.Tables["vlaninterface"][1]["ipv4enabled"] = json.Number("0")
		},
		"duplicate-mac":         func(s *legacyExport) { s.Tables["l2address"][2]["mac"] = "020000000001" },
		"invalid-mac":           func(s *legacyExport) { s.Tables["l2address"][0]["mac"] = "ff:ff:ff:ff:ff:ff" },
		"missing-mac-vli":       func(s *legacyExport) { s.Tables["l2address"][0]["vlan_interface_id"] = json.Number("999") },
		"hostname-destination":  func(s *legacyExport) { s.Tables["sflow_receiver"][0]["dst_ip"] = "receiver.example.test" },
		"multicast-destination": func(s *legacyExport) { s.Tables["sflow_receiver"][0]["dst_ip"] = "ff02::1" },
		"port-overflow":         func(s *legacyExport) { s.Tables["sflow_receiver"][0]["dst_port"] = json.Number("65536") },
		"port-fraction":         func(s *legacyExport) { s.Tables["sflow_receiver"][0]["dst_port"] = json.Number("6343.5") },
		"port-boolean":          func(s *legacyExport) { s.Tables["sflow_receiver"][0]["dst_port"] = true },
		"unknown-active":        func(s *legacyExport) { s.Tables["sflow_receiver"][0]["active"] = "unknown" },
		"unknown-lifecycle":     func(s *legacyExport) { s.Tables["sflow_receiver"][0]["status"] = "approved" },
		"partial-vli-owner":     func(s *legacyExport) { s.Tables["vlaninterface"][1]["virtualinterfaceid"] = "invalid" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			source := receiverFixture(t)
			change(&source)
			encoded, _ := json.Marshal(source)
			var out, log bytes.Buffer
			if err := run(nil, bytes.NewReader(encoded), &out, &log); err == nil || out.Len() != 0 {
				t.Fatal("non-strict emitted an unsafe/partial receiver import", err, out.String())
			}
			if !strings.Contains(log.String(), "review:") {
				t.Fatal("missing actionable source-row warning")
			}
		})
	}
}

func TestConfiguredMACConflictUsesActualVLANNotTagOrDestination(t *testing.T) {
	source := receiverFixture(t)
	delete(source.Tables, "macaddress")
	source.Tables["l2address"][1]["mac"] = "020000000001" // same MAC on a distinct VLAN is explicit.
	if result, err := convert(source); err != nil || len(result.Resources["mac-addresses"]) != 3 {
		t.Fatal("different VLAN identities collapsed", err)
	}
	// Create a second VI/physical attachment on the same VLAN and register the
	// same MAC there. It must fail even when both connections belong to one member.
	source.Tables["virtualinterface"] = append(source.Tables["virtualinterface"], row{"id": json.Number("2"), "name": "another VI", "custid": json.Number("1")})
	source.Tables["switchport"] = append(source.Tables["switchport"], row{"id": json.Number("3"), "name": "eth3", "switchid": json.Number("1"), "active": json.Number("1")})
	source.Tables["physicalinterface"] = append(source.Tables["physicalinterface"], row{"id": json.Number("3"), "virtualinterfaceid": json.Number("2"), "switchportid": json.Number("3"), "status": json.Number("1")})
	source.Tables["vlaninterface"] = append(source.Tables["vlaninterface"], row{"id": json.Number("3"), "virtualinterfaceid": json.Number("2"), "vlanid": json.Number("1"), "ipv4enabled": json.Number("0"), "ipv6enabled": json.Number("0")})
	source.Tables["l2address"] = append(source.Tables["l2address"], row{"id": json.Number("4"), "vlan_interface_id": json.Number("3"), "mac": "020000000001"})
	if _, err := convert(source); err == nil {
		t.Fatal("same VLAN cross-connection MAC conflict accepted")
	}
}

func TestReceiverSourceSecretsAndLegacyTimesDoNotBecomeApproval(t *testing.T) {
	source := receiverFixture(t)
	source.Tables["sflow_receiver"][0]["legacy_secret"] = "DO_NOT_COPY_RECEIVER_SECRET"
	source.Tables["macaddress"][0]["mac"] = "DO_NOT_COPY_LEARNED_VALUE"
	result, err := convert(source)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	if bytes.Contains(raw, []byte("DO_NOT_COPY")) || bytes.Contains(raw, []byte("2020-01-01")) {
		t.Fatal("source private value/timestamp copied into authority")
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "legacy_secret") {
		t.Fatal("unsupported field not reported")
	}
}

func TestRegisteredMACAndReceiverSameTagAcrossFabricsStaySeparate(t *testing.T) {
	raw, err := os.ReadFile("../../tools/migrate/example-legacy-fabrics.json")
	if err != nil {
		t.Fatal(err)
	}
	source := decodeSample(t, string(raw))
	source.Tables["l2address"] = []row{{"id": json.Number("1"), "vlan_interface_id": json.Number("1"), "mac": "020000000001"}, {"id": json.Number("2"), "vlan_interface_id": json.Number("2"), "mac": "020000000001"}}
	source.Tables["sflow_receiver"] = []row{{"id": json.Number("1"), "virtual_interface_id": json.Number("1"), "dst_ip": "192.0.2.200", "dst_port": json.Number("6343")}, {"id": json.Number("2"), "virtual_interface_id": json.Number("2"), "dst_ip": "192.0.2.201", "dst_port": json.Number("6343")}}
	result, err := convert(source)
	if err != nil || len(result.Resources["mac-addresses"]) != 2 || len(result.SFlowReceivers) != 2 {
		t.Fatal("same tag in separate actual fabrics was conflated", err)
	}
	if result.SFlowReceivers[0].Spec.VLANIDs[0] == result.SFlowReceivers[1].Spec.VLANIDs[0] {
		t.Fatal("receiver VLAN record identity lost")
	}
	source.Tables["switch"][1]["infrastructure"] = json.Number("1")
	if _, err = convert(source); err == nil {
		t.Fatal("receiver graph crossed physical infrastructure")
	}
}
