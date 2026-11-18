package routeros

import (
	"reflect"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/routeros/api"
)

func selectionFixture(t *testing.T) (Plan, api.Inventory, KnownState, TargetEvidence) {
	t.Helper()
	plan, inventory, known, evidence := inspectFixture(t)
	selection := &FilterRuleDisabledSelection{FalseCompleted: true, TrueCompleted: true}
	for _, row := range inventory.FilterRules {
		selection.FalseIDs = append(selection.FalseIDs, row[".id"])
		delete(row, "disabled")
	}
	evidence.FilterRuleDisabledSelection = selection
	return plan, inventory, known, evidence
}

func TestFilterDisabledSelectionFillsOnlyCompleteInventoryWithoutFakeGetters(t *testing.T) {
	plan, inventory, known, evidence := selectionFixture(t)
	before, proofBefore := hash(inventory), hash(evidence.FilterRuleDisabledSelection)
	normalized, err := NormalizeObservedInventory(inventory, plan.Target, evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range normalized.FilterRules {
		if row["disabled"] != "false" {
			t.Fatal("completed false partition was not normalized")
		}
	}
	if len(evidence.Properties) != 0 || hash(inventory) != before || hash(evidence.FilterRuleDisabledSelection) != proofBefore {
		t.Fatal("selection fabricated getters or mutated caller evidence")
	}
	if _, err := NewAPIResolver(inventory, plan.Target, evidence); err != nil {
		t.Fatal("resolver did not use the same complete-inventory proof", err)
	}
	snapshot, err := buildSnapshot(inventory, known, evidence, matchedCredential)
	if err != nil || !snapshot.Managed {
		t.Fatal("strict inspection did not consume independent selection evidence", err)
	}
	normalized.FilterRules[0]["rule"] = "mutated-result"
	if inventory.FilterRules[0]["rule"] == "mutated-result" {
		t.Fatal("normalized row aliased the original read")
	}
	plain, err := NormalizeObservedObject(api.FilterRule, inventory.FilterRules[0], plan.Target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, inferred := plain["disabled"]; inferred {
		t.Fatal("full-menu partition leaked into a single-object read")
	}
}

func TestFilterDisabledSelectionPreservesExplicitTrueFalseAndOtherUnknownFields(t *testing.T) {
	plan, inventory, _, evidence := selectionFixture(t)
	first, second := inventory.FilterRules[0], inventory.FilterRules[1]
	first["disabled"], second["disabled"] = "no", "yes"
	selection := evidence.FilterRuleDisabledSelection
	selection.TrueIDs = []string{strings.ToLower(second[".id"])}
	selection.FalseIDs = append(selection.FalseIDs[:1], selection.FalseIDs[2:]...)
	delete(inventory.Tables[0], "fib")
	delete(inventory.Instances[0], "disabled")
	delete(inventory.Connections[0], "disabled")
	delete(first, "inactive")
	normalized, err := NormalizeObservedInventory(inventory, plan.Target, evidence)
	if err != nil || normalized.FilterRules[0]["disabled"] != "false" || normalized.FilterRules[1]["disabled"] != "true" {
		t.Fatal("explicit flags or exact native ID case normalization changed", err)
	}
	for _, field := range []struct {
		row api.Record
		key string
	}{
		{normalized.Tables[0], "fib"}, {normalized.Instances[0], "disabled"}, {normalized.Connections[0], "disabled"}, {normalized.FilterRules[0], "inactive"},
	} {
		if _, guessed := field.row[field.key]; guessed {
			t.Fatal("filter disabled partition filled another resource or attribute")
		}
	}
}

func TestFilterDisabledSelectionRejectsIncompleteAmbiguousOrUnversionedProof(t *testing.T) {
	for _, change := range []string{"false-not-complete", "true-not-complete", "missing", "extra", "overlap", "duplicate-false", "duplicate-true", "invalid-id", "duplicate-inventory", "invalid-inventory-id", "print-true-conflict", "print-false-conflict", "print-unknown", "print-empty", "wrong-version", "wrong-actual-version", "partial-menu", "oversize", "unauthenticated"} {
		t.Run(change, func(t *testing.T) {
			plan, inventory, _, evidence := selectionFixture(t)
			selection := evidence.FilterRuleDisabledSelection
			switch change {
			case "false-not-complete":
				selection.FalseCompleted = false
			case "true-not-complete":
				selection.TrueCompleted = false
			case "missing":
				selection.FalseIDs = selection.FalseIDs[1:]
			case "extra":
				selection.FalseIDs = append(selection.FalseIDs, "*FFFF")
			case "overlap":
				selection.TrueIDs = []string{selection.FalseIDs[0]}
			case "duplicate-false":
				selection.FalseIDs = append(selection.FalseIDs, strings.ToLower(selection.FalseIDs[0]))
			case "duplicate-true":
				id := selection.FalseIDs[0]
				selection.FalseIDs = selection.FalseIDs[1:]
				selection.TrueIDs = []string{id, strings.ToLower(id)}
			case "invalid-id":
				selection.FalseIDs[0] = "0"
			case "duplicate-inventory":
				inventory.FilterRules = append(inventory.FilterRules, inventory.FilterRules[0])
			case "invalid-inventory-id":
				inventory.FilterRules[0][".id"] = "*A,*B"
			case "print-true-conflict":
				inventory.FilterRules[0]["disabled"] = "true"
			case "print-false-conflict":
				inventory.FilterRules[0]["disabled"] = "false"
				selection.TrueIDs = []string{selection.FalseIDs[0]}
				selection.FalseIDs = selection.FalseIDs[1:]
			case "print-unknown":
				inventory.FilterRules[0]["disabled"] = "maybe"
			case "print-empty":
				inventory.FilterRules[0]["disabled"] = ""
			case "wrong-version":
				plan.Target.Version, inventory.Resources[0]["version"] = "7.24.7", "7.24.7"
			case "wrong-actual-version":
				inventory.Resources[0]["version"] = "7.24.6 (stable)"
			case "partial-menu":
				delete(inventory.Capabilities, api.BGPConnection)
			case "oversize":
				selection.FalseIDs = make([]string, MaxPrepareOperations+20001)
			case "unauthenticated":
				evidence.APIAuthenticated = false
			}
			if _, err := NormalizeObservedInventory(inventory, plan.Target, evidence); err == nil {
				t.Fatal("invalid selection became normalized flags")
			}
		})
	}
}

func TestFilterDisabledSelectionChecksEveryRuleAndDoesNotReuseSubsetProof(t *testing.T) {
	plan, inventory, _, evidence := selectionFixture(t)
	foreign := api.Record{".id": "*FFFF", "chain": "unmanaged", "rule": "reject;", "comment": "", "inactive": "false"}
	inventory.FilterRules = append(inventory.FilterRules, foreign)
	if _, err := NormalizeObservedInventory(inventory, plan.Target, evidence); err == nil {
		t.Fatal("foreign rule outside the selection was ignored")
	}
	evidence.FilterRuleDisabledSelection.FalseIDs = append(evidence.FilterRuleDisabledSelection.FalseIDs, foreign[".id"])
	if _, err := NormalizeObservedInventory(inventory, plan.Target, evidence); err != nil {
		t.Fatal("complete selection including foreign IDs was rejected", err)
	}
	inventory.FilterRules = inventory.FilterRules[:len(inventory.FilterRules)-1]
	if _, err := NormalizeObservedInventory(inventory, plan.Target, evidence); err == nil {
		t.Fatal("full selection was reused with a projected subset")
	}
}

func TestFilterDisabledSelectionCrossChecksRealGetterAndRequiresAllOtherEvidence(t *testing.T) {
	plan, inventory, known, evidence := selectionFixture(t)
	id := inventory.FilterRules[0][".id"]
	evidence.Properties = []VersionPropertyEvidence{{Resource: api.FilterRule, ID: id, Field: "disabled", Completed: true, Property: api.PropertyValue{Known: true, Value: "false"}}}
	if _, err := NormalizeObservedInventory(inventory, plan.Target, evidence); err != nil {
		t.Fatal("consistent independent getter was rejected", err)
	}
	evidence.Properties[0].Property.Value = "true"
	if _, err := NormalizeObservedInventory(inventory, plan.Target, evidence); err == nil {
		t.Fatal("getter contradiction was hidden by selection")
	}
	evidence.Properties = nil
	delete(inventory.FilterRules[0], "inactive")
	if _, err := buildSnapshot(inventory, known, evidence, matchedCredential); err == nil {
		t.Fatal("disabled selection fabricated inactive validity")
	}
	_, empty, emptyKnown, emptyEvidence := inspectFixture(t)
	empty.Tables, empty.Instances, empty.Connections, empty.FilterRules = nil, nil, nil, nil
	emptyKnown = KnownState{Target: plan.Target}
	emptyEvidence.FilterRuleDisabledSelection = &FilterRuleDisabledSelection{FalseCompleted: true, TrueCompleted: true}
	snapshot, err := buildSnapshot(empty, emptyKnown, emptyEvidence, matchedCredential)
	if err != nil || !snapshot.NamespaceEmpty {
		t.Fatal("two completed empty selections did not prove an empty full menu", err)
	}
	other := emptyEvidence
	other.FilterRuleDisabledSelection = nil
	with, err := NormalizeObservedInventory(empty, plan.Target, emptyEvidence)
	without, otherErr := NormalizeObservedInventory(empty, plan.Target, other)
	if err != nil || otherErr != nil || !reflect.DeepEqual(with, without) {
		t.Fatal("empty selection changed unrelated evidence")
	}
}
