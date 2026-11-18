package routeros

import (
	"testing"

	"github.com/moedove/ixp-manager/internal/routeros/api"
)

func TestLocalASNIsExplicitAndPinnedBeforeInstanceChange(t *testing.T) {
	old, peers := bootstrap(t)
	router := routerFrom(old)
	router.ASN = 64520
	next, err := BuildPlan(old.Target, router, peers, observed(old), PlanOptions{AllowIdentityChange: true})
	if err != nil {
		t.Fatal(err)
	}
	connIndex, instanceIndex := -1, -1
	for i, op := range next.Operations {
		if op.Kind == SetConnection {
			connIndex = i
			if op.Connection.LocalASN != router.ASN || op.EstimatedActions < 19 {
				t.Fatal("desired local ASN or budget missing")
			}
		}
		if op.Kind == SetInstance {
			instanceIndex = i
		}
	}
	if connIndex < 0 || instanceIndex <= connIndex {
		t.Fatal("instance changed before connections pinned explicit AS")
	}
	compiled, err := Compile(next, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, cmd := range compiled.Commands {
		if cmd.Resource == BGPConnection && cmd.Action == Set {
			for _, a := range cmd.Attributes {
				if a.Name == "as" && a.Value != "64520" {
					t.Fatal("wrong local ASN compiled")
				}
			}
		}
	}
}

func TestRemovedPeerKeepsValidHistoricalLocalASN(t *testing.T) {
	old, inv, known, e := inspectFixture(t)
	router := routerFrom(old)
	router.ASN = old.Desired.Peers[0].ASN // removed peer's remote AS equals NEW instance AS
	next, err := BuildPlan(old.Target, router, nil, observed(old), PlanOptions{AllowIdentityChange: true})
	if err != nil {
		t.Fatal(err)
	}
	pinned := false
	setInstance := false
	for _, op := range next.Operations {
		if op.Kind == SetConnection {
			pinned = true
			if setInstance || op.Connection.LocalASN != old.Desired.ASN || op.Connection.LocalASN == op.Connection.RemoteASN {
				t.Fatal("retained connection was turned into an invalid eBGP pair")
			}
		}
		if op.Kind == SetInstance {
			setInstance = true
		}
	}
	if !pinned {
		t.Fatal("old possibly inherited ASN not explicitly pinned")
	}
	bridge, err := NewAPIBridge(next, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range bridge.Operations() {
		if op.Phase == Cleanup {
			continue
		}
		resolver, err := NewAPIResolver(inv, next.Target, e)
		if err != nil {
			t.Fatal(err)
		}
		m, err := bridge.Mutation(op.ID, resolver)
		if err != nil {
			t.Fatal(err)
		}
		modelMutation(t, &inv, m.Request)
	}
	known.Generations = append(known.Generations, KnownGeneration{Target: next.Target, Desired: next.Desired, PolicyChecksum: next.PolicyChecksum})
	known.ActiveGeneration = next.Generation
	e.Properties = []VersionPropertyEvidence{{Resource: api.BGPConnection, ID: "*1", Field: "input.limit-process-routes-ipv6", Completed: true}}
	s, err := buildSnapshot(inv, known, e, matchedCredential)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Connections) != 1 || !s.Connections[0].Disabled || s.Connections[0].Config.LocalASN != old.Desired.ASN || s.ASN != router.ASN {
		t.Fatal("retained historical state not represented")
	}
	withoutHistory := known
	withoutHistory.Generations = withoutHistory.Generations[1:]
	if _, err := buildSnapshot(inv, withoutHistory, e, matchedCredential); err == nil {
		t.Fatal("unproved historical ASN accepted")
	}
	inv.Connections[0]["disabled"] = "false"
	if _, err := buildSnapshot(inv, known, e, matchedCredential); err == nil {
		t.Fatal("enabled mismatched local ASN accepted")
	}
	inv.Connections[0]["disabled"] = "true"
	inv.Connections[0]["as"] = number(router.ASN)
	if _, err := buildSnapshot(inv, known, e, matchedCredential); err == nil {
		t.Fatal("disabled eBGP self-AS accepted")
	}
}

func TestUnknownLocalASNIsNeverInferred(t *testing.T) {
	_, inv, known, e := inspectFixture(t)
	delete(inv.Connections[0], "as")
	e.Properties = []VersionPropertyEvidence{{Resource: api.BGPConnection, ID: "*1", Field: "as", Completed: true}}
	if _, err := buildSnapshot(inv, known, e, matchedCredential); err == nil {
		t.Fatal("no-ret became a claimed local ASN")
	}
}
