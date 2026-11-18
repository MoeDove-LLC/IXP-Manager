package live

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routing"
)

// Count real API calls without exposing values or accidentally dropping the
// optional fixed safe-mode reader required by the new rule/cleanup adapters.
type nativeExtensionAPI struct {
	objects                             atomic.Int64
	identities                          atomic.Int64
	inventoryNanos                      atomic.Int64
	getterNanos                         atomic.Int64
	getterBatches                       atomic.Int64
	disabledGetters                     atomic.Int64
	selectionFalse, selectionTrue       atomic.Int64
	selectionFalseIDs, selectionTrueIDs atomic.Int64
	selectionObservations               atomic.Int64
	t                                   *testing.T
	phase                               atomic.Int32
	base                                apiChannel
	inventories                         atomic.Int64
	properties                          atomic.Int64
	safeReads                           atomic.Int64
	batchFinished                       atomic.Int64
}

// Preserve the fixed, typed predicate surface through the counting wrapper.
// Counts record attempted queries and only successfully returned native IDs.
func (a *nativeExtensionAPI) ReadFilterRuleDisabled(ctx context.Context, disabled bool) ([]string, error) {
	reader, ok := a.base.(filterRuleDisabledReader)
	if !ok {
		return nil, fail("native-extension-selection-reader-unavailable")
	}
	if disabled {
		a.selectionTrue.Add(1)
	} else {
		a.selectionFalse.Add(1)
	}
	ids, err := reader.ReadFilterRuleDisabled(ctx, disabled)
	if err == nil {
		if disabled {
			a.selectionTrueIDs.Add(int64(len(ids)))
		} else {
			a.selectionFalseIDs.Add(int64(len(ids)))
		}
	}
	return ids, err
}

// Require the native large-table fixture to use both actual predicates in both
// full observation rounds. Single-object disabled getters remain independent.
func (a *nativeExtensionAPI) observeSelection(ctx context.Context, r *Runtime, request control.ReadRequest, required bool) (control.Observation, error) {
	f, tr := a.selectionFalse.Load(), a.selectionTrue.Load()
	fi, ti, getters := a.selectionFalseIDs.Load(), a.selectionTrueIDs.Load(), a.disabledGetters.Load()
	observed, err := r.Observe(ctx, request)
	if err == nil && required && len(observed.Inventory.FilterRules) >= 128 {
		if !r.profile.FilterRuleDisabledSelectionVerified || a.selectionFalse.Load()-f != 2 || a.selectionTrue.Load()-tr != 2 || a.selectionFalseIDs.Load()-fi+a.selectionTrueIDs.Load()-ti != int64(2*len(observed.Inventory.FilterRules)) || a.disabledGetters.Load() != getters {
			return observed, fail("native-removal-selection-path-unverified")
		}
		a.selectionObservations.Add(1)
	}
	return observed, err
}

func (a *nativeExtensionAPI) ReadObject(ctx context.Context, resource api.Resource, id string) (api.Record, error) {
	reader, ok := a.base.(objectReader)
	if !ok {
		return nil, fail("object-reader-unavailable")
	}
	a.objects.Add(1)
	row, err := reader.ReadObject(ctx, resource, id)
	if err != nil {
		code := "unclassified"
		var failure *api.Error
		if errors.As(err, &failure) {
			code = string(failure.Code)
		}
		a.t.Logf("extension_api_resource=%s api_code=%s", resource, code)
	}
	return row, err
}
func (a *nativeExtensionAPI) ReadIdentity(ctx context.Context) (api.Inventory, error) {
	reader, ok := a.base.(objectReader)
	if !ok {
		return api.Inventory{}, fail("object-reader-unavailable")
	}
	a.identities.Add(1)
	return reader.ReadIdentity(ctx)
}

