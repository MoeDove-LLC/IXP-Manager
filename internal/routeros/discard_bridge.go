package routeros

import (
	"container/heap"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"sort"
	"strings"

	"github.com/moedove/ixp-manager/internal/routeros/api"
)

// PreparedDiscardObject is supplied by an authenticated journal's successful
// Prepare readbacks. Attributes are the final prepared, non-secret values. This
// DTO alone is not an ownership grant or an observed global snapshot.
type PreparedDiscardObject struct {
	PrepareOperationID string       `json:"prepareOperationId"`
	Resource           api.Resource `json:"resource"`
	ID                 string       `json:"id"`
	Attributes         api.Record   `json:"attributes"`
}

func (PreparedDiscardObject) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "RouterOS prepared object (attributes redacted)")
}

func PreparedDiscardOperationID(prepareID string) string { return "discard-" + prepareID }

// PreparedDiscardAttributesChecksum uses the same explicit-value normalization
// as the journal. It never inserts an absent property. Full-length address
// prefixes and an explicitly empty default VRF may be represented differently
// by print/get, while their authenticated journal meaning is unchanged.
func PreparedDiscardAttributesChecksum(resource api.Resource, row api.Record) (string, error) {
	if _, err := ObservedAttributesChecksum(resource, row); err != nil {
		return "", err
	}
	copy := api.Record{}
	for key, value := range row {
		if key == "vrf" && (resource == api.BGPInstance || resource == api.BGPConnection) && value == "" {
			value = "main"
		}
		if key == "remote.address" || key == "local.address" || key == "router-id" {
			if address, err := netip.ParseAddr(value); err == nil {
				value = address.String()
			} else if prefix, err := netip.ParsePrefix(value); err == nil && prefix.Bits() == prefix.Addr().BitLen() {
				value = prefix.Addr().String()
			}
		}
		copy[key] = value
	}
	return ObservedAttributesChecksum(resource, copy)
}

func discardResource(op Operation) (Resource, api.Resource, bool) {
	if op.Phase != Prepare {
		return "", "", false
	}
	switch op.Kind {
	case AddTable:
		return RoutingTable, api.RoutingTable, true
	case AddInstance:
		return BGPInstance, api.BGPInstance, true
	case AddRule:
		return FilterRule, api.FilterRule, true
	case AddDisabledConnection:
		return BGPConnection, api.BGPConnection, true
	}
	return "", "", false
}

func discardKey(resource api.Resource, id string) string {
	return string(resource) + "/" + strings.ToUpper(id)
}

