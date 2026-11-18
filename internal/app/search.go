package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const searchMaxRecords = 20000
const searchMaxBytes = 32 << 20

type searchDefinition struct {
	kind, label, scope  string
	fields, staffFields []string
}

var searchDefinitions = []searchDefinition{
	{"members", "成员", "resources:read", []string{"id", "name", "shortName", "abbreviatedName", "registeredName", "asn", "email", "website", "nocEmail", "nocWebsite", "irrASSet", "irrASSet6", "city", "country"}, []string{"notes", "registrationNotes", "registrationNumber", "billingName", "billingEmail", "billingAddress", "billingNotes", "statusReason"}},
	{"connections", "成员接入", "resources:read", []string{"id", "name", "ipv4", "ipv6", "dnsName4", "dnsName6", "status", "vlanName", "infrastructureName"}, nil},
	{"contacts", "联系人", "resources:read", []string{"id", "name", "email", "phone", "mobile", "role", "position"}, []string{"notes"}},
	{"users", "用户", "resources:read", []string{"id", "name", "email"}, nil},
	{"locations", "机房", "resources:read", []string{"id", "name", "city", "country", "address"}, nil},
	{"racks", "机柜", "resources:read", []string{"id", "name"}, nil},
	{"switches", "交换机", "resources:read", []string{"id", "name", "managementIp", "dnsName", "model", "serialNumber", "assetTag", "asn", "loopbackIpv4", "loopbackIpv6", "loopbackName"}, nil},
	{"ports", "物理端口", "resources:read", []string{"id", "name", "status", "snmpIfName", "snmpPhysicalAddress"}, nil},
	{"vlans", "VLAN", "resources:read", []string{"id", "name", "tag", "ipv4Prefix", "ipv6Prefix"}, []string{"notes"}},
	{"routers", "路由器", "resources:read", []string{"id", "name", "platform", "role", "asn", "routerId", "localIpv4", "localIpv6", "dnsName4", "dnsName6"}, nil},
	{"peers", "BGP 邻居", "resources:read", []string{"id", "name", "address", "asn"}, nil},
	{"vendors", "设备厂商", "resources:read", []string{"id", "name", "shortName", "website", "bundleName"}, nil},
	{"infrastructures", "交换网络", "resources:read", []string{"id", "name", "shortName", "country", "website"}, []string{"notes"}},
	{"mac-addresses", "注册 MAC", "resources:read", []string{"id", "name", "address"}, nil},
	{"ip-addresses", "IP 地址", "resources:read", []string{"id", "name", "address", "dnsName"}, nil},
	{"tags", "标签", "resources:read", []string{"id", "name", "displayAs", "description"}, nil},
	{"member-tags", "成员标签关联", "resources:read", []string{"id", "name"}, nil},
	{"notes", "备注", "resources:read", []string{"id", "name", "body"}, nil},
	{"equipment", "托管设备", "resources:read", []string{"id", "name", "description"}, nil},
	{"console-servers", "控制台服务器", "resources:read", []string{"id", "name", "managementIp", "hostname", "model", "serialNumber"}, []string{"notes"}},
	{"console-connections", "控制台接入", "resources:read", []string{"id", "name", "description", "port", "portLabel"}, []string{"notes"}},
	{"patch-panels", "配线架", "resources:read", []string{"id", "name"}, nil},
	{"patch-ports", "配线端口", "resources:read", []string{"id", "name", "number", "circuitId", "status", "panelName", "rackName", "locationName", "switchPortName", "switchName", "memberName"}, []string{"notes"}},
	{"core-links", "旧核心链路", "resources:read", []string{"id", "name", "type", "notes"}, nil},
	{"irr-sources", "IRR 来源", "resources:read", []string{"id", "name", "host", "port"}, nil},
	{"contact-groups", "联系人分组", "resources:read", []string{"id", "name", "description", "type"}, nil},
	{"contact-group-memberships", "联系人分组关联", "resources:read", []string{"id", "name"}, nil},
	{"sflow-exporters", "sFlow 导出器", "resources:read", []string{"id", "name", "sourceAddress", "agentAddress"}, nil},
	{"sflow-receivers", "sFlow 接收者", "sflow:read", []string{"id", "name", "address", "port"}, nil},
	{"provisioning-profiles", "配置工具方案", "provisioning:read", []string{"id", "name"}, nil},
	{"documents", "文档", "documents:read", []string{"id", "name", "description"}, nil},
	{"directories", "文档目录", "documents:read", []string{"id", "name", "description"}, nil},
	{"core-bundles", "核心互联", "core:read", []string{"id", "name", "graphTitle", "type", "ipv4Subnet", "ipv6Subnet", "interfaceA", "interfaceB"}, nil},
	{"learned-macs", "观测 MAC", "devices:read", []string{"mac", "portLabel", "connectionLabel", "memberLabel", "vlanLabel", "registrationStatus"}, nil},
}

