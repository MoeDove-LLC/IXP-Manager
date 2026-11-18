package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	external "github.com/moedove/ixp-manager/internal/integrations"
)

func externalSnapshotFixture() external.Snapshot {
	return external.Snapshot{Version: 1, Provider: "peeringdb", FetchedAt: time.Now().UTC(), Network: external.Network{ID: 101, ASN: 64501, Name: "Reviewed PeeringDB Network", Website: "https://network.example.test", IRRASSet: "AS-EXAMPLE", Status: "ok"}, NetIXLANs: []external.NetIXLAN{{ID: 201, NetID: 101, IXID: 301, IXLANID: 401, ASN: 64501, Name: "Public IX", IPv4: "192.0.2.10", IPv6: "2001:db8::10", Speed: 10000, Status: "ok"}}, Exchanges: []external.Exchange{}, Facilities: []external.Facility{}}
}
func importExternalFixture(t *testing.T, f monitorFixture, snapshot external.Snapshot) Record {
	t.Helper()
	document, checksum, err := external.Canonical(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return f.c.ok("POST", "/api/integrations/proposals/import", Record{"memberId": f.member["id"], "connectionId": f.connection["id"], "document": string(document), "checksum": checksum})
}
func TestExternalProposalReviewApplyProtectsAllocations(t *testing.T) {
	f := monitoringFixture(t)
	proposal := importExternalFixture(t, f, externalSnapshotFixture())
	if proposal["source"] != "offline-import" || proposal["snapshot"] == nil {
		t.Fatal("proposal source/snapshot missing")
	}
	id := str(proposal, "id")
	file := filepath.Join(f.c.a.integrationDir(), id+".json")
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("snapshot not private/file-backed")
	}
	list := f.c.ok("GET", "/api/integrations/proposals", nil)
	if list["items"].([]any)[0].(map[string]any)["snapshot"] != nil {
		t.Fatal("list loaded large snapshots instead of metadata")
	}
	f.c.ok("GET", "/api/integrations/proposals/"+id, nil)
	fields := []string{"member.name", "member.website", "member.peeringdbId", "connection.peeringdbNetixlanId"}
	body := Record{"checksum": proposal["checksum"], "baseChecksum": proposal["baseChecksum"], "fields": []string{"connection.ipv6"}}
	if w := f.c.request("POST", "/api/integrations/proposals/"+id+"/apply", body); w.Code != 409 {
		t.Fatal("external snapshot overwrote network allocation")
	}
	body["fields"] = fields
	applied := f.c.ok("POST", "/api/integrations/proposals/"+id+"/apply", body)
	if applied["status"] != "applied" {
		t.Fatal(applied)
	}
	state := f.c.a.store.Snapshot()
	member := find(&state, "members", str(f.member, "id"))
	connection := find(&state, "connections", str(f.connection, "id"))
	if str(member, "name") != "Reviewed PeeringDB Network" || number(member, "peeringdbId") != 101 || number(connection, "peeringdbNetixlanId") != 201 {
		t.Fatal("reviewed profile association not applied")
	}
	if number(member, "asn") != 64501 || str(connection, "ipv4") != "192.0.2.10" || str(connection, "ipv6") != "" || number(connection, "speed") != number(f.connection, "speed") {
		t.Fatal("authoritative network allocation changed")
	}
	if w := f.c.request("POST", "/api/integrations/proposals/"+id+"/apply", body); w.Code != 409 {
		t.Fatal("applied proposal replayed")
	}
}
func TestExternalProposalRejectsStaleBaseTamperingAndWrongASN(t *testing.T) {
	f := monitoringFixture(t)
	snapshot := externalSnapshotFixture()
	snapshot.Network.ASN = 64502
	snapshot.NetIXLANs[0].ASN = 64502
	document, checksum, _ := external.Canonical(snapshot)
	if w := f.c.request("POST", "/api/integrations/proposals/import", Record{"memberId": f.member["id"], "document": string(document), "checksum": checksum}); w.Code < 400 {
		t.Fatal("wrong ASN proposal accepted")
	}
	proposal := importExternalFixture(t, f, externalSnapshotFixture())
	id := str(proposal, "id")
	body := Record{"checksum": proposal["checksum"], "baseChecksum": proposal["baseChecksum"], "fields": []string{"member.name"}}
	f.c.a.store.Update(func(s *State) error {
		find(s, "members", str(f.member, "id"))["notes"] = "changed while reviewing"
		return nil
	})
	if w := f.c.request("POST", "/api/integrations/proposals/"+id+"/apply", body); w.Code != 409 {
		t.Fatal("stale local base applied")
	}
	dismissed := f.c.ok("POST", "/api/integrations/proposals/"+id+"/dismiss", Record{"checksum": proposal["checksum"], "reason": "保留本地修改"})
	if dismissed["status"] != "dismissed" {
		t.Fatal("obsolete proposal could not be closed")
	}
	proposal = importExternalFixture(t, f, externalSnapshotFixture())
	id = str(proposal, "id")
	if err := os.WriteFile(filepath.Join(f.c.a.integrationDir(), id+".json"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	body = Record{"checksum": proposal["checksum"], "baseChecksum": proposal["baseChecksum"], "fields": []string{"member.name"}}
	if w := f.c.request("POST", "/api/integrations/proposals/"+id+"/apply", body); w.Code != 409 {
		t.Fatal("tampered snapshot applied")
	}
}
func TestExternalProposalMemberAndBearerCannotApply(t *testing.T) {
	f := monitoringFixture(t)
	f.c.create("users", Record{"name": "Member", "email": "external-member@example.net", "role": "member", "memberId": f.member["id"], "password": "member-integration-password"})
	member := loginClient(t, f.c.a, Record{"email": "external-member@example.net", "password": "member-integration-password"})
	if w := member.request("GET", "/api/integrations/proposals", nil); w.Code != 403 {
		t.Fatal("member read staff proposals")
	}
	raw := str(f.c.ok("POST", "/api/account/tokens", Record{"name": "sync", "currentPassword": "very-long-test-password", "scopes": []string{"integrations:write"}}), "token")
	proposal := importExternalFixture(t, f, externalSnapshotFixture())
	body := Record{"checksum": proposal["checksum"], "baseChecksum": proposal["baseChecksum"], "fields": []string{"member.name"}}
	if w := bearerRequest(f.c.a, raw, "POST", "/api/integrations/proposals/"+str(proposal, "id")+"/apply", body); w.Code != 403 {
		t.Fatal("automation token bypassed browser review")
	}
}
func TestExternalSnapshotHistorySurvivesFreshnessWindow(t *testing.T) {
	f := monitoringFixture(t)
	proposal := importExternalFixture(t, f, externalSnapshotFixture())
	id := str(proposal, "id")
	snapshot := externalSnapshotFixture()
	snapshot.FetchedAt = time.Now().Add(-8 * 24 * time.Hour)
	raw, hash, _ := external.Canonical(snapshot)
	os.WriteFile(filepath.Join(f.c.a.integrationDir(), id+".json"), raw, 0600)
	f.c.a.store.Update(func(s *State) error {
		p := &s.ExternalProposals[len(s.ExternalProposals)-1]
		p.SnapshotChecksum = hash
		p.FetchedAt = snapshot.FetchedAt
		p.Checksum = proposalChecksum(*p)
		return nil
	})
	f.c.ok("GET", "/api/integrations/proposals/"+id, nil)
	state := f.c.a.store.Snapshot()
	p := state.ExternalProposals[len(state.ExternalProposals)-1]
	body := Record{"checksum": p.Checksum, "baseChecksum": p.BaseChecksum, "fields": []string{"member.name"}}
	if w := f.c.request("POST", "/api/integrations/proposals/"+id+"/apply", body); w.Code != 409 {
		t.Fatal("aged source applied")
	}
	if err := f.c.a.initIntegrations(""); err != nil {
		t.Fatal("historical snapshots prevented startup after seven days")
	}
}

type fixtureProvider struct {
	snapshot external.Snapshot
	asn      uint32
}

func (f *fixtureProvider) Fetch(_ context.Context, asn uint32, _, _ int64) (external.Snapshot, error) {
	f.asn = asn
	return f.snapshot, nil
}
func (f *fixtureProvider) Origin() string { return "https://configured-provider.example/api" }

// The public HTTP client's transport is separately fixture-tested; this helper
// records the API boundary's authoritative ASN without contacting a provider.

func TestConfiguredProviderProposalUsesAuthoritativeASN(t *testing.T) {
	f := monitoringFixture(t)
	fixture := &fixtureProvider{snapshot: externalSnapshotFixture()}
	f.c.a.peeringDB = fixture
	proposal := f.c.ok("POST", "/api/integrations/peeringdb/propose", Record{"memberId": f.member["id"], "connectionId": f.connection["id"]})
	if fixture.asn != 64501 || proposal["source"] != "provider-fetch" || proposal["providerOrigin"] != fixture.Origin() {
		t.Fatal("provider fetch not bound to authoritative ASN and configured origin")
	}
	f.c.a.peeringDB = nil
	if w := f.c.request("POST", "/api/integrations/peeringdb/propose", Record{"memberId": f.member["id"]}); w.Code != 503 {
		t.Fatal("disabled provider fetch did not fail closed")
	}
}

func TestExternalTerminalPruneAndAuditedMetadataDeletion(t *testing.T) {
	f := monitoringFixture(t)
	proposal := importExternalFixture(t, f, externalSnapshotFixture())
	id := str(proposal, "id")
	path := "/api/integrations/proposals/" + id
	retention := Record{"checksum": proposal["checksum"], "acknowledge": true, "reason": "保留审计，清理原始副本"}
	if w := f.c.request("POST", path+"/prune", retention); w.Code != 409 {
		t.Fatal("pending raw snapshot was pruned")
	}
	if w := f.c.request("DELETE", path, retention); w.Code != 409 {
		t.Fatal("pending metadata was deleted")
	}
	f.c.ok("POST", path+"/dismiss", Record{"checksum": proposal["checksum"], "reason": "采用本地资料"})
	if w := f.c.request("DELETE", path, retention); w.Code != 409 {
		t.Fatal("unpruned terminal metadata was deleted")
	}
	if w := f.c.request("POST", path+"/prune", Record{"checksum": proposal["checksum"]}); w.Code != 400 {
		t.Fatal("pruning did not require explicit acknowledgement")
	}
	pruned := f.c.ok("POST", path+"/prune", retention)
	if pruned["snapshotPruned"] != true || pruned["snapshotUnavailable"] != true || pruned["checksum"] != proposal["checksum"] || pruned["snapshotChecksum"] != proposal["snapshotChecksum"] || pruned["freedBytes"].(float64) <= 0 {
		t.Fatal("pruning failed to preserve review evidence or release storage")
	}
	if _, err := os.Stat(filepath.Join(f.c.a.integrationDir(), id+".json")); !os.IsNotExist(err) {
		t.Fatal("pruned file still occupies quota")
	}
	detail := f.c.ok("GET", path, nil)
	if detail["snapshot"] != nil || detail["snapshotUnavailable"] != true || len(detail["diff"].([]any)) == 0 {
		t.Fatal("pruned detail lost metadata/diff or pretended raw data exists")
	}
	state := f.c.a.store.Snapshot()
	if err := ValidateExternalArtifacts(filepath.Dir(f.c.a.store.path), state); err != nil {
		t.Fatal("intentionally pruned artifact failed offline validation", err)
	}
	if w := f.c.request("DELETE", path, Record{"checksum": proposal["checksum"], "acknowledge": true}); w.Code != 400 {
		t.Fatal("metadata deletion accepted without reason")
	}
	f.c.ok("DELETE", path, retention)
	state = f.c.a.store.Snapshot()
	if len(state.ExternalProposals) != 0 {
		t.Fatal("metadata count quota not released")
	}
	last := state.Audit[len(state.Audit)-1]
	if last.Action != "delete-external-proposal" || last.Details["checksum"] != proposal["checksum"] || last.Details["diff"] == nil {
		t.Fatal("terminal deletion lost immutable review diff/checksums")
	}
}
func TestExternalPruneCrashIntentResumesWithoutBlockingStartup(t *testing.T) {
	f := monitoringFixture(t)
	proposal := importExternalFixture(t, f, externalSnapshotFixture())
	id := str(proposal, "id")
	f.c.ok("POST", "/api/integrations/proposals/"+id+"/dismiss", Record{"checksum": proposal["checksum"]})
	f.c.a.store.Update(func(s *State) error {
		p := &s.ExternalProposals[0]
		p.SnapshotPruned = true
		p.SnapshotPrunedAt = time.Now().UTC()
		p.SnapshotPrunedBy = str(s.Resources["users"][0], "id")
		auditDetails(s, p.SnapshotPrunedBy, "prune-external-snapshot", "members", p.MemberID, proposalMetadata(*p))
		return nil
	})
	state := f.c.a.store.Snapshot()
	if err := ValidateExternalArtifacts(filepath.Dir(f.c.a.store.path), state); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(f.c.a.integrationDir(), id+".json")
	if _, err := os.Stat(file); err != nil {
		t.Fatal("read-only restore validation mutated file")
	}
	if err := f.c.a.initIntegrations(""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("committed pruning intent was not resumed")
	}
	if err := f.c.a.initIntegrations(""); err != nil {
		t.Fatal("prune resume was not idempotent")
	}
}
func TestExternalProposalCountQuotaCanRecoverWithAudit(t *testing.T) {
	f := monitoringFixture(t)
	proposal := importExternalFixture(t, f, externalSnapshotFixture())
	id := str(proposal, "id")
	f.c.ok("POST", "/api/integrations/proposals/"+id+"/dismiss", Record{"checksum": proposal["checksum"]})
	f.c.ok("POST", "/api/integrations/proposals/"+id+"/prune", Record{"checksum": proposal["checksum"], "acknowledge": true})
	f.c.a.store.Update(func(s *State) error {
		base := s.ExternalProposals[0]
		s.ExternalProposals = nil
		for n := 0; n < 2000; n++ {
			p := base
			p.ID = fmt.Sprintf("archived-%d", n)
			s.ExternalProposals = append(s.ExternalProposals, p)
		}
		return nil
	})
	document, checksum, _ := external.Canonical(externalSnapshotFixture())
	body := Record{"memberId": f.member["id"], "document": string(document), "checksum": checksum}
	if w := f.c.request("POST", "/api/integrations/proposals/import", body); w.Code != 409 {
		t.Fatal("proposal count bound ignored")
	}
	f.c.ok("DELETE", "/api/integrations/proposals/archived-0", Record{"checksum": proposal["checksum"], "acknowledge": true, "reason": "归档审计后释放提案配额"})
	f.c.ok("POST", "/api/integrations/proposals/import", body)
}
