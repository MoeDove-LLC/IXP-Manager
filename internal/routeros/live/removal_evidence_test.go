package live

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routeros/terminal"
)

func removalEvidenceFixture(t *testing.T, mode control.RuleRemovalMode) (*removalBatchFixture, control.ReadRequest, control.Observation) {
	t.Helper()
	f := newRemovalBatchFixture(t, mode)
	// This adapter-only model proves non-secret observations. It does not
	// simulate a supervisor journal or turn model credentials into authority.
	request := control.ReadRequest{KnownState: routeros.KnownState{Target: f.r.target}}
	before := f.api.base.calls
	observed, err := f.r.Observe(context.Background(), request)
	if err != nil || f.api.base.calls != before+2 || f.r.removalEvidence.Load() == nil {
		t.Fatal("successful two-pass Observe did not retain private evidence", err)
	}
	return f, request, observed
}

func TestRemovalEvidenceAvoidsOnlyRedundantHeadAndCannotBeReused(t *testing.T) {
	for _, mode := range []control.RuleRemovalMode{control.RuleRemovalCleanup, control.RuleRemovalPreparedDiscard} {
		t.Run(string(mode), func(t *testing.T) {
			f, _, observed := removalEvidenceFixture(t, mode)
			observed.Inventory.Resources[0]["version"] = "caller-mutated"
			observed.Inventory.Capabilities[api.FilterRule] = api.Unsupported
			observed.Inventory.SafeMode.Enabled = true
			observed.Inventory.FilterRules[0]["rule"] = "caller-mutated"
			reads := f.api.base.calls
			batch := removalBatchPrefix(f.batch, 2)
			recorder := &removalBatchRecorderModel{}
			if err := f.r.ApplyRuleRemovalBatch(context.Background(), batch, recorder); err != nil {
				t.Fatal(err)
			}
			if f.api.base.calls != reads || f.api.base.writes-f.initialWrites != 2 || f.r.removalEvidence.Load() != nil || len(recorder.absent) != 2 {
				t.Fatal("cached head skipped a durable item or survived the attempt")
			}
			for _, operation := range batch.Operations {
				if f.api.reads[cleanupKey(api.FilterRule, operation.Request.ID)] != 4 {
					t.Fatal("current existence/absence reads were replaced with cached data")
				}
			}
			next := f.batch
			next.Operations = next.Operations[2:4]
			reads = f.api.base.calls
			if err := f.r.ApplyRuleRemovalBatch(context.Background(), next, &removalBatchRecorderModel{}); err != nil || f.api.base.calls != reads+1 {
				t.Fatal("next batch reused earlier full evidence", err)
			}
		})
	}
}

func TestRemovalEvidenceStaleOrChangedBindingFallsBackToFullRead(t *testing.T) {
	for _, change := range []string{"expired", "future", "epoch", "owner-epoch", "session", "target", "mode", "plan", "fingerprint", "cursor", "missing"} {
		t.Run(change, func(t *testing.T) {
			f, _, _ := removalEvidenceFixture(t, control.RuleRemovalPreparedDiscard)
			evidence := *f.r.removalEvidence.Load()
			switch change {
			case "expired":
				f.r.config.OperationTimeoutSeconds = 30
				evidence.completedAt = time.Now().Add(-2 * time.Second)
			case "future":
				evidence.completedAt = time.Now().Add(time.Second)
			case "epoch":
				evidence.epoch--
			case "owner-epoch":
				evidence.ownerEpoch++
			case "session":
				evidence.session = "another-session"
			case "target":
				evidence.target.DeviceID = "other-device"
			case "mode":
				evidence.binding.mode = control.RuleRemovalCleanup
			case "plan":
				evidence.binding.plan = strings.Repeat("f", 64)
			case "fingerprint":
				evidence.binding.fingerprint = strings.Repeat("f", 64)
			case "cursor":
				evidence.binding.index++
			}
			if change == "missing" {
				f.r.removalEvidence.Store(nil)
			} else {
				f.r.removalEvidence.Store(&evidence)
			}
			reads := f.api.base.calls
			if err := f.r.ApplyRuleRemovalBatch(context.Background(), removalBatchPrefix(f.batch, 2), &removalBatchRecorderModel{}); err != nil || f.api.base.calls != reads+1 || f.api.base.writes-f.initialWrites != 2 || f.r.removalEvidence.Load() != nil {
				t.Fatal("mismatched evidence did not use the original full-read path", err)
			}
		})
	}
}

