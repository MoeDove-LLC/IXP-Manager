package routeros

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/moedove/ixp-manager/internal/routing"
)

var identifier = regexp.MustCompile("^[A-Za-z0-9_-]{1,100}$")

// DeviceID is bounded opaque data, never a path or command fragment. CHR
// system IDs use base64 characters, including leading '+' and '/'.
var deviceIdentifier = regexp.MustCompile("^[A-Za-z0-9_.:+/-]{1,128}$")
var digestPattern = regexp.MustCompile("^[0-9a-f]{64}$")
var generationPattern = regexp.MustCompile("^[0-9a-f]{16}$")
var internalID = regexp.MustCompile("^\\*[0-9A-Fa-f]{1,16}$")
var versionPattern = regexp.MustCompile("^7\\.([0-9]{1,3})(?:\\.([0-9]{1,3}))?$")
var revisionPattern = regexp.MustCompile("^[A-Za-z0-9_-]{22}$")

// OwnerName identifies the dedicated namespace; a name alone never proves ownership.
func OwnerName(routerID string) string { return "md_" + routing.Checksum(routerID)[:16] }
func hash(v any) string                { data, _ := json.Marshal(v); return routing.Checksum(string(data)) }
func inputRoot(owner string) string    { return owner + "_root_in" }
func outputRoot(owner string) string   { return owner + "_root_out" }
func generationChain(owner, generation, suffix string) string {
	return owner + "_g" + generation + "_" + suffix
}
func connectionName(owner, peerID string) string { return owner + "_" + routing.Checksum(peerID)[:12] }

func validateTarget(t Target) error {
	match := versionPattern.FindStringSubmatch(t.Version)
	if !identifier.MatchString(t.RouterID) || !deviceIdentifier.MatchString(t.DeviceID) || !digestPattern.MatchString(t.TransportIdentitySHA256) || match == nil {
		return errors.New("target requires an inventory ID, exact stable RouterOS version, device identity and SHA256 transport pin")
	}
	minor, _ := strconv.Atoi(match[1])
	if minor < 20 {
		return errors.New("the typed BGP instance plan requires RouterOS 7.20 or newer; native support remains unverified")
	}
	return nil
}

func normalizeDesired(router routing.Router, peers []routing.Peer) (Desired, error) {
	if !identifier.MatchString(router.ID) {
		return Desired{}, errors.New("router inventory ID is invalid")
	}
	if len(peers) > 10000 {
		return Desired{}, errors.New("too many peers")
	}
	seen := map[string]bool{}
	for _, p := range peers {
		if !identifier.MatchString(p.ID) || seen[p.ID] {
			return Desired{}, errors.New("peer IDs must be safe and unique")
		}
		seen[p.ID] = true
	}
	// Common IP/ASN, exact allowlist, origin-pair and credential validation.
	// This is structural validation, never a native parser or device check.
	if _, err := routing.RenderFor("routeros7", router, peers); err != nil {
		return Desired{}, err
	}
	canonical := func(s string) string {
		if s == "" {
			return ""
		}
		a, _ := netip.ParseAddr(s)
		return a.String()
	}
	d := Desired{RouterID: canonical(router.RouterID), ASN: router.ASN, LocalIPv4: canonical(router.LocalIPv4), LocalIPv6: canonical(router.LocalIPv6), Peers: []Peer{}}
	for _, p := range peers {
		if !p.Enabled {
			continue
		}
		n := Peer{ID: p.ID, ASN: p.ASN, Address: canonical(p.Address), MaxPrefixes: p.MaxPrefixes, Authentication: Authentication{Enabled: p.Password != "", Revision: p.AuthenticationRevision}, AllowedPrefixes: []string{}}
		prefixes := map[string]bool{}
		for _, value := range p.AllowedPrefixes {
			v, _ := netip.ParsePrefix(value)
			prefixes[v.String()] = true
		}
		for v := range prefixes {
			n.AllowedPrefixes = append(n.AllowedPrefixes, v)
		}
		sort.Strings(n.AllowedPrefixes)
		pairs := map[string]routing.PrefixOrigin{}
		for _, v := range p.AllowedRoutes {
			key := v.Prefix + "/" + strconv.FormatUint(uint64(v.OriginASN), 10)
			pairs[key] = v
		}
		for _, v := range pairs {
			n.AllowedRoutes = append(n.AllowedRoutes, v)
		}
		sort.Slice(n.AllowedRoutes, func(i, j int) bool {
			a, b := n.AllowedRoutes[i], n.AllowedRoutes[j]
			if a.Prefix != b.Prefix {
				return a.Prefix < b.Prefix
			}
			return a.OriginASN < b.OriginASN
		})
		d.Peers = append(d.Peers, n)
	}
	sort.Slice(d.Peers, func(i, j int) bool { return d.Peers[i].ID < d.Peers[j].ID })
	return d, nil
}

