package live

import (
	"context"
	"testing"

	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
)

type nativeGlobalTailDiscardAPI struct{ *nativeDiscardOnlyAPI }

func (a *nativeGlobalTailDiscardAPI) ReadObject(ctx context.Context, resource api.Resource, id string) (api.Record, error) {
	reader, ok := a.apiChannel.(objectReader)
	if !ok {
		return nil, fail("native-global-tail-object-reader-unavailable")
	}
	return reader.ReadObject(ctx, resource, id)
}
func (a *nativeGlobalTailDiscardAPI) ReadObjectPresence(ctx context.Context, resource api.Resource, id string) (api.ObjectPresence, error) {
	reader, ok := a.apiChannel.(presenceReader)
	if !ok {
		return api.ObjectPresence{}, fail("native-global-tail-presence-reader-unavailable")
	}
	return reader.ReadObjectPresence(ctx, resource, id)
}

type nativeGlobalTailDiscardRuntime struct {
	*nativeDiscardOnlyRuntime
	batches, ruleACKs, tails, pending, absent int
}

func (r *nativeGlobalTailDiscardRuntime) Observe(ctx context.Context, request control.ReadRequest) (control.Observation, error) {
	pending := r.runtime.removalTail != nil
	observation, err := r.nativeDiscardOnlyRuntime.Observe(ctx, request)
	if pending && err == nil && r.runtime.removalTail == nil {
		r.tails++
	}
	return observation, err
}
func (r *nativeGlobalTailDiscardRuntime) ApplyRuleRemovalBatch(ctx context.Context, batch control.RuleRemovalBatch, recorder control.RuleRemovalRecorder) error {
	if batch.Mode != control.RuleRemovalPreparedDiscard || batch.ProofMode != control.RuleRemovalProofGlobalTail {
		return r.counts.deny()
	}
	r.batches++
	before := r.ruleACKs
	err := r.runtime.ApplyRuleRemovalBatch(ctx, batch, &nativeGlobalTailRecorder{runtime: r, next: recorder})
	if err == nil && r.ruleACKs > before && r.runtime.removalTail != nil {
		r.pending++
	}
	r.counts.remember(err)
	return err
}

type nativeGlobalTailRecorder struct {
	runtime *nativeGlobalTailDiscardRuntime
	next    control.RuleRemovalRecorder
}

func (r *nativeGlobalTailRecorder) Before(id string, o control.RuleRemovalObservation) error {
	return r.next.Before(id, o)
}
func (r *nativeGlobalTailRecorder) Result(id string, result api.Result, err error) error {
	if failure := r.next.Result(id, result, err); failure != nil {
		return failure
	}
	if err == nil && result.Acknowledged && result.ID == "" {
		r.runtime.ruleACKs++
	}
	return nil
}
func (r *nativeGlobalTailRecorder) Absent(string, control.RuleRemovalObservation) error {
	r.runtime.absent++
	return r.runtime.counts.deny()
}
func (r *nativeGlobalTailDiscardRuntime) prove(t *testing.T) {
	t.Helper()
	if r.batches < 1 || r.ruleACKs != 6 || r.tails != r.batches || r.pending != r.batches || r.absent != 0 || r.runtime.removalTail != nil || CapabilityProfile().RuleRemovalGlobalTailVerified {
		t.Fatal("global_tail_complete=false")
	}
	t.Logf("global_tail_batches=%d global_tail_rule_acks=%d global_tail_verified_full_reads=%d global_tail_pending_guards=%d global_tail_absent_callbacks=%d global_tail_complete=true global_tail_gate_unchanged=true global_tail_large_scale_verified=false", r.batches, r.ruleACKs, r.tails, r.pending, r.absent)
}

func TestNativeCHRLivePreparedDiscardGlobalTailCycle(t *testing.T) { nativeDiscardCycle(t, true) }

var _ control.RuleRemovalBatchRuntime = (*nativeGlobalTailDiscardRuntime)(nil)

func TestNativeGlobalTailFixtureRejectsPerObjectAbsenceAndScope(t *testing.T) {
	counts := &nativeDiscardCounters{}
	runtime := &nativeGlobalTailDiscardRuntime{nativeDiscardOnlyRuntime: &nativeDiscardOnlyRuntime{counts: counts}}
	recorder := &nativeGlobalTailRecorder{runtime: runtime}
	if recorder.Absent("unexpected", control.RuleRemovalObservation{}) == nil || runtime.absent != 1 {
		t.Fatal("global fixture accepted a fabricated per-object absence")
	}
	if runtime.ApplyRuleRemovalBatch(context.Background(), control.RuleRemovalBatch{Mode: control.RuleRemovalPreparedDiscard, ProofMode: control.RuleRemovalProofPerObject}, nil) == nil || runtime.batches != 0 {
		t.Fatal("fixture accepted another proof protocol")
	}
}
