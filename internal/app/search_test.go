package app

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func searchRead(t *testing.T, c *client, query string, parameters url.Values) searchResponse {
	t.Helper()
	if parameters == nil {
		parameters = url.Values{}
	}
	parameters.Set("q", query)
	w := c.request("GET", "/api/search?"+parameters.Encode(), nil)
	if w.Code != 200 {
		t.Fatalf("search HTTP %d: %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("private search was cacheable")
	}
	var result searchResponse
	if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	return result
}
func searchIDs(result searchResponse) map[string]bool {
	out := map[string]bool{}
	for _, group := range result.Groups {
		for _, item := range group.Items {
			out[item.ID] = true
		}
	}
	return out
}
func searchCaptured(t *testing.T, c *client) (Record, Session) {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/search?q=visible", nil)
	r.AddCookie(c.cookie)
	user, session, ok := c.a.authenticate(r)
	if !ok {
		t.Fatal("fixture could not authenticate")
	}
	return user, session
}
func TestSearchMemberProjectionTenantSwitchAndHiddenRevision(t *testing.T) {
	c, _ := setupClient(t)
	one := c.create("members", Record{"name": "Harbor One", "asn": 64501, "notes": "vault-only-marker", "billingEmail": "billing-vault@example.net"})
	two := c.create("members", Record{"name": "Harbor Two", "asn": 64502})
	c.create("users", Record{"name": "Search member", "email": "search-member@example.net", "role": "member", "memberIds": []string{str(one, "id"), str(two, "id")}, "password": "search-member-password"})
	member := docLoginClient(t, c.a, "search-member@example.net", "search-member-password")
	public := c.create("tags", Record{"name": "Harbor public tag"})
	internal := c.create("tags", Record{"name": "Harbor internal tag", "internal": true})
	c.create("member-tags", Record{"name": "Harbor public association", "memberId": one["id"], "tagId": public["id"]})
	hiddenLink := c.create("member-tags", Record{"name": "Harbor private association", "memberId": one["id"], "tagId": internal["id"]})
	visibleNote := c.create("notes", Record{"name": "Harbor visible memo", "memberId": one["id"], "visibility": "member", "body": "authorized bulletin"})
	hiddenNote := c.create("notes", Record{"name": "Harbor internal memo", "memberId": one["id"], "visibility": "internal", "body": "vault-only-marker"})
	folder := c.ok("POST", "/api/directories", Record{"name": "Harbor own folder", "visibility": "member", "memberId": one["id"]})
	ownDoc := responseRecord(t, c.upload("/api/documents", "Harbor-own.txt", "document-body-never-indexed", map[string]string{"directoryId": str(folder, "id")}))
	otherDoc := responseRecord(t, c.upload("/api/documents", "Harbor-other.txt", "private two", map[string]string{"visibility": "member", "memberId": str(two, "id")}))
	staffDoc := responseRecord(t, c.upload("/api/documents", "Harbor-staff.txt", "private staff", nil))
	first := searchRead(t, member, "Harbor", url.Values{"limit": {"2"}})
	if first.Total < 5 || !first.Truncated || first.NextCursor == "" {
		t.Fatalf("real authorized pagination absent: %+v", first)
	}
	all := searchRead(t, member, "Harbor", url.Values{"limit": {"100"}})
	ids := searchIDs(all)
	for _, id := range []string{str(one, "id"), str(public, "id"), str(visibleNote, "id"), str(ownDoc, "id")} {
		if !ids[id] {
			t.Fatal("own visible result missing", id)
		}
	}
	for _, id := range []string{str(two, "id"), str(internal, "id"), str(hiddenLink, "id"), str(hiddenNote, "id"), str(otherDoc, "id"), str(staffDoc, "id")} {
		if ids[id] {
			t.Fatal("hidden tenant/internal entity leaked", id)
		}
	}
	for _, query := range []string{"vault-only-marker", "billing-vault@example.net", "document-body-never-indexed"} {
		if result := searchRead(t, member, query, nil); result.Total != 0 {
			t.Fatalf("private field/body matched %s: %+v", query, result)
		}
	}
	if result := searchRead(t, c, "document-body-never-indexed", nil); result.Total != 0 {
		t.Fatal("search read document blob content")
	}
	if e := c.a.store.Update(func(s *State) error {
		find(s, "members", str(one, "id"))["notes"] = "new hidden text"
		find(s, "members", str(two, "id"))["name"] = "Different hidden tenant"
		find(s, "tags", str(internal, "id"))["description"] = "other private text"
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	next := searchRead(t, member, "Harbor", url.Values{"limit": {"2"}, "cursor": {first.NextCursor}})
	if next.Revision != first.Revision || next.Total != first.Total {
		t.Fatal("hidden-only mutations changed public search version/count")
	}
	actor, session := searchCaptured(t, member)
	switched := member.ok("POST", "/api/memberships/select", Record{"memberId": two["id"]})
	member.csrf = str(switched, "csrfToken")
	stale := httptest.NewRecorder()
	c.a.search(stale, httptest.NewRequest("GET", "/api/search?q=Harbor", nil), actor, session)
	if stale.Code != 403 {
		t.Fatal("captured old-tenant authority survived selection change", stale.Code)
	}
	if w := member.request("GET", "/api/search?"+url.Values{"q": {"Harbor"}, "limit": {"2"}, "cursor": {first.NextCursor}}.Encode(), nil); w.Code != 409 {
		t.Fatal("old-tenant cursor survived tenant switch", w.Code)
	}
	second := searchRead(t, member, "Harbor", nil)
	if searchIDs(second)[str(one, "id")] || searchIDs(second)[str(ownDoc, "id")] {
		t.Fatal("new tenant search retained old tenant results")
	}
}

func TestSearchScopesUsersAndReadOnlyContract(t *testing.T) {
	c, _ := setupClient(t)
	c.create("members", Record{"name": "Scope marker", "asn": 64501})
	doc := responseRecord(t, c.upload("/api/documents", "Scope-marker.txt", "content", nil))
	viewerRow := c.create("users", Record{"name": "private account label", "email": "private-user@example.net", "role": "viewer", "password": "viewer-search-password"})
	viewer := docLoginClient(t, c.a, "private-user@example.net", "viewer-search-password")
	if result := searchRead(t, viewer, "private-user@example.net", nil); result.Total != 0 {
		t.Fatal("non-admin enumerated users by email")
	}
	if result := searchRead(t, c, "private-user@example.net", nil); !searchIDs(result)[str(viewerRow, "id")] {
		t.Fatal("admin browser email lookup missing")
	}
	token := func(scopes []string) string {
		return str(c.ok("POST", "/api/account/tokens", Record{"name": "Search scope", "currentPassword": "very-long-test-password", "scopes": scopes}), "token")
	}
	resources := token([]string{"resources:read"})
	documents := token([]string{"documents:read"})
	monitoring := token([]string{"monitoring:read"})
	for _, tc := range []struct{ token, kind string }{{resources, "members"}, {documents, "documents"}} {
		w := bearerRequest(c.a, tc.token, "GET", "/api/search?q=Scope", nil)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var result searchResponse
		json.Unmarshal(w.Body.Bytes(), &result)
		if len(result.Groups) != 1 || result.Groups[0].Kind != tc.kind {
			t.Fatal("token crossed a domain read scope", result)
		}
		if tc.kind == "documents" && !searchIDs(result)[str(doc, "id")] {
			t.Fatal("documents-only token could not search readable metadata")
		}
	}
	for _, tc := range []struct{ token, path string }{{resources, "/api/search?q=private-user@example.net&types=users"}, {documents, "/api/search?q=Scope&types=members"}, {monitoring, "/api/search?q=Scope"}} {
		if w := bearerRequest(c.a, tc.token, "GET", tc.path, nil); w.Code != 403 {
			t.Fatalf("wrong scope accepted: %d %s", w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/search?q=a", "/api/search?q=valid&types=secrets", "/api/search?q=valid&types=members,members", "/api/search?q=valid&memberId=another", "/api/search?q=valid&limit=101"} {
		if w := c.request("GET", path, nil); w.Code != 400 {
			t.Fatal("malformed search accepted", path, w.Code)
		}
	}
	if w := c.request("POST", "/api/search?q=Scope", nil); w.Code != 405 {
		t.Fatal("search accepted mutation method", w.Code)
	}
	if w := viewer.request("GET", "/api/search?q=private-user&types=users", nil); w.Code != 403 {
		t.Fatal("explicit types bypassed user-list authorization")
	}
}

func TestSearchLegacyModesAndStablePagination(t *testing.T) {
	c, _ := setupClient(t)
	member := c.create("members", Record{"name": "Search network", "shortName": "SN", "registeredName": "Search registered company", "asn": 64501, "irrASSet": "AS-SEARCH"})
	loc := c.create("locations", Record{"name": "Search DC"})
	sw := c.create("switches", Record{"name": "Search switch", "locationId": loc["id"]})
	port := c.create("ports", Record{"name": "Search port", "switchId": sw["id"], "speed": 1000})
	vlan := c.create("vlans", Record{"name": "Search LAN", "tag": 100, "ipv4Prefix": "192.0.2.0/24", "ipv6Prefix": "2001:db8::/64"})
	connection := c.create("connections", Record{"name": "Search access", "memberId": member["id"], "portId": port["id"], "vlanId": vlan["id"], "ipv4": "192.0.2.10", "ipv6": "2001:db8::10", "status": "active"})
	mac := c.create("mac-addresses", Record{"name": "Registered address", "connectionId": connection["id"], "address": "02:00:00:00:00:01"})
	if e := c.a.store.Update(func(s *State) error {
		s.Resources["patch-ports"] = append(s.Resources["patch-ports"], Record{"id": "legacy-patch-ports-123", "name": "PPP-123", "status": "connected", "memberId": member["id"], "circuitId": "CIRCUIT-ALPHA"})
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	for _, tc := range []struct{ q, mode, id string }{{"AS64501", "asn", str(member, "id")}, {"64501", "asn", str(member, "id")}, {"AS-SEARCH", "as-set", str(member, "id")}, {"192.0.2.10", "ip", str(connection, "id")}, {"2001:0db8:0:0:0:0:0:0010", "ip", str(connection, "id")}, {".10", "ipv4-suffix", str(connection, "id")}, {":10", "ipv6-suffix", str(connection, "id")}, {"020000000001", "mac", str(mac, "id")}, {"02-00-00-00-00-01", "mac", str(mac, "id")}, {"PPP-123", "patch-port", "legacy-patch-ports-123"}, {"xc: CIRCUIT-ALPHA", "cross-connect", "legacy-patch-ports-123"}, {"@admin", "username", accountID(c.a)}} {
		result := searchRead(t, c, tc.q, nil)
		if result.Mode != tc.mode || !searchIDs(result)[tc.id] {
			t.Fatalf("legacy lookup %q: %+v", tc.q, result)
		}
	}
	first := searchRead(t, c, "Search", url.Values{"limit": {"1"}})
	if !first.Truncated {
		t.Fatal("pagination fixture too small")
	}
	allIDs := map[string]bool{}
	page := first
	for {
		for id := range searchIDs(page) {
			if allIDs[id] {
				t.Fatal("stable cursor duplicated an entity")
			}
			allIDs[id] = true
		}
		if page.NextCursor == "" {
			break
		}
		page = searchRead(t, c, "Search", url.Values{"limit": {"1"}, "cursor": {page.NextCursor}})
	}
	if len(allIDs) != first.Total {
		t.Fatal("cursor pagination lost authorized results")
	}
	if e := c.a.store.Update(func(s *State) error {
		find(s, "members", str(member, "id"))["name"] = "Visible name changed"
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if w := c.request("GET", "/api/search?"+url.Values{"q": {"Search"}, "limit": {"1"}, "cursor": {first.NextCursor}}.Encode(), nil); w.Code != 409 {
		t.Fatal("stale visible snapshot cursor accepted", w.Code)
	}
	if w := c.request("GET", "/api/search?"+url.Values{"q": {"Search"}, "cursor": {"forged.signature"}}.Encode(), nil); w.Code != 400 {
		t.Fatal("forged cursor accepted")
	}
}

func TestSearchDocumentAncestorAndSnapshotAuthority(t *testing.T) {
	c, _ := setupClient(t)
	m := c.create("members", Record{"name": "Member network", "asn": 64501})
	c.create("users", Record{"name": "Reader", "email": "ancestor-reader@example.net", "role": "member", "memberId": m["id"], "password": "ancestor-search-password"})
	member := docLoginClient(t, c.a, "ancestor-reader@example.net", "ancestor-search-password")
	folder := c.ok("POST", "/api/directories", Record{"name": "visible folder", "visibility": "member", "memberId": m["id"]})
	doc := responseRecord(t, c.upload("/api/documents", "ancestor-marker.txt", "private body", map[string]string{"directoryId": str(folder, "id")}))
	if !searchIDs(searchRead(t, member, "ancestor-marker", nil))[str(doc, "id")] {
		t.Fatal("own inherited document missing")
	}
	if e := c.a.store.Update(func(s *State) error { directoryByID(s, str(folder, "id")).Visibility = "staff"; return nil }); e != nil {
		t.Fatal(e)
	}
	if searchRead(t, member, "ancestor-marker", nil).Total != 0 {
		t.Fatal("stale child visibility bypassed hidden ancestor")
	}
	actor, session := searchCaptured(t, member)
	if e := c.a.store.Update(func(s *State) error {
		security := s.Security[session.UserID]
		security.Generation++
		s.Security[session.UserID] = security
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	w := httptest.NewRecorder()
	c.a.search(w, httptest.NewRequest("GET", "/api/search?q=visible", nil), actor, session)
	if w.Code != 403 {
		t.Fatal("captured authority survived security generation change")
	}
}

func TestSearchLearnedMACUsesAuthorizedProjectionAndBindings(t *testing.T) {
	f := newMACLearningFixture(t)
	result := searchRead(t, f.c, "020000000001", nil)
	found := false
	for _, group := range result.Groups {
		for _, item := range group.Items {
			if item.Kind == "learned-macs" {
				found = true
				if !strings.HasPrefix(item.Href, "#ipam?") || !strings.Contains(item.Href, "switchId="+f.switchID) || !strings.Contains(item.Href, "candidateId=") {
					t.Fatal("learned result is not navigable", item)
				}
			}
		}
	}
	if !found {
		t.Fatal("legacy learned MAC lookup is missing")
	}
	for _, secret := range []string{"device-private-hostname", "previous-tenant-alias", "private-noc@example.net"} {
		if value := searchRead(t, f.c, secret, url.Values{"types": {"learned-macs"}}); value.Total != 0 {
			t.Fatal("raw SNMP private identity/alias was indexed", secret)
		}
	}
	one := str(f.c.ok("POST", "/api/account/tokens", Record{"name": "MAC read", "currentPassword": "very-long-test-password", "scopes": []string{"devices:read"}, "switchIds": []string{f.switchID}}), "token")
	w := bearerRequest(f.c.a, one, "GET", "/api/search?q=020000000001&types=learned-macs", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var scoped searchResponse
	json.Unmarshal(w.Body.Bytes(), &scoped)
	if scoped.Total != 1 {
		t.Fatal("bound MAC read failed", scoped)
	}
	otherSW := Record{"id": f.otherSwitch}
	other := str(f.c.ok("POST", "/api/account/tokens", Record{"name": "Other MAC read", "currentPassword": "very-long-test-password", "scopes": []string{"devices:read"}, "switchIds": []string{str(otherSW, "id")}}), "token")
	w = bearerRequest(f.c.a, other, "GET", "/api/search?q=020000000001&types=learned-macs", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	json.Unmarshal(w.Body.Bytes(), &scoped)
	if scoped.Total != 0 {
		t.Fatal("MAC results escaped current token switch binding")
	}
	rawBefore := result.Revision
	f.snapshot.ObservedAt = f.snapshot.ObservedAt.Add(time.Second)
	f.snapshot.Identity.Contact = "new-private-contact@example.net"
	responseRecord(t, bearerRequest(f.c.a, f.token, "POST", "/api/devices/observations", f.snapshot))
	after := searchRead(t, f.c, "020000000001", nil)
	if after.Revision != rawBefore {
		t.Fatal("raw-only SNMP metadata changed visible search version")
	}
}

func TestSearchUnicodeSnippetAndBounds(t *testing.T) {
	q := searchRequest{mode: "text", query: "needle", needle: "needle", terms: []string{"needle"}}
	doc := searchDocument{Kind: "notes", Fields: []searchField{{"body", strings.Repeat("Ⱥ", 250) + "needle" + strings.Repeat("文", 250)}}}
	snippet := searchSnippet(q, doc)
	if !utf8.ValidString(snippet) || !strings.Contains(snippet, "needle") || utf8.RuneCountInString(snippet) > 182 {
		t.Fatal("Unicode case folding corrupted/broke snippet bound")
	}
	for _, raw := range []string{strings.Repeat("a", 129), strings.Repeat("a", 65), "one two three four five six seven eight nine"} {
		if _, e := parseSearch(httptest.NewRequest("GET", "/api/search?q="+url.QueryEscape(raw), nil)); e == nil {
			t.Fatal("unbounded query accepted")
		}
	}
}

func TestSearchTypedReceiverUsesItsOwnScopeAndMemberGeneration(t *testing.T) {
	f := newFlowAPIFixture(t)
	created := f.c.ok("POST", "/api/sflow/receivers", Record{"name": "Search receiver mailbox", "spec": receiverFixtureSpec(f, false)})
	id := str(created, "id")
	found := searchRead(t, f.c, "Search receiver", url.Values{"types": {"sflow-receivers"}})
	if !searchIDs(found)[id] {
		t.Fatal("typed receiver was omitted")
	}
	item := found.Groups[0].Items[0]
	if !strings.HasPrefix(item.Href, "#sflow?") || !strings.Contains(item.Href, "receiverId="+id) {
		t.Fatal("receiver navigation is not stable", item.Href)
	}
	resources := str(f.c.ok("POST", "/api/account/tokens", Record{"name": "No receiver scope", "currentPassword": "very-long-test-password", "scopes": []string{"resources:read"}}), "token")
	if w := bearerRequest(f.c.a, resources, "GET", "/api/search?q=Search&types=sflow-receivers", nil); w.Code != 403 {
		t.Fatal("generic resource scope exposed typed receivers")
	}
	w := bearerRequest(f.c.a, f.writer, "GET", "/api/search?q=Search&types=sflow-receivers", nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result searchResponse
	json.Unmarshal(w.Body.Bytes(), &result)
	if !searchIDs(result)[id] {
		t.Fatal("bound sflow:read token lost its receiver")
	}
	if e := f.c.a.store.Update(func(s *State) error {
		for i := range s.SFlowReceivers {
			if s.SFlowReceivers[i].Spec.ID == id {
				s.SFlowReceivers[i].MemberRevision = strings.Repeat("0", 64)
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	f.c.create("users", Record{"name": "Receiver search member", "email": "receiver-search@example.net", "role": "member", "memberId": f.member["id"], "password": "receiver-search-password"})
	memberClient := docLoginClient(t, f.c.a, "receiver-search@example.net", "receiver-search-password")
	if value := searchRead(t, memberClient, "Search receiver", url.Values{"types": {"sflow-receivers"}}); value.Total != 0 {
		t.Fatal("reused member entity inherited a historical receiver search result")
	}
}

func TestSearchCoreTokenRequiresBothEndpointBindings(t *testing.T) {
	f := newCoreAPIFixture(t)
	bundle := f.create(t)
	name := str(bundle, "name")
	one := str(f.c.ok("POST", "/api/account/tokens", Record{"name": "One endpoint", "currentPassword": "very-long-test-password", "scopes": []string{"core:read"}, "switchIds": []string{f.swA}}), "token")
	both := str(f.c.ok("POST", "/api/account/tokens", Record{"name": "Both endpoints", "currentPassword": "very-long-test-password", "scopes": []string{"core:read"}, "switchIds": []string{f.swA, f.swB}}), "token")
	for _, tc := range []struct {
		token string
		want  int
	}{{one, 0}, {both, 1}} {
		w := bearerRequest(f.c.a, tc.token, "GET", "/api/search?"+url.Values{"q": {name}, "types": {"core-bundles"}}.Encode(), nil)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var result searchResponse
		json.Unmarshal(w.Body.Bytes(), &result)
		if result.Total != tc.want {
			t.Fatal("core search ignored endpoint scope", result)
		}
	}
}
