package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"

	consoleinventory "github.com/moedove/ixp-manager/internal/console"
)

func init() {
	Schemas["console-servers"] = Schema{"name": textField(true), "locationId": ref("locations", true), "managementIp": {Type: "ip"}, "hostname": textField(false), "rackId": ref("racks", false), "vendorId": ref("vendors", false), "model": textField(false), "serialNumber": textField(false), "active": {Type: "boolean"}, "notes": {Type: "longtext"}, "legacyConsole": textField(false)}
	Schemas["console-connections"] = Schema{"name": textField(true), "description": textField(false), "consoleServerId": ref("console-servers", true), "memberId": ref("members", true), "port": num(1, 65535, false), "portLabel": textField(false), "baudRate": num(300, 230400, false), "parity": enum("even", "odd", "none"), "stopBits": num(1, 2, false), "flowControl": enum("none", "rts-cts", "xon-xoff"), "autobaud": {Type: "boolean"}, "notes": {Type: "longtext"}, "legacySerial": textField(false)}
	for _, kind := range []string{"console-servers", "console-connections"} {
		Schemas[kind]["legacyTimestamps"] = Field{Type: "console-source-timestamps"}
	}
}
func consolePortLabel(r Record) (string, error) {
	label, present := r["portLabel"]
	text := ""
	if present && label != nil && label != "" {
		var ok bool
		text, ok = label.(string)
		if !ok {
			return "", errors.New("portLabel must be text")
		}
		if err := consoleinventory.ValidatePortLabel(text); err != nil {
			return "", err
		}
	}
	if raw, exists := r["port"]; exists && raw != nil && raw != "" {
		n, ok := raw.(float64)
		if !ok || math.IsNaN(n) || n != math.Trunc(n) || n < 1 || n > 65535 {
			return "", errors.New("legacy port must be an integer1..65535")
		}
		decimal := strconv.FormatInt(int64(n), 10)
		if text != "" && text != decimal {
			return "", errors.New("port and portLabel disagree; omit numeric port to preserve a label such as01")
		}
		if text == "" {
			text = decimal
		}
	}
	if text == "" {
		return "", errors.New("portLabel or a legacy numeric port is required")
	}
	return text, nil
}
func validConsoleProvenance(kind, key, value string) error {
	if value == "" {
		return nil
	}
	if len(value) > 4096 {
		return errors.New("legacy console source values exceed limit")
	}
	var fields map[string]any
	d := json.NewDecoder(strings.NewReader(value))
	d.UseNumber()
	if err := d.Decode(&fields); err != nil || fields == nil {
		return errors.New("legacy console source values must be a JSON object")
	}
	allowed := map[string]bool{"active": true, "activeMissing": true, "hostname": true}
	if kind == "console-connections" && key == "legacySerial" {
		allowed = map[string]bool{"speed": true, "parity": true, "stopbits": true, "flowcontrol": true, "autobaud": true, "switchid": true}
	}
	for k, v := range fields {
		if !allowed[k] {
			return errors.New("unsupported legacy console source field")
		}
		switch v := v.(type) {
		case nil, bool, json.Number:
		case string:
			if len(v) > 1024 || !utf8.ValidString(v) {
				return errors.New("legacy console source string exceeds limit")
			}
		default:
			return errors.New("legacy console source values must be scalars")
		}
		if k == "activeMissing" {
			if _, ok := v.(bool); !ok {
				return errors.New("activeMissing must be boolean")
			}
		}
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return errors.New("one legacy console source object required")
	}
	return nil
}

func consoleTimestampObject(raw any) Record {
	switch object := raw.(type) {
	case Record:
		return object
	case map[string]any:
		return Record(object)
	default:
		return nil
	}
}

