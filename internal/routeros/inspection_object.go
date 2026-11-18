package routeros

import (
	"errors"
	"github.com/moedove/ixp-manager/internal/routeros/api"
)

// NormalizeObservedObject only normalizes one actually read fixed-menu object.
// It provides no namespace, reference-uniqueness, identity or snapshot proof.
func NormalizeObservedObject(resource api.Resource, row api.Record, target Target, properties []VersionPropertyEvidence) (api.Record, error) {
	if err := validateTarget(target); err != nil {
		return nil, err
	}
	inv := api.Inventory{}
	switch resource {
	case api.RoutingTable:
		inv.Tables = []api.Record{row}
	case api.BGPInstance:
		inv.Instances = []api.Record{row}
	case api.BGPConnection:
		inv.Connections = []api.Record{row}
	case api.FilterRule:
		inv.FilterRules = []api.Record{row}
	default:
		return nil, errors.New("unsupported single-object resource")
	}
	normalized, err := normalizeVersionProperties(inv, target, properties)
	if err != nil {
		return nil, err
	}
	var rows []api.Record
	switch resource {
	case api.RoutingTable:
		rows = normalized.Tables
	case api.BGPInstance:
		rows = normalized.Instances
	case api.BGPConnection:
		rows = normalized.Connections
	case api.FilterRule:
		rows = normalized.FilterRules
	}
	closed, err := closedRows(rows, resource)
	if err != nil || len(closed) != 1 {
		return nil, errors.New("invalid single-object observation")
	}
	return closed[0], nil
}
