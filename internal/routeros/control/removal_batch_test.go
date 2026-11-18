package control

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routing"
)

type removalBatchDevice struct {
	*preparedDiscardDevice
	batchCalls, batchWrites, fullReadsInBatch int
	prefixLimit                               int
	noProgress, rejectGate, inBatch           bool
	waitForContext                            bool
	unknownNoEffect, unknownApplied           bool
	returnBeforeAbsence, failTail             bool
	tailOutage                                bool
	corrupt                                   string
	late                                      RuleRemovalRecorder
	lateObservation                           RuleRemovalObservation
	lateOperation                             string
	cleanupBridge                             *routeros.APIBridge
	cleanupRemaining                          []string
	cleanupBinds, cleanupWrites               int
	batchIDs                                  []string
	beforeReturn                              func()
}

func (r *removalBatchDevice) Binding() Binding {
	b := r.preparedDiscardDevice.Binding()
	b.RuleRemovalBatchVerified = !r.rejectGate
	b.CleanupVerified = true
	return b
}
func (r *removalBatchDevice) Observe(ctx context.Context, request ReadRequest) (Observation, error) {
	if r.inBatch {
		r.fullReadsInBatch++
	}
	if r.tailOutage {
		return Observation{}, errors.New("fixture tail observation unavailable")
	}
	return r.preparedDiscardDevice.Observe(ctx, request)
}
func (r *removalBatchDevice) BindCleanup(ctx context.Context, binding CleanupBinding) error {
	r.cleanupBinds++
	bridge, err := routeros.NewCleanupBridge(binding.Plan)
	if err != nil || ctx.Err() != nil {
		return errors.New("fixture cleanup bind")
	}
	r.cleanupBridge, r.cleanupRemaining = bridge, append([]string{}, binding.RemainingOperationIDs...)
	return nil
}
func (r *removalBatchDevice) ApplyCleanup(ctx context.Context, operation routeros.APIMutation) (api.Result, error) {
	if len(r.cleanupRemaining) == 0 || r.cleanupRemaining[0] != operation.OperationID || operation.Phase != routeros.Cleanup {
		return api.Result{}, errors.New("fixture cleanup order")
	}
	r.cleanupWrites++
	result, err := r.reviewDevice.Apply(ctx, operation)
	if err == nil {
		r.cleanupRemaining = r.cleanupRemaining[1:]
	}
	return result, err
}
func (r *removalBatchDevice) objectEvidence(batch RuleRemovalBatch, operation routeros.APIMutation) RuleRemovalObservation {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	observation := RuleRemovalObservation{BatchID: batch.ID, Target: r.target, Resource: api.FilterRule, ID: operation.Request.ID, ObservedAt: now, Owner: r.owner}
	owner := routeros.OwnerName(r.target.RouterID)
	for _, row := range r.inv.FilterRules {
		if row[".id"] == observation.ID {
			observation.Present, observation.Attributes = true, copyRecord(row)
		}
	}
	for _, menu := range []struct {
		resource api.Resource
		rows     []api.Record
	}{{api.RoutingTable, r.inv.Tables}, {api.BGPInstance, r.inv.Instances}, {api.BGPConnection, r.inv.Connections}, {api.FilterRule, r.inv.FilterRules}} {
		for _, row := range menu.rows {
			owned := row["name"] == owner || strings.HasPrefix(row["name"], owner+"_") || strings.HasPrefix(row["chain"], owner+"_") || strings.HasPrefix(row["comment"], owner+":")
			if !owned || menu.resource == api.FilterRule && row["chain"] != owner+"_root_in" && row["chain"] != owner+"_root_out" {
				continue
			}
			observation.Protected = append(observation.Protected, ObjectObservation{Target: r.target, Resource: menu.resource, ID: row[".id"], Attributes: copyRecord(row), ObservedAt: now, CrossChannelIdentityVerified: true})
		}
	}
	return observation
}
func (r *removalBatchDevice) ApplyRuleRemovalBatch(ctx context.Context, batch RuleRemovalBatch, recorder RuleRemovalRecorder) error {
	r.batchCalls++
	r.batchIDs = append(r.batchIDs, batch.ID)
	r.inBatch = true
	defer func() {
		r.inBatch = false
		if r.failTail {
			r.tailOutage = true
		}
		if r.beforeReturn != nil {
			r.beforeReturn()
		}
	}()
	if r.noProgress {
		return nil
	}
	if r.waitForContext {
		<-ctx.Done()
		return ctx.Err()
	}
	for i, operation := range batch.Operations {
		if r.prefixLimit > 0 && i >= r.prefixLimit {
			return nil
		}
		if operation.Request.Resource != api.FilterRule || operation.Request.Action != api.Remove {
			return errors.New("fixture batch scope")
		}
		before := r.objectEvidence(batch, operation)
		switch r.corrupt {
		case "batch":
			before.BatchID = "wrong-batch"
		case "target":
			before.Attributes["rule"] = "accept;"
		case "owner":
			before.Owner.Active = true
		case "missing-protection":
			before.Protected = nil
		case "stale":
			before.ObservedAt = before.ObservedAt.Add(-time.Hour)
		case "before-absent":
			before.Present = false
			before.Attributes = nil
		}
		r.late, r.lateObservation, r.lateOperation = recorder, before, operation.OperationID
		if err := recorder.Before(operation.OperationID, before); err != nil {
			return err
		}
		r.batchWrites++
		if r.unknownNoEffect {
			return recorder.Result(operation.OperationID, api.Result{}, errors.New("fixture unknown without effect"))
		}
		result, writeErr := r.reviewDevice.Apply(ctx, operation)
		if r.unknownApplied {
			writeErr = errors.New("fixture lost acknowledgement")
		}
		if err := recorder.Result(operation.OperationID, result, writeErr); err != nil {
			return err
		}
		if writeErr != nil {
			return writeErr
		}
		if r.returnBeforeAbsence {
			return nil
		}
		after := r.objectEvidence(batch, operation)
		if r.corrupt == "absence-present" {
			after.Present = true
			after.Attributes = before.Attributes
		}
		if r.corrupt == "absence-protection" {
			after.Protected[0].Attributes["comment"] = "changed"
		}
		if err := recorder.Absent(operation.OperationID, after); err != nil {
			return err
		}
		if batch.Mode == RuleRemovalCleanup {
			r.cleanupRemaining = r.cleanupRemaining[1:]
		} else {
			r.remaining = r.remaining[1:]
		}
	}
	return nil
}

