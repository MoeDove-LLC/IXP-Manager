package live

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routing"
)

type nativeAddedObject struct {
	resource api.Resource
	id       string
}

// This wrapper and its capability injection exist only in the opt-in test
// binary. They cannot be selected by Config or any production service request.
type nativeSupervisorRuntime struct {
	*Runtime
	t                               *testing.T
	mode                            string
	preparedKnown                   routeros.KnownState
	checkpoint                      *control.Observation
	added                           []nativeAddedObject
	prepareWrites, activationWrites int
	maximumHistory                  int
	journalDir                      string
}

func (r *nativeSupervisorRuntime) Apply(ctx context.Context, mutation routeros.APIMutation) (api.Result, error) {
	before, e := r.Runtime.Owner(ctx)
	if e != nil || !before.Known || !before.HistoryKnown || !before.Alive || before.HistoryActions < 0 || before.HistoryActions > 80 {
		return api.Result{}, fail("native-history-before-unknown")
	}
	if mutation.Phase == routeros.Prepare && before.Active || mutation.Phase == routeros.Activate && (!before.Active || !before.ThisSession) {
		return api.Result{}, fail("native-history-before-owner-mismatch")
	}
	result, e := r.Runtime.Apply(ctx, mutation)
	if e != nil {
		return result, e
	}
	if mutation.Request.Action == api.Add && result.Acknowledged && result.ID != "" {
		r.added = append(r.added, nativeAddedObject{mutation.Request.Resource, result.ID})
		if mutation.Request.Resource == api.BGPConnection {
			nativeLiveConnectionASEvidence(r.t, ctx, r.Runtime, result.ID)
		}
	}
	after, e := r.Runtime.Owner(ctx)
	if e != nil || !after.Known || !after.HistoryKnown || !after.Alive || before.SessionID != after.SessionID || after.HistoryActions < before.HistoryActions || after.HistoryActions > 80 {
		return result, fail("native-history-after-unknown")
	}
	if mutation.Phase == routeros.Prepare {
		if after.Active {
			return result, fail("native-prepare-created-owner")
		}
		r.prepareWrites++
	} else {
		if !after.Active || !after.ThisSession {
			return result, fail("native-activation-lost-owner")
		}
		r.activationWrites++
	}
	if after.HistoryActions > r.maximumHistory {
		r.maximumHistory = after.HistoryActions
	}
	r.t.Logf("native_supervisor_mode=%s phase=%s resource=%s action=%s history_before=%d history_after=%d history_delta=%d", r.mode, mutation.Phase, mutation.Request.Resource, mutation.Request.Action, before.HistoryActions, after.HistoryActions, after.HistoryActions-before.HistoryActions)
	return result, nil
}
func (r *nativeSupervisorRuntime) Take(ctx context.Context, deadline time.Time) (control.Owner, error) {
	checkpoint, e := r.Runtime.Observe(ctx, control.ReadRequest{KnownState: r.preparedKnown, Strict: false})
	if e != nil {
		return control.Owner{}, e
	}
	if !checkpoint.Owner.Known || checkpoint.Owner.Active || !checkpoint.Owner.HistoryKnown {
		return control.Owner{}, fail("native-checkpoint-owner-unknown")
	}
	if len(checkpoint.Credentials) != len(r.preparedKnown.Peers) {
		return control.Owner{}, fail("native-checkpoint-credential-count")
	}
	for _, credential := range checkpoint.Credentials {
		if !credential.Matches(nil) {
			return control.Owner{}, fail("native-checkpoint-empty-credential-unverified")
		}
	}
	for _, row := range checkpoint.Inventory.Connections {
		if strings.HasPrefix(row["name"], routeros.OwnerName(r.target.RouterID)) && row["disabled"] != "true" {
			return control.Owner{}, fail("native-checkpoint-connection-not-disabled")
		}
	}
	r.checkpoint = &checkpoint
	return r.Runtime.Take(ctx, deadline)
}

