package routeros

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/moedove/ixp-manager/internal/routeros/api"
)

// APIBridge holds only commands produced by Compile from a canonical typed plan.
// It performs no I/O, permission checks, journal writes or Safe Mode operations.
// The secret-bearing compiled attributes are private and never log-serialized.
type APIBridge struct {
	target   Target
	checksum string
	order    []string
	commands map[string]CompiledCommand
}

func (APIBridge) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "RouterOS typed API bridge (command values redacted)")
}
func (b APIBridge) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		PlanChecksum    string `json:"planChecksum"`
		NativeValidated bool   `json:"nativeValidated"`
	}{b.checksum, false})
}

type APIOperation struct {
	ID       string   `json:"id"`
	Phase    Phase    `json:"phase"`
	Resource Resource `json:"resource"`
	Action   Action   `json:"action"`
}

// APIMutation is a protocol-encodable request, never an execution permit.
// NativeChecks name target semantics that still require a tested device adapter.
// State verification includes the original baseline, exact prepared objects,
// operation ordering, exclusive control and current Safe Mode ownership.
type APIMutation struct {
	PlanChecksum                 string       `json:"planChecksum"`
	OperationID                  string       `json:"operationId"`
	Phase                        Phase        `json:"phase"`
	Request                      api.Mutation `json:"request"`
	NativeValidated              bool         `json:"nativeValidated"`
	RequiresTargetValidation     bool         `json:"requiresTargetValidation"`
	RequiresStateVerification    bool         `json:"requiresStateVerification"`
	CrossChannelIdentityVerified bool         `json:"crossChannelIdentityVerified"`
	NativeChecks                 []string     `json:"nativeChecks"`
}

// APIResolver is a private copy of completed, authenticated fixed-menu reads.
// It proves reference uniqueness within one resource menu, not object ownership,
// a transactionally consistent snapshot, baseline freshness, or Safe Mode state.
type APIResolver struct {
	target               Target
	crossChannelVerified bool
	rows                 map[api.Resource][]api.Record
	byIdentity           map[api.Resource]map[string][]api.Record
}

func (APIResolver) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "RouterOS API reference resolver (read values redacted)")
}
func (APIResolver) MarshalJSON() ([]byte, error) {
	return []byte(`{"resolver":"private-observed-state"}`), nil
}

func apiResource(r Resource) (api.Resource, error) {
	switch r {
	case RoutingTable:
		return api.RoutingTable, nil
	case BGPInstance:
		return api.BGPInstance, nil
	case BGPConnection:
		return api.BGPConnection, nil
	case FilterRule:
		return api.FilterRule, nil
	}
	return "", errors.New("unsupported typed API resource")
}
func apiAction(a Action) (api.Action, error) {
	switch a {
	case Add:
		return api.Add, nil
	case Set:
		return api.Set, nil
	case Enable:
		return api.Enable, nil
	case Disable:
		return api.Disable, nil
	case Remove:
		return api.Remove, nil
	case Unset:
		return api.Unset, nil
	}
	return "", errors.New("unsupported typed API action")
}

func mutationRequest(cmd CompiledCommand, id string) (api.Mutation, error) {
	resource, err := apiResource(cmd.Resource)
	if err != nil {
		return api.Mutation{}, err
	}
	action, err := apiAction(cmd.Action)
	if err != nil {
		return api.Mutation{}, err
	}
	m := api.Mutation{Resource: resource, Action: action, ID: id, Property: cmd.Property, Attributes: make([]api.Attribute, 0, len(cmd.Attributes))}
	for _, a := range cmd.Attributes {
		// Values are API strings, not CLI text: preserve empty values, dollar
		// signs, quotes and every byte. Never drop an unverified reset property.
		m.Attributes = append(m.Attributes, api.Attribute{Name: a.Name, Value: a.Value, Sensitive: a.Sensitive || a.Name == "tcp-md5-key"})
	}
	if err := api.ValidateMutation(m); err != nil {
		return api.Mutation{}, errors.New("typed command cannot be represented by the fixed API schema")
	}
	return m, nil
}

