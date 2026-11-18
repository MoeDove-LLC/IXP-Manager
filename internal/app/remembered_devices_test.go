package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type rememberedTestBrowser struct {
	t       *testing.T
	a       *App
	cookies map[string]*http.Cookie
	csrf    string
}

func (b *rememberedTestBrowser) request(method, path string, body any) *httptest.ResponseRecorder {
	b.t.Helper()
	var buffer bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buffer).Encode(body)
	}
	r := httptest.NewRequest(method, "https://manager.example.test"+path, &buffer)
	r.RemoteAddr = "127.0.0.1:10000"
	r.Header.Set("User-Agent", "Synthetic remembered browser")
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	for _, cookie := range b.cookies {
		r.AddCookie(cookie)
	}
	r.Header.Set("X-CSRF-Token", b.csrf)
	w := httptest.NewRecorder()
	b.a.ServeHTTP(w, r)
	return w
}
func (b *rememberedTestBrowser) receive(w *httptest.ResponseRecorder) Record {
	b.t.Helper()
	for _, cookie := range w.Result().Cookies() {
		if cookie.MaxAge < 0 {
			delete(b.cookies, cookie.Name)
		} else {
			b.cookies[cookie.Name] = cookie
		}
	}
	var data Record
	if json.Unmarshal(w.Body.Bytes(), &data) != nil {
		b.t.Fatal("non-JSON response")
	}
	if value, ok := data["csrfToken"].(string); ok {
		b.csrf = value
	}
	return data
}
func (b *rememberedTestBrowser) call(method, path string, body any) Record {
	b.t.Helper()
	w := b.request(method, path, body)
	if w.Code < 200 || w.Code >= 300 {
		b.t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
	}
	return b.receive(w)
}
func rememberedBrowser(t *testing.T, a *App) *rememberedTestBrowser {
	return &rememberedTestBrowser{t: t, a: a, cookies: map[string]*http.Cookie{}}
}
func rememberedLogin(t *testing.T, a *App, code string) *rememberedTestBrowser {
	t.Helper()
	b := rememberedBrowser(t, a)
	b.call("POST", "/api/login", Record{"email": "admin@example.net", "password": "very-long-test-password", "code": code, "rememberDevice": true})
	return b
}
func rememberedOnly(t *testing.T, a *App, cookie *http.Cookie) *rememberedTestBrowser {
	b := rememberedBrowser(t, a)
	b.cookies[rememberedCookieName] = cookie
	return b
}
func rememberedNoCookies(t *testing.T, w *httptest.ResponseRecorder, want int) {
	t.Helper()
	if w.Code != want || len(w.Result().Cookies()) != 0 {
		t.Fatal("failed auth changed browser cookies or status", w.Code, want)
	}
}
func TestRememberedExplicitOptInMFAHashAndSecureCookie(t *testing.T) {
	admin, dir := setupClient(t)
	if admin.ok("GET", "/api/setup", nil)["rememberDeviceAvailable"] != false {
		t.Fatal("plain deployment offered persistent authentication")
	}
	if w := admin.request("POST", "/api/login", Record{"email": "admin@example.net", "password": "very-long-test-password", "rememberDevice": true}); w.Code != 400 || len(w.Result().Cookies()) != 0 {
		t.Fatal("insecure opt-in issued credentials")
	}
	b := rememberedBrowser(t, admin.a)
	if b.call("GET", "/api/setup", nil)["rememberDeviceAvailable"] != true {
		t.Fatal("direct TLS did not enable secure persistent authentication")
	}
	b.call("POST", "/api/login", Record{"email": "admin@example.net", "password": "very-long-test-password"})
	if len(admin.a.store.Snapshot().RememberedDevices) != 0 || b.cookies[rememberedCookieName] != nil {
		t.Fatal("ordinary login implicitly remembered browser")
	}
	_, codes := enableMFA(t, admin)
	challenge := b.request("POST", "/api/login", Record{"email": "admin@example.net", "password": "very-long-test-password", "rememberDevice": true})
	rememberedNoCookies(t, challenge, 401)
	if !strings.Contains(challenge.Body.String(), `"mfaRequired":true`) || len(admin.a.store.Snapshot().RememberedDevices) != 0 {
		t.Fatal("password-only phase issued a remembered MFA grant")
	}
	b = rememberedLogin(t, admin.a, codes[0])
	cookie := b.cookies[rememberedCookieName]
	if cookie == nil || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" || cookie.MaxAge < 29*86400 || cookie.MaxAge > 30*86400 {
		t.Fatal("persistent cookie lacks bounded secure browser attributes")
	}
	if !b.cookies["moedove_session"].Secure {
		t.Fatal("TLS restore/login emitted an insecure ordinary session")
	}
	device := admin.a.store.Snapshot().RememberedDevices[0]
	parts := strings.Split(cookie.Value, ".")
	if device.ValidatorHash != rememberedHash(device.ID, parts[2]) || device.PreviousHash != "" || device.ExpiresAt.Sub(device.CreatedAt) != rememberedLifetime {
		t.Fatal("unexpected persisted validator or hard expiry")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(cookie.Value)) || bytes.Contains(raw, []byte(parts[2])) {
		t.Fatal("raw remembered credential persisted")
	}
	listed := b.request("GET", "/api/account/remembered-devices", nil)
	if listed.Code != 200 || strings.Contains(listed.Body.String(), device.ValidatorHash) || strings.Contains(listed.Body.String(), device.AccountBinding) || strings.Contains(listed.Body.String(), cookie.Value) {
		t.Fatal("device inventory exposed credential material")
	}
	items := responseRecord(t, listed)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["current"] != true {
		t.Fatal("current remembered device not identified")
	}
	if strings.Contains(admin.request("GET", "/api/export", nil).Body.String(), "rememberedDevices") {
		t.Fatal("ordinary resource export included browser grants")
	}
}
func TestRememberedConcurrentRestoreAndStaleResponsesNeverClearWinner(t *testing.T) {
	admin, _ := setupClient(t)
	login := rememberedLogin(t, admin.a, "")
	original := login.cookies[rememberedCookieName]
	before := admin.a.store.Snapshot().RememberedDevices[0]
	first, second := rememberedOnly(t, admin.a, original), rememberedOnly(t, admin.a, original)
	responses := make(chan *httptest.ResponseRecorder, 2)
	var group sync.WaitGroup
	for _, b := range []*rememberedTestBrowser{first, second} {
		group.Add(1)
		go func(b *rememberedTestBrowser) {
			defer group.Done()
			responses <- b.request("POST", "/api/session/restore", Record{})
		}(b)
	}
	group.Wait()
	close(responses)
	var winner, loser *httptest.ResponseRecorder
	for response := range responses {
		if response.Code == 200 {
			if winner != nil {
				t.Fatal("same validator restored twice")
			}
			winner = response
		} else {
			loser = response
		}
	}
	if winner == nil || loser == nil {
		t.Fatal("restore race did not have one winner")
	}
	rememberedNoCookies(t, loser, 401)
	if !strings.Contains(loser.Body.String(), `"retrySession":true`) {
		t.Fatal("rotation loser cannot request current-session reread")
	}
	recovered := rememberedBrowser(t, admin.a)
	recovered.receive(winner)
	recovered.receive(loser)
	recovered.call("GET", "/api/session", nil)
	if recovered.cookies[rememberedCookieName].Value == original.Value || recovered.csrf == login.csrf {
		t.Fatal("restore reused credential or CSRF")
	}
	after := admin.a.store.Snapshot().RememberedDevices[0]
	if after.ExpiresAt != before.ExpiresAt || after.CreatedAt != before.CreatedAt || after.ValidatorHash == before.ValidatorHash || after.PreviousHash != before.ValidatorHash {
		t.Fatal("rotation extended hard expiry or lost prior validator classification")
	}
	rememberedNoCookies(t, rememberedOnly(t, admin.a, original).request("POST", "/api/session/restore", Record{}), 401)
	// A stale persistent cookie must not destroy an already recovered session.
	recovered.cookies[rememberedCookieName] = original
	response := recovered.request("POST", "/api/session/restore", Record{})
	if response.Code != 200 || len(response.Result().Cookies()) != 0 {
		t.Fatal("valid session was rotated or cleared by stale persistent token")
	}
}
func TestRememberedRestoreExpiryReplayEntityAndSecurityChanges(t *testing.T) {
	for _, change := range []string{"expiry", "generation", "password", "role", "email", "entity", "deletion", "mfa-secret"} {
		t.Run(change, func(t *testing.T) {
			admin, _ := setupClient(t)
			b := rememberedLogin(t, admin.a, "")
			cookie := b.cookies[rememberedCookieName]
			id := accountID(admin.a)
			if err := admin.a.store.Update(func(s *State) error {
				u := find(s, "users", id)
				switch change {
				case "expiry":
					d := &s.RememberedDevices[0]
					d.CreatedAt = time.Now().Add(-rememberedLifetime - time.Second).UTC()
					d.LastUsedAt = d.CreatedAt
					d.ExpiresAt = d.CreatedAt.Add(rememberedLifetime)
				case "generation":
					invalidateSecurity(s, id)
				case "password":
					u["passwordHash"] = "changed-synthetic-password-hash"
				case "role":
					u["role"] = "viewer"
				case "email":
					u["email"] = "changed@example.test"
				case "entity":
					u["createdAt"] = time.Now().Add(time.Second).UTC().Format(time.RFC3339Nano)
				case "deletion":
					s.Resources["users"] = nil
					delete(s.Security, id)
				case "mfa-secret":
					security := s.Security[id]
					security.MFAEnabled = true
					security.Secret = "changed-encrypted-secret"
					s.Security[id] = security
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			rememberedNoCookies(t, rememberedOnly(t, admin.a, cookie).request("POST", "/api/session/restore", Record{}), 401)
			if b.request("GET", "/api/session", nil).Code != 401 || len(admin.a.store.Snapshot().RememberedDevices) != 0 {
				t.Fatal("stale device session or persistent grant survived security/entity change")
			}
		})
	}
}
func TestRememberedCurrentOtherAndAllRevocationLogoutAndSessionRemoval(t *testing.T) {
	admin, _ := setupClient(t)
	first, second := rememberedLogin(t, admin.a, ""), rememberedLogin(t, admin.a, "")
	firstID := strings.Split(first.cookies[rememberedCookieName].Value, ".")[1]
	secondCookie := second.cookies[rememberedCookieName]
	response := admin.ok("DELETE", "/api/account/remembered-devices/"+firstID, nil)
	if response["signedOut"] != false || first.request("GET", "/api/session", nil).Code != 401 {
		t.Fatal("remote revoke did not invalidate derived session or removed password-only caller")
	}
	rememberedNoCookies(t, rememberedOnly(t, admin.a, first.cookies[rememberedCookieName]).request("POST", "/api/session/restore", Record{}), 401)
	second.call("GET", "/api/session", nil)
	response = second.call("DELETE", "/api/account/remembered-devices", nil)
	if response["signedOut"] != true || second.request("GET", "/api/session", nil).Code != 401 {
		t.Fatal("all-device revocation retained current remembered session")
	}
	rememberedNoCookies(t, rememberedOnly(t, admin.a, secondCookie).request("POST", "/api/session/restore", Record{}), 401)
	logout := rememberedLogin(t, admin.a, "")
	logoutCookie := logout.cookies[rememberedCookieName]
	logout.call("POST", "/api/logout", nil)
	rememberedNoCookies(t, rememberedOnly(t, admin.a, logoutCookie).request("POST", "/api/session/restore", Record{}), 401)
	device := rememberedLogin(t, admin.a, "")
	deviceCookie := device.cookies[rememberedCookieName]
	sessions := device.call("GET", "/api/account/sessions", nil)["items"].([]any)
	for _, item := range sessions {
		v := item.(map[string]any)
		if v["current"] == true {
			device.call("DELETE", "/api/account/sessions/"+v["id"].(string), nil)
		}
	}
	rememberedNoCookies(t, rememberedOnly(t, admin.a, deviceCookie).request("POST", "/api/session/restore", Record{}), 401)
}
func TestRememberedMFAChangesResetAndRevokeOthersInvalidateGrants(t *testing.T) {
	admin, _ := setupClient(t)
	before := rememberedLogin(t, admin.a, "")
	original := before.cookies[rememberedCookieName]
	_, codes := enableMFA(t, admin)
	rememberedNoCookies(t, rememberedOnly(t, admin.a, original).request("POST", "/api/session/restore", Record{}), 401)
	b := rememberedLogin(t, admin.a, codes[0])
	cookie := b.cookies[rememberedCookieName]
	b.call("POST", "/api/account/recovery-codes", Record{"currentPassword": "very-long-test-password", "code": codes[1]})
	b.call("GET", "/api/session", nil)
	rememberedNoCookies(t, rememberedOnly(t, admin.a, cookie).request("POST", "/api/session/restore", Record{}), 401)
	// A separate fixture verifies the existing revoke-others generation gate.
	plain, _ := setupClient(t)
	remembered := rememberedLogin(t, plain.a, "")
	oldCookie := remembered.cookies[rememberedCookieName]
	remembered.call("POST", "/api/account/sessions/revoke-others", Record{"currentPassword": "very-long-test-password"})
	remembered.call("GET", "/api/session", nil)
	rememberedNoCookies(t, rememberedOnly(t, plain.a, oldCookie).request("POST", "/api/session/restore", Record{}), 401)
	password := rememberedLogin(t, plain.a, "")
	passwordCookie := password.cookies[rememberedCookieName]
	password.call("POST", "/api/account/password", Record{"currentPassword": "very-long-test-password", "newPassword": "new-remembered-password-fixture"})
	rememberedNoCookies(t, rememberedOnly(t, plain.a, passwordCookie).request("POST", "/api/session/restore", Record{}), 401)
}
func TestRememberedDomainAuthorityCSRFAndLegacyCookies(t *testing.T) {
	admin, _ := setupClient(t)
	b := rememberedLogin(t, admin.a, "")
	id := strings.Split(b.cookies[rememberedCookieName].Value, ".")[1]
	member := admin.create("members", Record{"name": "Separate remembered account", "asn": 64510})
	other := noteTestUser(t, admin, "member", "other-remembered@example.test", member)
	if w := other.request("DELETE", "/api/account/remembered-devices/"+id, nil); w.Code != 404 {
		t.Fatal("another account revoked remembered device", w.Code)
	}
	saved := b.csrf
	b.csrf = ""
	if b.request("DELETE", "/api/account/remembered-devices/"+id, nil).Code != 403 {
		t.Fatal("device revoke omitted CSRF")
	}
	b.csrf = saved
	tok := admin.ok("POST", "/api/account/tokens", Record{"name": "unrelated token", "currentPassword": "very-long-test-password"})
	for _, path := range []string{"/api/account/remembered-devices", "/api/session/restore"} {
		if w := bearerRequest(admin.a, str(tok, "token"), "GET", path, nil); w.Code != 403 {
			t.Fatal("API token reached remembered device domain")
		}
	}
	legacy := rememberedBrowser(t, admin.a)
	legacy.cookies["remember_web"] = &http.Cookie{Name: "remember_web", Value: "legacy-user|legacy-plaintext-token|legacy-passwordhash"}
	rememberedNoCookies(t, legacy.request("POST", "/api/session/restore", Record{}), 401)
	malformed := rememberedOnly(t, admin.a, &http.Cookie{Name: rememberedCookieName, Value: "rmd." + id + "." + strings.Repeat("A", 43)})
	rememberedNoCookies(t, malformed.request("POST", "/api/session/restore", Record{}), 401)
	b.call("GET", "/api/session", nil)
}
func TestRememberedRestartPersistenceAndInvalidState(t *testing.T) {
	admin, dir := setupClient(t)
	b := rememberedLogin(t, admin.a, "")
	cookie := b.cookies[rememberedCookieName]
	if err := admin.a.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	restored := rememberedOnly(t, reopened, cookie)
	restored.call("POST", "/api/session/restore", Record{})
	restored.call("GET", "/api/session", nil)
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state State
	if json.Unmarshal(raw, &state) != nil {
		t.Fatal("fixture state invalid")
	}
	state.RememberedDevices = append(state.RememberedDevices, state.RememberedDevices[0])
	raw, _ = json.Marshal(state)
	if os.WriteFile(filepath.Join(dir, "state.json"), raw, 0600) != nil {
		t.Fatal("could not write invalid fixture")
	}
	if s, err := OpenStore(dir); err == nil {
		s.Close()
		t.Fatal("duplicate remembered identity accepted on reopen")
	}
}

func TestRememberedEmptyStartupDoesNotSpendLoginBudgetAndOriginsFailClosed(t *testing.T) {
	admin, _ := setupClient(t)
	visitor := rememberedBrowser(t, admin.a)
	for range 30 {
		rememberedNoCookies(t, visitor.request("POST", "/api/session/restore", Record{}), 401)
	}
	visitor.call("POST", "/api/login", Record{"email": "admin@example.net", "password": "very-long-test-password", "rememberDevice": true})
	before := admin.a.store.Snapshot().RememberedDevices[0]
	for _, test := range []struct{ origin, site string }{{"https://other.example.test", "cross-site"}, {"http://manager.example.test", "same-site"}, {"https://child.manager.example.test", "same-site"}, {"", "cross-site"}} {
		r := httptest.NewRequest("POST", "https://manager.example.test/api/session/restore", strings.NewReader("{}"))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", test.origin)
		r.Header.Set("Sec-Fetch-Site", test.site)
		r.AddCookie(visitor.cookies[rememberedCookieName])
		w := httptest.NewRecorder()
		admin.a.ServeHTTP(w, r)
		rememberedNoCookies(t, w, 403)
	}
	after := admin.a.store.Snapshot().RememberedDevices[0]
	if before.ValidatorHash != after.ValidatorHash || before.LastUsedAt != after.LastUsedAt {
		t.Fatal("cross-origin request consumed persistent credential")
	}
}

func TestRememberedExplicitLoginRetiresConcurrentPreviousGrant(t *testing.T) {
	admin, _ := setupClient(t)
	other := admin.create("users", Record{"name": "Explicit second account", "email": "remembered-second@example.test", "password": "second-remembered-password-fixture", "role": "viewer"})
	first := rememberedLogin(t, admin.a, "")
	original := first.cookies[rememberedCookieName]
	oldID := strings.Split(original.Value, ".")[1]
	restoring := rememberedOnly(t, admin.a, original)
	late := restoring.request("POST", "/api/session/restore", Record{})
	if late.Code != 200 {
		t.Fatal("fixture restore did not rotate")
	}
	switching := rememberedOnly(t, admin.a, original)
	bad := switching.request("POST", "/api/login", Record{"email": "remembered-second@example.test", "password": "incorrect-fixture-password", "rememberDevice": true})
	rememberedNoCookies(t, bad, 401)
	snapshot := admin.a.store.Snapshot()
	if rememberedDevice(&snapshot, oldID) == nil {
		t.Fatal("unsuccessful password login revoked another device")
	}
	switching.call("POST", "/api/login", Record{"email": "remembered-second@example.test", "password": "second-remembered-password-fixture", "rememberDevice": true})
	secondCookie := switching.cookies[rememberedCookieName]
	snapshot = admin.a.store.Snapshot()
	if rememberedDevice(&snapshot, oldID) != nil {
		t.Fatal("full login failed to revoke concurrently rotated prior browser grant")
	}
	// Deliver the older successful restore after the explicit second login.
	// Browser cookies can be overwritten by HTTP response ordering; authority
	// must nevertheless remain revoked for the old account/device.
	switching.receive(late)
	if switching.request("GET", "/api/session", nil).Code != 401 {
		t.Fatal("late restore response resurrected the prior account")
	}
	rememberedNoCookies(t, switching.request("POST", "/api/session/restore", Record{}), 401)
	second := rememberedOnly(t, admin.a, secondCookie)
	result := second.call("POST", "/api/session/restore", Record{})
	if result["user"].(map[string]any)["id"] != other["id"] {
		t.Fatal("explicit second account grant was altered by stale cleanup")
	}
}

func TestRememberedCapacityRequiresExplicitRevocation(t *testing.T) {
	admin, _ := setupClient(t)
	if err := admin.a.store.Update(func(s *State) error {
		user := s.Resources["users"][0]
		for range maxUserRememberedDevices {
			if _, _, err := admin.a.newRememberedDevice(s, user, httptest.NewRequest("POST", "https://manager.example.test/api/login", nil)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b := rememberedBrowser(t, admin.a)
	w := b.request("POST", "/api/login", Record{"email": "admin@example.net", "password": "very-long-test-password", "rememberDevice": true})
	rememberedNoCookies(t, w, 409)
	if len(admin.a.store.Snapshot().RememberedDevices) != maxUserRememberedDevices {
		t.Fatal("capacity failure silently evicted or created a grant")
	}
	b.call("POST", "/api/login", Record{"email": "admin@example.net", "password": "very-long-test-password"})
}

func TestRememberedMailAndOfflinePasswordResetRevokePersistentGrants(t *testing.T) {
	admin, dir := setupClient(t)
	enableTestMail(admin.a) // Only encrypted local outbox data; no worker is run.
	b := rememberedLogin(t, admin.a, "")
	old := b.cookies[rememberedCookieName]
	b.call("POST", "/api/forgot-password", Record{"email": "admin@example.net"})
	reset := outboxToken(t, admin.a, "reset-password")
	b.call("POST", "/api/reset-password", Record{"token": reset, "newPassword": "remembered-reset-password-fixture"})
	rememberedNoCookies(t, rememberedOnly(t, admin.a, old).request("POST", "/api/session/restore", Record{}), 401)
	b.call("POST", "/api/login", Record{"email": "admin@example.net", "password": "remembered-reset-password-fixture", "rememberDevice": true})
	beforeOffline := b.cookies[rememberedCookieName]
	if err := admin.a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ResetPassword(dir, "admin@example.net", "remembered-offline-password-fixture", false); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	rememberedNoCookies(t, rememberedOnly(t, reopened, beforeOffline).request("POST", "/api/session/restore", Record{}), 401)
}
