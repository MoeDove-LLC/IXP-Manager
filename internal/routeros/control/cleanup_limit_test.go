package control

import (
	"context"
	"errors"
	"testing"
)

func cleanupRequestAt(status Status, maxBatches int) CleanupRequest {
	return CleanupRequest{TransactionID: status.TransactionID, PlanChecksum: status.PlanChecksum, MaxBatches: maxBatches}
}

func TestManualCleanupRequiresAnExplicitRequest(t *testing.T) {
	s, r, _, _, _ := removalCleanupConfiguredFixture(t, true)
	initial := s.Status()
	if initial.Phase != Confirmed || !initial.ConfirmationVerified || initial.Cleanup.Phase != CleanupPending ||
		initial.Cleanup.LastCode != cleanupAwaitingContinuation || initial.Cleanup.Completed != 0 || s.cleanupAuto || r.cleanupWrites+r.batchWrites != 0 {
		t.Fatal("manual mode started automatic cleanup or lost its confirmed scope")
	}
	status, err := s.CleanupBounded(context.Background(), cleanupRequestAt(initial, 1))
	if err != nil || status.Cleanup.Phase != CleanupComplete || !status.ConfirmationVerified || status.Cleanup.Completed != 3 || r.batchCalls != 1 || r.batchWrites != 3 {
		t.Fatal("one authorized verified batch could not finish confirmed cleanup", err)
	}
}

func TestCleanupBoundedReopenKeepsConfirmedScope(t *testing.T) {
	ctx := context.Background()
	s, r, config := globalTailFixture(t, RuleRemovalCleanup)
	initial := s.Status()
	for _, limit := range []int{-1, MaxCleanupRequestBatches + 1} {
		_, err := s.CleanupBounded(ctx, cleanupRequestAt(initial, limit))
		var invalid *Failure
		if !errors.As(err, &invalid) || invalid.Code != "cleanup-invalid-batch-limit" || !s.cleanupAuto || r.cleanupBinds+r.batchCalls != 0 {
			t.Fatal("bad input changed the existing cleanup policy or touched the device")
		}
	}
	r.prefixLimit = 1
	request := cleanupRequestAt(initial, 1)
	status, err := s.CleanupBounded(ctx, request)
	if err != nil || !status.ConfirmationVerified || status.Phase != Confirmed || status.Cleanup.Phase != CleanupPending ||
		status.Cleanup.LastCode != cleanupAwaitingContinuation || status.Cleanup.Completed != 1 || s.cleanupAuto || r.batchCalls != 1 || r.batchWrites != 1 || s.cleanup.Pending != nil || s.removalBatch != nil {
		t.Fatal("request did not stop exactly at an authenticated cleanup boundary", err)
	}
	if _, err = s.Review(ctx); err == nil {
		t.Fatal("pending cleanup was exported as an ordinary baseline")
	}
	next, fresh := globalTailReopen(t, s, r, config, false)
	recovered, err := next.Recover(ctx)
	if err != nil || recovered.Cleanup.Completed != 1 || recovered.Cleanup.LastCode != cleanupAwaitingContinuation || fresh.cleanupBinds+fresh.batchWrites+fresh.cleanupWrites != 0 {
		t.Fatal("recovery changed verified progress or wrote to the device", err)
	}
	if _, err = next.CleanupBounded(ctx, CleanupRequest{TransactionID: initial.TransactionID, PlanChecksum: "different", MaxBatches: 1}); err == nil || fresh.cleanupBinds != 0 {
		t.Fatal("continued removals accepted a different plan")
	}
	status, err = next.CleanupBounded(ctx, request)
	if err != nil || status.Cleanup.Phase != CleanupComplete || status.Cleanup.Completed != 3 || status.Cleanup.Total != 3 || !status.ConfirmationVerified ||
		fresh.batchCalls != 1 || fresh.batchWrites != 2 || fresh.cleanupWrites != 0 || status.PlanChecksum != initial.PlanChecksum {
		t.Fatal("continuation did not consume the precise remaining approved suffix", err)
	}
	if review, err := next.Review(ctx); err != nil || review.Snapshot.ActiveGeneration != next.begin.Plan.Generation {
		t.Fatal("confirmed desired generation did not survive cleanup", err)
	}
}
