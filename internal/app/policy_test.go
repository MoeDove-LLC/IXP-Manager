package app

import (
	"github.com/moedove/ixp-manager/internal/policy"
	"testing"
	"time"
)

func policyPeer(t *testing.T) (*client, Record) {
	c, _ := setupClient(t)
	m := c.create("members", Record{"name": "Network", "asn": 64501})
	v := c.create("vlans", Record{"name": "LAN", "tag": 100, "ipv4Prefix": "192.0.2.0/24"})
	router := c.create("routers", Record{"name": "RS", "asn": 64500, "routerId": "192.0.2.1", "localIpv4": "192.0.2.1", "vlanId": v["id"]})
	peer := c.create("peers", Record{"name": "Peer", "memberId": m["id"], "routerId": router["id"], "asn": 64501, "address": "192.0.2.2", "enabled": false, "maxPrefixes": 100, "allowedPrefixes": []string{"198.51.100.0/24"}})
	return c, peer
}
func TestPolicyReviewApplyAndStaleness(t *testing.T) {
	c, peer := policyPeer(t)
	now := time.Now().UTC()
	snap := policy.IRRSnapshot{Version: 1, Source: "fixture", FetchedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), ASSet: "AS64501", ASNs: []uint32{64501}, Routes: []policy.Route{{Prefix: "203.0.113.0/24", OriginASN: 64501}, {Prefix: "2001:db8::/32", OriginASN: 64501}}}
	meta := c.ok("POST", "/api/policy/snapshots", Record{"kind": "irr", "snapshot": snap})
	proposal := c.ok("POST", "/api/policy/candidates", Record{"peerId": peer["id"], "irrSnapshotId": meta["id"]})
	if len(proposal["prefixes"].([]any)) != 1 {
		t.Fatal("candidate address family not filtered")
	}
	c.ok("POST", "/api/policy/apply", Record{"peerId": peer["id"], "irrSnapshotId": meta["id"], "baseChecksum": proposal["baseChecksum"]})
	if w := c.request("POST", "/api/policy/apply", Record{"peerId": peer["id"], "irrSnapshotId": meta["id"], "baseChecksum": proposal["baseChecksum"]}); w.Code != 409 {
		t.Fatal("stale review applied")
	}
	snap.ExpiresAt = now.Add(-time.Second)
	if w := c.request("POST", "/api/policy/snapshots", Record{"kind": "irr", "snapshot": snap}); w.Code != 422 {
		t.Fatal("expired snapshot imported")
	}
	if len(c.a.store.Snapshot().Deployments) != 0 {
		t.Fatal("policy import applied router")
	}
}
func TestPolicyRPKISimulationAndReadOnlyRole(t *testing.T) {
	c, peer := policyPeer(t)
	now := time.Now().UTC()
	snap := policy.ROASnapshot{Version: 1, Source: "fixture", FetchedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), ROAs: []policy.ROA{{Prefix: "198.51.100.0/24", MaxLength: 24, ASN: 64501}}}
	meta := c.ok("POST", "/api/policy/snapshots", Record{"kind": "rpki", "snapshot": snap})
	c.create("users", Record{"name": "Viewer", "email": "viewer@example.net", "role": "viewer", "password": "viewer-long-password"})
	viewer := docLoginClient(t, c.a, "viewer@example.net", "viewer-long-password")
	result := viewer.ok("POST", "/api/policy/simulate", Record{"peerId": peer["id"], "rpkiSnapshotId": meta["id"], "rejectRpkiNotFound": true, "routes": []policy.Route{{Prefix: "198.51.100.0/24", OriginASN: 64501}, {Prefix: "198.51.100.0/25", OriginASN: 64501}}})
	decisions := result["decisions"].([]any)
	if decisions[0].(map[string]any)["accepted"] != true || decisions[1].(map[string]any)["accepted"] != false {
		t.Fatal(decisions)
	}
	if w := viewer.request("DELETE", "/api/policy/snapshots/"+str(meta, "id"), nil); w.Code != 403 {
		t.Fatal("viewer deleted cache")
	}
}

func TestASSetOriginPairsSurviveReviewAndGenericEdits(t *testing.T) {
	c, peer := policyPeer(t)
	now := time.Now().UTC()
	snapshot := policy.IRRSnapshot{Version: 1, Source: "fixture", FetchedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Hour), ASSet: "AS-EXAMPLE", ASNs: []uint32{64501, 64502}, Routes: []policy.Route{{Prefix: "198.51.100.0/24", OriginASN: 64502}}}
	meta := c.ok("POST", "/api/policy/snapshots", Record{"kind": "irr", "snapshot": snapshot})
	proposal := c.ok("POST", "/api/policy/candidates", Record{"peerId": peer["id"], "irrSnapshotId": meta["id"], "includeASSetOrigins": true})
	paired := proposal["allowedRoutes"].([]any)
	if len(paired) != 1 || paired[0].(map[string]any)["originAsn"] != float64(64502) {
		t.Fatal("AS-SET pairing lost")
	}
	saved := c.ok("POST", "/api/policy/apply", Record{"peerId": peer["id"], "irrSnapshotId": meta["id"], "baseChecksum": proposal["baseChecksum"], "includeASSetOrigins": true})
	result := c.ok("POST", "/api/policy/simulate", Record{"peerId": peer["id"], "routes": []policy.Route{{Prefix: "198.51.100.0/24", OriginASN: 64502, ASPath: []uint32{64501, 64502}}, {Prefix: "198.51.100.0/24", OriginASN: 64501, ASPath: []uint32{64501}}}})
	decisions := result["decisions"].([]any)
	if decisions[0].(map[string]any)["accepted"] != true || decisions[1].(map[string]any)["accepted"] != false {
		t.Fatal("simulation ignored bound origin")
	}
	delete(saved, "id")
	delete(saved, "createdAt")
	delete(saved, "updatedAt")
	delete(saved, "allowedRoutes")
	saved["name"] = "Renamed"
	updated := c.ok("PUT", "/api/resources/peers/"+str(peer, "id"), saved)
	if len(updated["allowedRoutes"].([]any)) != 1 {
		t.Fatal("generic edit discarded security policy")
	}
	saved["allowedPrefixes"] = []string{"203.0.113.0/24"}
	if w := c.request("PUT", "/api/resources/peers/"+str(peer, "id"), saved); w.Code != 400 {
		t.Fatal("generic edit silently removed origin binding")
	}
}