// NewPreparedDiscardBridge exposes only inverse removals of original Prepare
// additions. It never changes the original plan or authorizes cleanup of its
// baseline/retired objects. The controller must separately verify journal
// provenance, full checkpoint/credential HMACs and every actual readback.
func NewPreparedDiscardBridge(plan Plan, objects []PreparedDiscardObject) (*APIBridge, error) {
	if err := ValidatePlan(plan); err != nil {
		return nil, err
	}
	adds := map[string]Operation{}
	indices := map[string]int{}
	for index, op := range plan.Operations {
		if _, _, ok := discardResource(op); ok {
			adds[op.ID], indices[op.ID] = op, index
		}
	}
	if len(objects) != len(adds) || len(objects) > MaxPrepareOperations {
		return nil, errors.New("prepared discard must cover exactly the original Prepare additions")
	}
	baseline := map[string]bool{}
	addBase := func(resource api.Resource, ref ObjectRef) {
		if ref.ID != "" {
			baseline[discardKey(resource, ref.ID)] = true
		}
	}
	addBase(api.RoutingTable, plan.Base.Table)
	addBase(api.BGPInstance, plan.Base.Instance)
	addBase(api.FilterRule, plan.Base.InputEntry)
	addBase(api.FilterRule, plan.Base.OutputEntry)
	for _, c := range plan.Base.Connections {
		addBase(api.BGPConnection, c.Ref)
	}
	for _, generation := range plan.Base.Generations {
		for _, ref := range generation.Rules {
			addBase(api.FilterRule, ref)
		}
	}
	byID, byOperation := map[string]bool{}, map[string]PreparedDiscardObject{}
	tableID, instanceID := plan.Base.Table.ID, plan.Base.Instance.ID
	for _, supplied := range objects {
		op, exists := adds[supplied.PrepareOperationID]
		_, resource, adding := discardResource(op)
		key := discardKey(supplied.Resource, supplied.ID)
		if !exists || !adding || resource != supplied.Resource || !internalID.MatchString(supplied.ID) || supplied.ID != strings.ToUpper(supplied.ID) || byID[key] || baseline[key] || byOperation[op.ID].PrepareOperationID != "" {
			return nil, errors.New("invalid, duplicate or baseline prepared discard identity")
		}
		if _, suppliedID := supplied.Attributes[".id"]; suppliedID {
			return nil, errors.New("prepared discard attributes must keep native identity separate")
		}
		byID[key] = true
		copy := PreparedDiscardObject{PrepareOperationID: supplied.PrepareOperationID, Resource: resource, ID: supplied.ID, Attributes: api.Record{}}
		for field, value := range supplied.Attributes {
			copy.Attributes[field] = value
		}
		byOperation[op.ID] = copy
		if resource == api.RoutingTable {
			tableID = supplied.ID
		}
		if resource == api.BGPInstance {
			instanceID = supplied.ID
		}
	}
	b := &APIBridge{target: plan.Target, checksum: plan.Checksum, commands: map[string]CompiledCommand{}, order: []string{}}
	peers := map[string]Peer{}
	for _, peer := range plan.Desired.Peers {
		peers[peer.ID] = peer
	}
	for prepareID, supplied := range byOperation {
		op := adds[prepareID]
		row := api.Record{".id": supplied.ID}
		for key, value := range supplied.Attributes {
			row[key] = value
		}
		if err := checkPreparedDiscardAttributes(plan, op, supplied.Resource, row, tableID, instanceID, peers); err != nil {
			return nil, err
		}
		ref, err := objectRef(supplied.Resource, row)
		if err != nil {
			return nil, err
		}
		ref.StateChecksum, err = PreparedDiscardAttributesChecksum(supplied.Resource, row)
		if err != nil {
			return nil, err
		}
		// Named references use name+native ID; comments are not lookup selectors.
		if supplied.Resource != api.FilterRule {
			ref.Comment = ""
		}
		resource, _, _ := discardResource(op)
		id := PreparedDiscardOperationID(prepareID)
		cmd := CompiledCommand{OperationID: id, Phase: DiscardPrepared, Resource: resource, Action: Remove, Object: ref, Attributes: []Attribute{}}
		if _, err = mutationRequest(cmd, supplied.ID); err != nil {
			return nil, err
		}
		b.commands[id] = cmd
	}
	ordered, err := preparedDiscardOrder(adds, indices)
	if err != nil {
		return nil, err
	}
	for _, op := range ordered {
		b.order = append(b.order, PreparedDiscardOperationID(op.ID))
	}
	return b, nil
}