func (a *nativeExtensionAPI) Inventory(ctx context.Context) (api.Inventory, error) {
	a.inventories.Add(1)
	started := time.Now()
	inventory, err := a.base.Inventory(ctx)
	a.inventoryNanos.Add(time.Since(started).Nanoseconds())
	if err != nil {
		code, resource, limit, deadlineKind := "unclassified", "unknown", "none", "none"
		countBytes, countRecords, countSentences := 0, 0, 0
		deadline := false
		var failure *api.Error
		if errors.As(err, &failure) {
			code = string(failure.Code)
			if failure.Resource != "" {
				resource = string(failure.Resource)
			}
			if failure.LimitKind != "" {
				limit = failure.LimitKind
			}
			if failure.DeadlineKind != "" {
				deadlineKind = failure.DeadlineKind
			}
			countBytes, countRecords, countSentences = failure.ReplyBytes, failure.ReplyRecords, failure.ReplySentences
			deadline = failure.Deadline
		}
		stage := "baseline"
		if a.phase.Load() == 1 {
			stage = "batch"
		} else if a.phase.Load() == 2 {
			stage = "after-batch"
		}
		a.t.Logf("extension_api_resource=%s api_code=%s reply_bytes=%d reply_records=%d reply_sentences=%d limit_kind=%s deadline=%t deadline_kind=%s elapsed_milliseconds=%d extension_stage=%s", resource, code, countBytes, countRecords, countSentences, limit, deadline, deadlineKind, time.Since(started).Milliseconds(), stage)
	}
	return inventory, err
}
func (a *nativeExtensionAPI) ReadProperty(ctx context.Context, r api.Resource, id, field string) (api.PropertyValue, error) {
	count := a.properties.Add(1)
	if r == api.FilterRule && field == "disabled" {
		a.disabledGetters.Add(1)
	}
	started := time.Now()
	value, err := a.base.ReadProperty(ctx, r, id, field)
	a.getterNanos.Add(time.Since(started).Nanoseconds())
	if err != nil {
		code := "unclassified"
		var failure *api.Error
		if errors.As(err, &failure) {
			code = string(failure.Code)
		}
		afterBatch := int64(0)
		if done := a.batchFinished.Load(); done > 0 {
			afterBatch = time.Since(time.Unix(0, done)).Milliseconds()
		}
		a.t.Logf("extension_getter_resource=%s extension_getter_field=%s api_code=%s getter_count=%d getter_milliseconds=%d after_batch_milliseconds=%d getter_context_done=%t getter_context_deadline=%t", r, field, code, count, time.Since(started).Milliseconds(), afterBatch, ctx.Err() != nil, errors.Is(ctx.Err(), context.DeadlineExceeded))
	}
	return value, err
}
func (a *nativeExtensionAPI) ReadProperties(ctx context.Context, requests []api.PropertyRequest) ([]api.PropertyValue, error) {
	reader, ok := a.base.(propertyBatchReader)
	if !ok {
		return nil, fail("native-extension-property-batch-unavailable")
	}
	for _, q := range requests {
		if q.Resource == api.FilterRule && q.Property == "disabled" {
			a.disabledGetters.Add(1)
		}
	}
	a.getterBatches.Add(1)
	count := a.properties.Add(int64(len(requests)))
	started := time.Now()
	values, err := reader.ReadProperties(ctx, requests)
	a.getterNanos.Add(time.Since(started).Nanoseconds())
	if err != nil {
		code, resource, field := "unclassified", "unknown", "unknown"
		limit, deadlineKind := "none", "none"
		deadline := false
		replyBytes, replyRecords, replySentences := 0, 0, 0
		var failure *api.Error
		if errors.As(err, &failure) {
			code = string(failure.Code)
			deadline, replyBytes, replyRecords, replySentences = failure.Deadline, failure.ReplyBytes, failure.ReplyRecords, failure.ReplySentences
			if failure.DeadlineKind != "" {
				deadlineKind = failure.DeadlineKind
			}
			if failure.LimitKind != "" {
				limit = failure.LimitKind
			}
			if failure.Resource != "" {
				resource = string(failure.Resource)
			}
			if failure.Property != "" {
				field = failure.Property
			}
		}
		if len(requests) > 0 && resource == "unknown" {
			same := true
			for _, q := range requests {
				if q.Resource != requests[0].Resource || q.Property != requests[0].Property {
					same = false
				}
			}
			if same {
				resource = string(requests[0].Resource)
				field = requests[0].Property
			}
		}
		afterBatch := int64(0)
		if done := a.batchFinished.Load(); done > 0 {
			afterBatch = time.Since(time.Unix(0, done)).Milliseconds()
		}
		contextErr := ctx.Err()
		a.t.Logf("extension_getter_resource=%s extension_getter_field=%s api_code=%s getter_count=%d getter_milliseconds=%d after_batch_milliseconds=%d getter_context_done=%t getter_context_deadline=%t reply_bytes=%d reply_records=%d reply_sentences=%d limit_kind=%s deadline=%t deadline_kind=%s", resource, field, code, count, time.Since(started).Milliseconds(), afterBatch, contextErr != nil, errors.Is(contextErr, context.DeadlineExceeded), replyBytes, replyRecords, replySentences, limit, deadline, deadlineKind)
	}
	return values, err
}
func (a *nativeExtensionAPI) ReadCredential(ctx context.Context, id string) (api.Credential, error) {
	return a.base.ReadCredential(ctx, id)
}
func (a *nativeExtensionAPI) Apply(ctx context.Context, m api.Mutation) (api.Result, error) {
	return a.base.Apply(ctx, m)
}
func (a *nativeExtensionAPI) Close() error { return a.base.Close() }
func (a *nativeExtensionAPI) ReadSafeMode(ctx context.Context) (api.SafeModeState, error) {
	reader, ok := a.base.(safeModeReader)
	if !ok {
		return api.SafeModeState{}, fail("native-extension-owner-reader-unavailable")
	}
	a.safeReads.Add(1)
	return reader.ReadSafeMode(ctx)
}

