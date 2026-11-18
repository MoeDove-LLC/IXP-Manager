package app

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/notify"
)

func queueWelcomeMailFixture(t *testing.T, c *client) (Record, notify.Entry, MemberWelcomeReceipt) {
	t.Helper()
	member := c.create("members", Record{"name": "Welcome fixture", "asn": 64530, "status": "pending", "email": "welcome-to@example.test", "nocEmail": "welcome-cc@example.test"})
	c.create("contacts", Record{"name": "Private BCC contact", "email": "welcome-hidden-bcc@example.test", "memberId": member["id"], "role": "technical"})
	path := "/api/members/" + str(member, "id") + "/welcome-email"
	compose := c.ok(http.MethodGet, path, nil)
	draft := Record(compose["draft"].(map[string]any))
	draft["to"] = []string{"welcome-to@example.test"}
	draft["cc"] = []string{"welcome-cc@example.test"}
	draft["bcc"] = []string{"welcome-hidden-bcc@example.test"}
	draft["text"] = "Welcome captured confidential example contents"
	review := c.ok(http.MethodPost, path+"/review", draft)
	queue := Record(review["draft"].(map[string]any))
	queue["reviewToken"], queue["expiresAt"], queue["acknowledge"] = review["reviewToken"], review["expiresAt"], true
	result := c.ok(http.MethodPost, path+"/queue", queue)
	state := c.a.store.Snapshot()
	if len(state.WelcomeMail) != 1 || len(state.Outbox.Entries) != 1 || str(result, "outboxId") != state.Outbox.Entries[0].ID {
		t.Fatal("welcome review did not persist exactly one bound message")
	}
	return member, state.Outbox.Entries[0], state.WelcomeMail[0]
}

