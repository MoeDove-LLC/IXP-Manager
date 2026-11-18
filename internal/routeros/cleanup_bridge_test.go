package routeros

import (
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/routeros/api"
)

func TestCleanupBridgeNeedsNoPasswordAndCannotExposeOtherOperations(t *testing.T) {
	old, peers := bootstrap(t)
	peers[0].Password = "Private_fixture_0123"
	peers[0].AuthenticationRevision = strings.Repeat("A", 22)
	p, err := BuildPlan(old.Target, routerFrom(old), peers, observed(old), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewAPIBridge(p, nil); err == nil {
		t.Fatal("full execution bridge did not require the reviewed credential")
	}
	bridge, err := NewCleanupBridge(p)
	if err != nil {
		t.Fatal(err)
	}
	inv, evidence := bridgeInventory(t, false)
	resolver, err := NewAPIResolver(inv, p.Target, evidence)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, op := range p.Operations {
		mutation, err := bridge.Mutation(op.ID, resolver)
		if op.Phase != Cleanup {
			if err == nil {
				t.Fatal("cleanup bridge exposed preparation or activation")
			}
			continue
		}
		count++
		if err != nil || mutation.Phase != Cleanup || mutation.Request.Action != api.Remove || mutation.Request.ID != op.Object.ID || len(mutation.Request.Attributes) != 0 || mutation.NativeValidated {
			t.Fatal("cleanup lost exact removal identity or granted native authority", err)
		}
	}
	if count == 0 || len(bridge.Operations()) != count {
		t.Fatal("cleanup fixture did not cover old-generation removals")
	}
	changed := clonePlan(t, p)
	for i := range changed.Operations {
		if changed.Operations[i].Phase == Cleanup {
			changed.Operations[i].Object = observed(old).InputEntry
			break
		}
	}
	changed.Checksum = ""
	changed.Checksum = hash(changed)
	if _, err = NewCleanupBridge(changed); err == nil {
		t.Fatal("edited cleanup operations were accepted")
	}
}
