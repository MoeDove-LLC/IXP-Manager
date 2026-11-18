package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/app"
)

func legacyRouterFixture(t *testing.T) legacyExport {
	t.Helper()
	s := decodeSample(t, `{"tables":{"vlan":[{"id":1,"name":"Peering LAN","number":100}],"networkinfo":[{"id":1,"vlanid":1,"protocol":4,"network":"192.0.2.0","masklen":24},{"id":2,"vlanid":1,"protocol":6,"network":"2001:db8::","masklen":64}]}}`)
	for i, p := range []struct{ software, family, kind string }{{"1", "4", "1"}, {"1", "6", "1"}, {"6", "4", "1"}, {"7", "6", "1"}, {"3", "4", "1"}, {"4", "6", "1"}, {"2", "4", "1"}, {"5", "6", "1"}, {"6", "4", "2"}, {"3", "6", "2"}} {
		ordinal := strconv.Itoa(i + 1)
		id := json.Number(ordinal)
		if i == 0 {
			id = json.Number("9007199254740993")
		}
		address := "192.0.2." + ordinal
		if p.family == "6" {
			address = "2001:db8::" + ordinal
		}
		r := row{"id": id, "vlan_id": json.Number("1"), "software": p.software, "protocol": json.Number(p.family), "type": json.Number(p.kind), "name": "Explicit router " + id.String(), "router_id": "192.0.2." + strconv.Itoa(128+i), "peering_ip": address, "asn": json.Number("64510"), "quarantine": json.Number("0"), "rpki": false, "bgp_lc": "0", "skip_md5": false, "pause_updates": true, "rfc1997_passthru": json.Number("0"), "pair_id": json.Number("2"), "handle": "source-handle", "template": "IGNORED_TEMPLATE_SECRET", "mgmt_host": "IGNORED_MANAGEMENT_SECRET", "api": "https://user:IGNORED_API_PASSWORD@router.example.test/path?token=IGNORED_API_TOKEN", "md5password": "IGNORED_MD5_SECRET", "created_at": "1999-01-01 00:00:00"}
		if i == 1 {
			delete(r, "rfc1997_passthru")
		}
		if i == 3 {
			r["rfc1997_passthru"] = true
		}
		s.Tables["routers"] = append(s.Tables["routers"], r)
	}
	return s
}
func findConvertedRouter(result export, id string) row {
	for _, r := range result.Resources["routers"] {
		if r["id"] == id {
			return r
		}
	}
	return nil
}
func assertRouterSecretsAbsent(t *testing.T, output []byte) {
	t.Helper()
	for _, secret := range []string{"IGNORED_TEMPLATE_SECRET", "IGNORED_MANAGEMENT_SECRET", "IGNORED_API_PASSWORD", "IGNORED_API_TOKEN", "IGNORED_MD5_SECRET"} {
		if bytes.Contains(output, []byte(secret)) {
			t.Fatal("ignored source value escaped into output/logs", secret)
		}
	}
}

