package live

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routeros/terminal"
	"github.com/moedove/ixp-manager/internal/routing"
)

// This wrapper deliberately exposes only the ordinary controller path. Future
// optional Runtime interfaces must not silently broaden this native case.
type nativeDiscardBootstrap struct {
	runtime    *Runtime
	known      routeros.KnownState
	checkpoint *control.Observation
	added      map[string]bool
}

func (r *nativeDiscardBootstrap) Binding() control.Binding { return r.runtime.Binding() }
func (r *nativeDiscardBootstrap) BindPlan(p routeros.Plan, c routeros.Credentials) error {
	return r.runtime.BindPlan(p, c)
}
func (r *nativeDiscardBootstrap) ResetIdle(ctx context.Context) error {
	return r.runtime.ResetIdle(ctx)
}
func (r *nativeDiscardBootstrap) Observe(ctx context.Context, q control.ReadRequest) (control.Observation, error) {
	return r.runtime.Observe(ctx, q)
}
func (r *nativeDiscardBootstrap) Apply(ctx context.Context, m routeros.APIMutation) (api.Result, error) {
	result, err := r.runtime.Apply(ctx, m)
	if err == nil && result.Acknowledged && m.Phase == routeros.Prepare && m.Request.Action == api.Add && result.ID != "" {
		if r.added == nil {
			r.added = map[string]bool{}
		}
		r.added[cleanupKey(m.Request.Resource, result.ID)] = true
	}
	return result, err
}
func (r *nativeDiscardBootstrap) Take(ctx context.Context, deadline time.Time) (control.Owner, error) {
	checkpoint, err := r.runtime.Observe(ctx, control.ReadRequest{KnownState: r.known})
	if err != nil {
		return control.Owner{}, err
	}
	if !checkpoint.Owner.Known || checkpoint.Owner.Active || !checkpoint.Owner.HistoryKnown || len(checkpoint.Credentials) != len(r.known.Peers) {
		return control.Owner{}, fail("native-discard-checkpoint-unverified")
	}
	for _, value := range checkpoint.Credentials {
		if !value.Matches(nil) {
			return control.Owner{}, fail("native-discard-checkpoint-unverified")
		}
	}
	for _, row := range checkpoint.Inventory.Connections {
		if strings.HasPrefix(row["name"], routeros.OwnerName(r.runtime.target.RouterID)) && row["disabled"] != "true" {
			return control.Owner{}, fail("native-discard-checkpoint-unverified")
		}
	}
	r.checkpoint = &checkpoint
	return r.runtime.Take(ctx, deadline)
}
func (r *nativeDiscardBootstrap) Owner(ctx context.Context) (control.Owner, error) {
	return r.runtime.Owner(ctx)
}
func (r *nativeDiscardBootstrap) Confirm(ctx context.Context) (control.Owner, error) {
	return r.runtime.Confirm(ctx)
}
func (r *nativeDiscardBootstrap) Cancel(ctx context.Context) (control.Owner, error) {
	return r.runtime.Cancel(ctx)
}
func (r *nativeDiscardBootstrap) CloseOwner() error { return r.runtime.CloseOwner() }

type nativeDiscardCounters struct {
	forbidden atomic.Int64
	mu        sync.Mutex
	allowed   map[string]bool
	attempted map[string]bool
	acks      int
	lastCode  string
}

func (c *nativeDiscardCounters) deny() error {
	c.forbidden.Add(1)
	return fail("native-discard-forbidden-call")
}
func (c *nativeDiscardCounters) remember(err error) {
	if err != nil {
		c.mu.Lock()
		c.lastCode = liveCode(err)
		c.mu.Unlock()
	}
}
func (c *nativeDiscardCounters) snapshot() (int, int, int64, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	code := c.lastCode
	if code == "" {
		code = "none"
	}
	return len(c.attempted), c.acks, c.forbidden.Load(), code
}

