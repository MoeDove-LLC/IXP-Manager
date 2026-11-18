package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
)

func (s *Supervisor) discardOwnerVerified(owner Owner) bool {
	return owner.Known && owner.Alive && owner.HistoryKnown && owner.HistoryActions == 0 && !owner.Active && !owner.ThisSession && validSessionID(owner.SessionID) && owner.SessionID != s.status.SessionID
}

func (s *Supervisor) preparedDiscardKnown(state deviceState) (routeros.KnownState, error) {
	known, err := transactionKnown(s.begin, false, true)
	if err != nil {
		return known, err
	}
	names, chains := map[string]bool{}, map[string]bool{}
	for _, object := range state.Objects {
		if object.Resource == api.BGPConnection {
			names[object.Attributes["name"]] = true
		}
		if object.Resource == api.FilterRule {
			chains[object.Attributes["chain"]] = true
		}
	}
	peers := known.Peers[:0]
	for _, peer := range known.Peers {
		name, err := routeros.ManagedConnectionName(s.config.Target.RouterID, peer.ID)
		if err != nil {
			return known, failure("discard-manifest-invalid")
		}
		if names[name] {
			peers = append(peers, peer)
		}
	}
	known.Peers = peers
	baselineGenerations := map[string]bool{}
	for _, generation := range s.begin.Known.Generations {
		baselineGenerations[generation.PolicyChecksum] = true
	}
	generations := known.Generations[:0]
	for _, generation := range known.Generations {
		if len(generation.PolicyChecksum) != 64 {
			return known, failure("discard-manifest-invalid")
		}
		keep := baselineGenerations[generation.PolicyChecksum]
		for _, suffix := range []string{"in", "out", "src"} {
			keep = keep || chains[s.begin.Plan.Owner+"_g"+generation.PolicyChecksum[:16]+"_"+suffix]
		}
		if keep {
			generations = append(generations, generation)
		}
	}
	known.Generations = generations
	return known, nil
}

func (s *Supervisor) observePreparedDiscard(ctx context.Context) (Observation, deviceState, error) {
	known, err := s.preparedDiscardKnown(s.current)
	if err != nil {
		return Observation{}, deviceState{}, err
	}
	o, state, err := s.observe(ctx, ReadRequest{KnownState: known, CredentialAuthority: authorityFor(s.current)})
	if err == nil {
		return o, state, nil
	}
	pending := s.preparedDiscard.Pending
	if pending == nil || pending.Spec.Resource != api.BGPConnection {
		return o, state, err
	}
	// A successful pending Remove makes its old getter grant inapplicable.
	// Exclude only that exact ID in a new read; no unknown/empty credential is
	// inferred, and the complete resulting state must still be proved below.
	without := cloneState(s.current)
	applyDelta(&without, stateDelta{Key: objectKey(pending.Spec.Resource, pending.Spec.ID), Remove: true})
	known, knownErr := s.preparedDiscardKnown(without)
	if knownErr != nil {
		return o, state, knownErr
	}
	return s.observe(ctx, ReadRequest{KnownState: known, CredentialAuthority: authorityFor(without)})
}

func (s *Supervisor) preparedDiscardFailure(code string) (Status, error) {
	if !s.log.poisoned && s.preparedDiscard.Checksum != "" && s.status.PreparedDiscard.Phase != PreparedDiscardComplete {
		phase := PreparedDiscardPending
		if s.preparedDiscard.Pending != nil || s.removalBatch != nil {
			phase = PreparedDiscardUncertain
		}
		_ = s.setPreparedDiscard(phase, code)
	}
	return s.status, failure(code)
}

