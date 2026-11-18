package live

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routeros/terminal"
	"github.com/moedove/ixp-manager/internal/routing"
)

// These counters wrap real transports. None supplies an inventory, a property,
// a credential, an acknowledgement, or an object-presence result of its own.
type nativeRemovalScaleAPI struct {
	*nativeExtensionAPI
	presence, credentials, writes, adds, removes atomic.Int64
	guard                                        *nativeDiscardCounters
}

func (a *nativeRemovalScaleAPI) ReadObjectPresence(ctx context.Context, resource api.Resource, id string) (api.ObjectPresence, error) {
	a.presence.Add(1)
	reader, ok := a.base.(presenceReader)
	if !ok {
		return api.ObjectPresence{}, fail("native-removal-presence-reader-unavailable")
	}
	return reader.ReadObjectPresence(ctx, resource, id)
}
func (a *nativeRemovalScaleAPI) ReadCredential(ctx context.Context, id string) (api.Credential, error) {
	a.credentials.Add(1)
	return a.nativeExtensionAPI.ReadCredential(ctx, id)
}
func (a *nativeRemovalScaleAPI) Apply(ctx context.Context, m api.Mutation) (api.Result, error) {
	a.writes.Add(1)
	if m.Action == api.Add {
		a.adds.Add(1)
	} else if m.Action == api.Remove {
		a.removes.Add(1)
	}
	if a.guard != nil {
		guard := nativeDiscardOnlyAPI{apiChannel: a.nativeExtensionAPI, counts: a.guard}
		return guard.Apply(ctx, m)
	}
	return a.nativeExtensionAPI.Apply(ctx, m)
}

type nativeRemovalScaleTerminal struct {
	terminalChannel
	*nativeRemovalScaleTerminalCounts
}
type nativeRemovalScaleTerminalCounts struct {
	reads, queries, owned, takes, confirms, cancels atomic.Int64
}

func (t *nativeRemovalScaleTerminal) Synchronize(ctx context.Context) (terminal.Evidence, error) {
	t.reads.Add(1)
	return t.terminalChannel.Synchronize(ctx)
}
func (t *nativeRemovalScaleTerminal) Query(ctx context.Context, q terminal.Query) (terminal.Result, error) {
	t.queries.Add(1)
	return t.terminalChannel.Query(ctx, q)
}
func (t *nativeRemovalScaleTerminal) OwnerEvidence(ctx context.Context) (terminal.Evidence, error) {
	t.reads.Add(1)
	return t.terminalChannel.OwnerEvidence(ctx)
}
func (t *nativeRemovalScaleTerminal) ExecuteOwned(ctx context.Context, m api.Mutation) (terminal.OwnedReceipt, error) {
	t.owned.Add(1)
	writer, ok := t.terminalChannel.(ownedMutationChannel)
	if !ok {
		return terminal.OwnedReceipt{}, fail("native-removal-owned-writer-unavailable")
	}
	return writer.ExecuteOwned(ctx, m)
}
func (t *nativeRemovalScaleTerminal) EnterSafeMode(ctx context.Context) (terminal.Evidence, error) {
	t.takes.Add(1)
	return t.terminalChannel.EnterSafeMode(ctx)
}
func (t *nativeRemovalScaleTerminal) ConfirmSafeMode(ctx context.Context) (terminal.Confirmation, error) {
	t.confirms.Add(1)
	return t.terminalChannel.ConfirmSafeMode(ctx)
}
func (t *nativeRemovalScaleTerminal) CancelSafeMode(ctx context.Context) (terminal.Cancellation, error) {
	t.cancels.Add(1)
	return t.terminalChannel.CancelSafeMode(ctx)
}

type nativeRemovalScaleMetrics struct{ values [22]int64 }

type nativeRemovalScaleTiming struct {
	t          *testing.T
	started    time.Time
	accounted  int64
	lifecycle  context.Context
	globalTail bool
}

func nativeRemovalScaleRemaining(ctx context.Context, now time.Time) int64 {
	deadline, ok := ctx.Deadline()
	if !ok || !now.Before(deadline) {
		return 0
	}
	return deadline.Sub(now).Milliseconds()
}

func (d *nativeRemovalScaleTiming) record(ctx context.Context, label string, started time.Time, account bool) {
	now := time.Now()
	elapsed := now.Sub(started).Milliseconds()
	if account {
		d.accounted += elapsed
	}
	contextErr := ctx.Err()
	d.t.Logf("removal_scale_timing=%s removal_scale_timing_milliseconds=%d removal_scale_elapsed_milliseconds=%d removal_scale_remaining_milliseconds=%d removal_scale_context_done=%t removal_scale_context_deadline=%t", label, elapsed, now.Sub(d.started).Milliseconds(), nativeRemovalScaleRemaining(ctx, now), contextErr != nil, errors.Is(contextErr, context.DeadlineExceeded))
}

func (d *nativeRemovalScaleTiming) finish(ctx context.Context) {
	now := time.Now()
	elapsed := now.Sub(d.started).Milliseconds()
	contextErr := ctx.Err()
	d.t.Logf("removal_scale_timing=total removal_scale_timing_milliseconds=%d removal_scale_elapsed_milliseconds=%d removal_scale_remaining_milliseconds=%d removal_scale_context_done=%t removal_scale_context_deadline=%t removal_scale_accounted_milliseconds=%d removal_scale_unaccounted_milliseconds=%d", elapsed, elapsed, nativeRemovalScaleRemaining(ctx, now), contextErr != nil, errors.Is(contextErr, context.DeadlineExceeded), d.accounted, elapsed-d.accounted)
}

