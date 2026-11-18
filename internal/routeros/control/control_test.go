package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routing"
)

// memoryRuntime models fixed menu records and Safe Mode undo. It does not claim
// parser/native/transport evidence; production bindings remain separately gated.
type memoryRuntime struct {
	missingDesiredSnapshot                           bool
	mu                                               sync.Mutex
	target                                           routeros.Target
	inv                                              api.Inventory
	undo                                             api.Inventory
	owner                                            Owner
	gates                                            bool
	applyCount, takeCount, confirmCount, cancelCount int
	bindCount                                        int
	byOperation                                      map[string]int
	failOperation                                    string
	confirmLost                                      bool
	noUndo                                           bool
	unknownHistory                                   bool
	exhaustHistory                                   bool
	beforeApply                                      func(routeros.APIMutation)
}

func copyInventory(v api.Inventory) api.Inventory {
	data, _ := json.Marshal(v)
	var out api.Inventory
	_ = json.Unmarshal(data, &out)
	return out
}
func fixtureRuntime(t *testing.T) (*memoryRuntime, Request) {
	t.Helper()
	target := routeros.Target{RouterID: "router-control", Version: "7.24.6", DeviceID: "CHR-system-1", TransportIdentitySHA256: strings.Repeat("a", 64)}
	inv := api.Inventory{Resources: []api.Record{{"version": target.Version}}, Licenses: []api.Record{{"system-id": target.DeviceID}}, Capabilities: map[api.Resource]api.Capability{}}
	for _, r := range []api.Resource{api.SystemResource, api.SystemLicense, api.RoutingTable, api.BGPInstance, api.BGPConnection, api.FilterRule} {
		inv.Capabilities[r] = api.Supported
	}
	r := &memoryRuntime{target: target, inv: inv, gates: true, owner: Owner{Known: true, Alive: true, HistoryKnown: true}, byOperation: map[string]int{}}
	known := routeros.KnownState{Target: target}
	snapshot, err := routeros.BuildSnapshot(inv, known, routeros.CredentialEvidence{}, r.evidence())
	if err != nil {
		t.Fatal(err)
	}
	router := routing.Router{ID: target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1"}
	plan, err := routeros.BuildPlan(target, router, nil, snapshot, routeros.PlanOptions{Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	return r, Request{Plan: plan, KnownState: known, Timeout: 2 * time.Second, ExclusiveAccessAcknowledged: true}
}
func (r *memoryRuntime) evidence() routeros.TargetEvidence {
	return routeros.TargetEvidence{APIAuthenticated: true, APITransportIdentitySHA256: r.target.TransportIdentitySHA256, APIIdentitySource: routeros.LicenseSystemID, SSHAuthenticated: true, SSHTransportIdentitySHA256: r.target.TransportIdentitySHA256, SSHIdentitySource: routeros.LicenseSystemID, SSHDeviceID: r.target.DeviceID, SSHVersion: r.target.Version}
}
func (r *memoryRuntime) Binding() Binding {
	return Binding{Target: r.target, PrepareVerified: r.gates, ActivationVerified: r.gates, ConfirmationVerified: r.gates, MaxHistoryActions: 80}
}
func (r *memoryRuntime) BindPlan(p routeros.Plan, _ routeros.Credentials) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bindCount++
	if p.Target != r.target {
		return errors.New("wrong target")
	}
	return nil
}
func (r *memoryRuntime) Observe(ctx context.Context, q ReadRequest) (Observation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() != nil {
		return Observation{}, ctx.Err()
	}
	inv := copyInventory(r.inv)
	resolver, err := routeros.NewAPIResolver(inv, r.target, r.evidence())
	if err != nil {
		return Observation{}, err
	}
	o := Observation{Target: r.target, Inventory: inv, Resolver: resolver, Owner: r.owner, ObservedAt: time.Now().UTC(), CrossChannelIdentityVerified: true}
	if q.Strict {
		snapshot, err := routeros.BuildSnapshot(inv, q.KnownState, routeros.CredentialEvidence{}, r.evidence())
		if err != nil {
			return o, err
		}
		o.Snapshot = &snapshot
		if r.missingDesiredSnapshot && len(r.inv.Tables) > 0 {
			o.Snapshot = nil
		}
	}
	return o, nil
}
func rowsFor(inv *api.Inventory, r api.Resource) *[]api.Record {
	switch r {
	case api.RoutingTable:
		return &inv.Tables
	case api.BGPInstance:
		return &inv.Instances
	case api.FilterRule:
		return &inv.FilterRules
	case api.BGPConnection:
		return &inv.Connections
	}
	panic("unknown fixture menu")
}
func (r *memoryRuntime) Apply(ctx context.Context, envelope routeros.APIMutation) (api.Result, error) {
	if r.beforeApply != nil {
		r.beforeApply(envelope)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() != nil {
		return api.Result{}, ctx.Err()
	}
	m := envelope.Request
	if err := api.ValidateMutation(m); err != nil {
		return api.Result{}, err
	}
	r.applyCount++
	r.byOperation[envelope.OperationID]++
	rows := rowsFor(&r.inv, m.Resource)
	result := api.Result{Acknowledged: true}
	if m.Action == api.Add {
		used := map[string]bool{}
		for _, row := range *rows {
			used[row[".id"]] = true
		}
		for i := 1; result.ID == ""; i++ {
			id := fmt.Sprintf("*%X", i)
			if !used[id] {
				result.ID = id
			}
		}
		row := api.Record{".id": result.ID}
		switch m.Resource {
		case api.RoutingTable:
			row["fib"] = "false"
			row["disabled"] = "false"
			row["dynamic"] = "false"
			row["invalid"] = "false"
			row["comment"] = ""
		case api.BGPInstance:
			row["vrf"] = "main"
			row["disabled"] = "false"
			row["comment"] = ""
		case api.FilterRule:
			row["disabled"] = "false"
			row["inactive"] = "false"
		}
		for _, a := range m.Attributes {
			row[a.Name] = canonicalValue(m.Resource, a.Name, a.Value)
		}
		*rows = append(*rows, row)
	} else {
		found := false
		for i, row := range *rows {
			if row[".id"] != m.ID {
				continue
			}
			found = true
			switch m.Action {
			case api.Set:
				for _, a := range m.Attributes {
					row[a.Name] = canonicalValue(m.Resource, a.Name, a.Value)
				}
			case api.Enable:
				row["disabled"] = "false"
			case api.Disable:
				row["disabled"] = "true"
			case api.Unset:
				row[m.Property] = ""
			case api.Remove:
				*rows = append((*rows)[:i], (*rows)[i+1:]...)
			}
			break
		}
		if !found {
			return api.Result{}, errors.New("fixture missing ID")
		}
	}
	if r.owner.Active {
		r.owner.HistoryActions++
	}
	if r.failOperation == envelope.OperationID {
		return api.Result{}, errors.New("remote secret diagnostic must not escape")
	}
	return result, nil
}
func (r *memoryRuntime) Take(ctx context.Context, _ time.Time) (Owner, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() != nil {
		return Owner{}, ctx.Err()
	}
	r.takeCount++
	r.undo = copyInventory(r.inv)
	r.owner = Owner{Known: true, Active: true, ThisSession: true, Alive: true, SessionID: fmt.Sprintf("session-%032d", r.takeCount), HistoryKnown: !r.unknownHistory}
	if r.exhaustHistory {
		r.owner.HistoryActions = 80
	}
	return r.owner, nil
}
func (r *memoryRuntime) Owner(ctx context.Context) (Owner, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() != nil {
		return Owner{}, ctx.Err()
	}
	return r.owner, nil
}
func (r *memoryRuntime) Confirm(ctx context.Context) (Owner, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() != nil {
		return Owner{}, ctx.Err()
	}
	r.confirmCount++
	if !r.owner.Active || !r.owner.ThisSession || !r.owner.Alive {
		return Owner{}, errors.New("lost")
	}
	r.owner.Active = false
	r.owner.ThisSession = false
	if r.confirmLost {
		return Owner{}, errors.New("lost reply")
	}
	return r.owner, nil
}
func (r *memoryRuntime) Cancel(ctx context.Context) (Owner, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ctx.Err() != nil {
		return Owner{}, ctx.Err()
	}
	r.cancelCount++
	if !r.owner.Active || !r.owner.ThisSession {
		return Owner{}, errors.New("not ours")
	}
	if !r.noUndo {
		r.inv = copyInventory(r.undo)
	}
	r.owner.Active = false
	r.owner.ThisSession = false
	return r.owner, nil
}
func (r *memoryRuntime) CloseOwner() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.owner.Active && r.owner.ThisSession && !r.noUndo {
		r.inv = copyInventory(r.undo)
	}
	r.owner.Active = false
	r.owner.ThisSession = false
	r.owner.Alive = false
	return nil
}
func testConfig(t *testing.T, r *memoryRuntime) Config {
	t.Helper()
	return Config{Dir: filepath.Join(t.TempDir(), "control"), Target: r.target, OperationTimeout: time.Second, PrepareTimeout: 5 * time.Second, MinConfirmationTimeout: 100 * time.Millisecond, MaxConfirmationTimeout: 3 * time.Second, PollInterval: 10 * time.Millisecond}
}
func openTest(t *testing.T, c Config, r *memoryRuntime) *Supervisor {
	t.Helper()
	s, err := Open(c, r)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
func awaitPhase(t *testing.T, s *Supervisor, phase Phase) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.Status().Phase == phase {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("phase %s, wanted %s", s.Status().Phase, phase)
}

func TestStageConfirmAndDurablePrivateJournal(t *testing.T) {
	r, request := fixtureRuntime(t)
	config := testConfig(t, r)
	s := openTest(t, config, r)
	status, err := s.Stage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != AwaitingConfirmation {
		t.Fatal(status)
	}
	if _, err = s.Stage(context.Background(), request); err == nil {
		t.Fatal("pending transaction overwritten")
	}
	if _, err = s.Confirm(context.Background(), status.TransactionID, strings.Repeat("b", 64)); err == nil {
		t.Fatal("wrong checksum confirmed")
	}
	status, err = s.Confirm(context.Background(), status.TransactionID, status.PlanChecksum)
	if err != nil || status.Phase != Confirmed || !status.ConfirmationVerified {
		t.Fatalf("%+v %v", status, err)
	}
	r.mu.Lock()
	if r.confirmCount != 1 || r.cancelCount != 0 {
		t.Fatal("wrong terminal controls")
	}
	r.mu.Unlock()
	for _, name := range []string{"control.lock", "target.json", "journal.key", "journal.wal"} {
		info, err := os.Stat(filepath.Join(config.Dir, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("private journal mode %s", name)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTest(t, config, r)
	if reopened.Status().Phase != Confirmed {
		t.Fatalf("durable phase: %+v", reopened.Status())
	}
}

func TestDeadlineUsesCancelAndPreparedBaseline(t *testing.T) {
	r, request := fixtureRuntime(t)
	request.Timeout = 400 * time.Millisecond
	s := openTest(t, testConfig(t, r), r)
	if _, err := s.Stage(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	awaitPhase(t, s, RolledBack)
	status := s.Status()
	if !status.RollbackVerified || !status.PreparedArtifactsRetained {
		t.Fatal("rollback proof or artifact retention missing")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancelCount != 1 {
		t.Fatalf("deadline should send exactly one Ctrl-D cancellation: %d", r.cancelCount)
	}
	if len(r.inv.Tables) != 1 || len(r.inv.FilterRules) == 0 {
		t.Fatal("prepared objects incorrectly treated as Safe Mode rollback")
	}
}

func TestExpiredHealthyChannelCancellationHasFreshContext(t *testing.T) {
	r, request := fixtureRuntime(t)
	config := testConfig(t, r)
	config.PollInterval = 10 * time.Second
	s := openTest(t, config, r)
	st, err := s.Stage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.timer.Stop()
	s.status.Deadline = time.Now().Add(-time.Second)
	s.mu.Unlock()
	st, err = s.Cancel(context.Background(), st.TransactionID)
	if err != nil || st.Phase != RolledBack {
		t.Fatalf("%+v %v", st, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancelCount != 1 {
		t.Fatal("expired confirmation context suppressed explicit cancellation")
	}
}

func TestLostACKNeverRetriesAndRollsBackByReadback(t *testing.T) {
	r, request := fixtureRuntime(t)
	for _, op := range request.Plan.Operations {
		if op.Phase == routeros.Activate {
			r.failOperation = op.ID
			break
		}
	}
	s := openTest(t, testConfig(t, r), r)
	status, err := s.Stage(context.Background(), request)
	if err == nil || strings.Contains(err.Error(), "remote secret") {
		t.Fatal("unknown write mishandled or raw diagnostic leaked")
	}
	if status.Phase != RolledBack || !status.RollbackVerified {
		t.Fatalf("readback not verified: %+v", status)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byOperation[r.failOperation] != 1 {
		t.Fatal("uncertain write replayed")
	}
}

func TestRestartIsReadOnlyAndNeverAdoptsNewSessionForConfirm(t *testing.T) {
	r, request := fixtureRuntime(t)
	config := testConfig(t, r)
	s := openTest(t, config, r)
	status, err := s.Stage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	r.noUndo = true
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	r.mu.Lock()
	writes, takes, confirms := r.applyCount, r.takeCount, r.confirmCount
	r.mu.Unlock()
	reopened := openTest(t, config, r)
	if _, err = reopened.Confirm(context.Background(), status.TransactionID, status.PlanChecksum); err == nil {
		t.Fatal("reopened supervisor confirmed without its old session")
	}
	retained, err := reopened.Recover(context.Background())
	var retainedError *Failure
	if !errors.As(err, &retainedError) || retainedError.Code != UnconfirmedCandidateRetainedCode || retained.LastCode != UnconfirmedCandidateRetainedCode || retained.Phase != RollbackPending || retained.RollbackVerified || retained.ConfirmationVerified || !retained.RecoveryOnly {
		t.Fatal("retained candidate was not reported as unconfirmed and incompatible with rollback")
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
	reopened = openTest(t, config, r)
	if replayed := reopened.Status(); replayed.LastCode != UnconfirmedCandidateRetainedCode || replayed.Phase != RollbackPending || replayed.ConfirmationVerified || replayed.RollbackVerified {
		t.Fatal("retained candidate classification did not survive journal reopening")
	}
	r.mu.Lock()
	if r.applyCount != writes || r.takeCount != takes || r.confirmCount != confirms {
		t.Fatal("recovery performed a device write/take/confirm")
	}
	r.inv = copyInventory(r.undo)
	r.mu.Unlock()
	status, err = reopened.Recover(context.Background())
	if err != nil || status.Phase != RolledBack {
		t.Fatalf("%+v %v", status, err)
	}
}

func TestConfirmLostReplyRequiresDesiredAndReleasedOwner(t *testing.T) {
	r, request := fixtureRuntime(t)
	r.confirmLost = true
	config := testConfig(t, r)
	s := openTest(t, config, r)
	status, err := s.Stage(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Confirm(context.Background(), status.TransactionID, status.PlanChecksum); err == nil {
		t.Fatal("lost confirm reply hidden")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTest(t, config, r)
	status, err = reopened.Recover(context.Background())
	if err != nil || status.Phase != Confirmed || !status.ConfirmationVerified {
		t.Fatalf("durable confirm intent + desired + released owner: %+v %v", status, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.confirmCount != 1 {
		t.Fatal("confirm replayed after restart")
	}
}

func TestWriteAheadFailureAndUnknownHistoryBlockWrites(t *testing.T) {
	t.Run("intent-fsync", func(t *testing.T) {
		r, request := fixtureRuntime(t)
		s := openTest(t, testConfig(t, r), r)
		s.log.fail = func(kind string) error {
			if kind == "intent" {
				return errors.New("injected")
			}
			return nil
		}
		if _, err := s.Stage(context.Background(), request); err == nil {
			t.Fatal("journal failure ignored")
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.applyCount != 0 {
			t.Fatal("native write before durable intent")
		}
	})
	for _, exhausted := range []bool{false, true} {
		t.Run(fmt.Sprint(exhausted), func(t *testing.T) {
			r, request := fixtureRuntime(t)
			r.unknownHistory = !exhausted
			r.exhaustHistory = exhausted
			s := openTest(t, testConfig(t, r), r)
			if _, err := s.Stage(context.Background(), request); err == nil {
				t.Fatal("unknown/exhausted history accepted")
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			for _, op := range request.Plan.Operations {
				if op.Phase == routeros.Activate && r.byOperation[op.ID] != 0 {
					t.Fatal("activation wrote without actual history headroom")
				}
			}
		})
	}
}

func TestExclusiveLockAndNativeGates(t *testing.T) {
	r, request := fixtureRuntime(t)
	config := testConfig(t, r)
	s := openTest(t, config, r)
	if other, err := Open(config, r); err == nil {
		other.Close()
		t.Fatal("second supervisor acquired same private journal")
	}
	request.ExclusiveAccessAcknowledged = false
	if _, err := s.Stage(context.Background(), request); err == nil {
		t.Fatal("whole-device exclusivity precondition omitted")
	}
	request.ExclusiveAccessAcknowledged = true
	r.gates = false
	if _, err := s.Stage(context.Background(), request); err == nil {
		t.Fatal("unverified native adapter activated")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.applyCount != 0 || r.takeCount != 0 {
		t.Fatal("gates rejected after side effects")
	}
}

func TestDesiredSnapshotCannotBeOmitted(t *testing.T) {
	r, request := fixtureRuntime(t)
	r.missingDesiredSnapshot = true
	s := openTest(t, testConfig(t, r), r)
	status, err := s.Stage(context.Background(), request)
	if err == nil || status.Phase != RolledBack || status.ConfirmationVerified {
		t.Fatalf("nil strict desired snapshot accepted: %+v %v", status, err)
	}
}

func TestUnexpectedReadbackCancelsInsteadOfTrustingACK(t *testing.T) {
	r, request := fixtureRuntime(t)
	injected := false
	r.beforeApply = func(m routeros.APIMutation) {
		if m.Phase != routeros.Activate || injected {
			return
		}
		injected = true
		r.mu.Lock()
		r.inv.Tables = append(r.inv.Tables, api.Record{".id": "*F", "name": "foreign-change", "fib": "false", "disabled": "false", "dynamic": "false", "invalid": "false", "comment": ""})
		r.mu.Unlock()
	}
	s := openTest(t, testConfig(t, r), r)
	status, err := s.Stage(context.Background(), request)
	if err == nil || status.Phase != RolledBack {
		t.Fatalf("ACK overrode changed readback: %+v %v", status, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.confirmCount != 0 || r.cancelCount != 1 {
		t.Fatal("wrong response to concurrent external change")
	}
}

func TestConcurrentStageCannotReplacePendingTransaction(t *testing.T) {
	r, request := fixtureRuntime(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	once := sync.Once{}
	r.beforeApply = func(_ routeros.APIMutation) { once.Do(func() { close(entered); <-release }) }
	s := openTest(t, testConfig(t, r), r)
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { _, err := s.Stage(context.Background(), request); first <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("fixture did not enter write")
	}
	go func() { _, err := s.Stage(context.Background(), request); second <- err }()
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err == nil {
		t.Fatal("concurrent stage replaced active journal")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.takeCount != 1 {
		t.Fatal("second transaction took another owner")
	}
}
