package live

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routing"
)

type nativeReadScaleAPI struct {
	*nativeExtensionAPI
	approved                  []api.Mutation
	setup                     bool
	next, writes, credentials int64
	presence, presenceNanos   int64
	writeNanos                int64
	guard                     *nativeDiscardCounters
}

func (a *nativeReadScaleAPI) Apply(ctx context.Context, mutation api.Mutation) (api.Result, error) {
	if !a.setup || a.next >= int64(len(a.approved)) || mutation.Action != api.Add || mutation.Resource != api.FilterRule && mutation.Resource != api.RoutingTable && mutation.Resource != api.BGPInstance || !reflect.DeepEqual(mutation, a.approved[a.next]) {
		return api.Result{}, a.guard.deny()
	}
	a.next++ // an uncertain attempt is never eligible for another send
	a.writes++
	started := time.Now()
	result, err := a.base.Apply(ctx, mutation)
	a.writeNanos += time.Since(started).Nanoseconds()
	if err != nil {
		a.logError(ctx, mutation.Resource, err)
	}
	return result, err
}

func (a *nativeReadScaleAPI) ReadObjectPresence(ctx context.Context, resource api.Resource, id string) (api.ObjectPresence, error) {
	reader, ok := a.base.(presenceReader)
	if !ok {
		return api.ObjectPresence{}, fail("native-read-scale-presence-unavailable")
	}
	a.presence++
	started := time.Now()
	value, err := reader.ReadObjectPresence(ctx, resource, id)
	a.presenceNanos += time.Since(started).Nanoseconds()
	if err != nil {
		a.logError(ctx, resource, err)
	}
	return value, err
}

func (a *nativeReadScaleAPI) ReadCredential(context.Context, string) (api.Credential, error) {
	a.credentials++
	return api.Credential{}, a.guard.deny() // this fixture creates no peer
}

func (a *nativeReadScaleAPI) logError(ctx context.Context, resource api.Resource, err error) {
	code, kind, limit := "unclassified", "none", "none"
	deadline := false
	size, records, sentences := 0, 0, 0
	var failure *api.Error
	if errors.As(err, &failure) {
		code, deadline, size, records, sentences = string(failure.Code), failure.Deadline, failure.ReplyBytes, failure.ReplyRecords, failure.ReplySentences
		if failure.DeadlineKind != "" {
			kind = failure.DeadlineKind
		}
		if failure.LimitKind != "" {
			limit = failure.LimitKind
		}
	}
	contextErr := ctx.Err()
	a.t.Logf("read_scale_api_resource=%s api_code=%s deadline=%t deadline_kind=%s limit_kind=%s reply_bytes=%d reply_records=%d reply_sentences=%d read_scale_context_done=%t read_scale_context_deadline=%t", resource, code, deadline, kind, limit, size, records, sentences, contextErr != nil, errors.Is(contextErr, context.DeadlineExceeded))
}

type nativeReadScaleStats struct{ values [11]int64 }

func (a *nativeReadScaleAPI) measure() nativeReadScaleStats {
	return nativeReadScaleStats{[11]int64{a.inventories.Load(), a.properties.Load(), a.getterBatches.Load(), a.presence, a.safeReads.Load(), a.credentials, a.writes, a.inventoryNanos.Load(), a.getterNanos.Load(), a.presenceNanos, a.writeNanos}}
}

func (a *nativeReadScaleAPI) logPhase(phase string, started time.Time, before nativeReadScaleStats) {
	stats := a.measure()
	for i := range stats.values {
		stats.values[i] -= before.values[i]
	}
	v := stats.values
	a.t.Logf("read_scale_phase=%s read_scale_milliseconds=%d read_scale_inventory_calls=%d read_scale_property_items=%d read_scale_property_batches=%d read_scale_presence_reads=%d read_scale_owner_reads=%d read_scale_credential_reads=%d read_scale_api_writes=%d read_scale_inventory_milliseconds=%d read_scale_getter_milliseconds=%d read_scale_presence_milliseconds=%d read_scale_write_milliseconds=%d", phase, time.Since(started).Milliseconds(), v[0], v[1], v[2], v[3], v[4], v[5], v[6], time.Duration(v[7]).Milliseconds(), time.Duration(v[8]).Milliseconds(), time.Duration(v[9]).Milliseconds(), time.Duration(v[10]).Milliseconds())
}

