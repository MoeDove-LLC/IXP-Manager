package live

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
	"github.com/moedove/ixp-manager/internal/routeros/terminal"
)

// This list never includes tcp-md5-key. Only genuinely missing properties and
// presence-switch flags need getter evidence; no print omission means false.
var observedFields = map[api.Resource][]string{
	api.RoutingTable:  {"fib", "disabled", "dynamic", "invalid", "comment"},
	api.BGPInstance:   {"as", "router-id", "routing-table", "vrf", "disabled", "comment"},
	api.BGPConnection: {"instance", "as", "disabled", "dynamic", "templates", "afi", "local.address", "local.role", "remote.address", "remote.as", "routing-table", "vrf", "connect", "listen", "nexthop-choice", "multihop", "input.filter", "input.filter-nlri", "input.limit-process-routes-ipv4", "input.limit-process-routes-ipv6", "output.filter-chain", "output.filter-select", "output.network", "output.redistribute", "output.default-originate", "output.default-prepend", "output.remove-private-as", "output.as-override", "comment"},
	api.FilterRule:    {"chain", "rule", "disabled", "inactive", "comment"},
}

type menu struct {
	resource api.Resource
	rows     []api.Record
}
type propertyBatchReader interface {
	ReadProperties(context.Context, []api.PropertyRequest) ([]api.PropertyValue, error)
}

type filterRuleDisabledReader interface {
	ReadFilterRuleDisabled(context.Context, bool) ([]string, error)
}

