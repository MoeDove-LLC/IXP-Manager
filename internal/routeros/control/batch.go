package control

import (
	"context"
	"fmt"
	"strings"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
)

type ruleBatchStep struct {
	OperationID    string       `json:"operationId"`
	Spec           mutationSpec `json:"spec"`
	ResultRecorded bool         `json:"resultRecorded"`
	Acknowledged   bool         `json:"acknowledged"`
	ReturnedID     string       `json:"returnedId,omitempty"`
}
type ruleBatchState struct {
	ID           string          `json:"id"`
	PlanChecksum string          `json:"planChecksum"`
	Generation   string          `json:"generation"`
	OperationIDs []string        `json:"operationIds"`
	BeforeDigest string          `json:"beforeDigest"`
	Steps        []ruleBatchStep `json:"steps"`
}

func (s *Supervisor) ruleBatchIDs(operations []routeros.APIOperation) []string {
	if len(operations) == 0 || operations[0].Phase != routeros.Prepare || operations[0].Resource != routeros.FilterRule || operations[0].Action != routeros.Add {
		return nil
	}
	p := s.begin.Plan
	for _, g := range p.Base.Generations {
		if g.Generation == p.Generation {
			return nil
		}
	}
	byID := map[string]routeros.Operation{}
	for _, op := range p.Operations {
		byID[op.ID] = op
	}
	ids := []string{}
	for _, entry := range operations {
		op := byID[entry.ID]
		if op.Phase != routeros.Prepare || op.Kind != routeros.AddRule || op.Rule == nil || op.Object.ID != "" {
			break
		}
		root := p.Owner + "_g" + p.Generation + "_"
		if op.Rule.Chain != root+"in" && op.Rule.Chain != root+"out" && op.Rule.Chain != root+"src" {
			break
		}
		ids = append(ids, op.ID)
	}
	return ids
}
func (s *Supervisor) replayRuleBatch(ev event) error {
	switch ev.Kind {
	case "rule-batch-start":
		if s.begin == nil || s.batch != nil || s.lastIntent != nil || ev.Batch == nil || ev.Batch.PlanChecksum != s.begin.Plan.Checksum || ev.Batch.Generation != s.begin.Plan.Generation || len(ev.Batch.OperationIDs) == 0 || ev.Batch.BeforeDigest != stateMAC(s.log.key, s.current) {
			return failure("journal-rule-batch-invalid")
		}
		copy := *ev.Batch
		copy.OperationIDs = append([]string(nil), ev.Batch.OperationIDs...)
		copy.Steps = nil
		s.batch = &copy
	case "rule-batch-intent":
		b := s.batch
		if b == nil || ev.BatchID != b.ID || ev.Spec == nil || len(b.Steps) >= len(b.OperationIDs) || ev.OperationID != b.OperationIDs[len(b.Steps)] || len(b.Steps) > 0 && !b.Steps[len(b.Steps)-1].Acknowledged {
			return failure("journal-rule-batch-intent-invalid")
		}
		b.Steps = append(b.Steps, ruleBatchStep{OperationID: ev.OperationID, Spec: *ev.Spec})
		s.status.LastOperationID = ev.OperationID
	case "rule-batch-result":
		b := s.batch
		if b == nil || ev.BatchID != b.ID || len(b.Steps) == 0 {
			return failure("journal-rule-batch-result-invalid")
		}
		step := &b.Steps[len(b.Steps)-1]
		if step.OperationID != ev.OperationID || step.ResultRecorded || ev.Acknowledged && !validObjectID(strings.ToUpper(ev.ReturnedID)) {
			return failure("journal-rule-batch-result-invalid")
		}
		step.ResultRecorded = true
		step.Acknowledged = ev.Acknowledged
		step.ReturnedID = strings.ToUpper(ev.ReturnedID)
	case "rule-batch-readback":
		b := s.batch
		if b == nil || ev.BatchID != b.ID {
			return failure("journal-rule-batch-readback-invalid")
		}
		for _, delta := range ev.Deltas {
			applyDelta(&s.current, delta)
		}
		if ev.StateDigest != stateMAC(s.log.key, s.current) {
			return failure("journal-rule-batch-digest-invalid")
		}
		s.batch = nil
	}
	return nil
}

type batchRecorder struct {
	s        *Supervisor
	expected []routeros.APIMutation
	stopped  bool
}

