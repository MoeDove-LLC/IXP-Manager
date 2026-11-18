package control

import (
	"strings"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
)

// Reconstructed from authenticated intent/result/readback records. Names alone
// never create this authority, and the final attributes come from the prepared
// checkpoint (after new-connection authentication/AF initialization).
type preparedCreate struct {
	OperationID string
	Resource    api.Resource
	ID          string
}

func prepareAddResource(op routeros.Operation) (api.Resource, bool) {
	if op.Phase != routeros.Prepare || op.Object.ID != "" {
		return "", false
	}
	switch op.Kind {
	case routeros.AddTable:
		return api.RoutingTable, true
	case routeros.AddInstance:
		return api.BGPInstance, true
	case routeros.AddRule:
		return api.FilterRule, true
	case routeros.AddDisabledConnection:
		return api.BGPConnection, true
	}
	return "", false
}

func (s *Supervisor) createProof(operation string, spec mutationSpec, returned string, delta stateDelta) (preparedCreate, error) {
	if s.begin == nil || spec.Action != api.Add || spec.ID != "" || delta.Remove || delta.Object == nil || delta.Key != objectKey(delta.Object.Resource, delta.Object.ID) || !validObjectID(delta.Object.ID) {
		return preparedCreate{}, failure("journal-prepared-create-invalid")
	}
	if s.preparedOperations == nil {
		s.preparedOperations = map[string]routeros.Operation{}
		for _, candidate := range s.begin.Plan.Operations {
			s.preparedOperations[candidate.ID] = candidate
		}
	}
	op := s.preparedOperations[operation]
	resource, adding := prepareAddResource(op)
	obj := *delta.Object
	if !adding || resource != spec.Resource || resource != obj.Resource || returned != "" && !strings.EqualFold(returned, obj.ID) {
		return preparedCreate{}, failure("journal-prepared-create-scope")
	}
	if _, exists := s.current.Objects[delta.Key]; exists {
		return preparedCreate{}, failure("journal-prepared-create-existing-id")
	}
	if resource == api.FilterRule {
		if obj.Attributes["chain"] != op.Object.Chain || obj.Attributes["comment"] != op.Object.Comment {
			return preparedCreate{}, failure("journal-prepared-create-marker")
		}
	} else if obj.Attributes["name"] != op.Object.Name {
		return preparedCreate{}, failure("journal-prepared-create-name")
	}
	if newObjectMismatch(s.log.key, s.begin.Plan.Owner, obj, spec) != "" {
		return preparedCreate{}, failure("journal-prepared-create-attributes")
	}
	return preparedCreate{OperationID: operation, Resource: resource, ID: obj.ID}, nil
}

func (s *Supervisor) preparedEventProofs(ev event) ([]preparedCreate, error) {
	proofs := []preparedCreate{}
	switch ev.Kind {
	case "readback":
		if s.lastIntent == nil || s.lastIntent.Spec == nil || s.lastIntent.Spec.Action != api.Add {
			return proofs, nil
		}
		if s.lastResult == nil || s.lastResult.OperationID != ev.OperationID || s.lastIntent.OperationID != ev.OperationID || ev.Delta == nil {
			return nil, failure("journal-prepared-create-result-missing")
		}
		proof, err := s.createProof(ev.OperationID, *s.lastIntent.Spec, s.lastResult.ReturnedID, *ev.Delta)
		if err != nil {
			return nil, err
		}
		proofs = append(proofs, proof)
	case "rule-batch-readback":
		if s.batch == nil || ev.BatchID != s.batch.ID {
			return nil, failure("journal-prepared-batch-unbound")
		}
		type marker struct{ chain, comment string }
		steps := map[marker]ruleBatchStep{}
		for _, step := range s.batch.Steps {
			m := marker{step.Spec.Attributes["chain"], step.Spec.Attributes["comment"]}
			if _, exists := steps[m]; exists {
				return nil, failure("journal-prepared-batch-duplicate")
			}
			steps[m] = step
		}
		for _, delta := range ev.Deltas {
			if delta.Object == nil {
				return nil, failure("journal-prepared-batch-object-missing")
			}
			m := marker{delta.Object.Attributes["chain"], delta.Object.Attributes["comment"]}
			step, ok := steps[m]
			if !ok {
				return nil, failure("journal-prepared-batch-unattempted")
			}
			proof, err := s.createProof(step.OperationID, step.Spec, step.ReturnedID, delta)
			if err != nil {
				return nil, err
			}
			proofs = append(proofs, proof)
			delete(steps, m)
		}
	}
	return proofs, nil
}

func (s *Supervisor) checkPreparedEvent(ev event) error {
	if ev.Kind == "rule-batch-start" && s.removalBatch != nil {
		return failure("another-write-unresolved")
	}
	proofs, err := s.preparedEventProofs(ev)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, proof := range proofs {
		if _, exists := s.preparedCreates[proof.OperationID]; exists || seen[proof.OperationID] {
			return failure("journal-prepared-create-repeated")
		}
		seen[proof.OperationID] = true
	}
	return nil
}
func (s *Supervisor) rememberPreparedEvent(ev event) {
	proofs, _ := s.preparedEventProofs(ev) // checked before the durable append/replay
	if len(proofs) > 0 && s.preparedCreates == nil {
		s.preparedCreates = map[string]preparedCreate{}
	}
	for _, proof := range proofs {
		s.preparedCreates[proof.OperationID] = proof
	}
	switch ev.Kind {
	case "intent":
		s.lastResult = nil
	case "result":
		copy := ev
		s.lastResult = &copy
	case "readback":
		s.lastResult = nil
	}
}

func (s *Supervisor) preparedDiscardObjects() ([]routeros.PreparedDiscardObject, error) {
	if s.begin == nil || s.rollback == nil || !completeCredentials(s.begin.Baseline) || !completeCredentials(*s.rollback) {
		return nil, failure("discard-checkpoint-unavailable")
	}
	objects := []routeros.PreparedDiscardObject{}
	expected := cloneState(s.begin.Baseline)
	seen := map[string]bool{}
	for _, op := range s.begin.Plan.Operations {
		resource, adding := prepareAddResource(op)
		if !adding {
			continue
		}
		proof, ok := s.preparedCreates[op.ID]
		key := objectKey(resource, proof.ID)
		obj, present := s.rollback.Objects[key]
		_, existed := s.begin.Baseline.Objects[key]
		if !ok || proof.Resource != resource || !present || existed || seen[key] || obj.Resource != resource || obj.ID != proof.ID {
			return nil, failure("discard-prepare-provenance-unverified")
		}
		seen[key] = true
		objects = append(objects, routeros.PreparedDiscardObject{PrepareOperationID: op.ID, Resource: resource, ID: obj.ID, Attributes: copyRecord(obj.Attributes)})
		copy := obj
		copy.Attributes = copyRecord(obj.Attributes)
		applyDelta(&expected, stateDelta{Key: key, Object: &copy})
	}
	if len(objects) != len(s.preparedCreates) || stateMAC(s.log.key, expected) != stateMAC(s.log.key, *s.rollback) {
		return nil, failure("discard-checkpoint-not-exact-prepare-additions")
	}
	if _, err := routeros.NewPreparedDiscardBridge(s.begin.Plan, objects); err != nil {
		return nil, failure("discard-prepared-objects-invalid")
	}
	return objects, nil
}
