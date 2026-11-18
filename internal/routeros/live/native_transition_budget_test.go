package live

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routing"
)

// RouterOS uses the typed IPv4 AF name ip, not the display label ipv4.
const nativeTransitionIPv4Family = "ip"

type nativeTransitionOperation struct {
	descriptor routeros.APIOperation
	kind       routeros.OperationKind
}

// The embedded wrapper exposes only the ordinary controller interface. In
// particular no optional batch, cleanup, discard, or object-activation method
// can bypass the per-operation observation in Apply below.
type nativeTransitionRuntime struct {
	*nativeReviewReuseRuntime
	t                           *testing.T
	stage                       string
	operations                  []nativeTransitionOperation
	index, activated, maximum   int
	previousHistory, totalDelta int
	planChecksum                string
	added                       map[string]bool
	kinds                       map[routeros.OperationKind]int
}

func (r *nativeTransitionRuntime) BindPlan(plan routeros.Plan, credentials routeros.Credentials) error {
	bridge, err := routeros.NewAPIBridge(plan, credentials)
	if err != nil {
		return fail("native-transition-plan-invalid")
	}
	if err := r.nativeReviewReuseRuntime.BindPlan(plan, credentials); err != nil {
		return err
	}
	kinds := map[string]routeros.OperationKind{}
	for _, op := range plan.Operations {
		kinds[op.ID] = op.Kind
	}
	r.operations = nil
	for _, op := range bridge.Operations() {
		if op.Phase == routeros.Prepare || op.Phase == routeros.Activate {
			r.operations = append(r.operations, nativeTransitionOperation{op, kinds[op.ID]})
		}
	}
	r.planChecksum, r.index, r.activated, r.maximum, r.previousHistory, r.totalDelta = plan.Checksum, 0, 0, 0, 0, 0
	r.added, r.kinds = map[string]bool{}, map[routeros.OperationKind]int{}
	return nil
}

func nativeTransitionCurrentOwner(owner control.Owner) bool {
	return owner.SessionID != "" && owner.Known && owner.Alive && owner.Active && owner.ThisSession && owner.HistoryKnown && owner.HistoryActions >= 0 && owner.HistoryActions <= 80
}

func nativeTransitionHistoryDelta(before, after control.Owner) (int, error) {
	if !nativeTransitionCurrentOwner(before) || !nativeTransitionCurrentOwner(after) || before.SessionID != after.SessionID || after.HistoryActions < before.HistoryActions {
		return 0, fail("native-transition-history-unverified")
	}
	return after.HistoryActions - before.HistoryActions, nil
}

func (r *nativeTransitionRuntime) Apply(ctx context.Context, mutation routeros.APIMutation) (api.Result, error) {
	if r.index >= len(r.operations) || mutation.PlanChecksum != r.planChecksum {
		return api.Result{}, fail("native-transition-operation-order")
	}
	expected := r.operations[r.index]
	op := expected.descriptor
	if mutation.OperationID != op.ID || mutation.Phase != op.Phase || mutation.Request.Resource != api.Resource(op.Resource) || mutation.Request.Action != api.Action(op.Action) {
		return api.Result{}, fail("native-transition-operation-order")
	}
	var before control.Owner
	if mutation.Phase == routeros.Activate {
		var err error
		before, err = r.runtime.Owner(ctx)
		if err != nil || !nativeTransitionCurrentOwner(before) || before.HistoryActions != r.previousHistory {
			return api.Result{}, fail("native-transition-history-before-unverified")
		}
	}
	result, err := r.nativeReviewReuseRuntime.Apply(ctx, mutation)
	if err != nil || !result.Acknowledged {
		return result, err
	}
	if mutation.Phase == routeros.Prepare && mutation.Request.Action == api.Add {
		key := cleanupKey(mutation.Request.Resource, result.ID)
		if !nativeID.MatchString(result.ID) || r.added[key] {
			return result, fail("native-transition-prepare-id-invalid")
		}
		r.added[key] = true
	}
	if mutation.Phase == routeros.Activate {
		after, err := r.runtime.Owner(ctx)
		if err != nil {
			return result, fail("native-transition-history-after-unverified")
		}
		delta, err := nativeTransitionHistoryDelta(before, after)
		if err != nil {
			return result, err
		}
		r.activated++
		r.kinds[expected.kind]++
		r.previousHistory = after.HistoryActions
		r.totalDelta += delta
		if after.HistoryActions > r.maximum {
			r.maximum = after.HistoryActions
		}
		// Only fixed compiler enums and bounded counts; never IDs, properties,
		// command text, credential references, or credential values.
		r.t.Logf("transition_stage=%s transition_operation=%d transition_kind=%s transition_resource=%s transition_action=%s transition_history_before=%d transition_history_after=%d transition_history_delta=%d transition_owner_preserved=true", r.stage, r.activated, expected.kind, op.Resource, op.Action, before.HistoryActions, after.HistoryActions, delta)
	}
	r.index++
	return result, nil
}

