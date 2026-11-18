package routeros

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routing"
)

func inspectFixture(t *testing.T) (Plan, api.Inventory, KnownState, TargetEvidence) {
	t.Helper()
	p, _ := bootstrap(t)
	known := KnownState{Target: p.Target, ActiveGeneration: p.Generation, Generations: []KnownGeneration{{Target: p.Target, Desired: p.Desired, PolicyChecksum: p.PolicyChecksum}}}
	for _, peer := range p.Desired.Peers {
		known.Peers = append(known.Peers, KnownPeer{ID: peer.ID, Authentication: peer.Authentication})
	}
	inv := api.Inventory{Resources: []api.Record{{"version": p.Target.Version}}, Licenses: []api.Record{{"system-id": p.Target.DeviceID}}, Capabilities: map[api.Resource]api.Capability{}}
	for _, r := range []api.Resource{api.SystemResource, api.SystemLicense, api.RoutingTable, api.BGPInstance, api.BGPConnection, api.FilterRule} {
		inv.Capabilities[r] = api.Supported
	}
	inv.Tables = []api.Record{{".id": "*1", "name": p.Owner, "fib": "false", "disabled": "false", "dynamic": "false", "invalid": "false", "used": "true", "comment": ""}}
	inv.Instances = []api.Record{{".id": "*1", "name": p.Owner, "as": number(p.Desired.ASN), "router-id": p.Desired.RouterID, "routing-table": p.Owner, "vrf": "main", "disabled": "false", "inactive": "false", "comment": ""}}
	pm := map[string]Peer{}
	for _, peer := range p.Desired.Peers {
		pm[peer.ID] = peer
	}
	for _, suffix := range []string{"in", "out"} {
		chain := inputRoot(p.Owner)
		if suffix == "out" {
			chain = outputRoot(p.Owner)
		}
		r := Rule{Kind: JumpRule, Chain: chain, Comment: p.Owner + ":root:" + suffix, JumpTarget: generationChain(p.Owner, p.Generation, suffix)}
		body, _ := compileRule(r, pm)
		inv.FilterRules = append(inv.FilterRules, api.Record{".id": fmt.Sprintf("*%X", len(inv.FilterRules)+1), "chain": chain, "rule": body, "comment": r.Comment, "disabled": "false", "inactive": "false"})
	}
	for _, r := range generationRules(p.Owner, p.Generation, p.Desired) {
		body, _ := compileRule(r, pm)
		inv.FilterRules = append(inv.FilterRules, api.Record{".id": fmt.Sprintf("*%X", len(inv.FilterRules)+1), "chain": r.Chain, "rule": body, "comment": r.Comment, "disabled": "false", "inactive": "false"})
	}
	for i, peer := range p.Desired.Peers {
		c := connection(p.Owner, p.Desired, peer)
		row := api.Record{".id": fmt.Sprintf("*%X", i+1), "disabled": "false", "dynamic": "false", "inactive": "false", "routing-table": "", "vrf": "main", "connect": "true", "listen": "true", "multihop": "false", "output.remove-private-as": "false", "output.as-override": "false", "comment": "", "as": "", "input.filter-nlri": "", "output.filter-select": "", "output.network": "", "output.redistribute": ""}
		for _, a := range connectionAttributes(c) {
			row[a.Name] = a.Value
		}
		if c.Family == "ip" {
			row["input.limit-process-routes-ipv6"] = ""
		} else {
			row["input.limit-process-routes-ipv4"] = ""
		}
		row["disabled"] = "false"
		inv.Connections = append(inv.Connections, row)
	}
	e := TargetEvidence{APIAuthenticated: true, APIIdentitySource: LicenseSystemID, APITransportIdentitySHA256: p.Target.TransportIdentitySHA256}
	return p, inv, known, e
}

// The API package separately tests its opaque credential getter/comparator.
// These pure inspector fixtures model only that verified comparison boundary.
func matchedCredential(_ string, _ string, _ Authentication) bool { return true }

