package app

import "fmt"

const resellerFanoutKind = "reseller-fanouts"

func init() {
	Schemas[resellerFanoutKind] = Schema{
		"name": textField(true), "memberId": ref("members", true), "resellerId": ref("members", true),
		"peeringPortId": ref("ports", true), "fanoutPortId": ref("ports", true), "notes": textField(false),
	}
}

type resellerFanoutEndpoint struct {
	ownerID string
	passive bool
}

// A fanout reserves physical inventory. Only existing connections on its
// peering side carry the member's VLAN and address intent.
func validateResellerFanouts(s *State) error {
	pairs := s.Resources[resellerFanoutKind]
	if len(pairs) == 0 {
		return nil
	}
	endpoints := make(map[string]resellerFanoutEndpoint, 2*len(pairs))
	for _, pair := range pairs {
		id := str(pair, "id")
		memberID, resellerID := str(pair, "memberId"), str(pair, "resellerId")
		member, reseller := find(s, "members", memberID), find(s, "members", resellerID)
		if memberID == resellerID || member == nil || reseller == nil || str(member, "resellerId") != resellerID || reseller["isReseller"] != true {
			return fmt.Errorf("reseller fanout %s requires the member's directly designated reseller", id)
		}
		peeringID, fanoutID := str(pair, "peeringPortId"), str(pair, "fanoutPortId")
		if peeringID == fanoutID {
			return fmt.Errorf("reseller fanout %s requires distinct physical endpoints", id)
		}
		for _, side := range []struct {
			portID, ownerID string
			passive         bool
		}{{peeringID, memberID, false}, {fanoutID, resellerID, true}} {
			port := find(s, "ports", side.portID)
			if port == nil {
				return fmt.Errorf("reseller fanout %s requires existing physical ports", id)
			}
			if owner := str(port, "memberId"); owner != "" && owner != side.ownerID {
				return fmt.Errorf("reseller fanout endpoint %s is assigned to another member", side.portID)
			}
			if port["coreReserved"] == true {
				return fmt.Errorf("reseller fanout endpoint %s is reserved for core inventory", side.portID)
			}
			if _, exists := endpoints[side.portID]; exists {
				return fmt.Errorf("physical port %s is already used by a reseller fanout", side.portID)
			}
			endpoints[side.portID] = resellerFanoutEndpoint{ownerID: side.ownerID, passive: side.passive}
		}
	}
	connected := make(map[string]bool, len(pairs))
	for _, connection := range s.Resources["connections"] {
		ports, err := connectionPortIDs(connection)
		if err != nil {
			return fmt.Errorf("cannot validate reseller fanout against connection %s: %w", str(connection, "id"), err)
		}
		for _, portID := range ports {
			endpoint, exists := endpoints[portID]
			if !exists {
				continue
			}
			if endpoint.passive {
				return fmt.Errorf("passive reseller fanout port %s cannot be used by an access connection", portID)
			}
			if str(connection, "memberId") != endpoint.ownerID {
				return fmt.Errorf("reseller fanout peering port %s requires connections owned by its member", portID)
			}
			connected[portID] = true
		}
	}
	for _, pair := range pairs {
		if portID := str(pair, "peeringPortId"); !connected[portID] {
			return fmt.Errorf("reseller fanout peering port %s requires an existing member access connection", portID)
		}
	}
	for _, patch := range s.Resources["patch-ports"] {
		portID, owner := str(patch, "switchPortId"), str(patch, "memberId")
		if endpoint, exists := endpoints[portID]; exists && owner != "" && owner != endpoint.ownerID {
			return fmt.Errorf("reseller fanout endpoint %s has a patch allocation for another member", portID)
		}
	}
	for _, link := range s.Resources["core-links"] {
		for _, field := range []string{"portAId", "portBId"} {
			if portID := str(link, field); endpoints[portID].ownerID != "" {
				return fmt.Errorf("reseller fanout endpoint %s is used by an unbundled core link", portID)
			}
		}
	}
	for _, bundle := range s.CoreBundles {
		for _, portID := range corePorts(bundle) {
			if endpoints[portID].ownerID != "" {
				return fmt.Errorf("reseller fanout endpoint %s is used by a core bundle", portID)
			}
		}
	}
	return nil
}
