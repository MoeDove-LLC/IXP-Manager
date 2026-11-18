package live

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"reflect"
	"strconv"
	"strings"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
)

var _ control.CleanupRuntime = (*Runtime)(nil)

type cleanupSession struct {
	plan   routeros.Plan
	bridge *routeros.APIBridge
	all    []routeros.APIOperation
	next   int
}

func (r *Runtime) cleanupFinished() bool {
	return r.cleanup == nil || r.cleanup.next == len(r.cleanup.all)
}
func cleanupKey(resource api.Resource, id string) string {
	return string(resource) + "/" + strings.ToUpper(id)
}

// BindCleanup is called only by the confirmed, authenticated journal owner. It
// cannot bind Prepare/Activate, recover an owner, or obtain secret plaintext.
// The supervisor proves the complete current HMAC (including credentials)
// before this call and around each removal. This adapter independently proves
// the protected non-secret configuration and exact remaining removal targets.
func (r *Runtime) BindCleanup(ctx context.Context, binding control.CleanupBinding) error {
	r.invalidateObjectActivation()
	defer r.invalidateObjectActivation()
	r.invalidateWriteEvidence()
	ctx, done, e := r.begin(ctx)
	if e != nil {
		return e
	}
	defer done()
	defer r.invalidateWriteEvidence()
	if !r.profile.CleanupVerified || r.poisoned || r.taken || r.removalTail != nil || r.preparedDiscard != nil || binding.Plan.Target != r.target {
		return fail("cleanup-binding-not-permitted")
	}
	if r.bridge != nil && (!r.completed || r.boundPlan == nil || r.boundPlan.Checksum != binding.Plan.Checksum) {
		return fail("cleanup-would-replace-unconfirmed-plan")
	}
	bridge, e := routeros.NewCleanupBridge(binding.Plan)
	if e != nil {
		return fail("cleanup-plan-invalid")
	}
	all := bridge.Operations()
	offset := len(all) - len(binding.RemainingOperationIDs)
	if offset < 0 {
		return fail("cleanup-suffix-invalid")
	}
	for i, id := range binding.RemainingOperationIDs {
		if all[offset+i].ID != id {
			return fail("cleanup-suffix-invalid")
		}
	}
	data, e := json.Marshal(binding.Plan)
	if e != nil {
		return fail("cleanup-plan-invalid")
	}
	var frozen routeros.Plan
	if json.Unmarshal(data, &frozen) != nil {
		return fail("cleanup-plan-invalid")
	}
	candidate := &cleanupSession{plan: frozen, bridge: bridge, all: all, next: offset}
	_, _, _, _, e = r.cleanupState(ctx, candidate)
	if e != nil {
		return e
	}
	r.cleanup = candidate
	if r.cleanupFinished() {
		r.completed = true
	}
	return nil
}

func (r *Runtime) ApplyCleanup(ctx context.Context, mutation routeros.APIMutation) (api.Result, error) {
	r.invalidateObjectActivation()
	defer r.invalidateObjectActivation()
	r.invalidateWriteEvidence()
	ctx, done, e := r.begin(ctx)
	if e != nil {
		return api.Result{}, e
	}
	defer done()
	defer r.invalidateWriteEvidence()
	if !r.profile.CleanupVerified || r.poisoned || r.taken || r.removalTail != nil || r.cleanupFinished() {
		return api.Result{}, fail("cleanup-not-bound")
	}
	candidate := r.cleanup
	if candidate.all[candidate.next].ID != mutation.OperationID || mutation.Phase != routeros.Cleanup || mutation.Request.Action != api.Remove {
		return api.Result{}, fail("cleanup-operation-order")
	}
	raw, evidence, _, _, e := r.cleanupState(ctx, candidate)
	if e != nil {
		return api.Result{}, e
	}
	resolver, e := routeros.NewAPIResolver(raw, r.target, evidence)
	if e != nil {
		return api.Result{}, fail("cleanup-reference-unverified")
	}
	expected, e := candidate.bridge.Mutation(mutation.OperationID, resolver)
	if e != nil || !reflect.DeepEqual(expected, mutation) {
		return api.Result{}, fail("cleanup-does-not-match-plan")
	}
	reader, ok := r.api.(safeModeReader)
	if !ok {
		return api.Result{}, fail("cleanup-owner-reader-unavailable")
	}
	safe, e := reader.ReadSafeMode(ctx)
	if e != nil || safe.Enabled || safe.Current {
		return api.Result{}, fail("cleanup-owner-not-idle")
	}
	result, e := r.api.Apply(ctx, expected.Request)
	if e != nil || !result.Acknowledged || result.ID != "" {
		r.poisoned = true
		return api.Result{}, &Failure{Code: "cleanup-write-outcome-unknown", Uncertain: true}
	}
	if expected.Request.Resource == api.BGPConnection {
		delete(r.credentialIDs, strings.ToUpper(expected.Request.ID))
	}
	candidate.next++
	if r.cleanupFinished() {
		r.completed = true
	}
	return result, nil
}

