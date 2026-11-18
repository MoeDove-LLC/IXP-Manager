package control

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
)

type Supervisor struct {
	removalBatch               *ruleRemovalBatchState
	removalNeedsReopen         bool
	preparedDiscard            preparedDiscardProgress
	preparedCreates            map[string]preparedCreate
	preparedOperations         map[string]routeros.Operation
	preparedDiscardNeedsReopen bool
	lastResult                 *event
	cleanup                    cleanupProgress
	cleanupAuto                bool
	batch                      *ruleBatchState
	mu                         sync.Mutex
	config                     Config
	runtime                    Runtime
	log                        *journal
	status                     Status
	begin                      *beginRecord
	current                    deviceState
	rollback                   *deviceState
	lastIntent                 *event
	liveSession                string
	reopened                   bool
	closed                     bool
	done                       chan struct{}
	watchDone                  chan struct{}
	timer                      *time.Timer
	ownerMu                    sync.Mutex
	ownerEpoch                 uint64
	confirmIntent              bool
	lastRecoveryObservation    string
	desiredCredentials         routeros.Credentials
	desiredVerified            bool
	published                  atomic.Pointer[Status]
}

func checkedConfig(c Config) (Config, error) {
	if c.OperationTimeout == 0 {
		c.OperationTimeout = 10 * time.Second
	}
	if c.PrepareTimeout == 0 {
		c.PrepareTimeout = 10 * time.Minute
	}
	if c.MinConfirmationTimeout == 0 {
		c.MinConfirmationTimeout = 10 * time.Second
	}
	if c.MaxConfirmationTimeout == 0 {
		c.MaxConfirmationTimeout = 5 * time.Minute
	}
	if c.PollInterval == 0 {
		c.PollInterval = time.Second
	}
	if c.OperationTimeout < time.Millisecond || c.OperationTimeout > 30*time.Second || c.PrepareTimeout < time.Second || c.PrepareTimeout > 15*time.Minute || c.MinConfirmationTimeout < 100*time.Millisecond || c.MaxConfirmationTimeout < c.MinConfirmationTimeout || c.MaxConfirmationTimeout > 5*time.Minute || c.PollInterval < 10*time.Millisecond || c.PollInterval > 10*time.Second {
		return c, failure("invalid-timeout-bounds")
	}
	return c, nil
}
func Open(config Config, runtime Runtime) (*Supervisor, error) {
	c, err := checkedConfig(config)
	if err != nil {
		return nil, err
	}
	if runtime == nil || runtime.Binding().Target != c.Target {
		return nil, failure("runtime-target-mismatch")
	}
	j, events, err := openJournal(c.Dir, c.Target)
	if err != nil {
		return nil, err
	}
	s := &Supervisor{config: c, runtime: runtime, log: j, status: Status{Phase: Idle}, done: make(chan struct{}), watchDone: make(chan struct{})}
	for _, ev := range events {
		if err := s.replay(ev); err != nil {
			j.poisoned = true
			break
		}
	}
	if j.poisoned {
		if s.status.Phase == RolledBack && s.status.RollbackVerified {
			s.status.PreparedDiscard.Phase = PreparedDiscardUncertain
		} else if !s.status.ConfirmationVerified {
			s.status.Phase = Uncertain
		} else {
			s.status.Cleanup.Phase = CleanupUncertain
		}
		s.status.LastCode = "journal-integrity-unknown"
		s.status.RecoveryOnly = true
		s.reopened = true
	}
	if s.begin != nil {
		s.reopened = true
		if !terminal(s.status.Phase) {
			s.status.RecoveryOnly = true
		}
		s.liveSession = ""
	}
	s.publishStatus()
	go s.watch()
	return s, nil
}
func terminal(p Phase) bool {
	return p == Idle || p == Confirmed || p == RolledBack || p == PreparationFailed
}
func (s *Supervisor) Status() Status {
	if snapshot := s.published.Load(); snapshot != nil {
		return *snapshot
	}
	return Status{Phase: Idle}
}