func TestBuildSnapshotCanonicalObservedState(t *testing.T) {
	p, inv, known, e := inspectFixture(t)
	before := hash(inv)
	s, err := buildSnapshot(inv, known, e, matchedCredential)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Managed || s.NamespaceEmpty || s.CrossChannelIdentityVerified || s.TableFIB || s.UnexpectedObjects || s.ForeignReferences {
		t.Fatal("incorrect state/evidence projection")
	}
	if len(s.Connections) != 1 || !s.Connections[0].AuthenticationVerified {
		t.Fatal("verified mapping lost")
	}
	for _, r := range append([]ObjectRef{s.Table, s.Instance, s.InputEntry, s.OutputEntry, s.Connections[0].Ref}, s.Generations[0].Rules...) {
		if !digestPattern.MatchString(r.StateChecksum) {
			t.Fatal("missing actual attribute hash")
		}
	}
	if before != hash(inv) {
		t.Fatal("inspection mutated API inventory")
	}
	peers := []routing.Peer{{ID: "peer-a", ASN: 64501, Address: "198.51.100.2", Enabled: true, MaxPrefixes: 100, AllowedPrefixes: []string{"203.0.113.0/24"}}}
	next, err := BuildPlan(p.Target, routerFrom(p), peers, s, PlanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Operations) != 0 {
		t.Fatal("canonical actual snapshot does not roundtrip to no-op")
	}
	e.SSHAuthenticated = true
	e.SSHIdentitySource = LicenseSystemID
	e.SSHDeviceID = p.Target.DeviceID
	e.SSHVersion = p.Target.Version
	e.SSHTransportIdentitySHA256 = p.Target.TransportIdentitySHA256
	s, err = buildSnapshot(inv, known, e, matchedCredential)
	if err != nil || !s.CrossChannelIdentityVerified {
		t.Fatalf("matching explicit dual-channel evidence: %v", err)
	}
}

func TestSnapshotRejectsRuleDriftAndUnknownProperties(t *testing.T) {
	cases := map[string]func(*api.Inventory){
		"body": func(v *api.Inventory) { v.FilterRules[2]["rule"] = "accept;" },
		"reorder": func(v *api.Inventory) {
			v.FilterRules[2], v.FilterRules[len(v.FilterRules)-3] = v.FilterRules[len(v.FilterRules)-3], v.FilterRules[2]
		},
		"root":                    func(v *api.Inventory) { v.FilterRules[1]["rule"] = "accept;" },
		"missing flag":            func(v *api.Inventory) { delete(v.Tables[0], "fib") },
		"empty flag":              func(v *api.Inventory) { v.Tables[0]["fib"] = "" },
		"unknown attribute":       func(v *api.Inventory) { v.Connections[0]["run"] = "secret" },
		"local AS override":       func(v *api.Inventory) { v.Connections[0]["as"] = "64599" },
		"unknown inherited table": func(v *api.Inventory) { v.Connections[0]["routing-table"] = "main" },
		"missing inherited table": func(v *api.Inventory) { v.Connections[0]["routing-table"] = "" },
		"unknown default":         func(v *api.Inventory) { delete(v.Connections[0], "templates") },
		"unknown validity":        func(v *api.Inventory) { delete(v.FilterRules[2], "inactive") },
		"duplicate ID":            func(v *api.Inventory) { v.FilterRules[2][".id"] = v.FilterRules[1][".id"] },
		"dynamic":                 func(v *api.Inventory) { v.Connections[0]["dynamic"] = "true" },
		"non-FIB":                 func(v *api.Inventory) { v.Tables[0]["fib"] = "true" },
		"alternate selection":     func(v *api.Inventory) { v.Connections[0]["output.filter-select"] = "other" },
		"missing generation rule": func(v *api.Inventory) { v.FilterRules = v.FilterRules[:len(v.FilterRules)-1] },
		"unsupported read":        func(v *api.Inventory) { v.Capabilities[api.FilterRule] = api.Unknown },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			_, inv, known, e := inspectFixture(t)
			mutate(&inv)
			if _, err := buildSnapshot(inv, known, e, matchedCredential); err == nil {
				t.Fatal("ambiguous or changed state accepted")
			}
		})
	}
}

