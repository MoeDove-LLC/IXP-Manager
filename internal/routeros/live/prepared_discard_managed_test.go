package live

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routing"
)

// The API dictionary is an actual in-memory read/write model. Only the prior
// plan's AuthenticationVerified flags are simulated, as in cleanup_test.go.
// The adapter must not fetch or invent credential evidence; the controller's
// independent complete HMAC/credential checks own that part of the contract.
func managedDiscardAdapterFixture(t *testing.T) (*Runtime, *discardAPIModel, *cleanupTerminalModel, control.PreparedDiscardBinding, api.Inventory) {
	t.Helper()
	r, source, terminal, request := observerFixture()
	initial, err := r.Observe(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	router := routing.Router{ID: r.target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1", LocalIPv6: "2001:db8::1"}
	peers := []routing.Peer{
		{ID: "baseline-v4", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"203.0.113.0/24"}, Password: "simulated-old-v4", AuthenticationRevision: strings.Repeat("A", 22)},
		{ID: "baseline-v6", ASN: 64502, Address: "2001:db8::2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"2001:db8:1::/48"}, Password: "simulated-old-v6", AuthenticationRevision: strings.Repeat("B", 22)},
	}
	oldPlan, err := routeros.BuildPlan(r.target, router, peers, *initial.Snapshot, routeros.PlanOptions{Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	credentials := routeros.Credentials{}
	for _, peer := range peers {
		credentials[peer.ID] = routeros.Credential{Revision: peer.AuthenticationRevision, Password: peer.Password}
	}
	cleanupModelApplyPlan(t, oldPlan, credentials, &source.inv)
	known := routeros.KnownState{Target: r.target, ActiveGeneration: oldPlan.Generation, Generations: []routeros.KnownGeneration{{Target: r.target, Desired: oldPlan.Desired, PolicyChecksum: oldPlan.PolicyChecksum}}}
	for _, peer := range oldPlan.Desired.Peers {
		known.Peers = append(known.Peers, routeros.KnownPeer{ID: peer.ID, Authentication: peer.Authentication})
	}
	evidence, err := r.identity(context.Background(), source.inv)
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := routeros.BuildSnapshot(source.inv, known, routeros.CredentialEvidence{}, evidence)
	var inspection *routeros.InspectError
	if !errors.As(err, &inspection) || inspection.Code != "unverified-credential" || !baseline.Managed || baseline.NamespaceEmpty || baseline.ActiveGeneration != oldPlan.Generation || len(baseline.Connections) != 2 {
		t.Fatal("existing managed structural baseline not proved", err)
	}
	for i := range baseline.Connections {
		if baseline.Connections[i].Disabled {
			t.Fatal("baseline peer was not active in the actual model")
		}
		baseline.Connections[i].AuthenticationVerified = true // Prior controller receipt assumption only.
	}
	before := cloneInventory(source.inv)
	// A pending identity change exercises the projection boundary: the old
	// instance and connections retain their ASN, while the new disabled peer
	// already has its explicitly configured future ASN.
	router.ASN, router.RouterID = 64510, "192.0.2.10"
	peers[0].AllowedPrefixes = []string{"203.0.115.0/24"}
	peers = append(peers, routing.Peer{ID: "new-prepared", ASN: 64503, Address: "198.51.100.3", Enabled: true, MaxPrefixes: 20, AllowedPrefixes: []string{"198.51.100.0/24"}, Password: "simulated-new-key", AuthenticationRevision: strings.Repeat("C", 22)})
	credentials["new-prepared"] = routeros.Credential{Revision: peers[2].AuthenticationRevision, Password: peers[2].Password}
	next, err := routeros.BuildPlan(r.target, router, peers, baseline, routeros.PlanOptions{AllowIdentityChange: true})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := routeros.Compile(next, credentials)
	if err != nil {
		t.Fatal(err)
	}
	type addition struct {
		resource api.Resource
		row      api.Record
	}
	added := map[string]addition{}
	for _, command := range compiled.Commands {
		if command.Phase != routeros.Prepare {
			continue
		}
		resource := api.Resource(command.Resource)
		rows := cleanupModelRows(&source.inv, resource)
		var row api.Record
		if command.Action == routeros.Add {
			used := map[string]bool{}
			for _, existing := range *rows {
				used[existing[".id"]] = true
			}
			id := ""
			for i := 1; id == ""; i++ {
				candidate := fmt.Sprintf("*%X", i)
				if !used[candidate] {
					id = candidate
				}
			}
			row = cleanupModelDefaults(resource)
			row[".id"] = id
			*rows = append(*rows, row)
			added[command.OperationID] = addition{resource, row}
		} else {
			for _, candidate := range *rows {
				if candidate["name"] == command.Object.Name {
					if row != nil {
						t.Fatal("ambiguous prepared model reference")
					}
					row = candidate
				}
			}
		}
		if row == nil {
			t.Fatal("missing prepared model reference")
		}
		if command.Action == routeros.Unset {
			row[command.Property] = ""
		} else {
			for _, attribute := range command.Attributes {
				if attribute.Name == "tcp-md5-key" {
					continue // No simulated secrets are exposed to this adapter.
				}
				value := attribute.Value
				if value == "yes" {
					value = "true"
				}
				if value == "no" {
					value = "false"
				}
				row[attribute.Name] = value
			}
		}
	}
	binding := control.PreparedDiscardBinding{Plan: next, BaselineKnown: known}
	for operation, created := range added {
		attributes := api.Record{}
		for key, value := range created.row {
			if key != ".id" && key != "used" && (key != "inactive" || created.resource == api.FilterRule) {
				attributes[key] = value
			}
		}
		binding.Objects = append(binding.Objects, routeros.PreparedDiscardObject{PrepareOperationID: operation, Resource: created.resource, ID: created.row[".id"], Attributes: attributes})
	}
	bridge, err := routeros.NewPreparedDiscardBridge(next, binding.Objects)
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range bridge.Operations() {
		binding.RemainingOperationIDs = append(binding.RemainingOperationIDs, operation.ID)
	}
	if len(binding.Objects) == 0 || len(source.inv.Connections) != 3 || source.inv.Instances[0]["as"] != "64500" {
		t.Fatal("fixture did not preserve old instance plus prepared peer")
	}
	wrapped := &discardAPIModel{fakeAPI: source}
	owner := &cleanupTerminalModel{fakeTerminal: terminal}
	r.api, r.terminal = wrapped, owner
	r.profile.PreparedDiscardVerified = true
	return r, wrapped, owner, binding, before
}

func managedDiscardRows(inv api.Inventory) map[string]api.Record {
	rows := map[string]api.Record{}
	for _, menu := range menus(inv) {
		for _, row := range menu.rows {
			rows[cleanupKey(menu.resource, row[".id"])] = objectRecordCopy(row)
		}
	}
	return rows
}

func TestPreparedDiscardManagedPreservesOldActiveConfigurationAndIDs(t *testing.T) {
	r, device, owner, binding, baseline := managedDiscardAdapterFixture(t)
	defer r.Close()
	want := managedDiscardRows(baseline)
	if err := r.BindPreparedDiscard(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	projected, err := preparedDiscardProjection(r.preparedDiscard, cloneInventory(device.inv))
	if err != nil || !reflect.DeepEqual(managedDiscardRows(projected), want) {
		t.Fatal("projection hid or changed existing objects", err)
	}
	for i := range binding.RemainingOperationIDs {
		mutation := discardNext(t, r, binding, i)
		if _, baselineObject := want[cleanupKey(mutation.Request.Resource, mutation.Request.ID)]; baselineObject {
			t.Fatal("discard selected an original baseline object")
		}
		result, err := r.ApplyPreparedDiscard(context.Background(), mutation)
		if err != nil || !result.Acknowledged {
			t.Fatal("discard did not remove exact prepared target", err)
		}
		actual := managedDiscardRows(device.inv)
		for id, row := range want {
			if !reflect.DeepEqual(actual[id], row) {
				t.Fatal("discard changed a protected old ID or attribute", id)
			}
		}
	}
	if !r.preparedDiscardFinished() || !reflect.DeepEqual(device.inv, baseline) || device.writes != len(binding.Objects) || owner.controls != 0 || device.credentialCalls != 0 {
		t.Fatal("managed discard crossed device/owner/credential boundary")
	}
	_, _, err = r.preparedDiscardState(context.Background(), r.preparedDiscard)
	if err != nil {
		t.Fatal("actual final baseline did not pass structural adapter proof", err)
	}
	if len(device.inv.Connections) != 2 || len(device.inv.Tables) != len(baseline.Tables) || device.inv.Instances[0]["as"] != "64500" || device.inv.Instances[0]["router-id"] != "192.0.2.1" {
		t.Fatal("original managed baseline was replaced by an empty claim")
	}
}

func TestPreparedDiscardManagedRejectsBaselineAndForeignNativeReferences(t *testing.T) {
	for _, mode := range []string{"instance-attribute", "root-attribute", "connection-attribute", "foreign-instance-id", "foreign-table-id", "foreign-new-chain"} {
		t.Run(mode, func(t *testing.T) {
			r, device, owner, binding, _ := managedDiscardAdapterFixture(t)
			defer r.Close()
			if err := r.BindPreparedDiscard(context.Background(), binding); err != nil {
				t.Fatal(err)
			}
			mutation := discardNext(t, r, binding, 0)
			switch mode {
			case "instance-attribute":
				device.inv.Instances[0]["router-id"] = "192.0.2.99"
			case "root-attribute":
				device.inv.FilterRules[0]["comment"] += "-foreign-change"
			case "connection-attribute":
				device.inv.Connections[0]["comment"] = "foreign-change"
			case "foreign-instance-id", "foreign-table-id":
				row := objectRecordCopy(device.inv.Connections[0])
				row[".id"], row["name"] = "*F00", "external-peer"
				row["instance"], row["routing-table"], row["vrf"] = "external-instance", "main", "main"
				for _, field := range []string{"input.filter", "input.filter-nlri", "output.filter-chain", "output.filter-select", "templates"} {
					row[field] = ""
				}
				device.inv.Instances = append(device.inv.Instances, api.Record{".id": "*F00", "name": "external-instance", "as": "64599", "router-id": "192.0.2.99", "routing-table": "main", "vrf": "main", "disabled": "false", "inactive": "false", "comment": ""})
				if mode == "foreign-instance-id" {
					row["instance"] = device.inv.Instances[0][".id"]
				} else {
					ownedTable := device.inv.Instances[0]["routing-table"]
					for _, table := range device.inv.Tables {
						if table["name"] == ownedTable {
							row["routing-table"] = table[".id"]
							break
						}
					}
				}
				device.inv.Connections = append(device.inv.Connections, row)
			case "foreign-new-chain":
				chain := ""
				for _, object := range binding.Objects {
					if object.Resource == api.FilterRule {
						chain = object.Attributes["chain"]
						break
					}
				}
				if chain == "" {
					t.Fatal("fixture new chain missing")
				}
				device.inv.FilterRules = append(device.inv.FilterRules, api.Record{".id": "*F00", "chain": "external-chain", "rule": "jump " + chain + ";", "comment": "", "disabled": "false", "inactive": "false"})
			}
			if _, err := r.ApplyPreparedDiscard(context.Background(), mutation); err == nil || device.writes != 0 || owner.controls != 0 || device.credentialCalls != 0 {
				t.Fatal("late protected/reference drift permitted a removal", mode, err)
			}
		})
	}
}

func TestPreparedDiscardManagedRejectsCompletedNativeIDReappearance(t *testing.T) {
	r, device, owner, binding, baseline := managedDiscardAdapterFixture(t)
	defer r.Close()
	if err := r.BindPreparedDiscard(context.Background(), binding); err != nil {
		t.Fatal(err)
	}
	first := discardNext(t, r, binding, 0)
	var original api.Record
	for _, row := range *cleanupModelRows(&device.inv, first.Request.Resource) {
		if row[".id"] == first.Request.ID {
			original = objectRecordCopy(row)
			break
		}
	}
	if original == nil {
		t.Fatal("fixture target missing")
	}
	if _, err := r.ApplyPreparedDiscard(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	rows := cleanupModelRows(&device.inv, first.Request.Resource)
	*rows = append(*rows, original)
	second := discardNext(t, r, binding, 1)
	_, err := r.ApplyPreparedDiscard(context.Background(), second)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "prepared-discard-completed-object-reappeared" || device.writes != 1 || owner.controls != 0 || device.credentialCalls != 0 {
		t.Fatal("completed native ID was adopted or removed again", err)
	}
	actual := managedDiscardRows(device.inv)
	for key, row := range managedDiscardRows(baseline) {
		if !reflect.DeepEqual(actual[key], row) {
			t.Fatal("reappearance handling changed protected baseline", key)
		}
	}
}
