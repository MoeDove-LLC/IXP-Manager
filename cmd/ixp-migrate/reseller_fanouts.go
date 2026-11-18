package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
)

func (c *converter) fanoutError(table string, r row, reason string) error {
	c.warn(table, r, reason)
	return fmt.Errorf("unsafe reseller fanout: %s id %d: %s; no safe import file can be produced", table, idOf(r), reason)
}
func (c *converter) fanoutInteger(table string, r row, field string, optional bool) (int64, error) {
	c.used[table][strconv.FormatInt(idOf(r), 10)+":"+field] = true
	value := r[field]
	if optional && (value == nil || value == "") {
		return 0, nil
	}
	var text string
	switch v := value.(type) {
	case json.Number:
		text = v.String()
	case string:
		text = v
	default:
		return 0, c.fanoutError(table, r, field+" must be an exact decimal integer")
	}
	n, err := strconv.ParseInt(text, 10, 64)
	if err != nil || strconv.FormatInt(n, 10) != text || n < 0 || (!optional && n == 0) {
		return 0, c.fanoutError(table, r, field+" must be an exact positive source reference or declared zero")
	}
	return n, nil
}
func (c *converter) fanoutReference(table string, r row, field, target string) (row, error) {
	id, err := c.fanoutInteger(table, r, field, false)
	if err != nil {
		return nil, err
	}
	linked := c.index[target][strconv.FormatInt(id, 10)]
	if linked == nil {
		return nil, c.fanoutError(table, r, "missing "+field+" source reference")
	}
	if _, err = c.fanoutInteger(target, linked, "id", false); err != nil {
		return nil, err
	}
	return linked, nil
}
func (c *converter) fanoutFlag(table string, r row, field string) (bool, error) {
	c.used[table][strconv.FormatInt(idOf(r), 10)+":"+field] = true
	value := r[field]
	if value == nil || value == "" {
		return false, nil
	}
	if flag, ok := value.(bool); ok {
		return flag, nil
	}
	n, err := c.fanoutInteger(table, r, field, true)
	if err != nil {
		return false, err
	}
	if n > 1 {
		return false, c.fanoutError(table, r, field+" must be an explicit boolean or 0/1")
	}
	return n == 1, nil
}
func convertedFanoutPorts(connection row) []string {
	if id, ok := connection["portId"].(string); ok {
		return []string{id}
	}
	if ids, ok := connection["portIds"].([]string); ok {
		return ids
	}
	return nil
}
func fanoutContains(ids []string, id string) bool {
	for _, candidate := range ids {
		if candidate == id {
			return true
		}
	}
	return false
}

