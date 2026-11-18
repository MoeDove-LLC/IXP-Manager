package snmp

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
	"github.com/moedove/ixp-manager/internal/traffic"
)

func packetCounter(f *fakeTransport, table, column string, kind gosnmp.Asn1BER, value any) {
	f.tables[table] = append(f.tables[table], gosnmp.SnmpPDU{Name: table + "." + column + ".7", Type: kind, Value: value})
}
func TestIFMIBPacketCountersPreferHCWithoutPrecisionLoss(t *testing.T) {
	f := mibFixture()
	packetCounter(f, ifTable, "11", gosnmp.Counter32, uint32(3))
	packetCounter(f, ifTable, "17", gosnmp.Counter32, uint32(4))
	packetCounter(f, ifTable, "20", gosnmp.Counter32, uint32(0))
	packetCounter(f, ifTable, "13", gosnmp.Counter32, uint32(5))
	packetCounter(f, ifTable, "19", gosnmp.Counter32, uint32(6))
	for _, pair := range []struct {
		column string
		value  uint64
	}{{"7", 9007199254740995}, {"11", 9007199254740997}, {"8", 11}, {"12", 13}, {"9", 17}, {"13", 19}} {
		packetCounter(f, ifXTable, pair.column, gosnmp.Counter64, pair.value)
	}
	for _, column := range []string{"2", "3", "4", "5"} {
		packetCounter(f, ifXTable, column, gosnmp.Counter32, uint32(1))
	}
	s, err := CollectWithTransport(context.Background(), configFixture(), f)
	if err != nil {
		t.Fatal(err)
	}
	p := s.Interfaces[0]
	for name, metric := range map[string]*traffic.CounterPair{"unicast": p.Unicast, "multicast": p.Multicast, "broadcast": p.Broadcast} {
		if metric == nil || metric.Width(true) != 64 || metric.Width(false) != 64 {
			t.Fatalf("%s HC counters lost: %+v", name, metric)
		}
	}
	if p.Unicast.In != "9007199254740995" || p.Unicast.Out != "9007199254740997" || p.Multicast.In != "11" || p.Multicast.Out != "13" || p.Broadcast.In != "17" || p.Broadcast.Out != "19" {
		t.Fatalf("wrong IF-X columns: %+v", p)
	}
	if p.Errors == nil || p.Errors.Width(true) != 32 || p.Errors.In != "4" || p.Errors.Out != "0" || p.InErrors != p.Errors.In || p.OutErrors != p.Errors.Out || p.Discards == nil || p.Discards.Width(false) != 32 || p.InDiscards != "5" || p.OutDiscards != "6" {
		t.Fatalf("32-bit compatibility counters lost: %+v", p)
	}
	raw, _ := json.Marshal(s)
	if !strings.Contains(string(raw), `"in":"9007199254740995"`) {
		t.Fatal("large packet counter is not an exact decimal string")
	}
	for _, secret := range []string{configFixture().Username, configFixture().AuthPassphrase, configFixture().PrivacyPassphrase} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("secret reached extended snapshot")
		}
	}
	var restored Snapshot
	if err = json.Unmarshal(raw, &restored); err != nil || ValidateSnapshot(restored, time.Now()) != nil || restored.Interfaces[0].Unicast.In != p.Unicast.In {
		t.Fatal("typed counter snapshot round trip failed", err)
	}
}

func TestIFMIBMixedWidthsAndMissingDirectionsRemainExplicit(t *testing.T) {
	f := mibFixture()
	packetCounter(f, ifXTable, "7", gosnmp.Counter64, uint64(math.MaxUint64))
	packetCounter(f, ifTable, "17", gosnmp.Counter32, uint32(math.MaxUint32))
	packetCounter(f, ifXTable, "12", gosnmp.Counter64, uint64(0))
	packetCounter(f, ifTable, "13", gosnmp.Gauge32, uint32(99))
	s, err := CollectWithTransport(context.Background(), configFixture(), f)
	if err != nil {
		t.Fatal(err)
	}
	p := s.Interfaces[0]
	if p.Unicast == nil || p.Unicast.Width(true) != 64 || p.Unicast.Width(false) != 32 || p.Unicast.In != "18446744073709551615" || p.Unicast.Out != "4294967295" {
		t.Fatal("mixed-width directions discarded or narrowed", p.Unicast)
	}
	if p.Multicast == nil || p.Multicast.In != "" || p.Multicast.Out != "0" || p.Multicast.Width(false) != 64 || p.Broadcast != nil || p.Discards != nil || p.InDiscards != "" {
		t.Fatal("missing direction fabricated zero or gauge became counter")
	}
	if p.Errors.In != "4" || p.Errors.Out != "" || p.OutErrors != "" {
		t.Fatal("missing error direction fabricated")
	}
}

