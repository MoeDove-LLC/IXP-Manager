package control

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
)

func requireRestoreCode(t *testing.T, err error, code string) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}

func TestRestoreRetainedCandidateReviewAndRetry(t *testing.T) {
	ctx := context.Background()
	original, device, config, update, _, _ := reviewManagedPending(t)
	pending := original.Status()
	if _, err := original.ReviewRestore(ctx, pending.TransactionID, pending.PlanChecksum); err == nil {
		t.Fatal("original Safe Mode owner became restore authority")
	}
	device.mu.Lock()
	device.noUndo = true
	device.mu.Unlock()
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	fresh := reviewFreshDevice(device)
	controller, err := Open(config, fresh)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	if _, err = controller.Recover(ctx); err == nil {
		t.Fatal("fixture did not retain the original candidate")
	}
	requireRestoreCode(t, err, UnconfirmedCandidateRetainedCode)
	sequence := controller.log.sequence
	preview, err := controller.ReviewRestore(ctx, pending.TransactionID, pending.PlanChecksum)
	if err != nil || preview.Plan.Generation != update.Base.ActiveGeneration || preview.Plan.Base.ActiveGeneration != update.Generation ||
		preview.TransactionID != pending.TransactionID || preview.PlanChecksum != pending.PlanChecksum || preview.ObservedAt.IsZero() {
		t.Fatal("restore preview did not rebind the known original policy", err)
	}
	raw, _ := json.Marshal(preview)
	for _, credential := range []string{"review-original-A", "review-replacement-B", "review-added-C"} {
		if strings.Contains(string(raw), credential) {
			t.Fatal("restore preview exposed a credential")
		}
	}
	if controller.log.sequence != sequence || fresh.applyCount != 0 || fresh.takeCount != 0 || fresh.bindCount != 0 {
		t.Fatal("restore preview changed persistent state or device")
	}
	if _, err = controller.Review(ctx); err == nil {
		t.Fatal("ordinary review accepted the unconfirmed candidate")
	}
	request := RestoreRequest{
		TransactionID: pending.TransactionID, PlanChecksum: pending.PlanChecksum, RestoreChecksum: preview.Plan.Checksum,
		Credentials: routeros.Credentials{"old": {Revision: strings.Repeat("A", 22), Password: "review-original-A"}},
		BaselineCredentials: routeros.Credentials{
			"old":   {Revision: strings.Repeat("B", 22), Password: "review-replacement-B"},
			"added": {Revision: strings.Repeat("C", 22), Password: "review-added-C"},
		},
		Timeout: 10 * time.Second, ExclusiveAccessAcknowledged: true,
	}
	wrong := request
	wrong.RestoreChecksum = strings.Repeat("0", 64)
	_, err = controller.StageRestore(ctx, wrong)
	requireRestoreCode(t, err, "restore-review-changed")
	wrong = request
	wrong.Credentials = routeros.Credentials{"old": {Revision: strings.Repeat("A", 22), Password: "review-replacement-B"}}
	_, err = controller.StageRestore(ctx, wrong)
	requireRestoreCode(t, err, "restore-original-credential-mismatch")
	if fresh.applyCount != 0 || fresh.takeCount != 0 || controller.log.sequence != sequence {
		t.Fatal("bad authorization caused a restore write")
	}
	// The device model enforces HMAC read authority during read-only review. An
	// ordinary live stage independently proves the configured baseline passwords.
	fresh.requireAuthority = false
	active, err := controller.StageRestore(ctx, request)
	if err != nil || active.Phase != AwaitingConfirmation || active.TransactionID == pending.TransactionID {
		t.Fatal("new restore transaction did not stage", err)
	}
	if _, err = controller.Confirm(ctx, pending.TransactionID, pending.PlanChecksum); err == nil {
		t.Fatal("old confirmation authorized restoration")
	}
	reverted, err := controller.Cancel(ctx, active.TransactionID)
	if err != nil || reverted.Phase != RolledBack {
		t.Fatal("cancel did not verify the retained candidate again", err)
	}
	if err = controller.Close(); err != nil {
		t.Fatal(err)
	}
	retryDevice := reviewFreshDevice(fresh)
	retry, err := Open(config, retryDevice)
	if err != nil {
		t.Fatal(err)
	}
	defer retry.Close()
	if replayed := retry.Status(); replayed.Phase != RolledBack || !replayed.RollbackVerified || retry.log.poisoned {
		t.Fatal("restore link did not replay the interrupted restoration", replayed)
	}
	_, err = retry.Review(ctx)
	requireRestoreCode(t, err, "restore-requires-resolution")
	_, err = retry.Stage(ctx, Request{})
	requireRestoreCode(t, err, "restore-requires-resolution")
	if _, err = retry.StageRestore(ctx, request); err == nil {
		t.Fatal("first restore approval survived a transaction change")
	}
	preview, err = retry.ReviewRestore(ctx, reverted.TransactionID, reverted.PlanChecksum)
	if err != nil || preview.Plan.Generation != update.Base.ActiveGeneration {
		t.Fatal("could not review the original baseline after cancelling restoration", err)
	}
	request.TransactionID, request.PlanChecksum, request.RestoreChecksum = reverted.TransactionID, reverted.PlanChecksum, preview.Plan.Checksum
	retryDevice.requireAuthority = false
	active, err = retry.StageRestore(ctx, request)
	if err != nil || active.Phase != AwaitingConfirmation {
		t.Fatal("fresh approved retry did not stage the original policy", err)
	}
	confirmed, err := retry.Confirm(ctx, active.TransactionID, active.PlanChecksum)
	if err != nil || confirmed.Phase != Confirmed || !confirmed.ConfirmationVerified {
		t.Fatal("restore confirmation was not independently verified", err)
	}
	for _, operation := range preview.Plan.Operations {
		if operation.Phase == routeros.Prepare {
			t.Fatal("original policy should reuse its retained objects")
		}
	}
}
