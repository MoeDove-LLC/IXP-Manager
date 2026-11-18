package live

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routing"
)

// Each presence result is copied from the model's current native-ID dictionary.
// No synthetic global snapshot or secret evidence is supplied by this wrapper.
type removalBatchAPIModel struct {
	apiChannel
	base       *fakeAPI
	safe       safeModeReader
	apply      func(context.Context, api.Mutation) (api.Result, error)
	reads      map[string]int
	onRead     func(api.Resource, string, int)
	response   func(api.Resource, string, int, api.ObjectPresence) (api.ObjectPresence, error)
	onSafe     func()
	safeFault  bool
	keepTarget bool
}

func (a *removalBatchAPIModel) ReadObjectPresence(ctx context.Context, resource api.Resource, id string) (api.ObjectPresence, error) {
	if ctx.Err() != nil {
		return api.ObjectPresence{}, ctx.Err()
	}
	key := cleanupKey(resource, id)
	a.reads[key]++
	count := a.reads[key]
	if a.onRead != nil {
		a.onRead(resource, id, count)
	}
	if ctx.Err() != nil {
		return api.ObjectPresence{}, ctx.Err()
	}
	a.base.mu.Lock()
	result := api.ObjectPresence{}
	for _, row := range *cleanupModelRows(&a.base.inv, resource) {
		if strings.EqualFold(row[".id"], id) {
			result = api.ObjectPresence{Present: true, Record: objectRecordCopy(row)}
			break
		}
	}
	a.base.mu.Unlock()
	if a.response != nil {
		return a.response(resource, id, count, result)
	}
	return result, nil
}
func (a *removalBatchAPIModel) ReadSafeMode(ctx context.Context) (api.SafeModeState, error) {
	if a.onSafe != nil {
		a.onSafe()
	}
	if a.safeFault {
		return api.SafeModeState{Enabled: true}, nil
	}
	return a.safe.ReadSafeMode(ctx)
}
func (a *removalBatchAPIModel) Apply(ctx context.Context, m api.Mutation) (api.Result, error) {
	if ctx.Err() != nil {
		return api.Result{}, ctx.Err()
	}
	if a.keepTarget {
		a.base.mu.Lock()
		a.base.writes++
		a.base.mu.Unlock()
		return api.Result{Acknowledged: true}, nil
	}
	return a.apply(ctx, m)
}

type removalBatchRecorderModel struct {
	before, result, absent []string
	observations           []control.RuleRemovalObservation
	steps                  []string
	beforeHook             func(int, control.RuleRemovalObservation) error
	resultHook             func(int, api.Result, error) error
	absentHook             func(int, control.RuleRemovalObservation) error
}

func (r *removalBatchRecorderModel) Before(id string, o control.RuleRemovalObservation) error {
	r.before = append(r.before, id)
	r.observations = append(r.observations, o)
	r.steps = append(r.steps, "before:"+id)
	if r.beforeHook != nil {
		return r.beforeHook(len(r.before), o)
	}
	return nil
}
func (r *removalBatchRecorderModel) Result(id string, result api.Result, err error) error {
	r.result = append(r.result, id)
	r.steps = append(r.steps, "result:"+id)
	if r.resultHook != nil {
		return r.resultHook(len(r.result), result, err)
	}
	return nil
}
func (r *removalBatchRecorderModel) Absent(id string, o control.RuleRemovalObservation) error {
	r.absent = append(r.absent, id)
	r.observations = append(r.observations, o)
	r.steps = append(r.steps, "absent:"+id)
	if r.absentHook != nil {
		return r.absentHook(len(r.absent), o)
	}
	return nil
}

type removalBatchFixture struct {
	r             *Runtime
	api           *removalBatchAPIModel
	term          *cleanupTerminalModel
	batch         control.RuleRemovalBatch
	initialWrites int
	initialIndex  int
	index         func() int
	unknown       func()
}