func (r *Runtime) cleanupState(ctx context.Context, candidate *cleanupSession) (api.Inventory, routeros.TargetEvidence, api.Inventory, string, error) {
	return r.cleanupStateWithEvidence(ctx, candidate, nil)
}

func (r *Runtime) cleanupStateWithEvidence(ctx context.Context, candidate *cleanupSession, cached *observedRemovalEvidence) (api.Inventory, routeros.TargetEvidence, api.Inventory, string, error) {
	raw, evidence, normalized, digest, e := r.readRemovalState(ctx, cached)
	if e != nil {
		return raw, evidence, normalized, digest, e
	}
	if normalized.SafeMode == nil || normalized.SafeMode.Enabled || normalized.SafeMode.Current {
		return raw, evidence, normalized, digest, fail("cleanup-owner-not-idle")
	}
	protected, e := cleanupProjection(candidate, normalized)
	if e != nil {
		return raw, evidence, normalized, digest, e
	}
	known := routeros.KnownState{Target: candidate.plan.Target, ActiveGeneration: candidate.plan.Generation,
		Generations: []routeros.KnownGeneration{{Target: candidate.plan.Target, Desired: candidate.plan.Desired, PolicyChecksum: candidate.plan.PolicyChecksum}}}
	for _, peer := range candidate.plan.Desired.Peers {
		known.Peers = append(known.Peers, routeros.KnownPeer{ID: peer.ID, Authentication: peer.Authentication})
	}
	// Every value in protected was already normalized using actual getter
	// evidence. Retired getters must not be replayed against this smaller view.
	protectedEvidence := evidence
	protectedEvidence.Properties = nil
	protectedEvidence.FilterRuleDisabledSelection = nil
	snapshot, checkErr := routeros.BuildSnapshot(protected, known, routeros.CredentialEvidence{}, protectedEvidence)
	var inspection *routeros.InspectError
	if checkErr != nil && !(errors.As(checkErr, &inspection) && inspection.Code == "unverified-credential") {
		return raw, evidence, normalized, digest, fail("cleanup-protected-graph-unverified")
	}
	if !snapshot.Managed || snapshot.TableFIB || !snapshot.CrossChannelIdentityVerified || snapshot.ASN != candidate.plan.Desired.ASN || snapshot.RouterID != candidate.plan.Desired.RouterID || snapshot.ActiveGeneration != candidate.plan.Generation || len(snapshot.Connections) != len(candidate.plan.Desired.Peers) {
		return raw, evidence, normalized, digest, fail("cleanup-protected-configuration-mismatch")
	}
	actual := map[string]routeros.ConnectionState{}
	for _, c := range snapshot.Connections {
		actual[c.Config.PeerID] = c
	}
	for _, peer := range candidate.plan.Desired.Peers {
		name, err := routeros.ManagedConnectionName(candidate.plan.Target.RouterID, peer.ID)
		if err != nil {
			return raw, evidence, normalized, digest, fail("cleanup-protected-configuration-mismatch")
		}
		family, local := "ip", candidate.plan.Desired.LocalIPv4
		address, _ := netip.ParseAddr(peer.Address)
		if address.Is6() {
			family, local = "ipv6", candidate.plan.Desired.LocalIPv6
		}
		wanted := routeros.Connection{PeerID: peer.ID, Name: name, Instance: candidate.plan.Owner, Family: family, LocalAddress: local, LocalASN: candidate.plan.Desired.ASN, RemoteAddress: peer.Address, RemoteASN: peer.ASN, MaxPrefixes: peer.MaxPrefixes, InputChain: candidate.plan.Owner + "_root_in", OutputChain: candidate.plan.Owner + "_root_out", Authentication: peer.Authentication}
		observed, ok := actual[peer.ID]
		if !ok || observed.Disabled || observed.Config != wanted {
			return raw, evidence, normalized, digest, fail("cleanup-protected-connection-mismatch")
		}
	}
	return raw, evidence, normalized, digest, nil
}

