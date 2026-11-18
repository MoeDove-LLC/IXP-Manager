package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/lookingglass"
)

func lookingGlassFixture(t *testing.T) (*client, string, Record, string) {
	c, dir, peer := credentialFixture(t)
	router := c.ok("GET", "/api/resources/routers/"+str(peer, "routerId"), nil)
	edit := editableCredentialPeer(router)
	edit["platform"] = "bird2"
	c.ok("PUT", "/api/resources/routers/"+str(router, "id"), edit)
	created := c.ok("POST", "/api/account/tokens", Record{"name": "Diagnostic worker", "currentPassword": "very-long-test-password", "scopes": []string{"looking-glass:run"}, "routerIds": []string{str(router, "id")}})
	return c, dir, peer, str(created, "token")
}
func claimDiagnostic(t *testing.T, c *client, raw, routerID string) *lookingglass.Lease {
	t.Helper()
	w := bearerRequest(c.a, raw, "POST", "/api/looking-glass/claim", lookingglass.ClaimRequest{RouterID: routerID})
	if w.Code != 200 {
		t.Fatal("claim failed", w.Code, w.Body.String())
	}
	var result lookingglass.ClaimResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Lease
}
func routeResult(lease *lookingglass.Lease) lookingglass.Result {
	filtered := false
	return lookingglass.Result{Query: lease.Query, ObservedAt: time.Now().UTC(), Status: "ok", Complete: true, Identity: lookingglass.Identity{Address: lease.Query.NeighborAddress, ASN: lease.Query.NeighborASN, State: "Established", RouterID: lease.Query.ExpectedRouterID, RouterASN: lease.Query.ExpectedRouterASN}, Routes: []lookingglass.Route{{Prefix: "198.51.100.0/24", NextHops: []string{lease.Query.NeighborAddress}, ASPath: []lookingglass.PathSegment{{Type: "sequence", ASNs: []uint32{lease.Query.NeighborASN}}, {Type: "set", ASNs: []uint32{64502, 64503}}}, Communities: [][2]uint32{{64501, 100}}, LargeCommunities: [][3]uint32{}, Filtered: &filtered}}}
}
func submitDiagnostic(t *testing.T, c *client, raw string, lease *lookingglass.Lease, result lookingglass.Result) Record {
	t.Helper()
	w := bearerRequest(c.a, raw, "POST", "/api/looking-glass/complete", lookingglass.Completion{LeaseID: lease.ID, LeaseToken: lease.Token, Result: result})
	if w.Code != 200 {
		t.Fatal("completion failed", w.Code, w.Body.String())
	}
	var reply Record
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	return reply
}
func memberDiagnosticClient(t *testing.T, c *client, memberID, email string) *client {
	c.create("users", Record{"name": "Member diagnostic", "email": email, "role": "member", "memberId": memberID, "password": "member-diagnostic-password"})
	return docLoginClient(t, c.a, email, "member-diagnostic-password")
}

