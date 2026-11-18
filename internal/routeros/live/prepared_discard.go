package live

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"

	"github.com/moedove/ixp-manager/internal/routeros"
	"github.com/moedove/ixp-manager/internal/routeros/api"
	"github.com/moedove/ixp-manager/internal/routeros/control"
)

var _ control.PreparedDiscardRuntime = (*Runtime)(nil)

type preparedDiscardSession struct {
	binding     control.PreparedDiscardBinding
	bridge      *routeros.APIBridge
	operations  []routeros.APIOperation
	next        int
	fingerprint string
}

func (r *Runtime) preparedDiscardFinished() bool {
	return r.preparedDiscard == nil || r.preparedDiscard.next == len(r.preparedDiscard.operations)
}

// BindPreparedDiscard can bind only a fresh, unowned runtime. A prior cancelled
// or uncertain runtime remains poisoned. The durable controller is responsible
// for authenticating the original Add readbacks and complete credential HMACs.
func (r *Runtime) BindPreparedDiscard(ctx context.Context, binding control.PreparedDiscardBinding) error {
	r.invalidateObjectActivation()
	r.invalidateWriteEvidence()
	ctx, done, err := r.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	defer r.invalidateWriteEvidence()
	if !r.profile.PreparedDiscardVerified || r.poisoned || r.taken || r.removalTail != nil || r.ownerClosed.Load() || r.bridge != nil || r.boundPlan != nil || r.cleanup != nil || binding.Plan.Target != r.target || binding.BaselineKnown.Target != r.target {
		return fail("prepared-discard-binding-not-permitted")
	}
	data, err := json.Marshal(binding)
	if err != nil {
		return fail("prepared-discard-binding-invalid")
	}
	var frozen control.PreparedDiscardBinding
	if json.Unmarshal(data, &frozen) != nil {
		return fail("prepared-discard-binding-invalid")
	}
	sort.Slice(frozen.Objects, func(i, j int) bool {
		return frozen.Objects[i].PrepareOperationID < frozen.Objects[j].PrepareOperationID
	})
	bridge, err := routeros.NewPreparedDiscardBridge(frozen.Plan, frozen.Objects)
	if err != nil {
		return fail("prepared-discard-plan-invalid")
	}
	operations := bridge.Operations()
	offset := len(operations) - len(frozen.RemainingOperationIDs)
	if offset < 0 {
		return fail("prepared-discard-suffix-invalid")
	}
	for i, id := range frozen.RemainingOperationIDs {
		if operations[offset+i].ID != id {
			return fail("prepared-discard-suffix-invalid")
		}
	}
	identity := frozen
	identity.RemainingOperationIDs = nil
	encoded, err := json.Marshal(identity)
	if err != nil {
		return fail("prepared-discard-binding-invalid")
	}
	sum := sha256.Sum256(encoded)
	fingerprint := hex.EncodeToString(sum[:])
	if prior := r.preparedDiscard; prior != nil && (prior.fingerprint != fingerprint || prior.next != offset) {
		return fail("prepared-discard-rebind-not-permitted")
	}
	candidate := &preparedDiscardSession{binding: frozen, bridge: bridge, operations: operations, next: offset, fingerprint: fingerprint}
	if _, _, err = r.preparedDiscardState(ctx, candidate); err != nil {
		return err
	}
	r.preparedDiscard = candidate
	return nil
}