type searchField struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type searchDocument struct {
	Kind   string        `json:"kind"`
	ID     string        `json:"id"`
	Key    string        `json:"key"`
	Title  string        `json:"title"`
	Href   string        `json:"href"`
	Fields []searchField `json:"fields"`
}
type searchItem struct {
	Kind          string   `json:"kind"`
	ID            string   `json:"id"`
	Key           string   `json:"key"`
	Title         string   `json:"title"`
	Subtitle      string   `json:"subtitle"`
	Snippet       string   `json:"snippet"`
	MatchedFields []string `json:"matchedFields"`
	Href          string   `json:"href"`
}
type searchGroup struct {
	Kind  string       `json:"kind"`
	Label string       `json:"label"`
	Count int          `json:"count"`
	Items []searchItem `json:"items"`
}
type searchResponse struct {
	Query      string        `json:"query"`
	Mode       string        `json:"mode"`
	Groups     []searchGroup `json:"groups"`
	Total      int           `json:"total"`
	Limit      int           `json:"limit"`
	NextCursor string        `json:"nextCursor,omitempty"`
	Truncated  bool          `json:"truncated"`
	Revision   string        `json:"revision"`
}
type searchRequest struct {
	query, mode, needle, switchID, cursor string
	terms, types                          []string
	limit                                 int
}
type searchCursor struct {
	Version  int    `json:"v"`
	Request  string `json:"request"`
	Revision string `json:"revision"`
	Offset   int    `json:"offset"`
}
type searchFailure struct {
	status  int
	message string
}

