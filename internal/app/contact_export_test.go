package app

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func contactExportFixture() State {
	return State{Resources: map[string][]Record{
		"members": {
			{"id": "active", "name": "Active", "status": "active", "joinedAt": "2026-11-18"},
			{"id": "pending", "name": "Pending", "status": "pending", "joinedAt": "2025-01-01", "leftAt": "2026-11-18"},
			{"id": "suspended", "name": "Suspended", "status": "suspended", "joinedAt": "2025-01-01", "leftAt": "2027-01-01"},
			{"id": "future", "name": "Future", "status": "active", "joinedAt": "2026-11-19"},
			{"id": "left", "name": "Left", "status": "active", "joinedAt": "2025-01-01", "leftAt": "2026-11-17"},
			{"id": "unknown", "name": "No date", "status": "active"},
		},
		"contact-groups": {
			{"id": "g1", "name": "Operations", "type": "ROLE", "active": false, "limitedTo": 3},
			{"id": "g2", "name": "Operations", "type": "SOCIAL"},
			{"id": "g3", "name": "Billing", "type": "ROLE"},
		},
		"contacts": {
			{"id": "c1", "memberId": "active", "name": "A, \"quoted\"\nname", "position": "Engineer", "email": "same@example.test", "phone": "+8612345", "mobile": "0012345", "notes": "line one\nline two", "passwordHash": "EXCLUDED-SECRET", "role": "EXCLUDED-ROLE"},
			{"id": "c2", "memberId": "pending", "name": "Pending", "email": "same@example.test"},
			{"id": "c3", "memberId": "suspended", "name": "Suspended", "email": "suspended@example.test"},
			{"id": "c4", "memberId": "future", "name": "EXCLUDED-FUTURE"},
			{"id": "c5", "memberId": "left", "name": "EXCLUDED-LEFT"},
			{"id": "c6", "memberId": "unknown", "name": "EXCLUDED-NO-JOIN"},
		},
		"contact-group-memberships": {
			{"id": "r1", "contactId": "c1", "groupId": "g1"}, {"id": "r2", "contactId": "c1", "groupId": "g3"}, {"id": "r3", "contactId": "c1", "groupId": "g1"},
			{"id": "r4", "contactId": "c2", "groupId": "g2"}, {"id": "r5", "contactId": "c3", "groupId": "g3"},
			{"id": "r6", "contactId": "c4", "groupId": "g1"}, {"id": "r7", "contactId": "c5", "groupId": "g1"}, {"id": "r8", "contactId": "c6", "groupId": "g1"},
		},
	}}
}

func TestContactExportLegacyScopeAndTypedFields(t *testing.T) {
	s := contactExportFixture()
	actor := Record{"role": "admin"}
	rows, err := contactRows(&s, actor, contactExportQuery{Name: "Operations"}, "2026-11-18")
	if err != nil || len(rows) != 2 || rows[0].CustStatus != "Normal" || rows[1].CustStatus != "Not Connected" || rows[0].Email != rows[1].Email {
		t.Fatal("same-name union, exact contact dedup or date boundary changed", rows, err)
	}
	raw, err := encodeContactExport(rows, "json")
	if err != nil {
		t.Fatal(err)
	}
	var result []map[string]any
	if err = json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result[0]) != 7 || result[0]["position"] != "Engineer" || result[0]["phone"] != "+8612345" || result[0]["mobile"] != "0012345" || strings.Contains(string(raw), "EXCLUDED") {
		t.Fatal("typed export changed values or exposed extra fields", string(raw))
	}
	typed, err := contactRows(&s, actor, contactExportQuery{Type: "ROLE"}, "2026-11-18")
	if err != nil || len(typed) != 2 || typed[1].CustStatus != "Suspended" {
		t.Fatal("type union must include inactive groups and suspended in-date members", typed, err)
	}
	limited, err := contactRows(&s, actor, contactExportQuery{Name: "Operations", MemberID: "pending"}, "2026-11-18")
	if err != nil || len(limited) != 1 || limited[0].Name != "Pending" {
		t.Fatal(limited, err)
	}
	empty, err := contactRows(&s, actor, contactExportQuery{Name: "missing"}, "2026-11-18")
	if err != nil || len(empty) != 0 {
		t.Fatal("unknown selector expanded export", empty, err)
	}
	opts, err := contactOptions(&s, actor, "2026-11-18")
	if err != nil || opts.Names[1].Groups != 2 || opts.Types[0].Groups != 2 {
		t.Fatal("duplicate group identity not disclosed", opts, err)
	}
}

