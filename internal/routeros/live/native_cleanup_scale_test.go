package live

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routing"
)

// Counters record calls to the native driver without replacing readback or
// acknowledgements. The same ledger follows the journal through a reopen.
type nativeCleanupScaleLedger struct {
	targets map[string]string
	acks    map[string]int
	batches int
	tails   int
	singles int
}

type nativeCleanupScaleDriver struct {
	*nativeExtensionRuntime
	apiCount *nativeRemovalScaleAPI
	keys     routeros.Credentials
	ledger   *nativeCleanupScaleLedger
	binds    int
}

func (d *nativeCleanupScaleDriver) Observe(ctx context.Context, q control.ReadRequest) (control.Observation, error) {
	hadTail := d.r.removalTail != nil
	out, err := d.nativeExtensionRuntime.Observe(ctx, q)
	if hadTail && err == nil && d.r.removalTail == nil {
		d.ledger.tails++
	}
	return out, err
}

func (d *nativeCleanupScaleDriver) Take(ctx context.Context, deadline time.Time) (control.Owner, error) {
	checkpoint, err := d.Observe(ctx, control.ReadRequest{KnownState: d.known, BaselineCredentials: d.keys})
	if err != nil || !checkpoint.Owner.Known || checkpoint.Owner.Active || !checkpoint.Owner.HistoryKnown || checkpoint.Owner.HistoryActions != 0 || len(checkpoint.Credentials) != 1 {
		return control.Owner{}, fail("native-large-cleanup-checkpoint-unverified")
	}
	for _, got := range checkpoint.Credentials {
		if !got.Matches([]byte(d.keys["batch-peer"].Password)) {
			return control.Owner{}, fail("native-large-cleanup-checkpoint-unverified")
		}
	}
	d.checkpoint = &checkpoint
	return d.r.Take(ctx, deadline)
}

func (d *nativeCleanupScaleDriver) BindCleanup(ctx context.Context, binding control.CleanupBinding) error {
	d.binds++
	return d.nativeExtensionRuntime.BindCleanup(ctx, binding)
}

func (d *nativeCleanupScaleDriver) validRemoval(m routeros.APIMutation) bool {
	return m.Phase == routeros.Cleanup && m.Request.Action == api.Remove && m.Request.Resource == api.FilterRule &&
		d.ledger.targets[m.OperationID] == m.Request.ID && d.ledger.acks[m.OperationID] == 0
}

func (d *nativeCleanupScaleDriver) ApplyCleanup(ctx context.Context, m routeros.APIMutation) (api.Result, error) {
	if !d.validRemoval(m) {
		return api.Result{}, fail("native-large-cleanup-unreviewed-removal")
	}
	r, err := d.nativeExtensionRuntime.ApplyCleanup(ctx, m)
	if err == nil && r.Acknowledged && r.ID == "" {
		d.ledger.acks[m.OperationID]++
		d.ledger.singles++
	}
	return r, err
}

func (d *nativeCleanupScaleDriver) ApplyRuleRemovalBatch(ctx context.Context, batch control.RuleRemovalBatch, raw control.RuleRemovalRecorder) error {
	if batch.Mode != control.RuleRemovalCleanup || batch.ProofMode != control.RuleRemovalProofGlobalTail {
		return fail("native-large-cleanup-wrong-batch-mode")
	}
	for _, m := range batch.Operations {
		if !d.validRemoval(m) {
			return fail("native-large-cleanup-unreviewed-removal")
		}
	}
	d.ledger.batches++
	started := time.Now()
	recorder := &nativeCleanupScaleRecorder{RuleRemovalRecorder: raw, ledger: d.ledger}
	err := d.r.ApplyRuleRemovalBatch(ctx, batch, recorder)
	d.t.Logf("large_cleanup_batch=%d large_cleanup_batch_acks=%d large_cleanup_batch_milliseconds=%d", d.ledger.batches, recorder.acks, time.Since(started).Milliseconds())
	return err
}