func nativeRemovalScaleMeasure(a *nativeRemovalScaleAPI, ssh *nativeRemovalScaleTerminal) nativeRemovalScaleMetrics {
	return nativeRemovalScaleMetrics{[22]int64{
		a.inventories.Load(), a.objects.Load(), a.identities.Load(), a.presence.Load(),
		a.properties.Load(), a.getterBatches.Load(), a.safeReads.Load(), a.credentials.Load(),
		a.writes.Load(), a.adds.Load(), a.removes.Load(), ssh.reads.Load(), ssh.queries.Load(),
		ssh.owned.Load(), ssh.takes.Load(), ssh.confirms.Load() + ssh.cancels.Load(),
		a.disabledGetters.Load(), a.selectionFalse.Load(), a.selectionTrue.Load(), a.selectionFalseIDs.Load(), a.selectionTrueIDs.Load(), a.selectionObservations.Load(),
	}}
}

func nativeRemovalScaleLog(t *testing.T, timing *nativeRemovalScaleTiming, phase string, started time.Time, before nativeRemovalScaleMetrics, a *nativeRemovalScaleAPI, ssh *nativeRemovalScaleTerminal) {
	t.Helper()
	now := nativeRemovalScaleMeasure(a, ssh)
	for i := range now.values {
		now.values[i] -= before.values[i]
		if now.values[i] < 0 || now.values[i] > 1000000 {
			t.Fatal("removal_scale_counter_bounds=false")
		}
	}
	v := now.values
	elapsed := time.Since(started).Milliseconds()
	timing.accounted += elapsed
	t.Logf("removal_scale_phase=%s removal_scale_milliseconds=%d removal_scale_inventory_calls=%d removal_scale_object_reads=%d removal_scale_identity_reads=%d removal_scale_presence_reads=%d removal_scale_property_items=%d removal_scale_property_batches=%d removal_scale_owner_reads=%d removal_scale_credential_reads=%d removal_scale_api_writes=%d removal_scale_api_adds=%d removal_scale_api_removes=%d removal_scale_ssh_reads=%d removal_scale_ssh_queries=%d removal_scale_ssh_owned_writes=%d removal_scale_ssh_takes=%d removal_scale_ssh_releases=%d removal_scale_disabled_getters=%d removal_scale_false_queries=%d removal_scale_true_queries=%d removal_scale_false_ids_total=%d removal_scale_true_ids_total=%d removal_scale_selection_observations=%d", phase, elapsed, v[0], v[1], v[2], v[3], v[4], v[5], v[6], v[7], v[8], v[9], v[10], v[11], v[12], v[13], v[14], v[15], v[16], v[17], v[18], v[19], v[20], v[21])
	if timing.globalTail {
		contextErr := timing.lifecycle.Err()
		t.Logf("removal_scale_phase_budget=%s removal_scale_phase_remaining_milliseconds=%d removal_scale_phase_context_done=%t removal_scale_phase_context_deadline=%t", phase, nativeRemovalScaleRemaining(timing.lifecycle, time.Now()), contextErr != nil, errors.Is(contextErr, context.DeadlineExceeded))
	}
}

// Explicitly retain the constrained discard surface and opt in to only the
// batch-removal interface. Ordinary Apply/BindPlan/Take/Confirm remain denied.
type nativeRemovalScaleRuntime struct {
	*nativeDiscardOnlyRuntime
	batches, rules, singles, singleRules  atomic.Int64
	ruleACKs, absentCalls, tails, pending atomic.Int64
	proofMode                             control.RuleRemovalProofMode
	pendingBatch                          *nativeRemovalScaleBatchTrace
	discardStarted                        time.Time
	discardBudget                         time.Duration
	generationIDs                         map[string]bool
	timing                                *nativeRemovalScaleTiming
	selectionCounts                       *nativeExtensionAPI
}

type nativeRemovalScaleBatchTrace struct {
	sequence, acknowledgedBefore, completedBefore int64
	acknowledged                                  int64
	requested                                     int
	started                                       time.Time
}

func (r *nativeRemovalScaleRuntime) proof() control.RuleRemovalProofMode {
	if r.proofMode == "" {
		return control.RuleRemovalProofPerObject
	}
	return r.proofMode
}

func (r *nativeRemovalScaleRuntime) Observe(ctx context.Context, request control.ReadRequest) (control.Observation, error) {
	trace := r.pendingBatch
	if trace != nil && r.runtime.removalTail == nil {
		return control.Observation{}, fail("native-removal-global-tail-guard-unavailable")
	}
	observation, err := r.selectionCounts.observeSelection(ctx, r.runtime, request, true)
	if trace != nil && err == nil {
		if r.runtime.removalTail != nil || trace.acknowledged == 0 {
			return observation, fail("native-removal-global-tail-observation-unverified")
		}
		// This counts an actual successful runtime full tail, not a Result
		// callback. The final supervisor Completed/HMAC/credentials checks
		// below remain independently mandatory before declaring success.
		r.rules.Add(trace.acknowledged)
		r.tails.Add(1)
		r.pendingBatch = nil
		r.logGlobalBatch(ctx, "tail", trace, true, false)
	}
	return observation, err
}