// DiscardPrepared is a separately explicit operation on a fresh runtime. It
// never resumes activation, takes Safe Mode, modifies a baseline object, or
// repeats an uncertain Remove. Large rule sets use the same per-item WAL path;
// this implementation alone does not claim large-scale native usability.
func (s *Supervisor) DiscardPrepared(ctx context.Context, request DiscardRequest) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.publishStatus()
	if request.MaxBatches < 0 || request.MaxBatches > MaxDiscardRequestBatches {
		return s.status, failure("discard-invalid-batch-limit")
	}
	if s.closed || s.log.poisoned || s.begin == nil || s.begin.Restore != nil || s.rollback == nil || s.status.Phase != RolledBack || !s.status.RollbackVerified || s.status.ConfirmationVerified || request.TransactionID != s.status.TransactionID || request.PlanChecksum != s.status.PlanChecksum {
		return s.status, failure("discard-not-authorized")
	}
	if !request.ExclusiveAccessAcknowledged {
		return s.status, failure("exclusive-access-precondition-required")
	}
	if !s.reopened || s.preparedDiscardNeedsReopen || s.removalNeedsReopen {
		return s.status, failure("discard-runtime-reopen-required")
	}
	if s.status.PreparedDiscard.Phase == PreparedDiscardComplete {
		return s.finishPreparedDiscard(ctx)
	}
	runtime, ok := s.runtime.(PreparedDiscardRuntime)
	if !ok || !s.runtime.Binding().PreparedDiscardVerified {
		return s.status, failure("discard-native-gate-unverified")
	}
	work, cancel := context.WithTimeout(ctx, s.config.PrepareTimeout)
	defer cancel()
	if err := s.recoverRuleRemovalBatch(work, RuleRemovalPreparedDiscard); err != nil {
		return s.preparedDiscardFailure(ruleRemovalFailureCode(err))
	}
	if s.preparedDiscard.Pending != nil {
		if _, err := s.reconcilePreparedDiscardIntent(work, ""); err != nil {
			return s.preparedDiscardFailure("discard-outcome-unresolved")
		}
	}
	var observed Observation
	if s.status.PreparedDiscard.Phase == PreparedDiscardNone {
		progress, err := s.newPreparedDiscardProgress()
		if err != nil {
			return s.status, err
		}
		raw, err := json.Marshal(progress)
		if err != nil || s.log.size+int64(len(raw))+int64(len(progress.OperationIDs))*4096+(1<<20) > maxJournalBytes {
			return s.status, failure("discard-journal-capacity-insufficient")
		}
		known, err := transactionKnown(s.begin, false, true)
		if err != nil {
			return s.status, err
		}
		o, actual, err := s.observe(work, ReadRequest{KnownState: known, CredentialAuthority: authorityFor(*s.rollback)})
		if err != nil || !s.discardOwnerVerified(o.Owner) || !completeCredentials(actual) || stateMAC(s.log.key, actual) != stateMAC(s.log.key, *s.rollback) {
			return s.status, failure("discard-prepared-checkpoint-changed")
		}
		observed = o
		binding := PreparedDiscardBinding{Plan: s.begin.Plan, BaselineKnown: s.begin.Known, Objects: progress.Objects, RemainingOperationIDs: progress.OperationIDs}
		call, done := s.operationContext(work)
		err = runtime.BindPreparedDiscard(call, binding)
		done()
		if err != nil {
			return s.status, failure("discard-runtime-binding-failed")
		}
		if err := s.preparedDiscardEvent(event{Kind: "discard-start", PreparedDiscard: &progress, StateDigest: stateMAC(s.log.key, actual)}); err != nil {
			return s.status, err
		}
	} else {
		o, actual, err := s.observePreparedDiscard(work)
		if err != nil || !s.discardOwnerVerified(o.Owner) || !completeCredentials(actual) || stateMAC(s.log.key, actual) != stateMAC(s.log.key, s.current) {
			return s.preparedDiscardFailure("discard-current-state-changed")
		}
		observed = o
		binding := PreparedDiscardBinding{Plan: s.begin.Plan, BaselineKnown: s.begin.Known, Objects: s.preparedDiscard.Objects, RemainingOperationIDs: append([]string{}, s.preparedDiscard.OperationIDs[s.preparedDiscard.Index:]...)}
		call, done := s.operationContext(work)
		err = runtime.BindPreparedDiscard(call, binding)
		done()
		if err != nil {
			return s.preparedDiscardFailure("discard-runtime-binding-failed")
		}
	}
	if s.preparedDiscard.Index == len(s.preparedDiscard.OperationIDs) {
		return s.finishPreparedDiscard(work)
	}
	bridge, err := routeros.NewPreparedDiscardBridge(s.begin.Plan, s.preparedDiscard.Objects)
	if err != nil {
		return s.preparedDiscardFailure("discard-plan-invalid")
	}
	if err := s.setPreparedDiscard(PreparedDiscardRunning, ""); err != nil {
		return s.status, err
	}
	sessionID := observed.Owner.SessionID
	verifiedBatches := 0
	for s.preparedDiscard.Index < len(s.preparedDiscard.OperationIDs) {
		if work.Err() != nil {
			return s.preparedDiscardFailure("discard-context-ended")
		}
		batched, batchErr := s.tryRuleRemovalBatch(work, RuleRemovalPreparedDiscard, bridge)
		if batched {
			if batchErr != nil {
				return s.preparedDiscardFailure(ruleRemovalFailureCode(batchErr))
			}
			verifiedBatches++
			if stopped, status, err := s.stopPreparedDiscardAtBoundary(request.MaxBatches, verifiedBatches); stopped || err != nil {
				return status, err
			}
			continue
		}
		o, actual, err := s.observePreparedDiscard(work)
		if err != nil || !s.discardOwnerVerified(o.Owner) || o.Owner.SessionID != sessionID || !completeCredentials(actual) || stateMAC(s.log.key, actual) != stateMAC(s.log.key, s.current) {
			return s.preparedDiscardFailure("discard-state-changed-before-write")
		}
		id := s.preparedDiscard.OperationIDs[s.preparedDiscard.Index]
		mutation, err := bridge.Mutation(id, o.Resolver)
		if err != nil || mutation.Phase != routeros.DiscardPrepared || mutation.Request.Action != api.Remove {
			return s.preparedDiscardFailure("discard-reference-unverified")
		}
		spec := specFor(s.log.key, mutation.Request)
		if err := s.preparedDiscardEvent(event{Kind: "discard-intent", OperationID: id, Spec: &spec, StateDigest: stateMAC(s.log.key, s.current)}); err != nil {
			return s.status, err
		}
		call, done := s.operationContext(work)
		result, writeErr := runtime.ApplyPreparedDiscard(call, mutation)
		done()
		ack := writeErr == nil && result.Acknowledged && result.ID == ""
		if !ack {
			s.preparedDiscardNeedsReopen = true
		}
		if err := s.preparedDiscardEvent(event{Kind: "discard-result", OperationID: id, Acknowledged: ack}); err != nil {
			return s.status, err
		}
		if _, err := s.reconcilePreparedDiscardIntent(work, sessionID); err != nil {
			return s.preparedDiscardFailure("discard-removal-not-proven")
		}
		if !ack {
			return s.preparedDiscardFailure("discard-runtime-reopen-required")
		}
		verifiedBatches++
		if stopped, status, err := s.stopPreparedDiscardAtBoundary(request.MaxBatches, verifiedBatches); stopped || err != nil {
			return status, err
		}
	}
	return s.finishPreparedDiscard(work)
}

