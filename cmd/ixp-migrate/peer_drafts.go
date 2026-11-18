package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type peerDraftOptions struct {
	Enabled     bool
	LimitSchema string
	AsOf        string
}

func (o peerDraftOptions) validate() error {
	if !o.Enabled {
		if o.LimitSchema != "" || o.AsOf != "" {
			return errors.New("peer source declarations require --peer-drafts")
		}
		return nil
	}
	date, err := time.Parse("2006-01-02", o.AsOf)
	if err != nil || date.Format("2006-01-02") != o.AsOf || o.LimitSchema != "shared" && o.LimitSchema != "split" {
		return errors.New("--peer-drafts requires --peer-limit-schema shared|split and --peer-as-of YYYY-MM-DD")
	}
	return nil
}
func (o peerDraftOptions) validateColumns(source legacyExport) error {
	if !o.Enabled {
		return nil
	}
	for _, r := range source.Tables["vlaninterface"] {
		_, shared := r["maxbgpprefix"]
		_, v4 := r["ipv4maxbgpprefix"]
		_, v6 := r["ipv6maxbgpprefix"]
		if o.LimitSchema == "shared" && (v4 || v6) || o.LimitSchema == "split" && shared {
			return errors.New("peer max-prefix source columns contradict the declared schema; no mixed-era inference allowed")
		}
	}
	if o.LimitSchema == "shared" {
		for _, r := range source.Tables["cust"] {
			if _, exists := r["maxprefixesv6"]; exists {
				return errors.New("member IPv6 max-prefix column contradicts shared source schema")
			}
		}
	}
	return nil
}
func (c *converter) peerUsed(table string, r row, key string) {
	c.used[table][strconv.FormatInt(idOf(r), 10)+":"+key] = true
}
func (c *converter) peerInteger(table string, r row, key string) (int64, bool) {
	c.peerUsed(table, r, key)
	if _, bad := r[key].(bool); bad {
		return 0, false
	}
	n, err := integer(r[key])
	return n, err == nil
}
func (c *converter) peerFlag(table string, r row, key string) (bool, bool) {
	c.peerUsed(table, r, key)
	if b, ok := r[key].(bool); ok {
		return b, true
	}
	n, ok := c.peerInteger(table, r, key)
	return n == 1, ok && (n == 0 || n == 1)
}
func peerSourceDate(raw any) (string, bool) {
	v, ok := raw.(string)
	if !ok {
		return "", false
	}
	if len(v) == 19 && strings.HasSuffix(v, " 00:00:00") {
		v = v[:10]
	}
	t, err := time.Parse("2006-01-02", v)
	return v, err == nil && t.Format("2006-01-02") == v
}
func (c *converter) peerMemberEligible(member row) (bool, string) {
	status, ok := c.peerInteger("cust", member, "status")
	if !ok || status != 1 && status != 2 {
		return false, "source member is not explicitly normal/not-connected"
	}
	kind, ok := c.peerInteger("cust", member, "type")
	if !ok || kind != 1 && kind != 3 && kind != 4 {
		return false, "source member is associate or has no recognized traffic-bearing type"
	}
	c.peerUsed("cust", member, "datejoin")
	c.peerUsed("cust", member, "dateleave")
	joined, validJoin := peerSourceDate(member["datejoin"])
	leave, exists := member["dateleave"]
	if !validJoin || !exists {
		return false, "source membership DATE evidence is missing/invalid; current ownership is not assumed"
	}
	if joined > c.peerOptions.AsOf {
		return false, "source member has not joined by the declared source date"
	}
	if leave != nil {
		left, valid := peerSourceDate(leave)
		if !valid {
			return false, "source leave DATE is not explicit null or a valid date"
		}
		if left < c.peerOptions.AsOf {
			return false, "source member already left by the declared source date"
		}
	}
	return true, ""
}

