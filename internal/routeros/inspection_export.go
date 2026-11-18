package routeros

import (
	"errors"

	"github.com/moedove/ixp-manager/internal/routeros/api"
)

// NormalizeObservedInventory verifies identity and copies the complete fixed
// read dictionaries through the authoritative version/property normalization.
// It does not prove namespace ownership or fill an absent property by guessing.
func NormalizeObservedInventory(inv api.Inventory, target Target, evidence TargetEvidence) (api.Inventory, error) {
	if err := validateTarget(target); err != nil {
		return api.Inventory{}, err
	}
	if _, err := observedIdentity(inv, evidence, target); err != nil {
		return api.Inventory{}, err
	}
	normalized, err := normalizeTargetProperties(inv, target, evidence)
	if err != nil {
		return api.Inventory{}, err
	}
	menus := []struct {
		resource api.Resource
		rows     *[]api.Record
	}{{api.RoutingTable, &normalized.Tables}, {api.BGPInstance, &normalized.Instances}, {api.BGPConnection, &normalized.Connections}, {api.FilterRule, &normalized.FilterRules}}
	count, size := 0, 0
	for _, menu := range menus {
		if inv.Capabilities[menu.resource] != api.Supported {
			return api.Inventory{}, errors.New("incomplete normalized inventory")
		}
		count += len(*menu.rows)
		if count > MaxPrepareOperations+20000 {
			return api.Inventory{}, errors.New("normalized inventory row limit")
		}
		for _, row := range *menu.rows {
			for k, v := range row {
				size += len(k) + len(v)
				if size > 64<<20 {
					return api.Inventory{}, errors.New("normalized inventory byte limit")
				}
			}
		}
		copied, err := closedRows(*menu.rows, menu.resource)
		if err != nil {
			return api.Inventory{}, err
		}
		*menu.rows = copied
	}
	// Identity metadata is not a mutable view of the caller's dictionaries.
	copyRows := func(rows []api.Record) []api.Record {
		out := make([]api.Record, len(rows))
		for i, row := range rows {
			out[i] = api.Record{}
			for k, v := range row {
				out[i][k] = v
			}
		}
		return out
	}
	normalized.Resources = copyRows(inv.Resources)
	normalized.Licenses = copyRows(inv.Licenses)
	normalized.RouterBoards = copyRows(inv.RouterBoards)
	normalized.Identities = copyRows(inv.Identities)
	normalized.Capabilities = map[api.Resource]api.Capability{}
	for k, v := range inv.Capabilities {
		normalized.Capabilities[k] = v
	}
	if inv.SafeMode != nil {
		s := *inv.SafeMode
		normalized.SafeMode = &s
	}
	return normalized, nil
}

func ManagedConnectionName(routerID, peerID string) (string, error) {
	if !identifier.MatchString(routerID) || !identifier.MatchString(peerID) {
		return "", errors.New("invalid managed identity")
	}
	return connectionName(OwnerName(routerID), peerID), nil
}