func TestLegacyRouterInventoryPreservesDeclaredPlatformRoleAndFamily(t *testing.T) {
	s := legacyRouterFixture(t)
	before, _ := json.Marshal(s)
	result, err := convert(s)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(s)
	if !bytes.Equal(before, after) {
		t.Fatal("source mutated")
	}
	if len(result.Resources["routers"]) != 10 || len(result.Resources["peers"]) != 0 {
		t.Fatal("routers lost or sessions invented", result.Resources["routers"], result.Warnings)
	}
	wantPlatforms := []string{"bird1", "bird1", "bird2", "bird3", "frr", "openbgpd", "quagga", "cisco", "bird2", "frr"}
	for i, r := range result.Resources["routers"] {
		if r["platform"] != wantPlatforms[i] || r["asn"] != int64(64510) || r["routerId"] != "192.0.2."+strconv.Itoa(128+i) || r["vlanId"] != "legacy-vlans-1" || r["rpkiPolicy"] != "off" {
			t.Fatal("declared identity changed", r)
		}
		wantRole := "route-server"
		if i >= 8 {
			wantRole = "route-collector"
		}
		if r["role"] != wantRole || (r["localIpv4"] == nil) == (r["localIpv6"] == nil) {
			t.Fatal("role or AF merged/coerced", r)
		}
		for _, field := range []string{"pairId", "enabled", "password", "md5password", "api", "template", "createdAt", "approvedAt", "managementIp"} {
			if r[field] != nil {
				t.Fatal("unreviewed authority/source configuration imported", field)
			}
		}
	}
	v4, v6 := findConvertedRouter(result, "legacy-routers-9007199254740993"), findConvertedRouter(result, "legacy-routers-2")
	if v4 == nil || v6 == nil || v4["localIpv4"] != "192.0.2.1" || v6["localIpv6"] != "2001:db8::2" || v4["id"] == v6["id"] {
		t.Fatal("int64 identity or separate instances lost")
	}
	if _, present := v6["rfc1997Passthrough"]; present {
		t.Fatal("absent RFC1997 evidence invented false")
	}
	if findConvertedRouter(result, "legacy-routers-6")["rfc1997Passthrough"] != false || findConvertedRouter(result, "legacy-routers-4")["rfc1997Passthrough"] != true {
		t.Fatal("known false/true control changed")
	}
	raw, _ := json.Marshal(result)
	assertRouterSecretsAbsent(t, raw)
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "inert inventory") || !strings.Contains(strings.Join(result.Warnings, "\n"), "evidence is absent") {
		t.Fatal("review boundaries were silent", result.Warnings)
	}
}

func TestLegacyRouterMissingOrUnsupportedEvidenceIsNotGuessed(t *testing.T) {
	cases := map[string]func(row){
		"missing-ipv6-router-id":           func(r row) { r["protocol"] = "6"; r["peering_ip"] = "2001:db8::3"; delete(r, "router_id") },
		"IPv6-is-not-router-id":            func(r row) { r["router_id"] = "2001:db8::3" },
		"no-ASN-settings-fallback":         func(r row) { delete(r, "asn") },
		"boolean-ASN":                      func(r row) { r["asn"] = true },
		"reserved-ASN":                     func(r row) { r["asn"] = "23456" },
		"Other-not-Junos":                  func(r row) { r["software"] = "99"; r["software_version"] = "Junos" },
		"unknown-software":                 func(r row) { r["software"] = "IGNORED_API_PASSWORD" },
		"Other-role":                       func(r row) { r["type"] = "99" },
		"AS112-no-service-input":           func(r row) { r["type"] = "3"; r["asn"] = "112" },
		"Quagga-collector-not-RS":          func(r row) { r["software"] = "2"; r["type"] = "2" },
		"Cisco-collector-not-RS":           func(r row) { r["software"] = "5"; r["type"] = "2" },
		"wrong-family":                     func(r row) { r["protocol"] = "6" },
		"missing-family":                   func(r row) { delete(r, "protocol") },
		"wrong-vlan":                       func(r row) { r["vlan_id"] = "999" },
		"boolean-vlan":                     func(r row) { r["vlan_id"] = true },
		"outside-prefix":                   func(r row) { r["peering_ip"] = "198.51.100.3" },
		"unknown-quarantine":               func(r row) { delete(r, "quarantine") },
		"enabled-quarantine":               func(r row) { r["quarantine"] = true },
		"enabled-rpki":                     func(r row) { r["rpki"] = true },
		"unknown-rpki":                     func(r row) { r["rpki"] = nil },
		"enabled-large-communities":        func(r row) { r["bgp_lc"] = true },
		"unsupported-explicit-passthrough": func(r row) { r["software"] = "3"; r["rfc1997_passthru"] = true },
		"invalid-source-flag":              func(r row) { r["skip_md5"] = "invalid" },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			s := legacyRouterFixture(t)
			candidate := s.Tables["routers"][2]
			s.Tables["routers"] = []row{candidate}
			change(candidate)
			result, err := convert(s)
			if err != nil || len(result.Resources["routers"]) != 0 || !strings.Contains(strings.Join(result.Warnings, "\n"), "router not converted") {
				t.Fatal("unsafe row converted or silently lost", result.Resources["routers"], result.Warnings, err)
			}
			raw, _ := json.Marshal(result)
			assertRouterSecretsAbsent(t, raw)
		})
	}
	s := legacyRouterFixture(t)
	s.Tables["networkinfo"] = s.Tables["networkinfo"][:1]
	result, err := convert(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range result.Resources["routers"] {
		if r["localIpv6"] != nil {
			t.Fatal("IPv6 prefix invented")
		}
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "no declared prefix") {
		t.Fatal(result.Warnings)
	}
}

