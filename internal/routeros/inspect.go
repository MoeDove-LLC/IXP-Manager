package routeros

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routing"
)

// KnownState is private local manifest data, not device comments or browser input.
// It maps public IDs/revisions to the complete locally known generation policy.
type KnownState struct {
	Target           Target
	ActiveGeneration string
	Generations      []KnownGeneration
	Peers            []KnownPeer
}
type KnownGeneration struct {
	Target         Target
	Desired        Desired
	PolicyChecksum string
}
type KnownPeer struct {
	ID             string
	Authentication Authentication
}

// CredentialEvidence contains only the result of the dedicated sensitive API
// getter and locally held expected values. Never put either map in ordinary JSON.
type CredentialEvidence struct {
	Observed map[string]api.Credential
	Expected Credentials
}

func (CredentialEvidence) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "RouterOS credential evidence (redacted)")
}
func (CredentialEvidence) MarshalJSON() ([]byte, error) {
	return []byte(`{"credentials":"redacted"}`), nil
}

type IdentitySource string

const (
	LicenseSystemID   IdentitySource = "license-system-id"
	LicenseSoftwareID IdentitySource = "license-software-id"
	RouterBoardSerial IdentitySource = "routerboard-serial"
)

// TargetEvidence is supplied by trusted local transport adapters, never a remote
// assertion. SSH evidence may be absent; that leaves cross-channel proof false.
// This is not Safe Mode ownership, exclusivity or a native-validation grant.
type TargetEvidence struct {
	APIAuthenticated            bool
	APITransportIdentitySHA256  string
	APIIdentitySource           IdentitySource
	SSHAuthenticated            bool
	SSHTransportIdentitySHA256  string
	SSHIdentitySource           IdentitySource
	SSHDeviceID                 string
	SSHVersion                  string
	Properties                  []VersionPropertyEvidence
	FilterRuleDisabledSelection *FilterRuleDisabledSelection
}

// FilterRuleDisabledSelection is the result of two completed fixed predicates
// over the current authenticated connection's complete filter-rule menu. It
// is distinct from explicit property getter evidence and grants no ownership.
// FalseIDs is the verified union of explicit false and missing disabled flag;
// TrueIDs is the explicit true selection. Neither list alone proves a value.
type FilterRuleDisabledSelection struct {
	FalseCompleted bool
	TrueCompleted  bool
	FalseIDs       []string
	TrueIDs        []string
}

// VersionPropertyEvidence must come from a successful explicit getter on the
// current authenticated API connection. Completed=false is not unset evidence.
// Sensitive credentials have their own opaque channel and are forbidden here.
type VersionPropertyEvidence struct {
	Resource  api.Resource
	ID        string
	Field     string
	Completed bool
	Property  api.PropertyValue
}

type InspectError struct {
	Code     string
	Resource api.Resource
	Field    string
}

func (e *InspectError) Error() string {
	return "RouterOS inspection " + e.Code + " (" + string(e.Resource) + "/" + e.Field + ")"
}
func inspectError(code string, r api.Resource, field string) error {
	return &InspectError{Code: code, Resource: r, Field: field}
}

// Each map is a closed read schema. Missing and explicitly empty are distinct.
// Volatile fields are checked for shape but not hashed as configuration.
var inspectFields = map[api.Resource][]string{
	api.RoutingTable:  {".id", "name", "fib", "disabled", "dynamic", "invalid", "used", "comment"},
	api.BGPInstance:   {".id", "name", "as", "router-id", "routing-table", "vrf", "disabled", "inactive", "comment"},
	api.BGPConnection: {".id", "name", "instance", "as", "disabled", "dynamic", "inactive", "templates", "afi", "local.address", "local.role", "remote.address", "remote.as", "routing-table", "vrf", "connect", "listen", "nexthop-choice", "multihop", "input.filter", "input.filter-nlri", "input.limit-process-routes-ipv4", "input.limit-process-routes-ipv6", "output.filter-chain", "output.filter-select", "output.network", "output.redistribute", "output.default-originate", "output.default-prepend", "output.remove-private-as", "output.as-override", "comment"},
	api.FilterRule:    {".id", "chain", "rule", "disabled", "inactive", "comment"},
}
var inspectBoolean = map[string]bool{"fib": true, "disabled": true, "dynamic": true, "invalid": true, "inactive": true, "used": true, "connect": true, "listen": true, "multihop": true, "output.remove-private-as": true, "output.as-override": true}

