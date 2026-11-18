package app

import (
	"bytes"
	"encoding/base32"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTOTPRFC6238SHA1Vectors(t *testing.T) {
	key := []byte("12345678901234567890")
	for _, test := range []struct {
		at   int64
		want string
	}{{59, "94287082"}, {1111111109, "07081804"}, {1111111111, "14050471"}, {1234567890, "89005924"}, {2000000000, "69279037"}, {20000000000, "65353130"}} {
		if got := totp(key, test.at/30, 8); got != test.want {
			t.Errorf("time %d: got %s want %s", test.at, got, test.want)
		}
	}
}
func accountID(a *App) string { return str(a.store.Snapshot().Resources["users"][0], "id") }
func codeFor(secret string, at time.Time) string {
	key, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	return totp(key, at.Unix()/30, 6)
}
func bearerRequest(a *App, raw, method, path string, body any) *httptest.ResponseRecorder {
	var b bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&b).Encode(body)
	}
	r := httptest.NewRequest(method, path, &b)
	r.Header.Set("Authorization", "Bearer "+raw)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	a.ServeHTTP(w, r)
	return w
}
func loginClient(t *testing.T, a *App, body Record) *client {
	t.Helper()
	c := &client{a: a, t: t}
	w := c.request("POST", "/api/login", body)
	if w.Code != 200 {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	c.cookie = w.Result().Cookies()[0]
	var data Record
	json.Unmarshal(w.Body.Bytes(), &data)
	c.csrf = str(data, "csrfToken")
	return c
}
func enableMFA(t *testing.T, c *client) (string, []string) {
	t.Helper()
	enrollment := c.ok("POST", "/api/account/totp/enroll", Record{"currentPassword": "very-long-test-password"})
	secret := str(enrollment, "secret")
	codes := c.ok("POST", "/api/account/totp/verify", Record{"code": codeFor(secret, time.Now())})["recoveryCodes"].([]any)
	out := []string{}
	for _, v := range codes {
		out = append(out, v.(string))
	}
	return secret, out
}

func TestAccountMFAEncryptionChallengeAndRecovery(t *testing.T) {
	c, dir := setupClient(t)
	secret, codes := enableMFA(t, c)
	status := c.ok("GET", "/api/account/security", nil)
	if status["mfaEnabled"] != true || status["recoveryCodesRemaining"] != float64(10) {
		t.Fatal(status)
	}
	stored, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	if bytes.Contains(stored, []byte(secret)) || bytes.Contains(stored, []byte(codes[0])) {
		t.Fatal("MFA secret or recovery code stored in plaintext")
	}
	keyInfo, err := os.Stat(filepath.Join(dir, "security.key"))
	if err != nil || keyInfo.Mode().Perm() != 0600 {
		t.Fatal("instance encryption key is not private")
	}
	outsider := &client{a: c.a, t: t}
	challenge := outsider.request("POST", "/api/login", Record{"email": "admin@example.net", "password": "very-long-test-password"})
	if challenge.Code != 401 || !strings.Contains(challenge.Body.String(), `"mfaRequired":true`) {
		t.Fatal("MFA challenge missing")
	}
	recovered := loginClient(t, c.a, Record{"email": "admin@example.net", "password": "very-long-test-password", "code": codes[0]})
	recovered.ok("GET", "/api/session", nil)
	replay := outsider.request("POST", "/api/login", Record{"email": "admin@example.net", "password": "very-long-test-password", "code": codes[0]})
	if replay.Code != 401 {
		t.Fatal("recovery code replay accepted")
	}
	if _, err = c.a.decryptSecret("another-user", c.a.store.Snapshot().Security[accountID(c.a)].Secret); err == nil {
		t.Fatal("encrypted secret can be moved between users")
	}
}

func TestConcurrentTOTPAndRecoverySingleUse(t *testing.T) {
	c, _ := setupClient(t)
	id := accountID(c.a)
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	encrypted, err := c.a.encryptSecret(id, secret)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1234567890, 0)
	code := codeFor(secret, now)
	codes, hashes := recoveryCodes()
	if err = c.a.store.Update(func(s *State) error {
		s.Security[id] = SecurityUser{MFAEnabled: true, Secret: encrypted, LastStep: -1, RecoveryHashes: hashes}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, credential := range []string{code, codes[0]} {
		var successes atomic.Int32
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				err := c.a.store.Update(func(s *State) error {
					u := s.Security[id]
					if err := c.a.consumeMFA(id, &u, credential, now); err != nil {
						return err
					}
					s.Security[id] = u
					return nil
				})
				if err == nil {
					successes.Add(1)
				}
			}()
		}
		wg.Wait()
		if successes.Load() != 1 {
			t.Fatalf("expected one use, got %d", successes.Load())
		}
	}
}