// The real API transport remains responsible for decoding/read evidence. This
// test-only guard refuses any mutation except one Remove of each actually
// acknowledged original Prepare ID. It never supplies a fake acknowledgement.
type nativeDiscardOnlyAPI struct {
	apiChannel
	counts *nativeDiscardCounters
}

func (a *nativeDiscardOnlyAPI) Apply(ctx context.Context, mutation api.Mutation) (api.Result, error) {
	key := cleanupKey(mutation.Resource, mutation.ID)
	a.counts.mu.Lock()
	if mutation.Action != api.Remove || mutation.Property != "" || len(mutation.Attributes) != 0 || !a.counts.allowed[key] || a.counts.attempted[key] {
		a.counts.mu.Unlock()
		return api.Result{}, a.counts.deny()
	}
	a.counts.attempted[key] = true
	a.counts.mu.Unlock()
	result, err := a.apiChannel.Apply(ctx, mutation)
	if err == nil && result.Acknowledged && result.ID == "" {
		a.counts.mu.Lock()
		a.counts.acks++
		a.counts.mu.Unlock()
	}
	a.counts.remember(err)
	return result, err
}
func (a *nativeDiscardOnlyAPI) ReadSafeMode(ctx context.Context) (api.SafeModeState, error) {
	reader, ok := a.apiChannel.(safeModeReader)
	if !ok {
		return api.SafeModeState{}, fail("native-discard-owner-reader-unavailable")
	}
	return reader.ReadSafeMode(ctx)
}
func (a *nativeDiscardOnlyAPI) ReadProperties(ctx context.Context, requests []api.PropertyRequest) ([]api.PropertyValue, error) {
	reader, ok := a.apiChannel.(propertyBatchReader)
	if !ok {
		return nil, fail("native-discard-property-batch-unavailable")
	}
	return reader.ReadProperties(ctx, requests)
}

type nativeDiscardReadOnlyTerminal struct {
	terminalChannel
	counts *nativeDiscardCounters
}

func (t *nativeDiscardReadOnlyTerminal) EnterSafeMode(context.Context) (terminal.Evidence, error) {
	return terminal.Evidence{}, t.counts.deny()
}
func (t *nativeDiscardReadOnlyTerminal) ConfirmSafeMode(context.Context) (terminal.Confirmation, error) {
	return terminal.Confirmation{}, t.counts.deny()
}
func (t *nativeDiscardReadOnlyTerminal) CancelSafeMode(context.Context) (terminal.Cancellation, error) {
	return terminal.Cancellation{}, t.counts.deny()
}
func (t *nativeDiscardReadOnlyTerminal) ExecuteOwned(context.Context, api.Mutation) (terminal.OwnedReceipt, error) {
	return terminal.OwnedReceipt{}, t.counts.deny()
}

type nativeDiscardOnlyRuntime struct {
	runtime *Runtime
	counts  *nativeDiscardCounters
}