type nativeCleanupScaleRecorder struct {
	control.RuleRemovalRecorder
	ledger *nativeCleanupScaleLedger
	acks   int
}

func (r *nativeCleanupScaleRecorder) Result(id string, value api.Result, err error) error {
	if err == nil && value.Acknowledged && value.ID == "" {
		if r.ledger.targets[id] == "" || r.ledger.acks[id] != 0 {
			return fail("native-large-cleanup-unreviewed-removal")
		}
		r.ledger.acks[id]++
		r.acks++
	}
	return r.RuleRemovalRecorder.Result(id, value, err)
}

func (*nativeCleanupScaleRecorder) Absent(string, control.RuleRemovalObservation) error {
	return fail("native-large-cleanup-single-absence-unexpected")
}

func nativeCleanupScaleFailure(t *testing.T, stage string, err error) {
	t.Helper()
	if err != nil {
		t.Logf("large_cleanup_failure_code=%s", nativeSupervisorCode(err))
	}
	t.Fatalf("large_cleanup_failed=%s", stage)
}

// Verify the actual native large-table preparation, two real confirmations,
// and explicitly bounded confirmed-cleanup against one disposable CHR target.
func TestNativeCHRLiveLargeUpdateConfirmBoundedCleanup(t *testing.T) {
	cfg := nativeLiveConfig(t)
	cfg.RouterID = "native-cleanup-1000"
	cfg.SessionLifetimeSeconds, cfg.ObservationTimeoutSeconds, cfg.OperationTimeoutSeconds = 600, 180, 15
	cfg.MaxObjects, cfg.MaxGetterCalls = 10000, 50000
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	total := time.Now()
	defer func() { t.Logf("large_cleanup_total_milliseconds=%d", time.Since(total).Milliseconds()) }()
	profile := CapabilityProfile()
	if !profile.FilterRuleDisabledSelectionVerified || profile.Version != "7.24.6" || profile.PreparationVerified || profile.ActivationVerified || profile.CleanupVerified || profile.RuleRemovalBatchVerified || profile.RuleRemovalGlobalTailVerified || profile.MaxHistoryActions != 0 {
		nativeCleanupScaleFailure(t, "gates", nil)
	}
	keys := routeros.Credentials{"batch-peer": {Revision: strings.Repeat("A", 22), Password: "NativeCleanupScale_Retained"}}
	ledger := &nativeCleanupScaleLedger{targets: map[string]string{}, acks: map[string]int{}}
	dir, err := os.MkdirTemp("/scratch/tmp", "native-cleanup-scale-")
	if err != nil {
		nativeCleanupScaleFailure(t, "journal", err)
	}
	defer os.RemoveAll(dir)
	conf := control.Config{Dir: dir, OperationTimeout: 30 * time.Second, PrepareTimeout: 7 * time.Minute, MinConfirmationTimeout: 10 * time.Second, MaxConfirmationTimeout: 2 * time.Minute, PollInterval: time.Second, ManualCleanup: true}
	open := func() (*nativeCleanupScaleDriver, *control.Supervisor, *nativeRemovalScaleTerminalCounts) {
		t.Helper()
		r, err := Open(ctx, cfg)
		if err != nil {
			nativeCleanupScaleFailure(t, "open-runtime", err)
		}
		t.Cleanup(func() { _ = r.Close() })
		if conf.Target != (routeros.Target{}) && conf.Target != r.target {
			nativeCleanupScaleFailure(t, "target", nil)
		}
		conf.Target = r.target
		nativeDiscardEnableStage(r)
		r.profile.RuleBatchVerified, r.profile.ObjectObservationVerified = true, true
		r.profile.CleanupVerified, r.profile.RuleRemovalBatchVerified, r.profile.RuleRemovalGlobalTailVerified = true, true, true
		c := &nativeRemovalScaleAPI{nativeExtensionAPI: &nativeExtensionAPI{base: r.api, t: t}}
		r.api = c
		ssh := &nativeRemovalScaleTerminalCounts{}
		r.terminal = &nativeRemovalScaleTerminal{terminalChannel: r.terminal, nativeRemovalScaleTerminalCounts: ssh}
		dial := r.dialSSH
		r.dialSSH = func(ctx context.Context) (terminalChannel, error) {
			connection, err := dial(ctx)
			if err != nil {
				return nil, err
			}
			return &nativeRemovalScaleTerminal{terminalChannel: connection, nativeRemovalScaleTerminalCounts: ssh}, nil
		}
		d := &nativeCleanupScaleDriver{nativeExtensionRuntime: &nativeExtensionRuntime{r: r, t: t, counts: c.nativeExtensionAPI, requireSelection: true, deleted: map[string]bool{}}, apiCount: c, keys: keys, ledger: ledger}
		s, err := control.Open(conf, d)
		if err != nil {
			nativeCleanupScaleFailure(t, "open-journal", err)
		}
		t.Cleanup(func() { _ = s.Close() })
		return d, s, ssh
	}
	d, s, _ := open()
	baseKnown := routeros.KnownState{Target: conf.Target}
	start, err := d.Observe(ctx, control.ReadRequest{KnownState: baseKnown, Strict: true})
	if err != nil || start.Snapshot == nil || !start.Snapshot.NamespaceEmpty || !start.CrossChannelIdentityVerified {
		nativeCleanupScaleFailure(t, "initial-observe", err)
	}
	router := routing.Router{ID: conf.Target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1"}
	prefixes := make([]string, 1000)
	for i := range prefixes {
		prefixes[i] = fmt.Sprintf("198.18.%d.%d/32", i/256, i%256)
	}
	peers := []routing.Peer{{ID: "batch-peer", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 2000, AllowedPrefixes: prefixes, AuthenticationRevision: keys["batch-peer"].Revision, Password: keys["batch-peer"].Password}}
	planA, err := routeros.BuildPlan(conf.Target, router, peers, *start.Snapshot, routeros.PlanOptions{Bootstrap: true})
	if err != nil {
		nativeCleanupScaleFailure(t, "build-a", err)
	}
	d.known = nativeDiscardPreparedKnown(planA)
	t.Logf("large_cleanup_prefixes=%d", len(prefixes))
	stage := func(label string, plan routeros.Plan, known routeros.KnownState, initial bool) control.Status {
		t.Helper()
		started := time.Now()
		req := control.Request{Plan: plan, KnownState: known, BaselineCredentials: keys, Timeout: 90 * time.Second, ExclusiveAccessAcknowledged: true}
		if initial {
			req.Credentials = keys
		}
		status, err := s.Stage(ctx, req)
		t.Logf("large_cleanup_phase=%s-stage large_cleanup_phase_milliseconds=%d", label, time.Since(started).Milliseconds())
		if err != nil || status.Phase != control.AwaitingConfirmation {
			nativeCleanupScaleFailure(t, label+"-stage", err)
		}
		started = time.Now()
		status, err = s.Confirm(ctx, status.TransactionID, plan.Checksum)
		t.Logf("large_cleanup_phase=%s-confirm large_cleanup_phase_milliseconds=%d", label, time.Since(started).Milliseconds())
		if err != nil || status.Phase != control.Confirmed || !status.ConfirmationVerified {
			nativeCleanupScaleFailure(t, label+"-confirm", err)
		}
		t.Logf("large_cleanup_%s_confirmed=true", label)
		return status
	}
	stage("a", planA, baseKnown, true)
	if d.batchCalls != 1 || d.batchACKs != 2004 {
		nativeCleanupScaleFailure(t, "a-acks", nil)
	}
	t.Logf("large_cleanup_a_prepare_acks=%d", d.batchACKs)
	knownA := nativeExtensionDesired(planA)
	confirmedA, err := d.Observe(ctx, control.ReadRequest{KnownState: knownA, BaselineCredentials: keys, Strict: true})
	if err != nil || confirmedA.Snapshot == nil || confirmedA.Snapshot.ActiveGeneration != planA.Generation || len(confirmedA.Snapshot.Connections) != 1 {
		nativeCleanupScaleFailure(t, "observe-a", err)
	}
	oldPeer := confirmedA.Snapshot.Connections[0]
	peers[0].AllowedPrefixes = []string{"203.0.113.0/24"}
	planB, err := routeros.BuildPlan(conf.Target, router, peers, *confirmedA.Snapshot, routeros.PlanOptions{})
	if err != nil || planB.Generation == planA.Generation {
		nativeCleanupScaleFailure(t, "build-b", err)
	}
	for _, op := range planB.Operations {
		if op.Phase == routeros.Cleanup {
			if op.Kind != routeros.RemoveRule || op.Object.ID == "" || ledger.targets[op.ID] != "" {
				nativeCleanupScaleFailure(t, "targets", nil)
			}
			ledger.targets[op.ID] = op.Object.ID
		}
	}
	if len(ledger.targets) != 2004 {
		nativeCleanupScaleFailure(t, "targets", nil)
	}
	d.known = knownA
	d.known.Generations = append(append([]routeros.KnownGeneration{}, knownA.Generations...), nativeExtensionDesired(planB).Generations...)
	confirmedB := stage("b", planB, knownA, false)
	if confirmedB.Cleanup.Phase != control.CleanupPending || confirmedB.Cleanup.Completed != 0 || confirmedB.Cleanup.Total != len(ledger.targets) {
		nativeCleanupScaleFailure(t, "cleanup-pending", nil)
	}
	request := control.CleanupRequest{TransactionID: confirmedB.TransactionID, PlanChecksum: confirmedB.PlanChecksum, MaxBatches: 1}
	step := func(number int, prior control.Status) control.Status {
		t.Helper()
		before := len(ledger.acks)
		units := ledger.batches + ledger.singles
		started := time.Now()
		current, err := s.CleanupBounded(ctx, request)
		t.Logf("large_cleanup_request=%d large_cleanup_request_limit=%d large_cleanup_committed=%d large_cleanup_ack_total=%d large_cleanup_request_milliseconds=%d", number, request.MaxBatches, current.Cleanup.Completed, len(ledger.acks), time.Since(started).Milliseconds())
		if err != nil || current.Phase != control.Confirmed || !current.ConfirmationVerified || current.Cleanup.Total != len(ledger.targets) || current.Cleanup.Completed <= prior.Cleanup.Completed || current.Cleanup.Completed != len(ledger.acks) || len(ledger.acks) <= before || ledger.batches+ledger.singles-units > request.MaxBatches || ledger.tails != ledger.batches || d.r.removalTail != nil {
			nativeCleanupScaleFailure(t, "cleanup-request", err)
		}
		if current.Cleanup.Phase != control.CleanupComplete && (current.Cleanup.Phase != control.CleanupPending || current.Cleanup.LastCode != "cleanup-awaiting-explicit-continuation") {
			nativeCleanupScaleFailure(t, "cleanup-boundary", nil)
		}
		return current
	}
	progress := step(1, confirmedB)
	if progress.Cleanup.Phase != control.CleanupPending || progress.Cleanup.Completed >= progress.Cleanup.Total || ledger.batches != 1 {
		nativeCleanupScaleFailure(t, "first-boundary", nil)
	}
	reopens := 0
	reopen := func() {
		t.Helper()
		if err := s.Close(); err != nil {
			nativeCleanupScaleFailure(t, "close-journal", err)
		}
		if err := d.r.Close(); err != nil {
			nativeCleanupScaleFailure(t, "close-runtime", err)
		}
		var ssh *nativeRemovalScaleTerminalCounts
		d, s, ssh = open()
		inventory := d.apiCount.inventories.Load()
		recovered, err := s.Recover(ctx)
		written := d.apiCount.writes.Load() + ssh.owned.Load() + ssh.takes.Load() + ssh.confirms.Load() + ssh.cancels.Load() + int64(d.binds)
		if err != nil || recovered.Cleanup != progress.Cleanup || recovered.TransactionID != request.TransactionID || recovered.PlanChecksum != request.PlanChecksum || !recovered.ConfirmationVerified || recovered.Phase != control.Confirmed || written != 0 || d.apiCount.inventories.Load() <= inventory {
			nativeCleanupScaleFailure(t, "recover-read-only", err)
		}
		if _, err := s.Review(ctx); err == nil {
			nativeCleanupScaleFailure(t, "partial-review", nil)
		}
		reopens++
		t.Logf("large_cleanup_reopen=%d large_cleanup_recovered=%d large_cleanup_recovery_writes=%d", reopens, recovered.Cleanup.Completed, written)
		progress = recovered
	}
	reopen()
	request.MaxBatches = 2
	for number := 2; progress.Cleanup.Phase != control.CleanupComplete && number <= 32; number++ {
		if time.Until(d.r.sessionExpiresAt) < 150*time.Second {
			reopen()
		}
		progress = step(number, progress)
	}
	if progress.Cleanup.Phase != control.CleanupComplete || progress.Cleanup.Completed != len(ledger.targets) || len(ledger.acks) != len(ledger.targets) || ledger.batches < 2 || ledger.tails != ledger.batches {
		nativeCleanupScaleFailure(t, "cleanup-incomplete", nil)
	}
	final, err := d.Observe(ctx, control.ReadRequest{KnownState: nativeExtensionDesired(planB), BaselineCredentials: keys, Strict: true})
	if err != nil || final.Snapshot == nil || final.Snapshot.ActiveGeneration != planB.Generation || final.Snapshot.TableFIB || !final.Owner.Known || final.Owner.Active || len(final.Snapshot.Connections) != 1 || len(final.Credentials) != 1 || !final.Snapshot.Connections[0].AuthenticationVerified || final.Snapshot.Connections[0].Disabled || final.Snapshot.Connections[0].Config.Authentication != oldPeer.Config.Authentication || final.Snapshot.Connections[0].Ref.ID != oldPeer.Ref.ID || !final.Credentials[oldPeer.Ref.ID].Matches([]byte(keys["batch-peer"].Password)) {
		nativeCleanupScaleFailure(t, "final-strict", err)
	}
	oldIDs := map[string]bool{}
	for op, id := range ledger.targets {
		if ledger.acks[op] != 1 || oldIDs[id] {
			nativeCleanupScaleFailure(t, "exact-once", nil)
		}
		oldIDs[id] = true
	}
	for _, row := range final.Inventory.FilterRules {
		if oldIDs[row[".id"]] || strings.Contains(row["chain"], "_g"+planA.Generation+"_") {
			nativeCleanupScaleFailure(t, "old-generation-retained", nil)
		}
	}
	ready, err := s.Review(ctx)
	if err != nil || ready.Snapshot.ActiveGeneration != planB.Generation || len(ready.KnownState.Generations) != 1 {
		nativeCleanupScaleFailure(t, "final-review", err)
	}
	t.Logf("large_cleanup_approved=%d large_cleanup_acked=%d large_cleanup_batches=%d large_cleanup_global_tails=%d large_cleanup_single_removals=%d large_cleanup_reopens=%d large_cleanup_final_strict=true large_cleanup_old_generation_absent=true large_cleanup_key_exact=true large_cleanup_review_verified=true", len(ledger.targets), len(ledger.acks), ledger.batches, ledger.tails, ledger.singles, reopens)
}
