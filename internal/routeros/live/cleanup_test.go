package live

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routeros/terminal"
	"github.com/moedove/ixp-manager/internal/routing"
)

// This is an in-memory non-secret API read model. Authentication truth is
// simulated only while constructing the prior test baseline, never in runtime.
type cleanupAPIModel struct {
	*fakeAPI
	mutations []api.Mutation
	unknown   bool
}

func (a *cleanupAPIModel) ReadSafeMode(context.Context) (api.SafeModeState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.inv.SafeMode == nil {
		return api.SafeModeState{}, errors.New("unknown")
	}
	return *a.inv.SafeMode, nil
}
func (a *cleanupAPIModel) Apply(_ context.Context, m api.Mutation) (api.Result, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.writes++
	a.mutations = append(a.mutations, m)
	if a.unknown {
		return api.Result{}, &api.Error{Code: api.CodeTransport, Uncertain: true}
	}
	if m.Action != api.Remove || m.Resource != api.FilterRule && m.Resource != api.BGPConnection {
		return api.Result{}, errors.New("fixture only permits cleanup remove")
	}
	rows := cleanupModelRows(&a.inv, m.Resource)
	for i, row := range *rows {
		if strings.EqualFold(row[".id"], m.ID) {
			*rows = append((*rows)[:i], (*rows)[i+1:]...)
			return api.Result{Acknowledged: true}, nil
		}
	}
	return api.Result{}, &api.Error{Code: api.CodeTrap, Uncertain: true}
}

type cleanupTerminalModel struct {
	*fakeTerminal
	controls int
}

func (t *cleanupTerminalModel) EnterSafeMode(context.Context) (terminal.Evidence, error) {
	t.controls++
	return terminal.Evidence{}, errors.New("cleanup must not take")
}
func (t *cleanupTerminalModel) ConfirmSafeMode(context.Context) (terminal.Confirmation, error) {
	t.controls++
	return terminal.Confirmation{}, errors.New("cleanup must not confirm")
}
func (t *cleanupTerminalModel) CancelSafeMode(context.Context) (terminal.Cancellation, error) {
	t.controls++
	return terminal.Cancellation{}, errors.New("cleanup must not cancel")
}