var _ safeModeReader = (*nativeExtensionAPI)(nil)
var _ filterRuleDisabledReader = (*nativeExtensionAPI)(nil)

type nativeExtensionRuntime struct {
	r                *Runtime
	t                *testing.T
	counts           *nativeExtensionAPI
	known            routeros.KnownState
	checkpoint       *control.Observation
	mu               sync.Mutex
	added            []nativeAddedObject
	deleted          map[string]bool
	batchCalls       int
	batchACKs        int
	cleanupACKs      int
	requireSelection bool
}

func (d *nativeExtensionRuntime) Binding() control.Binding { return d.r.Binding() }
func (d *nativeExtensionRuntime) BindObjectActivation(ctx context.Context, sum string) error {
	err := d.r.BindObjectActivation(ctx, sum)
	if err != nil {
		d.t.Logf("extension_observation_code=%s", liveCode(err))
	}
	return err
}
func (d *nativeExtensionRuntime) ObserveObject(ctx context.Context, q control.ObjectReadRequest) (control.ObjectObservation, error) {
	o, err := d.r.ObserveObject(ctx, q)
	if err != nil {
		d.t.Logf("extension_observation_code=%s", liveCode(err))
	}
	return o, err
}
func (d *nativeExtensionRuntime) ApplyObject(ctx context.Context, m routeros.APIMutation) (api.Result, error) {
	started := time.Now()
	var proofAt time.Time
	if d.r.objectSession != nil && d.r.objectSession.before != nil {
		proofAt = d.r.objectSession.before.at
	}
	result, err := d.r.ApplyObject(ctx, m)
	if err != nil {
		age := int64(0)
		if !proofAt.IsZero() {
			age = time.Since(proofAt).Milliseconds()
			if age < 0 {
				age = 0
			}
		}
		d.t.Logf("extension_observation_code=%s object_proof_present=%t object_proof_age_milliseconds=%d object_apply_milliseconds=%d", liveCode(err), !proofAt.IsZero(), age, time.Since(started).Milliseconds())
	}
	return result, err
}
func (d *nativeExtensionRuntime) BindPlan(p routeros.Plan, c routeros.Credentials) error {
	return d.r.BindPlan(p, c)
}
func (d *nativeExtensionRuntime) ResetIdle(ctx context.Context) error { return d.r.ResetIdle(ctx) }
func (d *nativeExtensionRuntime) Observe(ctx context.Context, q control.ReadRequest) (control.Observation, error) {
	started := time.Now()
	i0, g0 := d.counts.inventoryNanos.Load(), d.counts.getterNanos.Load()
	ic, gc, bc := d.counts.inventories.Load(), d.counts.properties.Load(), d.counts.getterBatches.Load()
	observed, err := d.counts.observeSelection(ctx, d.r, q, d.requireSelection)
	elapsed := time.Since(started)
	if elapsed >= 5*time.Second {
		i, g := time.Duration(d.counts.inventoryNanos.Load()-i0), time.Duration(d.counts.getterNanos.Load()-g0)
		other := elapsed - i - g
		if other < 0 {
			other = 0
		}
		d.t.Logf("extension_observe_milliseconds=%d extension_inventory_milliseconds=%d extension_getter_milliseconds=%d extension_other_milliseconds=%d extension_inventory_calls=%d extension_getter_items=%d extension_getter_batches=%d", elapsed.Milliseconds(), i.Milliseconds(), g.Milliseconds(), other.Milliseconds(), d.counts.inventories.Load()-ic, d.counts.properties.Load()-gc, d.counts.getterBatches.Load()-bc)
	}
	return observed, err
}
func (d *nativeExtensionRuntime) Owner(ctx context.Context) (control.Owner, error) {
	return d.r.Owner(ctx)
}
func (d *nativeExtensionRuntime) Confirm(ctx context.Context) (control.Owner, error) {
	return d.r.Confirm(ctx)
}
func (d *nativeExtensionRuntime) Cancel(ctx context.Context) (control.Owner, error) {
	return d.r.Cancel(ctx)
}
func (d *nativeExtensionRuntime) CloseOwner() error { return d.r.CloseOwner() }
func (d *nativeExtensionRuntime) Take(ctx context.Context, deadline time.Time) (control.Owner, error) {
	o, err := d.counts.observeSelection(ctx, d.r, control.ReadRequest{KnownState: d.known}, d.requireSelection)
	if err != nil || !o.Owner.Known || o.Owner.Active || !o.Owner.HistoryKnown || o.Owner.HistoryActions != 0 {
		return control.Owner{}, fail("extension-prepared-owner-unverified")
	}
	if len(o.Credentials) != len(d.known.Peers) {
		return control.Owner{}, fail("extension-prepared-credential-count")
	}
	for _, credential := range o.Credentials {
		if !credential.Matches(nil) {
			return control.Owner{}, fail("extension-prepared-key-unverified")
		}
	}
	d.checkpoint = &o
	return d.r.Take(ctx, deadline)
}
func (d *nativeExtensionRuntime) track(resource api.Resource, result api.Result) {
	if result.Acknowledged && result.ID != "" {
		d.mu.Lock()
		d.added = append(d.added, nativeAddedObject{resource, result.ID})
		d.mu.Unlock()
	}
}
func (d *nativeExtensionRuntime) Apply(ctx context.Context, m routeros.APIMutation) (api.Result, error) {
	started, before := time.Now(), d.counts.inventories.Load()
	result, err := d.r.Apply(ctx, m)
	if d.counts.phase.Load() == 2 {
		reads := d.counts.inventories.Load() - before
		complete := err == nil && result.Acknowledged
		d.t.Logf("extension_apply_inventory_calls=%d extension_apply_milliseconds=%d extension_apply_completed=%t evidence_reused=%t", reads, time.Since(started).Milliseconds(), complete, complete && reads == 0)
	}
	if m.Request.Action == api.Add {
		d.track(m.Request.Resource, result)
	}
	return result, err
}

