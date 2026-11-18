package control

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
)

// This model reads its actual simulated device rows, never the supervisor's
// expected current state. It cannot claim native transport/rollback evidence.
type objectModel struct {
	*memoryRuntime
	plan                                              routeros.Plan
	bridge                                            *routeros.APIBridge
	resolver                                          *routeros.APIResolver
	fullReads, objectReads, objectWrites, bindObjects int
	fullAtBind                                        int
	globalAfterObjectWrites                           []int
	drift                                             string
	wrongID                                           bool
	postConfirmDrift                                  bool
}

func (r *objectModel) Binding() Binding {
	b := r.memoryRuntime.Binding()
	b.ObjectObservationVerified = true
	return b
}
func (r *objectModel) BindPlan(p routeros.Plan, c routeros.Credentials) error {
	if e := r.memoryRuntime.BindPlan(p, c); e != nil {
		return e
	}
	r.plan = p
	var e error
	r.bridge, e = routeros.NewAPIBridge(p, c)
	return e
}
func (r *objectModel) Observe(ctx context.Context, q ReadRequest) (Observation, error) {
	r.fullReads++
	if r.objectWrites > 0 {
		r.globalAfterObjectWrites = append(r.globalAfterObjectWrites, r.objectWrites)
	}
	return r.memoryRuntime.Observe(ctx, q)
}
func (r *objectModel) BindObjectActivation(_ context.Context, sum string) error {
	if sum != r.plan.Checksum || !r.owner.Active {
		return errors.New("not owned")
	}
	r.bindObjects++
	r.fullAtBind = r.fullReads
	var e error
	r.resolver, e = routeros.NewAPIResolver(copyInventory(r.inv), r.target, r.evidence())
	return e
}
func (r *objectModel) ObserveObject(_ context.Context, q ObjectReadRequest) (ObjectObservation, error) {
	if q.PlanChecksum != r.plan.Checksum || r.resolver == nil {
		return ObjectObservation{}, errors.New("not bound")
	}
	m, e := r.bridge.Mutation(q.OperationID, r.resolver)
	if e != nil {
		return ObjectObservation{}, e
	}
	r.objectReads++
	for _, row := range *rowsFor(&r.inv, m.Request.Resource) {
		if row[".id"] == m.Request.ID {
			id := m.Request.ID
			if r.wrongID {
				id = "*FFFF"
			}
			return ObjectObservation{Target: r.target, Resource: m.Request.Resource, ID: id, Attributes: copyRecord(row), ObservedAt: time.Now().UTC(), Owner: r.owner, CrossChannelIdentityVerified: true}, nil
		}
	}
	return ObjectObservation{}, errors.New("missing actual row")
}
func (r *objectModel) addDrift() {
	if r.drift == "other-owned" {
		r.inv.Instances[0]["comment"] = "unplanned"
		return
	}
	r.inv.Tables = append(r.inv.Tables, api.Record{".id": "*F000", "name": "unrelated", "fib": "true", "disabled": "false", "dynamic": "false", "invalid": "false", "comment": "external"})
}
func (r *objectModel) ApplyObject(ctx context.Context, m routeros.APIMutation) (api.Result, error) {
	r.objectWrites++
	result, e := r.memoryRuntime.Apply(ctx, m)
	if r.objectWrites == 1 && r.drift != "" {
		r.addDrift()
		r.owner.HistoryActions++
	}
	return result, e
}
func (r *objectModel) Confirm(ctx context.Context) (Owner, error) {
	owner, e := r.memoryRuntime.Confirm(ctx)
	if e == nil && r.postConfirmDrift {
		r.addDrift()
	}
	return owner, e
}
func objectTestSupervisor(t *testing.T) (*Supervisor, *objectModel, Request) {
	t.Helper()
	base, request := fixtureRuntime(t)
	r := &objectModel{memoryRuntime: base}
	s, e := Open(testConfig(t, base), r)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, r, request
}
func TestObjectActivationUsesRealLocalReadbacksAndRetainsGlobalBoundaries(t *testing.T) {
	s, r, request := objectTestSupervisor(t)
	st, e := s.Stage(context.Background(), request)
	if e != nil || st.Phase != AwaitingConfirmation {
		t.Fatal(st, e)
	}
	if r.bindObjects != 1 || r.objectWrites == 0 || r.objectReads != 2*r.objectWrites || r.fullReads != r.fullAtBind+1 {
		t.Fatal("object path omitted per-target or global-final proof")
	}
	for _, n := range r.globalAfterObjectWrites {
		if n != r.objectWrites {
			t.Fatal("unexpected global snapshot during local steps")
		}
	}
	before := r.fullReads
	st, e = s.Confirm(context.Background(), st.TransactionID, st.PlanChecksum)
	if e != nil || st.Phase != Confirmed || !st.ConfirmationVerified || r.fullReads != before+2 {
		t.Fatal("confirm global before/after checks lost", e)
	}
}
func TestObjectActivationDetectsOffTargetChangesAtFinalGlobalBoundary(t *testing.T) {
	for _, drift := range []string{"external", "other-owned"} {
		t.Run(drift, func(t *testing.T) {
			s, r, request := objectTestSupervisor(t)
			r.drift = drift
			st, e := s.Stage(context.Background(), request)
			if e == nil || st.Phase != RolledBack || !st.RollbackVerified || r.confirmCount != 0 || r.cancelCount != 1 {
				t.Fatal("off-target drift was published or not cancelled", st, e)
			}
			operations := 0
			for _, op := range request.Plan.Operations {
				if op.Phase == routeros.Activate {
					operations++
				}
			}
			if r.objectWrites != operations || len(r.globalAfterObjectWrites) == 0 || r.globalAfterObjectWrites[0] != operations {
				t.Fatal("test did not exercise deferred global drift detection")
			}
			if s.desiredVerified {
				t.Fatal("local deltas became a verified global snapshot")
			}
		})
	}
}
func TestObjectActivationRejectsWrongTargetAndUnknownACKWithoutReplay(t *testing.T) {
	for _, mode := range []string{"wrong-target", "unknown-ack", "missing-strict"} {
		t.Run(mode, func(t *testing.T) {
			s, r, request := objectTestSupervisor(t)
			if mode == "wrong-target" {
				r.wrongID = true
			}
			if mode == "missing-strict" {
				r.missingDesiredSnapshot = true
			}
			if mode == "unknown-ack" {
				for _, op := range request.Plan.Operations {
					if op.Phase == routeros.Activate {
						r.failOperation = op.ID
						break
					}
				}
			}
			st, e := s.Stage(context.Background(), request)
			if e == nil || st.Phase != RolledBack || r.confirmCount != 0 {
				t.Fatal("invalid object proof or unknown write accepted", st, e)
			}
			if mode == "wrong-target" && r.objectWrites != 0 {
				t.Fatal("wrong object authorized a write")
			}
			if mode == "unknown-ack" && (r.objectWrites != 1 || r.byOperation[r.failOperation] != 1) {
				t.Fatal("uncertain write retried")
			}
		})
	}
}
func TestObjectActivationConfirmStillRejectsGlobalDrift(t *testing.T) {
	for _, when := range []string{"before", "after"} {
		t.Run(when, func(t *testing.T) {
			s, r, request := objectTestSupervisor(t)
			st, e := s.Stage(context.Background(), request)
			if e != nil {
				t.Fatal(e)
			}
			if when == "before" {
				r.addDrift()
			} else {
				r.postConfirmDrift = true
			}
			st, e = s.Confirm(context.Background(), st.TransactionID, st.PlanChecksum)
			if e == nil || st.ConfirmationVerified {
				t.Fatal("changed global state was confirmed")
			}
			if when == "before" && (r.confirmCount != 0 || st.Phase != RolledBack) {
				t.Fatal("pre-confirm drift did not cancel")
			}
			if when == "after" && (r.confirmCount != 1 || st.Phase != Uncertain || r.cancelCount != 0) {
				t.Fatal("post-release uncertainty became rollback/confirmation")
			}
		})
	}
}
func TestObjectPredictionRejectsMembershipIdentityAndFIBChanges(t *testing.T) {
	row := api.Record{".id": "*1", "chain": "owned_root", "comment": "owned:in", "rule": "reject;", "disabled": "false", "inactive": "false"}
	for _, m := range []api.Mutation{
		{Resource: api.FilterRule, Action: api.Remove, ID: "*1"},
		{Resource: api.FilterRule, Action: api.Set, ID: "*1", Attributes: []api.Attribute{{Name: "chain", Value: "elsewhere"}}},
		{Resource: api.FilterRule, Action: api.Set, ID: "*1", Attributes: []api.Attribute{{Name: "comment", Value: "different"}}},
		{Resource: api.RoutingTable, Action: api.Unset, ID: "*1", Property: "fib"},
	} {
		if _, e := ExpectedObjectAttributes(row, m); e == nil {
			t.Fatal("forbidden object-only operation accepted")
		}
	}
	before := copyRecord(row)
	got, e := ExpectedObjectAttributes(row, api.Mutation{Resource: api.FilterRule, Action: api.Set, ID: "*1", Attributes: []api.Attribute{{Name: "rule", Value: "jump approved;"}}})
	if e != nil || got["rule"] != "jump approved;" || row["rule"] != before["rule"] {
		t.Fatal("pure object prediction changed original", e)
	}
}

