package live

import (
	"strings"

	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
)

// This is an expected delta, not observed absence. It is private and prevents
// the adapter from accepting another mutation until an actual two-pass Observe
// proves all acknowledged removals and every remaining non-secret object.
// The supervisor separately proves credentials and its authenticated journal.
type pendingRemovalTail struct {
	digest, session string
	ownerEpoch      uint64
	mode            control.RuleRemovalMode
}

func (r *Runtime) expectRemovalTail(before api.Inventory, ids []string, mode control.RuleRemovalMode, session string, epoch uint64) error {
	removed := map[string]bool{}
	for _, id := range ids {
		id = strings.ToUpper(id)
		if removed[id] {
			return fail("rule-removal-tail-invalid")
		}
		removed[id] = true
	}
	expected := before
	expected.FilterRules = []api.Record{}
	for _, row := range before.FilterRules {
		id := strings.ToUpper(row[".id"])
		if removed[id] {
			delete(removed, id)
		} else {
			expected.FilterRules = append(expected.FilterRules, row)
		}
	}
	if len(removed) != 0 {
		return fail("rule-removal-tail-invalid")
	}
	digest, err := stateDigest(expected, r.target)
	if err != nil {
		return err
	}
	r.removalTail = &pendingRemovalTail{digest: digest, session: session, ownerEpoch: epoch, mode: mode}
	return nil
}

func (r *Runtime) verifyRemovalTail(digest string, owner control.Owner) error {
	pending := r.removalTail
	if pending == nil {
		return nil
	}
	if digest != pending.digest || !owner.Known || !owner.Alive || owner.Active || owner.ThisSession || !owner.HistoryKnown || owner.HistoryActions != 0 || owner.SessionID != pending.session || r.removalSessionCurrent(pending.ownerEpoch, pending.session) != nil {
		r.poisoned = true
		return fail("rule-removal-tail-unverified")
	}
	r.removalTail = nil
	if pending.mode == control.RuleRemovalCleanup && r.cleanupFinished() {
		r.completed = true
	}
	return nil
}