func (r *Runtime) ApplyPreparedDiscard(ctx context.Context, mutation routeros.APIMutation) (api.Result, error) {
	r.invalidateObjectActivation()
	r.invalidateWriteEvidence()
	ctx, done, err := r.begin(ctx)
	if err != nil {
		return api.Result{}, err
	}
	defer done()
	defer r.invalidateWriteEvidence()
	if !r.profile.PreparedDiscardVerified || r.poisoned || r.taken || r.removalTail != nil || r.ownerClosed.Load() || r.bridge != nil || r.boundPlan != nil || r.cleanup != nil || r.preparedDiscardFinished() {
		return api.Result{}, fail("prepared-discard-not-bound")
	}
	candidate := r.preparedDiscard
	if mutation.Phase != routeros.DiscardPrepared || mutation.OperationID != candidate.operations[candidate.next].ID || mutation.Request.Action != api.Remove {
		return api.Result{}, fail("prepared-discard-operation-order")
	}
	raw, evidence, err := r.preparedDiscardState(ctx, candidate)
	if err != nil {
		return api.Result{}, err
	}
	resolver, err := routeros.NewAPIResolver(raw, r.target, evidence)
	if err != nil {
		return api.Result{}, fail("prepared-discard-reference-unverified")
	}
	expected, err := candidate.bridge.Mutation(mutation.OperationID, resolver)
	if err != nil || !reflect.DeepEqual(expected, mutation) {
		return api.Result{}, fail("prepared-discard-does-not-match-binding")
	}
	reader, ok := r.api.(safeModeReader)
	if !ok {
		return api.Result{}, fail("prepared-discard-owner-reader-unavailable")
	}
	owner, err := reader.ReadSafeMode(ctx)
	if err != nil || owner.Enabled || owner.Current {
		return api.Result{}, fail("prepared-discard-owner-not-idle")
	}
	result, err := r.api.Apply(ctx, expected.Request)
	if err != nil || !result.Acknowledged || result.ID != "" {
		r.poisoned = true
		return api.Result{}, &Failure{Code: "prepared-discard-write-outcome-unknown", Uncertain: true}
	}
	if expected.Request.Resource == api.BGPConnection {
		delete(r.credentialIDs, strings.ToUpper(expected.Request.ID))
	}
	candidate.next++
	return result, nil
}

func (r *Runtime) preparedDiscardState(ctx context.Context, candidate *preparedDiscardSession) (api.Inventory, routeros.TargetEvidence, error) {
	return r.preparedDiscardStateWithEvidence(ctx, candidate, nil)
}

func (r *Runtime) preparedDiscardStateWithEvidence(ctx context.Context, candidate *preparedDiscardSession, cached *observedRemovalEvidence) (api.Inventory, routeros.TargetEvidence, error) {
	raw, evidence, normalized, _, err := r.readRemovalState(ctx, cached)
	if err != nil {
		return raw, evidence, err
	}
	if normalized.SafeMode == nil || normalized.SafeMode.Enabled || normalized.SafeMode.Current {
		return raw, evidence, fail("prepared-discard-owner-not-idle")
	}
	owner, err := r.owner(ctx)
	if err != nil || !owner.Known || !owner.Alive || owner.Active || owner.ThisSession || !owner.HistoryKnown || owner.HistoryActions != 0 {
		return raw, evidence, fail("prepared-discard-owner-unverified")
	}
	protected, err := preparedDiscardProjection(candidate, normalized)
	if err != nil {
		return raw, evidence, err
	}
	structuralEvidence := evidence
	structuralEvidence.Properties = nil // these actual records were already normalized
	structuralEvidence.FilterRuleDisabledSelection = nil
	snapshot, checkErr := routeros.BuildSnapshot(protected, candidate.binding.BaselineKnown, routeros.CredentialEvidence{}, structuralEvidence)
	var inspection *routeros.InspectError
	if checkErr != nil && !(errors.As(checkErr, &inspection) && inspection.Code == "unverified-credential") {
		return raw, evidence, fail("prepared-discard-baseline-graph-unverified")
	}
	// This is only a structural projection for independent adapter checks. It
	// is never returned as an observed snapshot. The controller separately
	// verifies full current credentials, and final actual Strict baseline.
	expectedDigest, actualDigest := structuralSnapshotDigest(candidate.binding.Plan.Base), structuralSnapshotDigest(snapshot)
	if expectedDigest == "" || actualDigest == "" || expectedDigest != actualDigest {
		return raw, evidence, fail("prepared-discard-protected-baseline-changed")
	}
	return raw, evidence, nil
}