func (r *nativeTransitionRuntime) Take(ctx context.Context, deadline time.Time) (control.Owner, error) {
	if err := r.counts.permitted(); err != nil {
		return control.Owner{}, err
	}
	checkpoint, err := r.runtime.Observe(ctx, control.ReadRequest{KnownState: r.known, BaselineCredentials: r.credentials, Strict: r.stage == "transition"})
	if err != nil || !checkpoint.CrossChannelIdentityVerified || !checkpoint.Owner.Known || !checkpoint.Owner.Alive || checkpoint.Owner.Active || !checkpoint.Owner.HistoryKnown || checkpoint.Owner.HistoryActions != 0 || len(checkpoint.Credentials) != 1 {
		return control.Owner{}, fail("native-transition-checkpoint-unverified")
	}
	for _, value := range checkpoint.Credentials {
		if !value.Matches([]byte(r.credentials["transition-peer"].Password)) {
			return control.Owner{}, fail("native-transition-checkpoint-credential-unverified")
		}
	}
	if r.stage == "transition" && (checkpoint.Snapshot == nil || !checkpoint.Snapshot.Managed || checkpoint.Snapshot.ActiveGeneration != r.known.ActiveGeneration) {
		return control.Owner{}, fail("native-transition-checkpoint-unverified")
	}
	r.checkpoint = &checkpoint
	return r.runtime.Take(ctx, deadline)
}

// A test assertion over the original actual rows, not a returned inventory or
// snapshot. The only allowed additional rows are this Stage's real Add ACK IDs.
func nativeTransitionOriginalRowsEqual(before, after api.Inventory, added map[string]bool) bool {
	seen := map[string]bool{}
	for i, menu := range menus(before) {
		remaining := []api.Record{}
		for _, row := range menus(after)[i].rows {
			key := cleanupKey(menu.resource, row[".id"])
			if added[key] {
				if menu.resource != api.FilterRule || seen[key] {
					return false
				}
				seen[key] = true
			} else {
				remaining = append(remaining, row)
			}
		}
		if !reflect.DeepEqual(menu.rows, remaining) {
			return false
		}
	}
	return len(seen) == len(added)
}

func nativeTransitionCredentialExact(observed control.Observation, id, wanted, other string) bool {
	value, exists := observed.Credentials[id]
	return len(observed.Credentials) == 1 && exists && value.Matches([]byte(wanted)) && !value.Matches([]byte(other))
}

func nativeTransitionFailure(t *testing.T, field string, err error) {
	t.Helper()
	code := nativeSupervisorCode(err)
	if code == "unclassified" {
		code = "native-transition-unclassified"
	}
	t.Fatalf("%s=false code=%s", field, code)
}

