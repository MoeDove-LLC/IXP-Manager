package live

import (
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
)

// observedWriteEvidence is created only after a successful full Observe. It is
// private, immutable, non-secret and never supplied by the controller/caller.
type observedWriteEvidence struct {
	raw            api.Inventory
	evidence       routeros.TargetEvidence
	target         routeros.Target
	planChecksum   string
	operationIndex int
	sessionID      string
	taken          bool
	completedAt    time.Time
	epoch          uint64
}

func (r *Runtime) invalidateWriteEvidence() {
	r.writeEvidenceEpoch.Add(1)
	r.writeEvidence.Store(nil)
	r.removalEvidence.Store(nil)
}

func (r *Runtime) rememberWriteEvidence(raw api.Inventory, evidence routeros.TargetEvidence, epoch uint64) {
	if r.boundPlan == nil || r.bridge == nil || r.operationIndex >= len(r.operations) || r.completed || r.poisoned || r.closed.Load() || r.ownerClosed.Load() || r.writeEvidenceEpoch.Load() != epoch || r.config.OperationTimeoutSeconds <= 0 {
		return
	}
	raw = cloneObservedInventory(raw)
	evidence = cloneTargetEvidence(evidence)
	value := &observedWriteEvidence{raw: raw, evidence: evidence, target: r.target, planChecksum: r.boundPlan.Checksum, operationIndex: r.operationIndex, sessionID: r.sessionID, taken: r.taken, completedAt: time.Now(), epoch: epoch}
	r.writeEvidence.Store(value)
	// Close may have invalidated the slot while the immutable copy was built.
	if r.writeEvidenceEpoch.Load() != epoch || r.closed.Load() || r.ownerClosed.Load() {
		r.writeEvidence.CompareAndSwap(value, nil)
	}
}

func cloneTargetEvidence(evidence routeros.TargetEvidence) routeros.TargetEvidence {
	evidence.Properties = append([]routeros.VersionPropertyEvidence(nil), evidence.Properties...)
	if selection := evidence.FilterRuleDisabledSelection; selection != nil {
		copy := *selection
		copy.FalseIDs = append([]string{}, selection.FalseIDs...)
		copy.TrueIDs = append([]string{}, selection.TrueIDs...)
		evidence.FilterRuleDisabledSelection = &copy
	}
	return evidence
}

func cloneObservedInventory(raw api.Inventory) api.Inventory {
	copyRows := func(rows []api.Record) []api.Record {
		out := make([]api.Record, len(rows))
		for i, row := range rows {
			out[i] = api.Record{}
			for k, v := range row {
				out[i][k] = v
			}
		}
		return out
	}
	raw.Resources = copyRows(raw.Resources)
	raw.Licenses = copyRows(raw.Licenses)
	raw.RouterBoards = copyRows(raw.RouterBoards)
	raw.Identities = copyRows(raw.Identities)
	raw.Tables = copyRows(raw.Tables)
	raw.Instances = copyRows(raw.Instances)
	raw.Connections = copyRows(raw.Connections)
	raw.FilterRules = copyRows(raw.FilterRules)
	capabilities := map[api.Resource]api.Capability{}
	for k, v := range raw.Capabilities {
		capabilities[k] = v
	}
	raw.Capabilities = capabilities
	if raw.SafeMode != nil {
		safe := *raw.SafeMode
		raw.SafeMode = &safe
	}
	return raw
}

// detachWriteEvidence always consumes the slot, including for failed/incorrect
// write attempts. The increment also invalidates an Observe racing with Close.
func (r *Runtime) detachWriteEvidence() *observedWriteEvidence {
	evidence := r.writeEvidence.Swap(nil)
	r.removalEvidence.Store(nil)
	r.writeEvidenceEpoch.Add(1)
	return evidence
}

func (r *Runtime) usableWriteEvidence(e *observedWriteEvidence) bool {
	if e == nil || r.boundPlan == nil || r.bridge == nil || e.epoch+1 != r.writeEvidenceEpoch.Load() || r.closed.Load() || r.ownerClosed.Load() || r.poisoned || r.completed || e.target != r.target || e.planChecksum != r.boundPlan.Checksum || e.operationIndex != r.operationIndex || e.sessionID != r.sessionID || e.taken != r.taken || r.config.OperationTimeoutSeconds <= 0 {
		return false
	}
	age := time.Since(e.completedAt)
	limit := time.Duration(r.config.OperationTimeoutSeconds) * time.Second
	if limit > time.Second {
		limit = time.Second
	}
	return age >= 0 && age <= limit
}
