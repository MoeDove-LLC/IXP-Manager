package app

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

const memberUserRosterPath = "/api/member/users"

func TestMemberUserRosterCurrentTenantAndProjection(t *testing.T) {
	staff, _ := setupClient(t)
	one := str(staff.create("members", Record{"name": "One", "asn": 64501, "status": "active"}), "id")
	two := str(staff.create("members", Record{"name": "Two", "asn": 64502, "status": "active"}), "id")
	actor := staff.create("users", Record{
		"name": "Administrator", "email": "admin-member@example.net", "role": "member",
		"memberIds": []string{one, two}, "memberAdminIds": []string{one, two}, "password": "roster-admin-password",
	})
	shared := staff.create("users", Record{
		"name": "Shared", "email": "shared@example.net", "role": "member",
		"memberIds": []string{one, two}, "memberAdminIds": []string{two}, "password": "roster-member-password",
	})
	other := staff.create("users", Record{
		"name": "Other Tenant", "email": "z-other@example.net", "role": "member", "memberId": two,
		"password": "roster-other-password",
	})
	staff.create("users", Record{
		"name": "Operator", "email": "operator-roster@example.net", "role": "operator", "memberId": one,
		"password": "roster-operator-password",
	})
	administrator := docLoginClient(t, staff.a, str(actor, "email"), "roster-admin-password")
	ordinary := docLoginClient(t, staff.a, str(shared, "email"), "roster-member-password")
	enrollment := ordinary.ok(http.MethodPost, "/api/account/totp/enroll", Record{"currentPassword": "roster-member-password"})
	ordinary.ok(http.MethodPost, "/api/account/totp/verify", Record{"code": codeFor(str(enrollment, "secret"), time.Now())})

	for _, attempt := range []struct {
		name string
		w    *httptest.ResponseRecorder
		want int
	}{
		{"ordinary member", ordinary.request(http.MethodGet, memberUserRosterPath, nil), http.StatusForbidden},
		{"staff account", staff.request(http.MethodGet, memberUserRosterPath, nil), http.StatusForbidden},
		{"unauthenticated", (&client{a: staff.a, t: t}).request(http.MethodGet, memberUserRosterPath, nil), http.StatusUnauthorized},
	} {
		if attempt.w.Code != attempt.want {
			t.Errorf("%s: got HTTP %d, want %d: %s", attempt.name, attempt.w.Code, attempt.want, attempt.w.Body.String())
		}
	}
	apiToken := str(administrator.ok(http.MethodPost, "/api/account/tokens", Record{
		"currentPassword": "roster-admin-password", "name": "member-roster-token", "scopes": []string{"resources:read"},
	}), "token")
	if w := bearerRequest(staff.a, apiToken, http.MethodGet, memberUserRosterPath, nil); w.Code != http.StatusForbidden {
		t.Fatalf("member administrator token: HTTP %d: %s", w.Code, w.Body.String())
	}
	if w := administrator.request(http.MethodPost, memberUserRosterPath, nil); w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("write attempt: HTTP %d, Allow=%q: %s", w.Code, w.Header().Get("Allow"), w.Body.String())
	}

	item := func(user Record, role string, mfa bool) map[string]any {
		return map[string]any{
			"id": user["id"], "name": user["name"], "email": user["email"], "memberRole": role,
			"mfaEnabled": mfa, "createdAt": user["createdAt"],
		}
	}
	w := administrator.request(http.MethodGet, memberUserRosterPath+"?memberId="+two, nil)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control: %q", w.Header().Get("Cache-Control"))
	}
	want := Record{"memberId": one, "items": []any{item(actor, "member-admin", false), item(shared, "member", true)}}
	if got := responseRecord(t, w); !reflect.DeepEqual(got, want) {
		t.Fatalf("first tenant exact response: got %#v, want %#v", got, want)
	}

	administrator.ok(http.MethodPost, "/api/memberships/select", Record{"memberId": two})
	want = Record{"memberId": two, "items": []any{item(actor, "member-admin", false), item(shared, "member-admin", true), item(other, "member", false)}}
	if got := administrator.ok(http.MethodGet, memberUserRosterPath, nil); !reflect.DeepEqual(got, want) {
		t.Fatalf("second tenant exact response: got %#v, want %#v", got, want)
	}
}

func TestMemberUserRosterRejectsStaleBrowserAuthority(t *testing.T) {
	staff, _ := setupClient(t)
	one := str(staff.create("members", Record{"name": "One", "asn": 64501, "status": "active"}), "id")
	two := str(staff.create("members", Record{"name": "Two", "asn": 64502, "status": "pending"}), "id")
	actor := staff.create("users", Record{
		"name": "Administrator", "email": "roster-stale@example.net", "role": "member",
		"memberIds": []string{one, two}, "memberAdminIds": []string{one, two}, "password": "roster-stale-password",
	})
	administrator := docLoginClient(t, staff.a, str(actor, "email"), "roster-stale-password")
	request := httptest.NewRequest(http.MethodGet, memberUserRosterPath, nil)
	request.AddCookie(administrator.cookie)
	_, original, ok := staff.a.authenticate(request)
	if !ok {
		t.Fatal("initial request authentication failed")
	}
	assertCaptured := func(session Session, expected int) {
		t.Helper()
		w := httptest.NewRecorder()
		staff.a.memberUserRoster(w, request, session)
		if w.Code != expected {
			t.Fatalf("captured request: got HTTP %d, want %d: %s", w.Code, expected, w.Body.String())
		}
	}
	changed := administrator.ok(http.MethodPost, "/api/memberships/select", Record{"memberId": two})
	administrator.csrf = str(changed, "csrfToken")
	assertCaptured(original, http.StatusUnauthorized)
	if w := administrator.request(http.MethodGet, memberUserRosterPath, nil); w.Code != http.StatusForbidden {
		t.Fatalf("pending tenant: HTTP %d: %s", w.Code, w.Body.String())
	}
	changed = administrator.ok(http.MethodPost, "/api/memberships/select", Record{"memberId": one})
	administrator.csrf = str(changed, "csrfToken")
	assertCaptured(original, http.StatusUnauthorized)
	_, current, ok := staff.a.authenticate(request)
	if !ok {
		t.Fatal("current request authentication failed")
	}
	assertCaptured(current, http.StatusOK)

	// Change the current relationship without the normal session invalidation to exercise the roster read itself.
	if err := staff.a.store.Update(func(s *State) error {
		find(s, "users", str(actor, "id"))["memberAdminIds"] = []any{two}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertCaptured(current, http.StatusForbidden)
	administrator.ok(http.MethodPost, "/api/logout", nil)
	assertCaptured(current, http.StatusUnauthorized)
}
