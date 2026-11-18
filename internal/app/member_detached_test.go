package app

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"
)

func TestDetachedMemberReturnsThroughOwnInvitationAndMFA(t *testing.T) {
	staff, _ := setupClient(t)
	enableTestMail(staff.a)
	origin := str(staff.create("members", Record{"name": "Original organization", "asn": 64540, "status": "active"}), "id")
	destination := str(staff.create("members", Record{"name": "Inviting organization", "asn": 64541, "status": "active"}), "id")
	const administratorEmail, administratorPassword = "detach-admin@example.test", "detach-administrator-password"
	staff.create("users", Record{"name": "Organization administrator", "email": administratorEmail, "password": administratorPassword,
		"role": "member", "memberId": origin, "memberAdminIds": []string{origin}})
	const email, password = "detached-member@example.test", "detach-target-password"
	user := staff.create("users", Record{"name": "Organization member", "email": email, "password": password, "role": "member", "memberId": origin})
	id := str(user, "id")
	member := docLoginClient(t, staff.a, email, password)
	enrollment := member.ok(http.MethodPost, "/api/account/totp/enroll", Record{"currentPassword": password})
	recovery := member.ok(http.MethodPost, "/api/account/totp/verify", Record{"code": codeFor(str(enrollment, "secret"), time.Now())})["recoveryCodes"].([]any)
	administrator := docLoginClient(t, staff.a, administratorEmail, administratorPassword)
	path := "/api/member/users/" + id
	preview := administrator.request(http.MethodGet, path+"/removal-preview", nil)
	if preview.Code != http.StatusOK || preview.Result().Header.Get("ETag") == "" {
		t.Fatalf("detachment preview HTTP %d: %s", preview.Code, preview.Body.String())
	}
	if view := responseRecord(t, preview); view["detachesAccount"] != true || view["canRemove"] != true {
		t.Fatal("preview did not describe removal of the final organization")
	}
	remove := httptest.NewRequest(http.MethodDelete, path, nil)
	remove.AddCookie(administrator.cookie)
	remove.Header.Set("X-CSRF-Token", administrator.csrf)
	remove.Header.Set("If-Match", preview.Result().Header.Get("ETag"))
	removed := httptest.NewRecorder()
	staff.a.ServeHTTP(removed, remove)
	if removed.Code != http.StatusOK {
		t.Fatalf("detach HTTP %d: %s", removed.Code, removed.Body.String())
	}
	if old := member.request(http.MethodGet, "/api/session", nil); old.Code != http.StatusUnauthorized {
		t.Fatal("previous organization session remained valid", old.Code)
	}
	if signIn := (&client{a: staff.a, t: t}).request(http.MethodPost, "/api/login", Record{"email": email, "password": password}); signIn.Code != http.StatusUnauthorized {
		t.Fatal("detached member bypassed MFA", signIn.Code)
	}
	member = loginClient(t, staff.a, Record{"email": email, "password": password, "code": recovery[0]})
	owner := member.ok(http.MethodGet, "/api/session", nil)["user"].(map[string]any)
	if owner["id"] != id || owner["role"] != "member" || owner[membershipDetachedField] != true || owner["memberId"] != "" {
		t.Fatal("detachment changed account identity or global role", owner)
	}
	for _, endpoint := range []string{"/api/member/profile", "/api/dashboard", "/api/resources/members", "/api/search?q=example", "/api/memberships", "/api/account/security"} {
		if reply := member.request(http.MethodGet, endpoint, nil); reply.Code != http.StatusForbidden {
			t.Errorf("detached member reached %s with HTTP %d", endpoint, reply.Code)
		}
	}
	if reply := member.request(http.MethodPost, "/api/account/tokens", Record{"name": "unbound token", "currentPassword": password}); reply.Code != http.StatusForbidden {
		t.Fatal("detached member could issue a workspace token", reply.Code)
	}
	noCSRF := &client{a: staff.a, t: t, cookie: member.cookie}
	if reply := noCSRF.request(http.MethodPost, "/api/logout", nil); reply.Code != http.StatusForbidden {
		t.Fatal("detached account logout bypassed CSRF", reply.Code)
	}
	loggedOut := member.request(http.MethodPost, "/api/logout", nil)
	if loggedOut.Code != http.StatusOK || responseRecord(t, loggedOut)["ok"] != true {
		t.Fatalf("detached account logout HTTP %d: %s", loggedOut.Code, loggedOut.Body.String())
	}
	if !slices.ContainsFunc(loggedOut.Result().Cookies(), func(cookie *http.Cookie) bool {
		return cookie.Name == member.cookie.Name && cookie.MaxAge < 0
	}) {
		t.Fatal("logout did not expire the browser session cookie")
	}
	if reply := member.request(http.MethodGet, "/api/session", nil); reply.Code != http.StatusUnauthorized {
		t.Fatal("detached account logout did not invalidate its old session", reply.Code)
	}
	member = loginClient(t, staff.a, Record{"email": email, "password": password, "code": recovery[1]})
	staff.ok(http.MethodPost, "/api/invitations", Record{"email": email, "memberId": destination, "currentPassword": "very-long-test-password"})
	ticket := outboxToken(t, staff.a, "accept-invitation")
	if reply := member.request(http.MethodPost, "/api/invitations/accept", Record{"token": ticket, "currentPassword": password}); reply.Code != http.StatusForbidden {
		t.Fatal("detached member accepted an invitation without MFA", reply.Code)
	}
	member.ok(http.MethodPost, "/api/invitations/accept", Record{"token": ticket, "currentPassword": password, "code": recovery[2]})
	member = loginClient(t, staff.a, Record{"email": email, "password": password, "code": recovery[3]})
	owner = member.ok(http.MethodGet, "/api/session", nil)["user"].(map[string]any)
	if owner["id"] != id || owner["memberId"] != destination || owner[membershipDetachedField] != nil || owner["memberRole"] != "member" {
		t.Fatal("rejoin did not restore the same account to the approved organization", owner)
	}
	if current := member.ok(http.MethodGet, "/api/member/profile", nil); current["id"] != destination {
		t.Fatal("rejoin regained access to the old organization", current)
	}
}

func TestDetachedMemberStateIsServerManaged(t *testing.T) {
	staff, _ := setupClient(t)
	request := Record{"name": "Managed identity fixture", "email": "marker-fixture@example.test", "password": "org-member-password", "role": "member", membershipDetachedField: true}
	if response := staff.request(http.MethodPost, "/api/resources/users", request); response.Code != http.StatusBadRequest {
		t.Fatal("generic creation accepted a detachment marker", response.Code)
	}
	delete(request, membershipDetachedField)
	if response := staff.request(http.MethodPost, "/api/resources/users", request); response.Code != http.StatusBadRequest {
		t.Fatal("generic creation accepted a member without a membership", response.Code)
	}
}
