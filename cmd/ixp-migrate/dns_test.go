package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/app"
)

func disabledFamilyFixture(t *testing.T) legacyExport {
	source := decodeSample(t, sample)
	vli := source.Tables["vlaninterface"][0]
	vli["ipv6enabled"] = json.Number("0")
	vli["ipv4hostname"] = "LIVE.Example.NET"
	vli["ipv6hostname"] = "DISABLED.Example.NET."
	vli["ipv4canping"] = json.Number("0")
	vli["ipv6canping"] = json.Number("1")
	vli["busyhost"] = json.Number("1")
	source.Tables["switch"][0]["hostname"] = "SW.Example.NET"
	return source
}

func TestDisabledFamilyKeepsOwnerPTRAndMonitoringPreferences(t *testing.T) {
	source := disabledFamilyFixture(t)
	result, err := convert(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 0 {
		t.Fatal("supported disabled-family allocation warned", result.Warnings)
	}
	connection := result.Resources["connections"][0]
	if connection["ipv6"] != nil || connection["dnsName6"] != nil || connection["dnsName4"] != "live.example.net." || connection["canPing4"] != false || connection["canPing6"] != true || connection["monitorBusy"] != true {
		t.Fatal("family intent or monitoring/DNS metadata changed")
	}
	pools := result.Resources["ip-addresses"]
	if len(pools) != 1 || pools[0]["address"] != "2001:db8::10" || pools[0]["memberId"] != "legacy-members-1" || pools[0]["dnsName"] != "disabled.example.net." {
		t.Fatal("disabled but assigned address became free or lost its PTR", pools)
	}
	if result.Resources["switches"][0]["dnsName"] != "sw.example.net." {
		t.Fatal("switch hostname lost")
	}
	raw, _ := json.Marshal(source)
	var out, log bytes.Buffer
	if err := run([]string{"-strict"}, bytes.NewReader(raw), &out, &log); err != nil || out.Len() == 0 || log.Len() != 0 {
		t.Fatal("complete disabled-family conversion did not pass strict mode", err, log.String())
	}
	source.Tables["vlaninterface"][0]["ipv6monitorrcbgp"] = json.Number("1")
	source.Tables["vlaninterface"][0]["bgpmd5secret"] = "NeverCopyLegacySecret"
	result, err = convert(source)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(result)
	if bytes.Contains(raw, []byte("NeverCopyLegacySecret")) || !strings.Contains(strings.Join(result.Warnings, "\n"), "ipv6monitorrcbgp") {
		t.Fatal("secret copied or unsupported peer-monitor mapping silently claimed")
	}
}

func TestAmbiguousReferencedAddressesNeverBecomeFreePools(t *testing.T) {
	for _, change := range []func(*legacyExport){
		func(s *legacyExport) {
			duplicate := row{}
			for k, v := range s.Tables["vlaninterface"][0] {
				duplicate[k] = v
			}
			duplicate["id"] = json.Number("2")
			s.Tables["vlaninterface"] = append(s.Tables["vlaninterface"], duplicate)
		},
		func(s *legacyExport) { s.Tables["virtualinterface"][0]["custid"] = json.Number("999") },
		func(s *legacyExport) { s.Tables["ipv6address"][0]["vlanid"] = json.Number("999") },
		func(s *legacyExport) { s.Tables["vlan"][0]["number"] = json.Number("0") },
		func(s *legacyExport) { s.Tables["ipv6address"][0]["address"] = "2001:db8::wrong" },
	} {
		source := disabledFamilyFixture(t)
		change(&source)
		result, err := convert(source)
		if err == nil {
			t.Fatal("unresolved assigned ownership did not stop conversion")
		}
		if len(result.Warnings) == 0 {
			t.Fatal("unresolvable assignment silently accepted")
		}
		for _, pool := range result.Resources["ip-addresses"] {
			if pool["address"] == "2001:db8::10" {
				t.Fatal("unresolvable address was emitted as usable/free pool")
			}
		}
		raw, _ := json.Marshal(source)
		var out, log bytes.Buffer
		if err := run([]string{"-strict"}, bytes.NewReader(raw), &out, &log); err == nil || out.Len() != 0 {
			t.Fatal("strict mode published unresolved allocation")
		}
		out.Reset()
		log.Reset()
		if err := run(nil, bytes.NewReader(raw), &out, &log); err == nil || out.Len() != 0 || log.Len() == 0 || !strings.Contains(err.Error(), "fix the source ownership") {
			t.Fatal("non-strict conversion exposed an unresolved allocation as implicitly free", err)
		}
	}
}

func TestInvalidLegacyDNSDoesNotReleaseKnownAssignment(t *testing.T) {
	source := disabledFamilyFixture(t)
	source.Tables["vlaninterface"][0]["ipv6hostname"] = "invalid;host.example"
	source.Tables["switch"][0]["hostname"] = "unsafe;switch.example"
	result, err := convert(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) == 0 || len(result.Resources["ip-addresses"]) != 1 {
		t.Fatal("optional bad DNS lost known allocation")
	}
	pool := result.Resources["ip-addresses"][0]
	if pool["memberId"] != "legacy-members-1" || pool["address"] != "2001:db8::10" || pool["dnsName"] != nil {
		t.Fatal("bad hostname erased ownership or escaped into output")
	}
	if len(result.Resources["switches"]) != 1 || result.Resources["switches"][0]["dnsName"] != nil {
		t.Fatal("optional bad switch DNS discarded unrelated inventory")
	}
	raw, _ := json.Marshal(source)
	var out, log bytes.Buffer
	if err := run([]string{"-strict"}, bytes.NewReader(raw), &out, &log); err == nil || out.Len() != 0 {
		t.Fatal("strict conversion accepted unresolved DNS metadata")
	}
}

func TestDisabledIPv4KeepsReservation(t *testing.T) {
	source := disabledFamilyFixture(t)
	source.Tables["vlaninterface"][0]["ipv4enabled"] = json.Number("0")
	source.Tables["vlaninterface"][0]["ipv6enabled"] = json.Number("1")
	result, err := convert(source)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 0 {
		t.Fatal(result.Warnings)
	}
	connection := result.Resources["connections"][0]
	pool := result.Resources["ip-addresses"][0]
	if connection["ipv4"] != nil || connection["dnsName4"] != nil || connection["ipv6"] != "2001:db8::10" || pool["address"] != "192.0.2.10" || pool["memberId"] != "legacy-members-1" || pool["dnsName"] != "live.example.net." {
		t.Fatal("IPv4 disabled assignment differs from IPv6 behavior")
	}
}

type migrationDNSClient struct {
	t      *testing.T
	a      *app.App
	cookie *http.Cookie
	csrf   string
}

func (c *migrationDNSClient) request(method, path string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	conditional := (method == "PUT" || method == "DELETE") && strings.HasPrefix(path, "/api/resources/") && !strings.HasPrefix(path, "/api/resources/provisioning-profiles")
	tag := ""
	if conditional {
		latest := c.request("GET", path, nil)
		if latest.Code == 200 {
			tag = latest.Header().Get("ETag")
		}
		if row, ok := body.(app.Record); ok {
			copy := app.Record{}
			for key, value := range row {
				if key != "_etag" {
					copy[key] = value
				}
			}
			body = copy
		}
	}
	var raw bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&raw).Encode(body); err != nil {
			c.t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, path, &raw)
	r.RemoteAddr = "127.0.0.1:10000"
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != nil {
		r.AddCookie(c.cookie)
	}
	r.Header.Set("X-CSRF-Token", c.csrf)
	if tag != "" {
		r.Header.Set("If-Match", tag)
	}
	w := httptest.NewRecorder()
	c.a.ServeHTTP(w, r)
	if cookies := w.Result().Cookies(); len(cookies) > 0 {
		c.cookie = cookies[0]
	}
	return w
}
func (c *migrationDNSClient) call(method, path string, body any) app.Record {
	c.t.Helper()
	w := c.request(method, path, body)
	if w.Code < 200 || w.Code >= 300 {
		c.t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
	}
	result := app.Record{}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		c.t.Fatal(err)
	}
	if csrf, ok := result["csrfToken"].(string); ok {
		c.csrf = csrf
	}
	return result
}
func (c *migrationDNSClient) render(profile app.Record) app.Record {
	c.t.Helper()
	revision := c.call("POST", "/api/provisioning", app.Record{"profileId": profile["id"]})
	return c.call("GET", "/api/provisioning/"+revision["id"].(string), nil)
}

