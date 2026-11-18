package control

import (
	"fmt"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
)

const MaxRuleRemovalBatch = 256

type ruleRemovalStep struct {
	OperationID    string       `json:"operationId"`
	Spec           mutationSpec `json:"spec"`
	ResultRecorded bool         `json:"resultRecorded"`
	Acknowledged   bool         `json:"acknowledged"`
	AbsentVerified bool         `json:"absentVerified"`
}
type ruleRemovalBatchState struct {
	ID            string               `json:"id"`
	Mode          RuleRemovalMode      `json:"mode"`
	ProofMode     RuleRemovalProofMode `json:"proofMode,omitempty"`
	TransactionID string               `json:"transactionId"`
	PlanChecksum  string               `json:"planChecksum"`
	ScopeChecksum string               `json:"scopeChecksum"`
	OperationIDs  []string             `json:"operationIds"`
	BeforeDigest  string               `json:"beforeDigest"`
	StartIndex    int                  `json:"startIndex"`
	Steps         []ruleRemovalStep    `json:"steps"`
}

func (s *Supervisor) removalCursor(mode RuleRemovalMode) ([]string, int, *cleanupAttempt, error) {
	if s.begin == nil {
		return nil, 0, nil, failure("rule-removal-unbound")
	}
	switch mode {
	case RuleRemovalCleanup:
		if s.status.Phase != Confirmed || !s.status.ConfirmationVerified || s.status.Cleanup.Phase == CleanupNone || s.status.Cleanup.Phase == CleanupComplete {
			return nil, 0, nil, failure("rule-removal-cleanup-not-authorized")
		}
		return s.cleanup.OperationIDs, s.cleanup.Index, s.cleanup.Pending, nil
	case RuleRemovalPreparedDiscard:
		if s.status.Phase != RolledBack || !s.status.RollbackVerified || s.status.PreparedDiscard.Phase == PreparedDiscardNone || s.status.PreparedDiscard.Phase == PreparedDiscardComplete || s.preparedDiscard.Checksum == "" {
			return nil, 0, nil, failure("rule-removal-discard-not-authorized")
		}
		return s.preparedDiscard.OperationIDs, s.preparedDiscard.Index, s.preparedDiscard.Pending, nil
	default:
		return nil, 0, nil, failure("rule-removal-mode-invalid")
	}
}
func (s *Supervisor) removalScope(mode RuleRemovalMode) string {
	if mode == RuleRemovalPreparedDiscard {
		return s.preparedDiscard.Checksum
	}
	return mac(s.log.key, "confirmed-rule-removal-scope-v1", struct {
		Target                      routeros.Target
		TransactionID, PlanChecksum string
		OperationIDs                []string
	}{s.config.Target, s.status.TransactionID, s.begin.Plan.Checksum, s.cleanup.OperationIDs})
}
func (s *Supervisor) approvedRemoval(mode RuleRemovalMode, operation string) (api.Resource, string, bool) {
	if mode == RuleRemovalCleanup {
		for _, op := range s.begin.Plan.Operations {
			if op.ID == operation && op.Phase == routeros.Cleanup && op.Kind == routeros.RemoveRule {
				return api.FilterRule, strings.ToUpper(op.Object.ID), true
			}
		}
	} else if mode == RuleRemovalPreparedDiscard {
		for _, object := range s.preparedDiscard.Objects {
			if object.Resource == api.FilterRule && routeros.PreparedDiscardOperationID(object.PrepareOperationID) == operation {
				return api.FilterRule, object.ID, true
			}
		}
	}
	return "", "", false
}
func (s *Supervisor) removableGeneration(mode RuleRemovalMode, operation string) string {
	resource, id, approved := s.approvedRemoval(mode, operation)
	if !approved {
		return ""
	}
	object, exists := s.current.Objects[objectKey(resource, id)]
	if !exists {
		return ""
	}
	prefix := s.begin.Plan.Owner + "_g"
	chain := object.Attributes["chain"]
	if !strings.HasPrefix(chain, prefix) || len(chain) < len(prefix)+18 {
		return ""
	}
	generation := chain[len(prefix) : len(prefix)+16]
	for _, ch := range generation {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return ""
		}
	}
	suffix := chain[len(prefix)+16:]
	if suffix != "_in" && suffix != "_out" && suffix != "_src" || !strings.HasPrefix(object.Attributes["comment"], s.begin.Plan.Owner+":g:"+generation+":") {
		return ""
	}
	active := s.begin.Plan.Generation
	if mode == RuleRemovalPreparedDiscard {
		active = s.begin.Known.ActiveGeneration
	}
	if generation == active {
		return ""
	}
	return generation
}
func (s *Supervisor) ruleRemovalIDs(mode RuleRemovalMode) ([]string, error) {
	ids, start, pending, err := s.removalCursor(mode)
	if err != nil || pending != nil || start >= len(ids) {
		return nil, err
	}
	generation := s.removableGeneration(mode, ids[start])
	if generation == "" {
		return nil, nil
	}
	out := []string{}
	for _, id := range ids[start:] {
		if len(out) == MaxRuleRemovalBatch || s.removableGeneration(mode, id) != generation {
			break
		}
		out = append(out, id)
	}
	return out, nil
}