func TestLegacyRouterAmbiguousAddressAndStrictSourceRefuseOutput(t *testing.T) {
	s := legacyRouterFixture(t)
	first := s.Tables["routers"][2]
	conflicting := copyCoreRow(first)
	conflicting["id"] = json.Number("999")
	conflicting["software"] = "99"
	s.Tables["routers"] = []row{first, conflicting}
	result, err := convert(s)
	if err != nil || len(result.Resources["routers"]) != 0 || !strings.Contains(strings.Join(result.Warnings, "\n"), "duplicate peering address") {
		t.Fatal("unsupported competing owner was ignored", result.Warnings, err)
	}
	s = legacyRouterFixture(t)
	raw, _ := json.Marshal(s)
	var out, log bytes.Buffer
	if err := run([]string{"--strict"}, bytes.NewReader(raw), &out, &log); err == nil || out.Len() != 0 {
		t.Fatal("inventory-only review warnings passed strict conversion")
	}
	assertRouterSecretsAbsent(t, log.Bytes())
	for _, input := range []string{
		strings.Replace(string(raw), `"router_id":"192.0.2.128"`, `"router_id":"192.0.2.128","router_id":"IGNORED_API_PASSWORD"`, 1),
		`{"tables":{"routers":[],"routers":[{"id":1}]}}`,
		`{"tables":{"routers":[{"id":1,"name":"\ud800"}]}}`,
	} {
		out.Reset()
		log.Reset()
		err := run(nil, strings.NewReader(input), &out, &log)
		if err == nil || out.Len() != 0 || strings.Contains(err.Error(), "IGNORED_API_PASSWORD") {
			t.Fatal("ambiguous source JSON produced output or leaked values", err)
		}
	}
}

