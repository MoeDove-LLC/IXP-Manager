package app

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// These import facts describe the old non-executing customer selector. They
// never enable a connection, a peer or a routing deployment in the new app.
var legacyCustomerFields = map[string][]string{
	"connections": {"legacyRSClientIPv4", "legacyRSClientIPv6"},
	"vlans":       {"legacyRouterIPv4", "legacyRouterIPv6"},
}

var legacyCustomerAddressFields = [...]string{"ipv4", "ipv6"}

func legacyCustomerSelectionID(kind, value string) int64 {
	raw, ok := strings.CutPrefix(value, "legacy-"+kind+"-")
	id, err := strconv.ParseInt(raw, 10, 64)
	if !ok || err != nil || id <= 0 || strconv.FormatInt(id, 10) != raw {
		return 0
	}
	return id
}

func validateLegacyCustomerEvidence(kind string, row Record) error {
	for index, field := range legacyCustomerFields[kind] {
		raw, exists := row[field]
		if !exists {
			continue
		}
		value, ok := raw.(bool)
		if !ok || legacyCustomerSelectionID(kind, str(row, "id")) == 0 {
			return fmt.Errorf("%s requires a migrated resource identity and an explicit boolean", field)
		}
		if kind == "connections" && value && (legacyCustomerSelectionID("members", str(row, "memberId")) == 0 ||
			legacyCustomerSelectionID("vlans", str(row, "vlanId")) == 0 || str(row, legacyCustomerAddressFields[index]) == "") {
			return fmt.Errorf("%s requires a migrated member, VLAN and corresponding interface address", field)
		}
	}
	return nil
}

// Ordinary resource editors can round-trip facts, but cannot make historic
// eligibility claims for a new owner, fabric attachment or address family.
func guardLegacyCustomerResourceMutation(kind, method string, old, body Record) error {
	if method == http.MethodDelete {
		return nil
	}
	newBinding := kind == "connections" && (str(body, "memberId") != str(old, "memberId") || str(body, "vlanId") != str(old, "vlanId"))
	for index, field := range legacyCustomerFields[kind] {
		before, existed := old[field].(bool)
		if raw, supplied := body[field]; supplied {
			value, ok := raw.(bool)
			if !existed || !ok || value != before {
				return fmt.Errorf("%s is managed by legacy import", field)
			}
		}
		if kind == "connections" && (newBinding || str(body, legacyCustomerAddressFields[index]) == "") {
			delete(body, field)
		} else if existed {
			body[field] = before
		}
	}
	return nil
}

type legacySelectedCustomer struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

func legacyCustomerInputFields(values url.Values, fields map[string]string) bool {
	for key := range values {
		if strings.HasPrefix(key, "vlanid[") || strings.HasPrefix(key, "protocol[") {
			return false
		}
	}
	for _, name := range []string{"vlanid", "protocol"} {
		if value, exists := values[name]; exists {
			if len(value) != 1 {
				return false
			}
			fields[name] = value[0]
		}
	}
	return true
}

func legacyCustomerJSONFields(body io.Reader, fields map[string]string) int {
	d := json.NewDecoder(body)
	d.UseNumber()
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return http.StatusBadRequest
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] {
			return http.StatusBadRequest
		}
		seen[name] = true
		var value any
		if err := d.Decode(&value); err != nil {
			return http.StatusBadRequest
		}
		if name != "vlanid" && name != "protocol" {
			continue
		}
		switch value := value.(type) {
		case nil:
			fields[name] = ""
		case string:
			fields[name] = value
		case json.Number:
			fields[name] = value.String()
		default:
			return http.StatusNotFound
		}
	}
	if end, err := d.Token(); err != nil || end != json.Delim('}') || d.Decode(new(any)) != io.EOF {
		return http.StatusBadRequest
	}
	return 0
}