func TestContactExportCSVQuotesFormulaAndBounds(t *testing.T) {
	row := ContactExportRow{Name: "=SUM(1,2)", Position: "A, \"quoted\"\nsecond line", Email: "+alias@example.test", Phone: "+8612345", Mobile: "0012345", Notes: "\t  ＠SUM(1+1)", CustStatus: "Normal"}
	raw, err := encodeContactExport([]ContactExportRow{row}, "csv")
	if err != nil {
		t.Fatal(err)
	}
	got, err := csv.NewReader(strings.NewReader(string(raw))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"name", "position", "email", "phone", "mobile", "notes", "cust_status"}, {"text: =SUM(1,2)", row.Position, "text: +alias@example.test", "text: +8612345", "text: 0012345", "text: " + row.Notes, "Normal"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("CSV column order, quotes, formula guard or telephone text lost", got)
	}
	for _, value := range []string{"=1", "+1", "-1", "@x", "  =x", "\ufeff＝1", "＋1", "－1", "＠x", "\nnormal"} {
		if !strings.HasPrefix(contactCSVCell(value), `"text: `) {
			t.Fatal("unsafe CSV leading characters", value)
		}
	}
	raw, err = encodeContactExport([]ContactExportRow{}, "csv")
	if err != nil || !strings.HasPrefix(string(raw), `"name","position"`) {
		t.Fatal("empty CSV omitted header", err)
	}
	raw, err = encodeContactExport([]ContactExportRow{}, "json")
	if err != nil || string(raw) != "[]\n" {
		t.Fatal("empty JSON compatibility", string(raw), err)
	}
	large := make([]ContactExportRow, 260)
	for i := range large {
		large[i].Notes = strings.Repeat(`"`, 65536)
	}
	for _, format := range []string{"csv", "json"} {
		if _, err := encodeContactExport(large, format); err == nil {
			t.Fatal("output bound missing", format)
		}
	}
	for _, query := range []string{"", "name=", "name=A&type=B", "name=A&name=B", "name=A&format=xml", "name=A&unexpected=yes", "options=1&name=A", "options=0", "name=%00", "name=%GG", "name=A&memberId=../other"} {
		if _, err := parseContactExportQuery(query); err == nil {
			t.Fatal("ambiguous export query accepted", query)
		}
	}
}

