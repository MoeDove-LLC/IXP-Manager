package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/moedove/ixp-manager/internal/notify"
)

func TestOutboxDeliveryAndSanitizedFailure(t *testing.T) {
	c, _ := setupClient(t)
	sender := enableTestMail(c.a)
	c.request("POST", "/api/forgot-password", Record{"email": "admin@example.net"})
	sender.fail = true
	if err := c.a.deliverOne(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	entry := c.a.store.Snapshot().Outbox.Entries[0]
	if entry.Status != "pending" || entry.Attempts != 1 || strings.Contains(entry.LastError, "very-private") {
		t.Fatal("unsafe outbox failure state")
	}
	sender.fail = false
	if err := c.a.deliverOne(context.Background(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	after := c.a.store.Snapshot().Outbox.Entries[0]
	if after.Status != "sent" || after.ID != entry.ID {
		t.Fatal("retry did not complete immutable entry")
	}
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.messages) != 2 || sender.messages[0].ID != sender.messages[1].ID || sender.messages[0].Text != sender.messages[1].Text {
		t.Fatal("delivery retry changed message snapshot")
	}
}
func TestFixedJobRemindersIdempotentAndCleanup(t *testing.T) {
	c, _ := setupClient(t)
	enableTestMail(c.a)
	c.ok("POST", "/api/account/tokens", Record{"name": "expires soon", "currentPassword": "very-long-test-password", "expiresInDays": 1})
	now := time.Now()
	if err := c.a.runDueJob(now); err != nil {
		t.Fatal(err)
	}
	if len(c.a.store.Snapshot().Outbox.Entries) != 1 {
		t.Fatal("token expiry reminder missing")
	}
	c.ok("POST", "/api/jobs/token-expiry/run", nil)
	if err := c.a.runDueJob(time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(c.a.store.Snapshot().Outbox.Entries) != 1 {
		t.Fatal("repeat job duplicated reminder")
	}
	if w := c.request("POST", "/api/jobs/arbitrary-shell/run", Record{"command": "anything"}); w.Code != 404 {
		t.Fatal("arbitrary job accepted")
	}
	id := accountID(c.a)
	c.a.store.Update(func(s *State) error {
		u := s.Security[id]
		u.Tokens[0].ExpiresAt = now.Add(-time.Minute)
		s.Security[id] = u
		for i := range s.Jobs {
			if s.Jobs[i].Kind == "cleanup" {
				s.Jobs[i].Status = "running"
				s.Jobs[i].LeaseUntil = now.Add(-time.Second)
				s.Jobs[i].Lease = "crashed"
			} else {
				s.Jobs[i].NextRunAt = now.Add(time.Hour)
			}
		}
		return nil
	})
	if err := c.a.runDueJob(now); err != nil {
		t.Fatal(err)
	}
	state := c.a.store.Snapshot()
	if len(state.Security[id].Tokens) != 0 {
		t.Fatal("cleanup did not expire token")
	}
	for _, job := range state.Jobs {
		if job.Kind == "cleanup" && (job.Status != "idle" || job.Lease != "") {
			t.Fatal("crashed job lease not recovered")
		}
	}
}
func TestBackgroundCloseCancelsWorkerBeforeStore(t *testing.T) {
	c, _ := setupClient(t)
	sender := enableTestMail(c.a)
	sender.started = make(chan struct{}, 1)
	sender.wait = true
	c.request("POST", "/api/forgot-password", Record{"email": "admin@example.net"})
	c.a.StartBackground(context.Background())
	select {
	case <-sender.started:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not claim email")
	}
	done := make(chan struct{})
	go func() { c.a.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not cancel delivery before closing store")
	}
}
func TestDefaultDeliveryDisabledDoesNotSend(t *testing.T) {
	c, _ := setupClient(t)
	sender := &captureSender{}
	c.a.mailer = sender
	now := time.Now()
	c.a.store.Update(func(s *State) error {
		_, err := s.Outbox.Enqueue("fixture", notify.Message{From: "ixp@example.test", To: "person@example.test", Subject: "test", Text: "test"}, c.a.store.securityKey, now, time.Time{})
		return err
	})
	if err := c.a.deliverOne(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if len(sender.messages) != 0 {
		t.Fatal("default-disabled service sent mail")
	}
}

func TestMetadataNeverEchoesPersistedTransportErrors(t *testing.T) {
	c, _ := setupClient(t)
	enableTestMail(c.a)
	c.request("POST", "/api/forgot-password", Record{"email": "admin@example.net"})
	c.a.store.Update(func(s *State) error {
		s.Outbox.Entries[0].LastError = "very-secret-provider-echo"
		s.Jobs[0].LastError = "very-secret-provider-echo"
		return nil
	})
	for _, path := range []string{"/api/notifications", "/api/jobs"} {
		w := c.request("GET", path, nil)
		if w.Code != 200 || strings.Contains(w.Body.String(), "very-secret-provider-echo") || strings.Contains(w.Body.String(), "sealed") {
			t.Fatal("metadata API exposed transport or ciphertext details")
		}
	}
}