func legacyCustomerFilters(w http.ResponseWriter, r *http.Request) (vlanID, protocol int64, ok bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	fields := map[string]string{}
	if err != nil || !legacyCustomerInputFields(query, fields) {
		problem(w, http.StatusBadRequest, "invalid legacy selector query")
		return 0, 0, false
	}
	if r.ContentLength != 0 {
		contentType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			problem(w, http.StatusUnsupportedMediaType, "expected JSON or a URL-encoded form")
			return 0, 0, false
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
		switch contentType {
		case "application/json":
			if status := legacyCustomerJSONFields(r.Body, fields); status != 0 {
				problem(w, status, "invalid legacy customer selectors")
				return 0, 0, false
			}
		case "application/x-www-form-urlencoded":
			if r.ParseForm() != nil || !legacyCustomerInputFields(r.PostForm, fields) {
				problem(w, http.StatusBadRequest, "invalid legacy selector form")
				return 0, 0, false
			}
		default:
			problem(w, http.StatusUnsupportedMediaType, "expected JSON or a URL-encoded form")
			return 0, 0, false
		}
	}
	if value := strings.TrimSpace(fields["vlanid"]); value != "" && value != "0" {
		vlanID, err = strconv.ParseInt(value, 10, 64)
		if err != nil || vlanID <= 0 {
			problem(w, http.StatusNotFound, "VLAN not found")
			return 0, 0, false
		}
	}
	if value := strings.TrimSpace(fields["protocol"]); value != "" && value != "0" {
		protocol, err = strconv.ParseInt(value, 10, 64)
		if err != nil || protocol != 4 && protocol != 6 {
			problem(w, http.StatusNotFound, "protocol not found")
			return 0, 0, false
		}
	}
	return vlanID, protocol, true
}

func (a *App) legacyCustomersByVlanAndProtocol(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		problem(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	vlanID, protocol, ok := legacyCustomerFilters(w, r)
	if !ok {
		return
	}
	result, status, detail := a.legacyCustomerSelection(user, session, vlanID, protocol)
	if status != http.StatusOK {
		problem(w, status, detail)
		return
	}
	respond(w, status, Record{"listCustomers": result})
}

func (a *App) legacyCustomerSelection(user Record, session Session, vlanID, protocol int64) ([]legacySelectedCustomer, int, string) {
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	if a.store.closed {
		return nil, http.StatusForbidden, "current inventory read access required"
	}
	s := &a.store.state
	actor, err := a.atlasActor(s, user, session, "admin", "operator", "viewer", "member")
	if err != nil || session.API && !contains(session.Scopes, "resources:read") {
		return nil, http.StatusForbidden, "current inventory read access required"
	}
	tenant := ""
	if str(actor, "role") == "member" {
		tenant = str(actor, "memberId")
		if tenant == "" || str(find(s, "members", tenant), "status") != "active" {
			return nil, http.StatusForbidden, "active member access required"
		}
	}
	vlans := map[string]Record{}
	for _, vlan := range s.Resources["vlans"] {
		id := str(vlan, "id")
		if legacyCustomerSelectionID("vlans", id) != 0 {
			vlans[id] = vlan
		}
	}
	selectedVLAN := ""
	if vlanID > 0 {
		selectedVLAN = "legacy-vlans-" + strconv.FormatInt(vlanID, 10)
		known := false
		if tenant != "" {
			for _, c := range s.Resources["connections"] {
				if str(c, "memberId") == tenant && str(c, "vlanId") == selectedVLAN {
					known = true
					break
				}
			}
		}
		if vlans[selectedVLAN] == nil || tenant != "" && !known {
			return nil, http.StatusNotFound, "VLAN not found"
		}
	}
	customers := map[string]legacySelectedCustomer{}
	for _, member := range s.Resources["members"] {
		id := str(member, "id")
		if source := legacyCustomerSelectionID("members", id); source != 0 && (tenant == "" || id == tenant) {
			customers[id] = legacySelectedCustomer{ID: source, Name: str(member, "name")}
		}
	}
	result := []legacySelectedCustomer{}
	for _, connection := range s.Resources["connections"] {
		memberID, connectionVLAN := str(connection, "memberId"), str(connection, "vlanId")
		customer, exists := customers[memberID]
		vlan := vlans[connectionVLAN]
		if !exists || vlan == nil || selectedVLAN != "" && selectedVLAN != connectionVLAN {
			continue
		}
		v4 := connection["legacyRSClientIPv4"] == true && str(connection, "ipv4") != ""
		v6 := connection["legacyRSClientIPv6"] == true && str(connection, "ipv6") != ""
		router4, router6 := vlan["legacyRouterIPv4"] == true, vlan["legacyRouterIPv6"] == true
		// The source query has independent family ORs when protocol is omitted.
		if protocol == 4 && router4 && v4 || protocol == 6 && router6 && v6 || protocol == 0 && (router4 || router6) && (v4 || v6) {
			result = append(result, customer)
			delete(customers, memberID)
		}
	}
	// Approximate the source database's utf8mb4_unicode_ci name ordering.
	names := collate.New(language.Und, collate.IgnoreCase, collate.IgnoreDiacritics)
	sort.Slice(result, func(i, j int) bool {
		order := names.CompareString(result[i].Name, result[j].Name)
		if order == 0 {
			return result[i].ID < result[j].ID
		}
		return order < 0
	})
	return result, http.StatusOK, ""
}
