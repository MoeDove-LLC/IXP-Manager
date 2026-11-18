package live

import (
	"context"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routing"
)

// This fixed private test signal can reset only the sealed CHR fixture. The
// harness acknowledges it after it has observed the QMP RESET event.
func resetNativeDevice(ctx context.Context) bool {
	const path = "/scratch/device-reset.sock"
	info, err := os.Lstat(path)
	if err != nil || info.Mode() != os.ModeSocket|0600 {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return false
	}
	defer conn.Close()
	unix := conn.(*net.UnixConn)
	raw, err := unix.SyscallConn()
	if err != nil {
		return false
	}
	var peer *syscall.Ucred
	var peerErr error
	if err := raw.Control(func(fd uintptr) {
		peer, peerErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil || peerErr != nil || peer == nil || peer.Uid != uint32(os.Geteuid()) {
		return false
	}
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		return false
	}
	const request = "reset\n"
	if n, err := io.WriteString(conn, request); err != nil || n != len(request) || unix.CloseWrite() != nil {
		return false
	}
	reply, err := io.ReadAll(io.LimitReader(conn, 4))
	return err == nil && string(reply) == "ok\n"
}

func deviceResetUptime(inventory api.Inventory) (time.Duration, bool) {
	if len(inventory.Resources) != 1 {
		return 0, false
	}
	raw := inventory.Resources[0]["uptime"]
	if value, err := time.ParseDuration(raw); err == nil && value >= 0 {
		return value, true
	}
	if value, err := time.Parse("15:04:05", raw); err == nil {
		return time.Duration(value.Hour())*time.Hour + time.Duration(value.Minute())*time.Minute + time.Duration(value.Second())*time.Second, true
	}
	return 0, false
}

func openAfterDeviceReset(ctx context.Context, cfg Config) *Runtime {
	ctx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		attempt, done := context.WithTimeout(ctx, 12*time.Second)
		runtime, err := Open(attempt, cfg)
		done()
		if err == nil {
			return runtime
		}
		select {
		case <-ctx.Done():
		case <-time.After(500 * time.Millisecond):
		}
	}
	return nil
}

func TestNativeCHRLiveDeviceResetRecovery(t *testing.T) {
	if os.Getenv("MOEDOVE_CHR_DEVICE_RESET_TEST") != "1" {
		t.Skip("device_reset_fixture_enabled=false")
	}
	testNativeCHRDeviceReset(t, false)
}

func TestNativeCHRLiveDeviceResetRestore(t *testing.T) {
	if os.Getenv("MOEDOVE_CHR_DEVICE_RESTORE_TEST") != "1" {
		t.Skip("device_reset_fixture_enabled=false")
	}
	testNativeCHRDeviceReset(t, true)
}

