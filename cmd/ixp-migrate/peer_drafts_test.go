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

func peerDraftFixture(t *testing.T) legacyExport {
	t.Helper()
	s := decodeSample(t, sample)
	s.Tables["cust"][0]["type"] = json.Number("1")
	s.Tables["cust"][0]["datejoin"] = "2020-01-01"
	s.Tables["cust"][0]["dateleave"] = nil
	s.Tables["cust"][0]["maxprefixes"] = json.Number("100")
	s.Tables["cust"][0]["maxprefixesv6"] = json.Number("200")
	vli := s.Tables["vlaninterface"][0]
	vli["rsclient"] = true
	vli["ipv4maxbgpprefix"] = json.Number("30")
	vli["ipv6maxbgpprefix"] = nil
	vli["ipv4monitorrcbgp"], vli["ipv6monitorrcbgp"] = false, false
	vli["ipv4bgpmd5secret"], vli["ipv6bgpmd5secret"] = "DO_NOT_COPY_V4_MD5", "DO_NOT_COPY_V6_MD5"
	for i, spec := range []struct{ platform, family, role, template string }{
		{"6", "4", "1", "api/v4/router/server/bird2/standard"},
		{"1", "6", "1", "api/v4/router/server/bird/standard"},
		{"1", "4", "2", "api/v4/router/collector/bird/standard"},
		{"6", "6", "2", "api/v4/router/collector/bird2/standard"},
	} {
		n := strconv.Itoa(i + 1)
		address := "192.0.2." + n
		if spec.family == "6" {
			address = "2001:db8::" + n
		}
		s.Tables["routers"] = append(s.Tables["routers"], row{"id": json.Number(strconv.Itoa(501 + i)), "vlan_id": json.Number("1"), "software": spec.platform, "protocol": spec.family, "type": spec.role, "name": "Reviewed source router " + n, "router_id": "192.0.2." + strconv.Itoa(100+i), "peering_ip": address, "asn": json.Number("64510"), "quarantine": false, "rpki": false, "bgp_lc": false, "rfc1997_passthru": false, "skip_md5": false, "template": spec.template, "api": "https://user:DO_NOT_COPY_ROUTER_API_SECRET@router.example.test"})
	}
	return s
}
func peerDraftTestOptions() peerDraftOptions { return peerDraftOptions{true, "split", "2026-11-18"} }
func peerDraftSecretsAbsent(t *testing.T, raw []byte) {
	t.Helper()
	for _, secret := range []string{"DO_NOT_COPY_V4_MD5", "DO_NOT_COPY_V6_MD5", "DO_NOT_COPY_ROUTER_API_SECRET", "DO_NOT_COPY_UNKNOWN_TEMPLATE"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("source credential/template value exposed")
		}
	}
}

