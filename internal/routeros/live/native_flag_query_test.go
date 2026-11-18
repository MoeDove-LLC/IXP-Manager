package live

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros/api"
)

// Fixed disposable CHR probe only. This does not enable a production observer
// shortcut; both predicates must agree with explicit getters and exact IDs.
func TestNativeCHRLiveBooleanQuery(t *testing.T) {
	cfg := nativeLiveConfig(t)
	cfg.RouterID = "native-boolean-query"
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	r, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("flag_query_open=false code=%s", liveCode(err))
	}
	defer r.Close()
	reader, ok := r.api.(interface {
		ReadFilterRuleDisabled(context.Context, bool) ([]string, error)
	})
	if !ok {
		t.Fatal("flag_query_reader=false")
	}
	before, err := r.api.Inventory(ctx)
	if err != nil || len(before.FilterRules) != 0 || before.SafeMode == nil || before.SafeMode.Enabled {
		t.Fatal("flag_query_empty_baseline=false")
	}
	ids := []string{}
	for i := 0; i < 3; i++ {
		attrs := []api.Attribute{{Name: "chain", Value: "md_query_flags"}, {Name: "rule", Value: "reject;"}, {Name: "comment", Value: "md-query-flags"}}
		if i > 0 {
			value := "no"
			if i == 2 {
				value = "yes"
			}
			attrs = append(attrs, api.Attribute{Name: "disabled", Value: value})
		}
		result, e := r.api.Apply(ctx, api.Mutation{Resource: api.FilterRule, Action: api.Add, Attributes: attrs})
		if e != nil || !result.Acknowledged || result.ID == "" {
			t.Fatal("flag_query_fixture_add=false")
		}
		ids = append(ids, strings.ToUpper(result.ID))
	}
	check := func(mode string, want []bool) {
		t.Helper()
		for i, id := range ids {
			v, e := r.api.ReadProperty(ctx, api.FilterRule, id, "disabled")
			value := "false"
			if want[i] {
				value = "true"
			}
			if e != nil || !v.Known || v.Value != value {
				t.Fatal("flag_query_getter_exact=false")
			}
		}
		for _, value := range []bool{false, true} {
			rows, e := reader.ReadFilterRuleDisabled(ctx, value)
			if e != nil {
				t.Fatal("flag_query_predicate_completed=false")
			}
			expected := map[string]bool{}
			for i, id := range ids {
				if want[i] == value {
					expected[id] = true
				}
			}
			if len(rows) != len(expected) {
				t.Fatal("flag_query_membership_exact=false")
			}
			for _, id := range rows {
				if !expected[id] {
					t.Fatal("flag_query_membership_exact=false")
				}
				delete(expected, id)
			}
			if len(expected) != 0 {
				t.Fatal("flag_query_membership_exact=false")
			}
		}
		t.Logf("flag_query_mode=%s flag_query_getter_exact=true flag_query_membership_exact=true", mode)
	}
	check("initial", []bool{false, false, true})
	for i, action := range []api.Action{api.Disable, api.Enable} {
		id := ids[0]
		if i == 1 {
			id = ids[2]
		}
		result, e := r.api.Apply(ctx, api.Mutation{Resource: api.FilterRule, Action: action, ID: id})
		if e != nil || !result.Acknowledged {
			t.Fatal("flag_query_fixture_toggle=false")
		}
	}
	check("toggled", []bool{true, false, false})
	for _, id := range ids {
		result, e := r.api.Apply(ctx, api.Mutation{Resource: api.FilterRule, Action: api.Remove, ID: id})
		if e != nil || !result.Acknowledged {
			t.Fatal("flag_query_cleanup_after_proof=false")
		}
	}
	t.Log("flag_query_cleanup_after_proof=true")
}