func TestSnapshotNamespaceClosureDoesNotTrustNamePrefix(t *testing.T) {
	_, inv, known, e := inspectFixture(t)
	known.Generations = nil
	known.Peers = nil
	known.ActiveGeneration = ""
	inv.Tables = nil
	inv.Instances = nil
	inv.Connections = nil
	inv.FilterRules = nil
	base, err := BuildSnapshot(inv, known, CredentialEvidence{}, e)
	if err != nil || !base.NamespaceEmpty {
		t.Fatalf("explicitly complete empty namespace: %v", err)
	}
	for name, add := range map[string]func(*api.Inventory, string){
		"foreign instance": func(v *api.Inventory, owner string) {
			v.Instances = append(v.Instances, api.Record{".id": "*9", "name": "foreign", "routing-table": owner, "vrf": "main"})
		},
		"foreign connection": func(v *api.Inventory, owner string) {
			v.Connections = append(v.Connections, api.Record{".id": "*9", "name": "foreign", "instance": owner, "templates": "", "routing-table": "", "vrf": "main", "input.filter": "", "input.filter-nlri": "", "output.filter-chain": "", "output.filter-select": ""})
		},
		"foreign jump": func(v *api.Inventory, owner string) {
			v.FilterRules = append(v.FilterRules, api.Record{".id": "*9", "chain": "unrelated", "comment": "", "rule": "jump " + inputRoot(owner) + ";"})
		},
		"orphan owner table": func(v *api.Inventory, owner string) {
			v.Tables = append(v.Tables, api.Record{".id": "*9", "name": owner})
		},
		"opaque template": func(v *api.Inventory, owner string) {
			v.Connections = append(v.Connections, api.Record{".id": "*9", "name": "foreign", "instance": "other", "templates": "uninspected", "routing-table": "", "vrf": "main", "input.filter": "", "input.filter-nlri": "", "output.filter-chain": "", "output.filter-select": ""})
		},
	} {
		t.Run(name, func(t *testing.T) {
			copy := inv
			add(&copy, OwnerName(known.Target.RouterID))
			s, err := BuildSnapshot(copy, known, CredentialEvidence{}, e)
			if err == nil || s.NamespaceEmpty {
				t.Fatal("references hidden behind unrelated names accepted as empty")
			}
		})
	}
}

func TestSnapshotCredentialsNeverInferredFromManifestOrKnownFlag(t *testing.T) {
	_, inv, known, e := inspectFixture(t)
	for _, evidence := range []CredentialEvidence{
		{},
		{Observed: map[string]api.Credential{"*1": {Known: false}}},
		// A claimed Known bool with no opaque getter result cannot prove absence.
		{Observed: map[string]api.Credential{"*1": {Known: true}}},
	} {
		s, err := BuildSnapshot(inv, known, evidence, e)
		if err == nil || len(s.Connections) != 1 || s.Connections[0].AuthenticationVerified {
			t.Fatal("unknown credential became verified")
		}
	}
	secret := "PortableSecret_123"
	ev := CredentialEvidence{Expected: Credentials{"peer-a": {Revision: "AAAAAAAAAAAAAAAAAAAAAA", Password: secret}}}
	encoded, _ := json.Marshal(ev)
	if strings.Contains(string(encoded), secret) || strings.Contains(fmt.Sprintf("%+v", ev), secret) || strings.Contains(fmt.Sprintf("%#v", ev), secret) {
		t.Fatal("credential evidence leaked")
	}
}