func (r *nativeRemovalScaleRuntime) BindPreparedDiscard(ctx context.Context, b control.PreparedDiscardBinding) error {
	return r.nativeDiscardOnlyRuntime.BindPreparedDiscard(ctx, b)
}
func (r *nativeRemovalScaleRuntime) ApplyPreparedDiscard(ctx context.Context, m routeros.APIMutation) (api.Result, error) {
	r.singles.Add(1)
	if r.generationIDs[cleanupKey(m.Request.Resource, m.Request.ID)] {
		r.singleRules.Add(1)
	}
	return r.nativeDiscardOnlyRuntime.ApplyPreparedDiscard(ctx, m)
}
func (r *nativeRemovalScaleRuntime) ApplyRuleRemovalBatch(ctx context.Context, batch control.RuleRemovalBatch, recorder control.RuleRemovalRecorder) error {
	if batch.Mode != control.RuleRemovalPreparedDiscard || batch.ProofMode != r.proof() || r.pendingBatch != nil {
		return r.counts.deny()
	}
	sequence := r.batches.Add(1)
	started, completed := time.Now(), r.rules.Load()
	trace := &nativeRemovalScaleBatchTrace{sequence: sequence, requested: len(batch.Operations), started: started, acknowledgedBefore: r.ruleACKs.Load(), completedBefore: completed}
	if r.proof() == control.RuleRemovalProofGlobalTail {
		r.logGlobalBatch(ctx, "enter", trace, false, false)
	} else {
		r.logBatch(ctx, "enter", sequence, len(batch.Operations), 0, completed, started, false)
	}
	err := r.runtime.ApplyRuleRemovalBatch(ctx, batch, &nativeRemovalScaleRecorder{next: recorder, runtime: r})
	if r.proof() == control.RuleRemovalProofGlobalTail {
		trace.acknowledged = r.ruleACKs.Load() - trace.acknowledgedBefore
		if trace.acknowledged > 0 && r.runtime.removalTail != nil {
			r.pendingBatch = trace
			r.pending.Add(1)
		} else if trace.acknowledged > 0 && err == nil {
			err = fail("native-removal-global-tail-guard-unavailable")
		}
		r.logGlobalBatch(ctx, "exit", trace, false, err != nil)
	} else {
		r.logBatch(ctx, "exit", sequence, len(batch.Operations), r.rules.Load()-completed, r.rules.Load(), started, err != nil)
	}
	r.counts.remember(err)
	return err
}

func (r *nativeRemovalScaleRuntime) logGlobalBatch(ctx context.Context, event string, trace *nativeRemovalScaleBatchTrace, verified, failed bool) {
	now := time.Now()
	completed := int64(0)
	if verified {
		completed = trace.acknowledged
	}
	lifecycle, own, _ := r.budgetState(now)
	r.timing.t.Logf("removal_scale_batch_event=%s removal_scale_batch_sequence=%d removal_scale_batch_requested=%d removal_scale_batch_completed=%d removal_scale_batch_total_completed=%d removal_scale_batch_milliseconds=%d removal_scale_elapsed_milliseconds=%d removal_scale_remaining_milliseconds=%d removal_scale_batch_error=%t removal_scale_batch_acknowledged=%d removal_scale_batch_total_acknowledged=%d removal_scale_batch_verified_tail=%t removal_scale_batch_pending=%t removal_scale_lifecycle_remaining_milliseconds=%d removal_scale_discard_budget_remaining_milliseconds=%d", event, trace.sequence, trace.requested, completed, r.rules.Load(), now.Sub(trace.started).Milliseconds(), now.Sub(r.timing.started).Milliseconds(), nativeRemovalScaleRemaining(ctx, now), failed, trace.acknowledged, r.ruleACKs.Load(), verified, r.runtime.removalTail != nil, lifecycle, own)
}

func (r *nativeRemovalScaleRuntime) budgetState(now time.Time) (int64, int64, int64) {
	elapsed := now.Sub(r.discardStarted).Milliseconds()
	own := r.discardBudget.Milliseconds() - elapsed
	if own < 0 {
		own = 0
	}
	return nativeRemovalScaleRemaining(r.timing.lifecycle, now), own, elapsed
}

func (r *nativeRemovalScaleRuntime) logDiscardBudget(event string) {
	lifecycle, own, elapsed := r.budgetState(time.Now())
	// The configured 420-second budget is reported independently from the
	// shorter remaining parent lifetime. These numbers change no deadline.
	r.timing.t.Logf("removal_scale_budget_event=%s removal_scale_discard_budget_milliseconds=%d removal_scale_discard_elapsed_milliseconds=%d removal_scale_discard_budget_remaining_milliseconds=%d removal_scale_lifecycle_remaining_milliseconds=%d", event, r.discardBudget.Milliseconds(), elapsed, own, lifecycle)
}

func (r *nativeRemovalScaleRuntime) logBatch(ctx context.Context, event string, sequence int64, requested int, completed, total int64, started time.Time, failed bool) {
	now := time.Now()
	r.timing.t.Logf("removal_scale_batch_event=%s removal_scale_batch_sequence=%d removal_scale_batch_requested=%d removal_scale_batch_completed=%d removal_scale_batch_total_completed=%d removal_scale_batch_milliseconds=%d removal_scale_elapsed_milliseconds=%d removal_scale_remaining_milliseconds=%d removal_scale_batch_error=%t", event, sequence, requested, completed, total, now.Sub(started).Milliseconds(), now.Sub(r.timing.started).Milliseconds(), nativeRemovalScaleRemaining(ctx, now), failed)
}