func testNativeCHRDeviceReset(t *testing.T, restore bool) {
	cfg := nativeLiveConfig(t)
	cfg.RouterID = "native-device-reset"
	cfg.SessionLifetimeSeconds, cfg.ObservationTimeoutSeconds, cfg.OperationTimeoutSeconds = 600, 120, 15
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	runtime, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal("device_reset_runtime=false")
	}
	defer runtime.Close()
	nativeReviewReuseStages(runtime)
	target := runtime.Binding().Target
	empty := routeros.KnownState{Target: target}
	initial, err := runtime.Observe(ctx, control.ReadRequest{KnownState: empty, Strict: true})
	if err != nil || initial.Snapshot == nil || !initial.Snapshot.NamespaceEmpty {
		t.Fatal("device_reset_empty=false")
	}
	router := routing.Router{ID: target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1"}
	peers := []routing.Peer{{ID: "reset-peer", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"203.0.113.0/24"}, AuthenticationRevision: strings.Repeat("A", 22), Password: "Synthetic_Device_Reset_Key"}}
	credentials := routeros.Credentials{peers[0].ID: {Revision: peers[0].AuthenticationRevision, Password: peers[0].Password}}
	baseline, err := routeros.BuildPlan(target, router, peers, *initial.Snapshot, routeros.PlanOptions{Bootstrap: true})
	if err != nil {
		t.Fatal("device_reset_initial_plan=false")
	}
	known := nativeReviewReuseKnown(baseline)
	driver := &nativeReviewReuseRuntime{runtime: runtime, counts: &nativeReviewReuseCounts{}, known: known, credentials: credentials}
	dir, err := os.MkdirTemp("/scratch/tmp", "ixp-device-reset-journal-")
	if err != nil {
		t.Fatal("device_reset_journal=false")
	}
	defer os.RemoveAll(dir)
	config := control.Config{Dir: dir, Target: target, OperationTimeout: 30 * time.Second, PrepareTimeout: 3 * time.Minute, MaxConfirmationTimeout: 2 * time.Minute, PollInterval: 10 * time.Second}
	supervisor, err := control.Open(config, driver)
	if err != nil {
		t.Fatal("device_reset_journal=false")
	}
	defer supervisor.Close()
	status, err := supervisor.Stage(ctx, control.Request{Plan: baseline, KnownState: empty, Credentials: credentials, Timeout: 90 * time.Second, ExclusiveAccessAcknowledged: true})
	if err != nil || status.Phase != control.AwaitingConfirmation {
		t.Fatal("device_reset_baseline_confirmed=false")
	}
	status, err = supervisor.Confirm(ctx, status.TransactionID, baseline.Checksum)
	if err != nil || status.Phase != control.Confirmed || !status.ConfirmationVerified {
		t.Fatal("device_reset_baseline_confirmed=false")
	}
	t.Log("device_reset_baseline_confirmed=true")
	observed, err := runtime.Observe(ctx, control.ReadRequest{KnownState: known, BaselineCredentials: credentials, Strict: true})
	if err != nil || observed.Snapshot == nil || observed.Snapshot.ActiveGeneration != baseline.Generation || len(observed.Snapshot.Connections) != 1 || !observed.Snapshot.Connections[0].AuthenticationVerified {
		t.Fatal("device_reset_baseline_verified=false")
	}
	peers[0].AllowedPrefixes = []string{"192.0.2.0/24"}
	update, err := routeros.BuildPlan(target, router, peers, *observed.Snapshot, routeros.PlanOptions{})
	if err != nil || update.Generation == baseline.Generation {
		t.Fatal("device_reset_update_plan=false")
	}
	prepared := nativeReviewReuseKnown(baseline)
	prepared.Generations = append(prepared.Generations, routeros.KnownGeneration{Target: target, Desired: update.Desired, PolicyChecksum: update.PolicyChecksum})
	desired := prepared
	desired.ActiveGeneration = update.Generation
	driver.known = prepared
	pending, err := supervisor.Stage(ctx, control.Request{Plan: update, KnownState: known, BaselineCredentials: credentials, Timeout: 90 * time.Second, ExclusiveAccessAcknowledged: true})
	if err != nil || pending.Phase != control.AwaitingConfirmation || driver.checkpoint == nil || pending.PlanChecksum != update.Checksum {
		t.Fatal("device_reset_pending_owner_verified=false")
	}
	before, err := runtime.Observe(ctx, control.ReadRequest{KnownState: desired, BaselineCredentials: credentials, Strict: true})
	if err != nil || before.Snapshot == nil || before.Snapshot.ActiveGeneration != update.Generation {
		t.Fatal("device_reset_pending_snapshot_verified=false")
	}
	if !before.Owner.Known || !before.Owner.Active || !before.Owner.ThisSession || !before.Owner.Alive || !before.Owner.HistoryKnown || before.Owner.HistoryActions <= 0 || before.Owner.SessionID != pending.SessionID {
		t.Fatal("device_reset_pending_owner_verified=false")
	}
	t.Log("device_reset_pending_owner_verified=true")
	beforeUptime, ok := deviceResetUptime(before.Inventory)
	if !ok || beforeUptime < 5*time.Second {
		t.Fatal("device_reset_before_uptime_known=false")
	}
	checkpoint := *driver.checkpoint
	checkpointDigest, err := stateDigest(checkpoint.Inventory, target)
	candidateDigest, candidateErr := stateDigest(before.Inventory, target)
	if err != nil || candidateErr != nil || checkpointDigest == candidateDigest {
		t.Fatal("device_reset_pending_snapshot_verified=false")
	}
	if !resetNativeDevice(ctx) {
		t.Fatal("device_reset_qmp_acknowledged=false")
	}
	t.Log("device_reset_qmp_acknowledged=true")
	if supervisor.Close() != nil || runtime.Close() != nil {
		t.Fatal("device_reset_old_runtime_closed=false")
	}

	fresh := openAfterDeviceReset(ctx, cfg)
	if fresh == nil || fresh.Binding().Target != target {
		t.Fatal("device_reset_fresh_identity_verified=false")
	}
	defer fresh.Close()
	counts := &nativeReviewReuseCounts{}
	counts.readOnly.Store(true)
	fresh.api = &nativeReviewReuseAPI{apiChannel: fresh.api, counts: counts}
	fresh.terminal = &nativeReviewReuseTerminal{terminalChannel: fresh.terminal, counts: counts}
	reader := &nativeReadOnlyRecovery{Runtime: fresh}
	reopened, err := control.Open(config, reader)
	if err != nil {
		t.Fatal("device_reset_journal=false")
	}
	defer reopened.Close()
	if _, err = reopened.Confirm(ctx, pending.TransactionID, pending.PlanChecksum); nativeSupervisorCode(err) != "confirmation-not-authorized" {
		t.Fatal("device_reset_old_confirmation_rejected=false")
	}
	status, err = reopened.Recover(ctx)
	after, forbidden := reader.snapshot()
	if !after.CrossChannelIdentityVerified || after.Target != target {
		t.Fatal("device_reset_fresh_identity_verified=false")
	}
	afterUptime, ok := deviceResetUptime(after.Inventory)
	if !ok || afterUptime >= beforeUptime {
		t.Fatal("device_reset_uptime_restarted=false")
	}
	if !after.Owner.Known || !after.Owner.Alive || after.Owner.Active || after.Owner.ThisSession || !after.Owner.HistoryKnown || after.Owner.HistoryActions != 0 || after.Owner.SessionID == pending.SessionID {
		t.Fatal("device_reset_owner_released=false")
	}
	actualDigest, digestErr := stateDigest(after.Inventory, target)
	if digestErr != nil {
		t.Fatal("device_reset_observed_state=false")
	}
	outcome := "unrecognized-state"
	switch {
	case actualDigest == checkpointDigest && sameCredentials(checkpoint.Credentials, after.Credentials):
		outcome = "checkpoint-restored"
		if err != nil || status.Phase != control.RolledBack || !status.RollbackVerified || !status.RecoveryOnly || status.VerifiedAt.IsZero() || status.ConfirmationVerified {
			t.Fatal("device_reset_recovery_proven=false")
		}
		review, reviewErr := reopened.Review(ctx)
		if reviewErr != nil || !review.Snapshot.Managed || review.Snapshot.TableFIB || review.Snapshot.ActiveGeneration != baseline.Generation || len(review.Snapshot.Connections) != 1 || !review.Snapshot.Connections[0].AuthenticationVerified {
			t.Fatal("device_reset_recovery_proven=false")
		}
	case actualDigest == candidateDigest && sameCredentials(before.Credentials, after.Credentials):
		outcome = "candidate-retained"
	}
	if outcome != "checkpoint-restored" {
		wantError, wantStatus := "recovery-state-not-proven", "baseline-not-observed"
		if outcome == "candidate-retained" {
			wantError, wantStatus = control.UnconfirmedCandidateRetainedCode, control.UnconfirmedCandidateRetainedCode
		}
		if nativeSupervisorCode(err) != wantError || status.LastCode != wantStatus || status.Phase != control.RollbackPending || status.RollbackVerified || status.ConfirmationVerified || !status.RecoveryOnly {
			t.Fatal("device_reset_recovery_proven=false")
		}
		if _, err = reopened.Review(ctx); err == nil {
			t.Fatal("device_reset_recovery_proven=false")
		}
	}
	_, forbiddenAfter := reader.snapshot()
	if forbidden != 0 || forbiddenAfter != 0 || counts.forbidden.Load() != 0 || fresh.taken || fresh.boundPlan != nil {
		t.Fatal("device_reset_read_only=false")
	}
	t.Logf("device_reset_fresh_identity_verified=true device_reset_uptime_restarted=true device_reset_old_confirmation_rejected=true device_reset_owner_released=true device_reset_recovery_proven=true device_reset_read_only=true device_reset_outcome=%s", outcome)
	if !restore {
		return
	}
	if outcome != "candidate-retained" || reopened.Close() != nil || fresh.Close() != nil {
		t.Fatal("device_restore_fresh_controller=false")
	}
	writer, err := Open(ctx, cfg)
	if err != nil || writer.Binding().Target != target {
		t.Fatal("device_restore_fresh_controller=false")
	}
	defer writer.Close()
	nativeReviewReuseStages(writer)
	restoreController, err := control.Open(config, writer)
	if err != nil {
		t.Fatal("device_restore_fresh_controller=false")
	}
	defer restoreController.Close()
	review, err := restoreController.ReviewRestore(ctx, pending.TransactionID, pending.PlanChecksum)
	if err != nil || review.Plan.PolicyChecksum != baseline.PolicyChecksum || review.Plan.Base.ActiveGeneration != update.Generation {
		t.Fatal("device_restore_review_verified=false")
	}
	for _, op := range review.Plan.Operations {
		if op.Phase == routeros.Prepare {
			t.Fatal("device_restore_review_verified=false")
		}
	}
	t.Log("device_restore_review_verified=true")
	staged, err := restoreController.StageRestore(ctx, control.RestoreRequest{
		TransactionID: pending.TransactionID, PlanChecksum: pending.PlanChecksum, RestoreChecksum: review.Plan.Checksum,
		BaselineCredentials: credentials, Timeout: 90 * time.Second, ExclusiveAccessAcknowledged: true,
	})
	if err != nil || staged.Phase != control.AwaitingConfirmation || staged.TransactionID == pending.TransactionID || staged.PlanChecksum != review.Plan.Checksum {
		t.Fatal("device_restore_stage_verified=false")
	}
	if _, err = restoreController.Confirm(ctx, pending.TransactionID, pending.PlanChecksum); nativeSupervisorCode(err) != "confirmation-not-authorized" {
		t.Fatal("device_restore_stage_verified=false")
	}
	t.Log("device_restore_stage_verified=true")
	committed, err := restoreController.Confirm(ctx, staged.TransactionID, staged.PlanChecksum)
	if err != nil || committed.Phase != control.Confirmed || !committed.ConfirmationVerified || committed.RollbackVerified {
		t.Fatal("device_restore_confirm_verified=false")
	}
	final, err := writer.Observe(ctx, control.ReadRequest{KnownState: prepared, BaselineCredentials: credentials, Strict: true})
	if err != nil || final.Snapshot == nil || !final.Snapshot.Managed || final.Snapshot.TableFIB || final.Snapshot.ActiveGeneration != baseline.Generation ||
		len(final.Snapshot.Connections) != 1 || !final.Snapshot.Connections[0].AuthenticationVerified || final.Owner.Active || !sameCredentials(checkpoint.Credentials, final.Credentials) {
		t.Fatal("device_restore_confirm_verified=false")
	}
	t.Log("device_restore_confirm_verified=true")
	if restoreController.Close() != nil || writer.Close() != nil {
		t.Fatal("device_restore_journal_reopened=false")
	}
	readback, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal("device_restore_journal_reopened=false")
	}
	defer readback.Close()
	last, err := control.Open(config, &nativeReadOnlyRecovery{Runtime: readback})
	if err != nil {
		t.Fatal("device_restore_journal_reopened=false")
	}
	defer last.Close()
	if replayed := last.Status(); replayed.Phase != control.Confirmed || !replayed.ConfirmationVerified || replayed.TransactionID != committed.TransactionID {
		t.Fatal("device_restore_journal_reopened=false")
	}
	t.Log("device_restore_journal_reopened=true")
}