func removalDiscardFixture(t *testing.T) (*Supervisor, *removalBatchDevice, Config) {
	t.Helper()
	old, device, config, _ := rolledBackBootstrapForDiscard(t, false)
	old.Close()
	r := &removalBatchDevice{preparedDiscardDevice: freshDiscardDevice(device.reviewDevice)}
	s, err := Open(config, r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, r, config
}
func reopenRemovalFixture(t *testing.T, old *Supervisor, r *removalBatchDevice, config Config) (*Supervisor, *removalBatchDevice) {
	t.Helper()
	old.Close()
	fresh := &removalBatchDevice{preparedDiscardDevice: freshDiscardDevice(r.reviewDevice)}
	s, err := Open(config, fresh)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, fresh
}

func TestRuleRemovalBatchDiscardUsesActualObjectsAndGlobalBoundary(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			s, r, _ := removalDiscardFixture(t)
			if partial {
				r.prefixLimit = 1
			}
			status, err := s.DiscardPrepared(context.Background(), discardRequest(s))
			if err != nil || status.PreparedDiscard.Phase != PreparedDiscardComplete || r.fullReadsInBatch != 0 || s.removalBatch != nil {
				t.Fatal("rule batch did not complete through global proof", err)
			}
			if r.batchCalls < 1 || r.batchWrites+r.discardWrites != 7 || r.takeCount != 0 || r.confirmCount != 0 || r.cancelCount != 0 {
				t.Fatal("rule batch changed ownership or removal scope")
			}
			if partial && r.batchCalls < 2 {
				t.Fatal("short successful prefix was treated as the whole batch")
			}
			before := s.log.sequence
			if r.late.Before(r.lateOperation, r.lateObservation) == nil || r.late.Result(r.lateOperation, api.Result{Acknowledged: true}, nil) == nil || r.late.Absent(r.lateOperation, r.lateObservation) == nil || s.log.sequence != before {
				t.Fatal("late callback wrote the journal")
			}
		})
	}
}

func TestRuleRemovalBatchRequiresEvidenceAndRejectsNoProgress(t *testing.T) {
	for _, bad := range []string{"batch", "target", "owner", "missing-protection", "stale", "before-absent", "absence-present", "absence-protection", "no-progress", "incomplete"} {
		t.Run(bad, func(t *testing.T) {
			s, r, _ := removalDiscardFixture(t)
			r.corrupt = bad
			r.noProgress = bad == "no-progress"
			r.returnBeforeAbsence = bad == "incomplete"
			if _, err := s.DiscardPrepared(context.Background(), discardRequest(s)); err == nil {
				t.Fatal("invalid batch returned success")
			}
			if r.batchCalls != 1 {
				t.Fatal("failed/no-progress batch retried itself")
			}
			if bad != "absence-present" && bad != "absence-protection" && bad != "incomplete" && r.batchWrites != 0 {
				t.Fatal("unverified evidence preceded a removal")
			}
			if r.batchWrites > 1 {
				t.Fatal("failed batch sent another removal")
			}
		})
	}
}

