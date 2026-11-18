package app

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"testing"
)

func memberRemovalRequest(browser *client, id, match string) *http.Request {
	r := httptest.NewRequest(http.MethodDelete, memberUserRosterPath+"/"+id, nil)
	r.AddCookie(browser.cookie)
	r.Header.Set("X-CSRF-Token", browser.csrf)
	if match != "" {
		r.Header.Set("If-Match", match)
	}
	return r
}

func removeMemberUserRequest(browser *client, id, match string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	browser.a.ServeHTTP(w, memberRemovalRequest(browser, id, match))
	return w
}

func TestMemberUserRemovalPreservesAccountAndOtherTenant(t *testing.T) {
	staff, _ := setupClient(t)
	one := str(staff.create("members", Record{"name": "One", "asn": 64501, "status": "active"}), "id")
	two := str(staff.create("members", Record{"name": "Two", "asn": 64502, "status": "active"}), "id")
	moderator := staff.create("users", Record{
		"name": "Administrator", "email": "removal-admin@example.net", "role": "member",
		"memberIds": []string{one, two}, "memberAdminIds": []string{one, two}, "password": "removal-admin-password",
	})
	target := staff.create("users", Record{
		"name": "Target", "email": "removal-target@example.net", "role": "member", "memberId": one,
		"memberIds": []string{one, two}, "memberAdminIds": []string{two}, "password": "very-long-test-password",
	})
	other := staff.create("users", Record{
		"name": "Other", "email": "removal-outside@example.net", "role": "member", "memberId": two, "password": "removal-outside-password",
	})
	administrator := docLoginClient(t, staff.a, str(moderator, "email"), "removal-admin-password")
	targetBrowser := docLoginClient(t, staff.a, str(target, "email"), "very-long-test-password")
	targetID := str(target, "id")
	targetPath := memberUserRosterPath + "/" + targetID
	if w := administrator.request(http.MethodGet, memberUserRosterPath+"/"+str(other, "id")+"/removal-preview", nil); w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant preview: HTTP %d %s", w.Code, w.Body.String())
	}
	if w := removeMemberUserRequest(administrator, str(other, "id"), ""); w.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant removal: HTTP %d %s", w.Code, w.Body.String())
	}
	for _, browser := range []*client{staff, targetBrowser} {
		if w := browser.request(http.MethodGet, targetPath+"/removal-preview", nil); w.Code != http.StatusForbidden {
			t.Fatalf("unauthorized preview: HTTP %d %s", w.Code, w.Body.String())
		}
		if w := removeMemberUserRequest(browser, targetID, ""); w.Code != http.StatusForbidden {
			t.Fatalf("unauthorized removal: HTTP %d %s", w.Code, w.Body.String())
		}
	}
	moderatorToken := str(administrator.ok(http.MethodPost, "/api/account/tokens", Record{
		"currentPassword": "removal-admin-password", "name": "moderator", "scopes": []string{"resources:read"},
	}), "token")
	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		path := targetPath
		if method == http.MethodGet {
			path += "/removal-preview"
		}
		if w := bearerRequest(staff.a, moderatorToken, method, path, nil); w.Code != http.StatusForbidden {
			t.Fatalf("bearer %s: HTTP %d %s", method, w.Code, w.Body.String())
		}
	}

	_, codes := enableMFA(t, targetBrowser)
	remembered := rememberedBrowser(t, staff.a)
	remembered.call(http.MethodPost, "/api/login", Record{
		"email": target["email"], "password": "very-long-test-password", "code": codes[0], "rememberDevice": true,
	})
	rememberedCookie := remembered.cookies[rememberedCookieName]
	if rememberedCookie == nil {
		t.Fatal("target browser was not remembered")
	}
	changed := targetBrowser.ok(http.MethodPost, "/api/memberships/select", Record{"memberId": two})
	targetBrowser.csrf = str(changed, "csrfToken")
	targetToken := str(targetBrowser.ok(http.MethodPost, "/api/account/tokens", Record{
		"currentPassword": "very-long-test-password", "code": codes[1], "name": "remaining-tenant", "scopes": []string{"resources:read"},
	}), "token")
	if w := bearerRequest(staff.a, targetToken, http.MethodGet, "/api/resources/members", nil); w.Code != http.StatusOK {
		t.Fatalf("target token before removal: HTTP %d %s", w.Code, w.Body.String())
	}

	previewResponse := administrator.request(http.MethodGet, targetPath+"/removal-preview?memberId="+two, nil)
	preview := responseRecord(t, previewResponse)
	etag := previewResponse.Header().Get("ETag")
	wantPreview := Record{
		"memberId": one, "memberName": "One", "target": map[string]any{"id": targetID, "name": target["name"], "email": target["email"], "memberRole": "member"},
		"canRemove": true, "blockedReason": "", "detachesAccount": false, "signsOutCurrentSession": false, "_etag": preview["_etag"],
	}
	if !reflect.DeepEqual(preview, wantPreview) || !validResourceETag(str(preview, "_etag")) || etag != `"`+str(preview, "_etag")+`"` {
		t.Fatalf("wrong full preview %#v (ETag %q)", preview, etag)
	}
	if w := removeMemberUserRequest(administrator, targetID, ""); w.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing CAS: HTTP %d %s", w.Code, w.Body.String())
	}
	if w := removeMemberUserRequest(administrator, targetID, "*"); w.Code != http.StatusBadRequest {
		t.Fatalf("wildcard CAS: HTTP %d %s", w.Code, w.Body.String())
	}
	secondSession := docLoginClient(t, staff.a, str(moderator, "email"), "removal-admin-password")
	if w := removeMemberUserRequest(secondSession, targetID, etag); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("preview acquired by another session: HTTP %d %s", w.Code, w.Body.String())
	}
	staleRequest := memberRemovalRequest(administrator, targetID, etag)
	_, staleSession, ok := administrator.a.authenticate(staleRequest)
	if !ok {
		t.Fatal("captured release request was not authenticated")
	}
	changed = administrator.ok(http.MethodPost, "/api/memberships/select", Record{"memberId": two})
	administrator.csrf = str(changed, "csrfToken")
	if w := removeMemberUserRequest(administrator, targetID, etag); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("cross-tenant CAS: HTTP %d %s", w.Code, w.Body.String())
	}
	changed = administrator.ok(http.MethodPost, "/api/memberships/select", Record{"memberId": one})
	administrator.csrf = str(changed, "csrfToken")
	w := httptest.NewRecorder()
	administrator.a.memberUserRemoval(w, staleRequest, staleSession)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("stale session returning to initial tenant: HTTP %d %s", w.Code, w.Body.String())
	}
	fresh := administrator.request(http.MethodGet, targetPath+"/removal-preview", nil).Header().Get("ETag")
	if err := staff.a.store.Update(func(s *State) error {
		find(s, "users", targetID)["name"] = "Renamed Target"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if w := removeMemberUserRequest(administrator, targetID, fresh); w.Code != http.StatusPreconditionFailed {
		t.Fatalf("changed target facts: HTTP %d %s", w.Code, w.Body.String())
	}
	before := staff.a.store.Snapshot()
	etag = administrator.request(http.MethodGet, targetPath+"/removal-preview", nil).Header().Get("ETag")
	w = removeMemberUserRequest(administrator, targetID, etag)
	if got := responseRecord(t, w); !reflect.DeepEqual(got, Record{"ok": true, "memberId": one, "userId": targetID, "signedOut": false}) {
		t.Fatalf("removal response: %#v", got)
	}
	state := staff.a.store.Snapshot()
	kept := find(&state, "users", targetID)
	if str(kept, "role") != "member" || str(kept, "memberId") != two || !slices.Equal(membershipIDs(kept), []string{two}) || !memberAdministrator(kept, two) || memberAccountDetached(kept) ||
		kept["passwordHash"] != find(&before, "users", targetID)["passwordHash"] || kept["createdAt"] != target["createdAt"] {
		t.Fatal("removal changed the account identity, password, or remaining tenant authority")
	}
	expectedSecurity := before.Security[targetID]
	expectedSecurity.Generation++
	expectedSecurity.Tokens = nil
	if expectedSecurity.Secret == "" || !reflect.DeepEqual(state.Security[targetID], expectedSecurity) || len(state.RememberedDevices) != 0 {
		t.Fatal("security invalidation changed preserved MFA or retained credentials")
	}
	lastAudit := state.Audit[len(state.Audit)-1]
	if lastAudit.Action != "remove-member-user" || lastAudit.Actor != moderator["id"] || lastAudit.ResourceID != one || lastAudit.Details["userId"] != targetID {
		t.Fatalf("wrong audit entry: %#v", lastAudit)
	}
	if w := targetBrowser.request(http.MethodGet, "/api/session", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("prior session in remaining tenant survived: HTTP %d", w.Code)
	}
	if w := bearerRequest(staff.a, targetToken, http.MethodGet, "/api/resources/members", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("prior token bound to remaining tenant survived: HTTP %d", w.Code)
	}
	w = rememberedOnly(t, staff.a, rememberedCookie).request(http.MethodPost, "/api/session/restore", Record{})
	rememberedNoCookies(t, w, http.StatusUnauthorized)
	relogged := loginClient(t, staff.a, Record{"email": target["email"], "password": "very-long-test-password", "code": codes[2]})
	if profile := relogged.ok(http.MethodGet, "/api/member/profile", nil); profile["id"] != two {
		t.Fatal("fresh account login did not select the remaining tenant")
	}
	if w := removeMemberUserRequest(administrator, targetID, etag); w.Code != http.StatusNotFound {
		t.Fatalf("applied relationship remains removable: HTTP %d", w.Code)
	}
}

func TestMemberUserRemovalConcurrentSelfDetachKeepsMemberAdministrator(t *testing.T) {
	staff, _ := setupClient(t)
	memberID := str(staff.create("members", Record{"name": "One", "asn": 64501, "status": "active"}), "id")
	accounts := []Record{
		staff.create("users", Record{"name": "First", "email": "first-admin@example.net", "role": "member", "memberId": memberID,
			"memberAdminIds": []string{memberID}, "password": "removal-admin-password"}),
		staff.create("users", Record{"name": "Second", "email": "second-admin@example.net", "role": "member", "memberId": memberID,
			"memberAdminIds": []string{memberID}, "password": "removal-admin-password"}),
	}
	browsers := []*client{
		docLoginClient(t, staff.a, str(accounts[0], "email"), "removal-admin-password"),
		docLoginClient(t, staff.a, str(accounts[1], "email"), "removal-admin-password"),
	}
	requests := make([]*http.Request, len(browsers))
	for i, browser := range browsers {
		id := str(accounts[i], "id")
		w := browser.request(http.MethodGet, memberUserRosterPath+"/"+id+"/removal-preview", nil)
		preview := responseRecord(t, w)
		if preview["canRemove"] != true || preview["detachesAccount"] != true || preview["signsOutCurrentSession"] != true {
			t.Fatalf("unavailable self-detach: %#v", preview)
		}
		requests[i] = memberRemovalRequest(browser, id, w.Header().Get("ETag"))
	}
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, len(requests))
	for _, request := range requests {
		go func() {
			<-start
			w := httptest.NewRecorder()
			staff.a.ServeHTTP(w, request)
			results <- w
		}()
	}
	close(start)
	var detachedID string
	for range requests {
		w := <-results
		switch w.Code {
		case http.StatusOK:
			got := responseRecord(t, w)
			if detachedID != "" || got["signedOut"] != true || len(w.Result().Cookies()) != 0 {
				t.Fatalf("unexpected concurrent detach or sign-out receipt: %#v", got)
			}
			detachedID = str(got, "userId")
		case http.StatusPreconditionFailed:
		default:
			t.Fatalf("concurrent detach: HTTP %d %s", w.Code, w.Body.String())
		}
	}
	if detachedID == "" {
		t.Fatal("no concurrent detach committed")
	}
	state := staff.a.store.Snapshot()
	for i, account := range accounts {
		id := str(account, "id")
		current := find(&state, "users", id)
		if id == detachedID {
			if !memberAccountDetached(current) || len(membershipIDs(current)) != 0 || str(current, "memberId") != "" || memberAdministrator(current, memberID) {
				t.Fatal("detached account retains member authority")
			}
			if w := browsers[i].request(http.MethodGet, "/api/session", nil); w.Code != http.StatusUnauthorized {
				t.Fatalf("detached account's old session survived: HTTP %d", w.Code)
			}
			continue
		}
		if !memberAdministrator(current, memberID) || !contains(membershipIDs(current), memberID) {
			t.Fatal("remaining tenant administrator lost authority")
		}
		w := browsers[i].request(http.MethodGet, memberUserRosterPath+"/"+id+"/removal-preview", nil)
		preview := responseRecord(t, w)
		if preview["canRemove"] != false || preview["blockedReason"] != "last-member-admin" {
			t.Fatalf("last administrator was not blocked: %#v", preview)
		}
		if w := removeMemberUserRequest(browsers[i], id, w.Header().Get("ETag")); w.Code != http.StatusConflict {
			t.Fatalf("last administrator removal: HTTP %d %s", w.Code, w.Body.String())
		}
	}
}
