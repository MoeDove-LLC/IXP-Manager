package main

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"unicode/utf8"

	consolemeta "github.com/moedove/ixp-manager/internal/console"
)

func (c *converter) consoleUsed(table string, r row, key string) {
	if c.used[table] == nil {
		c.used[table] = map[string]bool{}
	}
	c.used[table][strconv.FormatInt(idOf(r), 10)+":"+key] = true
}
func (c *converter) consoleError(table string, r row, detail string) error {
	c.warn(table, r, detail)
	return fmt.Errorf("console migration: %s id %d: %s; no safe import file can be produced", table, idOf(r), detail)
}
func (c *converter) consoleText(table string, r row, key string) (string, error) {
	c.consoleUsed(table, r, key)
	if r[key] == nil {
		return "", nil
	}
	value, ok := r[key].(string)
	limit := 10000
	if key == "notes" {
		limit = 65536
	}
	if !ok || !utf8.ValidString(value) || len(value) > limit || strings.ContainsRune(value, 0) || (key != "notes" && strings.ContainsRune(value, '\r')) {
		return "", c.consoleError(table, r, key+" must be bounded source text")
	}
	return value, nil
}
func (c *converter) consoleRef(table string, r row, key, targetTable, targetKind string, optional bool) (string, error) {
	c.consoleUsed(table, r, key)
	raw, exists := r[key]
	if optional && (!exists || raw == nil) {
		return "", nil
	}
	number, err := integer(raw)
	_, isBool := raw.(bool)
	if err != nil || isBool || number < 1 || c.index[targetTable][strconv.FormatInt(number, 10)] == nil {
		return "", c.consoleError(table, r, "missing or invalid "+key+" reference")
	}
	id := legacyID(targetKind, number)
	if c.convertedRecord(targetKind, id) == nil {
		return "", c.consoleError(table, r, key+" does not resolve to a successfully converted "+targetKind+" record")
	}
	return id, nil
}
func consoleSourceBool(raw any) (bool, bool) {
	if boolean, ok := raw.(bool); ok {
		return boolean, true
	}
	if text, ok := raw.(string); ok {
		if text == "true" {
			return true, true
		}
		if text == "false" {
			return false, true
		}
	}
	n, err := integer(raw)
	if err != nil || n != 0 && n != 1 {
		return false, false
	}
	return n == 1, true
}
func consoleSourceInteger(raw any) (int64, bool) {
	if _, bad := raw.(bool); bad {
		return 0, false
	}
	n, err := integer(raw)
	return n, err == nil
}
func (c *converter) consoleProvenance(table string, r row, field string, values map[string]any, out row) error {
	if len(values) == 0 {
		return nil
	}
	for _, v := range values {
		switch v.(type) {
		case string:
			text := v.(string)
			if len(text) > 1024 || !utf8.ValidString(text) {
				return c.consoleError(table, r, "console source string exceeds1024 bytes")
			}
		case nil, bool, json.Number, int, int64:
		default:
			return c.consoleError(table, r, "unrecognized non-scalar console source value")
		}
	}
	encoded, err := json.Marshal(values)
	if err != nil || len(encoded) > 4096 {
		return c.consoleError(table, r, field+" source record exceeds supported4096-byte JSON limit")
	}
	out[field] = string(encoded)
	return nil
}
func consoleDisplayName(raw string, id int64) string {
	name := strings.TrimSpace(raw)
	if consolemeta.ValidatePortLabel(name) != nil {
		return fmt.Sprintf("Legacy console connection %d", id)
	}
	return name
}