type nativeExtensionRecorder struct {
	driver *nativeExtensionRuntime
	next   control.RuleBatchRecorder
}

func (r *nativeExtensionRecorder) Before(id string) error { return r.next.Before(id) }
func (r *nativeExtensionRecorder) After(id string, result api.Result, err error) error {
	// Capture even an ACK whose local journal write subsequently fails. These
	// IDs are used only for explicit cleanup after independent proof.
	if result.Acknowledged && result.ID != "" {
		r.driver.track(api.FilterRule, result)
		r.driver.mu.Lock()
		r.driver.batchACKs++
		r.driver.mu.Unlock()
	}
	return r.next.After(id, result, err)
}
func (d *nativeExtensionRuntime) ApplyRuleBatch(ctx context.Context, b control.RuleBatch, recorder control.RuleBatchRecorder) error {
	d.counts.phase.Store(1)
	defer d.counts.phase.Store(2)
	defer func() { d.counts.batchFinished.Store(time.Now().UnixNano()) }()
	d.mu.Lock()
	d.batchCalls++
	d.mu.Unlock()
	err := d.r.ApplyRuleBatch(ctx, b, &nativeExtensionRecorder{driver: d, next: recorder})
	d.mu.Lock()
	acknowledged := d.batchACKs
	d.mu.Unlock()
	d.t.Logf("extension_batch_runtime=%t code=%s batch_acknowledged=%d", err == nil, liveCode(err), acknowledged)
	return err
}
func (d *nativeExtensionRuntime) BindCleanup(ctx context.Context, b control.CleanupBinding) error {
	return d.r.BindCleanup(ctx, b)
}
func (d *nativeExtensionRuntime) ApplyCleanup(ctx context.Context, m routeros.APIMutation) (api.Result, error) {
	result, err := d.r.ApplyCleanup(ctx, m)
	if err == nil && result.Acknowledged {
		d.mu.Lock()
		d.cleanupACKs++
		d.deleted[cleanupKey(m.Request.Resource, m.Request.ID)] = true
		d.mu.Unlock()
	}
	return result, err
}

