package live

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routing"
)

type nativeSelectionScaleAPI struct {
	*nativeReadScaleAPI
	disabledGetters, queries, falseQueries, trueQueries int64
	falseIDs, trueIDs, queryNanos                       int64
	toggle                                              bool
	toggles                                             []api.Mutation
	toggleIndex                                         int
}

func (a *nativeSelectionScaleAPI) Apply(ctx context.Context, mutation api.Mutation) (api.Result, error) {
	if a.setup {
		return a.nativeReadScaleAPI.Apply(ctx, mutation)
	}
	if !a.toggle || a.toggleIndex >= len(a.toggles) || !reflect.DeepEqual(mutation, a.toggles[a.toggleIndex]) {
		return api.Result{}, a.guard.deny()
	}
	a.toggleIndex++
	a.writes++
	started := time.Now()
	result, err := a.base.Apply(ctx, mutation)
	a.writeNanos += time.Since(started).Nanoseconds()
	if err != nil {
		a.logError(ctx, mutation.Resource, err)
	}
	return result, err
}

func (a *nativeSelectionScaleAPI) ReadFilterRuleDisabled(ctx context.Context, disabled bool) ([]string, error) {
	reader, ok := a.base.(filterRuleDisabledReader)
	if !ok {
		return nil, fail("native-selection-reader-unavailable")
	}
	a.queries++
	if disabled {
		a.trueQueries++
	} else {
		a.falseQueries++
	}
	started := time.Now()
	ids, err := reader.ReadFilterRuleDisabled(ctx, disabled)
	a.queryNanos += time.Since(started).Nanoseconds()
	if err != nil {
		a.logError(ctx, api.FilterRule, err)
	} else if disabled {
		a.trueIDs += int64(len(ids))
	} else {
		a.falseIDs += int64(len(ids))
	}
	return ids, err
}

func (a *nativeSelectionScaleAPI) ReadProperty(ctx context.Context, resource api.Resource, id, field string) (api.PropertyValue, error) {
	if resource == api.FilterRule && field == "disabled" {
		a.disabledGetters++
	}
	return a.nativeExtensionAPI.ReadProperty(ctx, resource, id, field)
}
func (a *nativeSelectionScaleAPI) ReadProperties(ctx context.Context, requests []api.PropertyRequest) ([]api.PropertyValue, error) {
	for _, request := range requests {
		if request.Resource == api.FilterRule && request.Property == "disabled" {
			a.disabledGetters++
		}
	}
	return a.nativeExtensionAPI.ReadProperties(ctx, requests)
}

var _ filterRuleDisabledReader = (*nativeSelectionScaleAPI)(nil)

type nativeSelectionStats struct{ values [15]int64 }

func (a *nativeSelectionScaleAPI) selectionMeasure() nativeSelectionStats {
	return nativeSelectionStats{[15]int64{a.inventories.Load(), a.properties.Load(), a.getterBatches.Load(), a.disabledGetters,
		a.queries, a.falseQueries, a.trueQueries, a.falseIDs, a.trueIDs, a.writes, a.credentials,
		a.inventoryNanos.Load(), a.getterNanos.Load(), a.queryNanos, a.writeNanos}}
}
func (a *nativeSelectionScaleAPI) selectionLog(phase string, started time.Time, before nativeSelectionStats) {
	stats := a.selectionMeasure()
	for i := range stats.values {
		stats.values[i] -= before.values[i]
	}
	v := stats.values
	a.t.Logf("selection_scale_phase=%s selection_scale_milliseconds=%d selection_scale_inventory_calls=%d selection_scale_property_items=%d selection_scale_property_batches=%d selection_scale_disabled_getters=%d selection_scale_predicate_calls=%d selection_scale_false_queries=%d selection_scale_true_queries=%d selection_scale_false_ids_total=%d selection_scale_true_ids_total=%d selection_scale_api_writes=%d selection_scale_credential_reads=%d selection_scale_inventory_milliseconds=%d selection_scale_getter_milliseconds=%d selection_scale_predicate_milliseconds=%d selection_scale_write_milliseconds=%d", phase, time.Since(started).Milliseconds(), v[0], v[1], v[2], v[3], v[4], v[5], v[6], v[7], v[8], v[9], v[10], time.Duration(v[11]).Milliseconds(), time.Duration(v[12]).Milliseconds(), time.Duration(v[13]).Milliseconds(), time.Duration(v[14]).Milliseconds())
}