// consoles migrates inventory only. It never guesses a device from description
// or the removed switchid, opens a terminal, or resolves DNS over the network.
func (c *converter) consoles() error {
	names := map[string]bool{}
	for _, r := range c.source.Tables["console_server"] {
		const table = "console_server"
		out := row{"id": legacyID("console-servers", idOf(r))}
		name, err := c.consoleText(table, r, "name")
		if err != nil {
			return err
		}
		name = strings.TrimSpace(name)
		if consolemeta.ValidatePortLabel(name) != nil {
			return c.consoleError(table, r, "name is missing or invalid")
		}
		key := consolemeta.PortKey(name)
		if names[key] {
			return c.consoleError(table, r, "console server names collide under Unicode case folding")
		}
		names[key] = true
		out["name"] = name
		rackID, err := c.consoleRef(table, r, "cabinet_id", "cabinet", "racks", false)
		if err != nil {
			return err
		}
		rack := c.convertedRecord("racks", rackID)
		locationID, _ := rack["locationId"].(string)
		if locationID == "" || c.convertedRecord("locations", locationID) == nil {
			return c.consoleError(table, r, "cabinet has no converted location")
		}
		out["rackId"] = rackID
		out["locationId"] = locationID
		if vendorID, err := c.consoleRef(table, r, "vendor_id", "vendor", "vendors", true); err != nil {
			return err
		} else if vendorID != "" {
			out["vendorId"] = vendorID
		}
		for _, field := range []string{"model", "serialNumber", "notes"} {
			value, e := c.consoleText(table, r, field)
			if e != nil {
				return e
			}
			if field != "notes" && utf8.RuneCountInString(value) > 255 {
				return c.consoleError(table, r, field+" exceeds255 characters")
			}
			if value != "" {
				out[field] = value
			}
		}
		source := map[string]any{}
		hostname, err := c.consoleText(table, r, "hostname")
		if err != nil {
			return err
		}
		if hostname == "" {
			return c.consoleError(table, r, "hostname/IP is required; no address will be inferred from display text")
		}
		if address, e := netip.ParseAddr(hostname); e == nil {
			if address.IsUnspecified() || address.IsMulticast() || address.Is4In6() || address.Zone() != "" {
				return c.consoleError(table, r, "hostname contains an unusable literal IP")
			}
			out["managementIp"] = address.String()
			source["hostname"] = hostname
		} else {
			normalized, e := consolemeta.NormalizeHostname(hostname)
			if e != nil {
				return c.consoleError(table, r, "hostname is not a valid offline IDNA hostname or literal IP")
			}
			out["hostname"] = normalized
			if normalized != hostname {
				source["hostname"] = hostname
			}
		}
		raw, exists := r["active"]
		c.consoleUsed(table, r, "active")
		active, known := consoleSourceBool(raw)
		if !exists || raw == nil || !known {
			out["active"] = false
			source["active"] = raw
			if !exists {
				source["activeMissing"] = true
			}
			c.warn(table, r, "unknown active flag retained for staff review; imported inactive")
		} else {
			out["active"] = active
		}
		if err = c.consoleProvenance(table, r, "legacyConsole", source, out); err != nil {
			return err
		}
		if err = c.consoleTimestamps(table, r, out); err != nil {
			return err
		}
		c.add("console-servers", r, out)
		c.resolve(table, r)
	}
	seen := map[string]bool{}
	for _, r := range c.source.Tables["consoleserverconnection"] {
		const table = "consoleserverconnection"
		out := row{"id": legacyID("console-connections", idOf(r))}
		server, err := c.consoleRef(table, r, "console_server_id", "console_server", "console-servers", false)
		if err != nil {
			return err
		}
		out["consoleServerId"] = server
		member, err := c.consoleRef(table, r, "custid", "cust", "members", false)
		if err != nil {
			return err
		}
		out["memberId"] = member
		description, err := c.consoleText(table, r, "description")
		if err != nil {
			return err
		}
		if utf8.RuneCountInString(description) > 255 {
			return c.consoleError(table, r, "description exceeds255 characters")
		}
		out["name"] = consoleDisplayName(description, idOf(r))
		if description != "" {
			out["description"] = description
		}
		notes, err := c.consoleText(table, r, "notes")
		if err != nil {
			return err
		}
		if notes != "" {
			out["notes"] = notes
		}
		c.consoleUsed(table, r, "port")
		label, ok := r["port"].(string)
		if !ok {
			n, valid := consoleSourceInteger(r["port"])
			if !valid || n < 1 || n > 65535 {
				return c.consoleError(table, r, "port must be exact source text or a valid legacy numeric port")
			}
			label = strconv.FormatInt(n, 10)
		}
		if err = consolemeta.ValidatePortLabel(label); err != nil {
			return c.consoleError(table, r, "port label is invalid; it is not trimmed or converted to a number")
		}
		key := server + "\x00" + consolemeta.PortKey(label)
		if seen[key] {
			return c.consoleError(table, r, "port labels collide on the same console server under NFC/case folding")
		}
		seen[key] = true
		out["portLabel"] = label
		unknown := map[string]any{}
		for _, field := range []string{"speed", "parity", "stopbits", "flowcontrol", "autobaud"} {
			c.consoleUsed(table, r, field)
			raw, exists := r[field]
			if !exists || raw == nil {
				continue
			}
			if field == "autobaud" {
				if value, valid := consoleSourceBool(raw); valid {
					out["autobaud"] = value
					continue
				}
			} else if value, valid := consoleSourceInteger(raw); valid {
				switch field {
				case "speed":
					if value >= 0 && value <= 230400 && consolemeta.ValidBaudRate(int(value)) {
						out["baudRate"] = value
						continue
					}
				case "parity":
					if mapped, valid := map[int64]string{1: "even", 2: "odd", 3: "none"}[value]; valid {
						out["parity"] = mapped
						continue
					}
				case "stopbits":
					if value == 1 || value == 2 {
						out["stopBits"] = value
						continue
					}
				case "flowcontrol":
					if mapped, valid := map[int64]string{1: "none", 2: "rts-cts", 3: "xon-xoff"}[value]; valid {
						out["flowControl"] = mapped
						continue
					}
				}
			}
			unknown[field] = raw
			c.warn(table, r, "unknown "+field+" retained in legacySerial for staff review; no serial default invented")
		}
		if raw, exists := r["switchid"]; exists {
			c.consoleUsed(table, r, "switchid")
			unknown["switchid"] = raw
			c.warn(table, r, "removed switchid retained as source only; no device relation inferred")
		}
		if err = c.consoleProvenance(table, r, "legacySerial", unknown, out); err != nil {
			return err
		}
		if err = c.consoleTimestamps(table, r, out); err != nil {
			return err
		}
		c.add("console-connections", r, out)
		c.resolve(table, r)
	}
	return nil
}
