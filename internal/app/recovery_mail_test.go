package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/notify"
)

type captureSender struct {
	mu       sync.Mutex
	messages []notify.Message
	fail     bool
	started  chan struct{}
	wait     bool
}

func (s *captureSender) Send(ctx context.Context, m notify.Message) error {
	s.mu.Lock()
	s.messages = append(s.messages, m)
	s.mu.Unlock()
	if s.started != nil {
		select {
		case s.started <- struct{}{}:
		default:
		}
	}
	if s.wait {
		<-ctx.Done()
		return ctx.Err()
	}
	if s.fail {
		return errors.New("SMTP echoed very-private-token-and-body")
	}
	return nil
}
func enableTestMail(a *App) *captureSender {
	sender := &captureSender{}
	a.mailConfig = notify.Config{Enabled: true, Host: "127.0.0.1", Port: 2525, Mode: "plain", AllowLoopbackPlain: true, From: "ixp@example.test", BaseURL: "https://manager.example.test"}
	a.mailer = sender
	return sender
}
func outboxToken(t *testing.T, a *App, page string) string {
	t.Helper()
	s := a.store.Snapshot()
	for index := len(s.Outbox.Entries) - 1; index >= 0; index-- {
		message, err := notify.OpenMessage(a.store.securityKey, s.Outbox.Entries[index])
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(message.Text, "\n") {
			if !strings.Contains(line, "/#"+page+"?token=") {
				continue
			}
			parsed, err := url.Parse(line)
			if err != nil {
				t.Fatal("invalid fixture mail URL")
			}
			if parsed.Scheme != "https" || parsed.Host != "manager.example.test" {
				t.Fatal("mail URL did not use configured application origin")
			}
			parts := strings.SplitN(parsed.Fragment, "?", 2)
			query, _ := url.ParseQuery(parts[1])
			return query.Get("token")
		}
	}
	t.Fatal("test mail token missing")
	return ""
}
func TestForgotPasswordNoEnumerationAndEncryptedOutbox(t *testing.T) {
	c, _ := setupClient(t)
	enableTestMail(c.a)
	known := c.request("POST", "/api/forgot-password", Record{"email": "admin@example.net"})
	unknown := c.request("POST", "/api/forgot-password", Record{"email": "unknown@example.net"})
	if known.Code != 202 || unknown.Code != 202 || known.Body.String() != unknown.Body.String() {
		t.Fatal("forgot response enumerated accounts")
	}
	raw := outboxToken(t, c.a, "reset-password")
	snapshot := c.a.store.Snapshot()
	encoded, _ := json.Marshal(snapshot)
	if strings.Contains(string(encoded), raw) {
		t.Fatal("raw reset token persisted outside encryption")
	}
	c.request("POST", "/api/forgot-password", Record{"email": "admin@example.net"})
	if len(c.a.store.Snapshot().Outbox.Entries) != 1 {
		t.Fatal("repeated request bypassed per-account cooldown")
	}
	list := c.request("GET", "/api/notifications", nil)
	if list.Code != 200 || strings.Contains(list.Body.String(), raw) || strings.Contains(list.Body.String(), "sealed") || strings.Contains(list.Body.String(), "tokenHash") || strings.Contains(list.Body.String(), "Text") {
		t.Fatal("outbox metadata exposed secret payload")
	}
	c.a.mailConfig.Enabled = false
	disabled := c.request("POST", "/api/forgot-password", Record{"email": "other@example.net"})
	if disabled.Code != 202 || disabled.Body.String() != known.Body.String() {
		t.Fatal("disabled recovery leaked account/service detail")
	}
}
func TestRecoveryResetPreservesMFAAndConsumesToken(t *testing.T) {
	c, _ := setupClient(t)
	enableTestMail(c.a)
	_, codes := enableMFA(t, c)
	id := accountID(c.a)
	secret := c.a.store.Snapshot().Security[id].Secret
	c.request("POST", "/api/forgot-password", Record{"email": "admin@example.net"})
	raw := outboxToken(t, c.a, "reset-password")
	anonymous := &client{a: c.a, t: t}
	missing := anonymous.request("POST", "/api/reset-password", Record{"token": raw, "newPassword": "reset-new-long-password"})
	if missing.Code != 403 || !strings.Contains(missing.Body.String(), `"mfaRequired":true`) {
		t.Fatal("mail token bypassed enabled MFA")
	}
	if w := anonymous.request("POST", "/api/reset-password", Record{"token": raw, "newPassword": "reset-new-long-password", "code": "invalid"}); w.Code != 403 {
		t.Fatal("invalid MFA accepted")
	}
	anonymous.ok("POST", "/api/reset-password", Record{"token": raw, "newPassword": "reset-new-long-password", "code": codes[0]})
	after := c.a.store.Snapshot().Security[id]
	if !after.MFAEnabled || after.Secret != secret {
		t.Fatal("password recovery disabled MFA")
	}
	if w := c.request("GET", "/api/session", nil); w.Code != 401 {
		t.Fatal("session survived mail reset")
	}
	if w := anonymous.request("POST", "/api/reset-password", Record{"token": raw, "newPassword": "another-long-password", "code": codes[1]}); w.Code != 400 {
		t.Fatal("single-use recovery token replayed")
	}
	loginClient(t, c.a, Record{"email": "admin@example.net", "password": "reset-new-long-password", "code": codes[1]})
}
func TestRecoveryGenerationBindingAndExpiry(t *testing.T) {
	c, _ := setupClient(t)
	enableTestMail(c.a)
	c.request("POST", "/api/forgot-password", Record{"email": "admin@example.net"})
	raw := outboxToken(t, c.a, "reset-password")
	id := accountID(c.a)
	if err := c.a.store.Update(func(s *State) error { invalidateSecurity(s, id); return nil }); err != nil {
		t.Fatal(err)
	}
	anonymous := &client{a: c.a, t: t}
	if w := anonymous.request("POST", "/api/reset-password", Record{"token": raw, "newPassword": "new-reset-password"}); w.Code != 400 {
		t.Fatal("old account generation reset link accepted")
	}
	c.request("POST", "/api/forgot-password", Record{"email": "admin@example.net"})
	raw = outboxToken(t, c.a, "reset-password")
	c.a.store.Update(func(s *State) error {
		s.Recovery[len(s.Recovery)-1].ExpiresAt = time.Now().Add(-time.Second)
		return nil
	})
	if w := anonymous.request("POST", "/api/reset-password", Record{"token": raw, "newPassword": "new-reset-password"}); w.Code != 400 {
		t.Fatal("expired reset accepted")
	}
}
func TestNewInvitationRoleScopeAndIssuerRecheck(t *testing.T) {
	c, _ := setupClient(t)
	enableTestMail(c.a)
	member := c.create("members", Record{"name": "Invited member", "asn": 64520, "status": "active"})
	created := c.ok("POST", "/api/invitations", Record{"email": "new@example.net", "memberId": member["id"], "memberAdmin": true, "currentPassword": "very-long-test-password"})
	raw := outboxToken(t, c.a, "accept-invitation")
	if strings.Contains(fmtJSON(created), raw) {
		t.Fatal("invitation API exposed token")
	}
	anonymous := &client{a: c.a, t: t}
	inspection := anonymous.ok("POST", "/api/invitations/inspect", Record{"token": raw})
	if inspection["accountExists"] != false {
		t.Fatal(inspection)
	}
	anonymous.ok("POST", "/api/invitations/accept", Record{"token": raw, "name": "New member admin", "password": "invited-account-password"})
	newUser := loginClient(t, c.a, Record{"email": "new@example.net", "password": "invited-account-password"})
	session := newUser.ok("GET", "/api/session", nil)["user"].(map[string]any)
	if session["role"] != "member" || session["memberRole"] != "member-admin" || session["memberId"] != member["id"] {
		t.Fatal("invitation granted incorrect global/member role")
	}
	if w := anonymous.request("POST", "/api/invitations/accept", Record{"token": raw, "name": "again", "password": "invited-account-password"}); w.Code != 400 {
		t.Fatal("invitation reused")
	}
	// An issuer losing its scoped administration must invalidate pending grants.
	newUser.ok("POST", "/api/invitations", Record{"email": "another@example.net", "memberId": member["id"], "currentPassword": "invited-account-password"})
	pending := outboxToken(t, c.a, "accept-invitation")
	c.a.store.Update(func(s *State) error { u := emailUser(s, "new@example.net"); u["memberAdminIds"] = []any{}; return nil })
	if w := anonymous.request("POST", "/api/invitations/accept", Record{"token": pending, "name": "no", "password": "invited-account-password"}); w.Code != 400 {
		t.Fatal("revoked issuer authority accepted")
	}
}
func fmtJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func TestExistingAccountInvitationRequiresOwnSessionReauthentication(t *testing.T) {
	c, _ := setupClient(t)
	enableTestMail(c.a)
	one := c.create("members", Record{"name": "One", "asn": 64521, "status": "active"})
	two := c.create("members", Record{"name": "Two", "asn": 64522, "status": "active"})
	c.create("users", Record{"name": "Existing", "email": "existing@example.net", "role": "member", "memberId": one["id"], "password": "existing-user-password"})
	c.ok("POST", "/api/invitations", Record{"email": "existing@example.net", "memberId": two["id"], "currentPassword": "very-long-test-password"})
	raw := outboxToken(t, c.a, "accept-invitation")
	anonymous := &client{a: c.a, t: t}
	if w := anonymous.request("POST", "/api/invitations/accept", Record{"token": raw, "name": "attack", "password": "not-allowed-password"}); w.Code != 401 {
		t.Fatal("mail token silently attached existing account")
	}
	if w := c.request("POST", "/api/invitations/accept", Record{"token": raw, "currentPassword": "very-long-test-password"}); w.Code != 403 {
		t.Fatal("wrong signed-in account accepted invitation")
	}
	existing := loginClient(t, c.a, Record{"email": "existing@example.net", "password": "existing-user-password"})
	oldCSRF := existing.csrf
	existing.csrf = "wrong"
	if w := existing.request("POST", "/api/invitations/accept", Record{"token": raw, "currentPassword": "existing-user-password"}); w.Code != 403 {
		t.Fatal("invitation acceptance ignored CSRF")
	}
	existing.csrf = oldCSRF
	if w := existing.request("POST", "/api/invitations/accept", Record{"token": raw, "currentPassword": "wrong"}); w.Code != 403 {
		t.Fatal("invitation acceptance skipped password reauth")
	}
	existing.ok("POST", "/api/invitations/accept", Record{"token": raw, "currentPassword": "existing-user-password"})
	if w := existing.request("GET", "/api/session", nil); w.Code != 401 {
		t.Fatal("membership change retained old session")
	}
	state := c.a.store.Snapshot()
	if !contains(membershipIDs(emailUser(&state, "existing@example.net")), str(two, "id")) {
		t.Fatal("membership not attached after consent")
	}
}
func TestRecoveryRejectsCrossOrigin(t *testing.T) {
	c, _ := setupClient(t)
	r := httptest.NewRequest("POST", "/api/forgot-password", strings.NewReader(`{"email":"admin@example.net"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	c.a.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin recovery accepted")
	}
}

func TestConcurrentMailResetTokenSingleUse(t *testing.T) {
	c, _ := setupClient(t)
	enableTestMail(c.a)
	c.request("POST", "/api/forgot-password", Record{"email": "admin@example.net"})
	raw := outboxToken(t, c.a, "reset-password")
	statuses := make(chan int, 2)
	var wg sync.WaitGroup
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			anonymous := &client{a: c.a, t: t}
			w := anonymous.request("POST", "/api/reset-password", Record{"token": raw, "newPassword": "concurrent-reset-password"})
			statuses <- w.Code
		}()
	}
	wg.Wait()
	close(statuses)
	successes := 0
	for status := range statuses {
		if status == 200 {
			successes++
		} else if status != 400 {
			t.Fatalf("unexpected reset status %d", status)
		}
	}
	if successes != 1 {
		t.Fatal("recovery token did not enforce single-use atomically")
	}
}
func TestRevokedInvitationAndChangedResetAreNotDelivered(t *testing.T) {
	c, _ := setupClient(t)
	sender := enableTestMail(c.a)
	member := c.create("members", Record{"name": "Member", "asn": 64523, "status": "active"})
	invite := c.ok("POST", "/api/invitations", Record{"email": "invite@example.net", "memberId": member["id"], "currentPassword": "very-long-test-password"})
	c.ok("DELETE", "/api/invitations/"+str(invite, "id"), nil)
	c.request("POST", "/api/forgot-password", Record{"email": "admin@example.net"})
	id := accountID(c.a)
	c.a.store.Update(func(s *State) error { invalidateSecurity(s, id); return nil })
	if err := c.a.deliverOne(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.messages) != 0 {
		t.Fatal("obsolete security link delivered")
	}
	for _, entry := range c.a.store.Snapshot().Outbox.Entries {
		if entry.Status != "expired" {
			t.Fatal("obsolete entry was not invalidated")
		}
	}
}