func newRemovalBatchFixture(t *testing.T, mode control.RuleRemovalMode) *removalBatchFixture {
	t.Helper()
	f := &removalBatchFixture{}
	var plan routeros.Plan
	var bridge *routeros.APIBridge
	var operations []routeros.APIOperation
	if mode == control.RuleRemovalPreparedDiscard {
		r, a, term, binding := discardAdapterFixture(t)
		f.r, f.term = r, term
		if err := r.BindPreparedDiscard(context.Background(), binding); err != nil {
			t.Fatal(err)
		}
		for r.preparedDiscard.next < len(r.preparedDiscard.operations) {
			m := discardNext(t, r, binding, r.preparedDiscard.next)
			row := removalBatchActualRow(a.fakeAPI, m.Request.Resource, m.Request.ID)
			if m.Request.Resource == api.FilterRule && strings.HasPrefix(row["comment"], binding.Plan.Owner+":g:") {
				break
			}
			if _, err := r.ApplyPreparedDiscard(context.Background(), m); err != nil {
				t.Fatal(err)
			}
		}
		f.api = &removalBatchAPIModel{apiChannel: a, base: a.fakeAPI, safe: a, apply: a.Apply, reads: map[string]int{}}
		f.index = func() int { return r.preparedDiscard.next }
		f.unknown = func() { a.unknown = true }
		plan, bridge, operations = binding.Plan, r.preparedDiscard.bridge, r.preparedDiscard.operations
	} else {
		r, a, term, p, ids := cleanupAdapterFixture(t)
		f.r, f.term = r, term
		if err := r.BindCleanup(context.Background(), control.CleanupBinding{Plan: p, RemainingOperationIDs: ids}); err != nil {
			t.Fatal(err)
		}
		f.api = &removalBatchAPIModel{apiChannel: a, base: a.fakeAPI, safe: a, apply: a.Apply, reads: map[string]int{}}
		f.index = func() int { return r.cleanup.next }
		f.unknown = func() { a.unknown = true }
		plan, bridge, operations = p, r.cleanup.bridge, r.cleanup.all
	}
	f.r.api = f.api
	f.r.profile.RuleRemovalBatchVerified = true
	f.r.config.ObservationTimeoutSeconds = 60
	f.initialWrites = f.api.base.writes
	f.initialIndex = f.index()
	raw, evidence, _, _, err := f.r.readState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := routeros.NewAPIResolver(raw, f.r.target, evidence)
	if err != nil {
		t.Fatal(err)
	}
	f.batch = control.RuleRemovalBatch{ID: "batch-fixture-1", TransactionID: "transaction-fixture-1", PlanChecksum: plan.Checksum, ScopeChecksum: strings.Repeat("a", 64), Mode: mode}
	for _, op := range operations[f.initialIndex:] {
		m, err := bridge.Mutation(op.ID, resolver)
		if err != nil {
			t.Fatal(err)
		}
		row := removalBatchActualRow(f.api.base, m.Request.Resource, m.Request.ID)
		if m.Request.Resource != api.FilterRule || !strings.HasPrefix(row["comment"], plan.Owner+":g:") {
			break
		}
		f.batch.Operations = append(f.batch.Operations, m)
	}
	if len(f.batch.Operations) < 2 {
		t.Fatal("fixture needs a real contiguous rule prefix")
	}
	t.Cleanup(func() { _ = f.r.Close() })
	return f
}
func removalBatchActualRow(a *fakeAPI, resource api.Resource, id string) api.Record {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, row := range *cleanupModelRows(&a.inv, resource) {
		if strings.EqualFold(row[".id"], id) {
			return row
		}
	}
	return nil
}
func removalBatchPrefix(batch control.RuleRemovalBatch, n int) control.RuleRemovalBatch {
	out := batch
	out.Operations = append([]routeros.APIMutation(nil), batch.Operations[:n]...)
	return out
}

