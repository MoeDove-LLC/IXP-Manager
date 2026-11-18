package control

import (
	"context"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
)

// ObjectActivationEligible excludes anything that changes object membership or
// identity/order. Ineligible plans retain the original complete-read path.
func ObjectActivationEligible(p routeros.Plan) bool {
	count := 0
	for _, op := range p.Operations {
		if op.Phase != routeros.Activate {
			continue
		}
		count++
		switch op.Kind {
		case routeros.SetEntry, routeros.SetInstance, routeros.SetConnection, routeros.SetAuthentication, routeros.UnsetUnusedFamilyLimit, routeros.EnableConnection, routeros.DisableConnection:
		default:
			return false
		}
	}
	return count > 0
}
func objectGrant(obj objectState) (*CredentialAuthority, error) {
	if obj.Resource != api.BGPConnection {
		return nil, nil
	}
	row := copyRecord(obj.Attributes)
	row[".id"] = obj.ID
	digest, err := CredentialAttributesChecksum(row)
	if err != nil {
		return nil, err
	}
	return &CredentialAuthority{ID: obj.ID, Name: obj.Attributes["name"], AttributesChecksum: digest}, nil
}
func expectedObject(key []byte, before objectState, m api.Mutation) (objectState, error) {
	row := copyRecord(before.Attributes)
	row[".id"] = before.ID
	changed, err := ExpectedObjectAttributes(row, m)
	if err != nil {
		return objectState{}, err
	}
	attrs, err := canonicalRecord(m.Resource, changed)
	if err != nil {
		return objectState{}, err
	}
	out := before
	out.Attributes = attrs
	spec := specFor(key, m)
	if spec.CredentialDigest != "" {
		out.CredentialKnown = true
		out.CredentialDigest = spec.CredentialDigest
	}
	return out, nil
}
func (s *Supervisor) readObject(ctx context.Context, runtime ObjectRuntime, request ObjectReadRequest, resource api.Resource, id string) (ObjectObservation, objectState, error) {
	call, cancel := s.operationContext(ctx)
	defer cancel()
	started := time.Now()
	o, err := runtime.ObserveObject(call, request)
	if err != nil {
		return o, objectState{}, failure("object-observation-failed")
	}
	if o.Target != s.config.Target || o.ObservedAt.Before(started.Add(-time.Millisecond)) || o.ObservedAt.After(time.Now().Add(time.Second)) || time.Since(o.ObservedAt) > s.config.OperationTimeout+time.Second {
		return o, objectState{}, failure("object-observation-binding-invalid")
	}
	if !o.Owner.Known || !o.Owner.Alive || !o.Owner.Active || !o.Owner.ThisSession || !o.Owner.HistoryKnown || o.Owner.HistoryActions < 0 || o.Owner.HistoryActions > s.runtime.Binding().MaxHistoryActions || o.Owner.SessionID != s.liveSession {
		return o, objectState{}, failure("object-observation-owner-unverified")
	}
	obj, err := projectObject(s.log.key, resource, id, o)
	return o, obj, err
}

func (s *Supervisor) executeObject(ctx context.Context, bridge *routeros.APIBridge, runtime ObjectRuntime, resolver *routeros.APIResolver, operation routeros.APIOperation) error {
	if operation.Phase != routeros.Activate || resolver == nil {
		return failure("object-operation-not-activation")
	}
	m, err := bridge.Mutation(operation.ID, resolver)
	if err != nil {
		return failure("object-operation-resolution-failed")
	}
	id := strings.ToUpper(m.Request.ID)
	key := objectKey(m.Request.Resource, id)
	before, exists := s.current.Objects[key]
	if !exists || before.ID != id {
		return failure("object-operation-not-owned")
	}
	want, err := expectedObject(s.log.key, before, m.Request)
	if err != nil {
		return err
	}
	grant, err := objectGrant(before)
	if err != nil {
		return err
	}
	_, observed, err := s.readObject(ctx, runtime, ObjectReadRequest{PlanChecksum: s.begin.Plan.Checksum, OperationID: operation.ID, CredentialAuthority: grant}, m.Request.Resource, id)
	if err != nil {
		return err
	}
	if mac(s.log.key, "object", observed) != mac(s.log.key, "object", before) {
		return failure("object-changed-before-write")
	}
	reserve := len(m.Request.Attributes) + 1
	if reserve < 2 {
		reserve = 2
	}
	for _, op := range s.begin.Plan.Operations {
		if op.ID == operation.ID && op.EstimatedActions > reserve {
			reserve = op.EstimatedActions
			break
		}
	}
	if err = s.requireOwner(ctx, reserve); err != nil {
		return err
	}
	spec := specFor(s.log.key, m.Request)
	intent := event{Kind: "intent", OperationID: operation.ID, Spec: &spec, StateDigest: stateMAC(s.log.key, s.current)}
	if err = s.record(intent); err != nil {
		return err
	}
	s.lastIntent = &intent
	s.status.LastOperationID = operation.ID
	s.publishStatus()
	call, cancel := s.operationContext(ctx)
	result, writeErr := runtime.ApplyObject(call, m)
	cancel()
	if result.ID != "" {
		writeErr = failure("object-write-returned-unexpected-id")
	}
	code := ""
	if writeErr != nil || !result.Acknowledged {
		code = "write-outcome-unknown"
	}
	if err = s.record(event{Kind: "result", OperationID: operation.ID, Acknowledged: writeErr == nil && result.Acknowledged, Code: code}); err != nil {
		return err
	}
	grant, err = objectGrant(want)
	if err != nil {
		return err
	}
	_, actual, readErr := s.readObject(ctx, runtime, ObjectReadRequest{PlanChecksum: s.begin.Plan.Checksum, OperationID: operation.ID, After: true, CredentialAuthority: grant}, m.Request.Resource, id)
	if readErr != nil {
		return failure("object-write-readback-unavailable")
	}
	if mac(s.log.key, "object", actual) != mac(s.log.key, "object", want) {
		return failure("object-write-readback-mismatch")
	}
	delta := stateDelta{Key: key, Object: &actual}
	// This is the expected global state advanced by an actually verified object
	// delta. It is never returned as a globally observed Snapshot.
	next := cloneState(s.current)
	applyDelta(&next, delta)
	if err = s.record(event{Kind: "readback", OperationID: operation.ID, Delta: &delta, StateDigest: stateMAC(s.log.key, next), Code: "object-only-readback"}); err != nil {
		return err
	}
	applyDelta(&s.current, delta)
	s.lastIntent = nil
	if writeErr != nil || !result.Acknowledged {
		return failure("write-outcome-unknown")
	}
	return nil
}