func structuralSnapshotDigest(snapshot routeros.Snapshot) string {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return ""
	}
	var copy routeros.Snapshot
	if json.Unmarshal(raw, &copy) != nil {
		return ""
	}
	for i := range copy.Connections {
		copy.Connections[i].AuthenticationVerified = false
	}
	raw, err = json.Marshal(copy)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func preparedDiscardProjection(candidate *preparedDiscardSession, actual api.Inventory) (api.Inventory, error) {
	remaining, approved := map[string]bool{}, map[string]routeros.PreparedDiscardObject{}
	byOperation := map[string]routeros.PreparedDiscardObject{}
	for _, object := range candidate.binding.Objects {
		key := cleanupKey(object.Resource, object.ID)
		approved[key] = object
		byOperation[routeros.PreparedDiscardOperationID(object.PrepareOperationID)] = object
	}
	for _, operation := range candidate.operations[candidate.next:] {
		object := byOperation[operation.ID]
		remaining[cleanupKey(object.Resource, object.ID)] = true
	}
	if err := preparedDiscardForeignReferences(actual, approved); err != nil {
		return api.Inventory{}, err
	}
	project := func(resource api.Resource, rows []api.Record) ([]api.Record, error) {
		out := []api.Record{}
		for _, row := range rows {
			key := cleanupKey(resource, row[".id"])
			object, known := approved[key]
			if !known {
				out = append(out, row)
				continue
			}
			if !remaining[key] {
				return nil, fail("prepared-discard-completed-object-reappeared")
			}
			wanted := api.Record{".id": object.ID}
			for name, value := range object.Attributes {
				wanted[name] = value
			}
			expected, e1 := routeros.PreparedDiscardAttributesChecksum(resource, wanted)
			observed, e2 := routeros.PreparedDiscardAttributesChecksum(resource, row)
			if e1 != nil || e2 != nil || expected != observed {
				return nil, fail("prepared-discard-object-changed")
			}
			delete(remaining, key)
		}
		return out, nil
	}
	out := actual
	var err error
	if out.Connections, err = project(api.BGPConnection, actual.Connections); err != nil {
		return api.Inventory{}, err
	}
	if out.FilterRules, err = project(api.FilterRule, actual.FilterRules); err != nil {
		return api.Inventory{}, err
	}
	if out.Instances, err = project(api.BGPInstance, actual.Instances); err != nil {
		return api.Inventory{}, err
	}
	if out.Tables, err = project(api.RoutingTable, actual.Tables); err != nil {
		return api.Inventory{}, err
	}
	if len(remaining) != 0 {
		return api.Inventory{}, fail("prepared-discard-remaining-object-missing")
	}
	return out, nil
}

// Check references before projecting away a new table/instance. Otherwise a
// foreign native-ID reference could disappear from BuildSnapshot's lookup set.
func preparedDiscardForeignReferences(actual api.Inventory, approved map[string]routeros.PreparedDiscardObject) error {
	tables, instances, chains := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, object := range approved {
		switch object.Resource {
		case api.RoutingTable:
			tables[object.Attributes["name"]], tables[strings.ToUpper(object.ID)] = true, true
		case api.BGPInstance:
			instances[object.Attributes["name"]], instances[strings.ToUpper(object.ID)] = true, true
		case api.FilterRule:
			chains[object.Attributes["chain"]] = true
		}
	}
	refers := func(value string, names map[string]bool) bool {
		for _, part := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' }) {
			if names[part] || strings.HasPrefix(part, "*") && names[strings.ToUpper(part)] {
				return true
			}
		}
		return false
	}
	for _, row := range actual.Instances {
		if _, target := approved[cleanupKey(api.BGPInstance, row[".id"])]; !target && (refers(row["routing-table"], tables) || refers(row["vrf"], tables)) {
			return fail("prepared-discard-foreign-reference")
		}
	}
	for _, row := range actual.Connections {
		if _, target := approved[cleanupKey(api.BGPConnection, row[".id"])]; target {
			continue
		}
		if refers(row["instance"], instances) || refers(row["routing-table"], tables) || refers(row["vrf"], tables) {
			return fail("prepared-discard-foreign-reference")
		}
		for _, key := range []string{"input.filter", "input.filter-nlri", "output.filter-chain", "output.filter-select"} {
			if refers(row[key], chains) {
				return fail("prepared-discard-foreign-reference")
			}
		}
	}
	for _, row := range actual.FilterRules {
		if _, target := approved[cleanupKey(api.FilterRule, row[".id"])]; target {
			continue
		}
		if chains[row["chain"]] {
			return fail("prepared-discard-protected-chain-overlap")
		}
		for chain := range chains {
			if chain != "" && strings.Contains(row["rule"], chain) {
				return fail("prepared-discard-foreign-reference")
			}
		}
	}
	return nil
}
