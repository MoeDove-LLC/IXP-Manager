package routeros

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/routeros/api"
)

func preparedDiscardFixture(t *testing.T) (Plan, api.Inventory, TargetEvidence, []PreparedDiscardObject) {
	t.Helper()
	plan, inventory, _, evidence := inspectFixture(t)
	peers := map[string]Peer{}
	for _, peer := range plan.Desired.Peers {
		peers[peer.ID] = peer
	}
	objects := []PreparedDiscardObject{}
	for _, op := range plan.Operations {
		_, resource, ok := discardResource(op)
		if !ok {
			continue
		}
		var found api.Record
		for _, row := range *modelRows(&inventory, resource) {
			if resource == api.FilterRule && row["comment"] == op.Rule.Comment || resource != api.FilterRule && row["name"] == op.Object.Name {
				found = row
				break
			}
		}
		if found == nil {
			t.Fatalf("missing complete modeled Prepare object: %s", op.ID)
		}
		if resource == api.FilterRule {
			found["rule"], _ = compileRule(*op.Rule, peers)
		}
		if resource == api.BGPConnection {
			found["disabled"] = "true"
		}
		rows, err := closedRows([]api.Record{found}, resource)
		if err != nil {
			t.Fatal(err)
		}
		attrs := rows[0]
		id := attrs[".id"]
		delete(attrs, ".id")
		delete(attrs, "used")
		if resource != api.FilterRule {
			delete(attrs, "inactive")
		}
		objects = append(objects, PreparedDiscardObject{PrepareOperationID: op.ID, Resource: resource, ID: id, Attributes: attrs})
	}
	return plan, inventory, evidence, objects
}

func TestPreparedDiscardBridgeOnlyInvertsExactPreparedAdds(t *testing.T) {
	plan, inventory, evidence, objects := preparedDiscardFixture(t)
	bridge, err := NewPreparedDiscardBridge(plan, objects)
	if err != nil {
		t.Fatal(err)
	}
	if len(bridge.Operations()) != len(objects) {
		t.Fatal("prepared additions missing from discard")
	}
	byID := map[string]Operation{}
	for _, op := range plan.Operations {
		byID[op.ID] = op
	}
	for _, descriptor := range bridge.Operations() {
		resolver, err := NewAPIResolver(inventory, plan.Target, evidence)
		if err != nil {
			t.Fatal(err)
		}
		mutation, err := bridge.Mutation(descriptor.ID, resolver)
		if err != nil {
			t.Fatal(err)
		}
		if mutation.Phase != DiscardPrepared || mutation.Request.Action != api.Remove || len(mutation.Request.Attributes) != 0 || mutation.NativeValidated || !mutation.RequiresStateVerification {
			t.Fatal("discard exposed a write other than a constrained removal")
		}
		op := byID[strings.TrimPrefix(descriptor.ID, "discard-")]
		if op.Phase != Prepare {
			t.Fatal("discard reached an activation or old cleanup")
		}
		if mutation.Request.Resource == api.FilterRule {
			for _, remaining := range inventory.FilterRules {
				if strings.Contains(remaining["rule"], "jump "+op.Rule.Chain+";") {
					t.Fatal("discard removed a referenced rule chain")
				}
			}
		}
		if mutation.Request.Resource == api.BGPInstance && len(inventory.Connections) != 0 {
			t.Fatal("discard removed instance before dependent connections")
		}
		if mutation.Request.Resource == api.RoutingTable && (len(inventory.Connections) != 0 || len(inventory.Instances) != 0) {
			t.Fatal("discard removed table before dependants")
		}
		modelMutation(t, &inventory, mutation.Request)
	}
	if len(inventory.Tables)+len(inventory.Instances)+len(inventory.Connections)+len(inventory.FilterRules) != 0 {
		t.Fatal("bootstrap model did not return to empty namespace")
	}
	for _, op := range plan.Operations {
		resolver, _ := NewAPIResolver(inventory, plan.Target, evidence)
		if _, err := bridge.Mutation(op.ID, resolver); err == nil {
			t.Fatal("original Prepare/Activate command leaked from discard bridge")
		}
	}
}

func TestPreparedDiscardBridgeRejectsIncompleteForeignAndChangedObjects(t *testing.T) {
	plan, _, _, originals := preparedDiscardFixture(t)
	clone := func() []PreparedDiscardObject {
		raw, _ := json.Marshal(originals)
		var out []PreparedDiscardObject
		json.Unmarshal(raw, &out)
		return out
	}
	for _, name := range []string{"missing", "duplicate", "foreign-op", "resource", "display-id", "embedded-id", "password", "missing-flag", "fib", "enabled-connection", "changed-rule"} {
		t.Run(name, func(t *testing.T) {
			objects := clone()
			switch name {
			case "missing":
				objects = objects[1:]
			case "duplicate":
				objects[1] = objects[0]
			case "foreign-op":
				objects[0].PrepareOperationID = "not-in-original-plan"
			case "resource":
				objects[0].Resource = api.BGPConnection
			case "display-id":
				objects[0].ID = "0"
			case "embedded-id":
				objects[0].Attributes[".id"] = objects[0].ID
			case "password":
				objects[0].Attributes["tcp-md5-key"] = "must-never-be-an-attribute"
			case "missing-flag":
				delete(objects[0].Attributes, "disabled")
			case "fib":
				for i := range objects {
					if objects[i].Resource == api.RoutingTable {
						objects[i].Attributes["fib"] = "true"
					}
				}
			case "enabled-connection":
				for i := range objects {
					if objects[i].Resource == api.BGPConnection {
						objects[i].Attributes["disabled"] = "false"
					}
				}
			case "changed-rule":
				for i := range objects {
					if objects[i].Resource == api.FilterRule {
						objects[i].Attributes["rule"] = "accept;"
						break
					}
				}
			}
			if _, err := NewPreparedDiscardBridge(plan, objects); err == nil {
				t.Fatal("unsafe preparation claim accepted")
			}
		})
	}
}

