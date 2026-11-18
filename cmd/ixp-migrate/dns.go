package main

import (
	"strconv"
	"strings"

	"github.com/moedove/ixp-manager/internal/provision"
)

type legacyAddressClaim struct {
	referenced bool
	valid      bool
	memberID   string
	dnsName    string
}

func (c *converter) dnsHostname(table string, r row, field string) (string, bool) {
	value := c.str(table, r, field)
	if r[field] != nil {
		if _, ok := r[field].(string); !ok {
			return "", false
		}
	}
	if value == "" {
		return "", true
	}
	name, err := provision.SafeFQDN(value)
	if err != nil {
		c.warn(table, r, field+" is not a safe DNS hostname; record needs manual migration")
		return "", false
	}
	return name, true
}

func (c *converter) monitorPreferences(vli row, out row) bool {
	valid := true
	for old, key := range map[string]string{"ipv4canping": "canPing4", "ipv6canping": "canPing6", "busyhost": "monitorBusy"} {
		c.used["vlaninterface"][strconv.FormatInt(idOf(vli), 10)+":"+old] = true
		value := vli[old]
		if value == nil || value == "" {
			continue
		}
		n, err := integer(value)
		if err != nil || (n != 0 && n != 1) {
			c.warn("vlaninterface", vli, old+" must be 0 or1; connection not converted")
			valid = false
			continue
		}
		out[key] = n == 1
	}
	return valid
}

// Allocation is established by the VLI reference, independently of whether its
// address family is enabled or whether its physical connection was convertible.
func (c *converter) addressClaim(table string, address row) legacyAddressClaim {
	if c.addressClaims == nil {
		c.addressClaims = map[string]legacyAddressClaim{}
	}
	if c.addressReferences == nil {
		c.addressReferences = map[string][]row{}
		for _, family := range []string{"ipv4address", "ipv6address"} {
			for _, vli := range c.source.Tables["vlaninterface"] {
				if id := c.num("vlaninterface", vli, family+"id"); id > 0 {
					key := family + ":" + strconv.FormatInt(id, 10)
					c.addressReferences[key] = append(c.addressReferences[key], vli)
				}
			}
		}
	}
	key := table + ":" + strconv.FormatInt(idOf(address), 10)
	if claim, ok := c.addressClaims[key]; ok {
		return claim
	}
	claim := legacyAddressClaim{valid: true}
	refs := c.addressReferences[key]
	if len(refs) > 0 {
		claim.referenced = true
	}
	if len(refs) > 1 {
		c.warn(table, address, "ambiguous VLAN-interface references; address not converted as a free pool")
		claim.valid = false
		c.unsafeAddressOwnership = true
	} else if len(refs) == 1 {
		vli := refs[0]
		vi := c.lookup("vlaninterface", vli, "virtualinterfaceid", "virtualinterface")
		if vi != nil {
			claim.memberID = c.ref("virtualinterface", vi, "custid", "members")
		}
		if claim.memberID == "" || c.ref(table, address, "vlanid", "vlans") == "" || c.num("vlaninterface", vli, "vlanid") != c.num(table, address, "vlanid") {
			c.warn(table, address, "referenced address has no unambiguous converted owner/VLAN; not converted as a free pool")
			claim.valid = false
			c.unsafeAddressOwnership = true
		}
		family := strings.TrimSuffix(table, "address")
		var validName bool
		claim.dnsName, validName = c.dnsHostname("vlaninterface", vli, family+"hostname")
		if !validName {
			// A malformed optional hostname must never erase a known allocation.
			// Keep the owner/address; warnings (and strict mode) require review.
			claim.dnsName = ""
		}
	}
	c.addressClaims[key] = claim
	return claim
}
