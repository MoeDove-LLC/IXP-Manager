package live

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
)

// The model stores boolean truth independently from its print representation.
// Native tests, not this model, establish the actual RouterOS predicates.
type selectionAPIModel struct {
	*fakeAPI
	truth   map[string]bool
	queries int
	fault   string
}

func (a *selectionAPIModel) ReadFilterRuleDisabled(_ context.Context, disabled bool) ([]string, error) {
	a.queries++
	if a.fault == "false-query" && !disabled || a.fault == "true-query" && disabled {
		return nil, errors.New("fixture query failed")
	}
	ids := []string{}
	for id, value := range a.truth {
		if value == disabled {
			ids = append(ids, id)
		}
	}
	switch a.fault {
	case "missing":
		if !disabled {
			ids = ids[1:]
		}
	case "duplicate":
		if !disabled {
			ids = append(ids, ids[0])
		}
	case "extra":
		if !disabled {
			ids = append(ids, "*FFFF")
		}
	case "overlap":
		if disabled {
			ids = append(ids, "*2")
		}
	case "bound":
		if !disabled {
			for len(ids) <= aMaxSelectionFixtureObjects {
				ids = append(ids, "*FFFF")
			}
		}
	}
	return ids, nil
}

const aMaxSelectionFixtureObjects = 300

func selectionFixture(t *testing.T, count int) (*Runtime, *selectionAPIModel, control.ReadRequest) {
	t.Helper()
	r, base, _, request := observerFixture()
	r.config.MaxObjects = aMaxSelectionFixtureObjects
	r.config.MaxGetterCalls = 1000
	r.profile.FilterRuleDisabledSelectionVerified = true
	a := &selectionAPIModel{fakeAPI: base, truth: map[string]bool{}}
	for i := 0; i < count; i++ {
		id := fmt.Sprintf("*%X", i+1)
		value := i == 0
		a.truth[id] = value
		row := api.Record{".id": id, "chain": "external_fixture", "rule": "reject;", "inactive": "false", "comment": "fixture"}
		if i == 0 {
			row["disabled"] = "true"
		} else if i == 1 {
			row["disabled"] = "false"
		}
		base.inv.FilterRules = append(base.inv.FilterRules, row)
		base.properties["filter-rule/"+id+"/disabled"] = api.PropertyValue{Known: true, Value: fmt.Sprint(value)}
	}
	r.api = a
	request.Strict = false
	t.Cleanup(func() { _ = r.Close() })
	return r, a, request
}

func TestDisabledSelectionUsesCompletePartitionWithoutInventingGetters(t *testing.T) {
	r, a, request := selectionFixture(t, 128)
	observed, err := r.Observe(context.Background(), request)
	if err != nil || a.queries != 4 || a.calls != 2 || a.getters != 0 {
		t.Fatal("two complete observations did not use explicit predicates", err, a.queries, a.calls, a.getters)
	}
	for _, row := range observed.Inventory.FilterRules {
		if row["disabled"] != fmt.Sprint(a.truth[row[".id"]]) {
			t.Fatal("selection lost actual modeled boolean truth")
		}
	}
	_, evidence, _, _, err := r.readState(context.Background())
	if err != nil || evidence.FilterRuleDisabledSelection == nil {
		t.Fatal("distinct predicate evidence was lost", err)
	}
	for _, property := range evidence.Properties {
		if property.Resource == api.FilterRule && property.Field == "disabled" {
			t.Fatal("predicate was disguised as a getter")
		}
	}
}

func TestDisabledSelectionKeepsLegacyReadsWithoutGateOrReader(t *testing.T) {
	for _, mode := range []string{"gate", "reader", "small"} {
		t.Run(mode, func(t *testing.T) {
			count := 128
			if mode == "small" {
				count = 4
			}
			r, a, request := selectionFixture(t, count)
			if mode == "gate" {
				r.profile.FilterRuleDisabledSelectionVerified = false
			}
			if mode == "reader" {
				r.api = a.fakeAPI
			}
			if _, err := r.Observe(context.Background(), request); err != nil || a.queries != 0 || a.getters != 2*(count-2) {
				t.Fatal("fallback changed actual getter semantics", err, a.queries, a.getters)
			}
		})
	}
}

func TestDisabledSelectionFailuresNeverDefaultOrDowngrade(t *testing.T) {
	for _, fault := range []string{"false-query", "true-query", "missing", "duplicate", "extra", "overlap", "bound", "print-conflict", "between-observations"} {
		t.Run(fault, func(t *testing.T) {
			r, a, request := selectionFixture(t, 128)
			a.fault = fault
			if fault == "print-conflict" {
				a.inv.FilterRules[1]["disabled"] = "true"
			}
			if fault == "between-observations" {
				a.change = func(n int, inv *api.Inventory) {
					if n == 2 {
						a.truth["*2"] = true
						inv.FilterRules[1]["disabled"] = "true"
					}
				}
			}
			if _, err := r.Observe(context.Background(), request); err == nil || a.getters != 0 || r.writeEvidence.Load() != nil || r.removalEvidence.Load() != nil {
				t.Fatal("invalid or changing predicates became an observation")
			}
		})
	}
}

func TestDisabledSelectionEvidenceCopiesAreIndependent(t *testing.T) {
	original := routeros.TargetEvidence{FilterRuleDisabledSelection: &routeros.FilterRuleDisabledSelection{FalseCompleted: true, TrueCompleted: true, FalseIDs: []string{"*1"}, TrueIDs: []string{"*2"}}}
	copy := cloneTargetEvidence(original)
	original.FilterRuleDisabledSelection.FalseIDs[0] = "*BAD"
	original.FilterRuleDisabledSelection.TrueIDs[0] = "*BAD"
	original.FilterRuleDisabledSelection.TrueCompleted = false
	if copy.FilterRuleDisabledSelection.FalseIDs[0] != "*1" || copy.FilterRuleDisabledSelection.TrueIDs[0] != "*2" || !copy.FilterRuleDisabledSelection.TrueCompleted {
		t.Fatal("mutable caller evidence changed a retained proof")
	}
}