type nativeRemovalScaleRecorder struct {
	next    control.RuleRemovalRecorder
	runtime *nativeRemovalScaleRuntime
}

func (r *nativeRemovalScaleRecorder) Before(id string, o control.RuleRemovalObservation) error {
	return r.next.Before(id, o)
}
func (r *nativeRemovalScaleRecorder) Result(id string, result api.Result, err error) error {
	if failure := r.next.Result(id, result, err); failure != nil {
		return failure
	}
	if err == nil && result.Acknowledged && result.ID == "" {
		r.runtime.ruleACKs.Add(1)
	}
	return nil
}
func (r *nativeRemovalScaleRecorder) Absent(id string, o control.RuleRemovalObservation) error {
	r.runtime.absentCalls.Add(1)
	if r.runtime.proof() == control.RuleRemovalProofGlobalTail {
		return r.runtime.counts.deny()
	}
	if err := r.next.Absent(id, o); err != nil {
		return err
	}
	r.runtime.rules.Add(1)
	return nil
}

var _ presenceReader = (*nativeRemovalScaleAPI)(nil)
var _ objectReader = (*nativeRemovalScaleAPI)(nil)
var _ control.PreparedDiscardRuntime = (*nativeRemovalScaleRuntime)(nil)
var _ control.RuleRemovalBatchRuntime = (*nativeRemovalScaleRuntime)(nil)

func nativeRemovalScaleFailure(t *testing.T, field string, err error, counters *nativeDiscardCounters) {
	t.Helper()
	if counters != nil {
		attempts, acks, forbidden, code := counters.snapshot()
		t.Logf("removal_scale_remove_attempts=%d removal_scale_remove_acks=%d removal_scale_forbidden_calls=%d removal_scale_adapter_code=%s", attempts, acks, forbidden, code)
	}
	t.Fatalf("%s=false code=%s", field, nativeSupervisorCode(err))
}

// An independently selected, fixed 1000-prefix lifecycle. Only this disposable
// native fixture opens the local gates; it does not change CapabilityProfile.
// The total context and all command/confirmation budgets match batch1000.
func TestNativeCHRLiveRuleRemoval1000Discard(t *testing.T) {
	nativeRemovalScaleCycle(t, control.RuleRemovalProofPerObject)
}

func TestNativeCHRLiveRuleRemoval1000GlobalTail(t *testing.T) {
	nativeRemovalScaleCycle(t, control.RuleRemovalProofGlobalTail)
}

