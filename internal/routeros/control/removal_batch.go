package control

import (
	"context"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
)

func ruleRemovalFailureCode(err error) string {
	if value, ok := err.(*Failure); ok {
		return value.Code
	}
	return "rule-removal-failed"
}

func (s *Supervisor) observeRuleRemoval(ctx context.Context, mode RuleRemovalMode) (Observation, deviceState, error) {
	if mode == RuleRemovalCleanup {
		return s.observeCleanup(ctx)
	}
	if mode == RuleRemovalPreparedDiscard {
		return s.observePreparedDiscard(ctx)
	}
	return Observation{}, deviceState{}, failure("rule-removal-mode-invalid")
}

func (s *Supervisor) verifyRuleRemovalTail(actual deviceState) ([]stateDelta, *cleanupAttempt, error) {
	b := s.removalBatch
	if b == nil || b.BeforeDigest != stateMAC(s.log.key, s.current) || actual.ExternalDigest != s.current.ExternalDigest {
		return nil, nil, failure("rule-removal-global-state-changed")
	}
	if !validRuleRemovalProof(b.ProofMode) {
		return nil, nil, failure("rule-removal-proof-mode-invalid")
	}
	expected := cloneState(s.current)
	deltas := []stateDelta{}
	for _, step := range b.Steps {
		delta := stateDelta{Key: objectKey(step.Spec.Resource, step.Spec.ID), Remove: true}
		applyDelta(&expected, delta)
		deltas = append(deltas, delta)
	}
	if stateMAC(s.log.key, expected) == stateMAC(s.log.key, actual) {
		return deltas, nil, nil
	}
	if len(b.Steps) == 0 {
		return nil, nil, failure("rule-removal-unattempted-change")
	}
	if normalizedRuleRemovalProof(b.ProofMode) == RuleRemovalProofGlobalTail {
		for _, step := range b.Steps {
			if step.ResultRecorded && step.Acknowledged {
				if _, present := actual.Objects[objectKey(step.Spec.Resource, step.Spec.ID)]; present {
					return nil, nil, failure("rule-removal-acknowledged-target-present")
				}
			}
		}
	}
	last := b.Steps[len(b.Steps)-1]
	if last.AbsentVerified {
		return nil, nil, failure("rule-removal-observed-target-reappeared")
	}
	if !ruleRemovalStepCanRemainPending(b.ProofMode, last) {
		return nil, nil, failure("rule-removal-global-delta-unverified")
	}
	// At most the last intent can lack a completed absence. Commit only the
	// globally verified preceding prefix and retain that original unknown
	// intent in the ordinary source-specific cursor. It must never be resent.
	expected = cloneState(s.current)
	for _, delta := range deltas[:len(deltas)-1] {
		applyDelta(&expected, delta)
	}
	if stateMAC(s.log.key, expected) != stateMAC(s.log.key, actual) {
		return nil, nil, failure("rule-removal-global-delta-unverified")
	}
	pending := &cleanupAttempt{OperationID: last.OperationID, Spec: last.Spec, BeforeDigest: stateMAC(s.log.key, actual), ResultRecorded: last.ResultRecorded, Acknowledged: last.Acknowledged}
	return deltas[:len(deltas)-1], pending, nil
}

func (s *Supervisor) finishRuleRemovalBatch(ctx context.Context, ownerID string) (int, bool, error) {
	b := s.removalBatch
	if b == nil {
		return 0, false, failure("rule-removal-batch-unavailable")
	}
	o, actual, err := s.observeRuleRemoval(ctx, b.Mode)
	if err != nil || !s.ruleRemovalOwner(b.Mode, o.Owner, true) || ownerID != "" && o.Owner.SessionID != ownerID || !completeCredentials(actual) {
		return 0, false, failure("rule-removal-global-readback-unavailable")
	}
	deltas, pending, err := s.verifyRuleRemovalTail(actual)
	if err != nil {
		return 0, false, err
	}
	if err := s.removalEvent(event{Kind: "removal-batch-readback", BatchID: b.ID, Deltas: deltas, RemovalPending: pending, StateDigest: stateMAC(s.log.key, actual)}); err != nil {
		return 0, false, err
	}
	return len(deltas), pending != nil, nil
}

