package snmp

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gosnmp/gosnmp"
)

type fakeTransport struct {
	system  []gosnmp.SnmpPDU
	tables  map[string][]gosnmp.SnmpPDU
	walkErr error
	roots   []string
}

func (f *fakeTransport) Get(oids []string) ([]gosnmp.SnmpPDU, error) {
	for _, oid := range oids {
		if !systemOIDs[oid] {
			return nil, errors.New("unexpected system OID")
		}
	}
	return f.system, nil
}
func (f *fakeTransport) Walk(root string, fn func(gosnmp.SnmpPDU) error) error {
	f.roots = append(f.roots, root)
	if !walkRoots[root] {
		return errors.New("arbitrary OID requested")
	}
	if f.walkErr != nil {
		return f.walkErr
	}
	for _, p := range f.tables[root] {
		if err := fn(p); err != nil {
			return err
		}
	}
	return nil
}
func (*fakeTransport) EngineEpoch() string    { return "engine-boot-1" }
func (*fakeTransport) EngineIdentity() string { return strings.Repeat("a", 64) }
func (*fakeTransport) Close() error           { return nil }
func configFixture() Config {
	return Config{SwitchID: "switch-one", Target: "192.0.2.10", Version: "3", Username: "private-collector-user", AuthPassphrase: "private-auth-secret", PrivacyPassphrase: "private-privacy-secret", Mappings: []PortMapping{{PortID: "port-one", IfIndex: 7}}, CollectEntities: true, CollectSensors: true, CollectFDB: true}
}
func mibFixture() *fakeTransport {
	p := func(oid string, kind gosnmp.Asn1BER, value any) gosnmp.SnmpPDU {
		return gosnmp.SnmpPDU{Name: oid, Type: kind, Value: value}
	}
	return &fakeTransport{system: []gosnmp.SnmpPDU{p(systemBase+"1.0", gosnmp.OctetString, []byte("Example switch OS")), p(systemBase+"2.0", gosnmp.ObjectIdentifier, ".1.3.6.1.4.1.99999"), p(systemBase+"3.0", gosnmp.TimeTicks, uint32(123450)), p(systemBase+"5.0", gosnmp.OctetString, []byte("switch-a"))}, tables: map[string][]gosnmp.SnmpPDU{
		ifTable:       {p(ifTable+".1.7", gosnmp.Integer, 7), p(ifTable+".2.7", gosnmp.OctetString, []byte("Physical Ethernet7")), p(ifTable+".3.7", gosnmp.Integer, 6), p(ifTable+".4.7", gosnmp.Integer, 9000), p(ifTable+".5.7", gosnmp.Gauge32, uint32(math.MaxUint32)), p(ifTable+".6.7", gosnmp.OctetString, []byte{0, 17, 34, 51, 68, 85}), p(ifTable+".7.7", gosnmp.Integer, 1), p(ifTable+".8.7", gosnmp.Integer, 1), p(ifTable+".9.7", gosnmp.TimeTicks, uint32(10)), p(ifTable+".10.7", gosnmp.Counter32, uint32(200)), p(ifTable+".16.7", gosnmp.Counter32, uint32(300)), p(ifTable+".14.7", gosnmp.Counter32, uint32(4))},
		ifXTable:      {p(ifXTable+".1.7", gosnmp.OctetString, []byte("xe-7")), p(ifXTable+".6.7", gosnmp.Counter64, uint64(9007199254740993)), p(ifXTable+".10.7", gosnmp.Counter64, uint64(9007199254749993)), p(ifXTable+".15.7", gosnmp.Gauge32, uint32(10000)), p(ifXTable+".18.7", gosnmp.OctetString, []byte("private tenant alias")), p(ifXTable+".19.7", gosnmp.TimeTicks, uint32(50))},
		entityTable:   {p(entityTable+".4.100", gosnmp.Integer, 0), p(entityTable+".5.100", gosnmp.Integer, 10), p(entityTable+".7.100", gosnmp.OctetString, []byte("Port7")), p(entityTable+".11.100", gosnmp.OctetString, []byte("serial-private")), p(entityTable+".4.101", gosnmp.Integer, 100), p(entityTable+".5.101", gosnmp.Integer, 8), p(entityTable+".7.101", gosnmp.OctetString, []byte("Receive power"))},
		entityAliases: {p(entityAliases+".100.0", gosnmp.ObjectIdentifier, ifTable+".1.7")},
		sensorTable:   {p(sensorTable+".1.101", gosnmp.Integer, 6), p(sensorTable+".2.101", gosnmp.Integer, 7), p(sensorTable+".3.101", gosnmp.Integer, 2), p(sensorTable+".4.101", gosnmp.Integer, 1234), p(sensorTable+".5.101", gosnmp.Integer, 1)},
		bridgePortMap: {p(bridgePortMap+".5", gosnmp.Integer, 7)},
		qFDBPort:      {p(qFDBPort+".100.170.187.204.221.238.255", gosnmp.Integer, 5)},
		qFDBStatus:    {p(qFDBStatus+".100.170.187.204.221.238.255", gosnmp.Integer, 3)},
		qVLANFDB:      {p(qVLANFDB+".0.100", gosnmp.Gauge32, uint32(100))},
	}}
}
func TestCollectStandardMIBsLosslessly(t *testing.T) {
	c := configFixture()
	fake := mibFixture()
	snapshot, err := CollectWithTransport(context.Background(), c, fake)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Interfaces) != 1 || snapshot.Interfaces[0].InOctets != "9007199254740993" || snapshot.Interfaces[0].CounterBits != 64 || snapshot.Interfaces[0].SpeedMbps != 10000 || snapshot.Interfaces[0].DiscontinuityTicks != "50" {
		t.Fatalf("counter/interface data lost: %+v", snapshot.Interfaces)
	}
	if len(snapshot.Sensors) != 1 || snapshot.Sensors[0].IfIndex != 7 || snapshot.Sensors[0].Value == nil || math.Abs(*snapshot.Sensors[0].Value-0.00001234) > 1e-12 || snapshot.Sensors[0].Unit != "W" {
		t.Fatalf("standard optical sensor scaling failed: %+v", snapshot.Sensors)
	}
	if len(snapshot.FDB) != 1 || snapshot.FDB[0].MAC != "aa:bb:cc:dd:ee:ff" || snapshot.FDB[0].IfIndex != 7 || len(snapshot.FDB[0].VLANIDs) != 1 || snapshot.FDB[0].VLANIDs[0] != 100 {
		t.Fatal("Q-BRIDGE association lost")
	}
	data, _ := json.Marshal(snapshot)
	for _, secret := range []string{c.Username, c.AuthPassphrase, c.PrivacyPassphrase} {
		if strings.Contains(string(data), secret) {
			t.Fatal("credential serialized in snapshot")
		}
	}
	for _, root := range fake.roots {
		if !walkRoots[root] {
			t.Fatal("arbitrary MIB walked")
		}
	}
}
func TestCounterFallbackAndMissingMappings(t *testing.T) {
	c := configFixture()
	c.CollectEntities = false
	c.CollectSensors = false
	c.CollectFDB = false
	fake := mibFixture()
	delete(fake.tables, ifXTable)
	snapshot, err := CollectWithTransport(context.Background(), c, fake)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Interfaces[0].CounterBits != 32 || snapshot.Interfaces[0].InOctets != "200" || snapshot.Interfaces[0].SpeedMbps != 0 {
		t.Fatal("32-bit fallback or unknown speed fabricated")
	}
	c.Mappings[0].IfIndex = 8
	if _, err = CollectWithTransport(context.Background(), c, fake); err == nil {
		t.Fatal("missing explicit interface silently remapped")
	}
	c = configFixture()
	fake = mibFixture()
	fake.tables[ifXTable][1].Value = float64(9007199254740993)
	snapshot, err = CollectWithTransport(context.Background(), c, fake)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Interfaces[0].CounterBits != 0 {
		t.Fatal("lossy floating-point value accepted as64-bit counter")
	}
}
func TestWalkBoundsAndSecretErrorSanitization(t *testing.T) {
	c := configFixture()
	fake := mibFixture()
	fake.tables[ifTable] = append(fake.tables[ifTable], fake.tables[ifTable][0])
	if _, err := CollectWithTransport(context.Background(), c, fake); err == nil {
		t.Fatal("duplicate table instance accepted")
	}
	fake = mibFixture()
	fake.walkErr = errors.New("authentication failed for private-auth-secret")
	_, err := CollectWithTransport(context.Background(), c, fake)
	if err == nil || strings.Contains(err.Error(), "private-auth-secret") {
		t.Fatal("transport secret leaked")
	}
	fake = mibFixture()
	fake.tables[ifTable][0].Name = ".1.3.6.1.4.1.9999"
	if _, err = CollectWithTransport(context.Background(), c, fake); err == nil {
		t.Fatal("walk escaped fixed subtree")
	}
	value, _ := sensorValue(8, 9, -2, 1234)
	if value == nil || *value != 1234 {
		t.Fatal("negative precision incorrectly multiplied sensor value")
	}
}
func TestPrivateConfigAndExplicitV2OptIn(t *testing.T) {
	c := configFixture()
	data, _ := json.Marshal(c)
	path := filepath.Join(t.TempDir(), "snmp.json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPrivateConfig(path); err == nil {
		t.Fatal("world-readable credentials accepted")
	}
	_ = os.Chmod(path, 0600)
	loaded, err := ReadPrivateConfig(path)
	if err != nil || loaded.Username != c.Username {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "secret-link")
	_ = os.Symlink(path, link)
	if _, err = ReadPrivateConfig(link); err == nil {
		t.Fatal("credential symlink followed")
	}
	c = Config{SwitchID: "sw", Target: "192.0.2.1", Version: "2c", Community: "private-community"}
	if err = ValidateConfig(c); err == nil {
		t.Fatal("v2c silently enabled")
	}
	c.AllowV2c = true
	if err = ValidateConfig(c); err != nil {
		t.Fatal(err)
	}
	c = configFixture()
	c.AuthProtocol = "MD5"
	if err = ValidateConfig(c); err == nil {
		t.Fatal("weak MD5 profile accepted")
	}
}

func TestBinaryEngineIdentityIsNotLossilyUTF8Encoded(t *testing.T) {
	one := &realTransport{client: &gosnmp.GoSNMP{SecurityParameters: &gosnmp.UsmSecurityParameters{AuthoritativeEngineID: string([]byte{0x80, 0, 0xFE}), AuthoritativeEngineBoots: 1}}}
	two := &realTransport{client: &gosnmp.GoSNMP{SecurityParameters: &gosnmp.UsmSecurityParameters{AuthoritativeEngineID: string([]byte{0x81, 0, 0xFF}), AuthoritativeEngineBoots: 1}}}
	if one.EngineIdentity() == two.EngineIdentity() || one.EngineEpoch() == two.EngineEpoch() {
		t.Fatal("different binary engineIDs collapsed through UTF-8 replacement")
	}
	stable := one.EngineIdentity()
	one.client.SecurityParameters.(*gosnmp.UsmSecurityParameters).AuthoritativeEngineBoots = 2
	if one.EngineIdentity() != stable {
		t.Fatal("ordinary reboot changed stable engine identity")
	}
}
