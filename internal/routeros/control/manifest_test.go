package control

import (
	"context"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routing"
)

func TestPreparedAndDesiredManifestAuthenticationGenerations(t *testing.T) {
	_, fixture := fixtureRuntime(t)
	target := fixture.Plan.Target
	router := routing.Router{ID: target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1"}
	peer := routing.Peer{ID: "old", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"203.0.113.0/24"}, Password: "old-password", AuthenticationRevision: strings.Repeat("A", 22)}
	old, err := routeros.BuildPlan(target, router, []routing.Peer{peer}, fixture.Plan.Base, routeros.PlanOptions{Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	known := routeros.KnownState{Target: target, ActiveGeneration: old.Generation, Generations: []routeros.KnownGeneration{{Target: target, Desired: old.Desired, PolicyChecksum: old.PolicyChecksum}}, Peers: []routeros.KnownPeer{{ID: peer.ID, Authentication: old.Desired.Peers[0].Authentication}}}
	peer.Password = "new-password"
	peer.AuthenticationRevision = strings.Repeat("B", 22)
	added := routing.Peer{ID: "added", ASN: 64502, Address: "198.51.100.3", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"203.0.114.0/24"}, Password: "added-password", AuthenticationRevision: strings.Repeat("C", 22)}
	plan, err := routeros.BuildPlan(target, router, []routing.Peer{peer, added}, fixture.Plan.Base, routeros.PlanOptions{Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	begin := &beginRecord{Plan: plan, Known: known}
	prepared, err := transactionKnown(begin, false, true)
	if err != nil {
		t.Fatal(err)
	}
	desired, err := transactionKnown(begin, true, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Generations) != 2 || len(desired.Generations) != 2 || prepared.ActiveGeneration != old.Generation || desired.ActiveGeneration != plan.Generation {
		t.Fatal("generation context lost or cleanup silently assumed")
	}
	revisions := func(k routeros.KnownState) map[string]string {
		out := map[string]string{}
		for _, p := range k.Peers {
			out[p.ID] = p.Authentication.Revision
		}
		return out
	}
	before, after := revisions(prepared), revisions(desired)
	if before["old"] != strings.Repeat("A", 22) || after["old"] != strings.Repeat("B", 22) || before["added"] != strings.Repeat("C", 22) {
		t.Fatal("prepared and desired authentication epochs mixed")
	}
	if known.ActiveGeneration != old.Generation || len(known.Generations) != 1 {
		t.Fatal("baseline manifest mutated")
	}
}

func TestCredentialAuthorityBindsAllPublicProperties(t *testing.T) {
	r, _ := fixtureRuntime(t)
	owner := routeros.OwnerName(r.target.RouterID)
	name, err := routeros.ManagedConnectionName(r.target.RouterID, "peer-a")
	if err != nil {
		t.Fatal(err)
	}
	row := api.Record{".id": "*A", "name": name, "instance": owner, "routing-table": owner, "local.role": "ebgp-rs", "remote.address": "198.51.100.2", "remote.as": "64501", "as": "64500", "disabled": "true", "vrf": ""}
	digest, err := CredentialAttributesChecksum(row)
	if err != nil {
		t.Fatal(err)
	}
	attrs, err := canonicalRecord(api.BGPConnection, row)
	if err != nil {
		t.Fatal(err)
	}
	state := deviceState{Objects: map[string]objectState{objectKey(api.BGPConnection, "*A"): {Resource: api.BGPConnection, ID: "*A", Attributes: attrs}}}
	authority := authorityFor(state)
	if len(authority) != 1 || authority[0].ID != "*A" || authority[0].Name != name || authority[0].AttributesChecksum != digest {
		t.Fatal("journal object authority incomplete")
	}
	row["remote.as"] = "64502"
	different, err := CredentialAttributesChecksum(row)
	if err != nil || different == digest {
		t.Fatal("changed peer identity matched old authority")
	}
	row["tcp-md5-key"] = "secret"
	if _, err := CredentialAttributesChecksum(row); err == nil {
		t.Fatal("credential value entered public attribute digest")
	}
}

func TestStateDigestPreservesFilterOrderBetweenObservations(t *testing.T) {
	r, _ := fixtureRuntime(t)
	owner := routeros.OwnerName(r.target.RouterID)
	r.mu.Lock()
	r.inv.FilterRules = []api.Record{
		{".id": "*1", "chain": owner + "_g123_in", "comment": owner + ":one", "rule": "accept;", "disabled": "false", "inactive": "false"},
		{".id": "*2", "chain": owner + "_g123_in", "comment": owner + ":two", "rule": "reject;", "disabled": "false", "inactive": "false"},
	}
	r.mu.Unlock()
	first, err := r.Observe(context.Background(), ReadRequest{})
	if err != nil {
		t.Fatal(err)
	}
	before, err := projectObservation([]byte("fixture"), r.target, first)
	if err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	r.inv.FilterRules[0], r.inv.FilterRules[1] = r.inv.FilterRules[1], r.inv.FilterRules[0]
	r.mu.Unlock()
	second, err := r.Observe(context.Background(), ReadRequest{})
	if err != nil {
		t.Fatal(err)
	}
	after, err := projectObservation([]byte("fixture"), r.target, second)
	if err != nil {
		t.Fatal(err)
	}
	if stateMAC([]byte("fixture"), before) == stateMAC([]byte("fixture"), after) {
		t.Fatal("stable reordered filter set escaped the transaction baseline")
	}
}
