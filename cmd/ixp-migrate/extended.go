package main

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Pivot tables in the Laravel schema have composite keys and no synthetic id.
// Assign internal bookkeeping IDs in sorted-key order without modifying input.
func normalizePivots(source legacyExport) (legacyExport, error) {
	tables := make(map[string][]row, len(source.Tables))
	for k, v := range source.Tables {
		tables[k] = v
	}
	for table, keys := range map[string][2]string{"cust_to_cust_tag": {"customer_id", "customer_tag_id"}, "contact_to_group": {"contact_id", "contact_group_id"}} {
		rows := append([]row(nil), tables[table]...)
		pairs := map[string]bool{}
		for _, r := range rows {
			a, e1 := integer(r[keys[0]])
			b, e2 := integer(r[keys[1]])
			key := fmt.Sprintf("%d/%d", a, b)
			if e1 != nil || e2 != nil || a < 1 || b < 1 || pairs[key] {
				return legacyExport{}, fmt.Errorf("%s has invalid or duplicate composite keys", table)
			}
			pairs[key] = true
		}
		sort.Slice(rows, func(i, j int) bool {
			a, _ := integer(rows[i][keys[0]])
			b, _ := integer(rows[j][keys[0]])
			if a != b {
				return a < b
			}
			a, _ = integer(rows[i][keys[1]])
			b, _ = integer(rows[j][keys[1]])
			return a < b
		})
		copyRows := []row{}
		for i, r := range rows {
			copy := row{}
			for k, v := range r {
				copy[k] = v
			}
			if _, exists := copy["id"]; !exists {
				copy["id"] = json.Number(strconv.Itoa(i + 1))
			}
			copyRows = append(copyRows, copy)
		}
		tables[table] = copyRows
	}
	return legacyExport{Tables: tables}, nil
}
func (c *converter) date(table string, r row, field string) string {
	value := c.str(table, r, field)
	if value == "" {
		return ""
	}
	if len(value) >= 10 {
		value = value[:10]
	}
	if _, err := time.Parse("2006-01-02", value); err != nil {
		c.warn(table, r, field+" is not a valid date; preserve source value separately")
		return ""
	}
	return value
}
func (c *converter) textFields(table string, r, o row, mapping map[string]string) {
	for old, next := range mapping {
		if value := c.str(table, r, old); value != "" {
			o[next] = value
		}
	}
}
func (c *converter) membersExtended(r, o row) {
	types := map[int64]string{1: "full", 2: "associate", 3: "internal", 4: "probono"}
	if t := c.num("cust", r, "type"); t != 0 {
		if value := types[t]; value != "" {
			o["type"] = value
		} else {
			c.warn("cust", r, "unknown member type; kept as full")
			o["type"] = "full"
		}
	}
	if c.num("cust", r, "isReseller") == 1 {
		o["isReseller"] = true
	}
	c.textFields("cust", r, o, map[string]string{"shortname": "shortName", "abbreviatedName": "abbreviatedName", "nocphone": "phone", "noc24hphone": "noc24hPhone", "nocemail": "nocEmail", "nochours": "nocHours", "nocwww": "nocWebsite", "peeringmacro": "irrASSet", "peeringmacrov6": "irrASSet6"})
	if value := strings.ToLower(c.str("cust", r, "peeringpolicy")); value != "" {
		if value == "open" || value == "selective" || value == "restrictive" {
			o["peeringPolicy"] = value
		} else {
			o["peeringPolicy"] = "unknown"
			c.warn("cust", r, "unknown peering policy retained as unknown")
		}
	}
	if value := c.date("cust", r, "datejoin"); value != "" {
		o["joinedAt"] = value
	}
	if value := c.date("cust", r, "dateleave"); value != "" {
		o["leftAt"] = value
	}
	if id := c.num("cust", r, "company_registered_detail_id"); id > 0 {
		company := c.index["company_registration_detail"][strconv.FormatInt(id, 10)]
		if company == nil {
			c.warn("cust", r, "registered company record missing")
		} else {
			c.textFields("company_registration_detail", company, o, map[string]string{"registeredName": "registeredName", "companyNumber": "registrationNumber", "jurisdiction": "jurisdiction", "townCity": "city", "postcode": "postalCode", "country": "country", "notes": "registrationNotes"})
			lines := []string{}
			for _, field := range []string{"address1", "address2", "address3"} {
				if value := c.str("company_registration_detail", company, field); value != "" {
					lines = append(lines, value)
				}
			}
			if len(lines) > 0 {
				o["address"] = strings.Join(lines, "\n")
			}
			c.resolve("company_registration_detail", company)
		}
	}
	if id := c.num("cust", r, "company_billing_details_id"); id > 0 {
		company := c.index["company_billing_detail"][strconv.FormatInt(id, 10)]
		if company == nil {
			c.warn("cust", r, "billing company record missing")
		} else {
			c.textFields("company_billing_detail", company, o, map[string]string{"billingContactName": "billingName", "billingEmail": "billingEmail", "billingTownCity": "billingCity", "billingPostcode": "billingPostalCode", "billingCountry": "billingCountry", "billingTelephone": "billingPhone", "vatNumber": "vatNumber", "purchaseOrderNumber": "purchaseOrderNumber", "invoiceEmail": "invoiceEmail", "notes": "billingNotes"})
			lines := []string{}
			for _, field := range []string{"billingAddress1", "billingAddress2", "billingAddress3"} {
				if value := c.str("company_billing_detail", company, field); value != "" {
					lines = append(lines, value)
				}
			}
			if len(lines) > 0 {
				o["billingAddress"] = strings.Join(lines, "\n")
			}
			if company["purchaseOrderRequired"] != nil {
				o["purchaseOrderRequired"] = c.num("company_billing_detail", company, "purchaseOrderRequired") == 1
			}
			c.used["company_billing_detail"][strconv.FormatInt(id, 10)+":vatRate"] = true
			if value := company["vatRate"]; value != nil && value != "" {
				n, err := strconv.ParseFloat(fmt.Sprint(value), 64)
				if err != nil || n < 0 || n > 100 {
					c.warn("company_billing_detail", company, "invalid VAT rate; not converted")
				} else {
					o["vatRate"] = n
				}
			}
			if method := strings.ToLower(c.str("company_billing_detail", company, "invoiceMethod")); method != "" {
				if method == "email" || method == "post" {
					o["invoiceMethod"] = method
				} else {
					c.warn("company_billing_detail", company, "unknown invoice method")
				}
			}
			if frequency := strings.ToLower(c.str("company_billing_detail", company, "billingFrequency")); frequency != "" {
				if frequency == "nobilling" {
					frequency = "none"
				}
				switch frequency {
				case "monthly", "2monthly", "quarterly", "halfyearly", "annually", "none":
					o["billingFrequency"] = frequency
				default:
					c.warn("company_billing_detail", company, "unknown billing frequency")
				}
			}
			c.resolve("company_billing_detail", company)
		}
	}
}
func (c *converter) membersRelations() {
	for _, source := range c.source.Tables["cust"] {
		if id := c.num("cust", source, "reseller"); id > 0 {
			for _, member := range c.result.Resources["members"] {
				if member["id"] == legacyID("members", idOf(source)) {
					target := legacyID("members", id)
					if !c.exported["members"][target] {
						c.warn("cust", source, "reseller reference missing")
					} else {
						member["resellerId"] = target
					}
				}
			}
		}
	}
}
func (c *converter) extendedAfterMembers() {
	c.membersRelations()
	for _, r := range c.source.Tables["infrastructure"] {
		o := c.base("infrastructure", "infrastructures", r)
		o["name"] = c.str("infrastructure", r, "name")
		c.textFields("infrastructure", r, o, map[string]string{"shortname": "shortName", "country": "country", "notes": "notes"})
		for old, key := range map[string]string{"ixf_ix_id": "ixfId", "peeringdb_ix_id": "peeringdbId"} {
			if value := c.num("infrastructure", r, old); value > 0 {
				o[key] = value
			}
		}
		o["excludeIxf"] = true
		if r["exclude_from_ixf_export"] != nil {
			o["excludeIxf"] = c.num("infrastructure", r, "exclude_from_ixf_export") != 0
		}
		if r["isPrimary"] != nil {
			o["isPrimary"] = c.num("infrastructure", r, "isPrimary") == 1
		}
		if c.nameOK("infrastructure", r, o) {
			c.add("infrastructures", r, o)
		}
	}

	for _, r := range c.source.Tables["vendor"] {
		o := c.base("vendor", "vendors", r)
		o["name"] = c.str("vendor", r, "name")
		c.textFields("vendor", r, o, map[string]string{"shortname": "shortName", "bundle_name": "bundleName"})
		if c.nameOK("vendor", r, o) {
			c.add("vendors", r, o)
		}
	}
}
func (c *converter) extendedAfterContacts() {
	for _, r := range c.source.Tables["cust_tag"] {
		o := c.base("cust_tag", "tags", r)
		o["name"] = c.str("cust_tag", r, "tag")
		c.textFields("cust_tag", r, o, map[string]string{"display_as": "displayAs", "description": "description"})
		o["internal"] = c.legacyInternalFlag("cust_tag", r, "internal_only")
		if c.nameOK("cust_tag", r, o) {
			c.add("tags", r, o)
		}
	}
	for _, r := range c.source.Tables["cust_to_cust_tag"] {
		member := c.ref("cust_to_cust_tag", r, "customer_id", "members")
		tag := c.ref("cust_to_cust_tag", r, "customer_tag_id", "tags")
		if member == "" || tag == "" {
			c.warn("cust_to_cust_tag", r, "tag assignment references missing records")
			continue
		}
		o := row{"id": fmt.Sprintf("legacy-member-tags-%d-%d", c.num("cust_to_cust_tag", r, "customer_id"), c.num("cust_to_cust_tag", r, "customer_tag_id")), "name": "Legacy tag assignment", "memberId": member, "tagId": tag}
		c.add("member-tags", r, o)
	}
	for _, r := range c.source.Tables["cust_notes"] {
		o := c.base("cust_notes", "notes", r)
		o["name"] = c.str("cust_notes", r, "title")
		if o["name"] == "" {
			o["name"] = fmt.Sprintf("Legacy note %d", idOf(r))
		}
		o["body"] = c.str("cust_notes", r, "note")
		o["memberId"] = c.ref("cust_notes", r, "customer_id", "members")
		o["visibility"] = "internal"
		if !c.legacyInternalFlag("cust_notes", r, "private") {
			o["visibility"] = "member"
		}
		if o["body"] != "" && c.refsOK("cust_notes", r, o, "memberId") {
			c.add("notes", r, o)
		} else {
			c.warn("cust_notes", r, "empty note or missing member; not converted")
		}
	}
	for _, r := range c.source.Tables["contact_group"] {
		o := c.base("contact_group", "contact-groups", r)
		o["name"] = c.str("contact_group", r, "name")
		c.textFields("contact_group", r, o, map[string]string{"description": "description", "type": "type"})
		if r["active"] != nil {
			o["active"] = c.num("contact_group", r, "active") == 1
		}
		if r["limited_to"] != nil {
			o["limitedTo"] = c.num("contact_group", r, "limited_to")
		}
		if c.nameOK("contact_group", r, o) {
			c.add("contact-groups", r, o)
		}
	}
	for _, r := range c.source.Tables["contact_to_group"] {
		contact := c.ref("contact_to_group", r, "contact_id", "contacts")
		group := c.ref("contact_to_group", r, "contact_group_id", "contact-groups")
		if contact == "" || group == "" {
			c.warn("contact_to_group", r, "contact group assignment references missing records")
			continue
		}
		o := row{"id": fmt.Sprintf("legacy-contact-group-memberships-%d-%d", c.num("contact_to_group", r, "contact_id"), c.num("contact_to_group", r, "contact_group_id")), "name": "Legacy contact group assignment", "contactId": contact, "groupId": group}
		c.add("contact-group-memberships", r, o)
	}
	for _, r := range c.source.Tables["custkit"] {
		o := c.base("custkit", "equipment", r)
		o["name"] = c.str("custkit", r, "name")
		o["description"] = c.str("custkit", r, "descr")
		o["rackId"] = c.ref("custkit", r, "cabinetid", "racks")
		if c.nameOK("custkit", r, o) && c.refsOK("custkit", r, o, "rackId") {
			c.add("equipment", r, o)
		}
	}
}

