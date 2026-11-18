package control

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routing"
)

type cleanupRuntime struct {
	*memoryRuntime
	cleanupBinds   int
	cleanupWrites  int
	rejectCleanup  bool
	boundRemaining []string
}

func (r *cleanupRuntime) Binding() Binding {
	b := r.memoryRuntime.Binding()
	b.CleanupVerified = true
	return b
}
func (r *cleanupRuntime) BindCleanup(ctx context.Context, b CleanupBinding) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	r.cleanupBinds++
	r.boundRemaining = append([]string(nil), b.RemainingOperationIDs...)
	if r.owner.Active {
		return errors.New("owner active")
	}
	return nil
}
func (r *cleanupRuntime) ApplyCleanup(ctx context.Context, m routeros.APIMutation) (api.Result, error) {
	r.cleanupWrites++
	if m.Phase != routeros.Cleanup || m.Request.Action != api.Remove || len(r.boundRemaining) == 0 || r.boundRemaining[0] != m.OperationID {
		return api.Result{}, errors.New("scope/order")
	}
	if r.rejectCleanup {
		return api.Result{}, errors.New("lost response without effect")
	}
	result, err := r.memoryRuntime.Apply(ctx, m)
	if err == nil {
		r.boundRemaining = r.boundRemaining[1:]
	}
	return result, err
}
func confirmedUpdate(t *testing.T, r *cleanupRuntime, c Config) (*Supervisor, Status) {
	t.Helper()
	_, initial := fixtureRuntime(t)
	s, err := Open(c, r)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Stage(context.Background(), initial)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Confirm(context.Background(), first.TransactionID, first.PlanChecksum); err != nil {
		t.Fatal(err)
	}
	p := initial.Plan
	known := routeros.KnownState{Target: r.target, ActiveGeneration: p.Generation, Generations: []routeros.KnownGeneration{{Target: r.target, Desired: p.Desired, PolicyChecksum: p.PolicyChecksum}}}
	base, err := routeros.BuildSnapshot(copyInventory(r.inv), known, routeros.CredentialEvidence{}, r.evidence())
	if err != nil {
		t.Fatal(err)
	}
	router := routing.Router{ID: r.target.RouterID, ASN: 64520, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1"}
	next, err := routeros.BuildPlan(r.target, router, nil, base, routeros.PlanOptions{AllowIdentityChange: true})
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Stage(context.Background(), Request{Plan: next, KnownState: known, Timeout: 2 * time.Second, ExclusiveAccessAcknowledged: true})
	if err != nil {
		t.Fatal(err)
	}
	st, err = s.Confirm(context.Background(), st.TransactionID, st.PlanChecksum)
	if err != nil {
		t.Fatal(err)
	}
	if st.Phase != Confirmed || st.Cleanup.Total != 3 {
		t.Fatalf("missing authorized cleanup: %+v", st)
	}
	return s, st
}
func TestConfirmedCleanupRunsAutomaticallyWithoutAnotherApproval(t *testing.T) {
	model, _ := fixtureRuntime(t)
	r := &cleanupRuntime{memoryRuntime: model}
	config := testConfig(t, model)
	s, _ := confirmedUpdate(t, r, config)
	defer s.Close()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.Status().Cleanup.Phase == CleanupComplete {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	status := s.Status()
	if status.Phase != Confirmed || !status.ConfirmationVerified || status.Cleanup.Phase != CleanupComplete || status.Cleanup.Completed != 3 {
		t.Fatalf("cleanup did not preserve confirmation: %+v", status)
	}
	if r.cancelCount != 0 || r.cleanupWrites != 3 {
		t.Fatal("cleanup was rolled back or not executed exactly once")
	}
}
func TestCleanupFailureAndRestartNeverReplayUnknownRemoval(t *testing.T) {
	model, _ := fixtureRuntime(t)
	r := &cleanupRuntime{memoryRuntime: model, rejectCleanup: true}
	config := testConfig(t, model)
	config.PollInterval = 10 * time.Second
	s, st := confirmedUpdate(t, r, config)
	failed, err := s.Cleanup(context.Background(), st.TransactionID, st.PlanChecksum)
	if err == nil || failed.Phase != Confirmed || !failed.ConfirmationVerified || failed.Cleanup.Phase != CleanupUncertain {
		t.Fatal("failed cleanup lost committed status")
	}
	if _, err = s.Cleanup(context.Background(), st.TransactionID, st.PlanChecksum); err == nil {
		t.Fatal("unknown Remove silently retried")
	}
	if r.cleanupWrites != 1 || r.cancelCount != 0 {
		t.Fatal("uncertain cleanup replayed or canceled confirmed state")
	}
	pending := *s.cleanup.Pending
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(config, r)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	binds, writes := r.cleanupBinds, r.cleanupWrites
	if _, err = reopened.Recover(context.Background()); err == nil {
		t.Fatal("present target claimed removed")
	}
	if r.cleanupBinds != binds || r.cleanupWrites != writes {
		t.Fatal("read-only recovery wrote")
	}
	// Simulate the original delayed operation completing, without retransmit.
	_, err = r.memoryRuntime.Apply(context.Background(), routeros.APIMutation{OperationID: pending.OperationID, Phase: routeros.Cleanup, Request: api.Mutation{Resource: pending.Spec.Resource, Action: api.Remove, ID: pending.Spec.ID}})
	if err != nil {
		t.Fatal(err)
	}
	st, err = reopened.Recover(context.Background())
	if err != nil || st.Cleanup.Completed != 1 {
		t.Fatalf("readonly absence proof failed: %+v %v", st, err)
	}
	r.rejectCleanup = false
	st, err = reopened.Cleanup(context.Background(), st.TransactionID, st.PlanChecksum)
	if err != nil || st.Cleanup.Phase != CleanupComplete || st.Phase != Confirmed {
		t.Fatalf("authorized suffix not completed: %+v %v", st, err)
	}
	if r.cleanupWrites != 3 || r.cancelCount != 0 {
		t.Fatal("already removed target repeated or active config canceled")
	}
}

func TestCleanupJournalFailurePreservesConfirmedFact(t *testing.T) {
	model, _ := fixtureRuntime(t)
	r := &cleanupRuntime{memoryRuntime: model}
	config := testConfig(t, model)
	config.PollInterval = 10 * time.Second
	s, status := confirmedUpdate(t, r, config)
	defer s.Close()
	s.log.fail = func(kind string) error {
		if kind == "cleanup-intent" {
			return errors.New("disk failed")
		}
		return nil
	}
	result, err := s.Cleanup(context.Background(), status.TransactionID, status.PlanChecksum)
	if err == nil || result.Phase != Confirmed || !result.ConfirmationVerified {
		t.Fatal("cleanup durability failure erased confirmation")
	}
	if r.cleanupWrites != 0 || r.cancelCount != 0 {
		t.Fatal("write before intent or rollback after commit")
	}
}

func TestCleanupCannotDeleteChangedTargetOrAcceptNewPlan(t *testing.T) {
	model, _ := fixtureRuntime(t)
	r := &cleanupRuntime{memoryRuntime: model}
	config := testConfig(t, model)
	config.PollInterval = 10 * time.Second
	s, status := confirmedUpdate(t, r, config)
	defer s.Close()
	if _, err := s.Cleanup(context.Background(), status.TransactionID, strings.Repeat("f", 64)); err == nil {
		t.Fatal("wrong plan checksum")
	}
	r.mu.Lock()
	r.inv.FilterRules[2]["rule"] = "accept;"
	r.mu.Unlock()
	result, err := s.Cleanup(context.Background(), status.TransactionID, status.PlanChecksum)
	if err == nil || result.Phase != Confirmed || r.cleanupWrites != 0 {
		t.Fatal("changed cleanup target deleted or confirmed fact lost")
	}
}
