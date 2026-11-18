package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func legacyCustomerConvertedFixture(t *testing.T) Record {
	t.Helper()
	f, err := os.Open("../../tools/migrate/example-legacy.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var source struct {
		Tables map[string][]Record `json:"tables"`
	}
	decoder := json.NewDecoder(f)
	decoder.UseNumber()
	if err := decoder.Decode(&source); err != nil {
		t.Fatal(err)
	}
	const largeID int64 = 9007199254740993
	source.Tables["cust"][0]["id"] = largeID
	source.Tables["contact"][0]["custid"] = largeID
	source.Tables["virtualinterface"][0]["custid"] = largeID
	source.Tables["vlaninterface"][0]["ipv4enabled"] = false
	source.Tables["vlaninterface"][0]["rsclient"] = true
	delete(source.Tables, "ipv4address")

	// Router 1 is required by the old selector even though its platform has
	// no executable Go router representation. VLAN 2 is a separate tenant.
	source.Tables["routers"] = []Record{
		{"id": 1, "vlan_id": 1, "protocol": 4, "software": "unknown"},
		{"id": 2, "vlan_id": 2, "protocol": 4, "software": "unknown"},
		{"id": 3, "vlan_id": 2, "protocol": 4, "software": "unknown"},
		{"id": 4, "vlan_id": 2, "protocol": 6, "software": "unknown"},
	}
	source.Tables["cust"] = append(source.Tables["cust"], Record{"id": 2, "name": "Álpha", "autsys": 64497, "status": 3})
	source.Tables["vlan"] = append(source.Tables["vlan"], Record{"id": 2, "number": 200, "name": "Other fabric"})
	source.Tables["networkinfo"] = append(source.Tables["networkinfo"],
		Record{"id": 3, "vlanid": 2, "protocol": 4, "network": "198.51.100.0", "masklen": 24},
		Record{"id": 4, "vlanid": 2, "protocol": 6, "network": "2001:db8:2::", "masklen": 64})
	source.Tables["switchport"] = append(source.Tables["switchport"], Record{"id": 2, "name": "eth2", "switchid": 1, "ifHighSpeed": 10000, "active": 1})
	source.Tables["virtualinterface"] = append(source.Tables["virtualinterface"], Record{"id": 2, "name": "Other link", "custid": 2})
	source.Tables["physicalinterface"] = append(source.Tables["physicalinterface"], Record{"id": 2, "virtualinterfaceid": 2, "switchportid": 2, "speed": 10000, "status": 1})
	source.Tables["vlaninterface"] = append(source.Tables["vlaninterface"], Record{
		"id": 2, "virtualinterfaceid": 2, "vlanid": 2, "ipv4enabled": true, "ipv4addressid": 2,
		"ipv6enabled": true, "ipv6addressid": 2, "rsclient": 1,
	})
	source.Tables["ipv4address"] = []Record{{"id": 2, "vlanid": 2, "address": "198.51.100.20"}}
	source.Tables["ipv6address"] = append(source.Tables["ipv6address"], Record{"id": 2, "vlanid": 2, "address": "2001:db8:2::20"})
	input, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "source.json")
	if err := os.WriteFile(file, input, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	convert := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "run", "./cmd/ixp-migrate", "-input", file)
	convert.Dir = "../.."
	output, err := convert.Output()
	if err != nil {
		if failure, ok := err.(*exec.ExitError); ok {
			t.Fatalf("legacy export conversion: %v: %s", err, failure.Stderr)
		}
		t.Fatal(err)
	}
	var payload Record
	if err := json.Unmarshal(output, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func requireLegacyCustomerRows(t *testing.T, w *httptest.ResponseRecorder, want ...legacySelectedCustomer) {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("legacy selection returned %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Customers []legacySelectedCustomer `json:"listCustomers"`
	}
	d := json.NewDecoder(w.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(&response); err != nil || response.Customers == nil {
		t.Fatalf("legacy list response: %+v, %v", response, err)
	}
	if len(want) == 0 {
		want = []legacySelectedCustomer{}
	}
	if !reflect.DeepEqual(response.Customers, want) {
		t.Fatalf("legacy rows %v, want %v", response.Customers, want)
	}
}

func legacyCustomerRawRequest(c *client, media, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, legacyCustomerSelectionPath, strings.NewReader(body))
	r.Header.Set("Content-Type", media)
	r.Header.Set("X-CSRF-Token", c.csrf)
	r.AddCookie(c.cookie)
	w := httptest.NewRecorder()
	c.a.ServeHTTP(w, r)
	return w
}

func TestLegacyCustomerSelectionFromExportThroughHTTP(t *testing.T) {
	c, _ := setupClient(t)
	payload := legacyCustomerConvertedFixture(t)
	c.ok(http.MethodPost, "/api/import", payload)
	s := c.a.store.Snapshot()
	if len(s.Resources["routers"]) != 0 || len(s.Resources["peers"]) != 0 {
		t.Fatal("source inventory evidence created an executable routing target")
	}
	precise := legacySelectedCustomer{9007199254740993, "Example member"}
	other := legacySelectedCustomer{2, "Álpha"}
	selectRows := func(query string, body any) *httptest.ResponseRecorder {
		return c.request(http.MethodPost, legacyCustomerSelectionPath+query, body)
	}
	requireLegacyCustomerRows(t, selectRows("", nil), other, precise)
	requireLegacyCustomerRows(t, selectRows("", Record{"vlanid": "1", "protocol": nil}), precise)
	requireLegacyCustomerRows(t, selectRows("?protocol=4", nil), other)
	requireLegacyCustomerRows(t, selectRows("?protocol=6", Record{"protocol": "4"}), other)
	requireLegacyCustomerRows(t, selectRows("?protocol=6", nil), other)
	requireLegacyCustomerRows(t, selectRows("", Record{"vlanid": 1, "protocol": 4}))
	requireLegacyCustomerRows(t, selectRows("", Record{"vlanid": 1, "protocol": 6}))
	requireLegacyCustomerRows(t, legacyCustomerRawRequest(c, "application/x-www-form-urlencoded", "vlanid=2&protocol=4"), other)

	for _, spec := range []struct {
		query, media, body string
		status             int
	}{
		{"?vlanid=100", "", "", 404}, // VLAN tag is not the historical database ID.
		{"?protocol=5", "", "", 404},
		{"?protocol=4&protocol=6", "", "", 400},
		{"?protocol[]=4", "", "", 400},
		{"", "application/json", `{"protocol":4,"protocol":6}`, 400},
		{"", "application/json", `{"protocol":{} }`, 404},
		{"", "application/json", `{"protocol":4}{}`, 400},
		{"", "application/x-www-form-urlencoded", "vlanid=1&vlanid=2", 400},
	} {
		var w *httptest.ResponseRecorder
		if spec.media == "" {
			w = selectRows(spec.query, nil)
		} else {
			w = legacyCustomerRawRequest(c, spec.media, spec.body)
		}
		if w.Code != spec.status {
			t.Errorf("%s %q: got %d, want %d: %s", spec.query, spec.body, w.Code, spec.status, w.Body.String())
		}
	}

	const password = "long-legacy-customer-test-password"
	c.create("users", Record{"name": "Member", "email": "customer-selector@example.net", "password": password, "role": "member", "memberId": "legacy-members-9007199254740993"})
	member := docLoginClient(t, c.a, "customer-selector@example.net", password)
	requireLegacyCustomerRows(t, member.request(http.MethodPost, legacyCustomerSelectionPath, nil), precise)
	if w := member.request(http.MethodPost, legacyCustomerSelectionPath+"?vlanid=2", nil); w.Code != 404 {
		t.Fatalf("member could distinguish another tenant's VLAN: %d %s", w.Code, w.Body.String())
	}
	memberToken := str(member.ok(http.MethodPost, "/api/account/tokens", Record{"name": "customer read", "scopes": []string{"resources:read"}, "currentPassword": password}), "token")
	requireLegacyCustomerRows(t, bearerRequest(c.a, memberToken, http.MethodPost, legacyCustomerSelectionPath, Record{}), precise)
	wrongScope := str(c.ok(http.MethodPost, "/api/account/tokens", Record{"name": "customer export scope", "scopes": []string{"exports:read"}, "currentPassword": "very-long-test-password"}), "token")
	if w := bearerRequest(c.a, wrongScope, http.MethodPost, legacyCustomerSelectionPath, Record{}); w.Code != 403 {
		t.Fatalf("bearer without inventory read: %d %s", w.Code, w.Body.String())
	}
	anonymous := &client{a: c.a, t: t}
	if w := anonymous.request(http.MethodPost, legacyCustomerSelectionPath, Record{}); w.Code != 401 {
		t.Fatalf("anonymous legacy selection: %d %s", w.Code, w.Body.String())
	}
	if w := c.request(http.MethodGet, legacyCustomerSelectionPath, nil); w.Code != 405 || w.Header().Get("Allow") != "POST" {
		t.Fatalf("legacy selection method: %d %s", w.Code, w.Body.String())
	}
	if w := (&client{a: c.a, t: t, cookie: c.cookie}).request(http.MethodPost, legacyCustomerSelectionPath, nil); w.Code != 403 {
		t.Fatalf("missing CSRF accepted: %d %s", w.Code, w.Body.String())
	}

	c.ok(http.MethodPut, "/api/resources/vlans/legacy-vlans-2", Record{"name": "Renamed VLAN", "tag": 200, "ipv4Prefix": "198.51.100.0/24", "ipv6Prefix": "2001:db8:2::/64"})
	requireLegacyCustomerRows(t, selectRows("?vlanid=2&protocol=4", nil), other)

	const connection = "/api/resources/connections/legacy-connections-1"
	update := Record{
		"name": "Renamed uplink", "memberId": "legacy-members-9007199254740993",
		"portId": "legacy-ports-1", "vlanId": "legacy-vlans-1", "ipv6": "2001:db8::10",
		"speed": 10000, "status": "active",
	}
	if row := c.ok(http.MethodPut, connection, update); row["legacyRSClientIPv6"] != true {
		t.Fatalf("ordinary update dropped source evidence: %v", row)
	}
	update["legacyRSClientIPv6"] = false
	if w := c.request(http.MethodPut, connection, update); w.Code != 400 {
		t.Fatalf("ordinary update could rewrite source evidence: %d %s", w.Code, w.Body.String())
	}
	update["legacyRSClientIPv6"] = true
	delete(update, "ipv6")
	if row := c.ok(http.MethodPut, connection, update); row["legacyRSClientIPv6"] != nil {
		t.Fatalf("removed address left its eligibility active: %v", row)
	}
	requireLegacyCustomerRows(t, selectRows("", nil), other)
}

func TestLegacyCustomerEvidenceRejectsNativeOrReassignedClaims(t *testing.T) {
	native := Record{"id": "new-connection", "memberId": "legacy-members-1", "vlanId": "legacy-vlans-1", "ipv4": "192.0.2.10", "legacyRSClientIPv4": true}
	if validateLegacyCustomerEvidence("connections", native) == nil || guardLegacyCustomerResourceMutation("connections", http.MethodPost, nil, native) == nil {
		t.Fatal("native connection accepted fictional source evidence")
	}
	old := Record{"id": "legacy-connections-1", "memberId": "legacy-members-1", "vlanId": "legacy-vlans-1", "ipv4": "192.0.2.10", "legacyRSClientIPv4": true}
	if err := validateLegacyCustomerEvidence("connections", old); err != nil {
		t.Fatal(err)
	}
	for _, change := range []Record{{"memberId": "legacy-members-2"}, {"vlanId": "legacy-vlans-2"}} {
		reassigned := Record{}
		for key, value := range old {
			reassigned[key] = value
		}
		for key, value := range change {
			reassigned[key] = value
		}
		if err := guardLegacyCustomerResourceMutation("connections", http.MethodPut, old, reassigned); err != nil || reassigned["legacyRSClientIPv4"] != nil {
			t.Fatalf("assignment kept old route-selection evidence: %v %v", reassigned, err)
		}
	}
}
