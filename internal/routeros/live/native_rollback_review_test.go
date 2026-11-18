package live

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routeros/terminal"
	"github.com/moedove/ixp-manager/internal/routing"
)

type nativeReviewReuseCounts struct {
	readOnly  atomic.Bool
	forbidden atomic.Int64
	addRules  atomic.Int64
	confirms  atomic.Int64
}

func (c *nativeReviewReuseCounts) permitted() error {
	if c.readOnly.Load() {
		c.forbidden.Add(1)
		return fail("native-review-reuse-forbidden-call")
	}
	return nil
}

type nativeReviewReuseAPI struct {
	apiChannel
	counts *nativeReviewReuseCounts
}

func (a *nativeReviewReuseAPI) Apply(ctx context.Context, mutation api.Mutation) (api.Result, error) {
	if err := a.counts.permitted(); err != nil {
		return api.Result{}, err
	}
	if mutation.Resource == api.FilterRule && mutation.Action == api.Add {
		a.counts.addRules.Add(1)
	}
	return a.apiChannel.Apply(ctx, mutation)
}
func (a *nativeReviewReuseAPI) ReadSafeMode(ctx context.Context) (api.SafeModeState, error) {
	reader, ok := a.apiChannel.(safeModeReader)
	if !ok {
		return api.SafeModeState{}, fail("native-review-reuse-owner-reader-unavailable")
	}
	return reader.ReadSafeMode(ctx)
}
func (a *nativeReviewReuseAPI) ReadProperties(ctx context.Context, requests []api.PropertyRequest) ([]api.PropertyValue, error) {
	reader, ok := a.apiChannel.(propertyBatchReader)
	if !ok {
		return nil, fail("native-review-reuse-property-batch-unavailable")
	}
	return reader.ReadProperties(ctx, requests)
}

type nativeReviewReuseTerminal struct {
	terminalChannel
	counts *nativeReviewReuseCounts
}

func (t *nativeReviewReuseTerminal) EnterSafeMode(ctx context.Context) (terminal.Evidence, error) {
	if err := t.counts.permitted(); err != nil {
		return terminal.Evidence{}, err
	}
	return t.terminalChannel.EnterSafeMode(ctx)
}
func (t *nativeReviewReuseTerminal) ConfirmSafeMode(ctx context.Context) (terminal.Confirmation, error) {
	if err := t.counts.permitted(); err != nil {
		return terminal.Confirmation{}, err
	}
	return t.terminalChannel.ConfirmSafeMode(ctx)
}
func (t *nativeReviewReuseTerminal) CancelSafeMode(ctx context.Context) (terminal.Cancellation, error) {
	if err := t.counts.permitted(); err != nil {
		return terminal.Cancellation{}, err
	}
	return t.terminalChannel.CancelSafeMode(ctx)
}
func (t *nativeReviewReuseTerminal) ExecuteOwned(ctx context.Context, mutation api.Mutation) (terminal.OwnedReceipt, error) {
	if err := t.counts.permitted(); err != nil {
		return terminal.OwnedReceipt{}, err
	}
	writer, ok := t.terminalChannel.(ownedMutationChannel)
	if !ok {
		return terminal.OwnedReceipt{}, fail("native-review-reuse-forbidden-call")
	}
	return writer.ExecuteOwned(ctx, mutation)
}

// No optional cleanup or batching interface is promoted into this fixture.
type nativeReviewReuseRuntime struct {
	runtime     *Runtime
	counts      *nativeReviewReuseCounts
	known       routeros.KnownState
	credentials routeros.Credentials
	checkpoint  *control.Observation
	last        control.Observation
}