func nativeRemovalScaleCycle(t *testing.T, proofMode control.RuleRemovalProofMode) {
	cfg := nativeLiveConfig(t)
	if proofMode != control.RuleRemovalProofPerObject && proofMode != control.RuleRemovalProofGlobalTail {
		t.Fatal("removal_scale_proof_verified=false")
	}
	if proofMode == control.RuleRemovalProofGlobalTail {
		t.Log("removal_scale_proof_mode=global-tail")
	}
	cfg.RouterID = "native-rule-removal-1000"
	cfg.SessionLifetimeSeconds, cfg.ObservationTimeoutSeconds, cfg.OperationTimeoutSeconds = 600, 180, 15
	cfg.MaxObjects, cfg.MaxGetterCalls = 10000, 50000
	timing := &nativeRemovalScaleTiming{t: t, started: time.Now(), globalTail: proofMode == control.RuleRemovalProofGlobalTail}
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	timing.lifecycle = ctx
	defer cancel()
	defer timing.finish(ctx)
	profile := CapabilityProfile()
	if !profile.FilterRuleDisabledSelectionVerified || profile.Version != "7.24.6" || profile.PreparationVerified || profile.ActivationVerified || profile.RuleBatchVerified || profile.ObjectObservationVerified || profile.PreparedDiscardVerified || profile.RuleRemovalBatchVerified || profile.RuleRemovalGlobalTailVerified || profile.CleanupVerified || profile.MaxHistoryActions != 0 {
		t.Fatal("removal_scale_gates_unchanged_verified=false")
	}
	opening := time.Now()
	first, err := Open(ctx, cfg)
	timing.record(ctx, "first-open", opening, true)
	if err != nil {
		nativeRemovalScaleFailure(t, "removal_scale_first_open", err, nil)
	}
	defer first.Close()
	apiCounts := &nativeRemovalScaleAPI{nativeExtensionAPI: &nativeExtensionAPI{base: first.api, t: t}}
	sshCounts := &nativeRemovalScaleTerminal{terminalChannel: first.terminal, nativeRemovalScaleTerminalCounts: &nativeRemovalScaleTerminalCounts{}}
	first.api, first.terminal = apiCounts, sshCounts
	// Stage legitimately replaces the idle SSH connection. Keep its actual
	// newly dialed channel wrapped without redirecting Close of the old one.
	dialSSH := first.dialSSH
	first.dialSSH = func(c context.Context) (terminalChannel, error) {
		channel, err := dialSSH(c)
		if err != nil {
			return nil, err
		}
		return &nativeRemovalScaleTerminal{terminalChannel: channel, nativeRemovalScaleTerminalCounts: sshCounts.nativeRemovalScaleTerminalCounts}, nil
	}
	nativeDiscardEnableStage(first)
	first.profile.RuleBatchVerified, first.profile.ObjectObservationVerified = true, true
	known := routeros.KnownState{Target: first.target}
	driver := &nativeExtensionRuntime{r: first, t: t, counts: apiCounts.nativeExtensionAPI, requireSelection: true, known: known, deleted: map[string]bool{}}
	started, before := time.Now(), nativeRemovalScaleMeasure(apiCounts, sshCounts)
	baseline, err := driver.Observe(ctx, control.ReadRequest{KnownState: known, Strict: true})
	nativeRemovalScaleLog(t, timing, "baseline", started, before, apiCounts, sshCounts)
	if err != nil || baseline.Snapshot == nil || !baseline.Snapshot.NamespaceEmpty || !baseline.CrossChannelIdentityVerified || !baseline.Owner.Known || !baseline.Owner.Alive || baseline.Owner.Active || !baseline.Owner.HistoryKnown || baseline.Owner.HistoryActions != 0 || len(baseline.Credentials) != 0 {
		nativeRemovalScaleFailure(t, "removal_scale_baseline_verified", err, nil)
	}
	baselineDigest, err := stateDigest(baseline.Inventory, first.target)
	if err != nil {
		nativeRemovalScaleFailure(t, "removal_scale_baseline_verified", err, nil)
	}
	prefixes := make([]string, 1000)
	for i := range prefixes {
		prefixes[i] = fmt.Sprintf("198.18.%d.%d/32", i/256, i%256)
	}
	router := routing.Router{ID: first.target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1", LocalIPv6: "2001:db8::1"}
	peers := []routing.Peer{{ID: "batch-peer", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 2000, AllowedPrefixes: prefixes}}
	plan, err := routeros.BuildPlan(first.target, router, peers, *baseline.Snapshot, routeros.PlanOptions{Bootstrap: true})
	if err != nil {
		nativeRemovalScaleFailure(t, "removal_scale_plan", err, nil)
	}
	driver.known = nativeDiscardPreparedKnown(plan)
	expectedRules := 0
	for _, op := range plan.Operations {
		if generationRule(&plan, op) {
			expectedRules++
		}
	}
	if expectedRules != 2004 {
		t.Fatal("removal_scale_plan=false")
	}
	dir, err := os.MkdirTemp("/scratch/tmp", "native-removal-scale-journal-")
	if err != nil {
		nativeRemovalScaleFailure(t, "removal_scale_private_journal", err, nil)
	}
	defer os.RemoveAll(dir)
	configuration := control.Config{Dir: dir, Target: first.target, OperationTimeout: 30 * time.Second, PrepareTimeout: 7 * time.Minute, MinConfirmationTimeout: 10 * time.Second, MaxConfirmationTimeout: 2 * time.Minute, PollInterval: time.Second}
	opening = time.Now()
	original, err := control.Open(configuration, driver)
	timing.record(ctx, "journal-open", opening, true)
	if err != nil {
		nativeRemovalScaleFailure(t, "removal_scale_supervisor", err, nil)
	}
	defer original.Close()
	started, before = time.Now(), nativeRemovalScaleMeasure(apiCounts, sshCounts)
	pending, err := original.Stage(ctx, control.Request{Plan: plan, KnownState: known, Timeout: 90 * time.Second, ExclusiveAccessAcknowledged: true})
	nativeRemovalScaleLog(t, timing, "stage", started, before, apiCounts, sshCounts)
	if err != nil || pending.Phase != control.AwaitingConfirmation || driver.checkpoint == nil {
		nativeRemovalScaleFailure(t, "removal_scale_stage_verified", err, nil)
	}
	driver.mu.Lock()
	added := append([]nativeAddedObject{}, driver.added...)
	prepareBatches, prepareRules := driver.batchCalls, driver.batchACKs
	driver.mu.Unlock()
	allowed := map[string]bool{}
	for _, object := range added {
		key := cleanupKey(object.resource, object.id)
		if allowed[key] || !nativeID.MatchString(object.id) {
			t.Fatal("removal_scale_prepare_ids_verified=false")
		}
		allowed[key] = true
	}
	// The only non-generation additions are two reject roots, one disabled
	// peer, its instance and its non-FIB table. No receipt is synthesized.
	if prepareBatches != 1 || prepareRules != expectedRules || len(added) != expectedRules+5 || apiCounts.adds.Load() != int64(len(added)) {
		t.Fatal("removal_scale_prepare_ids_verified=false")
	}
	generationIDs := map[string]bool{}
	for _, row := range driver.checkpoint.Inventory.FilterRules {
		if generation, ok := removalRuleGeneration(plan.Owner, row); ok {
			if generation != plan.Generation {
				t.Fatal("removal_scale_prepare_ids_verified=false")
			}
			key := cleanupKey(api.FilterRule, row[".id"])
			if !allowed[key] || generationIDs[key] {
				t.Fatal("removal_scale_prepare_ids_verified=false")
			}
			generationIDs[key] = true
		}
	}
	if len(generationIDs) != expectedRules {
		t.Fatal("removal_scale_prepare_ids_verified=false")
	}
	started, before = time.Now(), nativeRemovalScaleMeasure(apiCounts, sshCounts)
	rolledBack, err := original.Cancel(ctx, pending.TransactionID)
	until := time.Now().Add(45 * time.Second)
	for rolledBack.Phase != control.RolledBack && time.Now().Before(until) && ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
		rolledBack, err = original.Recover(ctx)
	}
	nativeRemovalScaleLog(t, timing, "cancel", started, before, apiCounts, sshCounts)
	if err != nil || rolledBack.Phase != control.RolledBack || !rolledBack.RollbackVerified || !rolledBack.PreparedArtifactsRetained {
		nativeRemovalScaleFailure(t, "removal_scale_cancel_verified", err, nil)
	}
	started, before = time.Now(), nativeRemovalScaleMeasure(apiCounts, sshCounts)
	prepared, err := driver.Observe(ctx, control.ReadRequest{KnownState: driver.known})
	nativeRemovalScaleLog(t, timing, "checkpoint", started, before, apiCounts, sshCounts)
	if err != nil {
		nativeRemovalScaleFailure(t, "removal_scale_checkpoint_verified", err, nil)
	}
	want, e1 := stateDigest(driver.checkpoint.Inventory, first.target)
	actual, e2 := stateDigest(prepared.Inventory, first.target)
	if e1 != nil || e2 != nil || want != actual || !sameCredentials(driver.checkpoint.Credentials, prepared.Credentials) || !prepared.Owner.Known || prepared.Owner.Active {
		t.Fatal("removal_scale_checkpoint_verified=false")
	}
	t.Logf("removal_scale_generation_rules=%d removal_scale_prepare_rule_acks=%d removal_scale_add_total=%d removal_scale_add_unique_ids=%d removal_scale_baseline_verified=true removal_scale_stage_verified=true removal_scale_cancel_verified=true removal_scale_checkpoint_verified=true removal_scale_prepared_credentials_verified=true", expectedRules, prepareRules, len(added), len(allowed))
	if err = original.Close(); err != nil {
		nativeRemovalScaleFailure(t, "removal_scale_old_closed", err, nil)
	}
	if err = first.Close(); err != nil {
		nativeRemovalScaleFailure(t, "removal_scale_old_closed", err, nil)
	}

	started = time.Now()
	fresh, err := Open(ctx, cfg)
	// This nested interval is already included in the fresh phase below.
	timing.record(ctx, "fresh-open", started, false)
	if err != nil {
		nativeRemovalScaleFailure(t, "removal_scale_fresh_verified", err, nil)
	}
	defer fresh.Close()
	fresh.profile.PreparedDiscardVerified, fresh.profile.RuleRemovalBatchVerified = true, true
	fresh.profile.RuleRemovalGlobalTailVerified = proofMode == control.RuleRemovalProofGlobalTail
	guard := &nativeDiscardCounters{allowed: allowed, attempted: map[string]bool{}}
	freshAPI := &nativeRemovalScaleAPI{nativeExtensionAPI: &nativeExtensionAPI{base: fresh.api, t: t}, guard: guard}
	freshSSH := &nativeRemovalScaleTerminal{terminalChannel: fresh.terminal, nativeRemovalScaleTerminalCounts: &nativeRemovalScaleTerminalCounts{}}
	fresh.api = freshAPI
	fresh.terminal = &nativeDiscardReadOnlyTerminal{terminalChannel: freshSSH, counts: guard}
	guarded := &nativeRemovalScaleRuntime{nativeDiscardOnlyRuntime: &nativeDiscardOnlyRuntime{runtime: fresh, counts: guard}, generationIDs: generationIDs, timing: timing, proofMode: proofMode, selectionCounts: freshAPI.nativeExtensionAPI}
	observed, err := guarded.Observe(ctx, control.ReadRequest{KnownState: driver.known})
	nativeRemovalScaleLog(t, timing, "fresh", started, nativeRemovalScaleMetrics{}, freshAPI, freshSSH)
	if err != nil || !observed.CrossChannelIdentityVerified || !observed.Owner.Known || !observed.Owner.Alive || observed.Owner.Active || observed.Owner.ThisSession || !observed.Owner.HistoryKnown || observed.Owner.HistoryActions != 0 || observed.Owner.SessionID == pending.SessionID {
		nativeRemovalScaleFailure(t, "removal_scale_fresh_verified", err, guard)
	}
	opening = time.Now()
	reopened, err := control.Open(configuration, guarded)
	timing.record(ctx, "journal-replay", opening, true)
	if err != nil {
		nativeRemovalScaleFailure(t, "removal_scale_fresh_supervisor", err, guard)
	}
	defer reopened.Close()
	opening = time.Now()
	_, err = reopened.Review(ctx)
	timing.record(ctx, "rejected-review", opening, true)
	if err == nil {
		t.Fatal("removal_scale_early_review_rejected=false")
	} else {
		var failure *control.Failure
		if !errors.As(err, &failure) || failure.Code != "review-rollback-baseline-unavailable" {
			nativeRemovalScaleFailure(t, "removal_scale_early_review_rejected", err, guard)
		}
	}
	started, before = time.Now(), nativeRemovalScaleMeasure(freshAPI, freshSSH)
	guarded.discardStarted, guarded.discardBudget = started, configuration.PrepareTimeout
	if proofMode == control.RuleRemovalProofGlobalTail {
		guarded.logDiscardBudget("start")
	}
	discarded, err := reopened.DiscardPrepared(ctx, control.DiscardRequest{TransactionID: pending.TransactionID, PlanChecksum: plan.Checksum, ExclusiveAccessAcknowledged: true})
	if proofMode == control.RuleRemovalProofGlobalTail {
		guarded.logDiscardBudget("end")
	}
	nativeRemovalScaleLog(t, timing, "discard", started, before, freshAPI, freshSSH)
	t.Logf("removal_scale_batches=%d removal_scale_batch_rules=%d removal_scale_single_removals=%d removal_scale_single_rule_removals=%d", guarded.batches.Load(), guarded.rules.Load(), guarded.singles.Load(), guarded.singleRules.Load())
	if proofMode == control.RuleRemovalProofGlobalTail {
		t.Logf("removal_scale_batch_rule_acks=%d removal_scale_absent_callbacks=%d removal_scale_full_tail_reads=%d removal_scale_pending_guards=%d removal_scale_formal_completed=%d removal_scale_formal_total=%d", guarded.ruleACKs.Load(), guarded.absentCalls.Load(), guarded.tails.Load(), guarded.pending.Load(), discarded.PreparedDiscard.Completed, discarded.PreparedDiscard.Total)
	}
	if err != nil || discarded.PreparedDiscard.Phase != control.PreparedDiscardComplete || discarded.PreparedDiscard.Completed != len(added) || discarded.PreparedDiscard.Total != len(added) || discarded.PreparedArtifactsRetained {
		nativeRemovalScaleFailure(t, "removal_scale_discard_verified", err, guard)
	}
	if discarded.Phase != control.RolledBack || !discarded.RollbackVerified || discarded.ConfirmationVerified || discarded.TransactionID != pending.TransactionID || discarded.PlanChecksum != plan.Checksum {
		t.Fatal("removal_scale_rollback_fact_verified=false")
	}
	started, before = time.Now(), nativeRemovalScaleMeasure(freshAPI, freshSSH)
	after, err := guarded.Observe(ctx, control.ReadRequest{KnownState: known, Strict: true})
	if err != nil || after.Snapshot == nil || !after.Snapshot.NamespaceEmpty || !after.CrossChannelIdentityVerified || !after.Owner.Known || !after.Owner.Alive || after.Owner.Active || after.Owner.ThisSession || !after.Owner.HistoryKnown || after.Owner.HistoryActions != 0 {
		nativeRemovalScaleLog(t, timing, "final", started, before, freshAPI, freshSSH)
		nativeRemovalScaleFailure(t, "removal_scale_strict_baseline_verified", err, guard)
	}
	afterDigest, err := stateDigest(after.Inventory, first.target)
	if err != nil || afterDigest != baselineDigest || !sameCredentials(baseline.Credentials, after.Credentials) || len(after.Credentials) != 0 || !reflect.DeepEqual(*baseline.Snapshot, *after.Snapshot) {
		t.Fatal("removal_scale_strict_baseline_verified=false")
	}
	review, err := reopened.Review(ctx)
	nativeRemovalScaleLog(t, timing, "final", started, before, freshAPI, freshSSH)
	if err != nil || !reflect.DeepEqual(review.KnownState, known) || !reflect.DeepEqual(review.Snapshot, *baseline.Snapshot) {
		nativeRemovalScaleFailure(t, "removal_scale_review_verified", err, guard)
	}
	attempts, acks, forbidden, _ := guard.snapshot()
	t.Logf("removal_scale_remove_attempts=%d removal_scale_remove_acks=%d removal_scale_remove_unique_ids=%d removal_scale_forbidden_calls=%d", attempts, acks, attempts, forbidden)
	if attempts != len(added) || acks != attempts || forbidden != 0 || freshAPI.adds.Load() != 0 || freshAPI.writes.Load() != int64(attempts) || freshAPI.removes.Load() != int64(attempts) || guarded.batches.Load() == 0 || guarded.rules.Load()+guarded.singleRules.Load() != int64(expectedRules) || guarded.singleRules.Load() > 1 || guarded.singles.Load()-guarded.singleRules.Load() != 5 || freshSSH.owned.Load() != 0 || freshSSH.takes.Load() != 0 || freshSSH.confirms.Load() != 0 || freshSSH.cancels.Load() != 0 {
		t.Fatal("removal_scale_exact_once_verified=false")
	}
	if apiCounts.selectionObservations.Load() == 0 || freshAPI.selectionObservations.Load() == 0 {
		t.Fatal("removal_scale_selection_verified=false")
	}
	t.Log("removal_scale_selection_verified=true")
	if !reflect.DeepEqual(profile, CapabilityProfile()) {
		t.Fatal("removal_scale_gates_unchanged_verified=false")
	}
	if proofMode == control.RuleRemovalProofGlobalTail {
		if guarded.absentCalls.Load() != 0 || guarded.ruleACKs.Load() != guarded.rules.Load() || guarded.tails.Load() != guarded.batches.Load() || guarded.pending.Load() != guarded.batches.Load() || guarded.pendingBatch != nil || fresh.removalTail != nil {
			t.Fatal("removal_scale_global_tail_verified=false")
		}
		t.Log("removal_scale_global_tail_verified=true")
	}
	t.Log("removal_scale_fresh_verified=true removal_scale_early_review_rejected=true removal_scale_discard_verified=true removal_scale_rollback_fact_verified=true removal_scale_strict_baseline_verified=true removal_scale_final_credentials_verified=true removal_scale_review_verified=true removal_scale_exact_once_verified=true removal_scale_no_forbidden_verified=true removal_scale_gates_unchanged_verified=true removal_scale_publication_verified=false removal_scale_cleanup_verified=false removal_scale_manual_cleanup=false")
}