func (r *nativeDiscardOnlyRuntime) Binding() control.Binding { return r.runtime.Binding() }
func (r *nativeDiscardOnlyRuntime) Observe(ctx context.Context, q control.ReadRequest) (control.Observation, error) {
	return r.runtime.Observe(ctx, q)
}
func (r *nativeDiscardOnlyRuntime) Owner(ctx context.Context) (control.Owner, error) {
	return r.runtime.Owner(ctx)
}
func (r *nativeDiscardOnlyRuntime) CloseOwner() error { return r.runtime.CloseOwner() }
func (r *nativeDiscardOnlyRuntime) BindPreparedDiscard(ctx context.Context, binding control.PreparedDiscardBinding) error {
	err := r.runtime.BindPreparedDiscard(ctx, binding)
	r.counts.remember(err)
	return err
}
func (r *nativeDiscardOnlyRuntime) ApplyPreparedDiscard(ctx context.Context, mutation routeros.APIMutation) (api.Result, error) {
	result, err := r.runtime.ApplyPreparedDiscard(ctx, mutation)
	r.counts.remember(err)
	return result, err
}
func (r *nativeDiscardOnlyRuntime) BindPlan(routeros.Plan, routeros.Credentials) error {
	return r.counts.deny()
}
func (r *nativeDiscardOnlyRuntime) ResetIdle(context.Context) error { return r.counts.deny() }
func (r *nativeDiscardOnlyRuntime) Apply(context.Context, routeros.APIMutation) (api.Result, error) {
	return api.Result{}, r.counts.deny()
}
func (r *nativeDiscardOnlyRuntime) Take(context.Context, time.Time) (control.Owner, error) {
	return control.Owner{}, r.counts.deny()
}
func (r *nativeDiscardOnlyRuntime) Confirm(context.Context) (control.Owner, error) {
	return control.Owner{}, r.counts.deny()
}
func (r *nativeDiscardOnlyRuntime) Cancel(context.Context) (control.Owner, error) {
	return control.Owner{}, r.counts.deny()
}

func nativeDiscardFailure(t *testing.T, field string, err error, counters *nativeDiscardCounters) {
	t.Helper()
	if counters != nil {
		_, _, _, code := counters.snapshot()
		t.Logf("discard_adapter_code=%s", code)
	}
	code := nativeSupervisorCode(err)
	if code == "unclassified" {
		code = "native-discard-unclassified"
	}
	t.Fatalf("%s=false code=%s", field, code)
}

func nativeDiscardEnableStage(runtime *Runtime) {
	// Only this fixed opt-in test binary may exercise the closed production path.
	runtime.profile.PreparationVerified = true
	runtime.profile.ActivationVerified = true
	runtime.profile.ConfirmNativeVerified = true
	runtime.profile.MaxHistoryActions = 80
}

func nativeDiscardPreparedKnown(plan routeros.Plan) routeros.KnownState {
	known := routeros.KnownState{Target: plan.Target}
	for _, peer := range plan.Desired.Peers {
		known.Peers = append(known.Peers, routeros.KnownPeer{ID: peer.ID, Authentication: peer.Authentication})
	}
	return known
}

// A small, separate lifecycle selector. This is not a large-table deletion
// performance claim, an external BGP wire proof or a production capability gate.
func TestNativeCHRLivePreparedDiscardCycle(t *testing.T) {
	nativeDiscardCycle(t, false)
}

func nativeDiscardCycle(t *testing.T, globalTail bool) {
	nativeDiscardCycleWithContinuation(t, globalTail, false)
}

