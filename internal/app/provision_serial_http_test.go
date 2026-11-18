package app

import (
	"os"
	"strings"
	"testing"
)

// This explicitly selected test uses the real local syntax checker, never a
// simulated successful validation or a query to an authoritative DNS server.
func TestDNSRFC1982HTTPApprovalPersistenceRetentionAndRecreation(t *testing.T) {
	binary := os.Getenv("NAMED_CHECKZONE_BIN")
	if binary == "" {
		t.Skip("NAMED_CHECKZONE_BIN is required for the native zone-check branch")
	}
	c, dir, peer := credentialFixture(t)
	c.a.namedCheckzone = binary
	router := c.ok("GET", "/api/resources/routers/"+str(peer, "routerId"), nil)
	options := Record{"kind": "dns-arpa", "vlanId": router["vlanId"], "families": []int{4}, "dns": Record{"prefix": "192.0.2.0/24", "primaryNS": "ns1.example.net.", "responsibleMailbox": "hostmaster.example.net.", "nameServers": []string{"ns1.example.net."}, "nameSuffix": "ix.example.net."}}
	profile := c.ok("POST", "/api/resources/provisioning-profiles", Record{"name": "Wrapped zone", "options": options, "dnsSerialFloor": uint32(4294967293)})
	makeRevision := func(want uint32) Record {
		r := c.ok("POST", "/api/provisioning", Record{"profileId": profile["id"]})
		if r["dnsSerial"] != float64(want) {
			t.Fatalf("want serial %d, got %v", want, r["dnsSerial"])
		}
		return r
	}
	first := makeRevision(4294967294)
	firstPath := approveProvision(c, first)
	older := makeRevision(4294967295)
	olderPath := "/api/provisioning/" + str(older, "id")
	c.ok("POST", olderPath+"/validate", nil)
	wrapped := makeRevision(1)
	wrappedPath := approveProvision(c, wrapped)
	if w := c.request("POST", olderPath+"/approve", Record{"checksum": older["checksum"]}); w.Code != 409 {
		t.Fatal("pre-wrap draft approved after wrapped head")
	}
	if w := c.request("GET", firstPath+"/config", nil); w.Code != 409 {
		t.Fatal("pre-wrap approved artifact remained downloadable")
	}
	download := c.request("GET", wrappedPath+"/config", nil)
	if download.Code != 200 || !strings.Contains(download.Body.String(), "  1 ; serial") {
		t.Fatal("wrapped zone did not pass native validation/download")
	}
	draft := makeRevision(2)
	if w := c.request("GET", wrappedPath+"/config", nil); w.Code != 200 || w.Body.String() != download.Body.String() {
		t.Fatal("issuing a draft changed approved artifact")
	}
	profilePath := "/api/resources/provisioning-profiles/" + str(profile, "id")
	bad := Record{"name": "Wrong baseline", "options": options, "dnsSerialFloor": uint32(2147483650), "expectedRevision": "stale"}
	if w := c.request("PUT", profilePath, bad); w.Code != 409 {
		t.Fatal("stale baseline edit bypassed CAS")
	}
	bad["expectedRevision"] = profile["revision"]
	if w := c.request("PUT", profilePath, bad); w.Code != 400 {
		t.Fatal("undefined baseline comparison was accepted")
	}
	unchanged := c.ok("GET", profilePath, nil)
	if unchanged["revision"] != profile["revision"] {
		t.Fatal("rejected baseline edit mutated profile")
	}
	exported := c.ok("GET", "/api/export", nil)["resources"].(map[string]any)[provisionProfileKind].([]any)[0].(map[string]any)
	if exported["dnsSerialFloor"] != float64(2) || exported["dnsNewZone"] != false {
		t.Fatal("resource export rolled back wrapped issued head")
	}
	if err := c.a.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(Config{DataDir: dir, NamedCheckzone: binary})
	if err != nil {
		t.Fatal("wrapped persisted ledger rejected", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	c = loginClient(t, reopened, Record{"email": "admin@example.net", "password": "very-long-test-password"})
	if w := c.request("GET", wrappedPath+"/config", nil); w.Code != 200 || w.Body.String() != download.Body.String() {
		t.Fatal("approved download changed after reopening")
	}
	c.ok("DELETE", profilePath, Record{"expectedRevision": profile["revision"]})
	for _, r := range []Record{first, older, wrapped, draft} {
		path := "/api/provisioning/" + str(r, "id")
		body := Record{"checksum": r["checksum"], "acknowledge": true, "reason": "Retire this deleted profile without resetting its zone ledger"}
		c.ok("POST", path+"/prune", body)
		c.ok("DELETE", path, body)
	}
	profile = c.ok("POST", "/api/resources/provisioning-profiles", Record{"name": "Recreated known zone", "options": options, "dnsNewZone": true})
	next := makeRevision(3)
	approveProvision(c, next)
	state := reopened.store.Snapshot()
	if state.ProvisionSerials[serialTestZone] != 3 || state.ProvisionApprovedSerials[serialTestZone] != 3 || len(state.ProvisionRevisions) != 1 {
		t.Fatal("retention/profile recreation reset the persistent ledger")
	}
	if w := c.request("GET", wrappedPath+"/config", nil); w.Code != 404 {
		t.Fatal("removed old version revived")
	}
	if err := reopened.store.Update(func(s *State) error { s.ProvisionApprovedSerials[serialTestZone] = uint32(2147483651); return nil }); err != nil {
		t.Fatal(err)
	}
	if w := c.request("GET", "/api/export", nil); w.Code != 409 || strings.Contains(w.Body.String(), `"resources"`) {
		t.Fatal("undefined ledger leaked an export with an invented floor")
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if invalid, err := New(Config{DataDir: dir}); err == nil {
		invalid.Close()
		t.Fatal("startup accepted undefined approved/issued serial ordering")
	}
}