func TestIFMIBFallbackDoesNotRelabelNonUnicastOrTotalPackets(t *testing.T) {
	f := mibFixture()
	delete(f.tables, ifXTable)
	for _, column := range []string{"11", "17", "12", "18"} {
		packetCounter(f, ifTable, column, gosnmp.Counter32, uint32(15))
	}
	s, err := CollectWithTransport(context.Background(), configFixture(), f)
	if err != nil {
		t.Fatal(err)
	}
	p := s.Interfaces[0]
	if p.Unicast == nil || p.Unicast.Bits != 32 || p.Unicast.In != "15" || p.Unicast.Out != "15" {
		t.Fatal("unicast32 fallback lost")
	}
	if p.Multicast != nil || p.Broadcast != nil {
		t.Fatal("deprecated NUcast counters invented multicast/broadcast split")
	}
	f = mibFixture()
	packetCounter(f, ifXTable, "7", gosnmp.Counter64, "not-a-decoded-integer")
	packetCounter(f, ifTable, "11", gosnmp.Counter32, uint32(23))
	s, err = CollectWithTransport(context.Background(), configFixture(), f)
	if err != nil || s.Interfaces[0].Unicast.In != "23" || s.Interfaces[0].Unicast.Width(true) != 32 {
		t.Fatal("malformed HC prevented valid independent32 fallback", err)
	}
}

func TestMetricSnapshotValidationAndDeepClone(t *testing.T) {
	s, err := CollectWithTransport(context.Background(), configFixture(), mibFixture())
	if err != nil {
		t.Fatal(err)
	}
	s.Interfaces[0].Unicast = &traffic.CounterPair{In: "9007199254740995", Out: "10", InBits: 64, OutBits: 32}
	if err = ValidateSnapshot(s, time.Now()); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Interface){"empty": func(p *Interface) { p.Unicast = &traffic.CounterPair{} }, "width": func(p *Interface) { p.Unicast.InBits = 16 }, "overflow": func(p *Interface) { p.Unicast.Out = "4294967296" }, "negative": func(p *Interface) { p.Unicast.In = "-1" }, "too-long": func(p *Interface) { p.Unicast.In = strings.Repeat("9", 21) }, "missing-with-width": func(p *Interface) { p.Unicast.In = "" }, "errors64": func(p *Interface) { p.Errors.Bits = 64 }, "alias-conflict": func(p *Interface) { p.Errors.In = "99" }} {
		t.Run(name, func(t *testing.T) {
			copy := s.Clone()
			change(&copy.Interfaces[0])
			if ValidateSnapshot(copy, time.Now()) == nil {
				t.Fatal("invalid directional metric accepted")
			}
		})
	}
	copy := s.Clone()
	copy.Interfaces[0].Unicast.In = "1"
	copy.Interfaces[0].Errors.In = "8"
	*copy.Sensors[0].Value = 99
	copy.FDB[0].VLANIDs[0] = 999
	if s.Interfaces[0].Unicast.In != "9007199254740995" || s.Interfaces[0].Errors.In != "4" || *s.Sensors[0].Value == 99 || s.FDB[0].VLANIDs[0] == 999 {
		t.Fatal("snapshot clone shared nested mutable fields")
	}
	legacy := s.Clone()
	legacy.Interfaces[0].Errors = nil
	if ValidateSnapshot(legacy, time.Now()) != nil {
		t.Fatal("legacy error-only snapshot no longer accepted")
	}
}