// Call only under mu (or during Open before publication). Status is immutable
// after Store, so readers never wait for device I/O or preparation's main lock.
func (s *Supervisor) publishStatus() { snapshot := s.status; s.published.Store(&snapshot) }
func (s *Supervisor) record(ev event) error {
	ev.At = time.Now().UTC()
	if err := s.checkPreparedEvent(ev); err != nil {
		return err
	}
	if err := s.log.append(ev); err != nil {
		s.log.poisoned = true
		if s.status.Phase == RolledBack && s.status.RollbackVerified {
			s.status.PreparedDiscard.Phase = PreparedDiscardUncertain
			s.status.PreparedDiscard.LastCode = "journal-write-failed"
		} else if !s.status.ConfirmationVerified {
			s.status.Phase = Uncertain
		} else {
			s.status.Cleanup.Phase = CleanupUncertain
		}
		s.status.LastCode = "journal-write-failed"
		s.status.RecoveryOnly = true
		s.publishStatus()
		// A durable deadline already authorizes fail-safe loss of our channel;
		// do not send a configuration/confirm command when journaling failed.
		s.closeOwner()
		s.liveSession = ""
		return failure("journal-write-failed")
	}
	s.rememberPreparedEvent(ev)
	return nil
}
func (s *Supervisor) setStatus(phase Phase, code string) error {
	next := s.status
	next.Phase = phase
	next.LastCode = code
	if phase == RolledBack {
		next.RollbackVerified = true
	}
	if phase == Confirmed {
		next.ConfirmationVerified = true
	}
	if err := s.record(event{Kind: "status", Status: &next}); err != nil {
		return err
	}
	s.status = next
	s.publishStatus()
	if phase == Confirming {
		s.confirmIntent = true
	}
	if phase == Cancelling {
		s.confirmIntent = false
	}
	return nil
}
func (s *Supervisor) replay(ev event) error {
	switch ev.Kind {
	case "begin":
		if ev.Begin == nil || ev.Status == nil || ev.Begin.Plan.Target != s.config.Target || routeros.ValidatePlan(ev.Begin.Plan) != nil || s.removalBatch != nil || (s.status.PreparedDiscard.Phase != PreparedDiscardNone && s.status.PreparedDiscard.Phase != PreparedDiscardComplete) {
			return failure("journal-begin-invalid")
		}
		if ev.Begin.Restore != nil {
			basis, err := s.restoreBasisLocked()
			if err != nil || !reflect.DeepEqual(*ev.Begin.Restore, basis.link) || !reflect.DeepEqual(ev.Begin.Known, basis.known) ||
				!validRestorePlan(ev.Begin.Plan, basis) || stateMAC(s.log.key, ev.Begin.Baseline) != stateMAC(s.log.key, basis.state) ||
				ev.Status.Phase != Preparing || ev.Status.TransactionID == "" || ev.Status.TransactionID == s.status.TransactionID ||
				ev.Status.PlanChecksum != ev.Begin.Plan.Checksum || ev.Status.RestoreOf != basis.link.TransactionID || ev.Status.RollbackVerified || ev.Status.ConfirmationVerified {
				return failure("journal-restore-begin-invalid")
			}
		} else if !terminal(s.status.Phase) || (s.begin != nil && s.begin.Restore != nil && s.status.Phase != Confirmed) {
			return failure("journal-begin-invalid")
		}
		s.begin = ev.Begin
		s.status = *ev.Status
		s.current = cloneState(ev.Begin.Baseline)
		s.rollback = nil
		s.lastIntent = nil
		s.lastResult = nil
		s.preparedCreates = map[string]preparedCreate{}
		s.preparedOperations = nil
		s.preparedDiscard = preparedDiscardProgress{}
		s.preparedDiscardNeedsReopen = false
		s.removalBatch = nil
		s.removalNeedsReopen = false
		s.batch = nil
		s.cleanup = cleanupProgress{}
		s.cleanupAuto = false
		s.confirmIntent = false
		s.lastRecoveryObservation = ""
		s.desiredVerified = false
	case "status":
		if s.begin == nil || ev.Status == nil || ev.Status.TransactionID != s.status.TransactionID || ev.Status.PlanChecksum != s.status.PlanChecksum {
			return failure("journal-status-invalid")
		}
		s.status = *ev.Status
		if s.status.Phase == Confirming {
			s.confirmIntent = true
		}
		if s.status.Phase == Cancelling {
			s.confirmIntent = false
		}
	case "intent":
		if s.begin == nil || s.removalBatch != nil || ev.Spec == nil || ev.OperationID == "" || ev.StateDigest != stateMAC(s.log.key, s.current) {
			return failure("journal-intent-invalid")
		}
		copy := ev
		s.lastIntent = &copy
		s.lastResult = nil
		s.status.LastOperationID = ev.OperationID
	case "result":
		if s.lastIntent == nil || s.lastIntent.OperationID != ev.OperationID || s.lastResult != nil {
			return failure("journal-result-invalid")
		}
		copy := ev
		s.lastResult = &copy
	case "readback":
		if s.lastIntent == nil || s.lastResult == nil || s.lastIntent.OperationID != ev.OperationID || ev.Delta == nil {
			return failure("journal-readback-invalid")
		}
		if err := s.checkPreparedEvent(ev); err != nil {
			return err
		}
		s.rememberPreparedEvent(ev)
		applyDelta(&s.current, *ev.Delta)
		if ev.StateDigest != stateMAC(s.log.key, s.current) {
			return failure("journal-readback-digest")
		}
		s.lastIntent = nil
		s.lastResult = nil
	case "checkpoint":
		if s.begin == nil || ev.StateDigest != stateMAC(s.log.key, s.current) {
			return failure("journal-checkpoint-invalid")
		}
		copy := cloneState(s.current)
		s.rollback = &copy
		s.status.PreparedArtifactsRetained = true
	case "observation":
		if s.begin == nil {
			return failure("journal-observation-invalid")
		}
		s.lastRecoveryObservation = ev.Code
	case "deadline":
		if ev.Status == nil || ev.Status.TransactionID != s.status.TransactionID || ev.Status.SessionID == "" || ev.Status.Deadline.IsZero() {
			return failure("journal-deadline-invalid")
		}
	case "rule-batch-start", "rule-batch-intent", "rule-batch-result", "rule-batch-readback":
		if ev.Kind == "rule-batch-start" && s.removalBatch != nil {
			return failure("another-write-unresolved")
		}
		if err := s.checkPreparedEvent(ev); err != nil {
			return err
		}
		s.rememberPreparedEvent(ev)
		return s.replayRuleBatch(ev)
	case "cleanup-start", "cleanup-status", "cleanup-intent", "cleanup-result", "cleanup-readback", "cleanup-complete":
		return s.replayCleanup(ev)
	case "discard-start", "discard-status", "discard-intent", "discard-result", "discard-readback", "discard-complete":
		return s.replayPreparedDiscard(ev)
	case "removal-batch-start", "removal-batch-intent", "removal-batch-result", "removal-batch-absence", "removal-batch-readback":
		return s.replayRuleRemovalBatch(ev)
	case "desired-verified":
		if s.begin == nil || s.rollback == nil || ev.StateDigest != stateMAC(s.log.key, s.current) {
			return failure("journal-desired-checkpoint-invalid")
		}
		s.desiredVerified = true
	default:
		return failure("journal-event-unknown")
	}
	return nil
}
func cloneCredentials(in routeros.Credentials) routeros.Credentials {
	out := routeros.Credentials{}
	for k, v := range in {
		out[k] = v
	}
	return out
}
func (s *Supervisor) scopeKnown() routeros.KnownState {
	k, _ := transactionKnown(s.begin, false, s.rollback != nil)
	return k
}
func (s *Supervisor) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(s.config.OperationTimeout)
	if s.rollback != nil && !s.status.Deadline.IsZero() && s.liveSession != "" && s.status.Deadline.Before(deadline) {
		deadline = s.status.Deadline
	}
	return context.WithDeadline(ctx, deadline)
}
func (s *Supervisor) observe(ctx context.Context, request ReadRequest) (Observation, deviceState, error) {
	call, cancel := s.operationContext(ctx)
	defer cancel()
	started := time.Now()
	o, err := s.runtime.Observe(call, request)
	if err != nil {
		return Observation{}, deviceState{}, failure("observation-failed")
	}
	if o.ObservedAt.Before(started.Add(-time.Millisecond)) || o.ObservedAt.After(time.Now().Add(time.Second)) || time.Since(o.ObservedAt) > s.config.OperationTimeout+time.Second {
		return Observation{}, deviceState{}, failure("observation-time-invalid")
	}
	state, err := projectObservation(s.log.key, s.config.Target, o)
	return o, state, err
}
func (s *Supervisor) readCurrent(ctx context.Context) (Observation, deviceState, error) {
	return s.observe(ctx, ReadRequest{KnownState: s.scopeKnown()})
}
func (s *Supervisor) owner(ctx context.Context) (Owner, error) {
	call, cancel := s.operationContext(ctx)
	defer cancel()
	owner, err := s.runtime.Owner(call)
	if err != nil {
		return Owner{}, failure("owner-query-failed")
	}
	return owner, nil
}
func (s *Supervisor) requireOwner(ctx context.Context, reserve int) error {
	if s.liveSession == "" || s.reopened || !time.Now().Before(s.status.Deadline) {
		return failure("owner-session-unavailable")
	}
	o, err := s.owner(ctx)
	if err != nil {
		return err
	}
	if !o.Known || !o.Active || !o.ThisSession || !o.Alive || o.SessionID != s.liveSession {
		return failure("owner-session-lost")
	}
	limit := s.runtime.Binding().MaxHistoryActions
	if limit > 80 {
		limit = 80
	}
	if !o.HistoryKnown || o.HistoryActions < 0 || reserve < 0 || o.HistoryActions+reserve > limit {
		return failure("undo-history-unknown-or-exhausted")
	}
	return nil
}
func (s *Supervisor) Stage(ctx context.Context, request Request) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.publishStatus()
	return s.stageLocked(ctx, request, nil)
}