func nativeExtensionFixture(t *testing.T, mode string, batch, cleanup bool) (context.Context, *nativeExtensionRuntime, *control.Supervisor, routing.Router, routeros.KnownState) {
	t.Helper()
	cfg := nativeLiveConfig(t)
	cfg.RouterID = "native-extension-" + mode
	cfg.SessionLifetimeSeconds = 600
	cfg.ObservationTimeoutSeconds = 180
	cfg.OperationTimeoutSeconds = 15
	cfg.MaxObjects = 10000
	cfg.MaxGetterCalls = 50000
	ctx, cancel := context.WithTimeout(context.Background(), 9*time.Minute)
	t.Cleanup(cancel)
	r, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("extension_open=false code=%s", liveCode(err))
	}
	t.Cleanup(func() { _ = r.Close() })
	counts := &nativeExtensionAPI{base: r.api, t: t}
	r.api = counts
	// Deliberately confined to this opt-in disposable-lab binary. No Config
	// field, production CLI option or CapabilityProfile is changed.
	r.profile.PreparationVerified = true
	r.profile.ActivationVerified = true
	r.profile.ConfirmNativeVerified = true
	r.profile.MaxHistoryActions = 80
	r.profile.RuleBatchVerified = batch
	r.profile.CleanupVerified = cleanup
	r.profile.ObjectObservationVerified = batch && !cleanup
	d := &nativeExtensionRuntime{r: r, t: t, counts: counts, known: routeros.KnownState{Target: r.target}, deleted: map[string]bool{}}
	leaf, err := os.MkdirTemp("/scratch/tmp", "native-extension-journal-")
	if err != nil {
		t.Fatal("extension_private_journal=false")
	}
	t.Cleanup(func() { _ = os.RemoveAll(leaf) })
	s, err := control.Open(control.Config{Dir: leaf, Target: r.target, OperationTimeout: 30 * time.Second, PrepareTimeout: 7 * time.Minute, MinConfirmationTimeout: 10 * time.Second, MaxConfirmationTimeout: 2 * time.Minute, PollInterval: time.Second}, d)
	if err != nil {
		t.Fatalf("extension_supervisor_open=false code=%s", nativeSupervisorCode(err))
	}
	t.Cleanup(func() { _ = s.Close() })
	router := routing.Router{ID: r.target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1", LocalIPv6: "2001:db8::1"}
	return ctx, d, s, router, d.known
}
func nativeExtensionPlan(t *testing.T, ctx context.Context, d *nativeExtensionRuntime, router routing.Router, peers []routing.Peer, known routeros.KnownState, bootstrap bool) routeros.Plan {
	t.Helper()
	o, err := d.Observe(ctx, control.ReadRequest{KnownState: known, Strict: true})
	if err != nil || o.Snapshot == nil || !o.Owner.Known || o.Owner.Active {
		t.Fatalf("extension_baseline=false code=%s", liveCode(err))
	}
	p, err := routeros.BuildPlan(d.r.target, router, peers, *o.Snapshot, routeros.PlanOptions{Bootstrap: bootstrap})
	if err != nil {
		t.Fatal("extension_plan=false")
	}
	prepared := known
	seen := map[string]bool{}
	for _, peer := range prepared.Peers {
		seen[peer.ID] = true
	}
	for _, peer := range p.Desired.Peers {
		if !seen[peer.ID] {
			prepared.Peers = append(prepared.Peers, routeros.KnownPeer{ID: peer.ID, Authentication: peer.Authentication})
		}
	}
	d.known = prepared
	return p
}
func nativeExtensionDesired(p routeros.Plan) routeros.KnownState {
	k := routeros.KnownState{Target: p.Target, ActiveGeneration: p.Generation, Generations: []routeros.KnownGeneration{{Target: p.Target, Desired: p.Desired, PolicyChecksum: p.PolicyChecksum}}}
	for _, peer := range p.Desired.Peers {
		k.Peers = append(k.Peers, routeros.KnownPeer{ID: peer.ID, Authentication: peer.Authentication})
	}
	return k
}
func nativeExtensionCleanupAfterProof(t *testing.T, ctx context.Context, d *nativeExtensionRuntime) {
	t.Helper()
	inv, err := d.r.api.Inventory(ctx)
	if err != nil || inv.SafeMode == nil || inv.SafeMode.Enabled || inv.SafeMode.Current {
		t.Fatal("extension_cleanup_owner_clear=false")
	}
	byID := map[string]api.Record{}
	for _, m := range menus(inv) {
		for _, row := range m.rows {
			byID[cleanupKey(m.resource, row[".id"])] = row
		}
	}
	d.mu.Lock()
	added := append([]nativeAddedObject(nil), d.added...)
	deleted := map[string]bool{}
	for k, v := range d.deleted {
		deleted[k] = v
	}
	d.mu.Unlock()
	owner := routeros.OwnerName(d.r.target.RouterID)
	for i := len(added) - 1; i >= 0; i-- {
		object := added[i]
		key := cleanupKey(object.resource, object.id)
		if deleted[key] {
			continue
		}
		row := byID[key]
		owned := row != nil && (row["name"] == owner || strings.HasPrefix(row["name"], owner+"_") || strings.HasPrefix(row["chain"], owner+"_") && strings.HasPrefix(row["comment"], owner+":"))
		if !owned {
			t.Fatal("extension_cleanup_exact_ack_id=false")
		}
		result, err := d.r.api.Apply(ctx, api.Mutation{Resource: object.resource, Action: api.Remove, ID: object.id})
		if err != nil || !result.Acknowledged {
			t.Fatal("extension_cleanup_after_proof=false")
		}
	}
}