func menus(inv api.Inventory) []menu {
	return []menu{{api.RoutingTable, inv.Tables}, {api.BGPInstance, inv.Instances}, {api.BGPConnection, inv.Connections}, {api.FilterRule, inv.FilterRules}}
}
func observedDevice(inv api.Inventory, source routeros.IdentitySource) (string, bool) {
	var rows []api.Record
	var field string
	switch source {
	case routeros.LicenseSystemID:
		rows = inv.Licenses
		field = "system-id"
	case routeros.LicenseSoftwareID:
		rows = inv.Licenses
		field = "software-id"
	case routeros.RouterBoardSerial:
		rows = inv.RouterBoards
		field = "serial-number"
	default:
		return "", false
	}
	if len(rows) != 1 || !validDeviceIdentifier(source, rows[0][field]) {
		return "", false
	}
	return rows[0][field], true
}
func validVersion(raw, wanted string) bool {
	return raw == wanted || wanted == "7.24.6" && raw == "7.24.6 (long-term)"
}
func (r *Runtime) identity(ctx context.Context, inv api.Inventory) (routeros.TargetEvidence, error) {
	device, ok := observedDevice(inv, r.config.IdentitySource)
	if !ok || device != r.target.DeviceID || len(inv.Resources) != 1 || !validVersion(inv.Resources[0]["version"], r.target.Version) {
		return routeros.TargetEvidence{}, fail("api-device-or-version-mismatch")
	}
	// A runtime never replaces its owner channel. After cancellation/release,
	// readback may use the same API plus the identity already proven on that
	// pinned SSH session. This does not imply that the owner is still alive.
	if !r.sshIdentityVerified {
		if r.ownerClosed.Load() || r.terminal == nil || r.terminal.Snapshot().Closed {
			return routeros.TargetEvidence{}, fail("ssh-identity-unavailable")
		}
	}
	if !r.ownerClosed.Load() && r.terminal != nil && !r.terminal.Snapshot().Closed {
		version, e := r.terminal.Query(ctx, terminal.Version)
		if e != nil || !validVersion(version.Version, r.target.Version) {
			return routeros.TargetEvidence{}, fail("ssh-version-unverified")
		}
		query := terminal.LicenseSystemID
		switch r.config.IdentitySource {
		case routeros.LicenseSoftwareID:
			query = terminal.LicenseSoftwareID
		case routeros.RouterBoardSerial:
			query = terminal.RouterBoardSerial
		}
		result, e := r.terminal.Query(ctx, query)
		if e != nil || result.Query != query {
			return routeros.TargetEvidence{}, fail("ssh-identity-unverified")
		}
		sum := sha256.Sum256([]byte(device))
		if result.IdentifierFingerprint != hex.EncodeToString(sum[:]) {
			return routeros.TargetEvidence{}, fail("cross-channel-device-mismatch")
		}
		r.sshIdentityVerified = true
	}
	return routeros.TargetEvidence{APIAuthenticated: true, APITransportIdentitySHA256: r.target.TransportIdentitySHA256, APIIdentitySource: r.config.IdentitySource, SSHAuthenticated: true, SSHTransportIdentitySHA256: r.target.TransportIdentitySHA256, SSHIdentitySource: r.config.IdentitySource, SSHDeviceID: device, SSHVersion: r.target.Version}, nil
}
func (r *Runtime) readState(ctx context.Context) (api.Inventory, routeros.TargetEvidence, api.Inventory, string, error) {
	raw, e := r.api.Inventory(ctx)
	if e != nil {
		return api.Inventory{}, routeros.TargetEvidence{}, api.Inventory{}, "", fail("api-inventory-failed")
	}
	evidence, e := r.identity(ctx, raw)
	if e != nil {
		return raw, evidence, api.Inventory{}, "", e
	}
	count, getters, observedBytes := 0, 0, 0
	requests := []api.PropertyRequest{}
	selectionReader, hasSelectionReader := r.api.(filterRuleDisabledReader)
	useSelection := r.profile.FilterRuleDisabledSelectionVerified && r.target.Version == "7.24.6" && len(raw.FilterRules) >= 128 && hasSelectionReader
	for _, m := range menus(raw) {
		count += len(m.rows)
		if count > r.config.MaxObjects {
			return raw, evidence, api.Inventory{}, "", fail("inventory-object-bound")
		}
		for _, row := range m.rows {
			for key, value := range row {
				observedBytes += len(key) + len(value)
				if len(value) > 16384 || observedBytes > 32<<20 {
					return raw, evidence, api.Inventory{}, "", fail("inventory-byte-bound")
				}
			}
			for _, field := range observedFields[m.resource] {
				if useSelection && m.resource == api.FilterRule && field == "disabled" {
					continue // two actual full-menu predicates, never a guessed default
				}
				value, present := row[field]
				presence := m.resource == api.RoutingTable && (field == "fib" || field == "dynamic") || m.resource == api.BGPConnection && field == "dynamic"
				if present && !(m.resource == api.BGPConnection && (field == "as" || field == "output.default-prepend")) && (!presence || value != "") {
					continue
				}
				getters++
				if getters > r.config.MaxGetterCalls {
					return raw, evidence, api.Inventory{}, "", fail("property-getter-bound")
				}
				requests = append(requests, api.PropertyRequest{Resource: m.resource, ID: row[".id"], Property: field})
			}
		}
	}
	if useSelection {
		falseIDs, err := selectionReader.ReadFilterRuleDisabled(ctx, false)
		if err != nil {
			return raw, evidence, api.Inventory{}, "", fail("filter-rule-disabled-selection-failed")
		}
		trueIDs, err := selectionReader.ReadFilterRuleDisabled(ctx, true)
		if err != nil {
			return raw, evidence, api.Inventory{}, "", fail("filter-rule-disabled-selection-failed")
		}
		if len(falseIDs)+len(trueIDs) > r.config.MaxObjects {
			return raw, evidence, api.Inventory{}, "", fail("filter-rule-disabled-selection-bound")
		}
		// This distinct evidence is checked for a complete, disjoint exact-ID
		// partition against this actual inventory by the parent normalizer.
		// It is deliberately not represented as a property getter result.
		evidence.FilterRuleDisabledSelection = &routeros.FilterRuleDisabledSelection{FalseCompleted: true, TrueCompleted: true, FalseIDs: append([]string{}, falseIDs...), TrueIDs: append([]string{}, trueIDs...)}
	}
	for offset := 0; offset < len(requests); {
		end := offset + api.MaxPropertyBatch
		if end > len(requests) {
			end = len(requests)
		}
		part := requests[offset:end]
		values := make([]api.PropertyValue, len(part))
		if batch, ok := r.api.(propertyBatchReader); ok {
			var err error
			values, err = batch.ReadProperties(ctx, part)
			if err != nil || len(values) != len(part) {
				var failure *api.Error
				if errors.As(err, &failure) && failure.Resource != "" && failure.Property != "" {
					return raw, evidence, api.Inventory{}, "", fail("required-property-getter-failed-" + string(failure.Resource) + "-" + failure.Property)
				}
				return raw, evidence, api.Inventory{}, "", fail("required-property-getter-batch-failed")
			}
		} else {
			for i, q := range part {
				value, err := r.api.ReadProperty(ctx, q.Resource, q.ID, q.Property)
				if err != nil {
					return raw, evidence, api.Inventory{}, "", fail("required-property-getter-failed-" + string(q.Resource) + "-" + q.Property)
				}
				values[i] = value
			}
		}
		for i, q := range part {
			value := values[i]
			observedBytes += len(value.Value) + len(q.Property) + len(q.ID)
			if len(value.Value) > 16384 || observedBytes > 32<<20 {
				return raw, evidence, api.Inventory{}, "", fail("property-evidence-byte-bound")
			}
			evidence.Properties = append(evidence.Properties, routeros.VersionPropertyEvidence{Resource: q.Resource, ID: q.ID, Field: q.Property, Completed: true, Property: value})
		}
		offset = end
	}
	normalized, e := routeros.NormalizeObservedInventory(raw, r.target, evidence)
	if e != nil {
		return raw, evidence, api.Inventory{}, "", inspectionFailure(e)
	}
	digest, e := stateDigest(normalized, r.target)
	if e != nil {
		return raw, evidence, api.Inventory{}, "", e
	}
	return raw, evidence, normalized, digest, nil
}
func inspectionFailure(e error) error {
	var inspect *routeros.InspectError
	if errors.As(e, &inspect) {
		return fail("inspection-" + inspect.Code + "-" + string(inspect.Resource) + "-" + inspect.Field)
	}
	return fail("inventory-inspection-failed")
}
func stateDigest(inv api.Inventory, target routeros.Target) (string, error) {
	type rowDigest struct {
		Resource     api.Resource
		ID, Checksum string
	}
	rows := []rowDigest{}
	for _, m := range menus(inv) {
		part := make([]rowDigest, 0, len(m.rows))
		for _, row := range m.rows {
			digest, e := routeros.ObservedAttributesChecksum(m.resource, row)
			if e != nil {
				return "", fail("inventory-digest-failed")
			}
			part = append(part, rowDigest{m.resource, strings.ToUpper(row[".id"]), digest})
		}
		// Rule order is semantic. Other menus are sets indexed by immutable native ID.
		if m.resource != api.FilterRule {
			sort.Slice(part, func(i, j int) bool { return part[i].ID < part[j].ID })
		}
		rows = append(rows, part...)
	}
	raw, e := json.Marshal(struct {
		Domain string
		Target routeros.Target
		Rows   []rowDigest
	}{"routeros-live-observed-v1", target, rows})
	if e != nil {
		return "", fail("inventory-digest-failed")
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
func (r *Runtime) credentials(ctx context.Context, inv api.Inventory, known routeros.KnownState, authority map[string]string) (map[string]api.Credential, error) {
	if len(known.Peers) > r.config.MaxObjects {
		return nil, fail("manifest-peer-bound")
	}
	names := map[string]bool{}
	for _, peer := range known.Peers {
		name, e := routeros.ManagedConnectionName(r.target.RouterID, peer.ID)
		if e != nil || names[name] {
			return nil, fail("invalid-known-peer")
		}
		names[name] = true
	}
	values := map[string]api.Credential{}
	for _, row := range inv.Connections {
		id := strings.ToUpper(row[".id"])
		owner := routeros.OwnerName(r.target.RouterID)
		if names[row["name"]] && (r.credentialIDs[id] == row["name"] || authority[id] == row["name"]) && row["instance"] == owner && row["routing-table"] == owner && row["local.role"] == "ebgp-rs" {
			value, e := r.api.ReadCredential(ctx, id)
			if e != nil {
				return nil, fail("credential-observation-failed")
			}
			values[id] = value
		}
	}
	return values, nil
}
func sameCredentials(a, b map[string]api.Credential) bool {
	if len(a) != len(b) {
		return false
	}
	for id, old := range a {
		next, ok := b[id]
		if !ok || old.Known != next.Known {
			return false
		}
		if old.Known {
			raw := old.Value.CopyBytes()
			equal := next.Matches(raw)
			clear(raw)
			if !equal {
				return false
			}
		}
	}
	return true
}
func (r *Runtime) Observe(ctx context.Context, request control.ReadRequest) (control.Observation, error) {
	ctx, done, e := r.begin(ctx)
	if e != nil {
		return control.Observation{}, e
	}
	defer done()
	r.invalidateWriteEvidence()
	observed, e := r.observe(ctx, request)
	if e != nil {
		var failure *Failure
		code := "observation-failed"
		if errors.As(e, &failure) {
			code = failure.Code
		}
		r.recordFailure(code)
		return control.Observation{}, e
	}
	return observed, nil
}
func (r *Runtime) observe(ctx context.Context, request control.ReadRequest) (control.Observation, error) {
	epoch := r.writeEvidenceEpoch.Load()
	if request.KnownState.Target != r.target {
		return control.Observation{}, fail("manifest-target-mismatch")
	}
	firstRaw, firstEvidence, first, firstDigest, e := r.readState(ctx)
	if e != nil {
		return control.Observation{}, e
	}
	// A matching name or caller manifest is insufficient authority to read a
	// password. Validate the entire namespace and all non-secret attributes
	// first; only the dedicated final credential error is acceptable here.
	preflight, preflightErr := routeros.BuildSnapshot(firstRaw, request.KnownState, routeros.CredentialEvidence{}, firstEvidence)
	var inspection *routeros.InspectError
	closedNamespace := preflightErr == nil || errors.As(preflightErr, &inspection) && inspection.Code == "unverified-credential"
	if request.Strict && !closedNamespace {
		return control.Observation{}, inspectionFailure(preflightErr)
	}
	if closedNamespace {
		if r.credentialIDs == nil {
			r.credentialIDs = map[string]string{}
		}
		for _, connection := range preflight.Connections {
			r.credentialIDs[strings.ToUpper(connection.Ref.ID)] = connection.Config.Name
		}
	}
	firstAuthority, e := r.credentialAuthority(first, request.KnownState, request.CredentialAuthority)
	if e != nil {
		return control.Observation{}, e
	}
	firstCredentials, e := r.credentials(ctx, first, request.KnownState, firstAuthority)
	if e != nil {
		return control.Observation{}, e
	}
	raw, evidence, normalized, digest, e := r.readState(ctx)
	if e != nil {
		return control.Observation{}, e
	}
	if digest != firstDigest {
		return control.Observation{}, fail("configuration-changed-during-observation")
	}
	secondAuthority, e := r.credentialAuthority(normalized, request.KnownState, request.CredentialAuthority)
	if e != nil {
		return control.Observation{}, e
	}
	credentials, e := r.credentials(ctx, normalized, request.KnownState, secondAuthority)
	if e != nil {
		return control.Observation{}, e
	}
	if !sameCredentials(firstCredentials, credentials) {
		return control.Observation{}, fail("credentials-changed-during-observation")
	}
	resolver, e := routeros.NewAPIResolver(raw, r.target, evidence)
	if e != nil {
		return control.Observation{}, inspectionFailure(e)
	}
	out := control.Observation{Target: r.target, Inventory: normalized, Resolver: resolver, Credentials: credentials, CrossChannelIdentityVerified: true, ObservedAt: time.Now().UTC()}
	if request.Strict {
		snapshot, e := routeros.BuildSnapshot(raw, request.KnownState, routeros.CredentialEvidence{Observed: credentials, Expected: request.BaselineCredentials}, evidence)
		if e != nil {
			return control.Observation{}, inspectionFailure(e)
		}
		out.Snapshot = &snapshot
	}
	if !r.ownerClosed.Load() && !r.terminal.Snapshot().Closed {
		out.Owner, e = r.owner(ctx)
		if e != nil {
			return control.Observation{}, e
		}
	} else {
		out.Owner = control.Owner{SessionID: r.sessionID}
		if normalized.SafeMode != nil {
			out.Owner.Known = true
			out.Owner.Active = normalized.SafeMode.Enabled
		}
	}
	if e = r.verifyRemovalTail(digest, out.Owner); e != nil {
		return control.Observation{}, e
	}
	r.mu.Lock()
	r.status = Status{Target: r.target, ObservedAt: out.ObservedAt, SessionExpiresAt: r.sessionExpiresAt, Digest: digest, CrossChannelIdentityVerified: true, StrictSnapshotVerified: request.Strict, Owner: out.Owner, Profile: r.profile}
	r.mu.Unlock()
	r.rememberWriteEvidence(raw, evidence, epoch)
	r.rememberRemovalEvidence(raw, evidence, out.Owner, epoch)
	return out, nil
}