type nativeRemovalScaleGuardRecorder struct {
	resultError error
	absences    int
}

func (*nativeRemovalScaleGuardRecorder) Before(string, control.RuleRemovalObservation) error {
	return nil
}
func (r *nativeRemovalScaleGuardRecorder) Result(string, api.Result, error) error {
	return r.resultError
}
func (r *nativeRemovalScaleGuardRecorder) Absent(string, control.RuleRemovalObservation) error {
	r.absences++
	return nil
}

func TestNativeRemovalScaleProofGuardKeepsACKSeparateFromVerifiedTail(t *testing.T) {
	counts := &nativeDiscardCounters{}
	runtime := &nativeRemovalScaleRuntime{nativeDiscardOnlyRuntime: &nativeDiscardOnlyRuntime{counts: counts}, proofMode: control.RuleRemovalProofGlobalTail}
	next := &nativeRemovalScaleGuardRecorder{}
	recorder := &nativeRemovalScaleRecorder{runtime: runtime, next: next}
	if err := recorder.Result("first", api.Result{Acknowledged: true}, nil); err != nil || runtime.ruleACKs.Load() != 1 || runtime.rules.Load() != 0 || runtime.tails.Load() != 0 {
		t.Fatal("durable ACK was reported as observed removal")
	}
	next.resultError = errors.New("fixture journal refused")
	if recorder.Result("second", api.Result{Acknowledged: true}, nil) == nil || runtime.ruleACKs.Load() != 1 {
		t.Fatal("non-durable result was counted as acknowledged")
	}
	if recorder.Absent("first", control.RuleRemovalObservation{}) == nil || next.absences != 0 || runtime.rules.Load() != 0 || runtime.absentCalls.Load() != 1 {
		t.Fatal("global-tail accepted a per-object absence callback")
	}
	if runtime.ApplyRuleRemovalBatch(context.Background(), control.RuleRemovalBatch{Mode: control.RuleRemovalPreparedDiscard, ProofMode: control.RuleRemovalProofPerObject}, nil) == nil || runtime.batches.Load() != 0 {
		t.Fatal("fixture accepted another proof mode")
	}
	runtime.proofMode = control.RuleRemovalProofPerObject
	if err := recorder.Absent("legacy", control.RuleRemovalObservation{}); err != nil || next.absences != 1 || runtime.rules.Load() != 1 {
		t.Fatal("legacy per-object callback contract changed")
	}
}