func TestSnapshotIdentityCannotUseDisplayName(t *testing.T) {
	cases := map[string]func(*api.Inventory, *TargetEvidence){
		"unverified API":          func(v *api.Inventory, e *TargetEvidence) { e.APIAuthenticated = false },
		"wrong version":           func(v *api.Inventory, e *TargetEvidence) { v.Resources[0]["version"] = "7.24.5" },
		"wrong device":            func(v *api.Inventory, e *TargetEvidence) { v.Licenses[0]["system-id"] = "other" },
		"display name":            func(v *api.Inventory, e *TargetEvidence) { e.APIIdentitySource = "identity" },
		"missing stable identity": func(v *api.Inventory, e *TargetEvidence) { v.Licenses = nil },
		"SSH unverified values":   func(v *api.Inventory, e *TargetEvidence) { e.SSHDeviceID = "unverified" },
		"SSH mismatch":            func(v *api.Inventory, e *TargetEvidence) { e.SSHAuthenticated = true; e.SSHDeviceID = "other" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			_, inv, known, e := inspectFixture(t)
			mutate(&inv, &e)
			if _, err := buildSnapshot(inv, known, e, matchedCredential); err == nil {
				t.Fatal("invalid target evidence accepted")
			}
		})
	}
}

func TestObservedDigestCanonicalAndSecretFree(t *testing.T) {
	_, inv, _, _ := inspectFixture(t)
	row := inv.Tables[0]
	a, err := ObservedAttributesChecksum(api.RoutingTable, row)
	if err != nil {
		t.Fatal(err)
	}
	row["fib"] = "no"
	row["used"] = "false"
	b, err := ObservedAttributesChecksum(api.RoutingTable, row)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("equivalent bool or volatile used flag changed digest")
	}
	row["fib"] = "yes"
	c, _ := ObservedAttributesChecksum(api.RoutingTable, row)
	if a == c {
		t.Fatal("controlled value not bound")
	}
	row["tcp-md5-key"] = "weak-secret"
	if _, err := ObservedAttributesChecksum(api.RoutingTable, row); err == nil || strings.Contains(err.Error(), "weak-secret") {
		t.Fatal("secret accepted in ordinary digest or diagnostic")
	}
}

func TestSnapshotReferencesUseMenuSpecificIDs(t *testing.T) {
	_, inv, known, e := inspectFixture(t)
	inv.Tables[0][".id"] = "*A"
	inv.Instances[0][".id"] = "*B"
	// *A is a different menu's instance; it is not the owned table's identity.
	inv.Instances = append(inv.Instances, api.Record{".id": "*A", "name": "outside", "routing-table": "main", "vrf": "main"})
	foreign := api.Record{".id": "*A", "name": "outside", "instance": "*A", "templates": "", "routing-table": "main", "vrf": "main", "input.filter": "", "input.filter-nlri": "", "output.filter-chain": "", "output.filter-select": ""}
	inv.Connections = append(inv.Connections, foreign)
	if _, err := buildSnapshot(inv, known, e, matchedCredential); err != nil {
		t.Fatalf("unrelated menu-local IDs aliased: %v", err)
	}
	foreign["instance"] = "*b"
	s, err := buildSnapshot(inv, known, e, matchedCredential)
	if err == nil || !s.ForeignReferences {
		t.Fatal("actual managed-instance reference by ID missed")
	}
}

func TestSnapshotManifestCannotSubstituteGenerationHash(t *testing.T) {
	for name, mutate := range map[string]func(*KnownState){
		"checksum": func(k *KnownState) { k.Generations[0].PolicyChecksum = strings.Repeat("a", 64) },
		"policy":   func(k *KnownState) { k.Generations[0].Desired.Peers[0].AllowedPrefixes = []string{"203.0.113.0/25"} },
		"peer":     func(k *KnownState) { k.Peers[0].ID = "other-peer" },
		"active":   func(k *KnownState) { k.ActiveGeneration = strings.Repeat("a", 16) },
	} {
		t.Run(name, func(t *testing.T) {
			_, inv, known, e := inspectFixture(t)
			mutate(&known)
			if _, err := buildSnapshot(inv, known, e, matchedCredential); err == nil {
				t.Fatal("manifest mapping mismatch accepted")
			}
		})
	}
}