func TestLegacyRouterConversionImportsIntoGoWithoutAuthority(t *testing.T) {
	source := legacyRouterFixture(t)
	raw, _ := json.Marshal(source)
	var output, log bytes.Buffer
	if err := run(nil, bytes.NewReader(raw), &output, &log); err != nil {
		t.Fatal(err)
	}
	assertRouterSecretsAbsent(t, output.Bytes())
	assertRouterSecretsAbsent(t, log.Bytes())
	dir := t.TempDir()
	a, err := app.New(app.Config{DataDir: dir, BirdBinary: "/nonexistent/bird"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	c := &migrationDNSClient{t: t, a: a}
	c.call("POST", "/api/setup", app.Record{"name": "Router migration admin", "email": "router-migrate@example.test", "password": "router-migration-password", "ixpName": "Independent setup ASN", "asn": 64599, "setupToken": a.BootstrapToken()})
	payload := app.Record{}
	if err = json.Unmarshal(output.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	c.call("POST", "/api/import", payload)
	stored, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	assertRouterSecretsAbsent(t, stored)
	var state app.State
	if err = json.Unmarshal(stored, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Resources["routers"]) != 10 || len(state.Resources["peers"]) != 0 || len(state.PeerCredentials) != 0 || len(state.Deployments) != 0 || len(state.ProvisionRevisions) != 0 {
		t.Fatal("inventory import created operational authority")
	}
	byID := map[string]app.Record{}
	for _, r := range state.Resources["routers"] {
		byID[r["id"].(string)] = r
		if r["asn"] != float64(64510) || r["createdAt"] == nil || strings.HasPrefix(r["createdAt"].(string), "1999") {
			t.Fatal("source ASN changed or old authority timestamp reused", r)
		}
	}
	if byID["legacy-routers-9007199254740993"]["platform"] != "bird1" || byID["legacy-routers-9007199254740993"]["localIpv4"] != "192.0.2.1" || byID["legacy-routers-2"]["localIpv6"] != "2001:db8::2" || byID["legacy-routers-2"]["localIpv4"] != nil {
		t.Fatal("actual import merged v4/v6 identity", byID)
	}
	for id, platform := range map[string]string{"legacy-routers-7": "quagga", "legacy-routers-8": "cisco"} {
		if byID[id]["platform"] != platform || byID[id]["role"] != "route-server" {
			t.Fatal("known inert platform identity lost")
		}
	}
	if !reflect.DeepEqual(byID["legacy-routers-6"]["rfc1997Passthrough"], false) {
		t.Fatal("explicit false became native default")
	}
}

func TestLegacyRouterIdentifierCollisionRespectsInfrastructure(t *testing.T) {
	for _, mode := range []string{"default-same-vlan", "default-other-vlan", "same-named-fabric", "different-fabric"} {
		t.Run(mode, func(t *testing.T) {
			s := legacyRouterFixture(t)
			s.Tables["routers"] = s.Tables["routers"][:3]
			s.Tables["routers"][1]["router_id"] = s.Tables["routers"][0]["router_id"]
			if mode != "default-same-vlan" {
				s.Tables["vlan"] = append(s.Tables["vlan"], row{"id": json.Number("2"), "name": "Other LAN", "number": json.Number("200")})
				s.Tables["networkinfo"] = append(s.Tables["networkinfo"], row{"id": json.Number("3"), "vlanid": json.Number("2"), "protocol": json.Number("6"), "network": "2001:db8:2::", "masklen": json.Number("64")})
				s.Tables["routers"][1]["vlan_id"] = json.Number("2")
				s.Tables["routers"][1]["peering_ip"] = "2001:db8:2::2"
			}
			if mode == "same-named-fabric" || mode == "different-fabric" {
				s.Tables["infrastructure"] = []row{{"id": json.Number("1"), "name": "Fabric A"}, {"id": json.Number("2"), "name": "Fabric B"}}
				s.Tables["vlan"][0]["infrastructureid"] = json.Number("1")
				s.Tables["vlan"][1]["infrastructureid"] = json.Number("1")
				if mode == "different-fabric" {
					s.Tables["vlan"][1]["infrastructureid"] = json.Number("2")
				}
			}
			before, _ := json.Marshal(s)
			result, err := convert(s)
			if err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(s)
			if !bytes.Equal(before, after) {
				t.Fatal("source router identifiers changed")
			}
			if mode == "different-fabric" {
				if len(result.Resources["routers"]) != 3 || result.Resources["routers"][0]["routerId"] != result.Resources["routers"][1]["routerId"] {
					t.Fatal("independent infrastructure IDs renumbered or excluded", result.Resources["routers"], result.Warnings)
				}
			} else if len(result.Resources["routers"]) != 1 || result.Resources["routers"][0]["id"] != "legacy-routers-3" || !strings.Contains(strings.Join(result.Warnings, "\n"), "duplicate router_id") {
				t.Fatal("colliding candidates retained, arbitrary winner selected or unrelated router lost", result.Resources["routers"], result.Warnings)
			}
		})
	}
}
