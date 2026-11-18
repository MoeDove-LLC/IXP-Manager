package live

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routeros/terminal"
)

const maxRemovalRules = 256
const removalWriteWindow = 30 * time.Second

type ruleRemovalScope struct {
	mode                        control.RuleRemovalMode
	proof                       control.RuleRemovalProofMode
	transaction, plan, checksum string
}
type presenceReader interface {
	ReadObjectPresence(context.Context, api.Resource, string) (api.ObjectPresence, error)
}
type removalProtected struct {
	resource     api.Resource
	id, checksum string
}

var _ control.RuleRemovalBatchRuntime = (*Runtime)(nil)

// The caller must maintain an exclusive device configuration window. Owner-none
// is not a device lock. Actual global proofs bracket this operation; per-object
// observations never become a global snapshot and cannot authorize later batches.
func (r *Runtime) ApplyRuleRemovalBatch(ctx context.Context, batch control.RuleRemovalBatch, recorder control.RuleRemovalRecorder) (resultErr error) {
	r.invalidateObjectActivation()
	ctx, done, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	defer r.invalidateWriteEvidence()
	cached := r.detachRemovalEvidence()
	if !r.profile.RuleRemovalBatchVerified || r.poisoned || r.taken || r.removalTail != nil || r.closed.Load() || r.ownerClosed.Load() || recorder == nil || len(batch.Operations) == 0 || len(batch.Operations) > maxRemovalRules || !removalDigest(batch.ScopeChecksum) || !removalDigest(batch.PlanChecksum) || batch.ID == "" || len(batch.ID) > 128 || batch.TransactionID == "" || len(batch.TransactionID) > 128 {
		return fail("rule-removal-batch-not-permitted")
	}
	proofMode := batch.ProofMode
	if proofMode == "" {
		proofMode = control.RuleRemovalProofPerObject
	}
	if proofMode != control.RuleRemovalProofPerObject && proofMode != control.RuleRemovalProofGlobalTail || proofMode == control.RuleRemovalProofGlobalTail && !r.profile.RuleRemovalGlobalTailVerified {
		return fail("rule-removal-proof-mode-invalid")
	}
	reader, ok := r.api.(presenceReader)
	if !ok {
		return fail("rule-removal-presence-reader-unavailable")
	}
	safeReader, ok := r.api.(safeModeReader)
	if !ok {
		return fail("rule-removal-owner-reader-unavailable")
	}
	var plan routeros.Plan
	var bridge *routeros.APIBridge
	var operations []routeros.APIOperation
	var index int
	var head func(context.Context) (api.Inventory, routeros.TargetEvidence, api.Inventory, error)
	advance := func() {}
	switch batch.Mode {
	case control.RuleRemovalPreparedDiscard:
		if !r.profile.PreparedDiscardVerified || r.preparedDiscardFinished() || r.bridge != nil || r.boundPlan != nil || r.cleanup != nil {
			return fail("rule-removal-discard-not-bound")
		}
		p := r.preparedDiscard
		plan, bridge, operations, index = p.binding.Plan, p.bridge, p.operations, p.next
		head = func(c context.Context) (api.Inventory, routeros.TargetEvidence, api.Inventory, error) {
			raw, e, err := r.preparedDiscardStateWithEvidence(c, p, cached)
			if err != nil {
				return raw, e, api.Inventory{}, err
			}
			normal, err := routeros.NormalizeObservedInventory(raw, r.target, e)
			return raw, e, normal, err
		}
		advance = func() { p.next++ }
	case control.RuleRemovalCleanup:
		if !r.profile.CleanupVerified || r.cleanupFinished() || r.preparedDiscard != nil {
			return fail("rule-removal-cleanup-not-bound")
		}
		p := r.cleanup
		plan, bridge, operations, index = p.plan, p.bridge, p.all, p.next
		head = func(c context.Context) (api.Inventory, routeros.TargetEvidence, api.Inventory, error) {
			raw, e, normal, _, err := r.cleanupStateWithEvidence(c, p, cached)
			return raw, e, normal, err
		}
		advance = func() {
			p.next++
			if p.next == len(p.all) && proofMode == control.RuleRemovalProofPerObject {
				r.completed = true
			}
		}
	default:
		return fail("rule-removal-mode-invalid")
	}
	if batch.PlanChecksum != plan.Checksum || index+len(batch.Operations) > len(operations) {
		return fail("rule-removal-suffix-invalid")
	}
	scope := ruleRemovalScope{mode: batch.Mode, proof: proofMode, transaction: batch.TransactionID, plan: batch.PlanChecksum, checksum: batch.ScopeChecksum}
	if r.removalScope != nil && *r.removalScope != scope {
		return fail("rule-removal-scope-changed")
	}
	raw, evidence, normal, err := head(ctx)
	if err != nil {
		return err
	}
	owner, err := r.owner(ctx)
	if err != nil || !owner.Known || !owner.Alive || owner.Active || owner.ThisSession || !owner.HistoryKnown || owner.HistoryActions != 0 {
		return fail("rule-removal-owner-unverified")
	}
	resolver, err := routeros.NewAPIResolver(raw, r.target, evidence)
	if err != nil {
		return fail("rule-removal-reference-unverified")
	}
	rules := map[string]api.Record{}
	for _, row := range normal.FilterRules {
		rules[strings.ToUpper(row[".id"])] = row
	}
	expected := make([]routeros.APIMutation, len(batch.Operations))
	wanted := make([]string, len(expected))
	generation := ""
	for i, supplied := range batch.Operations {
		if supplied.OperationID != operations[index+i].ID {
			return fail("rule-removal-suffix-invalid")
		}
		m, err := bridge.Mutation(supplied.OperationID, resolver)
		if err != nil || !reflect.DeepEqual(m, supplied) || m.Request.Resource != api.FilterRule || m.Request.Action != api.Remove {
			return fail("rule-removal-operation-invalid")
		}
		row := rules[strings.ToUpper(m.Request.ID)]
		g, ok := removalRuleGeneration(plan.Owner, row)
		if !ok || generation != "" && generation != g {
			return fail("rule-removal-generation-invalid")
		}
		generation = g
		checksum, err := control.ObjectAttributesChecksum(api.FilterRule, row)
		if err != nil {
			return fail("rule-removal-object-unverified")
		}
		expected[i], wanted[i] = m, checksum
	}
	protected, err := removalProtection(plan.Owner, normal)
	if err != nil {
		return err
	}
	r.ownerMu.Lock()
	epoch := r.ownerEpoch
	r.ownerMu.Unlock()
	session := r.sessionID
	r.removalScope = &scope
	acknowledged := []string{}
	if proofMode == control.RuleRemovalProofGlobalTail {
		defer func() {
			// An ACK only advances this private attempt cursor. A successful
			// full Observe must prove the exact delta before any further write.
			if len(acknowledged) == 0 || r.poisoned {
				return
			}
			if err := r.expectRemovalTail(normal, acknowledged, batch.Mode, session, epoch); err != nil {
				r.poisoned = true
				resultErr = err
			}
		}()
	}
	until := time.Now().Add(removalWriteWindow)
	for i, m := range expected {
		if i > 0 && !time.Now().Before(until) {
			return nil
		}
		if err = r.removalSessionCurrent(epoch, session); err != nil {
			return err
		}
		before, err := r.removalObservation(ctx, reader, safeReader, batch.ID, m.Request.ID, true, protected, epoch, session)
		if err != nil {
			return err
		}
		checksum, err := control.ObjectAttributesChecksum(api.FilterRule, before.Attributes)
		if err != nil || checksum != wanted[i] {
			return fail("rule-removal-object-changed")
		}
		if !time.Now().Before(until) {
			return nil // no new intent after the soft write window
		}
		if err := ctx.Err(); err != nil {
			return fail("rule-removal-context-ended")
		}
		if err = recorder.Before(m.OperationID, before); err != nil {
			return fail("rule-removal-intent-not-durable")
		}
		var result api.Result
		writeErr := r.removalSessionCurrent(epoch, session)
		if writeErr == nil {
			state, ownerErr := safeReader.ReadSafeMode(ctx)
			if ownerErr != nil || state.Enabled || state.Current {
				writeErr = fail("rule-removal-owner-not-idle")
			}
		}
		if writeErr == nil {
			writeErr = r.removalSessionCurrent(epoch, session)
		}
		if writeErr == nil {
			result, writeErr = r.api.Apply(ctx, m.Request)
		}
		ack := writeErr == nil && result.Acknowledged && result.ID == ""
		if !ack {
			r.poisoned = true
		}
		if err = recorder.Result(m.OperationID, result, writeErr); err != nil {
			r.poisoned = true
			return fail("rule-removal-result-not-durable")
		}
		if !ack {
			return &Failure{Code: "rule-removal-outcome-unknown", Uncertain: true}
		}
		if proofMode == control.RuleRemovalProofGlobalTail {
			acknowledged = append(acknowledged, m.Request.ID)
			advance()
			continue
		}
		after, err := r.removalObservation(ctx, reader, safeReader, batch.ID, m.Request.ID, false, protected, epoch, session)
		if err != nil {
			r.poisoned = true
			return err
		}
		if err = recorder.Absent(m.OperationID, after); err != nil {
			r.poisoned = true
			return fail("rule-removal-absence-not-durable")
		}
		advance()
	}
	return nil
}

func removalDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func removalRuleGeneration(owner string, row api.Record) (string, bool) {
	chain := row["chain"]
	prefix := owner + "_g"
	if !strings.HasPrefix(chain, prefix) {
		return "", false
	}
	tail := strings.TrimPrefix(chain, prefix)
	if len(tail) < 19 || tail[16] != '_' {
		return "", false
	}
	for _, c := range tail[:16] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", false
		}
	}
	if suffix := tail[17:]; suffix != "in" && suffix != "out" && suffix != "src" {
		return "", false
	}
	return tail[:16], true
}
func removalProtection(owner string, inv api.Inventory) ([]removalProtected, error) {
	out := []removalProtected{}
	for _, menu := range menus(inv) {
		for _, row := range menu.rows {
			owned := row["name"] == owner || strings.HasPrefix(row["name"], owner+"_") || strings.HasPrefix(row["comment"], owner+":")
			if menu.resource == api.FilterRule {
				owned = row["chain"] == owner+"_root_in" || row["chain"] == owner+"_root_out"
			}
			if !owned {
				continue
			}
			checksum, err := control.ObjectAttributesChecksum(menu.resource, row)
			if err != nil {
				return nil, fail("rule-removal-protection-invalid")
			}
			out = append(out, removalProtected{menu.resource, strings.ToUpper(row[".id"]), checksum})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return cleanupKey(out[i].resource, out[i].id) < cleanupKey(out[j].resource, out[j].id)
	})
	return out, nil
}
func (r *Runtime) removalSessionCurrent(epoch uint64, session string) error {
	r.ownerMu.Lock()
	same := r.ownerEpoch == epoch
	r.ownerMu.Unlock()
	e := r.terminal.Snapshot()
	if !same || r.closed.Load() || r.ownerClosed.Load() || r.poisoned || r.taken || r.sessionID != session || e.Closed || !e.OwnerQueryAvailable || e.Owner != terminal.OwnerNone || !time.Now().Before(r.sessionExpiresAt) {
		return fail("rule-removal-session-lost")
	}
	return nil
}
func (r *Runtime) removalObservation(ctx context.Context, reader presenceReader, safe safeModeReader, batchID, id string, present bool, protected []removalProtected, epoch uint64, session string) (control.RuleRemovalObservation, error) {
	out := control.RuleRemovalObservation{BatchID: batchID, Target: r.target, Resource: api.FilterRule, ID: strings.ToUpper(id), Present: present, Protected: []control.ObjectObservation{}}
	var checksum string
	for pass := 0; pass < 2; pass++ {
		read := r.readRemovalObject
		if present {
			read = r.readRemovalExistingObject
		}
		row, exists, err := read(ctx, reader, api.FilterRule, id)
		if err != nil || exists != present {
			return out, fail("rule-removal-presence-unverified")
		}
		if present {
			value, err := control.ObjectAttributesChecksum(api.FilterRule, row)
			if err != nil || pass > 0 && value != checksum {
				return out, fail("rule-removal-object-unstable")
			}
			checksum = value
			out.Attributes = row
		}
	}
	for _, target := range protected {
		row, exists, err := r.readRemovalExistingObject(ctx, reader, target.resource, target.id)
		if err != nil || !exists {
			return out, fail("rule-removal-protected-object-missing")
		}
		digest, err := control.ObjectAttributesChecksum(target.resource, row)
		if err != nil || digest != target.checksum {
			return out, fail("rule-removal-protected-object-changed")
		}
		out.Protected = append(out.Protected, control.ObjectObservation{Target: r.target, Resource: target.resource, ID: target.id, Attributes: row, ObservedAt: time.Now().UTC(), CrossChannelIdentityVerified: true})
	}
	state, err := safe.ReadSafeMode(ctx)
	if err != nil || state.Enabled || state.Current {
		return out, fail("rule-removal-owner-not-idle")
	}
	if err = r.removalSessionCurrent(epoch, session); err != nil {
		return out, err
	}
	out.Owner = control.Owner{SessionID: session, Known: true, Alive: true, Active: false, ThisSession: false}
	out.ObservedAt = time.Now().UTC()
	return out, nil
}