func TestNativeCHRLiveIdentityAuthTransitionCancel(t *testing.T) {
	cfg := nativeLiveConfig(t)
	cfg.RouterID = "native-transition-budget"
	cfg.SessionLifetimeSeconds, cfg.ObservationTimeoutSeconds, cfg.OperationTimeoutSeconds = 600, 120, 15
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	profile := CapabilityProfile()
	runtime, err := Open(ctx, cfg)
	if err != nil {
		nativeTransitionFailure(t, "transition_open", err)
	}
	defer runtime.Close()
	nativeReviewReuseStages(runtime)
	empty := routeros.KnownState{Target: runtime.target}
	initial, err := runtime.Observe(ctx, control.ReadRequest{KnownState: empty, Strict: true})
	if err != nil || initial.Snapshot == nil || !initial.Snapshot.NamespaceEmpty || !initial.CrossChannelIdentityVerified {
		nativeTransitionFailure(t, "transition_initial_empty", err)
	}
	// Nonempty, distinct, synthetic lab credentials. They are never logged.
	const oldKey, newKey = "NativeTransitionBaseline_A", "NativeTransitionDesired_B"
	oldCredentials := routeros.Credentials{"transition-peer": {Revision: strings.Repeat("A", 22), Password: oldKey}}
	newCredentials := routeros.Credentials{"transition-peer": {Revision: strings.Repeat("B", 22), Password: newKey}}
	router := routing.Router{ID: runtime.target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1", LocalIPv6: "2001:db8::1"}
	peers := []routing.Peer{{ID: "transition-peer", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"203.0.113.0/24"}, AuthenticationRevision: oldCredentials["transition-peer"].Revision, Password: oldKey}}
	first, err := routeros.BuildPlan(runtime.target, router, peers, *initial.Snapshot, routeros.PlanOptions{Bootstrap: true})
	if err != nil {
		nativeTransitionFailure(t, "transition_baseline_plan", err)
	}
	oldKnown := nativeReviewReuseKnown(first)
	counts := &nativeReviewReuseCounts{}
	runtime.api = &nativeReviewReuseAPI{apiChannel: runtime.api, counts: counts}
	driver := &nativeTransitionRuntime{nativeReviewReuseRuntime: &nativeReviewReuseRuntime{runtime: runtime, counts: counts, known: oldKnown, credentials: oldCredentials}, t: t, stage: "baseline"}
	dir, err := os.MkdirTemp("/scratch/tmp", "native-transition-journal-")
	if err != nil {
		nativeTransitionFailure(t, "transition_private_journal", err)
	}
	defer os.RemoveAll(dir)
	supervisor, err := control.Open(control.Config{Dir: dir, Target: runtime.target, OperationTimeout: 30 * time.Second, PrepareTimeout: 3 * time.Minute, MinConfirmationTimeout: 10 * time.Second, MaxConfirmationTimeout: 2 * time.Minute, PollInterval: time.Second}, driver)
	if err != nil {
		nativeTransitionFailure(t, "transition_supervisor", err)
	}
	defer supervisor.Close()
	started := time.Now()
	status, err := supervisor.Stage(ctx, control.Request{Plan: first, KnownState: empty, Credentials: oldCredentials, Timeout: 90 * time.Second, ExclusiveAccessAcknowledged: true})
	if err != nil || status.Phase != control.AwaitingConfirmation || driver.activated != 4 || driver.index != len(driver.operations) {
		nativeTransitionFailure(t, "transition_baseline_stage", err)
	}
	status, err = supervisor.Confirm(ctx, status.TransactionID, first.Checksum)
	if err != nil || status.Phase != control.Confirmed || !status.ConfirmationVerified {
		nativeTransitionFailure(t, "transition_baseline_confirmed", err)
	}
	baseline, err := runtime.Observe(ctx, control.ReadRequest{KnownState: oldKnown, BaselineCredentials: oldCredentials, Strict: true})
	if err != nil || baseline.Snapshot == nil || !baseline.Snapshot.Managed || baseline.Snapshot.ActiveGeneration != first.Generation || len(baseline.Snapshot.Connections) != 1 || !baseline.Snapshot.Connections[0].AuthenticationVerified || baseline.Snapshot.Connections[0].Disabled {
		nativeTransitionFailure(t, "transition_baseline_strict", err)
	}
	oldPeer := baseline.Snapshot.Connections[0]
	if oldPeer.Config.Family != nativeTransitionIPv4Family || oldPeer.Config.Authentication != first.Desired.Peers[0].Authentication || !nativeTransitionCredentialExact(baseline, oldPeer.Ref.ID, oldKey, newKey) {
		t.Fatal("transition_baseline_credential_verified=false")
	}
	t.Logf("transition_baseline_milliseconds=%d transition_baseline_confirmed=true transition_baseline_strict=true transition_baseline_credential_verified=true", time.Since(started).Milliseconds())
	router.ASN, router.RouterID = 64510, "192.0.2.10"
	peers[0].Address, peers[0].AllowedPrefixes = "2001:db8::2", []string{"2001:db8:1::/48"}
	peers[0].AuthenticationRevision, peers[0].Password = newCredentials["transition-peer"].Revision, newKey
	update, err := routeros.BuildPlan(runtime.target, router, peers, *baseline.Snapshot, routeros.PlanOptions{AllowIdentityChange: true})
	if err != nil || update.Generation == first.Generation {
		nativeTransitionFailure(t, "transition_update_plan", err)
	}
	var wantedPeer routeros.Connection
	wantKinds := map[routeros.OperationKind]int{routeros.SetEntry: 3, routeros.DisableConnection: 1, routeros.SetConnection: 1, routeros.UnsetUnusedFamilyLimit: 1, routeros.SetAuthentication: 1, routeros.SetInstance: 1, routeros.EnableConnection: 1}
	planKinds := map[routeros.OperationKind]int{}
	prepareRules := 0
	for _, op := range update.Operations {
		if op.Phase == routeros.Prepare {
			if op.Kind != routeros.AddRule {
				t.Fatal("transition_update_plan=false")
			}
			prepareRules++
		}
		if op.Phase == routeros.Activate {
			planKinds[op.Kind]++
			if op.Kind == routeros.SetConnection {
				wantedPeer = *op.Connection
			}
		}
	}
	if prepareRules != 6 || !reflect.DeepEqual(planKinds, wantKinds) || wantedPeer.Family != "ipv6" || wantedPeer.Authentication != update.Desired.Peers[0].Authentication {
		t.Fatal("transition_update_plan=false")
	}
	preparedKnown := nativeReviewReuseKnown(first)
	preparedKnown.Generations = append(preparedKnown.Generations, routeros.KnownGeneration{Target: runtime.target, Desired: update.Desired, PolicyChecksum: update.PolicyChecksum})
	driver.stage, driver.known, driver.credentials = "transition", preparedKnown, oldCredentials
	started = time.Now()
	status, err = supervisor.Stage(ctx, control.Request{Plan: update, KnownState: oldKnown, BaselineCredentials: oldCredentials, Credentials: newCredentials, Timeout: 90 * time.Second, ExclusiveAccessAcknowledged: true})
	if err != nil || status.Phase != control.AwaitingConfirmation || driver.checkpoint == nil || driver.activated != 9 || driver.index != len(driver.operations) || !reflect.DeepEqual(driver.kinds, wantKinds) || len(driver.added) != 6 || driver.totalDelta != driver.maximum || driver.maximum > 80 {
		nativeTransitionFailure(t, "transition_update_stage", err)
	}
	checkpoint := *driver.checkpoint
	if checkpoint.Snapshot == nil || checkpoint.Snapshot.ASN != first.Desired.ASN || checkpoint.Snapshot.RouterID != first.Desired.RouterID || !nativeTransitionOriginalRowsEqual(baseline.Inventory, checkpoint.Inventory, driver.added) || !nativeTransitionCredentialExact(checkpoint, oldPeer.Ref.ID, oldKey, newKey) {
		t.Fatal("transition_prepared_checkpoint_verified=false")
	}
	newKnown := nativeReviewReuseKnown(update)
	newKnown.Generations = append([]routeros.KnownGeneration{oldKnown.Generations[0]}, newKnown.Generations...)
	pending, err := runtime.Observe(ctx, control.ReadRequest{KnownState: newKnown, BaselineCredentials: newCredentials, Strict: true})
	if err != nil || pending.Snapshot == nil || !pending.Snapshot.Managed || pending.Snapshot.TableFIB || pending.Snapshot.ASN != update.Desired.ASN || pending.Snapshot.RouterID != update.Desired.RouterID || pending.Snapshot.ActiveGeneration != update.Generation || len(pending.Snapshot.Generations) != 2 || len(pending.Snapshot.Connections) != 1 || pending.Snapshot.Connections[0].Config != wantedPeer || pending.Snapshot.Connections[0].Ref.ID != oldPeer.Ref.ID || pending.Snapshot.Connections[0].Disabled || !pending.Snapshot.Connections[0].AuthenticationVerified || !nativeTransitionCredentialExact(pending, oldPeer.Ref.ID, newKey, oldKey) {
		nativeTransitionFailure(t, "transition_pending_strict_verified", err)
	}
	t.Logf("transition_update_milliseconds=%d transition_activate_operations=%d transition_maximum_history=%d transition_history_total_delta=%d transition_prepared_additions=%d transition_compiled_order_verified=true transition_prepared_checkpoint_verified=true transition_pending_strict_verified=true transition_pending_credential_verified=true", time.Since(started).Milliseconds(), driver.activated, driver.maximum, driver.totalDelta, len(driver.added))
	started = time.Now()
	status, err = supervisor.Cancel(ctx, status.TransactionID)
	until := time.Now().Add(30 * time.Second)
	for status.Phase != control.RolledBack && time.Now().Before(until) && ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
		status, err = supervisor.Recover(ctx)
	}
	if err != nil || status.Phase != control.RolledBack || !status.RollbackVerified || !status.PreparedArtifactsRetained || status.ConfirmationVerified {
		nativeTransitionFailure(t, "transition_cancel_verified", err)
	}
	counts.readOnly.Store(true)
	rolled, err := runtime.Observe(ctx, control.ReadRequest{KnownState: preparedKnown, BaselineCredentials: oldCredentials, Strict: true})
	if err != nil || rolled.Snapshot == nil || !rolled.Snapshot.Managed || rolled.Snapshot.TableFIB || rolled.Snapshot.ActiveGeneration != first.Generation || rolled.Snapshot.ASN != first.Desired.ASN || rolled.Snapshot.RouterID != first.Desired.RouterID || len(rolled.Snapshot.Generations) != 2 || len(rolled.Snapshot.Connections) != 1 || rolled.Snapshot.Connections[0].Config != oldPeer.Config || rolled.Snapshot.Connections[0].Ref != oldPeer.Ref || rolled.Snapshot.Connections[0].Disabled || !rolled.Snapshot.Connections[0].AuthenticationVerified || !rolled.Owner.Known || rolled.Owner.Active {
		nativeTransitionFailure(t, "transition_rollback_strict_verified", err)
	}
	want, e1 := stateDigest(checkpoint.Inventory, runtime.target)
	actual, e2 := stateDigest(rolled.Inventory, runtime.target)
	if e1 != nil || e2 != nil || want != actual || !sameCredentials(checkpoint.Credentials, rolled.Credentials) || !nativeTransitionCredentialExact(rolled, oldPeer.Ref.ID, oldKey, newKey) || !nativeTransitionOriginalRowsEqual(baseline.Inventory, rolled.Inventory, driver.added) || !nativeReviewReusePreservesIDs(nativeReviewReuseIDs(*baseline.Snapshot), nativeReviewReuseIDs(*rolled.Snapshot)) {
		t.Fatal("transition_prepared_checkpoint_restored=false")
	}
	if counts.forbidden.Load() != 0 || counts.confirms.Load() != 1 || !reflect.DeepEqual(profile, CapabilityProfile()) {
		t.Fatal("transition_guards_verified=false")
	}
	t.Logf("transition_cancel_milliseconds=%d transition_cancel_verified=true transition_rollback_strict_verified=true transition_prepared_checkpoint_restored=true transition_old_native_ids_preserved=true transition_old_attributes_preserved=true transition_old_credential_restored=true transition_old_auth_reference_restored=true transition_prepared_generation_retained=true transition_forbidden_calls=%d transition_production_gates_unchanged=true transition_all_parameters_verified=false transition_history_capacity_verified=false transition_manual_cleanup=false", time.Since(started).Milliseconds(), counts.forbidden.Load())
}

