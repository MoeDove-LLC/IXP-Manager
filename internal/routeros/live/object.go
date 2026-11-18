package live

import (
	"context"
	"reflect"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routeros/terminal"
)

type objectReader interface {
	ReadObject(context.Context, api.Resource, string) (api.Record, error)
	ReadIdentity(context.Context) (api.Inventory, error)
}
type objectProof struct {
	operationID string
	row         api.Record
	credential  api.Credential
	at          time.Time
	index       int
}
type objectPending struct {
	mutation         routeros.APIMutation
	expected         api.Record
	beforeCredential api.Credential
}
type objectActivation struct {
	epoch                   uint64
	planChecksum, sessionID string
	resolver                *routeros.APIResolver // observed reference identities only; never a later global snapshot
	rows                    map[string]api.Record // expected target records advanced only by exact actual object readback
	before                  *objectProof
	pending                 *objectPending
}

var _ control.ObjectRuntime = (*Runtime)(nil)

func (r *Runtime) invalidateObjectActivation() { r.objectEpoch.Add(1) }
func objectRecordCopy(row api.Record) api.Record {
	out := api.Record{}
	for k, v := range row {
		out[k] = v
	}
	return out
}
func (r *Runtime) objectSessionValid() bool {
	s := r.objectSession
	return s != nil && r.boundPlan != nil && r.bridge != nil && s.epoch == r.objectEpoch.Load() && s.planChecksum == r.boundPlan.Checksum && s.sessionID == r.sessionID && r.taken && !r.closed.Load() && !r.ownerClosed.Load() && time.Now().Before(r.deadline)
}

func (r *Runtime) BindObjectActivation(ctx context.Context, checksum string) (resultErr error) {
	r.invalidateObjectActivation()
	ctx, done, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	defer func() {
		if resultErr != nil {
			r.invalidateObjectActivation()
		}
	}()
	if !r.profile.ObjectObservationVerified || !r.profile.ActivationVerified || r.poisoned || !r.taken || r.boundPlan == nil || checksum != r.boundPlan.Checksum || !control.ObjectActivationEligible(*r.boundPlan) {
		return fail("object-activation-not-permitted")
	}
	if _, ok := r.api.(objectReader); !ok {
		return fail("object-reader-unavailable")
	}
	cached := r.detachWriteEvidence()
	if !r.usableWriteEvidence(cached) || !cached.taken {
		return fail("object-global-checkpoint-unavailable")
	}
	owner, err := r.owner(ctx)
	if err != nil || !owner.Known || !owner.Alive || !owner.Active || !owner.ThisSession || !owner.HistoryKnown || owner.HistoryActions != 0 {
		return fail("object-activation-owner-unverified")
	}
	resolver, err := routeros.NewAPIResolver(cached.raw, r.target, cached.evidence)
	if err != nil {
		return fail("object-global-resolver-unverified")
	}
	normalized, err := routeros.NormalizeObservedInventory(cached.raw, r.target, cached.evidence)
	if err != nil {
		return fail("object-global-checkpoint-unverified")
	}
	all := map[string]api.Record{}
	for _, m := range menus(normalized) {
		for _, row := range m.rows {
			all[cleanupKey(m.resource, row[".id"])] = row
		}
	}
	s := &objectActivation{epoch: r.objectEpoch.Load(), planChecksum: checksum, sessionID: r.sessionID, resolver: resolver, rows: map[string]api.Record{}}
	for _, op := range r.operations {
		if op.Phase != routeros.Activate {
			continue
		}
		m, e := r.bridge.Mutation(op.ID, resolver)
		if e != nil {
			return fail("object-operation-reference-unverified")
		}
		key := cleanupKey(m.Request.Resource, m.Request.ID)
		row := all[key]
		if row == nil {
			return fail("object-operation-target-missing")
		}
		if _, e = control.ExpectedObjectAttributes(row, m.Request); e != nil {
			return fail("object-operation-not-permitted")
		}
		s.rows[key] = objectRecordCopy(row)
	}
	if !r.usableWriteEvidence(cached) {
		return fail("object-global-checkpoint-expired")
	}
	r.objectSession = s
	return nil
}

func (r *Runtime) objectMutation(id string, after bool) (routeros.APIMutation, api.Record, api.Credential, error) {
	if !r.objectSessionValid() {
		return routeros.APIMutation{}, nil, api.Credential{}, fail("object-session-unavailable")
	}
	s := r.objectSession
	if after {
		if s.pending == nil || s.pending.mutation.OperationID != id {
			return routeros.APIMutation{}, nil, api.Credential{}, fail("object-after-not-pending")
		}
		return s.pending.mutation, s.pending.expected, s.pending.beforeCredential, nil
	}
	if r.poisoned || s.pending != nil || r.operationIndex >= len(r.operations) || r.operations[r.operationIndex].ID != id || r.operations[r.operationIndex].Phase != routeros.Activate {
		return routeros.APIMutation{}, nil, api.Credential{}, fail("object-operation-order")
	}
	m, err := r.bridge.Mutation(id, s.resolver)
	if err != nil {
		return m, nil, api.Credential{}, fail("object-operation-reference-unverified")
	}
	row := s.rows[cleanupKey(m.Request.Resource, m.Request.ID)]
	if row == nil {
		return m, nil, api.Credential{}, fail("object-operation-target-missing")
	}
	return m, row, api.Credential{}, nil
}