func nativeReadScaleFailure(t *testing.T, field string, err error) {
	t.Helper()
	t.Fatalf("%s=false code=%s", field, liveCode(err))
}

// This is an explicitly selected fixture setup and read-performance probe.
// The direct Add commands create inert configuration only. No journal,
// production write authority, Safe Mode, rollback or removal is fabricated.
func TestNativeCHRLiveLargeReadPerformance(t *testing.T) {
	cfg := nativeLiveConfig(t)
	cfg.RouterID = "native-large-read-performance"
	cfg.SessionLifetimeSeconds, cfg.ObservationTimeoutSeconds, cfg.OperationTimeoutSeconds = 600, 180, 15
	cfg.MaxObjects, cfg.MaxGetterCalls = 10000, 50000
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	startedAll := time.Now()
	defer func() { t.Logf("read_scale_total_milliseconds=%d", time.Since(startedAll).Milliseconds()) }()
	r, err := Open(ctx, cfg)
	if err != nil {
		nativeReadScaleFailure(t, "read_scale_open", err)
	}
	defer r.Close()
	profile := r.profile
	known := routeros.KnownState{Target: r.target}
	initial, err := r.Observe(ctx, control.ReadRequest{KnownState: known, Strict: true})
	if err != nil || initial.Snapshot == nil || !initial.Snapshot.NamespaceEmpty || len(initial.Inventory.FilterRules) != 0 || len(initial.Inventory.Instances) != 0 || len(initial.Inventory.Connections) != 0 || !initial.Owner.Known || !initial.Owner.Alive || initial.Owner.Active || !initial.Owner.HistoryKnown || initial.Owner.HistoryActions != 0 {
		nativeReadScaleFailure(t, "read_scale_initial_empty", err)
	}
	prefixes := make([]string, 1000)
	for i := range prefixes {
		prefixes[i] = fmt.Sprintf("198.18.%d.%d/32", i/256, i%256)
	}
	router := routing.Router{ID: r.target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1", LocalIPv6: "2001:db8::1"}
	plan, err := routeros.BuildPlan(r.target, router, []routing.Peer{{ID: "batch-peer", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 2000, AllowedPrefixes: prefixes}}, *initial.Snapshot, routeros.PlanOptions{Bootstrap: true})
	if err != nil {
		nativeReadScaleFailure(t, "read_scale_plan", err)
	}
	compiled, err := routeros.Compile(plan, nil)
	if err != nil {
		nativeReadScaleFailure(t, "read_scale_plan", err)
	}
	generationOps := map[string]bool{}
	for _, op := range plan.Operations {
		if generationRule(&plan, op) {
			generationOps[op.ID] = true
		}
	}
	guard := &nativeDiscardCounters{}
	measured := &nativeReadScaleAPI{nativeExtensionAPI: &nativeExtensionAPI{base: r.api, t: t}, guard: guard, setup: true}
	var operations []routeros.CompiledCommand
	for _, command := range compiled.Commands {
		if command.Phase != routeros.Prepare || command.Action != routeros.Add || command.Resource == routeros.BGPConnection {
			continue
		}
		mutation := api.Mutation{Resource: api.Resource(command.Resource), Action: api.Add, Attributes: []api.Attribute{}}
		for _, attribute := range command.Attributes {
			if attribute.Sensitive {
				t.Fatal("read_scale_plan=false")
			}
			mutation.Attributes = append(mutation.Attributes, api.Attribute{Name: attribute.Name, Value: attribute.Value})
		}
		if api.ValidateMutation(mutation) != nil {
			t.Fatal("read_scale_plan=false")
		}
		measured.approved = append(measured.approved, mutation)
		operations = append(operations, command)
	}
	if len(generationOps) != 2004 || len(measured.approved) != 2008 {
		t.Fatal("read_scale_plan=false")
	}
	r.api = measured
	r.terminal = &nativeDiscardReadOnlyTerminal{terminalChannel: r.terminal, counts: guard}
	ids, generationIDs, protected := map[string]bool{}, []string{}, []nativeAddedObject{}
	expectedRows := map[string]api.Mutation{}
	started, before := time.Now(), measured.measure()
	for i, mutation := range measured.approved {
		result, err := measured.Apply(ctx, mutation)
		if err != nil || !result.Acknowledged || !nativeID.MatchString(result.ID) || ids[cleanupKey(mutation.Resource, result.ID)] {
			measured.logPhase("setup", started, before)
			nativeReadScaleFailure(t, "read_scale_setup", err)
		}
		ids[cleanupKey(mutation.Resource, result.ID)] = true
		expectedRows[cleanupKey(mutation.Resource, result.ID)] = mutation
		if generationOps[operations[i].OperationID] {
			generationIDs = append(generationIDs, result.ID)
		} else {
			protected = append(protected, nativeAddedObject{mutation.Resource, result.ID})
		}
	}
	measured.setup = false
	measured.logPhase("setup", started, before)
	var baseline api.Inventory
	var baselineDigest string
	for _, phase := range []string{"scan-one", "scan-two"} {
		started, before = time.Now(), measured.measure()
		scan, done := context.WithTimeout(ctx, 180*time.Second)
		_, _, actual, digest, err := r.readState(scan)
		done()
		measured.logPhase(phase, started, before)
		if err != nil || len(actual.FilterRules) != 2006 || len(actual.Instances) != 1 || len(actual.Connections) != 0 || actual.SafeMode == nil || actual.SafeMode.Enabled || actual.SafeMode.Current {
			nativeReadScaleFailure(t, "read_scale_inventory_consistent", err)
		}
		if baselineDigest != "" && baselineDigest != digest {
			t.Fatal("read_scale_inventory_consistent=false")
		}
		baseline, baselineDigest = actual, digest
	}
	actualRows := map[string]api.Record{}
	for _, menu := range menus(baseline) {
		for _, row := range menu.rows {
			actualRows[cleanupKey(menu.resource, row[".id"])] = row
		}
	}
	for key, expected := range expectedRows {
		row := actualRows[key]
		if row == nil {
			t.Fatal("read_scale_setup_readback=false")
		}
		for _, attribute := range expected.Attributes {
			if expected.Resource == api.BGPInstance && attribute.Name == "routing-table" {
				if row[attribute.Name] != attribute.Value && row[attribute.Name] != protected[0].id {
					t.Fatal("read_scale_setup_readback=false")
				}
			} else if row[attribute.Name] != attribute.Value {
				t.Fatal("read_scale_setup_readback=false")
			}
		}
	}
	for _, object := range protected {
		row := actualRows[cleanupKey(object.resource, object.id)]
		if row == nil || object.resource == api.RoutingTable && (row["name"] != plan.Owner || row["fib"] != "false") || object.resource == api.FilterRule && row["rule"] != "reject;" {
			t.Fatal("read_scale_protected_inert=false")
		}
	}
	started, before = time.Now(), measured.measure()
	observed, err := r.Observe(ctx, control.ReadRequest{KnownState: known})
	measured.logPhase("observe", started, before)
	if err != nil || observed.Snapshot != nil || len(observed.Credentials) != 0 || !observed.CrossChannelIdentityVerified || !observed.Owner.Known || !observed.Owner.Alive || observed.Owner.Active || !observed.Owner.HistoryKnown || observed.Owner.HistoryActions != 0 {
		nativeReadScaleFailure(t, "read_scale_complete_observation", err)
	}
	actualDigest, err := stateDigest(observed.Inventory, r.target)
	if err != nil || actualDigest != baselineDigest {
		nativeReadScaleFailure(t, "read_scale_inventory_consistent", err)
	}
	check := func(call context.Context, resource api.Resource, id string) error {
		row, exists, err := r.readRemovalObject(call, measured, resource, id)
		if err != nil {
			return err
		}
		wanted := actualRows[cleanupKey(resource, id)]
		a, e1 := control.ObjectAttributesChecksum(resource, row)
		b, e2 := control.ObjectAttributesChecksum(resource, wanted)
		if !exists || wanted == nil || e1 != nil || e2 != nil || a != b {
			return fail("native-read-scale-object-mismatch")
		}
		return nil
	}
	started, before = time.Now(), measured.measure()
	exact, done := context.WithTimeout(ctx, 180*time.Second)
	for _, index := range []int{0, 1002, 2003} {
		for range 2 {
			if err := check(exact, api.FilterRule, generationIDs[index]); err != nil {
				done()
				measured.logPhase("exact-rules", started, before)
				nativeReadScaleFailure(t, "read_scale_exact_object", err)
			}
		}
	}
	done()
	measured.logPhase("exact-rules", started, before)
	started, before = time.Now(), measured.measure()
	exact, done = context.WithTimeout(ctx, 180*time.Second)
	for _, object := range protected {
		if err := check(exact, object.resource, object.id); err != nil {
			done()
			measured.logPhase("protected", started, before)
			nativeReadScaleFailure(t, "read_scale_exact_object", err)
		}
	}
	done()
	measured.logPhase("protected", started, before)
	owner, err := r.Owner(ctx)
	safe, safeErr := measured.ReadSafeMode(ctx)
	if err != nil || safeErr != nil || safe.Enabled || safe.Current || !owner.Known || !owner.Alive || owner.Active || owner.ThisSession || !owner.HistoryKnown || owner.HistoryActions != 0 || owner.SessionID != initial.Owner.SessionID || guard.forbidden.Load() != 0 || measured.writes != 2008 || measured.credentials != 0 || len(ids) != 2008 || len(generationIDs) != 2004 || len(protected) != 4 || !reflect.DeepEqual(profile, r.profile) {
		nativeReadScaleFailure(t, "read_scale_final_guards", err)
	}
	t.Log("read_scale_generation_rules=2004 read_scale_setup_adds=2008 read_scale_unique_ids=2008 read_scale_protected_objects=4 read_scale_forbidden_calls=0 read_scale_setup_only=true read_scale_setup_readback=true read_scale_protected_inert=true read_scale_inventory_consistent=true read_scale_complete_observation=true read_scale_exact_rules=true read_scale_protected_exact=true read_scale_profile_unchanged=true read_scale_no_remove=true read_scale_no_owner_mutations=true read_scale_strict_authorization=false read_scale_publication_verified=false read_scale_rollback_verified=false")
}

func TestNativeReadScaleSetupGuard(t *testing.T) {
	mutation := api.Mutation{Resource: api.FilterRule, Action: api.Add, Attributes: []api.Attribute{{Name: "chain", Value: "fixture"}, {Name: "rule", Value: "reject;"}}}
	base := &fakeAPI{}
	guard := &nativeDiscardCounters{}
	wrapped := &nativeReadScaleAPI{nativeExtensionAPI: &nativeExtensionAPI{base: base, t: t}, guard: guard, approved: []api.Mutation{mutation}, setup: true}
	bad := mutation
	bad.Action = api.Remove
	if _, err := wrapped.Apply(context.Background(), bad); err == nil || base.writes != 0 {
		t.Fatal("fixture allowed an unapproved mutation")
	}
	if _, err := wrapped.Apply(context.Background(), mutation); err != nil || base.writes != 1 {
		t.Fatal("fixture rejected its one approved setup Add")
	}
	wrapped.setup = false
	if _, err := wrapped.Apply(context.Background(), mutation); err == nil || base.writes != 1 {
		t.Fatal("measurement phase permitted a write")
	}
	if _, err := wrapped.ReadCredential(context.Background(), "*99"); err == nil || base.credentialCalls != 0 {
		t.Fatal("peerless fixture acquired a credential")
	}
	if guard.forbidden.Load() != 3 {
		t.Fatal("forbidden calls were not counted")
	}
}