func (s *Supervisor) stageLocked(ctx context.Context, request Request, restore *restoreBasis) (Status, error) {
	if s.closed || s.log.poisoned {
		return s.status, failure("supervisor-unavailable")
	}
	if restore == nil && !terminal(s.status.Phase) {
		return s.status, failure("transaction-pending")
	}
	if restore == nil && s.begin != nil && s.begin.Restore != nil && s.status.Phase != Confirmed {
		return s.status, failure("restore-requires-resolution")
	}
	if s.status.PreparedDiscard.Phase != PreparedDiscardNone && s.status.PreparedDiscard.Phase != PreparedDiscardComplete {
		return s.status, failure("prepared-discard-pending")
	}
	if s.preparedDiscardNeedsReopen {
		return s.status, failure("discard-runtime-reopen-required")
	}
	if s.removalBatch != nil || s.removalNeedsReopen {
		return s.status, failure("rule-removal-recovery-required")
	}
	if s.status.Phase == Confirmed && s.status.Cleanup.Phase != CleanupNone && s.status.Cleanup.Phase != CleanupComplete && s.status.Cleanup.Total > 0 {
		return s.status, failure("approved-cleanup-pending")
	}
	if request.Plan.Target != s.config.Target || request.KnownState.Target != s.config.Target {
		return s.status, failure("request-target-mismatch")
	}
	if !request.ExclusiveAccessAcknowledged {
		return s.status, failure("exclusive-access-precondition-required")
	}
	if request.Timeout < s.config.MinConfirmationTimeout || request.Timeout > s.config.MaxConfirmationTimeout {
		return s.status, failure("confirmation-timeout-outside-bounds")
	}
	if err := routeros.ValidatePlan(request.Plan); err != nil {
		return s.status, failure("plan-invalid")
	}
	binding := s.runtime.Binding()
	if binding.Target != s.config.Target || !binding.PrepareVerified || !binding.ActivationVerified || !binding.ConfirmationVerified || binding.MaxHistoryActions < 1 || binding.MaxHistoryActions > 80 {
		return s.status, failure("native-adapter-gates-unverified")
	}
	if request.Plan.Budget.EstimatedActions > binding.MaxHistoryActions {
		return s.status, failure("activation-budget-exceeded")
	}
	bridge, err := routeros.NewAPIBridge(request.Plan, request.Credentials)
	if err != nil {
		return s.status, failure("plan-credentials-invalid")
	}
	// This is a separately authorized NEW transaction after a terminal result,
	// never an attempt to resume or confirm a pending transaction. The runtime
	// must independently reject lost/poisoned/taken channels and verify idle
	// dual-channel identity before replacing only an unowned session.
	if resetter, ok := s.runtime.(interface{ ResetIdle(context.Context) error }); ok {
		resetCtx, cancel := context.WithTimeout(ctx, s.config.OperationTimeout)
		resetErr := resetter.ResetIdle(resetCtx)
		cancel()
		if resetErr != nil {
			return s.status, failure("idle-session-reset-failed")
		}
	}
	// Freeze caller-owned slices/maps before any observation or persistence.
	planData, _ := json.Marshal(request.Plan)
	var plan routeros.Plan
	_ = json.Unmarshal(planData, &plan)
	knownData, _ := json.Marshal(request.KnownState)
	var known routeros.KnownState
	_ = json.Unmarshal(knownData, &known)
	baselineCredentials := cloneCredentials(request.BaselineCredentials)
	for id, c := range request.Credentials {
		if _, ok := baselineCredentials[id]; !ok {
			baselineCredentials[id] = c
		}
	}
	finalCredentials, err := desiredCredentials(request, baselineCredentials)
	if err != nil {
		return s.status, err
	}
	if restore != nil {
		for id, ref := range restore.link.Credentials {
			value, present := finalCredentials[id]
			if !present || credentialMAC(s.log.key, ref.ObjectID, []byte(value.Password)) != ref.Digest {
				return s.status, failure("restore-original-credential-mismatch")
			}
		}
	}
	if _, err = transactionKnown(&beginRecord{Plan: plan, Known: known}, true, true); err != nil {
		return s.status, err
	}
	prepareCtx, prepareCancel := context.WithTimeout(ctx, s.config.PrepareTimeout)
	defer prepareCancel()
	o, state, err := s.observe(prepareCtx, ReadRequest{KnownState: known, BaselineCredentials: baselineCredentials, Strict: true})
	if err != nil {
		return s.status, err
	}
	if o.Snapshot == nil {
		return s.status, failure("strict-baseline-snapshot-missing")
	}
	raw, _ := json.Marshal(o.Snapshot)
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != plan.BaseChecksum {
		return s.status, failure("reviewed-baseline-changed")
	}
	if !o.Owner.Known || o.Owner.Active || !completeCredentials(state) {
		return s.status, failure("baseline-owner-or-credentials-unverified")
	}
	if restore != nil && stateMAC(s.log.key, state) != stateMAC(s.log.key, restore.state) {
		return s.status, failure("restore-candidate-state-changed")
	}
	// Reserve enough disk for the fixed initial record and bounded per-op events.
	if s.log.size+int64(len(planData)+len(knownData))+int64(len(plan.Operations))*4096+1<<20 > maxJournalBytes {
		return s.status, failure("journal-capacity-insufficient")
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return s.status, failure("transaction-id-random")
	}
	// Bind only after all read-only baseline checks succeed. A stale/rejected
	// inspection must not leave an otherwise idle driver bound to an unfinished
	// plan and thereby prevent a later newly reviewed Stage.
	if err = s.runtime.BindPlan(plan, cloneCredentials(request.Credentials)); err != nil {
		return s.status, failure("runtime-plan-binding-failed")
	}
	status := Status{TransactionID: hex.EncodeToString(random[:]), PlanChecksum: plan.Checksum, Phase: Preparing, StartedAt: time.Now().UTC()}
	begin := &beginRecord{Plan: plan, Known: known, Baseline: cloneState(state), Timeout: request.Timeout}
	if restore != nil {
		begin.Restore = &restore.link
		status.RestoreOf = restore.link.TransactionID
	}
	if err = s.record(event{Kind: "begin", Status: &status, Begin: begin}); err != nil {
		return s.status, err
	}
	s.begin = begin
	s.status = status
	s.current = cloneState(state)
	s.rollback = nil
	s.lastIntent = nil
	s.lastResult = nil
	s.preparedCreates = map[string]preparedCreate{}
	s.preparedOperations = nil
	s.preparedDiscard = preparedDiscardProgress{}
	s.preparedDiscardNeedsReopen = false
	s.removalBatch = nil
	s.removalNeedsReopen = false
	s.batch = nil
	s.liveSession = ""
	s.reopened = false
	s.confirmIntent = false
	s.lastRecoveryObservation = ""
	s.desiredCredentials = finalCredentials
	s.desiredVerified = false
	s.cleanup = cleanupProgress{}
	s.cleanupAuto = false
	s.publishStatus()
	operations := bridge.Operations()
	for i := 0; i < len(operations); i++ {
		op := operations[i]
		if op.Phase == routeros.Prepare {
			if s.runtime.Binding().RuleBatchVerified {
				if batched, ok := s.runtime.(RuleBatchRuntime); ok {
					ids := s.ruleBatchIDs(operations[i:])
					if len(ids) > 0 {
						if err = s.executeRuleBatch(prepareCtx, bridge, batched, ids); err != nil {
							return s.failStage(err)
						}
						i += len(ids) - 1
						continue
					}
				}
			}
			if err = s.execute(prepareCtx, bridge, op); err != nil {
				return s.failStage(err)
			}
		}
	}
	if !completeCredentials(s.current) {
		return s.failStage(failure("prepared-credentials-unverified"))
	}
	if err = s.record(event{Kind: "checkpoint", StateDigest: stateMAC(s.log.key, s.current)}); err != nil {
		return s.status, err
	}
	prepared := cloneState(s.current)
	s.rollback = &prepared
	s.status.PreparedArtifactsRetained = true
	if err = s.setStatus(Prepared, ""); err != nil {
		return s.status, err
	}
	s.status.Deadline = time.Now().UTC().Add(request.Timeout)
	if err = s.setStatus(Taking, ""); err != nil {
		return s.status, err
	}
	s.ownerMu.Lock()
	s.ownerEpoch++
	call, cancel := s.operationContext(ctx)
	owner, takeErr := s.runtime.Take(call, s.status.Deadline)
	cancel()
	s.ownerMu.Unlock()
	if takeErr != nil || !owner.Known || !owner.Active || !owner.ThisSession || !owner.Alive || !validSessionID(owner.SessionID) {
		s.closeOwner()
		s.liveSession = ""
		_ = s.setStatus(RollbackPending, "take-unverified")
		return s.status, failure("take-unverified")
	}
	s.liveSession = owner.SessionID
	s.status.SessionID = owner.SessionID
	if err = s.setStatus(Activating, ""); err != nil {
		return s.status, err
	}
	deadlineStatus := s.status
	if err = s.record(event{Kind: "deadline", Status: &deadlineStatus, Code: "cancel-this-session-at-deadline"}); err != nil {
		return s.status, err
	}
	s.armDeadline()
	var objectRuntime ObjectRuntime
	var objectResolver *routeros.APIResolver
	if s.runtime.Binding().ObjectObservationVerified && ObjectActivationEligible(plan) {
		if candidate, ok := s.runtime.(ObjectRuntime); ok {
			checkpoint, actual, readErr := s.readCurrent(ctx)
			if readErr != nil || stateMAC(s.log.key, actual) != stateMAC(s.log.key, s.current) {
				return s.failStage(failure("activation-global-checkpoint-mismatch"))
			}
			if err = s.requireOwner(ctx, 0); err != nil {
				return s.failStage(err)
			}
			call, cancel := s.operationContext(ctx)
			bindErr := candidate.BindObjectActivation(call, plan.Checksum)
			cancel()
			if bindErr != nil {
				return s.failStage(failure("object-activation-binding-failed"))
			}
			objectRuntime, objectResolver = candidate, checkpoint.Resolver
		}
	}
	for _, op := range bridge.Operations() {
		if op.Phase == routeros.Activate {
			if objectRuntime != nil {
				err = s.executeObject(ctx, bridge, objectRuntime, objectResolver, op)
			} else {
				err = s.execute(ctx, bridge, op)
			}
			if err != nil {
				return s.failStage(err)
			}
		}
	}
	if err = s.requireOwner(ctx, 0); err != nil {
		return s.failStage(err)
	}
	_, actual, err := s.readDesired(ctx)
	if err != nil || stateMAC(s.log.key, actual) != stateMAC(s.log.key, s.current) {
		return s.failStage(failure("desired-final-readback-mismatch"))
	}
	if err = s.record(event{Kind: "desired-verified", StateDigest: stateMAC(s.log.key, actual)}); err != nil {
		return s.status, err
	}
	s.desiredVerified = true
	if err = s.setStatus(AwaitingConfirmation, ""); err != nil {
		return s.status, err
	}
	return s.status, nil
}
func (s *Supervisor) execute(ctx context.Context, bridge *routeros.APIBridge, operation routeros.APIOperation) error {
	o, before, err := s.readCurrent(ctx)
	if err != nil {
		return err
	}
	if stateMAC(s.log.key, before) != stateMAC(s.log.key, s.current) {
		return failure("state-changed-before-write")
	}
	envelope, err := bridge.Mutation(operation.ID, o.Resolver)
	if err != nil {
		return failure("operation-resolution-failed")
	}
	reserve := len(envelope.Request.Attributes) + 1
	if reserve < 2 {
		reserve = 2
	}
	for _, op := range s.begin.Plan.Operations {
		if op.ID == operation.ID && op.EstimatedActions > reserve {
			reserve = op.EstimatedActions
			break
		}
	}
	if operation.Phase == routeros.Activate {
		if err = s.requireOwner(ctx, reserve); err != nil {
			return err
		}
	} else if !o.Owner.Known || o.Owner.Active {
		return failure("safe-mode-active-during-prepare")
	}
	spec := specFor(s.log.key, envelope.Request)
	intent := event{Kind: "intent", OperationID: operation.ID, Spec: &spec, StateDigest: stateMAC(s.log.key, s.current)}
	if err = s.record(intent); err != nil {
		return err
	}
	s.lastIntent = &intent
	s.status.LastOperationID = operation.ID
	s.publishStatus()
	call, cancel := s.operationContext(ctx)
	result, writeErr := s.runtime.Apply(call, envelope)
	cancel()
	if result.ID != "" && (!validObjectID(strings.ToUpper(result.ID)) || envelope.Request.Action != api.Add) {
		result.ID = ""
		writeErr = failure("invalid-write-result")
	}
	code := ""
	if writeErr != nil || !result.Acknowledged {
		code = "write-outcome-unknown"
	}
	if err = s.record(event{Kind: "result", OperationID: operation.ID, Acknowledged: writeErr == nil && result.Acknowledged, ReturnedID: result.ID, Code: code}); err != nil {
		return err
	}
	_, actual, readErr := s.readCurrent(ctx)
	if readErr != nil {
		return failure("write-readback-unavailable")
	}
	delta, verifyErr := verifyMutation(s.log.key, routeros.OwnerName(s.config.Target.RouterID), s.current, actual, spec, result.ID)
	if verifyErr != nil {
		return verifyErr
	}
	if err = s.record(event{Kind: "readback", OperationID: operation.ID, Delta: &delta, StateDigest: stateMAC(s.log.key, actual)}); err != nil {
		return err
	}
	applyDelta(&s.current, delta)
	s.lastIntent = nil
	if writeErr != nil || !result.Acknowledged {
		return failure("write-outcome-unknown")
	}
	return nil
}
func (s *Supervisor) failStage(cause error) (Status, error) {
	if s.log.poisoned {
		s.closeOwner()
		return s.status, cause
	}
	if s.rollback == nil {
		_ = s.setStatus(Uncertain, "prepare-interrupted")
		s.status.RecoveryOnly = true
		return s.status, cause
	}
	_, _ = s.cancelLocked(context.Background(), "activation-failed")
	return s.status, cause
}
func (s *Supervisor) armDeadline() {
	if s.timer != nil {
		s.timer.Stop()
	}
	duration := time.Until(s.status.Deadline)
	if duration < 0 {
		duration = 0
	}
	// Deadline was durably written before Take. Closing our own channel is the
	// fail-safe action even when a blocked call holds the state mutex.
	s.ownerMu.Lock()
	epoch := s.ownerEpoch
	session := s.liveSession
	s.ownerMu.Unlock()
	s.timer = time.AfterFunc(duration, func() {
		s.ownerMu.Lock()
		defer s.ownerMu.Unlock()
		if epoch != s.ownerEpoch {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), s.config.OperationTimeout)
		defer cancel()
		owner, err := s.runtime.Owner(ctx)
		if err == nil && owner.Known && owner.Active && owner.ThisSession && owner.Alive && owner.SessionID == session {
			_, _ = s.runtime.Cancel(ctx)
		}
		_ = s.runtime.CloseOwner()
		s.ownerEpoch++
	})
}
func (s *Supervisor) Confirm(ctx context.Context, transactionID, checksum string) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.publishStatus()
	if s.closed || s.log.poisoned || s.reopened || s.status.RecoveryOnly || !s.desiredVerified || s.status.Phase != AwaitingConfirmation || s.status.TransactionID != transactionID || s.status.PlanChecksum != checksum {
		return s.status, failure("confirmation-not-authorized")
	}
	if err := s.requireOwner(ctx, 0); err != nil {
		_, _ = s.cancelLocked(context.Background(), "owner-lost-before-confirm")
		return s.status, err
	}
	_, actual, err := s.readDesired(ctx)
	if err != nil || stateMAC(s.log.key, actual) != stateMAC(s.log.key, s.current) {
		_, _ = s.cancelLocked(context.Background(), "desired-changed-before-confirm")
		return s.status, failure("desired-changed-before-confirm")
	}
	if err = s.setStatus(Confirming, ""); err != nil {
		return s.status, err
	}
	s.ownerMu.Lock()
	call, cancel := s.operationContext(ctx)
	owner, confirmErr := s.runtime.Confirm(call)
	cancel()
	if confirmErr == nil && owner.Known && !owner.Active && !owner.ThisSession {
		s.ownerEpoch++
	}
	s.ownerMu.Unlock()
	if confirmErr != nil || !owner.Known || owner.Active || owner.ThisSession {
		s.closeOwner()
		s.liveSession = ""
		_ = s.setStatus(Uncertain, "confirm-outcome-unknown")
		return s.status, failure("confirm-outcome-unknown")
	}
	if s.timer != nil {
		s.timer.Stop()
	}
	s.liveSession = ""
	o, after, err := s.readDesired(ctx)
	if err != nil || !o.Owner.Known || o.Owner.Active || stateMAC(s.log.key, after) != stateMAC(s.log.key, s.current) {
		_ = s.setStatus(Uncertain, "confirm-readback-unknown")
		return s.status, failure("confirm-readback-unknown")
	}
	if err = s.record(event{Kind: "observation", StateDigest: stateMAC(s.log.key, after), Code: "desired-with-owner-released"}); err != nil {
		return s.status, err
	}
	s.status.VerifiedAt = o.ObservedAt
	if err = s.setStatus(Confirmed, ""); err != nil {
		return s.status, err
	}
	if err = s.initializeCleanup(); err != nil {
		return s.status, err
	}
	s.cleanupAuto = !s.config.ManualCleanup && s.status.Cleanup.Phase == CleanupPending
	return s.status, nil
}
func (s *Supervisor) Cancel(ctx context.Context, transactionID string) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.publishStatus()
	if s.closed || s.begin == nil || s.status.TransactionID != transactionID || terminal(s.status.Phase) {
		return s.status, failure("no-cancellable-transaction")
	}
	return s.cancelLocked(ctx, "operator-cancel")
}
func (s *Supervisor) cancelLocked(ctx context.Context, code string) (Status, error) {
	if s.log.poisoned {
		s.closeOwner()
		s.liveSession = ""
		return s.status, failure("journal-unavailable")
	}
	if s.rollback == nil {
		_ = s.setStatus(Uncertain, "prepare-requires-inspection")
		return s.status, failure("prepare-requires-inspection")
	}
	if err := s.setStatus(Cancelling, code); err != nil {
		return s.status, err
	}
	s.ownerMu.Lock()
	owned := false
	// Cancellation remains authorized after the confirmation deadline. Do not
	// clamp this fresh bounded owner proof to an already expired deadline.
	call, cancel := context.WithTimeout(ctx, s.config.OperationTimeout)
	if !s.reopened && s.liveSession != "" {
		owner, err := s.runtime.Owner(call)
		owned = err == nil && owner.Known && owner.Active && owner.ThisSession && owner.Alive && owner.SessionID == s.liveSession
	}
	if owned {
		_, _ = s.runtime.Cancel(call)
	}
	cancel()
	_ = s.runtime.CloseOwner()
	s.ownerEpoch++
	s.ownerMu.Unlock()
	s.liveSession = ""
	if s.timer != nil {
		s.timer.Stop()
	}
	if err := s.setStatus(RollbackPending, code); err != nil {
		return s.status, err
	}
	return s.recoverLocked(ctx)
}
func (s *Supervisor) Recover(ctx context.Context) (Status, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.publishStatus()
	return s.recoverLocked(ctx)
}
func (s *Supervisor) recoverLocked(ctx context.Context) (Status, error) {
	if s.closed || s.begin == nil {
		return s.status, failure("no-recoverable-transaction")
	}
	if terminal(s.status.Phase) {
		if s.status.Phase == RolledBack && s.status.PreparedDiscard.Phase != PreparedDiscardNone {
			return s.recoverPreparedDiscardLocked(ctx)
		}
		if s.status.Phase == Confirmed {
			return s.recoverCleanupLocked(ctx)
		}
		return s.status, nil
	}
	o, actual, err := s.readRecovery(ctx)
	if err != nil {
		return s.status, err
	}
	if s.log.poisoned {
		return s.status, failure("journal-integrity-unknown")
	}
	if s.batch != nil {
		deltas, verifyErr := s.verifyRuleBatch(actual)
		if verifyErr != nil {
			return s.status, verifyErr
		}
		if err = s.finishRuleBatch(actual, deltas); err != nil {
			return s.status, err
		}
	}
	digest := stateMAC(s.log.key, actual)
	observed := mac(s.log.key, "recovery-observation", struct {
		Digest        string
		Known, Active bool
	}{digest, o.Owner.Known, o.Owner.Active})
	if observed != s.lastRecoveryObservation {
		if err = s.record(event{Kind: "observation", StateDigest: digest, Code: observed}); err != nil {
			return s.status, err
		}
		s.lastRecoveryObservation = observed
	}
	if !o.Owner.Known || o.Owner.Active {
		return s.status, failure("owner-release-not-proven")
	}
	if s.rollback == nil && s.lastIntent == nil && digest == stateMAC(s.log.key, s.current) {
		s.status.PreparedArtifactsRetained = len(s.current.Objects) > len(s.begin.Baseline.Objects)
		if err = s.setStatus(PreparationFailed, "inert-preparation-readback-verified"); err != nil {
			return s.status, err
		}
		return s.status, nil
	}
	// Only a previously durable confirm intent may become confirmed on restart.
	// Merely finding desired configuration with no owner does not prove commit.
	if s.confirmIntent && s.desiredVerified && s.lastIntent == nil && digest == stateMAC(s.log.key, s.current) {
		s.status.VerifiedAt = o.ObservedAt
		if err = s.setStatus(Confirmed, "confirm-readback-verified"); err != nil {
			return s.status, err
		}
		if err = s.initializeCleanup(); err != nil {
			return s.status, err
		}
		return s.status, nil
	}
	if s.rollback != nil && digest == stateMAC(s.log.key, *s.rollback) {
		s.liveSession = ""
		s.status.VerifiedAt = o.ObservedAt
		s.status.RecoveryOnly = s.reopened
		if err = s.setStatus(RolledBack, "baseline-readback-verified"); err != nil {
			return s.status, err
		}
		return s.status, nil
	}
	statusCode, resultCode := "baseline-not-observed", "recovery-state-not-proven"
	if s.rollback != nil && !s.confirmIntent && s.desiredVerified && s.lastIntent == nil && digest == stateMAC(s.log.key, s.current) {
		// A device can restart with the unconfirmed candidate intact. Exact
		// authenticated readback identifies this state without authorizing it.
		statusCode, resultCode = UnconfirmedCandidateRetainedCode, UnconfirmedCandidateRetainedCode
	}
	if s.status.Phase != Uncertain && !(s.status.Phase == RollbackPending && s.status.LastCode == statusCode) {
		if err = s.setStatus(RollbackPending, statusCode); err != nil {
			return s.status, err
		}
	}
	return s.status, failure(resultCode)
}
func (s *Supervisor) watch() {
	defer close(s.watchDone)
	ticker := time.NewTicker(s.config.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.mu.Lock()
			if !s.closed && s.begin != nil && s.status.Phase == Confirmed && s.cleanupAuto {
				ctx, cancel := context.WithTimeout(context.Background(), s.config.PrepareTimeout)
				_, _ = s.cleanupLocked(ctx, 1)
				cancel()
			} else if !s.closed && s.begin != nil && !terminal(s.status.Phase) {
				ctx, cancel := context.WithTimeout(context.Background(), s.config.OperationTimeout)
				if s.reopened || s.liveSession == "" {
					_, _ = s.recoverLocked(ctx)
				} else if !time.Now().Before(s.status.Deadline) {
					_, _ = s.cancelLocked(ctx, "confirmation-deadline")
				} else if err := s.requireOwner(ctx, 0); err != nil {
					_, _ = s.cancelLocked(ctx, "owner-lost")
				}
				cancel()
			}
			s.publishStatus()
			s.mu.Unlock()
		}
	}
}
func (s *Supervisor) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.done)
	if s.timer != nil {
		s.timer.Stop()
	}
	s.closeOwner()
	s.liveSession = ""
	s.log.close()
	clear(s.desiredCredentials)
	s.mu.Unlock()
	<-s.watchDone
	return nil
}

func (s *Supervisor) closeOwner() {
	s.ownerMu.Lock()
	defer s.ownerMu.Unlock()
	s.ownerEpoch++
	_ = s.runtime.CloseOwner()
}
func validSessionID(v string) bool {
	if len(v) < 16 || len(v) > 128 {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
