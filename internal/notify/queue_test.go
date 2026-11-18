package notify

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

func testMessage() Message {
	return Message{From: "ixp@example.test", To: "member@example.test", Subject: "密码重置", Text: "reset token: very-private-single-use-token"}
}
func TestOutboxImmutableEncryptedIdempotentSnapshot(t *testing.T) {
	var q Queue
	key := bytes.Repeat([]byte{7}, 32)
	now := time.Now()
	msg := testMessage()
	first, err := q.Enqueue("stable-key", msg, key, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	msg.To = "different@example.test"
	msg.Text = "changed"
	second, err := q.Enqueue("stable-key", msg, key, now, now.Add(time.Hour))
	if err != nil || first.ID != second.ID || len(q.Entries) != 1 {
		t.Fatal("idempotency did not preserve original snapshot")
	}
	opened, err := OpenMessage(key, second)
	if err != nil || opened.To != "member@example.test" || opened.Text != testMessage().Text {
		t.Fatal("snapshot mutated")
	}
	raw, _ := json.Marshal(q)
	if bytes.Contains(raw, []byte("very-private-single-use-token")) {
		t.Fatal("plaintext recovery token persisted")
	}
	second.ID = "other"
	if _, err = OpenMessage(key, second); err == nil {
		t.Fatal("ciphertext can be moved between records")
	}
}
func TestLeaseCrashRecoveryAndBoundedRetry(t *testing.T) {
	var q Queue
	now := time.Now()
	_, err := q.Enqueue("id", testMessage(), bytes.Repeat([]byte{1}, 32), now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	first, ok := q.Claim(now)
	if !ok {
		t.Fatal("not claimed")
	}
	if _, ok = q.Claim(now.Add(time.Second)); ok {
		t.Fatal("live lease claimed twice")
	}
	second, ok := q.Claim(now.Add(LeaseDuration + time.Second))
	if !ok || second.Lease == first.Lease {
		t.Fatal("crashed lease not recovered")
	}
	if err = q.Finish(first.ID, first.Lease, true, now); err == nil {
		t.Fatal("old worker completed newer lease")
	}
	if err = q.Finish(second.ID, second.Lease, false, now); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < MaxAttempts+2; n++ {
		now = now.Add(10 * time.Minute)
		entry, ok := q.Claim(now)
		if !ok {
			break
		}
		if err = q.Finish(entry.ID, entry.Lease, false, now); err != nil {
			t.Fatal(err)
		}
	}
	if q.Entries[0].Status != "failed" || q.Entries[0].Attempts != MaxAttempts {
		t.Fatal(q.Entries[0])
	}
	if q.Ready(now) {
		t.Fatal("exhausted entry still retried automatically")
	}
}
func TestExpiredSecretMailNeverDeliveredOrRetried(t *testing.T) {
	var q Queue
	now := time.Now()
	e, err := q.Enqueue("expiry", testMessage(), bytes.Repeat([]byte{2}, 32), now, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := q.Claim(now.Add(2 * time.Minute)); ok {
		t.Fatal("expired token mail delivered")
	}
	if err = q.Retry(e.ID, now.Add(2*time.Minute)); err == nil {
		t.Fatal("expired token mail manually retried")
	}
	if q.Entries[0].Status != "expired" {
		t.Fatal("expired status missing")
	}
}
func TestCiphertextTamperingAndHeaderInjection(t *testing.T) {
	var q Queue
	now := time.Now()
	key := bytes.Repeat([]byte{9}, 32)
	e, err := q.Enqueue("x", testMessage(), key, now, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	e.To = "attacker@example.test"
	if _, err = OpenMessage(key, e); err == nil {
		t.Fatal("recipient snapshot tampering accepted")
	}
	bad := testMessage()
	bad.Subject = "hello\r\nBcc: someone@example.test"
	if _, err = q.Enqueue("bad", bad, key, now, time.Time{}); err == nil {
		t.Fatal("header injection accepted")
	}
}
