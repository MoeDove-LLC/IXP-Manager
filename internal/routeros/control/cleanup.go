package control

import (
	"context"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
)

const cleanupAwaitingContinuation = "cleanup-awaiting-explicit-continuation"

type cleanupAttempt struct {
	OperationID    string       `json:"operationId"`
	Spec           mutationSpec `json:"spec"`
	BeforeDigest   string       `json:"beforeDigest"`
	ResultRecorded bool         `json:"resultRecorded"`
	Acknowledged   bool         `json:"acknowledged"`
}
type cleanupProgress struct {
	OperationIDs []string        `json:"operationIds"`
	Index        int             `json:"index"`
	Pending      *cleanupAttempt `json:"pending,omitempty"`
}

func cleanupIDs(p routeros.Plan) []string {
	ids := []string{}
	for _, op := range p.Operations {
		if op.Phase == routeros.Cleanup {
			ids = append(ids, op.ID)
		}
	}
	return ids
}
func (s *Supervisor) replayCleanup(ev event) error {
	if s.removalBatch != nil && ev.Kind != "cleanup-status" {
		return failure("journal-cleanup-batch-unresolved")
	}
	if s.begin == nil || !s.status.ConfirmationVerified || s.status.Phase != Confirmed {
		return failure("cleanup-without-confirmed-receipt")
	}
	switch ev.Kind {
	case "cleanup-start":
		if ev.Cleanup == nil || ev.Cleanup.Index != 0 || ev.Cleanup.Pending != nil {
			return failure("journal-cleanup-start-invalid")
		}
		want := cleanupIDs(s.begin.Plan)
		if len(want) != len(ev.Cleanup.OperationIDs) {
			return failure("journal-cleanup-scope-invalid")
		}
		for i := range want {
			if want[i] != ev.Cleanup.OperationIDs[i] {
				return failure("journal-cleanup-scope-invalid")
			}
		}
		s.cleanup = cleanupProgress{OperationIDs: append([]string(nil), want...)}
		s.status.Cleanup = CleanupStatus{Phase: CleanupPending, Total: len(want), UpdatedAt: ev.At}
		if len(want) == 0 {
			s.status.Cleanup.Phase = CleanupComplete
		}
	case "cleanup-status":
		if ev.Status == nil || ev.Status.TransactionID != s.status.TransactionID || ev.Status.PlanChecksum != s.status.PlanChecksum || ev.Status.Phase != Confirmed || !ev.Status.ConfirmationVerified || ev.Status.Cleanup.Completed != s.cleanup.Index || ev.Status.Cleanup.Total != len(s.cleanup.OperationIDs) {
			return failure("journal-cleanup-status-invalid")
		}
		s.status = *ev.Status
	case "cleanup-intent":
		if s.cleanup.Pending != nil || s.cleanup.Index >= len(s.cleanup.OperationIDs) || ev.OperationID != s.cleanup.OperationIDs[s.cleanup.Index] || ev.Spec == nil || ev.Spec.Action != api.Remove || !validObjectID(strings.ToUpper(ev.Spec.ID)) || ev.Spec.CredentialDigest != "" || len(ev.Spec.Attributes) != 0 || ev.StateDigest != stateMAC(s.log.key, s.current) {
			return failure("journal-cleanup-intent-invalid")
		}
		s.cleanup.Pending = &cleanupAttempt{OperationID: ev.OperationID, Spec: *ev.Spec, BeforeDigest: ev.StateDigest}
		s.status.Cleanup.LastOperationID = ev.OperationID
	case "cleanup-result":
		p := s.cleanup.Pending
		if p == nil || p.OperationID != ev.OperationID || p.ResultRecorded {
			return failure("journal-cleanup-result-invalid")
		}
		p.ResultRecorded = true
		p.Acknowledged = ev.Acknowledged
	case "cleanup-readback":
		p := s.cleanup.Pending
		if p == nil || p.OperationID != ev.OperationID || ev.Delta == nil || !ev.Delta.Remove || ev.Delta.Key != objectKey(p.Spec.Resource, p.Spec.ID) {
			return failure("journal-cleanup-readback-invalid")
		}
		applyDelta(&s.current, *ev.Delta)
		if ev.StateDigest != stateMAC(s.log.key, s.current) {
			return failure("journal-cleanup-readback-digest")
		}
		s.cleanup.Index++
		s.cleanup.Pending = nil
		s.status.Cleanup.Completed = s.cleanup.Index
	case "cleanup-complete":
		if s.cleanup.Pending != nil || s.cleanup.Index != len(s.cleanup.OperationIDs) || ev.StateDigest != stateMAC(s.log.key, s.current) {
			return failure("journal-cleanup-complete-invalid")
		}
		s.status.Cleanup.Phase = CleanupComplete
		s.status.Cleanup.LastCode = ""
		s.status.Cleanup.UpdatedAt = ev.At
	}
	return nil
}
func (s *Supervisor) cleanupEvent(ev event) error {
	ev.At = time.Now().UTC()
	if err := s.record(ev); err != nil {
		return err
	}
	if err := s.replayCleanup(ev); err != nil {
		s.log.poisoned = true
		s.status.Cleanup.Phase = CleanupUncertain
		s.publishStatus()
		return err
	}
	s.publishStatus()
	return nil
}
func (s *Supervisor) initializeCleanup() error {
	if s.status.Cleanup.Phase != "" && s.status.Cleanup.Phase != CleanupNone {
		return nil
	}
	if err := s.cleanupEvent(event{Kind: "cleanup-start", Cleanup: &cleanupProgress{OperationIDs: cleanupIDs(s.begin.Plan)}}); err != nil {
		return err
	}
	if s.config.ManualCleanup && s.status.Cleanup.Phase == CleanupPending {
		return s.setCleanup(CleanupPending, cleanupAwaitingContinuation)
	}
	return nil
}
func (s *Supervisor) setCleanup(phase CleanupPhase, code string) error {
	next := s.status
	next.Cleanup.Phase = phase
	next.Cleanup.LastCode = code
	next.Cleanup.UpdatedAt = time.Now().UTC()
	next.Cleanup.Total = len(s.cleanup.OperationIDs)
	next.Cleanup.Completed = s.cleanup.Index
	return s.cleanupEvent(event{Kind: "cleanup-status", Status: &next})
}
func (s *Supervisor) cleanupKnown() routeros.KnownState {
	known, _ := transactionKnown(s.begin, true, true)
	names := map[string]bool{}
	chains := map[string]bool{}
	for _, obj := range s.current.Objects {
		if obj.Resource == api.BGPConnection {
			names[obj.Attributes["name"]] = true
		}
		if obj.Resource == api.FilterRule {
			chains[obj.Attributes["chain"]] = true
		}
	}
	peers := known.Peers[:0]
	for _, peer := range known.Peers {
		name, err := routeros.ManagedConnectionName(s.config.Target.RouterID, peer.ID)
		if err == nil && names[name] {
			peers = append(peers, peer)
		}
	}
	known.Peers = peers
	generations := known.Generations[:0]
	for _, gen := range known.Generations {
		id := gen.PolicyChecksum[:16]
		keep := id == s.begin.Plan.Generation
		for _, suffix := range []string{"in", "out", "src"} {
			keep = keep || chains[s.begin.Plan.Owner+"_g"+id+"_"+suffix]
		}
		// Partial old generations remain local proof until their last rule is
		// removed. Strict snapshot is required only after the cleanup completes.
		if keep {
			generations = append(generations, gen)
		}
	}
	known.Generations = generations
	return known
}
func (s *Supervisor) observeCleanup(ctx context.Context) (Observation, deviceState, error) {
	known := s.cleanupKnown()
	o, state, err := s.observe(ctx, ReadRequest{KnownState: known, CredentialAuthority: authorityFor(s.current)})
	if err == nil {
		return o, state, nil
	}
	p := s.cleanup.Pending
	if p == nil || p.Spec.Resource != api.BGPConnection {
		return o, state, err
	}
	// If the approved Remove reached the device, the deleted object cannot
	// satisfy a credential grant. Retry this READ with only that ID excluded.
	without := cloneState(s.current)
	delete(without.Objects, objectKey(p.Spec.Resource, p.Spec.ID))
	return s.observe(ctx, ReadRequest{KnownState: known, CredentialAuthority: authorityFor(without)})
}
func (s *Supervisor) cleanupFailure(code string) (Status, error) {
	s.cleanupAuto = false
	if !s.log.poisoned {
		phase := CleanupPending
		if s.cleanup.Pending != nil || s.removalBatch != nil {
			phase = CleanupUncertain
		}
		_ = s.setCleanup(phase, code)
	}
	return s.status, failure(code)
}