func TestRuleRemovalBatchUsesActualObjectReadsAndDurableOrderedPrefix(t *testing.T) {
	for _, mode := range []control.RuleRemovalMode{control.RuleRemovalCleanup, control.RuleRemovalPreparedDiscard} {
		t.Run(string(mode), func(t *testing.T) {
			f := newRemovalBatchFixture(t, mode)
			batch := removalBatchPrefix(f.batch, 2)
			recorder := &removalBatchRecorderModel{}
			inventoryReads := f.api.base.calls
			if err := f.r.ApplyRuleRemovalBatch(context.Background(), batch, recorder); err != nil {
				t.Fatal(err)
			}
			if f.api.base.writes-f.initialWrites != 2 || f.index() != f.initialIndex+2 || len(recorder.before) != 2 || len(recorder.result) != 2 || len(recorder.absent) != 2 {
				t.Fatal("completed prefix/count mismatch")
			}
			if f.api.base.calls-inventoryReads != 1 {
				t.Fatal("per-item reads were replaced with repeated global scans", f.api.base.calls-inventoryReads)
			}
			for _, m := range batch.Operations {
				if removalBatchActualRow(f.api.base, api.FilterRule, m.Request.ID) != nil || f.api.reads[cleanupKey(api.FilterRule, m.Request.ID)] != 4 {
					t.Fatal("target did not receive two real before and two real absence reads")
				}
			}
			want := []string{}
			for _, m := range batch.Operations {
				want = append(want, "before:"+m.OperationID, "result:"+m.OperationID, "absent:"+m.OperationID)
			}
			if !reflect.DeepEqual(recorder.steps, want) {
				t.Fatal("WAL/receipt/absence ordering changed")
			}
			for _, o := range recorder.observations {
				if o.BatchID != batch.ID || o.Target != f.r.target || !o.Owner.Known || !o.Owner.Alive || o.Owner.Active || o.Owner.ThisSession || len(o.Protected) == 0 {
					t.Fatal("incomplete local evidence")
				}
				if o.Present != (o.Attributes != nil) {
					t.Fatal("absence carried fabricated attributes")
				}
			}
			if f.api.base.credentialCalls != 0 || f.term.controls != 0 {
				t.Fatal("batch used secret/owner mutation authority")
			}
			if len(f.batch.Operations) > 2 {
				next := f.batch
				next.ID = "batch-fixture-2"
				next.Operations = append([]routeros.APIMutation(nil), f.batch.Operations[2:3]...)
				next.ScopeChecksum = strings.Repeat("b", 64)
				if err := f.r.ApplyRuleRemovalBatch(context.Background(), next, &removalBatchRecorderModel{}); err == nil || f.api.base.writes-f.initialWrites != 2 {
					t.Fatal("later batch changed original authorization scope")
				}
				next.ScopeChecksum = batch.ScopeChecksum
				if err := f.r.ApplyRuleRemovalBatch(context.Background(), next, &removalBatchRecorderModel{}); err != nil || f.api.base.writes-f.initialWrites != 3 {
					t.Fatal("same-scope next prefix could not continue", err)
				}
			}
		})
	}
}

func TestRuleRemovalBatchRejectsScopeOrderRootsAndClosedOwner(t *testing.T) {
	for _, mode := range []string{"gate", "wrong-plan", "wrong-mode", "reordered", "skip-prefix", "duplicate", "root", "empty", "over-limit", "foreign-owner", "closed-channel", "expired-session"} {
		t.Run(mode, func(t *testing.T) {
			f := newRemovalBatchFixture(t, control.RuleRemovalPreparedDiscard)
			batch := removalBatchPrefix(f.batch, 2)
			switch mode {
			case "gate":
				f.r.profile.RuleRemovalBatchVerified = false
			case "wrong-plan":
				batch.PlanChecksum = strings.Repeat("b", 64)
			case "wrong-mode":
				batch.Mode = "arbitrary"
			case "reordered":
				batch.Operations[0], batch.Operations[1] = batch.Operations[1], batch.Operations[0]
			case "skip-prefix":
				batch.Operations = batch.Operations[1:]
			case "duplicate":
				batch.Operations[1] = batch.Operations[0]
			case "root":
				batch.Operations[0].Request.ID = f.api.base.inv.FilterRules[0][".id"]
			case "empty":
				batch.Operations = nil
			case "over-limit":
				for len(batch.Operations) <= maxRemovalRules {
					batch.Operations = append(batch.Operations, batch.Operations[0])
				}
			case "foreign-owner":
				f.api.base.inv.SafeMode = &api.SafeModeState{Enabled: true}
			case "closed-channel":
				_ = f.r.CloseOwner()
			case "expired-session":
				f.r.sessionExpiresAt = time.Now().Add(-time.Second)
			}
			recorder := &removalBatchRecorderModel{}
			if err := f.r.ApplyRuleRemovalBatch(context.Background(), batch, recorder); err == nil || f.api.base.writes != f.initialWrites || len(recorder.before) != 0 {
				t.Fatal("invalid batch reached an intent/write", mode, err)
			}
		})
	}
}