func TestNativeTransitionHistoryAndScopeGuards(t *testing.T) {
	owner := control.Owner{SessionID: "fixture-session", Known: true, Alive: true, Active: true, ThisSession: true, HistoryKnown: true, HistoryActions: 7}
	for _, next := range []int{7, 8, 80} {
		after := owner
		after.HistoryActions = next
		if delta, err := nativeTransitionHistoryDelta(owner, after); err != nil || delta != next-7 {
			t.Fatal("valid actual history delta rejected")
		}
	}
	for _, mode := range []string{"unknown", "dead", "foreign", "inactive", "history-unknown", "negative", "decreased", "over-limit", "session-changed"} {
		after := owner
		switch mode {
		case "unknown":
			after.Known = false
		case "dead":
			after.Alive = false
		case "foreign":
			after.ThisSession = false
		case "inactive":
			after.Active = false
		case "history-unknown":
			after.HistoryKnown = false
		case "negative":
			after.HistoryActions = -1
		case "decreased":
			after.HistoryActions--
		case "over-limit":
			after.HistoryActions = 81
		case "session-changed":
			after.SessionID += "-new"
		}
		if _, err := nativeTransitionHistoryDelta(owner, after); err == nil {
			t.Fatal("invalid history accepted", mode)
		}
	}
	guard := &nativeTransitionRuntime{}
	if _, err := guard.Apply(context.Background(), routeros.APIMutation{}); err == nil {
		t.Fatal("unbound operation reached runtime")
	}
	guard.planChecksum = "fixed-plan"
	guard.operations = []nativeTransitionOperation{{descriptor: routeros.APIOperation{ID: "fixed-operation", Phase: routeros.Prepare, Resource: routeros.FilterRule, Action: routeros.Add}, kind: routeros.AddRule}}
	for _, bad := range []routeros.APIMutation{
		{PlanChecksum: "another-plan", OperationID: "fixed-operation", Phase: routeros.Prepare, Request: api.Mutation{Resource: api.FilterRule, Action: api.Add}},
		{PlanChecksum: "fixed-plan", OperationID: "another-operation", Phase: routeros.Prepare, Request: api.Mutation{Resource: api.FilterRule, Action: api.Add}},
		{PlanChecksum: "fixed-plan", OperationID: "fixed-operation", Phase: routeros.Activate, Request: api.Mutation{Resource: api.FilterRule, Action: api.Add}},
		{PlanChecksum: "fixed-plan", OperationID: "fixed-operation", Phase: routeros.Prepare, Request: api.Mutation{Resource: api.BGPInstance, Action: api.Add}},
		{PlanChecksum: "fixed-plan", OperationID: "fixed-operation", Phase: routeros.Prepare, Request: api.Mutation{Resource: api.FilterRule, Action: api.Remove}},
	} {
		if _, err := guard.Apply(context.Background(), bad); err == nil {
			t.Fatal("operation outside compiled order reached runtime")
		}
	}
	for _, extended := range []bool{
		implementsTransitionBatch(guard), implementsTransitionObjects(guard), implementsTransitionCleanup(guard),
		implementsTransitionDiscard(guard), implementsTransitionRemoval(guard),
	} {
		if extended {
			t.Fatal("optional interface bypassed operation observation")
		}
	}
}

