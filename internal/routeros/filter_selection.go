package routeros

import (
	"strings"

	"github.com/moedove/ixp-manager/internal/routeros/api"
)

// This proof can only be consumed with the complete inventory to which its
// two selections belong. It is deliberately unavailable to single-object
// normalization and does not fabricate VersionPropertyEvidence getter calls.
func filterRuleDisabledPartition(inv api.Inventory, target Target, proof *FilterRuleDisabledSelection) (map[string]bool, error) {
	if proof == nil {
		return nil, nil
	}
	fail := func(code string) (map[string]bool, error) {
		return nil, inspectError(code, api.FilterRule, "disabled")
	}
	if target.Version != "7.24.6" || len(inv.Resources) != 1 || !observedVersionMatches(inv.Resources[0]["version"], target.Version) {
		return fail("unsupported-filter-rule-disabled-selection")
	}
	if !proof.FalseCompleted || !proof.TrueCompleted {
		return fail("incomplete-filter-rule-disabled-selection")
	}
	for _, resource := range []api.Resource{api.RoutingTable, api.BGPInstance, api.BGPConnection, api.FilterRule} {
		if inv.Capabilities[resource] != api.Supported {
			return fail("incomplete-filter-rule-disabled-selection")
		}
	}
	const maximum = MaxPrepareOperations + 20000
	if len(inv.FilterRules) > maximum || len(proof.FalseIDs) > maximum || len(proof.TrueIDs) > maximum-len(proof.FalseIDs) {
		return fail("invalid-filter-rule-disabled-selection")
	}
	actual := make(map[string]api.Record, len(inv.FilterRules))
	for _, row := range inv.FilterRules {
		rawID := row[".id"]
		if !internalID.MatchString(rawID) {
			return fail("invalid-filter-rule-disabled-selection")
		}
		id := strings.ToUpper(rawID)
		if _, duplicate := actual[id]; duplicate {
			return fail("invalid-filter-rule-disabled-selection")
		}
		actual[id] = row
	}
	selected := make(map[string]bool, len(actual))
	for _, selection := range []struct {
		ids      []string
		disabled bool
	}{{proof.FalseIDs, false}, {proof.TrueIDs, true}} {
		for _, rawID := range selection.ids {
			if !internalID.MatchString(rawID) {
				return fail("invalid-filter-rule-disabled-selection")
			}
			id := strings.ToUpper(rawID)
			row, exists := actual[id]
			_, duplicate := selected[id]
			if !exists || duplicate {
				return fail("invalid-filter-rule-disabled-selection")
			}
			if _, printed := row["disabled"]; printed {
				value, known := boolObserved(row, "disabled")
				if !known || value != selection.disabled {
					return fail("filter-rule-disabled-selection-conflict")
				}
			}
			selected[id] = selection.disabled
		}
	}
	if len(selected) != len(actual) {
		return fail("invalid-filter-rule-disabled-selection")
	}
	return selected, nil
}

func normalizeTargetProperties(inv api.Inventory, target Target, evidence TargetEvidence) (api.Inventory, error) {
	return normalizeVersionPropertiesWithSelection(inv, target, evidence.Properties, evidence.FilterRuleDisabledSelection)
}