// NewAPIBridge validates the entire plan and preflights every compiled operation
// against api.ValidateMutation before any caller can obtain a mutation. Placeholder
// IDs are used ONLY for this pure schema check and are never returned to callers.
func NewAPIBridge(plan Plan, credentials Credentials) (*APIBridge, error) {
	compiled, err := Compile(plan, credentials)
	if err != nil {
		return nil, err
	}
	b := &APIBridge{target: plan.Target, checksum: compiled.PlanChecksum, commands: map[string]CompiledCommand{}, order: []string{}}
	for _, cmd := range compiled.Commands {
		id := ""
		if cmd.Action != Add {
			id = "*1"
		}
		if _, err := mutationRequest(cmd, id); err != nil {
			return nil, err
		}
		if cmd.OperationID == "" {
			return nil, errors.New("typed operation ID missing")
		}
		if _, exists := b.commands[cmd.OperationID]; exists {
			return nil, errors.New("duplicate typed operation ID")
		}
		b.commands[cmd.OperationID] = cmd
		b.order = append(b.order, cmd.OperationID)
	}
	return b, nil
}

// Operations returns only safe descriptors. Commands and passwords remain private.
func (b *APIBridge) Operations() []APIOperation {
	if b == nil {
		return nil
	}
	out := make([]APIOperation, 0, len(b.order))
	for _, id := range b.order {
		cmd := b.commands[id]
		out = append(out, APIOperation{ID: id, Phase: cmd.Phase, Resource: cmd.Resource, Action: cmd.Action})
	}
	return out
}

// NewAPIResolver accepts complete fixed-menu reads plus independently verified
// transport/device evidence. A browser-supplied Inventory/Evidence is not trusted.
// Resolve again from a fresh read after each Add; never guess the object's ID.
func NewAPIResolver(inventory api.Inventory, target Target, evidence TargetEvidence) (*APIResolver, error) {
	if err := validateTarget(target); err != nil {
		return nil, err
	}
	cross, err := observedIdentity(inventory, evidence, target)
	if err != nil {
		return nil, err
	}
	inventory, err = normalizeTargetProperties(inventory, target, evidence)
	if err != nil {
		return nil, err
	}
	menus := map[api.Resource][]api.Record{api.RoutingTable: inventory.Tables, api.BGPInstance: inventory.Instances, api.BGPConnection: inventory.Connections, api.FilterRule: inventory.FilterRules}
	r := &APIResolver{target: target, crossChannelVerified: cross, rows: map[api.Resource][]api.Record{}, byIdentity: map[api.Resource]map[string][]api.Record{}}
	count, size := 0, 0
	for resource, rows := range menus {
		if inventory.Capabilities[resource] != api.Supported {
			return nil, errors.New("reference resolver requires all fixed menus to be read completely")
		}
		count += len(rows)
		if count > MaxPrepareOperations+20000 {
			return nil, errors.New("reference resolver inventory row limit exceeded")
		}
		for _, row := range rows {
			for k, v := range row {
				size += len(k) + len(v)
				if size > 64<<20 {
					return nil, errors.New("reference resolver inventory byte limit exceeded")
				}
			}
		}
		copied, err := closedRows(rows, resource)
		if err != nil {
			return nil, err
		}
		r.rows[resource] = copied
		index := map[string][]api.Record{}
		for _, row := range copied {
			identity := row["name"]
			if resource == api.FilterRule {
				identity = row["comment"]
			}
			index[identity] = append(index[identity], row)
		}
		r.byIdentity[resource] = index
	}
	return r, nil
}