func (r *batchRecorder) Before(id string) error {
	if r.stopped || r.s.batch == nil {
		return failure("rule-batch-recorder-stopped")
	}
	b := r.s.batch
	i := len(b.Steps)
	if i >= len(r.expected) || r.expected[i].OperationID != id || i > 0 && !b.Steps[i-1].Acknowledged {
		return failure("rule-batch-order-mismatch")
	}
	spec := specFor(r.s.log.key, r.expected[i].Request)
	if spec.Resource != api.FilterRule || spec.Action != api.Add || spec.ID != "" || spec.CredentialDigest != "" {
		return failure("rule-batch-not-inert")
	}
	ev := event{Kind: "rule-batch-intent", BatchID: b.ID, OperationID: id, Spec: &spec}
	if err := r.s.record(ev); err != nil {
		r.stopped = true
		return err
	}
	if err := r.s.replayRuleBatch(ev); err != nil {
		r.stopped = true
		return err
	}
	r.s.publishStatus()
	return nil
}
func (r *batchRecorder) After(id string, result api.Result, writeErr error) error {
	if r.stopped || r.s.batch == nil {
		return failure("rule-batch-recorder-stopped")
	}
	b := r.s.batch
	if len(b.Steps) == 0 || b.Steps[len(b.Steps)-1].OperationID != id || b.Steps[len(b.Steps)-1].ResultRecorded {
		return failure("rule-batch-result-order-mismatch")
	}
	good := writeErr == nil && result.Acknowledged && validObjectID(strings.ToUpper(result.ID))
	code := ""
	if !good {
		code = "write-outcome-unknown"
	}
	returned := ""
	if validObjectID(strings.ToUpper(result.ID)) {
		returned = strings.ToUpper(result.ID)
	}
	ev := event{Kind: "rule-batch-result", BatchID: b.ID, OperationID: id, Acknowledged: good, ReturnedID: returned, Code: code}
	if err := r.s.record(ev); err != nil {
		r.stopped = true
		return err
	}
	if err := r.s.replayRuleBatch(ev); err != nil {
		r.stopped = true
		return err
	}
	if !good {
		r.stopped = true
		return failure("rule-batch-outcome-unknown")
	}
	return nil
}
func (s *Supervisor) executeRuleBatch(ctx context.Context, bridge *routeros.APIBridge, runtime RuleBatchRuntime, ids []string) error {
	if s.batch != nil || s.lastIntent != nil {
		return failure("another-write-unresolved")
	}
	o, before, err := s.readCurrent(ctx)
	if err != nil {
		return err
	}
	if !o.Owner.Known || o.Owner.Active || stateMAC(s.log.key, before) != stateMAC(s.log.key, s.current) {
		return failure("rule-batch-baseline-changed")
	}
	commands := make([]routeros.APIMutation, 0, len(ids))
	for _, id := range ids {
		m, e := bridge.Mutation(id, o.Resolver)
		if e != nil {
			return failure("rule-batch-reference-unverified")
		}
		commands = append(commands, m)
	}
	batch := ruleBatchState{ID: fmt.Sprintf("%s-%s", s.status.TransactionID, ids[0]), PlanChecksum: s.begin.Plan.Checksum, Generation: s.begin.Plan.Generation, OperationIDs: append([]string(nil), ids...), BeforeDigest: stateMAC(s.log.key, s.current)}
	ev := event{Kind: "rule-batch-start", Batch: &batch}
	if err = s.record(ev); err != nil {
		return err
	}
	if err = s.replayRuleBatch(ev); err != nil {
		return err
	}
	recorder := &batchRecorder{s: s, expected: commands}
	writeErr := runtime.ApplyRuleBatch(ctx, RuleBatch{ID: batch.ID, PlanChecksum: batch.PlanChecksum, Generation: batch.Generation, Operations: commands}, recorder)
	if s.log.poisoned {
		return failure("journal-unavailable")
	}
	_, actual, readErr := s.readCurrent(ctx)
	if readErr != nil {
		return failure("rule-batch-readback-unavailable")
	}
	deltas, verifyErr := s.verifyRuleBatch(actual)
	if verifyErr != nil {
		return verifyErr
	}
	allAck := len(s.batch.Steps) == len(ids)
	for _, step := range s.batch.Steps {
		allAck = allAck && step.ResultRecorded && step.Acknowledged
	}
	if err = s.finishRuleBatch(actual, deltas); err != nil {
		return err
	}
	if writeErr != nil || !allAck {
		return failure("rule-batch-interrupted")
	}
	return nil
}
func (s *Supervisor) verifyRuleBatch(after deviceState) ([]stateDelta, error) {
	b := s.batch
	if b == nil || b.BeforeDigest != stateMAC(s.log.key, s.current) || s.current.ExternalDigest != after.ExternalDigest {
		return nil, failure("rule-batch-state-changed")
	}
	expected := cloneState(s.current)
	deltas := []stateDelta{}
	seen := map[string]bool{}
	type marker struct{ chain, comment string }
	added := map[marker][]objectState{}
	for key, obj := range after.Objects {
		if _, existed := s.current.Objects[key]; !existed && obj.Resource == api.FilterRule {
			m := marker{obj.Attributes["chain"], obj.Attributes["comment"]}
			added[m] = append(added[m], obj)
		}
	}
	for _, step := range b.Steps {
		var found *objectState
		matches := added[marker{step.Spec.Attributes["chain"], step.Spec.Attributes["comment"]}]
		if len(matches) > 1 {
			return nil, failure("rule-batch-duplicate-marker")
		}
		if len(matches) == 1 {
			copy := matches[0]
			found = &copy
		}
		if found == nil {
			if step.Acknowledged {
				return nil, failure("rule-batch-acked-object-missing")
			}
			continue
		}
		if mismatch := newObjectMismatch(s.log.key, s.begin.Plan.Owner, *found, step.Spec); mismatch != "" {
			return nil, failure("rule-batch-" + mismatch)
		}
		if step.Acknowledged && !strings.EqualFold(step.ReturnedID, found.ID) {
			return nil, failure("rule-batch-id-mismatch")
		}
		key := objectKey(found.Resource, found.ID)
		if seen[key] {
			return nil, failure("rule-batch-id-reused")
		}
		seen[key] = true
		delta := stateDelta{Key: key, Object: found}
		deltas = append(deltas, delta)
		applyDelta(&expected, delta)
	}
	if stateMAC(s.log.key, expected) != stateMAC(s.log.key, after) {
		return nil, failure("rule-batch-exact-difference-unverified")
	}
	return deltas, nil
}
func (s *Supervisor) finishRuleBatch(actual deviceState, deltas []stateDelta) error {
	ev := event{Kind: "rule-batch-readback", BatchID: s.batch.ID, Deltas: deltas, StateDigest: stateMAC(s.log.key, actual)}
	if err := s.record(ev); err != nil {
		return err
	}
	return s.replayRuleBatch(ev)
}