func TestNativeTransitionBaselineComparisonPreservesIDsAttributesAndOrder(t *testing.T) {
	for _, mode := range []string{"same", "old-id-changed", "old-attribute-changed", "old-chain-order-changed", "extra-unapproved", "missing-prepared", "extra-nonrule"} {
		t.Run(mode, func(t *testing.T) {
			before := api.Inventory{Tables: []api.Record{{".id": "*1", "name": "old-table"}}, Instances: []api.Record{}, Connections: []api.Record{}, FilterRules: []api.Record{{".id": "*2", "chain": "old-chain", "rule": "reject;"}, {".id": "*3", "chain": "old-chain", "rule": "accept;"}}}
			after := api.Inventory{Tables: []api.Record{{".id": "*1", "name": "old-table"}}, Instances: []api.Record{}, Connections: []api.Record{}, FilterRules: []api.Record{{".id": "*2", "chain": "old-chain", "rule": "reject;"}, {".id": "*3", "chain": "old-chain", "rule": "accept;"}, {".id": "*4", "chain": "prepared-chain", "rule": "reject;"}}}
			added := map[string]bool{cleanupKey(api.FilterRule, "*4"): true}
			switch mode {
			case "old-id-changed":
				after.Tables[0][".id"] = "*F"
			case "old-attribute-changed":
				after.FilterRules[0]["rule"] = "accept;"
			case "old-chain-order-changed":
				after.FilterRules[0], after.FilterRules[1] = after.FilterRules[1], after.FilterRules[0]
			case "extra-unapproved":
				after.FilterRules = append(after.FilterRules, api.Record{".id": "*5", "rule": "reject;"})
			case "missing-prepared":
				after.FilterRules = after.FilterRules[:2]
			case "extra-nonrule":
				added[cleanupKey(api.RoutingTable, "*5")] = true
				after.Tables = append(after.Tables, api.Record{".id": "*5", "name": "another-table"})
			}
			if got := nativeTransitionOriginalRowsEqual(before, after, added); got != (mode == "same") {
				t.Fatal("original object comparison accepted a different baseline")
			}
		})
	}
}