func (r *APIResolver) resolve(resource api.Resource, ref ObjectRef, adding bool) (string, error) {
	if r == nil {
		return "", errors.New("trusted API reference resolver is required")
	}
	if ref.ID != "" && !internalID.MatchString(ref.ID) {
		return "", errors.New("reference must use an internal object ID, never a display index")
	}
	if adding && ref.ID != "" {
		return "", errors.New("new objects cannot already have an ID")
	}
	if resource == api.FilterRule {
		if ref.Name != "" || ref.Chain == "" || ref.Comment == "" {
			return "", errors.New("filter reference requires an exact chain and comment")
		}
	} else if ref.Name == "" || ref.Chain != "" || ref.Comment != "" {
		return "", errors.New("named reference requires an exact name within its menu")
	}
	identity := ref.Name
	if resource == api.FilterRule {
		identity = ref.Comment
	}
	matches := r.byIdentity[resource][identity]
	if resource == api.FilterRule {
		for _, row := range matches {
			// A marker moved to another chain remains a collision, including
			// when another row with the same marker is on the expected chain.
			if row["chain"] != ref.Chain {
				return "", errors.New("filter reference has conflicting chain evidence")
			}
		}
	}
	if adding {
		if len(matches) != 0 {
			return "", errors.New("planned new object already exists; reconcile before retry")
		}
		return "", nil
	}
	if len(matches) != 1 {
		return "", errors.New("object reference is missing or ambiguous in the completed read")
	}
	id := matches[0][".id"]
	if !internalID.MatchString(id) {
		return "", errors.New("resolved object has no valid internal ID")
	}
	if ref.ID != "" && !strings.EqualFold(ref.ID, id) {
		return "", errors.New("object ID changed since the reviewed baseline")
	}
	return id, nil
}

// Mutation resolves exactly one already validated operation. It is deliberately
// not an API that accepts arbitrary CompiledCommand attributes from a caller.
// A returned request is encodable, but every safety/native gate remains required.
func (b *APIBridge) Mutation(operationID string, resolver *APIResolver) (APIMutation, error) {
	if b == nil || resolver == nil {
		return APIMutation{}, errors.New("typed bridge and trusted resolver are required")
	}
	if b.target != resolver.target {
		return APIMutation{}, errors.New("resolver belongs to a different reviewed target")
	}
	cmd, ok := b.commands[operationID]
	if !ok {
		return APIMutation{}, errors.New("operation is not part of this canonical plan")
	}
	resource, err := apiResource(cmd.Resource)
	if err != nil {
		return APIMutation{}, err
	}
	id, err := resolver.resolve(resource, cmd.Object, cmd.Action == Add)
	if err != nil {
		return APIMutation{}, err
	}
	if cmd.Phase == DiscardPrepared {
		identity := cmd.Object.Name
		if resource == api.FilterRule {
			identity = cmd.Object.Comment
		}
		rows := resolver.byIdentity[resource][identity]
		if len(rows) != 1 {
			return APIMutation{}, errors.New("prepared discard target is not uniquely observed")
		}
		checksum, err := PreparedDiscardAttributesChecksum(resource, rows[0])
		if err != nil || checksum != cmd.Object.StateChecksum {
			return APIMutation{}, errors.New("prepared discard target changed after its authenticated checkpoint")
		}
	}
	request, err := mutationRequest(cmd, id)
	if err != nil {
		return APIMutation{}, err
	}
	out := APIMutation{PlanChecksum: b.checksum, OperationID: operationID, Phase: cmd.Phase, Request: request, RequiresTargetValidation: true, RequiresStateVerification: true, CrossChannelIdentityVerified: resolver.crossChannelVerified, NativeChecks: []string{"exact-version-api-semantics"}}
	if cmd.Resource == RoutingTable && cmd.Action == Add {
		out.NativeChecks = append(out.NativeChecks, "native-non-fib-unset-readback")
	}
	if cmd.Action == Unset {
		out.NativeChecks = append(out.NativeChecks, "native-property-unset-and-readback")
	}
	if cmd.Resource == BGPConnection && (cmd.Action == Add || cmd.Action == Set) {
		for _, a := range cmd.Attributes {
			if a.Name == "routing-table" {
				out.NativeChecks = append(out.NativeChecks, "connection-and-instance-exact-table-readback")
				break
			}
		}
	}
	for _, a := range cmd.Attributes {
		switch {
		case a.Name == "rule":
			out.NativeChecks = append(out.NativeChecks, "filter-parser-and-cached-route-behavior")
		case a.Name == "tcp-md5-key" && a.Value == "":
			out.NativeChecks = append(out.NativeChecks, "empty-credential-clear")
		}
	}
	return out, nil
}
