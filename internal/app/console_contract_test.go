package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type consoleContractIDs struct{ location, otherLocation, rack, member, vendor string }

func consoleContractFixture(t *testing.T) (*client, consoleContractIDs) {
	t.Helper()
	c, _ := setupClient(t)
	location := c.create("locations", Record{"name": "Console DC"})
	other := c.create("locations", Record{"name": "Other DC"})
	rack := c.create("racks", Record{"name": "Console rack", "locationId": location["id"]})
	member := c.create("members", Record{"name": "Console member", "asn": 64501, "status": "active"})
	vendor := c.create("vendors", Record{"name": "Console vendor"})
	return c, consoleContractIDs{str(location, "id"), str(other, "id"), str(rack, "id"), str(member, "id"), str(vendor, "id")}
}
func TestConsoleContractMetadataLabelsAndSerialParameters(t *testing.T) {
	c, id := consoleContractFixture(t)
	server := c.create("console-servers", Record{"name": "Console A", "locationId": id.location, "rackId": id.rack, "vendorId": id.vendor, "hostname": "BÜCHER.Example.", "model": "Terminal", "serialNumber": "SN-01", "notes": "staff inventory only"})
	if str(server, "hostname") != "xn--bcher-kva.example" || server["active"] != true {
		t.Fatal("hostname canonicalization/default active projection absent")
	}
	conn := c.create("console-connections", Record{"name": "Leading zero", "description": "Console endpoint", "consoleServerId": server["id"], "memberId": id.member, "portLabel": "01", "baudRate": 115200, "parity": "odd", "stopBits": 2, "flowControl": "rts-cts", "autobaud": true, "notes": "serial settings retained"})
	if str(conn, "portLabel") != "01" || number(conn, "baudRate") != 115200 || conn["autobaud"] != true || str(conn, "consoleServerName") != "Console A" || str(conn, "locationId") != id.location {
		t.Fatal("metadata or relation projection lost")
	}
	numeric := c.create("console-connections", Record{"name": "Numeric compatibility", "consoleServerId": server["id"], "memberId": id.member, "port": 1})
	if str(numeric, "portLabel") != "1" {
		t.Fatal("legacy numeric port not projected as text")
	}
	for _, key := range []string{"baudRate", "parity", "stopBits", "flowControl", "autobaud"} {
		if _, present := numeric[key]; present {
			t.Fatalf("missing serial field %s was defaulted", key)
		}
	}
	c.create("console-connections", Record{"name": "Unicode port", "consoleServerId": server["id"], "memberId": id.member, "portLabel": "Straße"})
	for _, fields := range []Record{{"portLabel": "STRASSE"}, {"port": 1, "portLabel": "01"}, {"portLabel": "bad\nlabel"}, {"portLabel": " 02"}, {"portLabel": "02", "baudRate": 333}, {"portLabel": "02", "parity": "mark"}, {"portLabel": "02", "stopBits": 3}, {"portLabel": "02", "flowControl": "unknown"}, {"portLabel": "02", "autobaud": "false"}} {
		body := Record{"name": "Rejected", "consoleServerId": server["id"], "memberId": id.member}
		for k, v := range fields {
			body[k] = v
		}
		if w := c.request("POST", "/api/resources/console-connections", body); w.Code != 400 {
			t.Fatalf("bad serial/port record accepted: %v: %d %s", fields, w.Code, w.Body.String())
		}
	}
	for _, fields := range []Record{{"name": "CONSOLE a", "locationId": id.location, "hostname": "other.example"}, {"name": "Rack mismatch", "locationId": id.otherLocation, "rackId": id.rack, "hostname": "rack.example"}, {"name": "No endpoint", "locationId": id.location}, {"name": "Invalid hostname", "locationId": id.location, "hostname": "https://console.example"}, {"name": "Bad active", "locationId": id.location, "hostname": "console.example", "active": nil}, {"name": "Credential injection", "locationId": id.location, "hostname": "console.example", "password": "not-a-console-credential-field"}} {
		if w := c.request("POST", "/api/resources/console-servers", fields); w.Code != 400 {
			t.Fatalf("invalid server accepted %d %s", w.Code, w.Body.String())
		}
	}
	second := c.create("console-servers", Record{"name": "IP-only", "locationId": id.location, "managementIp": "192.0.2.50", "active": false})
	c.create("console-connections", Record{"name": "Same label other server", "consoleServerId": second["id"], "memberId": id.member, "portLabel": "01"})
	listing := c.ok("GET", "/api/resources/console-connections?consoleServerId="+str(second, "id"), nil)["items"].([]any)
	if len(listing) != 1 {
		t.Fatal("server-scoped connection list returned unrelated rows")
	}
	if w := c.request("DELETE", "/api/resources/console-servers/"+str(server, "id"), nil); w.Code < 400 || !strings.Contains(w.Body.String(), "关联") {
		t.Fatal("referenced console deletion was not clearly rejected")
	}
	if number(c.ok("GET", "/api/resources/console-servers/"+str(server, "id"), nil), "connectionCount") != 3 {
		t.Fatal("server connection count is incorrect")
	}
}
func TestConsoleContractSourceEvidenceRemainsReadOnlyAndStaffScoped(t *testing.T) {
	c, id := consoleContractFixture(t)
	source := `{"speed":333,"parity":9,"switchid":17}`
	c.ok("POST", "/api/import", Record{"version": 1, "resources": Record{
		"console-servers":     []Record{{"id": "import-console", "name": "Imported console", "locationId": id.location, "hostname": "console.example", "active": false, "legacyConsole": `{"active":null,"activeMissing":true}`}},
		"console-connections": []Record{{"id": "import-console-connection", "name": "Imported serial", "consoleServerId": "import-console", "memberId": id.member, "portLabel": "e\u0301", "legacySerial": source}},
	}})
	current := c.ok("GET", "/api/resources/console-connections/import-console-connection", nil)
	if str(current, "portLabel") != "e\u0301" || str(current, "legacySerial") != source {
		t.Fatal("original label or source evidence rewritten")
	}
	if w := c.request("POST", "/api/resources/console-connections", Record{"name": "Canonical collision", "consoleServerId": "import-console", "memberId": id.member, "portLabel": "é"}); w.Code != 400 {
		t.Fatal("NFC-equivalent duplicate accepted")
	}
	changed := c.ok("PUT", "/api/resources/console-connections/import-console-connection", Record{"name": "Corrected current fields", "consoleServerId": "import-console", "memberId": id.member, "baudRate": 9600, "parity": "none", "autobaud": nil})
	if str(changed, "portLabel") != "e\u0301" || str(changed, "legacySerial") != source || number(changed, "baudRate") != 9600 {
		t.Fatal("current correction erased immutable source or port label")
	}
	if value, present := changed["autobaud"]; present && value != nil {
		t.Fatal("explicit unknown autobaud became false")
	}
	for _, provenance := range []any{`{"speed":9600}`, "", nil} {
		if w := c.request("PUT", "/api/resources/console-connections/import-console-connection", Record{"name": "Source rewrite", "consoleServerId": "import-console", "memberId": id.member, "legacySerial": provenance}); w.Code != 400 {
			t.Fatal("ordinary PUT changed source evidence")
		}
	}
	if w := c.request("POST", "/api/resources/console-servers", Record{"name": "Forged source", "locationId": id.location, "hostname": "forged.example", "legacyConsole": "{}"}); w.Code != 400 {
		t.Fatal("ordinary create accepted source-only metadata")
	}
	c.create("users", Record{"name": "Member operator", "email": "console-member@example.test", "password": "console-member-long-password", "role": "member", "memberId": id.member})
	member := docLoginClient(t, c.a, "console-member@example.test", "console-member-long-password")
	for _, kind := range []string{"console-servers", "console-connections"} {
		w := member.request("GET", "/api/resources/"+kind, nil)
		if w.Code == 200 {
			var body Record
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body["items"].([]any)) != 0 {
				t.Fatal("member saw staff console inventory")
			}
		} else if w.Code != 403 && w.Code != 404 {
			t.Fatal(w.Code)
		}
	}
	if w := member.request("GET", "/api/resources/console-connections/import-console-connection", nil); w.Code != 403 && w.Code != 404 {
		t.Fatal("member read a known console source record")
	}
	for _, suffix := range []string{"/connect", "/terminal", "/credentials"} {
		if w := c.request("POST", "/api/console-servers/import-console"+suffix, Record{}); w.Code != 404 {
			t.Fatal("inventory created an unsolicited device/credential action")
		}
	}
}
func TestConsoleConverterOutputAcceptedByAPI(t *testing.T) {
	// A real CLI call proves source-table aliases and typed enum fields survive
	// JSON conversion before reaching the HTTP import boundary.
	source := Record{"tables": Record{
		"cust":     []Record{{"id": 1, "name": "Source member", "autsys": 64501, "status": 1}},
		"location": []Record{{"id": 1, "name": "Source DC"}}, "cabinet": []Record{{"id": 1, "name": "Source rack", "locationid": 1}},
		"console_server":          []Record{{"id": 1, "cabinet_id": 1, "name": "Source console", "hostname": "BÜCHER.example", "active": nil}},
		"consoleserverconnection": []Record{{"id": 1, "console_server_id": 1, "custid": 1, "description": "Source port", "port": "001", "speed": 230400, "parity": 3, "stopbits": 1, "flowcontrol": 3, "autobaud": nil, "switchid": 99}},
	}}
	data, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(t.TempDir(), "console.json")
	if err = os.WriteFile(input, data, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "run", "./cmd/ixp-migrate", "--input", input)
	command.Dir = "../.."
	raw, err := command.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			t.Fatalf("converter failed: %v %s", err, exit.Stderr)
		}
		t.Fatal(err)
	}
	var payload Record
	if err = json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	c, _ := setupClient(t)
	c.ok("POST", "/api/import", payload)
	server := c.ok("GET", "/api/resources/console-servers/legacy-console-servers-1", nil)
	if str(server, "hostname") != "xn--bcher-kva.example" || server["active"] != false {
		t.Fatal("converted console identity/activity changed")
	}
	conn := c.ok("GET", "/api/resources/console-connections/legacy-console-connections-1", nil)
	if str(conn, "memberId") != "legacy-members-1" || str(conn, "portLabel") != "001" || number(conn, "baudRate") != 230400 || str(conn, "flowControl") != "xon-xoff" {
		t.Fatal("converted serial contract changed")
	}
	exported := c.ok("GET", "/api/export", nil)
	encoded, _ := json.Marshal(exported)
	if !bytes.Contains(encoded, []byte(`"portLabel":"001"`)) || !bytes.Contains(encoded, []byte(`"legacySerial"`)) {
		t.Fatal("resource export lost label or source evidence")
	}
	before := c.a.store.Snapshot()
	if w := c.request("POST", "/api/import", payload); w.Code != 400 {
		t.Fatal("repeat import overwrote console identities")
	}
	after := c.a.store.Snapshot()
	if len(before.Resources["console-connections"]) != len(after.Resources["console-connections"]) {
		t.Fatal("rejected console import changed records")
	}
}
