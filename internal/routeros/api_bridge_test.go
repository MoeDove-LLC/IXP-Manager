package routeros

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/routeros/api"
)

func bridgeInventory(t *testing.T, empty bool) (api.Inventory, TargetEvidence) {
	t.Helper()
	_, inv, _, e := inspectFixture(t)
	if empty {
		inv.Tables = nil
		inv.Instances = nil
		inv.Connections = nil
		inv.FilterRules = nil
	}
	return inv, e
}
func modelRows(inv *api.Inventory, resource api.Resource) *[]api.Record {
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
	panic("unexpected fixture menu")
}

// modelMutation changes only an in-memory fixed read dictionary. It neither
// invokes RouterOS nor validates its native mutation/default/rollback semantics.
func modelMutation(t *testing.T, inv *api.Inventory, m api.Mutation) {
	t.Helper()
	rows := modelRows(inv, m.Resource)
	if m.Action == api.Add {
		used := map[string]bool{}
		for _, row := range *rows {
			used[row[".id"]] = true
		}
		id := ""
		for i := 1; id == ""; i++ {
			candidate := "*" + strings.ToUpper(strconv.FormatInt(int64(i), 16))
			if !used[candidate] {
				id = candidate
			}
		}
		row := api.Record{".id": id}
		if m.Resource == api.FilterRule {
			row["disabled"] = "false"
			row["inactive"] = "false"
		}
		for _, a := range m.Attributes {
			if a.Name != "tcp-md5-key" {
				row[a.Name] = a.Value
			}
		}
		*rows = append(*rows, row)
		return
	}
	for i, row := range *rows {
		if row[".id"] != m.ID {
			continue
		}
		switch m.Action {
		case api.Set:
			for _, a := range m.Attributes {
				if a.Name != "tcp-md5-key" {
					row[a.Name] = a.Value
				}
			}
		case api.Enable:
			row["disabled"] = "false"
		case api.Disable:
			row["disabled"] = "true"
		case api.Unset:
			delete(row, m.Property)
		case api.Remove:
			*rows = append((*rows)[:i], (*rows)[i+1:]...)
		default:
			t.Fatal("unexpected fixture action")
		}
		return
	}
	t.Fatal("fixture mutation object absent")
}
func bridgeAll(t *testing.T, p Plan, credentials Credentials, inv api.Inventory, e TargetEvidence) []APIMutation {
	t.Helper()
	bridge, err := NewAPIBridge(p, credentials)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := Compile(p, credentials)
	if err != nil {
		t.Fatal(err)
	}
	result := []APIMutation{}
	for i, descriptor := range bridge.Operations() {
		resolver, err := NewAPIResolver(inv, p.Target, e)
		if err != nil {
			t.Fatal(err)
		}
		envelope, err := bridge.Mutation(descriptor.ID, resolver)
		if err != nil {
			t.Fatalf("%s %s %s: %v", descriptor.ID, descriptor.Resource, descriptor.Action, err)
		}
		if err := api.ValidateMutation(envelope.Request); err != nil {
			t.Fatal(err)
		}
		if envelope.NativeValidated || !envelope.RequiresTargetValidation || !envelope.RequiresStateVerification || envelope.PlanChecksum != p.Checksum || len(envelope.NativeChecks) == 0 {
			t.Fatal("encoding was promoted to an execution proof")
		}
		want := compiled.Commands[i]
		if envelope.Request.Property != want.Property {
			t.Fatal("typed unset property changed")
		}
		if want.Resource == BGPConnection && (want.Action == Add || want.Action == Set) {
			configurationWrite := want.Action == Add
			tableSeen := false
			connectSeen, listenSeen := false, false
			localASSeen := false
			for _, a := range want.Attributes {
				if a.Name == "afi" {
					configurationWrite = true
				}
				if a.Name == "routing-table" {
					tableSeen = true
					if a.Value != p.Owner || !hasCheck(envelope, "connection-and-instance-exact-table-readback") {
						t.Fatal("connection routing table was not explicitly bound/read-back gated")
					}
				}
				if a.Name == "connect" {
					connectSeen = a.Value == "yes"
				}
				if a.Name == "listen" {
					listenSeen = a.Value == "yes"
				}
				if a.Name == "as" {
					localASSeen = true
				}
			}
			if configurationWrite && !tableSeen {
				t.Fatal("connection configuration omitted explicit managed table")
			}
			if configurationWrite && (!connectSeen || !listenSeen) {
				t.Fatal("connection configuration omitted explicit connect/listen")
			}
			if configurationWrite && !localASSeen {
				t.Fatal("connection configuration omitted explicit local ASN")
			}
		}
		if len(envelope.Request.Attributes) != len(want.Attributes) {
			t.Fatal("compiler attribute silently dropped")
		}
		for j, a := range want.Attributes {
			got := envelope.Request.Attributes[j]
			if got.Name != a.Name || got.Value != a.Value || got.Sensitive != (a.Sensitive || a.Name == "tcp-md5-key") {
				t.Fatal("compiler attribute silently changed")
			}
		}
		if descriptor.Action == Add {
			if envelope.Request.ID != "" {
				t.Fatal("placeholder ID escaped pure preflight")
			}
		} else if !internalID.MatchString(envelope.Request.ID) {
			t.Fatal("mutation did not use a read-back internal ID")
		}
		result = append(result, envelope)
		modelMutation(t, &inv, envelope.Request)
	}
	if len(result) != len(p.Operations) {
		t.Fatal("operation omitted")
	}
	return result
}
func hasCheck(m APIMutation, want string) bool {
	for _, v := range m.NativeChecks {
		if v == want {
			return true
		}
	}
	return false
}
func TestAPIBridgeAllGeneratedOperationsPreserveAttributes(t *testing.T) {
	p, peers := bootstrap(t)
	t.Run("bootstrap", func(t *testing.T) {
		inv, e := bridgeInventory(t, true)
		result := bridgeAll(t, p, nil, inv, e)
		sawTable, sawUnset, sawAuth := false, false, false
		for _, m := range result {
			if m.Request.Resource == api.RoutingTable && m.Request.Action == api.Add {
				sawTable = true
				if !hasCheck(m, "native-non-fib-unset-readback") {
					t.Fatal("non-FIB readback native gate missing")
				}
			}
			if m.Request.Action == api.Unset && m.Request.Property == "input.limit-process-routes-ipv6" {
				sawUnset = true
				if !hasCheck(m, "native-property-unset-and-readback") {
					t.Fatal("unset native gate missing")
				}
			}
			for _, a := range m.Request.Attributes {
				if a.Name == "fib" {
					t.Fatal("native fib presence switch emitted")
				}
				if a.Name == "input.limit-process-routes-ipv6" {
					t.Fatal("unused AF limit must be an unset operation")
				}
				if a.Name == "tcp-md5-key" && m.Phase == Prepare && a.Value == "" {
					sawAuth = true
				}
			}
		}
		if !sawTable || !sawUnset || !sawAuth {
			t.Fatal("bootstrap did not cover pending native properties")
		}
	})
	t.Run("policy-update-and-cleanup", func(t *testing.T) {
		base := observed(p)
		_, _, currentPeers, _ := fixture()
		currentPeers[0].AllowedPrefixes = []string{"203.0.113.0/25"}
		next, err := BuildPlan(p.Target, routerFrom(p), currentPeers, base, PlanOptions{})
		if err != nil {
			t.Fatal(err)
		}
		inv, e := bridgeInventory(t, false)
		bridgeAll(t, next, nil, inv, e)
	})
	t.Run("authentication", func(t *testing.T) {
		secret := "BridgePortable_SECRET+123/="
		rotated := append(peers[:0:0], peers...)
		rotated[0].Password = secret
		rotated[0].AuthenticationRevision = "AAAAAAAAAAAAAAAAAAAAAA"
		next, err := BuildPlan(p.Target, routerFrom(p), rotated, observed(p), PlanOptions{})
		if err != nil {
			t.Fatal(err)
		}
		creds := Credentials{"peer-a": {Revision: rotated[0].AuthenticationRevision, Password: secret}}
		inv, e := bridgeInventory(t, false)
		result := bridgeAll(t, next, creds, inv, e)
		saw := false
		for _, m := range result {
			for _, a := range m.Request.Attributes {
				if a.Name == "tcp-md5-key" {
					saw = true
					if a.Value != secret || !a.Sensitive {
						t.Fatal("credential changed before transport")
					}
				}
			}
			encoded, _ := json.Marshal(m)
			if strings.Contains(string(encoded), secret) || strings.Contains(fmt.Sprintf("%#v", m), secret) {
				t.Fatal("bridge result leaked a key")
			}
		}
		if !saw {
			t.Fatal("credential not expressed")
		}
		bridge, err := NewAPIBridge(next, creds)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(bridge)
		if strings.Contains(string(data), secret) || strings.Contains(fmt.Sprintf("%#v", bridge), secret) {
			t.Fatal("private bridge logged secret")
		}
	})
	t.Run("family-change", func(t *testing.T) {
		changed := append(peers[:0:0], peers...)
		changed[0].Address = "2001:db8::2"
		changed[0].AllowedPrefixes = []string{"2001:db8:2::/48"}
		next, err := BuildPlan(p.Target, routerFrom(p), changed, observed(p), PlanOptions{})
		if err != nil {
			t.Fatal(err)
		}
		inv, e := bridgeInventory(t, false)
		result := bridgeAll(t, next, nil, inv, e)
		saw, sawUnset := false, false
		for _, m := range result {
			if m.Request.Action == api.Unset && m.Request.Property == "input.limit-process-routes-ipv4" {
				sawUnset = true
				if !hasCheck(m, "native-property-unset-and-readback") {
					t.Fatal("AF clear lost native gate")
				}
			}
			if m.Request.Resource != api.BGPConnection || m.Request.Action != api.Set {
				continue
			}
			values := map[string]string{}
			for _, a := range m.Request.Attributes {
				values[a.Name] = a.Value
			}
			if values["afi"] == "ipv6" {
				saw = true
				_, ok := values["input.limit-process-routes-ipv4"]
				if ok || values["input.limit-process-routes-ipv6"] != "100" {
					t.Fatal("family-change active limit was not expressed exactly")
				}
			}
		}
		if !saw || !sawUnset {
			t.Fatal("family change not covered")
		}
	})
	t.Run("authentication-clear", func(t *testing.T) {
		target, router, current, empty := fixture()
		current[0].Password = "PortableSecret_123"
		current[0].AuthenticationRevision = "AAAAAAAAAAAAAAAAAAAAAA"
		authPlan, err := BuildPlan(target, router, current, empty, PlanOptions{Bootstrap: true})
		if err != nil {
			t.Fatal(err)
		}
		current[0].Password = ""
		current[0].AuthenticationRevision = "BBBBBBBBBBBBBBBBBBBBBB"
		next, err := BuildPlan(target, router, current, observed(authPlan), PlanOptions{})
		if err != nil {
			t.Fatal(err)
		}
		inv, e := bridgeInventory(t, false)
		// Authentication revisions change generation names but not the route
		// predicates in this fixture. Model the already observed prior generation.
		for _, row := range inv.FilterRules {
			for _, key := range []string{"chain", "comment", "rule"} {
				row[key] = strings.ReplaceAll(row[key], p.Generation, authPlan.Generation)
			}
		}
		result := bridgeAll(t, next, nil, inv, e)
		saw := false
		for _, m := range result {
			for _, a := range m.Request.Attributes {
				if a.Name == "tcp-md5-key" {
					saw = true
					if a.Value != "" || !hasCheck(m, "empty-credential-clear") {
						t.Fatal("authentication clear was omitted or promoted to native proof")
					}
				}
			}
		}
		if !saw {
			t.Fatal("authentication clear missing")
		}
	})
}