// Fanout relations reserve passive reseller ports. A warning-and-skip would
// expose those old ports as free inventory, so every ambiguous relation is a
// hard conversion error, including outside --strict mode.
func (c *converter) resellerFanouts() error {
	links := map[int64]int64{}
	memberPIs := []row{}
	for _, pi := range c.source.Tables["physicalinterface"] {
		target, err := c.fanoutInteger("physicalinterface", pi, "fanout_physical_interface_id", true)
		if err != nil {
			return err
		}
		if target != 0 {
			links[idOf(pi)] = target
			memberPIs = append(memberPIs, pi)
		}
	}
	// A typed but unassociated fanout is still an occupied legacy role. It
	// cannot silently become an ordinary free port when no member can be proved.
	typedFanouts := map[int64]row{}
	for _, port := range c.source.Tables["switchport"] {
		if kind, err := integer(port["type"]); err == nil && kind == 6 {
			typedFanouts[idOf(port)] = port
		}
	}
	if len(memberPIs) == 0 && len(typedFanouts) == 0 {
		return nil
	}
	piByPort, piByVI := map[int64][]row{}, map[int64][]row{}
	for _, pi := range c.source.Tables["physicalinterface"] {
		port, err := c.fanoutInteger("physicalinterface", pi, "switchportid", true)
		if err != nil {
			return err
		}
		vi, err := c.fanoutInteger("physicalinterface", pi, "virtualinterfaceid", true)
		if err != nil {
			return err
		}
		if port != 0 {
			piByPort[port] = append(piByPort[port], pi)
		}
		if vi != 0 {
			piByVI[vi] = append(piByVI[vi], pi)
		}
	}
	vliByVI := map[int64][]row{}
	for _, vli := range c.source.Tables["vlaninterface"] {
		vi, err := c.fanoutInteger("vlaninterface", vli, "virtualinterfaceid", true)
		if err != nil {
			return err
		}
		if vi != 0 {
			vliByVI[vi] = append(vliByVI[vi], vli)
		}
	}
	sort.Slice(memberPIs, func(i, j int) bool { return idOf(memberPIs[i]) < idOf(memberPIs[j]) })
	usedPIs, usedPorts := map[int64]bool{}, map[int64]bool{}
	for _, pi := range memberPIs {
		fn, err := c.fanoutReference("physicalinterface", pi, "fanout_physical_interface_id", "physicalinterface")
		if err != nil {
			return err
		}
		if idOf(pi) == idOf(fn) || links[idOf(fn)] != 0 || usedPIs[idOf(fn)] {
			return c.fanoutError("physicalinterface", pi, "fanout chains, self-links and duplicate targets are unsupported")
		}
		vi, err := c.fanoutReference("physicalinterface", pi, "virtualinterfaceid", "virtualinterface")
		if err != nil {
			return err
		}
		fnVI, err := c.fanoutReference("physicalinterface", fn, "virtualinterfaceid", "virtualinterface")
		if err != nil {
			return err
		}
		member, err := c.fanoutReference("virtualinterface", vi, "custid", "cust")
		if err != nil {
			return err
		}
		reseller, err := c.fanoutReference("virtualinterface", fnVI, "custid", "cust")
		if err != nil {
			return err
		}
		direct, err := c.fanoutReference("cust", member, "reseller", "cust")
		if err != nil {
			return err
		}
		isReseller, err := c.fanoutFlag("cust", reseller, "isReseller")
		if err != nil {
			return err
		}
		if idOf(member) == idOf(reseller) || idOf(direct) != idOf(reseller) || !isReseller {
			return c.fanoutError("physicalinterface", pi, "fanout owner must be the member's distinct direct designated reseller")
		}
		memberID, resellerID := legacyID("members", idOf(member)), legacyID("members", idOf(reseller))
		currentMember, currentReseller := c.convertedRecord("members", memberID), c.convertedRecord("members", resellerID)
		if currentMember == nil || currentReseller == nil || currentMember["resellerId"] != resellerID || currentReseller["isReseller"] != true {
			return c.fanoutError("physicalinterface", pi, "member or direct reseller was not successfully converted")
		}
		port, err := c.fanoutReference("physicalinterface", pi, "switchportid", "switchport")
		if err != nil {
			return err
		}
		fnPort, err := c.fanoutReference("physicalinterface", fn, "switchportid", "switchport")
		if err != nil {
			return err
		}
		if idOf(port) == idOf(fnPort) || usedPorts[idOf(port)] || usedPorts[idOf(fnPort)] || len(piByPort[idOf(port)]) != 1 || len(piByPort[idOf(fnPort)]) != 1 {
			return c.fanoutError("physicalinterface", pi, "fanout endpoints must be distinct uniquely occupied physical ports")
		}
		for _, endpoint := range []struct {
			pi, vi, port row
			expectedType int64
		}{{pi, vi, port, 1}, {fn, fnVI, fnPort, 6}} {
			if c.coreGraph.pis[idOf(endpoint.pi)] || c.coreGraph.vis[idOf(endpoint.vi)] || c.coreGraph.ports[idOf(endpoint.port)] {
				return c.fanoutError("physicalinterface", pi, "core reservations cannot be converted to fanout access")
			}
			if endpoint.port["type"] != nil && endpoint.port["type"] != "" {
				kind, e := c.fanoutInteger("switchport", endpoint.port, "type", true)
				if e != nil {
					return e
				}
				if kind != endpoint.expectedType {
					return c.fanoutError("switchport", endpoint.port, "declared port role disagrees with exact peering/fanout endpoints")
				}
			}
			sw, e := c.fanoutReference("switchport", endpoint.port, "switchid", "switch")
			if e != nil {
				return e
			}
			converted := c.convertedRecord("ports", legacyID("ports", idOf(endpoint.port)))
			if converted == nil || c.convertedRecord("switches", legacyID("switches", idOf(sw))) == nil || converted["switchId"] != legacyID("switches", idOf(sw)) || converted["coreReserved"] == true {
				return c.fanoutError("switchport", endpoint.port, "physical endpoint has no exact converted non-core switch and port")
			}
		}
		if idOf(vi) == idOf(fnVI) || len(piByVI[idOf(fnVI)]) != 1 || len(vliByVI[idOf(fnVI)]) != 0 {
			return c.fanoutError("virtualinterface", fnVI, "passive fanout must have its own single-PI VI without any VLAN access attachment")
		}
		for _, flag := range []string{"lag_framing", "trunk", "fastlacp"} {
			enabled, e := c.fanoutFlag("virtualinterface", fnVI, flag)
			if e != nil {
				return e
			}
			if enabled {
				return c.fanoutError("virtualinterface", fnVI, "passive fanout VI declares unsupported logical interface behavior")
			}
		}
		channel, err := c.fanoutInteger("virtualinterface", fnVI, "channelgroup", true)
		if err != nil {
			return err
		}
		if channel != 0 {
			return c.fanoutError("virtualinterface", fnVI, "passive fanout channel group cannot be represented by a physical pair")
		}
		peeringPortID, fanoutPortID := legacyID("ports", idOf(port)), legacyID("ports", idOf(fnPort))
		if len(vliByVI[idOf(vi)]) == 0 {
			return c.fanoutError("virtualinterface", vi, "member peering VI has no proven converted VLAN connection")
		}
		for _, vli := range vliByVI[idOf(vi)] {
			connection, e := c.receiverConnection("physicalinterface", pi, vli)
			if e != nil {
				return c.fanoutError("physicalinterface", pi, "every member VLAN attachment must convert with exact member and physical-port ownership")
			}
			converted := c.convertedRecord("connections", connection.ID)
			if connection.MemberID != memberID || !fanoutContains(convertedFanoutPorts(converted), peeringPortID) {
				return c.fanoutError("physicalinterface", pi, "converted VLAN connection does not retain the member peering port")
			}
		}
		for _, connection := range c.result.Resources["connections"] {
			ports := convertedFanoutPorts(connection)
			if fanoutContains(ports, fanoutPortID) || fanoutContains(ports, peeringPortID) && connection["memberId"] != memberID {
				return c.fanoutError("physicalinterface", pi, "fanout endpoint has an access connection or peering endpoint has another owner")
			}
		}
		convertedFnPort := c.convertedRecord("ports", fanoutPortID)
		if owner := convertedFnPort["memberId"]; owner != nil && owner != "" && owner != resellerID {
			return c.fanoutError("physicalinterface", fn, "passive port is already owned by a different member")
		}
		// Carry a real configured physical speed only when discovery did not
		// already establish one. No logical VLAN/interface is synthesized.
		if fn["speed"] != nil && fn["speed"] != "" {
			speed, e := c.fanoutInteger("physicalinterface", fn, "speed", true)
			if e != nil {
				return e
			}
			if speed > 10000000 {
				return c.fanoutError("physicalinterface", fn, "passive physical speed is outside the supported range")
			}
			if convertedFnPort["speed"] == nil && speed > 0 {
				convertedFnPort["speed"] = speed
			}
		}
		convertedFnPort["memberId"] = resellerID
		out := row{"id": legacyID("reseller-fanouts", idOf(pi)), "name": fmt.Sprintf("Legacy reseller fanout %d", idOf(pi)), "memberId": memberID, "resellerId": resellerID, "peeringPortId": peeringPortID, "fanoutPortId": fanoutPortID}
		c.add("reseller-fanouts", pi, out)
		c.resolve("physicalinterface", pi)
		c.resolve("physicalinterface", fn)
		c.resolve("virtualinterface", vi)
		c.resolve("virtualinterface", fnVI)
		usedPIs[idOf(fn)] = true
		usedPorts[idOf(port)], usedPorts[idOf(fnPort)] = true, true
		delete(typedFanouts, idOf(fnPort))
	}
	if len(typedFanouts) != 0 {
		ids := []int64{}
		for id := range typedFanouts {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		return c.fanoutError("switchport", typedFanouts[ids[0]], "typed fanout port has no complete member-to-reseller relation; refusing to import it as free inventory")
	}
	return nil
}
