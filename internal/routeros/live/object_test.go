package live

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routeros/terminal"
	"github.com/moedove/ixp-manager/internal/routing"
)

// These are actual in-memory read dictionaries. No callback substitutes a
// predicted global snapshot, no production capability is enabled, and opaque
// credentials remain genuinely Unknown. Known-empty/MD5 success belongs to the
// existing native API evidence; tests must not manufacture SecretValue bytes.
type typedObjectAPI struct {
	*fakeAPI
	identityReads, objectReads int
	queried                    []api.PropertyRequest
	credentialIDs              []string
	omit                       map[string]bool
	unknownGetter              bool
	identityFault              bool
	rowFault                   func(int, api.Record) (api.Record, error)
	afterGetter                func(*api.Inventory)
}

func (a *typedObjectAPI) ReadIdentity(context.Context) (api.Inventory, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.identityReads++
	if a.identityFault {
		return api.Inventory{Resources: []api.Record{{"version": "7.24.6"}}, Licenses: []api.Record{{"system-id": "AnotherDevice"}}}, nil
	}
	snapshot := cloneInventory(a.inv)
	return api.Inventory{Resources: snapshot.Resources, Licenses: snapshot.Licenses, RouterBoards: snapshot.RouterBoards, Capabilities: map[api.Resource]api.Capability{api.SystemResource: api.Supported, api.SystemLicense: api.Supported}}, nil
}
func (a *typedObjectAPI) ReadObject(_ context.Context, resource api.Resource, id string) (api.Record, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.objectReads++
	for _, row := range *cleanupModelRows(&a.inv, resource) {
		if strings.EqualFold(row[".id"], id) {
			copy := objectRecordCopy(row)
			for field := range a.omit {
				delete(copy, field)
			}
			if a.rowFault != nil {
				return a.rowFault(a.objectReads, copy)
			}
			return copy, nil
		}
	}
	return nil, errors.New("actual object missing")
}
func (a *typedObjectAPI) ReadProperty(_ context.Context, resource api.Resource, id, field string) (api.PropertyValue, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.getters++
	a.queried = append(a.queried, api.PropertyRequest{Resource: resource, ID: id, Property: field})
	if a.unknownGetter {
		return api.PropertyValue{}, nil
	}
	for _, row := range *cleanupModelRows(&a.inv, resource) {
		if strings.EqualFold(row[".id"], id) {
			value, known := row[field]
			if a.afterGetter != nil {
				a.afterGetter(&a.inv)
			}
			return api.PropertyValue{Known: known, Value: value}, nil
		}
	}
	return api.PropertyValue{}, errors.New("actual property target missing")
}
func (a *typedObjectAPI) ReadCredential(_ context.Context, id string) (api.Credential, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.credentialCalls++
	a.credentialIDs = append(a.credentialIDs, id)
	return api.Credential{}, nil
}

type typedObjectTerminal struct {
	*fakeTerminal
	model      *typedObjectAPI
	allowed    *api.Mutation
	writes     int
	unknownACK bool
}

func (t *typedObjectTerminal) EnterSafeMode(ctx context.Context) (terminal.Evidence, error) {
	value, err := t.fakeTerminal.EnterSafeMode(ctx)
	t.model.mu.Lock()
	t.model.inv.SafeMode = &api.SafeModeState{Enabled: true}
	t.model.mu.Unlock()
	return value, err
}
func (t *typedObjectTerminal) ExecuteOwned(_ context.Context, m api.Mutation) (terminal.OwnedReceipt, error) {
	if t.allowed == nil || !reflect.DeepEqual(*t.allowed, m) {
		return terminal.OwnedReceipt{}, errors.New("fixture rejects any mutation except its fixed compiled operation")
	}
	t.writes++
	t.model.mu.Lock()
	var found api.Record
	for _, row := range *cleanupModelRows(&t.model.inv, m.Resource) {
		if strings.EqualFold(row[".id"], m.ID) {
			found = row
			break
		}
	}
	if found == nil {
		t.model.mu.Unlock()
		return terminal.OwnedReceipt{}, errors.New("fixture has no actual write target")
	}
	switch m.Action {
	case api.Set:
		for _, attr := range m.Attributes {
			if attr.Name == "tcp-md5-key" {
				t.model.mu.Unlock()
				return terminal.OwnedReceipt{}, errors.New("fixture never writes credential plaintext")
			}
			value := attr.Value
			if value == "yes" {
				value = "true"
			}
			if value == "no" {
				value = "false"
			}
			found[attr.Name] = value
		}
	case api.Unset:
		found[m.Property] = ""
	case api.Enable:
		found["disabled"] = "false"
	case api.Disable:
		found["disabled"] = "true"
	default:
		t.model.mu.Unlock()
		return terminal.OwnedReceipt{}, errors.New("fixture never adds/removes objects")
	}
	t.model.mu.Unlock()
	t.mu.Lock()
	t.history++
	t.mu.Unlock()
	if t.unknownACK {
		return terminal.OwnedReceipt{}, errors.New("fixture applied write but lost its receipt")
	}
	return terminal.OwnedReceipt{Completed: true, Evidence: t.Snapshot()}, nil
}

