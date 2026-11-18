package live

import (
	"context"
	"reflect"
	"strings"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
)

var _ control.RuleBatchRuntime = (*Runtime)(nil)

type safeModeReader interface {
	ReadSafeMode(context.Context) (api.SafeModeState, error)
}

func generationChains(p *routeros.Plan) []string {
	base := p.Owner + "_g" + p.Generation
	return []string{base + "_in", base + "_out", base + "_src"}
}

func generationRule(p *routeros.Plan, operation routeros.Operation) bool {
	if operation.Phase != routeros.Prepare || operation.Kind != routeros.AddRule || operation.Rule == nil || !strings.HasPrefix(operation.Rule.Comment, p.Owner+":g:"+p.Generation+":") {
		return false
	}
	for _, chain := range generationChains(p) {
		if operation.Rule.Chain == chain {
			return true
		}
	}
	return false
}

// ApplyRuleBatch removes repeated full-inventory reads only for a complete new
// generation of unreferenced rules. It cannot update roots, change a connection,
// acquire Safe Mode or contain a password. The supervisor records each intent
// and result, then independently proves the exact complete group delta before
// its trusted current state advances. Exclusive device access remains required.
func (r *Runtime) ApplyRuleBatch(ctx context.Context, batch control.RuleBatch, recorder control.RuleBatchRecorder) error {
	r.invalidateObjectActivation()
	defer r.invalidateObjectActivation()
	r.invalidateWriteEvidence()
	ctx, done, e := r.begin(ctx)
	if e != nil {
		return e
	}
	defer done()
	defer r.invalidateWriteEvidence()
	if !r.profile.PreparationVerified || !r.profile.RuleBatchVerified {
		return fail("native-rule-batch-profile-unverified")
	}
	if r.poisoned || r.taken || r.completed || r.bridge == nil || r.boundPlan == nil || recorder == nil {
		return fail("rule-batch-not-bound")
	}
	if len(batch.ID) < 1 || len(batch.ID) > 128 || strings.IndexFunc(batch.ID, func(v rune) bool {
		return !(v >= 'a' && v <= 'z' || v >= 'A' && v <= 'Z' || v >= '0' && v <= '9' || v == '_' || v == '-')
	}) >= 0 || batch.PlanChecksum != r.boundPlan.Checksum || batch.Generation != r.boundPlan.Generation || len(batch.Operations) == 0 || len(batch.Operations) > routeros.MaxPrepareOperations {
		return fail("rule-batch-invalid")
	}
	start := r.operationIndex
	end := start
	for end < len(r.boundPlan.Operations) && generationRule(r.boundPlan, r.boundPlan.Operations[end]) {
		end++
	}
	if end-start != len(batch.Operations) || start > 0 && generationRule(r.boundPlan, r.boundPlan.Operations[start-1]) {
		return fail("rule-batch-not-complete-generation")
	}
	reader, ok := r.api.(safeModeReader)
	if !ok {
		return fail("rule-batch-owner-reader-unavailable")
	}
	owner, e := r.owner(ctx)
	if e != nil || !owner.Known || !owner.Alive || owner.Active || !owner.HistoryKnown || owner.HistoryActions != 0 {
		return fail("rule-batch-owner-not-idle")
	}
	raw, evidence, _, _, e := r.readState(ctx)
	if e != nil {
		return e
	}
	chains := generationChains(r.boundPlan)
	for _, menu := range menus(raw) {
		for _, row := range menu.rows {
			for _, value := range row {
				for _, chain := range chains {
					if strings.Contains(value, chain) {
						return fail("rule-batch-generation-already-present-or-referenced")
					}
				}
			}
		}
	}
	resolver, e := routeros.NewAPIResolver(raw, r.target, evidence)
	if e != nil {
		return fail("rule-batch-reference-unverified")
	}
	expected := make([]routeros.APIMutation, len(batch.Operations))
	for i, supplied := range batch.Operations {
		op := r.operations[start+i]
		if op.ID != supplied.OperationID || supplied.Phase != routeros.Prepare || supplied.Request.Resource != api.FilterRule || supplied.Request.Action != api.Add {
			return fail("rule-batch-operation-mismatch")
		}
		expected[i], e = r.bridge.Mutation(op.ID, resolver)
		if e != nil || !reflect.DeepEqual(expected[i], supplied) {
			return fail("rule-batch-does-not-match-plan")
		}
	}
	ids := map[string]bool{}
	for _, row := range raw.FilterRules {
		ids[strings.ToUpper(row[".id"])] = true
	}
	for _, mutation := range expected {
		if ctx.Err() != nil || r.closed.Load() || r.ownerClosed.Load() || r.terminal.Snapshot().Closed {
			return fail("rule-batch-channel-ended")
		}
		// This inexpensive fixed read is done for every individual write. The
		// full authenticated snapshot and namespace closure were checked above.
		safe, e := reader.ReadSafeMode(ctx)
		if e != nil || safe.Enabled || safe.Current {
			return fail("rule-batch-owner-changed")
		}
		if e = recorder.Before(mutation.OperationID); e != nil {
			return fail("rule-batch-intent-not-durable")
		}
		result, writeErr := r.api.Apply(ctx, mutation.Request)
		if writeErr == nil && (!result.Acknowledged || !nativeID.MatchString(result.ID) || ids[strings.ToUpper(result.ID)]) {
			result = api.Result{}
			writeErr = &Failure{Code: "rule-batch-receipt-invalid", Uncertain: true}
		}
		if writeErr != nil {
			r.poisoned = true
		}
		if e = recorder.After(mutation.OperationID, result, writeErr); e != nil {
			r.poisoned = true
			return &Failure{Code: "rule-batch-result-not-durable", Uncertain: true}
		}
		if writeErr != nil {
			return &Failure{Code: "rule-batch-write-failed", Uncertain: true}
		}
		ids[strings.ToUpper(result.ID)] = true
		r.operationIndex++
	}
	return nil
}