func (s *Supervisor) replayRuleRemovalBatch(ev event) error {
	if s.begin == nil {
		return failure("journal-rule-removal-unbound")
	}
	if ev.Kind == "removal-batch-start" {
		b := ev.RemovalBatch
		if b == nil || s.removalBatch != nil || s.batch != nil || s.lastIntent != nil || b.TransactionID != s.status.TransactionID || b.PlanChecksum != s.status.PlanChecksum || b.ScopeChecksum != s.removalScope(b.Mode) || len(b.ID) < 1 || len(b.ID) > 128 || len(b.Steps) != 0 || b.BeforeDigest != stateMAC(s.log.key, s.current) {
			return failure("journal-rule-removal-start-invalid")
		}
		if !validRuleRemovalProof(b.ProofMode) {
			return failure("rule-removal-proof-mode-invalid")
		}
		ids, start, pending, err := s.removalCursor(b.Mode)
		if err != nil || pending != nil || start != b.StartIndex || len(b.OperationIDs) < 2 || len(b.OperationIDs) > MaxRuleRemovalBatch || len(ids)-start < len(b.OperationIDs) {
			return failure("journal-rule-removal-scope-invalid")
		}
		generation := s.removableGeneration(b.Mode, b.OperationIDs[0])
		for i, id := range b.OperationIDs {
			if id != ids[start+i] || generation == "" || s.removableGeneration(b.Mode, id) != generation {
				return failure("journal-rule-removal-prefix-invalid")
			}
		}
		copy := *b
		copy.ProofMode = normalizedRuleRemovalProof(b.ProofMode)
		copy.OperationIDs, copy.Steps = append([]string{}, b.OperationIDs...), nil
		s.removalBatch = &copy
		return nil
	}
	b := s.removalBatch
	if b == nil || ev.RemovalBatch != nil || ev.BatchID != b.ID || b.BeforeDigest != stateMAC(s.log.key, s.current) {
		return failure("journal-rule-removal-unbound")
	}
	if !validRuleRemovalProof(b.ProofMode) {
		return failure("rule-removal-proof-mode-invalid")
	}
	_, index, pending, err := s.removalCursor(b.Mode)
	if err != nil || pending != nil || index != b.StartIndex {
		return failure("journal-rule-removal-cursor-changed")
	}
	switch ev.Kind {
	case "removal-batch-intent":
		i := len(b.Steps)
		if i >= len(b.OperationIDs) || ev.OperationID != b.OperationIDs[i] || ev.Spec == nil || ev.Spec.Action != api.Remove || ev.Spec.Resource != api.FilterRule || len(ev.Spec.Attributes) != 0 || ev.Spec.Property != "" || ev.Spec.CredentialDigest != "" || ev.StateDigest != b.BeforeDigest || i > 0 && !ruleRemovalStepCanAdvance(b.ProofMode, b.Steps[i-1]) {
			return failure("journal-rule-removal-intent-invalid")
		}
		resource, id, approved := s.approvedRemoval(b.Mode, ev.OperationID)
		if !approved || ev.Spec.Resource != resource || ev.Spec.ID != id {
			return failure("journal-rule-removal-target-invalid")
		}
		b.Steps = append(b.Steps, ruleRemovalStep{OperationID: ev.OperationID, Spec: *ev.Spec})
		if b.Mode == RuleRemovalCleanup {
			s.status.Cleanup.LastOperationID = ev.OperationID
		} else {
			s.status.PreparedDiscard.LastOperationID = ev.OperationID
		}
	case "removal-batch-result":
		if len(b.Steps) == 0 {
			return failure("journal-rule-removal-result-invalid")
		}
		step := &b.Steps[len(b.Steps)-1]
		if ev.OperationID != step.OperationID || step.ResultRecorded || ev.ReturnedID != "" {
			return failure("journal-rule-removal-result-invalid")
		}
		step.ResultRecorded, step.Acknowledged = true, ev.Acknowledged
	case "removal-batch-absence":
		if normalizedRuleRemovalProof(b.ProofMode) != RuleRemovalProofPerObject {
			return failure("rule-removal-absence-not-permitted")
		}
		if len(b.Steps) == 0 {
			return failure("journal-rule-removal-absence-invalid")
		}
		step := &b.Steps[len(b.Steps)-1]
		if ev.OperationID != step.OperationID || !step.ResultRecorded || !step.Acknowledged || step.AbsentVerified || ev.Code != "actual-object-absence" {
			return failure("journal-rule-removal-absence-invalid")
		}
		step.AbsentVerified = true
	case "removal-batch-readback":
		count := len(ev.Deltas)
		if count > len(b.Steps) || count < len(b.Steps)-1 {
			return failure("journal-rule-removal-readback-prefix")
		}
		next := cloneState(s.current)
		for i, delta := range ev.Deltas {
			step := b.Steps[i]
			if !delta.Remove || delta.Object != nil || delta.Key != objectKey(step.Spec.Resource, step.Spec.ID) {
				return failure("journal-rule-removal-readback-invalid")
			}
			applyDelta(&next, delta)
		}
		if ev.StateDigest != stateMAC(s.log.key, next) {
			return failure("journal-rule-removal-readback-digest")
		}
		var nextPending *cleanupAttempt
		if count < len(b.Steps) {
			step := b.Steps[count]
			if !ruleRemovalStepCanRemainPending(b.ProofMode, step) || ev.RemovalPending == nil {
				return failure("journal-rule-removal-pending-invalid")
			}
			wanted := cleanupAttempt{OperationID: step.OperationID, Spec: step.Spec, BeforeDigest: ev.StateDigest, ResultRecorded: step.ResultRecorded, Acknowledged: step.Acknowledged}
			if mac(s.log.key, "rule-removal-pending", wanted) != mac(s.log.key, "rule-removal-pending", *ev.RemovalPending) {
				return failure("journal-rule-removal-pending-invalid")
			}
			nextPending = &wanted
		} else if ev.RemovalPending != nil {
			return failure("journal-rule-removal-pending-invalid")
		}
		s.current = next
		if b.Mode == RuleRemovalCleanup {
			s.cleanup.Index += count
			s.cleanup.Pending = nextPending
			s.status.Cleanup.Completed = s.cleanup.Index
			s.status.Cleanup.UpdatedAt = ev.At
		} else {
			s.preparedDiscard.Index += count
			s.preparedDiscard.Pending = nextPending
			s.status.PreparedDiscard.Completed = s.preparedDiscard.Index
			s.status.PreparedDiscard.UpdatedAt = ev.At
		}
		s.removalBatch = nil
	default:
		return failure("journal-rule-removal-event-invalid")
	}
	return nil
}

func (s *Supervisor) removalEvent(ev event) error {
	ev.At = time.Now().UTC()
	if err := s.record(ev); err != nil {
		return err
	}
	if err := s.replayRuleRemovalBatch(ev); err != nil {
		s.log.poisoned = true
		return err
	}
	s.publishStatus()
	return nil
}
func (s *Supervisor) nextRemovalBatchID(mode RuleRemovalMode, index int) string {
	return fmt.Sprintf("%s-rm-%s-%d-%d", s.status.TransactionID, mode, index, s.log.sequence+1)
}