func TestMemberWelcomeMailDeliveredAndPrunedWithEncryptedReceipt(t *testing.T) {
	c, dir := setupClient(t)
	sender := enableTestMail(c.a)
	member, entry, receipt := queueWelcomeMailFixture(t, c)
	state := c.a.store.Snapshot()
	if err := validateMemberWelcomeMail(&state, c.a.store.securityKey); err != nil {
		t.Fatal(err)
	}
	if receipt.Member.ID != str(member, "id") || receipt.RecipientCount != 3 || receipt.BCCCount != 1 || receipt.OutboxID != entry.ID {
		t.Fatal("durable receipt omitted its scope or counts")
	}
	message, err := notify.OpenMessage(c.a.store.securityKey, entry)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(Record{"receipt": receipt, "outbox": entry})
	if err != nil || bytes.Contains(metadata, []byte(message.Text)) || bytes.Contains(metadata, []byte(message.Cc[0])) || bytes.Contains(metadata, []byte(message.Bcc[0])) {
		t.Fatal("plain message details escaped encrypted storage")
	}
	if err := c.a.deliverOne(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(sender.messages) != 1 || !reflect.DeepEqual(sender.messages[0], message) || c.a.store.Snapshot().Outbox.Entries[0].Status != "sent" {
		t.Fatal("welcome SMTP handoff changed the reviewed message or status")
	}
	if err := c.a.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if got := reopened.Snapshot(); len(got.WelcomeMail) != 1 || !reflect.DeepEqual(got.WelcomeMail[0], receipt) || got.Outbox.Entries[0].Status != "sent" {
		t.Fatal("restart lost the durable original receipt")
	}
	if err := reopened.Update(func(s *State) error {
		s.Outbox.Prune(entry.CreatedAt.Add(31 * 24 * time.Hour))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := reopened.Snapshot(); len(got.WelcomeMail) != 0 || len(got.Outbox.Entries) != 0 {
		t.Fatal("receipt did not follow outbox retention")
	}
}

func TestMemberWelcomeMailRechecksLeaseActorAndCurrentScope(t *testing.T) {
	c, _ := setupClient(t)
	sender := enableTestMail(c.a)
	member, entry, receipt := queueWelcomeMailFixture(t, c)
	now := time.Now().UTC()
	state := c.a.store.Snapshot()
	claimed, ok := state.Outbox.Claim(now)
	if !ok {
		t.Fatal("welcome message did not obtain a lease")
	}
	message, err := notify.OpenMessage(c.a.store.securityKey, claimed)
	if err != nil || !c.a.memberWelcomeTransportApplicable(&state, claimed, message, now) {
		t.Fatal("current scope and lease were rejected", err)
	}
	for name, change := range map[string]func(*State){
		"member suspended":      func(s *State) { find(s, "members", receipt.Member.ID)["status"] = "suspended" },
		"member recreated":      func(s *State) { find(s, "members", receipt.Member.ID)["createdAt"] = "2020-01-01T00:00:00Z" },
		"admin role lost":       func(s *State) { find(s, "users", receipt.Actor.ID)["role"] = "viewer" },
		"admin recreated":       func(s *State) { find(s, "users", receipt.Actor.ID)["createdAt"] = "2020-01-01T00:00:00Z" },
		"admin security change": func(s *State) { invalidateSecurity(s, receipt.Actor.ID) },
		"reviewed data changed": func(s *State) { find(s, "members", receipt.Member.ID)["nocEmail"] = "changed@example.test" },
		"lease replaced":        func(s *State) { s.Outbox.Entries[0].Lease = "other-lease" },
		"message row changed":   func(s *State) { s.Outbox.Entries[0].Subject = "Different subject" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := cloneState(state)
			change(&changed)
			if c.a.memberWelcomeTransportApplicable(&changed, claimed, message, now) {
				t.Fatal("stale claimed message remained transportable")
			}
		})
	}
	message.From = "different@example.test"
	if c.a.memberWelcomeTransportApplicable(&state, claimed, message, now) {
		t.Fatal("captured sender bypassed configured sender")
	}
	if err = c.a.store.Update(func(s *State) error {
		find(s, "members", str(member, "id"))["nocEmail"] = "changed@example.test"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = c.a.deliverOne(context.Background(), time.Now()); err != nil || len(sender.messages) != 0 || c.a.store.Snapshot().Outbox.Entries[0].Status != "expired" {
		t.Fatal("stale pending preview reached SMTP instead of expiry", err)
	}
	if err = c.a.store.Update(func(s *State) error {
		s.Outbox.Entries[0].Status = "failed"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if reply := c.request(http.MethodPost, "/api/notifications/"+entry.ID+"/retry", nil); reply.Code != http.StatusConflict ||
		strings.Contains(reply.Body.String(), message.Bcc[0]) || c.a.store.Snapshot().Outbox.Entries[0].Status != "failed" {
		t.Fatalf("stale failed welcome mail was requeued or leaked detail: HTTP %d", reply.Code)
	}
}

func TestMemberWelcomeMailRestoreRejectsMismatchedReceiptOrCipher(t *testing.T) {
	c, dir := setupClient(t)
	enableTestMail(c.a)
	_, _, _ = queueWelcomeMailFixture(t, c)
	state := c.a.store.Snapshot()
	for name, change := range map[string]func(*State){
		"missing receipt":        func(s *State) { s.WelcomeMail = nil },
		"duplicate receipt":      func(s *State) { s.WelcomeMail = append(s.WelcomeMail, s.WelcomeMail[0]) },
		"recipient mismatch":     func(s *State) { s.WelcomeMail[0].RecipientCount-- },
		"wrong nonce":            func(s *State) { s.WelcomeMail[0].ID = "some-other-nonce" },
		"cipher mismatch":        func(s *State) { s.Outbox.Entries[0].Sealed = "corrupted" },
		"duplicate outbox id":    func(s *State) { s.Outbox.Entries = append(s.Outbox.Entries, s.Outbox.Entries[0]) },
		"outbox expiry extended": func(s *State) { s.Outbox.Entries[0].ExpiresAt = s.Outbox.Entries[0].ExpiresAt.Add(time.Minute) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := cloneState(state)
			change(&changed)
			if err := validateMemberWelcomeMail(&changed, c.a.store.securityKey); err == nil {
				t.Fatal("corrupted receipt or ciphertext accepted")
			}
		})
	}
	if err := c.a.Close(); err != nil {
		t.Fatal(err)
	}
	state.WelcomeMail[0].BCCCount = 0
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if unexpected, err := OpenStore(dir); err == nil {
		unexpected.Close()
		t.Fatal("OpenStore did not enforce decrypted welcome receipt counts")
	}
}