// Called only after actual global readback and its durable cursor event. A
// request-local counter includes single-item fallback and cannot reset through
// another loop iteration. Final baseline/Strict checks still decide Complete.
func (s *Supervisor) stopPreparedDiscardAtBoundary(limit, verified int) (bool, Status, error) {
	if limit == 0 || verified < limit || s.preparedDiscard.Index == len(s.preparedDiscard.OperationIDs) {
		return false, s.status, nil
	}
	if s.log.poisoned || s.preparedDiscard.Pending != nil || s.removalBatch != nil || s.preparedDiscardNeedsReopen || s.removalNeedsReopen {
		return false, s.status, failure("discard-boundary-unverified")
	}
	if err := s.setPreparedDiscard(PreparedDiscardPending, "discard-awaiting-explicit-continuation"); err != nil {
		return false, s.status, err
	}
	return true, s.status, nil
}

func (s *Supervisor) reconcilePreparedDiscardIntent(ctx context.Context, sessionID string) (deviceState, error) {
	pending := s.preparedDiscard.Pending
	if pending == nil {
		return s.current, nil
	}
	o, actual, err := s.observePreparedDiscard(ctx)
	if err != nil || !s.discardOwnerVerified(o.Owner) || sessionID != "" && o.Owner.SessionID != sessionID || !completeCredentials(actual) {
		return deviceState{}, failure("discard-readback-unavailable")
	}
	delta, err := verifyMutation(s.log.key, s.begin.Plan.Owner, s.current, actual, pending.Spec, "")
	if err != nil {
		return actual, failure("discard-removal-not-proven")
	}
	if err := s.preparedDiscardEvent(event{Kind: "discard-readback", OperationID: pending.OperationID, Delta: &delta, StateDigest: stateMAC(s.log.key, actual)}); err != nil {
		return deviceState{}, err
	}
	return actual, nil
}