// Cleanup continues only the original confirmed plan's authorized deletion
// suffix. It is an execution request, not a new approval or a plan rebind.
func (s *Supervisor) Cleanup(ctx context.Context, id, checksum string) (Status, error) {
	return s.CleanupBounded(ctx, CleanupRequest{TransactionID: id, PlanChecksum: checksum})
}

func (s *Supervisor) CleanupBounded(ctx context.Context, request CleanupRequest) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.publishStatus()
	if request.MaxBatches < 0 || request.MaxBatches > MaxCleanupRequestBatches {
		return s.status, failure("cleanup-invalid-batch-limit")
	}
	if s.closed || s.log.poisoned || s.status.Phase != Confirmed || !s.status.ConfirmationVerified || s.status.TransactionID != request.TransactionID || s.status.PlanChecksum != request.PlanChecksum {
		return s.status, failure("cleanup-not-authorized")
	}
	// Only the authorized caller may take over from the automatic watcher.
	// This remains off after the request reaches its verified boundary.
	s.cleanupAuto = false
	return s.cleanupLocked(ctx, request.MaxBatches)
}
func (s *Supervisor) cleanupLocked(ctx context.Context, limit int) (Status, error) {
	if s.removalNeedsReopen {
		return s.cleanupFailure("rule-removal-runtime-reopen-required")
	}
	if err := s.recoverRuleRemovalBatch(ctx, RuleRemovalCleanup); err != nil {
		return s.cleanupFailure(ruleRemovalFailureCode(err))
	}
	if err := s.initializeCleanup(); err != nil {
		return s.status, err
	}
	if s.status.Cleanup.Phase == CleanupComplete {
		s.cleanupAuto = false
		return s.status, nil
	}
	if s.cleanup.Pending != nil {
		if _, err := s.reconcileCleanupIntent(ctx); err != nil {
			return s.cleanupFailure("cleanup-outcome-unresolved")
		}
	}
	if s.cleanup.Index == len(s.cleanup.OperationIDs) {
		return s.finishCleanup(ctx)
	}
	runtime, ok := s.runtime.(CleanupRuntime)
	if !ok || !s.runtime.Binding().CleanupVerified {
		return s.cleanupFailure("cleanup-native-gate-unverified")
	}
	bridge, err := routeros.NewCleanupBridge(s.begin.Plan)
	if err != nil {
		return s.cleanupFailure("cleanup-plan-invalid")
	}
	o, actual, err := s.observeCleanup(ctx)
	if err != nil || !o.Owner.Known || o.Owner.Active || stateMAC(s.log.key, actual) != stateMAC(s.log.key, s.current) {
		return s.cleanupFailure("cleanup-baseline-changed")
	}
	call, cancel := s.operationContext(ctx)
	err = runtime.BindCleanup(call, CleanupBinding{Plan: s.begin.Plan, RemainingOperationIDs: append([]string(nil), s.cleanup.OperationIDs[s.cleanup.Index:]...)})
	cancel()
	if err != nil {
		return s.cleanupFailure("cleanup-runtime-binding-failed")
	}
	if err = s.setCleanup(CleanupRunning, ""); err != nil {
		return s.status, err
	}
	for units := 0; s.cleanup.Index < len(s.cleanup.OperationIDs) && (limit == 0 || units < limit); units++ {
		if ctx.Err() != nil {
			return s.cleanupFailure("cleanup-context-ended")
		}
		batched, batchErr := s.tryRuleRemovalBatch(ctx, RuleRemovalCleanup, bridge)
		if batched {
			if batchErr != nil {
				return s.cleanupFailure(ruleRemovalFailureCode(batchErr))
			}
			continue
		}
		o, before, readErr := s.observeCleanup(ctx)
		if readErr != nil || !o.Owner.Known || o.Owner.Active || stateMAC(s.log.key, before) != stateMAC(s.log.key, s.current) {
			return s.cleanupFailure("cleanup-state-changed-before-write")
		}
		id := s.cleanup.OperationIDs[s.cleanup.Index]
		m, err := bridge.Mutation(id, o.Resolver)
		if err != nil || m.Phase != routeros.Cleanup || m.Request.Action != api.Remove {
			return s.cleanupFailure("cleanup-reference-unverified")
		}
		spec := specFor(s.log.key, m.Request)
		if err = s.cleanupEvent(event{Kind: "cleanup-intent", OperationID: id, Spec: &spec, StateDigest: stateMAC(s.log.key, s.current)}); err != nil {
			return s.status, err
		}
		call, cancel := s.operationContext(ctx)
		result, writeErr := runtime.ApplyCleanup(call, m)
		cancel()
		ack := writeErr == nil && result.Acknowledged && result.ID == ""
		if err = s.cleanupEvent(event{Kind: "cleanup-result", OperationID: id, Acknowledged: ack}); err != nil {
			return s.status, err
		}
		if _, err = s.reconcileCleanupIntent(ctx); err != nil {
			return s.cleanupFailure("cleanup-readback-unverified")
		}
		if !ack {
			if s.cleanup.Index == len(s.cleanup.OperationIDs) {
				return s.finishCleanup(ctx)
			}
			return s.cleanupFailure("cleanup-readback-resolved-runtime-reopen-required")
		}
	}
	if s.cleanup.Index == len(s.cleanup.OperationIDs) {
		return s.finishCleanup(ctx)
	}
	if ctx.Err() != nil {
		return s.cleanupFailure("cleanup-context-ended")
	}
	if s.cleanup.Pending != nil || s.removalBatch != nil || s.removalNeedsReopen {
		return s.cleanupFailure("cleanup-readback-unverified")
	}
	code := ""
	if !s.cleanupAuto && limit != 0 {
		code = cleanupAwaitingContinuation
	}
	if err = s.setCleanup(CleanupPending, code); err != nil {
		return s.status, err
	}
	return s.status, nil
}
func (s *Supervisor) reconcileCleanupIntent(ctx context.Context) (deviceState, error) {
	p := s.cleanup.Pending
	if p == nil {
		return s.current, nil
	}
	o, actual, err := s.observeCleanup(ctx)
	if err != nil {
		return deviceState{}, err
	}
	if !o.Owner.Known || o.Owner.Active {
		return deviceState{}, failure("cleanup-owner-not-clear")
	}
	delta, err := verifyMutation(s.log.key, s.begin.Plan.Owner, s.current, actual, p.Spec, "")
	if err != nil {
		return actual, failure("cleanup-removal-not-proven")
	}
	if err = s.cleanupEvent(event{Kind: "cleanup-readback", OperationID: p.OperationID, Delta: &delta, StateDigest: stateMAC(s.log.key, actual)}); err != nil {
		return deviceState{}, err
	}
	return actual, nil
}
func (s *Supervisor) finishCleanup(ctx context.Context) (Status, error) {
	o, actual, err := s.observeCleanup(ctx)
	if err != nil || !o.Owner.Known || o.Owner.Active || stateMAC(s.log.key, actual) != stateMAC(s.log.key, s.current) {
		return s.cleanupFailure("cleanup-final-state-unverified")
	}
	known := s.cleanupKnown()
	// Secrets are ephemeral getter values whose journal HMAC was just matched.
	// They are used only to require the normal Strict namespace validator.
	expected := routeros.Credentials{}
	for _, peer := range known.Peers {
		name, _ := routeros.ManagedConnectionName(s.config.Target.RouterID, peer.ID)
		for _, obj := range s.current.Objects {
			if obj.Resource == api.BGPConnection && obj.Attributes["name"] == name {
				value, ok := o.Credentials[obj.ID]
				if !ok || !value.Known {
					return s.cleanupFailure("cleanup-credentials-unverified")
				}
				raw := value.Value.CopyBytes()
				if credentialMAC(s.log.key, obj.ID, raw) != obj.CredentialDigest {
					clear(raw)
					return s.cleanupFailure("cleanup-credential-fingerprint-mismatch")
				}
				expected[peer.ID] = routeros.Credential{Revision: peer.Authentication.Revision, Password: string(raw)}
				clear(raw)
			}
		}
	}
	final, finalState, err := s.observe(ctx, ReadRequest{KnownState: known, BaselineCredentials: expected, Strict: true, CredentialAuthority: authorityFor(s.current)})
	clear(expected)
	if err != nil || final.Snapshot == nil || final.Snapshot.ActiveGeneration != s.begin.Plan.Generation || !final.Owner.Known || final.Owner.Active || stateMAC(s.log.key, finalState) != stateMAC(s.log.key, s.current) {
		return s.cleanupFailure("cleanup-strict-final-unverified")
	}
	if err = s.cleanupEvent(event{Kind: "cleanup-complete", StateDigest: stateMAC(s.log.key, s.current)}); err != nil {
		return s.status, err
	}
	s.cleanupAuto = false
	return s.status, nil
}
func (s *Supervisor) recoverCleanupLocked(ctx context.Context) (Status, error) {
	if s.log.poisoned {
		return s.status, failure("journal-integrity-unknown")
	}
	if err := s.recoverRuleRemovalBatch(ctx, RuleRemovalCleanup); err != nil {
		return s.status, err
	}
	if err := s.initializeCleanup(); err != nil {
		return s.status, err
	}
	if s.status.Cleanup.Phase == CleanupComplete {
		return s.status, nil
	}
	if s.cleanup.Pending != nil {
		if _, err := s.reconcileCleanupIntent(ctx); err != nil {
			return s.status, err
		}
	}
	if s.cleanup.Index == len(s.cleanup.OperationIDs) {
		return s.finishCleanup(ctx)
	}
	o, actual, err := s.observeCleanup(ctx)
	if err != nil || !o.Owner.Known || o.Owner.Active || stateMAC(s.log.key, actual) != stateMAC(s.log.key, s.current) {
		return s.status, failure("cleanup-recovery-state-unverified")
	}
	// Read-only recovery never BindCleanup/ApplyCleanup or replay any Remove.
	return s.status, nil
}