func TestRuleRemovalBatchUnknownIsReconciledReadOnlyAndNeverResent(t *testing.T) {
	for _, effect := range []bool{false, true} {
		t.Run(fmt.Sprint(effect), func(t *testing.T) {
			s, r, config := removalDiscardFixture(t)
			r.unknownNoEffect = !effect
			r.unknownApplied = effect
			if _, err := s.DiscardPrepared(context.Background(), discardRequest(s)); err == nil || r.batchWrites != 1 {
				t.Fatal("unknown removal was not stopped")
			}
			if _, err := s.DiscardPrepared(context.Background(), discardRequest(s)); err == nil || r.batchWrites != 1 {
				t.Fatal("unknown runtime was reused")
			}
			next, fresh := reopenRemovalFixture(t, s, r, config)
			_, recoverErr := next.Recover(context.Background())
			if fresh.batchCalls != 0 || fresh.discardWrites != 0 || fresh.discardBinds != 0 {
				t.Fatal("recovery performed writes")
			}
			if !effect {
				if recoverErr == nil {
					t.Fatal("still-present unknown target was resolved")
				}
				if _, err := next.DiscardPrepared(context.Background(), discardRequest(next)); err == nil || fresh.batchWrites != 0 || fresh.discardWrites != 0 {
					t.Fatal("unknown target was resent")
				}
				return
			}
			if recoverErr != nil {
				t.Fatal(recoverErr)
			}
			status, err := next.DiscardPrepared(context.Background(), discardRequest(next))
			if err != nil || status.PreparedDiscard.Phase != PreparedDiscardComplete || fresh.batchWrites+fresh.discardWrites+r.batchWrites+r.discardWrites != 7 {
				t.Fatal("resolved prefix was retried or omitted", err)
			}
		})
	}
}

func TestRuleRemovalBatchTailFailureAndCrashDoNotPublishPartialGlobalState(t *testing.T) {
	for _, kind := range []string{"tail-outage", "tail-drift", "journal-before", "journal-result", "journal-absence", "journal-tail"} {
		t.Run(kind, func(t *testing.T) {
			s, r, config := removalDiscardFixture(t)
			r.failTail = kind == "tail-outage"
			if kind == "tail-drift" {
				r.beforeReturn = func() {
					r.inv.FilterRules = append(r.inv.FilterRules, api.Record{".id": "*FFFFFF", "chain": "foreign", "comment": "external change", "rule": "reject;", "disabled": "false", "inactive": "false"})
				}
			}
			failKinds := map[string]string{"journal-before": "removal-batch-intent", "journal-result": "removal-batch-result", "journal-absence": "removal-batch-absence", "journal-tail": "removal-batch-readback"}
			if fault := failKinds[kind]; fault != "" {
				s.log.fail = func(event string) error {
					if event == fault {
						return errors.New("fixture journal failure")
					}
					return nil
				}
			}
			if _, err := s.DiscardPrepared(context.Background(), discardRequest(s)); err == nil {
				t.Fatal("missing full tail proof accepted")
			}
			if s.removalBatch == nil || s.preparedDiscard.Index != s.removalBatch.StartIndex || stateMAC(s.log.key, s.current) != s.removalBatch.BeforeDigest {
				t.Fatal("object-only receipts advanced formal global state")
			}
			if kind == "journal-before" && r.batchWrites != 0 {
				t.Fatal("native remove preceded durable intent")
			}
			next, fresh := reopenRemovalFixture(t, s, r, config)
			_, err := next.Recover(context.Background())
			if fresh.batchCalls != 0 || fresh.discardBinds != 0 || fresh.discardWrites != 0 {
				t.Fatal("reopen sent a mutation")
			}
			if kind == "tail-drift" {
				if err == nil {
					t.Fatal("foreign change adopted")
				}
				return
			}
			if err != nil {
				t.Fatal("valid actual prefix could not recover", err)
			}
			status, err := next.DiscardPrepared(context.Background(), discardRequest(next))
			if err != nil || status.PreparedDiscard.Phase != PreparedDiscardComplete || r.batchWrites+r.discardWrites+fresh.batchWrites+fresh.discardWrites != 7 {
				t.Fatal("crash recovery repeated or lost a remove", err)
			}
		})
	}
}