func TestRuleRemovalBatchRejectsAuthorizedRootAtCurrentDiscardCursor(t *testing.T) {
	f := newRemovalBatchFixture(t, control.RuleRemovalPreparedDiscard)
	for _, mutation := range f.batch.Operations {
		if _, err := f.r.ApplyPreparedDiscard(context.Background(), mutation); err != nil {
			t.Fatal(err)
		}
	}
	raw, evidence, _, _, err := f.r.readState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := routeros.NewAPIResolver(raw, f.r.target, evidence)
	if err != nil {
		t.Fatal(err)
	}
	operation := f.r.preparedDiscard.operations[f.r.preparedDiscard.next]
	root, err := f.r.preparedDiscard.bridge.Mutation(operation.ID, resolver)
	if err != nil {
		t.Fatal(err)
	}
	row := removalBatchActualRow(f.api.base, root.Request.Resource, root.Request.ID)
	if root.Request.Resource != api.FilterRule || !strings.Contains(row["chain"], "_root_") {
		t.Fatal("fixture did not reach an approved root removal")
	}
	before := f.api.base.writes
	batch := f.batch
	batch.Operations = []routeros.APIMutation{root}
	if err := f.r.ApplyRuleRemovalBatch(context.Background(), batch, &removalBatchRecorderModel{}); err == nil || f.api.base.writes != before {
		t.Fatal("an otherwise authorized root was deleted through rule batching", err)
	}
}