func implementsTransitionBatch(value any) bool   { _, ok := value.(control.RuleBatchRuntime); return ok }
func implementsTransitionObjects(value any) bool { _, ok := value.(control.ObjectRuntime); return ok }
func implementsTransitionCleanup(value any) bool { _, ok := value.(control.CleanupRuntime); return ok }
func implementsTransitionDiscard(value any) bool {
	_, ok := value.(control.PreparedDiscardRuntime)
	return ok
}
func implementsTransitionRemoval(value any) bool {
	_, ok := value.(control.RuleRemovalBatchRuntime)
	return ok
}

func TestNativeTransitionFamilyMatchesCompiledPlan(t *testing.T) {
	target := routeros.Target{RouterID: "native-transition-budget", Version: "7.24.6", DeviceID: "CHR-system-id-1", TransportIdentitySHA256: strings.Repeat("a", 64)}
	router := routing.Router{ID: target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1"}
	peers := []routing.Peer{{ID: "transition-peer", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"203.0.113.0/24"}}}
	plan, err := routeros.BuildPlan(target, router, peers, routeros.Snapshot{Target: target, Owner: routeros.OwnerName(target.RouterID), NamespaceEmpty: true}, routeros.PlanOptions{Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	connections := 0
	for _, op := range plan.Operations {
		if op.Kind == routeros.AddDisabledConnection {
			connections++
			if op.Connection == nil || op.Connection.Family != nativeTransitionIPv4Family {
				t.Fatal("native baseline assertion disagrees with actual typed IPv4 plan")
			}
		}
	}
	if connections != 1 {
		t.Fatal("fixed fixture must prepare exactly one connection")
	}
	bridge, err := routeros.NewAPIBridge(plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	actions := []routeros.APIOperation{}
	for _, op := range bridge.Operations() {
		if op.Phase == routeros.Activate {
			actions = append(actions, op)
		}
	}
	if len(actions) != 4 {
		t.Fatal("fixed baseline activation count changed")
	}
	for i, op := range actions {
		resource, action := routeros.FilterRule, routeros.Set
		if i == 3 {
			resource, action = routeros.BGPConnection, routeros.Enable
		}
		if op.Resource != resource || op.Action != action {
			t.Fatal("fixed baseline activation order changed")
		}
	}
}