func boolObserved(row api.Record, key string) (bool, bool) {
	v, ok := row[key]
	if !ok {
		return false, false
	}
	switch v {
	case "yes", "true":
		return true, true
	case "no", "false":
		return false, true
	}
	return false, false
}
func required(row api.Record, r api.Resource, keys ...string) error {
	for _, key := range keys {
		if _, ok := row[key]; !ok {
			return inspectError("unknown-property", r, key)
		}
	}
	return nil
}
func flags(row api.Record, r api.Resource, keys ...string) error {
	for _, key := range keys {
		if _, ok := boolObserved(row, key); !ok {
			return inspectError("unknown-flag", r, key)
		}
	}
	return nil
}
func falseFlags(row api.Record, r api.Resource, keys ...string) error {
	if err := flags(row, r, keys...); err != nil {
		return err
	}
	for _, key := range keys {
		if b, _ := boolObserved(row, key); b {
			return inspectError("unsupported-flag", r, key)
		}
	}
	return nil
}
func closedRows(rows []api.Record, r api.Resource) ([]api.Record, error) {
	allowed := map[string]bool{}
	for _, key := range inspectFields[r] {
		allowed[key] = true
	}
	out := make([]api.Record, 0, len(rows))
	ids := map[string]bool{}
	names := map[string]bool{}
	for _, raw := range rows {
		if len(raw) > 64 {
			return nil, inspectError("row-limit", r, "attributes")
		}
		row := api.Record{}
		for key, value := range raw {
			if !allowed[key] {
				return nil, inspectError("unrecognized-property", r, "attributes")
			}
			if len(value) > 16384 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
				return nil, inspectError("invalid-property", r, key)
			}
			if inspectBoolean[key] {
				b, ok := boolObserved(raw, key)
				if !ok {
					return nil, inspectError("unknown-flag", r, key)
				}
				value = strconv.FormatBool(b)
			}
			row[key] = value
		}
		row[".id"] = strings.ToUpper(row[".id"])
		if !internalID.MatchString(row[".id"]) || ids[row[".id"]] {
			return nil, inspectError("invalid-or-duplicate-id", r, ".id")
		}
		ids[row[".id"]] = true
		if r != api.FilterRule {
			if row["name"] == "" || len(row["name"]) > 128 || names[row["name"]] {
				return nil, inspectError("invalid-or-duplicate-name", r, "name")
			}
			names[row["name"]] = true
		}
		if r == api.FilterRule {
			if err := required(row, r, "chain", "rule", "comment"); err != nil {
				return nil, err
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// ObservedAttributesChecksum includes explicit empty properties and canonical
// booleans. It never accepts sensitive attributes, unknown attributes or a
// missing value masquerading as an explicit default.
func ObservedAttributesChecksum(resource api.Resource, row api.Record) (string, error) {
	if _, ok := inspectFields[resource]; !ok {
		return "", errors.New("unsupported observed resource")
	}
	rows, err := closedRows([]api.Record{row}, resource)
	if err != nil {
		return "", err
	}
	values := rows[0]
	delete(values, ".id")
	delete(values, "used")
	if resource != api.FilterRule {
		delete(values, "inactive")
	}
	return hash(struct {
		Resource   api.Resource
		Attributes api.Record
	}{resource, values}), nil
}
func objectRef(resource api.Resource, row api.Record) (ObjectRef, error) {
	digest, err := ObservedAttributesChecksum(resource, row)
	if err != nil {
		return ObjectRef{}, err
	}
	if err := flags(row, resource, "disabled"); err != nil {
		return ObjectRef{}, err
	}
	disabled, _ := boolObserved(row, "disabled")
	ref := ObjectRef{ID: row[".id"], StateChecksum: digest, Disabled: disabled}
	if resource == api.FilterRule {
		if err := flags(row, resource, "inactive"); err != nil {
			return ObjectRef{}, err
		}
		ref.Chain, ref.Comment, ref.RuleSHA256 = row["chain"], row["comment"], routing.Checksum(row["rule"])
		// The exact-tested filter menu exposes inactive, not an invalid getter.
		// This is filter usability only; it is not session/router health.
		ref.Invalid, _ = boolObserved(row, "inactive")
	} else {
		ref.Name, ref.Comment = row["name"], row["comment"]
		if resource == api.RoutingTable {
			if err := flags(row, resource, "invalid"); err != nil {
				return ObjectRef{}, err
			}
			ref.Invalid, _ = boolObserved(row, "invalid")
		}
	}
	return ref, nil
}

func observedIdentity(inv api.Inventory, e TargetEvidence, t Target) (bool, error) {
	if !e.APIAuthenticated || e.APITransportIdentitySHA256 != t.TransportIdentitySHA256 {
		return false, inspectError("unverified-transport", api.SystemResource, "identity")
	}
	if inv.Capabilities[api.SystemResource] != api.Supported || len(inv.Resources) != 1 || !observedVersionMatches(inv.Resources[0]["version"], t.Version) {
		return false, inspectError("target-version-mismatch", api.SystemResource, "version")
	}
	var rows []api.Record
	var field string
	var resource api.Resource
	switch e.APIIdentitySource {
	case LicenseSystemID:
		rows, field, resource = inv.Licenses, "system-id", api.SystemLicense
	case LicenseSoftwareID:
		rows, field, resource = inv.Licenses, "software-id", api.SystemLicense
	case RouterBoardSerial:
		rows, field, resource = inv.RouterBoards, "serial-number", api.RouterBoard
	default:
		return false, inspectError("unknown-device-identity-source", api.SystemResource, "identity")
	}
	if inv.Capabilities[resource] != api.Supported || len(rows) != 1 || rows[0][field] != t.DeviceID {
		return false, inspectError("target-device-mismatch", resource, field)
	}
	if !e.SSHAuthenticated {
		if e.SSHDeviceID != "" || e.SSHVersion != "" || e.SSHTransportIdentitySHA256 != "" || e.SSHIdentitySource != "" {
			return false, inspectError("inconsistent-ssh-evidence", api.SystemResource, "identity")
		}
		return false, nil
	}
	if e.SSHDeviceID != t.DeviceID || !observedVersionMatches(e.SSHVersion, t.Version) || e.SSHIdentitySource != e.APIIdentitySource || e.SSHTransportIdentitySHA256 != t.TransportIdentitySHA256 {
		return false, inspectError("cross-channel-mismatch", api.SystemResource, "identity")
	}
	return true, nil
}

func observedVersionMatches(raw, want string) bool {
	return raw == want || (want == "7.24.6" && raw == "7.24.6 (long-term)")
}

// These absent-return semantics were measured on exactly 7.24.6. Missing print
// fields alone never enter this map. A successful explicit getter with no ret is
// required, and tcp-md5-key deliberately has no normalization rule.
var unsetProperties7246 = map[api.Resource]map[string]string{
	api.RoutingTable: {"fib": "false", "dynamic": "false"},
	api.BGPInstance:  {"vrf": ""},
	api.BGPConnection: {
		"as": "", "vrf": "", "dynamic": "false", "multihop": "false", "input.filter-nlri": "",
		"input.limit-process-routes-ipv4": "", "input.limit-process-routes-ipv6": "",
		"output.filter-select": "", "output.network": "", "output.redistribute": "",
		"output.remove-private-as": "false", "output.as-override": "false",
	},
}

func normalizeVersionProperties(inv api.Inventory, target Target, evidence []VersionPropertyEvidence) (api.Inventory, error) {
	return normalizeVersionPropertiesWithSelection(inv, target, evidence, nil)
}

func normalizeVersionPropertiesWithSelection(inv api.Inventory, target Target, evidence []VersionPropertyEvidence, selection *FilterRuleDisabledSelection) (api.Inventory, error) {
	partition, err := filterRuleDisabledPartition(inv, target, selection)
	if err != nil {
		return api.Inventory{}, err
	}
	if len(evidence) > MaxPrepareOperations*4 {
		return api.Inventory{}, errors.New("property evidence exceeds bounded inspection")
	}
	menus := map[api.Resource][]api.Record{api.RoutingTable: inv.Tables, api.BGPInstance: inv.Instances, api.BGPConnection: inv.Connections, api.FilterRule: inv.FilterRules}
	index := map[api.Resource]map[string]api.Record{}
	count := 0
	for resource, rows := range menus {
		count += len(rows)
		if count > MaxPrepareOperations+20000 {
			return api.Inventory{}, errors.New("property inventory exceeds bounded inspection")
		}
		index[resource] = map[string]api.Record{}
		copies := make([]api.Record, 0, len(rows))
		for _, row := range rows {
			id := strings.ToUpper(row[".id"])
			if !internalID.MatchString(id) || index[resource][id] != nil {
				return api.Inventory{}, inspectError("invalid-or-duplicate-id", resource, ".id")
			}
			copy := api.Record{}
			for k, v := range row {
				copy[k] = v
			}
			copy[".id"] = id
			if resource == api.FilterRule && partition != nil {
				copy["disabled"] = strconv.FormatBool(partition[id])
			}
			index[resource][id] = copy
			copies = append(copies, copy)
		}
		menus[resource] = copies
	}
	seen := map[string]bool{}
	for _, proof := range evidence {
		fields, ok := inspectFields[proof.Resource]
		if !ok {
			return api.Inventory{}, errors.New("unknown property evidence resource")
		}
		allowed := false
		for _, field := range fields {
			if field == proof.Field && field != ".id" {
				allowed = true
			}
		}
		if !allowed {
			return api.Inventory{}, inspectError("unsupported-property-evidence", proof.Resource, "property")
		}
		id := strings.ToUpper(proof.ID)
		row := index[proof.Resource][id]
		key := string(proof.Resource) + "/" + id + "/" + proof.Field
		if row == nil || seen[key] {
			return api.Inventory{}, inspectError("unknown-or-duplicate-property-evidence", proof.Resource, proof.Field)
		}
		seen[key] = true
		if !proof.Completed {
			return api.Inventory{}, inspectError("incomplete-property-getter", proof.Resource, proof.Field)
		}
		value := proof.Property.Value
		presenceFlag := target.Version == "7.24.6" && ((proof.Resource == api.RoutingTable && (proof.Field == "fib" || proof.Field == "dynamic")) || (proof.Resource == api.BGPConnection && proof.Field == "dynamic"))
		_, printPresent := row[proof.Field]
		if presenceFlag && proof.Property.Known {
			flag, known := boolObserved(api.Record{"v": value}, "v")
			if !known || flag != printPresent {
				return api.Inventory{}, inspectError("property-read-conflict", proof.Resource, proof.Field)
			}
		}
		if !proof.Property.Known {
			if value != "" || target.Version != "7.24.6" {
				return api.Inventory{}, inspectError("unknown-property-value", proof.Resource, proof.Field)
			}
			if presenceFlag && printPresent {
				return api.Inventory{}, inspectError("property-read-conflict", proof.Resource, proof.Field)
			}
			var known bool
			value, known = unsetProperties7246[proof.Resource][proof.Field]
			if !known {
				return api.Inventory{}, inspectError("unknown-property-value", proof.Resource, proof.Field)
			}
		}
		if len(value) > 16384 || !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
			return api.Inventory{}, inspectError("invalid-property-evidence", proof.Resource, proof.Field)
		}
		if old, exists := row[proof.Field]; exists && old != "" && old != value {
			oldBool, oldKnown := boolObserved(api.Record{"v": old}, "v")
			newBool, newKnown := boolObserved(api.Record{"v": value}, "v")
			if !inspectBoolean[proof.Field] || !oldKnown || !newKnown || oldBool != newBool {
				return api.Inventory{}, inspectError("property-read-conflict", proof.Resource, proof.Field)
			}
		}
		row[proof.Field] = value
	}
	inv.Tables = menus[api.RoutingTable]
	inv.Instances = menus[api.BGPInstance]
	inv.Connections = menus[api.BGPConnection]
	inv.FilterRules = menus[api.FilterRule]
	return inv, nil
}

type knownPolicy struct {
	generation string
	checksum   string
	rules      []Rule
	bodies     []string
}

func inspectKnown(known KnownState) ([]knownPolicy, map[string]KnownPeer, error) {
	if err := validateTarget(known.Target); err != nil {
		return nil, nil, err
	}
	if len(known.Peers) > 10000 || len(known.Generations) > 64 {
		return nil, nil, errors.New("manifest exceeds inspection bounds")
	}
	data, _ := json.Marshal(known)
	if len(data) > MaxPlanBytes {
		return nil, nil, errors.New("manifest exceeds inspection size")
	}
	peers := map[string]KnownPeer{}
	names := map[string]bool{}
	for _, peer := range known.Peers {
		name := connectionName(OwnerName(known.Target.RouterID), peer.ID)
		if !identifier.MatchString(peer.ID) || peers[peer.ID].ID != "" || names[name] || (peer.Authentication.Enabled && peer.Authentication.Revision == "") || (peer.Authentication.Revision != "" && !revisionPattern.MatchString(peer.Authentication.Revision)) {
			return nil, nil, errors.New("invalid manifest peer identity or authentication revision")
		}
		peers[peer.ID] = peer
		names[name] = true
	}
	policies := []knownPolicy{}
	gens := map[string]bool{}
	total := 0
	for _, g := range known.Generations {
		if err := validateTarget(g.Target); err != nil {
			return nil, nil, err
		}
		if g.Target.RouterID != known.Target.RouterID || g.Target.DeviceID != known.Target.DeviceID || g.Target.TransportIdentitySHA256 != known.Target.TransportIdentitySHA256 {
			return nil, nil, errors.New("manifest generation belongs to a different target")
		}
		router := routing.Router{ID: g.Target.RouterID, ASN: g.Desired.ASN, RouterID: g.Desired.RouterID, LocalIPv4: g.Desired.LocalIPv4, LocalIPv6: g.Desired.LocalIPv6}
		ps := []routing.Peer{}
		for _, p := range g.Desired.Peers {
			password := ""
			if p.Authentication.Enabled {
				password = "structuralplaceholder"
			}
			ps = append(ps, routing.Peer{ID: p.ID, ASN: p.ASN, Address: p.Address, Enabled: true, MaxPrefixes: p.MaxPrefixes, AllowedPrefixes: p.AllowedPrefixes, AllowedRoutes: p.AllowedRoutes, Password: password, AuthenticationRevision: p.Authentication.Revision})
		}
		d, err := normalizeDesired(router, ps)
		if err != nil {
			return nil, nil, err
		}
		checksum := hash(struct {
			Target  Target
			Desired Desired
		}{g.Target, d})
		if checksum != g.PolicyChecksum || hash(d) != hash(g.Desired) {
			return nil, nil, errors.New("manifest generation policy is not canonical")
		}
		gen := checksum[:16]
		if gens[gen] {
			return nil, nil, errors.New("duplicate manifest generation")
		}
		gens[gen] = true
		rules := generationRules(OwnerName(g.Target.RouterID), gen, d)
		total += len(rules)
		if total > MaxPrepareOperations {
			return nil, nil, errors.New("manifest has too many retained rules")
		}
		policy := knownPolicy{generation: gen, checksum: checksum, rules: rules}
		pm := map[string]Peer{}
		for _, p := range d.Peers {
			pm[p.ID] = p
		}
		for _, rule := range rules {
			body, err := compileRule(rule, pm)
			if err != nil {
				return nil, nil, err
			}
			policy.bodies = append(policy.bodies, body)
		}
		policies = append(policies, policy)
	}
	if len(policies) == 0 {
		if known.ActiveGeneration != "" || len(peers) != 0 {
			return nil, nil, errors.New("empty manifest has active generation or peers")
		}
	} else if !gens[known.ActiveGeneration] {
		return nil, nil, errors.New("manifest active generation missing")
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].generation < policies[j].generation })
	return policies, peers, nil
}

