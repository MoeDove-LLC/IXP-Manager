package app

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

func recordReferences(r Record, key, id string) bool {
	if value, ok := r[key].(string); ok {
		return value == id
	}
	if values, ok := r[key].([]any); ok {
		for _, value := range values {
			if value == id {
				return true
			}
		}
	}
	return false
}

func connectionPortIDs(r Record) ([]string, error) {
	single, hasSingle := r["portId"]
	multiple, hasMultiple := r["portIds"]
	if hasSingle && hasMultiple {
		return nil, fmt.Errorf("provide portId or portIds, never both")
	}
	if hasSingle {
		value, ok := single.(string)
		if !ok || !identifier.MatchString(value) {
			return nil, fmt.Errorf("portId must reference a physical port")
		}
		return []string{value}, nil
	}
	if !hasMultiple {
		return nil, fmt.Errorf("at least one physical port is required")
	}
	values, ok := multiple.([]any)
	if !ok || len(values) < 1 || len(values) > 64 {
		return nil, fmt.Errorf("portIds requires 1..64 physical ports")
	}
	result := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, raw := range values {
		value, ok := raw.(string)
		if !ok || !identifier.MatchString(value) || seen[value] {
			return nil, fmt.Errorf("portIds must contain distinct physical port IDs")
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

// connectionCapacity never substitutes a declared logical aggregate for missing
// physical speeds. A zero return is deliberately unknown, not a fabricated sum.
func connectionCapacity(s *State, r Record) (float64, bool) {
	ids, err := connectionPortIDs(r)
	if err != nil {
		return 0, false
	}
	sum := float64(0)
	for _, id := range ids {
		port := find(s, "ports", id)
		speed := number(port, "speed")
		if port == nil || speed <= 0 {
			return 0, false
		}
		sum += speed
	}
	return sum, true
}
func validateNetworkConnection(s *State, r Record) error {
	ids, err := connectionPortIDs(r)
	if err != nil {
		return err
	}
	mode := str(r, "linkMode")
	if mode == "" {
		mode = "single"
		if len(ids) > 1 {
			mode = "lag"
		}
		r["linkMode"] = mode
	}
	if mode == "single" && len(ids) != 1 {
		return fmt.Errorf("single mode requires exactly one physical port")
	}
	if mode != "single" && mode != "lag" {
		return fmt.Errorf("linkMode must be single or lag")
	}
	own := map[string]bool{}
	for _, id := range ids {
		port := find(s, "ports", id)
		if port == nil {
			return fmt.Errorf("portIds references missing ports/%s", id)
		}
		if member := str(port, "memberId"); member != "" && member != str(r, "memberId") {
			return fmt.Errorf("physical port belongs to another member")
		}
		if str(r, "status") == "active" && str(port, "status") == "inactive" {
			return fmt.Errorf("an active connection cannot use an explicitly inactive physical port")
		}
		own[id] = true
	}
	if declared := number(r, "speed"); declared > 0 {
		capacity, known := connectionCapacity(s, r)
		if !known {
			return fmt.Errorf("set each physical port speed before asserting aggregate speed")
		}
		if declared != capacity {
			return fmt.Errorf("connection speed must equal the physical port sum (%g Mbps)", capacity)
		}
	}
	for _, other := range s.Resources["connections"] {
		if str(other, "id") == str(r, "id") {
			continue
		}
		otherPorts, e := connectionPortIDs(other)
		if e != nil {
			return fmt.Errorf("existing connection %s has invalid physical ports", str(other, "id"))
		}
		overlaps := false
		for _, id := range otherPorts {
			if own[id] {
				overlaps = true
				break
			}
		}
		if overlaps {
			if str(other, "memberId") != str(r, "memberId") {
				return fmt.Errorf("physical port already belongs to another member connection")
			}
			if str(other, "vlanId") == str(r, "vlanId") {
				return fmt.Errorf("physical port already connected to this VLAN")
			}
			if strings.Join(otherPorts, ",") != strings.Join(ids, ",") {
				return fmt.Errorf("overlapping bundles on different VLANs must use the identical physical port set")
			}
			otherMode := str(other, "linkMode")
			if otherMode == "" {
				otherMode = "single"
				if len(otherPorts) > 1 {
					otherMode = "lag"
				}
			}
			if otherMode != mode {
				return fmt.Errorf("shared physical bundle must have the same link mode across VLANs")
			}
			if number(r, "mtu") > 0 && number(other, "mtu") > 0 && number(r, "mtu") != number(other, "mtu") {
				return fmt.Errorf("shared physical bundle must have the same MTU across VLANs")
			}
		}
		if str(other, "vlanId") == str(r, "vlanId") {
			for _, field := range []string{"ipv4", "ipv6"} {
				if str(r, field) != "" && str(r, field) == str(other, field) {
					return fmt.Errorf("%s already allocated on VLAN", field)
				}
			}
		}
	}
	vlan := find(s, "vlans", str(r, "vlanId"))
	for _, pair := range [][2]string{{"ipv4", "ipv4Prefix"}, {"ipv6", "ipv6Prefix"}} {
		if str(r, pair[0]) == "" {
			continue
		}
		address, e := netip.ParseAddr(str(r, pair[0]))
		if e != nil || !vlanContains(vlan, address) {
			return fmt.Errorf("%s must be inside VLAN prefix", pair[0])
		}
	}
	for _, router := range s.Resources["routers"] {
		if str(router, "vlanId") != str(r, "vlanId") {
			continue
		}
		if (str(r, "ipv4") != "" && str(r, "ipv4") == str(router, "localIpv4")) || (str(r, "ipv6") != "" && str(r, "ipv6") == str(router, "localIpv6")) {
			return fmt.Errorf("connection address is reserved by a router")
		}
	}
	return nil
}

// decorateConnection exposes only the physical ports already referenced by a
// visible connection. It does not grant member accounts access to inventory.
func decorateConnection(s *State, r Record) Record {
	out := clean(r)
	vlan := find(s, "vlans", str(r, "vlanId"))
	out["vlanName"] = str(vlan, "name")
	out["infrastructureId"] = fabricID(vlan)
	out["infrastructureName"] = fabricName(s, fabricID(vlan))
	capacity, known := connectionCapacity(s, r)
	out["capacityKnown"] = known
	if known {
		out["capacityMbps"] = capacity
	}
	details := []Record{}
	ids, _ := connectionPortIDs(r)
	for _, id := range ids {
		port := find(s, "ports", id)
		sw := find(s, "switches", str(port, "switchId"))
		detail := Record{"id": id, "name": str(port, "name"), "switchName": str(sw, "name")}
		if number(port, "speed") > 0 {
			detail["speed"] = number(port, "speed")
		}
		details = append(details, detail)
	}
	out["portDetails"] = details
	return out
}