func nativeSupervisorCode(err error) string {
	if err == nil {
		return "none"
	}
	var controlled *control.Failure
	if errors.As(err, &controlled) {
		return controlled.Code
	}
	return liveCode(err)
}
func nativeSupervisorFixture(t *testing.T, mode string) (context.Context, context.CancelFunc, *nativeSupervisorRuntime, *control.Supervisor, control.Request, routing.Router, []routing.Peer) {
	t.Helper()
	cfg := nativeLiveConfig(t)
	cfg.RouterID = "native-supervisor-" + mode
	cfg.SessionLifetimeSeconds = 600
	cfg.ObservationTimeoutSeconds = 120
	cfg.OperationTimeoutSeconds = 15
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	runtime, e := Open(ctx, cfg)
	if e != nil {
		cancel()
		t.Fatalf("native_supervisor_open=false code=%s", liveCode(e))
	}
	t.Cleanup(func() { _ = runtime.Close(); cancel() })
	target := runtime.Binding().Target
	known := routeros.KnownState{Target: target}
	initial, e := runtime.Observe(ctx, control.ReadRequest{KnownState: known, Strict: true})
	if e != nil || initial.Snapshot == nil || !initial.Snapshot.NamespaceEmpty || !initial.Owner.Known || initial.Owner.Active || !initial.Owner.HistoryKnown {
		t.Fatalf("native_supervisor_initial_namespace=false code=%s", liveCode(e))
	}
	router := routing.Router{ID: target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1", LocalIPv6: "2001:db8::1"}
	peers := []routing.Peer{{ID: "native-peer4", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"203.0.113.0/24"}}, {ID: "native-peer6", ASN: 64502, Address: "2001:db8::2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"2001:db8:1::/48"}}}
	plan, e := routeros.BuildPlan(target, router, peers, *initial.Snapshot, routeros.PlanOptions{Bootstrap: true})
	if e != nil {
		t.Fatal("native_supervisor_plan_valid=false")
	}
	prepared := known
	for _, peer := range plan.Desired.Peers {
		prepared.Peers = append(prepared.Peers, routeros.KnownPeer{ID: peer.ID, Authentication: peer.Authentication})
	}
	driver := &nativeSupervisorRuntime{Runtime: runtime, t: t, mode: mode, preparedKnown: prepared}
	// Permit only this fixed, isolated test to exercise the as-yet unverified
	// execution path. Production CapabilityProfile remains closed. Every actual
	// write's floating-undo count is measured above; 80 is a refusal ceiling, not
	// a claim that arbitrary plans or concurrent writers are verified safe.
	runtime.profile.PreparationVerified = true
	runtime.profile.ActivationVerified = true
	runtime.profile.ConfirmNativeVerified = true
	runtime.profile.MaxHistoryActions = 80
	// testing.T.TempDir's nested directory need not be 0700. Create this
	// private journal leaf explicitly; production permission checks stay exact.
	journalDir, e := os.MkdirTemp("/scratch/tmp", "ixp-routeros-native-journal-")
	if e != nil {
		t.Fatal("native_supervisor_private_journal_create=false")
	}
	t.Cleanup(func() { _ = os.RemoveAll(journalDir) })
	info, e := os.Lstat(journalDir)
	if e != nil || !info.IsDir() || info.Mode().Perm() != 0700 || filepath.Dir(journalDir) != "/scratch/tmp" {
		t.Fatal("native_supervisor_private_journal_mode=false")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		t.Fatal("native_supervisor_private_journal_owner=false")
	}
	driver.journalDir = journalDir
	supervisor, e := control.Open(control.Config{Dir: journalDir, Target: target, OperationTimeout: 30 * time.Second, PrepareTimeout: 3 * time.Minute, MinConfirmationTimeout: 10 * time.Second, MaxConfirmationTimeout: 2 * time.Minute, PollInterval: time.Second}, driver)
	if e != nil {
		t.Fatalf("native_supervisor_open_journal=false code=%s", nativeSupervisorCode(e))
	}
	t.Cleanup(func() { _ = supervisor.Close() })
	return ctx, cancel, driver, supervisor, control.Request{Plan: plan, KnownState: known, Timeout: 90 * time.Second, ExclusiveAccessAcknowledged: true}, router, peers
}
func nativeSupervisorCleanupAfterProof(t *testing.T, r *nativeSupervisorRuntime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	inventory, e := r.api.Inventory(ctx)
	if e != nil || inventory.SafeMode == nil || inventory.SafeMode.Enabled || inventory.SafeMode.Current {
		t.Error("native_supervisor_cleanup_owner_clear=false")
		return
	}
	// Cleanup is intentionally AFTER all rollback/retention assertions. It is
	// never used to manufacture the cancellation proof.
	for i := len(r.added) - 1; i >= 0; i-- {
		object := r.added[i]
		result, e := r.api.Apply(ctx, api.Mutation{Resource: object.resource, Action: api.Remove, ID: object.id})
		if e != nil || !result.Acknowledged {
			t.Error("native_supervisor_cleanup_received_id=false")
			return
		}
	}
}
func nativeSupervisorPending(t *testing.T, ctx context.Context, r *nativeSupervisorRuntime, s *control.Supervisor, request control.Request) control.Status {
	t.Helper()
	status, e := s.Stage(ctx, request)
	if e != nil || status.Phase != control.AwaitingConfirmation || status.TransactionID == "" || status.PlanChecksum != request.Plan.Checksum || r.checkpoint == nil || r.prepareWrites == 0 || r.activationWrites == 0 {
		t.Fatalf("native_supervisor_stage_pending=false code=%s", nativeSupervisorCode(e))
	}
	owner, e := r.Owner(ctx)
	if e != nil || !owner.Known || !owner.Active || !owner.ThisSession || !owner.Alive || !owner.HistoryKnown || owner.SessionID != status.SessionID || owner.HistoryActions < 1 || owner.HistoryActions > 80 {
		t.Fatalf("native_supervisor_pending_owner=false code=%s", liveCode(e))
	}
	return status
}
func TestNativeCHRLiveSupervisorCancel(t *testing.T) {
	ctx, cancel, r, s, request, _, _ := nativeSupervisorFixture(t, "cancel")
	defer cancel()
	pending := nativeSupervisorPending(t, ctx, r, s, request)
	status, e := s.Cancel(ctx, pending.TransactionID)
	// RouterOS undo is asynchronous. Recovery is bounded and read-only; no API
	// mutations or new owner/confirm attempts are issued while awaiting it.
	until := time.Now().Add(30 * time.Second)
	for status.Phase != control.RolledBack && time.Now().Before(until) && ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
		status, e = s.Recover(ctx)
	}
	if e != nil || status.Phase != control.RolledBack || !status.RollbackVerified || !status.PreparedArtifactsRetained || status.VerifiedAt.IsZero() {
		t.Fatalf("native_supervisor_cancel_verified=false code=%s", nativeSupervisorCode(e))
	}
	after, e := r.Observe(ctx, control.ReadRequest{KnownState: r.preparedKnown, Strict: false})
	if e != nil {
		t.Fatalf("native_supervisor_cancel_observation=false code=%s", liveCode(e))
	}
	checkpointDigest, e := stateDigest(r.checkpoint.Inventory, r.target)
	if e != nil {
		t.Fatal("native_supervisor_checkpoint_digest=false")
	}
	afterDigest, e := stateDigest(after.Inventory, r.target)
	if e != nil || afterDigest != checkpointDigest || !sameCredentials(r.checkpoint.Credentials, after.Credentials) || !after.Owner.Known || after.Owner.Active || after.Owner.Alive || len(after.Inventory.Connections) < 2 {
		t.Fatal("native_supervisor_prepared_checkpoint_equal=false")
	}
	if r.maximumHistory < 1 || r.maximumHistory > 80 {
		t.Fatal("native_supervisor_actual_history_bound=false")
	}
	t.Log("native_supervisor_cancel_checkpoint_verified=true prepared_retained=true manual_cleanup_before_proof=false")
	nativeSupervisorCleanupAfterProof(t, r)
}
func TestNativeCHRLiveSupervisorConfirm(t *testing.T) {
	ctx, cancel, r, s, request, router, peers := nativeSupervisorFixture(t, "confirm")
	defer cancel()
	pending := nativeSupervisorPending(t, ctx, r, s, request)
	status, e := s.Confirm(ctx, pending.TransactionID, request.Plan.Checksum)
	if e != nil || status.Phase != control.Confirmed || !status.ConfirmationVerified || status.VerifiedAt.IsZero() {
		t.Fatalf("native_supervisor_confirm_verified=false code=%s", nativeSupervisorCode(e))
	}
	known := r.preparedKnown
	known.ActiveGeneration = request.Plan.Generation
	known.Generations = []routeros.KnownGeneration{{Target: r.target, Desired: request.Plan.Desired, PolicyChecksum: request.Plan.PolicyChecksum}}
	observed, e := r.Observe(ctx, control.ReadRequest{KnownState: known, Strict: true})
	if e != nil || observed.Snapshot == nil || !observed.Snapshot.Managed || !observed.Owner.Known || observed.Owner.Active || len(observed.Snapshot.Connections) != 2 {
		t.Fatalf("native_supervisor_confirm_strict_snapshot=false code=%s", liveCode(e))
	}
	for _, connection := range observed.Snapshot.Connections {
		if connection.Disabled || !connection.AuthenticationVerified {
			t.Fatal("native_supervisor_confirm_connections_enabled=false")
		}
	}
	noop, e := routeros.BuildPlan(r.target, router, peers, *observed.Snapshot, routeros.PlanOptions{})
	if e != nil || len(noop.Operations) != 0 {
		t.Fatal("native_supervisor_confirm_desired_roundtrip=false")
	}
	beforeDigest, e := stateDigest(observed.Inventory, r.target)
	if e != nil {
		t.Fatal("native_supervisor_confirm_digest=false")
	}
	if e = r.CloseOwner(); e != nil {
		t.Fatal("native_supervisor_confirm_owner_close=false")
	}
	retained, e := r.Observe(ctx, control.ReadRequest{KnownState: known, Strict: true})
	if e != nil {
		t.Fatalf("native_supervisor_confirm_postclose_readback=false code=%s", liveCode(e))
	}
	afterDigest, e := stateDigest(retained.Inventory, r.target)
	if e != nil || beforeDigest != afterDigest || !sameCredentials(observed.Credentials, retained.Credentials) || retained.Owner.Alive || !retained.Owner.Known || retained.Owner.Active {
		t.Fatal("native_supervisor_confirm_postclose_retained=false")
	}
	if r.maximumHistory < 1 || r.maximumHistory > 80 {
		t.Fatal("native_supervisor_actual_history_bound=false")
	}
	t.Log("native_supervisor_confirm_strict_snapshot_verified=true post_owner_close_retained=true manual_cleanup_before_proof=false")
	nativeSupervisorCleanupAfterProof(t, r)
}