func verifyObservedCredential(e CredentialEvidence, connectionID, peerID string, auth Authentication) bool {
	observed, ok := e.Observed[connectionID]
	if !ok || !observed.Known {
		return false
	}
	password := ""
	expected, present := e.Expected[peerID]
	if auth.Enabled {
		if !present || expected.Revision != auth.Revision || expected.Password == "" || routing.ValidateBGPPassword(expected.Password) != nil {
			return false
		}
		password = expected.Password
	} else if present && (expected.Password != "" || expected.Revision != auth.Revision) {
		return false
	}
	return observed.Matches([]byte(password))
}

// BuildSnapshot is a strict pure conversion of fixed API reads and trusted local
// manifest/transport evidence. It never contacts a device or grants activation.
// Optional bool properties need verified explicit getter or narrowly typed
// full-menu selection evidence; absence alone is not false. Unknown credentials return an
// error plus a snapshot whose AuthenticationVerified remains false.
func BuildSnapshot(inventory api.Inventory, known KnownState, credentials CredentialEvidence, target TargetEvidence) (Snapshot, error) {
	return buildSnapshot(inventory, known, target, func(id, peerID string, a Authentication) bool {
		return verifyObservedCredential(credentials, id, peerID, a)
	})
}

func buildSnapshot(inv api.Inventory, known KnownState, e TargetEvidence, credentialMatches func(string, string, Authentication) bool) (Snapshot, error) {
	policies, knownPeers, err := inspectKnown(known)
	if err != nil {
		return Snapshot{}, err
	}
	cross, err := observedIdentity(inv, e, known.Target)
	if err != nil {
		return Snapshot{}, err
	}
	inv, err = normalizeTargetProperties(inv, known.Target, e)
	if err != nil {
		return Snapshot{}, err
	}
	s := Snapshot{Target: known.Target, Owner: OwnerName(known.Target.RouterID), CrossChannelIdentityVerified: cross, Connections: []ConnectionState{}, Generations: []GenerationState{}}
	menus := map[api.Resource][]api.Record{api.RoutingTable: inv.Tables, api.BGPInstance: inv.Instances, api.BGPConnection: inv.Connections, api.FilterRule: inv.FilterRules}
	count, size := 0, 0
	for resource, rows := range menus {
		if inv.Capabilities[resource] != api.Supported {
			return s, inspectError("incomplete-inventory", resource, "capability")
		}
		count += len(rows)
		if count > MaxPrepareOperations+20000 {
			return s, inspectError("inventory-row-limit", resource, "rows")
		}
		for _, row := range rows {
			for key, v := range row {
				size += len(key) + len(v)
				if size > 64<<20 {
					return s, inspectError("inventory-byte-limit", resource, "attributes")
				}
			}
		}
		menus[resource], err = closedRows(rows, resource)
		if err != nil {
			return s, err
		}
	}
	owner := s.Owner
	containsOwner := func(row api.Record) bool {
		for _, v := range row {
			if strings.Contains(v, owner) {
				return true
			}
		}
		return false
	}
	var table, instance api.Record
	for _, row := range menus[api.RoutingTable] {
		if row["name"] == owner {
			table = row
		} else if containsOwner(row) {
			s.UnexpectedObjects = true
			return s, inspectError("unrecognized-owner-object", api.RoutingTable, "name")
		}
	}
	refers := func(value string, objectID string) bool {
		for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '	' }) {
			if objectID != "" && strings.EqualFold(part, objectID) {
				return true
			}
		}
		return strings.Contains(value, owner)
	}
	for _, row := range menus[api.BGPInstance] {
		if err := required(row, api.BGPInstance, "routing-table", "vrf"); err != nil {
			return s, err
		}
		if row["name"] == owner {
			instance = row
		} else if containsOwner(row) || refers(row["routing-table"], table[".id"]) || refers(row["vrf"], "") {
			s.ForeignReferences = true
			return s, inspectError("foreign-reference", api.BGPInstance, "routing-table")
		}
	}
	connectionRows := map[string]api.Record{}
	byName := map[string]KnownPeer{}
	for _, p := range knownPeers {
		byName[connectionName(owner, p.ID)] = p
	}
	for _, row := range menus[api.BGPConnection] {
		if err := required(row, api.BGPConnection, "instance", "templates", "routing-table", "vrf", "input.filter", "input.filter-nlri", "output.filter-chain", "output.filter-select"); err != nil {
			return s, err
		}
		if peer, ok := byName[row["name"]]; ok {
			connectionRows[peer.ID] = row
			continue
		}
		if containsOwner(row) || refers(row["instance"], instance[".id"]) || refers(row["routing-table"], table[".id"]) || refers(row["vrf"], "") || refers(row["input.filter"], "") || refers(row["input.filter-nlri"], "") || refers(row["output.filter-chain"], "") || refers(row["output.filter-select"], "") {
			s.ForeignReferences = true
			return s, inspectError("foreign-reference", api.BGPConnection, "references")
		}
		// A referenced template could hide a managed-chain reference; this fixed
		// inventory has no template content proof, so do not assert closure.
		if row["templates"] != "" {
			return s, inspectError("unresolved-template-reference", api.BGPConnection, "templates")
		}
	}
	expectedRules := map[string]Rule{}
	bodies := map[string]string{}
	chainOrder := map[string][]string{}
	for _, policy := range policies {
		for i, r := range policy.rules {
			expectedRules[r.Comment] = r
			bodies[r.Comment] = policy.bodies[i]
			chainOrder[r.Chain] = append(chainOrder[r.Chain], r.Comment)
		}
	}
	if len(policies) > 0 {
		for _, suffix := range []string{"in", "out"} {
			chain := inputRoot(owner)
			if suffix == "out" {
				chain = outputRoot(owner)
			}
			comment := owner + ":root:" + suffix
			r := Rule{Kind: JumpRule, Chain: chain, Comment: comment, JumpTarget: generationChain(owner, known.ActiveGeneration, suffix)}
			expectedRules[comment] = r
			bodies[comment], _ = compileRule(r, nil)
			chainOrder[chain] = []string{comment}
		}
	}
	ruleRows := map[string]api.Record{}
	actualOrder := map[string][]string{}
	for _, row := range menus[api.FilterRule] {
		comment, chain := row["comment"], row["chain"]
		if expected, ok := expectedRules[comment]; ok {
			if expected.Chain != chain || ruleRows[comment] != nil {
				s.UnexpectedObjects = true
				return s, inspectError("duplicate-or-moved-rule", api.FilterRule, "chain")
			}
			if row["rule"] != bodies[comment] {
				s.UnexpectedObjects = true
				return s, inspectError("rule-body-drift", api.FilterRule, "rule")
			}
			if err := falseFlags(row, api.FilterRule, "disabled", "inactive"); err != nil {
				return s, err
			}
			ruleRows[comment] = row
			actualOrder[chain] = append(actualOrder[chain], comment)
		} else if containsOwner(row) {
			s.ForeignReferences = true
			return s, inspectError("foreign-reference-or-unrecognized-rule", api.FilterRule, "rule")
		}
	}
	for chain, expected := range chainOrder {
		actual := actualOrder[chain]
		if len(actual) != len(expected) {
			return s, inspectError("missing-rule", api.FilterRule, "chain")
		}
		for i := range expected {
			if actual[i] != expected[i] {
				return s, inspectError("rule-order-drift", api.FilterRule, "chain")
			}
		}
	}
	if len(policies) == 0 {
		if table != nil || instance != nil || len(connectionRows) != 0 || len(ruleRows) != 0 {
			s.UnexpectedObjects = true
			return s, inspectError("unowned-existing-namespace", api.RoutingTable, "name")
		}
		s.NamespaceEmpty = true
		return s, nil
	}
	if table == nil || instance == nil {
		return s, inspectError("missing-owned-object", api.RoutingTable, "name")
	}
	if err := required(table, api.RoutingTable, "comment"); err != nil {
		return s, err
	}
	if err := falseFlags(table, api.RoutingTable, "fib", "disabled", "dynamic", "invalid"); err != nil {
		return s, err
	}
	if table["comment"] != "" {
		return s, inspectError("unrecognized-owned-property", api.RoutingTable, "comment")
	}
	if err := required(instance, api.BGPInstance, "as", "router-id", "comment"); err != nil {
		return s, err
	}
	if err := falseFlags(instance, api.BGPInstance, "disabled"); err != nil {
		return s, err
	}
	if instance["routing-table"] != owner || (instance["vrf"] != "main" && instance["vrf"] != "") || instance["comment"] != "" {
		return s, inspectError("unrecognized-owned-property", api.BGPInstance, "references")
	}
	asn, parseErr := strconv.ParseUint(instance["as"], 10, 32)
	rid, ridErr := netip.ParseAddr(instance["router-id"])
	if parseErr != nil || !validASN(uint32(asn)) || strconv.FormatUint(asn, 10) != instance["as"] || ridErr != nil || !rid.Is4() || !rid.IsGlobalUnicast() {
		return s, inspectError("invalid-owned-property", api.BGPInstance, "identity")
	}
	s.Managed = true
	s.ASN = uint32(asn)
	s.RouterID = rid.String()
	s.ActiveGeneration = known.ActiveGeneration
	s.Table, err = objectRef(api.RoutingTable, table)
	if err != nil {
		return s, err
	}
	s.Instance, err = objectRef(api.BGPInstance, instance)
	if err != nil {
		return s, err
	}
	s.InputEntry, err = objectRef(api.FilterRule, ruleRows[owner+":root:in"])
	if err != nil {
		return s, err
	}
	s.OutputEntry, err = objectRef(api.FilterRule, ruleRows[owner+":root:out"])
	if err != nil {
		return s, err
	}
	for _, policy := range policies {
		g := GenerationState{Generation: policy.generation, PolicyChecksum: policy.checksum, Rules: []ObjectRef{}}
		for _, r := range policy.rules {
			ref, e := objectRef(api.FilterRule, ruleRows[r.Comment])
			if e != nil {
				return s, e
			}
			g.Rules = append(g.Rules, ref)
		}
		s.Generations = append(s.Generations, g)
	}
	unknownCredential := false
	activePeers := map[string]bool{}
	for _, generation := range known.Generations {
		if generation.PolicyChecksum[:16] == known.ActiveGeneration {
			for _, p := range generation.Desired.Peers {
				activePeers[p.ID] = true
			}
		}
	}
	for _, peer := range known.Peers {
		row := connectionRows[peer.ID]
		if row == nil {
			return s, inspectError("missing-owned-connection", api.BGPConnection, "name")
		}
		c, err := inspectConnection(row, owner, peer)
		if err != nil {
			return s, err
		}
		disabled, _ := boolObserved(row, "disabled")
		if c.LocalASN != s.ASN {
			proven := false
			if disabled && !activePeers[peer.ID] {
				for _, generation := range known.Generations {
					if generation.Desired.ASN == c.LocalASN {
						for _, p := range generation.Desired.Peers {
							if p.ID == peer.ID {
								proven = true
							}
						}
					}
				}
			}
			if !proven {
				return s, inspectError("local-asn-not-bound-to-instance-or-retained-generation", api.BGPConnection, "as")
			}
		}
		ref, err := objectRef(api.BGPConnection, row)
		if err != nil {
			return s, err
		}
		verified := credentialMatches(ref.ID, peer.ID, peer.Authentication)
		unknownCredential = unknownCredential || !verified
		s.Connections = append(s.Connections, ConnectionState{Ref: ref, Config: c, Disabled: ref.Disabled, AuthenticationVerified: verified})
	}
	sort.Slice(s.Connections, func(i, j int) bool { return s.Connections[i].Config.PeerID < s.Connections[j].Config.PeerID })
	if unknownCredential {
		return s, inspectError("unverified-credential", api.BGPConnection, "authentication")
	}
	if _, err := normalizeSnapshot(s.Target, owner, s, PlanOptions{}); err != nil {
		return s, err
	}
	return s, nil
}