func (e searchFailure) Error() string             { return e.message }
func searchFail(status int, message string) error { return searchFailure{status, message} }
func searchDefinitionFor(kind string) (searchDefinition, bool) {
	for _, d := range searchDefinitions {
		if d.kind == kind {
			return d, true
		}
	}
	return searchDefinition{}, false
}
func searchMAC(value string) string {
	compact := strings.NewReplacer(":", "", "-", "", ".", "").Replace(strings.ToLower(value))
	if len(compact) != 12 {
		return ""
	}
	if _, e := hex.DecodeString(compact); e != nil {
		return ""
	}
	mac := make([]string, 6)
	for i := range mac {
		mac[i] = compact[i*2 : i*2+2]
	}
	return strings.Join(mac, ":")
}
func parseSearch(r *http.Request) (searchRequest, error) {
	q := searchRequest{limit: 20, mode: "text"}
	values := r.URL.Query()
	for key, value := range values {
		if len(value) != 1 {
			return q, searchFail(400, "each search parameter must occur once")
		}
		switch key {
		case "q", "types", "limit", "cursor", "switchId":
		default:
			return q, searchFail(400, "unknown search parameter")
		}
	}
	raw := values.Get("q")
	if !utf8.ValidString(raw) || len(raw) > 512 {
		return q, searchFail(400, "query must be valid UTF-8 and at most128 characters")
	}
	for _, ch := range raw {
		if unicode.IsControl(ch) && !unicode.IsSpace(ch) {
			return q, searchFail(400, "query contains control characters")
		}
	}
	q.query = strings.Join(strings.Fields(raw), " ")
	if n := utf8.RuneCountInString(q.query); n < 2 || n > 128 {
		return q, searchFail(400, "query must contain2..128 characters")
	}
	q.needle = strings.ToLower(q.query)
	q.terms = strings.Fields(q.needle)
	if len(q.terms) > 8 {
		return q, searchFail(400, "query may contain at most8 terms")
	}
	for _, term := range q.terms {
		if utf8.RuneCountInString(term) > 64 {
			return q, searchFail(400, "each query term may contain at most64 characters")
		}
	}
	if text := values.Get("limit"); text != "" {
		n, e := strconv.Atoi(text)
		if e != nil || n < 1 || n > 100 {
			return q, searchFail(400, "search limit must be1..100")
		}
		q.limit = n
	}
	q.cursor = values.Get("cursor")
	if len(q.cursor) > 2048 {
		return q, searchFail(400, "search cursor exceeds bound")
	}
	q.switchID = values.Get("switchId")
	if q.switchID != "" && !identifier.MatchString(q.switchID) {
		return q, searchFail(400, "invalid search switchId")
	}
	if types := values.Get("types"); types != "" {
		if len(types) > 1024 {
			return q, searchFail(400, "search type filter exceeds bound")
		}
		seen := map[string]bool{}
		for _, kind := range strings.Split(types, ",") {
			if _, ok := searchDefinitionFor(kind); !ok || seen[kind] {
				return q, searchFail(400, "unknown or duplicate search type")
			}
			seen[kind] = true
			q.types = append(q.types, kind)
		}
		sort.Strings(q.types)
	}
	if len(q.terms) == 1 {
		switch {
		case strings.HasPrefix(q.needle, "ppp-"):
			q.mode = "patch-port"
			q.needle = strings.TrimPrefix(q.needle, "ppp-")
			if !identifier.MatchString(q.needle) {
				return q, searchFail(400, "invalid PPP identifier")
			}
		case strings.HasPrefix(q.needle, "@"):
			q.mode = "username"
			q.needle = strings.TrimPrefix(q.needle, "@")
		case searchMAC(q.needle) != "":
			q.mode = "mac"
			q.needle = searchMAC(q.needle)
		case strings.HasPrefix(q.needle, "as-"):
			q.mode = "as-set"
		default:
			if ip, e := netip.ParseAddr(q.needle); e == nil && ip.Zone() == "" {
				q.mode = "ip"
				q.needle = ip.Unmap().String()
			} else {
				asn := q.needle
				if strings.HasPrefix(asn, "as") {
					asn = strings.TrimPrefix(asn, "as")
				}
				if n, e := strconv.ParseUint(asn, 10, 32); e == nil && n > 0 && n < 4294967295 {
					q.mode = "asn"
					q.needle = strconv.FormatUint(n, 10)
				} else if strings.HasPrefix(q.needle, ".") {
					if _, e := strconv.ParseUint(q.needle[1:], 10, 8); e == nil {
						q.mode = "ipv4-suffix"
					}
				} else if strings.HasPrefix(q.needle, ":") {
					if _, e := strconv.ParseUint(q.needle[1:], 16, 16); e == nil {
						q.mode = "ipv6-suffix"
					}
				}
			}
		}
	}
	if strings.HasPrefix(strings.ToLower(q.query), "xc:") {
		q.mode = "cross-connect"
		q.needle = strings.ToLower(strings.TrimSpace(q.query[3:]))
		if q.needle == "" {
			return q, searchFail(400, "xc: requires a circuit reference")
		}
	}
	return q, nil
}
func searchNeedsLearned(q searchRequest) bool {
	return contains(q.types, "learned-macs") || len(q.types) == 0 && q.mode == "mac"
}
func (a *App) searchActor(s *State, user Record, session Session) (Record, error) {
	actor, e := a.atlasActor(s, user, session, "admin", "operator", "viewer", "member")
	if e != nil {
		return nil, searchFail(403, "search authentication or authority changed")
	}
	if str(actor, "role") == "member" && find(s, "members", str(actor, "memberId")) == nil {
		return nil, searchFail(403, "selected member is no longer available")
	}
	if !session.API {
		valid := false
		a.mu.Lock()
		for _, current := range a.sessions {
			if current.ID == session.ID && current.UserID == session.UserID && current.Generation == session.Generation && current.MemberID == session.MemberID && current.CSRF == session.CSRF && time.Now().Before(current.Expires) {
				valid = true
				break
			}
		}
		a.mu.Unlock()
		if !valid {
			return nil, searchFail(403, "search session or selected member changed")
		}
	} else {
		valid := false
		for _, current := range s.Security[session.UserID].Tokens {
			memberID := current.MemberID
			if memberID == "" && str(actor, "role") == "member" && len(membershipIDs(actor)) == 1 {
				memberID = selectedMember(actor, "")
			}
			if current.ID == session.TokenID && memberID == session.MemberID && time.Now().Before(current.ExpiresAt) {
				valid = true
				break
			}
		}
		if !valid {
			return nil, searchFail(403, "search token binding changed")
		}
	}
	return actor, nil
}
func searchHasScope(actor Record, session Session, scope string) bool {
	return !session.API || contains(session.Scopes, scope) && contains(availableScopes(str(actor, "role")), scope)
}
func (a *App) searchAllowed(s *State, actor, user Record, session Session, q searchRequest) (map[string]bool, error) {
	allowed := map[string]bool{}
	for _, d := range searchDefinitions {
		if !searchHasScope(actor, session, d.scope) {
			continue
		}
		if d.kind == "users" && (session.API || str(actor, "role") != "admin") {
			continue
		}
		if str(actor, "role") == "member" {
			switch d.kind {
			case "members", "connections", "contacts", "member-tags", "tags", "notes", "equipment", "documents", "directories", "sflow-receivers":
			case "patch-ports":
				if session.API {
					continue
				}
			default:
				continue
			}
		}
		if d.kind == "core-bundles" {
			if _, _, e := a.coreActor(s, user, session, false); e != nil {
				continue
			}
		}
		if d.kind == "sflow-receivers" {
			if _, _, e := a.sflowActor(s, user, session, false); e != nil {
				continue
			}
		}
		if d.kind == "learned-macs" && !searchNeedsLearned(q) {
			continue
		}
		if d.kind != "documents" && d.kind != "directories" && d.kind != "core-bundles" && d.kind != "learned-macs" && d.kind != "sflow-receivers" {
			if _, ok := Schemas[d.kind]; !ok {
				continue
			}
		}
		allowed[d.kind] = true
	}
	for _, kind := range q.types {
		if !allowed[kind] {
			return nil, searchFail(403, "requested search type is outside current access")
		}
	}
	if len(q.types) > 0 {
		for kind := range allowed {
			if !contains(q.types, kind) {
				delete(allowed, kind)
			}
		}
	}
	if len(allowed) == 0 {
		return nil, searchFail(403, "no searchable read scope is available")
	}
	if q.switchID != "" && !allowed["learned-macs"] {
		return nil, searchFail(400, "switchId only scopes learned-macs searches")
	}
	return allowed, nil
}
func searchScalar(v any) string {
	switch n := v.(type) {
	case string:
		return n
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	case int:
		return strconv.Itoa(n)
	case uint32:
		return strconv.FormatUint(uint64(n), 10)
	case uint64:
		return strconv.FormatUint(n, 10)
	}
	return ""
}
func searchHref(page, id string, extra url.Values) string {
	if extra == nil {
		extra = url.Values{}
	}
	if id != "" {
		extra.Set("id", id)
	}
	return "#" + page + "?" + extra.Encode()
}
func searchDirectoryVisible(s *State, id string, actor Record) bool {
	seen := map[string]bool{}
	for id != "" {
		if seen[id] || len(seen) > 1000 {
			return false
		}
		seen[id] = true
		d := directoryByID(s, id)
		if d == nil || !directoryReadable(s, *d, actor) {
			return false
		}
		id = d.ParentID
	}
	return true
}
func searchClip(text string, max int) string {
	r := []rune(text)
	if len(r) <= max {
		return text
	}
	return string(r[:max]) + "…"
}
func searchFieldValue(fields []searchField, name string) string {
	for _, f := range fields {
		if f.Name == name {
			return f.Value
		}
	}
	return ""
}
func (a *App) searchProjection(s *State, actor, user Record, session Session, q searchRequest, allowed map[string]bool, observations map[string]deviceObservation) ([]searchDocument, error) {
	documents := []searchDocument{}
	bytes := 0
	add := func(kind, id, title, href string, fields []searchField, key string) error {
		if !identifier.MatchString(id) && kind != "learned-macs" {
			return searchFail(503, "searchable record identity is invalid")
		}
		for _, field := range fields {
			if len(field.Value) > 65536 {
				return searchFail(422, "searchable field exceeds its bound")
			}
			bytes += len(field.Name) + len(field.Value)
		}
		if bytes > searchMaxBytes || len(documents) >= searchMaxRecords {
			return searchFail(422, "search scope exceeds its bound; choose specific types or a switch")
		}
		if key == "" {
			key = kind + "/" + id
		}
		if title == "" {
			title = id
		}
		documents = append(documents, searchDocument{Kind: kind, ID: id, Key: key, Title: searchClip(title, 160), Href: href, Fields: fields})
		return nil
	}
	for _, definition := range searchDefinitions {
		kind := definition.kind
		if !allowed[kind] || kind == "documents" || kind == "directories" || kind == "core-bundles" || kind == "learned-macs" || kind == "sflow-receivers" {
			continue
		}
		records := visible(s, kind, actor)
		if kind == "patch-ports" && !session.API {
			records = visiblePatchPorts(s, actor)
		}
		for _, record := range records {
			id := str(record, "id")
			if kind == provisionProfileKind && session.API && len(session.ProfileIDs) > 0 && !contains(session.ProfileIDs, id) {
				continue
			}
			fields := []searchField{}
			names := append([]string(nil), definition.fields...)
			if str(actor, "role") != "member" {
				names = append(names, definition.staffFields...)
			}
			for _, name := range names {
				if value := searchScalar(record[name]); value != "" {
					fields = append(fields, searchField{name, value})
				}
			}
			if kind == "connections" {
				if details, ok := record["portDetails"].([]Record); ok {
					for _, detail := range details {
						for _, name := range []string{"name", "switchName"} {
							if value := str(detail, name); value != "" {
								fields = append(fields, searchField{"port." + name, value})
							}
						}
					}
				}
			}
			page := kind
			if kind == "patch-ports" {
				page = "patching"
			}
			if kind == "members" && str(actor, "role") == "member" {
				page = "member-profile"
			}
			href := searchHref(page, id, nil)
			if kind == provisionProfileKind {
				href = "#provisioning?" + url.Values{"profileId": {id}}.Encode()
			}
			if e := add(kind, id, str(record, "name"), href, fields, ""); e != nil {
				return nil, e
			}
		}
	}
	if allowed["directories"] {
		for _, d := range s.Directories {
			if !directoryReadable(s, d, actor) || !searchDirectoryVisible(s, d.ParentID, actor) {
				continue
			}
			if e := add("directories", d.ID, d.Name, searchHref("documents", "", url.Values{"directoryId": {d.ID}}), []searchField{{"id", d.ID}, {"name", d.Name}, {"description", d.Description}}, ""); e != nil {
				return nil, e
			}
		}
	}
	if allowed["documents"] {
		for _, d := range s.Documents {
			if !documentReadable(s, d, actor) || !searchDirectoryVisible(s, d.DirectoryID, actor) {
				continue
			}
			if e := add("documents", d.ID, d.Name, searchHref("documents", d.ID, url.Values{"directoryId": {d.DirectoryID}}), []searchField{{"id", d.ID}, {"name", d.Name}, {"description", d.Description}}, ""); e != nil {
				return nil, e
			}
		}
	}
	if allowed["core-bundles"] {
		_, switches, e := a.coreActor(s, user, session, false)
		if e != nil {
			return nil, searchFail(403, "core search access changed")
		}
		for _, b := range s.CoreBundles {
			if !coreVisible(b, switches) {
				continue
			}
			fields := []searchField{{"id", b.ID}, {"name", b.Name}, {"graphTitle", b.GraphTitle}, {"type", b.Type}, {"ipv4Subnet", b.IPv4Subnet}, {"ipv6Subnet", b.IPv6Subnet}, {"interfaceA", b.SideA.InterfaceName}, {"interfaceB", b.SideB.InterfaceName}}
			if e = add("core-bundles", b.ID, b.Name, searchHref("core", b.ID, nil), fields, ""); e != nil {
				return nil, e
			}
		}
	}
	if allowed["sflow-receivers"] {
		receiverActor, scope, e := a.sflowActor(s, user, session, false)
		if e != nil {
			return nil, searchFail(403, "receiver search access changed")
		}
		for _, receiver := range s.SFlowReceivers {
			if !a.receiverVisible(s, receiver, receiverActor, scope) {
				continue
			}
			fields := []searchField{{"id", receiver.Spec.ID}, {"name", receiver.Name}, {"address", receiver.Spec.Address}, {"port", strconv.Itoa(int(receiver.Spec.Port))}}
			href := "#sflow?" + url.Values{"tab": {"receivers"}, "receiverId": {receiver.Spec.ID}}.Encode()
			if e := add("sflow-receivers", receiver.Spec.ID, receiver.Name, href, fields, ""); e != nil {
				return nil, e
			}
		}
	}
	if allowed["learned-macs"] {
		ids := make([]string, 0, len(observations))
		for id := range observations {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, switchID := range ids {
			if _, e := macLearningActor(s, user, session, switchID, false); e != nil {
				continue
			}
			_, rows := macLearningProjection(s, observations[switchID], false, time.Now().UTC())
			for _, row := range rows {
				fields := []searchField{{"mac", row.MAC}, {"portLabel", row.PortLabel}, {"connectionLabel", row.ConnectionLabel}, {"memberLabel", row.MemberLabel}, {"vlanLabel", row.VLANLabel}, {"registrationStatus", row.RegistrationStatus}}
				href := "#ipam?" + url.Values{"tab": {"mac"}, "switchId": {switchID}, "candidateId": {row.ID}}.Encode()
				if e := add("learned-macs", row.ID, row.MAC, href, fields, "learned-macs/"+switchID+"/"+row.ID); e != nil {
					return nil, e
				}
			}
		}
	}
	sort.Slice(documents, func(i, j int) bool {
		a, b := documents[i], documents[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if strings.ToLower(a.Title) != strings.ToLower(b.Title) {
			return strings.ToLower(a.Title) < strings.ToLower(b.Title)
		}
		return a.Key < b.Key
	})
	return documents, nil
}
func searchSwitches(s *State, user Record, session Session, q searchRequest) []string {
	ids := []string{}
	for _, sw := range s.Resources["switches"] {
		id := str(sw, "id")
		if q.switchID != "" && id != q.switchID {
			continue
		}
		if _, e := macLearningActor(s, user, session, id, false); e == nil {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
func searchSameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func (a *App) searchSnapshot(q searchRequest, user Record, session Session) ([]searchDocument, string, error) {
	learned := searchNeedsLearned(q)
	if learned {
		a.deviceMu.Lock()
		defer a.deviceMu.Unlock()
	}
	observations := map[string]deviceObservation{}
	ids := []string{}
	a.store.mu.RLock()
	if a.store.closed {
		a.store.mu.RUnlock()
		return nil, "", searchFail(403, "search store is closed")
	}
	actor, e := a.searchActor(&a.store.state, user, session)
	if e != nil {
		a.store.mu.RUnlock()
		return nil, "", e
	}
	allowed, e := a.searchAllowed(&a.store.state, actor, user, session, q)
	if e != nil {
		a.store.mu.RUnlock()
		return nil, "", e
	}
	if allowed["learned-macs"] {
		ids = searchSwitches(&a.store.state, user, session, q)
	}
	a.store.mu.RUnlock()
	if len(ids) > 128 {
		return nil, "", searchFail(422, "MAC search spans too many switches; specify switchId")
	}
	var readBytes int64
	for _, id := range ids {
		observation, e := a.latestDevice(id)
		if e != nil {
			return nil, "", searchFail(503, "authorized MAC observation could not be read")
		}
		readBytes += observation.fileSize
		if readBytes > searchMaxBytes {
			return nil, "", searchFail(422, "MAC search exceeds observation read bound; specify switchId")
		}
		observations[id] = observation
	}
	a.store.mu.RLock()
	defer a.store.mu.RUnlock()
	if a.store.closed {
		return nil, "", searchFail(403, "search store is closed")
	}
	actor, e = a.searchActor(&a.store.state, user, session)
	if e != nil {
		return nil, "", e
	}
	allowed, e = a.searchAllowed(&a.store.state, actor, user, session, q)
	if e != nil {
		return nil, "", e
	}
	if allowed["learned-macs"] && !searchSameStrings(ids, searchSwitches(&a.store.state, user, session, q)) {
		return nil, "", searchFail(409, "search authorization or inventory changed; restart the query")
	}
	documents, e := a.searchProjection(&a.store.state, actor, user, session, q, allowed, observations)
	if e != nil {
		return nil, "", e
	}
	scope := append([]string(nil), session.Scopes...)
	sort.Strings(scope)
	identity := struct {
		UserID, Role, MemberID, SessionID, TokenID, CSRF string
		Generation                                       uint64
		Scopes                                           []string
	}{str(actor, "id"), str(actor, "role"), str(actor, "memberId"), session.ID, session.TokenID, session.CSRF, session.Generation, scope}
	raw, _ := json.Marshal(struct {
		Actor     any              `json:"actor"`
		Documents []searchDocument `json:"documents"`
	}{identity, documents})
	mac := hmac.New(sha256.New, a.store.securityKey)
	mac.Write([]byte("search-projection-v1\x00"))
	mac.Write(raw)
	return documents, hex.EncodeToString(mac.Sum(nil)), nil
}
func searchMatchField(q searchRequest, doc searchDocument, field searchField) bool {
	value := strings.ToLower(field.Value)
	switch q.mode {
	case "asn":
		return field.Name == "asn" && value == q.needle
	case "as-set":
		return (field.Name == "irrASSet" || field.Name == "irrASSet6") && strings.Contains(value, q.needle)
	case "username":
		if doc.Kind != "users" {
			return false
		}
		if field.Name == "email" {
			value = strings.SplitN(value, "@", 2)[0]
		}
		return (field.Name == "name" || field.Name == "email") && strings.Contains(value, q.needle)
	case "mac":
		return searchMAC(value) == q.needle
	case "ip":
		ip, e := netip.ParseAddr(value)
		return e == nil && ip.Unmap().String() == q.needle
	case "ipv4-suffix":
		ip, e := netip.ParseAddr(value)
		suffix, _ := strconv.ParseUint(q.needle[1:], 10, 8)
		return e == nil && ip.Is4() && ip.As4()[3] == uint8(suffix)
	case "ipv6-suffix":
		ip, e := netip.ParseAddr(value)
		if e != nil || !ip.Is6() || ip.Is4In6() {
			return false
		}
		suffix, _ := strconv.ParseUint(q.needle[1:], 16, 16)
		b := ip.As16()
		return uint16(b[14])<<8|uint16(b[15]) == uint16(suffix)
	case "patch-port":
		return doc.Kind == "patch-ports" && (field.Name == "id" && (value == q.needle || value == "legacy-patch-ports-"+q.needle) || field.Name == "name" && value == "ppp-"+q.needle)
	case "cross-connect":
		return doc.Kind == "patch-ports" && (field.Name == "circuitId" || field.Name == "notes") && strings.Contains(value, q.needle)
	default:
		if address, e := mail.ParseAddress(q.query); e == nil && address.Address == q.query {
			if !strings.Contains(strings.ToLower(field.Name), "email") {
				return false
			}
			return strings.EqualFold(field.Value, q.query)
		}
		for _, term := range q.terms {
			if strings.Contains(value, term) {
				return true
			}
		}
		return false
	}
}
func searchMatches(q searchRequest, doc searchDocument) ([]string, bool) {
	fields := []string{}
	found := map[string]bool{}
	for _, field := range doc.Fields {
		if searchMatchField(q, doc, field) {
			if !contains(fields, field.Name) {
				fields = append(fields, field.Name)
			}
			lower := strings.ToLower(field.Value)
			for _, term := range q.terms {
				if strings.Contains(lower, term) {
					found[term] = true
				}
			}
		}
	}
	if q.mode == "text" {
		address, e := mail.ParseAddress(q.query)
		if e != nil || address.Address != q.query {
			for _, term := range q.terms {
				if !found[term] {
					return nil, false
				}
			}
		}
	}
	return fields, len(fields) > 0
}
func searchSnippet(q searchRequest, doc searchDocument) string {
	for _, field := range doc.Fields {
		if !searchMatchField(q, doc, field) {
			continue
		}
		text := strings.Join(strings.Fields(field.Value), " ")
		runes := []rune(text)
		if len(runes) <= 180 {
			return text
		}
		lower := strings.ToLower(text)
		needle := q.needle
		if q.mode == "text" && len(q.terms) > 0 {
			needle = q.terms[0]
		}
		at := strings.Index(lower, needle)
		if at < 0 {
			return searchClip(text, 180)
		}
		start := utf8.RuneCountInString(lower[:at]) - 45
		if start < 0 {
			start = 0
		}
		end := start + 180
		if end > len(runes) {
			end = len(runes)
		}
		out := string(runes[start:end])
		if start > 0 {
			out = "…" + out
		}
		if end < len(runes) {
			out += "…"
		}
		return out
	}
	return ""
}
func (a *App) searchRequestKey(q searchRequest) string {
	raw, _ := json.Marshal(struct {
		Query, Mode, SwitchID string
		Types                 []string
		Limit                 int
	}{q.query, q.mode, q.switchID, q.types, q.limit})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func (a *App) searchEncodeCursor(c searchCursor) string {
	raw, _ := json.Marshal(c)
	mac := hmac.New(sha256.New, a.store.securityKey)
	mac.Write([]byte("search-cursor-v1\x00"))
	mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (a *App) searchDecodeCursor(raw string) (searchCursor, error) {
	var cursor searchCursor
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return cursor, searchFail(400, "invalid search cursor")
	}
	payload, e := base64.RawURLEncoding.DecodeString(parts[0])
	if e != nil {
		return cursor, searchFail(400, "invalid search cursor")
	}
	signature, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return cursor, searchFail(400, "invalid search cursor")
	}
	mac := hmac.New(sha256.New, a.store.securityKey)
	mac.Write([]byte("search-cursor-v1\x00"))
	mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) || json.Unmarshal(payload, &cursor) != nil || cursor.Version != 1 || cursor.Offset < 0 || cursor.Offset > searchMaxRecords {
		return cursor, searchFail(400, "invalid search cursor")
	}
	return cursor, nil
}
func (a *App) search(w http.ResponseWriter, r *http.Request, user Record, session Session) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Vary", "Cookie, Authorization")
	if r.URL.Path != "/api/search" {
		problem(w, 404, "search endpoint not found")
		return
	}
	if r.Method != "GET" {
		problem(w, 405, "search is read-only")
		return
	}
	fail := func(e error) {
		var searchErr searchFailure
		if errors.As(e, &searchErr) {
			problem(w, searchErr.status, searchErr.message)
		} else {
			problem(w, 503, "search could not be completed")
		}
	}
	query, e := parseSearch(r)
	if e != nil {
		fail(e)
		return
	}
	documents, revision, e := a.searchSnapshot(query, user, session)
	if e != nil {
		fail(e)
		return
	}
	offset := 0
	if query.cursor != "" {
		cursor, e := a.searchDecodeCursor(query.cursor)
		if e != nil {
			fail(e)
			return
		}
		if cursor.Request != a.searchRequestKey(query) {
			problem(w, 400, "cursor belongs to a different search query")
			return
		}
		if cursor.Revision != revision {
			problem(w, 409, "visible search data or authority changed; restart the query")
			return
		}
		offset = cursor.Offset
	}
	matched := []searchItem{}
	counts := map[string]int{}
	for _, doc := range documents {
		fields, ok := searchMatches(query, doc)
		if !ok {
			continue
		}
		definition, _ := searchDefinitionFor(doc.Kind)
		matched = append(matched, searchItem{Kind: doc.Kind, ID: doc.ID, Key: doc.Key, Title: doc.Title, Subtitle: definition.label, Snippet: searchSnippet(query, doc), MatchedFields: fields, Href: doc.Href})
		counts[doc.Kind]++
	}
	if offset > len(matched) {
		problem(w, 400, "cursor offset is outside search results")
		return
	}
	end := offset + query.limit
	if end > len(matched) {
		end = len(matched)
	}
	result := searchResponse{Query: query.query, Mode: query.mode, Groups: []searchGroup{}, Total: len(matched), Limit: query.limit, Truncated: end < len(matched), Revision: revision}
	groupMap := map[string]int{}
	for _, item := range matched[offset:end] {
		index, ok := groupMap[item.Kind]
		if !ok {
			definition, _ := searchDefinitionFor(item.Kind)
			index = len(result.Groups)
			groupMap[item.Kind] = index
			result.Groups = append(result.Groups, searchGroup{Kind: item.Kind, Label: definition.label, Count: counts[item.Kind], Items: []searchItem{}})
		}
		result.Groups[index].Items = append(result.Groups[index].Items, item)
	}
	if result.Truncated {
		result.NextCursor = a.searchEncodeCursor(searchCursor{Version: 1, Request: a.searchRequestKey(query), Revision: revision, Offset: end})
	}
	if _, current, e := a.searchSnapshot(query, user, session); e != nil {
		fail(e)
		return
	} else if current != revision {
		problem(w, 409, "visible search data changed while searching; restart the query")
		return
	}
	respond(w, 200, result)
}
