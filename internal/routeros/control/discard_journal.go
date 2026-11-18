package control

import (
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
)

type preparedDiscardProgress struct {
	Checksum     string                           `json:"checksum"`
	Objects      []routeros.PreparedDiscardObject `json:"objects"`
	OperationIDs []string                         `json:"operationIds"`
	Index        int                              `json:"index"`
	Pending      *cleanupAttempt                  `json:"pending,omitempty"`
}

func (s *Supervisor) preparedDiscardManifest(objects []routeros.PreparedDiscardObject, ids []string) string {
	return mac(s.log.key, "prepared-discard-v1", struct {
		Target       routeros.Target
		Transaction  string
		Plan         string
		Baseline     string
		Prepared     string
		Objects      []routeros.PreparedDiscardObject
		OperationIDs []string
	}{s.config.Target, s.status.TransactionID, s.begin.Plan.Checksum, stateMAC(s.log.key, s.begin.Baseline), stateMAC(s.log.key, *s.rollback), objects, ids})
}

func (s *Supervisor) newPreparedDiscardProgress() (preparedDiscardProgress, error) {
	objects, err := s.preparedDiscardObjects()
	if err != nil {
		return preparedDiscardProgress{}, err
	}
	bridge, err := routeros.NewPreparedDiscardBridge(s.begin.Plan, objects)
	if err != nil {
		return preparedDiscardProgress{}, failure("discard-plan-invalid")
	}
	ids := []string{}
	for _, op := range bridge.Operations() {
		ids = append(ids, op.ID)
	}
	return preparedDiscardProgress{Checksum: s.preparedDiscardManifest(objects, ids), Objects: objects, OperationIDs: ids}, nil
}