// Remove only recognized retired objects from the structural proof view.
// Unknown objects/references remain visible to BuildSnapshot and are rejected.
// References among a complete approved retired generation are permitted because
// that entire generation is unreferenced by the protected graph.
func cleanupProjection(candidate *cleanupSession, inv api.Inventory) (api.Inventory, error) {
	refs := map[string]routeros.Operation{}
	remaining := map[string]bool{}
	for _, op := range candidate.plan.Operations {
		if op.Phase == routeros.Cleanup {
			refs[op.ID] = op
		}
	}
	byObject := map[string]routeros.Operation{}
	for i, descriptor := range candidate.all {
		op := refs[descriptor.ID]
		resource := api.FilterRule
		if op.Kind == routeros.RemoveConnection {
			resource = api.BGPConnection
		}
		key := cleanupKey(resource, op.Object.ID)
		byObject[key] = op
		if i >= candidate.next {
			remaining[key] = true
		}
	}
	oldConnections := map[string]routeros.ConnectionState{}
	for _, c := range candidate.plan.Base.Connections {
		oldConnections[strings.ToUpper(c.Ref.ID)] = c
	}
	out := inv
	out.FilterRules = []api.Record{}
	out.Connections = []api.Record{}
	for _, menu := range []menu{{api.FilterRule, inv.FilterRules}, {api.BGPConnection, inv.Connections}} {
		for _, row := range menu.rows {
			key := cleanupKey(menu.resource, row[".id"])
			op, retired := byObject[key]
			if !retired {
				if menu.resource == api.FilterRule {
					out.FilterRules = append(out.FilterRules, row)
				} else {
					out.Connections = append(out.Connections, row)
				}
				continue
			}
			if !remaining[key] {
				return api.Inventory{}, fail("cleanup-completed-object-reappeared")
			}
			if menu.resource == api.FilterRule {
				if row["chain"] != op.Object.Chain || row["comment"] != op.Object.Comment {
					return api.Inventory{}, fail("cleanup-rule-identity-changed")
				}
			} else if row["name"] != op.Object.Name || row["disabled"] != "true" {
				return api.Inventory{}, fail("cleanup-connection-not-retired")
			}
			checked := row
			if menu.resource == api.BGPConnection {
				old, ok := oldConnections[strings.ToUpper(op.Object.ID)]
				if !ok {
					return api.Inventory{}, fail("cleanup-connection-baseline-missing")
				}
				checked = api.Record{}
				for field, value := range row {
					checked[field] = value
				}
				checked["disabled"] = strconv.FormatBool(old.Disabled)
			}
			checksum, e := routeros.ObservedAttributesChecksum(menu.resource, checked)
			if e != nil || checksum != op.Object.StateChecksum {
				return api.Inventory{}, fail("cleanup-retired-object-changed")
			}
			delete(remaining, key)
		}
	}
	if len(remaining) != 0 {
		return api.Inventory{}, fail("cleanup-remaining-object-missing")
	}
	return out, nil
}