func TestAPIBridgeNewObjectsMustBeReadBackAndOperationsAreClosed(t *testing.T) {
	p, _ := bootstrap(t)
	bridge, err := NewAPIBridge(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	inv, e := bridgeInventory(t, true)
	resolver, err := NewAPIResolver(inv, p.Target, e)
	if err != nil {
		t.Fatal(err)
	}
	enable := ""
	for _, op := range bridge.Operations() {
		if op.Action == Enable {
			enable = op.ID
		}
	}
	if _, err := bridge.Mutation(enable, resolver); err == nil {
		t.Fatal("new connection got guessed ID before read-back")
	}
	if _, err := bridge.Mutation("run-arbitrary", resolver); err == nil {
		t.Fatal("non-plan operation accepted")
	}
	if _, err := bridge.Mutation(bridge.Operations()[0].ID, nil); err == nil {
		t.Fatal("missing trusted resolver accepted")
	}
	other := p.Target
	other.RouterID = "other-router"
	wrong, err := NewAPIResolver(inv, other, e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.Mutation(bridge.Operations()[0].ID, wrong); err == nil {
		t.Fatal("wrong target resolver accepted")
	}
	full, e := bridgeInventory(t, false)
	existing, err := NewAPIResolver(full, p.Target, e)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bridge.Mutation(bridge.Operations()[0].ID, existing); err == nil {
		t.Fatal("Add silently adopted an existing object")
	}
}

func TestAPIBridgeResolverRejectsAmbiguousAndChangedIdentity(t *testing.T) {
	p, peers := bootstrap(t)
	peers[0].AllowedPrefixes = []string{"203.0.113.0/25"}
	next, err := BuildPlan(p.Target, routerFrom(p), peers, observed(p), PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewAPIBridge(next, nil)
	if err != nil {
		t.Fatal(err)
	}
	rootID := ""
	for _, op := range next.Operations {
		if op.Kind == SetEntry && op.Object.Chain == outputRoot(p.Owner) {
			rootID = op.ID
			break
		}
	}
	for name, mutate := range map[string]func(*api.Inventory){
		"duplicate marker": func(v *api.Inventory) {
			row := api.Record{}
			for k, x := range v.FilterRules[1] {
				row[k] = x
			}
			row[".id"] = "*F0"
			v.FilterRules = append(v.FilterRules, row)
		},
		"marker on wrong chain": func(v *api.Inventory) {
			row := api.Record{}
			for k, x := range v.FilterRules[1] {
				row[k] = x
			}
			row[".id"] = "*F0"
			row["chain"] = "foreign"
			v.FilterRules = append(v.FilterRules, row)
		},
		"replaced ID":    func(v *api.Inventory) { v.FilterRules[1][".id"] = "*F0" },
		"missing marker": func(v *api.Inventory) { v.FilterRules[1]["comment"] = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			inv, e := bridgeInventory(t, false)
			mutate(&inv)
			r, err := NewAPIResolver(inv, p.Target, e)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := bridge.Mutation(rootID, r); err == nil {
				t.Fatal("ambiguous, replaced or unknown reference accepted")
			}
		})
	}
	inv, e := bridgeInventory(t, false)
	inv.Tables[0][".id"] = "0"
	if _, err := NewAPIResolver(inv, p.Target, e); err == nil {
		t.Fatal("display row index accepted")
	}
	inv, e = bridgeInventory(t, false)
	inv.Capabilities[api.FilterRule] = api.Unknown
	if _, err := NewAPIResolver(inv, p.Target, e); err == nil {
		t.Fatal("incomplete menu treated as empty")
	}
}

func TestAPIBridgeMenuScopeAndCurrentCompilerAPISchema(t *testing.T) {
	p, peers := bootstrap(t)
	base := observed(p)
	base.Table.ID = "*F"
	base.Instance.ID = "*A"
	router := routerFrom(p)
	router.ASN = 64520
	next, err := BuildPlan(p.Target, router, peers, base, PlanOptions{AllowIdentityChange: true})
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewAPIBridge(next, nil)
	if err != nil {
		t.Fatal(err)
	}
	inv, e := bridgeInventory(t, false)
	inv.Tables[0][".id"] = "*F"
	inv.Instances[0][".id"] = "*A"
	r, err := NewAPIResolver(inv, p.Target, e)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, op := range bridge.Operations() {
		if op.Resource == BGPInstance && op.Action == Set {
			m, err := bridge.Mutation(op.ID, r)
			if err != nil {
				t.Fatal(err)
			}
			found = true
			if m.Request.ID != "*A" {
				t.Fatal("same-name table confused with BGP instance")
			}
		}
	}
	if !found {
		t.Fatal("instance update absent")
	}
	// Empty limit Set is rejected; only the separate typed Unset encodes reset.
	for _, name := range []string{"input.limit-process-routes-ipv4", "input.limit-process-routes-ipv6"} {
		if err := api.ValidateMutation(api.Mutation{Resource: api.BGPConnection, Action: api.Set, ID: "*1", Attributes: []api.Attribute{{Name: name, Value: ""}}}); err == nil {
			t.Fatalf("unsafe empty limit Set accepted for %s", name)
		}
		if err := api.ValidateMutation(api.Mutation{Resource: api.BGPConnection, Action: api.Unset, ID: "*1", Property: name}); err != nil {
			t.Fatalf("typed unset schema mismatch: %v", err)
		}
	}
}
