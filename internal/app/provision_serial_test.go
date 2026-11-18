package app

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/moedove/ixp-manager/internal/provision"
)

const serialTestZone = "2.0.192.in-addr.arpa."

func serialOptions() provision.Options {
	return provision.Options{Kind: "dns-arpa", DNS: provision.DNSOptions{Prefix: "192.0.2.0/24"}}
}
func serialRecord(serial uint32, status string) ProvisionRevision {
	return ProvisionRevision{ID: "serial-fixture", Kind: "dns-arpa", Zone: serialTestZone, Status: status, Options: provision.Options{Kind: "dns-arpa", DNS: provision.DNSOptions{Serial: serial}}}
}

func TestProvisionSerialWrapApprovalAndExplicitZeroBaseline(t *testing.T) {
	s := State{}
	p := Record{"dnsSerialFloor": float64(^uint32(0) - 1)}
	for _, serial := range []uint32{^uint32(0), 1, 2} {
		next, err := allocateProvisionSerial(&s, p, serialOptions())
		if err != nil || next != serial {
			t.Fatal("wrap allocation", next, err)
		}
		d := serialRecord(serial, "validated")
		s.ProvisionRevisions = append(s.ProvisionRevisions, d)
		if err := approveProvisionSerial(&s, d); err != nil {
			t.Fatal("forward serial rejected", err)
		}
		s.ProvisionRevisions[len(s.ProvisionRevisions)-1].Status = "approved"
		if err := validateProvisionSerials(&s); err != nil {
			t.Fatal("persistable wrapped ledger rejected", err)
		}
	}
	if err := approveProvisionSerial(&s, s.ProvisionRevisions[0]); err == nil {
		t.Fatal("old high integer serial reapproved after wrap")
	}
	if provisionSerialApproved(&s, s.ProvisionRevisions[0]) || !provisionSerialApproved(&s, s.ProvisionRevisions[2]) {
		t.Fatal("download head regressed")
	}
	approved := s.ProvisionApprovedSerials[serialTestZone]
	if next, err := allocateProvisionSerial(&s, p, serialOptions()); err != nil || next != 3 || s.ProvisionApprovedSerials[serialTestZone] != approved || !provisionSerialApproved(&s, s.ProvisionRevisions[2]) {
		t.Fatal("new draft implicitly changed approved serial", err)
	}
	var restored State
	raw, _ := json.Marshal(s)
	if err := json.Unmarshal(raw, &restored); err != nil || validateProvisionSerials(&restored) != nil || !reflect.DeepEqual(s.ProvisionSerials, restored.ProvisionSerials) || !reflect.DeepEqual(s.ProvisionApprovedSerials, restored.ProvisionApprovedSerials) {
		t.Fatal("wrapped ledger did not persist exactly")
	}
	zero := State{}
	if next, err := allocateProvisionSerial(&zero, Record{"dnsSerialFloor": float64(0)}, serialOptions()); err != nil || next != 1 {
		t.Fatal("explicit existing zero was treated as missing/new", err)
	}
	if _, err := allocateProvisionSerial(&State{}, Record{}, serialOptions()); err == nil {
		t.Fatal("missing production baseline was guessed")
	}
}

func TestProvisionSerialAmbiguityAndRetainedCollisionFailWithoutMutation(t *testing.T) {
	for _, mode := range []string{"floor-half", "approved-half", "retained-half", "retained-reuse", "issued-missing", "approved-newer"} {
		t.Run(mode, func(t *testing.T) {
			s := State{ProvisionSerials: map[string]uint32{serialTestZone: 1}}
			p := Record{"dnsNewZone": true}
			switch mode {
			case "floor-half":
				p = Record{"dnsSerialFloor": float64(2147483649)}
			case "approved-half":
				s.ProvisionApprovedSerials = map[string]uint32{serialTestZone: 2147483649}
			case "retained-half":
				s.ProvisionRevisions = []ProvisionRevision{serialRecord(2147483650, "draft")}
			case "retained-reuse":
				s.ProvisionRevisions = []ProvisionRevision{serialRecord(2, "draft")}
			case "issued-missing":
				s.ProvisionSerials = nil
				s.ProvisionRevisions = []ProvisionRevision{serialRecord(1, "draft")}
			case "approved-newer":
				s.ProvisionApprovedSerials = map[string]uint32{serialTestZone: 2}
			}
			before, _ := json.Marshal(s)
			if _, err := allocateProvisionSerial(&s, p, serialOptions()); err == nil {
				t.Fatal("ambiguous/backward allocation accepted")
			}
			after, _ := json.Marshal(s)
			if string(before) != string(after) {
				t.Fatal("failed allocation changed persistent head")
			}
		})
	}
	s := State{ProvisionSerials: map[string]uint32{serialTestZone: 1}, ProvisionRevisions: []ProvisionRevision{serialRecord(1, "draft"), serialRecord(1, "draft")}}
	if validateProvisionSerials(&s) == nil {
		t.Fatal("retained serial reuse accepted")
	}
}

func TestProvisionSerialExportAndRecreatedProfilePreserveWrappedHead(t *testing.T) {
	options := serialOptions()
	// A valid complete option set is needed by the resource-export mapper.
	options.VLANID = "v1"
	options.Families = []int{4}
	options.DNS.PrimaryNS = "ns1.example.net."
	options.DNS.ResponsibleMailbox = "hostmaster.example.net."
	options.DNS.NameServers = []string{"ns1.example.net."}
	p := Record{"dnsSerialFloor": float64(^uint32(0)), "options": optionsRecord(options)}
	s := State{Resources: map[string][]Record{provisionProfileKind: {p}}, ProvisionSerials: map[string]uint32{serialTestZone: 1}, ProvisionApprovedSerials: map[string]uint32{serialTestZone: 1}}
	if err := carryProvisionSerialFloors(&s); err != nil || p["dnsSerialFloor"] != float64(1) || p["dnsNewZone"] != false {
		t.Fatal("export restored pre-wrap numeric max", err)
	}
	imported := State{}
	if next, err := allocateProvisionSerial(&imported, p, options); err != nil || next != 2 {
		t.Fatal("resource reimport reset serial", err)
	}
	s.Resources = nil
	s.ProvisionRevisions = nil
	if next, err := allocateProvisionSerial(&s, Record{"dnsNewZone": true}, options); err != nil || next != 2 {
		t.Fatal("profile recreation discarded wrapped ledger", err)
	}
	p["dnsSerialFloor"] = float64(uint32(1) + uint32(1)<<31)
	s.Resources = map[string][]Record{provisionProfileKind: {p}}
	s.ProvisionSerials[serialTestZone] = 1
	if err := carryProvisionSerialFloors(&s); err == nil {
		t.Fatal("resource export chose a side of undefined comparison")
	}
}
