package routeros

import (
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"testing"
)

func TestRouteServerCompilerPinsZeroPrependAndAccountsForIt(t *testing.T) {
	first, peers := bootstrap(t)
	peers[0].MaxPrefixes++
	update, e := BuildPlan(first.Target, routerFrom(first), peers, observed(first), PlanOptions{})
	if e != nil {
		t.Fatal(e)
	}
	for _, plan := range []Plan{first, update} {
		compiled, e := Compile(plan, nil)
		if e != nil {
			t.Fatal(e)
		}
		checked := 0
		for _, command := range compiled.Commands {
			if command.Resource != BGPConnection || (command.Action != Add && command.Action != Set) {
				continue
			}
			// Authentication-only Set is independently typed and has no policy attrs.
			full := false
			for _, attribute := range command.Attributes {
				if attribute.Name == "name" {
					full = true
				}
			}
			if !full {
				continue
			}
			checked++
			zero := 0
			for _, attribute := range command.Attributes {
				if attribute.Name == "output.default-prepend" {
					if attribute.Value != "0" {
						t.Fatal("RS compiler prepends own ASN")
					}
					zero++
				}
			}
			if zero != 1 {
				t.Fatal("RS compiler omitted explicit zero prepend")
			}
			found := false
			for _, operation := range plan.Operations {
				if operation.ID == command.OperationID {
					found = true
					if operation.EstimatedActions != 20 || operation.EstimatedActions < len(command.Attributes)+1 {
						t.Fatal("new attribute missing from action budget")
					}
				}
			}
			if !found {
				t.Fatal("compiled command missing plan operation")
			}
		}
		if checked == 0 {
			t.Fatal("fixture missed add/set connection")
		}
	}
}
func TestSnapshotRequiresExplicitZeroPrepend(t *testing.T) {
	for _, mode := range []string{"missing", "one", "noncanonical", "unknown-getter"} {
		t.Run(mode, func(t *testing.T) {
			_, inv, known, evidence := inspectFixture(t)
			switch mode {
			case "missing":
				delete(inv.Connections[0], "output.default-prepend")
			case "one":
				inv.Connections[0]["output.default-prepend"] = "1"
			case "noncanonical":
				inv.Connections[0]["output.default-prepend"] = "00"
			case "unknown-getter":
				delete(inv.Connections[0], "output.default-prepend")
				evidence.Properties = []VersionPropertyEvidence{{Resource: api.BGPConnection, ID: "*1", Field: "output.default-prepend", Completed: true}}
			}
			if _, e := buildSnapshot(inv, known, evidence, matchedCredential); e == nil {
				t.Fatal("unproved/default AS prepend accepted")
			}
		})
	}
}