func nativeDiscardCycleWithContinuation(t *testing.T, globalTail, bounded bool) {
	cfg := nativeLiveConfig(t)
	cfg.RouterID = "native-prepared-discard-cycle"
	cfg.SessionLifetimeSeconds = 600
	cfg.ObservationTimeoutSeconds = 120
	cfg.OperationTimeoutSeconds = 15
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	firstRuntime, err := Open(ctx, cfg)
	if err != nil {
		nativeDiscardFailure(t, "discard_first_open", err, nil)
	}
	defer firstRuntime.Close()
	nativeDiscardEnableStage(firstRuntime)
	target := firstRuntime.target
	baselineKnown := routeros.KnownState{Target: target}
	baseline, err := firstRuntime.Observe(ctx, control.ReadRequest{KnownState: baselineKnown, Strict: true})
	if err != nil || baseline.Snapshot == nil || !baseline.Snapshot.NamespaceEmpty || !baseline.Owner.Known || baseline.Owner.Active {
		nativeDiscardFailure(t, "discard_initial_baseline", err, nil)
	}
	baselineDigest, err := stateDigest(baseline.Inventory, target)
	if err != nil {
		nativeDiscardFailure(t, "discard_initial_digest", err, nil)
	}
	router := routing.Router{ID: target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1"}
	peers := []routing.Peer{{ID: "discard-peer", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"203.0.113.0/24"}}}
	plan, err := routeros.BuildPlan(target, router, peers, *baseline.Snapshot, routeros.PlanOptions{Bootstrap: true})
	if err != nil {
		nativeDiscardFailure(t, "discard_initial_plan", err, nil)
	}
	first := &nativeDiscardBootstrap{runtime: firstRuntime, known: nativeDiscardPreparedKnown(plan)}
	dir, err := os.MkdirTemp("/scratch/tmp", "native-discard-journal-")
	if err != nil {
		nativeDiscardFailure(t, "discard_private_journal", err, nil)
	}
	defer os.RemoveAll(dir)
	configuration := control.Config{Dir: dir, Target: target, OperationTimeout: 30 * time.Second, PrepareTimeout: 3 * time.Minute, MinConfirmationTimeout: 10 * time.Second, MaxConfirmationTimeout: 2 * time.Minute, PollInterval: time.Second}
	original, err := control.Open(configuration, first)
	if err != nil {
		nativeDiscardFailure(t, "discard_first_supervisor", err, nil)
	}
	defer original.Close()
	pending, err := original.Stage(ctx, control.Request{Plan: plan, KnownState: baselineKnown, Timeout: 90 * time.Second, ExclusiveAccessAcknowledged: true})
	if err != nil || pending.Phase != control.AwaitingConfirmation || first.checkpoint == nil || len(first.added) == 0 || len(first.added) > 64 {
		nativeDiscardFailure(t, "discard_bootstrap_stage", err, nil)
	}
	rolledBack, err := original.Cancel(ctx, pending.TransactionID)
	until := time.Now().Add(30 * time.Second)
	for rolledBack.Phase != control.RolledBack && time.Now().Before(until) && ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
		rolledBack, err = original.Recover(ctx)
	}
	if err != nil || rolledBack.Phase != control.RolledBack || !rolledBack.RollbackVerified || !rolledBack.PreparedArtifactsRetained {
		nativeDiscardFailure(t, "discard_bootstrap_cancel_verified", err, nil)
	}
	prepared, err := firstRuntime.Observe(ctx, control.ReadRequest{KnownState: first.known})
	if err != nil {
		nativeDiscardFailure(t, "discard_cancel_observation", err, nil)
	}
	checkpointDigest, e1 := stateDigest(first.checkpoint.Inventory, target)
	preparedDigest, e2 := stateDigest(prepared.Inventory, target)
	if e1 != nil || e2 != nil || checkpointDigest != preparedDigest || !sameCredentials(first.checkpoint.Credentials, prepared.Credentials) || !prepared.Owner.Known || prepared.Owner.Active {
		t.Fatal("discard_prepared_checkpoint_equal=false")
	}
	t.Log("discard_bootstrap_cancel_verified=true discard_prepared_checkpoint_equal=true discard_prepared_credentials_equal=true discard_prepared_retained=true")
	if err := original.Close(); err != nil {
		nativeDiscardFailure(t, "discard_old_runtime_closed", err, nil)
	}
	if err := firstRuntime.Close(); err != nil {
		nativeDiscardFailure(t, "discard_old_runtime_closed", err, nil)
	}

	fresh, err := Open(ctx, cfg)
	if err != nil {
		nativeDiscardFailure(t, "discard_fresh_open", err, nil)
	}
	defer fresh.Close()
	fresh.profile.PreparedDiscardVerified = true
	counts := &nativeDiscardCounters{allowed: map[string]bool{}, attempted: map[string]bool{}}
	for id := range first.added {
		counts.allowed[id] = true
	}
	fresh.api = &nativeDiscardOnlyAPI{apiChannel: fresh.api, counts: counts}
	fresh.terminal = &nativeDiscardReadOnlyTerminal{terminalChannel: fresh.terminal, counts: counts}
	guarded := &nativeDiscardOnlyRuntime{runtime: fresh, counts: counts}
	var driver control.Runtime = guarded
	var global *nativeGlobalTailDiscardRuntime
	if globalTail {
		fresh.profile.RuleRemovalBatchVerified = true
		fresh.profile.RuleRemovalGlobalTailVerified = true
		fresh.api = &nativeGlobalTailDiscardAPI{nativeDiscardOnlyAPI: fresh.api.(*nativeDiscardOnlyAPI)}
		global = &nativeGlobalTailDiscardRuntime{nativeDiscardOnlyRuntime: guarded}
		driver = global
	}
	freshObserved, err := driver.Observe(ctx, control.ReadRequest{KnownState: first.known})
	if err != nil || !freshObserved.CrossChannelIdentityVerified || !freshObserved.Owner.Known || !freshObserved.Owner.Alive || freshObserved.Owner.Active || freshObserved.Owner.ThisSession || !freshObserved.Owner.HistoryKnown || freshObserved.Owner.HistoryActions != 0 || freshObserved.Owner.SessionID == pending.SessionID {
		nativeDiscardFailure(t, "discard_fresh_runtime_verified", err, counts)
	}
	reopened, err := control.Open(configuration, driver)
	if err != nil {
		nativeDiscardFailure(t, "discard_fresh_supervisor", err, counts)
	}
	defer reopened.Close()
	if _, err = reopened.Review(ctx); err == nil {
		t.Fatal("discard_bootstrap_review_rejected=false")
	} else {
		var failure *control.Failure
		if !errors.As(err, &failure) || failure.Code != "review-rollback-baseline-unavailable" {
			nativeDiscardFailure(t, "discard_bootstrap_review_rejected", err, counts)
		}
	}
	t.Log("discard_old_runtime_closed=true discard_fresh_runtime_verified=true discard_bootstrap_review_rejected=true")
	var discarded control.Status
	if bounded {
		reopened, fresh, driver, discarded = nativeBoundedDiscardContinue(t, ctx, cfg, configuration, reopened, fresh, driver, counts, control.DiscardRequest{TransactionID: pending.TransactionID, PlanChecksum: plan.Checksum, ExclusiveAccessAcknowledged: true})
		err = nil
	} else {
		discarded, err = reopened.DiscardPrepared(ctx, control.DiscardRequest{TransactionID: pending.TransactionID, PlanChecksum: plan.Checksum, ExclusiveAccessAcknowledged: true})
	}
	if err != nil || discarded.PreparedDiscard.Phase != control.PreparedDiscardComplete || discarded.PreparedDiscard.Completed != len(first.added) || discarded.PreparedDiscard.Total != len(first.added) || discarded.PreparedArtifactsRetained {
		nativeDiscardFailure(t, "discard_complete", err, counts)
	}
	if discarded.Phase != control.RolledBack || !discarded.RollbackVerified || discarded.ConfirmationVerified || discarded.TransactionID != pending.TransactionID || discarded.PlanChecksum != plan.Checksum {
		t.Fatal("discard_rollback_fact_preserved=false")
	}
	after, err := driver.Observe(ctx, control.ReadRequest{KnownState: baselineKnown, Strict: true})
	if err != nil || after.Snapshot == nil || !after.Snapshot.NamespaceEmpty || !after.Owner.Known || after.Owner.Active {
		nativeDiscardFailure(t, "discard_final_baseline", err, counts)
	}
	afterDigest, err := stateDigest(after.Inventory, target)
	if err != nil || afterDigest != baselineDigest || !sameCredentials(baseline.Credentials, after.Credentials) {
		t.Fatal("discard_original_baseline_restored=false")
	}
	attempts, acknowledged, forbidden, _ := counts.snapshot()
	if attempts != len(first.added) || acknowledged != attempts || forbidden != 0 {
		t.Fatal("discard_exact_removal_ids=false")
	}
	if globalTail {
		global.prove(t)
	}
	t.Logf("discard_original_additions=%d discard_remove_attempts=%d discard_remove_acknowledged=%d discard_native_forbidden_calls=%d discard_exact_removal_ids=true discard_complete=true discard_rollback_fact_preserved=true discard_original_baseline_restored=true discard_original_credentials_restored=true", len(first.added), attempts, acknowledged, forbidden)
	if err := reopened.Close(); err != nil {
		nativeDiscardFailure(t, "discard_service_closed", err, counts)
	}
	if err := fresh.Close(); err != nil {
		nativeDiscardFailure(t, "discard_service_closed", err, counts)
	}

	nextRuntime, err := Open(ctx, cfg)
	if err != nil {
		nativeDiscardFailure(t, "discard_next_open", err, nil)
	}
	defer nextRuntime.Close()
	nativeDiscardEnableStage(nextRuntime)
	next := &nativeDiscardBootstrap{runtime: nextRuntime}
	nextSupervisor, err := control.Open(configuration, next)
	if err != nil {
		nativeDiscardFailure(t, "discard_next_supervisor", err, nil)
	}
	defer nextSupervisor.Close()
	review, err := nextSupervisor.Review(ctx)
	if err != nil || !review.Snapshot.NamespaceEmpty || review.KnownState.Target != target || len(review.KnownState.Peers) != 0 || len(review.KnownState.Generations) != 0 {
		nativeDiscardFailure(t, "discard_fresh_review_empty", err, nil)
	}
	nextPlan, err := routeros.BuildPlan(target, router, peers, review.Snapshot, routeros.PlanOptions{Bootstrap: true})
	if err != nil {
		nativeDiscardFailure(t, "discard_next_plan", err, nil)
	}
	next.known = nativeDiscardPreparedKnown(nextPlan)
	nextStatus, err := nextSupervisor.Stage(ctx, control.Request{Plan: nextPlan, KnownState: review.KnownState, Timeout: 90 * time.Second, ExclusiveAccessAcknowledged: true})
	if err != nil || nextStatus.Phase != control.AwaitingConfirmation || nextStatus.TransactionID == pending.TransactionID {
		nativeDiscardFailure(t, "discard_next_stage", err, nil)
	}
	nextStatus, err = nextSupervisor.Confirm(ctx, nextStatus.TransactionID, nextPlan.Checksum)
	if err != nil || nextStatus.Phase != control.Confirmed || !nextStatus.ConfirmationVerified {
		nativeDiscardFailure(t, "discard_new_bootstrap_confirmed", err, nil)
	}
	nextKnown := nativeDiscardPreparedKnown(nextPlan)
	nextKnown.ActiveGeneration = nextPlan.Generation
	nextKnown.Generations = []routeros.KnownGeneration{{Target: target, Desired: nextPlan.Desired, PolicyChecksum: nextPlan.PolicyChecksum}}
	verified, err := nextRuntime.Observe(ctx, control.ReadRequest{KnownState: nextKnown, Strict: true})
	if err != nil || verified.Snapshot == nil || !verified.Snapshot.Managed || verified.Snapshot.TableFIB || verified.Snapshot.ActiveGeneration != nextPlan.Generation || !verified.Owner.Known || verified.Owner.Active {
		nativeDiscardFailure(t, "discard_new_bootstrap_strict", err, nil)
	}
	noop, err := routeros.BuildPlan(target, router, peers, *verified.Snapshot, routeros.PlanOptions{})
	if err != nil || len(noop.Operations) != 0 {
		nativeDiscardFailure(t, "discard_next_roundtrip", err, nil)
	}
	t.Log("discard_fresh_review_empty=true discard_new_bootstrap_confirmed=true discard_new_bootstrap_strict=true discard_no_manual_cleanup=true discard_large_scale_verified=false discard_packet_forwarding_verified=false")
}

