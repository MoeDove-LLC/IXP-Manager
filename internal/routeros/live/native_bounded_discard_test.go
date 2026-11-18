package live

import (
	"context"
	"reflect"
	"testing"

	"github.com/moedove/ixp-manager/internal/routeros/control"
)

// Same small real-device lifecycle as the existing global-tail case, with an
// explicit verified stop, fresh read-only recovery, then one unit per request.
func TestNativeCHRLivePreparedDiscardBoundedContinue(t *testing.T) {
	nativeDiscardCycleWithContinuation(t, true, true)
}

func nativeBoundedDiscardContinue(t *testing.T, ctx context.Context, cfg Config, configuration control.Config, supervisor *control.Supervisor, runtime *Runtime, driver control.Runtime, counts *nativeDiscardCounters, request control.DiscardRequest) (*control.Supervisor, *Runtime, control.Runtime, control.Status) {
	t.Helper()
	request.MaxBatches = 2
	status, err := supervisor.DiscardPrepared(ctx, request)
	attempts, acks, forbidden, _ := counts.snapshot()
	if err != nil || status.PreparedDiscard.Phase != control.PreparedDiscardPending || status.PreparedDiscard.LastCode != "discard-awaiting-explicit-continuation" || status.PreparedDiscard.Completed != 7 || status.PreparedDiscard.Total != 11 || !status.PreparedArtifactsRetained || !status.RollbackVerified || status.ConfirmationVerified || attempts != 7 || acks != 7 || forbidden != 0 || runtime.removalTail != nil {
		nativeDiscardFailure(t, "bounded_discard_first_boundary", err, counts)
	}
	t.Log("bounded_discard_request=1 bounded_discard_limit=2 bounded_discard_completed=7 bounded_discard_total=11 bounded_discard_pending=true bounded_discard_complete=false bounded_discard_return_verified=true")
	if _, err := supervisor.Review(ctx); err == nil {
		t.Fatal("bounded_discard_partial_review_rejected=false")
	}
	if err := supervisor.Close(); err != nil {
		nativeDiscardFailure(t, "bounded_discard_old_closed", err, counts)
	}
	if err := runtime.Close(); err != nil {
		nativeDiscardFailure(t, "bounded_discard_old_closed", err, counts)
	}
	fresh, err := Open(ctx, cfg)
	if err != nil {
		nativeDiscardFailure(t, "bounded_discard_fresh_open", err, counts)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	fresh.profile.PreparedDiscardVerified = true
	fresh.profile.RuleRemovalBatchVerified = true
	fresh.profile.RuleRemovalGlobalTailVerified = true
	fresh.api = &nativeGlobalTailDiscardAPI{nativeDiscardOnlyAPI: &nativeDiscardOnlyAPI{apiChannel: fresh.api, counts: counts}}
	fresh.terminal = &nativeDiscardReadOnlyTerminal{terminalChannel: fresh.terminal, counts: counts}
	guarded := &nativeGlobalTailDiscardRuntime{nativeDiscardOnlyRuntime: &nativeDiscardOnlyRuntime{runtime: fresh, counts: counts}}
	next, err := control.Open(configuration, guarded)
	if err != nil {
		nativeDiscardFailure(t, "bounded_discard_fresh_supervisor", err, counts)
	}
	t.Cleanup(func() { _ = next.Close() })
	before := next.Status()
	recovered, err := next.Recover(ctx)
	afterAttempts, afterACKs, afterForbidden, _ := counts.snapshot()
	if err != nil || recovered.PreparedDiscard.Phase != control.PreparedDiscardPending || recovered.PreparedDiscard.LastCode != "discard-awaiting-explicit-continuation" || recovered.PreparedDiscard.Completed != 7 || recovered.PreparedDiscard.Total != 11 || afterAttempts != attempts || afterACKs != acks || afterForbidden != 0 || guarded.batches != 0 || !reflect.DeepEqual(before.PreparedDiscard, recovered.PreparedDiscard) {
		nativeDiscardFailure(t, "bounded_discard_recovery_readonly", err, counts)
	}
	if _, err := next.Review(ctx); err == nil {
		t.Fatal("bounded_discard_partial_review_rejected=false")
	}
	t.Log("bounded_discard_fresh_recovery=true bounded_discard_recovery_completed=7 bounded_discard_recovery_mutations=0 bounded_discard_partial_review_rejected=true bounded_discard_automatic_continuation=false")
	request.MaxBatches = 1
	for number := 2; number <= 5; number++ {
		prior := 7 + number - 2
		status, err = next.DiscardPrepared(ctx, request)
		done := number == 5
		attempts, acks, forbidden, _ = counts.snapshot()
		if err != nil || status.PreparedDiscard.Completed != prior+1 || status.PreparedDiscard.Total != 11 || attempts != prior+1 || acks != attempts || forbidden != 0 || (status.PreparedDiscard.Phase == control.PreparedDiscardComplete) != done || status.PreparedArtifactsRetained == done || !status.RollbackVerified || status.ConfirmationVerified {
			nativeDiscardFailure(t, "bounded_discard_continuation_verified", err, counts)
		}
		if !done && (status.PreparedDiscard.Phase != control.PreparedDiscardPending || status.PreparedDiscard.LastCode != "discard-awaiting-explicit-continuation") {
			t.Fatal("bounded_discard_continuation_verified=false")
		}
		t.Logf("bounded_discard_request=%d bounded_discard_limit=1 bounded_discard_completed=%d bounded_discard_total=11 bounded_discard_pending=%t bounded_discard_complete=%t bounded_discard_return_verified=true", number, status.PreparedDiscard.Completed, !done, done)
	}
	if guarded.batches != 0 {
		t.Fatal("bounded_discard_continuation_verified=false")
	}
	t.Log("bounded_discard_requests=5 bounded_discard_final_verified=true bounded_discard_exact_once=true bounded_discard_large_scale_verified=false")
	return next, fresh, guarded, status
}
