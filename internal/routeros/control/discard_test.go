package control

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routing"
)

type preparedDiscardDevice struct {
	*reviewDevice
	discardBinds, discardWrites int
	usedPlan, poisoned          bool
	rejectGate                  bool
	noEffect, lostACK           bool
	batch                       bool
	bridge                      *routeros.APIBridge
	remaining                   []string
	removed                     []string
	missingGrantReads           int
}

var discardSessionSequence atomic.Uint64

func (r *preparedDiscardDevice) Binding() Binding {
	b := r.memoryRuntime.Binding()
	b.PreparedDiscardVerified = !r.rejectGate
	b.RuleBatchVerified = r.batch
	return b
}
func (r *preparedDiscardDevice) BindPlan(p routeros.Plan, credentials routeros.Credentials) error {
	if r.poisoned || len(r.remaining) > 0 {
		return errors.New("fixture runtime not reusable")
	}
	r.usedPlan = true
	return r.memoryRuntime.BindPlan(p, credentials)
}
func (r *preparedDiscardDevice) Observe(ctx context.Context, request ReadRequest) (Observation, error) {
	// Mirror the live reader: a deleted target cannot satisfy an old credential
	// grant. Only the controller's pending-ID exclusion may recover that read.
	r.mu.Lock()
	ids := map[string]bool{}
	for _, row := range r.inv.Connections {
		ids[row[".id"]] = true
	}
	for _, grant := range request.CredentialAuthority {
		if !ids[grant.ID] {
			r.missingGrantReads++
			r.mu.Unlock()
			return Observation{}, errors.New("fixture credential grant target missing")
		}
	}
	r.mu.Unlock()
	return r.reviewDevice.Observe(ctx, request)
}
func (r *preparedDiscardDevice) BindPreparedDiscard(ctx context.Context, b PreparedDiscardBinding) error {
	r.discardBinds++
	if ctx.Err() != nil || r.usedPlan || r.poisoned || r.owner.Active || !r.owner.Alive || b.Plan.Target != r.target {
		return errors.New("fixture discard requires a fresh idle runtime")
	}
	bridge, err := routeros.NewPreparedDiscardBridge(b.Plan, b.Objects)
	if err != nil {
		return err
	}
	all := bridge.Operations()
	offset := len(all) - len(b.RemainingOperationIDs)
	if offset < 0 {
		return errors.New("fixture invalid discard suffix")
	}
	for i, id := range b.RemainingOperationIDs {
		if all[offset+i].ID != id {
			return errors.New("fixture invalid discard suffix")
		}
	}
	r.bridge, r.remaining = bridge, append([]string{}, b.RemainingOperationIDs...)
	return nil
}
func (r *preparedDiscardDevice) ApplyPreparedDiscard(ctx context.Context, m routeros.APIMutation) (api.Result, error) {
	if r.poisoned || r.bridge == nil || len(r.remaining) == 0 || r.remaining[0] != m.OperationID || m.Phase != routeros.DiscardPrepared || m.Request.Action != api.Remove {
		return api.Result{}, errors.New("fixture discard order or scope")
	}
	r.discardWrites++
	if r.noEffect {
		r.poisoned = true
		return api.Result{}, errors.New("fixture unacknowledged request with no effect")
	}
	result, err := r.reviewDevice.Apply(ctx, m)
	if err == nil {
		r.removed = append(r.removed, m.OperationID)
	}
	if r.lostACK {
		r.poisoned = true
		return api.Result{}, errors.New("fixture lost acknowledgement")
	}
	if err == nil {
		r.remaining = r.remaining[1:]
	}
	return result, err
}
func (r *preparedDiscardDevice) ApplyRuleBatch(ctx context.Context, batch RuleBatch, recorder RuleBatchRecorder) error {
	for _, operation := range batch.Operations {
		if err := recorder.Before(operation.OperationID); err != nil {
			return err
		}
		result, writeErr := r.reviewDevice.Apply(ctx, operation)
		if err := recorder.After(operation.OperationID, result, writeErr); err != nil {
			return err
		}
		if writeErr != nil {
			return writeErr
		}
	}
	return nil
}
func freshDiscardDevice(old *reviewDevice) *preparedDiscardDevice {
	r := reviewFreshDevice(old)
	r.owner.SessionID = fmt.Sprintf("discard-fresh-session-%032d", discardSessionSequence.Add(1))
	return &preparedDiscardDevice{reviewDevice: r}
}
func discardRequest(s *Supervisor) DiscardRequest {
	status := s.Status()
	return DiscardRequest{TransactionID: status.TransactionID, PlanChecksum: status.PlanChecksum, ExclusiveAccessAcknowledged: true}
}
func openDiscardSupervisor(t *testing.T, config Config, runtime *preparedDiscardDevice) *Supervisor {
	t.Helper()
	s, err := Open(config, runtime)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func rolledBackBootstrapForDiscard(t *testing.T, batch bool) (*Supervisor, *preparedDiscardDevice, Config, Request) {
	t.Helper()
	model, request := fixtureRuntime(t)
	r := &preparedDiscardDevice{reviewDevice: &reviewDevice{memoryRuntime: model, secrets: map[string]string{}}, batch: batch}
	config := testConfig(t, model)
	config.PollInterval = 10 * time.Second
	s := openDiscardSupervisor(t, config, r)
	_, initial, err := s.observe(context.Background(), ReadRequest{KnownState: request.KnownState, Strict: true})
	if err != nil {
		t.Fatal(err)
	}
	baseline := stateMAC(s.log.key, initial)
	status, err := s.Stage(context.Background(), request)
	if err != nil || status.Phase != AwaitingConfirmation {
		t.Fatal("fixture stage failed", err)
	}
	if stateMAC(s.log.key, s.begin.Baseline) != baseline {
		t.Fatal("Prepare/Activate mutated the immutable original baseline")
	}
	status, err = s.Cancel(context.Background(), status.TransactionID)
	if err != nil || status.Phase != RolledBack || !status.RollbackVerified || stateMAC(s.log.key, s.begin.Baseline) != baseline {
		t.Fatal("cancel failed or original baseline was aliased", err)
	}
	return s, r, config, request
}

func TestPreparedDiscardBootstrapRecoversExactEmptyBaselineAndRestages(t *testing.T) {
	for _, batch := range []bool{false, true} {
		t.Run(fmt.Sprintf("batch-%t", batch), func(t *testing.T) {
			old, device, config, initial := rolledBackBootstrapForDiscard(t, batch)
			if _, err := old.DiscardPrepared(context.Background(), discardRequest(old)); err == nil || device.discardWrites != 0 || device.discardBinds != 0 {
				t.Fatal("cancelled runtime became a discard authority")
			}
			old.Close()
			fresh := freshDiscardDevice(device.reviewDevice)
			s := openDiscardSupervisor(t, config, fresh)
			if !s.reopened || len(s.preparedCreates) != 7 {
				t.Fatal("authenticated Prepare add/readback provenance was not replayed")
			}
			for _, bad := range []DiscardRequest{{TransactionID: s.Status().TransactionID, PlanChecksum: s.Status().PlanChecksum}, {TransactionID: "wrong", PlanChecksum: s.Status().PlanChecksum, ExclusiveAccessAcknowledged: true}, {TransactionID: s.Status().TransactionID, PlanChecksum: "wrong", ExclusiveAccessAcknowledged: true}} {
				if _, err := s.DiscardPrepared(context.Background(), bad); err == nil {
					t.Fatal("unbound discard request accepted")
				}
			}
			if fresh.discardWrites != 0 || fresh.discardBinds != 0 {
				t.Fatal("invalid request reached the runtime")
			}
			status, err := s.DiscardPrepared(context.Background(), discardRequest(s))
			if err != nil || status.Phase != RolledBack || !status.RollbackVerified || status.PreparedDiscard.Phase != PreparedDiscardComplete || status.PreparedArtifactsRetained || status.PreparedDiscard.Completed != 7 || status.PreparedDiscard.Total != 7 {
				t.Fatalf("discard did not restore the original empty baseline: %+v %v", status, err)
			}
			if fresh.discardWrites != 7 || fresh.takeCount != 0 || fresh.confirmCount != 0 || fresh.cancelCount != 0 || fresh.bindCount != 0 || stateMAC(s.log.key, s.current) != stateMAC(s.log.key, s.begin.Baseline) {
				t.Fatal("discard activated, changed a baseline, or skipped approved removals")
			}
			before := [2]int{fresh.discardBinds, fresh.discardWrites}
			if _, err := s.DiscardPrepared(context.Background(), discardRequest(s)); err != nil {
				t.Fatal(err)
			}
			if after := [2]int{fresh.discardBinds, fresh.discardWrites}; after != before {
				t.Fatal("completed discard repeated a binding or Remove")
			}
			review, err := s.Review(context.Background())
			if err != nil || !review.Snapshot.NamespaceEmpty || len(review.KnownState.Peers) != 0 || len(review.KnownState.Generations) != 0 {
				t.Fatal("discarded bootstrap could not provide a strict empty review", err)
			}
			router := routing.Router{ID: initial.Plan.Target.RouterID, ASN: initial.Plan.Desired.ASN, RouterID: initial.Plan.Desired.RouterID, LocalIPv4: initial.Plan.Desired.LocalIPv4}
			plan, err := routeros.BuildPlan(initial.Plan.Target, router, nil, review.Snapshot, routeros.PlanOptions{Bootstrap: true})
			if err != nil {
				t.Fatal(err)
			}
			fresh.requireAuthority = false // New Stage uses its newly bound plan; no old secret rows exist.
			if _, err := s.Stage(context.Background(), Request{Plan: plan, KnownState: review.KnownState, Timeout: initial.Timeout, ExclusiveAccessAcknowledged: true}); err != nil {
				t.Fatal("new explicit Stage failed after complete discard", err)
			}
		})
	}
}

func TestPreparedDiscardManagedUpdatePreservesOldAFAndAuthentication(t *testing.T) {
	old, device, config, _, _, _ := reviewManagedRollback(t)
	baseline := cloneState(old.begin.Baseline)
	baselineKnown := old.begin.Known
	old.Close()
	fresh := freshDiscardDevice(device)
	s := openDiscardSupervisor(t, config, fresh)
	status, err := s.DiscardPrepared(context.Background(), discardRequest(s))
	if err != nil || status.PreparedDiscard.Phase != PreparedDiscardComplete || stateMAC(s.log.key, s.current) != stateMAC(s.log.key, baseline) {
		t.Fatal("managed update did not return exactly to the original baseline", err)
	}
	if fresh.takeCount != 0 || fresh.confirmCount != 0 || fresh.cancelCount != 0 || fresh.bindCount != 0 {
		t.Fatal("discard changed activation ownership")
	}
	if len(fresh.inv.Connections) != 1 || fresh.inv.Connections[0]["afi"] != "ip" || fresh.secrets[fresh.inv.Connections[0][".id"]] != "review-original-A" {
		t.Fatal("discard lost the original IPv4/A connection or retained the new peer")
	}
	if fresh.missingGrantReads == 0 {
		t.Fatal("deleted credential target did not exercise exact pending-ID reconciliation")
	}
	review, err := s.Review(context.Background())
	if err != nil || !reflect.DeepEqual(review.KnownState, baselineKnown) || len(review.Snapshot.Connections) != 1 || review.Snapshot.Connections[0].Config.Authentication.Revision != strings.Repeat("A", 22) {
		t.Fatal("final review merged desired/new-peer authentication into the baseline", err)
	}
	for _, removal := range fresh.removed {
		prepareID := strings.TrimPrefix(removal, "discard-")
		proof := s.preparedCreates[prepareID]
		if _, existed := baseline.Objects[objectKey(proof.Resource, proof.ID)]; existed {
			t.Fatal("discard removed a baseline object")
		}
	}
}

func TestPreparedDiscardUnknownRemoveNeverResends(t *testing.T) {
	for _, effect := range []bool{false, true} {
		t.Run(fmt.Sprintf("effect-%t", effect), func(t *testing.T) {
			old, device, config, _ := rolledBackBootstrapForDiscard(t, false)
			old.Close()
			fresh := freshDiscardDevice(device.reviewDevice)
			fresh.noEffect, fresh.lostACK = !effect, effect
			s := openDiscardSupervisor(t, config, fresh)
			if _, err := s.DiscardPrepared(context.Background(), discardRequest(s)); err == nil || fresh.discardWrites != 1 {
				t.Fatal("unknown removal was accepted or retried")
			}
			if _, err := s.DiscardPrepared(context.Background(), discardRequest(s)); err == nil || fresh.discardWrites != 1 {
				t.Fatal("same uncertain runtime resent a Remove")
			}
			s.Close()
			restarted := freshDiscardDevice(fresh.reviewDevice)
			next := openDiscardSupervisor(t, config, restarted)
			_, err := next.Recover(context.Background())
			if restarted.discardBinds != 0 || restarted.discardWrites != 0 {
				t.Fatal("recovery bound or sent a removal")
			}
			if !effect {
				if err == nil {
					t.Fatal("still-present unknown target considered removed")
				}
				if _, err := next.DiscardPrepared(context.Background(), discardRequest(next)); err == nil || restarted.discardWrites != 0 || restarted.discardBinds != 0 {
					t.Fatal("fresh runtime resent an unresolved Remove")
				}
				return
			}
			if err != nil || next.Status().PreparedDiscard.Completed != 1 {
				t.Fatal("exact applied removal was not reconciled", err)
			}
			status, err := next.DiscardPrepared(context.Background(), discardRequest(next))
			if err != nil || status.PreparedDiscard.Phase != PreparedDiscardComplete || restarted.discardWrites != 6 {
				t.Fatal("explicit remaining suffix did not complete", err)
			}
			for _, id := range restarted.removed {
				if id == fresh.removed[0] {
					t.Fatal("resolved unknown removal was sent again")
				}
			}
		})
	}
}

func TestPreparedDiscardJournalBoundariesRecoverWithoutWrites(t *testing.T) {
	for _, kind := range []string{"discard-start", "discard-intent", "discard-result", "discard-readback", "discard-complete"} {
		t.Run(kind, func(t *testing.T) {
			old, device, config, _ := rolledBackBootstrapForDiscard(t, false)
			old.Close()
			fresh := freshDiscardDevice(device.reviewDevice)
			s := openDiscardSupervisor(t, config, fresh)
			s.log.fail = func(current string) error {
				if current == kind {
					return errors.New("fixture durable write failure")
				}
				return nil
			}
			if status, err := s.DiscardPrepared(context.Background(), discardRequest(s)); err == nil || status.Phase != RolledBack || !status.RollbackVerified {
				t.Fatal("failed discard erased the verified rollback fact")
			}
			writes := fresh.discardWrites
			if (kind == "discard-start" || kind == "discard-intent") && writes != 0 {
				t.Fatal("native deletion preceded durable authorization")
			}
			s.Close()
			restarted := freshDiscardDevice(fresh.reviewDevice)
			next := openDiscardSupervisor(t, config, restarted)
			if _, err := next.Recover(context.Background()); err != nil {
				t.Fatal("valid durable prefix could not recover", err)
			}
			if restarted.discardWrites != 0 || restarted.discardBinds != 0 || restarted.takeCount != 0 {
				t.Fatal("reopen/recovery performed an implicit mutation")
			}
			if kind == "discard-complete" && next.Status().PreparedDiscard.Phase != PreparedDiscardComplete {
				t.Fatal("complete baseline was not independently re-proved")
			}
			if _, err := next.DiscardPrepared(context.Background(), discardRequest(next)); err != nil {
				t.Fatal("explicit continuation of a proved durable prefix failed", err)
			}
			if restarted.discardWrites+writes != 7 {
				t.Fatal("durable recovery repeated or omitted an original target")
			}
		})
	}
}

func TestPreparedDiscardRejectsDriftMissingProvenanceAndStageDuringCleanup(t *testing.T) {
	for _, scenario := range []string{"external", "target", "owner", "provenance", "gate"} {
		t.Run(scenario, func(t *testing.T) {
			old, device, config, request := rolledBackBootstrapForDiscard(t, false)
			old.Close()
			fresh := freshDiscardDevice(device.reviewDevice)
			s := openDiscardSupervisor(t, config, fresh)
			switch scenario {
			case "external":
				fresh.inv.FilterRules = append(fresh.inv.FilterRules, api.Record{".id": "*FFFFFF", "chain": "outside", "comment": "unrelated", "rule": "reject;", "disabled": "false", "inactive": "false"})
			case "target":
				fresh.inv.FilterRules[0]["rule"] = "accept;"
			case "owner":
				fresh.owner.Active = true
			case "provenance":
				delete(s.preparedCreates, "op-000001")
			case "gate":
				fresh.rejectGate = true
			}
			if _, err := s.DiscardPrepared(context.Background(), discardRequest(s)); err == nil || fresh.discardWrites != 0 || fresh.discardBinds != 0 {
				t.Fatal("unverified target reached discard writes")
			}
			// An active discard cannot be bypassed by supplying another plan.
			s.status.PreparedDiscard.Phase = PreparedDiscardPending
			if _, err := s.Stage(context.Background(), request); err == nil || fresh.bindCount != 0 {
				t.Fatal("Stage bypassed unfinished discard")
			}
		})
	}
}

func TestPreparedDiscardZeroAddsStillProvesOriginalStrictBaseline(t *testing.T) {
	model, initial := fixtureRuntime(t)
	config := testConfig(t, model)
	config.PollInterval = 10 * time.Second
	first := openTest(t, config, model)
	status, err := first.Stage(context.Background(), initial)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = first.Confirm(context.Background(), status.TransactionID, status.PlanChecksum); err != nil {
		t.Fatal(err)
	}
	baseline, err := first.Review(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	router := routing.Router{ID: initial.Plan.Target.RouterID, ASN: initial.Plan.Desired.ASN, RouterID: initial.Plan.Desired.RouterID, LocalIPv4: initial.Plan.Desired.LocalIPv4}
	plan, err := routeros.BuildPlan(initial.Plan.Target, router, nil, baseline.Snapshot, routeros.PlanOptions{})
	if err != nil || len(plan.Operations) != 0 {
		t.Fatal("fixture plan unexpectedly has operations", err)
	}
	status, err = first.Stage(context.Background(), Request{Plan: plan, KnownState: baseline.KnownState, Timeout: initial.Timeout, ExclusiveAccessAcknowledged: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Cancel(context.Background(), status.TransactionID); err != nil {
		t.Fatal(err)
	}
	first.Close()
	fresh := freshDiscardDevice(&reviewDevice{memoryRuntime: model, secrets: map[string]string{}})
	s := openDiscardSupervisor(t, config, fresh)
	if len(s.preparedCreates) != 0 {
		t.Fatal("zero-add transaction retained a prior transaction's provenance")
	}
	status, err = s.DiscardPrepared(context.Background(), discardRequest(s))
	if err != nil || status.PreparedDiscard.Phase != PreparedDiscardComplete || status.PreparedDiscard.Total != 0 || status.PreparedDiscard.Completed != 0 || status.PreparedArtifactsRetained {
		t.Fatal("zero-add discard was not proved complete", err)
	}
	if fresh.discardWrites != 0 || fresh.applyCount != 0 || fresh.takeCount != 0 || fresh.confirmCount != 0 {
		t.Fatal("zero-add proof executed a mutation")
	}
	review, err := s.Review(context.Background())
	if err != nil || !reflect.DeepEqual(review.KnownState, baseline.KnownState) || !reflect.DeepEqual(review.Snapshot, baseline.Snapshot) {
		t.Fatal("zero-add discard did not preserve the original active baseline", err)
	}
}

func TestPreparedDiscardReplayCannotOverwriteAnUnfinishedSuffix(t *testing.T) {
	old, device, config, request := rolledBackBootstrapForDiscard(t, false)
	old.Close()
	fresh := freshDiscardDevice(device.reviewDevice)
	fresh.lostACK = true
	s := openDiscardSupervisor(t, config, fresh)
	if _, err := s.DiscardPrepared(context.Background(), discardRequest(s)); err == nil || s.preparedDiscard.Index != 1 {
		t.Fatal("fixture did not leave an unfinished discard")
	}
	before := s.Status()
	begin := &beginRecord{Plan: request.Plan, Known: request.KnownState, Baseline: cloneState(s.begin.Baseline), Timeout: request.Timeout}
	status := Status{TransactionID: strings.Repeat("f", 32), PlanChecksum: request.Plan.Checksum, Phase: Preparing}
	if err := s.replay(event{Kind: "begin", Begin: begin, Status: &status}); err == nil || s.Status() != before || s.preparedDiscard.Index != 1 {
		t.Fatal("replayed begin discarded an unfinished removal authorization")
	}
}