func (s *Supervisor) finishPreparedDiscard(ctx context.Context) (Status, error) {
	if s.preparedDiscard.Pending != nil || s.preparedDiscard.Index != len(s.preparedDiscard.OperationIDs) || stateMAC(s.log.key, s.current) != stateMAC(s.log.key, s.begin.Baseline) {
		return s.preparedDiscardFailure("discard-baseline-not-restored")
	}
	// Do not call transactionKnown(false,false): it still adds desired peers.
	raw, _ := json.Marshal(s.begin.Known)
	var known routeros.KnownState
	if json.Unmarshal(raw, &known) != nil {
		return s.preparedDiscardFailure("discard-baseline-manifest-invalid")
	}
	o, actual, err := s.observe(ctx, ReadRequest{KnownState: known, CredentialAuthority: authorityFor(s.begin.Baseline)})
	if err != nil || !s.discardOwnerVerified(o.Owner) || !completeCredentials(actual) || stateMAC(s.log.key, actual) != stateMAC(s.log.key, s.begin.Baseline) {
		return s.preparedDiscardFailure("discard-final-state-unverified")
	}
	expected := routeros.Credentials{}
	defer clear(expected)
	for _, peer := range known.Peers {
		name, err := routeros.ManagedConnectionName(s.config.Target.RouterID, peer.ID)
		if err != nil {
			return s.preparedDiscardFailure("discard-baseline-manifest-invalid")
		}
		found := false
		for _, object := range actual.Objects {
			if object.Resource != api.BGPConnection || object.Attributes["name"] != name {
				continue
			}
			value, ok := o.Credentials[object.ID]
			secret := value.Value.CopyBytes()
			if !ok || !value.Matches(secret) || credentialMAC(s.log.key, object.ID, secret) != object.CredentialDigest {
				clear(secret)
				return s.preparedDiscardFailure("discard-final-credential-unverified")
			}
			expected[peer.ID] = routeros.Credential{Revision: peer.Authentication.Revision, Password: string(secret)}
			clear(secret)
			found = true
		}
		if !found {
			return s.preparedDiscardFailure("discard-final-credential-unverified")
		}
	}
	final, state, err := s.observe(ctx, ReadRequest{KnownState: known, BaselineCredentials: expected, CredentialAuthority: authorityFor(s.begin.Baseline), Strict: true})
	if err != nil || final.Snapshot == nil || !s.discardOwnerVerified(final.Owner) || final.Owner.SessionID != o.Owner.SessionID || stateMAC(s.log.key, state) != stateMAC(s.log.key, s.begin.Baseline) {
		return s.preparedDiscardFailure("discard-final-strict-unverified")
	}
	encoded, err := json.Marshal(final.Snapshot)
	sum := sha256.Sum256(encoded)
	if err != nil || hex.EncodeToString(sum[:]) != s.begin.Plan.BaseChecksum {
		return s.preparedDiscardFailure("discard-original-snapshot-mismatch")
	}
	if s.status.PreparedDiscard.Phase != PreparedDiscardComplete {
		if err := s.preparedDiscardEvent(event{Kind: "discard-complete", StateDigest: stateMAC(s.log.key, state), Code: s.begin.Plan.BaseChecksum}); err != nil {
			return s.status, err
		}
	}
	return s.status, nil
}

func (s *Supervisor) recoverPreparedDiscardLocked(ctx context.Context) (Status, error) {
	if s.log.poisoned || s.preparedDiscard.Checksum == "" {
		return s.status, failure("discard-journal-unavailable")
	}
	if err := s.recoverRuleRemovalBatch(ctx, RuleRemovalPreparedDiscard); err != nil {
		return s.status, err
	}
	if s.preparedDiscard.Pending != nil {
		if _, err := s.reconcilePreparedDiscardIntent(ctx, ""); err != nil {
			return s.status, err
		}
	}
	if s.preparedDiscard.Index == len(s.preparedDiscard.OperationIDs) {
		return s.finishPreparedDiscard(ctx)
	}
	o, actual, err := s.observePreparedDiscard(ctx)
	if err != nil || !s.discardOwnerVerified(o.Owner) || !completeCredentials(actual) || stateMAC(s.log.key, actual) != stateMAC(s.log.key, s.current) {
		return s.status, failure("discard-recovery-state-unverified")
	}
	// Reopening/recovery never binds or sends a Remove. Continuing the remaining
	// suffix requires a new explicit DiscardPrepared request.
	code := "discard-awaiting-explicit-continuation"
	if s.preparedDiscardNeedsReopen {
		code = "discard-runtime-reopen-required"
	}
	if s.status.PreparedDiscard.Phase != PreparedDiscardPending || s.status.PreparedDiscard.LastCode != code {
		if err := s.setPreparedDiscard(PreparedDiscardPending, code); err != nil {
			return s.status, err
		}
	}
	return s.status, nil
}