func validateConsoleSourceTimestamps(kind string, row Record) error {
	raw, present := row["legacyTimestamps"]
	if !present {
		return nil
	}
	fields := consoleTimestampObject(raw)
	if len(fields) < 2 || len(fields) > 3 {
		return errors.New("legacy console timestamps require a source ID and at least one source time")
	}
	sourceID := str(fields, "sourceId")
	id, err := strconv.ParseInt(sourceID, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != sourceID || str(row, "id") != "legacy-"+kind+"-"+sourceID {
		return errors.New("legacy console timestamps must match the imported resource identity")
	}
	for field, rawTime := range fields {
		switch field {
		case "sourceId":
		case "createdAt", "updatedAt":
			if rawTime == nil {
				continue
			}
			value, ok := rawTime.(string)
			if !ok || consoleinventory.ValidateSourceTimestamp(value) != nil {
				return fmt.Errorf("invalid legacy console source %s", field)
			}
		default:
			return errors.New("unsupported legacy console timestamp field")
		}
	}
	return nil
}

func validateConsoleRecord(s *State, kind string, r Record) error {
	if kind != "console-servers" && kind != "console-connections" {
		return nil
	}
	if err := validateConsoleSourceTimestamps(kind, r); err != nil {
		return err
	}
	name := str(r, "name")
	if err := consoleinventory.ValidatePortLabel(name); err != nil {
		return errors.New("console name must contain1..255 visible characters without boundary whitespace")
	}
	if kind == "console-servers" {
		if value, exists := r["active"]; exists {
			if _, ok := value.(bool); !ok {
				return errors.New("active must be an explicit boolean")
			}
		}
		if host := str(r, "hostname"); host != "" {
			canonical, err := consoleinventory.NormalizeHostname(host)
			if err != nil {
				return err
			}
			r["hostname"] = canonical
		}
		if str(r, "hostname") == "" && str(r, "managementIp") == "" {
			return errors.New("a hostname or managementIp must be recorded")
		}
		if rack := str(r, "rackId"); rack != "" && str(find(s, "racks", rack), "locationId") != str(r, "locationId") {
			return errors.New("console server rack and location must match")
		}
		for _, old := range s.Resources[kind] {
			if str(old, "id") != str(r, "id") && consoleinventory.PortKey(str(old, "name")) == consoleinventory.PortKey(name) {
				return errors.New("console server name already exists")
			}
		}
		for _, key := range []string{"model", "serialNumber"} {
			if utf8.RuneCountInString(str(r, key)) > 255 {
				return fmt.Errorf("%s exceeds255 characters", key)
			}
		}
		return validConsoleProvenance(kind, "legacyConsole", str(r, "legacyConsole"))
	}
	label, err := consolePortLabel(r)
	if err != nil {
		return err
	}
	for _, old := range s.Resources[kind] {
		if str(old, "id") == str(r, "id") || str(old, "consoleServerId") != str(r, "consoleServerId") {
			continue
		}
		oldLabel, e := consolePortLabel(old)
		if e == nil && consoleinventory.PortKey(oldLabel) == consoleinventory.PortKey(label) {
			return errors.New("console port label already assigned on this server")
		}
	}
	if value, exists := r["baudRate"]; exists && value != nil && value != "" {
		if !consoleinventory.ValidBaudRate(int(number(r, "baudRate"))) {
			return errors.New("baudRate must be a supported recorded serial rate")
		}
	}
	if value, exists := r["autobaud"]; exists && value != nil {
		if _, ok := value.(bool); !ok {
			return errors.New("autobaud must be boolean or unset")
		}
	}
	if utf8.RuneCountInString(str(r, "description")) > 255 {
		return errors.New("console description exceeds255 characters")
	}
	return validConsoleProvenance(kind, "legacySerial", str(r, "legacySerial"))
}
func guardConsoleMutation(s *State, kind, method string, old, body Record) error {
	if kind != "console-servers" && kind != "console-connections" {
		return nil
	}
	if method == "DELETE" {
		if kind == "console-servers" {
			n := 0
			for _, c := range s.Resources["console-connections"] {
				if str(c, "consoleServerId") == str(old, "id") {
					n++
				}
			}
			if n > 0 {
				return fmt.Errorf("控制台服务器仍关联%d条接入记录；请先处理关联接入，不会自动级联删除", n)
			}
		}
		return nil
	}
	legacy := "legacyConsole"
	if kind == "console-connections" {
		legacy = "legacySerial"
	}
	if incoming, exists := body[legacy]; exists {
		text, ok := incoming.(string)
		if !ok || old == nil || text != str(old, legacy) {
			return errors.New("legacy console source evidence is import-only and read-only")
		}
	} else if old != nil && old[legacy] != nil {
		body[legacy] = old[legacy]
	}
	previous, hasPrevious := old["legacyTimestamps"]
	if incoming, supplied := body["legacyTimestamps"]; supplied {
		fields := consoleTimestampObject(incoming)
		if !hasPrevious || fields == nil || !reflect.DeepEqual(fields, consoleTimestampObject(previous)) {
			return errors.New("legacy console timestamps are import-only and read-only")
		}
	}
	if hasPrevious {
		body["legacyTimestamps"] = previous
	}
	if method == "PUT" {
		keys := []string{"hostname", "managementIp", "rackId", "vendorId", "model", "serialNumber", "active", "notes"}
		if kind == "console-connections" {
			keys = []string{"description", "baudRate", "parity", "stopBits", "flowControl", "autobaud", "notes"}
		}
		for _, key := range keys {
			if _, exists := body[key]; !exists {
				if value, present := old[key]; present {
					body[key] = value
				}
			}
		}
		if kind == "console-connections" {
			_, label := body["portLabel"]
			_, numeric := body["port"]
			if !label && !numeric {
				if old["portLabel"] != nil {
					body["portLabel"] = old["portLabel"]
				} else {
					body["port"] = old["port"]
				}
			}
		}
	}
	if kind == "console-servers" {
		if _, exists := body["active"]; !exists {
			body["active"] = true
		}
	}
	return nil
}
func decorateConsole(s *State, kind string, r Record) Record {
	out := clean(r)
	if kind == "console-servers" {
		out["active"] = r["active"] != false
		count := 0
		for _, c := range s.Resources["console-connections"] {
			if str(c, "consoleServerId") == str(r, "id") {
				count++
			}
		}
		out["connectionCount"] = count
	}
	if kind == "console-connections" {
		label, _ := consolePortLabel(r)
		out["portLabel"] = label
		server := find(s, "console-servers", str(r, "consoleServerId"))
		out["consoleServerName"] = str(server, "name")
		out["consoleServerActive"] = server["active"] != false
		out["locationId"] = str(server, "locationId")
		out["locationName"] = str(find(s, "locations", str(server, "locationId")), "name")
	}
	return out
}