type nativeDiscardGuardModel struct {
	apiChannel
	calls int
}

func (a *nativeDiscardGuardModel) Apply(context.Context, api.Mutation) (api.Result, error) {
	a.calls++
	return api.Result{Acknowledged: true}, nil
}

func TestNativeDiscardGuardAllowsOnlyOriginalSingleRemovals(t *testing.T) {
	base := &nativeDiscardGuardModel{}
	counts := &nativeDiscardCounters{allowed: map[string]bool{cleanupKey(api.RoutingTable, "*1"): true, cleanupKey(api.FilterRule, "*1"): true}, attempted: map[string]bool{}}
	guard := &nativeDiscardOnlyAPI{apiChannel: base, counts: counts}
	ctx := context.Background()
	if result, err := guard.Apply(ctx, api.Mutation{Resource: api.RoutingTable, Action: api.Remove, ID: "*1"}); err != nil || !result.Acknowledged {
		t.Fatal("exact approved removal did not reach the model")
	}
	for _, invalid := range []api.Mutation{
		{Resource: api.RoutingTable, Action: api.Remove, ID: "*1"},
		{Resource: api.FilterRule, Action: api.Set, ID: "*1"},
		{Resource: api.BGPConnection, Action: api.Remove, ID: "*1"},
		{Resource: api.FilterRule, Action: api.Remove, ID: "*1", Property: "disabled"},
		{Resource: api.FilterRule, Action: api.Remove, ID: "*1", Attributes: []api.Attribute{{Name: "disabled", Value: "true"}}},
	} {
		if _, err := guard.Apply(ctx, invalid); err == nil || base.calls != 1 {
			t.Fatal("duplicate, cross-menu or non-Remove request reached transport")
		}
	}
	if result, err := guard.Apply(ctx, api.Mutation{Resource: api.FilterRule, Action: api.Remove, ID: "*1"}); err != nil || !result.Acknowledged {
		t.Fatal("same native ID in its separately approved menu was lost")
	}
	attempts, acks, forbidden, _ := counts.snapshot()
	if base.calls != 2 || attempts != 2 || acks != 2 || forbidden != 5 {
		t.Fatal("native guard counters could mask a forbidden attempt")
	}
}