func cleanupModelRows(inv *api.Inventory, resource api.Resource) *[]api.Record {
	switch resource {
	case api.RoutingTable:
		return &inv.Tables
	case api.BGPInstance:
		return &inv.Instances
	case api.BGPConnection:
		return &inv.Connections
	case api.FilterRule:
		return &inv.FilterRules
	}
	panic("fixture unsupported menu")
}
func cleanupModelDefaults(resource api.Resource) api.Record {
	row := api.Record{}
	switch resource {
	case api.RoutingTable:
		row = api.Record{"fib": "false", "disabled": "false", "dynamic": "false", "invalid": "false", "comment": ""}
	case api.BGPInstance:
		row = api.Record{"disabled": "false", "vrf": "main", "comment": ""}
	case api.FilterRule:
		row = api.Record{"disabled": "false", "inactive": "false"}
	case api.BGPConnection:
		for _, field := range observedFields[resource] {
			row[field] = ""
		}
		for _, field := range []string{"disabled", "dynamic", "multihop", "output.remove-private-as", "output.as-override"} {
			row[field] = "false"
		}
		row["connect"] = "true"
		row["listen"] = "true"
		row["vrf"] = "main"
		row["output.default-prepend"] = "0"
	}
	return row
}
func cleanupModelApplyPlan(t *testing.T, p routeros.Plan, credentials routeros.Credentials, inv *api.Inventory) {
	t.Helper()
	compiled, err := routeros.Compile(p, credentials)
	if err != nil {
		t.Fatal(err)
	}
	for _, cmd := range compiled.Commands {
		if cmd.Phase == routeros.Cleanup {
			continue
		}
		resource := api.Resource(cmd.Resource)
		rows := cleanupModelRows(inv, resource)
		var target api.Record
		if cmd.Action == routeros.Add {
			used := map[string]bool{}
			for _, row := range *rows {
				used[strings.ToUpper(row[".id"])] = true
			}
			id := ""
			for i := 1; id == ""; i++ {
				candidate := fmt.Sprintf("*%X", i)
				if !used[candidate] {
					id = candidate
				}
			}
			target = cleanupModelDefaults(resource)
			target[".id"] = id
			*rows = append(*rows, target)
		} else {
			for _, row := range *rows {
				match := cmd.Object.ID != "" && strings.EqualFold(row[".id"], cmd.Object.ID)
				if cmd.Object.ID == "" {
					match = cmd.Object.Name != "" && row["name"] == cmd.Object.Name || cmd.Object.Chain != "" && row["chain"] == cmd.Object.Chain && row["comment"] == cmd.Object.Comment
				}
				if match {
					if target != nil {
						t.Fatal("ambiguous model reference")
					}
					target = row
				}
			}
			if target == nil {
				t.Fatal("missing model reference")
			}
		}
		switch cmd.Action {
		case routeros.Add, routeros.Set:
			for _, attribute := range cmd.Attributes {
				if attribute.Name == "tcp-md5-key" {
					continue
				}
				value := attribute.Value
				if value == "yes" {
					value = "true"
				}
				if value == "no" {
					value = "false"
				}
				target[attribute.Name] = value
			}
		case routeros.Enable:
			target["disabled"] = "false"
		case routeros.Disable:
			target["disabled"] = "true"
		case routeros.Unset:
			target[cmd.Property] = ""
		default:
			t.Fatal("unexpected non-cleanup action")
		}
	}
}
func cleanupAdapterFixture(t *testing.T) (*Runtime, *cleanupAPIModel, *cleanupTerminalModel, routeros.Plan, []string) {
	t.Helper()
	r, baseAPI, baseTerminal, request := observerFixture()
	observed, err := r.Observe(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	router := routing.Router{ID: r.target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1"}
	peers := []routing.Peer{
		{ID: "keep", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"203.0.113.0/24"}, Password: "old-keep-secret", AuthenticationRevision: strings.Repeat("A", 22)},
		{ID: "remove", ASN: 64502, Address: "198.51.100.3", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"203.0.114.0/24"}, Password: "old-remove-secret", AuthenticationRevision: strings.Repeat("B", 22)},
	}
	initial, err := routeros.BuildPlan(r.target, router, peers, *observed.Snapshot, routeros.PlanOptions{Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	oldCredentials := routeros.Credentials{"keep": {Revision: peers[0].AuthenticationRevision, Password: peers[0].Password}, "remove": {Revision: peers[1].AuthenticationRevision, Password: peers[1].Password}}
	cleanupModelApplyPlan(t, initial, oldCredentials, &baseAPI.inv)
	known := routeros.KnownState{Target: r.target, ActiveGeneration: initial.Generation, Generations: []routeros.KnownGeneration{{Target: r.target, Desired: initial.Desired, PolicyChecksum: initial.PolicyChecksum}}}
	for _, peer := range initial.Desired.Peers {
		known.Peers = append(known.Peers, routeros.KnownPeer{ID: peer.ID, Authentication: peer.Authentication})
	}
	evidence, err := r.identity(context.Background(), baseAPI.inv)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := routeros.BuildSnapshot(baseAPI.inv, known, routeros.CredentialEvidence{}, evidence)
	var inspection *routeros.InspectError
	if !errors.As(err, &inspection) || inspection.Code != "unverified-credential" {
		t.Fatalf("baseline structural fixture invalid: %v", err)
	}
	// Explicit test-only baseline credential assumption. Production Runtime
	// cannot receive this bool and cleanup itself never reads these passwords.
	for i := range baseline.Connections {
		baseline.Connections[i].AuthenticationVerified = true
	}
	peers = peers[:1]
	peers[0].AllowedPrefixes = []string{"203.0.115.0/24"}
	peers[0].Password = "new-keep-secret"
	peers[0].AuthenticationRevision = strings.Repeat("C", 22)
	next, err := routeros.BuildPlan(r.target, router, peers, baseline, routeros.PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cleanupModelApplyPlan(t, next, routeros.Credentials{"keep": {Revision: peers[0].AuthenticationRevision, Password: peers[0].Password}}, &baseAPI.inv)
	a := &cleanupAPIModel{fakeAPI: baseAPI}
	term := &cleanupTerminalModel{fakeTerminal: baseTerminal}
	r.api = a
	r.terminal = term
	r.profile.CleanupVerified = true
	bridge, err := routeros.NewCleanupBridge(next)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, op := range bridge.Operations() {
		ids = append(ids, op.ID)
	}
	if len(ids) < 2 || r.bridge != nil || r.boundPlan != nil {
		t.Fatal("fixture must be a fresh runtime and nonempty old scope")
	}
	return r, a, term, next, ids
}
func cleanupAdapterMutation(t *testing.T, r *Runtime, p routeros.Plan, id string) routeros.APIMutation {
	t.Helper()
	raw, evidence, _, _, err := r.readState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := routeros.NewAPIResolver(raw, r.target, evidence)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := routeros.NewCleanupBridge(p)
	if err != nil {
		t.Fatal(err)
	}
	m, err := bridge.Mutation(id, resolver)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestCleanupAdapterFreshBindingDeletesOnlyOriginalSuffixWithoutPasswords(t *testing.T) {
	r, a, term, p, ids := cleanupAdapterFixture(t)
	defer r.Close()
	if err := r.BindCleanup(context.Background(), control.CleanupBinding{Plan: p, RemainingOperationIDs: ids}); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		m := cleanupAdapterMutation(t, r, p, id)
		result, err := r.ApplyCleanup(context.Background(), m)
		if err != nil || !result.Acknowledged || result.ID != "" {
			t.Fatal("exact cleanup failed", err)
		}
	}
	if !r.cleanupFinished() || a.writes != len(ids) || term.controls != 0 || a.credentialCalls != 0 {
		t.Fatal("cleanup required credentials, changed owner, or skipped deletions")
	}
	for _, m := range a.mutations {
		if m.Action != api.Remove || m.Resource != api.FilterRule && m.Resource != api.BGPConnection || len(m.Attributes) != 0 {
			t.Fatal("cleanup escaped Remove-only scope")
		}
	}
}
func TestCleanupAdapterRejectsWrongScopeDriftAndOwnersBeforeWrite(t *testing.T) {
	cases := map[string]func(*cleanupAPIModel, *routeros.Plan, *[]string){
		"wrong suffix":                 func(a *cleanupAPIModel, p *routeros.Plan, ids *[]string) { (*ids)[0] = "op-invalid" },
		"reordered suffix":             func(a *cleanupAPIModel, p *routeros.Plan, ids *[]string) { (*ids)[0], (*ids)[1] = (*ids)[1], (*ids)[0] },
		"skipped still-present target": func(a *cleanupAPIModel, p *routeros.Plan, ids *[]string) { *ids = (*ids)[1:] },
		"native ID changed":            func(a *cleanupAPIModel, p *routeros.Plan, ids *[]string) { a.inv.FilterRules[2][".id"] = "*FFF" },
		"retired body changed":         func(a *cleanupAPIModel, p *routeros.Plan, ids *[]string) { a.inv.FilterRules[2]["rule"] = "accept;" },
		"retired connection enabled": func(a *cleanupAPIModel, p *routeros.Plan, ids *[]string) {
			for _, row := range a.inv.Connections {
				if row["disabled"] == "true" {
					row["disabled"] = "false"
				}
			}
		},
		"protected root changed": func(a *cleanupAPIModel, p *routeros.Plan, ids *[]string) { a.inv.FilterRules[0]["rule"] = "accept;" },
		"protected local ASN changed": func(a *cleanupAPIModel, p *routeros.Plan, ids *[]string) {
			for _, row := range a.inv.Connections {
				if row["disabled"] == "false" {
					row["as"] = "64590"
				}
			}
		},
		"safe mode active": func(a *cleanupAPIModel, p *routeros.Plan, ids *[]string) {
			a.inv.SafeMode = &api.SafeModeState{Enabled: true}
		},
		"outside reference": func(a *cleanupAPIModel, p *routeros.Plan, ids *[]string) {
			a.inv.FilterRules = append(a.inv.FilterRules, api.Record{".id": "*FFF", "chain": "outside", "comment": "", "rule": "jump " + a.inv.FilterRules[2]["chain"] + ";", "disabled": "false", "inactive": "false"})
		},
		"wrong target": func(a *cleanupAPIModel, p *routeros.Plan, ids *[]string) { p.Target.DeviceID = "different" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r, a, term, p, ids := cleanupAdapterFixture(t)
			defer r.Close()
			mutate(a, &p, &ids)
			if err := r.BindCleanup(context.Background(), control.CleanupBinding{Plan: p, RemainingOperationIDs: ids}); err == nil {
				t.Fatal("unsafe cleanup bound")
			}
			if a.writes != 0 || a.credentialCalls != 0 || term.controls != 0 {
				t.Fatal("rejected bind caused device/sensitive action")
			}
		})
	}
}

func TestCleanupAdapterFreshRuntimeResumesOnlyRemainingSuffix(t *testing.T) {
	r, a, _, p, ids := cleanupAdapterFixture(t)
	if err := r.BindCleanup(context.Background(), control.CleanupBinding{Plan: p, RemainingOperationIDs: ids}); err != nil {
		t.Fatal(err)
	}
	first := cleanupAdapterMutation(t, r, p, ids[0])
	if _, err := r.ApplyCleanup(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	remainingInventory := cloneInventory(a.inv)
	r.Close()
	fresh, base, baseTerminal, _ := observerFixture()
	defer fresh.Close()
	base.inv = remainingInventory
	model := &cleanupAPIModel{fakeAPI: base}
	terminalModel := &cleanupTerminalModel{fakeTerminal: baseTerminal}
	fresh.api = model
	fresh.terminal = terminalModel
	fresh.profile.CleanupVerified = true
	if err := fresh.BindCleanup(context.Background(), control.CleanupBinding{Plan: p, RemainingOperationIDs: ids[1:]}); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.ApplyCleanup(context.Background(), first); err == nil || model.writes != 0 {
		t.Fatal("already completed deletion replayed")
	}
	for _, id := range ids[1:] {
		m := cleanupAdapterMutation(t, fresh, p, id)
		if _, err := fresh.ApplyCleanup(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	if model.writes != len(ids)-1 || model.credentialCalls != 0 || terminalModel.controls != 0 || !fresh.cleanupFinished() {
		t.Fatal("fresh suffix execution used old credentials/owner or wrong scope")
	}
}
func TestCleanupAdapterRechecksMutationAndUnknownACKIsNeverRetried(t *testing.T) {
	for _, mode := range []string{"wrong ID", "changed attrs", "wrong order", "late protected drift", "late owner", "unknown ACK"} {
		t.Run(mode, func(t *testing.T) {
			r, a, term, p, ids := cleanupAdapterFixture(t)
			defer r.Close()
			if err := r.BindCleanup(context.Background(), control.CleanupBinding{Plan: p, RemainingOperationIDs: ids}); err != nil {
				t.Fatal(err)
			}
			m := cleanupAdapterMutation(t, r, p, ids[0])
			switch mode {
			case "wrong ID":
				m.Request.ID = "*FFF"
			case "changed attrs":
				m.Request.Attributes = []api.Attribute{{Name: "rule", Value: "reject;"}}
			case "wrong order":
				m.OperationID = ids[1]
			case "late protected drift":
				a.inv.FilterRules[0]["rule"] = "accept;"
			case "late owner":
				a.inv.SafeMode = &api.SafeModeState{Enabled: true}
			case "unknown ACK":
				a.unknown = true
			}
			_, err := r.ApplyCleanup(context.Background(), m)
			if err == nil {
				t.Fatal("bad/unknown cleanup accepted")
			}
			if mode == "unknown ACK" {
				var failure *Failure
				if !errors.As(err, &failure) || !failure.Uncertain || a.writes != 1 {
					t.Fatal("uncertain receipt lost")
				}
				a.unknown = false
				if _, err = r.ApplyCleanup(context.Background(), m); err == nil || a.writes != 1 {
					t.Fatal("unknown Remove repeated")
				}
				if err = r.BindCleanup(context.Background(), control.CleanupBinding{Plan: p, RemainingOperationIDs: ids}); err == nil {
					t.Fatal("poisoned runtime rebound")
				}
			} else if a.writes != 0 {
				t.Fatal("unapproved mutation sent")
			}
			if term.controls != 0 || a.credentialCalls != 0 {
				t.Fatal("cleanup used owner/secret authority")
			}
		})
	}
}
