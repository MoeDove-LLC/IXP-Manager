package live

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
)

// All non-read recovery calls are counted and refused before reaching a native
// transport. This is an independent fresh-runtime test, not main lifecycle QA.
type nativeReadOnlyRecovery struct {
	*Runtime
	mu        sync.Mutex
	forbidden int
	last      control.Observation
}

func (r *nativeReadOnlyRecovery) deny() error {
	r.mu.Lock()
	r.forbidden++
	r.mu.Unlock()
	return fail("native-recovery-forbidden-call")
}
func (r *nativeReadOnlyRecovery) Apply(context.Context, routeros.APIMutation) (api.Result, error) {
	return api.Result{}, r.deny()
}
func (r *nativeReadOnlyRecovery) Take(context.Context, time.Time) (control.Owner, error) {
	return control.Owner{}, r.deny()
}
func (r *nativeReadOnlyRecovery) Confirm(context.Context) (control.Owner, error) {
	return control.Owner{}, r.deny()
}
func (r *nativeReadOnlyRecovery) Cancel(context.Context) (control.Owner, error) {
	return control.Owner{}, r.deny()
}
func (r *nativeReadOnlyRecovery) ResetIdle(context.Context) error                    { return r.deny() }
func (r *nativeReadOnlyRecovery) BindPlan(routeros.Plan, routeros.Credentials) error { return r.deny() }
func (r *nativeReadOnlyRecovery) Observe(ctx context.Context, request control.ReadRequest) (control.Observation, error) {
	observation, e := r.Runtime.Observe(ctx, request)
	if e == nil {
		r.mu.Lock()
		r.last = observation
		r.mu.Unlock()
	}
	return observation, e
}
func (r *nativeReadOnlyRecovery) snapshot() (control.Observation, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last, r.forbidden
}

// Deliberately a separate selector. The harness should run this only after the
// fixed Cancel and Confirm lifecycle gates pass. It proves this SSH-disconnect
// scenario, not a half-open network or arbitrary RouterOS restart behavior.
func TestNativeCHRLiveSupervisorRestartReadOnly(t *testing.T) {
	ctx, cancel, original, supervisor, request, _, _ := nativeSupervisorFixture(t, "restart")
	defer cancel()
	pending := nativeSupervisorPending(t, ctx, original, supervisor, request)
	if original.checkpoint == nil {
		t.Fatal("native_restart_checkpoint_available=false")
	}
	checkpointDigest, e := stateDigest(original.checkpoint.Inventory, original.target)
	if e != nil {
		t.Fatal("native_restart_checkpoint_digest=false")
	}
	originalSession := pending.SessionID
	// No Cancel/Confirm command: lose the original owner, close its API, then
	// instantiate completely new API/SSH transports and reload the HMAC journal.
	if e = supervisor.Close(); e != nil {
		t.Fatal("native_restart_old_supervisor_closed=false")
	}
	if e = original.Runtime.Close(); e != nil {
		t.Fatal("native_restart_old_runtime_closed=false")
	}
	fresh, e := Open(ctx, original.config)
	if e != nil {
		t.Fatalf("native_restart_fresh_runtime=false code=%s", liveCode(e))
	}
	t.Cleanup(func() { _ = fresh.Close() })
	reader := &nativeReadOnlyRecovery{Runtime: fresh}
	reopened, e := control.Open(control.Config{Dir: original.journalDir, Target: original.target, OperationTimeout: 30 * time.Second, PrepareTimeout: 3 * time.Minute, MinConfirmationTimeout: 10 * time.Second, MaxConfirmationTimeout: 2 * time.Minute, PollInterval: time.Second}, reader)
	if e != nil {
		t.Fatalf("native_restart_journal_authenticated=false code=%s", nativeSupervisorCode(e))
	}
	t.Cleanup(func() { _ = reopened.Close() })
	status, e := reopened.Recover(ctx)
	until := time.Now().Add(45 * time.Second)
	for status.Phase != control.RolledBack && time.Now().Before(until) && ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
		status, e = reopened.Recover(ctx)
	}
	if e != nil || status.Phase != control.RolledBack || !status.RollbackVerified || !status.RecoveryOnly || status.VerifiedAt.IsZero() {
		t.Fatalf("native_restart_rollback_readback=false code=%s", nativeSupervisorCode(e))
	}
	observation, forbidden := reader.snapshot()
	digest, e := stateDigest(observation.Inventory, original.target)
	if e != nil || digest != checkpointDigest || !sameCredentials(original.checkpoint.Credentials, observation.Credentials) || !observation.CrossChannelIdentityVerified || !observation.Owner.Known || observation.Owner.Active || observation.Owner.SessionID == originalSession || forbidden != 0 {
		t.Fatal("native_restart_exact_checkpoint_without_owner_reuse=false")
	}
	if _, e = reopened.Confirm(ctx, pending.TransactionID, pending.PlanChecksum); e == nil {
		t.Fatal("native_restart_old_confirmation_rejected=false")
	}
	_, forbidden = reader.snapshot()
	if forbidden != 0 {
		t.Fatal("native_restart_recovery_read_only=false")
	}
	t.Log("native_supervisor_restart_readonly_verified=true exact_checkpoint_verified=true fresh_runtime_identity_verified=true old_confirmation_rejected=true forbidden_native_calls=0 manual_cleanup_before_proof=false")
	cleanup := &nativeSupervisorRuntime{Runtime: fresh, added: original.added}
	nativeSupervisorCleanupAfterProof(t, cleanup)
}