func TestNativeDiscardGuardRefusesConfigurationAndOwnerCalls(t *testing.T) {
	counts := &nativeDiscardCounters{}
	runtime := &nativeDiscardOnlyRuntime{counts: counts}
	ctx := context.Background()
	if runtime.BindPlan(routeros.Plan{}, nil) == nil || runtime.ResetIdle(ctx) == nil {
		t.Fatal("discard wrapper accepted rebinding or resetting")
	}
	if _, err := runtime.Apply(ctx, routeros.APIMutation{}); err == nil {
		t.Fatal("discard wrapper accepted the ordinary mutation path")
	}
	if _, err := runtime.Take(ctx, time.Now().Add(time.Minute)); err == nil {
		t.Fatal("discard wrapper accepted a Safe Mode take")
	}
	if _, err := runtime.Confirm(ctx); err == nil {
		t.Fatal("discard wrapper accepted confirmation")
	}
	if _, err := runtime.Cancel(ctx); err == nil {
		t.Fatal("discard wrapper accepted cancellation")
	}
	ssh := &nativeDiscardReadOnlyTerminal{counts: counts}
	if _, err := ssh.EnterSafeMode(ctx); err == nil {
		t.Fatal("terminal guard accepted a Safe Mode take")
	}
	if _, err := ssh.ConfirmSafeMode(ctx); err == nil {
		t.Fatal("terminal guard accepted confirmation")
	}
	if _, err := ssh.CancelSafeMode(ctx); err == nil {
		t.Fatal("terminal guard accepted cancellation")
	}
	if _, err := ssh.ExecuteOwned(ctx, api.Mutation{}); err == nil || counts.forbidden.Load() != 10 {
		t.Fatal("terminal mutation was forwarded or a forbidden call went uncounted")
	}
}