func (r *Runtime) readObjectOnce(ctx context.Context, reader objectReader, m routeros.APIMutation, want api.Record, grant *control.CredentialAuthority) (api.Record, api.Credential, error) {
	identity, err := reader.ReadIdentity(ctx)
	if err != nil {
		return nil, api.Credential{}, fail("object-identity-read-failed")
	}
	if _, err = r.identity(ctx, identity); err != nil {
		return nil, api.Credential{}, err
	}
	row, err := reader.ReadObject(ctx, m.Request.Resource, m.Request.ID)
	if err != nil {
		return nil, api.Credential{}, fail("object-record-read-failed")
	}
	queries := []api.PropertyRequest{}
	for _, field := range observedFields[m.Request.Resource] {
		value, present := row[field]
		presence := m.Request.Resource == api.BGPConnection && field == "dynamic"
		if present && !(m.Request.Resource == api.BGPConnection && (field == "as" || field == "output.default-prepend")) && (!presence || value != "") {
			continue
		}
		queries = append(queries, api.PropertyRequest{Resource: m.Request.Resource, ID: m.Request.ID, Property: field})
	}
	values := make([]api.PropertyValue, len(queries))
	if len(queries) > r.config.MaxGetterCalls || len(queries) > api.MaxPropertyBatch {
		return nil, api.Credential{}, fail("object-property-read-bound")
	}
	if len(queries) > 0 {
		if reader, ok := r.api.(propertyBatchReader); ok {
			values, err = reader.ReadProperties(ctx, queries)
		} else {
			for i, q := range queries {
				values[i], err = r.api.ReadProperty(ctx, q.Resource, q.ID, q.Property)
				if err != nil {
					break
				}
			}
		}
		if err != nil || len(values) != len(queries) {
			return nil, api.Credential{}, fail("object-property-read-failed")
		}
	}
	properties := make([]routeros.VersionPropertyEvidence, len(queries))
	for i, q := range queries {
		properties[i] = routeros.VersionPropertyEvidence{Resource: q.Resource, ID: q.ID, Field: q.Property, Completed: true, Property: values[i]}
	}
	row, err = routeros.NormalizeObservedObject(m.Request.Resource, row, r.target, properties)
	if err != nil {
		return nil, api.Credential{}, fail("object-properties-unverified")
	}
	actual, err := control.ObjectAttributesChecksum(m.Request.Resource, row)
	if err != nil {
		return nil, api.Credential{}, err
	}
	expected, err := control.ObjectAttributesChecksum(m.Request.Resource, want)
	if err != nil || actual != expected || row[".id"] != strings.ToUpper(m.Request.ID) {
		return nil, api.Credential{}, fail("object-attributes-mismatch")
	}
	credential := api.Credential{}
	if m.Request.Resource == api.BGPConnection {
		checksum, e := control.CredentialAttributesChecksum(row)
		owner := routeros.OwnerName(r.target.RouterID)
		if e != nil || grant == nil || grant.ID != row[".id"] || grant.Name != row["name"] || grant.AttributesChecksum != checksum || row["instance"] != owner || row["routing-table"] != owner || row["local.role"] != "ebgp-rs" {
			return nil, credential, fail("object-credential-authority-unverified")
		}
		credential, err = r.api.ReadCredential(ctx, row[".id"])
		value := credential.Value.CopyBytes()
		verified := credential.Matches(value)
		clear(value)
		if err != nil || !verified {
			return nil, api.Credential{}, fail("object-credential-read-unverified")
		}
	} else if grant != nil {
		return nil, credential, fail("object-credential-authority-unexpected")
	}
	return row, credential, nil
}

