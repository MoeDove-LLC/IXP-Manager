package routeros

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/routing"
)

func fixture() (Target, routing.Router, []routing.Peer, Snapshot) {
	target := Target{RouterID: "router-a", Version: "7.24.6", DeviceID: "CHR-system-id-1", TransportIdentitySHA256: strings.Repeat("a", 64)}
	r := routing.Router{ID: target.RouterID, ASN: 64500, RouterID: "192.0.2.1", LocalIPv4: "198.51.100.1", LocalIPv6: "2001:db8::1"}
	peers := []routing.Peer{{ID: "peer-a", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 100, AllowedPrefixes: []string{"203.0.113.0/24"}}}
	base := Snapshot{Target: target, Owner: OwnerName(r.ID), NamespaceEmpty: true}
	return target, r, peers, base
}
func bootstrap(t *testing.T) (Plan, []routing.Peer) {
	t.Helper()
	target, r, peers, base := fixture()
	p, err := BuildPlan(target, r, peers, base, PlanOptions{Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	return p, peers
}

// observed is a deterministic model fixture, NOT a native-device proof.
func observed(p Plan) Snapshot {
	s := Snapshot{Target: p.Target, Owner: p.Owner, Managed: true, ASN: p.Desired.ASN, RouterID: p.Desired.RouterID, ActiveGeneration: p.Generation,
		Table: ObjectRef{ID: "*1", Name: p.Owner}, Instance: ObjectRef{ID: "*1", Name: p.Owner},
		InputEntry:  ObjectRef{ID: "*1", Chain: inputRoot(p.Owner), Comment: p.Owner + ":root:in"},
		OutputEntry: ObjectRef{ID: "*2", Chain: outputRoot(p.Owner), Comment: p.Owner + ":root:out"}}
	g := GenerationState{Generation: p.Generation, PolicyChecksum: p.PolicyChecksum, Rules: []ObjectRef{}}
	peerMap := map[string]Peer{}
	for _, peer := range p.Desired.Peers {
		peerMap[peer.ID] = peer
	}
	for i, r := range generationRules(p.Owner, p.Generation, p.Desired) {
		body, _ := compileRule(r, peerMap)
		g.Rules = append(g.Rules, ObjectRef{ID: fmt.Sprintf("*%X", i+3), Chain: r.Chain, Comment: r.Comment, StateChecksum: hash(r), RuleSHA256: routing.Checksum(body)})
	}
	s.Generations = []GenerationState{g}
	for i, peer := range p.Desired.Peers {
		c := connection(p.Owner, p.Desired, peer)
		s.Connections = append(s.Connections, ConnectionState{Ref: ObjectRef{ID: fmt.Sprintf("*%X", i+1), Name: c.Name, StateChecksum: hash(c)}, Config: c, AuthenticationVerified: true})
	}
	s.Table.StateChecksum = hash([]string{p.Owner, "fib=no"})
	s.Instance.StateChecksum = hash([]string{p.Owner, p.Desired.RouterID, number(p.Desired.ASN)})
	s.InputEntry.RuleSHA256 = routing.Checksum("jump " + generationChain(p.Owner, p.Generation, "in") + ";")
	s.OutputEntry.RuleSHA256 = routing.Checksum("jump " + generationChain(p.Owner, p.Generation, "out") + ";")
	s.InputEntry.StateChecksum = hash(s.InputEntry)
	s.OutputEntry.StateChecksum = hash(s.OutputEntry)
	return s
}
func routerFrom(p Plan) routing.Router {
	return routing.Router{ID: p.Target.RouterID, ASN: p.Desired.ASN, RouterID: p.Desired.RouterID, LocalIPv4: p.Desired.LocalIPv4, LocalIPv6: p.Desired.LocalIPv6}
}
func clonePlan(t *testing.T, p Plan) Plan {
	t.Helper()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var result Plan
	if err = json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestBootstrapCompilesOnlyInertPrepare(t *testing.T) {
	p, _ := bootstrap(t)
	if err := ValidatePlan(p); err != nil {
		t.Fatal(err)
	}
	p = clonePlan(t, p)
	if err := ValidatePlan(p); err != nil {
		t.Fatalf("JSON roundtrip: %v", err)
	}
	c, err := Compile(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.NativeValidated || p.Budget.Verified || c.NativeValidated || !p.RequiresTargetValidation || !p.RequiresSafeMode || !p.RequiresExclusiveControl {
		t.Fatal("structural plan overclaims execution proof")
	}
	seenEnable, seenNonFIB, seenDisabled, seenAuthInit, seenUnset := false, false, false, false, false
	for _, cmd := range c.Commands {
		if cmd.Phase == Prepare && cmd.Action == Enable {
			t.Fatal("prepare activates connection")
		}
		if cmd.Phase == Prepare && cmd.Resource == BGPConnection {
			for _, a := range cmd.Attributes {
				if a.Name == "disabled" && a.Value == "yes" {
					seenDisabled = true
				}
				if a.Name == "tcp-md5-key" {
					seenAuthInit = true
					if !seenDisabled || a.Value != "" {
						t.Fatal("new no-auth connection was not explicitly initialized while disabled")
					}
				}
			}
		}
		if cmd.Resource == RoutingTable {
			seenNonFIB = true
			for _, a := range cmd.Attributes {
				if a.Name == "fib" {
					t.Fatal("native presence switch fib was emitted")
				}
			}
		}
		if cmd.Phase == Prepare && cmd.Action == Unset && cmd.Property == "input.limit-process-routes-ipv6" {
			seenUnset = true
		}
		if cmd.Phase == Activate && cmd.Action == Enable {
			seenEnable = true
		}
	}
	if !seenEnable || !seenNonFIB || !seenDisabled || !seenAuthInit || !seenUnset {
		t.Fatal("missing expected bounded bootstrap phases")
	}
}

func TestCompiledConnectionSetsConnectListenExplicitly(t *testing.T) {
	p, _ := bootstrap(t)
	compiled, err := Compile(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, command := range compiled.Commands {
		if command.Resource != BGPConnection || command.Action != Add {
			continue
		}
		found = true
		values := map[string]string{}
		for _, a := range command.Attributes {
			values[a.Name] = a.Value
		}
		if values["connect"] != "yes" || values["listen"] != "yes" || values["disabled"] != "yes" {
			t.Fatal("new connection must remain disabled with explicit connect/listen intent")
		}
	}
	if !found {
		t.Fatal("no connection creation checked")
	}
}

func TestPrepareInitializesAuthenticationBeforeActivation(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			target, router, peers, empty := fixture()
			credentials := Credentials{}
			if enabled {
				peers[0].Password = "ExplicitInit_123"
				peers[0].AuthenticationRevision = "AAAAAAAAAAAAAAAAAAAAAA"
				credentials[peers[0].ID] = Credential{Revision: peers[0].AuthenticationRevision, Password: peers[0].Password}
			}
			p, err := BuildPlan(target, router, peers, empty, PlanOptions{Bootstrap: true})
			if err != nil {
				t.Fatal(err)
			}
			compiled, err := Compile(p, credentials)
			if err != nil {
				t.Fatal(err)
			}
			created, unset, initialized := false, false, false
			for _, cmd := range compiled.Commands {
				if cmd.Phase == Activate && !initialized {
					t.Fatal("activation begins before explicit key initialization")
				}
				if cmd.Resource != BGPConnection {
					continue
				}
				if cmd.Action == Add {
					created = true
				}
				if cmd.Action == Unset {
					if !created {
						t.Fatal("unset before connection creation")
					}
					unset = true
				}
				for _, a := range cmd.Attributes {
					if a.Name == "tcp-md5-key" {
						if cmd.Phase != Prepare || !created || !unset || a.Value != peers[0].Password || !a.Sensitive {
							t.Fatal("new connection auth initialization is not explicit and inert")
						}
						initialized = true
					}
				}
			}
			if !initialized {
				t.Fatal("no explicit authentication initialization")
			}
		})
	}
}

func TestUpdateClosesExportsAndRechecksSourcePolicy(t *testing.T) {
	old, peers := bootstrap(t)
	base := observed(old)
	peers[0].AllowedPrefixes = []string{"203.0.113.0/25"}
	peers[0].AllowedRoutes = []routing.PrefixOrigin{{Prefix: "203.0.113.0/25", OriginASN: 64510}}
	p, err := BuildPlan(old.Target, routerFrom(old), peers, base, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	activation := []Operation{}
	for _, op := range p.Operations {
		if op.Phase == Activate {
			activation = append(activation, op)
		}
	}
	if len(activation) != 3 || activation[0].Kind != SetEntry || activation[0].Rule.Kind != RejectRule || activation[0].Object != base.OutputEntry {
		t.Fatalf("policy-only activation must gate exports before two roots: %+v", activation)
	}
	c, err := Compile(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, cmd := range c.Commands {
		for _, a := range cmd.Attributes {
			if a.Name == "rule" && strings.Contains(a.Value, "bgp-input-remote-as != bgp-output-remote-as") {
				found = true
				for _, want := range []string{"bgp-input-remote-addr == 198.51.100.2", "dst == 203.0.113.0/25", "bgp-as-path ^64501", "bgp-as-path 64510$"} {
					if !strings.Contains(a.Value, want) {
						t.Fatalf("output misses source constraint %q: %s", want, a.Value)
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("output did not recheck source policy")
	}
	for _, op := range p.Operations {
		if op.Kind == RemoveRule && op.Phase != Cleanup {
			t.Fatal("old generation removed before confirm")
		}
	}
}

func TestIdempotencyCanonicalOrderAndIndependentState(t *testing.T) {
	p, peers := bootstrap(t)
	base := observed(p)
	before := hash(base)
	unchanged, err := BuildPlan(p.Target, routerFrom(p), peers, base, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(unchanged.Operations) != 0 || unchanged.Budget.EstimatedActions != 0 {
		t.Fatal("identical managed plan is not a no-op")
	}
	if hash(base) != before {
		t.Fatal("caller base mutated")
	}
	target, r, peers, empty := fixture()
	peers = append(peers, routing.Peer{ID: "peer-b", ASN: 64502, Address: "2001:db8::2", Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"2001:db8:2::/48", "2001:db8:3::/48"}})
	first, err := BuildPlan(target, r, peers, empty, PlanOptions{Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	peers[0], peers[1] = peers[1], peers[0]
	peers[0].AllowedPrefixes[0], peers[0].AllowedPrefixes[1] = peers[0].AllowedPrefixes[1], peers[0].AllowedPrefixes[0]
	second, err := BuildPlan(target, r, peers, empty, PlanOptions{Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("input order changed canonical plan")
	}
}

func TestAuthenticationRotationAndSecretRedaction(t *testing.T) {
	old, peers := bootstrap(t)
	base := observed(old)
	secret := "Portable_SECRET+0123/="
	peers[0].Password = secret
	peers[0].AuthenticationRevision = "AAAAAAAAAAAAAAAAAAAAAA"
	p, err := BuildPlan(old.Target, routerFrom(old), peers, base, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, credentials := range []Credentials{nil, {"peer-a": {Revision: "BBBBBBBBBBBBBBBBBBBBBB", Password: secret}}, {"peer-a": {Revision: peers[0].AuthenticationRevision, Password: "bad\nvalue"}}} {
		if _, err := Compile(p, credentials); err == nil {
			t.Fatal("missing, stale or malformed credential accepted")
		}
	}
	credentials := Credentials{"peer-a": {Revision: peers[0].AuthenticationRevision, Password: secret}}
	c, err := Compile(p, credentials)
	if err != nil {
		t.Fatal(err)
	}
	saw := false
	for _, cmd := range c.Commands {
		for _, a := range cmd.Attributes {
			if a.Name == "tcp-md5-key" {
				saw = true
				if a.Value != secret || !a.Sensitive {
					t.Fatal("transport lost secret or sensitivity")
				}
			}
		}
	}
	if !saw {
		t.Fatal("rotation has no authentication operation")
	}
	for _, v := range []any{p, c, credentials, Attribute{Name: "tcp-md5-key", Value: secret, Sensitive: false}} {
		j, _ := json.Marshal(v)
		for _, rendered := range []string{string(j), fmt.Sprintf("%+v", v), fmt.Sprintf("%#v", v)} {
			if strings.Contains(rendered, secret) {
				t.Fatal("secret reached ordinary serialization/log formatting")
			}
		}
	}
	base = observed(p)
	peers[0].Password = ""
	peers[0].AuthenticationRevision = "BBBBBBBBBBBBBBBBBBBBBB"
	cleared, err := BuildPlan(p.Target, routerFrom(p), peers, base, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c, err = Compile(cleared, nil)
	if err != nil {
		t.Fatal(err)
	}
	saw = false
	for _, cmd := range c.Commands {
		for _, a := range cmd.Attributes {
			if a.Name == "tcp-md5-key" {
				saw = true
				if a.Value != "" {
					t.Fatal("clear did not clear")
				}
			}
		}
	}
	if !saw || cleared.PolicyChecksum == old.PolicyChecksum {
		t.Fatal("authentication tombstone not bound to policy")
	}
}

func TestRejectUnsafeBasesAndTargets(t *testing.T) {
	p, peers := bootstrap(t)
	cases := map[string]func(*Snapshot){
		"wrong target":          func(s *Snapshot) { s.Target.DeviceID = "other" },
		"FIB":                   func(s *Snapshot) { s.TableFIB = true },
		"foreign reference":     func(s *Snapshot) { s.ForeignReferences = true },
		"unrecognized property": func(s *Snapshot) { s.UnexpectedObjects = true },
		"credential unknown":    func(s *Snapshot) { s.Connections[0].AuthenticationVerified = false },
		"wrong connection":      func(s *Snapshot) { s.Connections[0].Ref.Name = "foreign" },
		"wrong chain":           func(s *Snapshot) { s.Generations[0].Rules[0].Chain = "foreign" },
		"malicious comment":     func(s *Snapshot) { s.Generations[0].Rules[0].Comment += "\n" },
		"duplicate root ID":     func(s *Snapshot) { s.InputEntry.ID = s.OutputEntry.ID },
		"missing generation":    func(s *Snapshot) { s.ActiveGeneration = strings.Repeat("0", 16) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			base := observed(p)
			mutate(&base)
			if _, err := BuildPlan(p.Target, routerFrom(p), peers, base, PlanOptions{}); err == nil {
				t.Fatal("unsafe base accepted")
			}
		})
	}
	target, r, peers, base := fixture()
	if _, err := BuildPlan(target, r, peers, base, PlanOptions{}); err == nil {
		t.Fatal("silent bootstrap")
	}
	for _, version := range []string{"7.19", "7.24.6beta1", "7.24.6;run", "8.0", "7.24.6 (stable)"} {
		target.Version = version
		base.Target = target
		if _, err := BuildPlan(target, r, peers, base, PlanOptions{Bootstrap: true}); err == nil {
			t.Fatalf("non-exact or unsupported version %q", version)
		}
	}
}

func TestPlanTamperAndActivationBudget(t *testing.T) {
	p, _ := bootstrap(t)
	for name, mutate := range map[string]func(*Plan){
		"resource":        func(p *Plan) { p.Operations[0].Kind = "run-script" },
		"native claim":    func(p *Plan) { p.NativeValidated = true },
		"safe-mode claim": func(p *Plan) { p.Budget.Verified = true },
		"skip guard":      func(p *Plan) { p.RequiresExclusiveControl = false },
		"rule": func(p *Plan) {
			for i := range p.Operations {
				if p.Operations[i].Rule != nil {
					p.Operations[i].Rule.JumpTarget = "evil"
					return
				}
			}
		},
		"owner": func(p *Plan) { p.Owner = "foreign" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := clonePlan(t, p)
			mutate(&bad)
			bad.Checksum = ""
			bad.Checksum = hash(bad)
			if err := ValidatePlan(bad); err == nil {
				t.Fatal("forged operation accepted despite recomputed hash")
			}
		})
	}
	target, r, peers, base := fixture()
	peers = nil
	for i := 0; i < 78; i++ {
		peers = append(peers, routing.Peer{ID: fmt.Sprintf("p%d", i), ASN: uint32(64501 + i), Address: fmt.Sprintf("198.51.100.%d", i+2), Enabled: true, MaxPrefixes: 10, AllowedPrefixes: []string{"203.0.113.0/24"}})
	}
	if _, err := BuildPlan(target, r, peers, base, PlanOptions{Bootstrap: true}); err == nil {
		t.Fatal("over-budget activation accepted")
	}
	p, err := BuildPlan(target, r, peers[:77], base, PlanOptions{Bootstrap: true})
	if err != nil {
		t.Fatal(err)
	}
	if p.Budget.EstimatedActions != 80 {
		t.Fatal("incorrect conservative budget")
	}
}

func TestRemovedPeerDisabledBeforeCleanupAndIdentityGate(t *testing.T) {
	old, peers := bootstrap(t)
	base := observed(old)
	peers[0].Enabled = false
	p, err := BuildPlan(old.Target, routerFrom(old), peers, base, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	disable, remove := -1, -1
	for i, op := range p.Operations {
		if op.Kind == DisableConnection {
			disable = i
		}
		if op.Kind == RemoveConnection {
			remove = i
			if op.Phase != Cleanup {
				t.Fatal("remove is not post-confirm cleanup")
			}
		}
	}
	if disable < 0 || remove <= disable {
		t.Fatal("removed peer not disabled before cleanup")
	}
	r := routerFrom(old)
	r.ASN++
	peers[0].Enabled = true
	peers[0].ASN = 64510
	if _, err := BuildPlan(old.Target, r, peers, base, PlanOptions{}); err == nil {
		t.Fatal("silent ASN change")
	}
	if _, err := BuildPlan(old.Target, r, peers, base, PlanOptions{AllowIdentityChange: true}); err != nil {
		t.Fatal(err)
	}
}

func TestObservedBodyAndAttributeDriftAreBoundToBase(t *testing.T) {
	old, peers := bootstrap(t)
	before := observed(old)
	// A comment and object ID are not a content proof.
	bad := observed(old)
	bad.Generations[0].Rules[0].RuleSHA256 = routing.Checksum("accept;")
	if _, err := BuildPlan(old.Target, routerFrom(old), peers, bad, PlanOptions{}); err == nil {
		t.Fatal("same generation with modified filter body was reused")
	}
	bad = observed(old)
	bad.OutputEntry.RuleSHA256 = routing.Checksum("accept;")
	if _, err := BuildPlan(old.Target, routerFrom(old), peers, bad, PlanOptions{}); err == nil {
		t.Fatal("root unexpectedly accepting all routes was accepted")
	}
	bad = observed(old)
	bad.Instance.StateChecksum = ""
	if _, err := BuildPlan(old.Target, routerFrom(old), peers, bad, PlanOptions{}); err == nil {
		t.Fatal("missing observed attribute digest accepted")
	}
	peers[0].AllowedPrefixes = []string{"203.0.113.0/25"}
	first, err := BuildPlan(old.Target, routerFrom(old), peers, before, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bad = observed(old)
	bad.Generations[0].Rules[0].RuleSHA256 = routing.Checksum("reject;")
	bad.Generations[0].Rules[0].StateChecksum = hash([]string{"actual different state"})
	second, err := BuildPlan(old.Target, routerFrom(old), peers, bad, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if first.BaseChecksum == second.BaseChecksum || first.Checksum == second.Checksum {
		t.Fatal("observed filter body drift did not invalidate reviewed plan")
	}
	bad = observed(old)
	bad.Table.StateChecksum = hash([]string{"changed controlled attribute"})
	third, err := BuildPlan(old.Target, routerFrom(old), peers, bad, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if first.BaseChecksum == third.BaseChecksum {
		t.Fatal("table property drift not bound")
	}
}

func TestUnsupportedPolicyAndInjectionRejected(t *testing.T) {
	for name, mutate := range map[string]func(*routing.Router, []routing.Peer){
		"collector":          func(r *routing.Router, p []routing.Peer) { r.Role = routing.RoleRouteCollector },
		"community override": func(r *routing.Router, p []routing.Peer) { yes := true; r.RFC1997Passthrough = &yes },
		"identifier":         func(r *routing.Router, p []routing.Peer) { p[0].ID = "p;run" },
		"prefix":             func(r *routing.Router, p []routing.Peer) { p[0].AllowedPrefixes = []string{"203.0.113.0/24; accept;"} },
		"secret": func(r *routing.Router, p []routing.Peer) {
			p[0].Password = "wrong\nkey"
			p[0].AuthenticationRevision = "AAAAAAAAAAAAAAAAAAAAAA"
		},
	} {
		t.Run(name, func(t *testing.T) {
			target, r, peers, base := fixture()
			mutate(&r, peers)
			if _, err := BuildPlan(target, r, peers, base, PlanOptions{Bootstrap: true}); err == nil {
				t.Fatal("unsupported or injected input accepted")
			}
		})
	}
}