func TestObjectDeltaJournalReopenUsesOnlyFullReadOnlyRecovery(t *testing.T) {
	base, request := fixtureRuntime(t)
	r := &objectModel{memoryRuntime: base}
	cfg := testConfig(t, base)
	s, e := Open(cfg, r)
	if e != nil {
		t.Fatal(e)
	}
	st, e := s.Stage(context.Background(), request)
	if e != nil {
		_ = s.Close()
		t.Fatal(e)
	}
	if st.Phase != AwaitingConfirmation || r.objectWrites == 0 {
		_ = s.Close()
		t.Fatal("fixture did not record object deltas")
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	fresh := &objectModel{memoryRuntime: base}
	reopened, e := Open(cfg, fresh)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	st, e = reopened.Recover(context.Background())
	if e != nil || st.Phase != RolledBack || !st.RollbackVerified {
		t.Fatal("authenticated object delta WAL did not reconcile complete baseline", st, e)
	}
	if fresh.objectReads != 0 || fresh.objectWrites != 0 || fresh.bindObjects != 0 || fresh.fullReads == 0 {
		t.Fatal("restart resumed a partial-object workflow")
	}
	if _, e = reopened.Confirm(context.Background(), st.TransactionID, st.PlanChecksum); e == nil {
		t.Fatal("new runtime confirmed former session")
	}
}