func (r *Runtime) ObserveObject(ctx context.Context, q control.ObjectReadRequest) (out control.ObjectObservation, resultErr error) {
	ctx, done, err := r.begin(ctx)
	if err != nil {
		return out, err
	}
	defer done()
	r.invalidateWriteEvidence()
	defer func() {
		if resultErr != nil {
			r.invalidateObjectActivation()
			r.poisoned = true
		}
	}()
	if !r.profile.ObjectObservationVerified || r.boundPlan == nil || q.PlanChecksum != r.boundPlan.Checksum {
		return out, fail("object-read-not-permitted")
	}
	m, want, beforeCredential, err := r.objectMutation(q.OperationID, q.After)
	if err != nil {
		return out, err
	}
	s := r.objectSession
	s.before = nil
	reader, ok := r.api.(objectReader)
	if !ok {
		return out, fail("object-reader-unavailable")
	}
	first, firstCred, err := r.readObjectOnce(ctx, reader, m, want, q.CredentialAuthority)
	if err != nil {
		return out, err
	}
	second, secondCred, err := r.readObjectOnce(ctx, reader, m, want, q.CredentialAuthority)
	if err != nil {
		return out, err
	}
	a, _ := control.ObjectAttributesChecksum(m.Request.Resource, first)
	b, _ := control.ObjectAttributesChecksum(m.Request.Resource, second)
	if a != b || !sameCredentials(map[string]api.Credential{m.Request.ID: firstCred}, map[string]api.Credential{m.Request.ID: secondCred}) {
		return out, fail("object-changed-during-observation")
	}
	if q.After && m.Request.Resource == api.BGPConnection {
		changed := false
		for _, attr := range m.Request.Attributes {
			if attr.Name == "tcp-md5-key" {
				changed = true
				if !secondCred.Matches([]byte(attr.Value)) {
					return out, fail("object-credential-readback-mismatch")
				}
			}
		}
		if !changed && !sameCredentials(map[string]api.Credential{m.Request.ID: beforeCredential}, map[string]api.Credential{m.Request.ID: secondCred}) {
			return out, fail("object-credential-readback-mismatch")
		}
	}
	owner, err := r.owner(ctx)
	if err != nil || !owner.Known || !owner.Alive || !owner.Active || !owner.ThisSession || !owner.HistoryKnown || owner.HistoryActions < 0 || owner.HistoryActions > r.profile.MaxHistoryActions || !r.objectSessionValid() {
		return out, fail("object-owner-unverified")
	}
	out = control.ObjectObservation{Target: r.target, Resource: m.Request.Resource, ID: strings.ToUpper(m.Request.ID), Attributes: objectRecordCopy(second), Credential: secondCred, ObservedAt: time.Now().UTC(), Owner: owner, CrossChannelIdentityVerified: true}
	if q.After {
		s.rows[cleanupKey(m.Request.Resource, m.Request.ID)] = objectRecordCopy(second)
		s.pending = nil
	} else {
		s.before = &objectProof{operationID: q.OperationID, row: objectRecordCopy(second), credential: secondCred, at: time.Now(), index: r.operationIndex}
	}
	return out, nil
}

func (r *Runtime) ApplyObject(ctx context.Context, m routeros.APIMutation) (api.Result, error) {
	ctx, done, err := r.begin(ctx)
	if err != nil {
		return api.Result{}, err
	}
	defer done()
	r.invalidateWriteEvidence()
	s := r.objectSession
	var proof *objectProof
	if s != nil {
		proof = s.before
		s.before = nil
	}
	if !r.profile.ObjectObservationVerified || !r.profile.ActivationVerified || r.poisoned || !r.objectSessionValid() {
		return api.Result{}, fail("object-write-not-permitted")
	}
	expected, want, _, err := r.objectMutation(m.OperationID, false)
	if err != nil || proof == nil || proof.operationID != m.OperationID || proof.index != r.operationIndex || time.Since(proof.at) < 0 || time.Since(proof.at) > time.Second || !reflect.DeepEqual(m, expected) {
		return api.Result{}, fail("object-write-proof-unavailable")
	}
	changed, err := control.ExpectedObjectAttributes(want, expected.Request)
	if err != nil {
		return api.Result{}, fail("object-write-not-permitted")
	}
	owner, err := r.owner(ctx)
	reserve := len(expected.Request.Attributes) + 1
	if reserve < 2 {
		reserve = 2
	}
	for _, op := range r.boundPlan.Operations {
		if op.ID == m.OperationID && op.EstimatedActions > reserve {
			reserve = op.EstimatedActions
			break
		}
	}
	if err != nil {
		return api.Result{}, fail("object-write-owner-query-failed")
	}
	if !owner.Known || !owner.Alive || !owner.Active || !owner.ThisSession {
		return api.Result{}, fail("object-write-owner-unverified")
	}
	if !owner.HistoryKnown || owner.HistoryActions < 0 {
		return api.Result{}, fail("object-write-history-unverified")
	}
	if r.profile.MaxHistoryActions <= 0 || owner.HistoryActions+reserve > r.profile.MaxHistoryActions {
		return api.Result{}, fail("object-write-history-exhausted")
	}
	if !r.objectSessionValid() {
		return api.Result{}, fail("object-write-session-invalidated")
	}
	if time.Since(proof.at) > time.Second {
		return api.Result{}, fail("object-write-proof-expired")
	}
	local := r.terminal.Snapshot()
	if local.Closed || local.Mode != terminal.SafeOwned || local.Owner != terminal.OwnerThisSession {
		return api.Result{}, fail("object-write-owner-unverified")
	}
	s.pending = &objectPending{mutation: expected, expected: changed, beforeCredential: proof.credential}
	writer, ok := r.terminal.(ownedMutationChannel)
	if !ok {
		return api.Result{}, fail("owned-mutation-channel-unavailable")
	}
	receipt, writeErr := writer.ExecuteOwned(ctx, expected.Request)
	if writeErr != nil || !receipt.Completed || receipt.Evidence.Closed || receipt.Evidence.Mode != terminal.SafeOwned || receipt.Evidence.Owner != terminal.OwnerThisSession || !receipt.Evidence.OwnerQueryAvailable {
		r.poisoned = true
		return api.Result{}, &Failure{Code: "owned-mutation-outcome-unknown", Uncertain: true}
	}
	r.operationIndex++
	return api.Result{Acknowledged: true}, nil
}