func connection(owner string, d Desired, p Peer) Connection {
	family, local := "ip", d.LocalIPv4
	if netip.MustParseAddr(p.Address).Is6() {
		family, local = "ipv6", d.LocalIPv6
	}
	return Connection{PeerID: p.ID, Name: connectionName(owner, p.ID), Instance: owner, Family: family, LocalAddress: local, RemoteAddress: p.Address, RemoteASN: p.ASN, LocalASN: d.ASN, MaxPrefixes: p.MaxPrefixes, InputChain: inputRoot(owner), OutputChain: outputRoot(owner), Authentication: p.Authentication}
}

func normalizeSnapshot(target Target, owner string, base Snapshot, options PlanOptions) (Snapshot, error) {
	if base.Target != target || base.Owner != owner {
		return Snapshot{}, errors.New("base state belongs to a different target or owner")
	}
	if base.ForeignReferences || base.UnexpectedObjects {
		return Snapshot{}, errors.New("foreign references or unrecognized managed objects require manual reconciliation")
	}
	if len(base.Connections) > 10000 || len(base.Generations) > 64 {
		return Snapshot{}, errors.New("base state exceeds bounded inventory limits")
	}
	// Clone before sorting: do not mutate caller state.
	data, _ := json.Marshal(base)
	if len(data) > MaxPlanBytes {
		return Snapshot{}, errors.New("base state too large")
	}
	_ = json.Unmarshal(data, &base)
	if !base.Managed {
		if !options.Bootstrap || !base.NamespaceEmpty || base.Table != (ObjectRef{}) || base.Instance != (ObjectRef{}) || base.InputEntry != (ObjectRef{}) || base.OutputEntry != (ObjectRef{}) || len(base.Connections) != 0 || len(base.Generations) != 0 || base.ActiveGeneration != "" || base.TableFIB || base.ASN != 0 || base.RouterID != "" {
			return Snapshot{}, errors.New("initial ownership requires explicit bootstrap and an independently verified empty namespace")
		}
		return base, nil
	}
	if options.Bootstrap || base.NamespaceEmpty || base.TableFIB {
		return Snapshot{}, errors.New("managed state must retain a dedicated non-FIB table and cannot be bootstrapped again")
	}
	checkRef := func(ref ObjectRef, name, chain, comment string) bool {
		return internalID.MatchString(ref.ID) && ref.Name == name && ref.Chain == chain && ref.Comment == comment && digestPattern.MatchString(ref.StateChecksum) && !ref.Invalid
	}
	if !checkRef(base.Table, owner, "", "") || !checkRef(base.Instance, owner, "", "") || !checkRef(base.InputEntry, "", inputRoot(owner), owner+":root:in") || !checkRef(base.OutputEntry, "", outputRoot(owner), owner+":root:out") {
		return Snapshot{}, errors.New("managed table, instance and stable root identities do not match")
	}
	if base.Table.Disabled || base.Instance.Disabled || base.InputEntry.Disabled || base.OutputEntry.Disabled ||
		base.InputEntry.RuleSHA256 != routing.Checksum("jump "+generationChain(owner, base.ActiveGeneration, "in")+";") ||
		base.OutputEntry.RuleSHA256 != routing.Checksum("jump "+generationChain(owner, base.ActiveGeneration, "out")+";") {
		return Snapshot{}, errors.New("observed roots must be enabled valid jumps to the verified active generation")
	}
	if !validASN(base.ASN) {
		return Snapshot{}, errors.New("base local ASN is invalid")
	}
	rid, err := netip.ParseAddr(base.RouterID)
	if err != nil || !rid.Is4() || !rid.IsGlobalUnicast() {
		return Snapshot{}, errors.New("base router-id is invalid")
	}
	ids := map[string]bool{base.InputEntry.ID: true, base.OutputEntry.ID: true}
	names := map[string]bool{}
	totalRules := 0
	// RouterOS IDs are per menu. Only the two filter entries must differ.
	if base.InputEntry.ID == base.OutputEntry.ID {
		return Snapshot{}, errors.New("root entries cannot share an object ID")
	}
	active := false
	for _, g := range base.Generations {
		if !generationPattern.MatchString(g.Generation) || !digestPattern.MatchString(g.PolicyChecksum) || g.Generation != g.PolicyChecksum[:16] || names[g.Generation] {
			return Snapshot{}, errors.New("invalid or duplicate generation identity")
		}
		names[g.Generation] = true
		if g.Generation == base.ActiveGeneration {
			active = true
		}
		if len(g.Rules) == 0 {
			return Snapshot{}, errors.New("generation has no verified rules")
		}
		totalRules += len(g.Rules)
		if totalRules > MaxPrepareOperations {
			return Snapshot{}, errors.New("too many retained generation rules")
		}
		for _, ref := range g.Rules {
			commentPrefix := owner + ":g:" + g.Generation + ":"
			suffix := strings.TrimPrefix(ref.Comment, commentPrefix)
			ruleNumber, numberErr := strconv.Atoi(suffix)
			if !internalID.MatchString(ref.ID) || ids[ref.ID] || ref.Name != "" || !digestPattern.MatchString(ref.StateChecksum) || !digestPattern.MatchString(ref.RuleSHA256) || ref.Disabled || ref.Invalid || !strings.HasPrefix(ref.Comment, commentPrefix) || len(suffix) != 6 || numberErr != nil || ruleNumber < 1 || ruleNumber > MaxPrepareOperations || (ref.Chain != generationChain(owner, g.Generation, "in") && ref.Chain != generationChain(owner, g.Generation, "out") && ref.Chain != generationChain(owner, g.Generation, "src")) {
				return Snapshot{}, errors.New("generation contains an unowned or duplicate rule")
			}
			ids[ref.ID] = true
		}
	}
	if !active {
		return Snapshot{}, errors.New("base active generation is not present")
	}
	ids = map[string]bool{}
	names = map[string]bool{}
	for _, c := range base.Connections {
		cfg := c.Config
		if !identifier.MatchString(cfg.PeerID) || names[cfg.PeerID] || !c.AuthenticationVerified || !checkRef(c.Ref, connectionName(owner, cfg.PeerID), "", "") || c.Ref.Disabled != c.Disabled || ids[c.Ref.ID] || cfg.Name != c.Ref.Name || cfg.Instance != owner || cfg.InputChain != inputRoot(owner) || cfg.OutputChain != outputRoot(owner) {
			return Snapshot{}, errors.New("connection ownership or credential state is not verified")
		}
		if err := validateConnection(cfg); err != nil {
			return Snapshot{}, err
		}
		if !c.Disabled && cfg.LocalASN != base.ASN {
			return Snapshot{}, errors.New("enabled connection local ASN differs from its instance")
		}
		ids[c.Ref.ID] = true
		names[cfg.PeerID] = true
	}
	sort.Slice(base.Connections, func(i, j int) bool { return base.Connections[i].Config.PeerID < base.Connections[j].Config.PeerID })
	sort.Slice(base.Generations, func(i, j int) bool { return base.Generations[i].Generation < base.Generations[j].Generation })
	return base, nil
}

