package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `{"tables":{
"cust":[{"id":1,"name":"Example member","autsys":64496,"status":1,"peeringemail":"noc@example.net","corpwww":"https://example.net"}],
"location":[{"id":1,"name":"Example DC","country":"ZZ","city":"Example","address":"Example street"}],
"cabinet":[{"id":1,"name":"Rack A","locationid":1,"height":42}],
"switch":[{"id":1,"name":"sw-a","cabinetid":1,"model":"Example","ipv4addr":"192.0.2.2"}],
"switchport":[{"id":1,"name":"eth1","switchid":1,"ifHighSpeed":10000,"active":1}],
"vlan":[{"id":1,"name":"Peering LAN","number":100}],
"networkinfo":[{"id":1,"vlanid":1,"protocol":4,"network":"192.0.2.0","masklen":24},{"id":2,"vlanid":1,"protocol":6,"network":"2001:db8::","masklen":64}],
"contact":[{"id":1,"name":"NOC","email":"noc@example.net","phone":"","custid":1}],
"virtualinterface":[{"id":1,"name":"Member uplink","custid":1}],
"physicalinterface":[{"id":1,"virtualinterfaceid":1,"switchportid":1,"speed":10000,"status":1}],
"vlaninterface":[{"id":1,"virtualinterfaceid":1,"vlanid":1,"ipv4enabled":1,"ipv6enabled":1,"ipv4addressid":1,"ipv6addressid":1}],
"ipv4address":[{"id":1,"vlanid":1,"address":"192.0.2.10"}],
"ipv6address":[{"id":1,"vlanid":1,"address":"2001:db8::10"}]
}}`

