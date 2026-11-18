package control

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
)

type ruleRemovalRecorder struct {
	s        *Supervisor
	mode     RuleRemovalMode
	batchID  string
	expected []routeros.APIMutation
	ownerID  string
	started  time.Time
	mu       sync.Mutex
	closed   atomic.Bool
	stopped  bool
}

func (r *ruleRemovalRecorder) close() {
	// Reject callbacks that arrive after Runtime returns, then wait for a
	// callback already holding the mutex before inspecting durable progress.
	r.closed.Store(true)
	r.mu.Lock()
	r.mu.Unlock()
}
func (s *Supervisor) ruleRemovalOwner(mode RuleRemovalMode, owner Owner, full bool) bool {
	if !owner.Known || !owner.Alive || owner.Active || owner.ThisSession || !validSessionID(owner.SessionID) || owner.HistoryKnown && owner.HistoryActions != 0 {
		return false
	}
	if full && !owner.HistoryKnown {
		return false
	}
	return mode != RuleRemovalPreparedDiscard || owner.SessionID != s.status.SessionID
}
func (r *ruleRemovalRecorder) fresh(at time.Time) bool {
	now := time.Now()
	return !at.IsZero() && !at.Before(r.started.Add(-time.Millisecond)) && !at.After(now.Add(time.Second)) && now.Sub(at) <= r.s.config.OperationTimeout+time.Second
}
func removalProtected(state deviceState, owner string) map[string]objectState {
	out := map[string]objectState{}
	for key, object := range state.Objects {
		if object.Resource == api.RoutingTable || object.Resource == api.BGPInstance || object.Resource == api.BGPConnection || object.Resource == api.FilterRule && (object.Attributes["chain"] == owner+"_root_in" || object.Attributes["chain"] == owner+"_root_out") {
			out[key] = object
		}
	}
	return out
}
func (r *ruleRemovalRecorder) verify(observation RuleRemovalObservation, mutation routeros.APIMutation, present bool) error {
	if observation.BatchID != r.batchID || observation.Target != r.s.config.Target || observation.Resource != api.FilterRule || observation.ID != mutation.Request.ID || observation.Present != present || !r.fresh(observation.ObservedAt) || !r.s.ruleRemovalOwner(r.mode, observation.Owner, false) || observation.Owner.SessionID != r.ownerID {
		return failure("rule-removal-observation-unverified")
	}
	if present {
		if observation.Attributes[".id"] != observation.ID {
			return failure("rule-removal-object-identity")
		}
		attrs, err := canonicalRecord(api.FilterRule, observation.Attributes)
		wanted, exists := r.s.current.Objects[objectKey(api.FilterRule, observation.ID)]
		if err != nil || !exists || mac(r.s.log.key, "rule-removal-attrs", attrs) != mac(r.s.log.key, "rule-removal-attrs", wanted.Attributes) {
			return failure("rule-removal-target-changed")
		}
	} else if len(observation.Attributes) != 0 {
		return failure("rule-removal-absence-not-empty")
	}
	protected := removalProtected(r.s.current, r.s.begin.Plan.Owner)
	if len(protected) != len(observation.Protected) {
		return failure("rule-removal-protection-incomplete")
	}
	seen := map[string]bool{}
	for _, actual := range observation.Protected {
		key := objectKey(actual.Resource, actual.ID)
		wanted, exists := protected[key]
		if !exists || seen[key] || actual.Target != r.s.config.Target || actual.Attributes[".id"] != actual.ID || !actual.CrossChannelIdentityVerified || !r.fresh(actual.ObservedAt) {
			return failure("rule-removal-protection-unverified")
		}
		attrs, err := canonicalRecord(actual.Resource, actual.Attributes)
		if err != nil || mac(r.s.log.key, "rule-removal-protection", attrs) != mac(r.s.log.key, "rule-removal-protection", wanted.Attributes) {
			return failure("rule-removal-protected-object-changed")
		}
		seen[key] = true
	}
	return nil
}

func (r *ruleRemovalRecorder) Before(id string, observation RuleRemovalObservation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed.Load() || r.stopped || r.s.removalBatch == nil {
		return failure("rule-removal-recorder-closed")
	}
	b := r.s.removalBatch
	i := len(b.Steps)
	if !validRuleRemovalProof(b.ProofMode) {
		r.stopped = true
		return failure("rule-removal-proof-mode-invalid")
	}
	if b.ID != r.batchID || i >= len(r.expected) || id != r.expected[i].OperationID || i > 0 && !ruleRemovalStepCanAdvance(b.ProofMode, b.Steps[i-1]) {
		r.stopped = true
		return failure("rule-removal-operation-order")
	}
	if err := r.verify(observation, r.expected[i], true); err != nil {
		r.stopped = true
		return err
	}
	spec := specFor(r.s.log.key, r.expected[i].Request)
	if err := r.s.removalEvent(event{Kind: "removal-batch-intent", BatchID: r.batchID, OperationID: id, Spec: &spec, StateDigest: b.BeforeDigest}); err != nil {
		r.stopped = true
		return err
	}
	if r.closed.Load() {
		r.stopped = true
		return failure("rule-removal-recorder-closed")
	}
	return nil
}
func (r *ruleRemovalRecorder) Result(id string, result api.Result, writeErr error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed.Load() || r.stopped {
		return failure("rule-removal-recorder-closed")
	}
	b := r.s.removalBatch
	if b == nil || b.ID != r.batchID || len(b.Steps) == 0 {
		return failure("rule-removal-recorder-closed")
	}
	if !validRuleRemovalProof(b.ProofMode) {
		r.stopped = true
		return failure("rule-removal-proof-mode-invalid")
	}
	step := b.Steps[len(b.Steps)-1]
	if step.OperationID != id || step.ResultRecorded {
		r.stopped = true
		return failure("rule-removal-result-order")
	}
	ack := writeErr == nil && result.Acknowledged && result.ID == ""
	if err := r.s.removalEvent(event{Kind: "removal-batch-result", BatchID: r.batchID, OperationID: id, Acknowledged: ack}); err != nil {
		r.stopped = true
		return err
	}
	if !ack {
		r.stopped = true
		return failure("rule-removal-write-outcome-unknown")
	}
	if r.closed.Load() {
		r.stopped = true
		return failure("rule-removal-recorder-closed")
	}
	return nil
}
func (r *ruleRemovalRecorder) Absent(id string, observation RuleRemovalObservation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed.Load() || r.stopped {
		return failure("rule-removal-recorder-closed")
	}
	b := r.s.removalBatch
	if b == nil || b.ID != r.batchID || len(b.Steps) == 0 {
		return failure("rule-removal-recorder-closed")
	}
	if normalizedRuleRemovalProof(b.ProofMode) != RuleRemovalProofPerObject {
		r.stopped = true
		return failure("rule-removal-absence-not-permitted")
	}
	i := len(b.Steps) - 1
	step := b.Steps[i]
	if step.OperationID != id || !step.ResultRecorded || !step.Acknowledged || step.AbsentVerified {
		r.stopped = true
		return failure("rule-removal-absence-order")
	}
	if err := r.verify(observation, r.expected[i], false); err != nil {
		r.stopped = true
		return err
	}
	if err := r.s.removalEvent(event{Kind: "removal-batch-absence", BatchID: r.batchID, OperationID: id, Code: "actual-object-absence"}); err != nil {
		r.stopped = true
		return err
	}
	if r.closed.Load() {
		r.stopped = true
		return failure("rule-removal-recorder-closed")
	}
	return nil
}