func (r *nativeReviewReuseRuntime) Binding() control.Binding { return r.runtime.Binding() }
func (r *nativeReviewReuseRuntime) Observe(ctx context.Context, request control.ReadRequest) (control.Observation, error) {
	observed, err := r.runtime.Observe(ctx, request)
	if err == nil {
		r.last = observed
	}
	return observed, err
}
func (r *nativeReviewReuseRuntime) Owner(ctx context.Context) (control.Owner, error) {
	return r.runtime.Owner(ctx)
}
func (r *nativeReviewReuseRuntime) CloseOwner() error { return r.runtime.CloseOwner() }
func (r *nativeReviewReuseRuntime) BindPlan(plan routeros.Plan, credentials routeros.Credentials) error {
	if err := r.counts.permitted(); err != nil {
		return err
	}
	return r.runtime.BindPlan(plan, credentials)
}
func (r *nativeReviewReuseRuntime) ResetIdle(ctx context.Context) error {
	if err := r.counts.permitted(); err != nil {
		return err
	}
	if err := r.runtime.ResetIdle(ctx); err != nil {
		return err
	}
	r.runtime.terminal = &nativeReviewReuseTerminal{terminalChannel: r.runtime.terminal, counts: r.counts}
	return nil
}
func (r *nativeReviewReuseRuntime) Apply(ctx context.Context, mutation routeros.APIMutation) (api.Result, error) {
	if err := r.counts.permitted(); err != nil {
		return api.Result{}, err
	}
	return r.runtime.Apply(ctx, mutation)
}
func (r *nativeReviewReuseRuntime) Take(ctx context.Context, deadline time.Time) (control.Owner, error) {
	if err := r.counts.permitted(); err != nil {
		return control.Owner{}, err
	}
	checkpoint, err := r.runtime.Observe(ctx, control.ReadRequest{KnownState: r.known, BaselineCredentials: r.credentials})
	if err != nil {
		return control.Owner{}, err
	}
	if !checkpoint.Owner.Known || checkpoint.Owner.Active || !checkpoint.Owner.HistoryKnown || len(checkpoint.Credentials) != len(r.known.Peers) {
		return control.Owner{}, fail("native-review-reuse-checkpoint-unverified")
	}
	for _, peer := range r.known.Peers {
		name, err := routeros.ManagedConnectionName(r.runtime.target.RouterID, peer.ID)
		if err != nil {
			return control.Owner{}, fail("native-review-reuse-checkpoint-unverified")
		}
		found := false
		for _, row := range checkpoint.Inventory.Connections {
			if row["name"] == name {
				observed, present := checkpoint.Credentials[row[".id"]]
				found = present && observed.Matches([]byte(r.credentials[peer.ID].Password))
			}
		}
		if !found {
			return control.Owner{}, fail("native-review-reuse-checkpoint-unverified")
		}
	}
	r.checkpoint = &checkpoint
	return r.runtime.Take(ctx, deadline)
}
func (r *nativeReviewReuseRuntime) Confirm(ctx context.Context) (control.Owner, error) {
	if err := r.counts.permitted(); err != nil {
		return control.Owner{}, err
	}
	r.counts.confirms.Add(1)
	return r.runtime.Confirm(ctx)
}
func (r *nativeReviewReuseRuntime) Cancel(ctx context.Context) (control.Owner, error) {
	if err := r.counts.permitted(); err != nil {
		return control.Owner{}, err
	}
	return r.runtime.Cancel(ctx)
}

func nativeReviewReuseStages(runtime *Runtime) {
	runtime.profile.PreparationVerified = true
	runtime.profile.ActivationVerified = true
	runtime.profile.ConfirmNativeVerified = true
	runtime.profile.MaxHistoryActions = 80
	runtime.profile.RuleBatchVerified = false
	runtime.profile.CleanupVerified = false
	runtime.profile.ObjectObservationVerified = false
	runtime.profile.PreparedDiscardVerified = false
	runtime.profile.RuleRemovalBatchVerified = false
}

func nativeReviewReuseKnown(plan routeros.Plan) routeros.KnownState {
	known := routeros.KnownState{Target: plan.Target, ActiveGeneration: plan.Generation, Generations: []routeros.KnownGeneration{{Target: plan.Target, Desired: plan.Desired, PolicyChecksum: plan.PolicyChecksum}}}
	for _, peer := range plan.Desired.Peers {
		known.Peers = append(known.Peers, routeros.KnownPeer{ID: peer.ID, Authentication: peer.Authentication})
	}
	return known
}

