package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	consolemeta "github.com/moedove/ixp-manager/internal/console"
)

const consoleFixtureJSON = `{"tables":{
"cust":[{"id":1,"name":"Console customer","autsys":64501,"status":1}],
"location":[{"id":1,"name":"Console location"}],
"cabinet":[{"id":1,"name":"Console rack","locationid":1}],
"vendor":[{"id":1,"name":"Console vendor"}],
"console_server":[{"id":1,"name":"Console A","hostname":"BÜCHER.Example","cabinet_id":1,"vendor_id":1,"model":"Terminal server","serialNumber":"SER-01","active":1,"notes":"Original server notes\nsecond line"}],
"consoleserverconnection":[{"id":1,"console_server_id":1,"custid":1,"description":"Original endpoint description","port":"01","speed":115200,"parity":2,"stopbits":2,"flowcontrol":2,"autobaud":1,"notes":"Original connection notes"}]
}}`

func consoleFixture(t *testing.T) legacyExport { return decodeSample(t, consoleFixtureJSON) }
func TestConsoleMigrationPreservesMetadataAndTypedSerial(t *testing.T) {
	result, err := convert(consoleFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("complete known console source warned: %v", result.Warnings)
	}
	server := result.Resources["console-servers"][0]
	for key, want := range map[string]any{"id": "legacy-console-servers-1", "name": "Console A", "hostname": "xn--bcher-kva.example", "locationId": "legacy-locations-1", "rackId": "legacy-racks-1", "vendorId": "legacy-vendors-1", "model": "Terminal server", "serialNumber": "SER-01", "active": true, "notes": "Original server notes\nsecond line"} {
		if server[key] != want {
			t.Errorf("server %s=%v want%v", key, server[key], want)
		}
	}
	var original map[string]any
	if err = json.Unmarshal([]byte(server["legacyConsole"].(string)), &original); err != nil || original["hostname"] != "BÜCHER.Example" {
		t.Fatal("IDNA source spelling lost")
	}
	connection := result.Resources["console-connections"][0]
	for key, want := range map[string]any{"id": "legacy-console-connections-1", "consoleServerId": "legacy-console-servers-1", "memberId": "legacy-members-1", "description": "Original endpoint description", "portLabel": "01", "baudRate": int64(115200), "parity": "odd", "stopBits": int64(2), "flowControl": "rts-cts", "autobaud": true, "notes": "Original connection notes"} {
		if connection[key] != want {
			t.Errorf("connection %s=%v want%v", key, connection[key], want)
		}
	}
	if _, present := connection["port"]; present {
		t.Fatal("text port was recast as a numeric transport port")
	}
	var out, log bytes.Buffer
	if err := run([]string{"-strict"}, strings.NewReader(consoleFixtureJSON), &out, &log); err != nil || log.Len() != 0 {
		t.Fatalf("strict known conversion: %v %s", err, log.String())
	}
}
func TestConsoleAllLegacyBaudRatesAndEnums(t *testing.T) {
	source := consoleFixture(t)
	source.Tables["consoleserverconnection"] = nil
	for index, rate := range consolemeta.BaudRates {
		source.Tables["consoleserverconnection"] = append(source.Tables["consoleserverconnection"], row{"id": json.Number(fmt.Sprint(index + 1)), "console_server_id": json.Number("1"), "custid": json.Number("1"), "port": fmt.Sprintf("baud-%d", rate), "speed": json.Number(fmt.Sprint(rate)), "parity": json.Number(fmt.Sprint(index%3 + 1)), "stopbits": json.Number(fmt.Sprint(index%2 + 1)), "flowcontrol": json.Number(fmt.Sprint(index%3 + 1)), "autobaud": index%2 == 0})
	}
	result, err := convert(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Resources["console-connections"]) != 13 || len(result.Warnings) != 0 {
		t.Fatal("legacy serial choices were lost")
	}
	for index, record := range result.Resources["console-connections"] {
		if record["baudRate"] != int64(consolemeta.BaudRates[index]) || record["parity"] != []string{"even", "odd", "none"}[index%3] || record["flowControl"] != []string{"none", "rts-cts", "xon-xoff"}[index%3] {
			t.Fatal("wrong legacy enum mapping")
		}
	}
}
func TestConsoleUnknownSerialAndRemovedSwitchRemainSourceOnly(t *testing.T) {
	source := consoleFixture(t)
	r := source.Tables["consoleserverconnection"][0]
	for key, value := range map[string]any{"speed": json.Number("999"), "parity": json.Number("9"), "stopbits": json.Number("7"), "flowcontrol": json.Number("8"), "autobaud": "unknown", "switchid": json.Number("27")} {
		r[key] = value
	}
	result, err := convert(source)
	if err != nil {
		t.Fatal(err)
	}
	record := result.Resources["console-connections"][0]
	for _, key := range []string{"baudRate", "parity", "stopBits", "flowControl", "autobaud", "switchId", "deviceId"} {
		if _, present := record[key]; present {
			t.Fatalf("invented current state %s", key)
		}
	}
	var legacy map[string]any
	if err = json.Unmarshal([]byte(record["legacySerial"].(string)), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy["speed"] != float64(999) || legacy["switchid"] != float64(27) || legacy["autobaud"] != "unknown" || len(result.Warnings) < 6 {
		t.Fatal("unknown raw serial metadata not retained/reported")
	}
	input, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "refused.json")
	var out, log bytes.Buffer
	if err = run([]string{"-strict", "-output", path}, bytes.NewReader(input), &out, &log); err == nil {
		t.Fatal("strict import silently accepted unknown serial settings")
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) || out.Len() != 0 {
		t.Fatal("failed strict conversion published output")
	}
}
func TestConsoleUnknownActiveAndNullAutobaudAreNotInvented(t *testing.T) {
	for _, value := range []any{nil, json.Number("2"), "unknown"} {
		source := consoleFixture(t)
		source.Tables["console_server"][0]["active"] = value
		source.Tables["consoleserverconnection"][0]["autobaud"] = nil
		delete(source.Tables["consoleserverconnection"][0], "speed")
		result, err := convert(source)
		if err != nil {
			t.Fatal(err)
		}
		server := result.Resources["console-servers"][0]
		connection := result.Resources["console-connections"][0]
		if server["active"] != false || len(result.Warnings) == 0 || server["legacyConsole"] == nil {
			t.Fatal("unknown activity was guessed active")
		}
		if _, exists := connection["autobaud"]; exists {
			t.Fatal("null autobaud became false")
		}
		if _, exists := connection["baudRate"]; exists {
			t.Fatal("missing speed became a default")
		}
	}
	source := consoleFixture(t)
	delete(source.Tables["console_server"][0], "active")
	result, err := convert(source)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	_ = json.Unmarshal([]byte(result.Resources["console-servers"][0]["legacyConsole"].(string)), &raw)
	if raw["activeMissing"] != true {
		t.Fatal("absent active flag not distinguished from source null")
	}
}
func TestConsolePortLabelsPreserveZerosAndRejectAmbiguousRelations(t *testing.T) {
	source := consoleFixture(t)
	source.Tables["consoleserverconnection"] = append(source.Tables["consoleserverconnection"], row{"id": json.Number("2"), "console_server_id": json.Number("1"), "custid": json.Number("1"), "port": "1"})
	result, err := convert(source)
	if err != nil || len(result.Resources["console-connections"]) != 2 {
		t.Fatal("01 and1 were collapsed")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*legacyExport)
	}{
		{"label case fold", func(s *legacyExport) {
			s.Tables["consoleserverconnection"][0]["port"] = "Straße"
			s.Tables["consoleserverconnection"] = append(s.Tables["consoleserverconnection"], row{"id": json.Number("2"), "console_server_id": json.Number("1"), "custid": json.Number("1"), "port": "STRASSE"})
		}},
		{"label NFC", func(s *legacyExport) {
			s.Tables["consoleserverconnection"][0]["port"] = "é"
			s.Tables["consoleserverconnection"] = append(s.Tables["consoleserverconnection"], row{"id": json.Number("2"), "console_server_id": json.Number("1"), "custid": json.Number("1"), "port": "e\u0301"})
		}},
		{"surrounding whitespace", func(s *legacyExport) { s.Tables["consoleserverconnection"][0]["port"] = " 01" }},
		{"control character", func(s *legacyExport) { s.Tables["consoleserverconnection"][0]["port"] = "01\n" }},
		{"missing cabinet", func(s *legacyExport) { s.Tables["console_server"][0]["cabinet_id"] = json.Number("999") }},
		{"missing vendor", func(s *legacyExport) { s.Tables["console_server"][0]["vendor_id"] = json.Number("999") }},
		{"missing console", func(s *legacyExport) {
			s.Tables["consoleserverconnection"][0]["console_server_id"] = json.Number("999")
		}},
		{"tag is not a customer", func(s *legacyExport) {
			s.Tables["cust_tag"] = []row{{"id": json.Number("99"), "tag": "not a customer"}}
			s.Tables["consoleserverconnection"][0]["custid"] = json.Number("99")
		}},
		{"source scalar object", func(s *legacyExport) {
			s.Tables["consoleserverconnection"][0]["speed"] = map[string]any{"fake": "speed"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := consoleFixture(t)
			tc.mutate(&source)
			if _, err := convert(source); err == nil {
				t.Fatal("unsafe console conversion accepted")
			}
		})
	}
}
func TestConsoleLiteralIPAndLongNotesPreserved(t *testing.T) {
	source := consoleFixture(t)
	source.Tables["console_server"][0]["hostname"] = "2001:db8::20"
	source.Tables["console_server"][0]["notes"] = strings.Repeat("n", 65536)
	result, err := convert(source)
	if err != nil {
		t.Fatal(err)
	}
	server := result.Resources["console-servers"][0]
	if server["managementIp"] != "2001:db8::20" || server["hostname"] != nil || len(server["notes"].(string)) != 65536 {
		t.Fatal("literal IP or bounded long notes changed")
	}
	source.Tables["console_server"][0]["notes"] = strings.Repeat("n", 65537)
	if _, err = convert(source); err == nil {
		t.Fatal("oversized notes silently truncated")
	}
}