func TestRemovalEvidenceDoesNotReplaceLiveObjectAndOwnerChecks(t *testing.T) {
	for _, change := range []string{"target", "protected", "owner", "closed", "expired-session", "wrong-operation", "gate"} {
		t.Run(change, func(t *testing.T) {
			f, _, _ := removalEvidenceFixture(t, control.RuleRemovalPreparedDiscard)
			batch := removalBatchPrefix(f.batch, 2)
			switch change {
			case "target":
				row := removalBatchActualRow(f.api.base, api.FilterRule, batch.Operations[0].Request.ID)
				changed := "if (dst == 192.0.2.255/32) { accept; } else { reject; }"
				if row["rule"] == changed {
					t.Fatal("fixture mutation must change the rule")
				}
				row["rule"] = changed
			case "protected":
				f.api.base.inv.Instances[0]["router-id"] = "192.0.2.99"
			case "owner":
				f.api.safeFault = true
			case "closed":
				_ = f.r.CloseOwner()
			case "expired-session":
				f.r.sessionExpiresAt = time.Now()
			case "wrong-operation":
				batch.Operations[0].OperationID = "not-authorized"
			case "gate":
				f.r.profile.RuleRemovalBatchVerified = false
			}
			if err := f.r.ApplyRuleRemovalBatch(context.Background(), batch, &removalBatchRecorderModel{}); err == nil || f.api.base.writes != f.initialWrites || f.r.removalEvidence.Load() != nil {
				t.Fatal("cached full read authorized a changed object/owner/binding")
			}
		})
	}
}

func TestRemovalEvidenceOnlyFullObserveCanCreateAndFailuresInvalidate(t *testing.T) {
	for _, change := range []string{"read-state", "failed-observe", "late-close", "late-publish", "failed-bind", "ordinary-apply"} {
		t.Run(change, func(t *testing.T) {
			f, request, observed := removalEvidenceFixture(t, control.RuleRemovalPreparedDiscard)
			prior := f.r.removalEvidence.Load()
			switch change {
			case "read-state":
				f.r.invalidateWriteEvidence()
				if _, _, _, _, err := f.r.readState(context.Background()); err != nil {
					t.Fatal(err)
				}
			case "failed-observe":
				f.api.base.inv.Resources[0]["version"] = "changed"
				if _, err := f.r.Observe(context.Background(), request); err == nil {
					t.Fatal("invalid observation succeeded")
				}
			case "late-close":
				last := f.api.base.calls + 2
				f.api.base.change = func(n int, _ *api.Inventory) {
					if n == last {
						_ = f.r.CloseOwner()
					}
				}
				_, _ = f.r.Observe(context.Background(), request)
			case "late-publish":
				f.r.invalidateWriteEvidence()
				f.r.rememberRemovalEvidence(prior.raw, prior.evidence, observed.Owner, prior.epoch)
			case "failed-bind":
				if f.r.BindPlan(routeros.Plan{}, nil) == nil {
					t.Fatal("unrelated plan bound during discard")
				}
			case "ordinary-apply":
				if _, err := f.r.Apply(context.Background(), f.batch.Operations[0]); err == nil {
					t.Fatal("ordinary write accepted discard")
				}
			}
			if f.r.removalEvidence.Load() != nil {
				t.Fatal("failed or incomplete observation retained evidence")
			}
		})
	}
}

func TestRemovalEvidenceDoesNotCrossObservedOwnerOrHistoryChange(t *testing.T) {
	for _, change := range []string{"other-owner", "floating-history", "still-idle"} {
		t.Run(change, func(t *testing.T) {
			f, _, _ := removalEvidenceFixture(t, control.RuleRemovalPreparedDiscard)
			switch change {
			case "other-owner":
				f.term.evidence.Owner = terminal.OwnerOther
				f.api.base.inv.SafeMode.Enabled = true
			case "floating-history":
				f.term.history = 1
			}
			owner, err := f.r.Owner(context.Background())
			if err != nil || !owner.Known || !owner.Alive || !owner.HistoryKnown {
				t.Fatal("fixture must produce a completed known owner observation", err)
			}
			if change == "still-idle" {
				if f.r.removalEvidence.Load() == nil {
					t.Fatal("unchanged idle owner discarded valid evidence")
				}
				return
			}
			if f.r.removalEvidence.Load() != nil {
				t.Fatal("known intervening owner/history change retained old evidence")
			}
			f.term.evidence.Owner = terminal.OwnerNone
			f.term.history = 0
			f.api.base.inv.SafeMode.Enabled = false
			chain := removalBatchActualRow(f.api.base, api.FilterRule, f.batch.Operations[0].Request.ID)["chain"]
			foreign := cleanupModelDefaults(api.FilterRule)
			foreign[".id"], foreign["chain"], foreign["comment"], foreign["rule"] = "*F1234", "foreign_chain", "", "jump "+chain+";"
			f.api.base.inv.FilterRules = append(f.api.base.inv.FilterRules, foreign)
			reads := f.api.base.calls
			if err := f.r.ApplyRuleRemovalBatch(context.Background(), removalBatchPrefix(f.batch, 2), &removalBatchRecorderModel{}); err == nil || f.api.base.calls != reads+1 || f.api.base.writes != f.initialWrites {
				t.Fatal("restored idle state reused pre-change proof and missed a foreign reference", err)
			}
		})
	}
}