func TestLookingGlassLeaseSnapshotAndMemberIsolation(t *testing.T) {
	c, dir, peer, raw := lookingGlassFixture(t)
	member := memberDiagnosticClient(t, c, str(peer, "memberId"), "lg-member@example.net")
	otherMember := c.create("members", Record{"name": "Other", "asn": 64502, "status": "active"})
	other := memberDiagnosticClient(t, c, str(otherMember, "id"), "lg-other@example.net")
	cap := member.ok("GET", "/api/looking-glass/capabilities", nil)
	if len(cap["peers"].([]any)) != 1 || len(cap["routers"].([]any)) != 0 {
		t.Fatal("member capability target projection is unsafe")
	}
	if len(other.ok("GET", "/api/looking-glass/capabilities", nil)["peers"].([]any)) != 0 {
		t.Fatal("other member saw peer targets")
	}
	if w := member.request("POST", "/api/looking-glass/queries", Record{"routerId": peer["routerId"], "view": "table", "family": 4}); w.Code != 403 {
		t.Fatal("member requested a whole routing table")
	}
	request := Record{"peerId": peer["id"], "view": "accepted", "prefix": "198.51.100.0/24", "maxRows": 20}
	denied := other.request("POST", "/api/looking-glass/queries", request)
	missing := other.request("POST", "/api/looking-glass/queries", Record{"peerId": "missing-peer", "view": "accepted"})
	if denied.Code != 404 || missing.Code != 404 || denied.Body.String() != missing.Body.String() {
		t.Fatal("member query disclosed whether another member's peer exists")
	}
	created := member.ok("POST", "/api/looking-glass/queries", request)["item"].(map[string]any)
	id := created["id"].(string)
	path := "/api/looking-glass/queries/" + id
	if w := other.request("GET", path, nil); w.Code != 404 {
		t.Fatal("another member read query metadata")
	}
	if w := bearerRequest(c.a, raw, "POST", "/api/looking-glass/claim", Record{"routerId": "another-router"}); w.Code != 403 {
		t.Fatal("worker claimed another router")
	}
	lease := claimDiagnostic(t, c, raw, str(peer, "routerId"))
	if lease == nil || lease.ID != id {
		t.Fatal("queued query not leased")
	}
	if next := claimDiagnostic(t, c, raw, str(peer, "routerId")); next != nil {
		t.Fatal("same router received concurrent native queries")
	}
	result := routeResult(lease)
	result.ObservedAt = time.Now().UTC().Add(-10 * time.Second)
	tampered := result
	tampered.Query.Prefix = "203.0.113.0/24"
	tampered.Routes = nil
	if w := bearerRequest(c.a, raw, "POST", "/api/looking-glass/complete", lookingglass.Completion{LeaseID: lease.ID, LeaseToken: lease.Token, Result: tampered}); w.Code != 409 {
		t.Fatal("worker changed leased query", w.Code)
	}
	submitDiagnostic(t, c, raw, lease, result)
	if submitDiagnostic(t, c, raw, lease, result)["duplicate"] != true {
		t.Fatal("lost completion response was not idempotent")
	}
	view := member.ok("GET", path+"/result", nil)
	if view["freshness"].(map[string]any)["status"] != "fresh" {
		t.Fatal("fresh snapshot mislabeled")
	}
	encoded, _ := json.Marshal(view)
	if !bytes.Contains(encoded, []byte(`"type":"set"`)) || bytes.Contains(encoded, []byte(lease.Token)) {
		t.Fatal("AS_SET flattened or lease secret exposed")
	}
	if w := other.request("GET", path+"/result", nil); w.Code != 404 {
		t.Fatal("route snapshot leaked to another member")
	}
	stored, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil || bytes.Contains(stored, []byte(`"nextHops"`)) || bytes.Contains(stored, []byte(lease.Token)) {
		t.Fatal("raw routes or lease secret stored in hot state")
	}
	cached := member.ok("POST", "/api/looking-glass/queries", request)
	if cached["cached"] != true || cached["item"].(map[string]any)["id"] != id {
		t.Fatal("recent identical query was not deduplicated")
	}
	// A source clock up to 30 seconds ahead must not prolong a cached result
	// after its server receipt is already stale. Members cannot force queries.
	if err := c.a.store.Update(func(s *State) error {
		lookingGlassByID(s, id).CompletedAt = result.ObservedAt.Add(-30 * time.Second)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if view := member.ok("GET", path+"/result", nil); view["freshness"].(map[string]any)["status"] != "stale" {
		t.Fatal("server receipt age did not bound freshness")
	}
	renewed := member.ok("POST", "/api/looking-glass/queries", request)
	if renewed["cached"] != false || renewed["item"].(map[string]any)["id"] == id {
		t.Fatal("stale snapshot blocked an ordinary member from collecting a fresh result")
	}
	current := c.ok("GET", path, nil)
	pruned := c.ok("POST", path+"/prune", Record{"expectedRevision": current["revision"], "acknowledge": true})
	if w := member.request("GET", path+"/result", nil); w.Code != 410 {
		t.Fatal("pruned result remained readable")
	}
	c.ok("DELETE", path, Record{"expectedRevision": pruned["revision"], "acknowledge": true, "reason": "retention verified"})
}

func TestLookingGlassRejectsLateOwnershipAndExpiredLease(t *testing.T) {
	c, _, peer, raw := lookingGlassFixture(t)
	member := memberDiagnosticClient(t, c, str(peer, "memberId"), "lg-stale@example.net")
	item := member.ok("POST", "/api/looking-glass/queries", Record{"peerId": peer["id"], "view": "accepted"})["item"].(map[string]any)
	lease := claimDiagnostic(t, c, raw, str(peer, "routerId"))
	if lease == nil {
		t.Fatal("no lease")
	}
	edit := editableCredentialPeer(peer)
	edit["address"] = "192.0.2.11"
	edit["enabled"] = false
	c.ok("PUT", "/api/resources/peers/"+str(peer, "id"), edit)
	if w := bearerRequest(c.a, raw, "POST", "/api/looking-glass/complete", lookingglass.Completion{LeaseID: lease.ID, LeaseToken: lease.Token, Result: routeResult(lease)}); w.Code != 409 {
		t.Fatal("late sample was relabeled as new ownership")
	}
	if w := member.request("GET", "/api/looking-glass/queries/"+item["id"].(string), nil); w.Code != 404 {
		t.Fatal("former binding remained visible to member")
	}
	edit["address"] = peer["address"]
	edit["enabled"] = true
	c.ok("PUT", "/api/resources/peers/"+str(peer, "id"), edit)
	if w := member.request("GET", "/api/looking-glass/queries/"+item["id"].(string), nil); w.Code != 404 {
		t.Fatal("A-B-A restored an old diagnostic authorization")
	}
	fresh := member.ok("POST", "/api/looking-glass/queries", Record{"peerId": peer["id"], "view": "accepted"})["item"].(map[string]any)
	newLease := claimDiagnostic(t, c, raw, str(peer, "routerId"))
	if newLease == nil {
		t.Fatal("no fresh lease")
	}
	if err := c.a.store.Update(func(s *State) error {
		q := lookingGlassByID(s, fresh["id"].(string))
		q.LeaseUntil = time.Now().Add(-time.Second)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if w := bearerRequest(c.a, raw, "POST", "/api/looking-glass/complete", lookingglass.Completion{LeaseID: newLease.ID, LeaseToken: newLease.Token, Result: routeResult(newLease)}); w.Code != 409 {
		t.Fatal("expired lease published a route snapshot")
	}
	reclaimed := claimDiagnostic(t, c, raw, str(peer, "routerId"))
	if reclaimed == nil || reclaimed.Token == newLease.Token {
		t.Fatal("expired lease was not safely reissued")
	}
	if w := bearerRequest(c.a, raw, "POST", "/api/looking-glass/complete", lookingglass.Completion{LeaseID: newLease.ID, LeaseToken: newLease.Token, Result: routeResult(newLease)}); w.Code != 409 {
		t.Fatal("stale lease token survived reissue")
	}
	submitDiagnostic(t, c, raw, reclaimed, routeResult(reclaimed))
}

func TestLookingGlassUnknownAndCancelledAreNotEmptyHealthyTables(t *testing.T) {
	c, _, peer, raw := lookingGlassFixture(t)
	item := c.ok("POST", "/api/looking-glass/queries", Record{"peerId": peer["id"], "view": "filtered"})["item"].(map[string]any)
	lease := claimDiagnostic(t, c, raw, str(peer, "routerId"))
	if lease == nil {
		t.Fatal("no lease")
	}
	failed := lookingglass.Result{Query: lease.Query, ObservedAt: time.Now().UTC(), Status: "unsupported", ErrorCode: "retention-unavailable", Routes: []lookingglass.Route{}}
	submitDiagnostic(t, c, raw, lease, failed)
	path := "/api/looking-glass/queries/" + item["id"].(string)
	metadata := c.ok("GET", path, nil)
	if metadata["status"] != "unsupported" || metadata["complete"] != false || metadata["observedAt"] != nil {
		t.Fatal("unsupported view advertised a complete or fresh empty table")
	}
	if w := c.request("GET", path+"/result", nil); w.Code != 409 {
		t.Fatal("unsupported query returned a successful empty snapshot")
	}
	next := c.ok("POST", "/api/looking-glass/queries", Record{"peerId": peer["id"], "view": "accepted"})["item"].(map[string]any)
	cancelPath := "/api/looking-glass/queries/" + next["id"].(string) + "/cancel"
	if w := c.request("POST", cancelPath, Record{"expectedRevision": "old"}); w.Code != 409 {
		t.Fatal("stale cancellation accepted")
	}
	c.ok("POST", cancelPath, Record{"expectedRevision": next["revision"]})
	if lease := claimDiagnostic(t, c, raw, str(peer, "routerId")); lease != nil {
		t.Fatal("cancelled query was executed")
	}
	if strings.Contains(c.request("GET", "/api/looking-glass/queries", nil).Body.String(), "leaseHash") {
		t.Fatal("internal lease exposed")
	}
}

func TestLookingGlassTableIdentityAndTruncation(t *testing.T) {
	c, _, peer, raw := lookingGlassFixture(t)
	item := c.ok("POST", "/api/looking-glass/queries", Record{"routerId": peer["routerId"], "view": "table", "family": 4, "maxRows": 1})["item"].(map[string]any)
	lease := claimDiagnostic(t, c, raw, str(peer, "routerId"))
	if lease == nil {
		t.Fatal("table query not leased")
	}
	result := lookingglass.Result{Query: lease.Query, ObservedAt: time.Now().UTC(), Status: "truncated", Truncated: true, Identity: lookingglass.Identity{RouterID: "192.0.2.99", RouterASN: 64500}, Routes: []lookingglass.Route{{Prefix: "198.51.100.0/24", NextHops: []string{"192.0.2.10"}, ASPath: []lookingglass.PathSegment{{Type: "sequence", ASNs: []uint32{64501}}}, Communities: [][2]uint32{}, LargeCommunities: [][3]uint32{}}}}
	if w := bearerRequest(c.a, raw, "POST", "/api/looking-glass/complete", lookingglass.Completion{LeaseID: lease.ID, LeaseToken: lease.Token, Result: result}); w.Code != 422 {
		t.Fatal("unproven router identity returned table routes")
	}
	result.Identity.RouterID = lease.Query.ExpectedRouterID
	result.Identity.RouterASN = lease.Query.ExpectedRouterASN
	submitDiagnostic(t, c, raw, lease, result)
	path := "/api/looking-glass/queries/" + item["id"].(string)
	view := c.ok("GET", path+"/result", nil)
	query := view["query"].(map[string]any)
	if query["status"] != "complete" || query["complete"] != false || query["truncated"] != true {
		t.Fatal("completed request was confused with a complete routing table")
	}
	// A future client clock must not extend freshness past the server receipt.
	if err := c.a.store.Update(func(s *State) error {
		q := lookingGlassByID(s, lease.ID)
		q.CompletedAt = time.Now().Add(-31 * time.Second)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if c.ok("GET", path+"/result", nil)["freshness"].(map[string]any)["status"] != "stale" {
		t.Fatal("old receipt remained fresh based on caller timestamp")
	}
}

func TestLookingGlassCommitFailureDoesNotStrandRetryArtifact(t *testing.T) {
	c, dir, peer, raw := lookingGlassFixture(t)
	c.ok("POST", "/api/looking-glass/queries", Record{"peerId": peer["id"], "view": "accepted"})
	lease := claimDiagnostic(t, c, raw, str(peer, "routerId"))
	if lease == nil {
		t.Fatal("no lease")
	}
	result := routeResult(lease)
	blocked := filepath.Join(dir, "state-publish-blocker")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	c.a.store.mu.Lock()
	original := c.a.store.path
	c.a.store.path = blocked
	c.a.store.mu.Unlock()
	defer func() { c.a.store.mu.Lock(); c.a.store.path = original; c.a.store.mu.Unlock() }()
	w := bearerRequest(c.a, raw, "POST", "/api/looking-glass/complete", lookingglass.Completion{LeaseID: lease.ID, LeaseToken: lease.Token, Result: result})
	if w.Code != 500 {
		t.Fatal("state commit failure reported success", w.Code)
	}
	c.a.store.mu.Lock()
	c.a.store.path = original
	c.a.store.mu.Unlock()
	if _, err := os.Stat(filepath.Join(dir, "looking-glass", lease.ID+".enc")); !os.IsNotExist(err) {
		t.Fatal("uncommitted fixed-ID snapshot blocks a later completion")
	}
	submitDiagnostic(t, c, raw, lease, result)
}