func TestNativeCHRLiveRuleBatch1000Cancel(t *testing.T) {
	ctx, d, s, router, known := nativeExtensionFixture(t, "batch1000", true, false)
	prefixes := make([]string, 1000)
	for i := range prefixes {
		prefixes[i] = fmt.Sprintf("198.18.%d.%d/32", i/256, i%256)
	}
	peers := []routing.Peer{{ID: "batch-peer", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 2000, AllowedPrefixes: prefixes}}
	p := nativeExtensionPlan(t, ctx, d, router, peers, known, true)
	expectedRules := 0
	for _, op := range p.Operations {
		if generationRule(&p, op) {
			expectedRules++
		}
	}
	started := time.Now()
	beforeInventory := d.counts.inventories.Load()
	status, err := s.Stage(ctx, control.Request{Plan: p, KnownState: known, Timeout: 90 * time.Second, ExclusiveAccessAcknowledged: true})
	if err != nil || status.Phase != control.AwaitingConfirmation || d.checkpoint == nil {
		code := d.r.Status().LastCode
		if code == "" {
			code = "none"
		}
		t.Logf("extension_observation_code=%s", code)
		t.Fatalf("extension_batch_stage=false code=%s", nativeSupervisorCode(err))
	}
	stageMS := time.Since(started).Milliseconds()
	stageInventories := d.counts.inventories.Load() - beforeInventory
	d.mu.Lock()
	batchCalls, batchACKs := d.batchCalls, d.batchACKs
	d.mu.Unlock()
	if batchCalls != 1 || batchACKs != expectedRules || expectedRules < 2000 || stageInventories >= int64(expectedRules/4) || d.counts.getterBatches.Load() == 0 || d.counts.getterBatches.Load() >= d.counts.properties.Load() || d.counts.objects.Load() == 0 || d.counts.identities.Load() != d.counts.objects.Load() {
		t.Fatal("extension_batch_complete_and_bounded_reads=false")
	}
	status, err = s.Cancel(ctx, status.TransactionID)
	until := time.Now().Add(45 * time.Second)
	for status.Phase != control.RolledBack && time.Now().Before(until) && ctx.Err() == nil {
		time.Sleep(250 * time.Millisecond)
		status, err = s.Recover(ctx)
	}
	if err != nil || !status.RollbackVerified || status.Phase != control.RolledBack {
		t.Fatalf("extension_batch_cancel=false code=%s", nativeSupervisorCode(err))
	}
	after, err := d.Observe(ctx, control.ReadRequest{KnownState: d.known})
	if err != nil {
		t.Fatalf("extension_batch_after=false code=%s", liveCode(err))
	}
	want, _ := stateDigest(d.checkpoint.Inventory, d.r.target)
	actual, err := stateDigest(after.Inventory, d.r.target)
	if err != nil || want != actual || !sameCredentials(d.checkpoint.Credentials, after.Credentials) || !after.Owner.Known || after.Owner.Active {
		t.Fatal("extension_batch_checkpoint_and_credentials_equal=false")
	}
	t.Logf("extension_mode=batch1000 generation_rules=%d acknowledged_rules=%d batches=%d stage_inventory_calls=%d stage_milliseconds=%d stage_object_reads=%d stage_identity_reads=%d checkpoint_equal=true credentials_equal=true cleanup_before_proof=false", expectedRules, batchACKs, batchCalls, stageInventories, stageMS, d.counts.objects.Load(), d.counts.identities.Load())
	nativeExtensionCleanupAfterProof(t, ctx, d)
}

