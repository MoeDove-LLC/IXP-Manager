package live

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routing"
)

type discardAPIModel struct {
	*fakeAPI
	unknown bool
	removes []api.Mutation
}

func (a *discardAPIModel) ReadSafeMode(context.Context) (api.SafeModeState, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.inv.SafeMode == nil {
		return api.SafeModeState{}, errors.New("unknown")
	}
	return *a.inv.SafeMode, nil
}
func (a *discardAPIModel) Apply(_ context.Context, m api.Mutation) (api.Result, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.writes++
	a.removes = append(a.removes, m)
	if a.unknown {
		return api.Result{}, &api.Error{Code: api.CodeTransport, Uncertain: true}
	}
	if m.Action != api.Remove || len(m.Attributes) != 0 {
		return api.Result{}, errors.New("only exact Remove is allowed")
	}
	rows := cleanupModelRows(&a.inv, m.Resource)
	for i, row := range *rows {
		if strings.EqualFold(row[".id"], m.ID) {
			*rows = append((*rows)[:i], (*rows)[i+1:]...)
			return api.Result{Acknowledged: true}, nil
		}
	}
	return api.Result{}, errors.New("missing object")
}

func discardAdapterFixture(t *testing.T) (*Runtime, *discardAPIModel, *cleanupTerminalModel, control.PreparedDiscardBinding) {
	t.Helper()
	r, a, terminal, request := observerFixture()
	observed, err := r.Observe(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := routeros.BuildPlan(r.target, routing.Router{ID: r.target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1"}, []routing.Peer{{ID: "peer", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"203.0.113.0/24"}}}, *observed.Snapshot, routeros.PlanOptions{Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	commands, err := routeros.Compile(plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	added := map[string]struct {
		resource api.Resource
		row      api.Record
	}{}
	for _, cmd := range commands.Commands {
		if cmd.Phase != routeros.Prepare {
			continue
		}
		resource := api.Resource(cmd.Resource)
		rows := cleanupModelRows(&a.inv, resource)
		var row api.Record
		if cmd.Action == routeros.Add {
			row = cleanupModelDefaults(resource)
			row[".id"] = fmt.Sprintf("*%X", len(*rows)+1)
			if resource == api.RoutingTable {
				row["used"] = "true"
			}
			if resource == api.BGPInstance {
				row["inactive"] = "false"
			}
			*rows = append(*rows, row)
			added[cmd.OperationID] = struct {
				resource api.Resource
				row      api.Record
			}{resource, row}
		} else {
			for _, candidate := range *rows {
				if candidate["name"] == cmd.Object.Name {
					row = candidate
					break
				}
			}
		}
		if row == nil {
			t.Fatal("Prepare model reference missing")
		}
		if cmd.Action == routeros.Unset {
			row[cmd.Property] = ""
		} else {
			for _, attr := range cmd.Attributes {
				if attr.Name == "tcp-md5-key" {
					continue
				}
				value := attr.Value
				if value == "yes" {
					value = "true"
				}
				if value == "no" {
					value = "false"
				}
				row[attr.Name] = value
			}
		}
	}
	binding := control.PreparedDiscardBinding{Plan: plan, BaselineKnown: routeros.KnownState{Target: r.target}}
	for op, created := range added {
		attributes := api.Record{}
		for key, value := range created.row {
			if key != ".id" && key != "used" && (key != "inactive" || created.resource == api.FilterRule) {
				attributes[key] = value
			}
		}
		binding.Objects = append(binding.Objects, routeros.PreparedDiscardObject{PrepareOperationID: op, Resource: created.resource, ID: created.row[".id"], Attributes: attributes})
	}
	bridge, err := routeros.NewPreparedDiscardBridge(plan, binding.Objects)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range bridge.Operations() {
		binding.RemainingOperationIDs = append(binding.RemainingOperationIDs, op.ID)
	}
	wrapped := &discardAPIModel{fakeAPI: a}
	term := &cleanupTerminalModel{fakeTerminal: terminal}
	r.api, r.terminal = wrapped, term
	r.profile.PreparedDiscardVerified = true
	return r, wrapped, term, binding
}

func discardNext(t *testing.T, r *Runtime, b control.PreparedDiscardBinding, index int) routeros.APIMutation {
	t.Helper()
	raw, evidence, _, _, err := r.readState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	resolver, err := routeros.NewAPIResolver(raw, r.target, evidence)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := routeros.NewPreparedDiscardBridge(b.Plan, b.Objects)
	if err != nil {
		t.Fatal(err)
	}
	m, err := bridge.Mutation(b.RemainingOperationIDs[index], resolver)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPreparedDiscardAdapterRestoresActualEmptyBaselineWithoutOwnerActions(t *testing.T) {
	r, a, terminal, b := discardAdapterFixture(t)
	defer r.Close()
	if err := r.BindPreparedDiscard(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	for i := range b.RemainingOperationIDs {
		m := discardNext(t, r, b, i)
		if result, err := r.ApplyPreparedDiscard(context.Background(), m); err != nil || !result.Acknowledged {
			t.Fatal(err)
		}
	}
	if a.writes != len(b.Objects) || terminal.controls != 0 || a.credentialCalls != 0 || !r.preparedDiscardFinished() {
		t.Fatal("discard crossed write/owner/credential boundary")
	}
	o, err := r.Observe(context.Background(), control.ReadRequest{KnownState: b.BaselineKnown, Strict: true})
	if err != nil || o.Snapshot == nil || !o.Snapshot.NamespaceEmpty {
		t.Fatal("actual baseline was not restored", err)
	}
	if structuralSnapshotDigest(*o.Snapshot) != structuralSnapshotDigest(b.Plan.Base) {
		t.Fatal("baseline changed")
	}
	if err = r.BindPreparedDiscard(context.Background(), b); err == nil {
		t.Fatal("completed removals could be rebound from beginning")
	}
}

func TestPreparedDiscardAdapterRejectsStaleForeignAndNonFreshBindings(t *testing.T) {
	for _, mode := range []string{"gate", "poisoned", "bound-plan", "taken", "owner", "history", "changed-row", "foreign-table-id", "foreign-instance-id", "foreign-chain", "missing", "reappeared"} {
		t.Run(mode, func(t *testing.T) {
			r, a, term, b := discardAdapterFixture(t)
			defer r.Close()
			switch mode {
			case "gate":
				r.profile.PreparedDiscardVerified = false
			case "poisoned":
				r.poisoned = true
			case "bound-plan":
				if err := r.BindPlan(b.Plan, nil); err != nil {
					t.Fatal(err)
				}
			case "taken":
				r.taken = true
			case "owner":
				a.inv.SafeMode = &api.SafeModeState{Enabled: true}
			case "history":
				term.history = 1
			case "changed-row":
				a.inv.FilterRules[0]["rule"] = "accept;"
			case "foreign-table-id":
				a.inv.Instances = append(a.inv.Instances, api.Record{".id": "*FF", "name": "foreign", "as": "64599", "router-id": "192.0.2.9", "routing-table": a.inv.Tables[0][".id"], "vrf": "main", "disabled": "false", "inactive": "false", "comment": ""})
			case "foreign-instance-id":
				row := api.Record{}
				for k, v := range a.inv.Connections[0] {
					row[k] = v
				}
				row[".id"], row["name"], row["instance"] = "*FF", "foreign", a.inv.Instances[0][".id"]
				a.inv.Connections = append(a.inv.Connections, row)
			case "foreign-chain":
				a.inv.FilterRules = append(a.inv.FilterRules, api.Record{".id": "*FF", "chain": "foreign", "comment": "", "rule": "jump " + a.inv.FilterRules[0]["chain"] + ";", "disabled": "false", "inactive": "false"})
			case "missing":
				a.inv.Connections = nil
			case "reappeared":
				b.RemainingOperationIDs = b.RemainingOperationIDs[1:]
			}
			if err := r.BindPreparedDiscard(context.Background(), b); err == nil || a.writes != 0 || term.controls != 0 {
				t.Fatal("unsafe discard binding accepted", err)
			}
		})
	}
}

func TestPreparedDiscardAdapterPinsOrderAndNeverRetriesUnknown(t *testing.T) {
	r, a, term, b := discardAdapterFixture(t)
	defer r.Close()
	if err := r.BindPreparedDiscard(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	m := discardNext(t, r, b, 0)
	wrong := m
	wrong.Request.Action = api.Enable
	if _, err := r.ApplyPreparedDiscard(context.Background(), wrong); err == nil || a.writes != 0 {
		t.Fatal("discard enabled a connection")
	}
	if err := r.BindPlan(b.Plan, nil); err == nil {
		t.Fatal("normal plan bypassed pending discard")
	}
	if _, err := r.Take(context.Background(), time.Now().Add(30*time.Second)); err == nil || term.controls != 0 {
		t.Fatal("discard acquired Safe Mode")
	}
	a.unknown = true
	if _, err := r.ApplyPreparedDiscard(context.Background(), m); err == nil || !r.poisoned || a.writes != 1 {
		t.Fatal("unknown removal was not quarantined")
	}
	if _, err := r.ApplyPreparedDiscard(context.Background(), m); err == nil || a.writes != 1 {
		t.Fatal("unknown removal was retried")
	}
	if err := r.BindPreparedDiscard(context.Background(), b); err == nil {
		t.Fatal("poisoned runtime was recycled")
	}
	if !reflect.DeepEqual(a.removes, []api.Mutation{m.Request}) {
		t.Fatal("unapproved native command attempted")
	}
}