func nativeReviewReuseIDs(snapshot routeros.Snapshot) map[string]string {
	ids := map[string]string{"table": snapshot.Table.ID, "instance": snapshot.Instance.ID, "root-in": snapshot.InputEntry.ID, "root-out": snapshot.OutputEntry.ID}
	for _, connection := range snapshot.Connections {
		ids["connection/"+connection.Config.PeerID] = connection.Ref.ID
	}
	for _, generation := range snapshot.Generations {
		for _, rule := range generation.Rules {
			ids["rule/"+rule.Comment] = rule.ID
		}
	}
	return ids
}

func nativeReviewReusePreservesIDs(before, after map[string]string) bool {
	for key, id := range before {
		if id == "" || after[key] != id {
			return false
		}
	}
	return true
}

func nativeReviewReuseFail(t *testing.T, field string, err error) {
	t.Helper()
	code := nativeSupervisorCode(err)
	if code == "unclassified" {
		code = "native-review-reuse-unclassified"
	}
	t.Fatalf("%s=false code=%s", field, code)
}

func TestNativeCHRLiveRollbackReviewReuse(t *testing.T) {
	cfg := nativeLiveConfig(t)
	cfg.RouterID = "native-rollback-review-reuse"
	cfg.SessionLifetimeSeconds, cfg.ObservationTimeoutSeconds, cfg.OperationTimeoutSeconds = 600, 120, 15
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	originalRuntime, err := Open(ctx, cfg)
	if err != nil {
		nativeReviewReuseFail(t, "review_reuse_first_open", err)
	}
	defer originalRuntime.Close()
	nativeReviewReuseStages(originalRuntime)
	target := originalRuntime.target
	empty := routeros.KnownState{Target: target}
	initial, err := originalRuntime.Observe(ctx, control.ReadRequest{KnownState: empty, Strict: true})
	if err != nil || initial.Snapshot == nil || !initial.Snapshot.NamespaceEmpty {
		nativeReviewReuseFail(t, "review_reuse_empty_baseline", err)
	}
	router := routing.Router{ID: target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1"}
	// A real non-empty synthetic MD5 key makes preservation of the old
	// credential revision and opaque getter evidence meaningful.
	peers := []routing.Peer{{ID: "review-peer", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"203.0.113.0/24"}, AuthenticationRevision: strings.Repeat("A", 22), Password: "NativeReviewBaseline_A"}}
	credentials := routeros.Credentials{"review-peer": {Revision: peers[0].AuthenticationRevision, Password: peers[0].Password}}
	first, err := routeros.BuildPlan(target, router, peers, *initial.Snapshot, routeros.PlanOptions{Bootstrap: true})
	if err != nil {
		nativeReviewReuseFail(t, "review_reuse_first_plan", err)
	}
	known := nativeReviewReuseKnown(first)
	original := &nativeReviewReuseRuntime{runtime: originalRuntime, counts: &nativeReviewReuseCounts{}, known: known, credentials: credentials}
	dir, err := os.MkdirTemp("/scratch/tmp", "native-review-reuse-journal-")
	if err != nil {
		nativeReviewReuseFail(t, "review_reuse_journal", err)
	}
	defer os.RemoveAll(dir)
	config := control.Config{Dir: dir, Target: target, OperationTimeout: 30 * time.Second, PrepareTimeout: 3 * time.Minute, MinConfirmationTimeout: 10 * time.Second, MaxConfirmationTimeout: 2 * time.Minute, PollInterval: time.Second}
	supervisor, err := control.Open(config, original)
	if err != nil {
		nativeReviewReuseFail(t, "review_reuse_first_supervisor", err)
	}
	defer supervisor.Close()
	status, err := supervisor.Stage(ctx, control.Request{Plan: first, KnownState: empty, Credentials: credentials, Timeout: 90 * time.Second, ExclusiveAccessAcknowledged: true})
	if err != nil || status.Phase != control.AwaitingConfirmation {
		nativeReviewReuseFail(t, "review_reuse_baseline_stage", err)
	}
	status, err = supervisor.Confirm(ctx, status.TransactionID, first.Checksum)
	if err != nil || status.Phase != control.Confirmed || !status.ConfirmationVerified {
		nativeReviewReuseFail(t, "review_reuse_baseline_confirmed", err)
	}
	baseline, err := originalRuntime.Observe(ctx, control.ReadRequest{KnownState: known, BaselineCredentials: credentials, Strict: true})
	if err != nil || baseline.Snapshot == nil || !baseline.Snapshot.Managed || len(baseline.Snapshot.Connections) != 1 || !baseline.Snapshot.Connections[0].AuthenticationVerified {
		nativeReviewReuseFail(t, "review_reuse_baseline_strict", err)
	}
	baselineIDs := nativeReviewReuseIDs(*baseline.Snapshot)
	peers[0].AllowedPrefixes = []string{"198.51.100.0/24"}
	update, err := routeros.BuildPlan(target, router, peers, *baseline.Snapshot, routeros.PlanOptions{})
	if err != nil || update.Generation == first.Generation {
		nativeReviewReuseFail(t, "review_reuse_update_plan", err)
	}
	preparedKnown := nativeReviewReuseKnown(first)
	preparedKnown.Generations = append(preparedKnown.Generations, routeros.KnownGeneration{Target: target, Desired: update.Desired, PolicyChecksum: update.PolicyChecksum})
	original.known = preparedKnown
	status, err = supervisor.Stage(ctx, control.Request{Plan: update, KnownState: known, BaselineCredentials: credentials, Timeout: 90 * time.Second, ExclusiveAccessAcknowledged: true})
	if err != nil || status.Phase != control.AwaitingConfirmation || original.checkpoint == nil {
		nativeReviewReuseFail(t, "review_reuse_update_stage", err)
	}
	oldTransaction := status.TransactionID
	oldSession := status.SessionID
	checkpoint := *original.checkpoint
	status, err = supervisor.Cancel(ctx, oldTransaction)
	until := time.Now().Add(30 * time.Second)
	for status.Phase != control.RolledBack && time.Now().Before(until) && ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
		status, err = supervisor.Recover(ctx)
	}
	if err != nil || status.Phase != control.RolledBack || !status.RollbackVerified || !status.PreparedArtifactsRetained {
		nativeReviewReuseFail(t, "review_reuse_update_cancel_verified", err)
	}
	rolled, err := originalRuntime.Observe(ctx, control.ReadRequest{KnownState: preparedKnown, BaselineCredentials: credentials})
	checkpointDigest, checkpointErr := stateDigest(checkpoint.Inventory, target)
	rolledDigest, digestErr := stateDigest(rolled.Inventory, target)
	if err != nil || checkpointErr != nil || digestErr != nil || checkpointDigest != rolledDigest || !sameCredentials(checkpoint.Credentials, rolled.Credentials) || !rolled.Owner.Known || rolled.Owner.Active {
		nativeReviewReuseFail(t, "review_reuse_checkpoint_equal", err)
	}
	if err := supervisor.Close(); err != nil {
		nativeReviewReuseFail(t, "review_reuse_old_runtime_closed", err)
	}
	if err := originalRuntime.Close(); err != nil {
		nativeReviewReuseFail(t, "review_reuse_old_runtime_closed", err)
	}
	t.Log("review_reuse_baseline_confirmed=true review_reuse_update_cancel_verified=true review_reuse_checkpoint_equal=true review_reuse_credentials_equal=true review_reuse_old_runtime_closed=true")

	freshRuntime, err := Open(ctx, cfg)
	if err != nil {
		nativeReviewReuseFail(t, "review_reuse_fresh_open", err)
	}
	defer freshRuntime.Close()
	freshRuntime.profile.RuleRemovalBatchVerified = false
	counts := &nativeReviewReuseCounts{}
	counts.readOnly.Store(true)
	freshRuntime.api = &nativeReviewReuseAPI{apiChannel: freshRuntime.api, counts: counts}
	freshRuntime.terminal = &nativeReviewReuseTerminal{terminalChannel: freshRuntime.terminal, counts: counts}
	fresh := &nativeReviewReuseRuntime{runtime: freshRuntime, counts: counts, credentials: credentials, known: preparedKnown}
	reopened, err := control.Open(config, fresh)
	if err != nil {
		nativeReviewReuseFail(t, "review_reuse_fresh_supervisor", err)
	}
	defer reopened.Close()
	if _, err = reopened.Confirm(ctx, oldTransaction, update.Checksum); err == nil {
		t.Fatal("review_reuse_old_confirm_rejected=false")
	} else {
		var failure *control.Failure
		if !errors.As(err, &failure) || failure.Code != "confirmation-not-authorized" {
			nativeReviewReuseFail(t, "review_reuse_old_confirm_rejected", err)
		}
	}
	beforeStatus := reopened.Status()
	review, err := reopened.Review(ctx)
	if err != nil {
		nativeReviewReuseFail(t, "review_reuse_strict_review", err)
	}
	if counts.forbidden.Load() != 0 || counts.addRules.Load() != 0 || !reflect.DeepEqual(beforeStatus, reopened.Status()) || freshRuntime.boundPlan != nil || freshRuntime.taken {
		t.Fatal("review_reuse_review_readonly=false")
	}
	owner := fresh.last.Owner
	if !owner.Known || !owner.Alive || owner.Active || owner.ThisSession || !owner.HistoryKnown || owner.HistoryActions != 0 || owner.SessionID == oldSession || !fresh.last.CrossChannelIdentityVerified {
		t.Fatal("review_reuse_fresh_session_verified=false")
	}
	actualDigest, err := stateDigest(fresh.last.Inventory, target)
	if err != nil || actualDigest != checkpointDigest || !sameCredentials(checkpoint.Credentials, fresh.last.Credentials) {
		t.Fatal("review_reuse_checkpoint_equal=false")
	}
	if review.Snapshot.NamespaceEmpty || !review.Snapshot.Managed || review.Snapshot.ActiveGeneration != first.Generation || len(review.KnownState.Generations) != 2 || len(review.Snapshot.Generations) != 2 {
		t.Fatal("review_reuse_retained_generation_verified=false")
	}
	reviewedIDs := nativeReviewReuseIDs(review.Snapshot)
	if !nativeReviewReusePreservesIDs(baselineIDs, reviewedIDs) {
		t.Fatal("review_reuse_native_ids_preserved=false")
	}
	if len(review.KnownState.Peers) != 1 || review.KnownState.Peers[0].Authentication != first.Desired.Peers[0].Authentication || len(review.Snapshot.Connections) != 1 || review.Snapshot.Connections[0].Config.Authentication != first.Desired.Peers[0].Authentication || !review.Snapshot.Connections[0].AuthenticationVerified || review.Snapshot.Connections[0].Disabled {
		t.Fatal("review_reuse_authentication_refs_preserved=false")
	}
	retry, err := routeros.BuildReferencedPlan(target, update.Desired, review.Snapshot, routeros.PlanOptions{})
	if err != nil || retry.Generation != update.Generation || !reflect.DeepEqual(retry.Desired, update.Desired) {
		nativeReviewReuseFail(t, "review_reuse_retry_plan", err)
	}
	for _, operation := range retry.Operations {
		if operation.Phase == routeros.Prepare || operation.Kind == routeros.AddRule {
			t.Fatal("review_reuse_zero_prepare_rules=false")
		}
	}
	t.Log("review_reuse_fresh_session_verified=true review_reuse_review_readonly=true review_reuse_retained_generation_verified=true review_reuse_native_ids_preserved=true review_reuse_authentication_refs_preserved=true review_reuse_zero_prepare_rules=true review_reuse_review_forbidden_calls=0")
	counts.readOnly.Store(false)
	nativeReviewReuseStages(freshRuntime)
	fresh.known = review.KnownState
	status, err = reopened.Stage(ctx, control.Request{Plan: retry, KnownState: review.KnownState, BaselineCredentials: credentials, Timeout: 90 * time.Second, ExclusiveAccessAcknowledged: true})
	if err != nil || status.Phase != control.AwaitingConfirmation || status.TransactionID == oldTransaction || counts.addRules.Load() != 0 {
		nativeReviewReuseFail(t, "review_reuse_retry_stage", err)
	}
	beforeConfirm := counts.confirms.Load()
	if _, err = reopened.Confirm(ctx, oldTransaction, update.Checksum); err == nil || counts.confirms.Load() != beforeConfirm || reopened.Status().Phase != control.AwaitingConfirmation {
		t.Fatal("review_reuse_old_confirm_rejected=false")
	}
	status, err = reopened.Confirm(ctx, status.TransactionID, retry.Checksum)
	if err != nil || status.Phase != control.Confirmed || !status.ConfirmationVerified {
		nativeReviewReuseFail(t, "review_reuse_confirmed", err)
	}
	finalKnown := review.KnownState
	finalKnown.ActiveGeneration = retry.Generation
	final, err := freshRuntime.Observe(ctx, control.ReadRequest{KnownState: finalKnown, BaselineCredentials: credentials, Strict: true})
	if err != nil || final.Snapshot == nil || !final.Snapshot.Managed || final.Snapshot.TableFIB || final.Snapshot.ActiveGeneration != retry.Generation || !final.Owner.Known || final.Owner.Active || !nativeReviewReusePreservesIDs(reviewedIDs, nativeReviewReuseIDs(*final.Snapshot)) || !sameCredentials(checkpoint.Credentials, final.Credentials) {
		nativeReviewReuseFail(t, "review_reuse_strict", err)
	}
	if counts.addRules.Load() != 0 || counts.forbidden.Load() != 0 || freshRuntime.profile.RuleRemovalBatchVerified || freshRuntime.profile.CleanupVerified {
		t.Fatal("review_reuse_zero_prepare_rules=false")
	}
	t.Log("review_reuse_old_confirm_rejected=true review_reuse_new_transaction=true review_reuse_confirmed=true review_reuse_strict=true review_reuse_second_add_rules=0 review_reuse_no_manual_cleanup=true review_reuse_batch_removal_used=false review_reuse_large_scale_verified=false review_reuse_packet_forwarding_verified=false")
}