// Only known stock bodies with an actual import limit are representable. The
// stock BIRD 2 collectors omit import limits; required maxPrefixes cannot be
// invented from a database value that those templates did not actually apply.
func peerDraftTemplate(router row, source row) (bool, string) {
	template, ok := source["template"].(string)
	if !ok {
		return false, "source template is missing; no session shape is assumed"
	}
	platform, role := router["platform"], router["role"]
	if role == "route-collector" {
		if platform == "bird1" && template == "api/v4/router/collector/bird/standard" {
			return true, ""
		}
		if template == "api/v4/router/collector/bird2/standard" || template == "api/v4/router/collector/bird2-2025/standard" {
			return false, "stock BIRD 2 collector has no effective import limit; required peer maxPrefixes cannot be fabricated"
		}
		return false, "collector template/platform has no reviewed bounded peer-draft mapping"
	}
	if role == "route-server" {
		if platform == "bird1" && template == "api/v4/router/server/bird/standard" {
			return true, ""
		}
		if (platform == "bird2" || platform == "bird3") && (template == "api/v4/router/server/bird2/standard" || template == "api/v4/router/server/bird2-2025/standard") {
			return true, ""
		}
	}
	return false, "router template/platform/role has no reviewed peer-draft mapping; role and software are not coerced"
}

func (c *converter) peerLimit(vli, member row, family int64) (int64, string) {
	local, global := "maxbgpprefix", "maxprefixes"
	if c.peerOptions.LimitSchema == "split" {
		local = fmt.Sprintf("ipv%dmaxbgpprefix", family)
		if family == 6 {
			global = "maxprefixesv6"
		}
	}
	c.peerUsed("vlaninterface", vli, local)
	v, exists := vli[local]
	if !exists {
		return 0, "per-VLI max-prefix evidence is absent; inheritance is not assumed"
	}
	if v != nil {
		n, ok := c.peerInteger("vlaninterface", vli, local)
		if !ok || n < 0 {
			return 0, "per-VLI max-prefix value is invalid"
		}
		if n > 0 {
			if n > 10000000 {
				return 0, "effective per-VLI max-prefix limit exceeds the current peer range"
			}
			return n, ""
		}
	}
	c.peerUsed("cust", member, global)
	if _, exists := member[global]; !exists {
		return 0, "member max-prefix evidence is absent; external defaults are not inferred"
	}
	if member[global] == nil {
		return 0, "effective max-prefix limit requires unavailable source configuration defaults"
	}
	n, ok := c.peerInteger("cust", member, global)
	if !ok || n < 0 {
		return 0, "member max-prefix value is invalid"
	}
	if n == 0 {
		return 0, "effective max-prefix limit requires unavailable source configuration defaults"
	}
	if n > 10000000 {
		return 0, "effective member max-prefix limit exceeds the current peer range"
	}
	return n, ""
}

func (c *converter) peerAddressOwners() map[string]map[int64]bool {
	owners := map[string]map[int64]bool{}
	for _, vli := range c.source.Tables["vlaninterface"] {
		for _, family := range []int64{4, 6} {
			table := fmt.Sprintf("ipv%daddress", family)
			id, ok := c.peerInteger("vlaninterface", vli, table+"id")
			if !ok || id < 1 {
				continue
			}
			a := c.index[table][strconv.FormatInt(id, 10)]
			if a == nil {
				continue
			}
			vlan, ok := c.peerInteger(table, a, "vlanid")
			if !ok || vlan < 1 {
				continue
			}
			text, ok := a["address"].(string)
			if !ok {
				continue
			}
			address, ok := routerSourceAddress(text, family == 4)
			if !ok {
				continue
			}
			key := legacyID("vlans", vlan) + "/" + address.String()
			if owners[key] == nil {
				owners[key] = map[int64]bool{}
			}
			owners[key][idOf(vli)] = true
		}
	}
	return owners
}

