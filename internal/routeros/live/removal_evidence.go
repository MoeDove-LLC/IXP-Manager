package live

import (
	"context"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
)

type removalEvidenceBinding struct {
	mode        control.RuleRemovalMode
	plan        string
	fingerprint string
	index       int
}

// Only successful full Observe calls create this private, non-secret copy.
// It replaces one redundant adapter scan immediately after the controller's
// two-pass global head. It is never supplied by a caller, never survives a
// write attempt and never replaces current owner or per-object observations.
type observedRemovalEvidence struct {
	raw         api.Inventory
	evidence    routeros.TargetEvidence
	target      routeros.Target
	binding     removalEvidenceBinding
	session     string
	epoch       uint64
	ownerEpoch  uint64
	completedAt time.Time
}

func (r *Runtime) removalEvidenceBinding() (removalEvidenceBinding, bool) {
	if !r.profile.RuleRemovalBatchVerified || r.taken || r.poisoned {
		return removalEvidenceBinding{}, false
	}
	if p := r.preparedDiscard; p != nil && r.profile.PreparedDiscardVerified && !r.preparedDiscardFinished() && r.cleanup == nil && r.boundPlan == nil && r.bridge == nil {
		return removalEvidenceBinding{control.RuleRemovalPreparedDiscard, p.binding.Plan.Checksum, p.fingerprint, p.next}, true
	}
	if p := r.cleanup; p != nil && r.profile.CleanupVerified && !r.cleanupFinished() && r.preparedDiscard == nil {
		return removalEvidenceBinding{control.RuleRemovalCleanup, p.plan.Checksum, "", p.next}, true
	}
	return removalEvidenceBinding{}, false
}

func (r *Runtime) rememberRemovalEvidence(raw api.Inventory, evidence routeros.TargetEvidence, owner control.Owner, epoch uint64) {
	binding, ok := r.removalEvidenceBinding()
	if !ok || !owner.Known || !owner.Alive || owner.Active || owner.ThisSession || !owner.HistoryKnown || owner.HistoryActions != 0 || owner.SessionID != r.sessionID || r.closed.Load() || r.ownerClosed.Load() || r.writeEvidenceEpoch.Load() != epoch || r.config.OperationTimeoutSeconds <= 0 {
		return
	}
	r.ownerMu.Lock()
	ownerEpoch := r.ownerEpoch
	r.ownerMu.Unlock()
	raw = cloneObservedInventory(raw)
	evidence = cloneTargetEvidence(evidence)
	value := &observedRemovalEvidence{raw: raw, evidence: evidence, target: r.target, binding: binding, session: r.sessionID, epoch: epoch, ownerEpoch: ownerEpoch, completedAt: time.Now()}
	r.removalEvidence.Store(value)
	if r.writeEvidenceEpoch.Load() != epoch || r.closed.Load() || r.ownerClosed.Load() {
		r.removalEvidence.CompareAndSwap(value, nil)
	}
}

func (r *Runtime) detachRemovalEvidence() *observedRemovalEvidence {
	value := r.removalEvidence.Swap(nil)
	r.writeEvidence.Store(nil)
	r.writeEvidenceEpoch.Add(1)
	return value
}

func (r *Runtime) usableRemovalEvidence(value *observedRemovalEvidence) bool {
	binding, ok := r.removalEvidenceBinding()
	if value == nil || !ok || value.binding != binding || value.target != r.target || value.epoch+1 != r.writeEvidenceEpoch.Load() || r.config.OperationTimeoutSeconds <= 0 || r.removalSessionCurrent(value.ownerEpoch, value.session) != nil {
		return false
	}
	age := time.Since(value.completedAt)
	limit := time.Duration(r.config.OperationTimeoutSeconds) * time.Second
	if limit > time.Second {
		limit = time.Second
	}
	return age >= 0 && age <= limit
}

func (r *Runtime) readRemovalState(ctx context.Context, value *observedRemovalEvidence) (api.Inventory, routeros.TargetEvidence, api.Inventory, string, error) {
	if !r.usableRemovalEvidence(value) {
		return r.readState(ctx)
	}
	normalized, err := routeros.NormalizeObservedInventory(value.raw, r.target, value.evidence)
	if err != nil {
		return api.Inventory{}, routeros.TargetEvidence{}, api.Inventory{}, "", err
	}
	digest, err := stateDigest(normalized, r.target)
	return value.raw, value.evidence, normalized, digest, err
}
