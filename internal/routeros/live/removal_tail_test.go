package live

import (
	"context"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routeros/terminal"
)

func globalTailFixture(t *testing.T, mode control.RuleRemovalMode) *removalBatchFixture {
	t.Helper()
	f := newRemovalBatchFixture(t, mode)
	f.r.profile.RuleRemovalGlobalTailVerified = true
	f.batch.ProofMode = control.RuleRemovalProofGlobalTail
	return f
}

func TestGlobalTailAdapterRequiresFullObservedDeltaBeforeNextWrite(t *testing.T) {
	for _, mode := range []control.RuleRemovalMode{control.RuleRemovalCleanup, control.RuleRemovalPreparedDiscard} {
		t.Run(string(mode), func(t *testing.T) {
			f := globalTailFixture(t, mode)
			batch := removalBatchPrefix(f.batch, 2)
			recorder := &removalBatchRecorderModel{}
			if err := f.r.ApplyRuleRemovalBatch(context.Background(), batch, recorder); err != nil {
				t.Fatal(err)
			}
			if len(recorder.before) != 2 || len(recorder.result) != 2 || len(recorder.absent) != 0 || f.r.removalTail == nil || f.index() != f.initialIndex+2 {
				t.Fatal("ACK manufactured absence or lost pending full proof")
			}
			for _, operation := range batch.Operations {
				if f.api.reads[cleanupKey(api.FilterRule, operation.Request.ID)] != 2 {
					t.Fatal("pre-write target proof changed")
				}
			}
			next := f.batch
			next.Operations = next.Operations[2:4]
			reads, writes := f.api.base.calls, f.api.base.writes
			if err := f.r.ApplyRuleRemovalBatch(context.Background(), next, &removalBatchRecorderModel{}); err == nil || f.api.base.writes != writes || f.api.base.calls != reads {
				t.Fatal("next batch started before observed full tail")
			}
			// This adapter model covers non-secret state only; control tests prove
			// the credential MAC and durable cursor independently.
			if _, err := f.r.Observe(context.Background(), control.ReadRequest{KnownState: routeros.KnownState{Target: f.r.target}}); err != nil {
				t.Fatal(err)
			}
			if f.r.removalTail != nil || f.api.base.calls != reads+2 {
				t.Fatal("tail was not a successful two-pass full observation")
			}
			if err := f.r.ApplyRuleRemovalBatch(context.Background(), next, &removalBatchRecorderModel{}); err != nil {
				t.Fatal("verified tail did not permit the next original prefix", err)
			}
		})
	}
}

func TestGlobalTailAdapterNeverAcceptsACKAsDeletionOrIgnoresDrift(t *testing.T) {
	for _, change := range []string{"ack-no-effect", "protected-change", "foreign-rule", "owner-lost", "expired-session", "owner-other-then-none"} {
		t.Run(change, func(t *testing.T) {
			f := globalTailFixture(t, control.RuleRemovalPreparedDiscard)
			if change == "ack-no-effect" {
				f.api.keepTarget = true
			}
			if err := f.r.ApplyRuleRemovalBatch(context.Background(), removalBatchPrefix(f.batch, 2), &removalBatchRecorderModel{}); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "protected-change":
				f.api.base.inv.Instances[0]["router-id"] = "192.0.2.99"
			case "foreign-rule":
				row := cleanupModelDefaults(api.FilterRule)
				row[".id"], row["chain"], row["rule"], row["comment"] = "*F1234", "foreign_chain", "reject;", ""
				f.api.base.inv.FilterRules = append(f.api.base.inv.FilterRules, row)
			case "owner-lost":
				_ = f.r.CloseOwner()
			case "expired-session":
				f.r.sessionExpiresAt = time.Now()
			case "owner-other-then-none":
				f.term.evidence.Owner = terminal.OwnerOther
				if owner, err := f.r.Owner(context.Background()); err != nil || !owner.Active {
					t.Fatal("fixture did not observe other owner", err)
				}
				f.term.evidence.Owner = terminal.OwnerNone
			}
			writes := f.api.base.writes
			if _, err := f.r.Observe(context.Background(), control.ReadRequest{KnownState: routeros.KnownState{Target: f.r.target}}); err == nil || !f.r.poisoned || f.r.removalTail == nil {
				t.Fatal("unverified tail cleared pending evidence")
			}
			if err := f.r.ApplyRuleRemovalBatch(context.Background(), f.batch, &removalBatchRecorderModel{}); err == nil || f.api.base.writes != writes {
				t.Fatal("unverified batch was resent")
			}
		})
	}
}

func TestGlobalTailAdapterGateModesAndUnknownStop(t *testing.T) {
	for _, change := range []string{"gate", "invalid-mode", "unknown", "unknown-applied"} {
		t.Run(change, func(t *testing.T) {
			f := globalTailFixture(t, control.RuleRemovalPreparedDiscard)
			batch := removalBatchPrefix(f.batch, 2)
			switch change {
			case "gate":
				f.r.profile.RuleRemovalGlobalTailVerified = false
			case "invalid-mode":
				batch.ProofMode = "caller-value"
			case "unknown":
				f.unknown()
			case "unknown-applied":
				apply := f.api.apply
				f.api.apply = func(ctx context.Context, m api.Mutation) (api.Result, error) {
					result, _ := apply(ctx, m)
					return result, &api.Error{Code: api.CodeTransport, Uncertain: true}
				}
			}
			recorder := &removalBatchRecorderModel{}
			if err := f.r.ApplyRuleRemovalBatch(context.Background(), batch, recorder); err == nil {
				t.Fatal("invalid/global unknown result accepted")
			}
			want := 0
			if change == "unknown" || change == "unknown-applied" {
				want = 1
			}
			if f.api.base.writes-f.initialWrites != want || len(recorder.absent) != 0 || f.r.removalTail != nil || f.index() != f.initialIndex {
				t.Fatal("unknown result advanced an attempt or fabricated absence")
			}
			if want == 1 {
				if !f.r.poisoned {
					t.Fatal("unknown did not poison runtime")
				}
				if _, err := f.r.Observe(context.Background(), control.ReadRequest{KnownState: routeros.KnownState{Target: f.r.target}}); err != nil {
					t.Fatal("unknown runtime lost its readonly recovery surface", err)
				}
			}
		})
	}
}

func TestGlobalTailAdapterPendingCannotBeReboundOrReset(t *testing.T) {
	f := globalTailFixture(t, control.RuleRemovalPreparedDiscard)
	if err := f.r.ApplyRuleRemovalBatch(context.Background(), removalBatchPrefix(f.batch, 2), &removalBatchRecorderModel{}); err != nil {
		t.Fatal(err)
	}
	if f.r.BindPlan(f.r.preparedDiscard.binding.Plan, nil) == nil || f.r.BindPreparedDiscard(context.Background(), f.r.preparedDiscard.binding) == nil || f.r.BindCleanup(context.Background(), control.CleanupBinding{Plan: f.r.preparedDiscard.binding.Plan}) == nil || f.r.ResetIdle(context.Background()) == nil {
		t.Fatal("pending actual tail could be bypassed by setup")
	}
	if _, err := f.r.Take(context.Background(), time.Now().Add(10*time.Second)); err == nil {
		t.Fatal("pending tail could take ownership")
	}
	if f.api.base.writes-f.initialWrites != 2 || f.r.removalTail == nil {
		t.Fatal("setup bypassed exact pending delta")
	}
}