// A successful direct rule read still returns the actual complete selected row.
// Failure is never reclassified as absence. Other menus keep their original
// query protocol; only filter-rule direct selection has the native proof.
func (r *Runtime) readRemovalExistingObject(ctx context.Context, reader presenceReader, resource api.Resource, id string) (api.Record, bool, error) {
	if direct, ok := reader.(interface {
		ReadObject(context.Context, api.Resource, string) (api.Record, error)
	}); ok && resource == api.FilterRule {
		row, err := direct.ReadObject(ctx, resource, id)
		if err != nil {
			return nil, false, err
		}
		return r.normalizeRemovalObject(ctx, resource, id, api.ObjectPresence{Present: true, Record: row})
	}
	return r.readRemovalObject(ctx, reader, resource, id)
}

func (r *Runtime) readRemovalObject(ctx context.Context, reader presenceReader, resource api.Resource, id string) (api.Record, bool, error) {
	result, err := reader.ReadObjectPresence(ctx, resource, id)
	if err != nil {
		return nil, false, err
	}
	return r.normalizeRemovalObject(ctx, resource, id, result)
}

func (r *Runtime) normalizeRemovalObject(ctx context.Context, resource api.Resource, id string, result api.ObjectPresence) (api.Record, bool, error) {
	if !result.Present {
		if result.Record != nil {
			return nil, false, fail("rule-removal-absence-invalid")
		}
		return nil, false, nil
	}
	if result.Record == nil || !strings.EqualFold(result.Record[".id"], id) {
		return nil, false, fail("rule-removal-object-invalid")
	}
	queries := []api.PropertyRequest{}
	for _, field := range observedFields[resource] {
		value, present := result.Record[field]
		presence := resource == api.RoutingTable && (field == "fib" || field == "dynamic") || resource == api.BGPConnection && field == "dynamic"
		if present && !(resource == api.BGPConnection && (field == "as" || field == "output.default-prepend")) && (!presence || value != "") {
			continue
		}
		queries = append(queries, api.PropertyRequest{Resource: resource, ID: id, Property: field})
	}
	if len(queries) > r.config.MaxGetterCalls || len(queries) > api.MaxPropertyBatch {
		return nil, false, fail("rule-removal-getter-bound")
	}
	values := make([]api.PropertyValue, len(queries))
	var err error
	if len(queries) > 0 {
		if batch, ok := r.api.(propertyBatchReader); ok {
			values, err = batch.ReadProperties(ctx, queries)
		} else {
			for i, q := range queries {
				values[i], err = r.api.ReadProperty(ctx, q.Resource, q.ID, q.Property)
				if err != nil {
					break
				}
			}
		}
		if err != nil || len(values) != len(queries) {
			return nil, false, fail("rule-removal-getter-failed")
		}
	}
	properties := make([]routeros.VersionPropertyEvidence, len(queries))
	for i, q := range queries {
		properties[i] = routeros.VersionPropertyEvidence{Resource: resource, ID: q.ID, Field: q.Property, Completed: true, Property: values[i]}
	}
	row, err := routeros.NormalizeObservedObject(resource, result.Record, r.target, properties)
	if err != nil {
		return nil, false, fail("rule-removal-properties-unverified")
	}
	for _, field := range observedFields[resource] {
		if _, present := row[field]; !present {
			return nil, false, fail("rule-removal-properties-incomplete")
		}
	}
	return row, true, nil
}