func decodeSample(t *testing.T, s string) legacyExport {
	t.Helper()
	var value legacyExport
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	if err := d.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestRelationsAndDualStack(t *testing.T) {
	result, err := convert(decodeSample(t, sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Resources) != 8 {
		t.Fatalf("unexpected resource families: %v", result.Resources)
	}
	got := result.Resources["connections"][0]
	for key, want := range map[string]any{"id": "legacy-connections-1", "memberId": "legacy-members-1", "portId": "legacy-ports-1", "vlanId": "legacy-vlans-1", "ipv4": "192.0.2.10", "ipv6": "2001:db8::10", "status": "active", "speed": int64(10000)} {
		if got[key] != want {
			t.Errorf("%s: got %v want %v", key, got[key], want)
		}
	}
	if result.Resources["switches"][0]["locationId"] != "legacy-locations-1" {
		t.Error("switch location not resolved through cabinet")
	}
}

func TestMissingLAGPortIsReportedNotFlattened(t *testing.T) {
	source := decodeSample(t, sample)
	source.Tables["physicalinterface"] = append(source.Tables["physicalinterface"], row{"id": json.Number("2"), "virtualinterfaceid": json.Number("1")})
	result, err := convert(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Resources["connections"]) != 0 {
		t.Fatal("LAG silently flattened")
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "missing or duplicate converted physical port") {
		t.Fatal("missing LAG member was not reported")
	}
}

func TestNoSecretsAndNoSilentUnsupportedFields(t *testing.T) {
	source := decodeSample(t, sample)
	source.Tables["switch"][0]["snmppasswd"] = "sensitive-value"
	source.Tables["users"] = []row{{"password": "another-secret"}}
	// Both the primary peering address and distinct NOC address must survive.
	source.Tables["cust"][0]["nocemail"] = "unconverted@example.net"
	source.Tables["cust"] = append(source.Tables["cust"], row{"id": json.Number("2"), "name": "Another member", "autsys": json.Number("64497"), "status": json.Number("1"), "nocemail": "converted@example.net"})
	result, err := convert(source)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(result)
	if strings.Contains(string(b), "sensitive-value") || strings.Contains(string(b), "another-secret") {
		t.Fatal("secret value leaked into output")
	}
	if result.Resources["members"][0]["nocEmail"] != "unconverted@example.net" || result.Resources["members"][1]["nocEmail"] != "converted@example.net" {
		t.Fatal("NOC contact information lost")
	}
	for _, field := range []string{"snmppasswd", "table users"} {
		if !strings.Contains(strings.Join(result.Warnings, "\n"), field) {
			t.Errorf("missing warning for %s", field)
		}
	}
}

func TestDuplicateAndPreciseIDs(t *testing.T) {
	source := decodeSample(t, `{"tables":{"location":[{"id":9007199254740993,"name":"A"}]}}`)
	result, err := convert(source)
	if err != nil {
		t.Fatal(err)
	}
	if result.Resources["locations"][0]["id"] != "legacy-locations-9007199254740993" {
		t.Fatal("ID precision lost")
	}
	source.Tables["location"] = append(source.Tables["location"], source.Tables["location"][0])
	if _, err = convert(source); err == nil {
		t.Fatal("duplicate ID accepted")
	}
}

func TestStrictConversionProducesNoOutput(t *testing.T) {
	var out, log bytes.Buffer
	input := strings.Replace(sample, `"phone":""`, `"phone":"","pager":"unsupported"`, 1)
	err := run([]string{"-strict"}, strings.NewReader(input), &out, &log)
	if err == nil || out.Len() != 0 || !strings.Contains(log.String(), "fields not migrated: pager") {
		t.Fatalf("strict conversion: err=%v out=%s log=%s", err, out.String(), log.String())
	}
}

func TestFileIsPrivateAndNeverOverwritten(t *testing.T) {
	output := filepath.Join(t.TempDir(), "import.json")
	input := `{"tables":{"location":[{"id":1,"name":"A"}]}}`
	var out, log bytes.Buffer
	if err := run([]string{"-output", output}, strings.NewReader(input), &out, &log); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions %o", info.Mode().Perm())
	}
	before, _ := os.ReadFile(output)
	if err = run([]string{"-output", output}, strings.NewReader(input), &out, &log); err == nil {
		t.Fatal("existing file overwritten")
	}
	after, _ := os.ReadFile(output)
	if !bytes.Equal(before, after) {
		t.Fatal("output changed")
	}
}

func TestRejectTrailingDocument(t *testing.T) {
	var out, log bytes.Buffer
	if err := run(nil, strings.NewReader(`{"tables":{}} {}`), &out, &log); err == nil || out.Len() != 0 {
		t.Fatal("trailing document accepted")
	}
}

func TestCompleteSinglePortStrictConversion(t *testing.T) {
	var out, log bytes.Buffer
	if err := run([]string{"-strict"}, strings.NewReader(sample), &out, &log); err != nil {
		t.Fatal(err)
	}
	if out.Len() == 0 || log.Len() != 0 {
		t.Fatalf("complete mapped source unexpectedly warned: %s", log.String())
	}
}
func TestLAGAndMultipleVLANsPreserved(t *testing.T) {
	data, err := os.ReadFile("../../tools/migrate/example-legacy-lag.json")
	if err != nil {
		t.Fatal(err)
	}
	result, err := convert(decodeSample(t, string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("supported LAG warned: %v", result.Warnings)
	}
	connections := result.Resources["connections"]
	if len(connections) != 2 {
		t.Fatal("lost VLAN attachment")
	}
	for _, c := range connections {
		ids, ok := c["portIds"].([]string)
		if !ok || len(ids) != 2 || ids[0] != "legacy-ports-1" || ids[1] != "legacy-ports-2" {
			t.Fatalf("LAG flattened: %v", c)
		}
		if _, exists := c["portId"]; exists {
			t.Fatal("both single and aggregate port fields emitted")
		}
		if c["linkMode"] != "lag" || c["speed"] != int64(35000) || c["mtu"] != int64(9000) {
			t.Fatalf("logical bundle metadata lost: %v", c)
		}
	}
	if result.Resources["ports"][0]["speed"] != int64(10000) || result.Resources["ports"][1]["speed"] != int64(25000) {
		t.Fatal("physical speed was replaced with aggregate")
	}
	if connections[0]["ipv4"] != "192.0.2.10" || connections[0]["ipv6"] != "2001:db8::10" || connections[1]["ipv4"] != "198.51.100.10" {
		t.Fatal("logical addresses lost")
	}
	var out, log bytes.Buffer
	if err := run([]string{"-strict"}, bytes.NewReader(data), &out, &log); err != nil {
		t.Fatal(err)
	}
}
func TestSinglePortLAGModeAndUnknownCapacity(t *testing.T) {
	source := decodeSample(t, sample)
	source.Tables["virtualinterface"][0]["lag_framing"] = true
	delete(source.Tables["switchport"][0], "ifHighSpeed")
	delete(source.Tables["physicalinterface"][0], "speed")
	result, err := convert(source)
	if err != nil {
		t.Fatal(err)
	}
	c := result.Resources["connections"][0]
	if c["linkMode"] != "lag" || c["portId"] != "legacy-ports-1" {
		t.Fatal("single-member LAG framing lost")
	}
	if _, exists := c["speed"]; exists {
		t.Fatal("unknown aggregate capacity fabricated")
	}
}

func TestExtendedBusinessPrivacyAndCompositeKeys(t *testing.T) {
	raw, err := os.ReadFile("../../tools/migrate/example-legacy-business.json")
	if err != nil {
		t.Fatal(err)
	}
	source := decodeSample(t, string(raw))
	result, err := convert(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("complete supported fixture has warnings: %v", result.Warnings)
	}
	member := result.Resources["members"][0]
	if member["registeredName"] != "示例网络有限公司" || member["vatRate"] != 23.5 || member["billingEmail"] != "billing@example.net" {
		t.Fatal("company records lost")
	}
	if _, ok := result.Resources["members"][1]["asn"]; ok {
		t.Fatal("associate ASN fabricated")
	}
	if result.Resources["tags"][0]["internal"] != true || result.Resources["notes"][0]["visibility"] != "internal" || result.Resources["notes"][0]["body"] != "line1\nline2" {
		t.Fatal("privacy or line endings changed")
	}
	if len(result.Resources["ip-addresses"]) != 1 {
		t.Fatal("unused address pool lost")
	}
	if result.Resources["member-tags"][0]["id"] != "legacy-member-tags-1-1" {
		t.Fatal("pivot ID not deterministic")
	}
	if _, ok := source.Tables["cust_to_cust_tag"][0]["id"]; ok {
		t.Fatal("converter mutated source pivot")
	}
	duplicate := decodeSample(t, `{"tables":{"contact_to_group":[{"contact_id":1,"contact_group_id":2},{"contact_id":1,"contact_group_id":2}]}}`)
	if _, err = convert(duplicate); err == nil {
		t.Fatal("duplicate pivot accepted")
	}
	if _, err = convert(legacyExport{}); err == nil {
		t.Fatal("missing tables accepted")
	}
}

func TestInfrastructureAndMultipleNetworkPrefixesPreserved(t *testing.T) {
	raw, err := os.ReadFile("../../tools/migrate/example-legacy-fabrics.json")
	if err != nil {
		t.Fatal(err)
	}
	result, err := convert(decodeSample(t, string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) > 0 {
		t.Fatal(result.Warnings)
	}
	if len(result.Resources["infrastructures"]) != 2 || len(result.Resources["vlans"]) != 2 || len(result.Resources["connections"]) != 2 {
		t.Fatal("fabric topology lost")
	}
	if result.Resources["switches"][0]["infrastructureId"] != "legacy-infrastructures-1" {
		t.Fatal("switch binding missing")
	}
	if len(result.Resources["vlans"][0]["prefixes"].([]string)) != 1 {
		t.Fatal("secondary network lost")
	}
	if len(result.Resources["ip-addresses"]) != 1 {
		t.Fatal("secondary subnet free pool lost")
	}
}