func TestTokenScopesHashExpiryAndSensitiveRoutes(t *testing.T) {
	c, _ := setupClient(t)
	created := c.ok("POST", "/api/account/tokens", Record{"name": "read only", "currentPassword": "very-long-test-password"})
	raw := str(created, "token")
	if raw == "" {
		t.Fatal("missing one-time token")
	}
	for _, path := range []string{"/api/resources/members", "/api/dashboard"} {
		if w := bearerRequest(c.a, raw, "GET", path, nil); w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := bearerRequest(c.a, raw, "POST", "/api/resources/locations", Record{"name": "no"}); w.Code != 403 {
		t.Fatal("default token can write")
	}
	for _, path := range []string{"/api/settings", "/api/account/security", "/api/resources/users", "/api/session"} {
		if w := bearerRequest(c.a, raw, "GET", path, nil); w.Code != 403 {
			t.Fatalf("token accessed protected route %s: %d", path, w.Code)
		}
	}
	if w := bearerRequest(c.a, raw, "POST", "/api/deployments/example/approve", Record{"checksum": "x"}); w.Code != 403 {
		t.Fatal("token approved deployment")
	}
	list := c.request("GET", "/api/account/tokens", nil)
	if strings.Contains(list.Body.String(), raw) || strings.Contains(list.Body.String(), digest(raw)) {
		t.Fatal("token hash or token leaked")
	}
	snapshot := c.a.store.Snapshot()
	if snapshot.Security[accountID(c.a)].Tokens[0].Hash != digest(raw) {
		t.Fatal("token hash not persisted")
	}
	id := accountID(c.a)
	if err := c.a.store.Update(func(s *State) error {
		u := s.Security[id]
		u.Tokens[0].ExpiresAt = time.Now().Add(-time.Second)
		s.Security[id] = u
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if w := bearerRequest(c.a, raw, "GET", "/api/resources/members", nil); w.Code != 401 {
		t.Fatal("expired token accepted")
	}
}

func TestPasswordChangeRevokesSessionsAndTokens(t *testing.T) {
	c, _ := setupClient(t)
	other := loginClient(t, c.a, Record{"email": "admin@example.net", "password": "very-long-test-password"})
	raw := str(c.ok("POST", "/api/account/tokens", Record{"name": "old", "currentPassword": "very-long-test-password"}), "token")
	if w := c.request("POST", "/api/account/password", Record{"currentPassword": "wrong", "newPassword": "a-new-long-password"}); w.Code != 403 {
		t.Fatal("password change accepted invalid reauth")
	}
	c.ok("POST", "/api/account/password", Record{"currentPassword": "very-long-test-password", "newPassword": "a-new-long-password"})
	if w := c.request("GET", "/api/session", nil); w.Code != 401 {
		t.Fatal("current session survived password change")
	}
	if w := other.request("GET", "/api/session", nil); w.Code != 401 {
		t.Fatal("other session survived password change")
	}
	if w := bearerRequest(c.a, raw, "GET", "/api/resources/members", nil); w.Code != 401 {
		t.Fatal("token survived password change")
	}
	loginClient(t, c.a, Record{"email": "admin@example.net", "password": "a-new-long-password"})
}

func TestSessionRevocationAndCSRF(t *testing.T) {
	c, _ := setupClient(t)
	other := loginClient(t, c.a, Record{"email": "admin@example.net", "password": "very-long-test-password"})
	list := c.ok("GET", "/api/account/sessions", nil)["items"].([]any)
	var id string
	for _, item := range list {
		v := item.(map[string]any)
		if v["current"] == false {
			id = v["id"].(string)
		}
	}
	if id == "" {
		t.Fatal("missing other session")
	}
	old := c.csrf
	c.csrf = "wrong"
	if w := c.request("DELETE", "/api/account/sessions/"+id, nil); w.Code != 403 {
		t.Fatal("CSRF accepted")
	}
	c.csrf = old
	c.ok("DELETE", "/api/account/sessions/"+id, nil)
	if w := other.request("GET", "/api/session", nil); w.Code != 401 {
		t.Fatal("revoked session still works")
	}
	c.ok("GET", "/api/session", nil)
}

func TestMFAKeyLossFailsClosed(t *testing.T) {
	c, dir := setupClient(t)
	enableMFA(t, c)
	c.a.Close()
	if err := os.Remove(filepath.Join(dir, "security.key")); err != nil {
		t.Fatal(err)
	}
	if a, err := New(Config{DataDir: dir}); err == nil {
		a.Close()
		t.Fatal("silently regenerated missing encryption key")
	}
}

func TestMemberTokenCannotElevateOrReadAnotherMember(t *testing.T) {
	admin, _ := setupClient(t)
	own := admin.create("members", Record{"name": "Own", "asn": 64510, "status": "active"})
	other := admin.create("members", Record{"name": "Other", "asn": 64511, "status": "active"})
	admin.create("users", Record{"name": "Member", "email": "member@example.net", "role": "member", "memberId": own["id"], "password": "member-long-password"})
	member := loginClient(t, admin.a, Record{"email": "member@example.net", "password": "member-long-password"})
	if w := member.request("POST", "/api/account/tokens", Record{"name": "elevate", "scopes": []string{"resources:write"}, "currentPassword": "member-long-password"}); w.Code != 403 {
		t.Fatal("member minted elevated token")
	}
	raw := str(member.ok("POST", "/api/account/tokens", Record{"name": "mine", "currentPassword": "member-long-password"}), "token")
	w := bearerRequest(admin.a, raw, "GET", "/api/resources/members", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), str(own, "id")) || strings.Contains(w.Body.String(), str(other, "id")) {
		t.Fatal("token bypassed member data scope")
	}
}
func TestSecurityStatePersistsAndRejectsStaleSessionGeneration(t *testing.T) {
	c, dir := setupClient(t)
	secret, codes := enableMFA(t, c)
	id := accountID(c.a)
	before := c.a.store.Snapshot().Security[id]
	if err := c.a.store.Update(func(s *State) error { invalidateSecurity(s, id); return nil }); err != nil {
		t.Fatal(err)
	}
	stale := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	snapshot := c.a.store.Snapshot()
	c.a.issueVersion(stale, r, find(&snapshot, "users", id), before.Generation)
	session := &client{a: c.a, t: t, cookie: stale.Result().Cookies()[0]}
	if w := session.request("GET", "/api/session", nil); w.Code != 401 {
		t.Fatal("stale login completion resurrected invalidated sessions")
	}
	c.a.Close()
	reopened, err := New(Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	state := reopened.store.Snapshot().Security[id]
	plain, err := reopened.decryptSecret(id, state.Secret)
	if err != nil || plain != secret || !state.MFAEnabled {
		t.Fatal("MFA state did not survive restart")
	}
	loginClient(t, reopened, Record{"email": "admin@example.net", "password": "very-long-test-password", "code": codes[0]})
}

func TestMFAEnableAndRevokeOthersRejectDelayedLoginCompletion(t *testing.T) {
	c, _ := setupClient(t)
	id := accountID(c.a)
	before := c.a.store.Snapshot()
	oldGeneration := before.Security[id].Generation
	enableMFA(t, c)
	c.ok("GET", "/api/session", nil)
	w := httptest.NewRecorder()
	request := httptest.NewRequest("GET", "/", nil)
	c.a.issueVersion(w, request, find(&before, "users", id), oldGeneration)
	stale := &client{a: c.a, t: t, cookie: w.Result().Cookies()[0]}
	if got := stale.request("GET", "/api/session", nil); got.Code != 401 {
		t.Fatal("MFA enable allowed a delayed password-only login")
	}

	d, _ := setupClient(t)
	id = accountID(d.a)
	before = d.a.store.Snapshot()
	other := loginClient(t, d.a, Record{"email": "admin@example.net", "password": "very-long-test-password"})
	raw := str(d.ok("POST", "/api/account/tokens", Record{"name": "automation", "currentPassword": "very-long-test-password"}), "token")
	d.ok("POST", "/api/account/sessions/revoke-others", Record{"currentPassword": "very-long-test-password"})
	d.ok("GET", "/api/session", nil)
	if got := other.request("GET", "/api/session", nil); got.Code != 401 {
		t.Fatal("other session survived revoke-others")
	}
	if got := bearerRequest(d.a, raw, "GET", "/api/resources/members", nil); got.Code != 200 {
		t.Fatal("session-only revocation incorrectly revoked API tokens")
	}
	w = httptest.NewRecorder()
	d.a.issueVersion(w, request, find(&before, "users", id), before.Security[id].Generation)
	stale = &client{a: d.a, t: t, cookie: w.Result().Cookies()[0]}
	if got := stale.request("GET", "/api/session", nil); got.Code != 401 {
		t.Fatal("revoke-others allowed delayed login completion")
	}
}