func TestRuleRemovalBatchKeepsFallbackAndClosesConcurrentCallbacks(t *testing.T) {
	s, r, _ := removalDiscardFixture(t)
	r.rejectGate = true
	status, err := s.DiscardPrepared(context.Background(), discardRequest(s))
	if err != nil || status.PreparedDiscard.Phase != PreparedDiscardComplete || r.batchCalls != 0 || r.discardWrites != 7 {
		t.Fatal("unverified runtime bypassed the original single-item path", err)
	}
	// Closed callbacks must not even read Supervisor state; that state may be
	// serving another request after the synchronous runtime call has returned.
	recorder := &ruleRemovalRecorder{s: s}
	recorder.close()
	var wait sync.WaitGroup
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for j := 0; j < 100; j++ {
				if recorder.Before("late", RuleRemovalObservation{}) == nil || recorder.Result("late", api.Result{}, nil) == nil || recorder.Absent("late", RuleRemovalObservation{}) == nil {
					t.Error("closed callback accepted")
				}
			}
		}()
	}
	s.mu.Lock()
	for i := 0; i < 100; i++ {
		s.removalBatch = &ruleRemovalBatchState{ID: fmt.Sprint(i)}
		s.removalBatch = nil
	}
	s.mu.Unlock()
	wait.Wait()
}