func TestNativeCHRLiveAutomaticCleanup(t *testing.T) {
	ctx, d, s, router, known := nativeExtensionFixture(t, "cleanup", true, true)
	peers := []routing.Peer{{ID: "keep", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"203.0.113.0/24"}}, {ID: "remove", ASN: 64502, Address: "2001:db8::2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"2001:db8:1::/48"}}}
	initial := nativeExtensionPlan(t, ctx, d, router, peers, known, true)
	first, err := s.Stage(ctx, control.Request{Plan: initial, KnownState: known, Timeout: 90 * time.Second, ExclusiveAccessAcknowledged: true})
	if err != nil {
		t.Fatalf("extension_cleanup_initial_stage=false code=%s", nativeSupervisorCode(err))
	}
	if first, err = s.Confirm(ctx, first.TransactionID, first.PlanChecksum); err != nil || first.Phase != control.Confirmed {
		t.Fatalf("extension_cleanup_initial_confirm=false code=%s", nativeSupervisorCode(err))
	}
	known = nativeExtensionDesired(initial)
	peers = peers[:1]
	peers[0].AllowedPrefixes = []string{"203.0.114.0/24"}
	next := nativeExtensionPlan(t, ctx, d, router, peers, known, false)
	expected := 0
	targets := map[string]bool{}
	for _, op := range next.Operations {
		if op.Phase == routeros.Cleanup {
			expected++
			resource := api.FilterRule
			if op.Kind == routeros.RemoveConnection {
				resource = api.BGPConnection
			}
			targets[cleanupKey(resource, op.Object.ID)] = true
		}
	}
	if expected == 0 {
		t.Fatal("extension_cleanup_authorized_suffix_empty=false")
	}
	st, err := s.Stage(ctx, control.Request{Plan: next, KnownState: known, Timeout: 90 * time.Second, ExclusiveAccessAcknowledged: true})
	if err != nil {
		t.Fatalf("extension_cleanup_update_stage=false code=%s", nativeSupervisorCode(err))
	}
	if st, err = s.Confirm(ctx, st.TransactionID, st.PlanChecksum); err != nil || st.Phase != control.Confirmed {
		t.Fatalf("extension_cleanup_update_confirm=false code=%s", nativeSupervisorCode(err))
	}
	until := time.Now().Add(3 * time.Minute)
	for st.Cleanup.Phase != control.CleanupComplete && time.Now().Before(until) && ctx.Err() == nil {
		if st.Phase != control.Confirmed || !st.ConfirmationVerified || st.Cleanup.Phase == control.CleanupUncertain {
			t.Fatalf("extension_cleanup_confirmed_fact=false code=%s", st.Cleanup.LastCode)
		}
		time.Sleep(250 * time.Millisecond)
		st = s.Status()
	}
	if st.Phase != control.Confirmed || st.Cleanup.Phase != control.CleanupComplete || st.Cleanup.Completed != expected {
		t.Fatalf("extension_cleanup_completed=false code=%s", st.Cleanup.LastCode)
	}
	final, err := d.Observe(ctx, control.ReadRequest{KnownState: nativeExtensionDesired(next), Strict: true})
	if err != nil || final.Snapshot == nil || final.Owner.Active {
		t.Fatalf("extension_cleanup_desired_retained=false code=%s", liveCode(err))
	}
	for _, m := range menus(final.Inventory) {
		for _, row := range m.rows {
			if targets[cleanupKey(m.resource, row[".id"])] {
				t.Fatal("extension_cleanup_old_target_absent=false")
			}
		}
	}
	noop, err := routeros.BuildPlan(d.r.target, router, peers, *final.Snapshot, routeros.PlanOptions{})
	if err != nil || len(noop.Operations) != 0 {
		t.Fatal("extension_cleanup_final_roundtrip=false")
	}
	d.mu.Lock()
	removed := d.cleanupACKs
	d.mu.Unlock()
	if removed != expected {
		t.Fatal("extension_cleanup_exactly_once=false")
	}
	t.Logf("extension_mode=cleanup approved_removals=%d acknowledged_removals=%d cleanup_complete=true confirmed_preserved=true strict_desired=true old_targets_absent=true new_review_requested=false cleanup_before_proof=false", expected, removed)
	nativeExtensionCleanupAfterProof(t, ctx, d)
}