func validASN(n uint32) bool { return n != 0 && n != 23456 && n != 65535 && n != 4294967295 }
func validateConnection(c Connection) error {
	local, e1 := netip.ParseAddr(c.LocalAddress)
	remote, e2 := netip.ParseAddr(c.RemoteAddress)
	if e1 != nil || e2 != nil || !local.IsGlobalUnicast() || !remote.IsGlobalUnicast() || local.Is4In6() || remote.Is4In6() || local.Zone() != "" || remote.Zone() != "" || local.Is4() != remote.Is4() || local == remote || ((c.Family == "ip") != local.Is4()) || (c.Family != "ip" && c.Family != "ipv6") || !validASN(c.RemoteASN) || !validASN(c.LocalASN) || c.LocalASN == c.RemoteASN || c.MaxPrefixes < 1 || c.MaxPrefixes > 1000000 {
		return errors.New("base connection has invalid typed attributes")
	}
	if (c.Authentication.Enabled && c.Authentication.Revision == "") || (c.Authentication.Revision != "" && !revisionPattern.MatchString(c.Authentication.Revision)) {
		return errors.New("base authentication revision is invalid")
	}
	return nil
}

// BuildPlan creates a reviewable structural plan, not an authorization to run it.
// current must come from an authenticated, schema-strict device reader. Flags
// claiming namespace emptiness or ownership supplied by a browser are not proof.
func BuildPlan(target Target, router routing.Router, peers []routing.Peer, current Snapshot, options PlanOptions) (Plan, error) {
	if err := validateTarget(target); err != nil {
		return Plan{}, err
	}
	if target.RouterID != router.ID {
		return Plan{}, errors.New("target/router inventory identity mismatch")
	}
	d, err := normalizeDesired(router, peers)
	if err != nil {
		return Plan{}, err
	}
	return build(target, d, current, options)
}