func TestContactExportStaffSessionAndReadOnlyHTTP(t *testing.T) {
	admin, _ := setupClient(t)
	member := admin.create("members", Record{"name": "Current member", "asn": 64500, "status": "active", "joinedAt": time.Now().UTC().Format("2006-01-02")})
	group := admin.create("contact-groups", Record{"name": "Operations", "type": "ROLE"})
	contact := admin.create("contacts", Record{"name": "Private contact", "email": "private-contact@example.test", "memberId": member["id"], "phone": "+8612345", "notes": "Authorized contact note"})
	admin.create("contact-group-memberships", Record{"name": "Group link", "contactId": contact["id"], "groupId": group["id"]})
	path := "/api/contact-export?name=" + url.QueryEscape("Operations")
	before, _ := json.Marshal(admin.a.store.Snapshot())
	w := admin.request("GET", path, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "private-contact@example.test") || w.Header().Get("X-Contact-Export-Count") != "1" || w.Header().Get("Cache-Control") != "private, no-store" || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatal(w.Code, w.Header(), w.Body.String())
	}
	after, _ := json.Marshal(admin.a.store.Snapshot())
	if string(before) != string(after) {
		t.Fatal("read-only export mutated state")
	}
	options := admin.request("GET", "/api/contact-export?options=1", nil)
	if options.Code != 200 || strings.Contains(options.Body.String(), "private-contact") || strings.Contains(options.Body.String(), "Authorized contact note") {
		t.Fatal("selector metadata exposed contact details", options.Body.String())
	}
	for _, role := range []string{"operator", "viewer", "member"} {
		u := noteTestUser(t, admin, role, role+"-export@example.test", member)
		want := 200
		if role == "member" {
			want = 403
		}
		for _, route := range []string{path, path + "&format=csv", "/api/contact-export?options=1"} {
			if response := u.request("GET", route, nil); response.Code != want {
				t.Fatal(role, response.Code, response.Body.String())
			}
		}
	}
	token := admin.ok("POST", "/api/account/tokens", Record{"name": "Ordinary export token", "currentPassword": "very-long-test-password", "scopes": []string{"resources:read", "exports:read"}})
	if w := bearerRequest(admin.a, str(token, "token"), "GET", path, nil); w.Code != 403 {
		t.Fatal("generic API token acquired private contact export", w.Code)
	}
	automation := admin.ok("POST", "/api/account/tokens", Record{"name": "Explicit contact automation", "currentPassword": "very-long-test-password", "scopes": []string{"contacts:export"}})
	raw := str(automation, "token")
	if response := bearerRequest(admin.a, raw, "GET", path, nil); response.Code != 200 || response.Body.String() != w.Body.String() || response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("scoped automation did not receive the same filtered private export", response.Code)
	}
	for _, other := range []string{"/api/contact-export?options=1", "/api/resources/contacts", "/api/export"} {
		if response := bearerRequest(admin.a, raw, "GET", other, nil); response.Code != 403 {
			t.Fatal("contact token escaped the selected export boundary", other, response.Code)
		}
	}
	if response := bearerRequest(admin.a, raw, "POST", path, Record{}); response.Code != 403 {
		t.Fatal("contact token acquired a write operation", response.Code)
	}
	item := Record(automation["item"].(map[string]any))
	admin.ok("DELETE", "/api/account/tokens/"+str(item, "id"), nil)
	if response := bearerRequest(admin.a, raw, "GET", path, nil); response.Code != 401 || strings.Contains(response.Body.String(), "private-contact@example.test") {
		t.Fatal("revoked token could still export contact addresses", response.Code)
	}
	if w := (&client{a: admin.a, t: t}).request("GET", path, nil); w.Code != 401 {
		t.Fatal("anonymous contact export", w.Code)
	}
	if w := admin.request("POST", path, Record{}); w.Code != 405 {
		t.Fatal("contact export accepted mutation", w.Code)
	}
}

func TestContactExportRechecksRevokedBrowserSession(t *testing.T) {
	admin, _ := setupClient(t)
	r := httptest.NewRequest("GET", "/api/contact-export?options=1", nil)
	r.AddCookie(admin.cookie)
	actor, session, ok := admin.a.authenticate(r)
	if !ok {
		t.Fatal("fixture authentication")
	}
	if w := admin.request("POST", "/api/logout", Record{}); w.Code != 200 {
		t.Fatal(w.Code)
	}
	w := httptest.NewRecorder()
	if !admin.a.contactExportRoutes(w, r, actor, session) || w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), `"members"`) {
		t.Fatal("handler used stale pre-authenticated session", w.Code, w.Body.String())
	}
}

type contactExportLockProbe struct {
	*httptest.ResponseRecorder
	a     *App
	t     *testing.T
	wrote bool
}

func (w *contactExportLockProbe) Write(data []byte) (int, error) {
	w.wrote = true
	if !w.a.store.mu.TryLock() {
		w.t.Error("response still holds Store lock; slow client would block inventory writes")
	} else {
		w.a.store.mu.Unlock()
	}
	return w.ResponseRecorder.Write(data)
}
func TestContactExportReleasesSnapshotLockBeforeResponse(t *testing.T) {
	admin, _ := setupClient(t)
	for _, path := range []string{"/api/contact-export?options=1", "/api/contact-export?type=missing", "/api/contact-export?type=missing&format=csv"} {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(admin.cookie)
		actor, session, ok := admin.a.authenticate(r)
		if !ok {
			t.Fatal("fixture authentication")
		}
		w := &contactExportLockProbe{ResponseRecorder: httptest.NewRecorder(), a: admin.a, t: t}
		if !admin.a.contactExportRoutes(w, r, actor, session) || w.Code != 200 || !w.wrote {
			t.Fatal("response probe did not run", w.Code)
		}
	}
}