func inspectConnection(row api.Record, owner string, peer KnownPeer) (Connection, error) {
	r := api.BGPConnection
	if err := required(row, r, "as", "afi", "local.address", "local.role", "remote.address", "remote.as", "nexthop-choice", "input.limit-process-routes-ipv4", "input.limit-process-routes-ipv6", "output.network", "output.redistribute", "output.default-originate", "output.default-prepend", "comment"); err != nil {
		return Connection{}, err
	}
	if err := flags(row, r, "disabled", "connect", "listen"); err != nil {
		return Connection{}, err
	}
	if err := falseFlags(row, r, "dynamic", "multihop", "output.remove-private-as", "output.as-override"); err != nil {
		return Connection{}, err
	}
	connect, _ := boolObserved(row, "connect")
	listen, _ := boolObserved(row, "listen")
	if row["instance"] != owner || row["templates"] != "" || row["routing-table"] != owner || (row["vrf"] != "main" && row["vrf"] != "") || row["input.filter"] != inputRoot(owner) || row["input.filter-nlri"] != "" || row["output.filter-chain"] != outputRoot(owner) || row["output.filter-select"] != "" || row["output.network"] != "" || row["output.redistribute"] != "" || row["local.role"] != "ebgp-rs" || row["nexthop-choice"] != "propagate" || row["output.default-originate"] != "never" || row["output.default-prepend"] != "0" || row["comment"] != "" || !connect || !listen {
		return Connection{}, inspectError("unrecognized-owned-property", r, "configuration")
	}
	family := row["afi"]
	limit, unused := row["input.limit-process-routes-ipv4"], row["input.limit-process-routes-ipv6"]
	if family == "ipv6" {
		limit, unused = unused, limit
	}
	if unused != "" {
		return Connection{}, inspectError("unrecognized-owned-property", r, "unused-family-limit")
	}
	n, err := strconv.Atoi(limit)
	asn, asnErr := strconv.ParseUint(row["remote.as"], 10, 32)
	localASN, localASNErr := strconv.ParseUint(row["as"], 10, 32)
	if err != nil || strconv.Itoa(n) != limit || asnErr != nil || number(uint32(asn)) != row["remote.as"] || localASNErr != nil || number(uint32(localASN)) != row["as"] {
		return Connection{}, inspectError("invalid-owned-property", r, "numeric")
	}
	remote, err := netip.ParseAddr(row["remote.address"])
	if err != nil {
		prefix, e := netip.ParsePrefix(row["remote.address"])
		if e != nil || prefix.Bits() != prefix.Addr().BitLen() {
			return Connection{}, inspectError("invalid-owned-property", r, "remote.address")
		}
		remote = prefix.Addr()
	}
	local, err := netip.ParseAddr(row["local.address"])
	if err != nil {
		return Connection{}, inspectError("invalid-owned-property", r, "local.address")
	}
	c := Connection{PeerID: peer.ID, Name: row["name"], Instance: owner, Family: family, LocalAddress: local.String(), RemoteAddress: remote.String(), RemoteASN: uint32(asn), LocalASN: uint32(localASN), MaxPrefixes: n, InputChain: inputRoot(owner), OutputChain: outputRoot(owner), Authentication: peer.Authentication}
	if err := validateConnection(c); err != nil {
		return Connection{}, inspectError("invalid-owned-property", r, "connection")
	}
	return c, nil
}