// This model checks interface preservation and counter boundaries only; native
// selection-scale establishes the actual query/getter equivalence.
type nativeSelectionCounterModel struct{ *selectionAPIModel }

func (a *nativeSelectionCounterModel) ReadProperties(ctx context.Context, requests []api.PropertyRequest) ([]api.PropertyValue, error) {
	values := make([]api.PropertyValue, len(requests))
	for i, request := range requests {
		value, err := a.fakeAPI.ReadProperty(ctx, request.Resource, request.ID, request.Property)
		if err != nil {
			return nil, err
		}
		values[i] = value
	}
	return values, nil
}

func TestNativeRemovalScaleSelectionForwardingAndCounters(t *testing.T) {
	r, model, request := selectionFixture(t, 128)
	r.profile = CapabilityProfile()
	base := &nativeSelectionCounterModel{model}
	counts := &nativeExtensionAPI{base: base, t: t}
	r.api = counts
	if _, err := counts.observeSelection(context.Background(), r, request, true); err != nil {
		t.Fatal("native wrapper lost the default verified read-only path", err)
	}
	if model.queries != 4 || counts.selectionFalse.Load() != 2 || counts.selectionTrue.Load() != 2 || counts.selectionFalseIDs.Load() != 254 || counts.selectionTrueIDs.Load() != 2 || counts.selectionObservations.Load() != 1 || counts.disabledGetters.Load() != 0 {
		t.Fatal("actual query, partition ID, or getter counts were lost")
	}
	if _, err := counts.ReadProperty(context.Background(), api.FilterRule, "*2", "disabled"); err != nil {
		t.Fatal(err)
	}
	if _, err := counts.ReadProperties(context.Background(), []api.PropertyRequest{{Resource: api.FilterRule, ID: "*2", Property: "disabled"}}); err != nil {
		t.Fatal(err)
	}
	if counts.disabledGetters.Load() != 2 || counts.properties.Load() != 2 || counts.getterBatches.Load() != 1 {
		t.Fatal("single/batch disabled getters were not counted exactly once")
	}
	r.profile.FilterRuleDisabledSelectionVerified = false
	if _, err := counts.observeSelection(context.Background(), r, request, true); err == nil || counts.selectionObservations.Load() != 1 {
		t.Fatal("large fixture silently accepted getter fallback")
	}
}

func TestNativeRemovalScaleSelectionFailureDoesNotCountIDsOrObservation(t *testing.T) {
	r, model, request := selectionFixture(t, 128)
	model.fault = "false-query"
	counts := &nativeExtensionAPI{base: &nativeSelectionCounterModel{model}, t: t}
	r.api = counts
	if _, err := counts.observeSelection(context.Background(), r, request, true); err == nil || counts.selectionFalse.Load() != 1 || counts.selectionTrue.Load() != 0 || counts.selectionFalseIDs.Load() != 0 || counts.selectionObservations.Load() != 0 || counts.disabledGetters.Load() != 0 {
		t.Fatal("failed query became success or getter fallback")
	}
}