func TestNativeReviewReuseGuardsAndIdentityComparison(t *testing.T) {
	counts := &nativeReviewReuseCounts{}
	counts.readOnly.Store(true)
	runtime := &nativeReviewReuseRuntime{counts: counts}
	ctx := context.Background()
	if runtime.BindPlan(routeros.Plan{}, nil) == nil || runtime.ResetIdle(ctx) == nil {
		t.Fatal("review permitted stateful setup")
	}
	if _, err := runtime.Apply(ctx, routeros.APIMutation{}); err == nil {
		t.Fatal("review permitted an ordinary write")
	}
	if _, err := runtime.Take(ctx, time.Now()); err == nil {
		t.Fatal("review permitted ownership")
	}
	if _, err := runtime.Confirm(ctx); err == nil {
		t.Fatal("review permitted confirmation")
	}
	if _, err := runtime.Cancel(ctx); err == nil {
		t.Fatal("review permitted cancellation")
	}
	ssh := &nativeReviewReuseTerminal{counts: counts}
	if _, err := ssh.ExecuteOwned(ctx, api.Mutation{}); err == nil {
		t.Fatal("review permitted native SSH writes")
	}
	apiGuard := &nativeReviewReuseAPI{counts: counts}
	if _, err := apiGuard.Apply(ctx, api.Mutation{Resource: api.FilterRule, Action: api.Add}); err == nil || counts.addRules.Load() != 0 || counts.forbidden.Load() != 8 {
		t.Fatal("review API write was sent or hidden from counters")
	}
	before := map[string]string{"connection/peer": "*A", "rule/old": "*B"}
	if !nativeReviewReusePreservesIDs(before, map[string]string{"connection/peer": "*A", "rule/old": "*B", "rule/new": "*C"}) || nativeReviewReusePreservesIDs(before, map[string]string{"connection/peer": "*C", "rule/old": "*B"}) || nativeReviewReusePreservesIDs(before, map[string]string{"connection/peer": "*A"}) {
		t.Fatal("native identity evidence was replaced by name/count equality")
	}
}