func build(target Target, d Desired, current Snapshot, options PlanOptions) (Plan, error) {
	if options.ActionLimit == 0 {
		options.ActionLimit = DefaultActionLimit
	}
	if options.ActionLimit < 1 || options.ActionLimit > MaximumActionLimit {
		return Plan{}, errors.New("activation action estimate must be limited to at most 80")
	}
	owner := OwnerName(target.RouterID)
	base, err := normalizeSnapshot(target, owner, current, options)
	if err != nil {
		return Plan{}, err
	}
	identityChange := base.Managed && (base.ASN != d.ASN || base.RouterID != d.RouterID)
	if identityChange && !options.AllowIdentityChange {
		return Plan{}, errors.New("changing local ASN or router-id requires an explicit reviewed identity change")
	}
	p := Plan{Version: SchemaVersion, Target: target, Owner: owner, Desired: d, Base: base, Options: options, Operations: []Operation{}, RequiresSafeMode: true, RequiresExclusiveControl: true, RequiresTargetValidation: true}
	p.PolicyChecksum = hash(struct {
		Target  Target
		Desired Desired
	}{target, d})
	p.Generation = p.PolicyChecksum[:16]
	p.BaseChecksum = hash(base)
	p.Budget = Budget{Limit: options.ActionLimit, Basis: "Conservative activation property/operation estimate; RouterOS history accounting and concurrent changes require exact-version lab verification. Prepare and cleanup are excluded and must run outside the activation Safe Mode transaction."}
	add := func(phase Phase, kind OperationKind, ref ObjectRef, rule *Rule, conn *Connection, peerID string, cost int) {
		p.Operations = append(p.Operations, Operation{ID: fmt.Sprintf("op-%06d", len(p.Operations)+1), Phase: phase, Kind: kind, Object: ref, Rule: rule, Connection: conn, PeerID: peerID, EstimatedActions: cost})
		if phase == Activate {
			p.Budget.EstimatedActions += cost
		}
	}
	inRef, outRef := base.InputEntry, base.OutputEntry
	if !base.Managed {
		add(Prepare, AddTable, ObjectRef{Name: owner}, nil, nil, "", 1)
		add(Prepare, AddInstance, ObjectRef{Name: owner}, nil, nil, "", 4)
		inRef = ObjectRef{Chain: inputRoot(owner), Comment: owner + ":root:in"}
		outRef = ObjectRef{Chain: outputRoot(owner), Comment: owner + ":root:out"}
		for _, ref := range []ObjectRef{inRef, outRef} {
			r := Rule{Kind: RejectRule, Chain: ref.Chain, Comment: ref.Comment}
			add(Prepare, AddRule, ref, &r, nil, "", 1)
		}
	}
	genExists := false
	for _, g := range base.Generations {
		if g.Generation == p.Generation {
			if g.PolicyChecksum != p.PolicyChecksum {
				return Plan{}, errors.New("generation checksum collision")
			}
			expectedRules := generationRules(owner, p.Generation, d)
			if len(g.Rules) != len(expectedRules) {
				return Plan{}, errors.New("existing generation rule count differs from the typed policy")
			}
			peerMap := map[string]Peer{}
			for _, peer := range d.Peers {
				peerMap[peer.ID] = peer
			}
			for i, rule := range expectedRules {
				body, err := compileRule(rule, peerMap)
				if err != nil {
					return Plan{}, err
				}
				actual := g.Rules[i]
				if actual.Chain != rule.Chain || actual.Comment != rule.Comment || actual.RuleSHA256 != routing.Checksum(body) {
					return Plan{}, errors.New("existing generation content or order differs from the typed policy")
				}
			}
			genExists = true
		}
	}
	if !genExists {
		rules := generationRules(owner, p.Generation, d)
		for i := range rules {
			rule := rules[i]
			add(Prepare, AddRule, ObjectRef{Chain: rule.Chain, Comment: rule.Comment}, &rule, nil, "", 1)
		}
	}
	old := map[string]ConnectionState{}
	for _, c := range base.Connections {
		old[c.Config.PeerID] = c
	}
	wanted := map[string]Connection{}
	refs := map[string]ObjectRef{}
	changed := map[string]bool{}
	for _, peer := range d.Peers {
		c := connection(owner, d, peer)
		wanted[peer.ID] = c
		before, exists := old[peer.ID]
		refs[peer.ID] = before.Ref
		if !exists {
			refs[peer.ID] = ObjectRef{Name: c.Name}
			add(Prepare, AddDisabledConnection, refs[peer.ID], nil, &c, peer.ID, 20)
			add(Prepare, UnsetUnusedFamilyLimit, refs[peer.ID], nil, &c, peer.ID, 1)
			// An absent getter value is not proof of missing authentication.
			// Initialize every new disabled connection explicitly, including "".
			add(Prepare, SetAuthentication, refs[peer.ID], nil, &c, peer.ID, 1)
		}
		changed[peer.ID] = !exists || before.Disabled || identityChange || before.Config != c
	}
	needsActivation := !base.Managed || base.ActiveGeneration != p.Generation || identityChange
	for id, c := range old {
		if _, ok := wanted[id]; !ok && !c.Disabled {
			needsActivation = true
		}
	}
	for _, v := range changed {
		needsActivation = needsActivation || v
	}
	if needsActivation {
		reject := Rule{Kind: RejectRule, Chain: outRef.Chain, Comment: outRef.Comment}
		add(Activate, SetEntry, outRef, &reject, nil, "", 1)
		for _, before := range base.Connections {
			_, exists := wanted[before.Config.PeerID]
			if !before.Disabled && (!exists || changed[before.Config.PeerID]) {
				add(Activate, DisableConnection, before.Ref, nil, nil, before.Config.PeerID, 1)
			}
		}
		// Pin retained disabled connections to their already verified old local
		// ASN before changing the instance. They await the authorized cleanup.
		if identityChange {
			for _, before := range base.Connections {
				if _, exists := wanted[before.Config.PeerID]; !exists {
					c := before.Config
					add(Activate, SetConnection, before.Ref, nil, &c, c.PeerID, 20)
					add(Activate, UnsetUnusedFamilyLimit, before.Ref, nil, &c, c.PeerID, 1)
				}
			}
		}
		for _, peer := range d.Peers {
			c := wanted[peer.ID]
			before, exists := old[peer.ID]
			plainBefore, plainAfter := before.Config, c
			plainBefore.Authentication = Authentication{}
			plainAfter.Authentication = Authentication{}
			if exists && plainBefore != plainAfter {
				add(Activate, SetConnection, refs[peer.ID], nil, &c, peer.ID, 20)
				add(Activate, UnsetUnusedFamilyLimit, refs[peer.ID], nil, &c, peer.ID, 1)
			}
			if exists && before.Config.Authentication != c.Authentication {
				add(Activate, SetAuthentication, refs[peer.ID], nil, &c, peer.ID, 1)
			}
		}
		// Existing peers have explicit desired ASNs now, so an instance change
		// cannot silently alter another object's inherited ASN during readback.
		if identityChange {
			add(Activate, SetInstance, base.Instance, nil, nil, "", 2)
		}
		for _, pair := range []struct {
			ref    ObjectRef
			suffix string
		}{{inRef, "in"}, {outRef, "out"}} {
			r := Rule{Kind: JumpRule, Chain: pair.ref.Chain, Comment: pair.ref.Comment, JumpTarget: generationChain(owner, p.Generation, pair.suffix)}
			add(Activate, SetEntry, pair.ref, &r, nil, "", 1)
		}
		for _, peer := range d.Peers {
			if changed[peer.ID] {
				add(Activate, EnableConnection, refs[peer.ID], nil, nil, peer.ID, 1)
			}
		}
	}
	for _, g := range base.Generations {
		if g.Generation != p.Generation {
			for _, ref := range g.Rules {
				add(Cleanup, RemoveRule, ref, nil, nil, "", 1)
			}
		}
	}
	for _, c := range base.Connections {
		if _, ok := wanted[c.Config.PeerID]; !ok {
			add(Cleanup, RemoveConnection, c.Ref, nil, nil, c.Config.PeerID, 1)
		}
	}
	if p.Budget.EstimatedActions > p.Budget.Limit {
		return Plan{}, errors.New("activation exceeds the conservative Safe Mode action budget; reduce the reviewed change, never silently split activation")
	}
	if len(p.Operations) > MaxPrepareOperations+20000 {
		return Plan{}, errors.New("plan contains too many operations")
	}
	p.Checksum = hash(p)
	encoded, _ := json.Marshal(p)
	if len(encoded) > MaxPlanBytes {
		return Plan{}, errors.New("plan exceeds the size limit")
	}
	return p, nil
}