func TestRuleRemovalBatchJournalSeparatesSourceCursorsAndPrepare(t *testing.T) {
	s, r, _ := removalDiscardFixture(t)
	r.failTail = true
	if _, err := s.DiscardPrepared(context.Background(), discardRequest(s)); err == nil || s.removalBatch == nil {
		t.Fatal("fixture did not retain an unresolved batch")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.removalBatch
	index, digest := s.preparedDiscard.Index, stateMAC(s.log.key, s.current)
	first := b.Steps[0]
	for _, kind := range []string{"cleanup-start", "cleanup-intent", "cleanup-result", "cleanup-readback", "cleanup-complete", "discard-start", "discard-intent", "discard-result", "discard-readback", "discard-complete", "intent", "rule-batch-start"} {
		event := event{Kind: kind, OperationID: first.OperationID, Spec: &first.Spec, StateDigest: digest,
			Batch: &ruleBatchState{ID: "overlapping-prepare", PlanChecksum: s.begin.Plan.Checksum, Generation: s.begin.Plan.Generation, OperationIDs: []string{"prepare-rule"}, BeforeDigest: digest}}
		if err := s.replay(event); err == nil {
			t.Fatalf("accepted overlapping source event %s", kind)
		}
	}
	if s.preparedDiscard.Index != index || stateMAC(s.log.key, s.current) != digest || s.removalBatch != b || s.cleanup.Pending != nil || s.preparedDiscard.Pending != nil {
		t.Fatal("rejected interleaving changed a source cursor")
	}
	start := *b
	start.Steps = nil
	s.removalBatch = nil
	defer func() { s.removalBatch = b }()
	for _, kind := range []string{"prepare-batch", "normal-intent", "source-pending"} {
		s.batch, s.lastIntent, s.preparedDiscard.Pending = nil, nil, nil
		switch kind {
		case "prepare-batch":
			s.batch = &ruleBatchState{ID: "unresolved-prepare"}
		case "normal-intent":
			s.lastIntent = &event{Kind: "intent"}
		case "source-pending":
			s.preparedDiscard.Pending = &cleanupAttempt{OperationID: "unresolved"}
		}
		if err := s.replayRuleRemovalBatch(event{Kind: "removal-batch-start", RemovalBatch: &start}); err == nil || s.removalBatch != nil {
			t.Fatalf("removal batch overlapped %s", kind)
		}
	}
	s.batch, s.lastIntent, s.preparedDiscard.Pending = nil, nil, nil
}

func removalCleanupFixture(t *testing.T) (*Supervisor, *removalBatchDevice, Config, routeros.Plan, Review) {
	t.Helper()
	return removalCleanupConfiguredFixture(t, false)
}

func removalCleanupConfiguredFixture(t *testing.T, manual bool) (*Supervisor, *removalBatchDevice, Config, routeros.Plan, Review) {
	t.Helper()
	base, initial := fixtureRuntime(t)
	r := &removalBatchDevice{preparedDiscardDevice: &preparedDiscardDevice{reviewDevice: &reviewDevice{memoryRuntime: base, secrets: map[string]string{}}}}
	config := testConfig(t, base)
	config.PollInterval = 10 * time.Second
	config.ManualCleanup = manual
	s, err := Open(config, r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	st, err := s.Stage(context.Background(), initial)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Confirm(context.Background(), st.TransactionID, st.PlanChecksum); err != nil {
		t.Fatal(err)
	}
	review, err := s.Review(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	router := routing.Router{ID: base.target.RouterID, ASN: 64520, RouterID: initial.Plan.Desired.RouterID, LocalIPv4: initial.Plan.Desired.LocalIPv4}
	plan, err := routeros.BuildPlan(base.target, router, nil, review.Snapshot, routeros.PlanOptions{AllowIdentityChange: true})
	if err != nil {
		t.Fatal(err)
	}
	st, err = s.Stage(context.Background(), Request{Plan: plan, KnownState: review.KnownState, Timeout: initial.Timeout, ExclusiveAccessAcknowledged: true})
	if err != nil {
		t.Fatal(err)
	}
	st, err = s.Confirm(context.Background(), st.TransactionID, st.PlanChecksum)
	if err != nil {
		t.Fatal(err)
	}
	// The real adapter has a live unowned session with zero floating history.
	r.owner.HistoryActions = 0
	return s, r, config, plan, review
}

func TestRuleRemovalBatchCleanupKeepsConfirmedActiveGeneration(t *testing.T) {
	s, r, _, plan, review := removalCleanupFixture(t)
	st, err := s.Cleanup(context.Background(), s.status.TransactionID, s.status.PlanChecksum)
	if err != nil || st.Phase != Confirmed || !st.ConfirmationVerified || st.Cleanup.Phase != CleanupComplete || r.batchCalls != 1 || r.batchWrites != 3 || r.cleanupWrites != 0 {
		t.Fatal("shared cleanup batch lost confirmed scope", err)
	}
	final, err := s.Review(context.Background())
	if err != nil || final.Snapshot.ActiveGeneration != plan.Generation {
		t.Fatal("confirmed desired state changed", err)
	}
	if reflect.DeepEqual(review.Snapshot, final.Snapshot) {
		t.Fatal("fixture never changed its active generation")
	}
}

func TestRuleRemovalBatchCleanupUnknownKeepsOriginalCursor(t *testing.T) {
	for _, effect := range []bool{false, true} {
		t.Run(fmt.Sprint(effect), func(t *testing.T) {
			s, r, config, _, _ := removalCleanupFixture(t)
			r.unknownNoEffect, r.unknownApplied = !effect, effect
			if _, err := s.Cleanup(context.Background(), s.status.TransactionID, s.status.PlanChecksum); err == nil || r.batchWrites != 1 {
				t.Fatal("cleanup unknown was not stopped")
			}
			if _, err := s.Cleanup(context.Background(), s.status.TransactionID, s.status.PlanChecksum); err == nil || r.batchWrites != 1 {
				t.Fatal("cleanup reused an unknown runtime")
			}
			next, fresh := reopenRemovalFixture(t, s, r, config)
			_, err := next.Recover(context.Background())
			if fresh.batchCalls != 0 || fresh.cleanupBinds != 0 || fresh.cleanupWrites != 0 {
				t.Fatal("cleanup recovery wrote or bound a bridge")
			}
			if !effect {
				if err == nil || next.cleanup.Pending == nil || next.cleanup.Index != 0 {
					t.Fatal("still-present cleanup unknown advanced")
				}
				if _, err := next.Cleanup(context.Background(), next.status.TransactionID, next.status.PlanChecksum); err == nil || fresh.batchCalls != 0 || fresh.cleanupWrites != 0 {
					t.Fatal("cleanup repeated an unknown Remove")
				}
				return
			}
			if err != nil || next.cleanup.Index != 1 {
				t.Fatal("cleanup did not recover the proved prefix", err)
			}
			status, err := next.Cleanup(context.Background(), next.status.TransactionID, next.status.PlanChecksum)
			if err != nil || status.Cleanup.Phase != CleanupComplete || !status.ConfirmationVerified || r.batchWrites+fresh.batchWrites+fresh.cleanupWrites != 3 {
				t.Fatal("cleanup did not preserve original authorization", err)
			}
		})
	}
}

func TestRuleRemovalBatchCleanupBackgroundContextIsBounded(t *testing.T) {
	s, r, _, _, _ := removalCleanupFixture(t)
	s.config.PrepareTimeout = 30 * time.Millisecond
	r.waitForContext = true
	started := time.Now()
	_, err := s.Cleanup(context.Background(), s.status.TransactionID, s.status.PlanChecksum)
	if err == nil || r.batchCalls != 1 || r.batchWrites != 0 || time.Since(started) > time.Second || !s.removalNeedsReopen {
		t.Fatal("background cleanup batch was not bounded or safely stopped", err)
	}
}