func (s *Supervisor) replayPreparedDiscard(ev event) error {
	if s.removalBatch != nil && ev.Kind != "discard-status" {
		return failure("journal-discard-batch-unresolved")
	}
	if s.begin == nil || s.rollback == nil || s.status.Phase != RolledBack || !s.status.RollbackVerified || s.status.ConfirmationVerified {
		return failure("discard-without-verified-rollback")
	}
	p := &s.preparedDiscard
	switch ev.Kind {
	case "discard-start":
		if s.status.PreparedDiscard.Phase != PreparedDiscardNone || ev.PreparedDiscard == nil || ev.PreparedDiscard.Index != 0 || ev.PreparedDiscard.Pending != nil || ev.StateDigest != stateMAC(s.log.key, *s.rollback) {
			return failure("journal-discard-start-invalid")
		}
		expected, err := s.newPreparedDiscardProgress()
		if err != nil || mac(s.log.key, "discard-manifest", expected) != mac(s.log.key, "discard-manifest", *ev.PreparedDiscard) {
			return failure("journal-discard-scope-invalid")
		}
		*p = expected
		// The actual prepared checkpoint was independently observed immediately
		// before this event. It supersedes the cancelled activation's current
		// state and pending intent; the immutable rollback checkpoint remains.
		s.current = cloneState(*s.rollback)
		s.lastIntent, s.lastResult = nil, nil
		s.liveSession = ""
		s.confirmIntent, s.desiredVerified = false, false
		s.status.PreparedDiscard = PreparedDiscardStatus{Phase: PreparedDiscardPending, Checksum: p.Checksum, Total: len(p.OperationIDs), UpdatedAt: ev.At}
	case "discard-status":
		if ev.Status == nil || p.Checksum == "" || ev.Status.TransactionID != s.status.TransactionID || ev.Status.PlanChecksum != s.status.PlanChecksum || ev.Status.Phase != RolledBack || !ev.Status.RollbackVerified || ev.Status.ConfirmationVerified || ev.Status.PreparedDiscard.Checksum != p.Checksum || ev.Status.PreparedDiscard.Completed != p.Index || ev.Status.PreparedDiscard.Total != len(p.OperationIDs) {
			return failure("journal-discard-status-invalid")
		}
		phase := ev.Status.PreparedDiscard.Phase
		if phase != PreparedDiscardPending && phase != PreparedDiscardRunning && phase != PreparedDiscardUncertain || s.status.PreparedDiscard.Phase == PreparedDiscardComplete {
			return failure("journal-discard-status-invalid")
		}
		unchanged := s.status
		unchanged.PreparedDiscard = ev.Status.PreparedDiscard
		if mac(s.log.key, "discard-status", unchanged) != mac(s.log.key, "discard-status", *ev.Status) {
			return failure("journal-discard-status-scope")
		}
		s.status = *ev.Status
	case "discard-intent":
		if p.Checksum == "" || p.Pending != nil || p.Index >= len(p.OperationIDs) || ev.OperationID != p.OperationIDs[p.Index] || ev.Spec == nil || ev.Spec.Action != api.Remove || ev.Spec.CredentialDigest != "" || len(ev.Spec.Attributes) != 0 || ev.Spec.Property != "" || ev.StateDigest != stateMAC(s.log.key, s.current) {
			return failure("journal-discard-intent-invalid")
		}
		var target *routeros.PreparedDiscardObject
		for i := range p.Objects {
			if routeros.PreparedDiscardOperationID(p.Objects[i].PrepareOperationID) == ev.OperationID {
				target = &p.Objects[i]
				break
			}
		}
		if target == nil || ev.Spec.Resource != target.Resource || ev.Spec.ID != target.ID {
			return failure("journal-discard-intent-scope")
		}
		key := objectKey(target.Resource, target.ID)
		actual, exists := s.current.Objects[key]
		prepared, wasPrepared := s.rollback.Objects[key]
		if !exists || !wasPrepared || mac(s.log.key, "discard-object", actual) != mac(s.log.key, "discard-object", prepared) {
			return failure("journal-discard-target-changed")
		}
		p.Pending = &cleanupAttempt{OperationID: ev.OperationID, Spec: *ev.Spec, BeforeDigest: ev.StateDigest}
		s.status.PreparedDiscard.LastOperationID = ev.OperationID
	case "discard-result":
		if p.Pending == nil || p.Pending.OperationID != ev.OperationID || p.Pending.ResultRecorded || ev.ReturnedID != "" {
			return failure("journal-discard-result-invalid")
		}
		p.Pending.ResultRecorded, p.Pending.Acknowledged = true, ev.Acknowledged
	case "discard-readback":
		pending := p.Pending
		if pending == nil || pending.OperationID != ev.OperationID || pending.BeforeDigest != stateMAC(s.log.key, s.current) || ev.Delta == nil || !ev.Delta.Remove || ev.Delta.Object != nil || ev.Delta.Key != objectKey(pending.Spec.Resource, pending.Spec.ID) {
			return failure("journal-discard-readback-invalid")
		}
		next := cloneState(s.current)
		applyDelta(&next, *ev.Delta)
		if ev.StateDigest != stateMAC(s.log.key, next) {
			return failure("journal-discard-readback-digest")
		}
		s.current = next
		p.Index++
		p.Pending = nil
		s.status.PreparedDiscard.Completed = p.Index
		s.status.PreparedDiscard.UpdatedAt = ev.At
	case "discard-complete":
		if p.Checksum == "" || p.Pending != nil || p.Index != len(p.OperationIDs) || ev.StateDigest != stateMAC(s.log.key, s.current) || ev.StateDigest != stateMAC(s.log.key, s.begin.Baseline) || ev.Code != s.begin.Plan.BaseChecksum {
			return failure("journal-discard-complete-invalid")
		}
		s.status.PreparedDiscard.Phase = PreparedDiscardComplete
		s.status.PreparedDiscard.LastCode = ""
		s.status.PreparedDiscard.UpdatedAt = ev.At
		s.status.PreparedArtifactsRetained = false
		s.status.VerifiedAt = ev.At
	default:
		return failure("journal-discard-event-invalid")
	}
	return nil
}

func (s *Supervisor) preparedDiscardEvent(ev event) error {
	ev.At = time.Now().UTC()
	if err := s.record(ev); err != nil {
		return err
	}
	if err := s.replayPreparedDiscard(ev); err != nil {
		s.log.poisoned = true
		s.status.PreparedDiscard.Phase = PreparedDiscardUncertain
		s.status.PreparedDiscard.LastCode = "journal-discard-replay-failed"
		s.publishStatus()
		return err
	}
	s.publishStatus()
	return nil
}
func (s *Supervisor) setPreparedDiscard(phase PreparedDiscardPhase, code string) error {
	next := s.status
	next.PreparedDiscard.Phase, next.PreparedDiscard.LastCode = phase, code
	next.PreparedDiscard.UpdatedAt = time.Now().UTC()
	next.PreparedDiscard.Checksum = s.preparedDiscard.Checksum
	next.PreparedDiscard.Completed, next.PreparedDiscard.Total = s.preparedDiscard.Index, len(s.preparedDiscard.OperationIDs)
	return s.preparedDiscardEvent(event{Kind: "discard-status", Status: &next})
}
