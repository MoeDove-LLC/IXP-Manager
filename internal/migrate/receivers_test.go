package migrate

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestReceiverImportPreservesScopeWithoutGrantingAuthority(t *testing.T) {
	source := ReceiverSource{ID: 9007199254740993, VirtualInterfaceID: 7, MemberID: "member", Address: "2001:0DB8::20", Port: 6343, Enabled: true}
	connections := []ReceiverConnection{{ID: "connection-b", MemberID: "member", VLANID: "vlan-b", VirtualInterfaceID: 7}, {ID: "connection-a", MemberID: "member", VLANID: "vlan-a", VirtualInterfaceID: 7}}
	got, err := ConvertReceiver(source, connections)
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.ID != "legacy-sflow-receiver-9007199254740993" || got.Spec.Revision != "" || got.Spec.Address != "2001:db8::20" || got.Spec.ConnectionIDs[0] != "connection-a" || len(got.Spec.VLANIDs) != 2 || !got.Spec.Enabled {
		t.Fatal("scope/intent/precision changed", got)
	}
	raw, _ := json.Marshal(got)
	for _, forbidden := range []string{"approved", "memberRevision", "createdAt", "delivery", "checksum"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatal("migration fabricated runtime authority", forbidden)
		}
	}
}

func TestReceiverConversionRejectsAmbiguousAndInvalidDestinations(t *testing.T) {
	base := ReceiverSource{ID: 1, VirtualInterfaceID: 7, MemberID: "member", Address: "192.0.2.20", Port: 6343, Enabled: true}
	valid := []ReceiverConnection{{ID: "connection", MemberID: "member", VLANID: "vlan", VirtualInterfaceID: 7}}
	for _, address := range []string{"receiver.example.net", "192.0.2.20/32", " 192.0.2.20", "224.0.0.1", "0.0.0.0", "255.255.255.255", "ff02::1", "fe80::1%eth0", "::ffff:192.0.2.20"} {
		source := base
		source.Address = address
		if _, err := ConvertReceiver(source, valid); err == nil {
			t.Fatal("unsafe destination accepted", address)
		}
	}
	for _, port := range []int64{-1, 0, 65536, 1 << 32} {
		source := base
		source.Port = port
		if _, err := ConvertReceiver(source, valid); err == nil {
			t.Fatal("port outside UDP range accepted")
		}
	}
	for _, connections := range [][]ReceiverConnection{nil, {{ID: "connection", MemberID: "other", VLANID: "vlan", VirtualInterfaceID: 7}}, {{ID: "connection", MemberID: "member", VLANID: "vlan", VirtualInterfaceID: 8}}, {{ID: "one", MemberID: "member", VLANID: "vlan", VirtualInterfaceID: 7}, {ID: "two", MemberID: "member", VLANID: "vlan", VirtualInterfaceID: 7}}} {
		if _, err := ConvertReceiver(base, connections); err == nil {
			t.Fatal("non-closed scope accepted", connections)
		}
	}
}

func TestRegisteredMACNormalizationIsExplicitEUI48(t *testing.T) {
	for _, value := range []string{"02000000000A", "02:00:00:00:00:0a", "0200.0000.000a"} {
		got, err := RegisteredMAC(value)
		if err != nil || got != "02:00:00:00:00:0a" {
			t.Fatal(value, got, err)
		}
	}
	for _, value := range []string{"", "02000000000g", "00:00:00:00:00:00", "ff:ff:ff:ff:ff:ff", "01:00:5e:00:00:01", "02:00:00:00:00:00:00:01", "02:00:00:00:00:01\n"} {
		if _, err := RegisteredMAC(value); err == nil {
			t.Fatal("invalid registered unicast EUI48 accepted", value)
		}
	}
}