func generationRules(owner, gen string, d Desired) []Rule {
	rules := []Rule{}
	appendRule := func(r Rule) {
		r.Comment = fmt.Sprintf("%s:g:%s:%06d", owner, gen, len(rules)+1)
		rules = append(rules, r)
	}
	for _, peer := range d.Peers {
		pairs := peer.AllowedRoutes
		if len(pairs) == 0 {
			for _, prefix := range peer.AllowedPrefixes {
				pairs = append(pairs, routing.PrefixOrigin{Prefix: prefix})
			}
		}
		for _, pair := range pairs {
			for _, kind := range []RuleKind{ImportRule, ExportRule} {
				suffix := "in"
				if kind == ExportRule {
					suffix = "src"
				}
				appendRule(Rule{Kind: kind, Chain: generationChain(owner, gen, suffix), PeerID: peer.ID, Prefix: pair.Prefix, OriginASN: pair.OriginASN})
			}
		}
		appendRule(Rule{Kind: RecipientRule, Chain: generationChain(owner, gen, "out"), PeerID: peer.ID, JumpTarget: generationChain(owner, gen, "src")})
	}
	for _, suffix := range []string{"in", "out", "src"} {
		appendRule(Rule{Kind: RejectRule, Chain: generationChain(owner, gen, suffix)})
	}
	return rules
}