func TestPreparedDiscardBridgePinsReadbackAndIndependentScope(t *testing.T) {
	plan, inventory, evidence, objects := preparedDiscardFixture(t)
	bridge, err := NewPreparedDiscardBridge(plan, objects)
	if err != nil {
		t.Fatal(err)
	}
	operations := bridge.Operations()
	for i, j := 0, len(objects)-1; i < j; i, j = i+1, j-1 {
		objects[i], objects[j] = objects[j], objects[i]
	}
	other, err := NewPreparedDiscardBridge(plan, objects)
	if err != nil || !reflect.DeepEqual(operations, other.Operations()) {
		t.Fatal("discard scope depended on caller/map order", err)
	}
	for _, object := range objects {
		if object.Resource != api.BGPConnection {
			continue
		}
		object.Attributes["disabled"] = "false" // caller mutation cannot alter bound checksum
		resolver, _ := NewAPIResolver(inventory, plan.Target, evidence)
		id := PreparedDiscardOperationID(object.PrepareOperationID)
		if _, err := bridge.Mutation(id, resolver); err != nil {
			t.Fatal("caller changed frozen discard scope", err)
		}
		inventory.Connections[0]["remote.address"] += "/32"
		inventory.Connections[0]["vrf"] = ""
		resolver, _ = NewAPIResolver(inventory, plan.Target, evidence)
		if _, err := bridge.Mutation(id, resolver); err != nil {
			t.Fatal("explicit native representation diverged from canonical journal meaning", err)
		}
		inventory.Connections[0]["remote.as"] = "64599"
		resolver, _ = NewAPIResolver(inventory, plan.Target, evidence)
		if _, err := bridge.Mutation(id, resolver); err == nil {
			t.Fatal("fresh resolver with changed attributes inherited old deletion authority")
		}
	}
}

func TestPreparedDiscardBridgeCannotTargetAnExistingBaselineGeneration(t *testing.T) {
	old, peers := bootstrap(t)
	base := observed(old)
	noop, err := BuildPlan(old.Target, routerFrom(old), peers, base, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewPreparedDiscardBridge(noop, nil)
	if err != nil || len(bridge.Operations()) != 0 {
		t.Fatal("reused baseline generation became new discard authority", err)
	}
	peers[0].AllowedPrefixes = []string{"198.51.100.0/24"}
	p, err := BuildPlan(old.Target, routerFrom(old), peers, base, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]Peer{}
	for _, peer := range p.Desired.Peers {
		known[peer.ID] = peer
	}
	objects := []PreparedDiscardObject{}
	for _, op := range p.Operations {
		if op.Phase != Prepare || op.Kind != AddRule {
			continue
		}
		body, err := compileRule(*op.Rule, known)
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, PreparedDiscardObject{PrepareOperationID: op.ID, Resource: api.FilterRule, ID: fmt.Sprintf("*%X", 256+len(objects)), Attributes: api.Record{"chain": op.Rule.Chain, "comment": op.Rule.Comment, "rule": body, "disabled": "false", "inactive": "false"}})
	}
	if len(objects) == 0 {
		t.Fatal("fixture has no new prepared rules")
	}
	if _, err = NewPreparedDiscardBridge(p, objects); err != nil {
		t.Fatal(err)
	}
	objects[0].ID = base.Generations[0].Rules[0].ID
	if _, err = NewPreparedDiscardBridge(p, objects); err == nil {
		t.Fatal("discard could remove an original baseline rule")
	}
}

func TestPreparedDiscardOrderWaitsForEverySharedChainReferrer(t *testing.T) {
	adds, indices := map[string]Operation{}, map[string]int{}
	const count = 3000
	for i := 0; i < count; i++ {
		ref, target := fmt.Sprintf("ref-%d", i), fmt.Sprintf("target-%d", i)
		adds[ref] = Operation{ID: ref, Phase: Prepare, Kind: AddRule, Rule: &Rule{Chain: "recipients", JumpTarget: "shared-source"}}
		adds[target] = Operation{ID: target, Phase: Prepare, Kind: AddRule, Rule: &Rule{Chain: "shared-source"}}
		indices[ref], indices[target] = i, count+i
	}
	ordered, err := preparedDiscardOrder(adds, indices)
	if err != nil || len(ordered) != 2*count {
		t.Fatal("shared-chain deletion order missing objects", err)
	}
	remaining := count
	for _, op := range ordered {
		if op.Rule.JumpTarget != "" {
			remaining--
		} else if remaining != 0 {
			t.Fatal("shared chain was removed while another recipient still referenced it")
		}
	}
	adds = map[string]Operation{"a": {ID: "a", Kind: AddRule, Rule: &Rule{Chain: "a", JumpTarget: "b"}}, "b": {ID: "b", Kind: AddRule, Rule: &Rule{Chain: "b", JumpTarget: "a"}}}
	if _, err := preparedDiscardOrder(adds, map[string]int{"a": 0, "b": 1}); err == nil {
		t.Fatal("cyclic deletion graph accepted")
	}
}