func TestPeerDraftsOptInAndDeclaredLimitPrecedence(t *testing.T) {
	s := peerDraftFixture(t)
	original, _ := json.Marshal(s)
	plain, err := convert(s)
	if err != nil {
		t.Fatal(err)
	}
	defaultOptions, err := convertWithPeerDrafts(s, peerDraftOptions{})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(plain)
	b, _ := json.Marshal(defaultOptions)
	if !bytes.Equal(a, b) || len(plain.Resources["peers"]) != 0 {
		t.Fatal("default inventory changed or guessed sessions")
	}
	result, err := convertWithPeerDrafts(s, peerDraftTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Resources["peers"]) != 3 {
		t.Fatal("wrong stock-template candidate scope", result.Resources["peers"], result.Warnings)
	}
	wanted := map[string]int64{"legacy-routers-501": 30, "legacy-routers-502": 200, "legacy-routers-503": 30}
	for _, p := range result.Resources["peers"] {
		if p["enabled"] != false || p["memberId"] != "legacy-members-1" || p["asn"] != int64(64496) || p["maxPrefixes"] != wanted[p["routerId"].(string)] || !reflect.DeepEqual(p["allowedPrefixes"], []string{}) {
			t.Fatal("candidate guessed authorization or changed source limit", p)
		}
		if p["routerId"] == "legacy-routers-502" && p["address"] != "2001:db8::10" {
			t.Fatal("IPv6 endpoint lost")
		}
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "collector has no effective import limit") {
		t.Fatal("BIRD2 collector was treated as bounded RS")
	}
	after, _ := json.Marshal(s)
	if !bytes.Equal(original, after) {
		t.Fatal("source mutated")
	}
	raw, _ := json.Marshal(result)
	peerDraftSecretsAbsent(t, raw)
	shared := peerDraftFixture(t)
	delete(shared.Tables["cust"][0], "maxprefixesv6")
	for _, vli := range shared.Tables["vlaninterface"] {
		delete(vli, "ipv4maxbgpprefix")
		delete(vli, "ipv6maxbgpprefix")
		vli["maxbgpprefix"] = json.Number("45")
	}
	legacy, err := convertWithPeerDrafts(shared, peerDraftOptions{true, "shared", "2026-11-18"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range legacy.Resources["peers"] {
		if p["maxPrefixes"] != int64(45) {
			t.Fatal("shared pre-migration source limit changed", p)
		}
	}
}

func TestPeerDraftsPreserveRoleSelectionDatesAndSourceIDs(t *testing.T) {
	s := peerDraftFixture(t)
	s.Tables["vlaninterface"][0]["rsclient"] = false
	s.Tables["vlaninterface"][0]["id"] = json.Number("9007199254740993")
	s.Tables["cust"][0]["status"] = json.Number("2")
	s.Tables["cust"][0]["datejoin"] = "2020-01-01 00:00:00"
	s.Tables["cust"][0]["dateleave"] = "2026-11-18"
	r, err := convertWithPeerDrafts(s, peerDraftTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Resources["peers"]) != 1 {
		t.Fatal("collector incorrectly required rsclient/monitor preference or inclusive date/status lost", r.Resources["peers"], r.Warnings)
	}
	p := r.Resources["peers"][0]
	if p["id"] != "legacy-peer-draft-r503-vli9007199254740993-v4" || p["routerId"] != "legacy-routers-503" || p["enabled"] != false {
		t.Fatal(p)
	}
	if r.Resources["members"][0]["status"] != "pending" {
		t.Fatal("source status changed to enable candidate")
	}
}

func TestPeerDraftsRefuseUnknownLimitsOwnershipAndSelections(t *testing.T) {
	cases := map[string]func(*legacyExport){
		"absent-local-limits": func(s *legacyExport) {
			delete(s.Tables["vlaninterface"][0], "ipv4maxbgpprefix")
			delete(s.Tables["vlaninterface"][0], "ipv6maxbgpprefix")
		},
		"external-defaults": func(s *legacyExport) {
			s.Tables["vlaninterface"][0]["ipv4maxbgpprefix"] = nil
			s.Tables["cust"][0]["maxprefixes"] = nil
			s.Tables["cust"][0]["maxprefixesv6"] = nil
		},
		"absent-member-limits": func(s *legacyExport) {
			s.Tables["vlaninterface"][0]["ipv4maxbgpprefix"] = nil
			delete(s.Tables["cust"][0], "maxprefixes")
			delete(s.Tables["cust"][0], "maxprefixesv6")
		},
		"overlarge-local-no-fallback": func(s *legacyExport) {
			s.Tables["vlaninterface"][0]["ipv4maxbgpprefix"] = json.Number("10000001")
			s.Tables["vlaninterface"][0]["ipv6maxbgpprefix"] = json.Number("10000001")
		},
		"boolean-limits": func(s *legacyExport) {
			s.Tables["vlaninterface"][0]["ipv4maxbgpprefix"] = true
			s.Tables["vlaninterface"][0]["ipv6maxbgpprefix"] = true
		},
		"already-left":            func(s *legacyExport) { s.Tables["cust"][0]["dateleave"] = "2026-11-17" },
		"not-yet-joined":          func(s *legacyExport) { s.Tables["cust"][0]["datejoin"] = "2026-11-19" },
		"missing-end-date":        func(s *legacyExport) { delete(s.Tables["cust"][0], "dateleave") },
		"missing-start-date":      func(s *legacyExport) { delete(s.Tables["cust"][0], "datejoin") },
		"suspended":               func(s *legacyExport) { s.Tables["cust"][0]["status"] = json.Number("3") },
		"associate":               func(s *legacyExport) { s.Tables["cust"][0]["type"] = json.Number("2") },
		"self-ASN":                func(s *legacyExport) { s.Tables["cust"][0]["autsys"] = json.Number("64510") },
		"unknown-type":            func(s *legacyExport) { delete(s.Tables["cust"][0], "type") },
		"disconnected":            func(s *legacyExport) { s.Tables["physicalinterface"][0]["status"] = json.Number("2") },
		"boolean-physical-status": func(s *legacyExport) { s.Tables["physicalinterface"][0]["status"] = true },
		"unknown-template": func(s *legacyExport) {
			for _, r := range s.Tables["routers"] {
				r["template"] = "DO_NOT_COPY_UNKNOWN_TEMPLATE"
			}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			s := peerDraftFixture(t)
			mutate(&s)
			r, err := convertWithPeerDrafts(s, peerDraftTestOptions())
			if err != nil || len(r.Resources["peers"]) != 0 {
				t.Fatal("unproven draft created", r.Resources["peers"], err)
			}
			raw, _ := json.Marshal(r)
			peerDraftSecretsAbsent(t, raw)
		})
	}
	for _, schema := range []string{"shared", "split"} {
		s := peerDraftFixture(t)
		if schema == "split" {
			s.Tables["vlaninterface"][0]["maxbgpprefix"] = json.Number("30")
		}
		if _, err := convertWithPeerDrafts(s, peerDraftOptions{true, schema, "2026-11-18"}); err == nil {
			t.Fatal("mixed source-era columns accepted", schema)
		}
	}
	for _, args := range [][]string{{"--peer-drafts"}, {"--peer-limit-schema", "split"}, {"--peer-drafts", "--peer-limit-schema", "split", "--peer-as-of", "bad-date"}, {"--peer-drafts", "--peer-limit-schema", "split", "--peer-as-of", "2026-11-18", "--mailing-archive"}} {
		var out, log bytes.Buffer
		if err := run(args, strings.NewReader(sample), &out, &log); err == nil || out.Len() != 0 {
			t.Fatal("implicit/mixed authorization accepted", args, err)
		}
	}
}

func TestPeerDraftsRejectDuplicateAddressOwnersIncludingDisabledReferences(t *testing.T) {
	s := peerDraftFixture(t)
	member := copyCoreRow(s.Tables["cust"][0])
	member["id"] = json.Number("2")
	member["autsys"] = json.Number("64497")
	member["name"] = "Other owner"
	s.Tables["cust"] = append(s.Tables["cust"], member)
	port := copyCoreRow(s.Tables["switchport"][0])
	port["id"] = json.Number("2")
	port["name"] = "eth2"
	s.Tables["switchport"] = append(s.Tables["switchport"], port)
	s.Tables["virtualinterface"] = append(s.Tables["virtualinterface"], row{"id": json.Number("2"), "custid": json.Number("2"), "name": "Other connection"})
	physical := copyCoreRow(s.Tables["physicalinterface"][0])
	physical["id"], physical["virtualinterfaceid"], physical["switchportid"] = json.Number("2"), json.Number("2"), json.Number("2")
	s.Tables["physicalinterface"] = append(s.Tables["physicalinterface"], physical)
	address := copyCoreRow(s.Tables["ipv4address"][0])
	address["id"] = json.Number("2")
	s.Tables["ipv4address"] = append(s.Tables["ipv4address"], address)
	vli := copyCoreRow(s.Tables["vlaninterface"][0])
	vli["id"], vli["virtualinterfaceid"], vli["ipv4addressid"] = json.Number("2"), json.Number("2"), json.Number("2")
	vli["ipv4enabled"], vli["ipv6enabled"] = false, false
	delete(vli, "ipv6addressid")
	s.Tables["vlaninterface"] = append(s.Tables["vlaninterface"], vli)
	raw, _ := json.Marshal(s)
	var out, log bytes.Buffer
	err := run([]string{"--peer-drafts", "--peer-limit-schema", "split", "--peer-as-of", "2026-11-18"}, bytes.NewReader(raw), &out, &log)
	if err == nil || out.Len() != 0 || !strings.Contains(err.Error(), "ownership") {
		t.Fatal("disabled competing owner was ignored", err, log.String())
	}
	peerDraftSecretsAbsent(t, log.Bytes())
}

func TestPeerDraftsImportThroughGoRemainDisabledAndCredentialFree(t *testing.T) {
	s := peerDraftFixture(t)
	raw, _ := json.Marshal(s)
	var out, log bytes.Buffer
	args := []string{"--peer-drafts", "--peer-limit-schema", "split", "--peer-as-of", "2026-11-18"}
	if err := run(args, bytes.NewReader(raw), &out, &log); err != nil {
		t.Fatal(err, log.String())
	}
	peerDraftSecretsAbsent(t, out.Bytes())
	peerDraftSecretsAbsent(t, log.Bytes())
	dir := t.TempDir()
	a, err := app.New(app.Config{DataDir: dir, BirdBinary: "/nonexistent/bird"})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	c := &migrationDNSClient{t: t, a: a}
	c.call("POST", "/api/setup", app.Record{"name": "Peer draft admin", "email": "peer-draft@example.test", "password": "peer-draft-fixture-password", "ixpName": "Peer draft fixture", "asn": 64599, "setupToken": a.BootstrapToken()})
	payload := app.Record{}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	c.call("POST", "/api/import", payload)
	stored, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	peerDraftSecretsAbsent(t, stored)
	var state app.State
	if err := json.Unmarshal(stored, &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Resources["peers"]) != 3 || len(state.PeerCredentials) != 0 || len(state.Deployments) != 0 || len(state.ProvisionRevisions) != 0 {
		t.Fatal("draft import created authority or lost candidates")
	}
	for _, p := range state.Resources["peers"] {
		if p["enabled"] != false || len(p["allowedPrefixes"].([]any)) != 0 {
			t.Fatal("peer became enabled or authorized", p)
		}
		if p["routerId"] == "legacy-routers-502" && p["maxPrefixes"] != float64(200) {
			t.Fatal("IPv6 effective limit changed", p)
		}
	}
	var strictOut, strictLog bytes.Buffer
	if err := run(append(args, "--strict"), bytes.NewReader(raw), &strictOut, &strictLog); err == nil || strictOut.Len() != 0 {
		t.Fatal("review-required drafts silently passed strict")
	}
}