// ValidatePlan regenerates the complete closed operation list. Recomputing the
// checksum of an edited arbitrary operation cannot turn it into a valid plan.
func ValidatePlan(p Plan) error {
	if err := validateTarget(p.Target); err != nil {
		return err
	}
	data, err := json.Marshal(p)
	if err != nil || len(data) > MaxPlanBytes {
		return errors.New("invalid or oversized plan")
	}
	router := routing.Router{ID: p.Target.RouterID, ASN: p.Desired.ASN, RouterID: p.Desired.RouterID, LocalIPv4: p.Desired.LocalIPv4, LocalIPv6: p.Desired.LocalIPv6}
	peers := make([]routing.Peer, 0, len(p.Desired.Peers))
	for _, v := range p.Desired.Peers {
		password := ""
		if v.Authentication.Enabled {
			password = "structuralplaceholder"
		}
		peers = append(peers, routing.Peer{ID: v.ID, ASN: v.ASN, Address: v.Address, Enabled: true, MaxPrefixes: v.MaxPrefixes, AllowedPrefixes: v.AllowedPrefixes, AllowedRoutes: v.AllowedRoutes, AuthenticationRevision: v.Authentication.Revision, Password: password})
	}
	d, err := normalizeDesired(router, peers)
	if err != nil {
		return err
	}
	expected, err := build(p.Target, d, p.Base, p.Options)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(expected, p) {
		return errors.New("plan is not the canonical typed plan for its target, desired state and verified base")
	}
	return nil
}