func checkPreparedDiscardAttributes(plan Plan, op Operation, resource api.Resource, row api.Record, tableID, instanceID string, peers map[string]Peer) error {
	if _, err := ObservedAttributesChecksum(resource, row); err != nil {
		return err
	}
	for _, key := range inspectFields[resource] {
		if key == "used" || key == "inactive" && resource != api.FilterRule {
			continue
		}
		if err := required(row, resource, key); err != nil {
			return err
		}
	}
	refers := func(value, id string) bool { return value == plan.Owner || id != "" && strings.EqualFold(value, id) }
	switch resource {
	case api.RoutingTable:
		if err := falseFlags(row, resource, "fib", "disabled", "dynamic", "invalid"); err != nil {
			return err
		}
		if row["name"] != plan.Owner || row["comment"] != "" {
			return errors.New("prepared table does not match its creation plan")
		}
	case api.BGPInstance:
		if err := falseFlags(row, resource, "disabled"); err != nil {
			return err
		}
		if row["name"] != plan.Owner || row["as"] != number(plan.Desired.ASN) || row["router-id"] != plan.Desired.RouterID || !refers(row["routing-table"], tableID) || row["vrf"] != "main" || row["comment"] != "" {
			return errors.New("prepared instance does not match its creation plan")
		}
	case api.FilterRule:
		if err := falseFlags(row, resource, "disabled", "inactive"); err != nil {
			return err
		}
		body, err := compileRule(*op.Rule, peers)
		if err != nil || row["chain"] != op.Rule.Chain || row["comment"] != op.Rule.Comment || row["rule"] != body {
			return errors.New("prepared rule does not match its creation plan")
		}
	case api.BGPConnection:
		disabled, known := boolObserved(row, "disabled")
		if !known || !disabled || !refers(row["instance"], instanceID) || !refers(row["routing-table"], tableID) {
			return errors.New("prepared connection is not an inert owned addition")
		}
		checked := api.Record{}
		for key, value := range row {
			checked[key] = value
		}
		checked["instance"], checked["routing-table"] = plan.Owner, plan.Owner
		got, err := inspectConnection(checked, plan.Owner, KnownPeer{ID: op.PeerID, Authentication: op.Connection.Authentication})
		if err != nil || got != *op.Connection {
			return errors.New("prepared connection does not match the complete prepared plan")
		}
	default:
		return errors.New("unsupported prepared discard resource")
	}
	return nil
}

type discardIndexHeap []int

func (h discardIndexHeap) Len() int           { return len(h) }
func (h discardIndexHeap) Less(i, j int) bool { return h[i] > h[j] }
func (h discardIndexHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *discardIndexHeap) Push(v any)        { *h = append(*h, v.(int)) }
func (h *discardIndexHeap) Pop() any {
	old := *h
	v := old[len(old)-1]
	*h = old[:len(old)-1]
	return v
}

func preparedDiscardOrder(adds map[string]Operation, indices map[string]int) ([]Operation, error) {
	connections, instances, tables := []Operation{}, []Operation{}, []Operation{}
	rules, chains := map[int]Operation{}, map[string][]int{}
	for id, op := range adds {
		switch op.Kind {
		case AddDisabledConnection:
			connections = append(connections, op)
		case AddInstance:
			instances = append(instances, op)
		case AddTable:
			tables = append(tables, op)
		case AddRule:
			index := indices[id]
			rules[index] = op
			chains[op.Rule.Chain] = append(chains[op.Rule.Chain], index)
		}
	}
	for _, group := range [][]Operation{connections, instances, tables} {
		sort.Slice(group, func(i, j int) bool { return indices[group[i].ID] > indices[group[j].ID] })
	}
	// Track references per chain rather than materializing referrer×rule
	// edges. Thousands of recipients may share one source chain.
	references := map[string]int{}
	for _, op := range rules {
		if op.Rule.JumpTarget == "" {
			continue
		}
		if len(chains[op.Rule.JumpTarget]) > 0 {
			references[op.Rule.JumpTarget]++
		}
	}
	ready := &discardIndexHeap{}
	heap.Init(ready)
	for chain, members := range chains {
		if references[chain] == 0 {
			for _, index := range members {
				heap.Push(ready, index)
			}
		}
	}
	result := append([]Operation{}, connections...)
	count := 0
	for ready.Len() > 0 {
		index := heap.Pop(ready).(int)
		result = append(result, rules[index])
		count++
		target := rules[index].Rule.JumpTarget
		if references[target] > 0 {
			references[target]--
			if references[target] == 0 {
				for _, next := range chains[target] {
					heap.Push(ready, next)
				}
			}
		}
	}
	if count != len(rules) {
		return nil, errors.New("prepared rule dependency cycle")
	}
	result = append(result, instances...)
	return append(result, tables...), nil
}