func (c *converter) peerConnectionTopology(viID int64, memberID string, connection row) bool {
	expected := map[string]bool{}
	if port, ok := connection["portId"].(string); ok {
		expected[port] = true
	}
	if ports, ok := connection["portIds"].([]string); ok {
		for _, port := range ports {
			expected[port] = true
		}
	}
	seen := map[string]bool{}
	for _, physical := range c.source.Tables["physicalinterface"] {
		owner, valid := c.peerInteger("physicalinterface", physical, "virtualinterfaceid")
		if !valid || owner != viID {
			continue
		}
		status, validStatus := c.peerInteger("physicalinterface", physical, "status")
		portID, validPort := c.peerInteger("physicalinterface", physical, "switchportid")
		if !validStatus || status != 1 || !validPort || portID < 1 {
			return false
		}
		id := legacyID("ports", portID)
		port := c.convertedRecord("ports", id)
		source := c.index["switchport"][strconv.FormatInt(portID, 10)]
		if source == nil || port == nil || seen[id] || !expected[id] || port["memberId"] != memberID {
			return false
		}
		active, known := c.peerFlag("switchport", source, "active")
		if !known || !active {
			return false
		}
		seen[id] = true
	}
	return len(expected) > 0 && len(seen) == len(expected)
}

func (c *converter) makePeerDrafts() error {
	owners := c.peerAddressOwners()
	byVLAN := map[int64][]row{}
	for _, vli := range c.source.Tables["vlaninterface"] {
		if vlan, ok := c.peerInteger("vlaninterface", vli, "vlanid"); ok && vlan > 0 {
			byVLAN[vlan] = append(byVLAN[vlan], vli)
		} else {
			c.warn("vlaninterface", vli, "peer-drafts: source VLAN identity is missing or invalid; no draft")
		}
	}
	count, considered := 0, 0
	for _, source := range c.source.Tables["routers"] {
		router := c.convertedRecord("routers", legacyID("routers", idOf(source)))
		if router == nil {
			c.warn("routers", source, "peer-drafts: no converted router identity; no peer inferred")
			continue
		}
		if ok, reason := peerDraftTemplate(router, source); !ok {
			c.warn("routers", source, "peer-drafts: "+reason)
			continue
		}
		family, _ := c.routerInteger(source, "protocol")
		vlan, _ := c.routerInteger(source, "vlan_id")
		familyKey := fmt.Sprintf("ipv%d", family)
		for _, vli := range byVLAN[vlan] {
			considered++
			if considered > 100000 {
				return errors.New("peer draft candidate work exceeds bound; no partial import file produced")
			}
			report := func(reason string) {
				c.warn("vlaninterface", vli, fmt.Sprintf("peer-drafts router id %d: %s; no draft", idOf(source), reason))
			}
			enabled, known := c.peerFlag("vlaninterface", vli, familyKey+"enabled")
			if !known {
				report("address-family enable evidence is missing or invalid")
				continue
			}
			if !enabled {
				continue
			}
			if router["role"] == "route-server" {
				client, known := c.peerFlag("vlaninterface", vli, "rsclient")
				if !known {
					report("rsclient evidence is missing or invalid")
					continue
				}
				if !client {
					continue
				}
			}
			viID, ok := c.peerInteger("vlaninterface", vli, "virtualinterfaceid")
			if !ok || viID < 1 {
				report("virtual interface identity is not explicit")
				continue
			}
			vi := c.index["virtualinterface"][strconv.FormatInt(viID, 10)]
			if vi == nil {
				report("virtual interface is unavailable")
				continue
			}
			memberID, ok := c.peerInteger("virtualinterface", vi, "custid")
			if !ok || memberID < 1 {
				report("member ownership is not explicit")
				continue
			}
			member := c.index["cust"][strconv.FormatInt(memberID, 10)]
			currentMember := c.convertedRecord("members", legacyID("members", memberID))
			if member == nil || currentMember == nil {
				report("member is not successfully converted")
				continue
			}
			if ok, reason := c.peerMemberEligible(member); !ok {
				report(reason)
				continue
			}
			asn, ok := c.peerInteger("cust", member, "autsys")
			if !ok || asn < 1 || asn > 4294967294 || asn == 23456 || asn == 65535 || currentMember["asn"] != asn {
				report("member ASN is missing, invalid or changed by conversion")
				continue
			}
			if router["asn"] == asn {
				continue
			}
			connection := c.convertedRecord("connections", legacyID("connections", idOf(vli)))
			if connection == nil || connection["status"] != "active" || connection["memberId"] != currentMember["id"] || connection["vlanId"] != router["vlanId"] {
				report("requires a fully active converted connection with the same member and VLAN; partial/disconnected topology is not guessed")
				continue
			}
			if !c.peerConnectionTopology(viID, currentMember["id"].(string), connection) {
				report("physical ownership/status evidence does not exactly match the active converted connection")
				continue
			}
			addressID, ok := c.peerInteger("vlaninterface", vli, familyKey+"addressid")
			addressRow := c.index[familyKey+"address"][strconv.FormatInt(addressID, 10)]
			if !ok || addressID < 1 || addressRow == nil {
				report("assigned address identity is missing or invalid")
				continue
			}
			addressVLAN, ok := c.peerInteger(familyKey+"address", addressRow, "vlanid")
			text, textual := addressRow["address"].(string)
			address, validAddress := routerSourceAddress(text, family == 4)
			claim := c.addressClaim(familyKey+"address", addressRow)
			if !ok || addressVLAN != vlan || !textual || !validAddress || !claim.valid || claim.memberID != currentMember["id"] || connection[familyKey] != address.String() {
				report("address/VLAN/connection ownership does not agree")
				continue
			}
			key := router["vlanId"].(string) + "/" + address.String()
			if len(owners[key]) != 1 || !owners[key][idOf(vli)] {
				report("multiple source VLAN interfaces claim this address, including disabled references")
				return errors.New("peer draft address ownership is ambiguous; no import file produced")
			}
			matches := 0
			for _, other := range c.result.Resources["connections"] {
				if other["vlanId"] == router["vlanId"] && other[familyKey] == address.String() {
					matches++
				}
			}
			if matches != 1 {
				report("converted connection ownership is not unique")
				return errors.New("peer draft connection ownership is ambiguous; no import file produced")
			}
			if address.String() == router[fmt.Sprintf("localIpv%d", family)] {
				report("neighbor address equals router local address")
				continue
			}
			limit, reason := c.peerLimit(vli, member, family)
			if reason != "" {
				report(reason)
				continue
			}
			if count == 10000 {
				return errors.New("peer drafts exceed 10000 rows; no partial import file produced")
			}
			id := fmt.Sprintf("legacy-peer-draft-r%d-vli%d-v%d", idOf(source), idOf(vli), family)
			out := row{"id": id, "name": fmt.Sprintf("Legacy %s draft R%d/VLI%d IPv%d", router["role"], idOf(source), idOf(vli), family), "routerId": router["id"], "memberId": currentMember["id"], "address": address.String(), "asn": asn, "enabled": false, "maxPrefixes": limit, "allowedPrefixes": []string{}}
			c.add("peers", vli, out)
			count++
			c.warn("vlaninterface", vli, fmt.Sprintf("peer-drafts router id %d: disabled candidate uses %s limit columns and source date %s; no approved prefixes, MD5, source filters or publication authority imported", idOf(source), c.peerOptions.LimitSchema, c.peerOptions.AsOf))
		}
	}
	c.result.Warnings = append(c.result.Warnings, fmt.Sprintf("peer-drafts: %d disabled candidates from declared %s columns at source calendar date %s; review source templates/skins, current ownership, prefix policy and MD5 before any enable", count, c.peerOptions.LimitSchema, c.peerOptions.AsOf))
	return nil
}