func TestRuleRemovalBatchRejectsTwoAuthorizedGenerations(t *testing.T) {
	r, base, terminal, request := observerFixture()
	defer r.Close()
	initial, err := r.Observe(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	known := routeros.KnownState{Target: r.target}
	snapshot := *initial.Snapshot
	router := routing.Router{ID: r.target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1"}
	var plan routeros.Plan
	for i := 0; i < 3; i++ {
		router.RouterID = fmt.Sprintf("192.0.2.%d", i+1)
		options := routeros.PlanOptions{Bootstrap: i == 0, AllowIdentityChange: i > 0}
		plan, err = routeros.BuildPlan(r.target, router, nil, snapshot, options)
		if err != nil {
			t.Fatal(err)
		}
		cleanupModelApplyPlan(t, plan, nil, &base.inv)
		known.ActiveGeneration = plan.Generation
		known.Generations = append(known.Generations, routeros.KnownGeneration{Target: r.target, Desired: plan.Desired, PolicyChecksum: plan.PolicyChecksum})
		evidence, e := r.identity(context.Background(), base.inv)
		if e != nil {
			t.Fatal(e)
		}
		snapshot, err = routeros.BuildSnapshot(base.inv, known, routeros.CredentialEvidence{}, evidence)
		if err != nil {
			t.Fatal(err)
		}
	}
	model := &cleanupAPIModel{fakeAPI: base}
	r.api = model
	r.terminal = &cleanupTerminalModel{fakeTerminal: terminal}
	r.profile.CleanupVerified = true
	r.profile.RuleRemovalBatchVerified = true
	bridge, err := routeros.NewCleanupBridge(plan)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, op := range bridge.Operations() {
		ids = append(ids, op.ID)
	}
	if err = r.BindCleanup(context.Background(), control.CleanupBinding{Plan: plan, RemainingOperationIDs: ids}); err != nil {
		t.Fatal(err)
	}
	apiModel := &removalBatchAPIModel{apiChannel: model, base: base, safe: model, apply: model.Apply, reads: map[string]int{}}
	r.api = apiModel
	raw, evidence, _, _, err := r.readState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := routeros.NewAPIResolver(raw, r.target, evidence)
	if err != nil {
		t.Fatal(err)
	}
	batch := control.RuleRemovalBatch{ID: "two-generation-batch", TransactionID: "two-generation-transaction", PlanChecksum: plan.Checksum, ScopeChecksum: strings.Repeat("a", 64), Mode: control.RuleRemovalCleanup}
	generations := map[string]bool{}
	for _, id := range ids {
		m, e := bridge.Mutation(id, resolver)
		if e != nil {
			t.Fatal(e)
		}
		batch.Operations = append(batch.Operations, m)
		row := removalBatchActualRow(base, api.FilterRule, m.Request.ID)
		generation, ok := removalRuleGeneration(plan.Owner, row)
		if !ok {
			t.Fatal("fixture selected a non-generation target")
		}
		generations[generation] = true
	}
	if len(generations) != 2 {
		t.Fatal("fixture did not contain two complete authorized generations")
	}
	if err = r.ApplyRuleRemovalBatch(context.Background(), batch, &removalBatchRecorderModel{}); err == nil || base.writes != 0 {
		t.Fatal("multiple authorized generations were silently combined", err)
	}
}

func TestRuleRemovalBatchRejectsActualTargetAndProtectionDrift(t *testing.T) {
	for _, mode := range []string{"target-changed", "target-unstable", "wrong-id", "missing-fib", "protected-table", "protected-instance", "protected-root", "protected-connection", "late-foreign-owner"} {
		t.Run(mode, func(t *testing.T) {
			f := newRemovalBatchFixture(t, control.RuleRemovalCleanup)
			batch := removalBatchPrefix(f.batch, 2)
			target := batch.Operations[0].Request.ID
			changed := false
			f.api.onRead = func(resource api.Resource, id string, count int) {
				if changed {
					return
				}
				if resource == api.FilterRule && id == target && (mode == "target-changed" || mode == "target-unstable") && (mode != "target-unstable" || count == 2) {
					row := removalBatchActualRow(f.api.base, resource, id)
					row["rule"] += " "
					changed = true
					return
				}
				field := ""
				if mode == "protected-table" && resource == api.RoutingTable {
					field = "comment"
				}
				if mode == "protected-instance" && resource == api.BGPInstance {
					field = "comment"
				}
				if mode == "protected-root" && resource == api.FilterRule && id != target {
					field = "rule"
				}
				if mode == "protected-connection" && resource == api.BGPConnection {
					field = "comment"
				}
				if field != "" {
					removalBatchActualRow(f.api.base, resource, id)[field] = "changed-protected-value"
					changed = true
				}
			}
			f.api.response = func(resource api.Resource, id string, _ int, result api.ObjectPresence) (api.ObjectPresence, error) {
				if mode == "wrong-id" && resource == api.FilterRule && id == target {
					result.Record[".id"] = "*FFFF"
				}
				if mode == "missing-fib" && resource == api.RoutingTable {
					delete(result.Record, "fib")
				}
				return result, nil
			}
			if mode == "late-foreign-owner" {
				f.api.safeFault = true
			}
			recorder := &removalBatchRecorderModel{}
			if err := f.r.ApplyRuleRemovalBatch(context.Background(), batch, recorder); err == nil || f.api.base.writes != f.initialWrites || len(recorder.before) != 0 {
				t.Fatal("object/protection drift permitted write", mode, err)
			}
		})
	}
}

func TestRuleRemovalBatchStopsOnWALAndUnknownWriteWithoutReplay(t *testing.T) {
	for _, mode := range []string{"before", "second-before", "result", "absence", "unknown-write", "kept-target", "error-absence", "nonempty-absence"} {
		t.Run(mode, func(t *testing.T) {
			f := newRemovalBatchFixture(t, control.RuleRemovalCleanup)
			batch := removalBatchPrefix(f.batch, 2)
			recorder := &removalBatchRecorderModel{}
			switch mode {
			case "before", "second-before":
				recorder.beforeHook = func(n int, _ control.RuleRemovalObservation) error {
					if mode == "before" || n == 2 {
						return errors.New("fixture WAL refused")
					}
					return nil
				}
			case "result":
				recorder.resultHook = func(int, api.Result, error) error { return errors.New("fixture result WAL refused") }
			case "absence":
				recorder.absentHook = func(int, control.RuleRemovalObservation) error { return errors.New("fixture absence WAL refused") }
			case "unknown-write":
				f.unknown()
			case "kept-target":
				f.api.keepTarget = true
			case "error-absence", "nonempty-absence":
				f.api.response = func(resource api.Resource, id string, _ int, result api.ObjectPresence) (api.ObjectPresence, error) {
					if resource == api.FilterRule && id == batch.Operations[0].Request.ID && !result.Present {
						if mode == "error-absence" {
							return api.ObjectPresence{}, &api.Error{Code: api.CodeTrap}
						}
						result.Record = api.Record{}
					}
					return result, nil
				}
			}
			if err := f.r.ApplyRuleRemovalBatch(context.Background(), batch, recorder); err == nil {
				t.Fatal("failed batch accepted", mode)
			}
			writes := f.api.base.writes - f.initialWrites
			if mode == "before" {
				if writes != 0 {
					t.Fatal("WAL rejection followed by mutation")
				}
			} else if writes != 1 {
				t.Fatal("batch wrote beyond its proved prefix", mode, writes)
			}
			if mode != "before" && mode != "second-before" {
				if !f.r.poisoned {
					t.Fatal("unknown/postwrite state not poisoned", mode)
				}
				_ = f.r.ApplyRuleRemovalBatch(context.Background(), batch, &removalBatchRecorderModel{})
				if f.api.base.writes-f.initialWrites != writes {
					t.Fatal("uncertain write repeated")
				}
			}
		})
	}
}

func TestRuleRemovalBatchRechecksChannelAndOwnerAfterWAL(t *testing.T) {
	for _, mode := range []string{"close-channel", "foreign-owner", "expired-session", "epoch-changed", "session-changed"} {
		t.Run(mode, func(t *testing.T) {
			f := newRemovalBatchFixture(t, control.RuleRemovalCleanup)
			batch := removalBatchPrefix(f.batch, 2)
			recorder := &removalBatchRecorderModel{}
			recorder.beforeHook = func(int, control.RuleRemovalObservation) error {
				switch mode {
				case "close-channel":
					return f.r.CloseOwner()
				case "foreign-owner":
					f.api.safeFault = true
				case "expired-session":
					f.r.sessionExpiresAt = time.Now().Add(-time.Second)
				case "epoch-changed":
					f.r.ownerMu.Lock()
					f.r.ownerEpoch++
					f.r.ownerMu.Unlock()
				case "session-changed":
					f.r.sessionID += "-new"
				}
				return nil
			}
			if err := f.r.ApplyRuleRemovalBatch(context.Background(), batch, recorder); err == nil || f.api.base.writes != f.initialWrites || len(recorder.before) != 1 || !f.r.poisoned {
				t.Fatal("write escaped post-WAL channel/owner gate", mode, err)
			}
			f.api.safeFault = false
			f.r.sessionExpiresAt = time.Now().Add(time.Minute)
			if err := f.r.ApplyRuleRemovalBatch(context.Background(), batch, &removalBatchRecorderModel{}); err == nil || f.api.base.writes != f.initialWrites {
				t.Fatal("post-intent uncertainty was replayed without a fresh runtime", mode)
			}
		})
	}
}

func TestRuleRemovalBatchRechecksSessionAfterPostWALOwnerQuery(t *testing.T) {
	for _, mode := range []string{"close-during-owner-query", "expire-exactly-during-owner-query"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newRemovalBatchFixture(t, control.RuleRemovalCleanup)
				batch := removalBatchPrefix(f.batch, 2)
				recorder := &removalBatchRecorderModel{}
				queries, failedResults := 0, 0
				recorder.beforeHook = func(int, control.RuleRemovalObservation) error {
					// The first post-WAL query starts with a valid owner/session,
					// then returns owner-none after the local channel is invalid.
					f.api.onSafe = func() {
						queries++
						if mode == "close-during-owner-query" {
							if err := f.r.CloseOwner(); err != nil {
								t.Fatal(err)
							}
						} else {
							f.r.sessionExpiresAt = time.Now()
						}
					}
					return nil
				}
				recorder.resultHook = func(_ int, result api.Result, err error) error {
					if result.Acknowledged || err == nil || liveCode(err) != "rule-removal-session-lost" {
						t.Fatal("invalid session result was not recorded as failure")
					}
					failedResults++
					return nil
				}
				err := f.r.ApplyRuleRemovalBatch(context.Background(), batch, recorder)
				if err == nil || queries != 1 || failedResults != 1 || len(recorder.before) != 1 || len(recorder.result) != 1 || len(recorder.absent) != 0 || f.api.base.writes != f.initialWrites || !f.r.poisoned {
					t.Fatal("owner-none reply outlived the post-WAL session gate", err)
				}
			})
		})
	}
}