// tryRuleRemovalBatch chooses only an authorized contiguous generation-rule
// prefix. Unverified/ineligible runtimes retain the existing single-item path.
// A nil runtime return may mean a clean shorter prefix; it never means that
// all submitted operations happened. Only durable steps and actual global
// readback advance the formal source cursor.
func (s *Supervisor) tryRuleRemovalBatch(ctx context.Context, mode RuleRemovalMode, bridge *routeros.APIBridge) (bool, error) {
	binding := s.runtime.Binding()
	if !binding.RuleRemovalBatchVerified {
		return false, nil
	}
	runtime, ok := s.runtime.(RuleRemovalBatchRuntime)
	if !ok {
		return false, nil
	}
	if s.removalNeedsReopen || s.removalBatch != nil || s.batch != nil || s.lastIntent != nil {
		return true, failure("rule-removal-recovery-required")
	}
	ids, err := s.ruleRemovalIDs(mode)
	if err != nil {
		return true, err
	}
	if len(ids) < 2 {
		return false, nil
	}
	_, index, pending, err := s.removalCursor(mode)
	if err != nil || pending != nil || bridge == nil {
		return true, failure("rule-removal-unresolved-source")
	}
	o, actual, err := s.observeRuleRemoval(ctx, mode)
	if err != nil || !s.ruleRemovalOwner(mode, o.Owner, true) || !completeCredentials(actual) || stateMAC(s.log.key, actual) != stateMAC(s.log.key, s.current) {
		return true, failure("rule-removal-global-baseline-changed")
	}
	operations := make([]routeros.APIMutation, 0, len(ids))
	for _, id := range ids {
		mutation, err := bridge.Mutation(id, o.Resolver)
		if err != nil {
			return true, failure("rule-removal-reference-unverified")
		}
		operations = append(operations, mutation)
	}
	proofMode := RuleRemovalProofPerObject
	if binding.RuleRemovalGlobalTailVerified {
		proofMode = RuleRemovalProofGlobalTail
	}
	state := &ruleRemovalBatchState{ID: s.nextRemovalBatchID(mode, index), Mode: mode, ProofMode: proofMode, TransactionID: s.status.TransactionID, PlanChecksum: s.status.PlanChecksum, ScopeChecksum: s.removalScope(mode), OperationIDs: ids, StartIndex: index, BeforeDigest: stateMAC(s.log.key, s.current)}
	if err := s.removalEvent(event{Kind: "removal-batch-start", RemovalBatch: state}); err != nil {
		return true, err
	}
	expected := make([]routeros.APIMutation, len(operations))
	for i, operation := range operations {
		expected[i] = operation
		expected[i].NativeChecks = append([]string{}, operation.NativeChecks...)
	}
	recorder := &ruleRemovalRecorder{s: s, mode: mode, batchID: state.ID, expected: expected, ownerID: o.Owner.SessionID, started: time.Now()}
	batch := RuleRemovalBatch{ID: state.ID, Mode: mode, ProofMode: proofMode, TransactionID: state.TransactionID, PlanChecksum: state.PlanChecksum, ScopeChecksum: state.ScopeChecksum, Operations: operations}
	// The runtime's 30-second soft window only stops new intents. Keep a total
	// preparation-budget bound even when automatic Cleanup has a background
	// context; the runtime may impose a shorter observation policy of its own.
	batchCtx, cancel := context.WithTimeout(ctx, s.config.PrepareTimeout)
	writeErr := runtime.ApplyRuleRemovalBatch(batchCtx, batch, recorder)
	recorder.close()
	cancel()
	if writeErr == nil && !recorder.stopped {
		for _, step := range s.removalBatch.Steps {
			if !ruleRemovalStepCanAdvance(proofMode, step) {
				writeErr = failure("rule-removal-runtime-incomplete")
				break
			}
		}
	}
	if writeErr != nil || recorder.stopped {
		s.removalNeedsReopen = true
	}
	if s.log.poisoned {
		return true, failure("journal-unavailable")
	}
	count, unresolved, err := s.finishRuleRemovalBatch(ctx, o.Owner.SessionID)
	if err != nil {
		s.removalNeedsReopen = true
		return true, err
	}
	if unresolved {
		s.removalNeedsReopen = true
		return true, failure("rule-removal-outcome-unresolved")
	}
	if writeErr != nil || recorder.stopped {
		return true, failure("rule-removal-runtime-reopen-required")
	}
	if count == 0 {
		return true, failure("rule-removal-no-progress")
	}
	return true, nil
}

func (s *Supervisor) recoverRuleRemovalBatch(ctx context.Context, mode RuleRemovalMode) error {
	if s.removalBatch == nil {
		return nil
	}
	if s.log.poisoned || s.removalBatch.Mode != mode {
		return failure("rule-removal-recovery-unavailable")
	}
	_, pending, err := s.finishRuleRemovalBatch(ctx, "")
	if err != nil {
		return err
	}
	if pending {
		return failure("rule-removal-outcome-unresolved")
	}
	return nil
}