type typedObjectFixture struct {
	runtime   *Runtime
	api       *typedObjectAPI
	terminal  *typedObjectTerminal
	plan      routeros.Plan
	request   control.ReadRequest
	mutations []routeros.APIMutation
}

func objectKnown(initial, next routeros.Plan) routeros.KnownState {
	known := nativeExtensionDesired(initial)
	if next.Checksum != "" {
		known.Generations = append(known.Generations, routeros.KnownGeneration{Target: next.Target, Desired: next.Desired, PolicyChecksum: next.PolicyChecksum})
	}
	return known
}
func objectPlanningSnapshot(t *testing.T, r *Runtime, model *typedObjectAPI, q control.ReadRequest, withPeer bool) routeros.Snapshot {
	t.Helper()
	observed, err := r.Observe(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if !withPeer {
		if observed.Snapshot == nil {
			t.Fatal("missing genuinely observed global snapshot")
		}
		return *observed.Snapshot
	}
	evidence, err := r.identity(context.Background(), observed.Inventory)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := routeros.BuildSnapshot(observed.Inventory, q.KnownState, routeros.CredentialEvidence{}, evidence)
	var failure *routeros.InspectError
	if !errors.As(err, &failure) || failure.Code != "unverified-credential" {
		t.Fatalf("negative credential fixture has invalid non-secret baseline: %v", err)
	}
	// Only old plan construction uses the established test-model baseline. All
	// runtime reads still return Unknown and BGP object proofs MUST reject them.
	for i := range snapshot.Connections {
		snapshot.Connections[i].AuthenticationVerified = true
	}
	return snapshot
}
func newTypedObjectFixture(t *testing.T, withPeer bool) *typedObjectFixture {
	t.Helper()
	r, base, ssh, empty := observerFixture()
	t.Cleanup(func() { _ = r.Close() })
	model := &typedObjectAPI{fakeAPI: base, omit: map[string]bool{}}
	owned := &typedObjectTerminal{fakeTerminal: ssh, model: model}
	r.api = model
	r.terminal = owned
	// Unit-private injected capability only. Config/production profile cannot
	// request it, and binding must still use actual Observe/Take/checkpoint reads.
	r.profile.PreparationVerified = true
	r.profile.ActivationVerified = true
	r.profile.ObjectObservationVerified = true
	r.profile.MaxHistoryActions = 80
	r.config.ObservationTimeoutSeconds = 5
	r.config.OperationTimeoutSeconds = 2
	observed, err := r.Observe(context.Background(), empty)
	if err != nil {
		t.Fatal(err)
	}
	device := routing.Router{ID: r.target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1"}
	peers := []routing.Peer{}
	if withPeer {
		peers = append(peers, routing.Peer{ID: "peer-a", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"203.0.113.0/24"}})
	}
	initial, err := routeros.BuildPlan(r.target, device, peers, *observed.Snapshot, routeros.PlanOptions{Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	cleanupModelApplyPlan(t, initial, nil, &model.inv)
	old := cloneInventory(model.inv)
	request := control.ReadRequest{KnownState: objectKnown(initial, routeros.Plan{}), Strict: !withPeer}
	baseline := objectPlanningSnapshot(t, r, model, request, withPeer)
	device.ASN = 64510
	device.RouterID = "192.0.2.9"
	draft, err := routeros.BuildPlan(r.target, device, peers, baseline, routeros.PlanOptions{AllowIdentityChange: true})
	if err != nil {
		t.Fatal(err)
	}
	cleanupModelApplyPlan(t, draft, nil, &model.inv)
	// Keep new generation rows, but restore every pre-existing object to its
	// observed old value. This models completed preparation, not activation.
	for _, resource := range []api.Resource{api.RoutingTable, api.BGPInstance, api.BGPConnection, api.FilterRule} {
		previous := map[string]api.Record{}
		for _, row := range *cleanupModelRows(&old, resource) {
			previous[row[".id"]] = row
		}
		rows := cleanupModelRows(&model.inv, resource)
		for i, row := range *rows {
			if before := previous[row[".id"]]; before != nil {
				(*rows)[i] = objectRecordCopy(before)
			}
		}
	}
	request.KnownState = objectKnown(initial, draft)
	prepared := objectPlanningSnapshot(t, r, model, request, withPeer)
	plan, err := routeros.BuildPlan(r.target, device, peers, prepared, routeros.PlanOptions{AllowIdentityChange: true})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.BindPlan(plan, nil); err != nil {
		t.Fatal(err)
	}
	for _, operation := range r.operations {
		if operation.Phase == routeros.Prepare {
			t.Fatalf("prepared fixture still has prepare operation: phase=%s resource=%s action=%s id=%s", operation.Phase, operation.Resource, operation.Action, operation.ID)
		}
	}
	if _, err = r.Observe(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	return &typedObjectFixture{runtime: r, api: model, terminal: owned, plan: plan, request: request}
}
func (f *typedObjectFixture) take(t *testing.T) {
	t.Helper()
	if _, err := f.runtime.Take(context.Background(), time.Now().Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
}
func (f *typedObjectFixture) checkpoint(t *testing.T) {
	t.Helper()
	observation, err := f.runtime.Observe(context.Background(), f.request)
	if err != nil {
		t.Fatal(err)
	}
	f.mutations = nil
	for _, operation := range f.runtime.operations {
		if operation.Phase != routeros.Activate {
			continue
		}
		m, e := f.runtime.bridge.Mutation(operation.ID, observation.Resolver)
		if e != nil {
			t.Fatal(e)
		}
		f.mutations = append(f.mutations, m)
	}
}
func (f *typedObjectFixture) bind(t *testing.T) {
	t.Helper()
	f.take(t)
	f.checkpoint(t)
	if err := f.runtime.BindObjectActivation(context.Background(), f.plan.Checksum); err != nil {
		t.Fatal(err)
	}
}
func (f *typedObjectFixture) query(m routeros.APIMutation, after bool) *control.ObjectReadRequest {
	request := &control.ObjectReadRequest{PlanChecksum: f.plan.Checksum, OperationID: m.OperationID, After: after}
	if m.Request.Resource == api.BGPConnection {
		f.api.mu.Lock()
		defer f.api.mu.Unlock()
		for _, row := range f.api.inv.Connections {
			if strings.EqualFold(row[".id"], m.Request.ID) {
				sum, err := control.CredentialAttributesChecksum(row)
				if err == nil {
					request.CredentialAuthority = &control.CredentialAuthority{ID: row[".id"], Name: row["name"], AttributesChecksum: sum}
				}
			}
		}
	}
	return request
}
func (f *typedObjectFixture) apply(t *testing.T, m routeros.APIMutation) {
	t.Helper()
	copy := m.Request
	copy.Attributes = append([]api.Attribute(nil), m.Request.Attributes...)
	f.terminal.allowed = &copy
	result, err := f.runtime.ApplyObject(context.Background(), m)
	if err != nil || !result.Acknowledged {
		t.Fatalf("compiled owned write failed: %v", err)
	}
}
func (f *typedObjectFixture) advanceToConnection(t *testing.T) routeros.APIMutation {
	t.Helper()
	for _, m := range f.mutations {
		if m.Request.Resource == api.BGPConnection {
			return m
		}
		if _, err := f.runtime.ObserveObject(context.Background(), *f.query(m, false)); err != nil {
			t.Fatal(err)
		}
		f.apply(t, m)
		if _, err := f.runtime.ObserveObject(context.Background(), *f.query(m, true)); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("fixture has no connection operation")
	return routeros.APIMutation{}
}

func TestObjectActivationRequiresTakenFullCheckpoint(t *testing.T) {
	for _, mode := range []string{"before-take", "without-checkpoint", "wrong-checksum", "valid"} {
		t.Run(mode, func(t *testing.T) {
			f := newTypedObjectFixture(t, false)
			if mode != "before-take" {
				f.take(t)
			}
			if mode == "wrong-checksum" || mode == "valid" {
				f.checkpoint(t)
			}
			checksum := f.plan.Checksum
			if mode == "wrong-checksum" {
				checksum = strings.Repeat("0", 64)
			}
			err := f.runtime.BindObjectActivation(context.Background(), checksum)
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("object activation bound without required global/owner evidence")
			}
			if f.terminal.writes != 0 || f.api.writes != 0 {
				t.Fatal("binding itself wrote device state")
			}
		})
	}
}
func TestObjectActivationActualBeforeAfterAndDetachedRows(t *testing.T) {
	f := newTypedObjectFixture(t, false)
	f.bind(t)
	inventoryCalls := f.api.calls
	seen := map[api.Resource]bool{}
	for _, m := range f.mutations {
		beforeReads := f.api.objectReads
		before, err := f.runtime.ObserveObject(context.Background(), *f.query(m, false))
		if err != nil {
			t.Fatal(err)
		}
		if before.Target != f.runtime.target || before.Resource != m.Request.Resource || before.ID != m.Request.ID || f.api.objectReads != beforeReads+2 || !before.CrossChannelIdentityVerified {
			t.Fatal("object proof did not come from two exact actual reads")
		}
		seen[m.Request.Resource] = true
		want, err := control.ExpectedObjectAttributes(before.Attributes, m.Request)
		if err != nil {
			t.Fatal(err)
		}
		before.Attributes["caller-mutated"] = "never authority"
		f.apply(t, m)
		after, err := f.runtime.ObserveObject(context.Background(), *f.query(m, true))
		if err != nil {
			t.Fatal(err)
		}
		gotDigest, _ := control.ObjectAttributesChecksum(m.Request.Resource, after.Attributes)
		wantDigest, _ := control.ObjectAttributesChecksum(m.Request.Resource, want)
		if gotDigest != wantDigest || f.api.objectReads != beforeReads+4 {
			t.Fatal("after was predicted instead of read")
		}
		after.Attributes["caller-mutated"] = "detached"
	}
	if !seen[api.FilterRule] || !seen[api.BGPInstance] || f.api.calls != inventoryCalls || f.api.writes != 0 || f.terminal.writes != len(f.mutations) {
		t.Fatal("typed object path lost instance/filter coverage or used the API write channel")
	}
}
func TestObjectActivationRejectsWrongReadTargetAndBinding(t *testing.T) {
	for _, mode := range []string{"checksum", "operation", "premature-after", "missing-id", "wrong-id", "missing-row", "changed-rule", "identity"} {
		t.Run(mode, func(t *testing.T) {
			f := newTypedObjectFixture(t, false)
			f.bind(t)
			m := f.mutations[0]
			q := f.query(m, false)
			switch mode {
			case "checksum":
				q.PlanChecksum = strings.Repeat("f", 64)
			case "operation":
				q.OperationID = "not-bound-operation"
			case "premature-after":
				q.After = true
			case "missing-id":
				f.api.rowFault = func(_ int, row api.Record) (api.Record, error) { delete(row, ".id"); return row, nil }
			case "wrong-id":
				f.api.rowFault = func(_ int, row api.Record) (api.Record, error) { row[".id"] = "*ABCD"; return row, nil }
			case "missing-row":
				f.api.rowFault = func(_ int, _ api.Record) (api.Record, error) { return nil, errors.New("missing actual target") }
			case "changed-rule":
				f.api.rowFault = func(_ int, row api.Record) (api.Record, error) { row["rule"] = "accept;"; return row, nil }
			case "identity":
				f.api.identityFault = true
			}
			if _, err := f.runtime.ObserveObject(context.Background(), *q); err == nil {
				t.Fatal("wrong object or request became evidence")
			}
			if f.terminal.writes != 0 || f.api.writes != 0 {
				t.Fatal("failed object read caused mutation")
			}
		})
	}
}
func TestObjectActivationMissingPropertiesUseActualGetters(t *testing.T) {
	for _, mode := range []string{"known", "unknown", "changes-during-getter"} {
		t.Run(mode, func(t *testing.T) {
			f := newTypedObjectFixture(t, false)
			f.bind(t)
			m := f.mutations[0]
			f.api.omit["disabled"] = true
			before := f.api.getters
			if mode == "unknown" {
				f.api.unknownGetter = true
			}
			if mode == "changes-during-getter" {
				f.api.afterGetter = func(inv *api.Inventory) {
					for _, row := range inv.FilterRules {
						if row[".id"] == m.Request.ID {
							row["rule"] = "reject; # drift"
						}
					}
				}
			}
			observation, err := f.runtime.ObserveObject(context.Background(), *f.query(m, false))
			if mode == "known" {
				if err != nil || observation.Attributes["disabled"] != "false" || f.api.getters != before+2 {
					t.Fatalf("explicit getter evidence missing: %v", err)
				}
			} else if err == nil {
				t.Fatal("missing/changed property was guessed stable")
			}
			if f.terminal.writes != 0 {
				t.Fatal("property getter caused a write")
			}
		})
	}
}
func TestObjectActivationRejectsUncompiledWritesAndConsumesProof(t *testing.T) {
	for _, mode := range []string{"operation", "attributes", "add", "remove", "fib"} {
		t.Run(mode, func(t *testing.T) {
			f := newTypedObjectFixture(t, false)
			f.bind(t)
			m := f.mutations[0]
			if _, err := f.runtime.ObserveObject(context.Background(), *f.query(m, false)); err != nil {
				t.Fatal(err)
			}
			bad := m
			bad.Request.Attributes = append([]api.Attribute(nil), m.Request.Attributes...)
			switch mode {
			case "operation":
				bad.OperationID = "unapproved"
			case "attributes":
				bad.Request.Attributes[0].Value = "accept;"
			case "add":
				bad.Request.Action = api.Add
			case "remove":
				bad.Request.Action = api.Remove
			case "fib":
				bad.Request.Resource = api.RoutingTable
				bad.Request.Action = api.Set
				bad.Request.Attributes = []api.Attribute{{Name: "fib", Value: "true"}}
			}
			if _, err := f.runtime.ApplyObject(context.Background(), bad); err == nil {
				t.Fatal("uncompiled object mutation accepted")
			}
			if _, err := f.runtime.ApplyObject(context.Background(), m); err == nil {
				t.Fatal("failed write attempt left a reusable before proof")
			}
			if f.terminal.writes != 0 || f.api.writes != 0 {
				t.Fatal("unsafe mutation reached a device channel")
			}
		})
	}
}
func TestObjectActivationPendingAfterBlocksNextWrite(t *testing.T) {
	f := newTypedObjectFixture(t, false)
	f.bind(t)
	first, next := f.mutations[0], f.mutations[1]
	if _, err := f.runtime.ObserveObject(context.Background(), *f.query(first, false)); err != nil {
		t.Fatal(err)
	}
	f.apply(t, first)
	if _, err := f.runtime.ApplyObject(context.Background(), next); err == nil || f.terminal.writes != 1 {
		t.Fatal("advanced before actual after read")
	}
	if _, err := f.runtime.ObserveObject(context.Background(), *f.query(first, true)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.runtime.ObserveObject(context.Background(), *f.query(next, false)); err != nil {
		t.Fatal(err)
	}
	f.apply(t, next)
	if f.terminal.writes != 2 {
		t.Fatal("verified after did not permit the next operation")
	}
}
func TestObjectActivationOwnerHistoryExpiryAndAfterDrift(t *testing.T) {
	for _, mode := range []string{"foreign-owner", "unknown-history", "history-budget", "expired-proof", "close-owner", "after-drift"} {
		t.Run(mode, func(t *testing.T) {
			f := newTypedObjectFixture(t, false)
			f.bind(t)
			m := f.mutations[0]
			if _, err := f.runtime.ObserveObject(context.Background(), *f.query(m, false)); err != nil {
				t.Fatal(err)
			}
			if mode == "after-drift" {
				f.apply(t, m)
				f.api.rowFault = func(_ int, row api.Record) (api.Record, error) { row["rule"] = "accept;"; return row, nil }
				if _, err := f.runtime.ObserveObject(context.Background(), *f.query(m, true)); err == nil {
					t.Fatal("after drift accepted")
				}
				return
			}
			switch mode {
			case "foreign-owner":
				f.terminal.mu.Lock()
				f.terminal.evidence.Owner = terminal.OwnerOther
				f.terminal.mu.Unlock()
			case "unknown-history":
				f.terminal.mu.Lock()
				f.terminal.historyKnown = false
				f.terminal.mu.Unlock()
			case "history-budget":
				f.terminal.mu.Lock()
				f.terminal.history = f.runtime.profile.MaxHistoryActions
				f.terminal.mu.Unlock()
			case "expired-proof":
				f.runtime.objectSession.before.at = time.Now().Add(-2 * time.Second)
			case "close-owner":
				_ = f.runtime.CloseOwner()
			}
			if _, err := f.runtime.ApplyObject(context.Background(), m); err == nil || f.terminal.writes != 0 {
				t.Fatal("stale ownership/history/expiry authorized a write")
			}
		})
	}
}
func TestObjectActivationUnknownACKNeverResendsEvenAfterReadback(t *testing.T) {
	f := newTypedObjectFixture(t, false)
	f.bind(t)
	m := f.mutations[0]
	if _, err := f.runtime.ObserveObject(context.Background(), *f.query(m, false)); err != nil {
		t.Fatal(err)
	}
	expected := m.Request
	f.terminal.allowed = &expected
	f.terminal.unknownACK = true
	result, err := f.runtime.ApplyObject(context.Background(), m)
	var failure *Failure
	if !errors.As(err, &failure) || !failure.Uncertain || result.Acknowledged || f.terminal.writes != 1 {
		t.Fatal("unknown write acknowledgement was trusted")
	}
	if _, err = f.runtime.ObserveObject(context.Background(), *f.query(m, true)); err != nil {
		t.Fatal("actual readback of an uncertain write should remain possible", err)
	}
	if _, err = f.runtime.ApplyObject(context.Background(), m); err == nil || f.terminal.writes != 1 {
		t.Fatal("uncertain write resent after successful readback")
	}
}
func TestObjectActivationCredentialUnknownAndWrongAuthorityCannotReadOtherObject(t *testing.T) {
	for _, mode := range []string{"unknown", "grant-id", "grant-name", "grant-checksum", "actual-id", "actual-attributes"} {
		t.Run(mode, func(t *testing.T) {
			f := newTypedObjectFixture(t, true)
			f.bind(t)
			m := f.advanceToConnection(t)
			q := f.query(m, false)
			if q.CredentialAuthority == nil {
				t.Fatal("fixture lacks actual attribute-bound grant")
			}
			before := len(f.api.credentialIDs)
			writes := f.terminal.writes
			switch mode {
			case "grant-id":
				q.CredentialAuthority.ID = "*FFFF"
			case "grant-name":
				q.CredentialAuthority.Name = "foreign-connection"
			case "grant-checksum":
				q.CredentialAuthority.AttributesChecksum = strings.Repeat("b", 64)
			case "actual-id":
				f.api.rowFault = func(_ int, row api.Record) (api.Record, error) { row[".id"] = "*FFFF"; return row, nil }
			case "actual-attributes":
				f.api.rowFault = func(_ int, row api.Record) (api.Record, error) {
					row["remote.address"] = "198.51.100.99"
					return row, nil
				}
			}
			if _, err := f.runtime.ObserveObject(context.Background(), *q); err == nil {
				t.Fatal("unknown or wrongly authorized credential became proof")
			}
			reads := f.api.credentialIDs[before:]
			if mode == "unknown" {
				if len(reads) != 1 || reads[0] != m.Request.ID {
					t.Fatal("unknown credential read wrong actual target")
				}
			} else if len(reads) != 0 {
				t.Fatal("credential getter ran before object/authority proof")
			}
			if f.terminal.writes != writes || f.api.writes != 0 {
				t.Fatal("failed credential proof wrote state")
			}
		})
	}
}