// Compare every normalized configuration property and its ordering. Identity
// is verified by each actual read. Volatile system uptime/free-memory are not
// configuration attributes and are deliberately not compared as configuration.
func nativeSelectionConfigEqual(left, right api.Inventory) bool {
	return reflect.DeepEqual(menus(left), menus(right)) && reflect.DeepEqual(left.Capabilities, right.Capabilities) && reflect.DeepEqual(left.SafeMode, right.SafeMode)
}

func TestNativeCHRLiveDisabledSelectionScaleCompare(t *testing.T) {
	cfg := nativeLiveConfig(t)
	cfg.RouterID = "native-large-selection-compare"
	cfg.SessionLifetimeSeconds, cfg.ObservationTimeoutSeconds, cfg.OperationTimeoutSeconds = 600, 180, 15
	cfg.MaxObjects, cfg.MaxGetterCalls = 10000, 50000
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	startedAll := time.Now()
	defer func() { t.Logf("selection_scale_total_milliseconds=%d", time.Since(startedAll).Milliseconds()) }()
	t.Log("selection_scale_variant=true")
	r, err := Open(ctx, cfg)
	if err != nil {
		nativeReadScaleFailure(t, "selection_scale_open", err)
	}
	defer r.Close()
	profile := CapabilityProfile()
	if !profile.FilterRuleDisabledSelectionVerified || profile.PreparationVerified || profile.ActivationVerified || profile.ObjectObservationVerified || profile.RuleBatchVerified || profile.RuleRemovalBatchVerified || profile.RuleRemovalGlobalTailVerified || profile.CleanupVerified || profile.PreparedDiscardVerified || profile.MaxHistoryActions != 0 {
		t.Fatal("selection_scale_gates_unchanged=false")
	}
	known := routeros.KnownState{Target: r.target}
	initial, err := r.Observe(ctx, control.ReadRequest{KnownState: known, Strict: true})
	if err != nil || initial.Snapshot == nil || !initial.Snapshot.NamespaceEmpty || len(initial.Inventory.FilterRules) != 0 || len(initial.Inventory.Instances) != 0 || len(initial.Inventory.Connections) != 0 || !initial.Owner.Known || !initial.Owner.Alive || initial.Owner.Active || !initial.Owner.HistoryKnown || initial.Owner.HistoryActions != 0 {
		nativeReadScaleFailure(t, "selection_scale_initial_empty", err)
	}
	prefixes := make([]string, 1000)
	for i := range prefixes {
		prefixes[i] = fmt.Sprintf("198.18.%d.%d/32", i/256, i%256)
	}
	router := routing.Router{ID: r.target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1", LocalIPv6: "2001:db8::1"}
	plan, err := routeros.BuildPlan(r.target, router, []routing.Peer{{ID: "batch-peer", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 2000, AllowedPrefixes: prefixes}}, *initial.Snapshot, routeros.PlanOptions{Bootstrap: true})
	if err != nil {
		nativeReadScaleFailure(t, "selection_scale_plan", err)
	}
	compiled, err := routeros.Compile(plan, nil)
	if err != nil {
		nativeReadScaleFailure(t, "selection_scale_plan", err)
	}
	generation := map[string]bool{}
	for _, op := range plan.Operations {
		if generationRule(&plan, op) {
			generation[op.ID] = true
		}
	}
	guard := &nativeDiscardCounters{}
	a := &nativeSelectionScaleAPI{nativeReadScaleAPI: &nativeReadScaleAPI{nativeExtensionAPI: &nativeExtensionAPI{base: r.api, t: t}, setup: true, guard: guard}}
	var operations []routeros.CompiledCommand
	genIndex := 0
	for _, command := range compiled.Commands {
		if command.Phase != routeros.Prepare || command.Action != routeros.Add || command.Resource == routeros.BGPConnection {
			continue
		}
		mutation := api.Mutation{Resource: api.Resource(command.Resource), Action: api.Add, Attributes: []api.Attribute{}}
		for _, attribute := range command.Attributes {
			if attribute.Sensitive {
				t.Fatal("selection_scale_plan=false")
			}
			mutation.Attributes = append(mutation.Attributes, api.Attribute{Name: attribute.Name, Value: attribute.Value})
		}
		if generation[command.OperationID] {
			if genIndex < 2 {
				value := "no"
				if genIndex == 1 {
					value = "yes"
				}
				mutation.Attributes = append(mutation.Attributes, api.Attribute{Name: "disabled", Value: value})
			}
			genIndex++
		}
		if api.ValidateMutation(mutation) != nil {
			t.Fatal("selection_scale_plan=false")
		}
		a.approved = append(a.approved, mutation)
		operations = append(operations, command)
	}
	if len(a.approved) != 2008 || genIndex != 2004 {
		t.Fatal("selection_scale_plan=false")
	}
	r.api, r.terminal = a, &nativeDiscardReadOnlyTerminal{terminalChannel: r.terminal, counts: guard}
	expected, generationIDs := map[string]api.Mutation{}, []string{}
	started, before := time.Now(), a.selectionMeasure()
	for i, mutation := range a.approved {
		result, err := a.Apply(ctx, mutation)
		key := cleanupKey(mutation.Resource, result.ID)
		_, duplicate := expected[key]
		if err != nil || !result.Acknowledged || !nativeID.MatchString(result.ID) || duplicate {
			a.selectionLog("setup", started, before)
			nativeReadScaleFailure(t, "selection_scale_setup", err)
		}
		expected[key] = mutation
		if generation[operations[i].OperationID] {
			generationIDs = append(generationIDs, result.ID)
		}
	}
	a.setup = false
	a.selectionLog("setup", started, before)
	a.toggles = []api.Mutation{{Resource: api.FilterRule, Action: api.Disable, ID: generationIDs[0]}, {Resource: api.FilterRule, Action: api.Enable, ID: generationIDs[1]}}
	previousDigest := ""
	compare := func(mode string, wantTrue string) {
		t.Helper()
		r.profile.FilterRuleDisabledSelectionVerified = false
		started, before := time.Now(), a.selectionMeasure()
		call, done := context.WithTimeout(ctx, 180*time.Second)
		_, getterEvidence, getter, getterDigest, err := r.readState(call)
		done()
		a.selectionLog(mode+"-getter", started, before)
		if err != nil || getterEvidence.FilterRuleDisabledSelection != nil || a.disabledGetters-before.values[3] == 0 || a.queries != before.values[4] {
			nativeReadScaleFailure(t, "selection_scale_getter_read", err)
		}
		if len(getter.FilterRules) != 2006 || len(getter.Connections) != 0 || len(getter.Instances) != 1 {
			t.Fatal("selection_scale_scope=false")
		}
		actual := map[string]api.Record{}
		trueCount := 0
		for _, menu := range menus(getter) {
			for _, row := range menu.rows {
				actual[cleanupKey(menu.resource, row[".id"])] = row
				if menu.resource == api.FilterRule {
					wanted := "false"
					if row[".id"] == wantTrue {
						wanted = "true"
						trueCount++
					}
					if row["disabled"] != wanted {
						t.Fatal("selection_scale_explicit_flags=false")
					}
				}
			}
		}
		if trueCount != 1 {
			t.Fatal("selection_scale_explicit_flags=false")
		}
		for key, mutation := range expected {
			row := actual[key]
			if row == nil {
				t.Fatal("selection_scale_scope=false")
			}
			if mutation.Resource == api.FilterRule {
				for _, attribute := range mutation.Attributes {
					if attribute.Name != "disabled" && row[attribute.Name] != attribute.Value {
						t.Fatal("selection_scale_body_verified=false")
					}
				}
			}
			if mutation.Resource == api.RoutingTable && row["fib"] != "false" {
				t.Fatal("selection_scale_scope=false")
			}
			if mutation.Resource == api.FilterRule && (row["chain"] == plan.Owner+"_root_in" || row["chain"] == plan.Owner+"_root_out") && row["rule"] != "reject;" {
				t.Fatal("selection_scale_scope=false")
			}
		}
		r.profile.FilterRuleDisabledSelectionVerified = true
		started, before = time.Now(), a.selectionMeasure()
		call, done = context.WithTimeout(ctx, 180*time.Second)
		_, selectionEvidence, selected, selectionDigest, err := r.readState(call)
		done()
		a.selectionLog(mode+"-selection", started, before)
		proof := selectionEvidence.FilterRuleDisabledSelection
		if err != nil || proof == nil || !proof.FalseCompleted || !proof.TrueCompleted || len(proof.FalseIDs) != 2005 || len(proof.TrueIDs) != 1 || a.disabledGetters != before.values[3] || a.queries-before.values[4] != 2 {
			nativeReadScaleFailure(t, "selection_scale_partition_exact", err)
		}
		if !nativeSelectionConfigEqual(getter, selected) || getterDigest != selectionDigest {
			t.Fatal("selection_scale_inventory_equal=false")
		}
		started, before = time.Now(), a.selectionMeasure()
		observed, err := r.Observe(ctx, control.ReadRequest{KnownState: known})
		a.selectionLog(mode+"-observe", started, before)
		if err != nil || observed.Snapshot != nil || !observed.CrossChannelIdentityVerified || len(observed.Credentials) != 0 || !observed.Owner.Known || !observed.Owner.Alive || observed.Owner.Active || !observed.Owner.HistoryKnown || observed.Owner.HistoryActions != 0 || a.queries-before.values[4] != 4 || a.disabledGetters != before.values[3] {
			nativeReadScaleFailure(t, "selection_scale_observe_equal", err)
		}
		digest, err := stateDigest(observed.Inventory, r.target)
		if err != nil || digest != getterDigest || !nativeSelectionConfigEqual(getter, observed.Inventory) {
			t.Fatal("selection_scale_observe_equal=false")
		}
		if mode == "toggled" && previousDigest == getterDigest {
			t.Fatal("selection_scale_toggle_verified=false")
		}
		previousDigest = getterDigest
		t.Logf("selection_scale_mode=%s selection_scale_inventory_equal=true selection_scale_digest_equal=true selection_scale_observe_equal=true selection_scale_partition_exact=true selection_scale_explicit_flags=true selection_scale_body_verified=true", mode)
	}
	compare("initial", generationIDs[1])
	started, before = time.Now(), a.selectionMeasure()
	a.toggle = true
	for _, mutation := range a.toggles {
		result, err := a.Apply(ctx, mutation)
		if err != nil || !result.Acknowledged || result.ID != "" {
			a.toggle = false
			a.selectionLog("toggle", started, before)
			nativeReadScaleFailure(t, "selection_scale_toggle_verified", err)
		}
	}
	a.toggle = false
	a.selectionLog("toggle", started, before)
	compare("toggled", generationIDs[0])
	r.profile.FilterRuleDisabledSelectionVerified = profile.FilterRuleDisabledSelectionVerified
	owner, err := r.Owner(ctx)
	if err != nil || !owner.Known || !owner.Alive || owner.Active || owner.ThisSession || !owner.HistoryKnown || owner.HistoryActions != 0 || owner.SessionID != initial.Owner.SessionID || a.writes != 2010 || a.credentials != 0 || guard.forbidden.Load() != 0 || a.toggleIndex != 2 || !reflect.DeepEqual(profile, CapabilityProfile()) || !reflect.DeepEqual(profile, r.profile) {
		nativeReadScaleFailure(t, "selection_scale_final_guards", err)
	}
	t.Log("selection_scale_generation_rules=2004 selection_scale_setup_adds=2008 selection_scale_toggle_writes=2 selection_scale_unique_ids=2008 selection_scale_forbidden_calls=0 selection_scale_toggle_verified=true selection_scale_gates_unchanged=true selection_scale_source_only=true selection_scale_no_connections=true selection_scale_no_active_roots=true selection_scale_publication_verified=false selection_scale_rollback_verified=false")
}

func TestNativeSelectionScaleGuardRequiresExplicitToggleWindowAndIDs(t *testing.T) {
	base := &fakeAPI{}
	guard := &nativeDiscardCounters{}
	a := &nativeSelectionScaleAPI{nativeReadScaleAPI: &nativeReadScaleAPI{nativeExtensionAPI: &nativeExtensionAPI{base: base, t: t}, guard: guard}, toggles: []api.Mutation{{Resource: api.FilterRule, Action: api.Disable, ID: "*A"}}}
	if _, err := a.Apply(context.Background(), a.toggles[0]); err == nil || base.writes != 0 {
		t.Fatal("read-only phase permitted a toggle")
	}
	a.toggle = true
	if _, err := a.Apply(context.Background(), api.Mutation{Resource: api.FilterRule, Action: api.Disable, ID: "*B"}); err == nil || base.writes != 0 {
		t.Fatal("toggle accepted a foreign native ID")
	}
	if _, err := a.Apply(context.Background(), a.toggles[0]); err != nil || base.writes != 1 {
		t.Fatal("explicit fixed toggle was not forwarded")
	}
	if _, err := a.Apply(context.Background(), a.toggles[0]); err == nil || base.writes != 1 {
		t.Fatal("toggle was repeated")
	}
}