func TestRuleRemovalBatchSoftWindowStopsBeforeNextIntent(t *testing.T) {
	for _, mode := range []string{"after-first-proof", "during-next-read"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newRemovalBatchFixture(t, control.RuleRemovalCleanup)
				batch := removalBatchPrefix(f.batch, 2)
				recorder := &removalBatchRecorderModel{}
				if mode == "after-first-proof" {
					recorder.absentHook = func(n int, _ control.RuleRemovalObservation) error {
						if n == 1 {
							time.Sleep(removalWriteWindow + time.Second)
						}
						return nil
					}
				}
				if mode == "during-next-read" {
					f.api.onRead = func(resource api.Resource, id string, count int) {
						if resource == api.FilterRule && id == batch.Operations[1].Request.ID && count == 1 {
							time.Sleep(removalWriteWindow + time.Second)
						}
					}
				}
				if err := f.r.ApplyRuleRemovalBatch(context.Background(), batch, recorder); err != nil {
					t.Fatal("soft window incorrectly became unknown write", err)
				}
				if f.api.base.writes-f.initialWrites != 1 || len(recorder.before) != 1 || len(recorder.result) != 1 || len(recorder.absent) != 1 || f.index() != f.initialIndex+1 || f.r.poisoned {
					t.Fatal("soft cutoff opened another intent or lost the completed prefix")
				}
			})
		})
	}
}