// The source columns are SQL tinyint flags. Exporters may preserve them as
// JSON integers, booleans, or exact scalar strings. A failed numeric parse must
// never become the public zero value. Unknown and missing values stay internal.
func (c *converter) legacyInternalFlag(table string, r row, field string) bool {
	c.used[table][strconv.FormatInt(idOf(r), 10)+":"+field] = true
	var raw string
	switch value := r[field].(type) {
	case bool:
		return value
	case string:
		raw = value
	case json.Number:
		raw = value.String()
	case int:
		raw = strconv.Itoa(value)
	case int64:
		raw = strconv.FormatInt(value, 10)
	}
	switch raw {
	case "0", "false":
		return false
	case "1", "true":
		return true
	default:
		c.warn(table, r, field+" is missing or unknown; imported as internal")
		return true
	}
}
func (c *converter) addressPools() {
	for _, table := range []string{"ipv4address", "ipv6address"} {
		for _, r := range c.source.Tables[table] {
			// Enabled assignments already live on their converted connection. Other
			// references still reserve the address, including disabled address families.
			if c.resolved[table][idOf(r)] {
				continue
			}
			claim := c.addressClaim(table, r)
			if !claim.valid {
				continue
			}
			raw := c.str(table, r, "address")
			address, err := netip.ParseAddr(raw)
			if err != nil || address.Is4() != (table == "ipv4address") {
				c.warn(table, r, "invalid address-pool address")
				if claim.referenced {
					c.unsafeAddressOwnership = true
				}
				continue
			}
			vlanID := c.ref(table, r, "vlanid", "vlans")
			if vlanID == "" {
				c.warn(table, r, "address-pool VLAN missing")
				continue
			}
			valid := false
			for _, vlan := range c.result.Resources["vlans"] {
				if vlan["id"] != vlanID {
					continue
				}
				field := "ipv6Prefix"
				if address.Is4() {
					field = "ipv4Prefix"
				}
				raws := []string{fmt.Sprint(vlan[field])}
				extra, _ := vlan["prefixes"].([]string)
				raws = append(raws, extra...)
				for _, raw := range raws {
					prefix, err := netip.ParsePrefix(raw)
					if err == nil && prefix.Contains(address) {
						valid = true
					}
				}
			}
			if !valid {
				c.warn(table, r, "address is outside its converted VLAN prefix")
				if claim.referenced {
					c.unsafeAddressOwnership = true
				}
				continue
			}
			o := row{"id": fmt.Sprintf("legacy-ip-addresses-%s-%d", table, idOf(r)), "name": address.String(), "vlanId": vlanID, "address": address.String()}
			if claim.referenced {
				o["memberId"] = claim.memberID
				if claim.dnsName != "" {
					o["dnsName"] = claim.dnsName
				}
			}
			c.add("ip-addresses", r, o)
			c.resolve(table, r)
		}
	}
}