func TestDisabledFamilyMigrationThroughRealImportPreservesPTRNotMonitoring(t *testing.T) {
	source := disabledFamilyFixture(t)
	raw, _ := json.Marshal(source)
	var out, log bytes.Buffer
	if err := run([]string{"-strict"}, bytes.NewReader(raw), &out, &log); err != nil {
		t.Fatal(err, log.String())
	}
	payload := app.Record{}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	server, err := app.New(app.Config{DataDir: t.TempDir(), SetupToken: "disabled-family-test", NamedCheckzone: "/nonexistent/not-used-for-preview"})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	c := &migrationDNSClient{t: t, a: server}
	c.call("POST", "/api/setup", app.Record{"name": "Migration admin", "email": "dns-migrate@example.net", "password": "synthetic-disabled-family-password", "ixpName": "Migration IX", "asn": 64500, "setupToken": "disabled-family-test"})
	c.call("POST", "/api/import", payload)
	connection := c.call("GET", "/api/resources/connections/legacy-connections-1", nil)
	if connection["ipv6"] != nil || connection["dnsName4"] != "live.example.net." || connection["canPing4"] != false || connection["monitorBusy"] != true {
		t.Fatal("real API import lost enabled-family preferences")
	}
	poolID := "legacy-ip-addresses-ipv6address-1"
	pool := c.call("GET", "/api/resources/ip-addresses/"+poolID, nil)
	if pool["memberId"] != "legacy-members-1" || pool["dnsName"] != "disabled.example.net." {
		t.Fatal("real API import lost disabled-family reservation")
	}
	occupancy := c.call("GET", "/api/ipam?vlanId=legacy-vlans-1", nil)
	reserved := false
	for _, raw := range occupancy["items"].([]any) {
		item := raw.(map[string]any)
		if item["address"] == "2001:db8::10" {
			reserved = item["state"] == "reserved" && item["memberId"] == "legacy-members-1"
		}
	}
	if !reserved {
		t.Fatal("assigned disabled address is exposed as free")
	}
	other := c.call("POST", "/api/resources/members", app.Record{"name": "Next owner", "asn": 64502, "status": "active"})
	if w := c.request("POST", "/api/ipam/allocate", app.Record{"vlanId": "legacy-vlans-1", "family": 6, "address": "2001:db8::10", "memberId": other["id"]}); w.Code != 409 {
		t.Fatal("disabled-family reservation was allocated to another member")
	}
	dns := c.call("POST", "/api/resources/provisioning-profiles", app.Record{"name": "Preserved reverse DNS", "dnsNewZone": true, "options": app.Record{"kind": "dns-arpa", "vlanId": "legacy-vlans-1", "families": []int{6}, "dns": app.Record{"prefix": "2001:db8::/64", "primaryNS": "ns1.example.net.", "responsibleMailbox": "hostmaster.example.net.", "nameServers": []string{"ns1.example.net."}}}})
	rendered := c.render(dns)
	if !strings.Contains(rendered["config"].(string), "IN PTR disabled.example.net.") || rendered["targetCount"] != float64(1) {
		t.Fatal("default inactive DNS coverage lost the retained PTR")
	}
	for _, kind := range []string{"nagios", "smokeping"} {
		profile := c.call("POST", "/api/resources/provisioning-profiles", app.Record{"name": kind, "options": app.Record{"kind": kind, "vlanId": "legacy-vlans-1", "families": []int{4, 6}}})
		if strings.Contains(c.render(profile)["config"].(string), "2001:db8::10") {
			t.Fatal("disabled-family DNS reservation became an active monitoring target", kind)
		}
	}
	// Full-record inventory updates must not silently carry a previous tenant's
	// hostname to a replacement owner; release clears it before reallocation.
	if w := c.request("PUT", "/api/resources/ip-addresses/"+poolID, app.Record{"name": pool["name"], "address": pool["address"], "vlanId": pool["vlanId"], "memberId": other["id"], "dnsName": pool["dnsName"]}); w.Code != 400 {
		t.Fatal("generic reassignment retained old-owner DNS")
	}
	c.call("POST", "/api/ipam/release", app.Record{"id": poolID, "expectedMemberId": "legacy-members-1"})
	released := c.call("GET", "/api/resources/ip-addresses/"+poolID, nil)
	if released["dnsName"] != nil || released["memberId"] != nil {
		t.Fatal("release retained private hostname or ownership")
	}
	c.call("POST", "/api/ipam/allocate", app.Record{"vlanId": "legacy-vlans-1", "family": 6, "address": "2001:db8::10", "memberId": other["id"]})
	reassigned := c.call("GET", "/api/resources/ip-addresses/"+poolID, nil)
	if reassigned["memberId"] != other["id"] || reassigned["dnsName"] != nil || strings.Contains(c.render(dns)["config"].(string), "disabled.example.net") {
		t.Fatal("new member inherited previous tenant's PTR")
	}
}
